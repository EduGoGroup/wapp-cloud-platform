//go:build integracion

package procesos

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// P4 · De mensaje a borrador (diseno.md §4 · T9.17): la ráfaga de un cliente por WhatsApp entra por
// el Edge, se junta en UNA ventana de captación, y el pipeline LLM —P2, P3 y P4 servidas por el Edge,
// más el cruce con el catálogo— la convierte en una solicitud en `pending_approval` con su revisión 1.
// El proceso está partido por tema: este fichero trae el recorrido feliz y el cierre; los casos
// adversarios van en p4_borrador_adversarial_test.go y la degradación en
// p4_borrador_degradation_test.go. El escenario y createDraft, que reutilizan P5–P8, viven en
// p4_borrador_helpers_test.go, y el «modelo» en guion_test.go.

const (
	// p4MainPn es el contacto del recorrido feliz.
	p4MainPn = "573004440001"
	// p4Needle es una aguja irrepetible que el cliente escribe en su primer mensaje, fuera de toda
	// `evidence`: lo que se busca después en TODAS las tablas y en el log.
	p4Needle = "ZZP4AGUJA7Q"

	// p4DraftFlow es el flow_id con el que el borrador publica su fila de telemetría.
	p4DraftFlow = "_intake_llm"
	// p4ShippingSKU es el SKU de la línea de envío, la última de todo borrador.
	p4ShippingSKU = "_shipping"

	// Los mensajes WARN del pipeline que este proceso espera (y cuenta).
	p4MsgModelDate   = "p4: la fecha que propuso el modelo no coincide con la que calculó Go; manda la de Go (D-044.9)"
	p4MsgIdeaDropped = "p2: la evidencia de una idea no aparece en el literal del cliente; la idea se descarta"
	p4MsgHintDropped = "p2: la evidencia de la pista de entrega no aparece en el literal del cliente; la pista se descarta"
	p4MsgExtraItems  = "p4: el modelo devolvió más ítems de los que P3 dejó vivos; los sobrantes se descartan"
	p4MsgPackageFix  = "p4: el modelo confundió el TAMAÑO del paquete con la cantidad; se corrige a un paquete (design §7.3)"
	p4MsgEvidenceP3  = "p4: la evidencia que devolvió el modelo no aparece en el literal del cliente; se conserva la de P3"
)

// p4Run es lo que una ráfaga deja para afirmar: sus wa_message_id, su job, su solicitud, su evento y
// el contact_id opaco del cliente.
type p4Run struct {
	contact   string
	ids       []string
	jobID     string
	intakeID  string
	eventID   string
	contactID string
}

// TestP4_MessageToDraft es el proceso P4. Los subtests comparten UN servidor y corren EN ORDEN: cada
// uno parte de lo que dejó el anterior. Se afirma lo que hace el binario viejo; el mismo test corre
// contra el nuevo sin distinguirlos.
//
//   - escenario: lo que draftScenario deja (plan, catálogo, flujo, disparo, Edge READY sin inferencias).
//   - borrador: la ráfaga de 3 entrantes → una ventana → barrido → P2, 3×P3, P4 → match → borrador, con
//     lo que queda en intake_jobs, intakes, intake_items, intake_revisions y flow_events, lo que
//     devuelven GET /api/v1/intakes y GET /api/v1/intakes/{id}, y el literal del cliente fuera de toda
//     columna en claro y del log.
//   - adversarios: un modelo que inventa evidencias y cantidades, y textos y SKUs con separadores
//     repetidos, espacios Unicode y dígitos no ASCII.
//   - degradación: P2 falla con OLLAMA_DOWN → aviso al dueño, contador, job en cola con backoff; el
//     flujo estático sigue respondiendo; al volver el Edge a READY el job se reanuda sin esperar.
//   - vía LLM: GET, PUT y DELETE de /api/v1/tenant-llm por el cable (p4_borrador_tenantllm_test.go): el
//     gate del plan, las guardias, los cuerpos inválidos y el recorrido sobre una segunda empresa.
//   - otro borrador: createDraft, el helper que reutilizan P5–P8, sobre la misma empresa.
//   - puerta: qué primer mensaje abre ventana y cuál no.
func TestP4_MessageToDraft(t *testing.T) {
	t.Parallel()
	sc := draftScenario(t, "p4", "p4-borrador")

	t.Run("escenario", func(t *testing.T) { p4Scenario(t, sc) })
	var main p4Run
	t.Run("borrador", func(t *testing.T) { main = p4HappyPath(t, sc) })
	t.Run("adversarios", func(t *testing.T) { p4Adversarial(t, sc) })
	t.Run("degradacion", func(t *testing.T) { p4Degradation(t, sc) })
	t.Run("via_llm", func(t *testing.T) { p4TenantLLM(t, sc) })
	t.Run("otro_borrador", func(t *testing.T) { p4AnotherDraft(t, sc, main) })
	t.Run("puerta", func(t *testing.T) { p4Door(t, sc) })
	t.Run("cierre", func(t *testing.T) { p4Closing(t, sc, main) })
}

// p4Scenario afirma lo que el escenario dejó ANTES de la primera ráfaga: el plan de la empresa con
// `llm_intake` y `cart_basic` y sin `api_llm`; el catálogo en tenant_content bajo la ref de la que lee
// el pipeline, con sus SKUs adversarios tal cual; el disparo `event_start`; ningún job, ninguna
// solicitud y ninguna inferencia pedida al Edge.
func p4Scenario(t *testing.T, sc *draftScene) {
	const features = `SELECT coalesce(string_agg(pf.feature, ',' ORDER BY pf.feature), '')
		FROM public.tenants t JOIN public.plan_features pf ON pf.plan_id = t.plan_id
		WHERE t.id = $1::uuid AND pf.feature IN ('llm_intake', 'cart_basic', 'api_llm', 'catalog_import')`
	if got := p9Scalar(t, sc.DB, features, sc.Tenant); got != "cart_basic,catalog_import,llm_intake" {
		t.Errorf("las features del plan %s = %q, quería cart_basic, catalog_import y llm_intake, sin api_llm", draftPlan, got)
	}
	if got := p9Scalar(t, sc.DB, `SELECT plan_id FROM public.tenants WHERE id = $1::uuid`, sc.Tenant); got != draftPlan {
		t.Errorf("el plan de la empresa = %q, quería %q", got, draftPlan)
	}
	const skus = `SELECT coalesce(string_agg(i->>'sku', ',' ORDER BY i->>'sku'), '')
		FROM public.tenant_content c, jsonb_array_elements(c.content->'categories') cat, jsonb_array_elements(cat->'items') i
		WHERE c.tenant_id = $1 AND c.ref = $2`
	if got := p9Scalar(t, sc.DB, skus, sc.Tenant, draftCatalogRef); got != "PAN-١٢٣,PAN@@1,PAN\u00a02,TEQ-30,TORTA-CHOC,TORTA-VAIN" {
		t.Errorf("los SKUs del catálogo en tenant_content = %q", got)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.flow_triggers
		WHERE tenant_id = $1::uuid AND kind = 'event_start' AND keyword = $2 AND match_type = 'contains' AND event_kind = $3 AND flow_id = $4 AND enabled`,
		sc.Tenant, draftKeyword, draftEventKind, draftFlowID); n != 1 {
		t.Errorf("flow_triggers tiene %d disparos event_start del escenario, quería 1", n)
	}
	for _, table := range []string{"intake_jobs", "intakes", "intake_revisions", "owner_degradation_notices", "conversation_events"} {
		if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.`+pgx.Identifier{table}.Sanitize()); n != 0 {
			t.Errorf("%s tiene %d filas antes de la primera ráfaga", table, n)
		}
	}
	if calls := sc.Script.Calls(""); len(calls) != 0 {
		t.Errorf("el Edge ya recibió %d inferencias antes de la primera ráfaga", len(calls))
	}
}

// p4HappyPath es el recorrido de diseno.md §4, pasos 2 y 3, con el guion del caso Ámbar.
func p4HappyPath(t *testing.T, sc *draftScene) p4Run {
	texts := draftBurst()
	texts[0] += " (ref " + p4Needle + ")"
	run := p4Run{contact: p4MainPn}

	start := time.Now()
	run.ids = sendDraftBurst(t, sc, run.contact, texts)
	p4OpenWindow(t, sc, &run, start)

	flushDraftWindow(t, sc)
	run.jobID, run.intakeID = waitDraft(t, sc, run.contact)
	p4FinishedJob(t, sc, run)
	p4ModelCalls(t, sc, run, texts)
	p4DraftRows(t, sc, run)
	p4DraftAPI(t, sc, run, texts)
	p4NoLiteralInClear(t, sc, run)
	sc.expectNoPendingText(t, "con el borrador ya creado")
	return run
}

// p4OpenWindow afirma la ventana ABIERTA: los tres mensajes en UNA fila `aggregating`, con sus
// referencias opacas en orden, la hora del cliente, y el sobre del literal todavía vacío (se compone
// al cerrar); cuelga de un evento `cart` abierto del contacto, cuyo hilo guarda los seis turnos
// cifrados. El contact_id es opaco: no es el teléfono. Rellena run.eventID y run.contactID.
func p4OpenWindow(t *testing.T, sc *draftScene, run *p4Run, start time.Time) {
	t.Helper()
	const job = `SELECT event_id::text, contact_id,
		(SELECT string_agg(r, ',' ORDER BY n) FROM jsonb_array_elements_text(j.source_refs) WITH ORDINALITY x(r, n)),
		status || '|' || coalesce(stage, '-') || '|' || attempts::text || '|' || intake_type || '|' || session_id,
		source_text_enc IS NULL AND source_text_dek IS NULL AND source_text_kek_id IS NULL AND intake_id IS NULL AND error IS NULL,
		message_ts
		FROM public.intake_jobs j WHERE tenant_id = $1 AND source_refs ? $2`
	var refs, state string
	var empty bool
	var messageTS time.Time
	if err := sc.DB.QueryRowContext(t.Context(), job, sc.Tenant, run.ids[0]).Scan(&run.eventID, &run.contactID, &refs, &state, &empty, &messageTS); err != nil {
		t.Fatalf("leer la ventana abierta: %v", err)
	}
	if refs != strings.Join(run.ids, ",") {
		t.Errorf("source_refs = %q, quería los tres wa_message_id en orden (%s)", refs, strings.Join(run.ids, ","))
	}
	if want := "aggregating|-|0|order|" + sc.Edge.SessionID; state != want {
		t.Errorf("la ventana abierta = %q, quería %q", state, want)
	}
	if !empty {
		t.Errorf("la ventana abierta ya trae sobre, solicitud o error: el literal se compone al CERRAR")
	}
	if messageTS.Before(start.Add(-2*time.Second)) || messageTS.After(time.Now().Add(2*time.Second)) {
		t.Errorf("message_ts = %s, quería la hora del mensaje del cliente (entre %s y ahora)", messageTS, start)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intake_jobs WHERE tenant_id = $1`, sc.Tenant); n != 1 {
		t.Errorf("intake_jobs tiene %d filas de la empresa, quería 1 (tres mensajes, UNA ventana)", n)
	}
	if run.contactID == "" || strings.Contains(run.contactID, run.contact) {
		t.Errorf("contact_id = %q: tiene que ser opaco, no el teléfono", run.contactID)
	}

	const event = `SELECT kind || '|' || status || '|' || flow_id || '|' || contact_id::text || '|' || session_id
		FROM public.conversation_events WHERE id = $1::uuid AND tenant_id = $2::uuid`
	if got, want := p9Scalar(t, sc.DB, event, run.eventID, sc.Tenant), draftEventKind+"|open|"+draftFlowID+"|"+run.contactID+"|"+sc.Edge.SessionID; got != want {
		t.Errorf("el evento de la ventana = %q, quería %q", got, want)
	}
	const thread = `SELECT coalesce(string_agg(role || ':' || entry_kind || ':' || origin || ':' || (body_enc IS NOT NULL AND payload IS NULL)::text, ',' ORDER BY seq), '')
		FROM public.conversation_event_messages WHERE event_id = $1::uuid`
	turn := "client:message:whatsapp:true,business:message:whatsapp:true"
	if got := p9Scalar(t, sc.DB, thread, run.eventID); got != turn+","+turn+","+turn {
		t.Errorf("el hilo del evento = %q, quería tres turnos cliente/negocio cifrados", got)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.conversation_welcomes WHERE tenant_id = $1::uuid AND contact_id = $2::uuid`, sc.Tenant, run.contactID); n != 1 {
		t.Errorf("conversation_welcomes tiene %d filas del contacto, quería 1", n)
	}
}

// p4FinishedJob afirma el job TERMINADO: `done` en la etapa `draft`, sin intentos cobrados ni error,
// con su solicitud; los cinco artefactos versionados; las referencias intactas; y —INV-13— las TRES
// piezas del sobre del literal vaciadas (las tres, no solo una).
func p4FinishedJob(t *testing.T, sc *draftScene, run p4Run) {
	t.Helper()
	const job = `SELECT status || '|' || coalesce(stage, '-') || '|' || attempts::text || '|' || coalesce(error, '-') || '|' || intake_id::text
			|| '|' || (source_text_enc IS NULL)::text || (source_text_dek IS NULL)::text || (source_text_kek_id IS NULL)::text
			|| '|' || jsonb_array_length(source_refs)::text
			|| '|' || (SELECT string_agg(k || ':' || coalesce(artifacts->k->>'version', '?'), ',' ORDER BY k) FROM jsonb_object_keys(artifacts) k)
		FROM public.intake_jobs WHERE id = $1::uuid`
	want := "done|draft|0|-|" + run.intakeID + "|truetruetrue|3|draft:1,match:1,p2:1,p3:1,p4:1"
	if got := p9Scalar(t, sc.DB, job, run.jobID); got != want {
		t.Errorf("el job terminado = %q, quería %q", got, want)
	}
	const draft = `SELECT (artifacts->'draft'->>'intake_id') || '|' || (artifacts->'draft'->>'revision_no') || '|' || (artifacts->'draft'->>'lines')
			|| '|' || ((artifacts->'draft'->>'elapsed_ms')::bigint BETWEEN 0 AND 60000)::text
			|| '|' || jsonb_array_length(artifacts->'p2'->'wants')::text || jsonb_array_length(artifacts->'p3'->'items')::text || jsonb_array_length(artifacts->'p4'->'items')::text
		FROM public.intake_jobs WHERE id = $1::uuid`
	if got, want := p9Scalar(t, sc.DB, draft, run.jobID), run.intakeID+"|1|4|true|333"; got != want {
		t.Errorf("los artefactos del job = %q, quería %q", got, want)
	}
}

// p4ModelCalls afirma lo que el Edge recibió: UNA P2, una P3 por idea y en su orden, y UNA P4; todas
// con el rótulo `lote` (que no dice la etapa: T-8) y con el techo de salida de su etapa; ninguna P1
// (sin catálogo de intenciones publicado no hay adelanto) ni P5. El literal viaja entero y rotulado
// en los cinco prompts, y ninguna referencia opaca de la ventana entra en un prompt.
func p4ModelCalls(t *testing.T, sc *draftScene, run p4Run, texts []string) {
	t.Helper()
	calls := sc.Script.Calls("")
	got := make([]string, 0, len(calls))
	for _, c := range calls {
		got = append(got, fmt.Sprintf("%s:%s:%s:%d", c.Stage, c.Item, c.Class, c.MaxOutputTokens))
	}
	want := []string{
		"p2::lote:512",
		"p3:" + scriptIdeaChoc + ":lote:512", "p3:" + scriptIdeaVanilla + ":lote:512", "p3:" + scriptIdeaTequenos + ":lote:512",
		"p4::lote:1024",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("las inferencias que recibió el Edge = %v, quería %v", got, want)
	}
	literal := "### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###\n" +
		"cliente: " + texts[0] + "\nnegocio: " + draftMenuPrompt + "\n" +
		"cliente: " + texts[1] + "\nnegocio: " + draftMenuInvalid + "\n" +
		"cliente: " + texts[2] + "\nnegocio: " + draftMenuInvalid + "\n" +
		"### FIN DE LOS MENSAJES ###"
	for i, c := range calls {
		if !strings.Contains(c.Prompt, literal) {
			t.Errorf("el prompt %d (%s) no lleva el hilo literal entero y rotulado", i, c.Stage)
		}
		if strings.Contains(c.Prompt, p3Welcome) {
			t.Errorf("el prompt %d (%s) lleva la bienvenida: no es un turno del hilo", i, c.Stage)
		}
		for _, id := range run.ids {
			if strings.Contains(c.Prompt, id) {
				t.Errorf("el prompt %d (%s) lleva la referencia opaca %s", i, c.Stage, id)
			}
		}
	}
	if p4 := calls[4].Prompt; !strings.Contains(p4, `delivery_date_basis vale exactamente "message_ts=`) || !strings.Contains(p4, `"product":"`+scriptIdeaTequenos+`"`) {
		t.Errorf("el prompt de P4 no lleva la fecha de referencia o los ítems que dejó P3")
	}
}

// p4OtherPn es el contacto del borrador que crea el helper reutilizable.
const p4OtherPn = "573004440005"

// p4AnotherDraft ejerce createDraft tal como lo usarán P5–P8, sobre una empresa que ya tiene tres
// borradores: otro contacto, otra solicitud. El pipeline corre una vez más (cinco inferencias), la
// solicitud nueva es distinta de las anteriores y trae la misma interpretación del caso Ámbar, y la
// lista de la dueña pasa a tener cuatro, sin que la primera cambie.
func p4AnotherDraft(t *testing.T, sc *draftScene, main p4Run) {
	calls := len(sc.Script.Calls(""))
	before := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intakes WHERE tenant_id = $1`, sc.Tenant)
	id := createDraft(t, sc, p4OtherPn)
	if !identidadUUID.MatchString(id) || id == main.intakeID {
		t.Fatalf("createDraft devolvió %q; quería el id de una solicitud nueva (la primera es %s)", id, main.intakeID)
	}
	if n := len(sc.Script.Calls("")) - calls; n != 5 {
		t.Errorf("createDraft provocó %d inferencias, quería 5 (P2, tres P3 y P4)", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intakes WHERE tenant_id = $1`, sc.Tenant); n != before+1 || n != 4 {
		t.Errorf("intakes pasó de %d a %d filas, quería 4 (feliz, adversaria, degradada y esta)", before, n)
	}
	p4CheckAmbarPayload(t, p4RevisionPayload(t, sc, id), "la revisión del borrador de createDraft")
	const status = `SELECT status || '|' || (SELECT contact_id FROM public.intake_jobs j WHERE j.intake_id = i.id) FROM public.intakes i WHERE id = $1::uuid`
	other := p9Scalar(t, sc.DB, status, id)
	if !strings.HasPrefix(other, "pending_approval|") || strings.HasSuffix(other, "|"+main.contactID) {
		t.Errorf("la solicitud de createDraft = %q; quería pending_approval y de OTRO contacto que %s", other, main.contactID)
	}
	var list struct{ Total int }
	r := sc.Pub.Get(t, "/api/v1/intakes", nil)
	r.JSON(t, &list)
	if list.Total != 4 {
		t.Errorf("GET /api/v1/intakes: total %d, quería 4\ncuerpo: %s", list.Total, recortar(r.Cuerpo))
	}
}

// p4Closing es el cierre del proceso, ANTES de parar el servidor: el borrador del recorrido feliz
// sigue intacto (una revisión, sin líneas propias), el guion atendió todo lo que se le pidió, el
// núcleo del Edge no anotó errores, ningún texto quedó sin leer y el log del servidor no trae ERROR.
func p4Closing(t *testing.T, sc *draftScene, main p4Run) {
	if got := p9Scalar(t, sc.DB, `SELECT status || '|' || (SELECT count(*) FROM public.intake_revisions r WHERE r.intake_id = i.id)::text
		FROM public.intakes i WHERE id = $1::uuid`, main.intakeID); got != "pending_approval|1" {
		t.Errorf("el borrador del recorrido feliz acabó en %q, quería pending_approval con una revisión", got)
	}
	if problems := sc.Script.Problems(); len(problems) != 0 {
		t.Errorf("el guion no supo atender: %v", problems)
	}
	for _, c := range sc.Script.Calls("") {
		if c.Class != "lote" || c.MaxOutputTokens != scriptCeilings[c.Stage] {
			t.Errorf("una inferencia de %s viajó con class %q y techo %d", c.Stage, c.Class, c.MaxOutputTokens)
		}
	}
	for _, req := range sc.Edge.Inferencias() {
		if req.GetWarmup() {
			t.Errorf("el Edge recibió un calentamiento sin catálogo de intenciones publicado")
		}
	}
	if errs := sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	sc.expectNoPendingText(t, "al cerrar el proceso")
	edgeSinErrores(t, sc.S, nil)
}
