//go:build integracion

package procesos

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// La rotación de KEK por POST /admin/crypto/rekey (permiso crypto.rekey): la parte de P10 que
// re-expresa crypto/rekey_integration_test.go. La tabla de la puerta y la auditoría van en
// p10_plataforma_rekey_door_test.go.
//
// 🔴 LO QUE ESTE PROCESO NO PUEDE HACER, Y ES EL CENTRO DEL TEST VIEJO: rotar de verdad. Re-envolver
// una DEK pide un servidor cuyo keyring tenga la KEK vieja y otra current (WAPP_KEK_KEYRING y
// WAPP_KEK_CURRENT), arrancado sobre la base en la que otro servidor ya cifró con la vieja. El arnés
// no sabe ni lo uno ni lo otro: opcionesServidor no admite variables de entorno y arrancar crea
// siempre una base nueva (hallazgo 42 de F9). Con la única KEK del arnés (WAPP_KEK_MASTER_B64,
// key_id «1») ninguna fila queda nunca pendiente. Por eso NO se afirma aquí que la pasada mueva el
// kek_id, que deje el dato cifrado intacto, que no toque el índice ciego, que Processed cuente
// sobres ni que el dato siga legible con la KEK vieja retirada.
//
// Lo que SÍ se ve por la puerta, y se afirma:
//   - la pasada sobre sobres ya al día es un no-op idempotente: processed 0, current «1», pendientes
//     vacío, ninguna fila del censo tocada (marca sobre las filas ENTERAS de sus siete tablas), y los
//     sobres siguen legibles: el servidor contesta a cada contacto en su número;
//   - una fila sin sobre (trío NULL) ni cuenta ni rompe la pasada, y nadie le inventa un sobre;
//   - las OCHO entradas del censo se barren (el test viejo cubría siete: intake_jobs entró después):
//     con un sobre bajo un key_id que el keyring no tiene, la pasada aborta con 500 sin tocar nada
//     (fail-safe), y eso solo ocurre si la entrada de ESE sobre está en el censo.

const (
	p10RouteRekey = "/admin/crypto/rekey"
	// p10ActRekey es el permiso de la ruta y la acción con la que se audita; p10ResRekey, el recurso.
	p10ActRekey = "crypto.rekey"
	p10ResRekey = "kek"
	// p10RekeyFailed es el cuerpo del 500: genérico, sin el error de dentro.
	p10RekeyFailed = "no se pudo completar la rotación de KEK"
	// p10CurrentKeyID es el key_id de la KEK única del camino de compatibilidad (el DEFAULT de
	// contacts.value_kek_id desde la 0007).
	p10CurrentKeyID = "1"
	// p10ForeignKeyID es un key_id que el keyring del servidor no tiene.
	p10ForeignKeyID = "p10-kek-ajena"

	// Los dos contactos del proceso y el nombre de perfil del primero (texto libre: no se normaliza).
	p10NamedPn    = "573001110101"
	p10NamelessPn = "573001110102"
	p10PushName   = "Nombre De Prueba"
)

// p10Envelopes es lo que rekey_envelopes deja para los subtests siguientes: los dos contactos, la
// solicitud y el job de fixture, la huella del secreto del puente y la marca del censo.
type p10Envelopes struct {
	named, nameless string // contact_id
	intake, job     string // fixtures de la otra empresa
	bridge          string // la firma del puente CRM que se guardó: aleatoria, no protege nada
	fingerprint     string // secret_fingerprint del puente, tal como lo dio el PUT
	mark            string // p10CensusMark tras sembrar
}

// p10CensusMark es la marca de estado de la rotación: un md5 de las filas ENTERAS de las siete
// tablas del censo (R9.5.c: todo lo que la operación puede tocar —la DEK envuelta, el key_id y
// updated_at— y lo que no debe: el dato cifrado y el índice ciego). De fleet_sessions solo entran
// las columnas del sobre y updated_at: last_seen_at lo mueve la presencia, no la rotación.
const p10CensusMark = `SELECT md5(
	coalesce((SELECT string_agg(to_jsonb(c)::text, '|' ORDER BY c.tenant_id, c.kind, c.value_bidx) FROM public.contacts c), '') || '#' ||
	coalesce((SELECT string_agg(to_jsonb(b)::text, '|' ORDER BY b.intake_id) FROM public.intake_buyer_data b), '') || '#' ||
	coalesce((SELECT string_agg(to_jsonb(i)::text, '|' ORDER BY i.tenant_id) FROM public.tenant_integrations i), '') || '#' ||
	coalesce((SELECT string_agg(jsonb_build_array(f.tenant_id, f.edge_id, f.session_id, encode(f.self_pn_enc, 'hex'),
		encode(f.self_pn_dek, 'hex'), f.self_pn_kek_id, f.self_pn_bidx, f.updated_at)::text, '|'
		ORDER BY f.tenant_id, f.edge_id, f.session_id) FROM public.fleet_sessions f), '') || '#' ||
	coalesce((SELECT string_agg(to_jsonb(l)::text, '|' ORDER BY l.tenant_id) FROM public.tenant_llm l), '') || '#' ||
	coalesce((SELECT string_agg(to_jsonb(j)::text, '|' ORDER BY j.id) FROM public.intake_jobs j), '') || '#' ||
	coalesce((SELECT string_agg(to_jsonb(r)::text, '|' ORDER BY r.intake_id, r.revision_no) FROM public.intake_revisions r), ''))`

// p10RekeyReport es el cuerpo 200 de la ruta.
type p10RekeyReport struct {
	Processed      int            `json:"processed"`
	CurrentKeyID   string         `json:"current_key_id"`
	PendingByKeyID map[string]int `json:"pending_by_key_id"`
}

// p10WantNoop exige que la respuesta sea el 200 de una pasada sin nada que rotar: solo los tres
// campos del informe (contadores y key_ids, ningún contenido), processed 0, la KEK current y el mapa
// de pendientes presente y vacío —que es lo que autoriza a retirar una KEK vieja—.
func p10WantNoop(t *testing.T, what string, r respuesta) {
	t.Helper()
	if r.Codigo != http.StatusOK {
		t.Errorf("%s: HTTP %d, quería 200\ncuerpo: %s", what, r.Codigo, recortar(r.Cuerpo))
		return
	}
	var fields map[string]json.RawMessage
	r.JSON(t, &fields)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if got := strings.Join(keys, ","); got != "current_key_id,pending_by_key_id,processed" {
		t.Errorf("%s: el informe trae los campos %q, quería solo current_key_id, pending_by_key_id y processed", what, got)
	}
	var rep p10RekeyReport
	r.JSON(t, &rep)
	if rep.Processed != 0 || rep.CurrentKeyID != p10CurrentKeyID || rep.PendingByKeyID == nil || len(rep.PendingByKeyID) != 0 {
		t.Errorf("%s: informe %s, quería processed 0, current %q y pendientes {}", what, recortar(r.Cuerpo), p10CurrentKeyID)
	}
}

// rekey llama a la ruta como c y apunta su fila de auditoría si audited (la petición llegó al
// handler: el 401 y el 403 del middleware no auditan).
func (w *p10World) rekey(t *testing.T, c p9Caller, method, query string, body any, audited bool) respuesta {
	t.Helper()
	action := ""
	if audited {
		action = p10ActRekey
	}
	path := p10RouteRekey
	if query != "" {
		path += "?" + query
	}
	return w.calls.call(t, c, action, method, path, body)
}

// wantMark exige que la marca del censo siga siendo la apuntada.
func (w *p10World) wantMark(t *testing.T, when string) {
	t.Helper()
	if got := consultaTexto(t, w.sc.DB, p10CensusMark); got != w.env.mark {
		t.Errorf("%s: las filas del censo cambiaron (marca %s → %s)", when, w.env.mark, got)
	}
}

// rekeyEnvelopes deja los sobres del censo. CUATRO los escribe el servidor por sus puertas, con su
// KEK: el número y el nombre de un contacto y el número de otro sin nombre (entrantes sellados del
// Edge), el número propio de la sesión (un latido) y el secreto del puente (PUT /api/v1/integrations).
// Los otros CUATRO son de fixture, de la otra empresa —sin Edge, así que nada los lee—: el
// comprador, el literal, la API key y el texto del job no tienen puerta sin el pipeline (P4).
func (w *p10World) rekeyEnvelopes(t *testing.T) {
	sc := w.sc
	sc.beatWithSelfPn(t, 5)
	edgeEsperarValor(t, sc.DB, "4", "el sobre del número propio de la sesión",
		`SELECT num_nonnulls(self_pn_enc, self_pn_dek, self_pn_kek_id, self_pn_bidx)::text FROM public.fleet_sessions
		 WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`, sc.Tenant, sc.Edge.EdgeID, sc.Edge.SessionID)

	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: p10NamedPn + "@s.whatsapp.net", WaID: "P10-IN-1", Text: p3Keyword, FromPn: p10NamedPn, PushName: p10PushName})
	sc.expectText(t, p10NamedPn, p3Welcome)
	sc.expectText(t, p10NamedPn, p3MenuPrompt)
	w.env.named = p3NewContactID(t, sc, nil)
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: p10NamelessPn + "@s.whatsapp.net", WaID: "P10-IN-2", Text: p3Keyword, FromPn: p10NamelessPn})
	sc.expectText(t, p10NamelessPn, p3Welcome)
	sc.expectText(t, p10NamelessPn, p3MenuPrompt)
	w.env.nameless = p3NewContactID(t, sc, []string{w.env.named})

	// «Las tres o ninguna»: el contacto con nombre tiene el sobre entero, y el que no lo trajo nace
	// con las tres columnas a NULL (la 0069 las creó sin DEFAULT a propósito).
	const pieces = `SELECT num_nonnulls(push_name_enc, push_name_dek, push_name_kek_id) FROM public.contacts WHERE tenant_id = $1::uuid AND contact_id = $2::uuid`
	if n := consultaEntero(t, sc.DB, pieces, sc.Tenant, w.env.named); n != 3 {
		t.Errorf("el contacto con nombre tiene %d de las 3 piezas del sobre del nombre", n)
	}
	if n := consultaEntero(t, sc.DB, pieces, sc.Tenant, w.env.nameless); n != 0 {
		t.Errorf("el contacto sin nombre nace con %d de las 3 piezas del sobre pobladas, quería 0", n)
	}

	w.env.bridge = "p10-" + edgeAleatorioHex(t, 16)
	r := w.calls.call(t, p9Caller{client: sc.Pub}, "", http.MethodPut, "/api/v1/integrations", map[string]any{
		"catalog_adapter": "local", "events_adapter": "webhook", "endpoint_url": "https://puente.example/hook",
		"secret": w.env.bridge, "enabled": false,
	})
	var bridge struct {
		SecretSet   bool   `json:"secret_set"`
		Fingerprint string `json:"secret_fingerprint"`
	}
	if r.Codigo == http.StatusOK {
		r.JSON(t, &bridge)
	}
	if r.Codigo != http.StatusOK || !bridge.SecretSet || bridge.Fingerprint == "" {
		t.Fatalf("guardar el secreto del puente: HTTP %d, quería 200 con secret_set y su huella\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	w.env.fingerprint = bridge.Fingerprint

	w.seedFixtureEnvelopes(t)
	// Todo sobre que existe está bajo la KEK current: no hay nada pendiente antes de empezar.
	for _, e := range p10Census {
		if n := consultaEntero(t, sc.DB, e.count, p10CurrentKeyID); n < 1 {
			t.Errorf("%s: %d sobres bajo la KEK %q, quería al menos 1", e.name, n, p10CurrentKeyID)
		}
		if n := consultaEntero(t, sc.DB, e.pending, p10CurrentKeyID); n != 0 {
			t.Errorf("%s: %d sobres bajo otra KEK antes de empezar", e.name, n)
		}
	}
	if strings.Contains(sc.S.Log(), p10PushName) || strings.Contains(sc.S.Log(), w.env.bridge) {
		t.Errorf("el nombre del contacto o el secreto del puente aparecen en el log del servidor")
	}
	w.env.mark = consultaTexto(t, sc.DB, p10CensusMark)
}

// seedFixtureEnvelopes siembra, en la otra empresa, los cuatro sobres que no tienen puerta y una
// integración SIN secreto. Los bytes no son un sobre de verdad: nada los abre (esa empresa no tiene
// Edge ni tráfico, la solicitud nace cerrada y el job, terminado) y a la rotación no le hace falta
// abrirlos mientras estén bajo la KEK current.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: el comprador, el literal y el texto del job los escribe el pipeline
// de captación (P4, con su guion de inferencia), y la API key es de la vía LLM `api`, que el cliente
// del arnés se niega a pedir (cero gasto). La fila de tenant_llm es de esa vía porque el esquema no
// admite un sobre en la vía local; con bytes que no descifran no hay credencial que gastar.
func (w *p10World) seedFixtureEnvelopes(t *testing.T) {
	t.Helper()
	db, tenant := w.sc.DB, w.otherTenant
	event := p2Text(t, db, `
		INSERT INTO public.conversation_events
			(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version, closed_at)
		VALUES ($1::uuid, 'sesion-p10', $2::uuid, 'cart', 'cart-p10-0001', 'closed', 'flujo-p10', 1, now())
		RETURNING id::text`, tenant, uuidAleatorio(t))
	w.env.intake = uuidAleatorio(t)
	p10Exec(t, db, `INSERT INTO public.intakes (id, tenant_id, contact_id, session_id, status, total, event_id)
		VALUES ($1::uuid, $2, 'contacto-opaco', 'sesion-p10', 'closed', 0, $3::uuid)`, w.env.intake, tenant, event)
	p10Exec(t, db, `INSERT INTO public.intake_buyer_data (intake_id, data_enc, data_dek, data_kek_id)
		VALUES ($1::uuid, '\x01'::bytea, '\x02'::bytea, $2)`, w.env.intake, p10CurrentKeyID)
	p10Exec(t, db, `INSERT INTO public.intake_revisions
			(intake_id, revision_no, kind, payload, created_by, literal_enc, literal_dek, literal_kek_id)
		VALUES ($1::uuid, 1, 'interpreted', '{"version":1,"lines":[]}'::jsonb, 'system', '\x03'::bytea, '\x04'::bytea, $2)`,
		w.env.intake, p10CurrentKeyID)
	p10Exec(t, db, `INSERT INTO public.tenant_llm
			(tenant_id, via, provider, model, api_key_enc, api_key_dek, api_key_kek_id, consented_at)
		VALUES ($1, 'api', 'anthropic', 'claude-sonnet-4-5', '\x05'::bytea, '\x06'::bytea, $2, now())`, tenant, p10CurrentKeyID)
	w.env.job = p2Text(t, db, `INSERT INTO public.intake_jobs
			(tenant_id, session_id, contact_id, event_id, status, source_text_enc, source_text_dek, source_text_kek_id)
		VALUES ($1, 'sesion-p10', 'contacto-opaco', $2::uuid, 'failed', '\x07'::bytea, '\x08'::bytea, $3)
		RETURNING id::text`, tenant, event, p10CurrentKeyID)
	// La fila SIN secreto: el trío del sobre a NULL. `NULL <> 'x'` no es TRUE en SQL.
	p10Exec(t, db, `INSERT INTO public.tenant_integrations (tenant_id, catalog_adapter, events_adapter) VALUES ($1, 'local', 'local')`, tenant)
}

// rekeyNoopPass: tres pasadas seguidas (por defecto, de una en una y con otra administradora: la
// rotación es global, no toma empresa de quien llama) no encuentran nada, no tocan una sola fila del
// censo ni le inventan un sobre al contacto sin nombre ni a la integración sin secreto. Y después
// todo sigue legible: el servidor resuelve el destino de cada contacto descifrando su número y le
// contesta, y el puente devuelve la misma huella de su secreto.
func (w *p10World) rekeyNoopPass(t *testing.T) {
	sc := w.sc
	p10WantNoop(t, "primera pasada", w.rekey(t, w.admin, http.MethodPost, "", nil, true))
	p10WantNoop(t, "segunda pasada, de una en una", w.rekey(t, w.admin, http.MethodPost, "batch=1", nil, true))
	p10WantNoop(t, "pasada de la administradora de otra empresa", w.rekey(t, w.other, http.MethodPost, "", nil, true))
	w.wantMark(t, "tras las pasadas sin nada que rotar")

	if n := consultaEntero(t, sc.DB, `SELECT num_nonnulls(push_name_enc, push_name_dek, push_name_kek_id) FROM public.contacts
		WHERE tenant_id = $1::uuid AND contact_id = $2::uuid`, sc.Tenant, w.env.nameless); n != 0 {
		t.Errorf("tras la pasada, el contacto sin nombre tiene %d de las 3 piezas del sobre pobladas, quería 0", n)
	}
	if n := consultaEntero(t, sc.DB, `SELECT num_nonnulls(secret_enc, secret_dek, secret_kek_id) FROM public.tenant_integrations
		WHERE tenant_id = $1`, w.otherTenant); n != 0 {
		t.Errorf("tras la pasada, la integración sin secreto tiene %d de las 3 piezas del sobre pobladas, quería 0", n)
	}

	// La lectura posterior de los contactos cifrados: la respuesta sale al número descifrado.
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: p10NamedPn + "@s.whatsapp.net", WaID: "P10-IN-3", Text: "1", FromPn: p10NamedPn, PushName: p10PushName})
	sc.expectText(t, p10NamedPn, p3SalesText)
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: p10NamelessPn + "@s.whatsapp.net", WaID: "P10-IN-4", Text: "2", FromPn: p10NamelessPn})
	sc.expectText(t, p10NamelessPn, p3SupportText)
	if ids := p3ContactIDs(t, sc); !slices.Equal(ids, slices.Sorted(slices.Values([]string{w.env.named, w.env.nameless}))) {
		t.Errorf("los contactos de la empresa son %v; los dos de antes eran %s y %s", ids, w.env.named, w.env.nameless)
	}
	r := w.calls.call(t, p9Caller{client: sc.Pub}, "", http.MethodGet, "/api/v1/integrations", nil)
	var bridge struct {
		SecretSet   bool   `json:"secret_set"`
		Fingerprint string `json:"secret_fingerprint"`
	}
	if r.Codigo == http.StatusOK {
		r.JSON(t, &bridge)
	}
	if r.Codigo != http.StatusOK || !bridge.SecretSet || bridge.Fingerprint != w.env.fingerprint {
		t.Errorf("leer el puente tras la pasada: HTTP %d con huella %q, quería 200 con la huella %q", r.Codigo, bridge.Fingerprint, w.env.fingerprint)
	}
	// Responder movió flow_state y el nombre ya estaba: las filas del censo siguen igual.
	w.wantMark(t, "tras leer los contactos y el puente")
}

// p10CensusEntry es una entrada del censo de la rotación, vista desde fuera: UN sobre —no una
// tabla: la fila de contacts tiene dos—, cómo se cuentan los suyos bajo una KEK y bajo cualquier
// otra, y cómo se cambia el key_id del de este proceso. args elige esa fila.
type p10CensusEntry struct {
	name    string
	count   string // sobres con kek_id = $1
	pending string // sobres con kek_id <> $1 (los NULL no entran: no son sobres)
	set     string // UPDATE … SET kek_id = $1 WHERE <la fila del proceso>
	args    func(w *p10World) []any
}

// p10Census son las ocho entradas, en el orden en que las barre el servidor.
var p10Census = []p10CensusEntry{
	{"contacts.value",
		`SELECT count(*) FROM public.contacts WHERE value_kek_id = $1`,
		`SELECT count(*) FROM public.contacts WHERE value_kek_id <> $1`,
		`UPDATE public.contacts SET value_kek_id = $1 WHERE tenant_id = $2::uuid AND contact_id = $3::uuid`,
		func(w *p10World) []any { return []any{w.sc.Tenant, w.env.nameless} }},
	{"intake_buyer_data.data",
		`SELECT count(*) FROM public.intake_buyer_data WHERE data_kek_id = $1`,
		`SELECT count(*) FROM public.intake_buyer_data WHERE data_kek_id <> $1`,
		`UPDATE public.intake_buyer_data SET data_kek_id = $1 WHERE intake_id = $2::uuid`,
		func(w *p10World) []any { return []any{w.env.intake} }},
	{"tenant_integrations.secret",
		`SELECT count(*) FROM public.tenant_integrations WHERE secret_kek_id = $1`,
		`SELECT count(*) FROM public.tenant_integrations WHERE secret_kek_id <> $1`,
		`UPDATE public.tenant_integrations SET secret_kek_id = $1 WHERE tenant_id = $2`,
		func(w *p10World) []any { return []any{w.sc.Tenant} }},
	{"fleet_sessions.self_pn",
		`SELECT count(*) FROM public.fleet_sessions WHERE self_pn_kek_id = $1`,
		`SELECT count(*) FROM public.fleet_sessions WHERE self_pn_kek_id <> $1`,
		`UPDATE public.fleet_sessions SET self_pn_kek_id = $1 WHERE tenant_id = $2::uuid AND session_id = $3`,
		func(w *p10World) []any { return []any{w.sc.Tenant, w.sc.Edge.SessionID} }},
	{"contacts.push_name",
		`SELECT count(*) FROM public.contacts WHERE push_name_kek_id = $1`,
		`SELECT count(*) FROM public.contacts WHERE push_name_kek_id <> $1`,
		`UPDATE public.contacts SET push_name_kek_id = $1 WHERE tenant_id = $2::uuid AND contact_id = $3::uuid`,
		func(w *p10World) []any { return []any{w.sc.Tenant, w.env.named} }},
	{"tenant_llm.api_key",
		`SELECT count(*) FROM public.tenant_llm WHERE api_key_kek_id = $1`,
		`SELECT count(*) FROM public.tenant_llm WHERE api_key_kek_id <> $1`,
		`UPDATE public.tenant_llm SET api_key_kek_id = $1 WHERE tenant_id = $2`,
		func(w *p10World) []any { return []any{w.otherTenant} }},
	{"intake_jobs.source_text",
		`SELECT count(*) FROM public.intake_jobs WHERE source_text_kek_id = $1`,
		`SELECT count(*) FROM public.intake_jobs WHERE source_text_kek_id <> $1`,
		`UPDATE public.intake_jobs SET source_text_kek_id = $1 WHERE id = $2::uuid`,
		func(w *p10World) []any { return []any{w.env.job} }},
	{"intake_revisions.literal",
		`SELECT count(*) FROM public.intake_revisions WHERE literal_kek_id = $1`,
		`SELECT count(*) FROM public.intake_revisions WHERE literal_kek_id <> $1`,
		`UPDATE public.intake_revisions SET literal_kek_id = $1 WHERE intake_id = $2::uuid AND revision_no = 1`,
		func(w *p10World) []any { return []any{w.env.intake} }},
}

// rekeyCensus pone, sobre a sobre, UNO solo bajo un key_id que el keyring no tiene y pide la pasada:
// contesta 500 con el cuerpo genérico, no toca nada (ni ese sobre ni los demás) y, devuelto el
// key_id, vuelve a contestar el no-op. Que aborte prueba que la entrada de ese sobre está en el
// censo: si faltara, la pasada no lo vería y contestaría 200 con el mapa de pendientes vacío —el
// mapa que autoriza a retirar una KEK— con un sobre todavía bajo otra. El contacto sin nombre es el
// que lleva el key_id ajeno en su número: su sobre de nombre, que no existe, no entra en nada.
func (w *p10World) rekeyCensus(t *testing.T) {
	for _, e := range p10Census {
		t.Run(e.name, func(t *testing.T) {
			set := func(keyID string) {
				t.Helper()
				if n := p10Exec(t, w.sc.DB, e.set, append([]any{keyID}, e.args(w)...)...); n != 1 {
					t.Fatalf("%s: cambiar el key_id a %q tocó %d filas, quería 1", e.name, keyID, n)
				}
			}
			set(p10ForeignKeyID)
			foreign := consultaTexto(t, w.sc.DB, p10CensusMark)
			if n := consultaEntero(t, w.sc.DB, e.pending, p10CurrentKeyID); n != 1 {
				t.Fatalf("%s: %d sobres bajo otra KEK, quería 1", e.name, n)
			}
			r := w.rekey(t, w.admin, http.MethodPost, "batch=1", nil, true)
			if !p9ErrorIs(r, http.StatusInternalServerError, p10RekeyFailed) || strings.Contains(string(r.Cuerpo), p10ForeignKeyID) {
				t.Errorf("%s bajo una KEK ausente: HTTP %d %q, quería 500 %q sin el key_id (¿la entrada salió del censo?)",
					e.name, r.Codigo, recortar(r.Cuerpo), p10RekeyFailed)
			}
			if got := consultaTexto(t, w.sc.DB, p10CensusMark); got != foreign {
				t.Errorf("%s: la pasada abortada tocó filas del censo", e.name)
			}
			set(p10CurrentKeyID)
			p10WantNoop(t, fmt.Sprintf("%s de vuelta a la KEK current", e.name), w.rekey(t, w.admin, http.MethodPost, "", nil, true))
		})
	}
	w.wantMark(t, "tras recorrer el censo")
}
