//go:build integracion

package procesos

import (
	"net/http"
	"testing"
)

// La mitad «lo que no entra» del login de operador de P1: la tabla de códigos de error, y el
// refresh y el logout con sus propios rechazos. El escenario, la concurrencia y el cierre están en
// p1_enrolamiento_operator_login_test.go.
//
// Los códigos son CONTRATO con el Edge, que los mapea a mensajes de la consola: por eso van aquí
// como literales y no como constantes compartidas con el servidor. El mismo literal tiene que salir
// del binario viejo y del nuevo (hallazgo 69 de F3).

// p1LoginErrorCase es un login que la nube tiene que rechazar: por qué puesto entra, con qué
// credenciales, el código exacto de la respuesta, el actor de su fila de auditoría (el id de la
// persona si llegó a resolverse; vacío si no) y lo que identity contestó (0 = ni se le preguntó).
type p1LoginErrorCase struct {
	name            string
	seat            *p1LoginSeat
	email, password string
	code            string
	actor           string
	identityStatus  int
}

// checkErrorCodes es el paso 3: cada rechazo alcanzable de caja negra, uno tras otro. Los cinco
// primeros los decide la nube sola o con el 401 de identity; los dos de empresa cruzada son
// credenciales BUENAS por el cable de otra empresa, en los dos sentidos; el resto es identity
// diciendo que no por algo que no son las credenciales (el guion del doble), que es donde vivía la
// traducción que el binario nuevo ya no hace con un adaptador.
func (p *p1LoginRun) checkErrorCodes(t *testing.T) {
	id := p.s.Identidad
	// outsider existe en identity pero no es miembro de ninguna empresa; los cuatro siguientes son
	// miembros de A a los que identity rechaza con un código fijo aunque acierten la contraseña.
	outsider := p.newOperator(t, "outsider")
	id.registerOperator(t, outsider.email, outsider.password, outsider.userID)
	scripted := map[int]p1LoginOperator{}
	for _, status := range []int{http.StatusForbidden, http.StatusBadRequest, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		op := p.newOperator(t, "scripted")
		edgeAltaAdminDelTenant(t, p.db, op.userID, p.tenantA)
		id.scriptLoginStatus(t, op.email, op.password, op.userID, status)
		scripted[status] = op
	}
	a, b := p.a1, p.b1
	cases := []p1LoginErrorCase{
		{"wrong_password", a, a.op.email, a.op.password + "x", "invalid_credentials", "", http.StatusUnauthorized},
		{"unknown_email", a, "nadie-" + a.op.email, a.op.password, "invalid_credentials", "", http.StatusUnauthorized},
		{"empty_email", a, "", a.op.password, "invalid_input", "", 0},
		{"empty_password", a, a.op.email, "", "invalid_input", "", 0},
		{"empty_email_and_password", b, "", "", "invalid_input", "", 0},
		{"operator_of_B_through_edge_of_A", a, b.op.email, b.op.password, "tenant_mismatch", b.op.userID, http.StatusOK},
		{"operator_of_A_through_edge_of_B", b, a.op.email, a.op.password, "tenant_mismatch", a.op.userID, http.StatusOK},
		{"user_without_any_tenant", a, outsider.email, outsider.password, "tenant_mismatch", outsider.userID, http.StatusOK},
		{"identity_403_app_not_granted", a, scripted[403].email, scripted[403].password, "user_inactive", "", http.StatusForbidden},
		{"identity_400", a, scripted[400].email, scripted[400].password, "invalid_input", "", http.StatusBadRequest},
		{"identity_503", a, scripted[503].email, scripted[503].password, "internal", "", http.StatusServiceUnavailable},
		{"identity_500", a, scripted[500].email, scripted[500].password, "internal", "", http.StatusInternalServerError},
	}
	for _, c := range cases {
		before := len(id.loginCalls())
		r, err := p.login(t, c.seat, edgeSesionControl, c.email, c.password)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		p1LoginWantError(t, c.name, r, c.code)
		p.wantAudit(t, c.seat, "edge.auth.login", "error", c.actor, edgeSesionControl, r.CommandID)
		calls := id.loginCalls()[before:]
		switch {
		case c.identityStatus == 0 && len(calls) != 0:
			t.Errorf("%s: la nube le preguntó a identity (%+v) por una entrada que tenía que rechazar sola", c.name, calls)
		case c.identityStatus != 0 && (len(calls) != 1 || calls[0].Status != c.identityStatus || calls[0].System != "wapp.edge"):
			t.Errorf("%s: llamadas a identity %+v; quería un login con system wapp.edge contestado con %d", c.name, calls, c.identityStatus)
		}
	}
	// Un rechazo no deja al operador legítimo fuera: tras toda la tabla, cada uno sigue entrando.
	for _, seat := range []*p1LoginSeat{a, b} {
		r, err := p.login(t, seat, edgeSesionControl, seat.op.email, seat.op.password)
		if err != nil {
			t.Fatal(err)
		}
		p.wantOwnTokens(t, seat, edgeSesionControl, r)
	}
}

// p1LoginWantError afirma que una respuesta de auth es el error code y nada más: la rama de error,
// ese código exacto, sin mensaje (el detalle no viaja: no hay oráculo), sin un solo token, y con el
// command_id y la sesión de control en el sobre y dentro.
func p1LoginWantError(t *testing.T, name string, r edgeAuthReply, code string) {
	t.Helper()
	if !r.IsError || r.Code != code || r.Message != "" {
		t.Errorf("%s: error=%v código %q mensaje %q; quería el error %q sin mensaje", name, r.IsError, r.Code, r.Message, code)
	}
	if r.AccessToken != "" || r.RefreshToken != "" || r.TokenType != "" || r.ExpiresAt != 0 {
		t.Errorf("%s: un rechazo trae tokens (tipo %q, expires_at %d)", name, r.TokenType, r.ExpiresAt)
	}
	if r.InnerCommandID != r.CommandID || r.SessionID != edgeSesionControl || r.InnerSessionID != edgeSesionControl {
		t.Errorf("%s: la respuesta no correlaciona: sobre %s/%s, interior %s/%s", name, r.CommandID, r.SessionID, r.InnerCommandID, r.InnerSessionID)
	}
}

// checkRefreshAndLogout es el paso 4: las otras dos peticiones del canal de control. El refresh
// rota el par y sigue siendo del mismo operador y la misma empresa; un refresh inventado o vacío se
// rechaza; y un refresh BUENO de la empresa A, replayado por el Edge de la empresa B, no entrega
// nada (el mismo guard de empresa cruzada que el login). El logout revoca y contesta por la rama de
// tokens con todo vacío —la convención del contrato—, las veces que se pida.
func (p *p1LoginRun) checkRefreshAndLogout(t *testing.T) {
	a, b := p.a1, p.b1
	first, err := p.login(t, a, edgeSesionControl, a.op.email, a.op.password)
	if err != nil {
		t.Fatal(err)
	}
	p.wantOwnTokens(t, a, edgeSesionControl, first)

	rotated := p.refresh(t, a, first.RefreshToken)
	p.wantOwnTokens(t, a, edgeSesionControl, rotated)
	p.wantAudit(t, a, "edge.auth.refresh", "ok", a.op.userID, edgeSesionControl, rotated.CommandID)
	if _, alive := p.s.Identidad.refreshOwner(first.RefreshToken); alive || rotated.RefreshToken == first.RefreshToken {
		t.Errorf("el refresh no rotó: el token viejo sigue vivo (%v) o volvió el mismo", alive)
	}

	for _, c := range []struct{ name, token, code string }{
		{"refresh_already_rotated", first.RefreshToken, "refresh_invalid"},
		{"refresh_made_up", "rt-inventado", "refresh_invalid"},
		{"refresh_empty", "", "invalid_input"},
	} {
		r := p.refresh(t, a, c.token)
		p1LoginWantError(t, c.name, r, c.code)
		p.wantAudit(t, a, "edge.auth.refresh", "error", "", edgeSesionControl, r.CommandID)
	}

	// El refresh vivo del operador de A, por el cable de B: identity lo da por bueno (no conoce
	// empresas) y es la nube la que no entrega el par. La auditoría lo anota en B, con su nombre.
	replayed := p.refresh(t, b, rotated.RefreshToken)
	p1LoginWantError(t, "refresh_of_A_through_edge_of_B", replayed, "tenant_mismatch")
	p.wantAudit(t, b, "edge.auth.refresh", "error", a.op.userID, edgeSesionControl, replayed.CommandID)

	session, err := p.login(t, a, edgeSesionControl, a.op.email, a.op.password)
	if err != nil {
		t.Fatal(err)
	}
	p.wantOwnTokens(t, a, edgeSesionControl, session)
	for range 2 { // idempotente: el segundo logout del mismo token contesta igual
		out := p.logout(t, a, session.RefreshToken)
		if out.IsError || out.AccessToken != "" || out.RefreshToken != "" || out.TokenType != "" || out.ExpiresAt != 0 ||
			out.InnerCommandID != out.CommandID || out.SessionID != edgeSesionControl {
			t.Errorf("logout = %+v; quería la rama de tokens, vacía, con su command_id", out)
		}
		p.wantAudit(t, a, "edge.auth.logout", "ok", "", edgeSesionControl, out.CommandID)
	}
	if _, alive := p.s.Identidad.refreshOwner(session.RefreshToken); alive {
		t.Errorf("tras el logout, el refresh token sigue vivo en identity")
	}
	empty := p.logout(t, a, "")
	p1LoginWantError(t, "logout_empty", empty, "invalid_input")
	p.wantAudit(t, a, "edge.auth.logout", "error", "", edgeSesionControl, empty.CommandID)
}

// refresh relaya un UserRefresh por el canal de control del puesto y devuelve su respuesta. Falla
// (t.Fatalf) si no llega.
func (p *p1LoginRun) refresh(t *testing.T, seat *p1LoginSeat, refreshToken string) edgeAuthReply {
	t.Helper()
	r, err := p.do(seat, func(e *edge) (edgeAuthReply, error) { return e.userRefresh(t.Context(), refreshToken) })
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// logout relaya un UserLogout por el canal de control del puesto y devuelve su respuesta. Falla
// (t.Fatalf) si no llega.
func (p *p1LoginRun) logout(t *testing.T, seat *p1LoginSeat, refreshToken string) edgeAuthReply {
	t.Helper()
	r, err := p.do(seat, func(e *edge) (edgeAuthReply, error) { return e.userLogout(t.Context(), refreshToken) })
	if err != nil {
		t.Fatal(err)
	}
	return r
}
