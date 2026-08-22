package runtime_test

import (
	"context"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/ingest"
)

// recordatorioSpy cuenta los toques del recordatorio de la seña y con qué contacto
// se pidieron (Plan 041 · T4.4). No manda nada: aquí se prueba QUE el motor toca,
// no qué se envía —eso vive en internal/intakes—.
type recordatorioSpy struct {
	mu     sync.Mutex
	toques []string // "tenant|contacto"
	// manda son los textos que el doble finge haber enviado en CADA toque (Plan 044
	// · T1.6): nil —el caso por defecto de estos tests— significa «no procedía», que
	// es lo que devuelve el recordatorio real el 99,9 % de las veces.
	manda []string
}

func (s *recordatorioSpy) RemindContact(_ context.Context, tenantID, contactID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toques = append(s.toques, tenantID+"|"+contactID)
	return s.manda
}

func (s *recordatorioSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.toques)
}

// newRuntimeConRecordatorio arma un runtime de encuesta con el recordatorio de la
// seña cableado y el rol de sesión dado ("" ⇒ bot).
func newRuntimeConRecordatorio(t *testing.T, role string, spy runtime.DepositReminder, opts ...runtime.Option) (*runtime.Runtime, *contact.MemoryResolver) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, surveyFlow()); err != nil {
		t.Fatalf("sembrar definición survey: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	all := append([]runtime.Option{runtime.WithDepositReminder(spy)}, opts...)
	rt := runtime.New(repo, newSurveyEngine(), &fakeSender{},
		fakeResolver{tenantID: testTenant, profile: role}, contacts, discardLogger(), all...)
	return rt, contacts
}

// TestRecordatorioSeña_UnEntranteSinConversaciónTocaIgual: el caso que importa. El
// cliente escribe «hola» SIN carrito abierto —el motor lo ignora (decisión C)— y aun
// así se evalúa el recordatorio: la conversación viva y la solicitud son cosas
// distintas, y la seña que debe es de un pedido de la semana pasada.
func TestRecordatorioSeña_UnEntranteSinConversaciónTocaIgual(t *testing.T) {
	spy := &recordatorioSpy{}
	rt, contacts := newRuntimeConRecordatorio(t, "", spy)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola", "wamid.1")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	if spy.count() != 1 {
		t.Fatalf("toques = %d, quiero exactamente 1", spy.count())
	}
	cid := resolveID(t, contacts, testContact)
	if got, want := spy.toques[0], testTenant+"|"+cid; got != want {
		t.Fatalf("toque = %q, quiero %q (tenant y contacto OPACO del entrante)", got, want)
	}
}

// TestRecordatorioSeña_LaSesiónPasivaNoToca: una sesión passive escucha y transporta
// pero NO auto-responde nada (Plan 020 · T1). El recordatorio se cuelga después de
// esa guarda justamente para heredarla: si tocara antes, el número personal del dueño
// empezaría a mandar recordatorios.
func TestRecordatorioSeña_LaSesiónPasivaNoToca(t *testing.T) {
	spy := &recordatorioSpy{}
	rt, _ := newRuntimeConRecordatorio(t, "passive", spy)

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "hola", "wamid.2")); err != nil {
		t.Fatalf("HandleIncoming passive: %v", err)
	}

	if spy.count() != 0 {
		t.Fatalf("toques = %d en una sesión passive, quiero 0", spy.count())
	}
}

// TestRecordatorioSeña_ElDuplicadoNoVuelveATocar: el outbox del Edge reenvía frames
// (at-least-once). El dedupe corta ANTES de tocar el motor, y el toque va después:
// un mismo mensaje no puede contar como dos visitas del cliente.
func TestRecordatorioSeña_ElDuplicadoNoVuelveATocar(t *testing.T) {
	spy := &recordatorioSpy{}
	rt, _ := newRuntimeConRecordatorio(t, "", spy,
		runtime.WithIngestDeduper(ingest.NewMemoryDeduper()))
	ctx := context.Background()

	for range 3 {
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola", "wamid.3")); err != nil {
			t.Fatalf("HandleIncoming: %v", err)
		}
	}

	if spy.count() != 1 {
		t.Fatalf("toques = %d con el MISMO wa_message_id tres veces, quiero 1", spy.count())
	}
}

// TestRecordatorioSeña_SinCablearElMotorNoCambia: sin la opción, el camino del
// entrante es exactamente el de antes de T4.4 (no-regresión). El test existe para
// que quitar el cableado no pase por "no hay nada que probar".
func TestRecordatorioSeña_SinCablearElMotorNoCambia(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, surveyFlow()); err != nil {
		t.Fatalf("sembrar definición survey: %v", err)
	}
	rt := runtime.New(repo, newSurveyEngine(), &fakeSender{},
		fakeResolver{tenantID: testTenant}, contact.NewMemoryResolver(repo), discardLogger())

	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "hola", "wamid.4")); err != nil {
		t.Fatalf("HandleIncoming sin recordatorio cableado: %v", err)
	}
}
