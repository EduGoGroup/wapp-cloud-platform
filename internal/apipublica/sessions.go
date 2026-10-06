// Porta internal/publicapi/sessions.go @ 115a4ba (listSessionsHandler, sessionDTO, isoFormat) y
// el registro de D2, D3 y D4 (internal/publicapi/publicapi.go @ 115a4ba, líneas 498-550).
//
// sessions.go — LAS SESIONES (TELÉFONOS VINCULADOS) DEL TENANT (mapa §2.4): D2 las lista, con su
// salud derivada (health.go); D3 y D4 fijan su perfil y su estado con los handlers de
// sessionadmin.go, a los que este fichero pone la cadena W. El puerto SessionLister, que la cara
// vieja compartía entre el envío y este listado, vive en messages.go.
//
// En el rojo solo existían SessionsDeps y MountSessions; el handler de D2 y su fila
// (sessionDTO) nacieron con el verde.

package apipublica

import (
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// SessionsDeps es lo que D2, D3 y D4 necesitan. Cada ruta se monta según SU dependencia (ver
// MountSessions). En la cara vieja era publicapi.SessionDeps más los campos Health, Alerter y
// DBTimeout de publicapi.Deps.
type SessionsDeps struct {
	// Sessions es la flota: lista las sesiones del tenant (D2). nil ⇒ D2 no se monta.
	Sessions SessionLister
	// Health deriva el campo "health" de cada fila de D2. Su valor cero vale (5 min / 2 min).
	Health HealthRules
	// Alerter es el seam del alerting push de D2 (ADR-0023). nil ⇒ NoopAlerter.
	Alerter Alerter
	// DBTimeout es el plazo del listado de D2, cableado desde config.PublicAPIDBTimeout. <= 0 ⇒
	// 1,5 s.
	DBTimeout time.Duration
	// SessionProfiles persiste el perfil de una sesión (D3). nil ⇒ D3 no se monta.
	SessionProfiles SessionProfileStore
	// ProfilePush empuja el perfil recién persistido a la sesión viva (D3). nil NO desmonta D3:
	// es un no-op (el empuje al conectar reconcilia).
	ProfilePush ProfilePusher
	// SessionStatus persiste el estado de una sesión (D4). nil ⇒ D4 no se monta.
	SessionStatus SessionStatusStore
}

// MountSessions registra en c, cada una SOLO si su dependencia no es nil (sin ella la ruta no
// existe: 404 de ruta inexistente):
//
//   - D2 "GET /api/v1/sessions" si d.Sessions != nil — cadena R, permiso "sessions.read", sin
//     auditoría;
//   - D3 "POST /api/v1/sessions/{id}/profile" si d.SessionProfiles != nil — cadena W, permiso
//     "sessions.write", recurso de auditoría "session"; sirve
//     SetSessionProfileHandler(d.SessionProfiles, d.ProfilePush, k.Log). d.ProfilePush nil NO la
//     desmonta;
//   - D4 "POST /api/v1/sessions/{id}/status" si d.SessionStatus != nil — cadena W, permiso
//     "sessions.write", recurso "session"; sirve SetSessionStatusHandler(d.SessionStatus).
//
// Las cadenas son las de Common: 401 sin token, 403 sin el permiso o con un token sin empresa, y
// en D3 y D4 EXACTAMENTE un registro de auditoría por petición que pasa el permiso ("success" o
// "failure" según el código); en D2, ninguno. Lo que D3 y D4 responden pasada la cadena lo dicen
// sus handlers (sessionadmin.go): sus errores son TEXTO PLANO, no el JSON {"error"} de D2.
//
// D2 lista las sesiones del tenant DEL TOKEN (INV-8: el tenant no viaja en la petición, y
// d.Sessions.List recibe ese tenant y ningún otro, así que una sesión ajena NUNCA aparece). Solo
// lectura. Responde 200 con un arreglo JSON —`[]`, nunca `null`, si el tenant no tiene
// sesiones— de una fila por sesión, en el orden de List, con estos campos EN ESTE ORDEN:
//
//	session_id, edge_id, state, self_pn*, last_connected_at*, last_seen_at*, profile, health*,
//	whatsapp_state*, degraded_reason*, degraded_since*, last_health_at*, last_event_age_s*,
//	outbox_depth*, binary_version*, uptime_s*, dek_load_duration_ms*, intent_circuit*,
//	worker_taskset*, intent_p50_ms*, intent_omitted_by_reason*, stuck_heads*, stuck_head_polls*,
//	failed_seal_dispatch*, failed_seal_budget*
//
// Los marcados con * se OMITEN si no se conocen (cadena vacía, instante cero, entero 0, puntero o
// mapa nil). Reglas que el cuerpo cumple:
//
//   - "state" es el del registro del stream CloudLink (online|offline|loggedout), SEPARADO de
//     "whatsapp_state", que es la verdad del socket;
//   - "profile" (active|passive) va SIEMPRE, sin omitirse: la columna es NOT NULL, así que
//     siempre se sabe, y un campo ausente diría «no lo sé». El campo `role` no existe (0064);
//   - los cuatro instantes van en RFC 3339, en UTC ("2006-01-02T15:04:05Z");
//   - "health" es d.Health aplicado a la sesión (ver HealthRules): "degraded", "stale" u omitido;
//   - el bloque del worker (worker_taskset … failed_seal_budget) se copia TAL CUAL: ningún
//     default ni suma. intent_p50_ms, stuck_heads, stuck_head_polls, failed_seal_dispatch y
//     failed_seal_budget son punteros: nil se omite y un 0 MEDIDO viaja como 0 (ausencia ≠ cero);
//     intent_omitted_by_reason viaja clave a clave, sin agregarse en un total;
//   - solo metadatos de operación: CERO credenciales y ninguna PII más allá de self_pn.
//
// Por cada sesión con "health" no vacío D2 llama UNA vez a d.Alerter.Alert(tenant del token,
// session_id, health) —d.Alerter nil ⇒ NoopAlerter—. Es best-effort: un error del Alerter no
// altera la respuesta y deja una línea Debug "alerting de salud falló (best-effort)" con
// "session_id", "estado" y "error". Una sesión sin salud derivada no lo invoca.
//
// PLAZO. List corre acotado por d.DBTimeout (<= 0 ⇒ 1,5 s): es la ruta que la consola consulta
// cada pocos segundos, y una base lenta debe ser un 504 legible, no una pantalla colgada. Vencido
// el plazo ⇒ 504 {"error":"el listado de sesiones no respondió a tiempo, reintenta"} y una línea
// Warn "lectura a BD vencida: se responde 504" con "op"="sessions.list" y "tenant_id"; otro fallo
// de List ⇒ 500 {"error":"no se pudieron listar las sesiones"}, sin esa línea.
//
// k.Log nil ⇒ mismas respuestas, sin líneas. Defensa que los tokens de sharedjwt no alcanzan
// (RequirePermission corta antes): una identidad sin empresa que llegara al handler de D2
// recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil hace panic AL MONTAR (ver Common), se monte alguna ruta o ninguna.
func MountSessions(c *Cara, k Common, d SessionsDeps) {
	panic(pendiente.Implementar("apipublica.MountSessions"))
}
