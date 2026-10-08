//go:build pendiente

package integrations_test

// worker.go se prueba en cuatro ficheros (E-13; el worker_test.go viejo medía 774 líneas):
//   - worker_test.go: las firmas, la configuración y el ciclo de Run (arranque, relojes, parada).
//   - worker_delivery_test.go: la entrega que sale bien (cuerpo, firma, cabeceras, cierre).
//   - worker_failure_test.go: los fallos, sus motivos literales, el backoff y `dead`.
//   - worker_claim_test.go: el claim perdido, la métrica, los errores del almacén y D-F6-7.
//
// Los dobles están en doubles_test.go y el banco de pruebas en worker_rig_test.go.

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
)

// Aserciones de compilación de lo que worker.go promete: el constructor con sus opciones, Run, el
// reloj inyectable, y que los tres puertos de lectura los satisfacen los adaptadores de verdad
// (los de intakes y tenantvars NUEVOS) y los dobles de este paquete.
var (
	_ func(integrations.Store, integrations.BuyerDataReader, integrations.CustomerNoteReader,
		integrations.TenantVariablesReader, logger.Logger, integrations.WorkerConfig, func(string),
		...integrations.WorkerOption) *integrations.Worker = integrations.NewWorker
	_ func(*integrations.Worker, context.Context)      = (*integrations.Worker).Run
	_ func(func() time.Time) integrations.WorkerOption = integrations.WithClock

	_ integrations.BuyerDataReader       = (*intakes.PostgresBuyerData)(nil)
	_ integrations.CustomerNoteReader    = (*intakes.Postgres)(nil)
	_ integrations.TenantVariablesReader = tenantvars.Store(nil)
	_ integrations.BuyerDataReader       = (*fakeBuyerData)(nil)
	_ integrations.CustomerNoteReader    = (*fakeCustomerNotes)(nil)
	_ integrations.TenantVariablesReader = (*fakeTenantVars)(nil)
)

// TestNewWorker_DoesNotTouchTheStore: construir el worker no llama al almacén ni arranca nada.
func TestNewWorker_DoesNotTouchTheStore(t *testing.T) {
	rig := newWorkerRig()
	if rig.build() == nil {
		t.Fatal("NewWorker devolvió nil")
	}
	if calls := rig.store.calls(); len(calls) != 0 {
		t.Errorf("construir el worker llamó al almacén: %v", calls)
	}
	if lines := rig.log.at("INFO"); len(lines) != 0 {
		t.Errorf("construir el worker dejó líneas en el log: %v", lines)
	}
}

// TestRun_StartsWithARescueAndThenAPoll: al arrancar, y sin esperar a ningún reloj (los dos están a
// una hora), Run rescata y después hace su primer poll, en ese orden.
func TestRun_StartsWithARescueAndThenAPoll(t *testing.T) {
	rig := newWorkerRig()
	rig.cfg = integrations.WorkerConfig{PollInterval: time.Hour, ClaimLease: time.Hour}
	stop := rig.start(t)
	rig.store.waitCalls(t, opClaim, 1)
	stop()

	if got, want := rig.store.calls(), []string{opRecover, opClaim}; !slices.Equal(got, want) {
		t.Errorf("llamadas al almacén al arrancar = %v, quería %v", got, want)
	}
	if lines := rig.log.at("WARN"); len(lines) != 0 {
		t.Errorf("un rescate que no recupera nada no dice nada, y el log tiene: %v", lines)
	}
}

// TestRun_ConfigDefaults: cada campo <= 0 de la configuración toma su default y un valor positivo
// se respeta. Se ve en lo que el worker le pide al almacén: el tamaño de lote del reclamo y el
// lease del rescate (derivado: 3× Timeout con piso de un minuto, calculado con el Timeout ya
// completado).
func TestRun_ConfigDefaults(t *testing.T) {
	cases := []struct {
		name      string
		cfg       integrations.WorkerConfig
		wantBatch int
		wantLease time.Duration
	}{
		{"everything zero", integrations.WorkerConfig{}, 20, time.Minute},
		{"negative values", integrations.WorkerConfig{BatchSize: -3, ClaimLease: -time.Second, Timeout: -time.Second}, 20, time.Minute},
		{"short timeout keeps the one minute floor", integrations.WorkerConfig{Timeout: time.Second}, 20, time.Minute},
		{"twenty seconds is exactly the floor", integrations.WorkerConfig{Timeout: 20 * time.Second}, 20, time.Minute},
		{"long timeout derives three times", integrations.WorkerConfig{Timeout: 2 * time.Minute}, 20, 6 * time.Minute},
		{"explicit lease and batch are honoured", integrations.WorkerConfig{BatchSize: 7, ClaimLease: 5 * time.Second, Timeout: time.Minute}, 7, 5 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newWorkerRig()
			rig.cfg = c.cfg
			rig.cfg.PollInterval = time.Hour
			stop := rig.start(t)
			rig.store.waitCalls(t, opClaim, 1)
			stop()

			if got := rig.store.seenLimits(); len(got) == 0 || got[0] != c.wantBatch {
				t.Errorf("tamaño de lote pedido = %v, quería %d", got, c.wantBatch)
			}
			if got := rig.store.seenLeases(); len(got) == 0 || got[0] != c.wantLease {
				t.Errorf("lease del rescate = %v, quería %v", got, c.wantLease)
			}
		})
	}
}

// TestRun_DefaultLeaseAlwaysExceedsTheTimeout es la invariante que impide reintroducir el bug por
// configuración: un lease por debajo del timeout HTTP rescataría entregas que siguen
// legítimamente en vuelo.
func TestRun_DefaultLeaseAlwaysExceedsTheTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Second, 10 * time.Second, 59 * time.Second, 2 * time.Minute, time.Hour} {
		rig := newWorkerRig()
		rig.cfg = integrations.WorkerConfig{PollInterval: time.Hour, Timeout: timeout}
		stop := rig.start(t)
		rig.store.waitCalls(t, opClaim, 1)
		stop()

		effective := timeout
		if effective <= 0 {
			effective = 10 * time.Second
		}
		if got := rig.store.seenLeases(); len(got) == 0 || got[0] <= effective {
			t.Errorf("Timeout=%v ⇒ lease=%v: por debajo del timeout se rescatarían entregas VIVAS", timeout, got)
		}
	}
}

// TestRun_PollsOnEveryTick: tras el primer poll, hay uno cada PollInterval.
func TestRun_PollsOnEveryTick(t *testing.T) {
	rig := newWorkerRig()
	rig.cfg = integrations.WorkerConfig{PollInterval: time.Millisecond, ClaimLease: time.Hour}
	stop := rig.start(t)
	rig.store.waitCalls(t, opClaim, 4)
	stop()
	if n := rig.store.count(opRecover); n != 1 {
		t.Errorf("con el lease a una hora hubo %d rescates, quería solo el del arranque", n)
	}
}

// TestRun_RescuesOnEveryLease: el rescate no es solo del arranque: se repite cada ClaimLease, con
// ese mismo lease, para que una réplica viva recoja lo de la que murió.
func TestRun_RescuesOnEveryLease(t *testing.T) {
	rig := newWorkerRig()
	rig.cfg = integrations.WorkerConfig{PollInterval: time.Hour, ClaimLease: 2 * time.Millisecond}
	stop := rig.start(t)
	rig.store.waitCalls(t, opRecover, 4)
	stop()

	for i, lease := range rig.store.seenLeases() {
		if lease != 2*time.Millisecond {
			t.Errorf("rescate %d con lease %v, quería el configurado (2ms)", i, lease)
		}
	}
	if n := rig.store.count(opClaim); n != 1 {
		t.Errorf("con el poll a una hora hubo %d reclamos, quería solo el del arranque", n)
	}
}

// TestRun_StartupRescueFeedsTheFirstPoll: la entrega que un worker muerto dejó en vuelo se rescata
// al arrancar y sale en el PRIMER poll; el rescate lo dice en WARN, con cuántas y con qué lease.
func TestRun_StartupRescueFeedsTheFirstPoll(t *testing.T) {
	rig := newWorkerRig()
	crm := newBridge(t)
	rig.integrate(t, rigTenant, crm.srv.URL)
	id := rig.enqueueTemplate(t)
	if _, err := rig.mem.ClaimWebhookBatch(context.Background(), 1); err != nil {
		t.Fatalf("simular el claim del worker muerto: %v", err)
	}
	rig.clock.Advance(2 * time.Hour) // muy por encima del lease por defecto (1 min)

	rig.runPolls(t, 1)

	row := rig.row(t, id)
	if row.Status != integrations.StatusDelivered || row.Attempts != 1 {
		t.Errorf("la entrega huérfana quedó (status=%q, attempts=%d), quería (delivered, 1: el claim vencido cuenta)", row.Status, row.Attempts)
	}
	crm.only(t)
	line := rig.log.line(t, "WARN", "webhook worker: entregas con el claim vencido devueltas a pending")
	requireKeys(t, line, "count=1", "lease=1m0s")
	rig.log.requireNoErrors(t)
}

// TestRun_Shutdown_SaysSoAtInfoAndReturns: Run bloquea hasta que el contexto se cancela; entonces
// lo dice en INFO, con su texto, y vuelve.
func TestRun_Shutdown_SaysSoAtInfoAndReturns(t *testing.T) {
	rig := newWorkerRig()
	stop := rig.start(t)
	rig.store.waitPolls(t, 2)
	if lines := rig.log.at("INFO"); len(lines) != 0 {
		t.Fatalf("con el worker en marcha el log ya tiene líneas a INFO: %v", lines)
	}
	stop()
	rig.log.line(t, "INFO", "webhook worker: apagando (contexto cancelado)")
	rig.log.requireNoErrors(t)
}

// TestRun_DeliversTheBatchInOrderOneAtATime: un lote se entrega en serie y en el orden en que lo
// dio el almacén: nunca dos POST a la vez.
func TestRun_DeliversTheBatchInOrderOneAtATime(t *testing.T) {
	rig := newWorkerRig()
	crm := newBridge(t)
	rig.integrate(t, rigTenant, crm.srv.URL)
	var want []string
	for range 4 {
		want = append(want, strconv.FormatInt(rig.enqueueTemplate(t), 10))
		rig.clock.Advance(time.Second)
	}

	rig.runPolls(t, 1)

	var got []string
	for _, req := range crm.received() {
		got = append(got, req.header.Get("X-Wapp-Delivery"))
	}
	if !slices.Equal(got, want) {
		t.Errorf("orden de las entregas = %v, quería %v (el del lote)", got, want)
	}
	if crm.maxInFlight != 1 {
		t.Errorf("hubo %d POST a la vez, quería 1: dentro de un lote se entrega en serie", crm.maxInFlight)
	}
	if got := rig.recorded(); len(got) != 4 {
		t.Errorf("métrica = %v, quería cuatro «delivered»", got)
	}
}

// TestWithClock_NilOrAbsent_UsesTheSystemClock: sin la opción, o con un reloj nil, el instante que
// se firma es el del sistema.
func TestWithClock_NilOrAbsent_UsesTheSystemClock(t *testing.T) {
	cases := []struct {
		name string
		opts []integrations.WorkerOption
	}{
		{"no option", nil},
		{"nil clock", []integrations.WorkerOption{integrations.WithClock(nil)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newWorkerRig()
			rig.mem.SetClock(nil)
			rig.opts = c.opts
			crm := newBridge(t)
			rig.integrate(t, rigTenant, crm.srv.URL)
			rig.enqueueTemplate(t)

			before := time.Now().Unix()
			rig.runPolls(t, 1)
			after := time.Now().Unix()

			stamp, err := strconv.ParseInt(crm.only(t).header.Get("X-Wapp-Timestamp"), 10, 64)
			if err != nil {
				t.Fatalf("X-Wapp-Timestamp no es un entero: %v", err)
			}
			if stamp < before || stamp > after {
				t.Errorf("X-Wapp-Timestamp = %d, quería el reloj del sistema (entre %d y %d)", stamp, before, after)
			}
		})
	}
}

// TestNewWorker_NilCallback_IsSafe: un worker sin callback de métricas entrega y falla igual, sin
// entrar en pánico.
func TestNewWorker_NilCallback_IsSafe(t *testing.T) {
	rig := newWorkerRig()
	rig.noCallback = true
	crm := newBridge(t, http.StatusOK, http.StatusInternalServerError)
	rig.integrate(t, rigTenant, crm.srv.URL)
	delivered, failed := rig.enqueueTemplate(t), rig.enqueueTemplate(t)

	rig.runPolls(t, 1)

	if got := rig.row(t, delivered).Status; got != integrations.StatusDelivered {
		t.Errorf("status de la primera = %q, quería delivered", got)
	}
	if got := rig.row(t, failed); got.Status != integrations.StatusPending || !strings.HasPrefix(got.LastError, "respuesta 500") {
		t.Errorf("la segunda quedó (status=%q, last_error=%q), quería pending con el 500", got.Status, got.LastError)
	}
}
