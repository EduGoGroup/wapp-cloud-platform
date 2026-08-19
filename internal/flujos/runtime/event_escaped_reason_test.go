package runtime_test

// event_escaped_reason_test.go — Plan 053 · Ola 4 · T4.1 (construye REQ-053.4).
//
// `event_escaped` lo emiten TRES sitios, y hasta esta tarea los tres producían una
// fila indistinguible. El nombre del efecto no los separa Y NO DEBE separarlos: su
// eje, fijado en la Ola 6, es «¿el Delete destruyó el flow_state?», y la respuesta
// es la misma en los tres (D-053.6 rechaza expresamente multiplicar el nombre). Lo
// que faltaba era el otro eje —POR QUÉ se destruyó—, y ése es el `reason`.
//
// Qué vigilan estos tests, y por qué hacen falta los dos:
//
//   - El primero, que las tres causas llegan y son DISTINTAS entre sí. Escrito con
//     los tres escenarios REALES (los mismos montajes que ya usan
//     release_finished_owner_test.go, orphan_menu_escape_test.go y
//     escape_effect_test.go), no invocando el emisor a mano: lo que puede
//     estropearse no es el helper —una línea— sino que un camino se quede con el
//     `reason` de otro, y eso solo se ve recorriendo el camino entero.
//   - El segundo, que el payload que YA existía no cambió de forma. Es el test
//     aburrido y es el que más protege: `history_id` y `kind` los consume el
//     colector de flowlifecycle y el endpoint de telemetría, y romperlos aquí se
//     vería en producción como una métrica que se queda plana, sin ningún error.
//
// Todo en memoria: ni Postgres, ni relojes de pared, ni time.Sleep. El viaje del
// payload HASTA flow_events y su agregación por `reason` los cubre, contra Postgres
// real, collector_integration_test.go (T4.2).

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// t41ReasonDelEscape devuelve el `reason` del PRIMER event_escaped que vio el sink.
// Falla el test si no hubo ninguno: un escenario que no emite el efecto no puede
// afirmar nada sobre su causa, y dejarlo pasar como "" convertiría este helper en
// una fábrica de falsos verdes —exactamente el modo en que un test deja de mirar—.
func t41ReasonDelEscape(t *testing.T, sink *ecSink) string {
	t.Helper()
	for _, eff := range sink.effectsAll() {
		if eff.Name != runtime.EffectEventEscaped {
			continue
		}
		reason, ok := eff.Payload["reason"]
		if !ok {
			t.Fatalf("el %q emitido no declara `reason` en su payload; payload=%v (T4.1: los TRES emisores pasan por emitEventEscaped, que lo exige)",
				runtime.EffectEventEscaped, eff.Payload)
		}
		s, ok := reason.(string)
		if !ok {
			t.Fatalf("el `reason` debe ser una cadena (viaja a payload->>'reason' y de ahí a una etiqueta de Prometheus); llegó %T con valor %v", reason, reason)
		}
		return s
	}
	t.Fatalf("el escenario no emitió ningún %q; efectos vistos=%v", runtime.EffectEventEscaped, effectNames(sink))
	return ""
}

// t41EnvMenuHuerfano monta el escenario del QUINTO camino de suelta
// (releaseOrphanMenu, E-6): un evento `menu` SIN flujo propio (D-043.3) sobre el que
// el contacto teclea algo que la lista del despachador no reconoce. Es el montaje de
// orphan_menu_escape_test.go, con el sink devuelto para poder leer el payload.
func t41EnvMenuHuerfano(t *testing.T) (*runtime.Runtime, *contact.MemoryResolver, *memEventStore, *ecSink) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range []trigger.Rule{
		{TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "menu",
			MatchType: trigger.MatchExact, EventKind: "menu", FlowID: "", Enabled: true},
		{TenantID: testTenant, Kind: trigger.KindFallback, FlowID: testFlow, Enabled: true},
	} {
		if _, err := ts.Insert(context.Background(), r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC))
	sink := &ecSink{}
	// La lista solo ofrece "9", igual que en orphan_menu_escape_test.go: así ningún
	// dígito real del despachador interfiere y lo que se mide es el camino SIN opción.
	menu := events.Menu{Options: []events.MenuOption{{Number: 9, Action: events.ActionStart, Kind: "cart"}}}
	rt := runtime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEventSink(sink),
		runtime.WithDispatcher(fakeDispatcher{menu: menu}),
		runtime.WithFlowForKind(fakeFlowForKind{flow: testFlow}))
	return rt, contacts, evs, sink
}

// TestEventEscaped_TresCausasDistinguibles (T4.1, criterio 1): los tres emisores
// declaran su causa, y las tres son distintas. Es el test que hace la telemetría
// capaz de contestar «¿este abandono lo pidió el cliente o se lo hicimos nosotros?»,
// que es la pregunta que el nombre único del efecto no puede contestar.
func TestEventEscaped_TresCausasDistinguibles(t *testing.T) {
	vistos := map[string]string{} // reason -> qué escenario lo produjo

	t.Run("el flujo llegó a su nodo terminal y su fila se recoge", func(t *testing.T) {
		sink := &ecSink{}
		rt, _, _, contacts, evs := mudoH2EnvCon(t, runtime.WithEventSink(sink))
		_ = resolveID(t, contacts, testContact)
		o2Escenario(t, rt, evs) // deja la fila TERMINAL con su dueño cerrado y el `menu` vivo

		// El ctx se declara AQUÍ y no en la función madre: los helpers de montaje
		// abren su propio context.Background(), y contextcheck marca (con razón)
		// cualquier llamada a uno de ellos con un ctx ya vivo en el ámbito.
		ctx := context.Background()
		// El turno de suelta: un texto normal sobre la fila terminal ⇒ releaseFinishedState.
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola que tal", "t41-o1")); err != nil {
			t.Fatalf("turno de suelta: %v", err)
		}

		got := t41ReasonDelEscape(t, sink)
		if got != runtime.EscapeReasonOwnerFlowFinished {
			t.Fatalf("releaseFinishedState debe declarar %q; declaró %q.\n"+
				"Aquí NADIE abandonó nada: el flujo se acabó solo y su fila se recoge — confundirlo con un abandono deliberado infla la métrica de fuga.",
				runtime.EscapeReasonOwnerFlowFinished, got)
		}
		vistos[got] = "releaseFinishedState"
	})

	t.Run("el menú no reconoció el texto y soltó su estado", func(t *testing.T) {
		rt, contacts, evs, sink := t41EnvMenuHuerfano(t)
		_ = resolveID(t, contacts, testContact)

		ctx := context.Background()
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "menu", "t41-o2a")); err != nil {
			t.Fatalf("menu: %v", err)
		}
		men := aliveOfKind(t, evs, "menu")
		if men.ID == "" {
			t.Fatal("precondición: debía nacer el evento `menu`")
		}
		// Un texto libre que la lista NO reconoce ⇒ releaseOrphanMenu.
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "pues no sé", "t41-o2b")); err != nil {
			t.Fatalf("texto libre: %v", err)
		}

		got := t41ReasonDelEscape(t, sink)
		if got != runtime.EscapeReasonOrphanMenu {
			t.Fatalf("releaseOrphanMenu debe declarar %q; declaró %q.\n"+
				"Esta causa mide algo que las otras dos no: la lista del despachador se está quedando corta para lo que la gente escribe.",
				runtime.EscapeReasonOrphanMenu, got)
		}
		vistos[got] = "releaseOrphanMenu"
	})

	t.Run("el cliente pidió salir con la palabra exacta", func(t *testing.T) {
		rt, _, _, contacts, _, sink := newEscapeRuntime(t, 5) // burst holgado: el aviso sí sale
		_ = resolveID(t, contacts, testContact)

		ctx := context.Background()
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-o3a")); err != nil {
			t.Fatalf("carrito: %v", err)
		}
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "t41-o3b")); err != nil {
			t.Fatalf("salir: %v", err)
		}

		got := t41ReasonDelEscape(t, sink)
		if got != runtime.EscapeReasonClientEscape {
			t.Fatalf("handleEscape debe declarar %q; declaró %q.\n"+
				"Es el ÚNICO de los tres que el contacto pidió: si se mezcla con los otros dos, el abandono deliberado deja de ser medible.",
				runtime.EscapeReasonClientEscape, got)
		}
		vistos[got] = "handleEscape"
	})

	// La aserción que ninguno de los tres subtests puede hacer por su cuenta: que las
	// tres causas son DISTINTAS. Sin esto, tres emisores que declararan todos el mismo
	// literal pasarían los tres subtests de arriba —cada uno comprueba el suyo— y la
	// tarea entera no habría servido de nada.
	if len(vistos) != 3 {
		t.Fatalf("las TRES causas deben ser distintas entre sí; se vieron %d valores distintos: %v\n"+
			"(REQ-053.4: el nombre del efecto es el mismo a propósito, así que el `reason` es lo ÚNICO que las separa)",
			len(vistos), vistos)
	}
}

// TestEventEscaped_PayloadExistenteNoCambiaDeForma (T4.1, criterio 2): añadir
// `reason` no toca lo que ya viajaba.
//
// `history_id` y `kind` los consume el colector de flowlifecycle (que agrupa por
// `payload->>'kind'`) y el endpoint de telemetría. Romperlos no daría ningún error:
// daría una métrica que se queda plana, que es la forma más cara de romper algo.
func TestEventEscaped_PayloadExistenteNoCambiaDeForma(t *testing.T) {
	rt, _, _, contacts, evs, sink := newEscapeRuntime(t, 5)
	_ = resolveID(t, contacts, testContact)

	ctx := context.Background()
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-p1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs, "cart")
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "t41-p2")); err != nil {
		t.Fatalf("salir: %v", err)
	}

	var payload map[string]any
	for _, eff := range sink.effectsAll() {
		if eff.Name == runtime.EffectEventEscaped {
			payload = eff.Payload
			break
		}
	}
	if payload == nil {
		t.Fatalf("no se emitió ningún %q; efectos vistos=%v", runtime.EffectEventEscaped, effectNames(sink))
	}

	// Las dos claves de siempre, con sus valores de siempre.
	if got := payload["history_id"]; got != cart.HistoryID {
		t.Fatalf("history_id = %v, quiero %v (lo consume el endpoint de telemetría)", got, cart.HistoryID)
	}
	if got := payload["kind"]; got != cart.Kind {
		t.Fatalf("payload[kind] = %v, quiero %q — y es el TIPO DE EVENTO, NUNCA la columna kind de la fila (\"event\"): "+
			"confundirlas colapsa la métrica del colector en una sola serie, sin error", got, cart.Kind)
	}
	// Y exactamente UNA clave nueva, ni más.
	if len(payload) != 3 {
		t.Fatalf("el payload debe llevar EXACTAMENTE {history_id, kind, reason}; llegó con %d claves: %v", len(payload), payload)
	}
}

// TestEventLifecycle_SinCausaNoGanaLaClave es el contraste del anterior, y protege
// la mitad que nadie mira: los otros SEIS efectos de ciclo de vida NO deben ganar
// `reason`.
//
// Escribir `"reason": ""` en todos habría sido más fácil y peor: `flow_events` es
// append-only, así que cada fila que se escriba con una clave vacía obliga para
// siempre a todo lector a distinguir «sin causa» de «causa vacía». La ausencia de la
// clave es el dato.
func TestEventLifecycle_SinCausaNoGanaLaClave(t *testing.T) {
	rt, _, _, contacts, _, sink := newEscapeRuntime(t, 5)
	_ = resolveID(t, contacts, testContact)

	ctx := context.Background()
	// Arrancar el carrito emite event_started: un efecto de ciclo de vida que NO es
	// escaped y que por tanto no tiene causa que declarar.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t41-s1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}

	visto := false
	for _, eff := range sink.effectsAll() {
		if eff.Name != runtime.EffectEventStarted {
			continue
		}
		visto = true
		if _, tiene := eff.Payload["reason"]; tiene {
			t.Fatalf("%q NO debe llevar `reason` en su payload: su nombre ya es su causa. Payload=%v",
				runtime.EffectEventStarted, eff.Payload)
		}
	}
	if !visto {
		t.Fatalf("precondición: arrancar el carrito debía emitir %q; efectos vistos=%v",
			runtime.EffectEventStarted, effectNames(sink))
	}
}
