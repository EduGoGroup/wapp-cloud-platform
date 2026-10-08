//go:build pendiente

package apipublica_test

// intakes_approve_test.go — G5 (POST …/{id}/approve) y G6 (POST …/{id}/request-info) del contrato
// de MountIntakes: las dos puertas por las que el DUEÑO le habla al cliente. Son la única llamada
// a Approve y a RequestInfo de toda la cara (INV-1; el candado AST vive en
// internal/modulos/solicitudes/intakes/inv1_aprobar_test.go). Los dobles están en intakes_test.go.

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

const (
	intakeApproveTarget = intakeTarget + "/approve"
	intakeAskTarget     = intakeTarget + "/request-info"

	intakeMsgNoQuoteText = "rendered_text es obligatorio: es el texto de la cotización que se le manda al cliente"
	intakeMsgNoQuestion  = "question es obligatoria: es la pregunta que se le manda al cliente, y jamás sale sola"
	intakeTransition422  = `{"error":"invalid_transition","status":"confirmed","requested":"needs_info","allowed":["cancelled","settled"]}`
)

// intakeOwnerDoors son las dos puertas, con la clave de su cuerpo y el método que invocan.
var intakeOwnerDoors = []struct{ id, target, key, call string }{
	{"G5", intakeApproveTarget, "rendered_text", "Approve"},
	{"G6", intakeAskTarget, "question", "RequestInfo"},
}

// TestMountIntakes_OwnerDoors_PassTheOwnersTextAsIs: el texto del dueño llega al servicio TAL
// CUAL —sin recortar ni sanear: es lo que se le manda al cliente— y cada puerta invoca SU método
// una sola vez y ningún otro (aprobar no pregunta, preguntar no aprueba, ninguna mueve el estado
// por su cuenta).
func TestMountIntakes_OwnerDoors_PassTheOwnersTextAsIs(t *testing.T) {
	for _, door := range intakeOwnerDoors {
		for body, want := range map[string]string{
			`{"` + door.key + `":"  Hola Ana,\nson $9.00 — ¿confirmas?  "}`:               "  Hola Ana,\nson $9.00 — ¿confirmas?  ",
			`{"` + door.key + `":"x","items":[{"sku":"A"}],"total":1,"status":"settled"}`: "x",
		} {
			svc := &intakeServiceSpy{detail: intakeDetailFixture()}
			rec := intakeDo(t, svc, http.MethodPost, door.target, body)
			wantCode(t, door.id, rec, http.StatusOK)
			wantExactBody(t, door.id, rec, intakeDetailBody)
			intakeWantCalls(t, door.id, svc, door.call)
			if svc.text != want || svc.id != intakeID {
				t.Errorf("%s: %s(id=%q, texto=%q); quiero id %q y el texto %q", door.id, door.call, svc.id, svc.text, intakeID, want)
			}
		}
	}
}

// TestMountIntakes_OwnerDoors_TheServiceDecidesWhatIsEmpty: la clave ausente, la cadena vacía y
// el null significan lo mismo —no hay nada que mandar— y llegan al servicio como "": quien
// decide que eso es un 400 es el dominio. Un {} por un fallo de la UI no puede confirmar un
// pedido ni mandar una pregunta sola. La clave de la OTRA puerta no cuenta.
func TestMountIntakes_OwnerDoors_TheServiceDecidesWhatIsEmpty(t *testing.T) {
	sentinels := map[string]error{"G5": intakes.ErrEmptyQuoteText, "G6": intakes.ErrEmptyQuestion}
	msgs := map[string]string{"G5": intakeMsgNoQuoteText, "G6": intakeMsgNoQuestion}
	for i, door := range intakeOwnerDoors {
		other := intakeOwnerDoors[1-i].key
		for _, body := range []string{`{}`, `null`, `{"` + door.key + `":""}`, `{"` + door.key + `":null}`, `{"` + other + `":"texto"}`} {
			svc := &intakeServiceSpy{err: sentinels[door.id]}
			rec := intakeDo(t, svc, http.MethodPost, door.target, body)
			wantCode(t, door.id+" "+body, rec, http.StatusBadRequest)
			wantErrorBody(t, door.id+" "+body, rec, msgs[door.id])
			intakeWantCalls(t, door.id+" "+body, svc, door.call)
			if svc.text != "" {
				t.Errorf("%s %s: el servicio recibió el texto %q, quiero vacío", door.id, body, svc.text)
			}
		}
	}
}

// TestMountIntakes_OwnerDoors_UnreadableBodyNeverReachesTheService: un cuerpo ilegible es 400 y
// no le escribe a nadie.
func TestMountIntakes_OwnerDoors_UnreadableBodyNeverReachesTheService(t *testing.T) {
	for _, door := range intakeOwnerDoors {
		for _, body := range []string{"", "{no es json", `[]`, `"texto suelto"`, `{"` + door.key + `":7}`, `{"` + door.key + `":["a"]}`, `{"` + door.key + `":{"text":"a"}}`} {
			svc := &intakeServiceSpy{}
			rec := intakeDo(t, svc, http.MethodPost, door.target, body)
			wantCode(t, door.id+" "+body, rec, http.StatusBadRequest)
			wantErrorBody(t, door.id+" "+body, rec, intakeMsgBadJSON)
			intakeWantCalls(t, door.id+" "+body, svc)
		}
	}
}

// TestMountIntakes_Approve_Errors: la política de códigos de G5. El 400 de las líneas sin precio
// las trae TODAS con su posición —la línea que el catálogo no reconoció no tiene sku—, y el 422
// propio dice desde dónde SÍ se aprueba.
func TestMountIntakes_Approve_Errors(t *testing.T) {
	transition := &intakes.TransitionError{From: "confirmed", To: "needs_info", Allowed: []string{"cancelled", "settled"}}
	pending := &intakes.PendingPriceError{Lines: []intakes.PendingPriceLine{{Index: 1, Label: "tequeños"}, {Index: 3, Label: ""}}}
	cases := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"not_found_is_404_never_403", intakes.ErrNotFound, http.StatusNotFound, `{"error":"solicitud no encontrada"}`},
		{"empty_quote_text_is_400", intakes.ErrEmptyQuoteText, http.StatusBadRequest, `{"error":"` + intakeMsgNoQuoteText + `"}`},
		{"lines_without_price_is_400", pending, http.StatusBadRequest,
			`{"error":"lines_without_price","lines":[{"index":1,"label":"tequeños"},{"index":3,"label":""}]}`},
		{"wrapped_lines_without_price", fmt.Errorf("servicio: %w", pending), http.StatusBadRequest,
			`{"error":"lines_without_price","lines":[{"index":1,"label":"tequeños"},{"index":3,"label":""}]}`},
		{"nothing_to_quote_is_400", intakes.ErrEmptyQuote, http.StatusBadRequest,
			`{"error":"la solicitud no tiene líneas que cotizar: guarda primero las líneas del borrador con PUT /api/v1/intakes/{id}/items"}`},
		{"not_approvable_is_422", &intakes.NotApprovableError{Status: "confirmed"}, http.StatusUnprocessableEntity,
			`{"error":"not_approvable","status":"confirmed","approvable_in":["pending_approval"]}`},
		// Cuando alguien la movió entre la validación y la escritura, el 422 útil es el del ciclo
		// de vida y no el de esta puerta.
		{"transition_error_is_the_lifecycle_422", transition, http.StatusUnprocessableEntity, intakeTransition422},
		{"conflict_is_409", intakes.ErrConflict, http.StatusConflict, `{"error":"` + intakeMsgConflict + `"}`},
		// Lo que el dominio no sabe hacer por un mal cableado no es culpa de la petición.
		{"no_quote_sender_is_500", intakes.ErrNoQuoteSender, http.StatusInternalServerError, `{"error":"no se pudo aprobar la solicitud"}`},
		{"other_doors_errors_are_500", intakes.ErrEmptyQuestion, http.StatusInternalServerError, `{"error":"no se pudo aprobar la solicitud"}`},
		{"not_editable_is_500", &intakes.NotEditableError{Status: "confirmed"}, http.StatusInternalServerError, `{"error":"no se pudo aprobar la solicitud"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := intakeDo(t, &intakeServiceSpy{err: tc.err}, http.MethodPost, intakeApproveTarget, `{"rendered_text":"Son $9.00"}`)
			wantCode(t, tc.name, rec, tc.code)
			wantExactBody(t, tc.name, rec, tc.body)
		})
	}
}

// TestMountIntakes_RequestInfo_Errors: la política de códigos de G6. El 422 es el del CICLO DE
// VIDA y no uno propio: esta puerta no estrecha la máquina de estados, así que lo útil es saber
// adónde SÍ puede ir la solicitud.
func TestMountIntakes_RequestInfo_Errors(t *testing.T) {
	transition := &intakes.TransitionError{From: "confirmed", To: "needs_info", Allowed: []string{"cancelled", "settled"}}
	const failed = `{"error":"no se pudo pedir más información sobre la solicitud"}`
	cases := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"not_found_is_404_never_403", intakes.ErrNotFound, http.StatusNotFound, `{"error":"solicitud no encontrada"}`},
		{"empty_question_is_400", intakes.ErrEmptyQuestion, http.StatusBadRequest, `{"error":"` + intakeMsgNoQuestion + `"}`},
		{"transition_error_is_the_lifecycle_422", transition, http.StatusUnprocessableEntity, intakeTransition422},
		{"wrapped_transition_error", fmt.Errorf("servicio: %w", transition), http.StatusUnprocessableEntity, intakeTransition422},
		{"conflict_is_409", intakes.ErrConflict, http.StatusConflict, `{"error":"` + intakeMsgConflict + `"}`},
		// Los rechazos propios de aprobar no tienen código aquí.
		{"not_approvable_is_500", &intakes.NotApprovableError{Status: "confirmed"}, http.StatusInternalServerError, failed},
		{"empty_quote_text_is_500", intakes.ErrEmptyQuoteText, http.StatusInternalServerError, failed},
		{"lines_without_price_is_500", &intakes.PendingPriceError{}, http.StatusInternalServerError, failed},
		{"no_quote_sender_is_500", intakes.ErrNoQuoteSender, http.StatusInternalServerError, failed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := intakeDo(t, &intakeServiceSpy{err: tc.err}, http.MethodPost, intakeAskTarget, `{"question":"¿Para cuándo?"}`)
			wantCode(t, tc.name, rec, tc.code)
			wantExactBody(t, tc.name, rec, tc.body)
		})
	}
}
