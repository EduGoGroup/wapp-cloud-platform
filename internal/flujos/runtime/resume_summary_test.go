package runtime_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// ---------------------------------------------------------------------------
// Plan 043 · Ola 3 · T3.4 — «además se envía al cliente al reanudar» (E-4)
//
// El resumen se arma AL VUELO y con vars nil: cuando el cliente vuelve, el flow_state
// ya no existe, y tras vencer la inactividad tampoco hay fila `summary` que leer
// —vencer no es abandonar—. Todo lo que se le recuerda sale de las fuentes DURABLES.
// ---------------------------------------------------------------------------

// inicioAnclado da el instante de arranque de estos tests ANCLADO A LA HORA REAL, y no
// una fecha inventada. No es manía: el resumen de la encuesta acota las respuestas con
// `r.CreatedAt.Before(ev.CreatedAt)`, y esas dos marcas vienen de relojes distintos en
// un test —el evento del reloj inyectado, las filas del reloj de la máquina—. Con una
// fecha fija en el futuro la cota descartaría respuestas legítimas; con una en el pasado
// dejaría pasar lo que producción filtra. Anclando al real, la cota se comporta aquí
// como allí.
//
// Se resta un margen para que el evento nazca ANTES que cualquier fila que el propio
// test escriba después, que es el orden que tiene la vida real.
func inicioAnclado() time.Time { return time.Now().Add(-4 * time.Hour) }

// newRescateRuntime arma el runtime del rescate con las fuentes durables REALES (las
// dos, contra el repositorio), que es lo que hace posible el resumen al vuelo. El
// carrito y la encuesta van los dos registrados porque los tests rescatan de ambos.
func newRescateRuntime(t *testing.T, inicio time.Time, rules ...trigger.Rule) (
	*runtime.Runtime, *store.MemoryRepository, *fakeSender, *contact.MemoryResolver, *relojEventStore,
) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	repo.SetTenantContent(testTenant, "catalogo", []byte(cartCatalogBlob))
	if _, err := repo.InsertDefinition(ctx, testTenant, cartFlow(testCartFlow)); err != nil {
		t.Fatalf("sembrar cart flow: %v", err)
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
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(survey.New())
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(content.NewRouter(content.NewStatic(), content.NewJSON(repo))))
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	evs := nuevoRelojEventStore(inicio)
	rt := runtime.New(repo, eng, sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithEventStore(evs),
		runtime.WithEventSink(persistSinkWith(repo)),
		cartResumeOpt(repo),
		runtime.WithSummarySources(runtime.NewSummarySources(repo)),
		runtime.WithClock(evs.ahora))
	return rt, repo, sender, contacts, evs
}

// TestRescate_ElCarritoVuelveConSusLineas: el cliente arma un pedido, se calla más de
// la cuenta y vuelve. Lo primero que recibe es lo que llevaba, leído de intake_items
// —durable— y no del flow_state, que al vencer la inactividad se soltó.
func TestRescate_ElCarritoVuelveConSusLineas(t *testing.T) {
	t0 := inicioAnclado()
	rt, repo, sender, _, evs := newRescateRuntime(t, t0, cartEventRule())
	sembrarInactividad(t, repo, time.Hour, 0)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.rc0")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	cartAddCafe(t, rt, "wamid.rcadd")
	ev := aliveOfKind(t, evs.memEventStore, "cart")

	// Silencio de 90 minutos contra 1 h de tolerancia: la conversación se suelta y el
	// evento sigue `open` (sin fila de resumen: vencer no es abandonar).
	evs.en(t0.Add(90 * time.Minute))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola?", "wamid.rc1")); err != nil {
		t.Fatalf("tras el silencio: %v", err)
	}
	antes := sender.count()

	// Vuelve y lo rescata.
	evs.en(t0.Add(2 * time.Hour))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.rc2")); err != nil {
		t.Fatalf("rescate: %v", err)
	}

	nuevos := sender.texts()[antes:]
	if len(nuevos) < 2 {
		t.Fatalf("el rescate manda el resumen Y la pantalla del flujo; mandó %d: %q", len(nuevos), nuevos)
	}
	if resumen := nuevos[0]; !strings.Contains(resumen, "Café") {
		t.Fatalf("el resumen del rescate debe traer las líneas del pedido: %q", resumen)
	}
	// Rescatar NO persiste: el resumen es un recordatorio, no un cierre (E-4).
	if got := evs.resumenesDe(ev.ID); len(got) != 0 {
		t.Fatalf("rescatar no escribe fila de resumen, y escribió %d", len(got))
	}
}

// TestRescate_LaEncuestaVuelveConSusRespuestas: el hermano del anterior para el otro
// tipo que acumula decisiones. Con vars nil las respuestas solo pueden salir de la
// fuente durable (survey_results).
func TestRescate_LaEncuestaVuelveConSusRespuestas(t *testing.T) {
	t0 := inicioAnclado()
	surveyRule := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "encuesta",
		MatchType: trigger.MatchExact, EventKind: "survey", FlowID: testSurveyFlow, Enabled: true,
	}
	rt, repo, sender, _, evs := newRescateRuntime(t, t0, surveyRule)
	sembrarInactividad(t, repo, time.Hour, 0)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "encuesta", "wamid.rs0")); err != nil {
		t.Fatalf("encuesta: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.rs1")); err != nil {
		t.Fatalf("responder q1: %v", err)
	}

	evs.en(t0.Add(90 * time.Minute))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola?", "wamid.rs2")); err != nil {
		t.Fatalf("tras el silencio: %v", err)
	}
	antes := sender.count()

	evs.en(t0.Add(2 * time.Hour))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "encuesta", "wamid.rs3")); err != nil {
		t.Fatalf("rescate: %v", err)
	}

	nuevos := sender.texts()[antes:]
	if len(nuevos) < 2 {
		t.Fatalf("el rescate manda el resumen Y la pantalla; mandó %d: %q", len(nuevos), nuevos)
	}
	// El assert va sobre el CONTEO, no sobre el `question_id`: el render de cara al
	// cliente no enseña identificadores internos —«q1» no significa nada para quien
	// responde— y comprobarlos aquí sería exigirle al texto que filtre jerga. Lo que
	// acredita que el lector durable funcionó es que sepa que había UNA respuesta:
	// sin él, el resumen saldría vacío y no se enviaría nada (ver la rotura R17).
	if resumen := nuevos[0]; !strings.Contains(resumen, "1 pregunta") {
		t.Fatalf("el resumen de la encuesta rescatada debe reflejar la respuesta ya dada: %q", resumen)
	}
}

// TestRescate_NoSeCuelaLaEncuestaDelMesPasado fija que la cota por fecha HACE algo.
//
// `survey_results` no tiene `session_id` ni identidad de pasada: la consulta por
// (tenant, contacto, flujo) devuelve también lo que esta misma persona respondió otra
// vez. Lo único que separa una tanda de otra es que el evento nace TARDE, después de
// todo lo anterior. Sin la cota, quien rescata su encuesta de hoy vería contadas las
// respuestas de la vez pasada.
func TestRescate_NoSeCuelaLaEncuestaDelMesPasado(t *testing.T) {
	t0 := inicioAnclado()
	surveyRule := trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEventStart, Keyword: "encuesta",
		MatchType: trigger.MatchExact, EventKind: "survey", FlowID: testSurveyFlow, Enabled: true,
	}
	rt, repo, sender, contacts, evs := newRescateRuntime(t, t0, surveyRule)
	sembrarInactividad(t, repo, time.Hour, 0)
	ctx := context.Background()

	// La tanda VIEJA: dos respuestas del mismo contacto al mismo flujo, fechadas mucho
	// antes de que naciera el evento de hoy.
	cid := resolveID(t, contacts, testContact)
	viejo := t0.Add(-30 * 24 * time.Hour)
	if err := repo.InsertResults(ctx, []store.SurveyResult{
		{TenantID: testTenant, ContactID: cid, FlowID: testSurveyFlow, FlowVersion: 1, QuestionID: "q1", AnswerCode: "2", CreatedAt: viejo},
		{TenantID: testTenant, ContactID: cid, FlowID: testSurveyFlow, FlowVersion: 1, QuestionID: "q2", AnswerCode: "2", CreatedAt: viejo},
	}); err != nil {
		t.Fatalf("sembrar la tanda vieja: %v", err)
	}

	// La de HOY: se empieza la encuesta y se responde UNA pregunta.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "encuesta", "wamid.vj0")); err != nil {
		t.Fatalf("encuesta: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.vj1")); err != nil {
		t.Fatalf("responder q1: %v", err)
	}

	evs.en(t0.Add(90 * time.Minute))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola?", "wamid.vj2")); err != nil {
		t.Fatalf("tras el silencio: %v", err)
	}
	antes := sender.count()
	evs.en(t0.Add(2 * time.Hour))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "encuesta", "wamid.vj3")); err != nil {
		t.Fatalf("rescate: %v", err)
	}

	resumen := sender.texts()[antes]
	if !strings.Contains(resumen, "1 pregunta") {
		t.Fatalf("solo cuenta la respuesta de HOY; las %d de la tanda vieja no se cuelan: %q", 2, resumen)
	}
}

// TestRescate_SinNadaDecididoNoMandaResumen: quien no decidió nada no tiene qué
// recordar, así que se retoma en silencio en vez de mandar un resumen vacío que solo
// ocuparía pantalla.
func TestRescate_SinNadaDecididoNoMandaResumen(t *testing.T) {
	t0 := inicioAnclado()
	rt, repo, sender, _, evs := newRescateRuntime(t, t0, cartEventRule())
	sembrarInactividad(t, repo, time.Hour, 0)
	ctx := context.Background()

	// Nace el carrito y NO se elige nada.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.rv0")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	evs.en(t0.Add(90 * time.Minute))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola?", "wamid.rv1")); err != nil {
		t.Fatalf("tras el silencio: %v", err)
	}
	antes := sender.count()

	evs.en(t0.Add(2 * time.Hour))
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "wamid.rv2")); err != nil {
		t.Fatalf("rescate: %v", err)
	}

	if nuevos := sender.texts()[antes:]; len(nuevos) != 1 {
		t.Fatalf("sin nada decidido solo va la pantalla del flujo; mandó %d: %q", len(nuevos), nuevos)
	}
}
