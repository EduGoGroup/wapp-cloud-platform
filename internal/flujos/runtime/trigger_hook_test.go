package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// errTriggerResolver es un trigger.Resolver que siempre falla en Resolve: sirve
// para verificar que un error del resolver NO aborta la recepción (REQ-A7).
type errTriggerResolver struct{}

func (errTriggerResolver) Resolve(context.Context, string, string) (trigger.Decision, error) {
	return trigger.Decision{}, errors.New("boom")
}

func (errTriggerResolver) IsEscape(context.Context, string, string) (bool, error) {
	return false, nil
}

// newTriggerRuntime arma un runtime con el flujo de menú publicado (v1) y un
// ConfigResolver respaldado por un MemoryStore sembrado con las reglas dadas
// (100% de BD, cero hardcode). Devuelve también el repo y el sender para asserts.
func newTriggerRuntime(t *testing.T, rules ...trigger.Rule) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)))
	return rt, repo, sender, contacts
}

// TestHandleIncoming_Keyword_Arranca: sin conversación viva, un entrante que casa
// una palabra clave arranca su flujo (startLocked reusando el mutex ya tomado).
func TestHandleIncoming_Keyword_Arranca(t *testing.T) {
	rule := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindKeyword,
		Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: testFlow, Enabled: true,
	}
	rt, repo, sender, contacts := newTriggerRuntime(t, rule)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "pedido", "wamid.kw")); err != nil {
		t.Fatalf("HandleIncoming keyword: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("keyword debería arrancar el flujo y enviar 1 (el menú), envió %d", sender.count())
	}
	if got := sender.texts()[0]; !strings.Contains(got, "Ventas") {
		t.Fatalf("texto del menú inesperado: %q", got)
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.CurrentNode != "root" || st.FlowVersion != 1 {
		t.Fatalf("estado tras keyword inesperado: %+v", st)
	}
}

// TestHandleIncoming_Fallback_Arranca: sin conversación viva y sin keyword que
// case, un fallback habilitado arranca su flujo.
func TestHandleIncoming_Fallback_Arranca(t *testing.T) {
	rule := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testFlow, Enabled: true,
	}
	rt, repo, sender, contacts := newTriggerRuntime(t, rule)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "cualquier cosa", "wamid.fb")); err != nil {
		t.Fatalf("HandleIncoming fallback: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("fallback debería arrancar el flujo, envió %d", sender.count())
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.CurrentNode != "root" {
		t.Fatalf("estado tras fallback inesperado: %+v", st)
	}
}

// TestHandleIncoming_Ignore_NoArranca: sin reglas que casen (ConfigResolver con
// store vacío) el entrante se ignora, idéntico a la decisión C.
func TestHandleIncoming_Ignore_NoArranca(t *testing.T) {
	rt, repo, sender, contacts := newTriggerRuntime(t) // sin reglas
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola", "wamid.ig")); err != nil {
		t.Fatalf("HandleIncoming ignore: %v", err)
	}
	if sender.count() != 0 {
		t.Fatalf("sin reglas no debería enviar nada, envió %d", sender.count())
	}
	if _, ok, lerr := repo.Load(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, contacts, testContact)}); lerr != nil || ok {
		t.Fatalf("sin reglas no debería crear estado (ok=%v err=%v)", ok, lerr)
	}
}

// TestHandleIncoming_ResolverError_SeIgnora: un fallo del resolver NO aborta la
// recepción (return nil) ni arranca nada (REQ-A7).
func TestHandleIncoming_ResolverError_SeIgnora(t *testing.T) {
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(errTriggerResolver{}))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "pedido", "wamid.err")); err != nil {
		t.Fatalf("error del resolver NO debe propagarse: %v", err)
	}
	if sender.count() != 0 {
		t.Fatalf("error del resolver no debería arrancar nada, envió %d", sender.count())
	}
}
