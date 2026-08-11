package runtime_test

// cart_cancel_outcome_test.go — el TEST GEMELO de la cancelación dentro del flujo
// (hallazgo #29, segunda vuelta · decisión de Jhoan 2026-08-11).
//
// Por qué gemelo y no dos tests sueltos: hasta esta ola, «cancelar termina el flujo»
// (cart_fin_de_flujo_test.go, módulo puro) y «qué queda escrito en el plano de
// eventos» vivían en pruebas distintas, y NINGUNA ataba las dos mitades. Ese es
// exactamente el defecto que este repo ya pagó antes (criterios gemelos): dos
// criterios que se cumplen por separado pueden contradecirse en pareja y nadie se
// entera. Aquí las tres afirmaciones se comprueban EN LA MISMA prueba y sobre la
// MISMA conversación: el evento queda `cancelled`, la solicitud queda `cancelled`
// —ni `abandoned` ni `closed`—, y el pedido siguiente nace con evento e intake
// PROPIOS.
//
// Este archivo corre SIN base de datos (plano de eventos y solicitudes en memoria),
// para que el gate rápido lo ejerza en cada `go test ./internal/flujos/...`. Su
// gemelo contra PostgreSQL real —con la proyección y los CHECK de verdad— es
// TestE2E_H29_CancelarDejaElEventoCancelled (internal/publicapi).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// cartEventStartRule es la regla event_start «carrito» → el flujo del carrito. Es la
// diferencia con cartKeywordRule (cart_resume_test.go): aquí SÍ hay plano de eventos
// cableado, así que el disparo tiene que PARIR un evento `cart` — que es el sujeto de
// todo lo que este archivo mide.
func cartEventStartRule() trigger.Rule {
	return trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "carrito",
		MatchType: trigger.MatchExact, EventKind: "cart", FlowID: testCartFlow, Enabled: true,
	}
}

// newCartEventRuntime es newCartRuntime (cart_resume_test.go) MÁS el plano de eventos
// y el abandonador de solicitudes. No se extiende aquel para no mover el cableado de
// los tests que lo usan hoy —que afirman cosas SIN plano de eventos a propósito—.
func newCartEventRuntime(t *testing.T) (*runtime.Runtime, *store.MemoryRepository, *memEventStore, *fakeAbandoner) {
	t.Helper()
	evs := newMemEventStore(time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	rt, repo, ab := newCartEventRuntimeConStore(t, evs)
	return rt, repo, evs, ab
}

// newCartEventRuntimeConStore es el de arriba con el EventStore que se le pase, para
// poder envolverlo y fabricar fallos (mismo patrón que newLifecycleRuntimeConStore).
func newCartEventRuntimeConStore(t *testing.T, wired runtime.EventStore) (*runtime.Runtime, *store.MemoryRepository, *fakeAbandoner) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	repo.SetTenantContent(testTenant, "catalogo", []byte(cartCatalogBlob))
	if _, err := repo.InsertDefinition(ctx, testTenant, cartFlow(testCartFlow)); err != nil {
		t.Fatalf("sembrar definición cart: %v", err)
	}
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(ctx, cartEventStartRule()); err != nil {
		t.Fatalf("insert regla event_start: %v", err)
	}
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(survey.New())
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(
		content.NewRouter(content.NewStatic(), content.NewJSON(repo))))
	ab := &fakeAbandoner{}
	rt := runtime.New(repo, eng, &fakeSender{}, fakeResolver{tenantID: testTenant},
		contact.NewMemoryResolver(repo), discardLogger(),
		runtime.WithEventSink(persistSinkWith(repo)), cartResumeOpt(repo),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(wired),
		runtime.WithIntakeAbandoner(ab))
	return rt, repo, ab
}

// TestCancelarDentroDelFlujo_DejaElEventoCancelledYLaSolicitudCancelled es el gemelo
// completo. Las tres mitades, en orden y sobre la misma conversación:
//
//  1. El EVENTO queda `cancelled`, no `closed`. Es la decisión del #29 (segunda
//     vuelta): `closed` es el «fin NATURAL del flujo» de D-043.5 y un pedido
//     cancelado no lo es; además la enmienda E-11 del ADR-0029 ya produce `cancelled`
//     para un cliente que cierra su evento con un gesto MENOS explícito.
//  2. La SOLICITUD queda `cancelled` —la dejó ahí la proyección de cart_cancelled—, y
//     nadie la ABANDONA por el camino: closeIfFinished tiene prohibido tocarla y el
//     abandonador no se llama ni una vez. `abandoned` es otra cosa (la cancelación
//     desde la app del dueño) y confundirlas mentiría en la bandeja.
//  3. El pedido SIGUIENTE nace con evento e intake propios. Es la mitad que convierte
//     esto en un criterio de producto y no en una comprobación de columnas: si el
//     evento no muriera, el «carrito» siguiente reusaría el vivo y su intake chocaría
//     en silencio contra intakes_event_id_uidx (el síntoma del #24).
func TestCancelarDentroDelFlujo_DejaElEventoCancelledYLaSolicitudCancelled(t *testing.T) {
	rt, repo, evs, ab := newCartEventRuntime(t)
	ctx := context.Background()

	// ── Pedido 1: «carrito» pare el evento y arranca el flujo; 1→1→2→2 deja el
	// carrito en L5 con su solicitud `open`; «9» cancela.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "h29-a0")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	vivos := evs.alive()
	if len(vivos) != 1 {
		t.Fatalf("el primer «carrito» debe parir UN evento vivo, hay %d: %+v", len(vivos), vivos)
	}
	ev1 := vivos[0]
	cartAddCafe(t, rt, "h29-a")
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "9", "h29-a-cancel")); err != nil {
		t.Fatalf("cancelar: %v", err)
	}

	// ── Mitad 1: el evento queda `cancelled`.
	if got := evs.statuses()[ev1.ID]; got != events.StatusCancelled {
		t.Fatalf("cancelar el pedido debe dejar su evento en %q; quedó %q (si dice %q, alguien revirtió la traducción del desenlace en closeIfFinished)",
			events.StatusCancelled, got, events.StatusClosed)
	}

	// ── Mitad 2: la solicitud queda `cancelled`, y NADIE la abandonó.
	if os := repo.Intakes(); len(os) != 1 || os[0].Status != "cancelled" {
		t.Fatalf("la solicitud debe quedar cancelled (la deja así la proyección de cart_cancelled), got %+v", os)
	}
	if got := ab.seen(); len(got) != 0 {
		t.Fatalf("el cierre por fin de flujo NO puede abandonar la solicitud (eso es de la cancelación desde la app): %v", got)
	}

	// ── El EFECTO sigue al ESTADO: event_cancelled, y CERO event_closed. Sin esto,
	// flow_events diría «se cerró» junto a una fila que dice `cancelled` — una
	// contradicción escrita en una bitácora append-only— y el recuento de
	// cancelaciones dejaría fuera justo la más frecuente.
	if fe := t54RequireOne(t, repo, runtime.EffectEventCancelled); fe.Payload["kind"] != "cart" {
		t.Fatalf("%s debe declarar el tipo del evento: %+v", runtime.EffectEventCancelled, fe.Payload)
	}
	t54RequireNone(t, repo, runtime.EffectEventClosed)

	// ── Mitad 3: el pedido siguiente nace con evento e intake PROPIOS.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "h29-b0")); err != nil {
		t.Fatalf("segundo carrito: %v", err)
	}
	vivos = evs.alive()
	if len(vivos) != 1 || vivos[0].ID == ev1.ID {
		t.Fatalf("el segundo «carrito» debe parir un evento PROPIO (el primero ya está cancelled), vivos=%+v", vivos)
	}
	cartAddCafe(t, rt, "h29-b")
	if open, cancelled := openIntakeCount(repo, "open"), openIntakeCount(repo, "cancelled"); open != 1 || cancelled != 1 {
		t.Fatalf("esperaba 1 solicitud open (pedido nuevo) + 1 cancelled (el de antes), got %+v", repo.Intakes())
	}
}

// TestCancelarDentroDelFlujo_ElReintentoDeE8SigueDiciendoCancelled fija la razón por
// la que el desenlace vive en Conversation.Vars (model.VarOutcome) y no en un campo
// suelto del struct: E-8 §4. Cuando TransitionEvent falla DE VERDAD, closeIfFinished
// conserva el puntero a propósito y el cierre lo reintenta el SIGUIENTE entrante,
// sobre un flow_state RELEÍDO DE LA BASE — un turno distinto, con el Result del módulo
// ya olvidado. Un campo del struct no sobrevive al Save (el repositorio escribe
// columnas nombradas, no el struct), así que ese reintento habría sellado `closed`
// sobre un pedido que la clienta canceló: el defecto del #29 otra vez, un turno más
// tarde y sin que nadie lo viera.
//
// Es el gemelo exacto de TestCierreNatural_FalloRealNoLimpiaElPuntero
// (event_lifecycle_test.go), que prueba lo mismo para el desenlace por defecto.
func TestCancelarDentroDelFlujo_ElReintentoDeE8SigueDiciendoCancelled(t *testing.T) {
	evsBase := newMemEventStore(time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	rota := &fallaTransicion{memEventStore: evsBase}
	rt, repo, _ := newCartEventRuntimeConStore(t, rota)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "h29-d0")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evsBase.alive()[0]
	cartAddCafe(t, rt, "h29-d")

	// La BD se cae justo en el turno que cancela: el evento sigue `open` y el puntero
	// se conserva para reintentar (E-8 §4, ya cubierto por su propio test).
	rota.err = errors.New("la BD se fue a almorzar")
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "9", "h29-d-cancel")); err != nil {
		t.Fatalf("el fallo del cierre NO aborta el turno: %v", err)
	}
	if got := evsBase.statuses()[ev.ID]; got != events.StatusOpen {
		t.Fatalf("con la transición rota el evento sigue open, quedó %q", got)
	}
	// La solicitud SÍ se canceló: su proyección no depende del plano de eventos.
	if os := repo.Intakes(); len(os) != 1 || os[0].Status != "cancelled" {
		t.Fatalf("la solicitud debe quedar cancelled aunque el evento no pudiera sellarse, got %+v", os)
	}

	// La BD vuelve: el siguiente entrante reintenta el cierre pendiente. Tiene que
	// sellar `cancelled`, no `closed` — el desenlace sobrevivió en el estado.
	rota.err = nil
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola", "h29-d-retry")); err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if got := evsBase.statuses()[ev.ID]; got != events.StatusCancelled {
		t.Fatalf("el reintento debe sellar %q (el desenlace viaja en el estado persistido); quedó %q",
			events.StatusCancelled, got)
	}
	t54RequireOne(t, repo, runtime.EffectEventCancelled)
	t54RequireNone(t, repo, runtime.EffectEventClosed)
}

// TestConfirmarDentroDelFlujo_SigueDejandoElEventoClosed es el CONTROL del gemelo de
// arriba, y no es redundante: lo que el #29 introduce es una BIFURCACIÓN (el módulo
// declara el desenlace y el runtime lo traduce), así que hace falta probar las dos
// ramas sobre el mismo cableado. Sin este control, alguien podría «arreglar» la
// traducción mandando TODO a `cancelled` y el test de arriba seguiría verde.
func TestConfirmarDentroDelFlujo_SigueDejandoElEventoClosed(t *testing.T) {
	rt, repo, evs, _ := newCartEventRuntime(t)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "h29-c0")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]
	cartAddCafe(t, rt, "h29-c")
	// «2» finaliza (L5 → resumen) y «1» confirma.
	for i, in := range []string{"2", "1"} {
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, in, "h29-c-fin"+string(rune('a'+i)))); err != nil {
			t.Fatalf("HandleIncoming %q: %v", in, err)
		}
	}

	if got := evs.statuses()[ev.ID]; got != events.StatusClosed {
		t.Fatalf("confirmar el pedido debe seguir dejando su evento en %q; quedó %q", events.StatusClosed, got)
	}
	if os := repo.Intakes(); len(os) != 1 || os[0].Status != "closed" {
		t.Fatalf("la solicitud del pedido confirmado debe quedar closed, got %+v", os)
	}
	t54RequireOne(t, repo, runtime.EffectEventClosed)
	t54RequireNone(t, repo, runtime.EffectEventCancelled)
}
