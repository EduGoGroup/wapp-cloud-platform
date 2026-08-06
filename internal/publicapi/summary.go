package publicapi

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// intakeSummaryResponse es el contrato de GET /api/v1/intakes/summary.json
// (design D-041.15): datos crudos y agregados de las solicitudes que casan con el
// filtro, pensados para que el dueño se los pegue a un LLM EXTERNO.
//
// Ese destino es la razón del invariante que gobierna todo este archivo: CERO PII,
// de ningún tipo. Ni `contact_id` —aunque sea opaco— ni `buyer_data`, que ni
// siquiera se lee. La lista y el detalle sí publican el contacto opaco porque los
// consume la consola del dueño; esto sale del perímetro y no lleva NADA que
// identifique a nadie. `session_id` tampoco entra: identifica el teléfono por el
// que se atendió, no aporta al análisis y es un dato de operación.
type intakeSummaryResponse struct {
	GeneratedAt string              `json:"generated_at"`
	Range       summaryRangeDTO     `json:"range"`
	Totals      summaryTotalsDTO    `json:"totals"`
	TopItems    []summaryTopItemDTO `json:"top_items"`
	Intakes     []summaryIntakeDTO  `json:"intakes"`
}

// summaryRangeDTO devuelve el rango REALMENTE aplicado. Una cota sin pedir viaja
// como cadena vacía: sin ella, quien lee el JSON no sabría si el rango se aplicó o
// si está viendo la bandeja entera.
type summaryRangeDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type summaryTotalsDTO struct {
	Intakes  int            `json:"intakes"`
	Revenue  float64        `json:"revenue"`
	ByStatus map[string]int `json:"by_status"`
}

type summaryTopItemDTO struct {
	SKU      string  `json:"sku"`
	Label    string  `json:"label"`
	QtyTotal int     `json:"qty_total"`
	Revenue  float64 `json:"revenue"`
}

// summaryIntakeDTO es una solicitud del detalle crudo. Reusa intakeItemDTO para
// las líneas (mismas claves que el detalle: sku/label/qty/unit_price), pero NO
// reusa intakeDTO para la cabecera, y esa es la diferencia que importa: intakeDTO
// lleva contact_id y session_id.
type summaryIntakeDTO struct {
	ID        string          `json:"id"`
	Status    string          `json:"status"`
	CreatedAt string          `json:"created_at"`
	Total     float64         `json:"total"`
	Items     []intakeItemDTO `json:"items"`
}

// intakeSummaryHandler sirve GET /api/v1/intakes/summary.json: los mismos filtros
// y la misma cota que el export, agregados. 200 con el contrato; 422 si el filtro
// abarca más de MaxExportIntakes solicitudes; 400 ante un filtro mal escrito.
func intakeSummaryHandler(svc IntakeService) http.Handler {
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

		sum, err := svc.Summary(r.Context(), id.TenantID, filter)
		switch {
		case errors.Is(err, intakes.ErrTooLarge):
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf(
				"el filtro abarca más de %d solicitudes: acótalo con from/to", intakes.MaxExportIntakes))
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "no se pudo resumir las solicitudes")
			return
		}

		writeJSON(w, http.StatusOK, toSummaryResponse(sum))
	})
}

// toSummaryResponse proyecta el agregado del dominio al wire. Es el único punto
// donde se decide qué sale: la proyección se construye campo a campo desde el
// Detail, NUNCA reusando el DTO de la lista, para que añadir mañana un campo a la
// cabecera del dominio no lo cuele aquí de rebote.
//
// `customization` SÍ entra (D-041.17, T4.1b) y no contradice el CERO PII: es dato
// de PRODUCTO —«sin sal»—, no de persona, y es justo lo que hace útil el resumen
// para el LLM del dueño («cuántos piden sin cebolla»). Lo que sigue sin aparecer es
// `customer_note` (D-041.19), cuya columna nace en T4.1c: al contrario que en el
// CSV —donde el hueco se reserva porque las columnas son posicionales— aquí las
// claves tienen nombre, así que esa tarea la añade sin romper a nadie y hoy no se
// publica una cadena vacía que aparentaría "el cliente no indicó nada" cuando la
// verdad es "todavía no se registra".
func toSummaryResponse(sum intakes.Summary) intakeSummaryResponse {
	byStatus := sum.ByStatus
	if byStatus == nil {
		byStatus = map[string]int{}
	}

	tops := make([]summaryTopItemDTO, 0, len(sum.TopItems))
	for _, t := range sum.TopItems {
		tops = append(tops, summaryTopItemDTO{
			SKU: t.SKU, Label: t.Label, QtyTotal: t.QtyTotal, Revenue: t.Revenue,
		})
	}

	list := make([]summaryIntakeDTO, 0, len(sum.Details))
	for _, d := range sum.Details {
		items := make([]intakeItemDTO, 0, len(d.Items))
		for _, it := range d.Items {
			items = append(items, intakeItemDTO{
				SKU: it.SKU, Label: it.Label, Customization: it.Customization,
				Qty: it.Qty, UnitPrice: it.UnitPrice,
			})
		}
		list = append(list, summaryIntakeDTO{
			ID:        d.ID,
			Status:    d.Status,
			CreatedAt: d.CreatedAt.UTC().Format(time.RFC3339),
			Total:     d.Total,
			Items:     items,
		})
	}

	return intakeSummaryResponse{
		GeneratedAt: sum.GeneratedAt.UTC().Format(time.RFC3339),
		Range:       summaryRangeDTO{From: summaryBound(sum.From), To: summaryBound(sum.To)},
		Totals: summaryTotalsDTO{
			Intakes: sum.Intakes, Revenue: sum.Revenue, ByStatus: byStatus,
		},
		TopItems: tops,
		Intakes:  list,
	}
}

// summaryBound formatea una cota del rango; la cota sin pedir sale vacía.
func summaryBound(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
