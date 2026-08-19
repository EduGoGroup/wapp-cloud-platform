package publicapi_test

// Plan 050 · Ola 5 · T5.4 — el guion INVERSO del envío: POST /api/v1/messages
// contra un Edge SATURADO, el escenario del 2026-08-06 en el que el POST colgó 88 s
// y el servidor cerró la conexión sin responder ni loguear nada (curl 52), dos veces
// seguidas; con el Edge descongestionado, 200 en 0,88 s.
//
// Lo que estos tests montan y por qué:
//
//   - Un servidor HTTP **REAL** (httptest.NewUnstartedServer con WriteTimeout), no el
//     httptest.NewRecorder del resto del paquete. Es la única diferencia que importa:
//     un recorder no tiene deadline de escritura ni conexión que cerrar, así que NO
//     PUEDE reproducir el síntoma —la respuesta vacía— por mucho que el handler tarde.
//     Este es el primer fichero de internal/publicapi/ que monta un servidor de verdad.
//   - El **Registry y el Gateway REALES** (session.NewRegistry + gatewaygrpc.New), no
//     un fakeSender. El defecto que se persigue vive en el reloj del Push
//     (session.ErrPushTimeout) y en cómo lo traduce writeSendError; contra un doble que
//     devuelve el error al instante los dos tramos quedarían sin ejercitar.
//
// ⚠️ SOBRE LOS RELOJES (INV-050.6: aquí NO se mueve ningún valor de producción). Los
// tests inyectan relojes CORTOS en el arnés —session.WithSendTimeout,
// gatewaygrpc.WithAckTimeout y el WriteTimeout del servidor de prueba— manteniendo la
// RELACIÓN que produce el defecto en producción, no los valores absolutos. El defecto
// no es que 10 s sea mucho: es que la suma de los relojes secuenciales del handler
// puede exceder el WriteTimeout, y esa relación se reproduce igual a escala 1/16 en
// menos de un segundo. Ningún default (defaultSendTimeout, defaultAckTimeout,
// writeTimeout, PublicAPIDBTimeout) se toca ni se lee desde aquí: una aserción contra
// la constante que se quiere proteger pasaría con cualquier valor.
//
// ⚠️ SOBRE EL NOMBRE DEL FICHERO. Lleva el sufijo _e2e_integration_test.go porque así
// lo nombra T5.4, pero NO abre Postgres (no llama a e2eOpenDB): el arnés es entero en
// memoria. El sufijo es convención, no gating —quien salta sin BD es e2eOpenDB—, de
// modo que estos tests corren TAMBIÉN en el gate de unidad (`make test`). Es lo
// deseable: son rápidos y no dependen de nada externo.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// --- Relojes del arnés (NO son los de producción) ----------------------------

const (
	// satPushCorto es el sendTimeout del Registry cuando lo que se quiere ejercitar es
	// que el EDGE se atasque (session.ErrPushTimeout). En producción vale 10 s
	// (WAPP_GRPC_PUSH_TIMEOUT).
	satPushCorto = 600 * time.Millisecond
	// satPushLargo es el sendTimeout cuando lo que tiene que vencer es el PRESUPUESTO
	// DE LA PETICIÓN y no el reloj del Edge. Es la relación de producción: el
	// presupuesto (9 s) es más corto que el push (10 s), así que gana el presupuesto.
	satPushLargo = 3 * time.Second
	// satAckCorto es el ackTimeout del Gateway (8 s en producción).
	satAckCorto = 300 * time.Millisecond
	// satSendLento es lo que tarda un Send que SÍ completa. Menor que satPushCorto a
	// propósito: así el Push tiene éxito y el reloj del ack arranca DESPUÉS, que es
	// lo que prueba la secuencialidad.
	satSendLento = 400 * time.Millisecond

	// satWriteApretado es el WriteTimeout del servidor de prueba en el escenario del
	// incidente. Los tests NO cablean el presupuesto a mano: lo derivan de este valor
	// con publicapi.SendBudgetFrom, la MISMA función que usa el bootstrap. Por eso
	// tiene que dejar sitio al margen de escritura real (1 s) en vez de ser
	// microscópico: lo que se prueba es la fórmula de producción, no una imitación.
	// SendBudgetFrom(1500ms) = 500ms de presupuesto, y 500ms < satPushLargo.
	satWriteApretado = 1500 * time.Millisecond
	// satPresupuestoApretado es lo que SendBudgetFrom deriva de satWriteApretado. Se
	// escribe aquí solo para las cotas de tiempo; el arnés NO lo usa para cablear (usa
	// la función), y satMontar comprueba que los dos coinciden — si el margen de
	// producción cambiara, el test lo dice en vez de medir contra un número obsoleto.
	satPresupuestoApretado = 500 * time.Millisecond
	// satWriteHolgado es el control: un WriteTimeout que el handler no puede agotar, y
	// del que se deriva un presupuesto (29 s) que tampoco estorba. Con él la MISMA
	// saturación sí produce respuesta: la variable que decide es la relación entre
	// relojes, no la carga de la máquina.
	satWriteHolgado = 30 * time.Second

	// satCotaSuperior acota por arriba: si se tarda más, ningún reloj se aplicó (el
	// margen absorbe un CI cargado con -race y sigue muy por debajo del cliente).
	satCotaSuperior = 3 * time.Second
	// satCotaInferior descarta el fallo OPUESTO —un reloj de cero respondería en
	// microsegundos—: el 80 % de satPushCorto prueba que el plazo se esperó de verdad.
	satCotaInferior = 480 * time.Millisecond
	// satCotaPresupuesto es la cota inferior del escenario del presupuesto: el 80 % de
	// satPresupuestoApretado.
	satCotaPresupuesto = 400 * time.Millisecond
	// satCotaSecuencial es la cota inferior del caso Push-lento-y-sin-ack: si los dos
	// relojes fueran alternativos, el total rondaría el mayor de los dos (400 ms); que
	// supere su SUMA menos holgura es lo que prueba que corren en serie.
	satCotaSecuencial = 650 * time.Millisecond

	// satEsperaCliente es el techo del cliente HTTP del test: muy por encima de todo
	// lo anterior, para que sea el servidor —no el cliente— quien decida el desenlace.
	satEsperaCliente = 10 * time.Second
)

// --- Veredictos: lo ÚNICO que la fase 2 tiene que tocar ----------------------
//
// Cada escenario compara contra un satVeredicto declarado aquí. Cuando el arreglo de
// REQ-050.19 entre, el diff sobre este fichero debería ser este bloque y poco más:
// una línea por escenario, no una reescritura de las pruebas.
//
// ⚠️ Esto NO es el patrón del test tautológico. Estas constantes son el veredicto que
// el test DECLARA esperar, escritas a mano; un test tautológico sería el que comparase
// el resultado contra la constante de PRODUCCIÓN que dice proteger (defaultSendTimeout,
// defaultAckTimeout…), porque entonces pasaría con cualquier valor. Aquí, cambiar el
// código sin cambiar este bloque pone el test rojo, que es justo lo que se quiere.

// satVeredicto es el desenlace COMPLETO tal y como lo ve el cliente.
type satVeredicto struct {
	// code es el código HTTP, y CERO significa «el cliente no recibió respuesta»: la
	// conexión se cerró vacía (el curl 52). Que el caso patológico quepa en el mismo
	// campo que los sanos es lo que permite que la fase 2 sea un cambio de número.
	code int
	// fragmento tiene que aparecer en el cuerpo. Vacío = no se espera cuerpo.
	fragmento string
	// conCmdID exige un identificador de comando no vacío en el cuerpo: sin él, un
	// 5xx de envío no es diagnosticable contra el outbox del Edge.
	conCmdID bool
	// dentroDelWriteTimeout exige que la respuesta se produzca ANTES de que venza el
	// deadline de escritura de la conexión. Es la condición que REQ-050.19 pide de
	// verdad («el tiempo acotado por el reloj») y la que hoy no se cumple.
	dentroDelWriteTimeout bool
}

var (
	// satEdgeAtascadoSinMargen · escenario A: el incidente del 2026-08-06. El Edge no
	// lee su stream y el reloj de abajo (push) es más largo que lo que el servidor
	// puede tardar en responder.
	//
	// ✅ FASE 2 (cerrado). Antes: {code: 0} — el cliente no recibía NADA, la conexión se
	// cerraba vacía a los 603,9 ms con un WriteTimeout de 120 ms (curl 52). Ahora el
	// presupuesto de la petición se rinde ANTES del deadline de escritura y el cliente
	// recibe un 504 explícito, con cuerpo, con command_id y a tiempo.
	satEdgeAtascadoSinMargen = satVeredicto{
		code: http.StatusGatewayTimeout, fragmento: "se agotó el plazo de la petición",
		conCmdID: true, dentroDelWriteTimeout: true,
	}

	// satEdgeAtascadoConMargen · escenario A': el MISMO Edge atascado con WriteTimeout
	// holgado, para que venza el reloj del EDGE y no el presupuesto. Aísla el mapeo del
	// error, sin el ruido del deadline de escritura.
	//
	// ✅ FASE 2 (cerrado). Antes: 500 «no se pudo enviar el texto» — el default genérico,
	// porque session.ErrPushTimeout no estaba en el switch de writeSendError. Ahora es
	// un 504 con texto propio, distinguible del 504 del ack y del 504 del stream caído.
	satEdgeAtascadoConMargen = satVeredicto{
		code: http.StatusGatewayTimeout, fragmento: "el Edge dejó de leer su stream",
		conCmdID: true, dentroDelWriteTimeout: true,
	}
)

// --- Dobles del Edge ---------------------------------------------------------

// satEdgeAtascado es el Edge del incidente: un stream que dejó de leerse, de modo que
// el Send de gRPC se queda bloqueado por control de flujo. Es el mismo doble que
// session.blockingSender (internal/gateway/session/registry_test.go), reescrito aquí
// porque el repo no comparte helpers de test entre paquetes.
type satEdgeAtascado struct{ liberar <-chan struct{} }

func (e satEdgeAtascado) Send(*cloudlinkv1.CloudToEdge) error {
	<-e.liberar
	return nil
}

// satEdgeContado es el Edge atascado que además CUENTA los Send que llegaron a
// ejecutarse. Existe para responder a la única pregunta que decide si el arreglo de la
// fase 2 es seguro: cuando el llamante HTTP se rinde, ¿el comando sale igualmente?
type satEdgeContado struct {
	liberar  <-chan struct{}
	mu       sync.Mutex
	enviados int
}

func (e *satEdgeContado) Send(*cloudlinkv1.CloudToEdge) error {
	<-e.liberar
	e.mu.Lock()
	defer e.mu.Unlock()
	e.enviados++
	return nil
}

func (e *satEdgeContado) total() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enviados
}

// satEdgeMudo acepta el comando (el Push tiene éxito) y NUNCA acusa: el Edge del
// 2026-08-06 visto desde el otro reloj, el del ack.
type satEdgeMudo struct{ demora time.Duration }

func (e satEdgeMudo) Send(*cloudlinkv1.CloudToEdge) error {
	time.Sleep(e.demora)
	return nil
}

// satFlotaRota es un SessionLister que falla con un error que NO es el deadline: el
// camino del 500 "no se pudo verificar la sesión" (messages.go:94), uno de los cinco
// desenlaces mudos.
type satFlotaRota struct{}

var errSatFlotaRota = errors.New("la flota no contesta")

func (satFlotaRota) List(_ context.Context, _ string) ([]fleet.Session, error) {
	return nil, errSatFlotaRota
}

// --- Arnés -------------------------------------------------------------------

// satArnes es la API pública servida por un servidor HTTP de verdad.
type satArnes struct {
	srv *httptest.Server
	api *testAPI
	log *dbLogSpy
}

// satFlotaCon devuelve el SessionLister que reconoce sess-a como propia de tenantA:
// la guarda de tenant pasa y el camino llega al Edge, que es donde se mide.
func satFlotaCon() fakeSessions {
	return fakeSessions{byTenant: map[string][]fleet.Session{
		tenantA: {{TenantID: tenantA, SessionID: "sess-a"}},
	}}
}

// satMontar levanta la API pública con el sender dado sobre un servidor HTTP REAL con
// el WriteTimeout indicado. El WriteTimeout se fija ANTES de arrancar (httptest lo
// permite con NewUnstartedServer) porque después ya no tendría efecto.
//
// El logger se RECIBE, no se fabrica aquí: el Gateway y la API pública tienen que
// escribir en el MISMO espía o las aserciones sobre el log mirarían a un objeto que
// nadie usa —y pasarían solas—.
func satMontar(t *testing.T, log *dbLogSpy, sender publicapi.MessageSender, sessions publicapi.SessionLister, writeTimeout time.Duration) *satArnes {
	t.Helper()
	// El presupuesto se DERIVA con la MISMA función que el bootstrap, del MISMO
	// writeTimeout con el que se arma el servidor de abajo. Cablearlo a mano aquí
	// probaría un arnés inventado en vez de la fórmula de producción.
	presupuesto := publicapi.SendBudgetFrom(writeTimeout)
	api := newAPIConLog(publicapi.Deps{
		Sender:      sender,
		SessionDeps: publicapi.SessionDeps{Sessions: sessions},
		DBTimeout:   presupuestoDB,
		SendBudget:  presupuesto,
	}, apiKeys(), log)

	// Las cotas de tiempo de los escenarios están calculadas sobre
	// satPresupuestoApretado. Si el margen de escritura de producción cambiara, la
	// derivación daría otro número y las cotas medirían contra algo obsoleto: mejor
	// que el test lo diga aquí que verlo fallar como una lentitud inexplicable.
	if writeTimeout == satWriteApretado && presupuesto != satPresupuestoApretado {
		t.Fatalf("SendBudgetFrom(%v) = %v, y las cotas de este fichero suponen %v: "+
			"cambió el margen de escritura, ajusta satPresupuestoApretado",
			writeTimeout, presupuesto, satPresupuestoApretado)
	}

	srv := httptest.NewUnstartedServer(api.mux)
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	t.Cleanup(srv.Close)

	return &satArnes{srv: srv, api: api, log: log}
}

// satGatewayReal arma el camino real Registry → Gateway con el Edge dado registrado
// como sess-a. Devuelve el Server, que es lo que consume publicapi.Deps.Sender. Un
// edge nil deja la sesión SIN registrar: es el caso offline.
func satGatewayReal(t *testing.T, log *dbLogSpy, edge session.Sender, pushTimeout time.Duration) *gatewaygrpc.Server {
	t.Helper()
	reg := session.NewRegistry(session.WithSendTimeout(pushTimeout))
	if edge != nil {
		t.Cleanup(reg.Register("sess-a", edge))
	}
	return gatewaygrpc.New(reg, log, gatewaygrpc.WithAckTimeout(satAckCorto))
}

// satResultado es lo observado por el CLIENTE, que es el punto de vista que el
// criterio de T5.4 exige: no lo que el handler cree haber hecho.
type satResultado struct {
	// code es 0 cuando no hubo respuesta ninguna (el equivalente del curl 52).
	code int
	body []byte
	// err es el error del cliente. No nil ⇒ la conexión se cerró sin respuesta.
	err          error
	transcurrido time.Duration
}

// satEnviar hace el POST de envío contra el servidor real y devuelve lo que el
// cliente vio. Un error del cliente NO es un fallo del test: es el dato que se mide.
func satEnviar(t *testing.T, a *satArnes, credencial, cuerpo string) satResultado {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		a.srv.URL+"/api/v1/messages", strings.NewReader(cuerpo))
	if err != nil {
		t.Fatalf("armando la petición: %v", err)
	}
	if credencial != "" {
		req.Header.Set("Authorization", "Bearer "+a.api.token(credencial))
	}

	cli := &http.Client{Timeout: satEsperaCliente}
	inicio := time.Now()
	resp, err := cli.Do(req) //nolint:bodyclose // se cierra abajo cuando resp no es nil
	transcurrido := time.Since(inicio)
	if err != nil {
		return satResultado{err: err, transcurrido: transcurrido}
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Logf("cerrando el cuerpo: %v", cerr)
		}
	}()
	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		// El cuerpo empezó a llegar y se cortó: también es "respuesta incompleta".
		return satResultado{code: resp.StatusCode, err: rerr, transcurrido: transcurrido}
	}
	return satResultado{code: resp.StatusCode, body: body, transcurrido: transcurrido}
}

// satCuerpoValido es el cuerpo que usa todo este fichero: la sesión sess-a de tenantA.
const satCuerpoValido = `{"session_id":"sess-a","to":"+15551234567","text":"hola"}`

// satAcotar comprueba el tiempo por los DOS lados contra los relojes DECLARADOS del
// arnés, nunca contra un número inventado: por arriba, que algún reloj se aplicó; por
// abajo, que no fue un plazo de cero (un WithTimeout(0) responde en microsegundos y
// sería tan defectuoso como no tener plazo).
func satAcotar(t *testing.T, transcurrido, cotaInferior time.Duration) {
	t.Helper()
	if transcurrido > satCotaSuperior {
		t.Fatalf("tardó %v: ningún reloj lo acotó (el techo del arnés es push=%v + ack=%v)",
			transcurrido, satPushCorto, satAckCorto)
	}
	if transcurrido < cotaInferior {
		t.Fatalf("respondió en %v, por debajo de %v: el plazo fue ~0, no el reloj declarado",
			transcurrido, cotaInferior)
	}
}

// satLiberar devuelve un Edge atascado cuyo bloqueo se suelta al terminar el test, de
// modo que la goroutine del Push no queda colgada más allá de la prueba.
func satLiberar(t *testing.T) satEdgeAtascado {
	t.Helper()
	liberar := make(chan struct{})
	t.Cleanup(func() { close(liberar) })
	return satEdgeAtascado{liberar: liberar}
}

// satExigirVeredicto compara lo observado contra el veredicto declarado. Es el único
// sitio con aserciones sobre el desenlace, para que cambiar de fase sea cambiar el
// veredicto y no reescribir las pruebas.
func satExigirVeredicto(t *testing.T, res satResultado, v satVeredicto, writeTimeout time.Duration) {
	t.Helper()

	if v.code == 0 {
		if res.err == nil {
			t.Fatalf("el cliente SÍ recibió respuesta (code=%d, body=%q).\n"+
				"Si es el arreglo de REQ-050.19, actualiza el veredicto de este escenario "+
				"al código y cuerpo nuevos (ver el bloque de veredictos).", res.code, res.body)
		}
		if res.code != 0 || len(res.body) != 0 {
			t.Fatalf("respuesta a medias inesperada: code=%d body=%q err=%v", res.code, res.body, res.err)
		}
	} else {
		if res.err != nil {
			t.Fatalf("no hubo respuesta y el veredicto esperaba %d: %v", v.code, res.err)
		}
		if res.code != v.code {
			t.Fatalf("code=%d, el veredicto declara %d; body=%s.\n"+
				"Si es el arreglo de REQ-050.19, actualiza el bloque de veredictos.",
				res.code, v.code, res.body)
		}
		if !strings.Contains(string(res.body), v.fragmento) {
			t.Fatalf("el cuerpo no se reconoce a simple vista: %q (falta %q)", res.body, v.fragmento)
		}
		if v.conCmdID {
			if _, cmdID := leerSendError(t, res.body); cmdID == "" {
				t.Fatal("respuesta sin command_id: sin él no hay forma de saber después " +
					"si el mensaje llegó a salir")
			}
		}
	}

	// El reloj: la mitad del criterio que hoy no se cumple.
	dentro := res.transcurrido < writeTimeout
	if dentro != v.dentroDelWriteTimeout {
		t.Fatalf("tardó %v con un WriteTimeout de %v (dentro=%v); el veredicto declara "+
			"dentro=%v.\nSi es el arreglo de REQ-050.19 —el handler ya se rinde dentro "+
			"del presupuesto de la petición—, actualiza el bloque de veredictos.",
			res.transcurrido, writeTimeout, dentro, v.dentroDelWriteTimeout)
	}
}

// ============================================================================
// A · El incidente: el Edge saturado y la respuesta que nunca llega
// ============================================================================

// TestIntegration_GatewaySaturado_SinRespuestaCuandoElPushExcedeElWriteTimeout es la
// reproducción del 2026-08-06. El Edge no lee su stream, el handler se queda en el
// Push hasta que su reloj se rinde, y para entonces el deadline de ESCRITURA de la
// conexión ya venció: el Write falla, el servidor cierra y el cliente se queda sin
// respuesta y sin código. Es exactamente el curl 52.
//
// 🔴 REQ-050.19 exige «cero respuestas vacías» y «un código HTTP explícito siempre».
// HOY NO SE CUMPLE, y este test lo deja constatado en vez de esconderlo: afirma el
// comportamiento REAL (respuesta vacía) y se pondrá rojo el día que se arregle, con
// el mensaje que dice qué hacer entonces. Un test que no distinga «arreglado» de
// «roto» no sirve de red.
//
// El defecto NO es el valor de ningún reloj (INV-050.6 los deja intactos): es que la
// suma de los relojes SECUENCIALES del handler puede exceder el WriteTimeout, y que
// nada interrumpe al handler cuando eso pasa (en Go el WriteTimeout no cancela el
// contexto de la petición, solo hace fallar el Write posterior).
func TestIntegration_GatewaySaturado_SinRespuestaCuandoElPushExcedeElWriteTimeout(t *testing.T) {
	log := &dbLogSpy{}
	gw := satGatewayReal(t, log, satLiberar(t), satPushLargo)
	a := satMontar(t, log, gw, satFlotaCon(), satWriteApretado)

	res := satEnviar(t, a, keyAFull, satCuerpoValido)
	t.Logf("A · Edge atascado, WriteTimeout=%v < push=%v → code=%d body=%q err=%v t=%v",
		satWriteApretado, satPushCorto, res.code, string(res.body), res.err, res.transcurrido)

	satExigirVeredicto(t, res, satEdgeAtascadoSinMargen, satWriteApretado)
	// El tiempo lo fija el PRESUPUESTO, no el reloj del Edge (satPushLargo) ni la
	// suerte: se responde a ~500 ms con un push de 3 s por detrás.
	satAcotar(t, res.transcurrido, satCotaPresupuesto)

	// Lo que SÍ mejoró desde el incidente: el desenlace deja traza con command_id. En
	// 2026-08-06 la ventana muda duró 4 min 13 s con 13 líneas y ninguna HTTP.
	satExigirTrazaDeEnvio(t, a.log.all())

	// ✅ FASE 2: la traza y lo que el cliente recibió ya COINCIDEN. En la fase 1 este
	// mismo punto constataba lo contrario —el log decía «status 500» sobre una respuesta
	// que nunca salió—, y esa discrepancia era más difícil de auditar que la ventana
	// muda del incidente, porque parecía que todo estaba registrado. Se exigen las dos
	// mitades juntas: sin comparar el log CONTRA lo que recibió el cliente, «hay una
	// línea» y «la línea dice la verdad» se confunden.
	if !strings.Contains(a.log.all(), fmt.Sprintf("status%d", res.code)) {
		t.Fatalf("el log no registra el MISMO status que recibió el cliente (%d); log=%q",
			res.code, a.log.all())
	}
	if strings.Contains(a.log.all(), "sin entregar") {
		t.Fatalf("el access-log dice que la respuesta no se entregó, pero el cliente la "+
			"recibió (code=%d); log=%q", res.code, a.log.all())
	}
	t.Logf("A · CERRADO: 504 explícito, con cuerpo y command_id, en %v — dentro del "+
		"WriteTimeout de %v. Antes: conexión cerrada sin cuerpo (curl 52).",
		res.transcurrido, satWriteApretado)
}

// satExigirTrazaDeEnvio afirma que el desenlace del envío dejó una línea con
// command_id y sin PII. Es lo que separa este escenario del incidente original, donde
// no había ninguna.
func satExigirTrazaDeEnvio(t *testing.T, lineas string) {
	t.Helper()
	if !strings.Contains(lineas, "envío por la API pública fallido") {
		t.Fatalf("el desenlace no dejó traza: ventana muda; log=%q", lineas)
	}
	if !strings.Contains(lineas, "command_id") {
		t.Fatalf("la traza no lleva command_id: no es correlacionable; log=%q", lineas)
	}
	if !strings.Contains(lineas, "sess-a") {
		t.Fatalf("la traza no permite ubicar la sesión; log=%q", lineas)
	}
	if strings.Contains(lineas, "+15551234567") || strings.Contains(lineas, "hola") {
		t.Fatalf("PII en el log del envío saturado; log=%q", lineas)
	}
}

// TestIntegration_GatewaySaturado_ConWriteTimeoutHolgadoSiHayCodigo es el CONTROL del
// test de arriba, y sin él la prueba no valdría: con el MISMO Edge atascado y la única
// diferencia del WriteTimeout, la respuesta sí llega. Eso prueba que lo que produce la
// respuesta vacía es la relación entre relojes y no la lentitud de la máquina.
//
// 🔴 REQ-050.19 exige además que el código de «sesión caída» sea distinguible del de
// «plazo agotado». HOY el Edge atascado devuelve **500 «no se pudo enviar el texto»**,
// que no es ninguno de los dos: session.ErrPushTimeout (registry.go:28) NO está en el
// switch de writeSendError (messages.go:189-197) y, por ser un errors.New puro que no
// envuelve context.DeadlineExceeded, tampoco lo recoge esa rama. El test afirma el 500
// de HOY y falla el día del arreglo.
func TestIntegration_GatewaySaturado_ConWriteTimeoutHolgadoSiHayCodigo(t *testing.T) {
	log := &dbLogSpy{}
	gw := satGatewayReal(t, log, satLiberar(t), satPushCorto)
	a := satMontar(t, log, gw, satFlotaCon(), satWriteHolgado)

	res := satEnviar(t, a, keyAFull, satCuerpoValido)
	t.Logf("A' · Edge atascado, WriteTimeout=%v > push=%v → code=%d body=%q err=%v t=%v",
		satWriteHolgado, satPushCorto, res.code, string(res.body), res.err, res.transcurrido)

	// El command_id SÍ viaja ya hoy (lo exige el veredicto), porque el Push vencido sale
	// envuelto en *SendError (send.go:335) y commandIDFrom lo saca por duck-typing. Eso
	// no hay que arreglarlo en la fase 2: hay que no romperlo.
	satExigirVeredicto(t, res, satEdgeAtascadoConMargen, satWriteHolgado)
	satAcotar(t, res.transcurrido, satCotaInferior)
	satExigirTrazaDeEnvio(t, a.log.all())

	msg, cmdID := leerSendError(t, res.body)
	t.Logf("A' · CERRADO: ErrPushTimeout → %d con texto propio (command_id=%s). "+
		"Antes: 500 «no se pudo enviar el texto», el default genérico. Texto: %q",
		res.code, cmdID, msg)
}

// ============================================================================
// Los relojes secuenciales
// ============================================================================

// TestIntegration_GatewaySaturado_LosRelojesDelEnvioSonSecuenciales mide la conducta
// que el comentario de internal/platform/config/config.go:98-105 describe mal. Ese
// comentario afirma que «los DOS relojes del envío son SECUENCIALES ... 1,5+8 = 9,5 s»
// y se salta el tercero: el sendTimeout del Push (registry.go:143, 10 s). El peor caso
// real es 1,5 + 10 + 8 = 19,5 s contra un WriteTimeout de 10 s.
//
// Aquí se prueba el nervio de esa cuenta SIN leer ninguna constante de producción: con
// un Edge que tarda satSendLento en aceptar el comando y luego nunca acusa, el total
// tiene que superar la SUMA de los dos plazos, no el mayor de ellos. Si Push y ack
// fueran alternativos, el POST volvería en ~satSendLento.
func TestIntegration_GatewaySaturado_LosRelojesDelEnvioSonSecuenciales(t *testing.T) {
	log := &dbLogSpy{}
	gw := satGatewayReal(t, log, satEdgeMudo{demora: satSendLento}, satPushCorto)
	a := satMontar(t, log, gw, satFlotaCon(), satWriteHolgado)

	res := satEnviar(t, a, keyAFull, satCuerpoValido)
	t.Logf("F · Push %v + ack %v → code=%d t=%v (suma esperada ≈ %v)",
		satSendLento, satAckCorto, res.code, res.transcurrido, satSendLento+satAckCorto)

	if res.err != nil {
		t.Fatalf("no hubo respuesta: %v", res.err)
	}
	if res.code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d, quiero 504: el Push tuvo éxito y el ack nunca llegó; body=%s",
			res.code, res.body)
	}
	if res.transcurrido < satCotaSecuencial {
		t.Fatalf("tardó %v, menos que push(%v)+ack(%v): los dos relojes no corrieron en "+
			"serie, y toda la aritmética de la invariante contra el WriteTimeout depende "+
			"de que sí", res.transcurrido, satSendLento, satAckCorto)
	}
	satAcotar(t, res.transcurrido, satCotaSecuencial)
	if msg, _ := leerSendError(t, res.body); msg != "timeout esperando el ack del Edge" {
		t.Fatalf("cuerpo=%q, se esperaba el 504 del ack", msg)
	}
}

// ============================================================================
// G · La pregunta que decide si el arreglo de la fase 2 es seguro
// ============================================================================

// TestIntegration_GatewaySaturado_RendirseNoCancelaElEnvio mide, en vez de suponer, lo
// que pasa con el comando cuando el LLAMANTE se rinde a mitad del Push. Es la pregunta
// que gobierna el diseño del presupuesto de petición de la fase 2: si añadir un plazo
// al handler abortase envíos que iban a salir bien, el arreglo cambiaría un fallo
// visible por uno silencioso —y peor— en la vida del cliente final.
//
// El camino se ejercita SIN tocar producción: el ctx del cliente HTTP se cancela a
// mitad de vuelo, y Go cancela con él el r.Context() del handler. Ese es exactamente el
// mismo camino de código que recorrería un context.WithTimeout puesto en el handler,
// porque Registry.Push mira ctx.Done() sin preguntar de dónde viene el plazo.
//
// 🔴 LO QUE SE COMPRUEBA: que el Send SE EJECUTA IGUALMENTE después. Está escrito en
// registry.go:126-131 («cancelar el ctx NO desbloquea el stream.Send de gRPC… lo que el
// ctx compra es que el LLAMANTE deje de esperar, no que el envío se cancele»), y aquí
// se verifica que sigue siendo verdad. Consecuencias para la fase 2, las dos:
//
//   - A FAVOR: el presupuesto NO aborta el envío. Un comando que iba a salir, sale.
//     El arreglo no puede perder mensajes por esta vía.
//   - EN CONTRA: el llamante recibe un error de un envío que después SÍ ocurre. El
//     texto de la respuesta no puede decir «el mensaje NO se envió, reintenta»: sería
//     falso y le duplicaría el WhatsApp a un cliente real.
func TestIntegration_GatewaySaturado_RendirseNoCancelaElEnvio(t *testing.T) {
	liberar := make(chan struct{})
	edge := &satEdgeContado{liberar: liberar}
	log := &dbLogSpy{}
	gw := satGatewayReal(t, log, edge, satPushCorto)
	a := satMontar(t, log, gw, satFlotaCon(), satWriteHolgado)

	// El cliente se rinde a mitad del Push (satRendicionCliente < satPushCorto), que es
	// lo que hará el presupuesto de petición de la fase 2.
	ctx, cancel := context.WithTimeout(t.Context(), satRendicionCliente)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.srv.URL+"/api/v1/messages", strings.NewReader(satCuerpoValido))
	if err != nil {
		t.Fatalf("armando la petición: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.api.token(keyAFull))

	inicio := time.Now()
	resp, doErr := (&http.Client{Timeout: satEsperaCliente}).Do(req) //nolint:bodyclose // resp es nil en este camino
	transcurrido := time.Since(inicio)
	if doErr == nil {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Logf("cerrando el cuerpo: %v", cerr)
		}
		t.Fatalf("el cliente no se rindió: code=%d", resp.StatusCode)
	}
	if edge.total() != 0 {
		t.Fatalf("el Send ya se había ejecutado antes de soltar al Edge: %d", edge.total())
	}
	t.Logf("G · el llamante se rinde a los %v (push=%v) → err=%v", transcurrido, satPushCorto, doErr)

	// El servidor ya cerró el desenlace por su lado. Se comprueba antes de soltar al
	// Edge para que no haya duda de cuál de los dos sucesos ocurrió primero.
	satEsperarTraza(t, log)
	lineas := log.all()
	if !strings.Contains(lineas, "504") {
		t.Fatalf("el Push abandonado por el llamante no salió como 504; log=%q", lineas)
	}

	// Y AHORA el stream se desatasca. El comando que el servidor dio por fallido sale.
	close(liberar)
	satEsperarEnvio(t, edge)
	t.Logf("G · CONSTATADO: el servidor cerró el envío con 504 y el comando SALIÓ igual "+
		"(Send ejecutado %d vez). log=%q", edge.total(), lineas)
}

// satRendicionCliente es cuándo se rinde el llamante del escenario G: a mitad del Push,
// para que la cancelación llegue con el Send todavía bloqueado.
const satRendicionCliente = 150 * time.Millisecond

// satEsperarTraza espera a que el handler cierre su desenlace. Hace falta porque el
// cliente se entera de su propia cancelación ANTES de que el servidor termine: sin la
// espera, la aserción sobre el log correría contra una carrera y fallaría a ratos.
func satEsperarTraza(t *testing.T, log *dbLogSpy) {
	t.Helper()
	limite := time.Now().Add(satCotaSuperior)
	for time.Now().Before(limite) {
		if strings.Contains(log.all(), "envío por la API pública fallido") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("el handler no cerró su desenlace en %v; log=%q", satCotaSuperior, log.all())
}

// satEsperarEnvio espera a que el Send diferido se ejecute tras desatascar el stream.
func satEsperarEnvio(t *testing.T, edge *satEdgeContado) {
	t.Helper()
	limite := time.Now().Add(satCotaSuperior)
	for time.Now().Before(limite) {
		if edge.total() > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("el Send NO se ejecutó tras desatascar el stream.\n" +
		"Si esto cambió, la premisa del arreglo de la fase 2 cambia con ello: hoy el " +
		"comando sale igual aunque el llamante se haya rendido (registry.go:126-131), y " +
		"por eso la respuesta no puede decir «el mensaje NO se envió».")
}

// ============================================================================
// B/C/D · los desenlaces que SÍ tienen que distinguirse entre sí
// ============================================================================

// satCaso describe un desenlace del envío tal y como lo ve el cliente.
type satCaso struct {
	nombre  string
	montar  func(t *testing.T, log *dbLogSpy) publicapi.MessageSender
	código  int
	fragmen string // fragmento que tiene que aparecer en el cuerpo
	conLog  bool   // ¿deja traza el desenlace?
}

// satCasos son los tres desenlaces distinguibles más el camino feliz. Van en la MISMA
// prueba a propósito: cualquiera por separado pasaría contra un handler que devolviera
// siempre lo mismo, y lo que T5.4 pide afirmar es justamente que se distinguen.
func satCasos() []satCaso {
	return []satCaso{
		{
			nombre: "B · sesión offline (sin stream vivo)",
			montar: func(t *testing.T, log *dbLogSpy) publicapi.MessageSender {
				return satGatewayReal(t, log, nil, satPushCorto) // nadie registrado para sess-a
			},
			código:  http.StatusBadGateway,
			fragmen: "sesión offline",
			conLog:  true,
		},
		{
			nombre: "C · el stream se cayó esperando el ack",
			montar: func(_ *testing.T, _ *dbLogSpy) publicapi.MessageSender {
				// Doble, no gateway real: cerrar el stream de una sesión viva exige
				// closeStream/cancelSessionAcks, que no están exportados. Que el Gateway
				// real produce este error se prueba en su propio paquete
				// (internal/gateway/grpc/send_error_test.go); aquí se prueba la traducción.
				return &fakeSender{err: &sendStreamCaidoError{cmdID: "cmd-caido", caido: true}}
			},
			código:  http.StatusGatewayTimeout,
			fragmen: "el stream del Edge se cerró",
			conLog:  true,
		},
		{
			nombre: "D · camino feliz",
			montar: func(_ *testing.T, _ *dbLogSpy) publicapi.MessageSender {
				return &fakeSender{ack: okAck()}
			},
			código:  http.StatusOK,
			fragmen: `"acked_command_id":"cmd-1"`,
			conLog:  true,
		},
	}
}

// TestIntegration_GatewaySaturado_DesenlacesDistinguibles recorre los casos de arriba
// contra el servidor HTTP real y exige que los tres códigos sean distintos entre sí y
// distintos del 500 que hoy produce el Edge atascado.
func TestIntegration_GatewaySaturado_DesenlacesDistinguibles(t *testing.T) {
	// La colisión se detecta DENTRO de cada subtest y no en un recuento al final: un
	// t.Fatalf de subtest no aborta al padre, así que el recuento posterior no sabría
	// distinguir «códigos repetidos» de «un subtest se rindió antes de registrarse» y
	// acusaría de lo primero cuando pasó lo segundo.
	vistos := map[int]string{}
	for _, c := range satCasos() {
		t.Run(c.nombre, satComprobarCaso(c, vistos))
	}
}

// satComprobarCaso devuelve el cuerpo del subtest. Es una función NOMBRADA y no un
// closure inline porque gocyclo imputa los FuncLit anidados a la función madre.
func satComprobarCaso(c satCaso, vistos map[int]string) func(*testing.T) {
	return func(t *testing.T) {
		log := &dbLogSpy{}
		a := satMontar(t, log, c.montar(t, log), satFlotaCon(), satWriteHolgado)

		res := satEnviar(t, a, keyAFull, satCuerpoValido)
		t.Logf("%s → code=%d body=%q t=%v", c.nombre, res.code, string(res.body), res.transcurrido)

		if otro, repetido := vistos[res.code]; repetido {
			t.Fatalf("code=%d ya lo usaba %q: el llamante no puede distinguir «no salió» "+
				"de «no sé si salió»", res.code, otro)
		}
		vistos[res.code] = c.nombre

		if res.err != nil {
			t.Fatalf("respuesta vacía en un desenlace que no debería producirla: %v", res.err)
		}
		if res.code != c.código {
			t.Fatalf("code=%d, quiero %d; body=%s", res.code, c.código, res.body)
		}
		if !strings.Contains(string(res.body), c.fragmen) {
			t.Fatalf("el cuerpo no se reconoce a simple vista: %q (falta %q)", res.body, c.fragmen)
		}
		if c.conLog && !strings.Contains(log.all(), "command_id") {
			t.Fatalf("el desenlace no dejó traza correlacionable; log=%q", log.all())
		}
		if strings.Contains(log.all(), "+15551234567") || strings.Contains(log.all(), "hola") {
			t.Fatalf("PII en el log; log=%q", log.all())
		}
	}
}

// ============================================================================
// E · el access-log: las ventanas mudas, cerradas
// ============================================================================

// satCaminoAntesMudo es un desenlace del handler que hasta la fase 2 no dejaba
// ninguna línea de log.
type satCaminoAntesMudo struct {
	nombre string
	// credencial vacía = sin Authorization.
	credencial string
	cuerpo     string
	// flotaRota cambia el SessionLister por uno que falla (camino del 500).
	flotaRota bool
	código    int
	// origen es el fichero:línea del writeError que respondía sin loguear.
	origen string
}

func satCaminosAntesMudos() []satCaminoAntesMudo {
	return []satCaminoAntesMudo{
		{
			nombre: "401 sin identidad", credencial: "", cuerpo: satCuerpoValido,
			código: http.StatusUnauthorized, origen: "messages.go:68 (y el middleware antes)",
		},
		{
			nombre: "400 cuerpo JSON inválido", credencial: keyAFull, cuerpo: `{no soy json`,
			código: http.StatusBadRequest, origen: "messages.go:74",
		},
		{
			nombre: "400 campos faltantes", credencial: keyAFull, cuerpo: `{"session_id":"sess-a"}`,
			código: http.StatusBadRequest, origen: "messages.go:78",
		},
		{
			nombre: "404 sesión de otro tenant", credencial: keyAFull,
			cuerpo: `{"session_id":"sess-ajena","to":"+15551234567","text":"hola"}`,
			código: http.StatusNotFound, origen: "messages.go:99",
		},
		{
			nombre: "500 la flota no contesta", credencial: keyAFull, cuerpo: satCuerpoValido,
			flotaRota: true, código: http.StatusInternalServerError, origen: "messages.go:94",
		},
	}
}

// TestIntegration_GatewaySaturado_TodaPeticionDejaRastro cierra la otra mitad de
// REQ-050.19: «cero ventanas mudas en el log — toda petición deja rastro».
//
// Los cinco desenlaces de esta tabla respondían MUDOS antes de la fase 2, y ninguna
// capa de arriba los cubría: ni protect, ni AuditMiddleware, ni PublicRateLimit, ni
// InstrumentHTTP emiten una línea por petición. Ahora la emite accessLog
// (accesslog.go), montado dentro de este paquete y no en bootstrap, para que la cadena
// que se prueba sea la que corre.
//
// ⚠️ NO se exige command_id: estos cinco ocurren ANTES de que exista uno que reportar,
// y pedirlo sería inalcanzable por construcción. El command_id lo aportan las líneas de
// envío (messagesHandler y writeSendError); esta aporta la otra mitad —que la petición
// existió y en qué acabó— y las dos se correlacionan por tenant e instante.
//
// Lo que sí se exige, y es lo que impide que este test pase con un log de adorno: la
// línea tiene que llevar el CÓDIGO real del desenlace, la ruta y el método. Un
// access-log que registrara siempre 200 sería peor que no tenerlo.
func TestIntegration_GatewaySaturado_TodaPeticionDejaRastro(t *testing.T) {
	for _, c := range satCaminosAntesMudos() {
		t.Run(c.nombre, satComprobarRastro(c))
	}
}

func satComprobarRastro(c satCaminoAntesMudo) func(*testing.T) {
	return func(t *testing.T) {
		var sessions publicapi.SessionLister = satFlotaCon()
		if c.flotaRota {
			sessions = satFlotaRota{}
		}
		log := &dbLogSpy{}
		a := satMontar(t, log, &fakeSender{ack: okAck()}, sessions, satWriteHolgado)

		res := satEnviar(t, a, c.credencial, c.cuerpo)
		t.Logf("E · %s → code=%d body=%q log=%q", c.nombre, res.code, string(res.body), log.all())

		if res.err != nil {
			t.Fatalf("respuesta vacía inesperada: %v", res.err)
		}
		if res.code != c.código {
			t.Fatalf("code=%d, quiero %d; body=%s", res.code, c.código, res.body)
		}
		if len(res.body) == 0 {
			t.Fatal("respuesta sin cuerpo: REQ-050.19 exige cero respuestas vacías")
		}

		lineas := log.all()
		if lineas == "" {
			t.Fatalf("VENTANA MUDA: este camino (%s) respondió %d sin dejar una sola "+
				"línea. Es el defecto que REQ-050.19 cierra; comprueba que accessLog "+
				"sigue montado en protect/protectRead (publicapi.go)", c.origen, res.code)
		}
		if !strings.Contains(lineas, "petición pública") {
			t.Fatalf("hay log pero no es el access-log: %q", lineas)
		}
		// El código REAL, no un 200 de adorno: sin esto, un access-log que registrara
		// siempre lo mismo pasaría esta prueba y no serviría para nada.
		if !strings.Contains(lineas, fmt.Sprintf("status%d", c.código)) {
			t.Fatalf("la línea no registra el status real (%d): %q", c.código, lineas)
		}
		if !strings.Contains(lineas, "/api/v1/messages") || !strings.Contains(lineas, http.MethodPost) {
			t.Fatalf("la línea no dice qué petición fue (método y ruta): %q", lineas)
		}
		// CERO PII, igual que el resto de los logs de esta capa.
		if strings.Contains(lineas, "+15551234567") || strings.Contains(lineas, "hola") {
			t.Fatalf("PII en el access-log: %q", lineas)
		}
	}
}

// TestIntegration_GatewaySaturado_ElWriteFallidoNoSeDetectaConCuerposPequenos es un
// test de LÍMITE CONOCIDO, y está aquí porque descubrirlo dos veces sale caro.
//
// La intuición razonable —«si writeJSONErr devuelve el error de escritura, el servidor
// se entera de que el cliente no recibió su respuesta»— es FALSA para los cuerpos de
// este endpoint. net/http bufferiza la respuesta (~4 KB) y hace el flush DESPUÉS de que
// el handler retorne, así que un cuerpo pequeño «se escribe» sin error aunque el
// deadline ya haya vencido y el cliente vaya a ver un EOF. Medido: con 350 B y con
// 2 KB, w.Write devuelve nil; a partir de ~3.951 B devuelve `i/o timeout`. Los cuerpos
// de error de esta API rondan los 350 B.
//
// 🔴 Lo que eso significa para el arreglo: el writeJSONErr de writeSendError es
// correcto y cubre los cuerpos grandes y las roturas de conexión detectables, pero NO
// es lo que cierra «cero respuestas vacías». Lo que lo cierra es el PRESUPUESTO: al
// responder siempre con margen por debajo del WriteTimeout, este caso deja de
// producirse. La misma advertencia vale para el camino del 200, cuyo comentario promete
// una detección que con respuestas pequeñas no ocurre — y eso ya era así antes de T5.4.
//
// Se provoca con un WriteTimeout de 50 ms, por debajo del margen de escritura, de modo
// que SendBudgetFrom devuelve 0 y no hay presupuesto que salve la situación. NO es la
// configuración de producción y no pretende serlo.
func TestIntegration_GatewaySaturado_ElWriteFallidoNoSeDetectaConCuerposPequenos(t *testing.T) {
	log := &dbLogSpy{}
	gw := satGatewayReal(t, log, satEdgeMudo{demora: satSendLento}, satPushCorto)
	a := satMontar(t, log, gw, satFlotaCon(), 50*time.Millisecond)

	res := satEnviar(t, a, keyAFull, satCuerpoValido)
	t.Logf("E' · WriteTimeout=50ms (sin presupuesto posible) → code=%d err=%v t=%v",
		res.code, res.err, res.transcurrido)

	if res.err == nil {
		t.Fatalf("se esperaba que el cliente no recibiera la respuesta; code=%d", res.code)
	}
	lineas := log.all()

	// El desenlace SÍ deja rastro, con su código y su command_id: eso es lo que la fase
	// 2 garantiza y lo que el incidente no tenía.
	if !strings.Contains(lineas, "status504") || !strings.Contains(lineas, "command_id") {
		t.Fatalf("el desenlace no dejó traza correlacionable: %q", lineas)
	}
	if !strings.Contains(lineas, "petición pública") {
		t.Fatalf("el access-log no registró la petición: %q", lineas)
	}

	// 🔴 Y el límite: NADIE detecta que no se entregó. Si algún día esto cambia —Go
	// cambia el buffering, o alguien añade un flush explícito con
	// http.NewResponseController— este test se pone rojo y hay que actualizar el
	// párrafo de arriba. El límite queda documentado contra la realidad medida, no
	// contra una suposición.
	if strings.Contains(lineas, "write_error") || strings.Contains(lineas, "sin entregar") {
		t.Fatalf("AHORA el fallo de escritura SÍ se detecta con cuerpos pequeños. Buena "+
			"noticia: actualiza el docstring de este test y el de writeSendError, porque "+
			"el límite que documentan ya no existe; log=%q", lineas)
	}
	t.Log("E' · LÍMITE CONFIRMADO: cuerpo de ~350 B ⇒ w.Write no falla, el flush ocurre " +
		"tras el handler y nadie se entera de que el cliente no recibió nada. Lo que " +
		"evita este caso en producción es el presupuesto, no la detección.")
}

// ============================================================================
// La fórmula del presupuesto
// ============================================================================

// TestIntegration_GatewaySaturado_ElPresupuestoSeDerivaDelWriteTimeout prueba la
// propiedad que hace que la aritmética de los relojes no pueda volver a mentir: el
// presupuesto no es un número suelto, es una función del WriteTimeout. Mover el
// WriteTimeout lo mueve; no hay nada que rehacer a mano.
//
// Las aserciones son sobre la RELACIÓN, no sobre el valor: comparar contra la constante
// margenDeEscritura sería tautológico —pasaría con cualquier margen—, mientras que
// «siempre deja sitio para escribir» y «nunca nace vencido» son propiedades que un
// cambio malo rompe.
func TestIntegration_GatewaySaturado_ElPresupuestoSeDerivaDelWriteTimeout(t *testing.T) {
	for _, wt := range []time.Duration{time.Second, 5 * time.Second, 10 * time.Second, time.Minute} {
		presupuesto := publicapi.SendBudgetFrom(wt)
		if presupuesto < 0 {
			t.Fatalf("SendBudgetFrom(%v) = %v: un presupuesto negativo nace vencido y "+
				"abortaría TODOS los envíos en el acto", wt, presupuesto)
		}
		if presupuesto >= wt {
			t.Fatalf("SendBudgetFrom(%v) = %v: sin margen por debajo del WriteTimeout, el "+
				"handler se rinde justo cuando el deadline de escritura ya venció y "+
				"volvemos a la respuesta vacía", wt, presupuesto)
		}
	}

	// El caso degenerado: un WriteTimeout que no da para el margen devuelve 0 = SIN
	// plazo, nunca un plazo negativo. Sin plazo se vuelve al comportamiento anterior
	// (malo pero conocido); con un plazo vencido al nacer, ningún envío saldría.
	if p := publicapi.SendBudgetFrom(time.Nanosecond); p != 0 {
		t.Fatalf("SendBudgetFrom(1ns) = %v, quiero 0: sin sitio para el margen no puede "+
			"haber presupuesto", p)
	}
	if p := publicapi.SendBudgetFrom(0); p != 0 {
		t.Fatalf("SendBudgetFrom(0) = %v, quiero 0", p)
	}
}
