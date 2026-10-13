//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// TestP2_InvitacionUnSoloCanje (R9.6.a, paso 3 de P2). Las reglas de los tres candados AST del
// canje viejo, pasadas a conducta observable por la puerta HTTP y en Postgres:
//
//   - membresia_unica_ast_test.go — un solo escritor de tenant_members, con su guarda: quien ya
//     es de otra empresa no entra en una segunda, y N canjes de una invitación dan UNA membresía.
//   - canje_orden_ast_test.go — el acceso se concede antes de marcar la invitación: un canje
//     rechazado NO quema la invitación, y el que la marca queda como miembro.
//   - canje_una_consulta_ast_test.go — «no existe» y «caducada» no se distinguen: aquí, el mismo
//     mensaje (el coste simétrico no es observable sin cronómetro y no se lleva; ver el commit).

const (
	// p2UnusableInvitation es el mensaje ÚNICO de una invitación que no existe (404) y de una
	// caducada (410): quien sondea tokens no aprende cuáles existieron.
	p2UnusableInvitation = "esa invitación no se puede usar"
	// p2SpentInvitation es el mensaje del 409: ya canjeada, revocada, o quien canjea ya pertenece
	// a otra empresa.
	p2SpentInvitation = "esa invitación ya no está disponible, o esta cuenta ya pertenece a una empresa"
	// p2ConcurrentAccepts es cuántas personas canjean a la vez la misma invitación.
	p2ConcurrentAccepts = 8

	p2InvitationState = `SELECT COALESCE(redeemed_by::text, '') || '|' || (redeemed_at IS NOT NULL)::text || '|' || (revoked_at IS NOT NULL)::text
		FROM public.tenant_invitations WHERE id = $1::uuid`
	p2MembershipsOf = `SELECT count(*) FROM public.tenant_members WHERE user_id = $1::uuid`
)

// p2Invitation es lo que devuelve la emisión de una invitación.
type p2Invitation struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	RoleID string `json:"role_id"`
	Token  string `json:"token"`
}

// p2IssueInvitation emite una invitación con el token de la administradora (cuerpo nil = sin rol y
// con la caducidad por defecto) y la devuelve. Falla (t.Fatalf) si no es un 201 con id y token.
func p2IssueInvitation(t *testing.T, s *servidor, tokenAdmin string, cuerpo any) p2Invitation {
	t.Helper()
	r := s.Publica(tokenAdmin).Post(t, p2RouteInvitations, cuerpo)
	var inv p2Invitation
	if r.Codigo == http.StatusCreated {
		r.JSON(t, &inv)
	}
	if r.Codigo != http.StatusCreated || !identidadUUID.MatchString(inv.ID) || inv.Token == "" || inv.Status != "pending" {
		t.Fatalf("emitir una invitación: HTTP %d %s, quería 201 con id, token y status pending", r.Codigo, recortar(r.Cuerpo))
	}
	return inv
}

// p2ExpireInvitation deja una invitación caducada desde hace una hora. Falla (t.Fatalf) si el id no
// es un UUID, el SQL falla o no tocó exactamente una fila.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: la caducidad es el paso del tiempo. La emisión acota el `ttl`
// entre 60 s y 30 días, así que por la puerta no se puede pedir una invitación ya vencida, y
// esperar un minuto de reloj sería una espera fija, que el arnés prohíbe.
func p2ExpireInvitation(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if err := exigirUUID("la invitación", id); err != nil {
		t.Fatalf("p2ExpireInvitation: %v", err)
	}
	ctx, cancelar := context.WithTimeout(t.Context(), topeFixture)
	defer cancelar()
	res, err := db.ExecContext(ctx, `UPDATE public.tenant_invitations SET expires_at = now() - interval '1 hour' WHERE id = $1::uuid`, id)
	if err != nil {
		t.Fatalf("p2ExpireInvitation: %v", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("p2ExpireInvitation: tocó %d filas (err=%v), quería 1", n, err)
	}
}

// p2Guest es una persona recién registrada: sin empresa, con su Context Token.
type p2Guest struct {
	ID    string
	Token string
}

func p2NewGuest(t *testing.T, s *servidor) p2Guest {
	t.Helper()
	id := uuidAleatorio(t)
	return p2Guest{ID: id, Token: p2Exchange(t, s, id).ContextToken}
}

// p2Accept canjea una invitación con el token de quien llama.
func p2Accept(t *testing.T, s *servidor, tokenUsuario, invitacion string) respuesta {
	t.Helper()
	return s.Publica(tokenUsuario).Post(t, p2RouteAccept, map[string]string{"token": invitacion})
}

// TestP2_InvitacionUnSoloCanje: la administradora emite una invitación y ocho personas la canjean a
// la vez; entra una. Y los rechazos del canje, con lo que dejan (o no) en Postgres. Necesita Docker.
func TestP2_InvitacionUnSoloCanje(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "p2_invitation", "p2-invitacion", false)
	admin := uuidAleatorio(t)
	edgeAltaAdminDelTenant(t, esc.DB, admin, esc.Tenant)
	esc.TokenAdmin = canjear(t, esc.S, esc.S.Identidad.TokenDe(admin, "wapp.bff"))

	t.Run("ocho canjes simultáneos: un éxito y una membresía", func(t *testing.T) { p2ConcurrentRedeem(t, esc, admin) })
	t.Run("inexistente y caducada contestan lo mismo", func(t *testing.T) { p2IndistinguishableRejections(t, esc) })
	t.Run("el token se normaliza y el cuerpo solo aporta el token", func(t *testing.T) { p2TokenNormalization(t, esc) })
	t.Run("quien ya es de otra empresa no quema la invitación", func(t *testing.T) { p2SingleMembership(t, esc) })
	t.Run("una invitación revocada no se canjea", func(t *testing.T) { p2RevokedInvitation(t, esc) })

	edgeSinErrores(t, esc.S, nil)
}

// p2ConcurrentRedeem emite una invitación con el rol viewer y lanza ocho canjes a la vez, cada uno
// de una persona distinta. Entra UNA (204); las otras siete reciben 409. En Postgres: una sola
// membresía y un solo rol entre las ocho, la invitación marcada una vez y por quien ganó; y quien
// ganó canjea su identidad y recibe un token de esa empresa con el rol de la invitación.
func p2ConcurrentRedeem(t *testing.T, esc edgeEscenario, admin string) {
	inv := p2IssueInvitation(t, esc.S, esc.TokenAdmin, map[string]any{"role_id": p2RoleViewer, "ttl": 600})
	if inv.RoleID != p2RoleViewer {
		t.Errorf("la invitación dice role_id=%q, quería %s", inv.RoleID, p2RoleViewer)
	}
	p2WantAudit(t, esc.DB, 1, admin, "members.write", "invitation", "success", esc.Tenant)

	invitados, respuestas, errores := p2RaceAccepts(t, esc, inv)
	ganador := p2SingleWinner(t, invitados, respuestas, errores)
	p2CheckRedeemTrail(t, esc, inv, invitados, ganador)

	// Paso 3 entero: el invitado, ya miembro, canjea su identidad y sale con la empresa y el rol.
	p2WantContext(t, "canje del ganador", p2Exchange(t, esc.S, ganador), esc.Tenant, "viewer")
	// Y un canje posterior de la misma invitación, por quien sea, es el mismo 409.
	for nombre, token := range map[string]string{"otra persona": p2NewGuest(t, esc.S).Token, "el propio ganador": p2Exchange(t, esc.S, ganador).ContextToken} {
		r := p2Accept(t, esc.S, token, inv.Token)
		p2WantCode(t, "canje de la invitación ya usada por "+nombre, r, http.StatusConflict)
	}
	// La administradora la ve canjeada, sin el token ni su digest.
	r := esc.S.Publica(esc.TokenAdmin).Get(t, p2RouteInvitations, nil)
	p2WantCode(t, "listado de invitaciones", r, http.StatusOK)
	if cuerpo := string(r.Cuerpo); !strings.Contains(cuerpo, `"status":"redeemed"`) || strings.Contains(cuerpo, "token") {
		t.Errorf("listado de invitaciones: %s, quería la invitación «redeemed» y ni rastro del token", recortar(r.Cuerpo))
	}
}

// p2RaceAccepts registra a las p2ConcurrentAccepts personas y lanza sus canjes de la invitación a
// la vez. Devuelve, por persona, su respuesta o el error de no haber llegado al servidor.
func p2RaceAccepts(t *testing.T, esc edgeEscenario, inv p2Invitation) ([]p2Guest, []respuesta, []error) {
	invitados := make([]p2Guest, p2ConcurrentAccepts)
	for i := range invitados {
		invitados[i] = p2NewGuest(t, esc.S)
	}
	cuerpo, err := json.Marshal(map[string]string{"token": inv.Token})
	if err != nil {
		t.Fatalf("serializar el canje: %v", err)
	}
	respuestas := make([]respuesta, len(invitados))
	errores := make([]error, len(invitados))
	salida := make(chan struct{})
	var grupo sync.WaitGroup
	for i, invitado := range invitados {
		cliente := esc.S.Publica(invitado.Token)
		grupo.Go(func() {
			<-salida // todos a la vez
			respuestas[i], errores[i] = cliente.despachar(t.Context(), http.MethodPost, p2RouteAccept, cuerpo)
		})
	}
	close(salida)
	grupo.Wait()
	return invitados, respuestas, errores
}

// p2SingleWinner mira las respuestas de la carrera: una con 204 y las demás con el 409 de la
// invitación ya usada. Devuelve quién ganó; falla (t.Fatalf) si un canje no llegó o no ganó nadie.
func p2SingleWinner(t *testing.T, invitados []p2Guest, respuestas []respuesta, errores []error) string {
	ganador := ""
	for i, r := range respuestas {
		if errores[i] != nil {
			t.Fatalf("el canje %d no llegó al servidor: %v", i, errores[i])
		}
		switch r.Codigo {
		case http.StatusNoContent:
			if ganador != "" {
				t.Errorf("dos canjes con éxito: %s y %s", ganador, invitados[i].ID)
			}
			ganador = invitados[i].ID
		case http.StatusConflict:
			if msg := p2ErrorOf(r); msg != p2SpentInvitation {
				t.Errorf("el canje perdedor %d dice %q, quería %q", i, msg, p2SpentInvitation)
			}
		default:
			t.Errorf("el canje %d: HTTP %d %s, quería 204 (uno) o 409 (los demás)", i, r.Codigo, recortar(r.Cuerpo))
		}
	}
	if ganador == "" {
		t.Fatalf("ninguno de los %d canjes simultáneos tuvo éxito", p2ConcurrentAccepts)
	}
	return ganador
}

// p2CheckRedeemTrail es lo que la carrera dejó en Postgres: la invitación marcada por quien ganó,
// una membresía y un rol entre todos, y nada en la bitácora que no sean canjes de identidad.
func p2CheckRedeemTrail(t *testing.T, esc edgeEscenario, inv p2Invitation, invitados []p2Guest, ganador string) {
	// La invitación se marcó una vez, y por quien ganó.
	if estado := p2Text(t, esc.DB, p2InvitationState, inv.ID); estado != ganador+"|true|false" {
		t.Errorf("tenant_invitations = %q, quería %q (redeemed_by|redeemed_at|revoked_at)", estado, ganador+"|true|false")
	}
	// Una membresía y un rol entre los ocho: los del ganador. Los perdedores no entraron en ninguna
	// empresa (su transacción deshizo la membresía que había concedido antes de perder el marcado).
	for _, invitado := range invitados {
		miembro := consultaEntero(t, esc.DB, p2MembershipsOf, invitado.ID)
		enEsta := consultaEntero(t, esc.DB, p2MembershipsOf+` AND tenant_id = $2::uuid`, invitado.ID, esc.Tenant)
		roles := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.iam_user_roles WHERE user_id = $1::uuid`, invitado.ID)
		quiere := 0
		if invitado.ID == ganador {
			quiere = 1
		}
		if miembro != quiere || enEsta != quiere || roles != quiere {
			t.Errorf("invitado %s (ganador=%v): %d membresías (%d en la empresa) y %d roles, quería %d de cada",
				invitado.ID, invitado.ID == ganador, miembro, enEsta, roles, quiere)
		}
	}
	if rol := p2Text(t, esc.DB, `SELECT role_id::text || '|' || tenant_id::text FROM public.iam_user_roles WHERE user_id = $1::uuid`, ganador); rol != p2RoleViewer+"|"+esc.Tenant {
		t.Errorf("iam_user_roles del ganador = %q, quería viewer acotado a la empresa", rol)
	}
	// El canje de invitación no pasa por la bitácora (quien llama no trae empresa): su rastro es
	// redeemed_by/redeemed_at. De los invitados solo hay canjes de identidad.
	for _, invitado := range invitados {
		if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.audit_events WHERE actor = $1 AND action <> $2`, invitado.ID, p2AuditExchange); n != 0 {
			t.Errorf("audit_events del invitado %s: %d filas que no son canjes de identidad", invitado.ID, n)
		}
	}
}

// p2IndistinguishableRejections: una invitación que no existe (404) y una caducada (410) contestan
// el MISMO mensaje, y ninguna deja nada escrito. Con los casos adversarios del token.
func p2IndistinguishableRejections(t *testing.T, esc edgeEscenario) {
	caducada := p2IssueInvitation(t, esc.S, esc.TokenAdmin, nil)
	p2ExpireInvitation(t, esc.DB, caducada.ID)
	invitado := p2NewGuest(t, esc.S)

	casos := []struct {
		caso    string
		token   string
		codigo  int
		mensaje string
	}{
		{"que no existe", "NOEXISTE-0000-0000", http.StatusNotFound, p2UnusableInvitation},
		{"caducada", caducada.Token, http.StatusGone, p2UnusableInvitation},
		{"con separador repetido", "a@@b", http.StatusNotFound, p2UnusableInvitation},
		{"con el token bueno partido por @@", caducada.Token[:4] + "@@" + caducada.Token[4:], http.StatusNotFound, p2UnusableInvitation},
		{"con dígitos árabe-índicos", "١٢٣", http.StatusNotFound, p2UnusableInvitation},
		{"de solo espacios Unicode", "  ", http.StatusNotFound, p2UnusableInvitation},
		{"vacía", "", http.StatusBadRequest, "falta el token de invitación"},
	}
	cuerpos := map[int]string{}
	for _, c := range casos {
		r := p2Accept(t, esc.S, invitado.Token, c.token)
		p2WantCode(t, "canje de una invitación "+c.caso, r, c.codigo)
		if msg := p2ErrorOf(r); msg != c.mensaje {
			t.Errorf("canje de una invitación %s: error %q, quería %q", c.caso, msg, c.mensaje)
		}
		cuerpos[r.Codigo] = string(r.Cuerpo)
	}
	// El cuerpo entero, no solo el campo: lo único que separa 404 de 410 es el código.
	if cuerpos[http.StatusNotFound] != cuerpos[http.StatusGone] {
		t.Errorf("«no existe» y «caducada» se distinguen por el cuerpo: %q frente a %q", cuerpos[http.StatusNotFound], cuerpos[http.StatusGone])
	}
	p2WantCode(t, "canje sin token de sesión", p2Accept(t, esc.S, "", caducada.Token), http.StatusUnauthorized)

	if estado := p2Text(t, esc.DB, p2InvitationState, caducada.ID); estado != "|false|false" {
		t.Errorf("la invitación caducada quedó %q, quería sin canjear ni revocar", estado)
	}
	if n := consultaEntero(t, esc.DB, p2MembershipsOf, invitado.ID); n != 0 {
		t.Errorf("los canjes rechazados dejaron %d membresía(s)", n)
	}
	p2WantContext(t, "canje de identidad tras los rechazos", p2Exchange(t, esc.S, invitado.ID), "")
}

// p2TokenNormalization: el token se compara en mayúsculas y sin espacios en los bordes (el viejo
// guarda SHA-256 de ToUpper(TrimSpace(token))), así que en minúsculas y entre espacios Unicode se
// acepta. El cuerpo del canje solo aporta el token: un tenant_id que venga en él se ignora y la
// membresía es la de la empresa que emitió. Sin role_id, el alta es sin rol.
func p2TokenNormalization(t *testing.T, esc edgeEscenario) {
	ajena := crearTenant(t, esc.S, esc.TokenStaff, "p2-inv-ajena")
	inv := p2IssueInvitation(t, esc.S, esc.TokenAdmin, nil)
	if inv.RoleID != "" {
		t.Errorf("invitación sin rol: role_id=%q", inv.RoleID)
	}
	minusculas := strings.ToLower(inv.Token)
	if minusculas == inv.Token {
		t.Fatalf("el token %q no tiene mayúsculas: el caso de las minúsculas no probaría nada", inv.Token)
	}
	invitado := p2NewGuest(t, esc.S)
	r := esc.S.Publica(invitado.Token).Post(t, p2RouteAccept, map[string]string{
		"token":     "  " + minusculas + "  ",
		"tenant_id": ajena,
	})
	p2WantCode(t, "canje con el token en minúsculas y entre espacios Unicode", r, http.StatusNoContent)
	if estado := p2Text(t, esc.DB, p2InvitationState, inv.ID); estado != invitado.ID+"|true|false" {
		t.Errorf("tenant_invitations = %q, quería canjeada por %s", estado, invitado.ID)
	}
	if empresa := p2Text(t, esc.DB, `SELECT string_agg(tenant_id::text, ',') FROM public.tenant_members WHERE user_id = $1::uuid`, invitado.ID); empresa != esc.Tenant {
		t.Errorf("membresías del invitado = %q, quería solo %s (el tenant_id del cuerpo se ignora)", empresa, esc.Tenant)
	}
	p2WantContext(t, "canje de identidad del invitado sin rol", p2Exchange(t, esc.S, invitado.ID), esc.Tenant)
}

// p2SingleMembership: quien ya pertenece a otra empresa no entra en una segunda por invitación
// (409), y ese rechazo NO quema la invitación: sigue sin marcar y otra persona la canjea después.
func p2SingleMembership(t *testing.T, esc edgeEscenario) {
	otra := crearTenant(t, esc.S, esc.TokenStaff, "p2-inv-otra")
	inv := p2IssueInvitation(t, esc.S, esc.TokenAdmin, map[string]any{"role_id": p2RoleOperator})
	yaMiembro := uuidAleatorio(t)
	p2AddMember(t, esc.DB, yaMiembro, otra, p2RoleViewer)

	r := p2Accept(t, esc.S, p2Exchange(t, esc.S, yaMiembro).ContextToken, inv.Token)
	p2WantCode(t, "canje de quien ya es de otra empresa", r, http.StatusConflict)
	if msg := p2ErrorOf(r); msg != p2SpentInvitation {
		t.Errorf("canje de quien ya es de otra empresa: error %q, quería %q", msg, p2SpentInvitation)
	}
	if estado := p2Text(t, esc.DB, p2InvitationState, inv.ID); estado != "|false|false" {
		t.Errorf("el rechazo quemó la invitación: %q", estado)
	}
	if empresas := p2Text(t, esc.DB, `SELECT string_agg(tenant_id::text, ',') FROM public.tenant_members WHERE user_id = $1::uuid`, yaMiembro); empresas != otra {
		t.Errorf("membresías de quien ya era de otra empresa = %q, quería solo %s", empresas, otra)
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.iam_user_roles WHERE user_id = $1::uuid AND tenant_id = $2::uuid`, yaMiembro, esc.Tenant); n != 0 {
		t.Errorf("el rechazo dejó %d rol(es) en la empresa que invitaba", n)
	}

	libre := p2NewGuest(t, esc.S)
	p2WantCode(t, "canje posterior de la invitación no quemada", p2Accept(t, esc.S, libre.Token, inv.Token), http.StatusNoContent)
	p2WantContext(t, "canje de identidad de quien sí entró", p2Exchange(t, esc.S, libre.ID), esc.Tenant, "operator")
	// Aceptar dos veces: la segunda es 409 y sigue habiendo una membresía.
	p2WantCode(t, "segundo canje de la misma persona", p2Accept(t, esc.S, libre.Token, inv.Token), http.StatusConflict)
	if n := consultaEntero(t, esc.DB, p2MembershipsOf, libre.ID); n != 1 {
		t.Errorf("quien entró tiene %d membresías, quería 1", n)
	}
}

// p2RevokedInvitation: la administradora revoca una invitación viva (204, auditada) y su canje
// posterior es 409 y no escribe membresía; revocar una ya canjeada es 409.
func p2RevokedInvitation(t *testing.T, esc edgeEscenario) {
	adm := esc.S.Publica(esc.TokenAdmin)
	inv := p2IssueInvitation(t, esc.S, esc.TokenAdmin, nil)
	p2WantCode(t, "revocar una invitación viva", adm.Delete(t, p2RouteInvitations+"/"+inv.ID, nil), http.StatusNoContent)
	if estado := p2Text(t, esc.DB, p2InvitationState, inv.ID); estado != "|false|true" {
		t.Errorf("tenant_invitations tras revocar = %q, quería revocada y sin canjear", estado)
	}
	invitado := p2NewGuest(t, esc.S)
	p2WantCode(t, "canje de una invitación revocada", p2Accept(t, esc.S, invitado.Token, inv.Token), http.StatusConflict)
	if n := consultaEntero(t, esc.DB, p2MembershipsOf, invitado.ID); n != 0 {
		t.Errorf("el canje de una revocada dejó %d membresía(s)", n)
	}

	canjeada := p2IssueInvitation(t, esc.S, esc.TokenAdmin, nil)
	p2WantCode(t, "canje de una invitación viva", p2Accept(t, esc.S, invitado.Token, canjeada.Token), http.StatusNoContent)
	p2WantCode(t, "revocar una invitación ya canjeada", adm.Delete(t, p2RouteInvitations+"/"+canjeada.ID, nil), http.StatusConflict)
	p2WantCode(t, "revocar una invitación que no existe", adm.Delete(t, p2RouteInvitations+"/"+uuidAleatorio(t), nil), http.StatusNotFound)
	if n := consultaEntero(t, esc.DB, p2MembershipsOf+` AND tenant_id = $2::uuid`, invitado.ID, esc.Tenant); n != 1 {
		t.Errorf("el invitado tiene %d membresías en la empresa, quería 1", n)
	}
}
