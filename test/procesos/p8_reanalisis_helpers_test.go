//go:build integracion

package procesos

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// El mundo y las ayudas de P8 · Re-análisis (T9.21): las empresas y credenciales del proceso, la
// llamada a `POST /api/v1/intakes/{id}/reanalyze` con reintento del 429, las marcas de Postgres
// (R9.5.c) y las lecturas de la solicitud y de sus revisiones.

const (
	// Los contactos del proceso: uno por solicitud (createDraft exige un contacto nuevo).
	p8MainPn     = "573008880001"
	p8ApprovedPn = "573008880002"
	p8RejectedPn = "573008880003"

	// p8Action es el permiso —y el `action` de audit_events— de las escrituras de la bandeja.
	p8Action = "intakes.write"
	// p8PlanNoIntake es un plan sembrado SIN la feature llm_intake (0039: `basic`).
	p8PlanNoIntake = "basic"

	// Los cuerpos de error de la puerta que no llevan clave propia.
	p8NotFound = `{"error":"solicitud no encontrada"}`
	p8BadBody  = `{"error":"cuerpo JSON inválido"}`
	p8NoIntake = `{"error":"feature_not_enabled","feature":"llm_intake"}`
	p8Failed   = `{"error":"no se pudo pedir el re-análisis de la solicitud"}`

	// p8MsgOpened es la línea INFO con la que el caso de uso dice que abrió el job, y p8MsgAttached
	// la del `draft` cuando cuelga la revisión de una solicitud que ya existía.
	p8MsgOpened   = "reanalisis: job abierto a petición del dueño"
	p8MsgAttached = "draft: el evento YA tenía contenido durable; la revisión se cuelga de él y su estado no se toca"

	// p8ThreadTurn es un turno cliente/negocio del hilo tal como lo deja la ráfaga, y p8ThreadPasted
	// la fila que añade una transcripción pegada por la dueña.
	p8ThreadTurn   = "client:message:whatsapp:true,business:message:whatsapp:true"
	p8ThreadPasted = "client:message:owner_pasted:true"
)

// p8Tables son las tablas que una petición de re-análisis puede tocar, directa o indirectamente: la
// cola, la solicitud y sus líneas, las revisiones, la telemetría, el evento y su hilo, los avisos de
// degradación y el outbox del puente CRM. p8Snapshot las vigila enteras: todas las columnas de todas
// las filas (R9.5.c).
var p8Tables = []string{
	"intake_jobs", "intakes", "intake_items", "intake_revisions", "flow_events",
	"conversation_events", "conversation_event_messages", "owner_degradation_notices", "webhook_outbox",
}

// p8Static son las tablas que un re-análisis ACEPTADO y sin texto pegado no puede tocar.
var p8Static = []string{"intakes", "intake_items", "conversation_events", "webhook_outbox"}

// p8Target es una solicitud del proceso: lo que createDraft dejó y por dónde va.
type p8Target struct {
	pn        string // el teléfono del contacto
	id        string // intakes.id
	eventID   string
	contactID string // el opaco
	firstJob  string // el job del pipeline normal, del que se hereda message_ts
	rev       int    // la última revisión escrita
	pasted    int    // filas owner_pasted del hilo
}

// p8World es lo que comparten los subtests: la escena de P4 (servidor, empresa, Edge con guion) y
// las credenciales de quien NO debe poder re-analizar.
type p8World struct {
	sc    *draftScene
	calls *p9World // el reintento del 429 y la cuenta de auditoría (p9World.call)

	owner    p9Caller     // la administradora de la empresa del proceso
	outsider p9Caller     // la administradora de OTRA empresa con el mismo plan
	noIntake p9Caller     // la administradora de una empresa SIN llm_intake
	viewer   *clienteHTTP // un miembro de la empresa del proceso sin intakes.write

	main *p8Target
}

// p8NewWorld arranca la escena y da de alta las otras dos empresas y el viewer. Hay que llamarla con
// el `t` del proceso (el Edge se ata a él).
func p8NewWorld(t *testing.T) *p8World {
	t.Helper()
	sc := draftScenario(t, "p8", "p8-reanalisis")
	w := &p8World{
		sc:    sc,
		calls: &p9World{root: t, audit: map[string]int{}},
		owner: p9Caller{client: sc.Pub, tenant: sc.Tenant},
	}
	w.outsider = w.newAdmin(t, "p8-otra-empresa", draftPlan)
	w.noIntake = w.newAdmin(t, "p8-sin-captacion", p8PlanNoIntake)
	viewer := uuidAleatorio(t)
	p2AddMember(t, sc.DB, viewer, sc.Tenant, p2RoleViewer)
	w.viewer = sc.S.Publica(canjear(t, sc.S, sc.S.Identidad.TokenDe(viewer, "wapp.bff")))
	return w
}

// newAdmin crea una empresa con el plan dado y devuelve el cliente de su administradora.
func (w *p8World) newAdmin(t *testing.T, slug, plan string) p9Caller {
	t.Helper()
	tenant := crearTenantConPlan(t, w.sc.S, w.sc.TokenStaff, slug, plan)
	admin := uuidAleatorio(t)
	edgeAltaAdminDelTenant(t, w.sc.DB, admin, tenant)
	return p9Caller{client: w.sc.S.Publica(canjear(t, w.sc.S, w.sc.S.Identidad.TokenDe(admin, "wapp.bff"))), tenant: tenant}
}

// newTarget crea con createDraft (el helper de P4) el borrador del contacto dado, con el guion del
// caso Ámbar repuesto, y devuelve la solicitud con lo que hace falta para seguirla. Falla
// (t.Fatalf) si el borrador no queda en `pending_approval` con su revisión 1.
func (w *p8World) newTarget(t *testing.T, pn string) *p8Target {
	t.Helper()
	w.sc.Script.UseAmbar()
	tgt := &p8Target{pn: pn, id: createDraft(t, w.sc, pn), rev: 1}
	const query = `SELECT i.event_id::text, i.contact_id, j.id::text, i.status
			|| '|' || (SELECT count(*) FROM public.intake_revisions r WHERE r.intake_id = i.id)::text
			|| '|' || (SELECT count(*) FROM public.intake_jobs x WHERE x.event_id = i.event_id)::text
		FROM public.intakes i JOIN public.intake_jobs j ON j.intake_id = i.id WHERE i.id = $1::uuid AND i.tenant_id = $2`
	var state string
	if err := w.sc.DB.QueryRowContext(t.Context(), query, tgt.id, w.sc.Tenant).Scan(&tgt.eventID, &tgt.contactID, &tgt.firstJob, &state); err != nil {
		t.Fatalf("leer el borrador de %s: %v", pn, err)
	}
	if state != "pending_approval|1|1" {
		t.Fatalf("el borrador de %s = %q, quería pending_approval con una revisión y un job", pn, state)
	}
	return tgt
}

// p8Path es la ruta del re-análisis de una solicitud; el id va URL-escapado, para que los
// adversarios (espacios Unicode, dígitos no ASCII) lleguen al servidor tal cual.
func p8Path(id string) string {
	return "/api/v1/intakes/" + url.PathEscape(id) + "/reanalyze"
}

// post pide el re-análisis de id como c, con el reintento del 429 de P9, y apunta su fila de
// auditoría: toda petición que llega al handler audita (`failure` desde 400).
func (w *p8World) post(t *testing.T, c p9Caller, id string, body any) respuesta {
	t.Helper()
	return w.calls.call(t, c, p8Action, http.MethodPost, p8Path(id), body)
}

// write hace otra escritura auditada de la bandeja (líneas, aprobar, estado, descarte) como la
// administradora del proceso.
func (w *p8World) write(t *testing.T, method, path string, body any) respuesta {
	t.Helper()
	return w.calls.call(t, w.owner, p8Action, method, path, body)
}

// postRaw pide el re-análisis de id como la administradora con un cuerpo CRUDO (bytes que no tienen
// por qué ser JSON), reintentando el 429 igual que p9World.call, y apunta su fila de auditoría.
func (w *p8World) postRaw(t *testing.T, id string, raw []byte) respuesta {
	t.Helper()
	var r respuesta
	edgeEsperar(t, edgeTopeFila, "POST "+p8Path(id)+" sin 429", func() bool {
		var err error
		if r, err = w.owner.client.despachar(t.Context(), http.MethodPost, p8Path(id), raw); err != nil {
			t.Fatalf("POST con cuerpo crudo: %v", err)
		}
		if r.Codigo == http.StatusTooManyRequests {
			w.calls.throttled++
			return false
		}
		return true
	})
	result := "success"
	if r.Codigo >= http.StatusBadRequest {
		result = "failure"
	}
	w.calls.audit[w.owner.tenant+"|"+p8Action+"|"+result]++
	return r
}

// p8Ack es el 200 del re-análisis: el acuse de que el job está abierto.
type p8Ack struct {
	IntakeID   string `json:"intake_id"`
	RevisionNo int    `json:"revision_no"`
	JobID      string `json:"job_id"`
	Via        string `json:"via"`
	Status     string `json:"status"`
}

// p8Is dice si la respuesta trae el código dado y EXACTAMENTE ese cuerpo JSON (sin el salto final).
func p8Is(r respuesta, code int, body string) bool {
	return r.Codigo == code && strings.TrimSpace(string(r.Cuerpo)) == body
}

// p8Snapshot devuelve, por tabla, una huella de TODAS las columnas de TODAS sus filas (nº de filas
// y md5 de los md5 de cada fila en JSON, ordenados). Dos huellas iguales = ni una fila ni una columna
// cambió. La base es del proceso, así que no hace falta acotar por empresa.
func p8Snapshot(t *testing.T, sc *draftScene, tables []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(tables))
	for _, table := range tables {
		out[table] = p9Scalar(t, sc.DB, `SELECT count(*)::text || ':' || md5(coalesce(string_agg(h, '' ORDER BY h), ''))
			FROM (SELECT md5(row_to_json(x)::text) AS h FROM public.`+pgx.Identifier{table}.Sanitize()+` x) y`)
	}
	return out
}

// p8Unchanged exige que las tablas de before sigan con la misma huella. when dice tras qué.
func p8Unchanged(t *testing.T, sc *draftScene, before map[string]string, when string) {
	t.Helper()
	tables := make([]string, 0, len(before))
	for table := range before {
		tables = append(tables, table)
	}
	for table, got := range p8Snapshot(t, sc, tables) {
		if got != before[table] {
			t.Errorf("%s: %s cambió (filas:huella %s → %s)", when, table, before[table], got)
		}
	}
}

// p8WaitJob espera, con tope draftTimeout, a que el job valga want en «estado/etapa/intentos».
// Falla (t.Fatalf) con lo último que vio.
func p8WaitJob(t *testing.T, sc *draftScene, jobID, want string) {
	t.Helper()
	last := ""
	if !draftPoll(t, draftTimeout, func() bool {
		last = p9Scalar(t, sc.DB, `SELECT status || '/' || coalesce(stage, '-') || '/' || attempts::text
			FROM public.intake_jobs WHERE id = $1::uuid`, jobID)
		return last == want
	}) {
		t.Fatalf("el job %s no llegó a %q en %s: quedó en %q", jobID, want, draftTimeout, last)
	}
}

// p8ReplyP4 es la respuesta de P4 del caso Ámbar con OTRA cantidad de paquetes de tequeños: es lo
// que hace distinguible cada revisión de la anterior (la cantidad de la tercera línea).
func p8ReplyP4(qty int) string {
	from := `"qty":1,"evidence":"` + scriptEvidenceTequenos
	to := fmt.Sprintf(`"qty":%d,"evidence":"`, qty) + scriptEvidenceTequenos
	return strings.Replace(scriptReplyP4, from, to, 1)
}

// p8Revision es una revisión tal como la devuelve GET /api/v1/intakes/{id}.
type p8Revision struct {
	RevisionNo int    `json:"revision_no"`
	Kind       string `json:"kind"`
	CreatedBy  string `json:"created_by"`
	Payload    struct {
		Lines      []p4Line `json:"lines"`
		SourceText string   `json:"source_text"`
		Analysis   struct {
			Source         string `json:"source"`
			Provider       string `json:"provider"`
			ReanalyzedFrom *int   `json:"reanalyzed_from"`
		} `json:"analysis"`
	} `json:"payload"`
}

// detail lee GET /api/v1/intakes/{id} como la administradora y devuelve el estado y las
// revisiones. Falla (t.Fatalf) si no es un 200 de esa solicitud.
func (w *p8World) detail(t *testing.T, id string) (status string, revisions []p8Revision) {
	t.Helper()
	r := w.calls.call(t, w.owner, "", http.MethodGet, "/api/v1/intakes/"+id, nil)
	var detail struct {
		ID, Status string
		Revisions  []p8Revision
	}
	if r.Codigo != http.StatusOK {
		t.Fatalf("GET /api/v1/intakes/%s: HTTP %d\ncuerpo: %s", id, r.Codigo, recortar(r.Cuerpo))
	}
	if err := json.Unmarshal(r.Cuerpo, &detail); err != nil || detail.ID != id {
		t.Fatalf("GET /api/v1/intakes/%s: %v\ncuerpo: %s", id, err, recortar(r.Cuerpo))
	}
	return detail.Status, detail.Revisions
}

// p8Thread resume el hilo del evento: «rol:clase:origen:cifrado» por fila, en orden. `cifrado` es
// que la fila lleva cuerpo cifrado y ningún payload en claro.
func p8Thread(t *testing.T, sc *draftScene, eventID string) string {
	t.Helper()
	return p9Scalar(t, sc.DB, `SELECT coalesce(string_agg(role || ':' || entry_kind || ':' || origin || ':'
			|| (body_enc IS NOT NULL AND payload IS NULL)::text, ',' ORDER BY seq), '')
		FROM public.conversation_event_messages WHERE event_id = $1::uuid`, eventID)
}

// wantThread es el hilo esperado de la solicitud: los tres turnos de la ráfaga y, detrás, una fila
// por transcripción pegada.
func (tgt *p8Target) wantThread() string {
	return p8ThreadTurn + "," + p8ThreadTurn + "," + p8ThreadTurn + strings.Repeat(","+p8ThreadPasted, tgt.pasted)
}
