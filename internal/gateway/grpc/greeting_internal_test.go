package gatewaygrpc

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// --- Dobles -----------------------------------------------------------------

// fleetSaludos es un fleet.Repository que ADEMÁS sabe saludar. Embebe el
// MemoryRepository —el mismo molde que fleetVigilante en connect_lane_internal_test.go—
// y le añade los dos métodos de sessionGreeter con el MISMO contrato que la
// implementación de Postgres, incluido el centinela: MarkGreeted devuelve marked=false
// si la marca ya estaba puesta.
//
// Vive aquí y no en fleet.MemoryRepository a propósito: el saludo consume un PUERTO
// PROPIO (sessionGreeter), no fleet.Repository, así que el doble de esa capacidad es
// de quien la consume. Ver el docstring de sessionGreeter.
type fleetSaludos struct {
	*fleet.MemoryRepository

	mu         sync.Mutex
	selfPn     string
	greeted    bool
	pendingErr error
	markErr    error
	consultas  int
	marcas     int
}

func (f *fleetSaludos) PendingGreeting(_ context.Context, _, _, _ string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consultas++
	if f.pendingErr != nil {
		return "", false, f.pendingErr
	}
	if f.greeted || f.selfPn == "" {
		return "", false, nil
	}
	return f.selfPn, true, nil
}

func (f *fleetSaludos) MarkGreeted(_ context.Context, _, _, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marcas++
	if f.markErr != nil {
		return false, f.markErr
	}
	if f.greeted { // centinela: WHERE greeted_at IS NULL
		return false, nil
	}
	f.greeted = true
	return true, nil
}

func (f *fleetSaludos) estado() (greeted bool, consultas, marcas int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.greeted, f.consultas, f.marcas
}

// edgeSaludos es el Edge del otro lado: registra los SendText que le llegan y los
// acusa. `okDe` decide el Ok de CADA acuse por número de envío (1-indexado), que es
// como se reproduce la ventana del lease: el primero se rechaza, el segundo pasa.
type edgeSaludos struct {
	srv  *Server
	okDe func(n int) bool

	mu       sync.Mutex
	textos   []string
	destinos []string
}

func (e *edgeSaludos) Send(msg *cloudlinkv1.CloudToEdge) error {
	st := msg.GetSendText()
	if st == nil {
		return nil
	}
	e.mu.Lock()
	e.textos = append(e.textos, st.GetText())
	e.destinos = append(e.destinos, st.GetTo())
	n := len(e.textos)
	e.mu.Unlock()

	ok := e.okDe == nil || e.okDe(n)
	ack := &cloudlinkv1.Ack{AckedCommandId: msg.GetCommandId(), Ok: ok}
	if !ok {
		ack.Error = "lease no vigente"
	}
	// En el gateway real el Ack lo entrega el bucle Recv, que es OTRA goroutine que
	// la que espera el acuse. Se imita con `go` (mismo molde que
	// send_ack_internal_test.go): entregarlo inline aquí bloquearía este Send.
	go e.srv.deliverAck(ack)
	return nil
}

func (e *edgeSaludos) enviados() ([]string, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.textos...), append([]string(nil), e.destinos...)
}

// bancoDeSaludo monta servidor + registro + Edge + repo listos para latir.
func bancoDeSaludo(t *testing.T, selfPn string, okDe func(n int) bool) (*Server, *fleetSaludos, *edgeSaludos, connCtx) {
	t.Helper()
	repo := &fleetSaludos{MemoryRepository: fleet.NewMemoryRepository(), selfPn: selfPn}
	reg := session.NewRegistry()
	srv := New(reg, logger.New(logger.WithWriter(io.Discard)),
		WithFleet(repo), WithAckTimeout(2*time.Second))
	edge := &edgeSaludos{srv: srv, okDe: okDe}
	t.Cleanup(reg.Register("sess-1", edge))
	return srv, repo, edge, connCtx{tenantID: "t1", edgeID: "e1", sessionID: "sess-1", hasIdentity: true}
}

// --- Tests ------------------------------------------------------------------

// TestSaludoEsIdempotenteEntreLatidos es el criterio (c) de T3.2: DOS latidos
// seguidos de la misma sesión producen UN mensaje, no dos. Un Heartbeat cada 30 s no
// puede convertirse en un saludo cada 30 s.
//
// Se pone rojo si alguien: (a) borra la comprobación de `pending` y manda siempre;
// (b) quita el `!f.greeted` del doble porque «MarkGreeted ya lo hace» —que es la
// mutación que delata al centinela ausente en Postgres—; o (c) marca ANTES de
// enviar y luego re-marca en cada latido.
func TestSaludoEsIdempotenteEntreLatidos(t *testing.T) {
	t.Parallel()
	srv, repo, edge, cc := bancoDeSaludo(t, "573001112233", nil)

	srv.greetIfNeeded(context.Background(), cc)
	srv.greetIfNeeded(context.Background(), cc)

	textos, destinos := edge.enviados()
	if len(textos) != 1 {
		t.Fatalf("se enviaron %d mensajes, quiero 1: %q", len(textos), textos)
	}
	if textos[0] != avisoSesionPasivaV1 {
		t.Fatalf("el texto enviado no es el literal canónico:\n%q", textos[0])
	}
	if destinos[0] != "573001112233" {
		t.Fatalf("destino = %q, quiero el self_pn que devolvió el repositorio", destinos[0])
	}
	greeted, consultas, marcas := repo.estado()
	if !greeted || marcas != 1 {
		t.Fatalf("greeted=%v marcas=%d, quiero true y 1", greeted, marcas)
	}
	// El segundo latido SÍ pregunta (no hay memoria en proceso) y no manda nada.
	if consultas != 2 {
		t.Fatalf("consultas=%d, quiero 2: el segundo latido pregunta y se calla", consultas)
	}
}

// TestSaludoNoSeMarcaSiElEdgeRechazaYReintentaEnElSiguienteLatido es la regla que
// MD-046.3 dejó escrita: el primer latido cae SIEMPRE dentro de la ventana del lease
// (el Validator del Edge nace cerrado, 0,5-1,1 s medidos en campo) y muere con
// Ack{ok=false} SIN error de Go. Si eso marcara la sesión, el dueño no se enteraría
// NUNCA; como no marca, el latido siguiente lo reintenta solo, sin temporizadores.
//
// Se pone rojo con la mutación de una línea que este plan más teme: cambiar
// `if !ack.GetOk() { return }` por un `if err == nil { marcar }`, o sea tratar «el
// Edge acusó» como «el mensaje salió». Entonces marcas pasa a 1 tras el primer
// latido y el segundo envío no ocurre.
func TestSaludoNoSeMarcaSiElEdgeRechazaYReintentaEnElSiguienteLatido(t *testing.T) {
	t.Parallel()
	// El primer envío cae en la ventana del lease; el segundo ya la encuentra abierta.
	srv, repo, edge, cc := bancoDeSaludo(t, "573001112233", func(n int) bool { return n > 1 })

	srv.greetIfNeeded(context.Background(), cc)
	if greeted, _, marcas := repo.estado(); greeted || marcas != 0 {
		t.Fatalf("tras un Ack{ok=false}: greeted=%v marcas=%d, quiero false y 0", greeted, marcas)
	}
	if textos, _ := edge.enviados(); len(textos) != 1 {
		t.Fatalf("primer latido: %d envíos, quiero 1", len(textos))
	}

	srv.greetIfNeeded(context.Background(), cc)
	textos, _ := edge.enviados()
	if len(textos) != 2 {
		t.Fatalf("segundo latido: %d envíos acumulados, quiero 2 (el reintento)", len(textos))
	}
	if greeted, _, marcas := repo.estado(); !greeted || marcas != 1 {
		t.Fatalf("tras el Ack bueno: greeted=%v marcas=%d, quiero true y 1", greeted, marcas)
	}

	// Y una vez marcada, deja de reintentar: la ventana del lease no se paga eterna.
	srv.greetIfNeeded(context.Background(), cc)
	if textos, _ := edge.enviados(); len(textos) != 2 {
		t.Fatalf("tercer latido: %d envíos, quiero 2 (ya estaba saludada)", len(textos))
	}
}

// TestSaludoNoSeMarcaSiElEnvioFalla: sin Ack no hay marca. Es el hermano del
// anterior por el otro camino de salida de SendText (sesión sin stream ⇒ *SendError).
// Se pone rojo si alguien mueve el MarkGreeted fuera del `if` del error.
func TestSaludoNoSeMarcaSiElEnvioFalla(t *testing.T) {
	t.Parallel()
	repo := &fleetSaludos{MemoryRepository: fleet.NewMemoryRepository(), selfPn: "573001112233"}
	srv := New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)),
		WithFleet(repo), WithAckTimeout(200*time.Millisecond))

	// Sin Register: la sesión no tiene stream, el Push falla en el acto.
	srv.greetIfNeeded(context.Background(), connCtx{
		tenantID: "t1", edgeID: "e1", sessionID: "sess-huerfana", hasIdentity: true})

	if greeted, _, marcas := repo.estado(); greeted || marcas != 0 {
		t.Fatalf("con el envío fallido: greeted=%v marcas=%d, quiero false y 0", greeted, marcas)
	}
}

// TestSaludoNoDisparaSinSelfPnNiSinIdentidad: las dos guardas de entrada. Una sesión
// sin número no tiene a quién escribirle, y sin identidad mTLS no se sabe de qué
// tenant es la fila. Se pone rojo si alguien quita el `!cc.hasIdentity` de la guarda
// (el envío se intentaría contra un tenant vacío) o si el emisor deja de exigir
// pending.
func TestSaludoNoDisparaSinSelfPnNiSinIdentidad(t *testing.T) {
	t.Parallel()

	t.Run("sin self_pn", func(t *testing.T) {
		t.Parallel()
		srv, repo, edge, cc := bancoDeSaludo(t, "", nil)
		srv.greetIfNeeded(context.Background(), cc)
		if textos, _ := edge.enviados(); len(textos) != 0 {
			t.Fatalf("sin self_pn se enviaron %d mensajes, quiero 0", len(textos))
		}
		if _, _, marcas := repo.estado(); marcas != 0 {
			t.Fatalf("sin self_pn se marcó %d veces, quiero 0", marcas)
		}
	})

	t.Run("sin identidad", func(t *testing.T) {
		t.Parallel()
		srv, repo, edge, cc := bancoDeSaludo(t, "573001112233", nil)
		cc.hasIdentity = false
		srv.greetIfNeeded(context.Background(), cc)
		if textos, _ := edge.enviados(); len(textos) != 0 {
			t.Fatalf("sin identidad se enviaron %d mensajes, quiero 0", len(textos))
		}
		if _, consultas, _ := repo.estado(); consultas != 0 {
			t.Fatalf("sin identidad se consultó %d veces, quiero 0", consultas)
		}
	})
}

// --- Golden del literal -----------------------------------------------------

// goldenAvisoSesionPasivaV1 es la copia INDEPENDIENTE del literal, tecleada aquí para
// que el test tenga con qué comparar sin importar la constante que vigila. Si alguien
// toca avisoSesionPasivaV1 y no toca esto, el test se pone rojo — que es exactamente
// lo que se quiere: el texto es contrato, no una cadena más.
const goldenAvisoSesionPasivaV1 = `Tu WhatsApp quedó vinculado a wApp, y esta sesión nació en perfil PASIVA.

Qué significa: por esta sesión SOLO SE ENVÍAN mensajes. Lo que te escriban NO SALE
DE ESTE EQUIPO: se queda aquí y no sube a la nube, así que wApp todavía no responde
solo.

Para que responda, cambia el perfil de la sesión a ACTIVA desde el panel de wApp, o
llama a POST /api/v1/sessions/{id}/profile con {"profile":"active"}.`

// TestGoldenDelLiteralDelAviso fija los BYTES del aviso. Cualquier cambio —una tilde,
// un salto de línea movido, una palabra «mejorada»— pone este test rojo, y esa es su
// única función: el texto es contrato (runbook §4) y viaja por DOS canales que tienen
// que decir lo mismo.
func TestGoldenDelLiteralDelAviso(t *testing.T) {
	t.Parallel()
	if avisoSesionPasivaV1 != goldenAvisoSesionPasivaV1 {
		t.Fatalf("el literal cambió sin actualizar su golden.\ntengo:\n%q\nquiero:\n%q",
			avisoSesionPasivaV1, goldenAvisoSesionPasivaV1)
	}
	if avisoSesionPasivaID != "AVISO_SESION_PASIVA_V1" {
		t.Fatalf("el ID del literal = %q; si el texto cambia, el ID sube a _V2 y este test se actualiza con él",
			avisoSesionPasivaID)
	}
}

// TestElLiteralDiceLasTresCosasYNadaMas es el criterio (d) de T3.2, comprobado por
// contenido y no por parecido: el texto nombra las tres cosas y viaja SIN MARCADO.
// Un '*' aquí sería negrita en WhatsApp y un asterisco literal en la pantalla de
// wapp-ctl: dos textos distintos por el mismo literal.
func TestElLiteralDiceLasTresCosasYNadaMas(t *testing.T) {
	t.Parallel()
	// (1) nació pasiva · (2) solo envía y lo entrante no sale de aquí · (3) las dos
	// vías de cambiarla (el panel y la API).
	for _, quiero := range []string{
		"PASIVA",
		"SOLO SE ENVÍAN",
		"NO SALE",
		"DE ESTE EQUIPO",
		"ACTIVA",
		"POST /api/v1/sessions/{id}/profile",
	} {
		if !strings.Contains(avisoSesionPasivaV1, quiero) {
			t.Errorf("el aviso no dice %q", quiero)
		}
	}
	for _, prohibido := range []string{"*", "_", "#", "<b>", "**"} {
		if strings.Contains(avisoSesionPasivaV1, prohibido) {
			t.Errorf("el aviso lleva marcado %q: el contrato lo prohíbe (texto plano, runbook §4)", prohibido)
		}
	}
}

// TestElLiteralCoincideConElRunbook cierra la única grieta que un golden en Go no
// puede cerrar solo: que la constante y el runbook —la FUENTE ÚNICA— divergan.
//
// 🔴 EL FICHERO VIVE DENTRO DE ESTE REPO A PROPÓSITO. Hasta el 2026-08-30 la fuente era
// docs/runbooks/perfiles-de-sesion.md, en el repo de documentación, que NO viaja con este
// git: en un checkout suelto el os.ReadFile fallaba y este test se SALTABA en silencio, con
// lo que el invariante quedaba sin vigilar precisamente donde no había nada más que lo
// vigilara. Ahora la fuente es documentations/literal-aviso-sesion-pasiva.md, de este mismo
// repo, y su ausencia es un fallo: no hay checkout en el que este test no pueda correr.
// Lo que la copia cuesta —que la de la nube y la del Edge diverjan entre sí— lo cubre
// scripts/check-literales-canonicos.py del repo de documentación.
func TestElLiteralCoincideConElRunbook(t *testing.T) {
	t.Parallel()
	const ruta = "../../../documentations/literal-aviso-sesion-pasiva.md"

	crudo, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("no se pudo leer %s: %v — es la fuente única del literal y vive en este repo, así que su ausencia es el defecto", ruta, err)
	}
	delRunbook, err := bloqueDelAviso(string(crudo))
	if err != nil {
		t.Fatalf("leyendo el literal del runbook: %v", err)
	}
	if delRunbook != avisoSesionPasivaV1 {
		t.Fatalf("la constante y el runbook §4 DIVERGEN (el runbook manda).\nrunbook:\n%q\nconstante:\n%q",
			delRunbook, avisoSesionPasivaV1)
	}
}

// bloqueDelAviso extrae del runbook el bloque ```text que sigue a la línea del ID del
// literal. Es un parser mínimo a propósito: si el runbook cambia de forma, este error
// es más útil que una comparación que pasa por accidente contra el bloque equivocado.
func bloqueDelAviso(md string) (string, error) {
	lineas := strings.Split(md, "\n")
	i := 0
	for ; i < len(lineas); i++ {
		if strings.Contains(lineas[i], "ID del literal") && strings.Contains(lineas[i], avisoSesionPasivaID) {
			break
		}
	}
	if i == len(lineas) {
		return "", errNoHayIDEnElRunbook
	}
	for ; i < len(lineas); i++ {
		if strings.TrimSpace(lineas[i]) == "```text" {
			break
		}
	}
	if i == len(lineas) {
		return "", errNoHayBloqueTrasElID
	}
	var cuerpo []string
	for j := i + 1; j < len(lineas); j++ {
		if strings.HasPrefix(strings.TrimSpace(lineas[j]), "```") {
			return strings.Join(cuerpo, "\n"), nil
		}
		cuerpo = append(cuerpo, lineas[j])
	}
	return "", errBloqueSinCerrar
}

var (
	errNoHayIDEnElRunbook  = stringError("el runbook ya no declara el ID " + avisoSesionPasivaID + " en su §4")
	errNoHayBloqueTrasElID = stringError("tras el ID del literal no hay ningún bloque ```text")
	errBloqueSinCerrar     = stringError("el bloque ```text del aviso no se cierra")
)

// stringError es un error constante de test: evita traerse errors.New a un fichero
// que no necesita nada más de ese paquete.
type stringError string

func (e stringError) Error() string { return string(e) }
