//go:build pendiente

package intakeahead_test

// doubles_test.go — EL BANCO Y LOS DOBLES de los tests del adelanto (sin fichero de
// producción gemelo): el catálogo publicado, el proveedor, el selector de vía, el sink,
// el almacén de catálogos y el banco que los monta.
//
// 🔴 TODO TEST DE ESTE PAQUETE CORRE DENTRO DE UNA BURBUJA `testing/synctest`. Es lo que
// permite las dos cosas que este paquete necesita y que con reloj real solo se consiguen
// durmiendo: (1) afirmar un NEGATIVO —«no se pidió nada»— tras `synctest.Wait()`, que
// vuelve cuando todas las goroutines de la burbuja están bloqueadas de forma duradera, y
// (2) un RELOJ FALSO: los `context.WithTimeout` del pool y los `time.Sleep` de los dobles
// corren sobre el reloj de la burbuja, que solo avanza cuando nadie puede avanzar. Ni un
// test espera tiempo real, y ninguno sondea.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intakeahead"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
)

const (
	tenantID  = "t-ahead"
	sessionID = "s-ahead"
	contactID = "c-ahead"
	eventID   = "e-ahead"
)

// clientText es el mensaje del cliente de casi todos los casos; goodEvidence es una
// frase que SÍ está en él.
const (
	clientText   = "Hola, quiero 200 sillas para el sábado"
	goodEvidence = "quiero 200 sillas"
)

// publishedCatalog es el catálogo con la forma PUBLICADA EN CAMPO: `intake_request` con
// `params: []` —la lista VACÍA, D-044.20— y sus ejemplos. `consulta` declara un param
// (`tema`) y un ejemplo anotado para poder ver que params y ejemplos viajan al prompt y
// para ejercer el allowlist de params declarados.
const publishedCatalog = `{
  "version": "v-campo",
  "umbral_confianza": 0.6,
  "vocabulario": ["sillas", "mesas"],
  "intents": [
    {
      "name": "intake_request",
      "descripcion": "El cliente pide un presupuesto o hace un pedido",
      "params": [],
      "ejemplos": [
        {"mensaje": "quiero 200 sillas para el sábado"},
        {"mensaje": "me pasas precio de 3 mesas"}
      ]
    },
    {
      "name": "consulta",
      "descripcion": "El cliente pregunta algo que no es un pedido",
      "params": ["tema"],
      "ejemplos": [{"mensaje": "a qué hora abren", "params": {"tema": "hora"}}]
    }
  ]
}`

// windowKey es la ventana de los casos.
func windowKey() intake.WindowKey {
	return intake.WindowKey{TenantID: tenantID, SessionID: sessionID, ContactID: contactID, EventID: eventID}
}

// windowOf es OTRA ventana del mismo tenant y la misma sesión: cambia el contacto.
func windowOf(contact string) intake.WindowKey {
	k := windowKey()
	k.ContactID = contact
	return k
}

// artifact arma un artefacto P1 tal como lo devolvería el modelo.
func artifact(intent string, confidence float64, evidence string, params map[string]string) string {
	b, err := json.Marshal(map[string]any{
		"version": 1, "intent": intent, "confidence": confidence,
		"evidence": evidence, "params": params,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// goodArtifact es una clasificación que se sostiene sobre clientText.
func goodArtifact() string { return artifact("intake_request", 0.91, goodEvidence, nil) }

// ---------------------------------------------------------------------------
// El log
// ---------------------------------------------------------------------------

// syncBuffer es un búfer que aguanta escrituras desde los workers mientras el test lee.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// lines devuelve las líneas del log que llevan ese nivel y ese mensaje exacto.
func (s *syncBuffer) lines(level, msg string) []string {
	var out []string
	for line := range strings.SplitSeq(s.String(), "\n") {
		if strings.Contains(line, "level="+level+" ") && strings.Contains(line, `msg="`+msg+`"`) {
			out = append(out, line)
		}
	}
	return out
}

// debugLog devuelve un logger en nivel DEBUG —los rótulos del adelanto salen casi todos
// por Debug— que escribe en buf.
func debugLog(buf *syncBuffer) logger.Logger {
	return logger.New(logger.WithWriter(buf), logger.WithLevel(slog.LevelDebug))
}

// ---------------------------------------------------------------------------
// Los dobles
// ---------------------------------------------------------------------------

// reply es lo que el proveedor contesta a UNA llamada.
type reply struct {
	raw string
	err error
}

// providerCall es lo que el proveedor anotó de UNA llamada.
type providerCall struct {
	in          llm.ClassifyRequestInput
	temperature float64
	// remaining es lo que le quedaba al ctx al entrar; bounded, si traía deadline.
	remaining time.Duration
	bounded   bool
}

// fakeProvider es un llm.LLMProvider de mentira: solo ClassifyRequest hace algo. Las
// otras cuatro etapas devuelven un error explícito, que es lo que haría ruido el día que
// el adelanto llamara a una por error.
type fakeProvider struct {
	mu sync.Mutex
	// replies se consumen en orden; la última se repite.
	replies []reply
	calls   []providerCall
	// inFlight y maxInFlight cuentan las llamadas que están DENTRO a la vez.
	inFlight, maxInFlight int
	// onCall, si no es nil, corre DENTRO de la llamada número n (desde 0), ya contada:
	// es el gancho para bloquearla o hacerle pasar tiempo. Si devuelve error, la llamada
	// devuelve ese error en vez de su reply.
	onCall func(ctx context.Context, n int) error
}

func (p *fakeProvider) ClassifyRequest(ctx context.Context, in llm.ClassifyRequestInput, opts llm.Options) (json.RawMessage, error) {
	p.mu.Lock()
	n := len(p.calls)
	call := providerCall{in: in, temperature: opts.Temperature}
	if deadline, ok := ctx.Deadline(); ok {
		call.bounded, call.remaining = true, time.Until(deadline)
	}
	p.calls = append(p.calls, call)
	p.inFlight++
	p.maxInFlight = max(p.maxInFlight, p.inFlight)
	hook := p.onCall
	p.mu.Unlock()

	var hookErr error
	if hook != nil {
		hookErr = hook(ctx, n)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.inFlight--
	if hookErr != nil {
		return nil, hookErr
	}
	r := p.replies[min(n, len(p.replies)-1)]
	return json.RawMessage(r.raw), r.err
}

func (p *fakeProvider) ExtractMainIdeas(context.Context, llm.ExtractMainIdeasInput, llm.Options) (json.RawMessage, error) {
	return nil, errNotP1
}

func (p *fakeProvider) ExtractItemSpecs(context.Context, llm.ExtractItemSpecsInput, llm.Options) (json.RawMessage, error) {
	return nil, errNotP1
}

func (p *fakeProvider) NormalizeQuantities(context.Context, llm.NormalizeQuantitiesInput, llm.Options) (json.RawMessage, error) {
	return nil, errNotP1
}

func (p *fakeProvider) GenerateQuoteText(context.Context, llm.GenerateQuoteTextInput, llm.Options) (json.RawMessage, error) {
	return nil, errNotP1
}

func (p *fakeProvider) seen() []providerCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]providerCall(nil), p.calls...)
}

func (p *fakeProvider) flying() (now, peak int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inFlight, p.maxInFlight
}

func (p *fakeProvider) setReplies(replies ...reply) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies = replies
}

func (p *fakeProvider) setHook(hook func(ctx context.Context, n int) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCall = hook
}

// selectorAsk es UNA petición de provider: para qué tenant, con qué sesión de origen y
// con cuánto plazo.
type selectorAsk struct {
	tenant, session string
	remaining       time.Duration
	bounded         bool
}

// fakeSelector es el selector de vía.
type fakeSelector struct {
	mu       sync.Mutex
	provider llm.LLMProvider
	err      error
	asks     []selectorAsk
}

func (s *fakeSelector) For(ctx context.Context, tenant, originSession string) (llm.LLMProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ask := selectorAsk{tenant: tenant, session: originSession}
	if deadline, ok := ctx.Deadline(); ok {
		ask.bounded, ask.remaining = true, time.Until(deadline)
	}
	s.asks = append(s.asks, ask)
	if s.err != nil {
		return nil, s.err
	}
	return s.provider, nil
}

func (s *fakeSelector) seen() []selectorAsk {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]selectorAsk(nil), s.asks...)
}

func (s *fakeSelector) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// delivery es UNA entrega al sink.
type delivery struct {
	key        intake.WindowKey
	intent     string
	confidence float64
}

// fakeSink recoge lo clasificado.
type fakeSink struct {
	mu   sync.Mutex
	got  []delivery
	hook func()
}

func (s *fakeSink) OnClassified(key intake.WindowKey, intent string, confidence float64) {
	s.mu.Lock()
	s.got = append(s.got, delivery{key: key, intent: intent, confidence: confidence})
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (s *fakeSink) seen() []delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]delivery(nil), s.got...)
}

// fakeConfig es el almacén de catálogos: un blob por tenant, o un error para todos.
type fakeConfig struct {
	mu    sync.Mutex
	blobs map[string]string
	err   error
	// asked son los tenants por los que se preguntó; bounded, si cada ctx traía deadline.
	asked   []string
	bounded []bool
}

func (c *fakeConfig) Get(ctx context.Context, tenant string) (intentcfg.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, hasDeadline := ctx.Deadline()
	c.asked = append(c.asked, tenant)
	c.bounded = append(c.bounded, hasDeadline)
	if c.err != nil {
		return intentcfg.Config{}, c.err
	}
	blob, ok := c.blobs[tenant]
	if !ok {
		return intentcfg.Config{}, intentcfg.ErrNotFound
	}
	return intentcfg.Config{Version: "v-campo", Blob: []byte(blob)}, nil
}

func (c *fakeConfig) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.asked...)
}

func (c *fakeConfig) seenBounded() []bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]bool(nil), c.bounded...)
}

func (c *fakeConfig) set(blobs map[string]string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blobs, c.err = blobs, err
}

// gate es una barrera que un doble cruza solo cuando el test la abre.
type gate struct {
	ch   chan struct{}
	once sync.Once
}

func (g *gate) wait() { <-g.ch }

func (g *gate) open() { g.once.Do(func() { close(g.ch) }) }

// ---------------------------------------------------------------------------
// El banco
// ---------------------------------------------------------------------------

// ahead es la superficie del Pool que usan sus llamantes: el agregador (Request), el
// arranque (Run) y el gateway (Warm). El banco guarda el pool con este tipo, y lo
// construye por newPool, por un motivo de andamiaje y no de diseño: con una llamada
// directa, mientras un cuerpo sea `panic(pendiente…)` el analizador estático da por
// muerto todo lo que sigue a la llamada y marca como sin usar lo que hay antes (SA4006).
type ahead interface {
	Request(key intake.WindowKey, text string)
	Run(ctx context.Context)
	Warm(tenantID, edgeID, sessionID, kind string)
}

var _ ahead = (*intakeahead.Pool)(nil)

// newPool es intakeahead.New, llamado a través de una variable: ver ahead.
var newPool = intakeahead.New

// bench son el pool y sus dobles. Se crea DENTRO de la burbuja: los canales del pool
// tienen que nacer en ella para que `synctest.Wait` los vea.
type bench struct {
	t    *testing.T
	log  *syncBuffer
	cfg  *fakeConfig
	sel  *fakeSelector
	prov *fakeProvider
	sink *fakeSink
	pool ahead

	gates  []*gate
	cancel context.CancelFunc
	done   chan struct{}
}

// newBench arma el pool con el catálogo publicado para tenantID y un proveedor que
// contesta goodArtifact. No arranca Run.
func newBench(t *testing.T, opts ...intakeahead.Option) *bench {
	t.Helper()
	b := &bench{
		t:    t,
		log:  &syncBuffer{},
		cfg:  &fakeConfig{blobs: map[string]string{tenantID: publishedCatalog}},
		prov: &fakeProvider{replies: []reply{{raw: goodArtifact()}}},
		sink: &fakeSink{},
	}
	b.sel = &fakeSelector{provider: b.prov}
	b.pool = newPool(debugLog(b.log), b.cfg, b.sel, b.sink, opts...)
	// Al salir: se abren las barreras y se para Run, en ese orden. La burbuja exige que
	// no quede ninguna goroutine viva.
	t.Cleanup(func() {
		for _, g := range b.gates {
			g.open()
		}
		b.stop()
	})
	return b
}

// start pone a correr Run con un ctx que stop cancela.
func (b *bench) start() {
	b.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel, b.done = cancel, make(chan struct{})
	go func() {
		defer close(b.done)
		b.pool.Run(ctx)
	}()
}

// stop cancela el ctx de Run y espera a que vuelva. Sin start no hace nada.
func (b *bench) stop() {
	if b.cancel == nil {
		return
	}
	b.cancel()
	<-b.done
}

// running dice si Run sigue sin volver, con todas las goroutines ya asentadas.
func (b *bench) running() bool {
	synctest.Wait()
	select {
	case <-b.done:
		return false
	default:
		return true
	}
}

// gate devuelve una barrera cerrada que el banco abre al salir.
func (b *bench) gate() *gate {
	g := &gate{ch: make(chan struct{})}
	b.gates = append(b.gates, g)
	return g
}

// blockProvider hace que toda llamada al proveedor espere en la barrera devuelta.
func (b *bench) blockProvider() *gate {
	g := b.gate()
	b.prov.setHook(func(context.Context, int) error {
		g.wait()
		return nil
	})
	return g
}

// settle espera a que todo lo que podía pasar haya pasado: vuelve cuando cada goroutine
// de la burbuja está bloqueada de forma duradera.
func settle() { synctest.Wait() }

// errNotP1 es lo que devuelve el proveedor para una etapa que no es P1.
var errNotP1 = errors.New("fake: el adelanto solo llama a ClassifyRequest")
