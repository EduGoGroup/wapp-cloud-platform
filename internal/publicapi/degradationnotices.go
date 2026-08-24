package publicapi

import (
	"context"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// degradationnotices.go es la LECTURA de los avisos de degradación del dueño
// (Plan 044 · Ola 1.5 · T1.5-4, D-044.32, REQ-38): GET /api/v1/degradation-notices.
// El dominio vive en internal/degradation; aquí solo se abre la puerta HTTP.
//
// 🔴 SOLO HAY GET, Y ES DELIBERADO. No hay POST (nadie crea un aviso desde fuera:
// los escribe el pipeline cuando el adaptador falla, y ese cableado es de T1.6-6
// y de la O2) ni hay PATCH de marcar-como-leída (la columna `read_at` existe en la
// 0075, pero el endpoint lo pide el Plan 045/047 y construirlo aquí sería adivinar
// su contrato). Un endpoint de escritura sin consumidor es una superficie de
// ataque sin usuario.
//
// El contrato de la respuesta está escrito en design.md §8.2, que es donde el
// plan de la app lo va a buscar.

// Techos de la página. El de aquí NO sustituye al del store (degradation.acotar):
// éste rechaza lo que el cliente PIDE y aquél garantiza lo que el store DEVUELVE,
// y quien llame al store desde otro sitio se sigue beneficiando del segundo.
const (
	// defaultDegradationLimit es la página sin `limit` en la query.
	defaultDegradationLimit = 50
	// maxDegradationLimit es el tope duro. Por encima se RECORTA, no se rechaza:
	// esta lista la pinta una pantalla de avisos, y devolverle un 422 por pedir
	// 500 la dejaría en blanco por un detalle que no le importa. (Contraste
	// deliberado con eventstelemetry.go:131, que sí devuelve 422 porque su
	// consumidor es un integrador que pagina con cursor y necesita enterarse.)
	maxDegradationLimit = 200
)

// DegradationNoticeLister es el puerto de LECTURA de los avisos de degradación
// que consume la API pública. Lo satisface *degradation.Postgres.
//
// 🔴 ES UN SUBCONJUNTO ESTRICTO de degradation.Store, y le falta EXACTAMENTE un
// método: Save. La ausencia es el mecanismo, el mismo criterio que mantiene la
// API key fuera de TenantLLMStore: esta capa LEE, y un puerto que además
// escribiera dejaría abierta la posibilidad de que un handler futuro creara un
// aviso a petición de un cliente — que es precisamente lo que convertiría el canal
// en un buzón de spam.
//
// El tenant es un ARGUMENTO que sale del token, nunca del filtro (INV-7 / INV-8):
// no hay forma de pedirle los avisos de otro.
type DegradationNoticeLister interface {
	List(ctx context.Context, tenantID string, f degradation.ListFilter) ([]degradation.Notice, error)
}

// degradationNoticeDTO es la proyección al wire de UN aviso.
//
// 🔴 TODOS LOS CAMPOS SON OPACOS O DE VOCABULARIO CERRADO (INV-6). `reason` y
// `via` son vocabulario de wApp, `id` es un UUID, y el resto son instantes y un
// entero. No hay `message`, no hay `detail`, no hay teléfono — y no porque se
// omitan al serializar, sino porque degradation.Notice no los tiene y la tabla no
// tiene columna para ellos. `tenant_id` NO viaja: siempre es el del token, y
// repetirlo en cada elemento solo daría a alguien la idea de mandarlo de vuelta.
//
// `read` va SIN omitempty aunque hoy sea siempre false: quien pinta la pantalla
// no tiene que adivinar si la clave falta porque el aviso está sin leer o porque
// este servidor todavía no la publica. `read_at` sí lleva omitempty, y su
// ausencia significa algo concreto: nadie lo ha leído.
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
// Devuelve `limit`/`offset` EFECTIVOS —los que se aplicaron, no los que se
// pidieron— porque el handler recorta en silencio: sin eso, un cliente que pida
// 500 y reciba 200 no tendría forma de saber que su siguiente página empieza en
// 200 y no en 500, y paginaría con agujeros.
//
// ⚠️ NO HAY `total` NI `unread_total`, y es una omisión CONSCIENTE, no un olvido:
// hoy nada escribe `read_at`, así que un contador de no-leídos sería igual al
// total y daría una cifra que no significa lo que su nombre dice. Lo pedirá el
// Plan 045/047 junto con el endpoint de marcar-como-leída, y entonces será una
// consulta más sobre el índice parcial idx_owner_degradation_notices_sin_leer, que
// ya existe en la 0075 — o sea, un handler, no una migración.
type degradationNoticeListResponse struct {
	Notices []degradationNoticeDTO `json:"notices"`
	Limit   int                    `json:"limit"`
	Offset  int                    `json:"offset"`
}

// toDegradationNoticeDTO proyecta un aviso del dominio al wire. La traducción
// «ReadAt cero = sin leer» NO se reinventa aquí: se pregunta a Notice.Leida(),
// que es donde vive.
func toDegradationNoticeDTO(n degradation.Notice) degradationNoticeDTO {
	dto := degradationNoticeDTO{
		ID:          n.ID,
		Reason:      string(n.Reason),
		Via:         n.Via,
		WindowStart: n.WindowStart.UTC().Format(rfc3339),
		WindowEnd:   n.WindowEnd.UTC().Format(rfc3339),
		Occurrences: n.Occurrences,
		Read:        n.Leida(),
		CreatedAt:   n.CreatedAt.UTC().Format(rfc3339),
		LastSeenAt:  n.LastSeenAt.UTC().Format(rfc3339),
	}
	if n.Leida() {
		dto.ReadAt = n.ReadAt.UTC().Format(rfc3339)
	}
	return dto
}

// listDegradationNoticesHandler sirve GET /api/v1/degradation-notices: los avisos
// de degradación del tenant del token (INV-7 / INV-8), el más reciente primero.
//
// Query: `limit` (default 50, tope 200, se recorta), `offset` (default 0) y
// `unread=true` (solo los no leídos — la pregunta que hace el teléfono).
//
// Respuestas: 200 con la página (lista `[]` y no `null` cuando no hay nada, que es
// el caso NORMAL en la Ola 1.5: nadie puebla la tabla todavía); 401 sin identidad;
// 403 sin la feature (lo pone el middleware); 500 si el store falla.
//
// 🔴 UNA LISTA VACÍA ES LA RESPUESTA SANA, y conviene decirlo aquí porque quien
// pruebe este endpoint en esta ola no va a ver ni una fila: significa que el LLM
// no se ha degradado, que es lo que se espera. No es un 404 ni un error.
func listDegradationNoticesHandler(lister DegradationNoticeLister, dbTimeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if lister == nil {
			writeError(w, http.StatusInternalServerError, "store de avisos de degradación no configurado")
			return
		}
		filtro := parseDegradationFilter(r)

		ctx, cancel := dbCtx(r.Context(), dbTimeout)
		defer cancel()
		avisos, err := lister.List(ctx, id.TenantID, filtro)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudieron leer los avisos de degradación")
			return
		}
		writeJSON(w, http.StatusOK, degradationNoticeListResponse{
			Notices: toDegradationNoticeDTOs(avisos),
			Limit:   filtro.Limit,
			Offset:  filtro.Offset,
		})
	})
}

// toDegradationNoticeDTOs proyecta la página entera. Devuelve una lista NO-nil
// aunque esté vacía: `[]` y no `null`, que es lo que una pantalla puede recorrer
// sin ramas.
func toDegradationNoticeDTOs(avisos []degradation.Notice) []degradationNoticeDTO {
	out := make([]degradationNoticeDTO, 0, len(avisos))
	for _, n := range avisos {
		out = append(out, toDegradationNoticeDTO(n))
	}
	return out
}

// parseDegradationFilter lee la query. NO devuelve error: los tres parámetros
// tienen default sano y un valor ilegible cae al default en vez de romper la
// pantalla. Es el mismo criterio que listAuditHandler (parseIntQuery) y el
// contrario que parseEventTelemetryFilter, y la diferencia está razonada en el
// comentario de maxDegradationLimit.
//
// 🔴 EL TENANT NO SE LEE DE LA QUERY, y no hay parámetro que lo permita: sale de
// la Identity en el handler (INV-7 / INV-8).
func parseDegradationFilter(r *http.Request) degradation.ListFilter {
	limit := parseIntQuery(r, "limit", defaultDegradationLimit)
	if limit <= 0 {
		limit = defaultDegradationLimit
	}
	if limit > maxDegradationLimit {
		limit = maxDegradationLimit
	}
	offset := parseIntQuery(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	return degradation.ListFilter{
		// Solo el literal "true" enciende el filtro. Un `unread=false` o un
		// `unread=cualquier-cosa` NO filtran: el valor por defecto de esta lista es
		// «enséñamelo todo», y un typo del cliente no debe esconderle avisos.
		SoloSinLeer: r.URL.Query().Get("unread") == "true",
		Limit:       limit,
		Offset:      offset,
	}
}
