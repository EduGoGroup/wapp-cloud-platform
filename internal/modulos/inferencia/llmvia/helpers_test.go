package llmvia_test

// Los dobles que comparten los tests externos del paquete: el transporte falso (y el que
// además sabe enrutar), un store de filas fijas para lo que el doble en memoria no deja
// sembrar, un error del cable con motivo, el contador del observador y las entradas mínimas.
// notify_test.go es paquete interno y lleva los suyos.

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation/degradationhelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm/tenantllmhelpertest"
)

const (
	// testTenant es el tenant de casi todos los casos.
	testTenant = "tenant-1"
	// validOutput es una salida de P1 que llm.ExtractJSON acepta tal cual.
	validOutput = `{"version":1,"intent":"intake_request","confidence":0.9,"evidence":"pizzas"}`
	// storeReadPrefix es el prefijo literal del error cuando la fila no se puede leer.
	storeReadPrefix = "llmvia: leyendo la configuración LLM del tenant: "
)

// frameCall es una petición tal como la vio el transporte.
type frameCall struct {
	tenant string
	req    edgegrpc.InferRequest
	// budget es lo que le quedaba al ctx al llegar; hasBudget false ⇒ sin deadline.
	budget    time.Duration
	hasBudget bool
}

// fakeFrame es el doble del transporte de la vía local: guarda cada petición que recibe y
// contesta lo sembrado. Lleva candado porque un test lo comparte entre goroutines.
type fakeFrame struct {
	mu   sync.Mutex
	seen []frameCall
	out  string
	err  error
}

func (f *fakeFrame) Infer(ctx context.Context, tenantID string, req edgegrpc.InferRequest) (string, error) {
	call := frameCall{tenant: tenantID, req: req}
	if deadline, ok := ctx.Deadline(); ok {
		call.hasBudget, call.budget = true, time.Until(deadline)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, call)
	return f.out, f.err
}

// calls devuelve una copia de las peticiones que llegaron al cable.
func (f *fakeFrame) calls() []frameCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]frameCall(nil), f.seen...)
}

// only devuelve la ÚNICA petición que llegó, y falla el test si no hubo exactamente una.
func (f *fakeFrame) only(t *testing.T) frameCall {
	t.Helper()
	calls := f.calls()
	if len(calls) != 1 {
		t.Fatalf("peticiones al transporte = %d, quería 1", len(calls))
	}
	return calls[0]
}

// requireUntouched afirma que no se tocó el cable.
func (f *fakeFrame) requireUntouched(t *testing.T) {
	t.Helper()
	if calls := f.calls(); len(calls) != 0 {
		t.Errorf("se tocó el cable %d veces, quería 0", len(calls))
	}
}

// routerFrame es el transporte que SÍ sabe decir qué Edge atiende, como *edgegrpc.Server.
// Apunta lo que le preguntan: es la única forma de demostrar que por vía API NO se le pregunta.
type routerFrame struct {
	fakeFrame
	edge  string
	found bool
	asked [][2]string
}

func (f *routerFrame) PlazaDe(tenantID, originSessionID string) (string, bool) {
	f.asked = append(f.asked, [2]string{tenantID, originSessionID})
	return f.edge, f.found
}

// stubStore es un llmvia.Store de respuestas fijas, para lo que el doble en memoria de
// tenantllm no deja sembrar: una vía fuera del vocabulario, un Get que falla o una
// credencial que no se puede descifrar. Cuenta las veces que se pidió la CREDENCIAL.
type stubStore struct {
	row      tenantllm.Config
	found    bool
	key      string
	getErr   error
	keyErr   error
	keyCalls int
}

func (s *stubStore) Get(context.Context, string) (tenantllm.Config, bool, error) {
	return s.row, s.found, s.getErr
}

func (s *stubStore) APIKey(context.Context, string) (string, error) {
	s.keyCalls++
	if s.keyErr != nil {
		return "", s.keyErr
	}
	return s.key, nil
}

// rowStore devuelve el doble en memoria de tenantllm con la fila de testTenant ya sembrada.
// Para la fila api la clave es FALSA (T-16): ningún test de este paquete sale a la red.
func rowStore(t *testing.T, cfg tenantllm.Config) *tenantllmhelpertest.Memoria {
	t.Helper()
	store := tenantllmhelpertest.NewMemoria()
	cfg.TenantID = testTenant
	key, consent := "", time.Time{}
	if cfg.Provider != "" {
		key, consent = "clave-falsa-de-test", time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	}
	if err := store.Upsert(context.Background(), cfg, key, consent); err != nil {
		t.Fatalf("sembrando la fila de tenant_llm: %v", err)
	}
	return store
}

// apiRow es una fila de la vía api completa. Sirve para las entradas que NO construyen el
// provider (Warm, PlazaDe, Turno); For la usa solo con algo que haga fallar a api.New.
func apiRow() tenantllm.Config {
	return tenantllm.Config{Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: "un-modelo"}
}

// transportError es un error del cable CON motivo, con la forma por la que se consume
// *edgegrpc.InferError: el método Motivo(). Receptor puntero: «el mismo error» es identidad.
type transportError struct{ reason string }

func (e *transportError) Error() string  { return "el cable falló: " + e.reason }
func (e *transportError) Motivo() string { return e.reason }

// fallCounter recoge lo que sale por el observador: (origen, vía, motivo) por caída.
type fallCounter struct {
	mu    sync.Mutex
	falls [][3]string
}

func (c *fallCounter) count(origin, route, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.falls = append(c.falls, [3]string{origin, route, reason})
}

func (c *fallCounter) all() [][3]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][3]string(nil), c.falls...)
}

// noticeBook es el almacén de avisos en memoria y la opción que cablea sobre él el escritor
// REAL de degradation: lo que se afirma es lo que quedaría en owner_degradation_notices.
type noticeBook struct {
	rows *degradationhelpertest.Memoria
}

func newNoticeBook() *noticeBook {
	return &noticeBook{rows: degradationhelpertest.NewMemoria()}
}

func (b *noticeBook) option() llmvia.SelectorOption {
	return llmvia.WithNotifier(degradation.NewNotifier(b.rows, degradation.VentanaPorDefecto))
}

// written devuelve los avisos de testTenant como pares (motivo, vía). El motivo va con
// string(): Reason.String es contrato de otro paquete.
func (b *noticeBook) written() [][2]string {
	rows := b.rows.Rows(testTenant)
	out := make([][2]string, 0, len(rows))
	for _, n := range rows {
		out = append(out, [2]string{string(n.Reason), n.Via})
	}
	return out
}

// requireSilence afirma que no se escribió ni se intentó escribir ningún aviso y que el
// observador no contó nada.
func requireSilence(t *testing.T, book *noticeBook, falls *fallCounter) {
	t.Helper()
	if n := book.rows.Saves(); n != 0 {
		t.Errorf("el almacén de avisos se tocó %d veces (%v), quería 0", n, book.written())
	}
	if got := falls.all(); len(got) != 0 {
		t.Errorf("caídas contadas = %v, quería ninguna", got)
	}
}

// newSelector construye el selector con un logger mudo y exige que no falle.
func newSelector(t *testing.T, store llmvia.Store, opts ...llmvia.SelectorOption) *llmvia.Selector {
	t.Helper()
	s, err := llmvia.NewSelector(store, logger.New(logger.WithWriter(io.Discard)), opts...)
	if err != nil {
		t.Fatalf("NewSelector: %v", err)
	}
	if s == nil {
		t.Fatal("NewSelector devolvió un Selector nil sin error")
	}
	return s
}

// classifyInput es un catálogo mínimo pero REAL para la P1.
func classifyInput() llm.ClassifyRequestInput {
	return llm.ClassifyRequestInput{
		Text:         "quiero tres pizzas",
		Catalog:      []llm.IntentSpec{{Name: "intake_request", Description: "pide productos"}},
		UnknownLabel: "desconocido",
	}
}

// classify llama a la P1 del provider y devuelve solo el error.
func classify(ctx context.Context, p llm.LLMProvider) error {
	_, err := p.ClassifyRequest(ctx, classifyInput(), llm.Options{})
	return err
}
