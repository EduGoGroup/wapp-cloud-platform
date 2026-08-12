package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/ingest"
)

// stubDeduper es un IngestDeduper de test: devuelve un veredicto fijo y registra
// las claves con que se lo invoca (para verificar que la ingesta lo consulta con
// (session_id, wa_message_id) y que NO se consulta ante wa_message_id vacío).
type stubDeduper struct {
	seen  bool
	err   error
	calls int
	keys  []string
}

func (d *stubDeduper) Seen(_ context.Context, sessionID, waMessageID string) (bool, error) {
	d.calls++
	d.keys = append(d.keys, sessionID+"|"+waMessageID)
	return d.seen, d.err
}

// newDedupeSurveyRuntime arma un runtime de encuesta (q1→q2→cierre) con un
// IngestDeduper inyectado. La encuesta permite forzar VARIOS avances sobre la MISMA
// clave de conversación y observar el efecto del dedupe frente a un reenvío.
func newDedupeSurveyRuntime(t *testing.T, ded runtime.IngestDeduper) (*runtime.Runtime, *fakeSender, *store.MemoryRepository, *contact.MemoryResolver) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, surveyFlow()); err != nil {
		t.Fatalf("sembrar definición survey: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, newSurveyEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(),
		runtime.WithIngestDeduper(ded))
	return rt, sender, repo, contacts
}

// TestIngestDedupe_ReplayIntercalado_EfectoUnaVez: el aporte específico del dedupe
// PERSISTENTE frente a la idempotencia CONSECUTIVA (last_wa_message_id). Se avanza
// la encuesta hasta el cierre (a1→a2) y luego se REENVÍA a1 INTERCALADO (tras a2):
// el last_wa_message_id ya es a2, así que la guarda consecutiva NO lo cortaría; el
// deduper persistente sí ⇒ ningún envío extra ni error.
func TestIngestDedupe_ReplayIntercalado_EfectoUnaVez(t *testing.T) {
	rt, sender, repo, contacts := newDedupeSurveyRuntime(t, ingest.NewMemoryDeduper())
	ctx := context.Background()

	// Sembrado directo (no Start): desde Plan 054 · T2.3 la encuesta es SIEMPRE
	// durable y Start() ya no puede abrirla — ver seedSurveyOpen.
	seedSurveyOpen(t, repo, contacts)
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.a1")); err != nil {
		t.Fatalf("HandleIncoming a1: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.a2")); err != nil {
		t.Fatalf("HandleIncoming a2: %v", err)
	}
	// q2 (a1) + cierre (a2) = 2 envíos (sin el q1 de Start, que ya no aplica a un
	// flujo durable: el fixture se siembra directo en q1).
	if got := sender.count(); got != 2 {
		t.Fatalf("tras completar la encuesta se esperaban 2 envíos, hubo %d", got)
	}

	// REENVÍO INTERCALADO de a1: duplicado del outbox llegado fuera de orden.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.a1")); err != nil {
		t.Fatalf("HandleIncoming a1 (reenvío): %v", err)
	}
	if got := sender.count(); got != 2 {
		t.Fatalf("el reenvío intercalado de a1 NO debió producir efectos; envíos=%d (quiero 2)", got)
	}
}

// TestIngestDedupe_ShortCircuit_NoTocaElMotor: un deduper que marca el frame como
// YA VISTO corta el entrante ANTES de tocar el motor (sin avanzar el estado ni
// auto-responder), y se lo invoca con la clave (session_id, wa_message_id).
func TestIngestDedupe_ShortCircuit_NoTocaElMotor(t *testing.T) {
	ded := &stubDeduper{seen: true}
	rt, sender, repo, contacts := newDedupeSurveyRuntime(t, ded)
	ctx := context.Background()

	// Sembrado directo (no Start): ver seedSurveyOpen / Plan 054 · T2.3.
	cid := seedSurveyOpen(t, repo, contacts)
	stBefore := loadState(t, repo, cid)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.dup")); err != nil {
		t.Fatalf("HandleIncoming duplicado: %v", err)
	}
	// El entrante "duplicado" no avanzó nada: cero envíos (sin Start no hay q1 que
	// contar, y el dedupe corta el avance ANTES de tocar el motor).
	if got := sender.count(); got != 0 {
		t.Fatalf("un entrante ya-visto no debió auto-responder; envíos=%d (quiero 0)", got)
	}
	stAfter := loadState(t, repo, cid)
	if stAfter.CurrentNode != stBefore.CurrentNode {
		t.Fatalf("el estado avanzó pese al dedupe: %q → %q", stBefore.CurrentNode, stAfter.CurrentNode)
	}
	if ded.calls != 1 || len(ded.keys) != 1 || !strings.HasSuffix(ded.keys[0], "|wamid.dup") {
		t.Fatalf("el deduper debió consultarse una vez con (session, wamid.dup); calls=%d keys=%v", ded.calls, ded.keys)
	}
}

// TestIngestDedupe_WaMessageIDVacio_NoDeduplica: un entrante sin wa_message_id
// (evento sintético) NO se deduplica —cae al camino de siempre—: el deduper ni se
// consulta y la conversación avanza normal.
func TestIngestDedupe_WaMessageIDVacio_NoDeduplica(t *testing.T) {
	ded := &stubDeduper{seen: true} // si se consultara, cortaría el avance
	rt, sender, repo, contacts := newDedupeSurveyRuntime(t, ded)
	ctx := context.Background()

	// Sembrado directo (no Start): ver seedSurveyOpen / Plan 054 · T2.3.
	seedSurveyOpen(t, repo, contacts)
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "")); err != nil {
		t.Fatalf("HandleIncoming sin wamid: %v", err)
	}
	if ded.calls != 0 {
		t.Fatalf("con wa_message_id vacío el deduper NO debió consultarse; calls=%d", ded.calls)
	}
	// Avanzó a q2 (1 envío): el dedupe no bloqueó nada.
	if got := sender.count(); got != 1 {
		t.Fatalf("sin wamid la conversación debió avanzar (1 envío), hubo %d", got)
	}
}

// TestIngestDedupe_FailOpen_ContinuaAlFallar: si el deduper falla, la ingesta NO
// pierde el entrante (fail-open): se continúa el procesamiento normal.
func TestIngestDedupe_FailOpen_ContinuaAlFallar(t *testing.T) {
	ded := &stubDeduper{err: errors.New("bd caída")}
	rt, sender, repo, contacts := newDedupeSurveyRuntime(t, ded)
	ctx := context.Background()

	// Sembrado directo (no Start): ver seedSurveyOpen / Plan 054 · T2.3.
	seedSurveyOpen(t, repo, contacts)
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.z1")); err != nil {
		t.Fatalf("HandleIncoming con deduper caído: %v", err)
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("fail-open: el entrante debió procesarse pese al fallo del deduper (1 envío), hubo %d", got)
	}
}
