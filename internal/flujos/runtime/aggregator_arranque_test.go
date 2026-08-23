// aggregator_arranque_test.go — Plan 044 · Ola 1. EL MENSAJE QUE ARRANCA EL EVENTO.
//
// ⏳ NO SE HA EJECUTADO. Escrito en un entorno sin Go, sin red y sin Postgres, así que
// no está declarado como pasado. Lo que sí está escrito es CÓMO ponerlo rojo: la
// mutación va con cada test y está elegida para que COMPILE.
//
// # QUÉ CUBRE, Y POR QUÉ HACÍA FALTA UN FICHERO PARA ESTO
//
// El resto de tests del agregador (aggregator_test.go) ejercen el IntakeAggregator
// DIRECTAMENTE: llaman a Observe con una IncomingRef ya montada. Eso prueba la ventana,
// pero no prueba QUIÉN la alimenta — y ahí estaba el defecto: `observeForAggregation`
// tenía UN solo punto de llamada, `advanceLiveStep`, que es el turno de una conversación
// YA VIVA. El mensaje que ABRE el evento —el «quiero presupuesto de X» que arranca la
// ráfaga, el caso literal que este plan existe para resolver— entra por el camino del
// disparador (handleTrigger → startFromDecision) y NO llegaba a `source_refs`.
//
// La consecuencia medible no era «falta una referencia»: era que la ventana la abría el
// mensaje SIGUIENTE, así que `message_ts` —la BASE DE FECHAS del presupuesto, D-044.9—
// quedaba anclado al segundo mensaje. «Para el jueves» se resolvía contra el instante
// equivocado, sin error y sin que nadie lo viera.
//
// Por eso esto se prueba de PUNTA A PUNTA (HandleIncoming ⇒ intake_jobs) y no llamando a
// Observe: lo que estaba roto era el cableado, no la ventana.
package runtime_test

import (
	"context"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

// entranteConTS es `incoming` (runtime_engine_test.go) MÁS el `ts_unix` del cliente.
// Aquí el ts no es decorativo y no se puede omitir: es lo que acaba en
// `intake_jobs.message_ts`, y este test existe justo para afirmar CUÁL de los dos
// mensajes lo puso.
func entranteConTS(from, text, waID string, ts int64) *cloudlinkv1.IncomingMessage {
	return &cloudlinkv1.IncomingMessage{From: from, Text: text, WaMessageId: waID, TsUnix: ts}
}

// arranqueEntorno arma el runtime del plano de eventos (el mismo de newEventRuntime)
// MÁS el agregador cableado con la feature `llm_intake` encendida. Devuelve lo que los
// asserts necesitan: el runtime, el doble de intake_jobs y el resolver de contactos.
func arranqueEntorno(t *testing.T, rules ...trigger.Rule) (*flowruntime.Runtime, *intake.MemoryStore, *contact.MemoryResolver) {
	t.Helper()
	ctx := context.Background()

	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC))

	jobs := intake.NewMemoryStore(nuevoAggReloj().now)
	ents := entitlements.NewFake()
	ents.Enable(testTenant, entitlements.FeatureLLMIntake)
	// El sink se construye con el DEFAULT de reloj y de ventana a propósito: este test
	// no barre nada. Lo que afirma es lo que queda en la fila MIENTRAS la ventana sigue
	// abierta, que es donde vive el defecto.
	agg := flowruntime.NewIntakeAggregator(aggLogger(), jobs, repo, ents)

	rt := flowruntime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant},
		contacts, discardLogger(),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		flowruntime.WithEventStore(evs),
		flowruntime.WithAggregator(agg))
	return rt, jobs, contacts
}

// TestArranqueDelEvento_ElPrimerMensajeEntraEnLaVentana es el test del defecto, dicho
// con el caso real: el cliente escribe «carrito» —que es la palabra que ABRE el
// pedido— y siete segundos después teclea su primera opción. Eso es UNA ráfaga de dos
// mensajes, y la ventana tiene que llevarlos LOS DOS, en orden, anclada en el PRIMERO.
//
// Las dos aserciones son distintas y las dos importan:
//
//   - `source_refs` con las DOS referencias y en orden: es el rastro que sobrevive al
//     vaciado del sobre en estado terminal (INV-13), así que perder una es perder para
//     siempre la constancia de que ese mensaje existió;
//   - `message_ts` = el ts del PRIMERO: es la base de fechas del presupuesto (D-044.9).
//     Esta es la que fallaba en silencio — con la ventana abierta por el segundo
//     mensaje, «para el jueves» se resolvía contra su instante y no contra el del
//     pedido.
//
// MUTACIÓN (compila, y es EXACTAMENTE el estado del que se viene): en incoming.go, en
// startFromDecision, sustituir la línea
//
//	rt.observeForAggregation(ctx, tenantID, sessionID, contactID, eventID, m)
//
// por
//
//	_ = eventID
//
// («eventID» sigue usada y el paquete compila). La ventana vuelve a abrirla el segundo
// mensaje: `source_refs` queda con UNA sola referencia y `message_ts` pasa a ser el ts
// del segundo. Las dos aserciones se ponen rojas, que es el punto — el defecto no era
// una, eran las dos.
//
// MUTACIÓN 2 (compila, y prueba el otro medio cable): en events.go, en birthEvent,
// sustituir la ÚLTIMA línea de la función,
//
//	return ev.ID, eerr
//
// por
//
//	return "", eerr
//
// 🔧 REESCRITA EL 2026-08-22 CONTRA LA FIRMA REAL. Antes citaba literalmente el
// `return ev.ID, rt.enterEventFlow(ctx, key, …, tagline)` de una sola línea, y esa línea
// ya no existe: enterEventFlow ganó dos parámetros/valores —el turno de apertura
// (`opening`, Plan 044 · T1.4) y el bool del arranque cortado por el sink durable— y la
// cola de birthEvent se partió en un `if cortado { return "", eerr }` más el `return`
// final. Copiada tal cual, la mutación vieja NO se podía aplicar. Ojo al elegir cuál se
// toca: la del `if cortado` ya devuelve "" a propósito; la que hay que mutar es la
// ÚLTIMA, el camino normal.
//
// El evento nace igual y el turno se consume igual, pero el id no sube: la clave de
// ventana queda sin `event_id`, `WindowKey.Valid()` da false y `Observe` descarta el
// arranque sin decir nada. Mismo resultado rojo por una causa distinta, que es
// justamente la que hace falta vigilar: aquí el fallo sería MUDO.
func TestArranqueDelEvento_ElPrimerMensajeEntraEnLaVentana(t *testing.T) {
	ctx := context.Background()
	rt, jobs, contacts := arranqueEntorno(t, eventStartRule("carrito", "cart"))

	const tsArranque int64 = 1_755_859_200 // el instante del PEDIDO. Lo único que puede anclar la ventana.
	const tsSegundo int64 = tsArranque + 7

	// 1) EL MENSAJE QUE ABRE EL EVENTO. Sin conversación viva ⇒ camino del disparador.
	if err := rt.HandleIncoming(ctx, testSession,
		entranteConTS(testContact, "carrito", "wamid.arranque", tsArranque)); err != nil {
		t.Fatalf("HandleIncoming del arranque: %v", err)
	}

	// 2) EL SEGUNDO MENSAJE DE LA MISMA RÁFAGA. Ya hay conversación viva ⇒ camino del
	// turno normal (advanceLiveStep), que es el único que observaba antes de esta
	// corrección.
	if err := rt.HandleIncoming(ctx, testSession,
		entranteConTS(testContact, "1", "wamid.segundo", tsSegundo)); err != nil {
		t.Fatalf("HandleIncoming del segundo mensaje: %v", err)
	}

	abiertos := jobs.Jobs()
	if len(abiertos) != 1 {
		t.Fatalf("los dos mensajes son UNA ventana; hay %d jobs (%+v)", len(abiertos), abiertos)
	}
	j := abiertos[0]

	quiero := []string{"wamid.arranque", "wamid.segundo"}
	if len(j.SourceRefs) != len(quiero) {
		t.Fatalf("source_refs = %v; se esperaban las DOS referencias, la del arranque incluida", j.SourceRefs)
	}
	for i, ref := range quiero {
		if j.SourceRefs[i] != ref {
			t.Fatalf("source_refs[%d] = %q; se esperaba %q (el ORDEN es parte del contrato)", i, j.SourceRefs[i], ref)
		}
	}

	esperado := time.Unix(tsArranque, 0).UTC()
	if !j.MessageTS.Equal(esperado) {
		t.Fatalf("message_ts = %v; tenía que ser el del mensaje que ARRANCÓ el evento (%v), no el del segundo (%v). "+
			"Es la base de fechas del presupuesto (D-044.9)",
			j.MessageTS, esperado, time.Unix(tsSegundo, 0).UTC())
	}

	// La ventana sigue ABIERTA: nada de esto cierra nada en línea con el mensaje
	// (D-044.26 — el cierre lo ejecuta el barrido, fuera del camino del entrante).
	if j.Status != intake.StatusAggregating {
		t.Fatalf("status = %q; el camino del entrante NUNCA cierra una ventana", j.Status)
	}

	// Y la ventana cuelga del evento de verdad, no de una clave a medias: sin
	// `event_id` la fila no existiría (NOT NULL en la 0072) y este job no estaría aquí.
	if j.Key.EventID == "" {
		t.Fatal("la ventana se abrió sin event_id: la clave estaría incompleta y la 0072 la rechazaría")
	}
	if j.Key.ContactID != resolveID(t, contacts, testContact) {
		t.Fatalf("contact_id de la ventana = %q; tenía que ser el opaco ya resuelto", j.Key.ContactID)
	}
}

// TestArranqueDelEvento_SinLaFeatureNoSeAbreNadaPorEsteCamino cierra la otra mitad del
// cableado nuevo: el punto de llamada añadido NO trae una puerta trasera. El gate del
// agregador es `llm_intake` y es fail-closed, y eso tiene que seguir siendo cierto
// también por el camino del disparador — que es el que acaba de aprender a observar.
//
// Es una no-regresión barata y deliberada: un cable nuevo que se saltara el gate abriría
// ventanas para tenants que no tienen el pipeline contratado, y lo haría solo en el
// primer mensaje de cada pedido, que es el sitio donde nadie miraría.
//
// MUTACIÓN (compila): en aggregator.go, dentro de Observe, sustituir
//
//	if !has {
//
// por
//
//	if !has && false {
//
// («has» sigue usada y el paquete compila, pero el gate deja de cerrar).
func TestArranqueDelEvento_SinLaFeatureNoSeAbreNadaPorEsteCamino(t *testing.T) {
	ctx := context.Background()

	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(ctx, eventStartRule("carrito", "cart")); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	jobs := intake.NewMemoryStore(nuevoAggReloj().now)
	// El tenant NO tiene la feature: el Fake se queda sin habilitar nada.
	agg := flowruntime.NewIntakeAggregator(aggLogger(), jobs, repo, entitlements.NewFake())
	rt := flowruntime.New(repo, newEngine(), &fakeSender{}, fakeResolver{tenantID: testTenant},
		contact.NewMemoryResolver(repo), discardLogger(),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		flowruntime.WithEventStore(newMemEventStore(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC))),
		flowruntime.WithAggregator(agg))

	if err := rt.HandleIncoming(ctx, testSession,
		entranteConTS(testContact, "carrito", "wamid.arranque", 1_755_859_200)); err != nil {
		t.Fatalf("HandleIncoming del arranque: %v", err)
	}

	if n := len(jobs.Jobs()); n != 0 {
		t.Fatalf("sin llm_intake el arranque no puede abrir ninguna ventana; hay %d jobs", n)
	}
	if c := jobs.Counters(); c.OpenOrAppend != 0 {
		t.Fatalf("el gate cierra ANTES de escribir: OpenOrAppend=%d, se esperaba 0", c.OpenOrAppend)
	}
}
