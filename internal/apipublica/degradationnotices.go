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
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// Techos de la página. El de aquí NO sustituye al del store: éste rechaza lo que el cliente
// PIDE y aquél garantiza lo que el store DEVUELVE, y quien llame al store desde otro sitio se
// sigue beneficiando del segundo.
const (
	// defaultDegradationLimit es la página sin `limit` en la query.
	defaultDegradationLimit = 50
	// maxDegradationLimit es el tope duro. Por encima se RECORTA, no se rechaza: esta lista la
	// pinta una pantalla de avisos, y devolverle un 422 por pedir 500 la dejaría en blanco por
	// un detalle que no le importa. (Contraste deliberado con la telemetría de eventos, que sí
	// devuelve 422 porque su consumidor es un integrador que pagina con cursor y necesita
	// enterarse.)
	maxDegradationLimit = 200
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
	// Sin store o sin resolver de features la ruta NO se monta: un 404 de ruta inexistente es
	// más honesto que una bandeja de avisos que responde 500 —o, peor, que responde 200 con la
	// lista de otro porque el gate no se pudo resolver—.
	if d.DegradationNotices == nil || d.Entitlements == nil {
		return
	}
	mustHaveMW(k, "MountDegradationNotices")

	// 🔴 EL GATE ES `llm_intake`, NO `api_llm`, Y ESO ES LA DECISIÓN DE ESTA FUNCIÓN (ADR-0044,
	// D-044.28, la misma doctrina que T1.5-1). Lo que se lee aquí es «tu captación asistida se
	// degradó al Nivel A», y eso le pasa —y le importa— a CUALQUIER tenant con el nivel, use la
	// vía que use. Gatear con `api_llm` dejaría sin sus propios avisos exactamente a los tenants
	// de la vía LOCAL, que son los dueños de SEIS de los ocho motivos del vocabulario
	// (`ollama_down`, `breaker_open`, `edge_offline`, `timeout` y, desde T1.6-6, `lease_invalid`
	// y `edge_sin_capacidad`): tendrían el Ollama caído y una bandeja que responde 403.
	// `api_llm` gatea la VÍA, no la capacidad, y este endpoint no es de la vía.
	llmIntake := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureLLMIntake)

	// SCOPE `llm.read`, el MISMO que ya usa el GET de /api/v1/tenant-llm, y a propósito: no se
	// estrena clave nueva. Un scope recién inventado sin grant sembrado deja la ruta devolviendo
	// 403 a quien la necesita (es lo que pagó la 0057), y aquí no hace falta correr ese riesgo
	// —«leer la configuración LLM del tenant» y «leer los avisos de esa misma configuración» son
	// la misma faena, y el reparto ya está resuelto: tenant_admin (`*`) y viewer (`*.read`) por
	// glob (0015_iam_roles.sql:65,73), operator fuera por su lista explícita. Cero migración de
	// grants.
	//
	// NO SE AUDITA (protectRead): es una lectura sin efecto. Sí queda en el access-log.
	c.Handle("GET /api/v1/degradation-notices", protectRead(k, "llm.read",
		llmIntake(listDegradationNoticesHandler(d.DegradationNotices, d.DBTimeout))))
}

// degradationNoticeDTO es la proyección al wire de UN aviso.
//
// 🔴 TODOS LOS CAMPOS SON OPACOS O DE VOCABULARIO CERRADO (INV-6). `reason` y `via` son
// vocabulario de wApp, `id` es un UUID, y el resto son instantes y un entero. No hay `message`,
// no hay `detail`, no hay teléfono — y no porque se omitan al serializar, sino porque
// degradation.Notice no los tiene y la tabla no tiene columna para ellos. `tenant_id` NO viaja:
// siempre es el del token, y repetirlo en cada elemento solo daría a alguien la idea de mandarlo
// de vuelta.
//
// `read` va SIN omitempty aunque hoy sea siempre false: quien pinta la pantalla no tiene que
// adivinar si la clave falta porque el aviso está sin leer o porque este servidor todavía no la
// publica. `read_at` sí lleva omitempty, y su ausencia significa algo concreto: nadie lo ha
// leído.
type degradationNoticeDTO struct {
	ID          string `json:"id"`
	Reason      string `json:"reason"`
	Via         string `json:"via"`
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
	Occurrences int    `json:"occurrences"`
	Read        bool   `json:"read"`
	ReadAt      string `json:"read_at,omitempty"`
	CreatedAt   string `json:"created_at"`
	LastSeenAt  string `json:"last_seen_at"`
}

// degradationNoticeListResponse es el contrato de GET /api/v1/degradation-notices.
//
// Devuelve `limit`/`offset` EFECTIVOS —los que se aplicaron, no los que se pidieron— porque el
// handler recorta en silencio: sin eso, un cliente que pida 500 y reciba 200 no tendría forma de
// saber que su siguiente página empieza en 200 y no en 500, y paginaría con agujeros.
//
// ⚠️ NO HAY `total` NI `unread_total`, y es una omisión CONSCIENTE, no un olvido: hoy nada
// escribe `read_at`, así que un contador de no-leídos sería igual al total y daría una cifra que
// no significa lo que su nombre dice. Lo pedirá el Plan 045/047 junto con el endpoint de
// marcar-como-leída, y entonces será una consulta más sobre el índice parcial
// idx_owner_degradation_notices_sin_leer, que ya existe en la 0075 — o sea, un handler, no una
// migración.
type degradationNoticeListResponse struct {
	Notices []degradationNoticeDTO `json:"notices"`
	Limit   int                    `json:"limit"`
	Offset  int                    `json:"offset"`
}

// toDegradationNoticeDTO proyecta un aviso del dominio al wire. La traducción «ReadAt cero = sin
// leer» NO se reinventa aquí: se pregunta a Notice.Leida(), que es donde vive.
func toDegradationNoticeDTO(n degradation.Notice) degradationNoticeDTO {
	dto := degradationNoticeDTO{
		ID:          n.ID,
		Reason:      string(n.Reason),
		Via:         n.Via,
		WindowStart: n.WindowStart.UTC().Format(time.RFC3339),
		WindowEnd:   n.WindowEnd.UTC().Format(time.RFC3339),
		Occurrences: n.Occurrences,
		Read:        n.Leida(),
		CreatedAt:   n.CreatedAt.UTC().Format(time.RFC3339),
		LastSeenAt:  n.LastSeenAt.UTC().Format(time.RFC3339),
	}
	if n.Leida() {
		dto.ReadAt = n.ReadAt.UTC().Format(time.RFC3339)
	}
	return dto
}

// listDegradationNoticesHandler sirve GET /api/v1/degradation-notices: los avisos de degradación
// del tenant del token (INV-7 / INV-8), el más reciente primero.
//
// 🔴 UNA LISTA VACÍA ES LA RESPUESTA SANA: significa que el LLM no se ha degradado, que es lo
// que se espera. No es un 404 ni un error.
//
// El plazo vencido de la lectura NO se traduce a 504 (dbTimedOut504), al revés que en las
// lecturas de edge: la cara vieja respondía el 500 de siempre y la nueva lo conserva.
//
// La rama vieja «lister == nil ⇒ 500 store de avisos de degradación no configurado» no se
// porta: la ruta solo se monta con un lector no nil (MountDegradationNotices), así que era
// inalcanzable.
func listDegradationNoticesHandler(lister DegradationNoticeLister, dbTimeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		filter := parseDegradationFilter(r)

		ctx, cancel := dbCtx(r.Context(), dbTimeout)
		defer cancel()
		notices, err := lister.List(ctx, id.TenantID, filter)
		if err != nil {
			// El mensaje NO repite el error del puerto: el del driver puede llevar el DSN.
			writeError(w, http.StatusInternalServerError, "no se pudieron leer los avisos de degradación")
			return
		}
		writeJSON(w, http.StatusOK, degradationNoticeListResponse{
			Notices: toDegradationNoticeDTOs(notices),
			Limit:   filter.Limit,
			Offset:  filter.Offset,
		})
	})
}

// toDegradationNoticeDTOs proyecta la página entera. Devuelve una lista NO-nil aunque esté
// vacía: `[]` y no `null`, que es lo que una pantalla puede recorrer sin ramas.
func toDegradationNoticeDTOs(notices []degradation.Notice) []degradationNoticeDTO {
	out := make([]degradationNoticeDTO, 0, len(notices))
	for _, n := range notices {
		out = append(out, toDegradationNoticeDTO(n))
	}
	return out
}

// parseDegradationFilter lee la query. NO devuelve error: los tres parámetros tienen default
// sano y un valor ilegible cae al default en vez de romper la pantalla. Es el mismo criterio que
// listAuditHandler (parseIntQuery), y la diferencia con la telemetría de eventos está razonada
// en el comentario de maxDegradationLimit.
//
// 🔴 EL TENANT NO SE LEE DE LA QUERY, y no hay parámetro que lo permita: sale de la Identity en
// el handler (INV-7 / INV-8).
func parseDegradationFilter(r *http.Request) degradation.ListFilter {
	limit := parseIntQuery(r, "limit", defaultDegradationLimit)
	if limit <= 0 {
		limit = defaultDegradationLimit
	}
	if limit > maxDegradationLimit {
		limit = maxDegradationLimit
	}
	// La cara vieja llevaba aquí un `if offset < 0 { offset = 0 }` que no se porta:
	// parseIntQuery ya devuelve el defecto (0) ante cualquier negativo, así que era inalcanzable.
	offset := parseIntQuery(r, "offset", 0)
	return degradation.ListFilter{
		// Solo el literal "true" enciende el filtro. Un `unread=false` o un
		// `unread=cualquier-cosa` NO filtran: el valor por defecto de esta lista es «enséñamelo
		// todo», y un typo del cliente no debe esconderle avisos.
		SoloSinLeer: r.URL.Query().Get("unread") == "true",
		Limit:       limit,
		Offset:      offset,
	}
}
