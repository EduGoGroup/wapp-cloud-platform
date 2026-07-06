package runtime_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// newRoleTriggerRuntime es como newTriggerRuntime pero con el ROL de sesión
// inyectado en el fakeResolver (Plan 020 · T1): permite probar la guarda passive.
func newRoleTriggerRuntime(t *testing.T, role string, rules ...trigger.Rule) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver) {
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
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant, role: role}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)))
	return rt, repo, sender, contacts
}

// keywordRule arma una regla keyword→testFlow (match exacto) para las pruebas de rol.
func keywordRule() trigger.Rule {
	return trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindKeyword,
		Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: testFlow, Enabled: true,
	}
}

// TestHandleIncoming_SesionPassive_NoDisparaTrigger: una sesión passive NO entra al
// motor reactivo — un entrante que casa una palabra clave NI arranca el flujo NI
// auto-responde NI crea estado (Plan 020 · T1).
func TestHandleIncoming_SesionPassive_NoDisparaTrigger(t *testing.T) {
	rt, repo, sender, contacts := newRoleTriggerRuntime(t, "passive", keywordRule())
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "pedido", "wamid.pv")); err != nil {
		t.Fatalf("HandleIncoming passive: %v", err)
	}
	if sender.count() != 0 {
		t.Fatalf("una sesión passive no debería auto-responder, envió %d", sender.count())
	}
	if _, ok, lerr := repo.Load(ctx, store.Key{TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, contacts, testContact)}); lerr != nil || ok {
		t.Fatalf("una sesión passive no debería crear estado (ok=%v err=%v)", ok, lerr)
	}
}

// TestHandleIncoming_SesionBot_DisparaTrigger: no-regresión del keyword-trigger del
// 019 — con rol bot EXPLÍCITO, el MISMO entrante arranca el flujo y envía el menú.
func TestHandleIncoming_SesionBot_DisparaTrigger(t *testing.T) {
	rt, repo, sender, contacts := newRoleTriggerRuntime(t, "bot", keywordRule())
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "pedido", "wamid.bt")); err != nil {
		t.Fatalf("HandleIncoming bot: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("una sesión bot debería arrancar el flujo (1 envío), envió %d", sender.count())
	}
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.CurrentNode != "root" {
		t.Fatalf("estado tras keyword en sesión bot inesperado: %+v", st)
	}
}

// TestHandleIncoming_PassiveNoAvanzaConversacionViva: una conversación EN CURSO en
// una sesión que resuelve passive deja de avanzar (no auto-responde); su estado NO
// se borra (criterio conservador "passive no auto-responde"). Se materializa
// arrancando la conversación con un resolver bot y avanzándola con uno passive.
func TestHandleIncoming_PassiveNoAvanzaConversacionViva(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)

	// Arranca la conversación por API (el rol no interviene en Start): estado vivo en "root".
	rtBot := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant, role: "bot"}, contacts, discardLogger())
	if _, err := rtBot.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sentAfterStart := sender.count()

	// La MISMA sesión ahora resuelve passive: un entrante NO avanza ni auto-responde.
	rtPassive := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant, role: "passive"}, contacts, discardLogger())
	if err := rtPassive.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.pv2")); err != nil {
		t.Fatalf("HandleIncoming passive sobre conversación viva: %v", err)
	}
	if sender.count() != sentAfterStart {
		t.Fatalf("passive no debería auto-responder sobre conversación viva: antes=%d ahora=%d", sentAfterStart, sender.count())
	}
	// El estado sigue vivo y SIN avanzar (no se borró): sigue en "root".
	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.CurrentNode != "root" {
		t.Fatalf("passive no debería avanzar el estado: %+v", st)
	}
}
