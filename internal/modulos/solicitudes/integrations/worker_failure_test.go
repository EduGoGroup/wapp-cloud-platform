package integrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// errSource es el fallo de una fuente en los tests de este fichero.
var errSource = errors.New("fuente caída")

// failureCase es una forma de fallar una entrega: cómo se estropea el banco y qué motivo tiene que
// quedar en last_error.
type failureCase struct {
	name string
	// sabotage estropea el banco (que parte con el tenant integrado contra un puente que contesta 200).
	sabotage func(t *testing.T, rig *workerRig, crm *bridge)
	// template es la plantilla encolada; vacía = la de prueba.
	template string
	// want es el motivo; si prefix es true, basta con que empiece así (lo demás lo pone la
	// biblioteca estándar).
	want   string
	prefix bool
	// posts son los POST que el puente tiene que haber recibido.
	posts int
}

// reconfigure deja la integración del tenant del banco como diga change.
func reconfigure(change func(*integrations.TenantIntegration)) func(*testing.T, *workerRig, *bridge) {
	return func(t *testing.T, rig *workerRig, crm *bridge) {
		t.Helper()
		cfg := integrations.TenantIntegration{
			TenantID: rigTenant, CatalogAdapter: "local", EventsAdapter: "webhook", EndpointURL: crm.srv.URL, Enabled: true,
		}
		change(&cfg)
		if err := rig.mem.UpsertTenantIntegration(context.Background(), cfg, ""); err != nil {
			t.Fatalf("reconfigurar la integración: %v", err)
		}
	}
}

// failureCases son los motivos de fallo del contrato de Run, uno por fila, con su texto literal.
func failureCases() []failureCase {
	del := func(t *testing.T, rig *workerRig, _ *bridge) {
		t.Helper()
		if err := rig.mem.DeleteTenantIntegration(context.Background(), rigTenant); err != nil {
			t.Fatalf("borrar la integración: %v", err)
		}
	}
	noSecret := func(t *testing.T, rig *workerRig, crm *bridge) {
		del(t, rig, crm)
		reconfigure(func(*integrations.TenantIntegration) {})(t, rig, crm)
	}
	return []failureCase{
		{name: "template is not a JSON object", template: `["no","soy","un","objeto"]`,
			want: "plantilla del payload no es JSON válido: ", prefix: true},
		{name: "template is a JSON null", template: `null`,
			want: "plantilla del payload no es un objeto JSON"},
		{name: "buyer data reader fails", sabotage: func(_ *testing.T, rig *workerRig, _ *bridge) { rig.buyer.err = errSource },
			want: "leer buyer_data de " + rigIntake + ": fuente caída"},
		{name: "customer note reader fails", sabotage: func(_ *testing.T, rig *workerRig, _ *bridge) { rig.notes.err = errSource },
			want: "leer la indicación del cliente de " + rigIntake + ": fuente caída"},
		{name: "tenant variables reader fails", sabotage: func(_ *testing.T, rig *workerRig, _ *bridge) { rig.vars.err = errSource },
			want: "leer tenant_variables de " + rigTenant + ": fuente caída"},
		{name: "integration lookup fails", sabotage: func(_ *testing.T, rig *workerRig, _ *bridge) { rig.store.fail(opGetTenant, errSource) },
			want: "leer integración de " + rigTenant + ": fuente caída"},
		{name: "integration deleted after enqueueing", sabotage: del,
			want: "tenant " + rigTenant + " ya no tiene integración webhook habilitada"},
		{name: "integration switched off", sabotage: reconfigure(func(c *integrations.TenantIntegration) { c.Enabled = false }),
			want: "tenant " + rigTenant + " ya no tiene integración webhook habilitada"},
		{name: "events adapter no longer webhook", sabotage: reconfigure(func(c *integrations.TenantIntegration) { c.EventsAdapter = "local" }),
			want: "tenant " + rigTenant + " ya no tiene integración webhook habilitada"},
		{name: "integration without endpoint", sabotage: reconfigure(func(c *integrations.TenantIntegration) { c.EndpointURL = "" }),
			want: "tenant " + rigTenant + " ya no tiene integración webhook habilitada"},
		{name: "secret lookup fails", sabotage: func(_ *testing.T, rig *workerRig, _ *bridge) { rig.store.fail(opGetSecret, errSource) },
			want: "leer secreto de " + rigTenant + ": fuente caída"},
		{name: "integration without secret", sabotage: noSecret,
			want: "tenant " + rigTenant + " no tiene secreto de firma configurado"},
		{name: "endpoint is not a URL", sabotage: reconfigure(func(c *integrations.TenantIntegration) { c.EndpointURL = "http://puente con espacios/hook" }),
			want: "construir request: ", prefix: true},
		{name: "bridge is unreachable", sabotage: func(_ *testing.T, _ *workerRig, crm *bridge) { crm.srv.Close() },
			want: "POST: ", prefix: true},
		{name: "bridge answers 500", sabotage: func(_ *testing.T, _ *workerRig, crm *bridge) { crm.statuses = []int{500} },
			want: "respuesta 500 del puente", posts: 1},
		{name: "bridge answers 404", sabotage: func(_ *testing.T, _ *workerRig, crm *bridge) { crm.statuses = []int{404} },
			want: "respuesta 404 del puente", posts: 1},
		{name: "bridge answers 300", sabotage: func(_ *testing.T, _ *workerRig, crm *bridge) { crm.statuses = []int{300} },
			want: "respuesta 300 del puente", posts: 1},
	}
}

// TestRun_Failure_ReasonsAndRetry: cada forma de fallar deja la fila en `pending` con UN intento
// contado, sin claim, reprogramada para más tarde y con su motivo LITERAL en last_error; cuenta
// «failed» y no deja ERROR (un intento fallido que se va a reintentar no es una avería). Los que
// fallan antes del POST no llegan a llamar al puente.
func TestRun_Failure_ReasonsAndRetry(t *testing.T) {
	for _, c := range failureCases() {
		t.Run(c.name, func(t *testing.T) {
			rig := newWorkerRig()
			crm := newBridge(t)
			rig.integrate(t, rigTenant, crm.srv.URL)
			if c.sabotage != nil {
				c.sabotage(t, rig, crm)
			}
			template := templatePayload(t, rigIntake, rigTenant)
			if c.template != "" {
				template = json.RawMessage(c.template)
			}
			id := rig.enqueue(t, rigTenant, template)

			rig.runPolls(t, 1)

			row := rig.row(t, id)
			if row.Status != integrations.StatusPending || row.Attempts != 1 || !row.ClaimedAt.IsZero() {
				t.Errorf("fila = (status=%q, attempts=%d, claimed_at=%v), quería (pending, 1, sin claim)", row.Status, row.Attempts, row.ClaimedAt)
			}
			if !row.NextAttemptAt.After(rig.clock.Now()) {
				t.Errorf("next_attempt_at = %v, quería un instante posterior al reloj (%v)", row.NextAttemptAt, rig.clock.Now())
			}
			requireReason(t, row.LastError, c.want, c.prefix)
			for _, leak := range []string{rigSecret, "v1="} {
				if strings.Contains(row.LastError, leak) {
					t.Errorf("FUGA: el motivo lleva %q: %s", leak, row.LastError)
				}
			}
			if got := rig.recorded(); !slices.Equal(got, []string{"failed"}) {
				t.Errorf("métrica = %v, quería [failed]", got)
			}
			if got := len(crm.received()); got != c.posts {
				t.Errorf("el puente recibió %d POST, quería %d", got, c.posts)
			}
			rig.log.requireNoErrors(t)
		})
	}
}

// requireReason afirma el motivo: byte a byte, o por su prefijo si lo demás es de la biblioteca.
func requireReason(t *testing.T, got, want string, prefix bool) {
	t.Helper()
	if prefix {
		if !strings.HasPrefix(got, want) || got == want {
			t.Errorf("motivo = %q, quería el prefijo %q seguido de la causa", got, want)
		}
		return
	}
	if got != want {
		t.Errorf("motivo =\n%s\nquería, byte a byte:\n%s", got, want)
	}
}

// TestRun_Failure_FirstFailingStepWins: las lecturas van en orden —buyer_data, la nota, las
// variables y el destino— y el motivo que queda es el del PRIMER paso que falla.
func TestRun_Failure_FirstFailingStepWins(t *testing.T) {
	steps := []struct {
		name string
		heal func(rig *workerRig)
		want string
	}{
		{"buyer data first", func(*workerRig) {}, "leer buyer_data de "},
		{"then the note", func(rig *workerRig) { rig.buyer.err = nil }, "leer la indicación del cliente de "},
		{"then the variables", func(rig *workerRig) { rig.buyer.err, rig.notes.err = nil, nil }, "leer tenant_variables de "},
		{"then the destination", func(rig *workerRig) { rig.buyer.err, rig.notes.err, rig.vars.err = nil, nil, nil }, "leer integración de "},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			rig := newWorkerRig()
			rig.buyer.err, rig.notes.err, rig.vars.err = errSource, errSource, errSource
			rig.store.fail(opGetTenant, errSource)
			step.heal(rig)
			id := rig.enqueueTemplate(t)

			rig.runPolls(t, 1)

			if got := rig.row(t, id).LastError; !strings.HasPrefix(got, step.want) {
				t.Errorf("motivo = %q, quería el del primer paso que falla (%q…)", got, step.want)
			}
		})
	}
}

// TestRun_Failure_HonoursTheTimeout: el plazo de cada entrega es Timeout: un puente que no
// contesta falla el intento por «POST: » en vez de dejar al worker colgado.
func TestRun_Failure_HonoursTheTimeout(t *testing.T) {
	rig := newWorkerRig()
	rig.cfg.Timeout = 30 * time.Millisecond
	crm := newBridge(t)
	crm.onPost = func(r *http.Request) { <-r.Context().Done() } // no contesta hasta que el cliente cuelga
	rig.integrate(t, rigTenant, crm.srv.URL)
	id := rig.enqueueTemplate(t)

	rig.runPolls(t, 1)

	row := rig.row(t, id)
	if row.Status != integrations.StatusPending || !strings.HasPrefix(row.LastError, "POST: ") {
		t.Errorf("fila = (status=%q, last_error=%q), quería pending con un motivo «POST: …»", row.Status, row.LastError)
	}
	if got := rig.recorded(); !slices.Equal(got, []string{"failed"}) {
		t.Errorf("métrica = %v, quería [failed]", got)
	}
}

// TestRun_Failure_RetriesUntilTheBridgeAnswers: dos fallos y una entrega: la fila acaba
// `delivered` con los DOS intentos fallidos contados (entregar no cuenta), el puente recibe tres
// POST y la métrica cuenta failed, failed, delivered. Entre intentos la fila no sale hasta que
// vence su reprogramación.
func TestRun_Failure_RetriesUntilTheBridgeAnswers(t *testing.T) {
	rig := newWorkerRig()
	crm := newBridge(t, http.StatusInternalServerError, http.StatusBadGateway, http.StatusOK)
	rig.integrate(t, rigTenant, crm.srv.URL)
	id := rig.enqueueTemplate(t)

	stop := rig.start(t)
	rig.store.waitPolls(t, 1)
	// Sin mover el reloj, los polls siguientes no la reclaman: aún no ha vencido.
	rig.store.waitFreshPoll(t)
	if got := len(crm.received()); got != 1 {
		t.Fatalf("antes de vencer la reprogramación el puente lleva %d POST, quería 1", got)
	}
	rig.clock.Advance(2 * time.Hour)
	rig.store.waitFreshPoll(t)
	rig.clock.Advance(2 * time.Hour)
	rig.store.waitFreshPoll(t)
	stop()

	row := rig.row(t, id)
	if row.Status != integrations.StatusDelivered || row.Attempts != 2 || row.LastError != "respuesta 502 del puente" {
		t.Errorf("fila = (status=%q, attempts=%d, last_error=%q), quería (delivered, 2, el del último fallo)",
			row.Status, row.Attempts, row.LastError)
	}
	if got := len(crm.received()); got != 3 {
		t.Errorf("el puente recibió %d POST, quería 3", got)
	}
	if got := rig.recorded(); !slices.Equal(got, []string{"failed", "failed", "delivered"}) {
		t.Errorf("métrica = %v, quería [failed failed delivered]", got)
	}
}

// TestRun_Failure_ExhaustedAttempts_Dead: el intento que falla es Attempts+1; cuando llega a
// MaxAttempts la fila queda `dead` —visible, con su motivo y con su payload— y se cuenta «dead»,
// no «failed». Queda un ERROR con el id, el tenant, los intentos y el motivo (R6.4.d).
func TestRun_Failure_ExhaustedAttempts_Dead(t *testing.T) {
	cases := []struct {
		name        string
		maxAttempts int
		burned      int
		wantStatus  string
		wantMetric  string
	}{
		{"one attempt allowed, first failure kills", 1, 0, integrations.StatusDead, "dead"},
		{"second of two kills", 2, 1, integrations.StatusDead, "dead"},
		{"first of two retries", 2, 0, integrations.StatusPending, "failed"},
		{"default is ten: the ninth retries", 0, 8, integrations.StatusPending, "failed"},
		{"default is ten: the tenth kills", 0, 9, integrations.StatusDead, "dead"},
		{"already past the cap still dies", 3, 7, integrations.StatusDead, "dead"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newWorkerRig()
			rig.cfg.MaxAttempts = c.maxAttempts
			crm := newBridge(t, http.StatusServiceUnavailable)
			rig.integrate(t, rigTenant, crm.srv.URL)
			id := rig.enqueueTemplate(t)
			rig.burnAttempts(t, id, c.burned)
			queued := string(rig.row(t, id).Payload)

			rig.runPolls(t, 1)

			row := rig.row(t, id)
			if row.Status != c.wantStatus || row.Attempts != c.burned+1 || row.LastError != "respuesta 503 del puente" {
				t.Errorf("fila = (status=%q, attempts=%d, last_error=%q), quería (%s, %d, respuesta 503 del puente)",
					row.Status, row.Attempts, row.LastError, c.wantStatus, c.burned+1)
			}
			if string(row.Payload) != queued {
				t.Errorf("payload = %s, quería el encolado: es lo único que dice qué no se entregó", row.Payload)
			}
			if got := rig.recorded(); !slices.Equal(got, []string{c.wantMetric}) {
				t.Errorf("métrica = %v, quería [%s]", got, c.wantMetric)
			}
			if c.wantStatus != integrations.StatusDead {
				rig.log.requireNoErrors(t)
				return
			}
			line := rig.log.line(t, "ERROR", "webhook worker: entrega DEAD (reintentos agotados)")
			requireKeys(t, line, "outbox_id=", "tenant="+rigTenant, "attempts=", "reason=respuesta 503 del puente")
		})
	}
}

// TestRun_Failure_DeadAlsoBeforeThePOST: un fallo anterior al POST agota los reintentos por el
// mismo camino que un POST fallido: un solo lugar decide.
func TestRun_Failure_DeadAlsoBeforeThePOST(t *testing.T) {
	rig := newWorkerRig()
	rig.cfg.MaxAttempts = 1
	id := rig.enqueueTemplate(t) // el tenant no tiene integración

	rig.runPolls(t, 1)

	row := rig.row(t, id)
	if row.Status != integrations.StatusDead || row.LastError != "tenant "+rigTenant+" ya no tiene integración webhook habilitada" {
		t.Errorf("fila = (status=%q, last_error=%q), quería dead con el motivo de la integración", row.Status, row.LastError)
	}
	if got := rig.recorded(); !slices.Equal(got, []string{"dead"}) {
		t.Errorf("métrica = %v, quería [dead]", got)
	}
}

// backoffBase es D-042.4: 30s × 2^(intento−1), con tope de una hora.
func backoffBase(attempt int) time.Duration {
	return min(30*time.Second<<min(attempt-1, 12), time.Hour)
}

// requireBackoff afirma que la espera cae en la ventana del jitter de ±20 % de ese intento.
func requireBackoff(t *testing.T, attempt int, delay time.Duration) {
	t.Helper()
	base := backoffBase(attempt)
	low, high := time.Duration(float64(base)*0.8), time.Duration(float64(base)*1.2)
	if delay < low || delay >= high {
		t.Errorf("intento %d: reprogramado a %v del reloj, quería entre %v y %v (base %v ±20 %%)", attempt, delay, low, high, base)
	}
}

// TestRun_Failure_BackoffGrowsAndIsCapped: la reprogramación es reloj + 30s × 2^(intento−1), con
// tope de una hora y un jitter de ±20 %: 30s, 1m, 2m, 4m… y a partir del octavo intento, la hora.
// El «reloj» es el inyectado, no el de pared.
func TestRun_Failure_BackoffGrowsAndIsCapped(t *testing.T) {
	rig := newWorkerRig()
	rig.cfg.MaxAttempts = 1000
	id := rig.enqueueTemplate(t) // sin integración: falla sin llamar a nadie

	stop := rig.start(t)
	const attempts = 10
	for attempt := 1; attempt <= attempts; attempt++ {
		now := rig.clock.Now()
		rig.store.waitFreshPoll(t)
		marks := rig.store.marked()
		if len(marks) != attempt {
			t.Fatalf("tras el intento %d hay %d cierres, quería %d", attempt, len(marks), attempt)
		}
		last := marks[attempt-1]
		if last.op != opFailed || last.claim.ID != id || last.claim.Attempts != attempt-1 {
			t.Fatalf("cierre %d = %+v, quería un MarkWebhookFailed de la entrega con %d intentos previos", attempt, last, attempt-1)
		}
		requireBackoff(t, attempt, last.next.Sub(now))
		rig.clock.Advance(3 * time.Hour)
	}
	stop()
}

// TestRun_Failure_BackoffNeverExceedsTheCap: por alto que sea el intento, la espera no pasa de la
// hora más su jitter (y no se desborda).
func TestRun_Failure_BackoffNeverExceedsTheCap(t *testing.T) {
	rig := newWorkerRig()
	rig.cfg.MaxAttempts = 1000
	id := rig.enqueueTemplate(t)
	rig.burnAttempts(t, id, 79)
	now := rig.clock.Now()

	rig.runPolls(t, 1)

	marks := rig.store.marked()
	if len(marks) != 1 || marks[0].op != opFailed {
		t.Fatalf("cierres = %+v, quería un MarkWebhookFailed", marks)
	}
	requireBackoff(t, 80, marks[0].next.Sub(now))
}

// TestRun_Failure_JitterReallySpreads blinda la corrección del 2026-08-08: el jitter salía del
// reloj y, con resolución de 1 µs (darwin/arm64), colapsaba a DOS valores —justo la tormenta de
// reintentos que el jitter existe para evitar—. El umbral es flojo a propósito (50 valores
// distintos de 400 posibles en 1000 muestras, donde lo esperable son ~370): no es frágil y aun así
// falla de lleno si el jitter vuelve a depender del reloj.
func TestRun_Failure_JitterReallySpreads(t *testing.T) {
	const samples, minDistinct = 1000, 50
	rig := newWorkerRig()
	rig.cfg.BatchSize = samples
	for range samples {
		rig.enqueueTemplate(t) // sin integración: fallan todas en el mismo poll
	}
	now := rig.clock.Now()

	rig.runPolls(t, 1)

	marks := rig.store.marked()
	if len(marks) != samples {
		t.Fatalf("cierres = %d, quería %d", len(marks), samples)
	}
	distinct := map[time.Duration]struct{}{}
	for _, m := range marks {
		delay := m.next.Sub(now)
		requireBackoff(t, 1, delay)
		distinct[delay] = struct{}{}
	}
	if len(distinct) < minDistinct {
		t.Errorf("el backoff del primer intento dio solo %d valores distintos en %d fallos (esperaba al menos %d): "+
			"el jitter no dispersa, mira si volvió a depender del reloj", len(distinct), samples, minDistinct)
	}
}
