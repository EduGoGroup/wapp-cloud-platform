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

func (errTriggerResolver) Resolve(context.Context, string, string, string) (trigger.Decision, error) {
	return trigger.Decision{}, errors.New("boom")
}

func (errTriggerResolver) IsEscape(context.Context, string, string, string) (bool, string, error) {
	return false, "", nil
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

// TestHandleIncoming_Escape_CierraYAvisa: con una conversación viva, un texto de
// escape borra el estado (libera la clave) y envía el aviso de cierre.
func TestHandleIncoming_Escape_CierraYAvisa(t *testing.T) {
	rule := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEscape,
		Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true,
	}
	rt, repo, sender, contacts := newTriggerRuntime(t, rule)
	ctx := context.Background()

	// Conversación viva (menú enviado, estado en "root").
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("Start debería enviar el menú, envió %d", sender.count())
	}

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "wamid.esc")); err != nil {
		t.Fatalf("HandleIncoming escape: %v", err)
	}
	// Se envió el aviso de cierre (segundo texto).
	if sender.count() != 2 {
		t.Fatalf("escape debería enviar 1 aviso extra, total %d", sender.count())
	}
	if got := sender.texts()[1]; !strings.Contains(strings.ToLower(got), "cerramos") {
		t.Fatalf("aviso de escape inesperado: %q", got)
	}
	// La clave quedó libre: la conversación ya no existe.
	if _, ok, lerr := repo.Load(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, contacts, testContact)}); lerr != nil || ok {
		t.Fatalf("escape debería borrar el estado (liberar la clave) (ok=%v err=%v)", ok, lerr)
	}
}

// TestHandleIncoming_Escape_UsaMessageDelTenant: una regla de escape con message
// configurado envía ESE aviso (Plan 019 · T4b) en vez del default del runtime.
func TestHandleIncoming_Escape_UsaMessageDelTenant(t *testing.T) {
	rule := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEscape,
		Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true,
		Message: "Gracias por tu tiempo, vuelve pronto.",
	}
	rt, _, sender, _ := newTriggerRuntime(t, rule)
	ctx := context.Background()

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "wamid.escmsg")); err != nil {
		t.Fatalf("HandleIncoming escape: %v", err)
	}
	if sender.count() != 2 {
		t.Fatalf("escape debería enviar 1 aviso extra, total %d", sender.count())
	}
	if got := sender.texts()[1]; got != "Gracias por tu tiempo, vuelve pronto." {
		t.Fatalf("escape debe usar el message del tenant, got %q", got)
	}
}

// TestHandleIncoming_TextoNormal_ConEscapeConfigurado_SigueFlujo: con escape
// configurado pero un texto que NO casa, el camino normal queda idéntico
// (no-regresión): la conversación avanza por engine.Step.
func TestHandleIncoming_TextoNormal_ConEscapeConfigurado_SigueFlujo(t *testing.T) {
	rule := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEscape,
		Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true,
	}
	rt, repo, sender, contacts := newTriggerRuntime(t, rule)
	ctx := context.Background()

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Opción válida "1" (ventas): no es escape → avanza.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.opt")); err != nil {
		t.Fatalf("HandleIncoming opción válida: %v", err)
	}
	if sender.count() != 2 {
		t.Fatalf("el flujo debería avanzar (menú + respuesta), total %d", sender.count())
	}
	if got := sender.texts()[1]; !strings.Contains(got, "Ventas") {
		t.Fatalf("respuesta del flujo inesperada: %q", got)
	}
	// El estado sigue vivo (avanzó, no se borró): loadState falla si no existe.
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if !st.Finished() {
		t.Fatalf("tras elegir una hoja message el flujo debería terminar: %+v", st)
	}
}
