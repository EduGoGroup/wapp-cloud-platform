//go:build integracion

package procesos

import (
	"database/sql"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// P1 · El login del operador por el canal de control (T3.29, ADR-0025 y ADR-0048). Es el tramo de
// P1 que TestP1_EnrollmentAndLease no recorre: la persona que se sienta en la consola local del Edge
// escribe su correo y su contraseña, el Edge las RELAYA a la nube por el stream mTLS que ya tiene
// abierto —con el session_id del canal de control, porque puede no haber ningún teléfono emparejado
// todavía— y la nube le contesta, por ese mismo stream, con su par de tokens o con un código de error.
//
// Lo que este proceso fija, de caja negra y contra el binario que elija el arnés:
//
//   - CADA EDGE RECIBE LA RESPUESTA SUYA. El canal de control es una constante idéntica en todos los
//     Edge; mientras fue la clave del registro de sesiones, el login del operador de una empresa —con
//     sus tokens dentro— salía por el cable de OTRA (HS-14/HS-15). Aquí tres Edge de dos empresas
//     entran a la vez, ronda tras ronda, y a cada uno le llega solo lo suyo.
//   - LOS CÓDIGOS DE ERROR, como literales (hallazgo 69 de F3): el mismo literal contra el binario
//     viejo y contra el nuevo, que ya no tiene el adaptador que los traducía.
//   - EL CANAL DE CONTROL NO ES UNA SESIÓN: ni fila en fleet_sessions, ni lease, ni apertura de sesión
//     en la auditoría para el Edge que solo habló por él.
//
// Quién valida las credenciales es identity (su doble: identidad_login_test.go); la nube canjea esa
// identidad por un Context Token de la empresa del operador y lo compara con la del certificado.
//
// Está partido por tema:
//   - p1_enrolamiento_operator_login_test.go          el escenario, la concurrencia y el canal de control
//   - p1_enrolamiento_operator_login_errors_test.go   la tabla de errores, el refresh, el logout y el cierre

// p1LoginRounds es cuántas veces entran los tres Edge a la vez: cada ronda es una ocasión para el cruce.
const p1LoginRounds = 8

// p1LoginOperator es una persona que puede (o no) entrar: su id en identity, su correo y su
// contraseña, generados para la corrida.
type p1LoginOperator struct{ userID, email, password string }

// p1LoginSeat es un puesto: un Edge enrolado en una empresa y el operador que se sienta en su consola.
type p1LoginSeat struct {
	name   string
	tenant string
	e      *edge
	op     p1LoginOperator
	// sent son los command_id de las peticiones de auth que ESTE Edge mandó y cuya respuesta recibió:
	// al final, su buzón tiene que traer exactamente esos y ninguno más.
	sent []string
}

// p1LoginRun es el estado que comparten los pasos: el servidor, su base y los tres puestos.
type p1LoginRun struct {
	s  *servidor
	db *sql.DB
	// tenantA y tenantB son las dos empresas. a1 es un Edge de A SIN teléfono (solo canal de control),
	// a2 otro Edge de A con una sesión emparejada y b1 el Edge de B, también sin teléfono.
	tenantA, tenantB string
	a1, a2, b1       *p1LoginSeat
	// requests cuenta las peticiones de auth mandadas: cada una deja UNA fila edge.auth.* en la auditoría.
	requests atomic.Int64
	// secrets es todo lo que no puede aparecer en el log del servidor: contraseñas y tokens entregados.
	secrets []string
}

// TestP1_OperatorLoginOverControlChannel recorre el login de operador sobre un servidor propio, con
// los pasos en orden. Las conexiones se abren en el test padre: el stream vive mientras viva el
// contexto del test que lo abrió. Necesita Docker.
func TestP1_OperatorLoginOverControlChannel(t *testing.T) {
	t.Parallel()
	p := p1LoginSetup(t)
	p.a1.e.connectControlOnly(t)
	p.b1.e.connectControlOnly(t)
	p.a2.e.conectar(t)

	p.step(t, "concurrent_logins_each_edge_gets_its_own", p.checkConcurrentLogins)
	p.step(t, "login_on_a_paired_session_echoes_it", p.checkPairedSessionLogin)
	p.step(t, "login_error_codes", p.checkErrorCodes)
	p.step(t, "refresh_and_logout", p.checkRefreshAndLogout)
	p.step(t, "control_channel_is_not_a_session", p.checkControlIsNotASession)
	p.step(t, "mailboxes_audit_and_log", p.checkClosing)
}

// step corre un paso como subtest y detiene el proceso si falla (los siguientes parten de él).
func (p *p1LoginRun) step(t *testing.T, name string, fn func(t *testing.T)) {
	t.Helper()
	if !t.Run(name, fn) {
		t.Fatalf("el login de operador se detiene: falló el paso %s", name)
	}
}

// p1LoginSetup arranca un servidor que SÍ sabe a quién preguntarle por unas credenciales
// (IdentityLogin), da de alta dos empresas por la puerta del staff, enrola de verdad tres Edge
// (dos en A, uno en B: cada uno canjea su código por su certificado) y da de alta a sus operadores
// en la empresa (miembro y administrador) y en el doble de identity (correo y contraseña).
func p1LoginSetup(t *testing.T) *p1LoginRun {
	t.Helper()
	s := arrancar(t, opcionesServidor{Proceso: "p1_login", IdentityLogin: true})
	p := &p1LoginRun{s: s, db: s.Base.Abrir(t)}
	staff := uuidAleatorio(t)
	altaStaffPlataforma(t, p.db, staff)
	tokenStaff := canjear(t, s, s.Identidad.TokenDe(staff, "wapp.bff"))
	p.tenantA = crearTenant(t, s, tokenStaff, "p1-login-a")
	p.tenantB = crearTenant(t, s, tokenStaff, "p1-login-b")
	seat := func(name, tenant string) *p1LoginSeat {
		op := p.newOperator(t, name)
		edgeAltaAdminDelTenant(t, p.db, op.userID, tenant)
		s.Identidad.registerOperator(t, op.email, op.password, op.userID)
		return &p1LoginSeat{name: name, tenant: tenant, op: op, e: enrolar(t, s, edgeEmitirCodigo(t, s, tokenStaff, tenant))}
	}
	p.a1, p.a2, p.b1 = seat("a1", p.tenantA), seat("a2", p.tenantA), seat("b1", p.tenantB)
	return p
}

// newOperator genera una persona: un UUID, un correo único y una contraseña aleatoria, que queda
// apuntada entre lo que el log del servidor no puede contener. No la da de alta en ningún sitio.
func (p *p1LoginRun) newOperator(t *testing.T, name string) p1LoginOperator {
	t.Helper()
	op := p1LoginOperator{
		userID:   uuidAleatorio(t),
		email:    "op-" + name + "-" + edgeAleatorioHex(t, 4) + "@procesos.test",
		password: "pw-" + edgeAleatorioHex(t, 16),
	}
	p.secrets = append(p.secrets, op.password)
	return op
}

// do manda una petición de auth por el puesto, la cuenta y, si hubo respuesta, apunta su command_id
// en el puesto. No falla el test: se llama desde goroutines. Quien la llama en paralelo usa un
// puesto distinto en cada goroutine (sent no tiene candado).
func (p *p1LoginRun) do(seat *p1LoginSeat, call func(e *edge) (edgeAuthReply, error)) (edgeAuthReply, error) {
	p.requests.Add(1)
	reply, err := call(seat.e)
	if err == nil {
		seat.sent = append(seat.sent, reply.CommandID)
	}
	return reply, err
}

// login relaya un login por el puesto y la sesión dados (ver do).
func (p *p1LoginRun) login(t *testing.T, seat *p1LoginSeat, session, email, password string) (edgeAuthReply, error) {
	return p.do(seat, func(e *edge) (edgeAuthReply, error) { return e.userLogin(t.Context(), session, email, password) })
}

// checkConcurrentLogins es el paso 1: en cada ronda los tres Edge relayan A LA VEZ el login de su
// operador por el canal de control (una barrera los suelta juntos). Cada respuesta tiene que ser la
// del Edge que la pidió: su command_id, y dentro un token de SU operador y SU empresa.
func (p *p1LoginRun) checkConcurrentLogins(t *testing.T) {
	seats := []*p1LoginSeat{p.a1, p.a2, p.b1}
	for round := range p1LoginRounds {
		replies := make([]edgeAuthReply, len(seats))
		errs := make([]error, len(seats))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, seat := range seats {
			wg.Go(func() {
				<-start
				replies[i], errs[i] = p.login(t, seat, edgeSesionControl, seat.op.email, seat.op.password)
			})
		}
		close(start)
		wg.Wait()
		for i, seat := range seats {
			if errs[i] != nil {
				t.Fatalf("ronda %d, puesto %s: %v", round, seat.name, errs[i])
			}
			p.wantOwnTokens(t, seat, edgeSesionControl, replies[i])
			p.wantAudit(t, seat, "edge.auth.login", "ok", seat.op.userID, edgeSesionControl, replies[i].CommandID)
		}
	}
	// Lo que identity vio: un login por petición, todos para la aplicación del Edge.
	calls := p.s.Identidad.loginCalls()
	if len(calls) != p1LoginRounds*len(seats) {
		t.Errorf("identity recibió %d logins, quería %d", len(calls), p1LoginRounds*len(seats))
	}
	for _, c := range calls {
		if c.Path != identidadLoginRuta || c.System != "wapp.edge" || c.Status != http.StatusOK {
			t.Errorf("llamada a identity inesperada: %+v (quería un login 200 con system wapp.edge)", c)
		}
	}
}

// wantOwnTokens afirma que una respuesta de auth es un par de tokens DEL operador del puesto: el
// sobre y el interior correlacionan (command_id, la sesión de la petición), el access token es un
// Context Token firmado con la clave del servidor cuyos claims nombran a ese usuario y a la empresa
// del Edge, el propio servidor lo acepta y dice lo mismo (whoami), y el refresh token es el que
// identity le dio a esa persona. Los tokens quedan apuntados como secretos.
func (p *p1LoginRun) wantOwnTokens(t *testing.T, seat *p1LoginSeat, session string, r edgeAuthReply) {
	t.Helper()
	if r.IsError {
		t.Fatalf("puesto %s: el login contestó el error %q (%q), quería tokens", seat.name, r.Code, r.Message)
	}
	if r.InnerCommandID != r.CommandID || r.SessionID != session || r.InnerSessionID != session {
		t.Errorf("puesto %s: la respuesta no correlaciona: sobre %s/%s, interior %s/%s, sesión pedida %s",
			seat.name, r.CommandID, r.SessionID, r.InnerCommandID, r.InnerSessionID, session)
	}
	if r.TokenType != "Bearer" || r.ExpiresAt <= time.Now().Unix() || r.AccessToken == "" || r.RefreshToken == "" {
		t.Fatalf("puesto %s: tokens incompletos (tipo %q, expires_at %d, access vacío %v, refresh vacío %v)",
			seat.name, r.TokenType, r.ExpiresAt, r.AccessToken == "", r.RefreshToken == "")
	}
	p.secrets = append(p.secrets, r.AccessToken, r.RefreshToken)
	if kid := segmentoJWT(t, r.AccessToken, 0)["kid"]; kid != p.s.Claves.JWTKid {
		t.Errorf("puesto %s: el access token trae kid %v, quería %s (la clave del servidor)", seat.name, kid, p.s.Claves.JWTKid)
	}
	claims := segmentoJWT(t, r.AccessToken, 1)
	if claims["user_id"] != seat.op.userID || claims["tenant_id"] != seat.tenant {
		t.Fatalf("puesto %s: el access token es de usuario %v y empresa %v; quería %s y %s (¿la respuesta de OTRO Edge?)",
			seat.name, claims["user_id"], claims["tenant_id"], seat.op.userID, seat.tenant)
	}
	p.wantAcceptedByServer(t, seat, r)
}

// wantAcceptedByServer es la segunda mitad de wantOwnTokens, la que no se fía de leer claims: el
// propio servidor acepta ese access token en su API y dice de quién y de qué empresa es (whoami), y
// identity reconoce el refresh token como uno vivo de esa persona.
func (p *p1LoginRun) wantAcceptedByServer(t *testing.T, seat *p1LoginSeat, r edgeAuthReply) {
	t.Helper()
	var me struct {
		TenantID string `json:"tenant_id"`
		Subject  string `json:"subject"`
	}
	who := p.s.Publica(r.AccessToken).Get(t, p2RouteWhoAmI, nil)
	who.JSON(t, &me)
	if who.Codigo != http.StatusOK || me.TenantID != seat.tenant || me.Subject != seat.op.userID {
		t.Errorf("puesto %s: whoami con su access token = HTTP %d, %+v; quería 200, %s y %s", seat.name, who.Codigo, me, seat.tenant, seat.op.userID)
	}
	if owner, alive := p.s.Identidad.refreshOwner(r.RefreshToken); !alive || owner != seat.op.userID {
		t.Errorf("puesto %s: el refresh token es de %q (vivo %v), quería el de %s", seat.name, owner, alive, seat.op.userID)
	}
}

// p1LoginAuditCount cuenta las filas de auditoría de UNA petición de auth de operador: la empresa
// del canal, el actor (el id opaco de la persona; vacío si no llegó a resolverse), la acción, el
// resultado y, en meta, la etiqueta de operador, el Edge, la sesión, el command_id y el canal.
const p1LoginAuditCount = `SELECT count(*)::text FROM public.audit_events
	WHERE tenant_id = $1::uuid AND COALESCE(actor, '') = $2 AND action = $3 AND resource = 'edge.auth' AND result = $4
	  AND meta->>'actor_type' = 'operator' AND meta->>'edge_id' = $5 AND meta->>'session_id' = $6
	  AND meta->>'command_id' = $7 AND meta->>'channel' = 'cloudlink'`

// wantAudit espera a que la petición commandID del puesto tenga exactamente UNA fila de auditoría
// con esa acción, ese resultado y ese actor, en la empresa del Edge (la del certificado, no otra).
func (p *p1LoginRun) wantAudit(t *testing.T, seat *p1LoginSeat, action, result, actor, session, commandID string) {
	t.Helper()
	edgeEsperarValor(t, p.db, "1", "auditoría de "+action+" ("+result+") de "+commandID+" en el puesto "+seat.name,
		p1LoginAuditCount, seat.tenant, actor, action, result, seat.e.EdgeID, session, commandID)
}

// checkPairedSessionLogin es el paso 2: un Edge que ya tiene una sesión emparejada puede relayar el
// login con ESA sesión en vez de la de control; la respuesta vuelve por su stream con el eco de la
// sesión de la petición, y la auditoría la anota con ella.
func (p *p1LoginRun) checkPairedSessionLogin(t *testing.T) {
	seat := p.a2
	r, err := p.login(t, seat, seat.e.SessionID, seat.op.email, seat.op.password)
	if err != nil {
		t.Fatal(err)
	}
	p.wantOwnTokens(t, seat, seat.e.SessionID, r)
	p.wantAudit(t, seat, "edge.auth.login", "ok", seat.op.userID, seat.e.SessionID, r.CommandID)
}

// checkControlIsNotASession es el paso 5 (ADR-0048): tras decenas de frames por el canal de control,
// ese id no es una sesión de nadie. Los dos Edge que SOLO hablaron por él no tienen fila de flota,
// ni lease —ni en Postgres ni por el cable—, ni apertura de sesión en la auditoría; lo único que
// recibieron sin pedirlo es la config inicial, una vez por stream. El Edge emparejado sigue con su
// única sesión y su único lease: usar el canal de control no le añadió nada.
func (p *p1LoginRun) checkControlIsNotASession(t *testing.T) {
	if n := consultaEntero(t, p.db, `SELECT count(*) FROM public.fleet_sessions WHERE session_id = $1`, edgeSesionControl); n != 0 {
		t.Errorf("el canal de control quedó como sesión de flota: %d filas", n)
	}
	for _, seat := range []*p1LoginSeat{p.a1, p.b1} {
		if n := consultaEntero(t, p.db, `
			SELECT (SELECT count(*) FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND edge_id = $2) +
			       (SELECT count(*) FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2) +
			       (SELECT count(*) FROM public.audit_events WHERE tenant_id = $1::uuid AND action = 'edge.session.open' AND actor = $2)`,
			seat.tenant, seat.e.EdgeID); n != 0 {
			t.Errorf("puesto %s (solo canal de control): %d filas entre fleet_sessions, leases y edge.session.open, quería 0", seat.name, n)
		}
		if seat.e.Leases() != 0 || seat.e.puedeOperar() {
			t.Errorf("puesto %s (solo canal de control): recibió %d LeaseUpdate y puedeOperar=%v; quería 0 y falso", seat.name, seat.e.Leases(), seat.e.puedeOperar())
		}
		// La config inicial llega por el canal de control (sin ella este Edge arrancaría con el
		// catálogo vacío) y UNA vez por stream, por muchos frames que mande.
		jwks := seat.e.esperarConfig(t, "jwks", edgeTopeFila)
		if jwks.Sesion != edgeSesionControl || jwks.Version != p.s.Claves.JWTKid {
			t.Errorf("puesto %s: jwks por la sesión %q y versión %q; quería %q y %q", seat.name, jwks.Sesion, jwks.Version, edgeSesionControl, p.s.Claves.JWTKid)
		}
		if n := p1LoginCountConfigs(seat.e, "jwks"); n != 1 {
			t.Errorf("puesto %s: recibió %d jwks, quería 1 (la config inicial es una por stream)", seat.name, n)
		}
	}
	paired := p.a2
	edgeEsperarValor(t, p.db, "1/"+paired.e.SessionID+"/1", "la flota y el lease del Edge emparejado (sesiones/sesión/leases)", `
		SELECT (SELECT count(*)::text || '/' || min(session_id) FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND edge_id = $2) || '/' ||
		       (SELECT count(*)::text FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2)`, paired.tenant, paired.e.EdgeID)
	if !paired.e.puedeOperar() || paired.e.revocado() {
		t.Errorf("el Edge emparejado dejó de poder operar tras usar el canal de control (revocado=%v)", paired.e.revocado())
	}
	for _, c := range paired.e.Configs() {
		if c.Sesion != paired.e.SessionID && c.Sesion != edgeSesionControl {
			t.Errorf("el Edge emparejado recibió un ConfigUpdate de una sesión que no es suya: %q (%s)", c.Sesion, c.Kind)
		}
	}
}

// p1LoginCountConfigs cuenta los ConfigUpdate de un kind que recibió un Edge.
func p1LoginCountConfigs(e *edge, kind string) int {
	n := 0
	for _, c := range e.Configs() {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

// checkClosing es el último paso. BUZONES: cada Edge recibió exactamente las respuestas de las
// peticiones que él mandó —ni una de otro, ni una repetida—. AUDITORÍA: una fila edge.auth.* por
// petición, ninguna con un correo dentro. LOG: ni una contraseña ni un token entregado, y ninguna
// línea ERROR. Y los tres Edge siguen sin errores propios.
func (p *p1LoginRun) checkClosing(t *testing.T) {
	seen := map[string]string{}
	for _, seat := range []*p1LoginSeat{p.a1, p.a2, p.b1} {
		got := make([]string, 0, len(seat.sent))
		for _, r := range seat.e.AuthReplies() {
			got = append(got, r.CommandID)
		}
		want := slices.Clone(seat.sent)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("puesto %s: su buzón trae %d respuestas y mandó %d peticiones; los command_id no coinciden\nrecibió: %v\nmandó:   %v",
				seat.name, len(got), len(want), got, want)
		}
		for _, id := range got {
			if other, dup := seen[id]; dup {
				t.Errorf("el command_id %s llegó a dos buzones: %s y %s", id, other, seat.name)
			}
			seen[id] = seat.name
		}
		if errs := seat.e.Errores(); len(errs) != 0 {
			t.Errorf("puesto %s: el Edge anotó errores: %v", seat.name, errs)
		}
	}
	edgeEsperarValor(t, p.db, strconv.FormatInt(p.requests.Load(), 10), "una fila edge.auth.* por petición de auth",
		`SELECT count(*)::text FROM public.audit_events WHERE resource = 'edge.auth'`)
	if n := consultaEntero(t, p.db, `
		SELECT count(*) FROM public.audit_events
		WHERE resource = 'edge.auth' AND (meta::text LIKE '%@procesos.test%' OR actor LIKE '%@%' OR meta::text LIKE '%password%')`); n != 0 {
		t.Errorf("la auditoría de auth trae PII (un correo o una contraseña) en %d filas", n)
	}
	log := p.s.Log()
	for _, secret := range p.secrets {
		if strings.Contains(log, secret) {
			t.Errorf("el log del servidor contiene un secreto (una contraseña o un token entregado) que empieza por %.6s…", secret)
		}
	}
	edgeSinErrores(t, p.s, nil)
}
