// Porta internal/publicapi/intakes.go @ ed60c24 (1073 líneas: la aprobación, líneas 732-854; la
// petición de información, líneas 856-936).
//
// intakes_approve.go — LAS DOS PUERTAS POR LAS QUE EL DUEÑO LE HABLA AL CLIENTE: G5, aprobar el
// presupuesto (POST /api/v1/intakes/{id}/approve; Plan 044 · T4.3, D-044.49), y G6, pedir más
// información (POST /api/v1/intakes/{id}/request-info; T4.4, D-044.49 §2). Es un trozo de
// intakes.go partido por tema (05 E-13).
//
// 🔴 INV-1: en este fichero están la ÚNICA llamada de la cara al método Approve del servicio y
// la ÚNICA a RequestInfo, una cada una. Las cuenta sobre el AST el candado
// internal/modulos/solicitudes/intakes/inv1_aprobar_test.go: un segundo llamante —otra ruta, un
// auxiliar «de conveniencia», una rama duplicada— lo pone rojo. Aprobar MANDA UN WHATSAPP con
// precio, deja la solicitud en `confirmed` y empuja al CRM; nada de eso puede tener dos puertas.
//
// No exporta nada y nació con el verde (05 E-4, P6): sus promesas están en el contrato de
// MountIntakes y las prueba intakes_approve_test.go.

package apipublica

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// intakeApproveRequest (approveIntakeRequest en la cara vieja) es el cuerpo de POST
// /api/v1/intakes/{id}/approve: el texto que el DUEÑO le manda al cliente (D-044.49, decisión del
// 2026-08-27).
//
// Es un solo campo y es OBLIGATORIO. No hay `items` ni `total` en el cuerpo, y esa
// ausencia es la decisión: lo que se cotiza son las líneas que la solicitud YA tiene
// —el dueño las corrige con el `PUT …/items`, que es la otra puerta de la misma
// pantalla—, así que aceptar aquí una lista abriría un segundo camino para escribir
// lo mismo. Es exactamente el duplicado que D-044.48 §1 cerró para `correct`.
//
// No es puntero (a diferencia de intakeEditItemsRequest.Items) porque aquí no hay
// dos ausencias que distinguir: la clave que falta y la cadena vacía significan lo
// mismo —no hay cotización que mandar— y las dos salen por el mismo 400.
type intakeApproveRequest struct {
	RenderedText string `json:"rendered_text"`
}

// intakePendingPriceResponse (pendingPriceResponse en la cara vieja) es el cuerpo del 400 de una
// aprobación cuyo borrador todavía tiene líneas sin precio (precondición de T4.3). Lleva TODAS
// las líneas pendientes con su posición y su etiqueta, por el mismo criterio que
// intakeInvalidItemsResponse: quien tiene tres renglones sin precificar tiene que verlos los
// tres.
//
// La POSICIÓN y no el sku: la línea `unmatched` —la que el catálogo no reconoció, que
// es justo la que suele no tener precio— no tiene sku (Plan 044 · T3.2).
type intakePendingPriceResponse struct {
	Error string                     `json:"error"`
	Lines []intakes.PendingPriceLine `json:"lines"`
}

// intakeNotApprovableResponse (notApprovableResponse en la cara vieja) es el cuerpo del 422 de
// una aprobación sobre una solicitud que no está por aprobar: dónde está y desde dónde SÍ se
// aprueba. Es el gemelo de intakeNotEditableResponse, y son dos cuerpos y no uno porque son dos
// preguntas: se puede estar en un estado editable y no aprobable nunca a la vez, pero quien
// recibe el error tiene que saber cuál de las dos puertas le cerró.
type intakeNotApprovableResponse struct {
	Error        string   `json:"error"`
	Status       string   `json:"status"`
	ApprovableIn []string `json:"approvable_in"`
}

// intakeApproveHandler (approveIntakeHandler en la cara vieja) sirve POST
// /api/v1/intakes/{id}/approve: la acción APROBAR del dueño (Plan 044 · T4.3). Responde el
// detalle completo —con la revisión `approved` recién escrita— para que la consola repinte sin
// un segundo GET, por el MISMO camino que el GET y el PUT de líneas (intakeWriteDetail), de modo
// que el gate por campo del 044 se aplique igual.
//
// Códigos: 200 con el detalle; 400 si falta el texto, si el borrador tiene líneas sin
// precio o si no hay nada que cotizar; 404 si la solicitud no es del tenant (nunca
// 403: confirmaría que existe); 422 si no está en `pending_approval`; 409 si alguien
// la movió entre la lectura y la escritura.
//
// El 200 significa «la aprobación se aplicó y quedó registrada», NUNCA «el cliente
// recibió el mensaje»: el envío va después de la escritura y no puede tumbarla (ver
// intakes/approve.go). Con la sesión del negocio offline el dueño sigue viendo su 200
// y el fallo queda en el log con su command_id.
func intakeApproveHandler(svc IntakeService, feats entitlements.Resolver, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req intakeApproveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}

		detail, err := svc.Approve(r.Context(), id.TenantID, r.PathValue("id"), req.RenderedText)
		if err != nil {
			intakeWriteApproveError(w, err)
			return
		}
		intakeWriteDetail(r.Context(), w, feats, id.TenantID, detail, now())
	})
}

// intakeWriteApproveError (writeApproveError en la cara vieja) traduce el fallo del dominio al
// código y al cuerpo que le sirven a quien llama. La política de códigos vive aquí y no en el
// dominio: el dominio dice QUÉ pasó, el transporte decide cómo se cuenta (mismo reparto que
// intakeWriteEditItemsError).
//
// *TransitionError sale 422 con el MISMO cuerpo que el <select> de estado
// (intakeInvalidTransitionResponse) y no con el de not_approvable: cuando aparece es porque
// alguien movió la solicitud entre la validación y el compare-and-swap, y la respuesta
// útil ahí es la del ciclo de vida —dónde está y adónde puede ir—, no la de esta puerta.
func intakeWriteApproveError(w http.ResponseWriter, err error) {
	var (
		pending       *intakes.PendingPriceError
		notApprovable *intakes.NotApprovableError
		invalid       *intakes.TransitionError
	)
	switch {
	case errors.Is(err, intakes.ErrNotFound):
		writeError(w, http.StatusNotFound, "solicitud no encontrada")
	case errors.Is(err, intakes.ErrEmptyQuoteText):
		writeError(w, http.StatusBadRequest,
			"rendered_text es obligatorio: es el texto de la cotización que se le manda al cliente")
	case errors.As(err, &pending):
		writeJSON(w, http.StatusBadRequest, intakePendingPriceResponse{
			Error: "lines_without_price", Lines: pending.Lines,
		})
	case errors.Is(err, intakes.ErrEmptyQuote):
		writeError(w, http.StatusBadRequest,
			"la solicitud no tiene líneas que cotizar: guarda primero las líneas del borrador con PUT /api/v1/intakes/{id}/items")
	case errors.As(err, &notApprovable):
		writeJSON(w, http.StatusUnprocessableEntity, intakeNotApprovableResponse{
			Error:        "not_approvable",
			Status:       notApprovable.Status,
			ApprovableIn: []string{intakes.ApprovableStatus},
		})
	case errors.As(err, &invalid):
		intakeWriteInvalidTransition(w, invalid)
	case errors.Is(err, intakes.ErrConflict):
		writeError(w, http.StatusConflict, "la solicitud cambió de estado; recárgala y reintenta")
	default:
		writeError(w, http.StatusInternalServerError, "no se pudo aprobar la solicitud")
	}
}

// intakeRequestInfoRequest (requestInfoRequest en la cara vieja) es el cuerpo de POST
// /api/v1/intakes/{id}/request-info: la pregunta que el DUEÑO le manda al cliente (D-044.49 §2,
// decisión del 2026-08-27).
//
// Un solo campo y OBLIGATORIO. El sistema PREPARA la pregunta —las
// `suggested_questions` de la revisión, que el detalle ya publica— pero quien la
// manda es el dueño después de editarla: por eso viaja en el cuerpo y no se deduce
// aquí de la revisión. Una petición de información que el servidor redactara solo
// sería exactamente el mensaje automático que D-044.49 §2 apaga.
//
// No es puntero, igual que intakeApproveRequest: la clave ausente y la cadena vacía
// significan lo mismo —no hay pregunta que mandar— y salen por el mismo 400.
type intakeRequestInfoRequest struct {
	Question string `json:"question"`
}

// intakeRequestInfoHandler (requestInfoIntakeHandler en la cara vieja) sirve POST
// /api/v1/intakes/{id}/request-info: la acción PEDIR MÁS INFORMACIÓN del dueño (Plan 044 · T4.4).
// Responde el detalle completo por el MISMO camino que el GET, el PUT y `approve`
// (intakeWriteDetail), para que el gate por campo del 044 se aplique igual.
//
// Códigos: 200 con el detalle; 400 si falta la pregunta; 404 si la solicitud no es del
// tenant (nunca 403: confirmaría que existe); 422 si no está en `pending_approval`
// —con el cuerpo del ciclo de vida, que dice adónde SÍ puede ir—; 409 si alguien la
// movió entre la lectura y la escritura.
//
// El 200 significa «la solicitud quedó esperando la respuesta del cliente», NUNCA «el
// cliente recibió la pregunta»: el envío va después de la transición y no puede
// tumbarla (ver intakes/requestinfo.go).
func intakeRequestInfoHandler(svc IntakeService, feats entitlements.Resolver, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req intakeRequestInfoRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}

		detail, err := svc.RequestInfo(r.Context(), id.TenantID, r.PathValue("id"), req.Question)
		if err != nil {
			intakeWriteRequestInfoError(w, err)
			return
		}
		intakeWriteDetail(r.Context(), w, feats, id.TenantID, detail, now())
	})
}

// intakeWriteRequestInfoError (writeRequestInfoError en la cara vieja) traduce el fallo del
// dominio al código y al cuerpo (mismo reparto que intakeWriteEditItemsError e
// intakeWriteApproveError: el dominio dice QUÉ pasó, el transporte decide cómo se cuenta).
//
// *TransitionError sale con el cuerpo del ciclo de vida y NO con uno propio tipo
// `not_requestable`, al revés que `approve`: esta puerta no estrecha la máquina de
// estados —a `needs_info` solo se llega desde `pending_approval` y eso ya lo dice la
// tabla—, así que el 422 útil es el que enseña dónde está la solicitud y adónde puede
// ir. Un cuerpo propio habría duplicado esa regla para decir lo mismo peor.
func intakeWriteRequestInfoError(w http.ResponseWriter, err error) {
	var invalid *intakes.TransitionError
	switch {
	case errors.Is(err, intakes.ErrNotFound):
		writeError(w, http.StatusNotFound, "solicitud no encontrada")
	case errors.Is(err, intakes.ErrEmptyQuestion):
		writeError(w, http.StatusBadRequest,
			"question es obligatoria: es la pregunta que se le manda al cliente, y jamás sale sola")
	case errors.As(err, &invalid):
		intakeWriteInvalidTransition(w, invalid)
	case errors.Is(err, intakes.ErrConflict):
		writeError(w, http.StatusConflict, "la solicitud cambió de estado; recárgala y reintenta")
	default:
		writeError(w, http.StatusInternalServerError, "no se pudo pedir más información sobre la solicitud")
	}
}
