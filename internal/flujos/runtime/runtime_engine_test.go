package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

const (
	testTenant  = "tenant-1"
	testFlow    = "menu-soporte"
	testSession = "sess-1"
	testContact = "573001112233"
)

// --- dobles ---

type sentText struct{ sessionID, to, text string }

type fakeSender struct {
	mu   sync.Mutex
	sent []sentText
	err  error
	n    int
}

func (f *fakeSender) SendText(_ context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.n++
	f.sent = append(f.sent, sentText{sessionID, to, text})
	return &cloudlinkv1.Ack{AckedCommandId: fmt.Sprintf("cmd-%d", f.n), Ok: true}, nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeSender) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	for i, s := range f.sent {
		out[i] = s.text
	}
	return out
}

type fakeResolver struct {
	tenantID string
	err      error
}

func (f fakeResolver) ResolveTenant(_ context.Context, _ string) (string, error) {
	return f.tenantID, f.err
}

// --- helpers ---

func sampleFlow() model.Flow {
	return model.Flow{
		FlowID:  testFlow,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {
				Type:    model.NodeTypeMenu,
				Prompt:  "Hola 👋\n1) Ventas\n2) Soporte",
				Options: map[string]string{"1": "ventas", "2": "soporte"},
			},
			"ventas":  {Type: model.NodeTypeMessage, Text: "Te paso con Ventas."},
			"soporte": {Type: model.NodeTypeMessage, Text: "Cuéntame tu problema."},
		},
	}
}

func newEngine() *engine.Engine {
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	return engine.New(reg)
}

func discardLogger() logger.Logger {
	return logger.New(logger.WithWriter(io.Discard))
}

// newRuntime arma un runtime con repo en memoria, sender/resolver falsos y una
// definición ya publicada (versión 1).
func newRuntime(t *testing.T) (*runtime.Runtime, *store.MemoryRepository, *fakeSender) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	sender := &fakeSender{}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, discardLogger())
	return rt, repo, sender
}

func incoming(from, text, waID string) *cloudlinkv1.IncomingMessage {
	return &cloudlinkv1.IncomingMessage{From: from, Text: text, WaMessageId: waID}
}

func loadState(t *testing.T, repo *store.MemoryRepository, contact string) model.Conversation {
	t.Helper()
	st, ok, err := repo.Load(context.Background(), store.Key{TenantID: testTenant, SessionID: testSession, ContactID: contact})
	if err != nil || !ok {
		t.Fatalf("Load(%s): ok=%v err=%v", contact, ok, err)
	}
	return st
}

// --- tests ---

func TestStart_EnviaMenuYCreaEstado(t *testing.T) {
	rt, repo, sender := newRuntime(t)
	ctx := context.Background()

	ack, err := rt.Start(ctx, testTenant, testFlow, testSession, testContact)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ack == nil || !ack.GetOk() {
		t.Fatalf("Start ack inesperado: %+v", ack)
	}
	if sender.count() != 1 {
		t.Fatalf("Start debería enviar 1 mensaje (el menú), envió %d", sender.count())
	}
	if got := sender.texts()[0]; !strings.Contains(got, "Ventas") {
		t.Fatalf("texto del menú inesperado: %q", got)
	}
	st := loadState(t, repo, testContact)
	if st.CurrentNode != "root" || st.FlowVersion != 1 {
		t.Fatalf("estado inicial inesperado: %+v", st)
	}
}

func TestStart_ClaveExistente_Devuelve409(t *testing.T) {
	rt, _, _ := newRuntime(t)
	ctx := context.Background()

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, testContact); err != nil {
		t.Fatalf("primer Start: %v", err)
	}
	_, err := rt.Start(ctx, testTenant, testFlow, testSession, testContact)
	if !errors.Is(err, runtime.ErrConversationExists) {
		t.Fatalf("segundo Start debería dar ErrConversationExists, dio: %v", err)
	}
}

func TestHandleIncoming_SinEstado_SeIgnora(t *testing.T) {
	rt, _, sender := newRuntime(t)
	ctx := context.Background()

	err := rt.HandleIncoming(ctx, testSession, incoming("570000", "1", "wamid.X"))
	if err != nil {
		t.Fatalf("HandleIncoming sin estado debería ser nil: %v", err)
	}
	if sender.count() != 0 {
		t.Fatalf("no debería enviar nada para clave sin estado, envió %d", sender.count())
	}
}

func TestHandleIncoming_OpcionValida_Avanza(t *testing.T) {
	rt, repo, sender := newRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, testContact); err != nil {
		t.Fatalf("Start: %v", err)
	}
	before := sender.count()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.1")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if sender.count() != before+1 {
		t.Fatalf("avance debería enviar 1 mensaje, total=%d (before=%d)", sender.count(), before)
	}
	if last := sender.texts()[sender.count()-1]; !strings.Contains(last, "Ventas") {
		t.Fatalf("respuesta de avance inesperada: %q", last)
	}
	st := loadState(t, repo, testContact)
	if !st.Finished() {
		t.Fatalf("tras elegir una hoja message el flujo debería terminar: %+v", st)
	}
	if st.LastWaMessageID != "wamid.1" {
		t.Fatalf("LastWaMessageID no persistido: %+v", st)
	}
}

func TestHandleIncoming_OpcionInvalida_Reprompt(t *testing.T) {
	rt, repo, sender := newRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, testContact); err != nil {
		t.Fatalf("Start: %v", err)
	}
	before := sender.count()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "99", "wamid.bad")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if sender.count() != before+1 {
		t.Fatalf("reprompt debería enviar 1 mensaje, total=%d", sender.count())
	}
	if last := sender.texts()[sender.count()-1]; !strings.Contains(last, "no válida") {
		t.Fatalf("se esperaba reprompt, se obtuvo: %q", last)
	}
	st := loadState(t, repo, testContact)
	if st.CurrentNode != "root" {
		t.Fatalf("opción inválida no debe avanzar de nodo: %+v", st)
	}
}

func TestHandleIncoming_Idempotencia(t *testing.T) {
	rt, repo, sender := newRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, testContact); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.same")); err != nil {
		t.Fatalf("primer entrante: %v", err)
	}
	afterFirst := sender.count()
	stAfter := loadState(t, repo, testContact)

	// Re-entrega del MISMO wa_message_id: no avanza ni reenvía.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.same")); err != nil {
		t.Fatalf("re-entrega: %v", err)
	}
	if sender.count() != afterFirst {
		t.Fatalf("re-entrega no debería reenviar: antes=%d ahora=%d", afterFirst, sender.count())
	}
	stRe := loadState(t, repo, testContact)
	if stRe.CurrentNode != stAfter.CurrentNode {
		t.Fatalf("re-entrega no debería cambiar el estado: %q vs %q", stRe.CurrentNode, stAfter.CurrentNode)
	}
}

func TestHandleIncoming_ResolverError_Propaga(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sender := &fakeSender{}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{err: errors.New("boom")}, discardLogger())

	err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "1", "wamid.1"))
	if err == nil {
		t.Fatal("error del resolver debería propagarse")
	}
	// El wrapper OnIncoming NO debe panickear con el mismo error.
	rt.OnIncoming(testSession, incoming(testContact, "1", "wamid.1"))
}

// TestConcurrencia_MismaClaveSeSerializa lanza N entrantes inválidos concurrentes
// sobre la MISMA clave (cada uno con su wa_message_id único). Bajo -race no debe
// haber data race; cada inválido emite un reprompt → N envíos exactos, prueba de
// que el single-flight serializó sin perder ni corromper estado.
func TestConcurrencia_MismaClaveSeSerializa(t *testing.T) {
	rt, repo, sender := newRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, testContact); err != nil {
		t.Fatalf("Start: %v", err)
	}
	startCount := sender.count()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			// "x" es opción inválida → reprompt; wa id único evita la idempotencia.
			if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "x", fmt.Sprintf("wamid.%d", i))); err != nil {
				t.Errorf("HandleIncoming concurrente: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if got := sender.count() - startCount; got != n {
		t.Fatalf("se esperaban %d reprompts, hubo %d (estado corrupto / no serializado)", n, got)
	}
	// El estado sigue íntegro y en el nodo de menú.
	st := loadState(t, repo, testContact)
	if st.CurrentNode != "root" {
		t.Fatalf("estado final incoherente: %+v", st)
	}
}

// TestConcurrencia_ClavesDistintasEnParalelo arranca M conversaciones de
// contactos distintos en paralelo; todas deben crearse y enviar su menú.
func TestConcurrencia_ClavesDistintasEnParalelo(t *testing.T) {
	rt, repo, sender := newRuntime(t)
	ctx := context.Background()

	const m = 30
	var wg sync.WaitGroup
	wg.Add(m)
	for i := 0; i < m; i++ {
		go func(i int) {
			defer wg.Done()
			contact := fmt.Sprintf("5730000000%02d", i)
			if _, err := rt.Start(ctx, testTenant, testFlow, testSession, contact); err != nil {
				t.Errorf("Start concurrente contacto %s: %v", contact, err)
			}
		}(i)
	}
	wg.Wait()

	if sender.count() != m {
		t.Fatalf("se esperaban %d menús enviados, hubo %d", m, sender.count())
	}
	for i := 0; i < m; i++ {
		contact := fmt.Sprintf("5730000000%02d", i)
		if ok, err := repo.Exists(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: contact}); err != nil || !ok {
			t.Fatalf("conversación de %s no creada", contact)
		}
	}
}
