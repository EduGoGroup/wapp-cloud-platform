// Porta internal/publicapi/intakes.go @ ed60c24 (1073 líneas: invalidTransitionResponse,
// setIntakeStatusRequest y setIntakeStatusHandler, líneas 225-238 y 369-420; el descarte por
// lotes, líneas 422-520).
//
// intakes_status.go — LAS DOS PUERTAS QUE MUEVEN EL ESTADO DE UNA SOLICITUD: G3, el cambio de
// estado del ciclo de vida (POST /api/v1/intakes/{id}/status), y G8, el descarte manual por
// lotes (POST /api/v1/intakes/discard). Es un trozo de intakes.go partido por tema (05 E-13).
//
// Son dos y no una, y esa separación es una decisión (Jhoan, 2026-08-06): `expired → abandoned`
// se acepta por el descarte y se rechaza con 422 por el cambio de estado. Quien «unifique» el
// descarte sobre SetStatus abre esa transición en la API y en el <select> de la consola.
//
// No exporta nada y nació con el verde (05 E-4, P6): sus promesas están en el contrato de
// MountIntakes y las prueba intakes_status_test.go.

package apipublica

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// intakeInvalidTransitionResponse (invalidTransitionResponse en la cara vieja) es el cuerpo del
// 422 de POST …/status: dónde está la solicitud AHORA y adónde sí puede ir. Sin `allowed`, el
// llamante tendría que adivinar el ciclo de vida a base de reintentos.
type intakeInvalidTransitionResponse struct {
	Error     string   `json:"error"`
	Status    string   `json:"status"`
	Requested string   `json:"requested"`
	Allowed   []string `json:"allowed"`
}

// intakeWriteInvalidTransition responde el 422 del ciclo de vida. Es UN punto porque lo
// responden tres puertas con el MISMO cuerpo —el cambio de estado, `approve` y `request-info`—;
// en la cara vieja el literal estaba escrito tres veces.
func intakeWriteInvalidTransition(w http.ResponseWriter, invalid *intakes.TransitionError) {
	writeJSON(w, http.StatusUnprocessableEntity, intakeInvalidTransitionResponse{
		Error:     "invalid_transition",
		Status:    invalid.From,
		Requested: invalid.To,
		Allowed:   invalid.Allowed,
	})
}

// intakeSetStatusRequest es el cuerpo de POST /api/v1/intakes/{id}/status.
type intakeSetStatusRequest struct {
	Status string `json:"status"`
}

// intakeSetStatusHandler (setIntakeStatusHandler en la cara vieja) sirve POST
// /api/v1/intakes/{id}/status: aplica una transición del ciclo de vida (D-041.10). 200 con la
// solicitud transicionada; 404 si no es del tenant; 422 con el estado actual y los destinos
// permitidos si la transición no es válida; 409 si otro operador se adelantó.
//
// El AVISO AL CLIENTE (D-041.14, T4.2) lo dispara el servicio DESPUÉS de escribir,
// y no puede cambiar lo que devuelve esta ruta: un 200 significa «la transición se
// aplicó», nunca «el cliente recibió el mensaje». Con la sesión del negocio offline
// el dueño sigue viendo su 200 y su bandeja al día; el envío fallido queda en el
// log del servidor con su command_id.
func intakeSetStatusHandler(svc IntakeService, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req intakeSetStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}
		if req.Status == "" {
			writeError(w, http.StatusBadRequest, "status es obligatorio")
			return
		}

		// NoticeToClient: esta puerta NO escribe ningún texto propio, así que el
		// aviso genérico del estado destino es el ÚNICO que el cliente va a recibir
		// (D-041.14). Quitárselo aquí lo dejaría sin enterarse del cambio.
		updated, err := svc.SetStatus(r.Context(), id.TenantID, r.PathValue("id"), req.Status, intakes.NoticeToClient)
		var invalid *intakes.TransitionError
		switch {
		case errors.Is(err, intakes.ErrNotFound):
			writeError(w, http.StatusNotFound, "solicitud no encontrada")
		case errors.As(err, &invalid):
			intakeWriteInvalidTransition(w, invalid)
		case errors.Is(err, intakes.ErrConflict):
			writeError(w, http.StatusConflict, "la solicitud cambió de estado; recárgala y reintenta")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "no se pudo cambiar el estado de la solicitud")
		default:
			writeJSON(w, http.StatusOK, intakeToDTO(updated, now()))
		}
	})
}

// intakeDiscardRequest (discardIntakesRequest en la cara vieja) es el cuerpo de POST
// /api/v1/intakes/discard: los ids que el dueño quiere sacar de su bandeja.
//
// Es una lista EXPLÍCITA de ids y no un filtro, y eso es la decisión de fondo de
// esta puerta: el descarte es irreversible y no hay papelera (D-041.22), así que
// quien descarta tiene que NOMBRAR lo que descarta. Un filtro dejaría el conjunto
// afectado a merced de lo que hubiera cambiado entre la pantalla y el POST.
type intakeDiscardRequest struct {
	IntakeIDs []string `json:"intake_ids"`
}

// intakeDiscardSkipDTO (discardSkipDTO en la cara vieja) es UNA solicitud que no se descartó,
// con su razón. Las razones son las claves del dominio (intakes.DiscardSkip*) y viajan tal cual:
// la pantalla las traduce a la voz del dueño, el contrato no.
type intakeDiscardSkipDTO struct {
	IntakeID string `json:"intake_id"`
	Reason   string `json:"reason"`
}

// intakeDiscardResponse (discardIntakesResponse en la cara vieja) es el contrato del 200: qué se
// descartó y qué no.
//
// Un lote MIXTO —unos válidos y otros no— es el caso NORMAL, no el excepcional, y
// por eso la respuesta tiene dos listas en vez de un código de error: descartar
// veinte huérfanos no puede fallar entero porque uno de ellos ya lo estuviera.
type intakeDiscardResponse struct {
	Discarded []string               `json:"discarded"`
	Skipped   []intakeDiscardSkipDTO `json:"skipped"`
}

// intakeDiscardHandler (discardIntakesHandler en la cara vieja) sirve POST
// /api/v1/intakes/discard: el DESCARTE MANUAL por lotes del pedido huérfano (T4.8, REQ-32 /
// D-041.18). 200 con el desglose por ítem; 400 solo por cuerpo malformado, lista vacía o más de
// intakes.MaxDiscardBatch ids.
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
// del 043, así que no hay evento que cerrar.
//
// ✅ ACTUALIZADO 2026-08-27 (Plan 044 · T4.8): el filtro `orphan=true` que esta
// cabecera difería YA ESTÁ, y donde le tocaba — en el LISTADO (parseIntakeFilter,
// `Filter.Orphan`), no aquí. Este endpoint sigue sin aceptar un filtro y eso NO es
// una carencia: es intakeDiscardRequest, arriba.
func intakeDiscardHandler(svc IntakeService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req intakeDiscardRequest
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
			writeJSON(w, http.StatusOK, intakeToDiscardResponse(res))
		}
	})
}

// intakeToDiscardResponse (toDiscardResponse en la cara vieja) proyecta el resultado del lote al
// wire. Las dos listas salen SIEMPRE, también vacías (`[]`, nunca `null`): quien pinta la
// pantalla no tiene que ramificar por el nulo para decir "no se descartó nada".
func intakeToDiscardResponse(res intakes.DiscardResult) intakeDiscardResponse {
	skipped := make([]intakeDiscardSkipDTO, 0, len(res.Skipped))
	for _, s := range res.Skipped {
		skipped = append(skipped, intakeDiscardSkipDTO{IntakeID: s.IntakeID, Reason: s.Reason})
	}
	discarded := res.Discarded
	if discarded == nil {
		discarded = []string{}
	}
	return intakeDiscardResponse{Discarded: discarded, Skipped: skipped}
}
