package integrationshelpertest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// Los valores que comparten los casos.
const (
	// kindPush es el verbo de las entregas de la suite (el único de hoy en wapp-crm-v1).
	kindPush = "intake.push"
	// samplePayload es una plantilla con contenido reconocible: si sobreviviera a la entrega, se
	// vería.
	samplePayload = `{"contract_version":"1","verb":"intake.push","intake_id":"i-1","total":10.5,"items":[{"sku":"a","qty":2}]}`
	// witnessError es el last_error con el que quedan las entregas testigo.
	witnessError = "testigo: no me toques"
	// witnessSecret es el secreto de la integración testigo de TenantB. No es una credencial.
	witnessSecret = "secreto-del-testigo-de-la-suite" // #nosec G101 -- material de test
	// orphanReason es el last_error LITERAL que deja el rescate por lease vencido.
	orphanReason = "claim vencido: el worker que reclamó la entrega no la resolvió dentro del lease"
)

// tenantSnapshot es la MARCA DE ESTADO de la integración de un tenant: la fila guardada entera y
// lo que el puerto dice de ella, secreto incluido.
type tenantSnapshot struct {
	row         IntegrationRow
	found       bool
	secret      string
	secretFound bool
}

// takeTenant saca la marca de estado de la integración del tenant, y de paso comprueba que lo que
// el puerto lee (GetTenantIntegration) es lo que hay guardado.
func takeTenant(t *testing.T, m Montaje, tenant string) tenantSnapshot {
	t.Helper()
	ctx := context.Background()
	var s tenantSnapshot
	s.row, s.found = m.IntegrationRow(t, tenant)
	ti, found, err := m.Store.GetTenantIntegration(ctx, tenant)
	if err != nil {
		t.Fatalf("GetTenantIntegration(%q): error inesperado %v", tenant, err)
	}
	if found != s.found {
		t.Fatalf("GetTenantIntegration(%q) found=%v, pero la fila guardada existe=%v", tenant, found, s.found)
	}
	if found {
		requirePortMatchesRow(t, ti, s.row)
	}
	s.secret, s.secretFound, err = m.Store.GetTenantSecret(ctx, tenant)
	if err != nil {
		t.Fatalf("GetTenantSecret(%q): error inesperado %v", tenant, err)
	}
	if s.secretFound != (s.found && s.row.HasSecret) {
		t.Errorf("GetTenantSecret(%q) found=%v, pero la fila guardada dice HasSecret=%v (existe=%v)",
			tenant, s.secretFound, s.row.HasSecret, s.found)
	}
	return s
}

// requirePortMatchesRow afirma que la integración que da el puerto es la fila guardada, campo a
// campo.
func requirePortMatchesRow(t *testing.T, ti integrations.TenantIntegration, row IntegrationRow) {
	t.Helper()
	if ti.TenantID != row.TenantID || ti.CatalogAdapter != row.CatalogAdapter || ti.EventsAdapter != row.EventsAdapter ||
		ti.EndpointURL != row.EndpointURL || ti.Enabled != row.Enabled || ti.HasSecret != row.HasSecret {
		t.Errorf("GetTenantIntegration = %+v, y la fila guardada es %+v", ti, row)
	}
	if !ti.CreatedAt.Equal(row.CreatedAt) || !ti.UpdatedAt.Equal(row.UpdatedAt) {
		t.Errorf("GetTenantIntegration: (CreatedAt, UpdatedAt) = (%v, %v), y la fila guardada tiene (%v, %v)",
			ti.CreatedAt, ti.UpdatedAt, row.CreatedAt, row.UpdatedAt)
	}
}

// requireSameTenant afirma que got es la misma marca de estado que want: la fila ENTERA, la marca
// del sobre y el secreto.
func requireSameTenant(t *testing.T, what string, got, want tenantSnapshot) {
	t.Helper()
	if got.found != want.found {
		t.Fatalf("%s: la fila existe=%v, quería %v", what, got.found, want.found)
	}
	g, w := got.row, want.row
	if g.TenantID != w.TenantID || g.CatalogAdapter != w.CatalogAdapter || g.EventsAdapter != w.EventsAdapter ||
		g.EndpointURL != w.EndpointURL || g.Enabled != w.Enabled {
		t.Errorf("%s: configuración = %+v, quería %+v", what, g, w)
	}
	if !g.CreatedAt.Equal(w.CreatedAt) {
		t.Errorf("%s: CreatedAt = %v, quería %v", what, g.CreatedAt, w.CreatedAt)
	}
	if !g.UpdatedAt.Equal(w.UpdatedAt) {
		t.Errorf("%s: UpdatedAt = %v, quería %v", what, g.UpdatedAt, w.UpdatedAt)
	}
	if g.HasSecret != w.HasSecret || g.SecretSeal != w.SecretSeal {
		t.Errorf("%s: sobre del secreto (HasSecret=%v, marca %q), quería (HasSecret=%v, marca %q)",
			what, g.HasSecret, g.SecretSeal, w.HasSecret, w.SecretSeal)
	}
	if got.secretFound != want.secretFound || got.secret != want.secret {
		t.Errorf("%s: el secreto que da el puerto cambió (found=%v, quería %v)", what, got.secretFound, want.secretFound)
	}
}

// requireSameOutbox afirma que got es la misma fila que want: las DIEZ columnas. El payload se
// compara por su contenido y los instantes con Equal.
func requireSameOutbox(t *testing.T, what string, got, want OutboxRow) {
	t.Helper()
	if got.ID != want.ID || got.TenantID != want.TenantID || got.Kind != want.Kind {
		t.Errorf("%s: (id, tenant, kind) = (%d, %q, %q), quería (%d, %q, %q)",
			what, got.ID, got.TenantID, got.Kind, want.ID, want.TenantID, want.Kind)
	}
	if got.Status != want.Status {
		t.Errorf("%s: status = %q, quería %q", what, got.Status, want.Status)
	}
	if got.Attempts != want.Attempts {
		t.Errorf("%s: attempts = %d, quería %d", what, got.Attempts, want.Attempts)
	}
	if got.LastError != want.LastError {
		t.Errorf("%s: last_error = %q, quería %q", what, got.LastError, want.LastError)
	}
	if !sameJSON(got.Payload, want.Payload) {
		t.Errorf("%s: payload = %s, quería %s", what, got.Payload, want.Payload)
	}
	for _, f := range []struct {
		name      string
		got, want time.Time
	}{
		{"next_attempt_at", got.NextAttemptAt, want.NextAttemptAt},
		{"created_at", got.CreatedAt, want.CreatedAt},
		{"claimed_at", got.ClaimedAt, want.ClaimedAt},
	} {
		if !f.got.Equal(f.want) {
			t.Errorf("%s: %s = %v, quería %v", what, f.name, f.got, f.want)
		}
	}
}

// sameJSON dice si dos payloads tienen el mismo contenido, sin mirar espacios ni el orden de sus
// claves. Dos payloads que no son JSON no son iguales a nada.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// witness son los testigos de un caso: lo que NINGUNA operación del caso debe tocar.
type witness struct {
	// rows son dos entregas, una de TenantA y otra de TenantB, en espera de reintento dentro de
	// 24 h: no son reclamables (no han vencido) ni rescatables (no están en vuelo).
	rows []OutboxRow
	// tenant es la integración de TenantB, con su secreto.
	tenant tenantSnapshot
}

// seedWitness siembra los testigos por el propio puerto, guarda su marca de estado y deja pasar el
// reloj. Tiene que llamarse con la cola vacía: reclama todo lo que hay para poder aparcarlo.
func seedWitness(t *testing.T, m Montaje) witness {
	t.Helper()
	ctx := context.Background()
	a := enqueue(t, m, m.TenantA, `{"witness":"a"}`)
	b := enqueue(t, m, m.TenantB, `{"witness":"b"}`)
	parked := wholeSeconds(m.Now(t)).Add(24 * time.Hour)
	for _, claim := range claimExactly(t, m, a, b) {
		if err := m.Store.MarkWebhookFailed(ctx, claim, parked, witnessError); err != nil {
			t.Fatalf("aparcar la entrega testigo %d: %v", claim.ID, err)
		}
	}
	upsert(t, m, integrations.TenantIntegration{
		TenantID: m.TenantB, CatalogAdapter: "local", EventsAdapter: "webhook",
		EndpointURL: "https://witness.example/hook", Enabled: true,
	}, witnessSecret)

	w := witness{rows: []OutboxRow{row(t, m, a), row(t, m, b)}, tenant: takeTenant(t, m, m.TenantB)}
	for _, r := range w.rows {
		if r.Status != integrations.StatusPending || r.Attempts != 1 || !r.NextAttemptAt.Equal(parked) || !r.ClaimedAt.IsZero() {
			t.Fatalf("la entrega testigo %d no quedó aparcada: %+v", r.ID, r)
		}
	}
	if !w.tenant.found || !w.tenant.secretFound || w.tenant.secret != witnessSecret {
		t.Fatalf("la integración testigo de TenantB no quedó con su secreto: %+v", w.tenant.row)
	}
	m.Advance(t)
	return w
}

// requireUntouched afirma que los testigos siguen EXACTAMENTE como se sembraron.
func (w witness) requireUntouched(t *testing.T, m Montaje) {
	t.Helper()
	for _, want := range w.rows {
		requireSameOutbox(t, fmt.Sprintf("la entrega testigo %d (de %s)", want.ID, want.TenantID), row(t, m, want.ID), want)
	}
	requireSameTenant(t, "la integración testigo (TenantB)", takeTenant(t, m, m.TenantB), w.tenant)
}

// wholeSeconds recorta un instante al segundo: las fechas que la suite ESCRIBE no llevan
// fracciones, para que Postgres (microsegundos) las devuelva tal cual.
func wholeSeconds(at time.Time) time.Time { return at.Truncate(time.Second) }

// enqueue encola una entrega de la suite o falla el test.
func enqueue(t *testing.T, m Montaje, tenant, payload string) int64 {
	t.Helper()
	id, err := m.Store.EnqueueWebhook(context.Background(), tenant, kindPush, json.RawMessage(payload))
	if err != nil {
		t.Fatalf("EnqueueWebhook(%q): error inesperado %v", tenant, err)
	}
	return id
}

// claim reclama un lote o falla el test.
func claim(t *testing.T, m Montaje, limit int) []integrations.WebhookOutbox {
	t.Helper()
	batch, err := m.Store.ClaimWebhookBatch(context.Background(), limit)
	if err != nil {
		t.Fatalf("ClaimWebhookBatch(%d): error inesperado %v", limit, err)
	}
	return batch
}

// claimExactly reclama con un límite holgado y exige que salgan JUSTO esas filas; las devuelve en
// el orden de ids pedido.
func claimExactly(t *testing.T, m Montaje, ids ...int64) []integrations.WebhookOutbox {
	t.Helper()
	batch := claim(t, m, len(ids)+10)
	requireIDs(t, "ClaimWebhookBatch", batch, ids...)
	out := make([]integrations.WebhookOutbox, 0, len(ids))
	for _, id := range ids {
		for _, c := range batch {
			if c.ID == id {
				out = append(out, c)
			}
		}
	}
	return out
}

// claimOne encola una entrega de TenantA con el payload de muestra y la reclama.
func claimOne(t *testing.T, m Montaje) integrations.WebhookOutbox {
	t.Helper()
	id := enqueue(t, m, m.TenantA, samplePayload)
	return claimExactly(t, m, id)[0]
}

// requireIDs afirma que el lote trae exactamente esas filas (sin mirar el orden: ver Contrato).
func requireIDs(t *testing.T, what string, batch []integrations.WebhookOutbox, want ...int64) {
	t.Helper()
	got := make([]int64, 0, len(batch))
	for _, c := range batch {
		got = append(got, c.ID)
	}
	slices.Sort(got)
	sorted := slices.Clone(want)
	slices.Sort(sorted)
	if !slices.Equal(got, sorted) {
		t.Fatalf("%s devolvió las filas %v, quería %v", what, got, sorted)
	}
}

// row lee la fila entera de la entrega o falla el test si no existe.
func row(t *testing.T, m Montaje, id int64) OutboxRow {
	t.Helper()
	r, found := m.OutboxRow(t, id)
	if !found {
		t.Fatalf("la entrega %d no existe en la cola", id)
	}
	return r
}

// upsert guarda la integración o falla el test.
func upsert(t *testing.T, m Montaje, ti integrations.TenantIntegration, secret string) {
	t.Helper()
	if err := m.Store.UpsertTenantIntegration(context.Background(), ti, secret); err != nil {
		t.Fatalf("UpsertTenantIntegration(%q): error inesperado %v", ti.TenantID, err)
	}
}

// recoverOrphans rescata con ese lease y exige esa cuenta.
func recoverOrphans(t *testing.T, m Montaje, lease time.Duration, want int) {
	t.Helper()
	n, err := m.Store.RecoverOrphanDeliveries(context.Background(), lease)
	if err != nil {
		t.Fatalf("RecoverOrphanDeliveries(%v): error inesperado %v", lease, err)
	}
	if n != want {
		t.Fatalf("RecoverOrphanDeliveries(%v) recuperó %d entregas, quería %d", lease, n, want)
	}
}

// requireClaimLost afirma que err es el rechazo de la valla: envuelve ErrClaimLost y su texto es,
// byte a byte, «integrations: entrega <id> hacia <qué>: » + el del centinela.
func requireClaimLost(t *testing.T, err error, id int64, what string) {
	t.Helper()
	if !errors.Is(err, integrations.ErrClaimLost) {
		t.Fatalf("err = %v, quería uno que envuelva ErrClaimLost", err)
	}
	want := fmt.Sprintf("integrations: entrega %d hacia %s: integrations: el claim de la entrega ya no es vigente", id, what)
	if err.Error() != want {
		t.Errorf("texto del rechazo =\n%s\nquería, byte a byte:\n%s", err, want)
	}
}
