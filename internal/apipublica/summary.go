// Porta internal/publicapi/summary.go @ ed60c24 (158 líneas).
//
// summary.go — EL RESUMEN DE LA BANDEJA PARA UN LLM EXTERNO (Plan 041 · T1.3, REQ-04, D-041.15;
// mapa §2.7, G10): `GET /api/v1/intakes/summary.json`. Lo monta MountIntakeReports
// (intakereports.go), cuyo comentario es su contrato.
//
// No exporta nada: nace con el verde (05 E-4, P6). En la cara vieja sus tipos eran
// intakeSummaryResponse, intakeSummaryHandler y toSummaryResponse; aquí llevan todos el prefijo
// `summary`, y las líneas tienen su DTO propio (summaryItemDTO) en vez de reusar el de la
// bandeja.

package apipublica

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// summaryResponse es el contrato de GET /api/v1/intakes/summary.json
// (design D-041.15): datos crudos y agregados de las solicitudes que casan con el
// filtro, pensados para que el dueño se los pegue a un LLM EXTERNO.
//
// Ese destino es la razón del invariante que gobierna todo este archivo: CERO PII,
// de ningún tipo. Ni `contact_id` —aunque sea opaco— ni `buyer_data`, que ni
// siquiera se lee. La lista y el detalle sí publican el contacto opaco porque los
// consume la consola del dueño; esto sale del perímetro y no lleva NADA que
// identifique a nadie. `session_id` tampoco entra: identifica el teléfono por el
// que se atendió, no aporta al análisis y es un dato de operación.
type summaryResponse struct {
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

// summaryIntakeDTO es una solicitud del detalle crudo. NO reusa el DTO de la cabecera de la
// bandeja, y esa es la diferencia que importa: aquél lleva contact_id y session_id.
type summaryIntakeDTO struct {
	ID           string           `json:"id"`
	Status       string           `json:"status"`
	CreatedAt    string           `json:"created_at"`
	Total        float64          `json:"total"`
	CustomerNote string           `json:"customer_note"`
	Items        []summaryItemDTO `json:"items"`
}

// summaryItemDTO es una línea del detalle crudo, con las mismas claves que la línea del detalle
// de la bandeja (sku/label/customization/qty/unit_price). En la cara vieja se reusaba el DTO de
// la bandeja (intakeItemDTO); aquí es propio, por el mismo motivo que la cabecera: que añadir
// mañana un campo a la línea de la bandeja no lo cuele en lo que sale del perímetro.
type summaryItemDTO struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Customization string  `json:"customization"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
}

// summaryHandler sirve GET /api/v1/intakes/summary.json: los mismos filtros
// y la misma cota que el export, agregados. 200 con el contrato; 422 si el filtro
// abarca más de MaxExportIntakes solicitudes; 400 ante un filtro mal escrito.
func summaryHandler(svc IntakeReportService) http.Handler {
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

		writeJSON(w, http.StatusOK, summaryResponseOf(sum))
	})
}

// summaryResponseOf (toSummaryResponse en la cara vieja) proyecta el agregado del dominio al
// wire. Es el único punto donde se decide qué sale: la proyección se construye campo a campo
// desde el Detail, NUNCA reusando el DTO de la lista, para que añadir mañana un campo a la
// cabecera del dominio no lo cuele aquí de rebote.
//
// `customization` (D-041.17, T4.1b) y `customer_note` (D-041.19, T4.1c) SÍ entran y no
// contradicen el CERO PII por doctrina: la primera es dato de PRODUCTO —«sin sal»—, no de
// persona, y es justo lo que hace útil el resumen para el LLM del dueño («cuántos piden sin
// cebolla»); la segunda es la indicación del pedido, que el dominio sanea en su puerta
// (intakes.Intake.CustomerNote dice qué la contiene).
//
// generated_at es el instante que trae el agregado: lo pone el servicio con su reloj inyectado
// (D-F6-5, intakes.WithClock), no la cara.
func summaryResponseOf(sum intakes.Summary) summaryResponse {
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
		items := make([]summaryItemDTO, 0, len(d.Items))
		for _, it := range d.Items {
			items = append(items, summaryItemDTO{
				SKU: it.SKU, Label: it.Label, Customization: it.Customization,
				Qty: it.Qty, UnitPrice: it.UnitPrice,
			})
		}
		list = append(list, summaryIntakeDTO{
			ID:           d.ID,
			Status:       d.Status,
			CreatedAt:    d.CreatedAt.UTC().Format(time.RFC3339),
			Total:        d.Total,
			CustomerNote: d.CustomerNote,
			Items:        items,
		})
	}

	return summaryResponse{
		GeneratedAt: sum.GeneratedAt.UTC().Format(time.RFC3339),
		// La cota sin pedir sale vacía: formatInstant (instants.go) hace lo que hacía el
		// summaryBound de la cara vieja, que no se porta.
		Range: summaryRangeDTO{From: formatInstant(sum.From), To: formatInstant(sum.To)},
		Totals: summaryTotalsDTO{
			Intakes: sum.Intakes, Revenue: sum.Revenue, ByStatus: byStatus,
		},
		TopItems: tops,
		Intakes:  list,
	}
}
