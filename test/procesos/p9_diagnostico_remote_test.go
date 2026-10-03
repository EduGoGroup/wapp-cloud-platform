//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// P9, primera mitad: el diagnóstico remoto (ADR-0023 capa 3). La administradora lo pide por HTTP, el
// Edge recibe un DiagnosticsRequest, contesta con un DiagnosticsBundle y la descarga lo devuelve
// mientras no venza. Sale de p9_diagnostico_test.go por tamaño.

const (
	// p9DiagTTL es el plazo de un diagnóstico cuando el entorno no fija WAPP_DIAGNOSTICS_BUNDLE_TTL
	// (el arnés no la pone): 30 minutos (defaultDiagnosticsTTL, internal/publicapi).
	p9DiagTTL = 30 * time.Minute
	// p9DiagRow cuenta las filas de diagnostics_bundles de un command_id.
	p9DiagRow = `SELECT count(*)::text FROM public.diagnostics_bundles WHERE command_id = $1`
	// p9DiagByTenant cuenta las filas de diagnostics_bundles de una empresa.
	p9DiagByTenant = `SELECT count(*)::text FROM public.diagnostics_bundles WHERE tenant_id = $1::uuid`
	// p9DiagState da, de una solicitud, su estado y si ya tiene contenido.
	p9DiagState = `SELECT status || '|' || (received_at IS NOT NULL)::text || '|' || COALESCE(log_tail, '<null>')
		FROM public.diagnostics_bundles WHERE command_id = $1`
)

// p9DiagAccepted es la respuesta 202 de POST /api/v1/sessions/{id}/diagnostics.
type p9DiagAccepted struct {
	CommandID string `json:"command_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
}

// p9DiagDownload es la respuesta 200 de GET /api/v1/diagnostics/{command_id}.
type p9DiagDownload struct {
	CommandID      string `json:"command_id"`
	SessionID      string `json:"session_id"`
	RequestedBy    string `json:"requested_by"`
	RequestedAt    string `json:"requested_at"`
	ReceivedAt     string `json:"received_at"`
	LogTail        string `json:"log_tail"`
	GoroutineDump  string `json:"goroutine_dump"`
	SubsystemsJSON string `json:"subsystems_json"`
}

// p9ExpireDiagnostics retrasa el vencimiento de una solicitud de diagnóstico en 31 minutos (su plazo
// más uno), como si se hubiera pedido hace más de media hora. Falla (t.Fatalf) si el SQL falla o no
// toca exactamente una fila.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: el vencimiento se mide contra el reloj (expires_at frente a now()) y
// ninguna ruta lo mueve; el plazo solo se acorta con WAPP_DIAGNOSTICS_BUNDLE_TTL, que el arnés no
// pone y un proceso no puede cambiar. Y `envejecer` (fixtures_test.go) no sirve: su lista blanca son
// tablas con `id`, created_at y updated_at, y diagnostics_bundles tiene command_id y expires_at.
func p9ExpireDiagnostics(t *testing.T, db *sql.DB, commandID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	res, err := db.ExecContext(ctx, `UPDATE public.diagnostics_bundles
		SET expires_at = expires_at - interval '31 minutes' WHERE command_id = $1`, commandID)
	if err != nil {
		t.Fatalf("p9ExpireDiagnostics(%s): %v", commandID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("p9ExpireDiagnostics(%s): %d filas tocadas (err %v), quería 1", commandID, n, err)
	}
}

// p9SetDiagnosticsConsent deja escrito el consentimiento de diagnóstico remoto de una empresa
// (tenant_diagnostics_consent.enabled): false es el opt-out. Es un upsert. Falla (t.Fatalf) si el SQL
// falla.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: ninguna ruta de /api/v1 ni de /admin escribe esta tabla; el
// consentimiento es por defecto «sí» (sin fila) y el opt-out lo anota hoy el operador por SQL.
func p9SetDiagnosticsConsent(t *testing.T, db *sql.DB, tenant string, enabled bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_diagnostics_consent (tenant_id, enabled) VALUES ($1::uuid, $2)
		ON CONFLICT (tenant_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`, tenant, enabled); err != nil {
		t.Fatalf("p9SetDiagnosticsConsent(%s, %v): %v", tenant, enabled, err)
	}
}

// requestDiag pide un diagnóstico de la sesión del Edge e como c, exige el 202 pendiente y espera a
// que el Edge reciba el DiagnosticsRequest con el scope wantScope. Apunta el command_id entre los que
// ese Edge tiene que haber recibido. body es el cuerpo de la petición (nil = sin cuerpo).
func (w *p9World) requestDiag(t *testing.T, c p9Caller, e *edge, body any, wantScope string) p9DiagAccepted {
	t.Helper()
	r := w.call(t, c, p9ActDiag, http.MethodPost, p9DiagPath(e.SessionID), body)
	var acc p9DiagAccepted
	if r.Codigo != http.StatusAccepted {
		t.Fatalf("pedir el diagnóstico de %s: HTTP %d, quería 202\ncuerpo: %s", e.SessionID, r.Codigo, recortar(r.Cuerpo))
	}
	r.JSON(t, &acc)
	if !identidadUUID.MatchString(acc.CommandID) || acc.SessionID != e.SessionID || acc.Status != "pending" {
		t.Fatalf("pedir el diagnóstico: %+v, quería un command_id UUID, la sesión %s y status pending", acc, e.SessionID)
	}
	w.diags[e.SessionID] = append(w.diags[e.SessionID], acc.CommandID)
	want := diagnosticoPedido{ComandoID: acc.CommandID, Scope: wantScope, Sesion: e.SessionID}
	var last diagnosticoPedido
	edgeEsperar(t, edgeTopeFila, "el DiagnosticsRequest "+acc.CommandID+" en el Edge", func() bool {
		ds := e.Diagnosticos()
		if len(ds) == 0 {
			return false
		}
		last = ds[len(ds)-1]
		return last.ComandoID == acc.CommandID
	})
	if last != want {
		t.Errorf("el Edge recibió %+v, quería %+v", last, want)
	}
	return acc
}

// diagnosticsCycle es el camino entero: la petición sin cuerpo (scope «full»), la fila pendiente con
// su plazo de 30 min, la descarga pendiente (202), los bundles que NO casan (de otra sesión, de otra
// empresa, de un command_id que nadie pidió), el bundle bueno, la descarga (200) y el duplicado.
func (w *p9World) diagnosticsCycle(t *testing.T) {
	db, s := w.esc.DB, w.esc.S
	acc := w.requestDiag(t, w.main, w.e, nil, "full")
	requestedBy := w.diagPendingRequest(t, acc)
	w.diagUnmatchedBundles(t, acc)

	// El bueno.
	const logTail = "cola de log del Edge p9"
	w.diagMatchingBundle(t, acc, requestedBy, logTail)

	// Un segundo bundle del mismo command_id ya no encuentra solicitud pendiente: no pisa el primero.
	w.e.bundle(t, acc.CommandID, "bundle duplicado")
	p9WaitLogLines(t, s, p9MsgOrphan, map[string]string{"command_id": acc.CommandID, "session_id": w.e.SessionID}, 1)
	if got := p9Scalar(t, db, p9DiagState, acc.CommandID); got != "ready|true|"+logTail {
		t.Errorf("tras el bundle duplicado la solicitud está %q", got)
	}
	// El contenido del bundle es material del cliente: no va al log del servidor.
	for _, text := range []string{logTail, "bundle de otra sesión", "bundle huérfano", "volcado de prueba"} {
		if strings.Contains(s.Log(), text) {
			t.Errorf("el log del servidor trae contenido de un bundle: %q", text)
		}
	}
}

// diagPendingRequest mira la solicitud recién pedida: la fila pendiente con quién la pidió y su
// plazo de 30 minutos, y la descarga antes del bundle (202). Devuelve el requested_by de la fila.
func (w *p9World) diagPendingRequest(t *testing.T, acc p9DiagAccepted) string {
	db := w.esc.DB
	// La fila: pendiente, de la empresa y la sesión, con quién la pidió y un plazo de 30 minutos.
	row := p9Scalar(t, db, `SELECT status || '|' || tenant_id::text || '|' || session_id || '|' ||
		((expires_at - requested_at) BETWEEN interval '29 minutes' AND interval '31 minutes')::text || '|' ||
		(received_at IS NULL AND log_tail IS NULL AND goroutine_dump IS NULL AND subsystems_json IS NULL)::text
		FROM public.diagnostics_bundles WHERE command_id = $1`, acc.CommandID)
	if want := "pending|" + w.esc.Tenant + "|" + w.e.SessionID + "|true|true"; row != want {
		t.Errorf("diagnostics_bundles = %q, quería %q", row, want)
	}
	requestedBy := p9Scalar(t, db, `SELECT requested_by FROM public.diagnostics_bundles WHERE command_id = $1`, acc.CommandID)
	if !identidadUUID.MatchString(requestedBy) {
		t.Errorf("requested_by = %q, quería el subject (UUID) de la administradora", requestedBy)
	}
	expires, err := time.Parse(time.RFC3339, acc.ExpiresAt)
	if left := time.Until(expires); err != nil || left < p9DiagTTL-time.Minute || left > p9DiagTTL+time.Minute {
		t.Errorf("expires_at = %q (faltan %s, err %v), quería ~%s", acc.ExpiresAt, left, err, p9DiagTTL)
	}

	// La descarga antes del bundle: 202 con el estado.
	pend := w.call(t, w.main, p9ActDiag, http.MethodGet, p9BundlePath(acc.CommandID), nil)
	var pending struct {
		CommandID string `json:"command_id"`
		Status    string `json:"status"`
	}
	pend.JSON(t, &pending)
	if pend.Codigo != http.StatusAccepted || pending.CommandID != acc.CommandID || pending.Status != "pending" {
		t.Errorf("la descarga antes del bundle: HTTP %d %+v, quería 202 pending", pend.Codigo, pending)
	}
	return requestedBy
}

// diagUnmatchedBundles manda los tres bundles que no casan con la solicitud (de otra sesión, de
// otra empresa, de un command_id que nadie pidió): quedan huérfanos y la solicitud sigue pendiente.
func (w *p9World) diagUnmatchedBundles(t *testing.T, acc p9DiagAccepted) {
	db, s := w.esc.DB, w.esc.S
	// Tres bundles que no casan: la correlación es command_id + empresa + sesión del certificado.
	w.e2.bundle(t, acc.CommandID, "bundle de otra sesión")
	p9WaitLogLines(t, s, p9MsgOrphan, map[string]string{"command_id": acc.CommandID, "session_id": w.e2.SessionID}, 1)
	w.eOth.bundle(t, acc.CommandID, "bundle de otra empresa")
	p9WaitLogLines(t, s, p9MsgOrphan, map[string]string{"command_id": acc.CommandID, "session_id": w.eOth.SessionID}, 1)
	w.e.bundle(t, "diag-que-nadie-pidio", "bundle huérfano")
	p9WaitLogLines(t, s, p9MsgOrphan, map[string]string{"command_id": "diag-que-nadie-pidio", "session_id": w.e.SessionID}, 1)
	if got := p9Scalar(t, db, p9DiagState, acc.CommandID); got != "pending|false|<null>" {
		t.Errorf("tras los bundles que no casan la solicitud está %q, quería pending|false|<null>", got)
	}
	if got := p9Scalar(t, db, p9DiagRow, "diag-que-nadie-pidio"); got != "0" {
		t.Errorf("el bundle huérfano dejó %s filas", got)
	}
}

// diagMatchingBundle manda el bundle bueno y mira la descarga (200): trae lo guardado, quién lo
// pidió y dos instantes recientes y en orden. Falla (t.Fatalf) si la descarga no es un 200.
func (w *p9World) diagMatchingBundle(t *testing.T, acc p9DiagAccepted, requestedBy, logTail string) {
	db := w.esc.DB
	w.e.bundle(t, acc.CommandID, logTail)
	edgeEsperarValor(t, db, "ready|true|"+logTail, "la solicitud con su bundle", p9DiagState, acc.CommandID)
	got := w.call(t, w.main, p9ActDiag, http.MethodGet, p9BundlePath(acc.CommandID), nil)
	if got.Codigo != http.StatusOK {
		t.Fatalf("la descarga del bundle: HTTP %d, quería 200\ncuerpo: %s", got.Codigo, recortar(got.Cuerpo))
	}
	var dl p9DiagDownload
	got.JSON(t, &dl)
	stored := p9Scalar(t, db, `SELECT goroutine_dump || '|' || subsystems_json FROM public.diagnostics_bundles WHERE command_id = $1`, acc.CommandID)
	if dl.CommandID != acc.CommandID || dl.SessionID != w.e.SessionID || dl.RequestedBy != requestedBy ||
		dl.LogTail != logTail || dl.GoroutineDump == "" || dl.SubsystemsJSON == "" || dl.GoroutineDump+"|"+dl.SubsystemsJSON != stored {
		t.Errorf("la descarga trae %+v (guardado %q)", dl, stored)
	}
	reqAt, errReq := time.Parse(time.RFC3339, dl.RequestedAt)
	recAt, errRec := time.Parse(time.RFC3339, dl.ReceivedAt)
	if errReq != nil || errRec != nil || recAt.Before(reqAt) || time.Since(reqAt) > time.Minute {
		t.Errorf("requested_at %q y received_at %q: quería dos instantes RFC3339 recientes y en orden", dl.RequestedAt, dl.ReceivedAt)
	}
}

// diagnosticsIsolation: el command_id y la sesión de una empresa no existen para la administradora
// de otra (404, sin fila y sin petición al Edge), y sin token no hay nada (401).
func (w *p9World) diagnosticsIsolation(t *testing.T) {
	cmd := w.diags[w.e.SessionID][0]
	if r := w.call(t, w.other, p9ActDiag, http.MethodGet, p9BundlePath(cmd), nil); !p9ErrorIs(r, http.StatusNotFound, "diagnóstico no encontrado") {
		t.Errorf("descargar el bundle de otra empresa: HTTP %d %s, quería 404", r.Codigo, recortar(r.Cuerpo))
	}
	if r := w.call(t, w.other, p9ActDiag, http.MethodPost, p9DiagPath(w.e.SessionID), nil); !p9ErrorIs(r, http.StatusNotFound, "sesión no encontrada para el tenant") {
		t.Errorf("pedir el diagnóstico de la sesión de otra empresa: HTTP %d %s, quería 404", r.Codigo, recortar(r.Cuerpo))
	}
	if r := w.call(t, w.main, p9ActDiag, http.MethodPost, p9DiagPath("sesion-que-no-existe"), nil); r.Codigo != http.StatusNotFound {
		t.Errorf("pedir el diagnóstico de una sesión inexistente: HTTP %d, quería 404", r.Codigo)
	}
	if r := w.call(t, w.main, p9ActDiag, http.MethodGet, p9BundlePath(uuidAleatorio(t)), nil); r.Codigo != http.StatusNotFound {
		t.Errorf("descargar un command_id desconocido: HTTP %d, quería 404", r.Codigo)
	}
	anon := p9Caller{client: w.esc.S.Publica(""), tenant: ""}
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, p9DiagPath(w.e.SessionID)},
		{http.MethodGet, p9BundlePath(cmd)},
	} {
		if r := w.call(t, anon, "", c.method, c.path, nil); r.Codigo != http.StatusUnauthorized {
			t.Errorf("%s %s sin token: HTTP %d, quería 401", c.method, c.path, r.Codigo)
		}
	}
	if got := p9Scalar(t, w.esc.DB, p9DiagByTenant, w.other.tenant); got != "0" {
		t.Errorf("la otra empresa tiene %s solicitudes de diagnóstico, quería 0", got)
	}
	if got := p9Scalar(t, w.esc.DB, p9DiagByTenant, w.esc.Tenant); got != "1" {
		t.Errorf("la empresa tiene %s solicitudes de diagnóstico, quería 1 (la del ciclo)", got)
	}
	// La otra empresa sí puede pedir el de SU sesión: el diagnóstico no depende del plan.
	own := w.requestDiag(t, w.other, w.eOth, map[string]string{"scope": "logs"}, "logs")
	if r := w.call(t, w.main, p9ActDiag, http.MethodGet, p9BundlePath(own.CommandID), nil); r.Codigo != http.StatusNotFound {
		t.Errorf("la administradora de la empresa descarga el de la otra: HTTP %d, quería 404", r.Codigo)
	}
}

// diagnosticsOffline: a una sesión sin stream vivo no se le puede pedir nada. La API contesta 502 y
// deshace la fila pendiente (nunca recibiría bundle). Al reconectar, el Edge recibe de nuevo su
// config inicial —sin catálogo publicado, jwks y filters— y vuelve a poder ser diagnosticado.
func (w *p9World) diagnosticsOffline(t *testing.T) {
	db := w.esc.DB
	w.e2.desconectar(t)
	edgeEsperarValor(t, db, "offline", "la sesión tras desconectar", edgeEstadoSesion, w.esc.Tenant, w.e2.EdgeID, w.e2.SessionID)
	r := w.call(t, w.main, p9ActDiag, http.MethodPost, p9DiagPath(w.e2.SessionID), nil)
	if !p9ErrorIs(r, http.StatusBadGateway, "sesión offline") {
		t.Errorf("pedir el diagnóstico de una sesión offline: HTTP %d %s, quería 502", r.Codigo, recortar(r.Cuerpo))
	}
	if got := p9Scalar(t, db, `SELECT count(*)::text FROM public.diagnostics_bundles WHERE session_id = $1`, w.e2.SessionID); got != "0" {
		t.Errorf("el 502 dejó %s filas de la sesión offline, quería 0 (la solicitud se deshace)", got)
	}

	from := len(w.e2.Configs())
	w.reconnectEdge(t, w.e2)
	p9WaitConfigs(t, w.e2, from, "filters", 1)
	if kinds := p9Kinds(p9ConfigsSince(w.e2, from, "")); fmt.Sprint(kinds) != "[jwks filters]" {
		t.Errorf("al reconectar sin catálogo publicado el Edge recibió %v, quería [jwks filters]", kinds)
	}
	back := w.requestDiag(t, w.main, w.e2, map[string]string{"scope": "logs"}, "logs")
	w.e2.bundle(t, back.CommandID, "de vuelta")
	edgeEsperarValor(t, db, "ready|true|de vuelta", "el bundle de la sesión reconectada", p9DiagState, back.CommandID)
}

// diagnosticsExpiry: pasado el plazo, un bundle que llega tarde se ignora, la descarga contesta 410
// y borra la fila (borrado perezoso), y después ya no existe (404). Pedir otro diagnóstico purga de
// paso las vencidas de cualquier sesión.
func (w *p9World) diagnosticsExpiry(t *testing.T) {
	db, s := w.esc.DB, w.esc.S
	late := w.requestDiag(t, w.main, w.e, nil, "full")
	p9ExpireDiagnostics(t, db, late.CommandID)
	orphans := map[string]string{"command_id": late.CommandID, "session_id": w.e.SessionID}
	w.e.bundle(t, late.CommandID, "llega tarde")
	p9WaitLogLines(t, s, p9MsgOrphan, orphans, 1)
	if got := p9Scalar(t, db, p9DiagState, late.CommandID); got != "pending|false|<null>" {
		t.Errorf("el bundle tardío cambió la solicitud vencida: %q", got)
	}
	if r := w.call(t, w.main, p9ActDiag, http.MethodGet, p9BundlePath(late.CommandID), nil); !p9ErrorIs(r, http.StatusGone, "diagnóstico expirado") {
		t.Errorf("descargar un diagnóstico vencido: HTTP %d %s, quería 410", r.Codigo, recortar(r.Cuerpo))
	}
	if got := p9Scalar(t, db, p9DiagRow, late.CommandID); got != "0" {
		t.Errorf("tras el 410 quedan %s filas de la solicitud vencida, quería 0", got)
	}
	if r := w.call(t, w.main, p9ActDiag, http.MethodGet, p9BundlePath(late.CommandID), nil); r.Codigo != http.StatusNotFound {
		t.Errorf("descargar de nuevo el vencido: HTTP %d, quería 404", r.Codigo)
	}

	// Un bundle YA recibido también vence: el plazo es de la solicitud, no del contenido.
	ready := w.requestDiag(t, w.main, w.e, nil, "full")
	w.e.bundle(t, ready.CommandID, "listo y luego vencido")
	edgeEsperarValor(t, db, "ready|true|listo y luego vencido", "el bundle recibido", p9DiagState, ready.CommandID)
	p9ExpireDiagnostics(t, db, ready.CommandID)
	// La purga perezosa: la siguiente petición (de otra sesión) se la lleva sin que nadie la descargue.
	w.requestDiag(t, w.main, w.e2, nil, "full")
	if got := p9Scalar(t, db, p9DiagRow, ready.CommandID); got != "0" {
		t.Errorf("pedir otro diagnóstico no purgó la vencida: quedan %s filas", got)
	}
	if r := w.call(t, w.main, p9ActDiag, http.MethodGet, p9BundlePath(ready.CommandID), nil); r.Codigo != http.StatusNotFound {
		t.Errorf("descargar la purgada: HTTP %d, quería 404", r.Codigo)
	}
}

// diagnosticsConsent: el consentimiento es de la empresa y por defecto «sí» (sin fila). Con el
// opt-out, pedir contesta 403, no deja fila y el Edge no recibe nada; descargar lo ya pedido sigue
// valiendo, y la otra empresa no se entera. Al reactivarlo, vuelve.
func (w *p9World) diagnosticsConsent(t *testing.T) {
	db := w.esc.DB
	if got := p9Scalar(t, db, `SELECT count(*)::text FROM public.tenant_diagnostics_consent`); got != "0" {
		t.Fatalf("tenant_diagnostics_consent tiene %s filas antes del opt-out, quería 0 (el consentimiento por defecto no escribe)", got)
	}
	before := p9Scalar(t, db, p9DiagByTenant, w.esc.Tenant)
	p9SetDiagnosticsConsent(t, db, w.esc.Tenant, false)
	for _, body := range []any{nil, map[string]string{"scope": "logs"}} {
		r := w.call(t, w.main, p9ActDiag, http.MethodPost, p9DiagPath(w.e.SessionID), body)
		if !p9ErrorIs(r, http.StatusForbidden, "el tenant desactivó el diagnóstico remoto (opt-out)") {
			t.Errorf("pedir el diagnóstico con el opt-out: HTTP %d %s, quería 403", r.Codigo, recortar(r.Cuerpo))
		}
	}
	if got := p9Scalar(t, db, p9DiagByTenant, w.esc.Tenant); got != before {
		t.Errorf("con el opt-out las solicitudes de la empresa pasaron de %s a %s", before, got)
	}
	// El opt-out va ANTES que la guarda de sesión: una sesión ajena también da 403, no 404.
	if r := w.call(t, w.main, p9ActDiag, http.MethodPost, p9DiagPath(w.eOth.SessionID), nil); r.Codigo != http.StatusForbidden {
		t.Errorf("con el opt-out, pedir el de una sesión ajena: HTTP %d, quería 403", r.Codigo)
	}
	// Lo ya recibido se sigue pudiendo descargar: el consentimiento solo gatea la petición.
	cycle := w.diags[w.e.SessionID][0]
	if r := w.call(t, w.main, p9ActDiag, http.MethodGet, p9BundlePath(cycle), nil); r.Codigo != http.StatusOK {
		t.Errorf("con el opt-out, descargar un bundle ya recibido: HTTP %d, quería 200", r.Codigo)
	}
	// El opt-out es de UNA empresa.
	w.requestDiag(t, w.other, w.eOth, nil, "full")

	p9SetDiagnosticsConsent(t, db, w.esc.Tenant, true)
	// requestDiag exige que el ÚLTIMO DiagnosticsRequest del Edge sea este: si alguno de los 403
	// hubiera llegado al Edge, el cierre del proceso lo vería en la lista.
	w.requestDiag(t, w.main, w.e, nil, "full")
}

// diagnosticsAdversarial es la tabla de casos adversarios del diagnóstico (reglas.md §2): lo que
// hace el servidor viejo con separadores repetidos, dígitos no ASCII y espacios Unicode en los tres
// sitios donde la puerta deja meter un texto.
func (w *p9World) diagnosticsAdversarial(t *testing.T) {
	db := w.esc.DB
	before := p9Scalar(t, db, p9DiagByTenant, w.esc.Tenant)
	sid := w.e.SessionID
	cycle := w.diags[sid][0]

	// 1 · El session_id de la ruta se compara byte a byte: nada «parecido» es la sesión.
	for name, bad := range map[string]string{
		"separador repetido":     "a@@b",
		"sesión con @@":          sid + "@@" + sid,
		"dígitos árabe-índicos":  p9ArabicDigits(sid),
		"solo dígitos no ASCII":  "١٢٣",
		"espacio U+00A0 detrás":  sid + " ",
		"espacio U+2003 delante": " " + sid,
		"mayúsculas":             strings.ToUpper(sid) + "X",
	} {
		if r := w.call(t, w.main, p9ActDiag, http.MethodPost, p9DiagPath(bad), nil); !p9ErrorIs(r, http.StatusNotFound, "sesión no encontrada para el tenant") {
			t.Errorf("session_id adversario (%s) %q: HTTP %d %s, quería 404", name, bad, r.Codigo, recortar(r.Cuerpo))
		}
	}
	// 2 · El command_id de la descarga, igual.
	for name, bad := range map[string]string{
		"separador repetido":    "a@@b",
		"dígitos árabe-índicos": p9ArabicDigits(cycle),
		"espacio U+00A0 detrás": cycle + " ",
		"espacio U+2003 dentro": cycle[:8] + " " + cycle[8:],
		"guiones repetidos":     strings.ReplaceAll(cycle, "-", "--"),
	} {
		if r := w.call(t, w.main, p9ActDiag, http.MethodGet, p9BundlePath(bad), nil); !p9ErrorIs(r, http.StatusNotFound, "diagnóstico no encontrado") {
			t.Errorf("command_id adversario (%s) %q: HTTP %d %s, quería 404", name, bad, r.Codigo, recortar(r.Cuerpo))
		}
	}
	if got := p9Scalar(t, db, p9DiagByTenant, w.esc.Tenant); got != before {
		t.Errorf("los identificadores adversarios cambiaron las solicitudes de la empresa: de %s a %s", before, got)
	}

	// 3 · El scope NO se valida: el servidor le quita los espacios de los extremos (los Unicode
	// también: strings.TrimSpace) y lo reenvía tal cual al Edge, que ignora el que no conoce. Un
	// scope que queda vacío vale «full».
	for _, c := range []struct{ name, scope, want string }{
		{"separador repetido", "a@@b", "a@@b"},
		{"dígitos árabe-índicos", "١٢٣", "١٢٣"},
		{"espacios Unicode en los extremos", " logs ", "logs"},
		{"espacio Unicode dentro", "lo gs", "lo gs"},
		{"solo espacios Unicode", "  ", "full"},
	} {
		acc := w.requestDiag(t, w.main, w.e, map[string]string{"scope": c.scope}, c.want)
		if got := p9Scalar(t, db, p9DiagState, acc.CommandID); got != "pending|false|<null>" {
			t.Errorf("scope adversario (%s): la solicitud está %q, quería pendiente", c.name, got)
		}
	}
	// Un cuerpo que no es JSON: 400, y ya pasó el consentimiento y la guarda de sesión.
	if r := w.call(t, w.main, p9ActDiag, http.MethodPost, p9DiagPath(sid), "no soy un objeto"); r.Codigo != http.StatusBadRequest {
		t.Errorf("pedir el diagnóstico con un cuerpo que no es un objeto: HTTP %d, quería 400", r.Codigo)
	}
}
