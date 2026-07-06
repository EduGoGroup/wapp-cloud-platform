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
func newLimitedSurveyRuntime(t *testing.T, lim runtime.ReplyLimiter) (*runtime.Runtime, *fakeSender) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, surveyFlow()); err != nil {
		t.Fatalf("sembrar definición survey: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, newSurveyEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithReplyLimiter(lim))
	return rt, sender
}

// TestReplyRateLimit_ExcedeLimite_DejaDeResponder: con burst 1 (y ritmo ~nulo:
// 1/hora) la conversación agota su único token en el primer avance por Step y la
// segunda auto-respuesta se corta. Start NO pasa por el limiter (es acción de
// admin), así que la pregunta inicial se envía siempre.
func TestReplyRateLimit_ExcedeLimite_DejaDeResponder(t *testing.T) {
	lim := ratelimit.NewLimiter(rate.Every(time.Hour), 1) // burst 1, prácticamente sin recarga
	rt, sender := newLimitedSurveyRuntime(t, lim)
	ctx := context.Background()

	// Abre la encuesta por API: envía q1 (1 send, sin consumir cuota anti-loop).
	if _, err := rt.Start(ctx, testTenant, testSurveyFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start survey: %v", err)
	}
	// 1.er entrante (respuesta a q1): avanza a q2 y auto-responde → consume el token.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.a1")); err != nil {
		t.Fatalf("HandleIncoming q1: %v", err)
	}
	// 2.º entrante (respuesta a q2): el token está agotado → la auto-respuesta se corta.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.a2")); err != nil {
		t.Fatalf("HandleIncoming q2: %v", err)
	}

	if got := sender.count(); got != 2 {
		t.Fatalf("con burst 1 se esperaban 2 envíos (q1 por Start + q2), hubo %d", got)
	}
	for _, txt := range sender.texts() {
		if strings.Contains(txt, "Gracias") {
			t.Fatalf("el cierre de la encuesta NO debió enviarse (rate-limited): %q", txt)
		}
	}
}

// TestReplyRateLimit_DentroDelLimite_NoAfecta: con un burst holgado (3, el default)
// la MISMA conversación completa la encuesta sin recortes: q1 (Start) + q2 + cierre.
func TestReplyRateLimit_DentroDelLimite_NoAfecta(t *testing.T) {
	lim := ratelimit.NewLimiter(rate.Limit(0.5), 3) // defaults del Plan 020 · T0
	rt, sender := newLimitedSurveyRuntime(t, lim)
	ctx := context.Background()

	if _, err := rt.Start(ctx, testTenant, testSurveyFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start survey: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.b1")); err != nil {
		t.Fatalf("HandleIncoming q1: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.b2")); err != nil {
		t.Fatalf("HandleIncoming q2: %v", err)
	}

	if got := sender.count(); got != 3 {
		t.Fatalf("dentro del límite se esperaban 3 envíos (q1+q2+cierre), hubo %d", got)
	}
	last := sender.texts()[len(sender.texts())-1]
	if !strings.Contains(last, "Gracias") {
		t.Fatalf("el cierre de la encuesta debió enviarse; último texto: %q", last)
	}
}
