package runtime_test

// escape_effect_test.go — E4/E5 (Ola 6, decisión de Jhoan 2026-08-11): el ORDEN de
// handleEscape (resumen → Delete → event_escaped → SOLO ENTONCES replyAllowed
// gobernando el aviso) y el efecto event_escaped que registra el abandono destructivo
// (el mismo trato textual que stopEvent ya se aplica a sí mismo en events.go).
//
// Antes de esta corrección, replyAllowed cortaba de PRIMERO: con el cupo anti-loop
// agotado, el escape global NO OCURRÍA en absoluto —ni Delete, ni resumen, ni
// telemetría— y el cliente que pedía la palabra de emergencia se quedaba atrapado en
// silencio total. Estos dos tests fijan que, agotado el cupo, el escape SÍ ocurre
// (solo el AVISO se corta), y que con cupo todo ocurre igual que siempre.

import (
	"context"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
)

// newEscapeRuntime arma un runtime con una regla event_start (cart) y una de escape
// («salir»), un ReplyLimiter de burst AJUSTABLE (para agotar o no la cuota
// anti-loop) y un sink que captura los efectos de ciclo de vida.
func newEscapeRuntime(t *testing.T, burst int) (*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *memEventStore, *ecSink) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range []trigger.Rule{
		eventStartRule("carrito", "cart"),
		{TenantID: testTenant, Kind: trigger.KindEscape, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true},
	} {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 11, 11, 0, 0, 0, time.UTC))
	sink := &ecSink{}
	// rate.Every(time.Hour): prácticamente sin recarga dentro del test — lo único que
	// importa es EL BURST inicial.
	lim := ratelimit.NewLimiter(rate.Every(time.Hour), burst)
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEventSink(sink),
		runtime.WithReplyLimiter(lim))
	return rt, repo, sender, contacts, evs, sink
}

// TestHandleEscape_ConCupoAgotado_BorraYEmiteSinAvisar (E-4, E-5): «carrito» consume
// el ÚNICO token del burst (nacer un evento auto-responde). Con el cupo YA agotado,
// «salir» debe seguir BORRANDO el flow_state y emitiendo event_escaped — SOLO el
// aviso al cliente se corta.
func TestHandleEscape_ConCupoAgotado_BorraYEmiteSinAvisar(t *testing.T) {
	rt, repo, sender, contacts, evs, sink := newEscapeRuntime(t, 1) // burst 1: "carrito" se lo lleva.
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)
	key := store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "e4-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs, "cart")
	if got := sender.count(); got != 1 {
		t.Fatalf("precondición: «carrito» debe consumir el ÚNICO token (1 envío), hubo %d", got)
	}

	antes := sender.count()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "e4-2")); err != nil {
		t.Fatalf("salir: %v", err)
	}

	// El escape OCURRIÓ: el flow_state se borró.
	if _, ok, err := repo.Load(ctx, key); err != nil {
		t.Fatalf("load: %v", err)
	} else if ok {
		t.Fatal("E-4: con el cupo agotado el escape debe seguir BORRANDO el flow_state")
	}
	// Y el efecto SÍ se emitió.
	conEscape, visto := findEffectContext(sink, runtime.EffectEventEscaped)
	if !visto {
		t.Fatalf("E-4/E-5: con el cupo agotado debe emitirse %q igual, efectos vistos=%v", runtime.EffectEventEscaped, effectNames(sink))
	}
	if conEscape.EventID != cart.ID {
		t.Fatalf("el event_escaped debe llevar el EventID del evento abandonado (%q), llegó %q", cart.ID, conEscape.EventID)
	}
	// Pero el AVISO NO se mandó (el cupo lo corta).
	if got := sender.count(); got != antes {
		t.Fatalf("E-4: con el cupo agotado NO debe mandarse el aviso de escape; envíos antes=%d después=%d", antes, sender.count())
	}
}

// TestHandleEscape_ConCupo_TodoOcurre (E-4, contraste): con cupo disponible, el
// escape borra, emite event_escaped Y avisa — el camino de siempre, sin recortes.
func TestHandleEscape_ConCupo_TodoOcurre(t *testing.T) {
	rt, repo, sender, contacts, evs, sink := newEscapeRuntime(t, 5) // burst holgado.
	ctx := context.Background()
	cid := resolveID(t, contacts, testContact)
	key := store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "e4b-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs, "cart")

	antes := sender.count()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "e4b-2")); err != nil {
		t.Fatalf("salir: %v", err)
	}

	if _, ok, err := repo.Load(ctx, key); err != nil {
		t.Fatalf("load: %v", err)
	} else if ok {
		t.Fatal("el escape debe borrar el flow_state")
	}
	conEscape, visto := findEffectContext(sink, runtime.EffectEventEscaped)
	if !visto {
		t.Fatalf("debe emitirse %q, efectos vistos=%v", runtime.EffectEventEscaped, effectNames(sink))
	}
	if conEscape.EventID != cart.ID {
		t.Fatalf("event_escaped debe llevar el EventID del evento abandonado (%q), llegó %q", cart.ID, conEscape.EventID)
	}
	if got := sender.count(); got != antes+1 {
		t.Fatalf("con cupo el aviso de escape SÍ debe mandarse; envíos antes=%d después=%d", antes, got)
	}
}

func effectNames(s *ecSink) []string {
	effs := s.effectsAll()
	out := make([]string, len(effs))
	for i, e := range effs {
		out[i] = e.Name
	}
	return out
}

// findEffectContext busca por NOMBRE el primer efecto emitido y devuelve el
// EffectContext con el que viajó (índice a índice con effectsAll/all). Compartido
// por los tests de E4/E5/E6 para no repetir el mismo bucle en cada fichero.
func findEffectContext(s *ecSink, name string) (runtime.EffectContext, bool) {
	for i, eff := range s.effectsAll() {
		if eff.Name == name {
			return s.all()[i], true
		}
	}
	return runtime.EffectContext{}, false
}
