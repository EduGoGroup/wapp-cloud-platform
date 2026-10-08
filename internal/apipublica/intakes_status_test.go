//go:build pendiente

package apipublica_test

// intakes_status_test.go — G3 (POST …/{id}/status) y G8 (POST /api/v1/intakes/discard) del
// contrato de MountIntakes: las dos puertas que mueven el ESTADO de una solicitud, y por qué son
// dos. Los dobles están en intakes_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

const (
	intakeStatusTarget  = intakeTarget + "/status"
	intakeDiscardTarget = intakesTarget + "/discard"
)

// TestMountIntakes_Status_PassesTheStatusAndAlwaysNoticeToClient: el estado pedido llega al
// servicio TAL CUAL —validar la transición es del dominio— y el aviso es siempre NoticeToClient:
// esta puerta no escribe texto propio, así que nada del cuerpo puede silenciar al cliente.
func TestMountIntakes_Status_PassesTheStatusAndAlwaysNoticeToClient(t *testing.T) {
	for _, tc := range []struct{ body, status string }{
		{`{"status":"confirmed"}`, "confirmed"},
		{`{"status":"closed"}`, "closed"},
		{`{"status":"expired"}`, "expired"},
		{`{"status":" Confirmed "}`, " Confirmed "},
		{`{"status":"deposit_requested","notice":"by_caller","tenant_id":"otro","id":"otro"}`, "deposit_requested"},
	} {
		svc := &intakeServiceSpy{header: intakeDue()}
		rec := intakeDo(t, svc, http.MethodPost, intakeStatusTarget, tc.body)
		wantCode(t, tc.body, rec, http.StatusOK)
		intakeWantCalls(t, tc.body, svc, "SetStatus")
		if svc.status != tc.status || svc.notice != intakes.NoticeToClient || svc.id != intakeID {
			t.Errorf("%s: SetStatus(id=%q, status=%q, notice=%d); quiero id %q, status %q y NoticeToClient",
				tc.body, svc.id, svc.status, svc.notice, intakeID, tc.status)
		}
	}
}

// TestMountIntakes_Status_BadBodies: sin `status` no hay nada que aplicar, y ningún 400 llega al
// servicio.
func TestMountIntakes_Status_BadBodies(t *testing.T) {
	const missing = "status es obligatorio"
	for body, msg := range map[string]string{
		"":                      intakeMsgBadJSON,
		"{no es json":           intakeMsgBadJSON,
		`[]`:                    intakeMsgBadJSON,
		`"confirmed"`:           intakeMsgBadJSON,
		`{"status":7}`:          intakeMsgBadJSON,
		`{"status":["open"]}`:   intakeMsgBadJSON,
		`{}`:                    missing,
		`null`:                  missing,
		`{"status":""}`:         missing,
		`{"status":null}`:       missing,
		`{"estado":"settled"}`:  missing,
		`{"STATUS ":"settled"}`: missing,
	} {
		svc := &intakeServiceSpy{}
		rec := intakeDo(t, svc, http.MethodPost, intakeStatusTarget, body)
		wantCode(t, body, rec, http.StatusBadRequest)
		wantErrorBody(t, body, rec, msg)
		intakeWantCalls(t, body, svc)
	}
}

// TestMountIntakes_Status_Errors: la política de códigos de G3. El 422 dice dónde está la
// solicitud y adónde SÍ puede ir: sin `allowed`, el llamante adivinaría el ciclo de vida a base
// de reintentos.
func TestMountIntakes_Status_Errors(t *testing.T) {
	transition := &intakes.TransitionError{From: "pending_approval", To: "settled", Allowed: []string{"cancelled", "confirmed", "needs_info", "rejected"}}
	cases := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"not_found_is_404_never_403", intakes.ErrNotFound, http.StatusNotFound, `{"error":"solicitud no encontrada"}`},
		{"wrapped_not_found", fmt.Errorf("leyendo: %w", intakes.ErrNotFound), http.StatusNotFound, `{"error":"solicitud no encontrada"}`},
		{"invalid_transition_is_422", transition, http.StatusUnprocessableEntity,
			`{"error":"invalid_transition","status":"pending_approval","requested":"settled","allowed":["cancelled","confirmed","needs_info","rejected"]}`},
		{"wrapped_transition", fmt.Errorf("servicio: %w", transition), http.StatusUnprocessableEntity,
			`{"error":"invalid_transition","status":"pending_approval","requested":"settled","allowed":["cancelled","confirmed","needs_info","rejected"]}`},
		{"terminal_has_no_destinations", &intakes.TransitionError{From: "expired", To: "abandoned", Allowed: []string{}}, http.StatusUnprocessableEntity,
			`{"error":"invalid_transition","status":"expired","requested":"abandoned","allowed":[]}`},
		{"conflict_is_409", intakes.ErrConflict, http.StatusConflict, `{"error":"` + intakeMsgConflict + `"}`},
		// Los errores de OTRAS puertas no tienen código propio aquí.
		{"other_doors_errors_are_500", &intakes.NotEditableError{Status: "confirmed"}, http.StatusInternalServerError,
			`{"error":"no se pudo cambiar el estado de la solicitud"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &intakeServiceSpy{err: tc.err}
			rec := intakeDo(t, svc, http.MethodPost, intakeStatusTarget, `{"status":"settled"}`)
			wantCode(t, tc.name, rec, tc.code)
			wantExactBody(t, tc.name, rec, tc.body)
		})
	}
}

// TestMountIntakes_Discard_MixedBatchIsA200: un lote mixto es el caso NORMAL. Los ids llegan al
// servicio tal cual —repetidos y en su orden— y cada rechazo sale con la razón del dominio.
func TestMountIntakes_Discard_MixedBatchIsA200(t *testing.T) {
	svc := &intakeServiceSpy{discard: intakes.DiscardResult{
		Discarded: []string{"a", "c"},
		Skipped: []intakes.DiscardSkip{
			{IntakeID: "b", Reason: intakes.DiscardSkipNotFound},
			{IntakeID: "d", Reason: intakes.DiscardSkipAlreadyDiscarded},
			{IntakeID: "e", Reason: intakes.DiscardSkipNotOpen},
			{IntakeID: "f", Reason: intakes.DiscardSkipLiveEvent},
		},
	}}
	rec := intakeDo(t, svc, http.MethodPost, intakeDiscardTarget, `{"intake_ids":["a","b","c","a","d","e","f"],"status":"open","tenant_id":"otro"}`)
	wantCode(t, "G8", rec, http.StatusOK)
	wantExactBody(t, "G8", rec, `{"discarded":["a","c"],"skipped":[{"intake_id":"b","reason":"not_found"},`+
		`{"intake_id":"d","reason":"already_discarded"},{"intake_id":"e","reason":"not_open"},{"intake_id":"f","reason":"live_event"}]}`)
	if want := []string{"a", "b", "c", "a", "d", "e", "f"}; !slices.Equal(svc.ids, want) {
		t.Errorf("Discard recibió %q, quiero %q (tal cual: colapsar repetidos es del dominio)", svc.ids, want)
	}
}

// TestMountIntakes_Discard_BothListsAreAlwaysArrays: [] y nunca null, también si el servicio
// devuelve las listas nil.
func TestMountIntakes_Discard_BothListsAreAlwaysArrays(t *testing.T) {
	for name, res := range map[string]intakes.DiscardResult{
		"nil_lists":   {},
		"empty_lists": {Discarded: []string{}, Skipped: []intakes.DiscardSkip{}},
	} {
		rec := intakeDo(t, &intakeServiceSpy{discard: res}, http.MethodPost, intakeDiscardTarget, `{"intake_ids":["a"]}`)
		wantCode(t, name, rec, http.StatusOK)
		wantExactBody(t, name, rec, `{"discarded":[],"skipped":[]}`)
	}
}

// TestMountIntakes_Discard_Rejections: los tres únicos 400 de la puerta. Un cuerpo ilegible no
// llega al servicio; el lote vacío y el que se pasa los decide el dominio. Y NO hay 404 ni 422:
// un lote no tiene UN recurso ni UN estado.
func TestMountIntakes_Discard_Rejections(t *testing.T) {
	for _, body := range []string{"", "{no es json", `[]`, `["a","b"]`, `{"intake_ids":"a"}`, `{"intake_ids":[1,2]}`, `{"intake_ids":{"a":1}}`} {
		svc := &intakeServiceSpy{}
		rec := intakeDo(t, svc, http.MethodPost, intakeDiscardTarget, body)
		wantCode(t, body, rec, http.StatusBadRequest)
		wantErrorBody(t, body, rec, intakeMsgBadJSON)
		intakeWantCalls(t, body, svc)
	}

	// Sin ids (clave ausente, null o lista vacía) el servicio recibe el lote vacío y lo rechaza.
	for _, body := range []string{`{}`, `null`, `{"intake_ids":null}`, `{"intake_ids":[]}`, `{"ids":["a"]}`} {
		svc := &intakeServiceSpy{err: intakes.ErrEmptyDiscardBatch}
		rec := intakeDo(t, svc, http.MethodPost, intakeDiscardTarget, body)
		wantCode(t, body, rec, http.StatusBadRequest)
		wantErrorBody(t, body, rec, "intake_ids es obligatorio: manda entre 1 y 200 ids")
		intakeWantCalls(t, body, svc, "Discard")
		if len(svc.ids) != 0 {
			t.Errorf("%s: Discard recibió %q, quiero el lote vacío", body, svc.ids)
		}
	}

	cases := map[string]struct {
		err  error
		code int
		msg  string
	}{
		"too_large_batch":       {&intakes.TooLargeBatchError{Count: 201, Max: 200}, http.StatusBadRequest, "el lote trae 201 solicitudes y el máximo es 200"},
		"wrapped_too_large":     {fmt.Errorf("servicio: %w", &intakes.TooLargeBatchError{Count: 999, Max: 7}), http.StatusBadRequest, "el lote trae 999 solicitudes y el máximo es 7"},
		"not_found_is_not_404":  {intakes.ErrNotFound, http.StatusInternalServerError, "no se pudieron descartar las solicitudes"},
		"transition_is_not_422": {&intakes.TransitionError{From: "expired", To: "abandoned"}, http.StatusInternalServerError, "no se pudieron descartar las solicitudes"},
		"conflict_is_not_409":   {intakes.ErrConflict, http.StatusInternalServerError, "no se pudieron descartar las solicitudes"},
	}
	for name, tc := range cases {
		rec := intakeDo(t, &intakeServiceSpy{err: tc.err}, http.MethodPost, intakeDiscardTarget, `{"intake_ids":["a"]}`)
		wantCode(t, name, rec, tc.code)
		wantErrorBody(t, name, rec, tc.msg)
	}
}

// TestMountIntakes_ExpiredIsDiscardedOnlyThroughTheBatchDoor: la decisión de Jhoan del
// 2026-08-06, extremo a extremo con el servicio REAL del módulo. La MISMA pareja de estados
// (`expired → abandoned`) se rechaza con 422 por el cambio de estado y se acepta por el descarte
// por lotes; un id de OTRO tenant sale `not_found`, indistinguible de uno inexistente; y repetir
// el lote no falla. Si alguien «unifica» el descarte sobre SetStatus, la primera mitad se cae.
func TestMountIntakes_ExpiredIsDiscardedOnlyThroughTheBatchDoor(t *testing.T) {
	store := intakes.NewMemoryStore()
	store.SetClock(intakeClock)
	expired := intakeDue()
	expired.ID, expired.Status = "exp-1", intakes.StatusExpired
	foreign := intakeDue()
	foreign.ID, foreign.Status = "ajena-1", intakes.StatusOpen
	store.Add(tenantA, expired)
	store.Add(apipublicahelpertest.TenantB, foreign)

	h := apipublicahelpertest.New(t)
	cara := intakeCara(h.Common(), intakeDeps(intakes.NewService(store), entitlements.FeatureCartBasic))
	token := h.With(tenantA, intakeWritePerm)

	rec := h.Call(cara, token, http.MethodPost, intakesTarget+"/exp-1/status", `{"status":"abandoned"}`)
	wantCode(t, "expired → abandoned por G3", rec, http.StatusUnprocessableEntity)
	wantExactBody(t, "expired → abandoned por G3", rec, `{"error":"invalid_transition","status":"expired","requested":"abandoned","allowed":[]}`)
	if got := store.StoredStatus(tenantA, "exp-1"); got != intakes.StatusExpired {
		t.Fatalf("el 422 movió la solicitud a %q", got)
	}

	const batch = `{"intake_ids":["exp-1","ajena-1","no-existe"]}`
	rec = h.Call(cara, token, http.MethodPost, intakeDiscardTarget, batch)
	wantCode(t, "descarte", rec, http.StatusOK)
	wantExactBody(t, "descarte", rec, `{"discarded":["exp-1"],"skipped":[{"intake_id":"ajena-1","reason":"not_found"},{"intake_id":"no-existe","reason":"not_found"}]}`)
	if got := store.StoredStatus(tenantA, "exp-1"); got != intakes.StatusAbandoned {
		t.Errorf("tras el descarte la solicitud está en %q, quiero abandoned", got)
	}
	if got := store.StoredStatus(apipublicahelpertest.TenantB, "ajena-1"); got != intakes.StatusOpen {
		t.Errorf("el descarte del tenant A movió la solicitud del B a %q", got)
	}

	rec = h.Call(cara, token, http.MethodPost, intakeDiscardTarget, batch)
	wantCode(t, "descarte repetido", rec, http.StatusOK)
	var again struct {
		Discarded []string `json:"discarded"`
		Skipped   []struct {
			IntakeID string `json:"intake_id"`
			Reason   string `json:"reason"`
		} `json:"skipped"`
	}
	wantJSON(t, "descarte repetido", rec, &again)
	if len(again.Discarded) != 0 || len(again.Skipped) != 3 || again.Skipped[0].Reason != intakes.DiscardSkipAlreadyDiscarded {
		t.Errorf("descarte repetido = %+v; quiero nada descartado y exp-1 como already_discarded", again)
	}
	// Lo descartado NO desaparece: sigue en la bandeja, terminal y sin destinos.
	detail, err := store.Get(context.Background(), tenantA, "exp-1")
	if err != nil || detail.Status != intakes.StatusAbandoned {
		t.Fatalf("la solicitud descartada ya no se lee: %+v, %v", detail.Intake, err)
	}
	rec = h.Call(cara, h.With(tenantA, intakeReadPerm), http.MethodGet, intakesTarget+"/exp-1", "")
	var body map[string]json.RawMessage
	wantJSON(t, "detalle de la descartada", rec, &body)
	if string(body["status"]) != `"abandoned"` || string(body["allowed_transitions"]) != "[]" {
		t.Errorf("detalle de la descartada: status %s y allowed_transitions %s; quiero abandoned y []", body["status"], body["allowed_transitions"])
	}
}
