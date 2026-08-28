package publicapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes/quotetext"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// quotesuggestion.go es la puerta HTTP del GENERADOR DE COTIZACIÓN (Plan 044 · Ola 5
// · T5.1, D-044.11): `POST /api/v1/intakes/{id}/quote-suggestion`.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 QUÉ HACE ESTE ENDPOINT Y QUÉ NO — Y POR QUÉ NO ESTÁ DENTRO DE `approve`
// ════════════════════════════════════════════════════════════════════════════
//
// DEVUELVE UN TEXTO. No persiste, no transiciona y NO LE MANDA NADA AL CLIENTE. La
// consola lo llama para PRECARGAR el `rendered_text` del formulario de aprobación, y
// el dueño lo edita y aprueba por el camino de siempre.
//
// El diseño del plan NO fija el punto de integración de T5.1 —lo comprobé: D-044.11
// (design.md §5) habla del few-shot y de nada más—, así que esto es una decisión de la
// tarea, y la decisión es la conservadora:
//
//   - **Dentro de `Approve` habría sido lo cómodo y está MAL.** `rendered_text` es
//     obligatorio en ese contrato y es EL TEXTO DEL DUEÑO; que el servidor lo
//     redactara cuando viniera vacío convertiría la aprobación en el mensaje
//     automático que INV-1 y D-044.49 §2 apagan. La dueña tiene la última palabra, y
//     la forma de que la tenga es que la máquina sugiera por un camino y ella apruebe
//     por otro.
//   - **Es POST y no GET aunque no escriba nada**, porque consume una inferencia: no
//     es cacheable, no es gratis y no debe dispararlo un prefetch del navegador.
//
// El gate son las DOS features, y ésta es la única ruta de la bandeja donde `llm_intake`
// aparece en la cadena de middlewares: `cart_basic` porque se opera sobre un pedido, y
// `llm_intake` porque esto es literalmente «la máquina que redacta el borrador sola»,
// que es lo que D-044.49 §3 dice que se vende aparte. `approve` y `request-info` no lo
// llevan por el argumento contrario y ahí sigue siendo válido: aprobar es del OBJETO.
// ════════════════════════════════════════════════════════════════════════════

// QuoteSuggester es el puerto del generador. Lo satisface *quotetext.Servicio.
//
// UN método, y no puede tener más: desde aquí no se puede aprobar, ni escribir la
// revisión, ni mandarle el texto al cliente. Esa estrechez es lo que sostiene el
// párrafo de arriba — no un comentario.
type QuoteSuggester interface {
	Sugerir(ctx context.Context, tenantID, intakeID string) (quotetext.Sugerencia, error)
}

// quoteSuggestionResponse es el cuerpo del 200.
//
// `rendered_text` se llama EXACTAMENTE como el campo del cuerpo de `approve`, y no es
// una casualidad de nombres: es lo que la consola copia de una respuesta al siguiente
// formulario, y dos nombres distintos para el mismo texto es como se introduce el
// mapeo que un día se hace mal.
type quoteSuggestionResponse struct {
	// RenderedText es la cotización sugerida.
	RenderedText string `json:"rendered_text"`
	// Source es `llm` o `deterministic` (quotetext.Origen*).
	//
	// Se publica y no se esconde porque el dueño tiene derecho a saber si lo que va a
	// mandar lo redactó el modelo o es el respaldo sobrio, y porque sin él la consola
	// no puede distinguir «no funciona» de «este tenant todavía no tiene historial».
	Source string `json:"source"`
	// FallbackReason dice POR QUÉ no fue el modelo. Se omite cuando sí lo fue.
	//
	// Es un vocabulario CERRADO (las constantes Motivo* de quotetext) y no una frase
	// libre: sale por la API, y una cadena arbitraria aquí sería un campo que nadie
	// puede agregar ni traducir.
	FallbackReason string `json:"fallback_reason,omitempty"`
}

// quoteSuggestionHandler sirve POST /api/v1/intakes/{id}/quote-suggestion.
//
// NO LEE EL CUERPO, y es deliberado: todo lo que hace falta está en el token (el
// tenant, INV-7) y en la ruta (la solicitud). Un cuerpo con parámetros —cuántos
// ejemplos, qué tono, qué vía— sería dejar que una llamada suelta se saltara la
// configuración del tenant, que es el mismo argumento por el que `reanalyze` no acepta
// `provider`.
//
// Códigos: 200 con el texto; 400 si la solicitud no tiene nada que cotizar o le faltan
// precios; 404 si no es del tenant (nunca 403: confirmaría que existe, INV-8); 500 en
// fallo del store.
//
// 🔴 NO HAY 502 NI 503 PARA EL PROVEEDOR CAÍDO, y no falta: con el modelo muerto este
// endpoint responde 200 con el texto determinista y `fallback_reason`. Ésa es la
// conducta que se quiere —el dueño obtiene una cotización utilizable igual— y por eso
// el dominio no devuelve error por esa vía (ver quotetext.redactar).
func quoteSuggestionHandler(svc QuoteSuggester) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		out, err := svc.Sugerir(r.Context(), id.TenantID, r.PathValue("id"))
		if err != nil {
			writeQuoteSuggestionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, quoteSuggestionResponse{
			RenderedText:   out.Texto,
			Source:         out.Origen,
			FallbackReason: out.Motivo,
		})
	})
}

// writeQuoteSuggestionError traduce el fallo del dominio al código y al cuerpo.
//
// Los dos cuerpos de 400 son LOS MISMOS que los de `approve` —`lines_without_price`
// con su lista, y el texto que manda a `PUT …/items`— a propósito: son la misma
// precondición sobre el mismo objeto, y que la consola tuviera que tratarlas distinto
// según por qué puerta entró sería el duplicado que este plan ya pagó una vez.
func writeQuoteSuggestionError(w http.ResponseWriter, err error) {
	var pending *intakes.PendingPriceError
	switch {
	case errors.Is(err, intakes.ErrNotFound):
		writeError(w, http.StatusNotFound, "solicitud no encontrada")
	case errors.As(err, &pending):
		writeJSON(w, http.StatusBadRequest, pendingPriceResponse{
			Error: "lines_without_price", Lines: pending.Lines,
		})
	case errors.Is(err, quotetext.ErrSinLineas):
		writeError(w, http.StatusBadRequest,
			"la solicitud no tiene líneas que cotizar: guarda primero las líneas del borrador con PUT /api/v1/intakes/{id}/items")
	default:
		writeError(w, http.StatusInternalServerError, "no se pudo generar la cotización sugerida")
	}
}
