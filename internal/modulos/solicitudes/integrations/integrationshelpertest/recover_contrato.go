package integrationshelpertest

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// rescued devuelve cómo tiene que quedar una fila en vuelo tras rescatarla: `pending`, con un
// intento más, sin claim y con el last_error literal del rescate. Todo lo demás, igual.
func rescued(inFlight OutboxRow) OutboxRow {
	want := inFlight
	want.Status, want.ClaimedAt = integrations.StatusPending, time.Time{}
	want.Attempts, want.LastError = inFlight.Attempts+1, orphanReason
	return want
}

// caseRecoverNothing: una fila `pending` (aunque esté vencida), una `delivered` y una `dead` no
// son huérfanas: ni con lease 0 se recupera nada, y quedan idénticas.
func caseRecoverNothing(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	ctx := context.Background()
	delivered, dead := claimOne(t, m), claimOne(t, m)
	if err := m.Store.MarkWebhookDelivered(ctx, delivered); err != nil {
		t.Fatalf("MarkWebhookDelivered: error inesperado %v", err)
	}
	if err := m.Store.MarkWebhookDead(ctx, dead, "se acabó"); err != nil {
		t.Fatalf("MarkWebhookDead: error inesperado %v", err)
	}
	pending := enqueue(t, m, m.TenantA, samplePayload)
	rows := []OutboxRow{row(t, m, delivered.ID), row(t, m, dead.ID), row(t, m, pending)}
	m.Advance(t)

	recoverOrphans(t, m, 0, 0)
	recoverOrphans(t, m, time.Hour, 0)
	for _, want := range rows {
		requireSameOutbox(t, "la fila "+want.Status+" tras el rescate", row(t, m, want.ID), want)
	}
	w.requireUntouched(t, m)
}

// caseRecoverLiveClaim: una entrega recién reclamada está VIVA: con un lease holgado el rescate no
// la toca, y su dueño la cierra después sin encontrársela robada (es la regresión del hallazgo #2
// del review de las Olas 1-3: antes del lease, el arranque de una réplica revertía el trabajo de
// la otra).
func caseRecoverLiveClaim(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	c := claimOne(t, m)
	inFlight := row(t, m, c.ID)
	m.Advance(t)

	recoverOrphans(t, m, time.Hour, 0)
	requireSameOutbox(t, "la entrega en vuelo tras el rescate", row(t, m, c.ID), inFlight)
	if err := m.Store.MarkWebhookDelivered(context.Background(), c); err != nil {
		t.Errorf("el dueño del claim vigente no pudo cerrar su entrega: %v", err)
	}
	w.requireUntouched(t, m)
}

// caseRecoverExpired: el rescate devuelve a `pending` SOLO las entregas cuyo claim lleva más que
// el lease, sean del tenant que sean: attempts+1, sin claim, con el last_error literal, y con su
// payload y su next_attempt_at intactos. Devuelve cuántas. La que lleva menos que el lease sigue
// en vuelo, idéntica.
func caseRecoverExpired(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	ids := []int64{
		enqueue(t, m, m.TenantA, samplePayload),
		enqueue(t, m, m.TenantB, `{"de":"b"}`),
		enqueue(t, m, m.TenantA, `{"viva":true}`),
	}
	claimExactly(t, m, ids...)
	now := wholeSeconds(m.Now(t))
	m.SetClaimedAt(t, ids[0], now.Add(-2*time.Hour))
	m.SetClaimedAt(t, ids[1], now.Add(-90*time.Minute))
	m.SetClaimedAt(t, ids[2], now.Add(-30*time.Minute))
	expiredA, expiredB, live := row(t, m, ids[0]), row(t, m, ids[1]), row(t, m, ids[2])

	recoverOrphans(t, m, time.Hour, 2)

	requireSameOutbox(t, "la entrega de TenantA con el claim vencido", row(t, m, ids[0]), rescued(expiredA))
	requireSameOutbox(t, "la entrega de TenantB con el claim vencido", row(t, m, ids[1]), rescued(expiredB))
	requireSameOutbox(t, "la entrega con el claim vigente", row(t, m, ids[2]), live)
	// Rescatar otra vez no encuentra nada: ya no están en vuelo.
	recoverOrphans(t, m, time.Hour, 0)
	requireSameOutbox(t, "la entrega rescatada, tras un segundo rescate", row(t, m, ids[0]), rescued(expiredA))
	w.requireUntouched(t, m)
}

// caseRecoverUnsealed: una fila `delivering` SIN claimed_at solo puede venir del código anterior a
// la migración 0049 (hoy todo claim se sella): es huérfana inmediata, por holgado que sea el lease.
func caseRecoverUnsealed(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	c := claimOne(t, m)
	m.SetClaimedAt(t, c.ID, time.Time{})
	unsealed := row(t, m, c.ID)
	if unsealed.Status != integrations.StatusDelivering || !unsealed.ClaimedAt.IsZero() {
		t.Fatalf("la siembra no dejó la fila en vuelo y sin sello: %+v", unsealed)
	}

	recoverOrphans(t, m, 24*time.Hour, 1)
	requireSameOutbox(t, "la fila sin sello tras el rescate", row(t, m, c.ID), rescued(unsealed))
	w.requireUntouched(t, m)
}

// caseRecoverZeroLease: con lease 0 todo claim sellado ANTES de este instante está vencido. Y lo
// vencido cuenta un intento: sin contarlo, una entrega que tumba a su worker giraría para siempre
// sin llegar a `dead`.
func caseRecoverZeroLease(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	first, second := claimOne(t, m), claimOne(t, m)
	rows := []OutboxRow{row(t, m, first.ID), row(t, m, second.ID)}
	m.Advance(t)

	recoverOrphans(t, m, 0, 2)
	for _, inFlight := range rows {
		requireSameOutbox(t, "la entrega rescatada con lease 0", row(t, m, inFlight.ID), rescued(inFlight))
	}
	w.requireUntouched(t, m)
}

// caseRecoverNewHolder: la entrega rescatada vuelve a ser reclamable enseguida, y quien la reclama
// recibe un sello NUEVO y la fila con su intento contado. El worker que la tenía ya no puede
// cerrarla (ErrClaimLost) ni estropear lo del nuevo, que sí la cierra.
func caseRecoverNewHolder(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	ctx := context.Background()
	old := claimOne(t, m)
	m.Advance(t)
	recoverOrphans(t, m, 0, 1)
	m.Advance(t)

	fresh := claimExactly(t, m, old.ID)[0]
	if !fresh.ClaimedAt.After(old.ClaimedAt) {
		t.Fatalf("el claim nuevo lleva el sello %v, quería uno posterior al viejo (%v)", fresh.ClaimedAt, old.ClaimedAt)
	}
	if fresh.Attempts != 1 || fresh.LastError != orphanReason {
		t.Errorf("la fila reclamada tras el rescate trae (attempts=%d, last_error=%q), quería (1, el del rescate)",
			fresh.Attempts, fresh.LastError)
	}
	held := row(t, m, old.ID)

	requireClaimLost(t, m.Store.MarkWebhookDelivered(ctx, old), old.ID, "delivered")
	requireSameOutbox(t, "la fila del dueño nuevo tras el intento del viejo", row(t, m, old.ID), held)
	if err := m.Store.MarkWebhookDelivered(ctx, fresh); err != nil {
		t.Errorf("el dueño nuevo no pudo cerrar su entrega: %v", err)
	}
	if got := row(t, m, old.ID); got.Status != integrations.StatusDelivered {
		t.Errorf("status final = %q, quería delivered", got.Status)
	}
	w.requireUntouched(t, m)
}
