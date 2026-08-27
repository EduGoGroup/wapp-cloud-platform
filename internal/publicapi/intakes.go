package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// IntakeService es el puerto de SOLICITUDES que consume la API pública (Plan 041 ·
// T1.1/T1.4, ADR-0031). Lo satisface *intakes.Service. Toda operación va acotada al
// tenant, que sale de la Identity del token (INV-8) y NUNCA de la URL o el cuerpo.
type IntakeService interface {
	// List devuelve la página de solicitudes del tenant que casan con el filtro.
	List(ctx context.Context, tenantID string, f intakes.Filter) (intakes.Page, error)
	// Get devuelve la solicitud con sus líneas; intakes.ErrNotFound si no es del
	// tenant (404 opaco).
	Get(ctx context.Context, tenantID, intakeID string) (intakes.Detail, error)
	// SetStatus aplica una transición del ciclo de vida (D-041.10).
	SetStatus(ctx context.Context, tenantID, intakeID, status string) (intakes.Intake, error)
	// Discard DESCARTA a mano un lote de solicitudes huérfanas dejándolas en
	// `abandoned` (T4.8, D-041.18). Contesta por ítem: no es todo-o-nada.
	Discard(ctx context.Context, tenantID string, intakeIDs []string) (intakes.DiscardResult, error)
	// ReplaceItems sustituye las líneas de cliente de una solicitud en
	// `pending_approval` y deja la revisión `corrected` del dueño (T4.10, REQ-36).
	ReplaceItems(ctx context.Context, tenantID, intakeID string, items []intakes.Item) (intakes.Detail, error)
	// ListDetails devuelve las solicitudes del filtro CON sus líneas y sin
	// paginar: es lo que desnormaliza el export (T1.2). intakes.ErrTooLarge si el
	// filtro abarca más de intakes.MaxExportIntakes.
	ListDetails(ctx context.Context, tenantID string, f intakes.Filter) ([]intakes.Detail, error)
	// Summary agrega esas mismas solicitudes para summary.json (T1.3).
	Summary(ctx context.Context, tenantID string, f intakes.Filter) (intakes.Summary, error)
}

// intakeDTO es la proyección al wire de una cabecera de solicitud.
//
// contact_id viaja OPACO TAL CUAL está en BD (INV-04 / ADR-0010): es un
// identificador sin número ni JID, y esta capa no lo descifra ni lo enriquece con
// nombre o teléfono. tenant_id NO viaja: siempre es el del token.
// `customer_note` es la indicación del cliente para todo el pedido (D-041.19).
// Viaja SIEMPRE, también vacía, por la misma razón que `customization` en la
// línea: quien consume tiene que poder pintar la cabecera sin preguntarse si la
// clave falta porque el cliente no indicó nada o porque este servidor todavía no
// la publica. Va en el DTO de la cabecera y no solo en el del detalle, así que la
// lista la trae igual: es un campo de la solicitud, y una bandeja que muestra
// «dejarlo en portería» junto al pedido es exactamente para lo que existe.
type intakeDTO struct {
	ID           string  `json:"id"`
	ContactID    string  `json:"contact_id"`
	SessionID    string  `json:"session_id"`
	Status       string  `json:"status"`
	Total        float64 `json:"total"`
	CustomerNote string  `json:"customer_note"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// intakeItemDTO es una línea de la solicitud. sku/label son códigos del catálogo
// del tenant (dato de negocio), NUNCA PII.
//
// `customization` es la personalización no facturable de la línea (D-041.17): el
// «sin cebolla». Viaja SIEMPRE, también vacía (sin `omitempty`), y eso es
// deliberado: quien consume el detalle —la consola del dueño, el puente del CRM—
// tiene que poder pintar la línea sin preguntarse si la clave falta porque no hay
// personalización o porque este servidor todavía no la publica.
type intakeItemDTO struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Customization string  `json:"customization"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
}

// intakeListResponse es el contrato de GET /api/v1/intakes (design §4): la página
// más el TOTAL de coincidencias del filtro, que es lo que la UI necesita para
// pintar el paginador.
type intakeListResponse struct {
	Intakes  []intakeDTO `json:"intakes"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Total    int         `json:"total"`
}

// intakeDetailResponse es el contrato de GET /api/v1/intakes/{id}: la cabecera
// (campos promovidos), sus líneas y los destinos a los que la solicitud puede ir
// desde donde está.
//
// `allowed_transitions` sale de la MISMA fuente que el cuerpo del 422
// (intakes.AllowedTransitions) y en el mismo orden determinista. Sin él, una
// consola que quiera pintar el selector de cambio de estado tendría dos salidas y
// las dos son malas: provocar un 422 para averiguar qué puede hacer, o duplicar el
// mapa de estados en el cliente y desincronizarlo en cuanto la Ola 4 lo amplíe.
//
// Un estado TERMINAL devuelve `[]`, nunca `null`: "no hay acciones" y "no sé" son
// respuestas distintas y la UI pinta cosas distintas con cada una.
//
// `revisions` es la NEGOCIACIÓN auditada (ADR-0031 §3, tabla intake_revisions de la
// migración 0045). Va aparte de `items` porque son cosas distintas: `items` es lo
// VENDIDO y `revisions` el rastro de cómo se llegó a ello. Un `[]` aquí ya no es
// una respuesta fingida como lo habría sido antes de que la tabla existiera:
// significa literalmente "esta solicitud no tiene revisiones registradas".
// `buyer_data_present` (D-041.13, T4.5) es TODO lo que esta API publica del
// checklist del comprador: un booleano. Los valores —RUT, dirección de entrega—
// están cifrados en public.intake_buyer_data y su descifrado está CUSTODIADO: llega
// con su consumidor real (el puente del Plan 042 o una pantalla que lo justifique),
// no por si acaso.
//
// Sirve para lo que una consola necesita de verdad sin exponer a nadie: saber si el
// pedido trae los datos que el dueño pidió antes de ponerse a prepararlo. Quien
// añada aquí el contenido está deshaciendo la tarea entera, no ampliando un DTO.
type intakeDetailResponse struct {
	intakeDTO
	Items              []intakeItemDTO     `json:"items"`
	Revisions          []intakeRevisionDTO `json:"revisions"`
	AllowedTransitions []string            `json:"allowed_transitions"`
	BuyerDataPresent   bool                `json:"buyer_data_present"`
}

// intakeRevisionDTO es una revisión al wire.
//
// `payload` viaja como JSON CRUDO (json.RawMessage), no como un objeto tipado: su
// forma depende de `kind` y del `version` que lleva dentro, y tiparlo aquí
// obligaría a esta capa a conocer todas las formas presentes y futuras — incluida
// la del Plan 044, que aún no existe. El `version` dentro del blob es lo que
// permite a un cliente saber si entiende lo que lee.
//
// `created_by` es un ROL, nunca una persona, y ni el payload ni el texto renderizado
// llevan los datos del COMPRADOR (RUT, dirección: ésos viven cifrados en
// intake_buyer_data y de ahí no salen por esta ruta).
//
// 🔧 LO QUE SÍ PUEDE VIAJAR AQUÍ DESDE T3.5: el TEXTO LITERAL del cliente. Una
// revisión `interpreted` del pipeline LLM trae dentro del payload el `source_text`
// que el cliente escribió y las `evidence` que sostienen cada línea. En reposo van
// CIFRADOS con KEK y fuera de la columna `payload` (migración 0079); el store los
// descifra al leer, así que llegan a este DTO en claro — y ESO ES EL REQUISITO, no
// una fuga: sin el original al lado de la interpretación, el dueño no puede validar
// que el LLM entendió bien ni pedir un re-análisis (D-044.13 / D-14, ADR-0034
// §Decisión 2). Lo que protege esa entrega es la MISMA puerta que todo lo demás de
// esta ruta: identidad válida y tenant propio (INV-8). Vencido el TTL de retención
// del tenant, el literal ya no está —lo podó la lectura anterior— y el payload llega
// con su interpretación estructurada sola, que es el comportamiento correcto.
type intakeRevisionDTO struct {
	RevisionNo   int             `json:"revision_no"`
	Kind         string          `json:"kind"`
	Payload      json.RawMessage `json:"payload"`
	RenderedText string          `json:"rendered_text,omitempty"`
	CreatedBy    string          `json:"created_by,omitempty"`
	CreatedAt    string          `json:"created_at"`
}

// invalidTransitionResponse es el cuerpo del 422 de POST …/status: dónde está la
// solicitud AHORA y adónde sí puede ir. Sin `allowed`, el llamante tendría que
// adivinar el ciclo de vida a base de reintentos.
type invalidTransitionResponse struct {
	Error     string   `json:"error"`
	Status    string   `json:"status"`
	Requested string   `json:"requested"`
	Allowed   []string `json:"allowed"`
}

// setIntakeStatusRequest es el cuerpo de POST /api/v1/intakes/{id}/status.
type setIntakeStatusRequest struct {
	Status string `json:"status"`
}

// listIntakesHandler sirve GET /api/v1/intakes: las solicitudes del tenant del
// token (INV-8), con filtros from/to/status/session, orden `sort` (más recientes
// primero por defecto) y paginación page/page_size (default 50, máx 200).
//
// `status` se REPITE para pedir varios estados a la vez; ver parseIntakeFilter.
func listIntakesHandler(svc IntakeService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		filter, msg := parseIntakeFilter(r)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}

		page, err := svc.List(r.Context(), id.TenantID, filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudieron listar las solicitudes")
			return
		}

		out := make([]intakeDTO, 0, len(page.Intakes))
		for _, in := range page.Intakes {
			out = append(out, toIntakeDTO(in))
		}
		writeJSON(w, http.StatusOK, intakeListResponse{
			Intakes: out, Page: page.Page, PageSize: page.PageSize, Total: page.Total,
		})
	})
}

// getIntakeHandler sirve GET /api/v1/intakes/{id}: cabecera + líneas. Una
// solicitud de OTRO tenant responde 404, no 403: un 403 confirmaría que el id
// existe, y el aislamiento entre tenants no puede filtrar ni eso (INV-8).
//
// Recibe el resolver de features APARTE del middleware que ya gatea la ruta, y no
// es una duplicación: el middleware decide si se ENTRA (`cart_basic`), y esto
// decide qué CAMPOS salen (`llm_intake`, T4.1 / D-044.48 §2). Son dos preguntas
// distintas y la segunda no se puede contestar desde un middleware, que no ve el
// cuerpo.
func getIntakeHandler(svc IntakeService, feats entitlements.Resolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		detail, err := svc.Get(r.Context(), id.TenantID, r.PathValue("id"))
		switch {
		case errors.Is(err, intakes.ErrNotFound):
			writeError(w, http.StatusNotFound, "solicitud no encontrada")
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "no se pudo leer la solicitud")
			return
		}

		writeIntakeDetail(r.Context(), w, feats, id.TenantID, detail)
	})
}

// writeIntakeDetail escribe el detalle YA FILTRADO por el plan del tenant. Es el
// ÚNICO camino por el que el detalle sale al wire —lo usan el GET y el PUT de
// líneas—, por la misma razón por la que toIntakeDetailResponse es un solo punto:
// dos salidas separadas divergirían en el primer campo nuevo, y aquí divergir
// significa que una de las dos filtra y la otra no.
//
// Un fallo al filtrar responde 500 y NO el payload sin filtrar: servir el cuerpo
// entero ante un error dejaría el gate abierto justo cuando algo va mal, que es lo
// contrario de fail-closed. En la práctica no se alcanza —lo que se reserializa
// salió de un json.Unmarshal válido—, y por eso no tiene un cuerpo de error propio.
func writeIntakeDetail(ctx context.Context, w http.ResponseWriter, feats entitlements.Resolver, tenantID string, detail intakes.Detail) {
	resp := toIntakeDetailResponse(detail)
	if err := aplicarGateLLMIntake(ctx, feats, tenantID, &resp); err != nil {
		writeError(w, http.StatusInternalServerError, "no se pudo preparar la solicitud")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// toIntakeDetailResponse proyecta la solicitud completa al wire. Es UN punto y no
// dos porque la edición manual (T4.10) responde EXACTAMENTE el mismo cuerpo que el
// detalle: la consola repinta con lo que le devuelve el PUT, sin un segundo GET, y
// dos proyecciones separadas empezarían a divergir en el primer campo nuevo.
func toIntakeDetailResponse(detail intakes.Detail) intakeDetailResponse {
	items := make([]intakeItemDTO, 0, len(detail.Items))
	for _, it := range detail.Items {
		items = append(items, intakeItemDTO{
			SKU: it.SKU, Label: it.Label, Customization: it.Customization,
			Qty: it.Qty, UnitPrice: it.UnitPrice,
		})
	}
	revisions := make([]intakeRevisionDTO, 0, len(detail.Revisions))
	for _, rev := range detail.Revisions {
		revisions = append(revisions, intakeRevisionDTO{
			RevisionNo:   rev.RevisionNo,
			Kind:         rev.Kind,
			Payload:      rev.Payload,
			RenderedText: rev.RenderedText,
			CreatedBy:    rev.CreatedBy,
			CreatedAt:    rev.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return intakeDetailResponse{
		intakeDTO: toIntakeDTO(detail.Intake),
		Items:     items,
		Revisions: revisions,
		// detail.Status ya viene normalizado del dominio: una solicitud
		// guardada como `closed` ofrece los destinos de `confirmed`.
		AllowedTransitions: intakes.AllowedTransitions(detail.Status),
		BuyerDataPresent:   detail.BuyerDataPresent,
	}
}

// setIntakeStatusHandler sirve POST /api/v1/intakes/{id}/status: aplica una
// transición del ciclo de vida (D-041.10). 200 con la solicitud transicionada;
// 404 si no es del tenant; 422 con el estado actual y los destinos permitidos si
// la transición no es válida; 409 si otro operador se adelantó.
//
// El AVISO AL CLIENTE (D-041.14, T4.2) lo dispara el servicio DESPUÉS de escribir,
// y no puede cambiar lo que devuelve esta ruta: un 200 significa «la transición se
// aplicó», nunca «el cliente recibió el mensaje». Con la sesión del negocio offline
// el dueño sigue viendo su 200 y su bandeja al día; el envío fallido queda en el
// log del servidor con su command_id. Ver intakes.Service.notify.
func setIntakeStatusHandler(svc IntakeService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req setIntakeStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}
		if req.Status == "" {
			writeError(w, http.StatusBadRequest, "status es obligatorio")
			return
		}

		updated, err := svc.SetStatus(r.Context(), id.TenantID, r.PathValue("id"), req.Status)
		var invalid *intakes.TransitionError
		switch {
		case errors.Is(err, intakes.ErrNotFound):
			writeError(w, http.StatusNotFound, "solicitud no encontrada")
		case errors.As(err, &invalid):
			writeJSON(w, http.StatusUnprocessableEntity, invalidTransitionResponse{
				Error:     "invalid_transition",
				Status:    invalid.From,
				Requested: invalid.To,
				Allowed:   invalid.Allowed,
			})
		case errors.Is(err, intakes.ErrConflict):
			writeError(w, http.StatusConflict, "la solicitud cambió de estado; recárgala y reintenta")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "no se pudo cambiar el estado de la solicitud")
		default:
			writeJSON(w, http.StatusOK, toIntakeDTO(updated))
		}
	})
}

// discardIntakesRequest es el cuerpo de POST /api/v1/intakes/discard: los ids que
// el dueño quiere sacar de su bandeja.
//
// Es una lista EXPLÍCITA de ids y no un filtro, y eso es la decisión de fondo de
// esta puerta: el descarte es irreversible y no hay papelera (D-041.22), así que
// quien descarta tiene que NOMBRAR lo que descarta. Un filtro dejaría el conjunto
// afectado a merced de lo que hubiera cambiado entre la pantalla y el POST.
type discardIntakesRequest struct {
	IntakeIDs []string `json:"intake_ids"`
}

// discardSkipDTO es UNA solicitud que no se descartó, con su razón. Las razones son
// las claves del dominio (intakes.DiscardSkip*) y viajan tal cual: la pantalla las
// traduce a la voz del dueño, el contrato no.
type discardSkipDTO struct {
	IntakeID string `json:"intake_id"`
	Reason   string `json:"reason"`
}

// discardIntakesResponse es el contrato del 200: qué se descartó y qué no.
//
// Un lote MIXTO —unos válidos y otros no— es el caso NORMAL, no el excepcional, y
// por eso la respuesta tiene dos listas en vez de un código de error: descartar
// veinte huérfanos no puede fallar entero porque uno de ellos ya lo estuviera.
type discardIntakesResponse struct {
	Discarded []string         `json:"discarded"`
	Skipped   []discardSkipDTO `json:"skipped"`
}

// discardIntakesHandler sirve POST /api/v1/intakes/discard: el DESCARTE MANUAL por
// lotes del pedido huérfano (T4.8, REQ-32 / D-041.18). 200 con el desglose por ítem;
// 400 solo por cuerpo malformado, lista vacía o más de intakes.MaxDiscardBatch ids.
//
// NO hay 404 ni 422 aquí, y no es un olvido: un lote no tiene UN recurso ni UN
// estado. Un id inexistente —o de otro tenant, que es indistinguible (INV-8)— sale
// como `not_found` DENTRO del 200, junto a los que sí se descartaron.
//
// ⚠️ Lo que este endpoint NO hace todavía: CERRAR el evento conversacional
// (`conversation_events` a `cancelled`, `flow_state.event_id` a NULL — REQ-32e,
// criterios (g)/(h) del plan). Dueño declarado: **043 · T4.3**.
//
// ACTUALIZADO 2026-08-09 (Plan 043 · Ola 1): las dos piezas YA EXISTEN en el esquema
// —migraciones `0051_conversation_events.sql` y `0052_event_seams.sql`— y el ciclo
// con este plan se rompió (el `abandoned` que el 043 esperaba está publicado). Lo que
// falta ahora es el PRODUCTOR: nadie crea eventos ni apunta el puntero hasta la Ola 2
// del 043, así que no hay evento que cerrar. El detalle de por qué tampoco se entrega
// el filtro `orphan=true` está en la cabecera de internal/intakes/discard.go.
func discardIntakesHandler(svc IntakeService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req discardIntakesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}

		res, err := svc.Discard(r.Context(), id.TenantID, req.IntakeIDs)
		var tooLarge *intakes.TooLargeBatchError
		switch {
		case errors.Is(err, intakes.ErrEmptyDiscardBatch):
			writeError(w, http.StatusBadRequest,
				"intake_ids es obligatorio: manda entre 1 y "+strconv.Itoa(intakes.MaxDiscardBatch)+" ids")
		case errors.As(err, &tooLarge):
			writeError(w, http.StatusBadRequest,
				"el lote trae "+strconv.Itoa(tooLarge.Count)+" solicitudes y el máximo es "+strconv.Itoa(tooLarge.Max))
		case err != nil:
			// Lo ya descartado QUEDA descartado (cada solicitud es su propia unidad
			// de trabajo). Reintentar el mismo lote es seguro: lo hecho vuelve como
			// `already_discarded` y el resto se termina.
			writeError(w, http.StatusInternalServerError, "no se pudieron descartar las solicitudes")
		default:
			writeJSON(w, http.StatusOK, toDiscardResponse(res))
		}
	})
}

// toDiscardResponse proyecta el resultado del lote al wire. Las dos listas salen
// SIEMPRE, también vacías (`[]`, nunca `null`): quien pinta la pantalla no tiene que
// ramificar por el nulo para decir "no se descartó nada".
func toDiscardResponse(res intakes.DiscardResult) discardIntakesResponse {
	skipped := make([]discardSkipDTO, 0, len(res.Skipped))
	for _, s := range res.Skipped {
		skipped = append(skipped, discardSkipDTO{IntakeID: s.IntakeID, Reason: s.Reason})
	}
	discarded := res.Discarded
	if discarded == nil {
		discarded = []string{}
	}
	return discardIntakesResponse{Discarded: discarded, Skipped: skipped}
}

// editIntakeItemDTO es UNA línea tal como la manda el dueño al editar a mano
// (T4.10). Es la MISMA forma que devuelve el detalle (intakeItemDTO) menos lo que
// no le toca poner: `added_at` lo fecha la BD.
//
// `unit_price` viaja en el cuerpo y NO se resuelve contra el catálogo, y esa es la
// decisión de fondo de esta puerta: la edición manual existe precisamente para
// cobrar lo que el catálogo NO tiene todavía (la escena del queso extra,
// D-041.26 §e). Resolver el precio contra el catálogo dejaría al dueño sin poder
// hacer lo único que esta ruta existe para hacer, y además exigiría adivinar CUÁL
// de las refs de contenido del tenant es «su» catálogo. Quien quiera el precio del
// catálogo lo lee de ahí y lo manda: es su UI la que tiene el catálogo delante.
type editIntakeItemDTO struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Customization string  `json:"customization"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
}

// editIntakeItemsRequest es el cuerpo de PUT /api/v1/intakes/{id}/items: el
// conjunto COMPLETO de líneas de cliente que debe quedar.
//
// `Items` es un puntero para distinguir «no mandaste la clave» (cuerpo mal formado
// ⇒ 400) de «mandaste la lista vacía» (quitar todas las líneas ⇒ se aplica). La
// diferencia importa: sin ella, un cuerpo `{}` por un fallo de la UI vaciaría el
// presupuesto en silencio.
type editIntakeItemsRequest struct {
	Items *[]editIntakeItemDTO `json:"items"`
}

// invalidItemsResponse es el cuerpo del 400 por líneas mal formadas: TODOS los
// defectos de una vez, con su posición y su campo (mismo criterio que el validador
// del import). Quien llena diez líneas no puede descubrir sus errores de uno en
// uno.
type invalidItemsResponse struct {
	Error  string               `json:"error"`
	Errors []intakes.LineDefect `json:"errors"`
}

// notEditableResponse es el cuerpo del 422 de una edición sobre una solicitud que
// no está por aprobar: dónde está y desde dónde SÍ se edita, que es lo que el
// llamante necesita para arreglarlo (mover a `pending_approval`, D-041.26) sin
// adivinar.
type notEditableResponse struct {
	Error      string   `json:"error"`
	Status     string   `json:"status"`
	EditableIn []string `json:"editable_in"`
}

// putIntakeItemsHandler sirve PUT /api/v1/intakes/{id}/items: la EDICIÓN MANUAL de
// las líneas de un presupuesto por su dueño (REQ-36 / D-041.26), sin LLM de por
// medio. Responde el detalle completo —con la revisión `corrected` recién escrita—
// para que la consola repinte sin un segundo GET.
//
// PUT y no POST: el cuerpo es el conjunto COMPLETO de líneas de cliente que debe
// quedar, así que mandar dos veces el mismo cuerpo deja la solicitud igual. Lo que
// NO es idempotente es la AUDITORÍA: cada PUT deja su revisión, porque dos
// ediciones son dos actos del dueño aunque el resultado coincida (misma regla que
// InsertRevision).
//
// Códigos: 200 con el detalle; 400 si el cuerpo o las líneas están mal; 404 si la
// solicitud no es del tenant (nunca 403: confirmaría que existe); 422 si no está en
// `pending_approval`; 409 si alguien la movió entre la lectura y la escritura.
func putIntakeItemsHandler(svc IntakeService, feats entitlements.Resolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req editIntakeItemsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}
		if req.Items == nil {
			writeError(w, http.StatusBadRequest, "items es obligatorio (manda [] para dejar la solicitud sin líneas)")
			return
		}

		items, defects := decodeEditItems(*req.Items)
		if len(defects) > 0 {
			writeJSON(w, http.StatusBadRequest, invalidItemsResponse{
				Error: "invalid_items", Errors: mergeItemDefects(defects, items),
			})
			return
		}

		detail, err := svc.ReplaceItems(r.Context(), id.TenantID, r.PathValue("id"), items)
		if err != nil {
			writeEditItemsError(w, err)
			return
		}
		// Por el MISMO camino que el GET, y no por un writeJSON suelto: el cuerpo es
		// el mismo, así que el gate por campo tiene que ser el mismo. Repintar la
		// consola con lo que devuelve el PUT no puede enseñar lo que el GET tapa.
		writeIntakeDetail(r.Context(), w, feats, id.TenantID, detail)
	})
}

// decodeEditItems traduce las líneas del wire a líneas de dominio SANEANDO su
// texto libre por la MISMA puerta que el carrito: cart.SanitizeNote (D-041.19).
// No se copia la regla —se llama—, porque `customization` es una columna con UN
// contrato y ya tiene dos productores (el cart y el pipeline del 044); éste es el
// tercero.
//
// La etiqueta pasa por el mismo saneo que la personalización, y no por parecido:
// las dos acaban en una CELDA del CSV y en una LÍNEA de la comanda, así que un
// salto de línea o un carácter invisible rompen exactamente lo mismo.
//
// El único defecto que produce el saneo es el LARGO (SanitizeNote no trunca a
// propósito: recortar «…y sin maní» pierde justo el alérgeno). El resto de la
// validación —sku, cantidad, precio— es del dominio, que no se fía de esta puerta.
func decodeEditItems(raw []editIntakeItemDTO) ([]intakes.Item, []intakes.LineDefect) {
	items := make([]intakes.Item, 0, len(raw))
	var defects []intakes.LineDefect

	for i, in := range raw {
		it := intakes.Item{SKU: strings.TrimSpace(in.SKU), Qty: in.Qty, UnitPrice: in.UnitPrice}

		label, err := cart.SanitizeNote(in.Label)
		if err != nil {
			defects = append(defects, intakes.LineDefect{
				Index: i, Field: "label",
				Message: "la etiqueta pasa del máximo de " + strconv.Itoa(cart.MaxNoteRunes) + " caracteres",
			})
		}
		custom, err := cart.SanitizeNote(in.Customization)
		if err != nil {
			defects = append(defects, intakes.LineDefect{
				Index: i, Field: "customization",
				Message: "la personalización pasa del máximo de " + strconv.Itoa(cart.MaxNoteRunes) + " caracteres",
			})
		}

		it.Label, it.Customization = label, custom
		items = append(items, it)
	}
	return items, defects
}

// mergeItemDefects añade a los defectos del saneo los que ve el dominio, ordenados
// por línea. Es lo que hace que un cuerpo con la etiqueta demasiado larga Y la
// cantidad en cero se conteste UNA vez con los dos problemas, en vez de mandar al
// llamante a descubrirlos por turnos.
func mergeItemDefects(defects []intakes.LineDefect, items []intakes.Item) []intakes.LineDefect {
	var invalid *intakes.InvalidItemsError
	if err := intakes.ValidateEditableItems(items); errors.As(err, &invalid) {
		defects = append(defects, invalid.Defects...)
	}
	slices.SortStableFunc(defects, func(a, b intakes.LineDefect) int { return a.Index - b.Index })
	return defects
}

// writeEditItemsError traduce el fallo del dominio al código y al cuerpo que le
// sirven a quien llama. La política de códigos vive aquí y no en el dominio: el
// dominio dice QUÉ pasó, el transporte decide cómo se cuenta.
func writeEditItemsError(w http.ResponseWriter, err error) {
	var (
		invalid     *intakes.InvalidItemsError
		tooMany     *intakes.TooManyItemsError
		notEditable *intakes.NotEditableError
	)
	switch {
	case errors.Is(err, intakes.ErrNotFound):
		writeError(w, http.StatusNotFound, "solicitud no encontrada")
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, invalidItemsResponse{
			Error: "invalid_items", Errors: invalid.Defects,
		})
	case errors.As(err, &tooMany):
		writeError(w, http.StatusBadRequest,
			"la edición trae "+strconv.Itoa(tooMany.Count)+" líneas y el máximo es "+strconv.Itoa(tooMany.Max))
	case errors.As(err, &notEditable):
		writeJSON(w, http.StatusUnprocessableEntity, notEditableResponse{
			Error:      "not_editable",
			Status:     notEditable.Status,
			EditableIn: []string{intakes.EditableStatus},
		})
	case errors.Is(err, intakes.ErrConflict):
		writeError(w, http.StatusConflict, "la solicitud cambió de estado; recárgala y reintenta")
	default:
		writeError(w, http.StatusInternalServerError, "no se pudieron guardar las líneas de la solicitud")
	}
}

// toIntakeDTO proyecta una cabecera al wire. El estado ya viene NORMALIZADO del
// dominio (el `closed` legado del módulo cart sale como `confirmed`).
func toIntakeDTO(in intakes.Intake) intakeDTO {
	return intakeDTO{
		ID:           in.ID,
		ContactID:    in.ContactID,
		SessionID:    in.SessionID,
		Status:       in.Status,
		Total:        in.Total,
		CustomerNote: in.CustomerNote,
		CreatedAt:    in.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    in.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// parseIntakeFilter traduce la query a un intakes.Filter. Devuelve el mensaje de
// error (cadena vacía = todo bien) en vez de escribir la respuesta: la política de
// códigos vive en el handler.
//
// `status` acepta las claves nuevas Y el `closed` legado (se normaliza a
// `confirmed`, y el store alcanza igual las filas viejas). Una clave desconocida se
// rechaza con 400 en vez de listar todo: un typo que devuelve la bandeja entera es
// peor que un error.
//
// 🔑 `status` SE REPITE para pedir varios (`?status=pending_approval&status=
// needs_info`, Plan 044 · T4.1 / D-044.47 §2). Hasta el 044 esto usaba
// `q.Get("status")`, que devuelve el PRIMER valor y descarta el resto EN SILENCIO:
// una bandeja que pedía dos estados recibía uno y no tenía forma de enterarse. Un
// solo `?status=x` sigue significando exactamente lo de antes, así que el 041 no
// ve cambiar nada.
// ⚠️ La forma con comas (`?status=a,b`) NO se acepta: `a,b` no es un estado
// conocido y sale por el 400 de siempre, que es un error visible y no otro
// descarte mudo.
//
// `sort` elige el orden (`newest`|`oldest`, D-044.48 §3). Ausente ⇒ `newest`, que
// es lo que esta ruta sirve y documenta desde el Plan 041: quien no lo pide no ve
// cambiar su pantalla. Un valor desconocido se rechaza con 400 por la misma razón
// que un `status` desconocido — servir otro orden en silencio sería peor.
func parseIntakeFilter(r *http.Request) (intakes.Filter, string) {
	q := r.URL.Query()

	from, err := parseFilterTime(q.Get("from"), false)
	if err != nil {
		return intakes.Filter{}, "from inválido: usa YYYY-MM-DD o RFC3339"
	}
	to, err := parseFilterTime(q.Get("to"), true)
	if err != nil {
		return intakes.Filter{}, "to inválido: usa YYYY-MM-DD o RFC3339"
	}

	// q["status"] y NO q.Get("status"): el Get se queda con el primero.
	var statuses []string
	for _, raw := range q["status"] {
		if raw == "" {
			continue // `?status=` es "sin filtro", no un estado vacío
		}
		status := intakes.NormalizeStatus(raw)
		if !intakes.IsStatus(status) {
			return intakes.Filter{}, "status desconocido"
		}
		statuses = append(statuses, status)
	}

	sort := q.Get("sort")
	if sort != "" && !intakes.IsSort(sort) {
		return intakes.Filter{}, "sort desconocido: usa newest u oldest"
	}

	return intakes.Filter{
		From:      from,
		To:        to,
		Statuses:  statuses,
		SessionID: q.Get("session"),
		Sort:      sort,
		Page:      parseIntQuery(r, "page", 1),
		PageSize:  parseIntQuery(r, "page_size", intakes.DefaultPageSize),
	}, ""
}

// parseFilterTime acepta una fecha suelta (YYYY-MM-DD, en UTC) o un instante
// RFC3339. Vacío ⇒ sin cota.
//
// El rango del filtro es [From, To). Una fecha suelta en `to` significa "hasta el
// final de ESE día", así que se le suma un día: sin eso, `to=2026-08-06` no
// devolvería ninguna solicitud del 6 de agosto y el usuario juraría que perdió
// pedidos. Un RFC3339 explícito se respeta tal cual (quien escribe un instante
// sabe lo que pide).
func parseFilterTime(raw string, endOfDay bool) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if day, err := time.ParseInLocation(time.DateOnly, raw, time.UTC); err == nil {
		if endOfDay {
			return day.AddDate(0, 0, 1), nil
		}
		return day, nil
	}
	return time.Parse(time.RFC3339, raw)
}
