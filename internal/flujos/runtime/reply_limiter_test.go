package runtime_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
)

// newLimitedSurveyRuntime arma un runtime de encuesta (dos preguntas encadenadas)
// con un ReplyLimiter inyectado (Plan 020 · T0). La encuesta sirve para forzar
// VARIAS auto-respuestas sobre la MISMA clave de conversación y observar el tope.
func newLimitedSurveyRuntime(t *testing.T, lim runtime.ReplyLimiter) (*runtime.Runtime, *fakeSender, *store.MemoryRepository, *contact.MemoryResolver) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, surveyFlow()); err != nil {
		t.Fatalf("sembrar definición survey: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, newSurveyEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithReplyLimiter(lim))
	return rt, sender, repo, contacts
}

// TestReplyRateLimit_ExcedeLimite_DejaDeResponder: con burst 1 (y ritmo ~nulo:
// 1/hora) la conversación agota su único token en el primer avance por Step y la
// segunda auto-respuesta se corta.
//
// El bootstrap se siembra DIRECTO (repo.Save), no vía Start: desde Plan 054 · T2.3
// la encuesta es SIEMPRE durable y Start() ya no puede abrirla (ver seedSurveyOpen).
// El viejo comentario «Start NO pasa por el limiter» describía el envío de q1 por
// Start, que ya no ocurre aquí — lo que este test mide (el tope de auto-respuestas
// sobre avances) es ortogonal a cómo se abrió la conversación.
func TestReplyRateLimit_ExcedeLimite_DejaDeResponder(t *testing.T) {
	lim := ratelimit.NewLimiter(rate.Every(time.Hour), 1) // burst 1, prácticamente sin recarga
	rt, sender, repo, contacts := newLimitedSurveyRuntime(t, lim)
	ctx := context.Background()

	seedSurveyOpen(t, repo, contacts)
	// 1.er entrante (respuesta a q1): avanza a q2 y auto-responde → consume el token.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.a1")); err != nil {
		t.Fatalf("HandleIncoming q1: %v", err)
	}
	// 2.º entrante (respuesta a q2): el token está agotado → la auto-respuesta se corta.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.a2")); err != nil {
		t.Fatalf("HandleIncoming q2: %v", err)
	}

	if got := sender.count(); got != 1 {
		t.Fatalf("con burst 1 se esperaba 1 envío (q2; el token se agotó ahí), hubo %d", got)
	}
	for _, txt := range sender.texts() {
		if strings.Contains(txt, "Gracias") {
			t.Fatalf("el cierre de la encuesta NO debió enviarse (rate-limited): %q", txt)
		}
	}
}

// TestReplyRateLimit_DentroDelLimite_NoAfecta: con un burst holgado (3, el default)
// la MISMA conversación completa la encuesta sin recortes: q2 + cierre (bootstrap
// sembrado directo, ver el comentario del test anterior).
func TestReplyRateLimit_DentroDelLimite_NoAfecta(t *testing.T) {
	lim := ratelimit.NewLimiter(rate.Limit(0.5), 3) // defaults del Plan 020 · T0
	rt, sender, repo, contacts := newLimitedSurveyRuntime(t, lim)
	ctx := context.Background()

	seedSurveyOpen(t, repo, contacts)
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.b1")); err != nil {
		t.Fatalf("HandleIncoming q1: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.b2")); err != nil {
		t.Fatalf("HandleIncoming q2: %v", err)
	}

	if got := sender.count(); got != 2 {
		t.Fatalf("dentro del límite se esperaban 2 envíos (q2+cierre), hubo %d", got)
	}
	last := sender.texts()[len(sender.texts())-1]
	if !strings.Contains(last, "Gracias") {
		t.Fatalf("el cierre de la encuesta debió enviarse; último texto: %q", last)
	}
}

// newLimitedMenuRuntime arma un runtime de menú puro (sampleFlow, NO durable) con un
// ReplyLimiter inyectado: el gemelo de newLimitedSurveyRuntime pero sobre un flujo
// que Start() SÍ puede abrir. Lo necesita D3 (review de código): la propiedad «Start
// no cobra token del limiter» solo se puede demostrar arrancando algo por Start, y
// desde Plan 054 · T2.3 la encuesta es SIEMPRE durable (Start le da SIEMPRE
// ErrDurableFlowNeedsEvent, ver seedSurveyOpen) — por eso este gemelo usa el menú.
func newLimitedMenuRuntime(t *testing.T, lim runtime.ReplyLimiter) (*runtime.Runtime, *fakeSender, *store.MemoryRepository, *contact.MemoryResolver) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición menú: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithReplyLimiter(lim))
	return rt, sender, repo, contacts
}

// TestReplyRateLimit_Start_NoCobraToken (D3, review de código) recupera la propiedad
// que el docstring viejo de TestReplyRateLimit_ExcedeLimite_DejaDeResponder
// enunciaba («Start NO pasa por el limiter») y que se perdió sin aserción propia al
// reescribir ese test para la encuesta SIEMPRE-durable de Plan 054 · T2.3. La
// propiedad SIGUE siendo cierta —startLocked llama a rt.send (send.go), que nunca
// consulta replyAllowed; solo sendReply, el camino de los AVANCES en HandleIncoming,
// lo hace (send.go:65-70)— y merece su propia aserción, ahora sobre un flujo NO
// durable (testFlow/menú) que Start() sí puede abrir.
//
// Con burst 1: Start() envía el menú (1er saliente) SIN cobrar nada; el único token
// sigue intacto para el avance siguiente, que SÍ pasa por sendReply y lo cobra ahí.
// Si Start cobrara el token, el avance se quedaría sin cupo y su envío no saldría.
func TestReplyRateLimit_Start_NoCobraToken(t *testing.T) {
	lim := ratelimit.NewLimiter(rate.Every(time.Hour), 1) // burst 1, prácticamente sin recarga
	rt, sender, _, _ := newLimitedMenuRuntime(t, lim)
	ctx := context.Background()

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("Start debe enviar el menú (1 envío), hubo %d", got)
	}

	// El avance pasa por sendReply/replyAllowed (send.go): con el token intacto tras
	// Start, debe salir.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.avance")); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if got := sender.count(); got != 2 {
		t.Fatalf("con el token intacto tras Start (burst 1), el avance debía enviarse (2 envíos totales); hubo %d: %q", got, sender.texts())
	}
}
