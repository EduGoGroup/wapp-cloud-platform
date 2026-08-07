package intakes_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// revalidate_test.go cubre la REVALIDACIÓN CONTRA EL CATÁLOGO VIGENTE (Plan 041 ·
// T4.9, D-041.25): la función pura y la escritura de `intake_items` + la revisión
// `revalidated`.
//
// Este archivo importa el módulo `cart` a propósito, y puede hacerlo porque es un
// paquete de test EXTERNO (`intakes_test`): así el test del criterio (f) puede
// comprobar la propiedad de verdad —que lo GUARDADO en `rendered_text` es byte a
// byte el mismo string que se le MANDA al cliente— en vez de comprobar dos textos
// parecidos escritos a mano en dos sitios.

const (
	revalTenant = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	revalIntake = "id-marta"
)

// primeroDeEnero es la fecha del pedido que se rescata el 15 de enero.
var primeroDeEnero = time.Date(2026, time.January, 1, 15, 4, 0, 0, time.UTC)

// líneasDeMarta son las líneas del 1 de enero: 2 × Pan $2.00 + 1 × Queso $3.00.
func líneasDeMarta() []intakes.Item {
	return []intakes.Item{
		{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2.00},
		{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3.00},
	}
}

// catálogoDelQuince es el catálogo VIGENTE: el pan a $2.50 y el queso ya no está.
func catálogoDelQuince() intakes.PriceList {
	return intakes.NewPriceList(map[string]intakes.CatalogEntry{
		"PAN": {Label: "Pan", Price: 2.50},
	})
}

// bandejaConMarta siembra la solicitud `open` de Marta con sus dos líneas.
func bandejaConMarta(extra ...intakes.Item) *intakes.MemoryStore {
	st := intakes.NewMemoryStore()
	st.Add(revalTenant, intakes.Intake{
		ID: revalIntake, ContactID: "contacto-opaco", SessionID: "sess-a",
		Status: intakes.StatusOpen, Total: 7, CreatedAt: primeroDeEnero, UpdatedAt: primeroDeEnero,
	}, append(líneasDeMarta(), extra...)...)
	return st
}

// --- la función pura -------------------------------------------------------

// TestRevalidate_EscenaDeMarta es el criterio (a) por el lado de la aritmética:
// una línea re-preciada, una retirada, una viva y el total de $7.00 a $5.00.
func TestRevalidate_EscenaDeMarta(t *testing.T) {
	rv := intakes.Revalidate(líneasDeMarta(), catálogoDelQuince())

	if !rv.Changed() {
		t.Fatal("hubo cambios: Changed() tiene que ser true")
	}
	if len(rv.Items) != 1 || rv.Items[0].SKU != "PAN" || rv.Items[0].UnitPrice != 2.50 {
		t.Fatalf("líneas vivas=%+v; quiero solo PAN a 2.50", rv.Items)
	}
	if rv.TotalBefore != 7 || rv.TotalAfter != 5 {
		t.Fatalf("total antes=%v después=%v; quiero 7 y 5", rv.TotalBefore, rv.TotalAfter)
	}
}

// TestRevalidate_EscenaDeMartaElDiff es la otra mitad del criterio (a): qué se le va
// a contar al cliente, y en qué orden.
func TestRevalidate_EscenaDeMartaElDiff(t *testing.T) {
	rv := intakes.Revalidate(líneasDeMarta(), catálogoDelQuince())

	repriced, removed := rv.Repriced(), rv.Removed()
	if len(repriced) != 1 || repriced[0].SKU != "PAN" {
		t.Fatalf("repriced=%+v; quiero solo PAN", repriced)
	}
	if repriced[0].From != 2 || repriced[0].To != 2.5 {
		t.Fatalf("repriced=%+v; quiero 2.00→2.50", repriced[0])
	}
	if len(removed) != 1 || removed[0].SKU != "QUESO" {
		t.Fatalf("removed=%+v; quiero solo QUESO", removed)
	}
	if removed[0].Qty != 1 || removed[0].From != 3 {
		t.Fatalf("removed=%+v; quiero x1 a 3.00 (el precio por el que creía pagar)", removed[0])
	}
	// El orden de los cambios es el de las LÍNEAS del pedido: el aviso se lee al
	// lado del resumen.
	if rv.Changes[0].SKU != "PAN" || rv.Changes[1].SKU != "QUESO" {
		t.Fatalf("orden de los cambios=%v; quiero el de las líneas", rv.Changes)
	}
}

// TestRevalidate_NoMutaLaEntrada: la función es pura y la lista que le dan sigue
// intacta después de llamarla.
func TestRevalidate_NoMutaLaEntrada(t *testing.T) {
	in := líneasDeMarta()
	intakes.Revalidate(in, catálogoDelQuince())

	if len(in) != 2 || in[0].UnitPrice != 2.00 || in[1].SKU != "QUESO" {
		t.Fatalf("la entrada se mutó: %+v", in)
	}
}

// TestRevalidate_NadaCambió es el criterio (b): con el catálogo que aún tiene los
// dos artículos a su precio, no hay cambios que contar.
func TestRevalidate_NadaCambió(t *testing.T) {
	rv := intakes.Revalidate(líneasDeMarta(), intakes.NewPriceList(map[string]intakes.CatalogEntry{
		"PAN":   {Label: "Pan", Price: 2.00},
		"QUESO": {Label: "Queso", Price: 3.00},
	}))

	if rv.Changed() || len(rv.Changes) != 0 {
		t.Fatalf("nada cambió pero Changes=%+v", rv.Changes)
	}
	if rv.TotalBefore != 7 || rv.TotalAfter != 7 {
		t.Fatalf("el total no puede moverse: antes=%v después=%v", rv.TotalBefore, rv.TotalAfter)
	}
}

// TestRevalidate_PrecioQueBaja es el criterio (c): la bajada se avisa igual que la
// subida, y con el mismo tipo de cambio.
func TestRevalidate_PrecioQueBaja(t *testing.T) {
	rv := intakes.Revalidate(líneasDeMarta(), intakes.NewPriceList(map[string]intakes.CatalogEntry{
		"PAN":   {Label: "Pan", Price: 1.50},
		"QUESO": {Label: "Queso", Price: 3.00},
	}))

	repriced := rv.Repriced()
	if len(repriced) != 1 || repriced[0].From != 2.00 || repriced[0].To != 1.50 {
		t.Fatalf("repriced=%+v; quiero PAN 2.00→1.50", repriced)
	}
	if len(rv.Removed()) != 0 {
		t.Fatalf("una bajada no retira nada: %+v", rv.Removed())
	}
	if rv.TotalAfter != 6 {
		t.Fatalf("total después=%v; quiero 6", rv.TotalAfter)
	}
}

// TestRevalidate_ShippingIntacto es el criterio (d): la línea de la plataforma no
// se re-precia, no se retira y sigue sumando, aunque el catálogo no la conozca —que
// es siempre, por diseño (D-041.11).
func TestRevalidate_ShippingIntacto(t *testing.T) {
	envío := intakes.Item{SKU: intakes.ShippingSKU, Label: "Envío por confirmar", Qty: 1, UnitPrice: 4}
	rv := intakes.Revalidate(append(líneasDeMarta(), envío), catálogoDelQuince())

	var visto bool
	for _, it := range rv.Items {
		if it.SKU != intakes.ShippingSKU {
			continue
		}
		visto = true
		if it != envío {
			t.Fatalf("la línea de envío se tocó: %+v; quiero %+v", it, envío)
		}
	}
	if !visto {
		t.Fatal("la línea de envío desapareció del pedido")
	}
	for _, c := range rv.Changes {
		if c.SKU == intakes.ShippingSKU {
			t.Fatalf("el envío entró al resumen de cambios: %+v", c)
		}
	}
	if rv.TotalBefore != 11 || rv.TotalAfter != 9 {
		t.Fatalf("total antes=%v después=%v; quiero 11 y 9 (el envío suma en los dos)", rv.TotalBefore, rv.TotalAfter)
	}
}

// TestRevalidate_CatálogoQueNoResuelve es el criterio (e): con el valor cero de
// PriceList —el que queda cuando el catálogo no se pudo leer— no se retira nada, no
// se re-precia nada y no hay resumen. Jamás se borra una línea por un fallo nuestro.
func TestRevalidate_CatálogoQueNoResuelve(t *testing.T) {
	rv := intakes.Revalidate(líneasDeMarta(), intakes.PriceList{})

	if rv.Changed() {
		t.Fatalf("sin catálogo no hay cambios que contar: %+v", rv.Changes)
	}
	if len(rv.Items) != 2 {
		t.Fatalf("líneas=%d; quiero las 2 intactas", len(rv.Items))
	}
	if rv.Items[0].UnitPrice != 2.00 || rv.Items[1].SKU != "QUESO" {
		t.Fatalf("las líneas se tocaron: %+v", rv.Items)
	}
	if rv.TotalBefore != 7 || rv.TotalAfter != 7 {
		t.Fatalf("total antes=%v después=%v; quiero 7 y 7", rv.TotalBefore, rv.TotalAfter)
	}
}

// TestRevalidate_CatálogoVacíoPeroLEÍDO es la otra mitad del criterio (e), y la
// razón de que PriceList sea un tipo y no un mapa: un catálogo que SÍ se leyó y no
// vende nada retira las líneas, mientras que uno que no se pudo leer no toca
// ninguna. Si las dos situaciones se confundieran, un fallo de lectura vaciaría
// pedidos.
func TestRevalidate_CatálogoVacíoPeroLEÍDO(t *testing.T) {
	rv := intakes.Revalidate(líneasDeMarta(), intakes.NewPriceList(nil))

	if len(rv.Items) != 0 || len(rv.Removed()) != 2 {
		t.Fatalf("un catálogo leído y vacío retira todo: líneas=%d retiradas=%d", len(rv.Items), len(rv.Removed()))
	}
}

// TestRevalidate_EtiquetaNuevaNoEsCambio: el cliente pidió un producto, no una
// cadena de texto (D-041.25). La etiqueta vigente se aplica a la línea, pero por sí
// sola no despierta ni aviso ni revisión.
func TestRevalidate_EtiquetaNuevaNoEsCambio(t *testing.T) {
	rv := intakes.Revalidate(
		[]intakes.Item{{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2.00}},
		intakes.NewPriceList(map[string]intakes.CatalogEntry{"PAN": {Label: "Pan de masa madre", Price: 2.00}}),
	)

	if rv.Changed() {
		t.Fatalf("un cambio de etiqueta no se avisa: %+v", rv.Changes)
	}
	if rv.Items[0].Label != "Pan de masa madre" {
		t.Fatalf("la etiqueta vigente no se aplicó: %q", rv.Items[0].Label)
	}
}

// TestRevalidate_CéntimoDeRuido: dos importes que redondean al mismo céntimo NO son
// un cambio de precio. Sin esta tolerancia, un bit de diferencia entre la columna
// NUMERIC y el número del JSON le mandaría al cliente «Pan: $2.00 → $2.00».
func TestRevalidate_CéntimoDeRuido(t *testing.T) {
	rv := intakes.Revalidate(
		[]intakes.Item{{SKU: "PAN", Label: "Pan", Qty: 1, UnitPrice: 2.00}},
		intakes.NewPriceList(map[string]intakes.CatalogEntry{"PAN": {Label: "Pan", Price: 2.000000001}}),
	)
	if rv.Changed() {
		t.Fatalf("por debajo del céntimo no hay cambio: %+v", rv.Changes)
	}
}

// TestRevalidate_LaPersonalizaciónSeVaConSuLínea: la indicación de una línea
// retirada desaparece con ella (sin línea no tiene a qué pegarse), y la de una
// línea que sobrevive se conserva.
func TestRevalidate_LaPersonalizaciónSeVaConSuLínea(t *testing.T) {
	rv := intakes.Revalidate([]intakes.Item{
		{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2.00, Customization: "sin sal"},
		{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3.00, Customization: "en lonchas"},
	}, catálogoDelQuince())

	if len(rv.Items) != 1 || rv.Items[0].Customization != "sin sal" {
		t.Fatalf("la línea viva perdió su indicación: %+v", rv.Items)
	}
}

// --- el payload y su ausencia de PII ---------------------------------------

// TestRevalidatedRevisionPayload_Forma comprueba el contrato v1 de D-041.25 §d.
func TestRevalidatedRevisionPayload_Forma(t *testing.T) {
	rv := intakes.Revalidate(líneasDeMarta(), catálogoDelQuince())
	raw, err := intakes.RevalidatedRevisionPayload(rv)
	if err != nil {
		t.Fatalf("RevalidatedRevisionPayload: %v", err)
	}

	var got struct {
		Version  int `json:"version"`
		Repriced []struct {
			SKU  string  `json:"sku"`
			From float64 `json:"from"`
			To   float64 `json:"to"`
		} `json:"repriced"`
		Removed []struct {
			SKU       string  `json:"sku"`
			Label     string  `json:"label"`
			Qty       int     `json:"qty"`
			UnitPrice float64 `json:"unit_price"`
		} `json:"removed"`
		TotalBefore float64 `json:"total_before"`
		TotalAfter  float64 `json:"total_after"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload ilegible: %v (%s)", err, raw)
	}
	if got.Version != 1 || got.TotalBefore != 7 || got.TotalAfter != 5 {
		t.Fatalf("payload=%+v; quiero version 1 y 7→5", got)
	}
	if len(got.Repriced) != 1 || got.Repriced[0].SKU != "PAN" || got.Repriced[0].From != 2 || got.Repriced[0].To != 2.5 {
		t.Fatalf("repriced=%+v", got.Repriced)
	}
	if len(got.Removed) != 1 || got.Removed[0].SKU != "QUESO" || got.Removed[0].Qty != 1 || got.Removed[0].UnitPrice != 3 {
		t.Fatalf("removed=%+v", got.Removed)
	}
}

// TestRevalidatedRevisionPayload_ListasVacíasNoSonNull: quien lea la revisión tiene
// que poder distinguir «no se retiró nada» de «aquí no se registró qué se retiró».
func TestRevalidatedRevisionPayload_ListasVacíasNoSonNull(t *testing.T) {
	raw, err := intakes.RevalidatedRevisionPayload(intakes.Revalidation{})
	if err != nil {
		t.Fatalf("RevalidatedRevisionPayload: %v", err)
	}
	if s := string(raw); !strings.Contains(s, `"repriced":[]`) || !strings.Contains(s, `"removed":[]`) {
		t.Fatalf("payload=%s; las listas vacías van como [] y nunca null", s)
	}
}

// TestApplyRevalidation_PayloadSinPII es el criterio (g), y se prueba BUSCANDO el
// dato en lo persistido, no leyendo el código que lo omite: se siembra una línea
// retirada con una personalización que lleva un nombre y una dirección y se exige
// que ni una sílaba de eso aparezca en el payload guardado.
//
// `rendered_text` queda deliberadamente FUERA de esta comprobación, y no es una
// laguna: esa columna ES el mensaje que se le mandó al cliente y su razón de existir
// es poder citarlo literalmente (REQ-35b). Lo que no puede llevar PII es el DIFF,
// que es lo que después viaja a un export, a un webhook o a un ticket.
func TestApplyRevalidation_PayloadSinPII(t *testing.T) {
	const datoDelCliente = "para la Sra. Marta Pérez, Av. Siempre Viva 742"
	st := intakes.NewMemoryStore()
	st.Add(revalTenant, intakes.Intake{
		ID: revalIntake, ContactID: "contacto-opaco", SessionID: "sess-a",
		Status: intakes.StatusOpen, Total: 3, CreatedAt: primeroDeEnero, UpdatedAt: primeroDeEnero,
		CustomerNote: datoDelCliente,
	}, intakes.Item{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3, Customization: datoDelCliente})

	rv := intakes.Revalidate([]intakes.Item{
		{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3, Customization: datoDelCliente},
	}, catálogoDelQuince())

	svc := intakes.NewService(st)
	detail, err := svc.ApplyRevalidation(context.Background(), revalTenant, revalIntake, rv, "el mensaje que se mandó")
	if err != nil {
		t.Fatalf("ApplyRevalidation: %v", err)
	}
	if len(detail.Revisions) != 1 {
		t.Fatalf("revisiones=%d; quiero 1", len(detail.Revisions))
	}
	payload := string(detail.Revisions[0].Payload)
	for _, aguja := range []string{datoDelCliente, "Marta", "Pérez", "Siempre Viva", "742", "customization", "customer_note"} {
		if strings.Contains(payload, aguja) {
			t.Fatalf("el payload guardado lleva %q: %s", aguja, payload)
		}
	}
}

// --- la escritura ----------------------------------------------------------

// TestApplyRevalidation_EscenaDeMarta: la escritura deja UNA línea viva re-preciada,
// el total cuadrado, UNA revisión `revalidated` de `system` con su texto, y el
// `status` donde estaba.
func TestApplyRevalidation_EscenaDeMarta(t *testing.T) {
	st := bandejaConMarta()
	rv := intakes.Revalidate(líneasDeMarta(), catálogoDelQuince())
	svc := intakes.NewService(st)

	detail, err := svc.ApplyRevalidation(context.Background(), revalTenant, revalIntake, rv, "aviso")
	if err != nil {
		t.Fatalf("ApplyRevalidation: %v", err)
	}
	if len(detail.Items) != 1 || detail.Items[0].SKU != "PAN" || detail.Items[0].UnitPrice != 2.50 {
		t.Fatalf("líneas=%+v; quiero solo PAN a 2.50", detail.Items)
	}
	if detail.Total != 5 {
		t.Fatalf("total=%v; quiero 5", detail.Total)
	}
	if detail.Status != intakes.StatusOpen {
		t.Fatalf("status=%q; la revalidación NO mueve el estado (E-7)", detail.Status)
	}
	if len(detail.Revisions) != 1 {
		t.Fatalf("revisiones=%d; quiero UNA por rescate, jamás una por línea", len(detail.Revisions))
	}
	rev := detail.Revisions[0]
	if rev.Kind != intakes.RevisionKindRevalidated || rev.CreatedBy != intakes.RevisionBySystem {
		t.Fatalf("revisión kind=%q created_by=%q; quiero revalidated/system", rev.Kind, rev.CreatedBy)
	}
	if rev.RevisionNo != 1 || rev.RenderedText != "aviso" {
		t.Fatalf("revisión no=%d rendered_text=%q", rev.RevisionNo, rev.RenderedText)
	}
}

// TestApplyRevalidation_SinCambiosNoEscribeNada es el criterio (b) por el lado de la
// persistencia: ni revisión, ni líneas tocadas, ni total movido.
func TestApplyRevalidation_SinCambiosNoEscribeNada(t *testing.T) {
	st := bandejaConMarta()
	rv := intakes.Revalidate(líneasDeMarta(), intakes.NewPriceList(map[string]intakes.CatalogEntry{
		"PAN":   {Label: "Pan", Price: 2.00},
		"QUESO": {Label: "Queso", Price: 3.00},
	}))
	svc := intakes.NewService(st)

	detail, err := svc.ApplyRevalidation(context.Background(), revalTenant, revalIntake, rv, "")
	if err != nil {
		t.Fatalf("ApplyRevalidation: %v", err)
	}
	if len(detail.Revisions) != 0 {
		t.Fatalf("revisiones=%d; sin cambios NO se escribe ninguna", len(detail.Revisions))
	}
	if len(detail.Items) != 2 || detail.Total != 7 {
		t.Fatalf("líneas=%d total=%v; el pedido tiene que quedar tal cual", len(detail.Items), detail.Total)
	}
}

// TestApplyRevalidation_TextoVacíoSeRechaza: una revisión que registre el cambio
// pero no lo que se dijo no defiende de nada. Se rechaza SIN escribir.
func TestApplyRevalidation_TextoVacíoSeRechaza(t *testing.T) {
	st := bandejaConMarta()
	rv := intakes.Revalidate(líneasDeMarta(), catálogoDelQuince())
	svc := intakes.NewService(st)

	if _, err := svc.ApplyRevalidation(context.Background(), revalTenant, revalIntake, rv, ""); !errors.Is(err, intakes.ErrEmptyRevalidationText) {
		t.Fatalf("err=%v; quiero ErrEmptyRevalidationText", err)
	}
	detail, err := st.Get(context.Background(), revalTenant, revalIntake)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.Revisions) != 0 || len(detail.Items) != 2 || detail.Total != 7 {
		t.Fatalf("el rechazo escribió algo: revisiones=%d líneas=%d total=%v",
			len(detail.Revisions), len(detail.Items), detail.Total)
	}
}

// TestApplyRevalidation_TodasRetiradas es el criterio (i): si el catálogo se llevó
// todas las líneas, la solicitud se queda `open` con cero líneas y total cero. Nadie
// la mata: el reloj está derogado (D-041.16) y solo un humano la mueve (E-7).
func TestApplyRevalidation_TodasRetiradas(t *testing.T) {
	st := bandejaConMarta()
	rv := intakes.Revalidate(líneasDeMarta(), intakes.NewPriceList(nil))
	svc := intakes.NewService(st)

	detail, err := svc.ApplyRevalidation(context.Background(), revalTenant, revalIntake, rv, "ya no queda nada")
	if err != nil {
		t.Fatalf("ApplyRevalidation: %v", err)
	}
	if len(detail.Items) != 0 || detail.Total != 0 {
		t.Fatalf("líneas=%+v total=%v; quiero cero y cero", detail.Items, detail.Total)
	}
	if detail.Status != intakes.StatusOpen {
		t.Fatalf("status=%q; la solicitud se queda OPEN con cero líneas (E-7)", detail.Status)
	}
	if len(detail.Revisions) != 1 {
		t.Fatalf("revisiones=%d; quiero 1", len(detail.Revisions))
	}
}

// TestApplyRevalidation_ShippingSobrevive es el criterio (d) por el lado de la
// escritura: la línea de la plataforma sigue ahí, con su precio puesto a mano, y
// sigue contando en el total aunque se hayan retirado todas las del cliente.
func TestApplyRevalidation_ShippingSobrevive(t *testing.T) {
	envío := intakes.Item{SKU: intakes.ShippingSKU, Label: "Envío — Providencia", Qty: 1, UnitPrice: 4}
	st := bandejaConMarta(envío)
	rv := intakes.Revalidate(append(líneasDeMarta(), envío), intakes.NewPriceList(nil))
	svc := intakes.NewService(st)

	detail, err := svc.ApplyRevalidation(context.Background(), revalTenant, revalIntake, rv, "aviso")
	if err != nil {
		t.Fatalf("ApplyRevalidation: %v", err)
	}
	if len(detail.Items) != 1 || detail.Items[0] != envío {
		t.Fatalf("líneas=%+v; quiero solo la de envío, intacta", detail.Items)
	}
	if detail.Total != 4 {
		t.Fatalf("total=%v; quiero 4 (solo el envío)", detail.Total)
	}
}

// TestApplyRevalidation_NoEsOpen: el rescate solo rescata solicitudes `open`
// (INV-17). Encontrarla en otro estado no se fuerza: ErrConflict y que el llamante
// relea. Re-preciar una `confirmed` cambiaría lo que el cliente ya aceptó.
func TestApplyRevalidation_NoEsOpen(t *testing.T) {
	st := intakes.NewMemoryStore()
	st.Add(revalTenant, intakes.Intake{
		ID: revalIntake, ContactID: "contacto-opaco", SessionID: "sess-a",
		Status: intakes.StatusConfirmed, Total: 7, CreatedAt: primeroDeEnero, UpdatedAt: primeroDeEnero,
	}, líneasDeMarta()...)
	rv := intakes.Revalidate(líneasDeMarta(), catálogoDelQuince())

	_, err := intakes.NewService(st).ApplyRevalidation(context.Background(), revalTenant, revalIntake, rv, "aviso")
	if !errors.Is(err, intakes.ErrConflict) {
		t.Fatalf("err=%v; quiero ErrConflict", err)
	}
}

// TestApplyRevalidation_OtroTenant: aislamiento (INV-8) — 404 opaco.
func TestApplyRevalidation_OtroTenant(t *testing.T) {
	st := bandejaConMarta()
	rv := intakes.Revalidate(líneasDeMarta(), catálogoDelQuince())

	_, err := intakes.NewService(st).ApplyRevalidation(context.Background(), "otro-tenant", revalIntake, rv, "aviso")
	if !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("err=%v; quiero ErrNotFound", err)
	}
}

// TestApplyRevalidation_TextoGuardadoEsElQueSeManda es el criterio (f), y la razón
// de que este archivo importe el módulo `cart`: el string que se persiste en
// `rendered_text` se compara con el que produce EL MISMO renderizador que le habla
// al cliente, byte a byte y sin "contiene". Si mañana alguien renderiza el aviso por
// segunda vez en otro sitio, o le mete un salto de línea de más al guardarlo, esto
// se rompe.
func TestApplyRevalidation_TextoGuardadoEsElQueSeManda(t *testing.T) {
	st := bandejaConMarta()
	catálogo := cart.PriceListOf(cart.Catalog{Categories: []cart.Category{{
		Code: "1", Label: "Panadería",
		Items: []cart.Article{{Code: "1", SKU: "PAN", Label: "Pan", Price: 2.50}},
	}}})

	rv := intakes.Revalidate(líneasDeMarta(), catálogo)
	mandado := cart.RevalidationMessage(rv, primeroDeEnero, "")
	if mandado == "" {
		t.Fatal("hubo cambios: el mensaje no puede venir vacío")
	}

	detail, err := intakes.NewService(st).ApplyRevalidation(context.Background(), revalTenant, revalIntake, rv, mandado)
	if err != nil {
		t.Fatalf("ApplyRevalidation: %v", err)
	}
	if guardado := detail.Revisions[0].RenderedText; guardado != mandado {
		t.Fatalf("lo guardado NO es byte a byte lo mandado.\n--- guardado ---\n%s\n--- mandado ---\n%s", guardado, mandado)
	}
}
