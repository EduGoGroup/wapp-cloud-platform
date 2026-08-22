// llm_caida_best_effort_test.go custodia UNA MITAD del tercer criterio del Plan
// 044 · Ola 0 · T0.4: «caída simulada de la API ⇒ menús y carrito v1 funcionan
// igual» (REQ-06, INV-10) — la mitad que ocurre DENTRO del turno del cliente.
//
// 🔴 QUÉ CUSTODIA ESTE FICHERO, DICHO SIN ADORNO: la rama best-effort del
// fan-out de dispatch() (resume.go:184-185) cuando el sink que falla es el del
// LLM. Es decir: que un EventSink caído —por ahí entra al runtime el
// AggregatorSink que construirá T1.1, design §2— no altere ni un saliente ni el
// estado guardado. Dentro de dispatch() el turno ya se decidió: el motor no conoce el
// LLM (engine.Step), el envío tampoco (send.go), y el Save viene después.
//
// 🔴 QUÉ NO CUSTODIA, Y ES LA MAYOR PARTE DEL RIESGO REAL DE INV-10. El LLM NO
// entra al sistema por una sola puerta, y afirmarlo sería falso:
//
//	(a) EL SINK NO LLAMA AL PROVEEDOR. El AggregatorSink hace «ventana + flush +
//	    persistencia intake_jobs(aggregating)» (design §5 y la tabla §9): las
//	    llamadas P2/P3/P4 viven en internal/intake/pipeline.go y las mueve un
//	    WORKER sobre intake_jobs, fuera de dispatch y fuera del turno. Ahí es
//	    donde el proveedor va a fallar de verdad, y ahí NO llega este fichero.
//	    Dueño de esa custodia: Plan 044 · Ola 2 · T2.5.
//	(b) HAY UNA SEGUNDA PUERTA, Y ES HTTP. POST /api/v1/intakes/{id}/reanalyze
//	    (design §8.1) dispara el pipeline sin pasar por dispatch ni por el
//	    runtime de flujos. Dueño de esa custodia: Plan 044 · Ola 4 · T4.6.
//
// Lo que sigue, por tanto, es una REGRESIÓN DEL FAN-OUT —valiosa y suficiente
// para lo suyo—, no la prueba entera de INV-10.
//
// LO QUE SOSTIENE LA PROMESA es la rama best-effort de dispatch()
// (resume.go:184-185): un error de sink que NO viene marcado con
// ErrMaterializationFailed se loguea y el fan-out sigue. La excepción acotada de
// D-054.4 —reintento y corte del turno— es SOLO para el sink que MATERIALIZA
// contenido durable, y el del LLM no materializa nada del pedido.
//
// ⚠️ CONDICIÓN PARA T1.1, que estos tests fijan por el lado del runtime pero no
// pueden imponer sobre código que aún no existe: el sink del pipeline LLM NO
// debe marcar su error con runtime.ErrMaterializationFailed. Si lo marcara, un
// proveedor caído cortaría el turno del cliente en un flujo durable — que es
// exactamente lo que INV-10 prohíbe.
//
// Estas dos escenas NO repiten a TestDurableSink_SinkNoDurableSigueBestEffort_
// NoCorta (durable_sink_retry_test.go): aquella mira un sink de PhaseNotify con
// un error inventado y afirma que el turno no se corta; éstas miran un sink de
// PhaseProject —la fase donde el agregador tiene más papeletas de vivir, porque
// lee el efecto recién proyectado— con una caída de red REAL, y afirman algo más
// fuerte que «no se corta»: que la salida al cliente y el estado guardado son
// IDÉNTICOS a los de la corrida sin LLM ninguno.
package runtime_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// sinkLLMCaído hace de PROVEEDOR de la etapa LLM con la API caída. Es un
// EventSink de la fase por defecto (PhaseProject) que, por cada efecto, llama de
// verdad por HTTP a una dirección que ya no escucha a nadie.
//
// LA LLAMADA ES REAL a propósito, en vez de un errors.New: lo que hay que probar
// es que una caída de infraestructura —la forma que tiene de fallar un proveedor
// externo, con su error de red envuelto— no altera el turno. Un error fabricado
// a mano probaría el fan-out, no el escenario.
type sinkLLMCaído struct {
	url     string
	cliente *http.Client

	mu       sync.Mutex
	llamadas int
	último   error
}

// nuevoSinkLLMCaído levanta un servidor de pruebas SOLO para quedarse con su
// dirección y lo cierra en el acto: así la URL apunta a un puerto que estuvo
// vivo y ya no lo está, que es una caída de proveedor y no un puerto inventado
// que podría estar ocupado por otra cosa.
func nuevoSinkLLMCaído(t *testing.T) *sinkLLMCaído {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close()
	return &sinkLLMCaído{url: url, cliente: &http.Client{Timeout: 2 * time.Second}}
}

// Handle implementa runtime.EventSink: cuenta la llamada, intenta hablar con el
// proveedor y devuelve el error TAL CUAL, sin marcarlo con
// ErrMaterializationFailed — ver la condición para T1.1 en la cabecera.
func (s *sinkLLMCaído) Handle(ctx context.Context, _ runtime.EffectContext, _ modules.Effect) error {
	err := s.pedirAlProveedor(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.llamadas++
	s.último = err
	return err
}

// pedirAlProveedor imita la llamada de la implementación `api` (T0.2): un POST
// al endpoint de mensajes del proveedor. Contra el puerto cerrado siempre falla
// en el Do; las otras dos salidas existen para que la función sea honesta si
// alguien reapunta la URL a un servidor vivo.
func (s *sinkLLMCaído) pedirAlProveedor(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/v1/messages", http.NoBody)
	if err != nil {
		return fmt.Errorf("armando la llamada al proveedor LLM: %w", err)
	}
	resp, err := s.cliente.Do(req)
	if err != nil {
		return fmt.Errorf("el proveedor LLM no responde: %w", err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		return fmt.Errorf("cerrando la respuesta del proveedor LLM: %w", cerr)
	}
	return fmt.Errorf("el proveedor LLM devolvió %d sin artefacto utilizable", resp.StatusCode)
}

func (s *sinkLLMCaído) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.llamadas
}

func (s *sinkLLMCaído) errorObservado() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.último
}

// exigeQueElProveedorSeCayó comprueba las DOS mitades sin las cuales estas
// escenas pasarían por casualidad: que el sink llegó a correr (si el efecto no
// llegara a él, el test no estaría mirando nada) y que la llamada falló de
// verdad (si el puerto respondiera, la caída no se habría simulado).
func exigeQueElProveedorSeCayó(t *testing.T, llm *sinkLLMCaído, llamadasEsperadas int) {
	t.Helper()
	if got := llm.count(); got != llamadasEsperadas {
		t.Fatalf("el sink del LLM debía recibir %d efecto(s), recibió %d — el test no está mirando lo que dice",
			llamadasEsperadas, got)
	}
	if err := llm.errorObservado(); err == nil {
		t.Fatal("la llamada al proveedor NO falló: la caída no se simuló y la escena no prueba nada")
	}
}

// corrida es la foto de lo que un turno produjo hacia fuera: lo que el cliente
// recibió y dónde quedó la conversación. Comparar dos corridas es lo que
// convierte «no se rompió» en «funciona IGUAL».
type corrida struct {
	textos    []string
	nodo      string
	terminado bool
}

// corridaDeMenú avanza un turno del flujo de menú (sampleFlow) con los sinks
// extra que se le den y devuelve su foto. El motor declara un efecto para que el
// fan-out tenga algo que repartir: sin efecto, ningún sink correría y la escena
// del menú sería vacía.
func corridaDeMenú(t *testing.T, extra ...runtime.EventSink) corrida {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	opts := make([]runtime.Option, 0, len(extra))
	for _, s := range extra {
		opts = append(opts, runtime.WithEventSink(s))
	}
	rt := runtime.New(repo, newEffectEngine([]modules.Effect{sampleEffect()}), sender,
		fakeResolver{tenantID: testTenant}, contacts, discardLogger(), opts...)
	if err := startAndStep(t, rt); err != nil {
		t.Fatalf("el turno no debe fallar: %v", err)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	return corrida{textos: sender.texts(), nodo: st.CurrentNode, terminado: st.Finished()}
}

// TestSinkDelLLMCaídoEnDispatch_ElMenúRespondeIgual es la primera mitad: el
// flujo de menú con el SINK del LLM caído produce EXACTAMENTE los mismos
// salientes y el mismo estado que sin pipeline ninguno. No basta con que el
// turno no devuelva error: lo que INV-10 promete es que el cliente no note nada.
// Cubre el fan-out, no el worker del pipeline ni /reanalyze (ver la cabecera).
//
// 🔬 MUTACIÓN: quitar el `continue` best-effort de dispatch (resume.go:185, bajo
// la condición de :184) para que cualquier error de sink se propague ⇒
// startAndStep devuelve error y el test cae en su primer Fatalf. Y si alguien
// deja de registrar el sink, salta exigeQueElProveedorSeCayó.
func TestSinkDelLLMCaídoEnDispatch_ElMenúRespondeIgual(t *testing.T) {
	sinLLM := corridaDeMenú(t)

	llm := nuevoSinkLLMCaído(t)
	conLLMCaído := corridaDeMenú(t, llm)

	exigeQueElProveedorSeCayó(t, llm, 1)
	if !slices.Equal(sinLLM.textos, conLLMCaído.textos) {
		t.Fatalf("el menú respondió DISTINTO con la API caída:\n sin LLM = %q\n con LLM caído = %q",
			sinLLM.textos, conLLMCaído.textos)
	}
	if sinLLM.nodo != conLLMCaído.nodo || sinLLM.terminado != conLLMCaído.terminado {
		t.Fatalf("el estado quedó distinto: sin LLM {nodo=%q terminado=%v}, con LLM caído {nodo=%q terminado=%v}",
			sinLLM.nodo, sinLLM.terminado, conLLMCaído.nodo, conLLMCaído.terminado)
	}
}

// TestSinkDelLLMCaídoEnDispatch_ElCarritoV1SeCierraIgual es la segunda mitad, y
// la que más pesa de las dos: el carrito es DURABLE
// (ProducesDurableContent()==true), así que es el único flujo donde un sink
// puede cortarle el turno al cliente (D-054.4). Con el sink del LLM caído el
// pedido tiene que cerrarse igual —despedida normal, sin aviso de fallo, con su
// solicitud proyectada— y el sink caído no debe gastar ni un reintento del cupo
// durable, que no es suyo.
//
// 🔬 MUTACIÓN: hacer que dispatch entre al reintento acotado por `ec.Durable`
// solo, sin exigir ErrMaterializationFailed (resume.go:184) ⇒ el sink del LLM se
// llamaría 3 veces y el turno se cortaría: caen a la vez el conteo de llamadas,
// la despedida y la solicitud proyectada.
func TestSinkDelLLMCaídoEnDispatch_ElCarritoV1SeCierraIgual(t *testing.T) {
	llm := nuevoSinkLLMCaído(t)
	almacén := store.NewMemoryRepository()
	rt, repo, sender, cid := newDurableCartRuntime(t, almacén, llm)

	if err := rt.HandleIncoming(context.Background(), testSession,
		incoming(testContact, "confirmar", "wamid.llm-caido")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	// Una sola llamada: el cupo de D-054.4 es del sink que MATERIALIZA, y el del
	// LLM no lo es. Reintentarlo alargaría el turno del cliente por un artefacto
	// que nadie le va a enseñar en esta conversación.
	exigeQueElProveedorSeCayó(t, llm, 1)

	if got := sender.count(); got != 1 {
		t.Fatalf("el cliente debe recibir EXACTAMENTE un saliente (la despedida), recibió %d: %v",
			got, sender.texts())
	}
	salida := sender.texts()[0]
	if !strings.Contains(salida, despedidaCartText) {
		t.Fatalf("el carrito debe cerrar con la despedida normal pese a la API caída, salió: %q", salida)
	}
	if strings.Contains(salida, "No pudimos registrar tu pedido") {
		t.Fatalf("la caída del LLM NO debe producir el aviso de fallo del turno durable: %q", salida)
	}
	st := loadState(t, repo, cid)
	if st.CurrentNode != model.NodeTerminal || st.Outcome() != model.OutcomeCompleted {
		t.Fatalf("el flujo debe terminar completed pese a la API caída: node=%q outcome=%q",
			st.CurrentNode, st.Outcome())
	}
	items := almacén.Intakes()
	if len(items) != 1 || items[0].Status != "closed" {
		t.Fatalf("la solicitud del carrito debe quedar cerrada igualmente: %+v", items)
	}
}
