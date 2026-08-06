package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

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
type intakeDetailResponse struct {
	intakeDTO
	Items              []intakeItemDTO     `json:"items"`
	Revisions          []intakeRevisionDTO `json:"revisions"`
	AllowedTransitions []string            `json:"allowed_transitions"`
}

// intakeRevisionDTO es una revisión al wire.
//
// `payload` viaja como JSON CRUDO (json.RawMessage), no como un objeto tipado: su
// forma depende de `kind` y del `version` que lleva dentro, y tiparlo aquí
// obligaría a esta capa a conocer todas las formas presentes y futuras — incluida
// la del Plan 044, que aún no existe. El `version` dentro del blob es lo que
// permite a un cliente saber si entiende lo que lee.
//
// CERO PII: ni el payload ni el texto renderizado llevan datos del comprador (esos
// viven cifrados en intake_buyer_data); `created_by` es un ROL, nunca una persona.
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
// token (INV-8), más recientes primero, con filtros from/to/status/session y
// paginación page/page_size (default 50, máx 200).
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
func getIntakeHandler(svc IntakeService) http.Handler {
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
		writeJSON(w, http.StatusOK, intakeDetailResponse{
			intakeDTO: toIntakeDTO(detail.Intake),
			Items:     items,
			Revisions: revisions,
			// detail.Status ya viene normalizado del dominio: una solicitud
			// guardada como `closed` ofrece los destinos de `confirmed`.
			AllowedTransitions: intakes.AllowedTransitions(detail.Status),
		})
	})
}

// setIntakeStatusHandler sirve POST /api/v1/intakes/{id}/status: aplica una
// transición del ciclo de vida (D-041.10). 200 con la solicitud transicionada;
// 404 si no es del tenant; 422 con el estado actual y los destinos permitidos si
// la transición no es válida; 409 si otro operador se adelantó.
//
// Los efectos colaterales (seña, notificación, revisión) NO se disparan aquí:
// llegan en la Ola 4. Esta ruta solo persiste la transición válida.
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

	status := intakes.NormalizeStatus(q.Get("status"))
	if status != "" && !intakes.IsStatus(status) {
		return intakes.Filter{}, "status desconocido"
	}

	return intakes.Filter{
		From:      from,
		To:        to,
		Status:    status,
		SessionID: q.Get("session"),
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
