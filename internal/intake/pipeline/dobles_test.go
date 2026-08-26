package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// dobles_test.go — los aparejos de la suite de robustez de T2.5.
//
// Es un fichero de test INTERNO (`package pipeline`) a propósito: la política que hay
// que probar necesita mandar en el RELOJ del worker (`w.ahora`), y sin eso los tests
// del backoff tendrían que dormir 30 segundos de verdad o bajar la base hasta que
// cupiera en un sleep — con lo que estarían probando otra política.

// ---------------------------------------------------------------------------
// EL RELOJ
// ---------------------------------------------------------------------------

// reloj es un reloj que solo avanza cuando un test lo empuja. Lo comparten el worker
// y el store: si tuvieran dos, el `next_attempt_at <= now()` del claim se resolvería
// contra un instante distinto del que escribió el backoff, que es exactamente el
// defecto «comparar dos relojes» que la casa ya tiene documentado.
type reloj struct {
	mu sync.Mutex
	t  time.Time
}

func nuevoReloj() *reloj {
	return &reloj{t: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)}
}

func (r *reloj) ahora() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.t
}

func (r *reloj) avanzar(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.t = r.t.Add(d)
}

// ---------------------------------------------------------------------------
// EL LOG QUE SE PUEDE INTERROGAR
// ---------------------------------------------------------------------------

// linea es una emisión del log con sus pares clave/valor ya indexados.
type linea struct {
	nivel  string
	msg    string
	campos map[string]any
}

// captor implementa logger.Logger guardando lo emitido. Es la única forma de afirmar
// el criterio de observabilidad de T2.5 («log estructurado por etapa con job_id, stage,
// elapsed») por EJECUCIÓN y no por lectura del código.
type captor struct {
	mu     sync.Mutex
	lineas []linea
}

func (c *captor) emitir(nivel, msg string, args []any) {
	campos := make(map[string]any, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		k, ok := args[i].(string)
		if !ok {
			continue
		}
		campos[k] = args[i+1]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lineas = append(c.lineas, linea{nivel: nivel, msg: msg, campos: campos})
}

func (c *captor) Debug(msg string, args ...any) { c.emitir("debug", msg, args) }
func (c *captor) Info(msg string, args ...any)  { c.emitir("info", msg, args) }
func (c *captor) Warn(msg string, args ...any)  { c.emitir("warn", msg, args) }
func (c *captor) Error(msg string, args ...any) { c.emitir("error", msg, args) }

// With devuelve el MISMO captor y descarta los args: este doble no modela el
// arrastre de contexto porque el worker no lo usa. Si algún día lo usara, esto
// mentiría — y por eso se dice aquí en vez de dejarlo a la sorpresa.
func (c *captor) With(_ ...any) logger.Logger { return c }

// buscar devuelve las líneas cuyo mensaje contiene `fragmento`.
func (c *captor) buscar(fragmento string) []linea {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]linea, 0, 2)
	for _, l := range c.lineas {
		if strings.Contains(l.msg, fragmento) {
			out = append(out, l)
		}
	}
	return out
}

// unica exige EXACTAMENTE una línea con ese fragmento y la devuelve.
func (c *captor) unica(t *testing.T, fragmento string) linea {
	t.Helper()
	got := c.buscar(fragmento)
	if len(got) != 1 {
		t.Fatalf("se esperaba UNA línea de log con %q, hay %d: %s", fragmento, len(got), c.volcado())
	}
	return got[0]
}

// volcado son los mensajes emitidos, para que un fallo diga qué SÍ había.
func (c *captor) volcado() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, l := range c.lineas {
		fmt.Fprintf(&b, "\n  [%s] %s %v", l.nivel, l.msg, l.campos)
	}
	return b.String()
}

// exigeCampos comprueba que la línea trae esas claves con valor no vacío.
func (l linea) exigeCampos(t *testing.T, claves ...string) {
	t.Helper()
	for _, k := range claves {
		v, ok := l.campos[k]
		if !ok {
			t.Fatalf("la línea %q no trae el campo %q; trae %v", l.msg, k, l.campos)
		}
		if s, esCadena := v.(string); esCadena && s == "" {
			t.Fatalf("la línea %q trae el campo %q VACÍO", l.msg, k)
		}
	}
}

// ---------------------------------------------------------------------------
// LAS ETAPAS FALSAS
// ---------------------------------------------------------------------------

// guionEtapa es lo que una etapa falsa hace en su i-ésima llamada. Un guión más corto
// que el número de llamadas repite su última entrada: así un test que quiere «falla
// siempre» escribe una entrada y no diez.
type guionEtapa struct {
	err error
	// dura es lo que el reloj avanza durante la llamada. Es lo que hace que
	// `elapsed_ms` sea distinto de cero SIN dormir de verdad.
	dura time.Duration
}

// etapaBase es lo común a las tres falsas: el guión, el contador y el reloj.
type etapaBase struct {
	mu       sync.Mutex
	guion    []guionEtapa
	llamadas int
	rel      *reloj
	// alLlamar corre ANTES de decidir el resultado. Es el hueco por el que un test
	// mete «otro worker terminó el job mientras esta etapa corría».
	alLlamar func()
}

func (e *etapaBase) paso() error {
	e.mu.Lock()
	i := e.llamadas
	e.llamadas++
	g := guionEtapa{}
	if len(e.guion) > 0 {
		g = e.guion[min(i, len(e.guion)-1)]
	}
	hook := e.alLlamar
	e.mu.Unlock()

	if hook != nil {
		hook()
	}
	if g.dura > 0 && e.rel != nil {
		e.rel.avanzar(g.dura)
	}
	return g.err
}

func (e *etapaBase) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.llamadas
}

// p2Falsa implementa EtapaIdeas. Persiste de verdad su artefacto cuando sale bien,
// para que la reanudación que se prueba después sea la reanudación real y no una
// escenografía.
type p2Falsa struct {
	etapaBase
	store stages.StageStore
	wants []llm.Want
	hint  *llm.Hint
}

func (e *p2Falsa) Run(ctx context.Context, job intake.ClaimedJob, _ string) (*llm.MainIdeas, error) {
	if err := e.paso(); err != nil {
		return nil, err
	}
	art := &llm.MainIdeas{Version: llm.ArtifactVersion, Wants: e.wants, DeliveryHint: e.hint}
	return art, guardar(ctx, e.store, job.ID, intake.StageP2, art)
}

// p3Falsa implementa EtapaEspecificaciones.
type p3Falsa struct {
	etapaBase
	store stages.StageStore
	items []llm.ItemSpec
}

func (e *p3Falsa) Run(ctx context.Context, job intake.ClaimedJob, _ string, _ []llm.Want) (*stages.ArtefactoP3, error) {
	if err := e.paso(); err != nil {
		return nil, err
	}
	art := &stages.ArtefactoP3{Version: llm.ArtifactVersion, Items: e.items}
	return art, guardar(ctx, e.store, job.ID, intake.StageP3, art)
}

// p4Falsa implementa EtapaNormalizacion.
type p4Falsa struct {
	etapaBase
	store stages.StageStore
	// fecha es la fecha de entrega ABSOLUTA que P4 calcula. Está aquí desde T3.8
	// porque es el único dato que viaja de P4 al BORRADOR saltándose el match, y un
	// cableado que lo perdiera no daría error: el borrador saldría sin fecha, que es
	// un estado legítimo cuando la expresión no se reconoce.
	fecha string
}

func (e *p4Falsa) Run(ctx context.Context, job intake.ClaimedJob, _ string,
	items []llm.ItemSpec, _ *llm.Hint) (*llm.Quantities, error) {
	if err := e.paso(); err != nil {
		return nil, err
	}
	norm := make([]llm.NormalizedItem, 0, len(items))
	for range items {
		norm = append(norm, llm.NormalizedItem{Qty: 1})
	}
	art := &llm.Quantities{Version: llm.ArtifactVersion, Items: norm, DeliveryDate: e.fecha}
	return art, guardar(ctx, e.store, job.ID, intake.StageP4, art)
}

// matchFalso implementa EtapaMatch. Además de dejar su artefacto, GUARDA la entrada
// que recibió: es la única forma de afirmar que el worker le pasó el índice del
// catálogo y las zonas de envío —las dos lecturas por job de T3.8— en vez de
// llamarla con la mano vacía, que compilaría igual.
type matchFalso struct {
	etapaBase
	store   stages.StageStore
	entrada stages.EntradaMatch
	recibio bool
}

func (e *matchFalso) Run(ctx context.Context, job intake.ClaimedJob, in stages.EntradaMatch) (*stages.ArtefactoMatch, error) {
	e.mu.Lock()
	e.entrada, e.recibio = in, true
	e.mu.Unlock()
	if err := e.paso(); err != nil {
		return nil, err
	}
	art := &stages.ArtefactoMatch{Version: llm.ArtifactVersion, Lines: lineasDe(in)}
	return art, guardar(ctx, e.store, job.ID, intake.StageMatch, art)
}

// vioLaEntrada devuelve la entrada de la última llamada.
func (e *matchFalso) vioLaEntrada() (stages.EntradaMatch, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.entrada, e.recibio
}

// lineasDe arma una línea por ítem más la de envío, que es el ORDEN que el match real
// declara contrato. No copia su lógica —eso lo prueban los tests de `stages`—: lo que
// aquí importa es que el número de líneas dependa de lo que P4 dejó, para que un
// worker que se saltara P4 no pudiera pasar desapercibido.
func lineasDe(in stages.EntradaMatch) []stages.Linea {
	n := 0
	if in.Cantidades != nil {
		n = len(in.Cantidades.Items)
	}
	out := make([]stages.Linea, 0, n+1)
	for i := range n {
		out = append(out, stages.Linea{Kind: stages.KindUnmatched, Label: fmt.Sprintf("item-%d", i), Qty: 1})
	}
	return append(out, stages.Linea{Kind: stages.KindShipping, Label: "Envío por confirmar", Qty: 1})
}

// draftFalso implementa EtapaDraft. Devuelve SIEMPRE el mismo `intake_id` para que el
// test pueda afirmar que ese valor exacto llegó a `intake_jobs.intake_id`: un id
// sorteado dentro del doble haría que la aserción solo pudiera comprobar «no vacío»,
// que es lo que un `COALESCE` mal puesto también satisface.
type draftFalso struct {
	etapaBase
	store    stages.StageStore
	intakeID string
	entrada  stages.EntradaDraft
	recibio  bool
}

func (e *draftFalso) Run(ctx context.Context, job intake.ClaimedJob, in stages.EntradaDraft) (*stages.ArtefactoDraft, error) {
	e.mu.Lock()
	e.entrada, e.recibio = in, true
	e.mu.Unlock()
	if err := e.paso(); err != nil {
		return nil, err
	}
	art := &stages.ArtefactoDraft{
		Version: intakes.RevisionPayloadVersion, IntakeID: e.intakeID, RevisionNo: 1,
	}
	if in.Match != nil {
		art.Lines = len(in.Match.Lines)
	}
	return art, guardar(ctx, e.store, job.ID, intake.StageDraft, art)
}

// vioLaEntrada devuelve la entrada de la última llamada.
func (e *draftFalso) vioLaEntrada() (stages.EntradaDraft, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.entrada, e.recibio
}

// catalogoDePrueba es el doble EXPORTADO del puerto (ver memoria.go), no uno propio:
// el mismo objeto que usa el test de INV-10 en `flujos/runtime`, que no puede
// construirse el suyo sin cruzar la frontera del índice.
func catalogoDePrueba(t *testing.T) *CatalogoEnMemoria {
	t.Helper()
	c, err := NuevoCatalogoEnMemoria("Torta de chocolate")
	if err != nil {
		t.Fatalf("construir el catálogo de prueba: %v", err)
	}
	return c
}

// itemsDePrueba son `n` ítems especificados, como los que P3 deja para P4. El texto es
// el del catálogo del doble para que el match real —el de un test— pueda casarlos.
func itemsDePrueba(n int) []llm.ItemSpec {
	out := make([]llm.ItemSpec, 0, n)
	for i := range n {
		out = append(out, llm.ItemSpec{
			Product:  "torta de chocolate",
			Evidence: "quiero la torta " + strconv.Itoa(i+1),
		})
	}
	return out
}

// wantsDePrueba son `n` ideas vivas, las que P2 deja para P3. Hace falta al menos una:
// con cero, el worker emite el aviso de «borrador VACÍO» y el escenario deja de ser el
// camino normal.
func wantsDePrueba(n int) []llm.Want {
	out := make([]llm.Want, 0, n)
	for i := range n {
		out = append(out, llm.Want{
			Idea:     "torta de chocolate",
			Evidence: "quiero la torta " + strconv.Itoa(i+1),
		})
	}
	return out
}

// olaTres arma las tres piezas que T3.8 volvió OBLIGATORIAS en NewWorker. Existe para
// los tests que se construyen su propio worker fuera de `nuevoBanco` (los del aforo,
// los del flanco a READY, el del tope de ítems): a ninguno le importa el borrador,
// pero todos tienen que pasar por él, porque la cadena de producción lo recorre.
func olaTres(t *testing.T, rel *reloj, store stages.StageStore) (*matchFalso, *draftFalso, *CatalogoEnMemoria) {
	t.Helper()
	return &matchFalso{etapaBase: etapaBase{rel: rel}, store: store},
		&draftFalso{etapaBase: etapaBase{rel: rel}, store: store, intakeID: intakeIDDePrueba},
		catalogoDePrueba(t)
}

// guardar es lo que hace que las etapas falsas dejen rastro REAL en la máquina: sin
// esto, «la reanudación salta las etapas ya persistidas» se probaría contra artefactos
// que nadie escribió, y el test no estaría mirando el camino que corre en producción.
func guardar(ctx context.Context, store stages.StageStore, jobID, etapa string, art any) error {
	payload, err := json.Marshal(art)
	if err != nil {
		return err
	}
	ok, err := store.SaveStage(ctx, jobID, intake.Artifact{Stage: etapa, Payload: payload})
	if err != nil {
		return err
	}
	if !ok {
		return stages.ErrJobFueraDeProcessing
	}
	return nil
}

// ---------------------------------------------------------------------------
// EL BANCO DE PRUEBAS
// ---------------------------------------------------------------------------

// banco es el worker cableado contra los dobles, con todo lo que un test necesita
// interrogar después.
type banco struct {
	w     *Worker
	store *StoreEnMemoria
	log   *captor
	rel   *reloj
	p2    *p2Falsa
	p3    *p3Falsa
	p4    *p4Falsa
	// match, draft, catalogos y zonas son la Ola 3 (T3.8). Están en TODOS los bancos
	// —y no solo en los tests que los miran— porque la cadena de producción los
	// recorre siempre: un banco que parara en P4 probaría un worker que ya no existe.
	match     *matchFalso
	draft     *draftFalso
	catalogos *CatalogoEnMemoria
	zonas     *intakes.MemoryStore
}

// intakeIDDePrueba es el id que devuelve el draft falso. Es un UUID fijo para que la
// aserción pueda comparar contra ESTE valor y no solo contra «no vacío».
const intakeIDDePrueba = "9f3c1d52-4b8a-4a6e-9c11-7d2e6f0a5b34"

// nuevoBanco arma el worker. `cfg` se completa con los defaults de producción, así que
// un test que no diga nada está probando la política REAL y no una de laboratorio.
func nuevoBanco(t *testing.T, cfg Config) *banco {
	t.Helper()
	rel := nuevoReloj()
	store := NuevoStoreEnMemoria(rel.ahora)
	log := &captor{}
	b := &banco{store: store, log: log, rel: rel}
	b.p2 = &p2Falsa{etapaBase: etapaBase{rel: rel}, store: store,
		wants: []llm.Want{{Idea: "torta", Evidence: "torta"}}}
	b.p3 = &p3Falsa{etapaBase: etapaBase{rel: rel}, store: store,
		items: []llm.ItemSpec{{Product: "torta", Evidence: "torta"}}}
	b.p4 = &p4Falsa{etapaBase: etapaBase{rel: rel}, store: store}
	b.match = &matchFalso{etapaBase: etapaBase{rel: rel}, store: store}
	b.draft = &draftFalso{etapaBase: etapaBase{rel: rel}, store: store, intakeID: intakeIDDePrueba}
	b.catalogos = catalogoDePrueba(t)
	// El lector de zonas es el `MemoryStore` REAL del dominio de solicitudes, no un
	// doble propio: es el gemelo declarado de `*intakes.Postgres` y ya sabe sembrar
	// zonas (SetShippingZones). Un tercer doble aquí sería una tercera idea de lo que
	// significa «este tenant no configuró zonas».
	b.zonas = intakes.NewMemoryStore()

	w, err := NewWorker(log, store, b.p2, b.p3, b.p4, b.match, b.draft, b.catalogos,
		cifraFalsa{}, cfg, ConZonasDeEnvio(b.zonas))
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}
	w.ahora = rel.ahora
	b.w = w
	return b
}

// sembrarSano mete un job `pending` con sobre completo, listo para correr.
func (b *banco) sembrarSano(id string) string {
	return b.store.Sembrar(Fila{
		ID: id,
		Key: intake.WindowKey{TenantID: "tenant-1", SessionID: "sess-1",
			ContactID: "contacto-1", EventID: "11111111-1111-1111-1111-111111111111"},
		SourceText: intake.SourceText{Enc: []byte("cifrado"), DEK: []byte("dek"), KEKID: "kek-1"},
		MessageTS:  b.rel.ahora(),
		CreatedAt:  b.rel.ahora(),
	})
}

// ver devuelve la fila o falla.
func (b *banco) ver(t *testing.T, id string) Fila {
	t.Helper()
	f, ok := b.store.Ver(id)
	if !ok {
		t.Fatalf("la fila %s no existe", id)
	}
	return f
}

// cifraFalsa devuelve un literal fijo. No modela el envelope: lo que se prueba aquí es
// la política del worker, y el cifrado real tiene sus propios tests.
type cifraFalsa struct{}

func (cifraFalsa) Decrypt(_, _ []byte, _ string) (string, error) {
	return "quiero una torta de chocolate para el viernes", nil
}

// cifraRota falla siempre: es la KEK que no desenvuelve (KMS caído, clave retirada).
type cifraRota struct{ err error }

func (c cifraRota) Decrypt(_, _ []byte, _ string) (string, error) { return "", c.err }

// ---------------------------------------------------------------------------
// LA CAÍDA DE PROVEEDOR **DE VERDAD**
// ---------------------------------------------------------------------------

// errorDeRedReal levanta un servidor de pruebas SOLO para quedarse con su dirección,
// lo cierra en el acto y devuelve el error de una petición contra ese puerto muerto.
//
// 🔴 ES UN ERROR DE RED REAL Y NO UN `errors.New`, y el patrón es el de
// `sinkLLMCaído` (runtime/llm_caida_best_effort_test.go). Importa porque lo que la
// política clasifica es una FAMILIA de error: un error fabricado a mano probaría que
// `causaDe` sabe leer un centinela, no que un fallo de infraestructura de verdad —con
// su `*url.Error` envolviendo un `*net.OpError` envolviendo un `syscall.Errno`— cae
// del lado de `infra` y no del de `calidad`.
func errorDeRedReal(t *testing.T) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close()

	cli := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url+"/api/generate", http.NoBody)
	if err != nil {
		t.Fatalf("armar la petición al proveedor: %v", err)
	}
	resp, err := cli.Do(req) //nolint:bodyclose // el Do falla: no hay Body que cerrar.
	if err == nil {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Fatalf("cerrar la respuesta: %v", cerr)
		}
		t.Fatal("el puerto cerrado RESPONDIÓ: la caída no se simuló y ningún test que use esto prueba nada")
	}
	return fmt.Errorf("p2: pedir las ideas principales: %w", err)
}
