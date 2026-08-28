package quotetext_test

// dobles_test.go — los dobles compartidos por los tests de este paquete.
//
// El fake de LLM implementa el puerto ENTERO y devuelve errNoLlamar en los cuatro
// métodos que no son P5: si algún día este paquete llamara a otra etapa, el test que
// lo pillara fallaría con un mensaje que dice exactamente qué pasó. Es el molde de
// `internal/intake/stages/p2_test.go`, y se copia a propósito en vez de compartirse:
// un fake común entre paquetes acaba creciendo campos que solo le sirven a uno.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// errNoLlamar es lo que devuelve todo método del puerto que este paquete NO debe usar.
var errNoLlamar = errors.New("este método del puerto no se debe llamar desde quotetext")

// errProveedorMuerto simula la caída del proveedor.
var errProveedorMuerto = errors.New("el proveedor no responde")

// provFake es el doble de llm.LLMProvider.
//
// Guarda las entradas que recibió, y ESE es el mirador que de verdad importa: sin él,
// «se le pasó el few-shot» sería una promesa. `veces` cuenta las llamadas, que es lo
// que hace que el assert negativo del caso «sin historial» no sea vacuo.
type provFake struct {
	mu        sync.Mutex
	respuesta json.RawMessage
	err       error
	entradas  []llm.GenerateQuoteTextInput
}

func (p *provFake) GenerateQuoteText(_ context.Context, in llm.GenerateQuoteTextInput, _ llm.Options) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entradas = append(p.entradas, in)
	return p.respuesta, p.err
}

func (p *provFake) veces() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entradas)
}

func (p *provFake) ultima(t *testing.T) llm.GenerateQuoteTextInput {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.entradas) == 0 {
		t.Fatalf("se esperaba al menos una llamada a GenerateQuoteText y no hubo ninguna")
	}
	return p.entradas[len(p.entradas)-1]
}

func (p *provFake) ClassifyRequest(context.Context, llm.ClassifyRequestInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamar
}

func (p *provFake) ExtractMainIdeas(context.Context, llm.ExtractMainIdeasInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamar
}

func (p *provFake) ExtractItemSpecs(context.Context, llm.ExtractItemSpecsInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamar
}

func (p *provFake) NormalizeQuantities(context.Context, llm.NormalizeQuantitiesInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamar
}

// selFake es el selector de vía. Anota CON QUÉ se le pidió el provider —el tenant y la
// sesión de origen— y cuántas veces: ese contador es la prueba de que el camino «sin
// historial» no toca el LLM ni para elegirlo.
type selFake struct {
	mu       sync.Mutex
	prov     llm.LLMProvider
	err      error
	tenants  []string
	sesiones []string
}

func (s *selFake) For(_ context.Context, tenantID, originSessionID string) (llm.LLMProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants = append(s.tenants, tenantID)
	s.sesiones = append(s.sesiones, originSessionID)
	if s.err != nil {
		return nil, s.err
	}
	return s.prov, nil
}

func (s *selFake) veces() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tenants)
}

// semillaFake es el lector de tenant_content.
type semillaFake struct {
	blob []byte
	err  error
	refs []string
}

func (s *semillaFake) GetTenantContent(_ context.Context, _, ref string) ([]byte, error) {
	s.refs = append(s.refs, ref)
	if s.err != nil {
		return nil, s.err
	}
	return s.blob, nil
}

// historialRoto es el lector de historial que siempre falla. Existe para el caso «la
// BD se cayó»: el generador tiene que seguir dando el determinista.
type historialRoto struct{ err error }

func (h historialRoto) ApprovedRenderedTexts(context.Context, string, int) ([]string, error) {
	return nil, h.err
}

// artefactoP5 arma la salida cruda del modelo con el texto dado: la misma forma que
// devuelve el proveedor real ({"version":N,"text":"…"}).
func artefactoP5(t *testing.T, texto string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(struct {
		Version int    `json:"version"`
		Text    string `json:"text"`
	}{Version: llm.ArtifactVersion, Text: texto})
	if err != nil {
		t.Fatalf("armando el artefacto P5: %v", err)
	}
	return raw
}

// aprobadaEnOtraSolicitud siembra en el tenant una solicitud APARTE con su revisión
// `approved` y el texto dado. Es como se construye el historial del que sale el
// few-shot, y va en otra solicitud a propósito: la voz de la dueña se aprende de lo
// que escribió en OTROS pedidos, no en éste.
//
// `cuando` se pasa EXPLÍCITO y no se deja al reloj del store porque el orden del
// few-shot —más reciente primero— es contrato, y dos escrituras seguidas con
// `time.Now()` lo dejarían a merced de la resolución del reloj de la máquina.
func aprobadaEnOtraSolicitud(t *testing.T, store *intakes.MemoryStore, tenantID, intakeID, texto string, cuando time.Time) {
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
		RenderedText: texto,
		CreatedBy:    intakes.RevisionByOwner,
		CreatedAt:    cuando,
	}); err != nil {
		t.Fatalf("sembrando la revisión aprobada: %v", err)
	}
}
