package intakes_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

const intakeEditable = "33333333-3333-3333-3333-333333333333"

// solicitudEditable siembra una solicitud EN `pending_approval` con la línea de la
// escena (1 × Hamburguesa $8.00 con «con queso extra» pegado) y las zonas de envío
// que se le pidan. Devuelve el store y el servicio.
//
// El envío se materializa por el MISMO camino que en producción —entrar a
// `pending_approval`—, no sembrándolo a mano: si se sembrara, el test no diría nada
// sobre la línea que de verdad se va a encontrar la edición.
func solicitudEditable(t *testing.T, zonas ...intakes.ShippingZone) (*intakes.MemoryStore, *intakes.Service) {
	t.Helper()
	st := intakes.NewMemoryStore()
	st.Add(tenantA, intakes.Intake{
		ID:        intakeEditable,
		ContactID: "contacto-opaco-1",
		SessionID: "sess-a",
		Status:    intakes.StatusConfirmed,
		Total:     8,
		CreatedAt: time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	}, intakes.Item{
		SKU: "HAMB", Label: "Hamburguesa", Customization: "con queso extra",
		Qty: 1, UnitPrice: 8,
	})
	if len(zonas) > 0 {
		st.SetShippingZones(tenantA, zonas...)
	}

	svc := intakes.NewService(st)
	if _, err := svc.SetStatus(context.Background(), tenantA, intakeEditable, intakes.StatusPendingApproval, intakes.NoticeToClient); err != nil {
		t.Fatalf("llevando la solicitud a pending_approval: %v", err)
	}
	return st, svc
}

// líneasPorSKU indexa las líneas de la solicitud por sku (una lista por sku: dos
// líneas pueden compartirlo, D-041.20).
func líneasPorSKU(t *testing.T, svc *intakes.Service, intakeID string) map[string][]intakes.Item {
	t.Helper()
	d, err := svc.Get(context.Background(), tenantA, intakeID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	out := map[string][]intakes.Item{}
	for _, it := range d.Items {
		out[it.SKU] = append(out[it.SKU], it)
	}
	return out
}

// ==================== (a) la transición que YA EXISTÍA ====================

// TestCanTransition_ConfirmedAPendingApproval verifica el criterio (a) de T4.10,
// que la enmienda del 2026-08-06 declara ya cumplido desde T4.1: la vuelta a un
// estado editable existe, y `expired` sigue sin ser destino de nada.
//
// Es VERIFICACIÓN, no implementación: si alguien retirara la entrada de
// `transitions` —o "arreglara" la guarda de expired—, la edición manual quedaría
// inalcanzable y este test lo dice antes que el e2e.
func TestCanTransition_ConfirmedAPendingApproval(t *testing.T) {
	if !intakes.CanTransition(intakes.StatusConfirmed, intakes.StatusPendingApproval) {
		t.Error("confirmed → pending_approval tiene que ser válida (D-041.26): sin ella no hay re-presupuestar")
	}
	// El `closed` legado del cart se evalúa como confirmed: un pedido histórico
	// también se re-presupuesta.
	if !intakes.CanTransition(intakes.StatusClosedLegacy, intakes.StatusPendingApproval) {
		t.Error("el `closed` legado tiene que ofrecer la misma vuelta que confirmed")
	}
	for _, from := range []string{
		intakes.StatusOpen, intakes.StatusConfirmed, intakes.StatusPendingApproval,
		intakes.StatusSettled, intakes.StatusAbandoned, intakes.StatusExpired,
	} {
		if intakes.CanTransition(from, intakes.StatusExpired) {
			t.Errorf("%s → expired: nada vence por tiempo (D-041.16)", from)
		}
	}
}

// ==================== la escena del queso extra, en el dominio ====================

// TestReplaceItems_EscenaDelQuesoExtra recorre el paso 5 de D-041.26 §e: el pedido
// cerrado a $8.00 con «con queso extra» pegado a la línea vuelve a
// `pending_approval`, el dueño añade la línea del queso a mano y el total queda en
// $9.00 con su revisión `corrected`.
//
// El tenant NO tiene zonas de envío, así que la línea que la transición materializa
// sale «por confirmar» a 0: el total del criterio se cumple con ella dentro, que es
// justamente lo que hay que demostrar.
func TestReplaceItems_EscenaDelQuesoExtra(t *testing.T) {
	st, svc := solicitudEditable(t)

	detail, err := svc.ReplaceItems(context.Background(), tenantA, intakeEditable, []intakes.Item{
		{SKU: "HAMB", Label: "Hamburguesa", Customization: "con queso extra", Qty: 1, UnitPrice: 8},
		{SKU: "QUESO-EX", Label: "Queso extra", Qty: 1, UnitPrice: 1},
	})
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}

	if detail.Total != 9 {
		t.Fatalf("total=%v, quiero 9 (8 de la hamburguesa + 1 del queso + 0 del envío por confirmar)", detail.Total)
	}
	// La personalización viaja con SU línea: el «con queso extra» sigue pegado a la
	// hamburguesa aunque ahora el queso se cobre aparte (INV-12).
	porSKU := líneasPorSKU(t, svc, intakeEditable)
	if len(porSKU["HAMB"]) != 1 || porSKU["HAMB"][0].Customization != "con queso extra" {
		t.Fatalf("la línea de la hamburguesa quedó %+v; la personalización tiene que sobrevivir a la edición", porSKU["HAMB"])
	}
	if len(porSKU["QUESO-EX"]) != 1 || porSKU["QUESO-EX"][0].UnitPrice != 1 {
		t.Fatalf("la línea del queso quedó %+v; quiero una a 1.00", porSKU["QUESO-EX"])
	}

	exigirRevisiónDeCorrección(t, st.Revisions(intakeEditable), 9)

	// Y se cierra el ciclo: vuelve a confirmed, que ya existía.
	if _, err := svc.SetStatus(context.Background(), tenantA, intakeEditable, intakes.StatusConfirmed, intakes.NoticeToClient); err != nil {
		t.Fatalf("pending_approval → confirmed: %v", err)
	}
}

// exigirRevisiónDeCorrección comprueba que la edición dejó UNA revisión, firmada
// por el dueño, con una foto que CUADRA: su total es el de la solicitud y sus
// líneas suman ese total. Un payload cuyo total no sume sus propias líneas no
// serviría ni para auditar ni para reconstruir.
func exigirRevisiónDeCorrección(t *testing.T, revs []intakes.Revision, total float64) {
	t.Helper()
	if len(revs) != 1 {
		t.Fatalf("revisiones=%d, quiero exactamente 1 (la corrección); una edición es UN acto", len(revs))
	}
	rev := revs[0]
	if rev.Kind != intakes.RevisionKindCorrected || rev.CreatedBy != intakes.RevisionByOwner {
		t.Fatalf("revisión kind=%q created_by=%q; quiero corrected/owner", rev.Kind, rev.CreatedBy)
	}

	var payload struct {
		Version int     `json:"version"`
		Total   float64 `json:"total"`
		Items   []struct {
			Qty       int     `json:"qty"`
			UnitPrice float64 `json:"unit_price"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rev.Payload, &payload); err != nil {
		t.Fatalf("payload de la revisión ilegible: %v; raw=%s", err, rev.Payload)
	}
	if payload.Version != intakes.RevisionPayloadVersion || payload.Total != total {
		t.Fatalf("payload version=%d total=%v; quiero %d y %v",
			payload.Version, payload.Total, intakes.RevisionPayloadVersion, total)
	}
	var suma float64
	for _, l := range payload.Items {
		suma += float64(l.Qty) * l.UnitPrice
	}
	if suma != payload.Total {
		t.Fatalf("la foto no cuadra: sus líneas suman %v y dice total %v", suma, payload.Total)
	}
}

// ==================== la frontera con T4.3: la línea de envío ====================

// TestReplaceItems_ElEnvíoSobreviveIntacto es el borde donde dos tareas de la misma
// ola se pisan. La edición NO menciona la línea de envío, y tras ella tiene que
// haber EXACTAMENTE UNA, con su etiqueta y su precio, contando en el total.
//
// Con zona resuelta ($3.00) para que el envío tenga precio: si la edición lo
// borrara, el total lo delataría; si lo duplicara, el conteo también.
func TestReplaceItems_ElEnvíoSobreviveIntacto(t *testing.T) {
	_, svc := solicitudEditable(t, intakes.ShippingZone{Code: "z1", Label: "Providencia", Price: 3})

	antes := líneasPorSKU(t, svc, intakeEditable)
	if len(antes[intakes.ShippingSKU]) != 1 {
		t.Fatalf("fixture: envíos antes de editar = %d, quiero 1", len(antes[intakes.ShippingSKU]))
	}

	detail, err := svc.ReplaceItems(context.Background(), tenantA, intakeEditable, []intakes.Item{
		{SKU: "HAMB", Label: "Hamburguesa", Qty: 2, UnitPrice: 8},
	})
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}

	después := líneasPorSKU(t, svc, intakeEditable)
	envíos := después[intakes.ShippingSKU]
	if len(envíos) != 1 {
		t.Fatalf("envíos tras editar = %d, quiero 1: ni duplicado ni borrado", len(envíos))
	}
	if envíos[0].UnitPrice != 3 || envíos[0].Label != antes[intakes.ShippingSKU][0].Label {
		t.Fatalf("el envío quedó %+v; la edición no puede tocarle etiqueta ni precio", envíos[0])
	}
	if detail.Total != 19 {
		t.Fatalf("total=%v, quiero 19 (2×8 + 3 de envío): el envío tiene que seguir contando", detail.Total)
	}
}

// TestReplaceItems_RechazaElSKUReservado: mandar una línea del sistema por esta
// puerta se rechaza en la validación, ANTES de escribir nada. Es la primera de las
// dos capas que impiden dos envíos sumados; la segunda es el índice único de la BD.
func TestReplaceItems_RechazaElSKUReservado(t *testing.T) {
	st, svc := solicitudEditable(t, intakes.ShippingZone{Code: "z1", Label: "Providencia", Price: 3})

	_, err := svc.ReplaceItems(context.Background(), tenantA, intakeEditable, []intakes.Item{
		{SKU: "HAMB", Label: "Hamburguesa", Qty: 1, UnitPrice: 8},
		{SKU: intakes.ShippingSKU, Label: "Envío gratis", Qty: 1, UnitPrice: 0},
	})
	var invalid *intakes.InvalidItemsError
	if !errors.As(err, &invalid) {
		t.Fatalf("err=%v; quiero *InvalidItemsError por el sku reservado", err)
	}
	if len(invalid.Defects) != 1 || invalid.Defects[0].Index != 1 || invalid.Defects[0].Field != "sku" {
		t.Fatalf("defectos=%+v; quiero uno solo, en la línea 1, campo sku", invalid.Defects)
	}
	// Y NO se escribió nada: ni líneas, ni revisión.
	if revs := st.Revisions(intakeEditable); len(revs) != 0 {
		t.Fatalf("revisiones=%d; una edición rechazada no deja rastro", len(revs))
	}
	if n := len(líneasPorSKU(t, svc, intakeEditable)[intakes.ShippingSKU]); n != 1 {
		t.Fatalf("envíos=%d tras el rechazo, quiero 1: la validación es todo-o-nada", n)
	}
}

// ==================== estado, aislamiento y concurrencia ====================

// TestReplaceItems_422SoloEnPendingApproval: editar un `confirmed` se rechaza con
// el estado actual, para que quien llama sepa que tiene que re-presupuestarlo
// primero. Editar lo confirmado cambiaría lo que el cliente ya aceptó sin decírselo.
func TestReplaceItems_422SoloEnPendingApproval(t *testing.T) {
	for _, estado := range []string{
		intakes.StatusConfirmed, intakes.StatusOpen, intakes.StatusCancelled,
		intakes.StatusSettled, intakes.StatusClosedLegacy,
	} {
		st := seedStore(t, estado)
		svc := intakes.NewService(st)

		_, err := svc.ReplaceItems(context.Background(), tenantA,
			"11111111-1111-1111-1111-111111111111",
			[]intakes.Item{{SKU: "X", Label: "X", Qty: 1, UnitPrice: 1}})

		var notEditable *intakes.NotEditableError
		if !errors.As(err, &notEditable) {
			t.Fatalf("desde %q err=%v; quiero *NotEditableError", estado, err)
		}
		if notEditable.Status != intakes.NormalizeStatus(estado) {
			t.Fatalf("el error dice %q y la solicitud está en %q", notEditable.Status, intakes.NormalizeStatus(estado))
		}
	}
}

// TestReplaceItems_404OtroTenant: una solicitud ajena no existe (INV-8). Nunca un
// error que confirme que el id es de alguien.
func TestReplaceItems_404OtroTenant(t *testing.T) {
	_, svc := solicitudEditable(t)

	_, err := svc.ReplaceItems(context.Background(), tenantB, intakeEditable,
		[]intakes.Item{{SKU: "X", Label: "X", Qty: 1, UnitPrice: 1}})
	if !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("err=%v, quiero ErrNotFound", err)
	}
}

// TestReplaceItems_409SiAlguienLaMovió: entre la lectura que validó el estado y la
// escritura, otro operador confirmó la solicitud. La edición NO se aplica.
func TestReplaceItems_409SiAlguienLaMovió(t *testing.T) {
	st, _ := solicitudEditable(t)
	svc := intakes.NewService(&storeQueConfirma{MemoryStore: st})

	_, err := svc.ReplaceItems(context.Background(), tenantA, intakeEditable,
		[]intakes.Item{{SKU: "X", Label: "X", Qty: 1, UnitPrice: 1}})
	if !errors.Is(err, intakes.ErrConflict) {
		t.Fatalf("err=%v, quiero ErrConflict", err)
	}
	if len(líneasPorSKU(t, intakes.NewService(st), intakeEditable)["X"]) != 0 {
		t.Fatal("la edición se aplicó pese al conflicto")
	}
}

// storeQueConfirma mueve la solicitud a `confirmed` JUSTO DESPUÉS de la lectura que
// valida el estado, que es la ventana que el compare-and-swap cierra.
type storeQueConfirma struct {
	*intakes.MemoryStore
	movida bool
}

func (s *storeQueConfirma) Get(ctx context.Context, tenantID, intakeID string) (intakes.Detail, error) {
	d, err := s.MemoryStore.Get(ctx, tenantID, intakeID)
	if err != nil || s.movida {
		return d, err
	}
	s.movida = true
	if _, uerr := s.UpdateStatus(ctx, tenantID, intakeID, intakes.StatusConfirmed,
		[]string{intakes.StatusPendingApproval}); uerr != nil {
		return intakes.Detail{}, uerr
	}
	return d, nil
}

// ==================== validación pura ====================

// TestValidateEditableItems_AcumulaLosDefectos: una línea con dos problemas
// devuelve los dos, y las líneas buenas no inventan defectos.
func TestValidateEditableItems_AcumulaLosDefectos(t *testing.T) {
	err := intakes.ValidateEditableItems([]intakes.Item{
		{SKU: "OK", Label: "Bien", Qty: 1, UnitPrice: 10},
		{SKU: "", Label: "", Qty: 0, UnitPrice: -1},
	})
	var invalid *intakes.InvalidItemsError
	if !errors.As(err, &invalid) {
		t.Fatalf("err=%v, quiero *InvalidItemsError", err)
	}
	campos := map[string]bool{}
	for _, d := range invalid.Defects {
		if d.Index != 1 {
			t.Fatalf("defecto en la línea %d; la 0 es válida: %+v", d.Index, d)
		}
		campos[d.Field] = true
	}
	for _, f := range []string{"sku", "label", "qty", "unit_price"} {
		if !campos[f] {
			t.Errorf("falta el defecto del campo %q: %+v", f, invalid.Defects)
		}
	}
}

// TestValidateEditableItems_LoQueSÍPasa: precio 0 (artículo de regalo), dos líneas
// con el mismo sku (D-041.20 las parte por personalización) y la lista VACÍA
// (quitar la última línea es una edición legítima).
func TestValidateEditableItems_LoQueSÍPasa(t *testing.T) {
	if err := intakes.ValidateEditableItems([]intakes.Item{
		{SKU: "REGALO", Label: "De la casa", Qty: 1, UnitPrice: 0},
		{SKU: "HAMB", Label: "Hamburguesa", Customization: "sin cebolla", Qty: 1, UnitPrice: 8},
		{SKU: "HAMB", Label: "Hamburguesa", Customization: "sin sal", Qty: 1, UnitPrice: 8},
	}); err != nil {
		t.Fatalf("err=%v; precio 0 y skus repetidos con personalización distinta son válidos", err)
	}
	if err := intakes.ValidateEditableItems(nil); err != nil {
		t.Fatalf("err=%v; la lista vacía es «quitar todas las líneas», no un error", err)
	}
}

// TestValidateEditableItems_Cota: una edición de miles de líneas se rechaza entera.
func TestValidateEditableItems_Cota(t *testing.T) {
	items := make([]intakes.Item, intakes.MaxEditableItems+1)
	for i := range items {
		items[i] = intakes.Item{SKU: "X", Label: "X", Qty: 1, UnitPrice: 1}
	}
	var tooMany *intakes.TooManyItemsError
	if err := intakes.ValidateEditableItems(items); !errors.As(err, &tooMany) {
		t.Fatalf("err=%v, quiero *TooManyItemsError", err)
	}
	if tooMany.Max != intakes.MaxEditableItems {
		t.Fatalf("el error dice máximo %d y la cota es %d", tooMany.Max, intakes.MaxEditableItems)
	}
}

// TestReservedSKUPrefix_CubreLaLíneaDeEnvío ata los dos literales de este paquete:
// si ShippingSKU dejara de empezar por el prefijo reservado, la edición manual
// podría borrar el envío del pedido sin que nadie lo notara.
func TestReservedSKUPrefix_CubreLaLíneaDeEnvío(t *testing.T) {
	if !strings.HasPrefix(intakes.ShippingSKU, intakes.ReservedSKUPrefix) {
		t.Fatalf("ShippingSKU=%q no empieza por %q", intakes.ShippingSKU, intakes.ReservedSKUPrefix)
	}
}
