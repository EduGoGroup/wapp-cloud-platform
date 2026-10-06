// Porta internal/publicapi/diagnostics.go @ 115a4ba (DiagnosticsRequester, DiagnosticsStore,
// preflightDiagnostics, resolveScope, requestDiagnosticsHandler, getDiagnosticsHandler y sus
// textos) y el registro de D5 y D6 (internal/publicapi/publicapi.go @ 115a4ba, líneas 523-541).
//
// diagnostics.go — EL DIAGNÓSTICO REMOTO BAJO DEMANDA (Plan 031 · T5, ADR-0023 capa 3; mapa
// §2.4): D5 pide a una sesión del Edge su bundle de diagnóstico y D6 lo descarga. El bundle es
// material OPERATIVO saneado en origen por el Edge: aquí viaja opaco, sin llaves ni credenciales.
//
// En el rojo solo existían los puertos, DiagnosticsDeps y MountDiagnostics; los dos handlers y
// sus auxiliares nacieron con el verde. Reutiliza de messages.go el puerto SessionLister, la
// guarda sessionBelongsToTenant y la traducción writeSendError, y de deadlines.go los plazos.

package apipublica

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// DiagnosticsRequester emite un DiagnosticsRequest por el stream CloudLink a una sesión. Lo
// satisface *grpc.Server del módulo edge NUEVO (internal/modulos/edge/grpc). El command_id lo
// genera y persiste D5 ANTES de llamar (correlación sin carrera), y la llamada NO espera el
// bundle. De su error D5 espera lo mismo que D1 del de SendText (ver MessageSender).
type DiagnosticsRequester interface {
	RequestDiagnostics(ctx context.Context, sessionID, commandID, scope string) error
}

// DiagnosticsStore persiste las solicitudes y sus bundles, y resuelve el consentimiento por
// tenant. Lo satisfacen *diagnostics.Postgres del módulo edge NUEVO y, en los tests,
// *diagnosticshelpertest.Memoria. Toda operación va acotada al tenant del token (INV-8). Es el
// subconjunto de diagnostics.Store que la cara consume (la recepción del bundle es del gateway).
type DiagnosticsStore interface {
	// ConsentEnabled: default ON (opt-out); false solo si el tenant lo desactivó.
	ConsentEnabled(ctx context.Context, tenantID string) (bool, error)
	// CreateRequest registra la solicitud pendiente que el bundle correlacionará.
	CreateRequest(ctx context.Context, tenantID, sessionID, commandID, requestedBy string, expiresAt time.Time) error
	// DeleteRequest borra la solicitud del tenant: el rollback de D5.
	DeleteRequest(ctx context.Context, tenantID, commandID string) error
	// GetBundle devuelve el bundle listo, o diagnostics.ErrNotFound, ErrExpired o ErrPending.
	GetBundle(ctx context.Context, tenantID, commandID string) (diagnostics.Record, error)
}

// DiagnosticsDeps es lo que D5 y D6 necesitan. En la cara vieja era publicapi.DiagDeps más los
// campos Sessions y DBTimeout de publicapi.Deps.
type DiagnosticsDeps struct {
	// Diagnostics es el almacén de solicitudes y bundles. nil ⇒ no se monta ninguna de las dos.
	Diagnostics DiagnosticsStore
	// DiagnosticsRequester es el gateway CloudLink. nil ⇒ no se monta ninguna de las dos.
	DiagnosticsRequester DiagnosticsRequester
	// Sessions es la flota, para la guarda de aislamiento de D5. nil ⇒ no se monta ninguna.
	Sessions SessionLister
	// BundleTTL (DiagnosticsBundleTTL en la cara vieja) es la retención de la solicitud y de su
	// bundle, cableada desde config. <= 0 ⇒ 30 min.
	BundleTTL time.Duration
	// DBTimeout es el plazo de CADA lectura a BD (el consentimiento y la guarda de D5, el bundle
	// de D6), cableado desde config.PublicAPIDBTimeout. <= 0 ⇒ 1,5 s.
	DBTimeout time.Duration
}

// MountDiagnostics registra en c las DOS rutas o NINGUNA: solo si d.Diagnostics,
// d.DiagnosticsRequester y d.Sessions son los tres distintos de nil (si falta cualquiera, ninguna
// existe: 404 de ruta inexistente). Las dos llevan cadena W y el permiso "diagnostics.request":
//
//   - D5 "POST /api/v1/sessions/{id}/diagnostics", recurso de auditoría "session";
//   - D6 "GET /api/v1/diagnostics/{command_id}", recurso "diagnostics". Es una LECTURA auditada
//     a propósito: descargar un bundle es sensible.
//
// La cadena es la de Common: 401 sin token, 403 sin el permiso o con un token sin empresa, y
// EXACTAMENTE un registro de auditoría por petición que pasa el permiso ("success" o "failure"
// según el código), también en D6.
//
// D5 pide el diagnóstico de la sesión {id} del tenant DEL TOKEN (INV-8: el tenant no viaja en
// la petición). El cuerpo es OPCIONAL: {"scope"}; vacío, sin scope o con uno en blanco ⇒ "full"
// (el scope viaja recortado). En orden:
//
//   - CONSENTIMIENTO: d.Diagnostics.ConsentEnabled(tenant), acotado por d.DBTimeout. Vencido el
//     plazo ⇒ 504 {"error":"la verificación del consentimiento no respondió a tiempo,
//     reintenta"} y una línea Warn "lectura a BD vencida: se responde 504" con
//     "op"="diagnostics.consent", "tenant_id" y "session_id"; otro fallo ⇒ 500 {"error":"no se
//     pudo verificar el consentimiento"} (un fallo NO abre la capacidad); opt-out ⇒ 403
//     {"error":"el tenant desactivó el diagnóstico remoto (opt-out)"};
//   - GUARDA DE TENANT: d.Sessions.List(tenant), acotada por d.DBTimeout. Vencida ⇒ 504
//     {"error":"la verificación de la sesión no respondió a tiempo, reintenta"} y la misma línea
//     Warn con "op"="diagnostics.guarda_tenant"; otro fallo ⇒ 500 {"error":"no se pudo verificar
//     la sesión"}; la sesión no es del tenant ⇒ 404 {"error":"sesión no encontrada para el
//     tenant"} (404 y no 403: no se revela si existe en OTRO tenant);
//   - cuerpo que no es JSON ⇒ 400 {"error":"cuerpo JSON inválido"}. Va DESPUÉS de las dos
//     consultas, como en la cara vieja;
//   - genera el command_id (diagnostics.NewCommandID) y registra la solicitud con
//     CreateRequest(tenant, {id}, command_id, subject del token, ahora + d.BundleTTL —<= 0 ⇒
//     30 min—). Si falla ⇒ 500 {"error":"no se pudo registrar la solicitud"} y no se emite nada;
//   - d.DiagnosticsRequester.RequestDiagnostics({id}, command_id, scope). Si falla, ROLLBACK:
//     DeleteRequest(tenant, command_id), para no dejar una solicitud que nunca recibirá bundle
//     —si el rollback falla, una línea Warn "diagnóstico: rollback de solicitud tras push
//     fallido falló" con "tenant_id", "command_id" y "error", sin cambiar la respuesta—, y el
//     error se traduce como el de un envío (ver MountMessages: sesión offline ⇒ 502, los 504 del
//     empuje, resto ⇒ 500 "no se pudo enviar el texto", con su línea Error);
//   - 202 {"command_id","session_id","status":"pending","expires_at"} —expires_at en RFC 3339
//     UTC, el mismo instante que recibió CreateRequest— y una línea Info "diagnóstico remoto
//     solicitado" con "tenant_id", "subject", "session_id", "command_id" y "scope". En el camino
//     feliz NO hay DeleteRequest.
//
// Ninguno de los desenlaces anteriores al empuje emite nada al Edge, así que sus 504 sí dicen
// «reintenta». CreateRequest, RequestDiagnostics y DeleteRequest reciben el contexto de la
// petición SIN plazo propio (d.DBTimeout acota solo las lecturas).
//
// D6 descarga el bundle {command_id} del tenant DEL TOKEN: GetBundle(tenant, command_id),
// acotado por d.DBTimeout. El plazo vencido se mira ANTES que los centinelas del store:
//
//   - plazo vencido ⇒ 504 {"error":"la lectura del diagnóstico no respondió a tiempo, reintenta
//     la descarga"} y la línea Warn con "op"="diagnostics.bundle", "tenant_id" y "command_id";
//   - diagnostics.ErrNotFound ⇒ 404 {"error":"diagnóstico no encontrado"}: no existe, o es de
//     OTRO tenant (404 opaco, INV-8);
//   - diagnostics.ErrExpired ⇒ 410 {"error":"diagnóstico expirado"};
//   - diagnostics.ErrPending ⇒ 202 {"command_id","message":"el Edge aún no respondió; reintentar
//     la descarga","status":"pending"};
//   - otro fallo ⇒ 500 {"error":"no se pudo leer el diagnóstico"};
//   - listo ⇒ 200 {"command_id","session_id","requested_by","requested_at","received_at",
//     "log_tail","goroutine_dump","subsystems_json"}, en ese orden, sin omitir ninguno, con los
//     dos instantes en RFC 3339 UTC, y una línea Info "diagnóstico remoto descargado" con
//     "tenant_id", "subject", "session_id" y "command_id".
//
// CERO PII y cero contenido del bundle en los logs. k.Log nil ⇒ mismas respuestas, sin líneas.
//
// Defensas que el mux y los tokens de sharedjwt no dejan alcanzar: una identidad sin empresa
// recibiría 401 {"error":"autenticación requerida"}; un {id} vacío, 400 {"error":"session id
// requerido en la ruta"}; un {command_id} vacío, 400 {"error":"command_id requerido en la ruta"}.
//
// Fallo de cableado: k.MW nil hace panic AL MONTAR (ver Common), se monten las rutas o no.
func MountDiagnostics(c *Cara, k Common, d DiagnosticsDeps) {
	panic(pendiente.Implementar("apipublica.MountDiagnostics"))
}
