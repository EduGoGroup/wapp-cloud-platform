//go:build pendiente

package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// La telemetría de la bandeja vista desde su cableado (WithMetrics, WithMetricsClock): qué se
// publica en Approve y en RequestInfo, con qué reloj, y qué pasa cuando no hay publicador o
// falla. Los dobles y la siembra están en service_test.go; los payloads esperados salen del
// fichero viejo (internal/intakes/metricas.go @ 64c181a) sobre esta misma escena.

// svcLogSpy retiene lo emitido como "NIVEL mensaje args".
type svcLogSpy struct{ lines []string }

func (l *svcLogSpy) record(level, msg string, args ...any) {
	l.lines = append(l.lines, level+" "+msg+" "+fmt.Sprint(args...))
}

func (l *svcLogSpy) Debug(msg string, args ...any) { l.record("DEBUG", msg, args...) }
func (l *svcLogSpy) Info(msg string, args ...any)  { l.record("INFO", msg, args...) }
func (l *svcLogSpy) Warn(msg string, args ...any)  { l.record("WARN", msg, args...) }
func (l *svcLogSpy) Error(msg string, args ...any) { l.record("ERROR", msg, args...) }
func (l *svcLogSpy) With(...any) logger.Logger     { return l }

// count dice cuántas líneas de ese nivel hay.
func (l *svcLogSpy) count(level string) int {
	n := 0
	for _, line := range l.lines {
		if strings.HasPrefix(line, level+" ") {
			n++
		}
	}
	return n
}

var (
	// svcDraftAt es cuándo el pipeline dejó el borrador; svcDecisionAt, cuándo aprueba el dueño:
	// 31 min 40 s después, o sea 1 900 000 ms (el ejemplo de design §10).
	svcDraftAt    = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	svcDecisionAt = time.Date(2026, 8, 1, 12, 31, 40, 0, time.UTC)
)

// svcFixedClock devuelve un reloj clavado en `at`.
func svcFixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

// metricsScene es la solicitud por aprobar con un publicador y un log espiados. `draftAt` cero
// la deja SIN borrador del pipeline (una solicitud nacida del carrito).
type metricsScene struct {
	svc     *Service
	store   *MemoryStore
	metrics *svcMetricsSpy
	log     *svcLogSpy
}

func newMetricsScene(t *testing.T, draftAt time.Time, opts ...Option) *metricsScene {
	t.Helper()
	sc := &metricsScene{store: svcSeedStore(t, StatusPendingApproval), metrics: &svcMetricsSpy{}, log: &svcLogSpy{}}
	if !draftAt.IsZero() {
		sc.store.SetClock(svcFixedClock(draftAt))
		svcSeedRevision(t, sc.store, RevisionKindInterpreted, svcDraftPayload)
	}
	base := []Option{WithQuoteSender(&svcQuoteSpy{}), WithMetrics(sc.metrics, sc.log), WithMetricsClock(svcFixedClock(svcDecisionAt))}
	sc.svc = NewService(sc.store, append(base, opts...)...)
	return sc
}

// wantOne exige UNA medición, del tenant y el contacto OPACO de la solicitud, con ese nombre y
// exactamente ese payload (sin una clave de más).
func (sc *metricsScene) wantOne(t *testing.T, name, payload string) {
	t.Helper()
	if len(sc.metrics.calls) != 1 {
		t.Fatalf("mediciones = %d, quería exactamente 1", len(sc.metrics.calls))
	}
	got := sc.metrics.calls[0]
	if got.tenantID != svcTenantA || got.contactID != "contacto-opaco-1" || got.name != name {
		t.Errorf("medición = (%q, %q, %q), quería (tenant A, contacto opaco, %q)", got.tenantID, got.contactID, got.name, name)
	}
	raw, err := json.Marshal(got.payload)
	if err != nil {
		t.Fatalf("el payload no es serializable: %v", err)
	}
	if string(raw) != payload {
		t.Errorf("payload = %s, quería %s", raw, payload)
	}
}

// TestWithMetrics_ApprovePublishesTheRevisionAndTheElapsedTime: `rev` es la revisión que escribe
// la aprobación (la 2: había un borrador) y el tiempo corre desde el borrador hasta el reloj
// inyectado. El nombre es contrato de wire.
func TestWithMetrics_ApprovePublishesTheRevisionAndTheElapsedTime(t *testing.T) {
	t.Parallel()
	sc := newMetricsScene(t, svcDraftAt)

	if _, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza"); err != nil {
		t.Fatalf("Approve devolvió el error %v", err)
	}
	sc.wantOne(t, "intake_approved", `{"elapsed_from_draft_ms":1900000,"rev":2}`)
	if sc.log.count("WARN") != 0 {
		t.Errorf("el camino normal dejó avisos en el log: %v", sc.log.lines)
	}
}

// svcReversedStore devuelve el histórico AL REVÉS: lo que se mide no puede depender del orden de
// la lista.
type svcReversedStore struct{ *MemoryStore }

func (s svcReversedStore) Get(ctx context.Context, tenantID, intakeID string) (Detail, error) {
	d, err := s.MemoryStore.Get(ctx, tenantID, intakeID)
	d.Revisions = slices.Clone(d.Revisions)
	slices.Reverse(d.Revisions)
	return d, err
}

// TestWithMetrics_ElapsedRunsFromTheFirstDraft: un re-análisis escribe una segunda revisión
// `interpreted` más tarde, y medir desde ella diría que el dueño aprobó en minutos un pedido que
// llevaba horas en su bandeja. Cuenta la de número MÁS BAJO, venga la lista en el orden que venga.
func TestWithMetrics_ElapsedRunsFromTheFirstDraft(t *testing.T) {
	t.Parallel()
	for _, reversed := range []bool{false, true} {
		sc := newMetricsScene(t, svcDraftAt)
		sc.store.SetClock(svcFixedClock(svcDraftAt.Add(10 * time.Minute)))
		svcSeedRevision(t, sc.store, RevisionKindCorrected,
			`{"version":1,"total":21500,"items":[{"sku":"torta-v1","label":"Torta","qty":1,"unit_price":18000}]}`)
		sc.store.SetClock(svcFixedClock(svcDraftAt.Add(20 * time.Minute)))
		svcSeedRevision(t, sc.store, RevisionKindInterpreted, svcDraftPayload)
		if reversed {
			sc.svc = NewService(svcReversedStore{sc.store},
				WithQuoteSender(&svcQuoteSpy{}), WithMetrics(sc.metrics, sc.log), WithMetricsClock(svcFixedClock(svcDecisionAt)))
		}

		if _, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza"); err != nil {
			t.Fatalf("Approve (reversed=%v) devolvió el error %v", reversed, err)
		}
		sc.wantOne(t, EventApproved, `{"elapsed_from_draft_ms":1900000,"rev":4}`)
	}
}

// TestWithMetrics_IntakeBornFromTheCartPublishesZero: sin revisión `interpreted` no hay borrador
// que cronometrar y el único valor honesto es 0. Es el curso NORMAL de un pedido del carrito:
// se registra en Debug y no en Warn.
func TestWithMetrics_IntakeBornFromTheCartPublishesZero(t *testing.T) {
	t.Parallel()
	sc := newMetricsScene(t, time.Time{})

	if _, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza"); err != nil {
		t.Fatalf("Approve devolvió el error %v", err)
	}
	sc.wantOne(t, EventApproved, `{"elapsed_from_draft_ms":0,"rev":1}`)
	if sc.log.count("DEBUG") != 1 || sc.log.count("WARN") != 0 {
		t.Errorf("log = %v, quería una línea de Debug y ninguna de Warn", sc.log.lines)
	}
}

// TestWithMetrics_NegativeElapsedIsClampedAndWarned: un tiempo negativo en un panel no se lee
// como «relojes desajustados», se lee como un bug. Se publica 0 y se avisa.
func TestWithMetrics_NegativeElapsedIsClampedAndWarned(t *testing.T) {
	t.Parallel()
	sc := newMetricsScene(t, svcDecisionAt.Add(time.Second))

	if _, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza"); err != nil {
		t.Fatalf("Approve devolvió el error %v", err)
	}
	sc.wantOne(t, EventApproved, `{"elapsed_from_draft_ms":0,"rev":2}`)
	if sc.log.count("WARN") != 1 || !strings.Contains(strings.Join(sc.log.lines, "\n"), svcIntakeID) {
		t.Errorf("log = %v, quería un Warn con el intake_id", sc.log.lines)
	}
}

// TestWithMetrics_RequestInfoPublishesOneQuestion: el 1 es literal y es correcto — la puerta
// manda UNA pregunta. En el payload no viaja ni un trozo de ella.
func TestWithMetrics_RequestInfoPublishesOneQuestion(t *testing.T) {
	t.Parallel()
	sc := newMetricsScene(t, svcDraftAt)

	if _, err := sc.svc.RequestInfo(context.Background(), svcTenantA, svcIntakeID, "¿Para cuántas personas?"); err != nil {
		t.Fatalf("RequestInfo devolvió el error %v", err)
	}
	sc.wantOne(t, "intake_info_requested", `{"questions":1}`)
}

// TestWithMetrics_PublisherFailureIsWarnedNotReturned: BEST-EFFORT. La acción del dueño se aplica
// entera y el fallo queda en un Warn con el nombre del evento y el intake_id.
func TestWithMetrics_PublisherFailureIsWarnedNotReturned(t *testing.T) {
	t.Parallel()
	actions := []struct {
		name       string
		event      string
		wantStatus string
		run        func(*Service) (Detail, error)
	}{
		{name: "approve", event: EventApproved, wantStatus: StatusConfirmed, run: func(s *Service) (Detail, error) {
			return s.Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza")
		}},
		{name: "request info", event: EventInfoRequested, wantStatus: StatusNeedsInfo, run: func(s *Service) (Detail, error) {
			return s.RequestInfo(context.Background(), svcTenantA, svcIntakeID, "¿cuántas?")
		}},
	}
	for _, a := range actions {
		t.Run(a.name, func(t *testing.T) {
			t.Parallel()
			sc := newMetricsScene(t, svcDraftAt)
			sc.metrics.err = errors.New("flow_events caída")

			detail, err := a.run(sc.svc)
			if err != nil || detail.Status != a.wantStatus || svcStatusOf(t, sc.store) != a.wantStatus {
				t.Fatalf("la acción devolvió (%q, %v), quería (%q, nil): la telemetría no puede tumbarla", detail.Status, err, a.wantStatus)
			}
			if sc.log.count("WARN") != 1 || sc.log.count("ERROR") != 0 {
				t.Fatalf("log = %v, quería exactamente un Warn", sc.log.lines)
			}
			all := strings.Join(sc.log.lines, "\n")
			if !strings.Contains(all, svcIntakeID) || !strings.Contains(all, a.event) || !strings.Contains(all, "flow_events caída") {
				t.Errorf("el Warn no trae el intake_id, el nombre del evento y la causa: %s", all)
			}
		})
	}
}

// TestWithMetrics_NilLogKeepsTheDefaultLogger: un log nil no deja al servicio sin dónde avisar.
func TestWithMetrics_NilLogKeepsTheDefaultLogger(t *testing.T) {
	t.Parallel()
	failing := &svcMetricsSpy{err: errors.New("flow_events caída")}
	svc := NewService(svcSeedStore(t, StatusPendingApproval), WithQuoteSender(&svcQuoteSpy{}), WithMetrics(failing, nil))

	if _, err := svc.Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza"); err != nil {
		t.Fatalf("Approve devolvió el error %v", err)
	}
	if len(failing.calls) != 1 {
		t.Errorf("mediciones intentadas = %d, quería 1", len(failing.calls))
	}
}

// TestWithMetrics_WithoutPublisherNothingIsComputed es R-04: sin publicador el dominio funciona
// entero, no publica y NI SIQUIERA calcula el KPI — el log queda mudo, aunque la solicitud sea
// de las que dejarían una línea al medir (no tiene borrador).
func TestWithMetrics_WithoutPublisherNothingIsComputed(t *testing.T) {
	t.Parallel()
	log := &svcLogSpy{}
	cases := []struct {
		name string
		opts []Option
	}{
		{name: "option not wired", opts: nil},
		{name: "nil publisher with a log", opts: []Option{WithMetrics(nil, log)}},
	}
	for _, c := range cases {
		st := svcSeedStore(t, StatusPendingApproval)
		svc := NewService(st, append([]Option{WithQuoteSender(&svcQuoteSpy{})}, c.opts...)...)

		detail, err := svc.Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza")
		if err != nil || detail.Status != StatusConfirmed {
			t.Errorf("%s: Approve devolvió (%q, %v), quería (confirmed, nil)", c.name, detail.Status, err)
		}
	}
	if len(log.lines) != 0 {
		t.Errorf("sin publicador el log debería quedar mudo, y dice: %v", log.lines)
	}
}

// TestWithMetrics_RejectedActionsPublishNothing: lo que no se aplicó no se mide.
func TestWithMetrics_RejectedActionsPublishNothing(t *testing.T) {
	t.Parallel()
	sc := newMetricsScene(t, svcDraftAt)

	if _, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, "  "); err == nil {
		t.Error("aprobar sin texto debería rechazarse")
	}
	if _, err := sc.svc.RequestInfo(context.Background(), svcTenantB, svcIntakeID, "¿cuántas?"); err == nil {
		t.Error("preguntar por una solicitud ajena debería rechazarse")
	}
	if len(sc.metrics.calls) != 0 {
		t.Errorf("mediciones = %d, quería 0", len(sc.metrics.calls))
	}
}

// TestServiceClocks_AreIndependent: el reloj de la telemetría (WithMetricsClock) y el de Summary
// (WithClock, D-F6-5) son dos; ninguno mueve al otro, se pasen en el orden que se pasen, y un
// nil no borra ninguno.
func TestServiceClocks_AreIndependent(t *testing.T) {
	t.Parallel()
	summaryAt := time.Date(2026, 9, 15, 8, 30, 0, 0, time.UTC)
	orders := map[string][]Option{
		"metrics clock first": {WithMetricsClock(svcFixedClock(svcDecisionAt)), WithClock(svcFixedClock(summaryAt)), WithMetricsClock(nil), WithClock(nil)},
		"summary clock first": {WithClock(svcFixedClock(summaryAt)), WithMetricsClock(svcFixedClock(svcDecisionAt)), WithClock(nil), WithMetricsClock(nil)},
	}
	for name, opts := range orders {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sc := newMetricsScene(t, svcDraftAt, opts...)

			summary, err := sc.svc.Summary(context.Background(), svcTenantA, Filter{})
			if err != nil {
				t.Fatalf("Summary devolvió el error %v", err)
			}
			if !summary.GeneratedAt.Equal(summaryAt) {
				t.Errorf("GeneratedAt = %v, quería %v: el reloj de la telemetría no es el de Summary", summary.GeneratedAt, summaryAt)
			}
			if _, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza"); err != nil {
				t.Fatalf("Approve devolvió el error %v", err)
			}
			sc.wantOne(t, EventApproved, `{"elapsed_from_draft_ms":1900000,"rev":2}`)
		})
	}
}
