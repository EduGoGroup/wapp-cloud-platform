package runtime_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// ---------------------------------------------------------------------------
// Plan 043 · Ola 3 · T3.4 — el CABLEADO del resumen (E-4)
//
// El resumen lo construye events.PersistSummary; lo que se prueba aquí es QUIÉN lo
// llama, que es la mitad que decide si el historial cuenta la verdad. Y sobre todo
// quién NO lo llama: el vencimiento de inactividad.
// ---------------------------------------------------------------------------

// lineasFijas es el lector durable de las líneas del pedido. Devuelve siempre lo
// mismo porque lo que se está probando no es el contenido del resumen —eso es de D—
// sino que exista una fila, en el evento correcto y en el momento correcto.
type lineasFijas struct {
	lineas []events.SummaryLine
	veces  int
}

func (l *lineasFijas) OpenIntakeLines(context.Context, string, string, string) ([]events.SummaryLine, error) {
	l.veces++
	return l.lineas, nil
}

func unaLinea() *lineasFijas {
	return &lineasFijas{lineas: []events.SummaryLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5},
	}}
}

// newResumenRuntime arma el runtime con el plano de eventos, el lector de líneas y las
// reglas dadas. El reloj del almacén es fijo salvo que el test lo mueva.
func newResumenRuntime(t *testing.T, inicio time.Time, lineas *lineasFijas, rules ...trigger.Rule) (
	*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *relojEventStore,
) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar menu flow: %v", err)
	}
	if _, err := repo.InsertDefinition(ctx, testTenant, surveyFlow()); err != nil {
		t.Fatalf("sembrar survey flow: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range rules {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := nuevoRelojEventStore(inicio)
	// El lector de líneas se suplanta para no armar un carrito real en cada escenario:
	// lo que estos tests prueban es QUIÉN llama al resumen, no de dónde salen las
	// líneas. El adaptador de verdad tiene su propio test.
	rt := runtime.New(repo, newSurveyEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithSummarySources(events.SummarySources{Lines: lineas, Answers: runtime.NewSummarySources(repo).Answers}),
		runtime.WithEventSink(persistSinkWith(repo)),
		runtime.WithClock(evs.ahora))
	return rt, repo, sender, contacts, evs
}

// reglaEvento ata una palabra a un tipo de evento y a un flujo.
func reglaEvento(keyword, kind, flowID string) trigger.Rule {
	return trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: keyword,
		MatchType: trigger.MatchExact, EventKind: kind, FlowID: flowID, Enabled: true,
	}
}

// TestResumen_SaltoPorTipoEscribeUnaFilaEnElQueSeAbandona es el criterio del plan:
// saltar de `cart` a `survey` deja EXACTAMENTE una fila de resumen, y ligada al evento
// que se abandonó — no al de destino, que es el error que un cableado descuidado
// comete sin que nada chille.
//
// El segundo salto añade una segunda fila SIN pisar la primera: el historial es
// append-only y `seq` es lo que lo demuestra.
func TestResumen_SaltoPorTipoEscribeUnaFilaEnElQueSeAbandona(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	lineas := unaLinea()
	rt, _, _, _, evs := newResumenRuntime(t, t0, lineas,
		reglaEvento("carrito", "cart", testFlow),
		reglaEvento("encuesta", "survey", testSurveyFlow))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.s1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs.memEventStore, "cart")
	if got := evs.totalResumenes(); got != 0 {
		t.Fatalf("nacer no es abandonar: no debe haber resúmenes todavía, hay %d", got)
	}

	// El salto: de carrito a encuesta.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "encuesta", "wamid.s2")); err != nil {
		t.Fatalf("encuesta: %v", err)
	}
	survey := aliveOfKind(t, evs.memEventStore, "survey")

	if got := len(evs.resumenesDe(cart.ID)); got != 1 {
		t.Fatalf("el salto debe dejar UNA fila de resumen en el evento abandonado, hay %d", got)
	}
	if got := len(evs.resumenesDe(survey.ID)); got != 0 {
		t.Fatalf("y NINGUNA en el evento de destino, que no se ha abandonado; hay %d", got)
	}
	if got := evs.totalResumenes(); got != 1 {
		t.Fatalf("una fila en total, hay %d", got)
	}

	// Volver al carrito abandona la encuesta, pero aquí NADIE la respondió: se saltó a
	// ella y se saltó fuera. Sin una sola respuesta no hay nada que resumir, así que
	// PersistSummary devuelve escrito=false y no escribe fila — el caso normal, no una
	// carencia. Con la encuesta respondida sí deja su fila:
	// ver TestResumen_LaEncuestaResumeLoQueYaSeRespondio.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.s3")); err != nil {
		t.Fatalf("carrito otra vez: %v", err)
	}
	if got := len(evs.resumenesDe(survey.ID)); got != 0 {
		t.Fatalf("una encuesta sin responder nada no tiene qué resumir; apareció(eron) %d fila(s)", got)
	}

	// El SEGUNDO abandono del carrito sí añade su fila: es la garantía append-only del
	// criterio —una segunda fila SIN pisar la primera—, y el `seq` que devuelve el
	// almacén es lo que lo demuestra.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "encuesta", "wamid.s4")); err != nil {
		t.Fatalf("encuesta otra vez: %v", err)
	}
	if got := len(evs.resumenesDe(cart.ID)); got != 2 {
		t.Fatalf("el segundo abandono del carrito añade su fila: hay %d", got)
	}
	primera, segunda := evs.resumenesDe(cart.ID)[0], evs.resumenesDe(cart.ID)[1]
	if len(primera) == 0 || string(primera) != string(segunda) {
		// Mismo estado durable ⇒ mismo cuerpo. Si difirieran, el resumen no sería
		// determinista y el historial contaría dos cosas distintas del mismo pedido.
		t.Fatalf("las dos filas describen el mismo pedido y deben coincidir:\n1ª: %s\n2ª: %s", primera, segunda)
	}
}

// TestResumen_LaEncuestaResumeLoQueYaSeRespondio: abandonar una encuesta CON respuestas
// deja su fila, con las respuestas dentro.
//
// Este test nació como centinela de un agujero que resultó no existir, y la distinción
// que lo aclaró merece quedarse escrita: las respuestas de la encuesta salen de
// `flow_state.vars`, igual que el nivel del carrito, NO de survey_results. Por eso
// LoadSummary no necesita un lector aparte para este tipo. Lo que sí es cierto —y es
// otra cosa— es que una encuesta SIN una sola respuesta no tiene nada que resumir, y
// entonces PersistSummary devuelve escrito=false sin escribir: ese es el caso normal
// que el criterio del plan llama «nada que resumir», no una carencia.
func TestResumen_LaEncuestaResumeLoQueYaSeRespondio(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	lineas := unaLinea()
	rt, _, _, _, evs := newResumenRuntime(t, t0, lineas,
		reglaEvento("encuesta", "survey", testSurveyFlow),
		reglaEvento("carrito", "cart", testFlow))
	ctx := context.Background()

	// Nace la encuesta y se responde su primera pregunta: ya hay algo decidido.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "encuesta", "wamid.q1")); err != nil {
		t.Fatalf("encuesta: %v", err)
	}
	survey := aliveOfKind(t, evs.memEventStore, "survey")
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.q2")); err != nil {
		t.Fatalf("responder q1: %v", err)
	}
	// Y se abandona saltando al carrito: uno de los tres abandonos reales.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.q3")); err != nil {
		t.Fatalf("carrito: %v", err)
	}

	filas := evs.resumenesDe(survey.ID)
	if len(filas) != 1 {
		t.Fatalf("abandonar una encuesta ya respondida deja UNA fila, hay %d", len(filas))
	}
	// El cuerpo lleva la respuesta, no solo el tipo: un resumen que dijera «encuesta» y
	// nada más no serviría para lo que existe —recordar qué se había decidido—.
	if cuerpo := string(filas[0]); !strings.Contains(cuerpo, "q1") {
		t.Fatalf("el resumen debe traer las respuestas ya dadas: %s", cuerpo)
	}
	// El carrito de destino no resume: no se ha abandonado.
	if got := evs.totalResumenes(); got != 1 {
		t.Fatalf("solo el evento abandonado resume; hay %d filas en total", got)
	}
}

// TestResumen_ElVencimientoDeInactividadNoEscribeNada es la decisión de producto de
// esta ola, y va con test propio porque es una AUSENCIA: nada la delata sola.
//
// Al vencer el silencio el evento no muere —sigue `open` y rescatable, y lo único que
// pasa es que la conversación suelta el puntero—. CALLARSE NO ES ABANDONAR. Escribir
// ahí una fila marcaría como terminado lo que sigue abierto y, peor, la reescribiría
// en cada vencimiento sucesivo del mismo evento: el historial acabaría contando varios
// finales de algo que nunca terminó.
func TestResumen_ElVencimientoDeInactividadNoEscribeNada(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	lineas := unaLinea()
	rt, repo, _, _, evs := newResumenRuntime(t, t0, lineas, reglaEvento("carrito", "cart", testFlow))
	sembrarInactividad(t, repo, time.Hour, 0)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.v1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs.memEventStore, "cart")

	// Noventa minutos de silencio contra una hora de tolerancia.
	evs.en(t0.Add(90 * time.Minute))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "cualquier cosa", "wamid.v2")); err != nil {
		t.Fatalf("tras el vencimiento: %v", err)
	}

	if got := evs.totalResumenes(); got != 0 {
		t.Fatalf("vencer la inactividad NO escribe resumen (E-6): hay %d filas", got)
	}
	if got := lineas.veces; got != 0 {
		t.Fatalf("ni siquiera se leen las líneas para construirlo; se leyó %d veces", got)
	}
	if got := evs.alive(); len(got) != 1 || got[0].ID != cart.ID {
		t.Fatalf("y el evento sigue vivo y rescatable, que es justo el motivo: %+v", got)
	}

	// Y el segundo vencimiento tampoco: si se escribiera, el historial contaría dos
	// finales de un pedido que nadie ha terminado.
	evs.en(t0.Add(4 * time.Hour))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "otra cosa", "wamid.v3")); err != nil {
		t.Fatalf("segundo vencimiento: %v", err)
	}
	if got := evs.totalResumenes(); got != 0 {
		t.Fatalf("el segundo vencimiento tampoco escribe: hay %d filas", got)
	}
}

// TestResumen_EventStopEscribeSuFila: `event_stop` sí es un abandono —lo declara el
// cliente— y por eso deja su fila. El evento sigue `open`, que es lo que hace la
// diferencia con el vencimiento: aquí hubo una decisión, allí solo silencio.
func TestResumen_EventStopEscribeSuFila(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	lineas := unaLinea()
	parar := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStop, Keyword: "déjalo",
		MatchType: trigger.MatchExact, Enabled: true,
	}
	rt, _, sender, _, evs := newResumenRuntime(t, t0, lineas, reglaEvento("carrito", "cart", testFlow), parar)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.p1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs.memEventStore, "cart")
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "déjalo", "wamid.p2")); err != nil {
		t.Fatalf("déjalo: %v", err)
	}

	if got := len(evs.resumenesDe(cart.ID)); got != 1 {
		t.Fatalf("event_stop debe dejar UNA fila de resumen, hay %d", got)
	}
	if got := evs.statuses()[cart.ID]; got != events.StatusOpen {
		t.Fatalf("y el evento sigue open (event_stop no mata): quedó %q", got)
	}
	if got := strings.Join(sender.texts(), "\n"); !strings.Contains(got, "Sigue abierto") {
		t.Fatalf("y se confirma al cliente: %q", got)
	}
}

// TestResumen_EscapeGlobalEscribeSuFila: el tercero y el más brusco. El escape borra
// el flow_state, así que si el resumen se escribiera DESPUÉS del borrado se perdería
// el nivel de la sub-máquina sin que nada fallara.
func TestResumen_EscapeGlobalEscribeSuFila(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	lineas := unaLinea()
	escape := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEscape, Keyword: "salir",
		MatchType: trigger.MatchExact, Enabled: true,
	}
	rt, repo, _, contacts, evs := newResumenRuntime(t, t0, lineas, reglaEvento("carrito", "cart", testFlow), escape)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.e1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cart := aliveOfKind(t, evs.memEventStore, "cart")
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "wamid.e2")); err != nil {
		t.Fatalf("salir: %v", err)
	}

	if got := len(evs.resumenesDe(cart.ID)); got != 1 {
		t.Fatalf("el escape global debe dejar UNA fila de resumen, hay %d", got)
	}
	// Y el escape conserva su semántica EXACTA: el flow_state se fue.
	if _, ok, err := repo.Load(ctx, store.Key{
		TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, contacts, testContact),
	}); err != nil || ok {
		t.Fatalf("el escape sigue borrando el estado (ok=%v err=%v)", ok, err)
	}
}
