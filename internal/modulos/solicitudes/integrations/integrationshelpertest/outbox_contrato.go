package integrationshelpertest

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// caseEnqueueNewRow: la fila recién encolada es `pending`, con cero intentos, sin error, sin claim,
// con el tenant, el verbo y el payload dados, y reclamable desde ya (next_attempt_at = created_at,
// no posterior al reloj).
func caseEnqueueNewRow(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	id := enqueue(t, m, m.TenantA, samplePayload)
	got := row(t, m, id)
	if got.CreatedAt.IsZero() || got.CreatedAt.After(m.Now(t)) {
		t.Errorf("created_at = %v, quería un instante no posterior al reloj (%v)", got.CreatedAt, m.Now(t))
	}
	requireSameOutbox(t, "la entrega recién encolada", got, OutboxRow{
		ID: id, TenantID: m.TenantA, Kind: kindPush, Payload: json.RawMessage(samplePayload),
		Status: integrations.StatusPending, NextAttemptAt: got.CreatedAt, CreatedAt: got.CreatedAt,
	})
	w.requireUntouched(t, m)
}

// caseEnqueueIDsGrow: los id crecen en el orden de encolado, y la secuencia es UNA para todos los
// tenants. Cada fila es de quien la encoló.
func caseEnqueueIDsGrow(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	owners := []string{m.TenantA, m.TenantB, m.TenantA, m.TenantB, m.TenantA}
	last := w.rows[len(w.rows)-1].ID
	for i, tenant := range owners {
		id := enqueue(t, m, tenant, `{"n":1}`)
		if id <= last {
			t.Fatalf("encolado %d: id %d, y el anterior fue %d; quería uno mayor", i, id, last)
		}
		if got := row(t, m, id); got.TenantID != tenant {
			t.Errorf("la entrega %d es de %q, quería %q", id, got.TenantID, tenant)
		}
		last = id
	}
	w.requireUntouched(t, m)
}

// caseEnqueueInvalid: un payload que no es JSON válido (o vacío) se rechaza con el prefijo del
// puerto y no deja fila: lo único reclamable sigue siendo lo que había.
func caseEnqueueInvalid(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	good := enqueue(t, m, m.TenantA, `{"ok":true}`)
	for _, payload := range []json.RawMessage{json.RawMessage(`{"sin cerrar":`), json.RawMessage(``), nil} {
		id, err := m.Store.EnqueueWebhook(context.Background(), m.TenantA, kindPush, payload)
		if err == nil {
			t.Fatalf("EnqueueWebhook con el payload %q no falló (id %d)", payload, id)
		}
		if want := "integrations: encolar entrega de " + kindPush + ": "; !strings.HasPrefix(err.Error(), want) {
			t.Errorf("error = %q, quería el prefijo %q", err, want)
		}
		if id != 0 {
			t.Errorf("un encolado rechazado devolvió el id %d, quería 0", id)
		}
	}
	later := enqueue(t, m, m.TenantA, `{"ok":true}`)
	if later <= good {
		t.Errorf("tras los rechazos el id es %d, quería uno mayor que %d", later, good)
	}
	claimExactly(t, m, good, later)
	w.requireUntouched(t, m)
}

// caseEnqueueCopiesPayload: el almacén se queda con SU copia del payload; que el llamante
// reutilice el slice después no cambia lo encolado.
func caseEnqueueCopiesPayload(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	payload := json.RawMessage(`{"n":1}`)
	id, err := m.Store.EnqueueWebhook(context.Background(), m.TenantA, kindPush, payload)
	if err != nil {
		t.Fatalf("EnqueueWebhook: error inesperado %v", err)
	}
	copy(payload, `{"n":9}`)
	if got := row(t, m, id); !sameJSON(got.Payload, json.RawMessage(`{"n":1}`)) {
		t.Errorf("payload guardado = %s tras reutilizar el slice del llamante, quería {\"n\":1}", got.Payload)
	}
	w.requireUntouched(t, m)
}

// caseClaimNothingDue: sin nada reclamable —solo hay filas que no han vencido— el reclamo devuelve
// cero filas y ningún error, y no toca nada.
func caseClaimNothingDue(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	if batch := claim(t, m, 10); len(batch) != 0 {
		t.Errorf("con la cola sin nada vencido se reclamaron %d filas: %+v", len(batch), batch)
	}
	w.requireUntouched(t, m)
}

// caseClaimSeals: reclamar marca `delivering` y SELLA con claimed_at (no cero, no anterior al
// encolado); lo que devuelve es la fila tal como queda guardada, y no toca intentos,
// next_attempt_at, created_at, payload ni last_error.
func caseClaimSeals(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	id := enqueue(t, m, m.TenantA, samplePayload)
	before := row(t, m, id)
	m.Advance(t)

	got := claimExactly(t, m, id)[0]
	if got.ClaimedAt.IsZero() {
		t.Fatal("el claim vuelve sin sellar: claimed_at es la valla de las tres transiciones")
	}
	if !got.ClaimedAt.After(before.CreatedAt) {
		t.Errorf("claimed_at = %v, quería un instante posterior al encolado (%v)", got.ClaimedAt, before.CreatedAt)
	}
	want := before
	want.Status, want.ClaimedAt = integrations.StatusDelivering, got.ClaimedAt
	requireSameOutbox(t, "la fila que devuelve el reclamo", got, want)
	requireSameOutbox(t, "la fila guardada tras el reclamo", row(t, m, id), want)
	w.requireUntouched(t, m)
}

// caseClaimNotTwice: una fila reclamada no vuelve a salir mientras siga en `delivering`, por mucho
// que pase el reloj: el reclamo no rescata (para eso está RecoverOrphanDeliveries).
func caseClaimNotTwice(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	first := claimOne(t, m)
	held := row(t, m, first.ID)
	m.Advance(t)
	if batch := claim(t, m, 10); len(batch) != 0 {
		t.Errorf("la fila ya reclamada volvió a salir: %+v", batch)
	}
	// Ni siquiera con el sello de hace horas: vencido no es reclamable.
	old := wholeSeconds(m.Now(t)).Add(-6 * time.Hour)
	m.SetClaimedAt(t, first.ID, old)
	if batch := claim(t, m, 10); len(batch) != 0 {
		t.Errorf("una fila en vuelo con el claim vencido salió por el reclamo: %+v", batch)
	}
	held.ClaimedAt = old
	requireSameOutbox(t, "la fila en vuelo", row(t, m, first.ID), held)
	w.requireUntouched(t, m)
}

// caseClaimLimit: el reclamo respeta el tamaño de lote y, si hay más filas que límite, se lleva
// las de next_attempt_at más antiguo; las demás quedan intactas para el siguiente.
func caseClaimLimit(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	ids := make([]int64, 0, 5)
	for range 5 {
		ids = append(ids, enqueue(t, m, m.TenantA, `{"n":1}`))
		m.Advance(t)
	}
	waiting := row(t, m, ids[4])

	requireIDs(t, "el primer lote (límite 2)", claim(t, m, 2), ids[0], ids[1])
	requireIDs(t, "el segundo lote (límite 2)", claim(t, m, 2), ids[2], ids[3])
	requireSameOutbox(t, "la fila que no cupo en ningún lote", row(t, m, ids[4]), waiting)
	requireIDs(t, "el tercer lote (límite 2)", claim(t, m, 2), ids[4])
	w.requireUntouched(t, m)
}

// caseClaimZeroLimit: con límite 0 no se reclama nada y la fila vencida sigue esperando.
func caseClaimZeroLimit(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	id := enqueue(t, m, m.TenantA, samplePayload)
	before := row(t, m, id)
	if batch := claim(t, m, 0); len(batch) != 0 {
		t.Errorf("con límite 0 se reclamaron %d filas", len(batch))
	}
	requireSameOutbox(t, "la fila tras un reclamo con límite 0", row(t, m, id), before)
	w.requireUntouched(t, m)
}

// caseClaimOrder: lo que decide quién entra en el lote es next_attempt_at, no el id: la fila que
// se encoló después, pero lleva más tiempo vencida, sale primero.
func caseClaimOrder(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	ctx := context.Background()
	older := enqueue(t, m, m.TenantA, `{"n":1}`)
	m.Advance(t)
	newer := enqueue(t, m, m.TenantA, `{"n":2}`)
	claims := claimExactly(t, m, older, newer)
	base := wholeSeconds(m.Now(t))
	// La de id menor vence hace una hora; la de id mayor, hace dos.
	if err := m.Store.MarkWebhookFailed(ctx, claims[0], base.Add(-time.Hour), "x"); err != nil {
		t.Fatalf("MarkWebhookFailed: %v", err)
	}
	if err := m.Store.MarkWebhookFailed(ctx, claims[1], base.Add(-2*time.Hour), "x"); err != nil {
		t.Fatalf("MarkWebhookFailed: %v", err)
	}
	requireIDs(t, "el lote de una fila", claim(t, m, 1), newer)
	requireIDs(t, "el lote siguiente", claim(t, m, 1), older)
	w.requireUntouched(t, m)
}

// caseClaimNotDue: una fila reprogramada para el futuro no se reclama y queda intacta; una
// reprogramada para el pasado se reclama, y con un sello NUEVO.
func caseClaimNotDue(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	ctx := context.Background()
	future, past := claimOne(t, m), claimOne(t, m)
	base := wholeSeconds(m.Now(t))
	if err := m.Store.MarkWebhookFailed(ctx, future, base.Add(time.Hour), "todavía no"); err != nil {
		t.Fatalf("MarkWebhookFailed: %v", err)
	}
	if err := m.Store.MarkWebhookFailed(ctx, past, base.Add(-time.Hour), "ya toca"); err != nil {
		t.Fatalf("MarkWebhookFailed: %v", err)
	}
	waiting := row(t, m, future.ID)
	m.Advance(t)

	again := claimExactly(t, m, past.ID)[0]
	if !again.ClaimedAt.After(past.ClaimedAt) {
		t.Errorf("el segundo claim lleva el sello %v, quería uno posterior al primero (%v)", again.ClaimedAt, past.ClaimedAt)
	}
	if again.Attempts != 1 || again.LastError != "ya toca" {
		t.Errorf("la fila reclamada de nuevo trae (attempts=%d, last_error=%q), quería (1, \"ya toca\")", again.Attempts, again.LastError)
	}
	requireSameOutbox(t, "la fila que aún no ha vencido", row(t, m, future.ID), waiting)
	w.requireUntouched(t, m)
}

// caseClaimEveryTenant: la cola es una; un reclamo se lleva lo vencido de todos los tenants, cada
// fila con su dueño.
func caseClaimEveryTenant(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	a := enqueue(t, m, m.TenantA, `{"de":"a"}`)
	b := enqueue(t, m, m.TenantB, `{"de":"b"}`)
	claims := claimExactly(t, m, a, b)
	if claims[0].TenantID != m.TenantA || claims[1].TenantID != m.TenantB {
		t.Errorf("dueños del lote = (%q, %q), quería (%q, %q)", claims[0].TenantID, claims[1].TenantID, m.TenantA, m.TenantB)
	}
	w.requireUntouched(t, m)
}

// caseClaimConcurrent: varios reclamos a la vez se reparten la cola sin llevarse nunca la misma
// fila y sin dejarse ninguna (es el criterio de T3.1: FOR UPDATE SKIP LOCKED en Postgres).
func caseClaimConcurrent(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	const total, workers = 40, 8
	want := make([]int64, 0, total)
	for range total {
		want = append(want, enqueue(t, m, m.TenantA, `{"n":1}`))
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = map[int64]int{}
		errs []error
	)
	for range workers {
		wg.Go(func() {
			// El tope evita que una implementación que devuelve siempre las mismas filas deje el
			// caso girando: con él, ese defecto sale como «reclamada N veces».
			for range total {
				batch, err := m.Store.ClaimWebhookBatch(context.Background(), 5)
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				}
				for _, c := range batch {
					seen[c.ID]++
				}
				mu.Unlock()
				if err != nil || len(batch) == 0 {
					return
				}
			}
		})
	}
	wg.Wait()

	if len(errs) != 0 {
		t.Fatalf("%d reclamos concurrentes fallaron; el primero: %v", len(errs), errs[0])
	}
	for _, id := range want {
		if seen[id] != 1 {
			t.Errorf("la fila %d se reclamó %d veces, quería exactamente 1", id, seen[id])
		}
	}
	if len(seen) != total {
		t.Errorf("se reclamaron %d filas distintas, quería %d", len(seen), total)
	}
	w.requireUntouched(t, m)
}
