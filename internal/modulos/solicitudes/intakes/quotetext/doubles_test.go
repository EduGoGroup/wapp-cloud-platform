//go:build pendiente

package quotetext_test

// doubles_test.go — los dobles y el montaje que comparten los tests de precios.go y
// quotetext.go. Lleva la etiqueta `pendiente` mientras solo lo usen tests en rojo.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
)

const (
	testTenant  = "tenant-fusion"
	testIntake  = "intake-ambar"
	testSession = "sess-fusion"
)

// Las dos marcas de tiempo del historial. Fijas para que el orden del few-shot no
// dependa del reloj de la máquina que corre el test.
var (
	today     = time.Date(2026, 8, 27, 17, 24, 0, 0, time.UTC)
	yesterday = today.Add(-24 * time.Hour)
)

// Las dos cotizaciones de muestra del few-shot. NO son una transcripción: son la
// descripción abreviada del caso Fusión rehidratada a un mensaje verosímil, con la
// estructura producto + tamaño + specs + precio + qué incluye.
const (
	sampleQuoteOld = "Pastel para 15 personas, chocolate húmedo, relleno chocolate y oreo, " +
		"decoración infantil segun las fotos que me mandaste. 2100. " +
		"Incluye impresiones no comestibles"
	sampleQuoteNew = "El otro para 25-30 personas, vainilla, ddl y merengue, " +
		"con la lluvia de colores. 2950 pesos"
)

// errMustNotCall es lo que devuelve todo método del puerto LLM que este paquete NO
// debe usar: solo redacta (P5).
var errMustNotCall = errors.New("este método del puerto no se debe llamar desde quotetext")

// errDown simula la caída de una dependencia.
var errDown = errors.New("la dependencia no responde")

// providerCall es lo que el proveedor recibió en UNA llamada.
type providerCall struct {
	input       llm.GenerateQuoteTextInput
	options     llm.Options
	hasDeadline bool
	timeLeft    time.Duration
}

// fakeProvider es el doble de llm.LLMProvider. Guarda cada llamada: sin ese mirador,
// «se le pasó el few-shot» sería una promesa.
type fakeProvider struct {
	mu       sync.Mutex
	response json.RawMessage
	err      error
	calls    []providerCall
}

func (p *fakeProvider) GenerateQuoteText(ctx context.Context, in llm.GenerateQuoteTextInput, opts llm.Options) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	call := providerCall{input: in, options: opts}
	if deadline, ok := ctx.Deadline(); ok {
		call.hasDeadline, call.timeLeft = true, time.Until(deadline)
	}
	p.calls = append(p.calls, call)
	return p.response, p.err
}

func (p *fakeProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// last devuelve la última llamada, y falla el test si no hubo ninguna.
func (p *fakeProvider) last(t *testing.T) providerCall {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		t.Fatalf("se esperaba al menos una llamada a GenerateQuoteText y no hubo ninguna")
	}
	return p.calls[len(p.calls)-1]
}

func (p *fakeProvider) ClassifyRequest(context.Context, llm.ClassifyRequestInput, llm.Options) (json.RawMessage, error) {
	return nil, errMustNotCall
}

func (p *fakeProvider) ExtractMainIdeas(context.Context, llm.ExtractMainIdeasInput, llm.Options) (json.RawMessage, error) {
	return nil, errMustNotCall
}

func (p *fakeProvider) ExtractItemSpecs(context.Context, llm.ExtractItemSpecsInput, llm.Options) (json.RawMessage, error) {
	return nil, errMustNotCall
}

func (p *fakeProvider) NormalizeQuantities(context.Context, llm.NormalizeQuantitiesInput, llm.Options) (json.RawMessage, error) {
	return nil, errMustNotCall
}

// fakeSelector es el selector de vía. Anota CON QUÉ se le pidió el provider —el
// tenant y la sesión de origen— y cuántas veces: ese contador es la prueba de que un
// camino no toca el LLM ni para elegirlo.
type fakeSelector struct {
	mu       sync.Mutex
	provider llm.LLMProvider
	err      error
	tenants  []string
	sessions []string
}

func (s *fakeSelector) For(_ context.Context, tenantID, originSessionID string) (llm.LLMProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants = append(s.tenants, tenantID)
	s.sessions = append(s.sessions, originSessionID)
	if s.err != nil {
		return nil, s.err
	}
	return s.provider, nil
}

func (s *fakeSelector) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tenants)
}

// fakeSeed es el lector de tenant_content. Anota qué se le pidió.
type fakeSeed struct {
	blob    []byte
	err     error
	tenants []string
	refs    []string
}

func (s *fakeSeed) GetTenantContent(_ context.Context, tenantID, ref string) ([]byte, error) {
	s.tenants = append(s.tenants, tenantID)
	s.refs = append(s.refs, ref)
	if s.err != nil {
		return nil, s.err
	}
	return s.blob, nil
}

// seedOf arma el lector de semilla con los textos dados, en la forma de array pelado.
func seedOf(t *testing.T, texts ...string) *fakeSeed {
	t.Helper()
	blob, err := json.Marshal(texts)
	if err != nil {
		t.Fatalf("armando el blob de la semilla: %v", err)
	}
	return &fakeSeed{blob: blob}
}

// fakeHistory es el lector de historial con respuesta fija. Existe para lo que el
// almacén en memoria no deja montar: un historial caído, textos sin sanear, y ver
// con qué `limit` se le pregunta.
type fakeHistory struct {
	texts  []string
	err    error
	limits []int
}

func (h *fakeHistory) ApprovedRenderedTexts(_ context.Context, _ string, limit int) ([]string, error) {
	h.limits = append(h.limits, limit)
	if h.err != nil {
		return nil, h.err
	}
	if limit < len(h.texts) {
		return h.texts[:limit], nil
	}
	return h.texts, nil
}

// p5Artifact arma la salida cruda del modelo con el texto dado: la misma forma que
// devuelve el proveedor real ({"version":N,"text":"…"}).
func p5Artifact(t *testing.T, text string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(struct {
		Version int    `json:"version"`
		Text    string `json:"text"`
	}{Version: llm.ArtifactVersion, Text: text})
	if err != nil {
		t.Fatalf("armando el artefacto P5: %v", err)
	}
	return raw
}

// approveElsewhere siembra en el tenant una solicitud APARTE con su revisión
// `approved` y el texto dado: la voz de la dueña se aprende de lo que escribió en
// OTROS pedidos. `when` va explícito porque el orden del few-shot —más reciente
// primero— es contrato.
func approveElsewhere(t *testing.T, store *intakes.MemoryStore, tenantID, intakeID, text string, when time.Time) {
	t.Helper()
	store.Add(tenantID, intakes.Intake{ID: intakeID, Status: intakes.StatusConfirmed, SessionID: "sess-vieja"})
	payload, err := intakes.ApprovedRevisionPayload(1000, []intakes.RevisionLine{
		{SKU: "X", Label: "algo", Qty: 1, UnitPrice: 1000},
	})
	if err != nil {
		t.Fatalf("payload de la revisión aprobada: %v", err)
	}
	if _, err := store.InsertRevision(context.Background(), intakes.Revision{
		IntakeID:     intakeID,
		Kind:         intakes.RevisionKindApproved,
		Payload:      payload,
		RenderedText: text,
		CreatedBy:    intakes.RevisionByOwner,
		CreatedAt:    when,
	}); err != nil {
		t.Fatalf("sembrando la revisión aprobada: %v", err)
	}
}

// scene es el montaje completo del generador con sus dobles.
type scene struct {
	svc      *quotetext.Service
	store    *intakes.MemoryStore
	provider *fakeProvider
	selector *fakeSelector
	logs     *bytes.Buffer
}

// fusionStore devuelve un almacén con la solicitud del caso Fusión sembrada.
func fusionStore() *intakes.MemoryStore {
	store := intakes.NewMemoryStore()
	store.Add(testTenant, intakes.Intake{
		ID: testIntake, Status: intakes.StatusPendingApproval,
		SessionID: testSession, Total: fusionTotal,
	}, fusionItems...)
	return store
}

// newSceneOn arma el servicio sobre el almacén y el historial dados. `response` es lo
// que devolverá el modelo si se le llega a llamar.
func newSceneOn(t *testing.T, store *intakes.MemoryStore, history quotetext.HistoryReader,
	response json.RawMessage, opts ...quotetext.Option) *scene {
	t.Helper()
	logs := &bytes.Buffer{}
	provider := &fakeProvider{response: response}
	selector := &fakeSelector{provider: provider}
	svc, err := quotetext.NewService(logger.New(logger.WithWriter(logs)), store, history, selector, opts...)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &scene{svc: svc, store: store, provider: provider, selector: selector, logs: logs}
}

// newScene es el montaje del caso Fusión, con el historial leído del propio almacén.
func newScene(t *testing.T, response json.RawMessage, opts ...quotetext.Option) *scene {
	t.Helper()
	store := fusionStore()
	return newSceneOn(t, store, store, response, opts...)
}

// withHistory siembra los textos como cotizaciones aprobadas de OTRAS solicitudes del
// tenant, de la PRIMERA (más reciente) a la última (más antigua).
func (s *scene) withHistory(t *testing.T, texts ...string) *scene {
	t.Helper()
	for i, text := range texts {
		approveElsewhere(t, s.store, testTenant, "intake-viejo-"+string(rune('a'+i)), text,
			today.Add(-time.Duration(i)*time.Hour))
	}
	return s
}

// withSampleHistory siembra las dos cotizaciones de muestra. La antigua se inserta
// PRIMERO: así se comprueba de paso que el few-shot llega en el orden del contrato
// (la última primero) y no en el de inserción.
func (s *scene) withSampleHistory(t *testing.T) *scene {
	t.Helper()
	approveElsewhere(t, s.store, testTenant, "intake-viejo-1", sampleQuoteOld, yesterday)
	approveElsewhere(t, s.store, testTenant, "intake-viejo-2", sampleQuoteNew, today)
	return s
}

// suggest pide la sugerencia del caso Fusión y falla el test si devuelve error.
func (s *scene) suggest(t *testing.T) quotetext.Suggestion {
	t.Helper()
	out, err := s.svc.Suggest(context.Background(), testTenant, testIntake)
	if err != nil {
		t.Fatalf("Suggest devolvió error y no debe: %v", err)
	}
	return out
}

// examples son los ejemplos que viajaron en la última llamada al modelo.
func (s *scene) examples(t *testing.T) []string {
	t.Helper()
	return s.provider.last(t).input.Examples
}

// wantDeterministic exige el desenlace de respaldo: el render del caso Fusión con el
// motivo dado.
func wantDeterministic(t *testing.T, out quotetext.Suggestion, reason string) {
	t.Helper()
	if out.Source != quotetext.SourceDeterministic || out.Reason != reason {
		t.Fatalf("origen=%q motivo=%q; se esperaba (%q, %q)",
			out.Source, out.Reason, quotetext.SourceDeterministic, reason)
	}
	if out.Text != quotetext.Render(fusionDraft()) {
		t.Errorf("el texto no es el render determinista:\n%q", out.Text)
	}
}
