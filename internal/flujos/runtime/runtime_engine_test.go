package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

const (
	testTenant     = "tenant-1"
	testFlow       = "menu-soporte"
	testSurveyFlow = "encuesta-nps"
	testSession    = "sess-1"
	testContact    = "573001112233"
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

// surveyFlow arma una encuesta de DOS preguntas de opción cerrada encadenadas
// que terminan en un nodo message (hoja → NodeTerminal). Cada pregunta registra
// como answer_code la CLAVE de la opción tecleada (§10.D).
func surveyFlow() model.Flow {
	return model.Flow{
		FlowID:  testSurveyFlow,
		Initial: "q1",
		Nodes: map[string]model.Node{
			"q1": {
				Type:       model.NodeTypeSurveyQuestion,
				Prompt:     "¿Nos recomendarías?\n1) Sí\n2) No",
				QuestionID: "q1",
				Options:    map[string]string{"1": "q2", "2": "q2"},
			},
			"q2": {
				Type:       model.NodeTypeSurveyQuestion,
				Prompt:     "¿Qué tal la atención?\n1) Buena\n2) Mala",
				QuestionID: "q2",
				Options:    map[string]string{"1": "gracias", "2": "gracias"},
			},
			"gracias": {Type: model.NodeTypeMessage, Text: "¡Gracias por responder!"},
		},
	}
}

// newSurveyEngine registra menú Y encuesta (el engine delega por tipo de nodo).
func newSurveyEngine() *engine.Engine {
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(survey.New())
	return engine.New(reg)
}

// newSurveyRuntime es el gemelo de newRuntime pero con la definición de encuesta
// ya publicada (versión 1) y un engine que maneja survey_question.
func newSurveyRuntime(t *testing.T) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, surveyFlow()); err != nil {
		t.Fatalf("sembrar definición survey: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, newSurveyEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger())
	return rt, repo, sender, contacts
}

func discardLogger() logger.Logger {
	return logger.New(logger.WithWriter(io.Discard))
}

// newRuntime arma un runtime con repo en memoria, sender/resolver falsos, un
// contact.Resolver en memoria (respaldado por el mismo repo, que migra el
// flow_state en la fusión) y una definición ya publicada (versión 1).
func newRuntime(t *testing.T) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger())
	return rt, repo, sender, contacts
}

// phoneRef construye una contact.Ref phone_e164 (normalizada) o falla el test.
func phoneRef(t *testing.T, value string) contact.Ref {
	t.Helper()
	ref, err := contact.NewRef(contact.KindPhoneE164, value)
	if err != nil {
		t.Fatalf("NewRef(phone, %q): %v", value, err)
	}
	return ref
}

// resolveID devuelve el contact_id opaco que el resolver asigna a un número
// (idempotente): sirve para clavar/leer el estado en los asserts.
func resolveID(t *testing.T, contacts *contact.MemoryResolver, phone string) string {
	t.Helper()
	id, err := contacts.Resolve(context.Background(), testTenant, []contact.Ref{phoneRef(t, phone)}, "")
	if err != nil {
		t.Fatalf("Resolve(%s): %v", phone, err)
	}
	return id
}

func incoming(from, text, waID string) *cloudlinkv1.IncomingMessage {
	return &cloudlinkv1.IncomingMessage{From: from, Text: text, WaMessageId: waID}
}

func loadState(t *testing.T, repo *store.MemoryRepository, contactID string) model.Conversation {
	t.Helper()
	st, ok, err := repo.Load(context.Background(), store.Key{TenantID: testTenant, SessionID: testSession, ContactID: contactID})
	if err != nil || !ok {
		t.Fatalf("Load(%s): ok=%v err=%v", contactID, ok, err)
	}
	return st
}

// --- tests ---

func TestStart_EnviaMenuYCreaEstado(t *testing.T) {
	rt, repo, sender, contacts := newRuntime(t)
	ctx := context.Background()

	ack, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact))
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
	// El destino enviable de un contacto phone_e164 es el número crudo (§10.E).
	if to := sender.sent[0].to; to != testContact {
		t.Fatalf("destino del menú = %q, quiero %q", to, testContact)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.CurrentNode != "root" || st.FlowVersion != 1 {
		t.Fatalf("estado inicial inesperado: %+v", st)
	}
}

func TestStart_ClaveExistente_Devuelve409(t *testing.T) {
	rt, _, _, _ := newRuntime(t)
	ctx := context.Background()

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("primer Start: %v", err)
	}
	_, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact))
	if !errors.Is(err, runtime.ErrConversationExists) {
		t.Fatalf("segundo Start debería dar ErrConversationExists, dio: %v", err)
	}
}

func TestHandleIncoming_SinEstado_SeIgnora(t *testing.T) {
	rt, _, sender, _ := newRuntime(t)
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
	rt, repo, sender, contacts := newRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
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
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if !st.Finished() {
		t.Fatalf("tras elegir una hoja message el flujo debería terminar: %+v", st)
	}
	if st.LastWaMessageID != "wamid.1" {
		t.Fatalf("LastWaMessageID no persistido: %+v", st)
	}
}

func TestHandleIncoming_OpcionInvalida_Reprompt(t *testing.T) {
	rt, repo, sender, contacts := newRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
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
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.CurrentNode != "root" {
		t.Fatalf("opción inválida no debe avanzar de nodo: %+v", st)
	}
}

func TestHandleIncoming_Idempotencia(t *testing.T) {
	rt, repo, sender, contacts := newRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.same")); err != nil {
		t.Fatalf("primer entrante: %v", err)
	}
	afterFirst := sender.count()
	cid := resolveID(t, contacts, testContact)
	stAfter := loadState(t, repo, cid)

	// Re-entrega del MISMO wa_message_id: no avanza ni reenvía.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.same")); err != nil {
		t.Fatalf("re-entrega: %v", err)
	}
	if sender.count() != afterFirst {
		t.Fatalf("re-entrega no debería reenviar: antes=%d ahora=%d", afterFirst, sender.count())
	}
	stRe := loadState(t, repo, cid)
	if stRe.CurrentNode != stAfter.CurrentNode {
		t.Fatalf("re-entrega no debería cambiar el estado: %q vs %q", stRe.CurrentNode, stAfter.CurrentNode)
	}
}

// TestHandleIncoming_LIDTrasStartPorNumero_AvanzaMismoEstado prueba el corazón
// del Plan 010: se ABRE por número y el contacto RESPONDE con un entrante que
// llega en formato LID (from_lid) + su número (from_pn). El resolver casa ambas
// refs al MISMO contact_id, de modo que el avance cae sobre el estado existente
// (no crea uno nuevo).
func TestHandleIncoming_LIDTrasStartPorNumero_AvanzaMismoEstado(t *testing.T) {
	rt, repo, sender, contacts := newRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	before := sender.count()

	// Entrante en formato LID, pero con la identidad enriquecida del Edge:
	// from_lid (JID lid) + from_pn (el número). Debe casar el estado abierto.
	m := &cloudlinkv1.IncomingMessage{
		From:        "88887777@lid",
		FromLid:     "88887777",
		FromPn:      testContact,
		Text:        "1",
		WaMessageId: "wamid.lid.1",
	}
	if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
		t.Fatalf("HandleIncoming LID: %v", err)
	}
	if sender.count() != before+1 {
		t.Fatalf("el entrante LID debería avanzar el estado existente (1 envío), total=%d", sender.count())
	}
	if last := sender.texts()[sender.count()-1]; !strings.Contains(last, "Ventas") {
		t.Fatalf("respuesta de avance inesperada: %q", last)
	}
	// El estado sigue siendo el MISMO (clavado por el contact_id del número).
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if !st.Finished() || st.LastWaMessageID != "wamid.lid.1" {
		t.Fatalf("el entrante LID no avanzó el MISMO estado: %+v", st)
	}
}

func TestHandleIncoming_ResolverError_Propaga(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sender := &fakeSender{}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{err: errors.New("boom")}, contact.NewMemoryResolver(repo), discardLogger())

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
	rt, repo, sender, contacts := newRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
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
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.CurrentNode != "root" {
		t.Fatalf("estado final incoherente: %+v", st)
	}
}

// TestHandleIncoming_Encuesta_FlushDosRespuestas recorre una encuesta de 2
// preguntas end-to-end en el runtime: Start renderiza P1, un entrante avanza a
// P2 y el segundo entrante llega a la hoja (Finished). Al terminar, el runtime
// hace UN flush a survey_results con las 2 respuestas correctas (§10.G).
func TestHandleIncoming_Encuesta_FlushDosRespuestas(t *testing.T) {
	rt, repo, sender, contacts := newSurveyRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testSurveyFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Aún nada: la encuesta no ha terminado (sólo se renderizó P1).
	if got := repo.SurveyResults(); len(got) != 0 {
		t.Fatalf("no debería haber resultados antes de terminar, hubo %d", len(got))
	}

	// Responde P1 con "1" → registra q1=1 y avanza a P2.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.q1")); err != nil {
		t.Fatalf("responder P1: %v", err)
	}
	if got := repo.SurveyResults(); len(got) != 0 {
		t.Fatalf("no debería haber flush a mitad de la encuesta, hubo %d", len(got))
	}
	if last := sender.texts()[sender.count()-1]; !strings.Contains(last, "atención") {
		t.Fatalf("tras P1 debería renderizarse P2, se obtuvo: %q", last)
	}

	// Responde P2 con "2" → registra q2=2, cae en la hoja message y TERMINA.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "2", "wamid.q2")); err != nil {
		t.Fatalf("responder P2: %v", err)
	}
	cid := resolveID(t, contacts, testContact)
	if st := loadState(t, repo, cid); !st.Finished() {
		t.Fatalf("la encuesta debería haber terminado: %+v", st)
	}

	got := repo.SurveyResults()
	want := []store.SurveyResult{
		{TenantID: testTenant, ContactID: cid, FlowID: testSurveyFlow, FlowVersion: 1, QuestionID: "q1", AnswerCode: "1"},
		{TenantID: testTenant, ContactID: cid, FlowID: testSurveyFlow, FlowVersion: 1, QuestionID: "q2", AnswerCode: "2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resultados del flush inesperados:\n got=%+v\nwant=%+v", got, want)
	}
}

// TestHandleIncoming_Encuesta_FlushIdempotente comprueba que reprocesar el MISMO
// entrante final (mismo wa_message_id) NO duplica filas: la dedupe por
// last_wa_message_id corta antes del Step, así que el flush no se re-ejecuta.
func TestHandleIncoming_Encuesta_FlushIdempotente(t *testing.T) {
	rt, repo, _, _ := newSurveyRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testSurveyFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.q1")); err != nil {
		t.Fatalf("responder P1: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "2", "wamid.final")); err != nil {
		t.Fatalf("responder P2 (final): %v", err)
	}
	if got := repo.SurveyResults(); len(got) != 2 {
		t.Fatalf("el primer flush debería dejar 2 filas, hubo %d", len(got))
	}

	// Re-entrega del MISMO entrante final → idempotencia: ni avanza ni re-flusha.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "2", "wamid.final")); err != nil {
		t.Fatalf("re-entrega del final: %v", err)
	}
	if got := repo.SurveyResults(); len(got) != 2 {
		t.Fatalf("la re-entrega NO debe duplicar resultados, hubo %d", len(got))
	}
}

// TestHandleIncoming_MenuPuro_NoEscribeResultados verifica que un flujo sin
// nodos survey_question llega a Finished con `answers` vacío → el flush es un
// no-op (nada en survey_results).
func TestHandleIncoming_MenuPuro_NoEscribeResultados(t *testing.T) {
	rt, repo, _, _ := newRuntime(t)
	ctx := context.Background()
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// "1" → hoja message "Te paso con Ventas." (Next nil) → termina el flujo.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.menu")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if got := repo.SurveyResults(); len(got) != 0 {
		t.Fatalf("un menú puro no debe escribir resultados, hubo %d: %+v", len(got), got)
	}
}

// TestConcurrencia_ClavesDistintasEnParalelo arranca M conversaciones de
// contactos distintos en paralelo; todas deben crearse y enviar su menú.
func TestConcurrencia_ClavesDistintasEnParalelo(t *testing.T) {
	rt, repo, sender, contacts := newRuntime(t)
	ctx := context.Background()

	const m = 30
	var wg sync.WaitGroup
	wg.Add(m)
	for i := 0; i < m; i++ {
		go func(i int) {
			defer wg.Done()
			phone := fmt.Sprintf("5730000000%02d", i)
			if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, phone)); err != nil {
				t.Errorf("Start concurrente contacto %s: %v", phone, err)
			}
		}(i)
	}
	wg.Wait()

	if sender.count() != m {
		t.Fatalf("se esperaban %d menús enviados, hubo %d", m, sender.count())
	}
	for i := 0; i < m; i++ {
		phone := fmt.Sprintf("5730000000%02d", i)
		cid := resolveID(t, contacts, phone)
		if ok, err := repo.Exists(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}); err != nil || !ok {
			t.Fatalf("conversación de %s no creada", phone)
		}
	}
}
