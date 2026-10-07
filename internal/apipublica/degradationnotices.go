// Porta internal/publicapi/degradationnotices.go @ 3c74b80 y el registro de F4
// (internal/publicapi/publicapi.go @ 3c74b80, registerDegradationNotices, líneas 994-1032).
//
// degradationnotices.go — LA LECTURA DE LOS AVISOS DE DEGRADACIÓN DEL DUEÑO (Plan 044 · Ola 1.5
// · T1.5-4, D-044.32, REQ-38; mapa §2.6, F4): GET /api/v1/degradation-notices. El dominio vive
// en internal/modulos/inferencia/degradation; aquí solo se abre la puerta HTTP. El contrato de
// la respuesta está escrito en design.md §8.2.
//
// 🔴 SOLO HAY GET, Y ES DELIBERADO. No hay POST (nadie crea un aviso desde fuera: los escribe el
// pipeline cuando el adaptador falla) ni hay PATCH de marcar-como-leída (la columna `read_at`
// existe en la 0075, pero el endpoint lo pide el Plan 045/047 y construirlo aquí sería adivinar
// su contrato). Un endpoint de escritura sin consumidor es una superficie de ataque sin usuario.
//
// En el rojo solo existían el puerto, DegradationNoticesDeps y MountDegradationNotices; el
// handler y sus auxiliares nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// DegradationNoticeLister es el puerto de LECTURA de los avisos de degradación que consume la
// cara. Lo satisfacen *degradation.Postgres del módulo inferencia NUEVO y, en los tests,
// *degradationhelpertest.Memoria.
//
// 🔴 ES UN SUBCONJUNTO ESTRICTO de degradation.Store, y le falta EXACTAMENTE un método: Save. La
// ausencia es el mecanismo, el mismo criterio que mantiene la API key fuera de TenantLLMStore:
// esta capa LEE, y un puerto que además escribiera dejaría abierta la posibilidad de que un
// handler futuro creara un aviso a petición de un cliente — que es precisamente lo que
// convertiría el canal en un buzón de spam.
//
// El tenant es un ARGUMENTO que sale del token, nunca del filtro (INV-7 / INV-8): no hay forma
// de pedirle los avisos de otro.
type DegradationNoticeLister interface {
	// List devuelve los avisos de tenantID, el más reciente primero, acotados por f.
	List(ctx context.Context, tenantID string, f degradation.ListFilter) ([]degradation.Notice, error)
}

// DegradationNoticesDeps es lo que F4 necesita. En la cara vieja eran los campos
// DegradationNotices, Entitlements y DBTimeout de publicapi.Deps.
type DegradationNoticesDeps struct {
	// DegradationNotices lee los avisos. nil ⇒ F4 no se monta.
	DegradationNotices DegradationNoticeLister
	// Entitlements es el resolver de derechos del módulo acceso NUEVO, el MISMO (una sola caché)
	// que gatea el resto de la plataforma. nil ⇒ F4 no se monta.
	Entitlements entitlements.Resolver
	// DBTimeout es el plazo de la lectura a BD, cableado desde config.PublicAPIDBTimeout.
	// <= 0 ⇒ 1,5 s.
	DBTimeout time.Duration
}

// MountDegradationNotices registra en c "GET /api/v1/degradation-notices" (F4), solo si
// d.DegradationNotices y d.Entitlements son los dos distintos de nil. Si falta cualquiera la
// ruta no existe (404 de ruta inexistente): es más honesto que una bandeja de avisos que
// responde 500.
//
// Cadena R con permiso "llm.read" —el MISMO del GET de /api/v1/tenant-llm, a propósito: no se
// estrena clave— (ver Common: 401 sin token; 403 {"error":"permiso denegado"} sin el permiso o
// con un token sin empresa; NINGÚN registro de auditoría, tampoco en el camino feliz) y, POR
// DENTRO de ella, el gate de la feature "llm_intake" (entitlements.RequireFeature). El orden es
// Authenticate → RequirePermission → gate → handler:
//
//   - sin el permiso Y sin la feature ⇒ el 403 es el del permiso, no el del gate;
//   - con el permiso y sin la feature ⇒ 403 con EXACTAMENTE el cuerpo
//     {"error":"feature_not_enabled","feature":"llm_intake"}; el puerto no se consulta;
//   - 🔴 EL GATE ES "llm_intake", NO "api_llm" (ADR-0044, D-044.28): lo que se lee es «tu
//     captación asistida se degradó», y eso le pasa a CUALQUIER tenant con el nivel, use la vía
//     que use. Un tenant con "llm_intake" y SIN "api_llm" —el de la vía local, dueño de seis de
//     los ocho motivos— lee sus avisos; uno con "api_llm" y sin "llm_intake" recibe el 403;
//   - el gate es fail-closed: un resolver que falla corta con ese MISMO 403, nunca con 500.
//
// La petición:
//
//   - List recibe SIEMPRE el tenant del token (INV-7 / INV-8): no hay parámetro que lo cambie y
//     un tenant en la query no cuenta;
//   - "limit": entero; si falta, no es un entero no negativo en dígitos ASCII, o es 0 ⇒ 50; por
//     encima de 200 se RECORTA a 200 (no se rechaza: esta lista la pinta una pantalla);
//   - "offset": entero no negativo; si falta o es inválido ⇒ 0;
//   - "unread": SOLO el literal "true" pide los no leídos (ListFilter.SoloSinLeer); "false",
//     "TRUE", "1" o cualquier otra cosa NO filtran: un typo no debe esconder avisos;
//   - ningún valor de la query es un error: lo ilegible cae a su defecto;
//   - la lectura va acotada por d.DBTimeout (<= 0 ⇒ 1,5 s).
//
// La respuesta:
//
//   - 200 {"notices":[…],"limit":L,"offset":O}, con el limit y el offset EFECTIVOS —los que se
//     aplicaron, no los que se pidieron—, los avisos en el orden que da el puerto, y
//     "notices":[] (nunca null) cuando no hay ninguno, que es la respuesta SANA: el LLM no se ha
//     degradado. No hay "total" ni "unread_total";
//   - cada aviso: {"id","reason","via","window_start","window_end","occurrences","read",
//     "read_at","created_at","last_seen_at"}, en ese orden. "read" sale SIEMPRE (false si
//     Notice.Leida() es false) y "read_at" SOLO si está leído; los instantes van en UTC,
//     RFC 3339 con segundos. El tenant NO viaja. Todo es opaco o de vocabulario cerrado
//     (INV-6): no hay mensaje, ni detalle, ni teléfono;
//   - un fallo de List —el plazo vencido incluido— ⇒ 500 {"error":"no se pudieron leer los
//     avisos de degradación"}, que NO repite el error del puerto (el del driver puede llevar el
//     DSN).
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil con las dos dependencias presentes hace panic AL MONTAR (ver
// Common).
func MountDegradationNotices(c *Cara, k Common, d DegradationNoticesDeps) {
	panic(pendiente.Implementar("apipublica.MountDegradationNotices"))
}
