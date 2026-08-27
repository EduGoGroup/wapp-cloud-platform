package intakes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// zonaÚnica es la configuración más común de un tenant que cobra envío: una
// tarifa plana. Es el único caso en que wApp puede poner precio sola (v1).
func zonaÚnica() intakes.ShippingZone {
	return intakes.ShippingZone{Code: "z1", Label: "Providencia", Price: 3000}
}

// TestDesiredShippingLine_UnaZonaPoneElPrecio: sin nada que elegir, la tarifa de
// la zona va cobrada en la línea.
func TestDesiredShippingLine_UnaZonaPoneElPrecio(t *testing.T) {
	got := intakes.DesiredShippingLine([]intakes.ShippingZone{zonaÚnica()})

	if !got.Priced || got.UnitPrice != 3000 {
		t.Fatalf("línea=%+v; con UNA zona el precio lo pone la configuración", got)
	}
	if got.Label != "Envío — Providencia" {
		t.Fatalf("label=%q; quiero la zona nombrada en la línea", got.Label)
	}
}

// TestDesiredShippingLine_SinZonasEsPorConfirmar: el estado de arranque de
// cualquier tenant. La línea existe, vale 0 y su etiqueta ES la marca que el dueño
// lee para precificarla (D-041.11).
func TestDesiredShippingLine_SinZonasEsPorConfirmar(t *testing.T) {
	got := intakes.DesiredShippingLine(nil)

	if got.Priced || got.UnitPrice != 0 || got.Label != intakes.ShippingPendingLabel {
		t.Fatalf("línea=%+v; quiero «%s» a 0 sin precio de configuración",
			got, intakes.ShippingPendingLabel)
	}
}

// TestDesiredShippingLine_VariasZonasNoSeAdivinan: con dos zonas wApp NO elige la
// primera. La zona del cliente no se pregunta en este plan, y cobrar la tarifa de
// Providencia a alguien de Puente Alto porque estaba antes en la lista es un cobro
// mal hecho, no un valor por defecto.
func TestDesiredShippingLine_VariasZonasNoSeAdivinan(t *testing.T) {
	got := intakes.DesiredShippingLine([]intakes.ShippingZone{
		zonaÚnica(),
		{Code: "z2", Label: "Puente Alto", Price: 5000},
	})

	if got.Priced || got.UnitPrice != 0 || got.Label != intakes.ShippingPendingLabel {
		t.Fatalf("línea=%+v; con varias zonas la precifica el dueño", got)
	}
}

// TestDesiredShippingLine_ZonaSinNombreNoCuenta: una zona que no se puede nombrar
// no puede dar una línea legible ("Envío — "), así que no resuelve nada; la que sí
// tiene nombre queda sola y manda.
func TestDesiredShippingLine_ZonaSinNombreNoCuenta(t *testing.T) {
	got := intakes.DesiredShippingLine([]intakes.ShippingZone{
		{Price: 9999},
		{Code: "z9", Price: 2000},
	})

	if !got.Priced || got.UnitPrice != 2000 || got.Label != "Envío — z9" {
		t.Fatalf("línea=%+v; la zona sin nombre ni código no cuenta y la otra se nombra por su código", got)
	}
}

// TestParseShippingZones: vacío es «sin zonas» (el DEFAULT de la columna); un blob
// ilegible es un error que se propaga, no un silencio que deja de cobrar el envío.
func TestParseShippingZones(t *testing.T) {
	if zones, err := intakes.ParseShippingZones(nil); err != nil || zones != nil {
		t.Fatalf("vacío: zonas=%v err=%v; quiero nil sin error", zones, err)
	}
	zones, err := intakes.ParseShippingZones([]byte(`[{"code":"z1","label":"Providencia","price":3000}]`))
	if err != nil || len(zones) != 1 || zones[0].Price != 3000 {
		t.Fatalf("zonas=%+v err=%v", zones, err)
	}
	if _, err := intakes.ParseShippingZones([]byte(`{"z1":3000}`)); err == nil {
		t.Fatal("un shipping_zones ilegible tiene que fallar, no devolver «sin zonas»")
	}
}

// TestShippingLine_SupersedesRespetaElPrecioHumano: la mitad delicada de la
// idempotencia. Con zona, la configuración manda y pisa lo viejo; sin zona, lo que
// hay es el precio que puso el dueño y no se toca (v1: «el dueño precifica»).
func TestShippingLine_SupersedesRespetaElPrecioHumano(t *testing.T) {
	deZona := intakes.DesiredShippingLine([]intakes.ShippingZone{zonaÚnica()})
	igual := intakes.Item{SKU: intakes.ShippingSKU, Label: "Envío — Providencia", Qty: 1, UnitPrice: 3000}

	if deZona.Supersedes(igual) {
		t.Error("la línea ya es la de la zona vigente: no hay nada que escribir")
	}
	vieja := intakes.Item{SKU: intakes.ShippingSKU, Label: "Envío — Providencia", Qty: 1, UnitPrice: 2000}
	if !deZona.Supersedes(vieja) {
		t.Error("cambió la tarifa de la zona: la línea vieja no puede sobrevivir")
	}

	porConfirmar := intakes.DesiredShippingLine(nil)
	precificadaAMano := intakes.Item{SKU: intakes.ShippingSKU, Label: intakes.ShippingPendingLabel, Qty: 1, UnitPrice: 2500}
	if porConfirmar.Supersedes(precificadaAMano) {
		t.Error("sin zona no hay autoridad de configuración: pisar aquí borra el precio del dueño")
	}
}

// solicitudPorAprobar siembra una solicitud `open` de una sola línea (1 × 18000) y
// devuelve el store y el servicio sobre él.
func solicitudPorAprobar(t *testing.T) (*intakes.MemoryStore, *intakes.Service) {
	t.Helper()
	st := intakes.NewMemoryStore()
	st.Add(tenantA, intakes.Intake{
		ID:        intakeConEnvío,
		ContactID: "contacto-opaco-1",
		SessionID: "sess-a",
		Status:    intakes.StatusOpen,
		Total:     18000,
		CreatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}, intakes.Item{SKU: "torta-v1", Label: "Torta 10-12 porciones", Qty: 1, UnitPrice: 18000})
	return st, intakes.NewService(st)
}

const intakeConEnvío = "22222222-2222-2222-2222-222222222222"

// líneasDeEnvío cuenta las líneas de envío de una solicitud y devuelve la primera.
// CONTAR es el punto: el criterio de T4.3 no es «hay línea», es «hay UNA».
func líneasDeEnvío(t *testing.T, svc *intakes.Service, intakeID string) (int, intakes.Item, float64) {
	t.Helper()
	d, err := svc.Get(context.Background(), tenantA, intakeID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var (
		n       int
		primera intakes.Item
	)
	for _, it := range d.Items {
		if it.SKU != intakes.ShippingSKU {
			continue
		}
		if n == 0 {
			primera = it
		}
		n++
	}
	return n, primera, d.Total
}

// TestEnsureShippingLine_Idempotente: el criterio central. Se aplica CINCO veces y
// se CUENTAN las líneas — no se lee el código: sigue habiendo una, con el mismo
// precio, y el total de la cabecera no se movió del primer valor.
func TestEnsureShippingLine_Idempotente(t *testing.T) {
	st, svc := solicitudPorAprobar(t)
	st.SetShippingZones(tenantA, zonaÚnica())
	ctx := context.Background()

	for i := range 5 {
		if err := svc.EnsureShippingLine(ctx, tenantA, intakeConEnvío, intakes.ShippingAlways); err != nil {
			t.Fatalf("EnsureShippingLine (pasada %d): %v", i+1, err)
		}
	}

	n, línea, total := líneasDeEnvío(t, svc, intakeConEnvío)
	if n != 1 {
		t.Fatalf("líneas de envío tras 5 pasadas: got %d, want 1", n)
	}
	if línea.UnitPrice != 3000 || línea.Qty != 1 {
		t.Errorf("línea=%+v; quiero 1 × 3000", línea)
	}
	if total != 21000 {
		t.Fatalf("total=%v; quiero 18000 + 3000 UNA sola vez", total)
	}
}

// TestSetStatus_PendingApprovalPoneElEnvío: la regla de D-041.11 —entrar a
// `pending_approval` garantiza la línea— y con ella el total que devuelve la propia
// transición, que es lo que la consola pinta sin releer.
func TestSetStatus_PendingApprovalPoneElEnvío(t *testing.T) {
	st, svc := solicitudPorAprobar(t)
	st.SetShippingZones(tenantA, zonaÚnica())

	got, err := svc.SetStatus(context.Background(), tenantA, intakeConEnvío, intakes.StatusPendingApproval, intakes.NoticeToClient)
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got.Total != 21000 {
		t.Fatalf("total devuelto por la transición=%v; quiero 21000 (ya con el envío)", got.Total)
	}

	n, línea, total := líneasDeEnvío(t, svc, intakeConEnvío)
	if n != 1 || línea.Label != "Envío — Providencia" || total != 21000 {
		t.Fatalf("tras la transición: n=%d línea=%+v total=%v", n, línea, total)
	}
}

// TestSetStatus_PendingApprovalSinZonasPoneLaMarca: un tenant que no configuró
// nada TAMBIÉN lleva la línea, «por confirmar» y a 0. El presupuesto es
// exactamente el momento en que el dueño le pone precio, así que la línea tiene
// que estar delante de sus ojos; y a 0 no mueve el total.
func TestSetStatus_PendingApprovalSinZonasPoneLaMarca(t *testing.T) {
	_, svc := solicitudPorAprobar(t)

	if _, err := svc.SetStatus(context.Background(), tenantA, intakeConEnvío, intakes.StatusPendingApproval, intakes.NoticeToClient); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	n, línea, total := líneasDeEnvío(t, svc, intakeConEnvío)
	if n != 1 || línea.Label != intakes.ShippingPendingLabel || línea.UnitPrice != 0 {
		t.Fatalf("n=%d línea=%+v; quiero UNA «%s» a 0", n, línea, intakes.ShippingPendingLabel)
	}
	if total != 18000 {
		t.Fatalf("total=%v; una línea a 0 no mueve el dinero", total)
	}
}

// TestEnsureShippingLine_CambioDeZona es el caso feo: la línea YA existe cuando el
// tenant cambia de zona. Lo que no puede pasar es que queden dos envíos sumados ni
// que sobreviva el viejo — el pedido acabaría cobrando un reparto que el tenant ya
// no cobra.
func TestEnsureShippingLine_CambioDeZona(t *testing.T) {
	st, svc := solicitudPorAprobar(t)
	st.SetShippingZones(tenantA, zonaÚnica())
	ctx := context.Background()

	if err := svc.EnsureShippingLine(ctx, tenantA, intakeConEnvío, intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine (zona vieja): %v", err)
	}

	// El tenant se muda de zona: otra etiqueta y otra tarifa.
	st.SetShippingZones(tenantA, intakes.ShippingZone{Code: "z2", Label: "Puente Alto", Price: 5000})
	if err := svc.EnsureShippingLine(ctx, tenantA, intakeConEnvío, intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine (zona nueva): %v", err)
	}

	n, línea, total := líneasDeEnvío(t, svc, intakeConEnvío)
	if n != 1 {
		t.Fatalf("líneas de envío tras el cambio de zona: got %d, want 1 (ni dos sumadas, ni la vieja de más)", n)
	}
	if línea.Label != "Envío — Puente Alto" || línea.UnitPrice != 5000 {
		t.Errorf("línea=%+v; el envío viejo no puede sobrevivir al cambio", línea)
	}
	if total != 23000 {
		t.Fatalf("total=%v; quiero 18000 + 5000, sin rastro de los 3000 viejos", total)
	}
}

// TestEnsureShippingLine_NoPisaElPrecioDelDueño: sin zonas configuradas, el precio
// de la línea es humano. Re-presupuestar el pedido (confirmed → pending_approval,
// D-041.26) NO puede devolverla a 0: sería borrar el trabajo del dueño con una
// transición de rutina.
func TestEnsureShippingLine_NoPisaElPrecioDelDueño(t *testing.T) {
	st, svc := solicitudPorAprobar(t)
	ctx := context.Background()

	if err := svc.EnsureShippingLine(ctx, tenantA, intakeConEnvío, intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine: %v", err)
	}
	// El dueño precifica a mano lo que salió «por confirmar».
	st.SetShippingPrice(tenantA, intakeConEnvío, 2500)

	if err := svc.EnsureShippingLine(ctx, tenantA, intakeConEnvío, intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine (segunda pasada): %v", err)
	}

	n, línea, total := líneasDeEnvío(t, svc, intakeConEnvío)
	if n != 1 || línea.UnitPrice != 2500 {
		t.Fatalf("n=%d línea=%+v; el precio del dueño manda mientras no haya zona", n, línea)
	}
	if total != 20500 {
		t.Fatalf("total=%v; quiero 18000 + 2500", total)
	}
}

// TestEnsureShippingLine_OnlyIfZonesNoMolestaAQuienNoCobraEnvío: el cierre del
// carrito de un tenant sin zonas no gana ninguna línea. Va directo a `confirmed`
// sin ciclo de aprobación: una línea «por confirmar» que nadie va a precificar
// solo ensucia el pedido.
func TestEnsureShippingLine_OnlyIfZonesNoMolestaAQuienNoCobraEnvío(t *testing.T) {
	_, svc := solicitudPorAprobar(t)

	if err := svc.EnsureShippingLine(context.Background(), tenantA, intakeConEnvío, intakes.ShippingOnlyIfZones); err != nil {
		t.Fatalf("EnsureShippingLine: %v", err)
	}

	n, _, total := líneasDeEnvío(t, svc, intakeConEnvío)
	if n != 0 || total != 18000 {
		t.Fatalf("n=%d total=%v; sin zonas, el cierre del carrito no toca nada", n, total)
	}
}

// TestEnsureShippingLine_CrossTenant: una solicitud de otro tenant no existe
// (INV-8), y menos aún se le pone una línea.
func TestEnsureShippingLine_CrossTenant(t *testing.T) {
	st, _ := solicitudPorAprobar(t)
	st.SetShippingZones(tenantB, zonaÚnica())

	err := intakes.NewService(st).EnsureShippingLine(
		context.Background(), tenantB, intakeConEnvío, intakes.ShippingAlways)
	if !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("err=%v, quiero ErrNotFound", err)
	}
}
