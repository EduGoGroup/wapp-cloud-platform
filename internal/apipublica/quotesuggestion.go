// Porta internal/publicapi/quotesuggestion.go @ ed60c24 (134 líneas).
//
// quotesuggestion.go es la puerta HTTP del GENERADOR DE COTIZACIÓN (Plan 044 · Ola 5 · T5.1,
// D-044.11; mapa §2.7, G7): `POST /api/v1/intakes/{id}/quote-suggestion`. La monta
// MountIntakeReports (intakereports.go), con su plazo de escritura propio (writedeadline.go).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 QUÉ HACE ESTE ENDPOINT Y QUÉ NO — Y POR QUÉ NO ESTÁ DENTRO DE LA APROBACIÓN
// ════════════════════════════════════════════════════════════════════════════
//
// DEVUELVE UN TEXTO. No persiste, no transiciona y NO LE MANDA NADA AL CLIENTE. La consola lo
// llama para PRECARGAR el `rendered_text` del formulario de aprobación, y el dueño lo edita y
// aprueba por el camino de siempre.
//
// El diseño del plan NO fija el punto de integración de T5.1 —D-044.11 (design.md §5) habla del
// few-shot y de nada más—, así que esto es una decisión de la tarea, y la decisión es la
// conservadora:
//
//   - **Dentro de la aprobación habría sido lo cómodo y está MAL.** `rendered_text` es
//     obligatorio en ese contrato y es EL TEXTO DEL DUEÑO; que el servidor lo redactara cuando
//     viniera vacío convertiría la aprobación en el mensaje automático que INV-1 y D-044.49 §2
//     apagan. La dueña tiene la última palabra, y la forma de que la tenga es que la máquina
//     sugiera por un camino y ella apruebe por otro.
//   - **Es POST y no GET aunque no escriba nada**, porque consume una inferencia: no es
//     cacheable, no es gratis y no debe dispararlo un prefetch del navegador.
//
// El gate son las DOS features, y ésta es la única ruta de la bandeja donde `llm_intake` aparece
// en la cadena: `cart_basic` porque se opera sobre un pedido, y `llm_intake` porque esto es
// literalmente «la máquina que redacta el borrador sola», que es lo que D-044.49 §3 dice que se
// vende aparte. Aprobar y pedir información no lo llevan por el argumento contrario y ahí sigue
// siendo válido: aprobar es del OBJETO.
// ════════════════════════════════════════════════════════════════════════════
//
// En el rojo solo existía el puerto; el handler y sus auxiliares nacieron con el verde (05 E-4,
// P6).

package apipublica

import (
	"context"
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// QuoteSuggester es el puerto del generador de la cotización sugerida. Lo satisface
// *quotetext.Service del módulo solicitudes NUEVO.
//
// UN método, y no puede tener más: desde aquí no se puede aprobar, ni escribir la revisión, ni
// mandarle el texto al cliente. Esa estrechez es lo que sostiene que la sugerencia no sea un
// mensaje automático — no un comentario.
//
// Era `Sugerir` (y devolvía `quotetext.Sugerencia`) en la cara vieja (05 E-11).
//
// G7, `POST /api/v1/intakes/{id}/quote-suggestion`, es su única ruta. Lo que promete (la cadena,
// los gates y el plazo de escritura los dice MountIntakeReports):
//
//   - NO LEE EL CUERPO, y es deliberado: todo lo que hace falta está en el token (el tenant,
//     INV-7) y en la ruta (la solicitud). Suggest recibe SIEMPRE el tenant del token y el {id}
//     de la ruta tal cual; un cuerpo con parámetros —cuántos ejemplos, qué tono, qué vía, otro
//     tenant— no cambia nada: sería dejar que una llamada suelta se saltara la configuración
//     del tenant;
//   - 200 {"rendered_text": Text, "source": Source} y, SOLO si Reason no es vacío,
//     "fallback_reason": Reason, en ese orden. `rendered_text` se llama EXACTAMENTE como el
//     campo del cuerpo de la aprobación: es lo que la consola copia de una respuesta al
//     siguiente formulario. `source` (`llm` o `deterministic`) se publica porque el dueño tiene
//     derecho a saber si lo que va a mandar lo redactó el modelo o es el respaldo sobrio;
//     `fallback_reason` es el vocabulario CERRADO de quotetext (Reason…), no una frase libre;
//   - 🔴 NO HAY 502 NI 503 PARA EL PROVEEDOR CAÍDO, y no falta: con el modelo muerto el dominio
//     no devuelve error, y la ruta responde 200 con el texto determinista y su
//     `fallback_reason`;
//   - intakes.ErrNotFound (también envuelto) ⇒ 404 {"error":"solicitud no encontrada"}: la
//     solicitud no es del tenant. Nunca 403, que confirmaría que existe (INV-8);
//   - *intakes.PendingPriceError (también envuelto) ⇒ 400
//     {"error":"lines_without_price","lines":[{"index":…,"label":…},…]}, con TODAS las líneas y
//     en su orden: EL MISMO cuerpo que el de la aprobación, a propósito (es la misma
//     precondición sobre el mismo objeto);
//   - quotetext.ErrNoLines (también envuelto) ⇒ 400 {"error":"la solicitud no tiene líneas que
//     cotizar: guarda primero las líneas del borrador con PUT /api/v1/intakes/{id}/items"};
//   - cualquier otro error ⇒ 500 {"error":"no se pudo generar la cotización sugerida"}, que NO
//     repite el error del dominio (el del driver puede llevar el DSN);
//   - el contexto que recibe Suggest es el de la petición, SIN plazo añadido por la cara: el
//     trabajo lo acota el plazo de la llamada al modelo, dentro del dominio.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"}.
type QuoteSuggester interface {
	// Suggest redacta la cotización sugerida de la solicitud intakeID de tenantID. No persiste,
	// no transiciona y no envía nada.
	Suggest(ctx context.Context, tenantID, intakeID string) (quotetext.Suggestion, error)
}

// quoteSuggestionResponse es el cuerpo del 200.
//
// `rendered_text` se llama EXACTAMENTE como el campo del cuerpo de la aprobación, y no es
// una casualidad de nombres: es lo que la consola copia de una respuesta al siguiente
// formulario, y dos nombres distintos para el mismo texto es como se introduce el
// mapeo que un día se hace mal.
type quoteSuggestionResponse struct {
	// RenderedText es la cotización sugerida.
	RenderedText string `json:"rendered_text"`
	// Source es `llm` o `deterministic` (quotetext.Source…).
	//
	// Se publica y no se esconde porque el dueño tiene derecho a saber si lo que va a
	// mandar lo redactó el modelo o es el respaldo sobrio, y porque sin él la consola
	// no puede distinguir «no funciona» de «este tenant todavía no tiene historial».
	Source string `json:"source"`
	// FallbackReason dice POR QUÉ no fue el modelo. Se omite cuando sí lo fue.
	//
	// Es un vocabulario CERRADO (las constantes Reason… de quotetext) y no una frase
	// libre: sale por la API, y una cadena arbitraria aquí sería un campo que nadie
	// puede agregar ni traducir.
	FallbackReason string `json:"fallback_reason,omitempty"`
}

// quotePendingPriceResponse es el cuerpo del 400 de una solicitud con líneas sin precio. Tiene
// LA MISMA forma que el de la aprobación (`pendingPriceResponse` en la cara vieja, que este
// fichero reusaba): aquí es un tipo propio para que G7 no dependa del fichero de la bandeja,
// y que los dos cuerpos sigan siendo el mismo lo fija el test de cada uno, byte a byte.
type quotePendingPriceResponse struct {
	Error string                     `json:"error"`
	Lines []intakes.PendingPriceLine `json:"lines"`
}

// quoteSuggestionHandler sirve POST /api/v1/intakes/{id}/quote-suggestion.
//
// NO LEE EL CUERPO, y es deliberado: todo lo que hace falta está en el token (el
// tenant, INV-7) y en la ruta (la solicitud). Un cuerpo con parámetros —cuántos
// ejemplos, qué tono, qué vía— sería dejar que una llamada suelta se saltara la
// configuración del tenant, que es el mismo argumento por el que el re-análisis no acepta
// `provider`.
//
// Códigos: 200 con el texto; 400 si la solicitud no tiene nada que cotizar o le faltan
// precios; 404 si no es del tenant (nunca 403: confirmaría que existe, INV-8); 500 en
// fallo del store.
//
// 🔴 NO HAY 502 NI 503 PARA EL PROVEEDOR CAÍDO, y no falta: con el modelo muerto este
// endpoint responde 200 con el texto determinista y `fallback_reason`. Ésa es la
// conducta que se quiere —el dueño obtiene una cotización utilizable igual— y por eso
// el dominio no devuelve error por esa vía (ver quotetext, compose).
func quoteSuggestionHandler(svc QuoteSuggester) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		out, err := svc.Suggest(r.Context(), id.TenantID, r.PathValue("id"))
		if err != nil {
			quoteWriteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, quoteSuggestionResponse{
			RenderedText:   out.Text,
			Source:         out.Source,
			FallbackReason: out.Reason,
		})
	})
}

// quoteWriteError (writeQuoteSuggestionError en la cara vieja) traduce el fallo del dominio al
// código y al cuerpo.
//
// Los dos cuerpos de 400 son LOS MISMOS que los de la aprobación —`lines_without_price`
// con su lista, y el texto que manda a `PUT …/items`— a propósito: son la misma
// precondición sobre el mismo objeto, y que la consola tuviera que tratarlas distinto
// según por qué puerta entró sería el duplicado que este plan ya pagó una vez.
func quoteWriteError(w http.ResponseWriter, err error) {
	var pending *intakes.PendingPriceError
	switch {
	case errors.Is(err, intakes.ErrNotFound):
		writeError(w, http.StatusNotFound, "solicitud no encontrada")
	case errors.As(err, &pending):
		writeJSON(w, http.StatusBadRequest, quotePendingPriceResponse{
			Error: "lines_without_price", Lines: pending.Lines,
		})
	case errors.Is(err, quotetext.ErrNoLines):
		writeError(w, http.StatusBadRequest,
			"la solicitud no tiene líneas que cotizar: guarda primero las líneas del borrador con PUT /api/v1/intakes/{id}/items")
	default:
		writeError(w, http.StatusInternalServerError, "no se pudo generar la cotización sugerida")
	}
}
