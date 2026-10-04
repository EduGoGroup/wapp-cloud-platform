package iamhttp

// Una aserción por promesa de R-H4 y R-H5 (canje.go). El anti-oráculo se comprueba comparando
// BYTES (bytes.Equal), no leyendo los dos mensajes: un test que afirmara cada cuerpo por separado
// seguiría verde el día que a uno le añadieran una coma.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// fakeRedeemer devuelve siempre el mismo error (o nil) y guarda lo que recibió.
type fakeRedeemer struct {
	err      error
	received string
	calls    int
}

var _ in.InvitationRedeemer = (*fakeRedeemer)(nil)

func (r *fakeRedeemer) RedeemInvitation(_ context.Context, token string) error {
	r.calls++
	r.received = token
	return r.err
}

func redeem(t *testing.T, r *fakeRedeemer, body string) *httptest.ResponseRecorder {
	t.Helper()
	return serve(t, newRedeemHandler(r).Accept(), http.MethodPost, "/api/v1/invitations/accept", body)
}

// newRedeemHandler construye el handler sobre el doble.
func newRedeemHandler(r *fakeRedeemer) *InvitationRedeemHandler {
	return NewInvitationRedeemHandler(r)
}

// R-H4: «no existe» (404) y «caducada» (410) dan bytes idénticos y el mismo Content-Type.
func TestAccept_NotFoundAndExpiredAreIndistinguishable(t *testing.T) {
	notFound := redeem(t, &fakeRedeemer{err: domain.ErrNotFound}, `{"token":"WAPP-INV-0123"}`)
	expired := redeem(t, &fakeRedeemer{err: domain.ErrInvitationExpired}, `{"token":"WAPP-INV-0123"}`)

	if notFound.Code != http.StatusNotFound || expired.Code != http.StatusGone {
		t.Errorf("códigos = %d y %d; se esperaban 404 y 410", notFound.Code, expired.Code)
	}
	a, b := notFound.Body.Bytes(), expired.Body.Bytes()
	if !bytes.Equal(a, b) {
		t.Errorf("los cuerpos DIFIEREN, y eso es un oráculo:\n  404: %s\n  410: %s", a, b)
	}
	if ca, cb := notFound.Header().Get("Content-Type"), expired.Header().Get("Content-Type"); ca != cb {
		t.Errorf("Content-Type distinto (%q / %q): el mismo cuerpo servido de dos formas sigue distinguiendo", ca, cb)
	}
	// Anti-hueco: dos cuerpos vacíos también serían «iguales».
	if string(a) != errorBody("esa invitación no se puede usar") {
		t.Errorf("cuerpo compartido = %s; se esperaba %s", a, errorBody("esa invitación no se puede usar"))
	}
	for _, leak := range []string{"caduc", "expir", "no encontr", "not found", "vencid", "revocad"} {
		if strings.Contains(strings.ToLower(string(a)), leak) {
			t.Errorf("el cuerpo compartido dice %q: nombra la causa", leak)
		}
	}
}

// R-H4: el 409 tiene voz propia, distinta del par.
func TestAccept_ConflictHasItsOwnBody(t *testing.T) {
	pair := redeem(t, &fakeRedeemer{err: domain.ErrNotFound}, `{"token":"x"}`)
	conflict := redeem(t, &fakeRedeemer{err: domain.ErrConflict}, `{"token":"x"}`)
	want := errorBody("esa invitación ya no está disponible, o esta cuenta ya pertenece a una empresa")
	if conflict.Code != http.StatusConflict || conflict.Body.String() != want {
		t.Errorf("respuesta = %d %s; se esperaba 409 %s", conflict.Code, conflict.Body.String(), want)
	}
	if bytes.Equal(pair.Body.Bytes(), conflict.Body.Bytes()) {
		t.Errorf("el 409 responde el cuerpo mudo del par: %s", conflict.Body.String())
	}
}

// R-H4: los demás desenlaces.
func TestAccept_OtherOutcomes(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		body      string
		status    int
		wantBody  string
		wantCalls int
	}{
		{"redeemed_is_204_without_body", nil, `{"token":"WAPP-INV-ok"}`, http.StatusNoContent, "", 1},
		{"missing_token_is_400", domain.ErrInvalidInput, `{"token":""}`, http.StatusBadRequest, errorBody("falta el token de invitación"), 1},
		{"broken_json_is_400_without_port", nil, `{`, http.StatusBadRequest, errorBody("cuerpo JSON inválido"), 0},
		{"infra_is_500", errors.New("la base se cayó"), `{"token":"x"}`, http.StatusInternalServerError, errorBody("error interno"), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeRedeemer{err: c.err}
			rec := redeem(t, r, c.body)
			if rec.Code != c.status || rec.Body.String() != c.wantBody {
				t.Errorf("respuesta = %d %q; se esperaba %d %q", rec.Code, rec.Body.String(), c.status, c.wantBody)
			}
			if r.calls != c.wantCalls {
				t.Errorf("llamadas al puerto = %d; se esperaban %d", r.calls, c.wantCalls)
			}
		})
	}
}

// R-H4: el token viaja tal cual (sin recortar ni normalizar). R-H5: un tenant_id en el cuerpo
// no cambia lo que recibe el puerto.
func TestAccept_TokenTravelsAsIs(t *testing.T) {
	const noisy = "  wapp-inv-abc123  "
	r := &fakeRedeemer{}
	rec := redeem(t, r, `{"token":"`+noisy+`","tenant_id":"`+tenantB+`"}`)
	if rec.Code != http.StatusNoContent || r.calls != 1 || r.received != noisy {
		t.Errorf("código = %d, llamadas = %d, token = %q; se esperaba 204, 1 y %q tal cual", rec.Code, r.calls, r.received, noisy)
	}
}

// R-H5, la mitad estructural (nace en el verde: el DTO es no exportado): el cuerpo del canje
// tiene EXACTAMENTE la clave token, y un tenant_id no sobrevive a un round-trip.
func TestRedeemInvitationRequest_OnlyCarriesToken(t *testing.T) {
	if got := jsonFieldNames(redeemInvitationRequest{}); !slices.Equal(got, []string{"token"}) {
		t.Fatalf("el cuerpo del canje acepta %v; solo puede aceptar [token]: la empresa sale de la FILA de la "+
			"invitación, nunca del cuerpo (INV-04)", got)
	}
	var req redeemInvitationRequest
	if err := json.Unmarshal([]byte(`{"token":"WAPP-INV-abc","tenant_id":"`+tenantA+`"}`), &req); err != nil {
		t.Fatalf("decodificar: %v", err)
	}
	back, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("serializar: %v", err)
	}
	if req.Token != "WAPP-INV-abc" || strings.Contains(string(back), "tenant") {
		t.Errorf("token %q, round-trip %s; se esperaba el token y nada de tenant", req.Token, back)
	}
}

// indistinguishableCode: solo el par (no existe / caducada) es indistinguible, también envuelto;
// todo lo demás —incluido el 409— sigue por el switch con su voz propia.
func TestIndistinguishableCode(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantOK   bool
	}{
		{"not_found_is_404", domain.ErrNotFound, http.StatusNotFound, true},
		{"expired_is_410", domain.ErrInvitationExpired, http.StatusGone, true},
		{"wrapped_expired_is_410", fmt.Errorf("canje: %w", domain.ErrInvitationExpired), http.StatusGone, true},
		{"conflict_is_not_in_the_pair", domain.ErrConflict, 0, false},
		{"nil_is_not_in_the_pair", nil, 0, false},
		{"infra_is_not_in_the_pair", errors.New("caído"), 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, ok := indistinguishableCode(c.err)
			if code != c.wantCode || ok != c.wantOK {
				t.Errorf("indistinguishableCode = (%d, %v); se esperaba (%d, %v)", code, ok, c.wantCode, c.wantOK)
			}
		})
	}
}
