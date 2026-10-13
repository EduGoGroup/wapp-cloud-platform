package intakeshelpertest

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// Las zonas de las siembras. Las etiquetas no deciden ningún orden.
var (
	zoneProvidencia = intakes.ShippingZone{Code: "prov", Label: "Providencia", Price: 3000}
	zoneNunoa       = intakes.ShippingZone{Code: "nun", Label: "Ñuñoa", Price: 4500}
)

// zoneLine es la línea de envío que dicta una zona resuelta.
func zoneLine(z intakes.ShippingZone) intakes.Item {
	return intakes.Item{SKU: intakes.ShippingSKU, Label: "Envío — " + z.Label, Qty: 1, UnitPrice: z.Price}
}

// ensureShipping garantiza la línea o falla el test.
func ensureShipping(t *testing.T, m Montaje, r ref, policy intakes.ShippingPolicy) {
	t.Helper()
	if err := m.Store.EnsureShippingLine(bg(), r.tenant, r.id, policy); err != nil {
		t.Fatalf("EnsureShippingLine(%s): error inesperado %v", r.id, err)
	}
}

// caseShippingIdempotent: aplicada tres veces deja UNA línea y el total cuadrado; la segunda y la
// tercera no escriben nada, tampoco UpdatedAt.
func caseShippingIdempotent(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	m.SetShippingZones(t, m.TenantA, zoneProvidencia)
	r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	want := take(t, m, r).clone()
	m.Advance(t)

	ensureShipping(t, m, r, intakes.ShippingAlways)
	got := take(t, m, r)
	want.detail.Items = append(want.detail.Items, zoneLine(zoneProvidencia))
	want.detail.Total = 3008
	adoptAddedAt(t, "la primera vez", got, &want, 3)
	adoptRefreshedUpdatedAt(t, "la primera vez", got, &want)
	requireSame(t, "la primera vez", got, want)

	for _, policy := range []intakes.ShippingPolicy{intakes.ShippingAlways, intakes.ShippingOnlyIfZones} {
		m.Advance(t)
		ensureShipping(t, m, r, policy)
		requireSame(t, "al repetir", take(t, m, r), want)
	}
	w.requireUntouched(t, m)
}

// caseShippingPolicyWithoutZones: el contraste de las dos políticas sobre el MISMO tenant sin
// configurar. El cierre del carrito no toca nada; el presupuesto pone la línea «por confirmar» a
// 0, que no mueve el total. Y lo que cuenta son las zonas CONFIGURADAS, no las resolubles: con dos
// zonas la política del carrito sí la pone, por confirmar.
func caseShippingPolicyWithoutZones(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	m.SetShippingZones(t, m.TenantB, zoneProvidencia) // las zonas de OTRO tenant no cuentan
	pending := intakes.Item{SKU: intakes.ShippingSKU, Label: intakes.ShippingPendingLabel, Qty: 1}

	r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	want := take(t, m, r).clone()
	m.Advance(t)
	ensureShipping(t, m, r, intakes.ShippingOnlyIfZones)
	requireSame(t, "sin zonas, con la política del carrito", take(t, m, r), want)

	ensureShipping(t, m, r, intakes.ShippingAlways)
	got := take(t, m, r)
	want.detail.Items = append(want.detail.Items, pending)
	adoptAddedAt(t, "sin zonas, con la política del presupuesto", got, &want, 3)
	adoptRefreshedUpdatedAt(t, "sin zonas, con la política del presupuesto", got, &want)
	requireSame(t, "sin zonas, con la política del presupuesto", got, want)

	m.SetShippingZones(t, m.TenantA, zoneProvidencia, zoneNunoa)
	other := seed(t, m, m.TenantA, intakes.StatusOpen, 2, eventCancelled, customerLines()...)
	want = take(t, m, other).clone()
	m.Advance(t)
	ensureShipping(t, m, other, intakes.ShippingOnlyIfZones)
	got = take(t, m, other)
	want.detail.Items = append(want.detail.Items, pending)
	adoptAddedAt(t, "con dos zonas", got, &want, 3)
	adoptRefreshedUpdatedAt(t, "con dos zonas", got, &want)
	requireSame(t, "con dos zonas, con la política del carrito", got, want)
	w.requireUntouched(t, m)
}

// caseShippingZoneChange: la línea ya existe cuando el tenant cambia de zona o de tarifa. Se
// sustituye EN SU SITIO —misma posición, mismo AddedAt—: ni dos envíos sumados ni el reparto viejo.
func caseShippingZoneChange(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	m.SetShippingZones(t, m.TenantA, zoneProvidencia)
	r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	ensureShipping(t, m, r, intakes.ShippingAlways)
	want := take(t, m, r).clone()

	cheaper := zoneNunoa
	cheaper.Price = 1000
	for _, c := range []struct {
		name  string
		zone  intakes.ShippingZone
		total float64
	}{
		{"cambio de zona", zoneNunoa, 4508},
		{"cambio de tarifa", cheaper, 1008},
	} {
		m.SetShippingZones(t, m.TenantA, c.zone)
		m.Advance(t)
		ensureShipping(t, m, r, intakes.ShippingAlways)
		got := take(t, m, r)
		line := zoneLine(c.zone)
		line.AddedAt = want.detail.Items[3].AddedAt // la de la línea que ya estaba
		want.detail.Items[3] = line
		want.detail.Total = c.total
		adoptRefreshedUpdatedAt(t, c.name, got, &want)
		requireSame(t, c.name, got, want)
	}
	w.requireUntouched(t, m)
}

// caseShippingKeepsOwnerPrice: sin zona resuelta el precio lo pone el dueño, y nada lo devuelve a
// 0: ni garantizar la línea otra vez, ni re-presupuestar (`confirmed → pending_approval`, que es
// rutina), ni que el tenant tenga varias zonas sin resolver.
func caseShippingKeepsOwnerPrice(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	lines := append(customerLines(), shippingLine(intakes.ShippingPendingLabel, 4500))
	r := seed(t, m, m.TenantA, intakes.StatusConfirmed, 1, eventCancelled, lines...)
	want := take(t, m, r).clone()
	m.Advance(t)

	ensureShipping(t, m, r, intakes.ShippingAlways)
	requireSame(t, "al garantizar la línea", take(t, m, r), want)

	m.SetShippingZones(t, m.TenantA, zoneProvidencia, zoneNunoa)
	ensureShipping(t, m, r, intakes.ShippingAlways)
	requireSame(t, "con dos zonas sin resolver", take(t, m, r), want)

	updateStatus(t, m, r, intakes.StatusPendingApproval, intakes.StoredVariants(intakes.StatusConfirmed))
	got := take(t, m, r)
	want.stored, want.detail.Status = intakes.StatusPendingApproval, intakes.StatusPendingApproval
	adoptRefreshedUpdatedAt(t, "al re-presupuestar", got, &want)
	requireSame(t, "al re-presupuestar", got, want)
	w.requireUntouched(t, m)
}

func caseShippingNotFound(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	m.SetShippingZones(t, m.TenantA, zoneProvidencia)
	for name, target := range map[string]ref{
		"de otro tenant":    {tenant: m.TenantA, id: w.foreign().id},
		"inexistente":       {tenant: m.TenantA, id: uuid.NewString()},
		"id que no es UUID": {tenant: m.TenantA, id: "no-soy-un-uuid"},
	} {
		err := m.Store.EnsureShippingLine(bg(), target.tenant, target.id, intakes.ShippingAlways)
		if !errors.Is(err, intakes.ErrNotFound) {
			t.Errorf("EnsureShippingLine de una solicitud %s: err = %v, quería ErrNotFound", name, err)
		}
	}
	w.requireUntouched(t, m)
}

// caseShippingZonesRead: las zonas se leen como se configuraron, en su orden, por tenant.
func caseShippingZonesRead(t *testing.T, m Montaje) {
	read := func(what, tenant string, want ...intakes.ShippingZone) {
		t.Helper()
		got, err := m.Store.ShippingZones(bg(), tenant)
		if err != nil {
			t.Fatalf("ShippingZones (%s): error inesperado %v", what, err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("ShippingZones (%s) = %+v, quería %+v", what, got, want)
		}
	}
	read("tenant sin configurar", m.TenantA)
	m.SetShippingZones(t, m.TenantA, zoneNunoa, zoneProvidencia)
	read("dos zonas, en su orden", m.TenantA, zoneNunoa, zoneProvidencia)
	read("el otro tenant", m.TenantB)
	m.SetShippingZones(t, m.TenantA, zoneProvidencia)
	read("tras sustituirlas", m.TenantA, zoneProvidencia)
	m.SetShippingZones(t, m.TenantA)
	read("tras quitarlas", m.TenantA)
}

// caseNotifySettings: un tenant sin configurar no es un error, es un tenant recién nacido — sin
// plantilla y con el plazo por defecto.
func caseNotifySettings(t *testing.T, m Montaje) {
	read := func(what, tenant string, want intakes.NotifySettings) {
		t.Helper()
		got, err := m.Store.NotifySettings(bg(), tenant)
		if err != nil {
			t.Fatalf("NotifySettings (%s): error inesperado %v", what, err)
		}
		if got != want {
			t.Errorf("NotifySettings (%s) = %+v, quería %+v", what, got, want)
		}
	}
	defaults := intakes.NotifySettings{DepositDueDays: intakes.DefaultDepositDueDays}
	if defaults.DepositDueDays != 3 {
		t.Errorf("DefaultDepositDueDays = %d, quería 3 (el DEFAULT de la columna)", defaults.DepositDueDays)
	}
	read("tenant sin configurar", m.TenantA, defaults)
	const template = "Transfiere la seña a la cuenta 123 antes del {fecha_limite}"
	m.SetDepositTemplate(t, m.TenantA, template, 5)
	read("tenant configurado", m.TenantA, intakes.NotifySettings{DepositTemplate: template, DepositDueDays: 5})
	read("el otro tenant", m.TenantB, defaults)
	read("un tenant desconocido", uuid.NewString(), defaults)
}
