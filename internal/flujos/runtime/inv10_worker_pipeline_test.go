// inv10_worker_pipeline_test.go custodia LA OTRA MITAD de INV-10 — la que
// `llm_caida_best_effort_test.go` dice por escrito que NO cubre.
//
// # POR QUÉ EXISTE ESTE FICHERO, Y QUÉ ATRIBUCIÓN CORRIGE
//
// T0.4 dejó escrito que su test de caída custodiaba INV-10 «porque el LLM entra por
// una sola puerta, el fan-out de dispatch()». Era FALSO, y su propia cabecera acabó
// diciéndolo: el `AggregatorSink` solo hace ventana + flush + persistencia
// `intake_jobs(aggregating)` y NO LLAMA AL PROVEEDOR. Quien lo llama es el WORKER del
// pipeline (Plan 044 · Ola 2 · T2.5), sobre `intake_jobs`, FUERA del turno del cliente.
// Ahí es donde el proveedor falla de verdad, y hasta este fichero no lo custodiaba
// nadie.
//
// ⚠️ Y SIGUE QUEDANDO UNA PUERTA FUERA: `POST /api/v1/intakes/{id}/reanalyze` (design
// §8.1) dispara el pipeline sin pasar por dispatch ni por el worker. Dueño: Ola 4 ·
// T4.6. Este fichero NO la cubre y no se le debe atribuir.
//
// # LOS CUATRO PUNTOS DEL CRITERIO, Y DÓNDE ESTÁ CADA UNO
//
//	(1) el job queda `pending` con backoff y JAMÁS `done`   → TestINV10_...ElJobNuncaLlegaADone
//	(2) el flujo estático del MISMO contacto responde IGUAL → TestINV10_...ElMenuYElCarritoRespondenIGUAL
//	(3) cero salientes originados en el pipeline            → ídem (el conteo del sender)
//	(4) ni goroutines filtradas ni latencia en el turno     → TestINV10_...NiFiltraNiBloqueaElTurno
//
// # EL PROVEEDOR ESTÁ CAÍDO **DE VERDAD**
//
// `proveedorLLMMuerto` levanta un servidor HTTP de pruebas, se queda con su dirección y
// lo cierra en el acto: la URL apunta a un puerto que ESTUVO vivo y ya no lo está, así
// que cada llamada produce un `*url.Error` real envolviendo un `connection refused`. Es
// el patrón de `sinkLLMCaído` y no un `errors.New`, por el mismo motivo que allí: lo que
// hay que probar es que una caída de infraestructura no altera el turno, y un error
// fabricado a mano probaría el fan-out, no el escenario.
package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// ---------------------------------------------------------------------------
// EL PROVEEDOR CAÍDO DE VERDAD
// ---------------------------------------------------------------------------

// demoraDelProveedor es cuánto tarda cada llamada en fallar. NO es realismo por
// realismo: es lo que garantiza que, cuando el test corre un turno del cliente, hay una
// llamada al proveedor EN VUELO. Sin demora, la llamada fallaría en microsegundos y
// «durante la caída» sería una casualidad de planificador, no una condición del test.
const demoraDelProveedor = time.Second

// proveedorLLMMuerto implementa llm.LLMProvider hablando de verdad por HTTP con un
// puerto muerto. Embebe la interfaz para no tener que escribir los métodos que el
// pipeline de la Ola 2 no usa: si alguien los llamara, el nil panic diría exactamente
// qué método falta en vez de devolver un cero silencioso.
type proveedorLLMMuerto struct {
	llm.LLMProvider
	url     string
	cliente *http.Client

	mu       sync.Mutex
	llamadas int
	enVuelo  int
	último   error
}

func nuevoProveedorLLMMuerto(t *testing.T) *proveedorLLMMuerto {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close()
	return &proveedorLLMMuerto{url: url, cliente: &http.Client{Timeout: 2 * time.Second}}
}

// pedir es la llamada real. Las tres salidas existen para que la función sea honesta si
// alguien reapunta la URL a un servidor vivo; contra el puerto cerrado siempre falla en
// el Do.
func (p *proveedorLLMMuerto) pedir(ctx context.Context) (json.RawMessage, error) {
	p.mu.Lock()
	p.llamadas++
	p.enVuelo++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.enVuelo--
		p.mu.Unlock()
	}()

	select {
	case <-time.After(demoraDelProveedor):
	case <-ctx.Done():
		return nil, fmt.Errorf("el proveedor LLM no contestó a tiempo: %w", ctx.Err())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url+"/api/generate", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("armando la llamada al proveedor LLM: %w", err)
	}
	resp, err := p.cliente.Do(req)
	if err != nil {
		e := fmt.Errorf("el proveedor LLM no responde: %w", err)
		p.mu.Lock()
		p.último = e
		p.mu.Unlock()
		return nil, e
	}
	if cerr := resp.Body.Close(); cerr != nil {
		return nil, fmt.Errorf("cerrando la respuesta del proveedor LLM: %w", cerr)
	}
	return nil, fmt.Errorf("el proveedor LLM devolvió %d sin artefacto utilizable", resp.StatusCode)
}

func (p *proveedorLLMMuerto) ExtractMainIdeas(ctx context.Context, _ llm.ExtractMainIdeasInput,
	_ llm.Options) (json.RawMessage, error) {
	return p.pedir(ctx)
}

func (p *proveedorLLMMuerto) ExtractItemSpecs(ctx context.Context, _ llm.ExtractItemSpecsInput,
	_ llm.Options) (json.RawMessage, error) {
	return p.pedir(ctx)
}

func (p *proveedorLLMMuerto) NormalizeQuantities(ctx context.Context, _ llm.NormalizeQuantitiesInput,
	_ llm.Options) (json.RawMessage, error) {
	return p.pedir(ctx)
}

func (p *proveedorLLMMuerto) cuenta() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.llamadas
}

func (p *proveedorLLMMuerto) hayLlamadaEnVuelo() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.enVuelo > 0
}

func (p *proveedorLLMMuerto) errorObservado() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.último
}

// selectorDelMuerto devuelve siempre el proveedor caído, venga el tenant que venga.
type selectorDelMuerto struct{ prov llm.LLMProvider }

func (s selectorDelMuerto) For(_ context.Context, _, _ string) (llm.LLMProvider, error) {
	return s.prov, nil
}

// cifraDePrueba abre el sobre. Devuelve un literal fijo: lo que este fichero prueba es
// que el pipeline no toca el turno, no el envelope (que tiene sus propios tests).
type cifraDePrueba struct{}

func (cifraDePrueba) Decrypt(_, _ []byte, _ string) (string, error) {
	return "quiero dos tortas de chocolate para el viernes", nil
}

// ---------------------------------------------------------------------------
// EL BANCO: WORKER REAL, ETAPAS REALES, PROVEEDOR MUERTO
// ---------------------------------------------------------------------------

// pipelineCaído es el worker de T2.5 cableado con las etapas de VERDAD (stages.P2/P3/P4)
// contra el proveedor muerto, más un job `pending` del MISMO contacto que usan los
// tests del runtime de este paquete.
type pipelineCaído struct {
	worker *pipeline.Worker
	store  *pipeline.StoreEnMemoria
	prov   *proveedorLLMMuerto
	jobID  string
}

// nuevoPipelineCaído arma el banco. El backoff se pone en milisegundos A PROPÓSITO: lo
// que este fichero prueba NO es la curva (eso es de `pipeline_test.go`, con su reloj
// falso) sino que el worker martillee de verdad mientras el cliente conversa. Con la
// base real de 30 s el worker haría UN intento y se quedaría quieto, y «durante la
// caída» dejaría de significar nada.
func nuevoPipelineCaído(t *testing.T) *pipelineCaído {
	t.Helper()
	prov := nuevoProveedorLLMMuerto(t)
	almacén := pipeline.NuevoStoreEnMemoria(nil)
	log := discardLogger()
	sel := selectorDelMuerto{prov: prov}

	p2, err := stages.NewP2(log, sel, almacén, stages.ConPlazoPorLlamada(pipeline.PlazoPorLlamadaSuelo))
	if err != nil {
		t.Fatalf("construir P2: %v", err)
	}
	p3, err := stages.NewP3(log, sel, almacén, stages.ConPlazoPorLlamada(pipeline.PlazoPorLlamadaSuelo))
	if err != nil {
		t.Fatalf("construir P3: %v", err)
	}
	p4, err := stages.NewP4(log, sel, almacén, stages.ZonaPorDefecto,
		stages.ConPlazoPorLlamada(pipeline.PlazoPorLlamadaSuelo))
	if err != nil {
		t.Fatalf("construir P4: %v", err)
	}

	// LAS DOS ETAPAS DE LA OLA 3 Y LA CACHÉ DEL CATÁLOGO (T3.8). Son REALES y no
	// dobles: aquí ya se construyen P2-P4 de verdad, y un doble solo para las dos
	// últimas dejaría este test probando un worker distinto del de producción.
	//
	// En ESTE escenario no llegan a correr —el proveedor está muerto y P2 tumba la
	// cadena en su primera llamada—, que es justo lo que el criterio INV-10 mira: el
	// job no llega a `done`. Están porque el constructor las exige, y las exige para
	// que nadie pueda volver a dejarlas apagadas.
	repoDeFlujos := store.NewMemoryRepository()
	etapaMatch, err := stages.NewMatch(log, almacén)
	if err != nil {
		t.Fatalf("construir el match: %v", err)
	}
	etapaDraft, err := stages.NewDraft(log, almacén, repoDeFlujos, intakes.NewMemoryStore(), repoDeFlujos)
	if err != nil {
		t.Fatalf("construir el draft: %v", err)
	}
	// 🔴 EL CATÁLOGO ENTRA POR `pipeline.NuevoCatalogoEnMemoria` Y NO POR
	// `catalogo.NewCache`, y no es comodidad: `TestFrontera_NingunFicheroDeFlujosImportaElIndice`
	// prohíbe que ningún fichero de `internal/flujos/**` —tests incluidos, y lo dice
	// con esas palabras— importe `internal/intake/catalogo`. Construirlo aquí cruzaría
	// esa frontera; usar el doble que vive en `pipeline` no la toca.
	catalogos, err := pipeline.NuevoCatalogoEnMemoria()
	if err != nil {
		t.Fatalf("construir el catálogo en memoria: %v", err)
	}

	w, err := pipeline.NewWorker(log, almacén, p2, p3, p4,
		etapaMatch, etapaDraft, catalogos, cifraDePrueba{}, pipeline.Config{
			Cadencia:         time.Millisecond,
			BackoffBase:      time.Millisecond,
			BackoffTope:      5 * time.Millisecond,
			MaxIntentosInfra: 1_000_000, // que no muera dentro del test: lo que se prueba es que NO llega a done
		})
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}

	// El job es del MISMO tenant/sesión/contacto que el flujo estático de este
	// paquete: el criterio habla del «flujo estático del MISMO contacto».
	jobID := almacén.Sembrar(pipeline.Fila{
		Key: intake.WindowKey{
			TenantID: testTenant, SessionID: testSession, ContactID: testContact,
			EventID: "33333333-3333-3333-3333-333333333333",
		},
		SourceText: intake.SourceText{Enc: []byte("cifrado"), DEK: []byte("dek"), KEKID: "kek-1"},
		MessageTS:  time.Now().UTC(),
	})
	return &pipelineCaído{worker: w, store: almacén, prov: prov, jobID: jobID}
}

// arrancar pone el worker a correr y devuelve la función que lo para y espera. El
// `t.Cleanup` NO sustituye a llamarla: los tests necesitan que el worker esté PARADO
// antes de contar goroutines.
func (p *pipelineCaído) arrancar(t *testing.T) (parar func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		p.worker.Run(ctx)
	}()
	var unaVez sync.Once
	return func() {
		unaVez.Do(func() {
			cancel()
			select {
			case <-fin:
			case <-time.After(10 * time.Second):
				t.Error("el worker no volvió tras cancelar el contexto")
			}
		})
	}
}

// esperarLlamadaEnVuelo bloquea hasta que hay una llamada al proveedor EN CURSO. Es lo
// que convierte «durante la caída» en una condición del test y no en una esperanza.
func (p *pipelineCaído) esperarLlamadaEnVuelo(t *testing.T) {
	t.Helper()
	limite := time.Now().Add(10 * time.Second)
	for !p.prov.hayLlamadaEnVuelo() {
		if time.Now().After(limite) {
			t.Fatalf("el worker no llegó a llamar al proveedor (llamadas=%d): el test no está mirando nada",
				p.prov.cuenta())
		}
		time.Sleep(time.Millisecond)
	}
}

// esperarIntentos bloquea hasta que el job acumule `n` intentos cobrados.
func (p *pipelineCaído) esperarIntentos(t *testing.T, n int) pipeline.Fila {
	t.Helper()
	limite := time.Now().Add(20 * time.Second)
	for {
		f, ok := p.store.Ver(p.jobID)
		if !ok {
			t.Fatalf("la fila %s desapareció", p.jobID)
		}
		if f.Attempts >= n {
			return f
		}
		if time.Now().After(limite) {
			t.Fatalf("el job no llegó a %d intentos (va por %d, estado %q, llamadas al proveedor %d)",
				n, f.Attempts, f.Status, p.prov.cuenta())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// exigeQueElProveedorSeCayóDeVerdad comprueba las dos mitades sin las cuales todo lo
// demás pasaría por casualidad: que se le llamó, y que la llamada falló.
//
// 🔴 ESPERA a que una llamada haya TERMINADO de fallar, no le basta con que haya
// empezado: `demoraDelProveedor` hace que una llamada esté en vuelo durante un segundo,
// y un turno del cliente dura microsegundos. Sin la espera, esta comprobación miraría
// `último` cuando todavía es nil y declararía en falso que la caída no se simuló — que
// es exactamente lo que hizo la primera versión de este fichero.
func (p *pipelineCaído) exigeQueElProveedorSeCayóDeVerdad(t *testing.T) {
	t.Helper()
	limite := time.Now().Add(15 * time.Second)
	err := p.prov.errorObservado()
	for err == nil && time.Now().Before(limite) {
		time.Sleep(5 * time.Millisecond)
		err = p.prov.errorObservado()
	}
	if p.prov.cuenta() == 0 {
		t.Fatal("nadie llamó al proveedor: el worker no corrió y el test no prueba nada")
	}
	if err == nil {
		t.Fatal("ninguna llamada al proveedor llegó a fallar: la caída no se simuló y la escena no prueba nada")
	}
	if !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "refused") {
		t.Fatalf("el error no parece de red REAL, parece fabricado: %v", err)
	}
}

// ---------------------------------------------------------------------------
// (1) EL JOB QUEDA `pending` CON BACKOFF Y JAMÁS `done`
// ---------------------------------------------------------------------------

// TestINV10_ProveedorCaido_ElJobNuncaLlegaADone es el punto (1) del criterio: con el
// proveedor caído y el worker martilleando, el job vuelve una y otra vez a `pending`
// con su marca empujada, y NUNCA aparece en `done`.
//
// El «jamás» se mide MIRANDO durante la ventana en la que el worker está trabajando, no
// una sola vez al final: un estado terminal es absorbente, así que una foto final no
// distinguiría «nunca estuvo en done» de «pasó por done y volvió» — que es imposible,
// pero eso es una conclusión sobre la máquina, no algo que este test pueda dar por
// hecho.
//
// 🔬 MUTACIÓN EJECUTADA (M18): en `pipeline.(*Worker).ideas`, devolver
// `&llm.MainIdeas{Version: llm.ArtifactVersion}, nil` cuando P2 falla —o sea, tragarse
// la caída del proveedor SIN pasar por `tropiezo`—. COMPILA. RESULTADO: rojo, el job
// llega a `done` en el primer intento.
//
// 🔴 UNA MUTACIÓN MÁS FLOJA NO BASTA, Y ES UN HALLAZGO: ignorar el error en `cadena`
// pero DEJANDO que `desenlace` llame a `tropiezo` sale VERDE, porque `tropiezo` ya
// devolvió el job a `pending` y el `Finish` posterior encuentra el guard
// `status = 'processing'` y afecta 0 filas. O sea que después de un tropiezo el `done`
// es inalcanzable por construcción de la máquina, no solo por el `if err != nil` del
// worker. Medido con la mutación, no supuesto.
func TestINV10_ProveedorCaido_ElJobNuncaLlegaADone(t *testing.T) {
	p := nuevoPipelineCaído(t)
	parar := p.arrancar(t)
	defer parar()

	// Un vigía mira el estado mientras el worker martillea: nadie puede ver `done`.
	//
	// 🔴 EL CANAL DE PARADA Y EL DE HALLAZGO SON DISTINTOS, y no es un detalle de
	// estilo: con uno solo, el `close` que para al vigía haría que el `select` de
	// abajo leyera el VALOR CERO de un canal cerrado y el test fallara siempre
	// declarando que vio un estado `""`. Le pasó a la primera versión de este fichero.
	alto := make(chan struct{})
	fin := make(chan struct{})
	vioDone := make(chan string, 1)
	go func() {
		defer close(fin)
		for {
			select {
			case <-alto:
				return
			default:
			}
			if f, ok := p.store.Ver(p.jobID); ok && f.Status == intake.StatusDone {
				vioDone <- f.Status
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	f := p.esperarIntentos(t, 2)
	close(alto)
	<-fin
	parar()

	p.exigeQueElProveedorSeCayóDeVerdad(t)
	select {
	case s := <-vioDone:
		t.Fatalf("el job pasó por %q con el proveedor caído: INV-10 (1) roto", s)
	default:
	}
	if f.Status != intake.StatusPending {
		t.Fatalf("entre intentos el job debe quedar PENDING, quedó %q", f.Status)
	}
	if f.NextAttemptAt.IsZero() {
		t.Fatal("el job volvió a la cola SIN backoff: la marca sigue en cero")
	}
	if final, _ := p.store.Ver(p.jobID); final.Status == intake.StatusDone {
		t.Fatal("el job acabó en DONE tras parar el worker: INV-10 (1) roto")
	}
}

// ---------------------------------------------------------------------------
// (2) y (3) EL FLUJO ESTÁTICO RESPONDE **IGUAL**, Y CERO SALIENTES DEL PIPELINE
// ---------------------------------------------------------------------------

// TestINV10_ProveedorCaido_ElMenuRespondeIGUAL_DuranteYDespues es el punto (2) para el
// menú y el punto (3) entero: mismos salientes y mismo estado guardado que sin pipeline
// ninguno, tanto MIENTRAS hay una llamada al proveedor en vuelo como DESPUÉS de parar
// el worker.
//
// 🔴 «CERO SALIENTES ORIGINADOS EN EL PIPELINE» SE MIDE ASÍ Y NO CONTANDO CERO: si el
// pipeline mandara algo al cliente, la lista de textos de la corrida con worker tendría
// una entrada de más que la de sin worker, y `slices.Equal` lo dice. Contar «cero
// mensajes del pipeline» exigiría poder distinguirlos, y la única forma honesta de
// distinguirlos es que la corrida sea IDÉNTICA.
//
// 🔬 MUTACIÓN EJECUTADA (M22, del MONTAJE): en `nuevoProveedorLLMMuerto`, cambiar
// `srv.Close()` por `t.Cleanup(srv.Close)`, o sea NO tirar el servidor. COMPILA.
// RESULTADO: rojo — `exigeQueElProveedorSeCayóDeVerdad` dice que la caída no se simuló.
// Es la mutación que importa aquí, y hay que decir por qué:
//
// 🔴 **NO EXISTE NINGUNA MUTACIÓN DE CÓDIGO DE PRODUCCIÓN QUE PONGA ROJA ESTA ESCENA, y
// eso ES el resultado.** El menú no es durable, así que un sink que falle se traga por
// la rama best-effort de `dispatch()`; y el worker del pipeline no tiene delante ni el
// `Sender`, ni el repositorio de flujos, ni el estado de la conversación — no hay una
// línea que tocar que cambie lo que el cliente ve. La garantía de esta mitad es
// ESTRUCTURAL, no de conducta, y por eso lo único que se puede romper es el montaje.
// La mitad que SÍ es de conducta —y sí tiene mutación de producción— es la del carrito,
// que es durable: ver el test siguiente (M20).
func TestINV10_ProveedorCaido_ElMenuRespondeIGUAL_DuranteYDespues(t *testing.T) {
	sinPipeline := corridaDeMenú(t)

	p := nuevoPipelineCaído(t)
	parar := p.arrancar(t)
	defer parar()
	p.esperarLlamadaEnVuelo(t)

	durante := corridaDeMenú(t)
	exigeCorridasIguales(t, "durante la caída", sinPipeline, durante)

	p.esperarIntentos(t, 2)
	parar()

	despues := corridaDeMenú(t)
	exigeCorridasIguales(t, "después de la caída", sinPipeline, despues)
	p.exigeQueElProveedorSeCayóDeVerdad(t)
}

// exigeCorridasIguales compara las dos fotos: los salientes y el estado guardado.
func exigeCorridasIguales(t *testing.T, cuando string, esperada, obtenida corrida) {
	t.Helper()
	if !slices.Equal(esperada.textos, obtenida.textos) {
		t.Fatalf("%s el menú respondió DISTINTO:\n sin pipeline = %q\n con pipeline = %q",
			cuando, esperada.textos, obtenida.textos)
	}
	if esperada.nodo != obtenida.nodo || esperada.terminado != obtenida.terminado {
		t.Fatalf("%s el estado guardado quedó distinto: sin pipeline {nodo=%q terminado=%v}, con pipeline {nodo=%q terminado=%v}",
			cuando, esperada.nodo, esperada.terminado, obtenida.nodo, obtenida.terminado)
	}
}

// TestINV10_ProveedorCaido_ElCarritoV1SeCierraIGUAL es el punto (2) para el carrito, y
// es la mitad que más pesa: el carrito es DURABLE, así que es el único flujo donde algo
// podría cortarle el turno al cliente (D-054.4). Con el worker martilleando contra el
// proveedor muerto tiene que cerrarse exactamente igual: la despedida normal, sin aviso
// de fallo, con su solicitud proyectada y cerrada.
//
// 🔬 MUTACIÓN EJECUTADA (M20): registrar en `newDurableCartRuntime` un EventSink que
// llame al proveedor del pipeline DENTRO del turno y devuelva su error marcado con
// `runtime.ErrMaterializationFailed` — que es EXACTAMENTE la condición que
// `llm_caida_best_effort_test.go` dejó escrita para T1.1: «el sink del pipeline LLM NO
// debe marcar su error con ErrMaterializationFailed». COMPILA. RESULTADO: rojo — el
// turno se corta, el cliente recibe «No pudimos registrar tu pedido» en vez de la
// despedida y la solicitud no queda cerrada.
func TestINV10_ProveedorCaido_ElCarritoV1SeCierraIGUAL(t *testing.T) {
	p := nuevoPipelineCaído(t)
	parar := p.arrancar(t)
	defer parar()
	p.esperarLlamadaEnVuelo(t)

	almacén := store.NewMemoryRepository()
	rt, repo, sender, cid := newDurableCartRuntime(t, almacén)
	if err := rt.HandleIncoming(context.Background(), testSession,
		incoming(testContact, "confirmar", "wamid.inv10-carrito")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if got := sender.count(); got != 1 {
		t.Fatalf("el cliente debe recibir EXACTAMENTE un saliente (la despedida), recibió %d: %v",
			got, sender.texts())
	}
	salida := sender.texts()[0]
	if !strings.Contains(salida, despedidaCartText) {
		t.Fatalf("el carrito debe cerrar con la despedida normal pese al pipeline caído, salió: %q", salida)
	}
	if strings.Contains(salida, "No pudimos registrar tu pedido") {
		t.Fatalf("el pipeline caído NO debe producir el aviso de fallo del turno durable: %q", salida)
	}
	st := loadState(t, repo, cid)
	if st.CurrentNode != model.NodeTerminal || st.Outcome() != model.OutcomeCompleted {
		t.Fatalf("el flujo debe terminar completed pese al pipeline caído: node=%q outcome=%q",
			st.CurrentNode, st.Outcome())
	}
	items := almacén.Intakes()
	if len(items) != 1 || items[0].Status != "closed" {
		t.Fatalf("la solicitud del carrito debe quedar cerrada igualmente: %+v", items)
	}
	p.exigeQueElProveedorSeCayóDeVerdad(t)
}

// ---------------------------------------------------------------------------
// (4) NI GOROUTINES FILTRADAS NI LATENCIA EN EL TURNO
// ---------------------------------------------------------------------------

// techoDelTurno es lo que puede tardar un turno del cliente contra almacenes en memoria.
// Es un techo GENEROSÍSIMO —el turno real tarda microsegundos— y por eso sirve: no está
// puesto para medir el turno, está puesto para detectar que el turno se puso a ESPERAR
// al proveedor. La demora del proveedor es `demoraDelProveedor` (1 s), así que cualquier
// serialización del turno detrás de él da >= 1 s, cuatro veces este techo.
const techoDelTurno = 250 * time.Millisecond

// TestINV10_ProveedorCaido_NiFiltraGoroutinesNiBloqueaElTurno es el punto (4).
//
// Son dos afirmaciones y ninguna implica a la otra: un worker puede no filtrar
// goroutines y aun así serializar el turno detrás suyo (bastaría con que el turno lo
// esperase), y puede no bloquear el turno y filtrar una goroutine por vuelta.
//
// 🔴 EL TECHO DE LATENCIA NO ES UNA MEDICIÓN DE RENDIMIENTO. Se comprueba con una
// llamada al proveedor EN VUELO —garantizado, no esperado— y el techo es 250 ms frente
// a una demora del proveedor de 1 s: si el fallo del proveedor apareciera en la latencia
// de `HandleIncoming`, el turno tardaría al menos esa demora.
//
// 🔬 MUTACIÓN EJECUTADA (M21, mitad de la latencia): registrar en el runtime un
// EventSink que llame a `p.prov.ExtractMainIdeas(ctx, ...)` de forma síncrona —o sea,
// meter el proveedor DENTRO del turno—. COMPILA. RESULTADO: rojo, el turno tarda >= 1 s.
// 🔬 MUTACIÓN EJECUTADA (M19, mitad de las goroutines): en `pipeline.(*Worker).Run`,
// `bloqueada := make(chan struct{}); go func() { <-bloqueada }()`. COMPILA. RESULTADO:
// rojo, «quedaron 1 goroutine(s) de más».
func TestINV10_ProveedorCaido_NiFiltraGoroutinesNiBloqueaElTurno(t *testing.T) {
	antes := estabilizarGoroutines(runtime.NumGoroutine())

	p := nuevoPipelineCaído(t)
	parar := p.arrancar(t)
	defer parar()
	p.esperarLlamadaEnVuelo(t)

	// El turno del cliente, MIENTRAS el proveedor está colgado.
	almacén := store.NewMemoryRepository()
	rt, _, _, _ := newDurableCartRuntime(t, almacén)
	inicio := time.Now()
	if err := rt.HandleIncoming(context.Background(), testSession,
		incoming(testContact, "confirmar", "wamid.inv10-latencia")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	tardó := time.Since(inicio)

	if !p.prov.hayLlamadaEnVuelo() && p.prov.cuenta() == 0 {
		t.Fatal("no había ninguna llamada al proveedor durante el turno: el test no midió nada")
	}
	if tardó >= techoDelTurno {
		t.Fatalf("el turno tardó %s (techo %s): el fallo del proveedor está apareciendo en la latencia de HandleIncoming",
			tardó, techoDelTurno)
	}

	p.esperarIntentos(t, 2)
	parar()
	p.exigeQueElProveedorSeCayóDeVerdad(t)

	if despues := estabilizarGoroutines(antes); despues > antes {
		t.Fatalf("quedaron %d goroutine(s) de más tras parar el worker (antes %d, después %d)",
			despues-antes, antes, despues)
	}
}

// estabilizarGoroutines espera a que el conteo baje hasta `objetivo` o a que se agote el
// plazo, y devuelve el último valor. La espera no es cosmética: una goroutine recién
// cancelada tarda un instante en desaparecer del conteo, y sin ella el test sería
// intermitente — que es peor que no tenerlo.
func estabilizarGoroutines(objetivo int) int {
	n := runtime.NumGoroutine()
	limite := time.Now().Add(5 * time.Second)
	for n > objetivo && time.Now().Before(limite) {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}
