package integrationshelpertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// Los secretos de la suite. No son credenciales: son cadenas inventadas.
const (
	firstSecret   = "secreto-uno-de-la-suite-xkq" // #nosec G101 -- material de test
	rotatedSecret = "secreto-DOS-de-la-suite-zwv" // #nosec G101 -- material de test
	otherSecret   = "secreto-del-otro-tenant-pqr" // #nosec G101 -- material de test
)

// webhookConfig es la integración «encendida y completa» de un tenant, lista para un upsert.
func webhookConfig(tenant string) integrations.TenantIntegration {
	return integrations.TenantIntegration{
		TenantID: tenant, CatalogAdapter: "local", EventsAdapter: "webhook",
		EndpointURL: "https://bridge.example/hook", Enabled: true,
	}
}

// requireTenant afirma la marca de estado de la integración del tenant tras una escritura: la
// configuración de cfg, ese secreto ("" = sin secreto) y fechas puestas por el almacén. Devuelve
// la marca para que el caso siga comparando contra ella.
func requireTenant(t *testing.T, m Montaje, what string, cfg integrations.TenantIntegration, secret string) tenantSnapshot {
	t.Helper()
	got := takeTenant(t, m, cfg.TenantID)
	if !got.found {
		t.Fatalf("%s: el tenant %q no tiene fila de integración", what, cfg.TenantID)
	}
	r := got.row
	stored := integrations.TenantIntegration{
		TenantID: r.TenantID, CatalogAdapter: r.CatalogAdapter, EventsAdapter: r.EventsAdapter,
		EndpointURL: r.EndpointURL, Enabled: r.Enabled,
	}
	if stored != cfg {
		t.Errorf("%s: configuración guardada = %+v, quería %+v", what, stored, cfg)
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		t.Errorf("%s: (CreatedAt, UpdatedAt) = (%v, %v), quería dos fechas puestas y en orden", what, r.CreatedAt, r.UpdatedAt)
	}
	requireSecret(t, what, got, secret)
	return got
}

// requireSecret afirma que la marca de estado tiene ESE secreto ("" = ninguno), por las tres vías
// que lo dejan ver: la fila guardada, el puerto y la marca del sobre, que nunca lo contiene.
func requireSecret(t *testing.T, what string, got tenantSnapshot, secret string) {
	t.Helper()
	has := secret != ""
	if got.row.HasSecret != has || got.secretFound != has {
		t.Errorf("%s: hay secreto según (la fila=%v, el puerto=%v), quería %v", what, got.row.HasSecret, got.secretFound, has)
	}
	if got.secret != secret {
		t.Errorf("%s: el secreto que da el puerto no es el del caso", what)
	}
	if (got.row.SecretSeal != "") != has {
		t.Errorf("%s: marca del sobre %q: tiene que haber marca si y solo si hay secreto (%v)", what, got.row.SecretSeal, has)
	}
	if has && strings.Contains(got.row.SecretSeal, secret) {
		t.Errorf("%s: FUGA: la marca del sobre contiene el secreto en claro", what)
	}
}

// caseTenantNoRow: un tenant sin fila no tiene integración (found=false, valor cero, sin error) ni
// secreto. Equivale a local/local.
func caseTenantNoRow(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	ctx := context.Background()
	ti, found, err := m.Store.GetTenantIntegration(ctx, m.TenantA)
	if err != nil || found || ti != (integrations.TenantIntegration{}) {
		t.Errorf("GetTenantIntegration sin fila = (%+v, %v, %v), quería (cero, false, nil)", ti, found, err)
	}
	secret, found, err := m.Store.GetTenantSecret(ctx, m.TenantA)
	if err != nil || found || secret != "" {
		t.Errorf("GetTenantSecret sin fila = (found=%v, err=%v, vacío=%v), quería (false, nil, vacío)", found, err, secret == "")
	}
	w.requireUntouched(t, m)
}

// caseTenantCreate: el alta con secreto guarda la configuración y el secreto; la lectura de la
// integración dice HasSecret y NUNCA lo trae (ningún campo lo contiene); el secreto solo sale por
// GetTenantSecret. En el alta, created_at y updated_at son el mismo instante.
func caseTenantCreate(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	cfg := webhookConfig(m.TenantA)
	cfg.CatalogAdapter = "http"
	upsert(t, m, cfg, firstSecret)

	got := requireTenant(t, m, "el alta con secreto", cfg, firstSecret)
	if !got.row.CreatedAt.Equal(got.row.UpdatedAt) {
		t.Errorf("en el alta (CreatedAt, UpdatedAt) = (%v, %v), quería el mismo instante", got.row.CreatedAt, got.row.UpdatedAt)
	}
	ti, _, err := m.Store.GetTenantIntegration(context.Background(), m.TenantA)
	if err != nil {
		t.Fatalf("GetTenantIntegration: error inesperado %v", err)
	}
	for _, field := range []string{ti.TenantID, ti.CatalogAdapter, ti.EventsAdapter, ti.EndpointURL} {
		if strings.Contains(field, firstSecret) {
			t.Errorf("FUGA: GetTenantIntegration devuelve el secreto en un campo: %q", field)
		}
	}
	w.requireUntouched(t, m)
}

// caseTenantNoSecret: el alta sin secreto crea la fila sin sobre (HasSecret=false, GetTenantSecret
// no encuentra nada), y un endpoint vacío se queda vacío.
func caseTenantNoSecret(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	cfg := integrations.TenantIntegration{TenantID: m.TenantA, CatalogAdapter: "local", EventsAdapter: "local"}
	upsert(t, m, cfg, "")
	requireTenant(t, m, "el alta sin secreto", cfg, "")
	w.requireUntouched(t, m)
}

// caseTenantKeepsSecret: reconfigurar con secret "" NO toca el secreto guardado: sigue saliendo el
// mismo y el sobre no se reescribe. Es la regla en la que se apoya el PUT para cambiar el endpoint
// sin reenviar un valor que el GET no devuelve.
func caseTenantKeepsSecret(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	cfg := webhookConfig(m.TenantA)
	upsert(t, m, cfg, firstSecret)
	before := requireTenant(t, m, "el alta", cfg, firstSecret)
	m.Advance(t)

	cfg.EndpointURL, cfg.Enabled = "https://other.example/hook", false
	upsert(t, m, cfg, "")

	after := requireTenant(t, m, "la reconfiguración sin secreto", cfg, firstSecret)
	if after.row.SecretSeal != before.row.SecretSeal {
		t.Errorf("el sobre del secreto se reescribió (marca %q → %q) en un upsert sin secreto", before.row.SecretSeal, after.row.SecretSeal)
	}
	w.requireUntouched(t, m)
}

// caseTenantRotates: un secreto nuevo sustituye al anterior (el viejo ya no sale) sin tocar la
// fecha de alta.
func caseTenantRotates(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	cfg := webhookConfig(m.TenantA)
	upsert(t, m, cfg, firstSecret)
	before := requireTenant(t, m, "el alta", cfg, firstSecret)
	m.Advance(t)

	upsert(t, m, cfg, rotatedSecret)

	after := requireTenant(t, m, "la rotación", cfg, rotatedSecret)
	if !after.row.CreatedAt.Equal(before.row.CreatedAt) {
		t.Errorf("la rotación movió CreatedAt: %v → %v", before.row.CreatedAt, after.row.CreatedAt)
	}
	if !after.row.UpdatedAt.After(before.row.UpdatedAt) {
		t.Errorf("la rotación no refrescó UpdatedAt: %v → %v", before.row.UpdatedAt, after.row.UpdatedAt)
	}
	w.requireUntouched(t, m)
}

// caseTenantReplacesConfig: las cuatro columnas de configuración se REEMPLAZAN con lo que trae el
// upsert, no se mezclan con lo anterior: un endpoint vacío borra el endpoint, y enabled=false
// apaga. El secreto, que no viene, sigue.
func caseTenantReplacesConfig(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	upsert(t, m, webhookConfig(m.TenantA), firstSecret)
	m.Advance(t)

	cfg := integrations.TenantIntegration{TenantID: m.TenantA, CatalogAdapter: "webhook", EventsAdapter: "local"}
	upsert(t, m, cfg, "")
	requireTenant(t, m, "la configuración reemplazada", cfg, firstSecret)
	w.requireUntouched(t, m)
}

// caseTenantReadOnlyFields: HasSecret, CreatedAt y UpdatedAt son de lectura. Lo que traigan en el
// upsert se ignora: no se puede declarar un secreto que no se manda, ni fechar el alta.
func caseTenantReadOnlyFields(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	cfg := webhookConfig(m.TenantA)
	lie := cfg
	lie.HasSecret = true
	lie.CreatedAt = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	lie.UpdatedAt = time.Date(2002, 2, 2, 0, 0, 0, 0, time.UTC)
	started := m.Now(t)
	upsert(t, m, lie, "")

	got := requireTenant(t, m, "el alta con campos de lectura rellenos", cfg, "")
	if got.row.CreatedAt.Before(wholeSeconds(started)) || got.row.UpdatedAt.Before(wholeSeconds(started)) {
		t.Errorf("(CreatedAt, UpdatedAt) = (%v, %v): son las fechas del llamante, no las del almacén (%v)",
			got.row.CreatedAt, got.row.UpdatedAt, started)
	}
	w.requireUntouched(t, m)
}

// caseTenantSameConfig: repetir el mismo upsert deja la fila igual salvo updated_at, que se
// refresca SIEMPRE (dice cuándo se guardó, no cuándo cambió); created_at es el del alta.
func caseTenantSameConfig(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	cfg := webhookConfig(m.TenantA)
	upsert(t, m, cfg, firstSecret)
	before := requireTenant(t, m, "el alta", cfg, firstSecret)
	m.Advance(t)

	upsert(t, m, cfg, "")

	after := requireTenant(t, m, "el segundo upsert idéntico", cfg, firstSecret)
	if !after.row.UpdatedAt.After(before.row.UpdatedAt) {
		t.Errorf("UpdatedAt no se refrescó: %v → %v", before.row.UpdatedAt, after.row.UpdatedAt)
	}
	want := before
	want.row.UpdatedAt = after.row.UpdatedAt
	requireSameTenant(t, "la fila tras el segundo upsert idéntico", after, want)
	w.requireUntouched(t, m)
}

// caseTenantDelete: borrar quita la fila y, con ella, el secreto (es la única forma de retirarlo).
// Un alta posterior sin secreto nace SIN secreto y con fecha de alta nueva: nada de la fila
// borrada vuelve.
func caseTenantDelete(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	cfg := webhookConfig(m.TenantA)
	upsert(t, m, cfg, firstSecret)
	before := requireTenant(t, m, "el alta", cfg, firstSecret)

	if err := m.Store.DeleteTenantIntegration(context.Background(), m.TenantA); err != nil {
		t.Fatalf("DeleteTenantIntegration: error inesperado %v", err)
	}
	if gone := takeTenant(t, m, m.TenantA); gone.found || gone.secretFound {
		t.Fatalf("tras borrar: la fila existe=%v y el secreto existe=%v; quería que no quedara nada", gone.found, gone.secretFound)
	}
	m.Advance(t)

	upsert(t, m, cfg, "")
	reborn := requireTenant(t, m, "el alta tras el borrado", cfg, "")
	if !reborn.row.CreatedAt.After(before.row.CreatedAt) {
		t.Errorf("el alta tras el borrado conserva la fecha de la fila borrada: %v (antes %v)", reborn.row.CreatedAt, before.row.CreatedAt)
	}
	w.requireUntouched(t, m)
}

// caseTenantDeleteNothing: borrar la integración de un tenant que no tiene no es un error y no
// crea nada.
func caseTenantDeleteNothing(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	if err := m.Store.DeleteTenantIntegration(context.Background(), m.TenantA); err != nil {
		t.Errorf("DeleteTenantIntegration sin fila: error inesperado %v", err)
	}
	if got := takeTenant(t, m, m.TenantA); got.found {
		t.Errorf("borrar lo que no había dejó una fila: %+v", got.row)
	}
	w.requireUntouched(t, m)
}

// caseTenantIsolation: cada tenant tiene SU integración y SU secreto. Crear, reconfigurar, rotar y
// borrar la de TenantA no mueve la de TenantB —que tiene la misma forma—, y TenantA nunca lee el
// secreto de TenantB.
func caseTenantIsolation(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	ctx := context.Background()
	if secret, found, err := m.Store.GetTenantSecret(ctx, m.TenantA); err != nil || found || secret != "" {
		t.Fatalf("TenantA, sin integración, lee un secreto (found=%v, err=%v): es el de otro tenant", found, err)
	}

	cfg := webhookConfig(m.TenantA)
	upsert(t, m, cfg, otherSecret)
	requireTenant(t, m, "la integración de TenantA", cfg, otherSecret)
	w.requireUntouched(t, m)

	cfg.Enabled = false
	upsert(t, m, cfg, rotatedSecret)
	w.requireUntouched(t, m)

	if err := m.Store.DeleteTenantIntegration(ctx, m.TenantA); err != nil {
		t.Fatalf("DeleteTenantIntegration: error inesperado %v", err)
	}
	w.requireUntouched(t, m)
}

// caseTenantLeavesQueue: la configuración y la cola son tablas distintas: dar de alta, cambiar o
// borrar la integración de un tenant no toca sus entregas, ni las que esperan ni las que vuelan.
func caseTenantLeavesQueue(t *testing.T, m Montaje) {
	w := seedWitness(t, m)
	inFlight := claimOne(t, m)
	pending := enqueue(t, m, m.TenantA, samplePayload)
	rows := []OutboxRow{row(t, m, inFlight.ID), row(t, m, pending)}

	upsert(t, m, webhookConfig(m.TenantA), firstSecret)
	if err := m.Store.DeleteTenantIntegration(context.Background(), m.TenantA); err != nil {
		t.Fatalf("DeleteTenantIntegration: error inesperado %v", err)
	}
	for _, want := range rows {
		requireSameOutbox(t, "la entrega de un tenant cuya integración se creó y se borró", row(t, m, want.ID), want)
	}
	w.requireUntouched(t, m)
}
