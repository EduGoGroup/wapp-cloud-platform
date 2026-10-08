package integrationshelpertest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// caseDelivered: cerrar en 2xx deja la fila `delivered`, sin claim y con el payload VACÍO ({}), y
// nada más: no cuenta un intento ni mueve next_attempt_at, created_at o last_error. La fila
// sobrevive: es el recibo.
func caseDelivered(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	c := claimOne(t, m)
	before := row(t, m, c.ID)
	if sameJSON(before.Payload, json.RawMessage(`{}`)) {
		t.Fatal("la fila reclamada ya tiene el payload vacío: el caso no probaría nada")
	}
	m.Advance(t)
	if err := m.Store.MarkWebhookDelivered(context.Background(), c); err != nil {
		t.Fatalf("MarkWebhookDelivered: error inesperado %v", err)
	}
	want := before
	want.Status, want.ClaimedAt, want.Payload = integrations.StatusDelivered, time.Time{}, json.RawMessage(`{}`)
	requireSameOutbox(t, "la fila entregada", row(t, m, c.ID), want)
	w.requireUntouched(t, m)
}

// caseDeliveredAfterFailure: una entrega que falló y luego llegó conserva en su recibo los
// intentos y el último error de antes; entregar no los borra ni cuenta otro intento.
func caseDeliveredAfterFailure(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	ctx := context.Background()
	c := claimOne(t, m)
	if err := m.Store.MarkWebhookFailed(ctx, c, wholeSeconds(m.Now(t)).Add(-time.Minute), "respuesta 500 del puente"); err != nil {
		t.Fatalf("MarkWebhookFailed: error inesperado %v", err)
	}
	m.Advance(t)
	retry := claimExactly(t, m, c.ID)[0]
	before := row(t, m, c.ID)
	if err := m.Store.MarkWebhookDelivered(ctx, retry); err != nil {
		t.Fatalf("MarkWebhookDelivered: error inesperado %v", err)
	}
	want := before
	want.Status, want.ClaimedAt, want.Payload = integrations.StatusDelivered, time.Time{}, json.RawMessage(`{}`)
	if want.Attempts != 1 || want.LastError != "respuesta 500 del puente" {
		t.Fatalf("antes de entregar la fila traía (attempts=%d, last_error=%q), quería (1, el del fallo)", want.Attempts, want.LastError)
	}
	requireSameOutbox(t, "la fila entregada al segundo intento", row(t, m, c.ID), want)
	w.requireUntouched(t, m)
}

// caseFailed: un intento fallido devuelve la fila a `pending` con attempts+1, el next_attempt_at
// y el last_error dados y sin claim; el payload se conserva (hay que volver a entregarlo).
func caseFailed(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	c := claimOne(t, m)
	before := row(t, m, c.ID)
	next := wholeSeconds(m.Now(t)).Add(37 * time.Minute)
	if err := m.Store.MarkWebhookFailed(context.Background(), c, next, "POST: connection refused"); err != nil {
		t.Fatalf("MarkWebhookFailed: error inesperado %v", err)
	}
	want := before
	want.Status, want.ClaimedAt = integrations.StatusPending, time.Time{}
	want.Attempts, want.NextAttemptAt, want.LastError = before.Attempts+1, next, "POST: connection refused"
	requireSameOutbox(t, "la fila con el intento fallido", row(t, m, c.ID), want)
	w.requireUntouched(t, m)
}

// caseDead: agotar los reintentos deja la fila `dead` con attempts+1, el last_error dado y sin
// claim, y CON su payload: es lo único que dice qué no se entregó. next_attempt_at no se mueve.
func caseDead(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	c := claimOne(t, m)
	before := row(t, m, c.ID)
	if err := m.Store.MarkWebhookDead(context.Background(), c, "respuesta 502 del puente"); err != nil {
		t.Fatalf("MarkWebhookDead: error inesperado %v", err)
	}
	want := before
	want.Status, want.ClaimedAt = integrations.StatusDead, time.Time{}
	want.Attempts, want.LastError = before.Attempts+1, "respuesta 502 del puente"
	requireSameOutbox(t, "la fila muerta", row(t, m, c.ID), want)
	if !sameJSON(want.Payload, json.RawMessage(samplePayload)) {
		t.Errorf("payload de la fila muerta = %s, quería el encolado", want.Payload)
	}
	w.requireUntouched(t, m)
}

// caseTerminalRowsStay: una fila `delivered` o `dead` es terminal: no se reclama aunque su
// next_attempt_at esté vencido, ni la rescata un lease de cero.
func caseTerminalRowsStay(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	ctx := context.Background()
	delivered, dead := claimOne(t, m), claimOne(t, m)
	if err := m.Store.MarkWebhookDelivered(ctx, delivered); err != nil {
		t.Fatalf("MarkWebhookDelivered: error inesperado %v", err)
	}
	if err := m.Store.MarkWebhookDead(ctx, dead, "se acabó"); err != nil {
		t.Fatalf("MarkWebhookDead: error inesperado %v", err)
	}
	closed := []OutboxRow{row(t, m, delivered.ID), row(t, m, dead.ID)}
	m.Advance(t)

	if batch := claim(t, m, 10); len(batch) != 0 {
		t.Errorf("se reclamaron filas terminales: %+v", batch)
	}
	recoverOrphans(t, m, 0, 0)
	for _, want := range closed {
		requireSameOutbox(t, "la fila terminal ("+want.Status+")", row(t, m, want.ID), want)
	}
	w.requireUntouched(t, m)
}

// caseCloseOnlyOwnRow: cada una de las tres transiciones toca SOLO la fila de su claim; una vecina
// en vuelo, reclamada en el mismo lote (y por tanto con el mismo sello o uno casi igual), queda
// idéntica.
func caseCloseOnlyOwnRow(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	ctx := context.Background()
	ids := []int64{
		enqueue(t, m, m.TenantA, `{"n":1}`), enqueue(t, m, m.TenantA, `{"n":2}`),
		enqueue(t, m, m.TenantA, `{"n":3}`), enqueue(t, m, m.TenantA, `{"n":4}`),
	}
	claims := claimExactly(t, m, ids...)
	neighbour := row(t, m, ids[3])

	if err := m.Store.MarkWebhookDelivered(ctx, claims[0]); err != nil {
		t.Fatalf("MarkWebhookDelivered: error inesperado %v", err)
	}
	requireSameOutbox(t, "la vecina tras un delivered", row(t, m, ids[3]), neighbour)
	if err := m.Store.MarkWebhookFailed(ctx, claims[1], wholeSeconds(m.Now(t)).Add(time.Hour), "x"); err != nil {
		t.Fatalf("MarkWebhookFailed: error inesperado %v", err)
	}
	requireSameOutbox(t, "la vecina tras un failed", row(t, m, ids[3]), neighbour)
	if err := m.Store.MarkWebhookDead(ctx, claims[2], "x"); err != nil {
		t.Fatalf("MarkWebhookDead: error inesperado %v", err)
	}
	requireSameOutbox(t, "la vecina tras un dead", row(t, m, ids[3]), neighbour)
	// Y la vecina sigue pudiendo cerrarse con su propio claim.
	if err := m.Store.MarkWebhookDelivered(ctx, claims[3]); err != nil {
		t.Errorf("la vecina no pudo cerrar su propia entrega: %v", err)
	}
	w.requireUntouched(t, m)
}

// closing es una de las tres transiciones que cierran un claim.
type closing struct {
	// name es el método del puerto.
	name string
	// what es como la transición se nombra en el texto del rechazo.
	what string
	// apply la ejecuta con ese claim.
	apply func(m Montaje, claim integrations.WebhookOutbox) error
}

// closings son las tres, con argumentos cualesquiera para las que los llevan.
func closings() []closing {
	ctx := context.Background()
	return []closing{
		{"MarkWebhookDelivered", "delivered", func(m Montaje, c integrations.WebhookOutbox) error {
			return m.Store.MarkWebhookDelivered(ctx, c)
		}},
		{"MarkWebhookFailed", "reintento", func(m Montaje, c integrations.WebhookOutbox) error {
			return m.Store.MarkWebhookFailed(ctx, c, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), "no debe quedar escrito")
		}},
		{"MarkWebhookDead", "dead", func(m Montaje, c integrations.WebhookOutbox) error {
			return m.Store.MarkWebhookDead(ctx, c, "no debe quedar escrito")
		}},
	}
}

// staleClaim es una forma de presentar un claim que NO vale: deja la cola en ese estado y devuelve
// el claim que la valla tiene que rechazar.
type staleClaim struct {
	name  string
	setup func(t *testing.T, m Montaje) integrations.WebhookOutbox
}

// staleClaims son las ocho formas. El comentario de cada una es por qué el claim no vale.
func staleClaims() []staleClaim {
	return []staleClaim{
		// El lease venció y el rescate devolvió la fila a pending: ya no hay claim.
		{"AfterRescue", func(t *testing.T, m Montaje) integrations.WebhookOutbox {
			c := claimOne(t, m)
			m.Advance(t)
			recoverOrphans(t, m, 0, 1)
			return c
		}},
		// Además otro worker la reclamó: está en vuelo, pero con OTRO sello.
		{"AfterReclaim", func(t *testing.T, m Montaje) integrations.WebhookOutbox {
			c := claimOne(t, m)
			m.Advance(t)
			recoverOrphans(t, m, 0, 1)
			m.Advance(t)
			claimExactly(t, m, c.ID)
			return c
		}},
		// La fila está en vuelo y el id es el suyo, pero el sello no es el de su claim.
		{"ForeignSeal", func(t *testing.T, m Montaje) integrations.WebhookOutbox {
			c := claimOne(t, m)
			c.ClaimedAt = c.ClaimedAt.Add(-time.Hour)
			return c
		}},
		// La fila nunca se reclamó: no hay claim que cerrar.
		{"NeverClaimed", func(t *testing.T, m Montaje) integrations.WebhookOutbox {
			return integrations.WebhookOutbox{ID: enqueue(t, m, m.TenantA, samplePayload)}
		}},
		// No existe ninguna fila con ese id.
		{"UnknownID", func(t *testing.T, m Montaje) integrations.WebhookOutbox {
			c := claimOne(t, m)
			c.ID += 1000
			return c
		}},
		// El claim ya se cerró: presentarlo otra vez no puede reescribir el recibo.
		{"AlreadyClosed", func(t *testing.T, m Montaje) integrations.WebhookOutbox {
			c := claimOne(t, m)
			if err := m.Store.MarkWebhookDelivered(context.Background(), c); err != nil {
				t.Fatalf("MarkWebhookDelivered: error inesperado %v", err)
			}
			return c
		}},
		// La fila está en vuelo SIN sello (la dejó el código anterior a la 0049): un claim sin
		// sello no la casa. «Sin sello» no es igual a «sin sello».
		{"UnsealedRow", func(t *testing.T, m Montaje) integrations.WebhookOutbox {
			c := claimOne(t, m)
			m.SetClaimedAt(t, c.ID, time.Time{})
			c.ClaimedAt = time.Time{}
			return c
		}},
		// El sello casa, pero la fila no está en vuelo: el estado también es parte de la valla.
		{"SealedButNotInFlight", func(t *testing.T, m Montaje) integrations.WebhookOutbox {
			id := enqueue(t, m, m.TenantA, samplePayload)
			seal := wholeSeconds(m.Now(t))
			m.SetClaimedAt(t, id, seal)
			return integrations.WebhookOutbox{ID: id, ClaimedAt: seal}
		}},
	}
}

// claimLostCases cruza las tres transiciones con las ocho formas de claim que no vale: cada una
// devuelve ErrClaimLost con su texto y deja la fila —y todo lo demás— idéntica.
func claimLostCases() []contractCase {
	transitions, claims := closings(), staleClaims()
	out := make([]contractCase, 0, len(transitions)*len(claims))
	for _, tr := range transitions {
		for _, sc := range claims {
			out = append(out, contractCase{
				name: tr.name + "_" + sc.name + "_ErrClaimLost",
				run:  func(t *testing.T, m Montaje) { caseClaimLost(t, m, tr, sc) },
			})
		}
	}
	return out
}

func caseClaimLost(t *testing.T, m Montaje, tr closing, sc staleClaim) {
	w := seedWitness(t, m)
	stale := sc.setup(t, m)
	before, existed := m.OutboxRow(t, stale.ID)

	requireClaimLost(t, tr.apply(m, stale), stale.ID, tr.what)

	after, exists := m.OutboxRow(t, stale.ID)
	if exists != existed {
		t.Fatalf("tras el rechazo la fila %d existe=%v, y antes existía=%v", stale.ID, exists, existed)
	}
	if existed {
		requireSameOutbox(t, "la fila tras el rechazo", after, before)
	}
	w.requireUntouched(t, m)
}
