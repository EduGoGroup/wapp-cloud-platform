//go:build pendiente

package integrations_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// closeSite es uno de los tres sitios donde el worker cierra una fila, con cómo llevar una entrega
// hasta él.
type closeSite struct {
	// op es el método del almacén; transition, como se nombra en el log; logMsg, el mensaje del
	// ERROR cuando ese cierre falla por una causa que no es el claim.
	op, transition, logMsg string
	// prepare deja el banco de modo que la entrega acabe en ese cierre.
	prepare func(t *testing.T, rig *workerRig)
}

func closeSites() []closeSite {
	return []closeSite{
		{opDelivered, "delivered", "webhook worker: marcar delivered", func(t *testing.T, rig *workerRig) {
			rig.integrate(t, rigTenant, newBridge(t).srv.URL)
		}},
		// Sin integración, la entrega falla antes del POST: con intentos de sobra, se reprograma…
		{opFailed, "failed", "webhook worker: marcar failed", func(*testing.T, *workerRig) {}},
		// …y con uno solo, muere.
		{opDead, "dead", "webhook worker: marcar dead", func(_ *testing.T, rig *workerRig) { rig.cfg.MaxAttempts = 1 }},
	}
}

// TestRun_ClaimLost_CountedApartNeverAsDeliveryNorFailure: si al cerrar la fila el almacén dice
// ErrClaimLost —envuelto—, el worker NO se apunta la entrega ni un fallo: cuenta «claim_lost» y
// nada más, y lo avisa en WARN (no en ERROR) con la transición que no pudo hacer (R-14).
func TestRun_ClaimLost_CountedApartNeverAsDeliveryNorFailure(t *testing.T) {
	const warning = "webhook worker: el claim expiró antes de cerrar la entrega; la resolverá quien la reclamó después"
	for _, site := range closeSites() {
		t.Run(site.transition, func(t *testing.T) {
			rig := newWorkerRig()
			site.prepare(t, rig)
			rig.store.fail(site.op, fmt.Errorf("almacén: entrega tardía: %w", integrations.ErrClaimLost))
			id := rig.enqueueTemplate(t)

			rig.runPolls(t, 1)

			if got := rig.recorded(); !slices.Equal(got, []string{"claim_lost"}) {
				t.Errorf("métrica = %v, quería [claim_lost]: un claim perdido no es ni delivered ni failed ni dead", got)
			}
			line := rig.log.line(t, "WARN", warning)
			requireKeys(t, line, fmt.Sprintf("outbox_id=%d", id), "tenant="+rigTenant, "transicion="+site.transition, "lease=1m0s")
			rig.log.requireNoErrors(t)
		})
	}
}

// TestRun_ClaimLost_AfterA2xx_TheRowBelongsToTheNewHolder es el caso de verdad, sin guion: mientras
// el puente contesta, el lease vence y otro worker rescata y reclama la fila. El POST ya salió con
// 2xx, pero el cierre llega tarde: el almacén lo rechaza, este worker cuenta «claim_lost» y la
// fila sigue en vuelo, intacta, en manos de quien la reclamó (que la entregará otra vez:
// at-least-once).
func TestRun_ClaimLost_AfterA2xx_TheRowBelongsToTheNewHolder(t *testing.T) {
	rig := newWorkerRig()
	crm := newBridge(t)
	rig.integrate(t, rigTenant, crm.srv.URL)
	id := rig.enqueueTemplate(t)

	var thief integrations.WebhookOutbox
	crm.onPost = func(*http.Request) {
		ctx := context.Background()
		rig.clock.Advance(2 * time.Hour)
		if n, err := rig.mem.RecoverOrphanDeliveries(ctx, time.Minute); err != nil || n != 1 {
			t.Errorf("el rescate del otro worker dio (%d, %v), quería (1, nil)", n, err)
		}
		batch, err := rig.mem.ClaimWebhookBatch(ctx, 1)
		if err != nil || len(batch) != 1 {
			t.Errorf("el reclamo del otro worker dio (%d filas, %v), quería una", len(batch), err)
			return
		}
		thief = batch[0]
	}

	rig.runPolls(t, 1)

	if got := rig.recorded(); !slices.Equal(got, []string{"claim_lost"}) {
		t.Errorf("métrica = %v, quería [claim_lost]", got)
	}
	row := rig.row(t, id)
	if row.Status != integrations.StatusDelivering || !row.ClaimedAt.Equal(thief.ClaimedAt) || row.Attempts != 1 {
		t.Errorf("fila = (status=%q, claimed_at=%v, attempts=%d), quería en vuelo con el claim del otro worker (%v) y su intento",
			row.Status, row.ClaimedAt, row.Attempts, thief.ClaimedAt)
	}
	if string(row.Payload) == `{}` {
		t.Error("el worker que llegó tarde vació el payload de una fila que ya no era suya")
	}
	rig.log.requireNoErrors(t)
}

// TestRun_MetricStatuses_AreExactlyFour: el callback recibe uno de cuatro valores literales y
// ninguno más —es la etiqueta `status` de wapp_webhook_deliveries_total, de cardinalidad FIJA y
// sin tenant (R6.4.d, R-14)—, y uno solo por entrega resuelta.
func TestRun_MetricStatuses_AreExactlyFour(t *testing.T) {
	lost := fmt.Errorf("tarde: %w", integrations.ErrClaimLost)
	scenarios := []struct {
		want    string
		prepare func(t *testing.T, rig *workerRig)
	}{
		{"delivered", func(t *testing.T, rig *workerRig) { rig.integrate(t, rigTenant, newBridge(t).srv.URL) }},
		{"failed", func(*testing.T, *workerRig) {}},
		{"dead", func(_ *testing.T, rig *workerRig) { rig.cfg.MaxAttempts = 1 }},
		{"claim_lost", func(_ *testing.T, rig *workerRig) { rig.store.fail(opFailed, lost) }},
	}
	seen := map[string]bool{}
	for _, sc := range scenarios {
		rig := newWorkerRig()
		sc.prepare(t, rig)
		rig.enqueueTemplate(t)
		rig.runPolls(t, 1)
		got := rig.recorded()
		if !slices.Equal(got, []string{sc.want}) {
			t.Errorf("métrica = %v, quería exactamente [%s]", got, sc.want)
		}
		for _, status := range got {
			seen[status] = true
		}
	}
	if len(seen) != 4 {
		t.Errorf("valores distintos de la métrica = %v, quería los cuatro: delivered, failed, dead, claim_lost", seen)
	}
}

// TestRun_StoreErrors_LoggedAtErrorAndTheWorkerGoesOn: con el contexto VIVO, un fallo del almacén
// (que no sea el claim perdido) va a ERROR con su mensaje literal y la causa, y no tumba al worker,
// que sigue haciendo polls. Un cierre que falla no cuenta nada en la métrica: la entrega no está
// resuelta.
func TestRun_StoreErrors_LoggedAtErrorAndTheWorkerGoesOn(t *testing.T) {
	boom := errors.New("base caída")
	type errorSite struct {
		name, op, logMsg string
		prepare          func(t *testing.T, rig *workerRig)
		// outbox: la línea nombra la entrega (los tres cierres).
		outbox bool
		// wantMetric es lo que se cuenta pese al fallo.
		wantMetric []string
	}
	nothing := func(*testing.T, *workerRig) {}
	sites := []errorSite{
		// Un rescate fallido no impide el poll: la entrega (sin integración) se intenta y falla.
		{"rescue", opRecover, "webhook worker: rescatar entregas con el claim vencido", nothing, false, []string{"failed"}},
		// Un reclamo fallido deja el poll sin lote: no hay entrega ni cuenta.
		{"claim", opClaim, "webhook worker: reclamar lote", nothing, false, nil},
	}
	for _, site := range closeSites() {
		// Un cierre fallido no cuenta nada: la entrega no quedó resuelta.
		sites = append(sites, errorSite{site.transition, site.op, site.logMsg, site.prepare, true, nil})
	}
	for _, site := range sites {
		t.Run(site.name, func(t *testing.T) {
			rig := newWorkerRig()
			site.prepare(t, rig)
			rig.store.fail(site.op, boom)
			id := rig.enqueueTemplate(t)

			rig.runPolls(t, 2) // dos polls enteros: el fallo del primero no paró el ciclo

			var line string
			for _, l := range rig.log.at("ERROR") {
				if strings.HasPrefix(l, "ERROR "+site.logMsg+" |") {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("no hay ERROR %q; líneas a ERROR: %v", site.logMsg, rig.log.at("ERROR"))
			}
			requireKeys(t, line, "error=base caída")
			if got := rig.recorded(); !slices.Equal(got, site.wantMetric) {
				t.Errorf("métrica = %v, quería %v", got, site.wantMetric)
			}
			if site.outbox {
				requireKeys(t, line, fmt.Sprintf("outbox_id=%d", id))
			}
		})
	}
}

// TestRun_ContextCancelled_ReturnsWithoutLoggingAtError es D-F6-7: la parada no es un error. Si el
// contexto se cancela mientras una llamada al almacén está en curso —el rescate, el reclamo o
// cualquiera de los tres cierres—, o mientras el POST está en vuelo, Run vuelve y el log no tiene
// NI UNA línea a ERROR: solo el aviso de apagado. (El control positivo es el test de arriba: con
// el contexto vivo, esos mismos fallos sí van a ERROR.)
func TestRun_ContextCancelled_ReturnsWithoutLoggingAtError(t *testing.T) {
	type site struct {
		name    string
		blockOn string
		prepare func(t *testing.T, rig *workerRig)
	}
	sites := []site{
		{"during the startup rescue", opRecover, func(*testing.T, *workerRig) {}},
		{"during the claim", opClaim, func(*testing.T, *workerRig) {}},
	}
	for _, s := range closeSites() {
		sites = append(sites, site{"while marking " + s.transition, s.op, s.prepare})
	}
	for _, s := range sites {
		t.Run(s.name, func(t *testing.T) {
			rig := newWorkerRig()
			s.prepare(t, rig)
			rig.store.blockOn = s.blockOn
			rig.enqueueTemplate(t)

			stop := rig.start(t)
			select {
			case <-rig.store.blocked:
			case <-time.After(waitLimit):
				t.Fatalf("el worker no llegó a %s en %v", s.blockOn, waitLimit)
			}
			stop() // cancela con la llamada a medias y espera a que Run vuelva

			rig.log.requireNoErrors(t)
			rig.log.line(t, "INFO", "webhook worker: apagando (contexto cancelado)")
		})
	}

	t.Run("while the POST is in flight", func(t *testing.T) {
		rig := newWorkerRig()
		crm := newBridge(t)
		arrived := make(chan struct{})
		crm.onPost = func(r *http.Request) {
			close(arrived)
			<-r.Context().Done() // no contesta: el POST muere con la cancelación
		}
		rig.integrate(t, rigTenant, crm.srv.URL)
		id := rig.enqueueTemplate(t)

		stop := rig.start(t)
		select {
		case <-arrived:
		case <-time.After(waitLimit):
			t.Fatalf("el POST no llegó al puente en %v", waitLimit)
		}
		stop()

		rig.log.requireNoErrors(t)
		rig.log.line(t, "INFO", "webhook worker: apagando (contexto cancelado)")
		// La fila queda en vuelo: la rescatará el lease, no se da por fallida ni por entregada.
		if row := rig.row(t, id); row.Status != integrations.StatusDelivering || row.Attempts != 0 {
			t.Errorf("fila = (status=%q, attempts=%d), quería en vuelo y sin intento contado", row.Status, row.Attempts)
		}
		if got := rig.recorded(); len(got) != 0 {
			t.Errorf("métrica = %v, quería nada: la entrega no quedó resuelta", got)
		}
	})
}
