package cart_test

import (
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// effects_payload_test.go — qué lleva cada efecto que el carrito declara (effects.go):
// Kind, payload público, claves privadas y la foto de líneas.

const kindEvent, kindPersist = "event", "persist"

// category_selected {category_code} e item_viewed {sku}: telemetría, sin nada privado.
func TestEffect_CategorySelectedAndItemViewed(t *testing.T) {
	m := cart.New()
	_, effs, vars := driveEffects(t, m, seededVars(), "2") // Postres
	e := effectNamed(t, effs, cart.EffectCategorySelected)
	if e.Kind != kindEvent || jsonOf(t, e.Payload) != `{"category_code":"2"}` || len(e.PrivateKeys) != 0 {
		t.Errorf("category_selected = %+v", e)
	}
	_, effs, vars = driveEffects(t, m, vars, "1") // Flan: elegir artículo no declara nada
	if len(effs) != 0 {
		t.Errorf("elegir artículo declaró %v, quiero nada", effectNames(effs))
	}
	_, effs, vars = driveEffects(t, m, vars, "1") // ver descripción
	if len(effs) != 1 {
		t.Fatalf("efectos = %v, quiero solo item_viewed", effectNames(effs))
	}
	e = effs[0]
	if e.Name != cart.EffectItemViewed || e.Kind != kindEvent || jsonOf(t, e.Payload) != `{"sku":"FLAN"}` || len(e.PrivateKeys) != 0 {
		t.Errorf("item_viewed = %+v", e)
	}
	_, effs, _ = driveEffects(t, m, vars, "0") // volver tampoco
	if len(effs) != 0 {
		t.Errorf("volver declaró %v, quiero nada", effectNames(effs))
	}
}

// item_added: el payload público de siempre y, privada, la foto COMPLETA del carrito
// ya mutado.
func TestEffect_ItemAddedCarriesThePrivateSnapshot(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2")
	_, effs, vars := driveEffects(t, m, vars, "2") // Café ×2
	if len(effs) != 1 {
		t.Fatalf("efectos = %v, quiero solo item_added", effectNames(effs))
	}
	e := effs[0]
	if e.Name != cart.EffectItemAdded || e.Kind != kindEvent {
		t.Fatalf("efecto = %+v, quiero item_added de Kind event", e)
	}
	if e.Payload["sku"] != "CAFE" || e.Payload["label"] != "Café" || e.Payload["qty"] != 2 || e.Payload["unit_price"] != 2.5 {
		t.Errorf("payload = %v, quiero sku/label/qty/unit_price nativos del Café ×2", e.Payload)
	}
	if got := jsonOf(t, e.PublicPayload()); got != `{"label":"Café","qty":2,"sku":"CAFE","unit_price":2.5}` {
		t.Errorf("payload público = %s: el flow_event de item_added no puede cambiar", got)
	}
	if !reflect.DeepEqual(e.PrivateKeys, []string{"items"}) {
		t.Errorf("PrivateKeys = %v, quiero [items]", e.PrivateKeys)
	}
	if got := jsonOf(t, e.Payload["items"]); got != `[{"label":"Café","qty":2,"sku":"CAFE","unit_price":2.5}]` {
		t.Errorf("foto = %s", got)
	}

	// La segunda línea: la foto trae las DOS, en orden.
	vars = walk(t, m, vars, "1", "2", "2") // agregar más → Té → agregar
	_, effs, _ = driveEffects(t, m, vars, "1")
	e = effectNamed(t, effs, cart.EffectItemAdded)
	want := `[{"label":"Café","qty":2,"sku":"CAFE","unit_price":2.5},{"label":"Té","qty":1,"sku":"TE","unit_price":2}]`
	if got := jsonOf(t, e.Payload["items"]); got != want {
		t.Errorf("foto = %s\nquiero %s", got, want)
	}
}

// cart_closed sin nota: Kind persist, items públicos como []map[string]any, total, y
// ni customer_note ni PrivateKeys.
func TestEffect_CartClosedWithoutNote(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2", "2", "2")
	_, effs, _ := driveEffects(t, m, vars, "1")
	e := effectNamed(t, effs, cart.EffectCartClosed)
	if e.Kind != kindPersist {
		t.Errorf("Kind = %q, quiero persist", e.Kind)
	}
	if e.Payload["total"] != 5.0 {
		t.Errorf("total = %v, quiero 5.0", e.Payload["total"])
	}
	items, ok := e.Payload["items"].([]map[string]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v, quiero una lista []map[string]any de una línea", e.Payload["items"])
	}
	if it := items[0]; it["sku"] != "CAFE" || it["label"] != "Café" || it["qty"] != 2 || it["unit_price"] != 2.5 || len(it) != 4 {
		t.Errorf("línea = %v, quiero solo sku/label/qty/unit_price", it)
	}
	if got := jsonOf(t, e.PublicPayload()); got != `{"items":[{"label":"Café","qty":2,"sku":"CAFE","unit_price":2.5}],"total":5}` {
		t.Errorf("payload público = %s", got)
	}
	if e.PrivateKeys != nil {
		t.Errorf("PrivateKeys = %v, quiero nil: sin nota no hay nada privado", e.PrivateKeys)
	}
}

// cart_closed con indicaciones: la de línea va en la línea (pública); la del pedido
// viaja como customer_note y se declara privada (defecto A2 del Plan 041). El total
// no se mueve (INV-13).
func TestEffect_CartClosedWithNotes(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2", "1", // Café ×1 → continue
		"3", "sin azúcar", // indicación de línea
		"2", "3", "Dejarlo en portería") // resumen → indicación de pedido
	_, effs, _ := driveEffects(t, m, vars, "1")
	e := effectNamed(t, effs, cart.EffectCartClosed)
	if e.Payload["customer_note"] != "Dejarlo en portería" {
		t.Errorf("customer_note = %v", e.Payload["customer_note"])
	}
	if !reflect.DeepEqual(e.PrivateKeys, []string{"customer_note"}) {
		t.Errorf("PrivateKeys = %v, quiero [customer_note]", e.PrivateKeys)
	}
	want := `{"items":[{"customization":"sin azúcar","label":"Café","qty":1,"sku":"CAFE","unit_price":2.5}],"total":2.5}`
	if got := jsonOf(t, e.PublicPayload()); got != want {
		t.Errorf("payload público = %s\nquiero %s", got, want)
	}
}

// cart_cancelled {}: telemetría, desde los dos menús.
func TestEffect_CartCancelled(t *testing.T) {
	for _, path := range [][]string{{"1", "1", "2", "1"}, {"1", "1", "2", "1", "2"}} {
		m := cart.New()
		vars := walk(t, m, seededVars(), path...)
		_, effs, _ := driveEffects(t, m, vars, "9")
		if len(effs) != 1 {
			t.Fatalf("efectos = %v, quiero solo cart_cancelled", effectNames(effs))
		}
		e := effs[0]
		if e.Name != cart.EffectCartCancelled || e.Kind != kindEvent || e.Payload == nil || len(e.Payload) != 0 || len(e.PrivateKeys) != 0 {
			t.Errorf("cart_cancelled = %+v, quiero Kind event y payload {}", e)
		}
	}
}

// note_added, ámbito "item": lleva el sku y el TEXTO (dato de producción) y la foto
// privada con la indicación ya puesta.
func TestEffect_NoteAddedItemScope(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2", "1", "3") // Café ×1 → texto de la indicación
	_, effs, _ := driveEffects(t, m, vars, "sin azúcar")
	if len(effs) != 1 {
		t.Fatalf("efectos = %v, quiero solo note_added", effectNames(effs))
	}
	e := effs[0]
	if e.Name != cart.EffectNoteAdded || e.Kind != kindEvent {
		t.Fatalf("efecto = %+v", e)
	}
	if got := jsonOf(t, e.PublicPayload()); got != `{"scope":"item","sku":"CAFE","text":"sin azúcar"}` {
		t.Errorf("payload público = %s", got)
	}
	if !reflect.DeepEqual(e.PrivateKeys, []string{"items"}) {
		t.Errorf("PrivateKeys = %v, quiero [items]", e.PrivateKeys)
	}
	want := `[{"customization":"sin azúcar","label":"Café","qty":1,"sku":"CAFE","unit_price":2.5}]`
	if got := jsonOf(t, e.Payload["items"]); got != want {
		t.Errorf("foto = %s\nquiero %s", got, want)
	}
}

// note_added con split (D-041.20): mismo ámbito "item" más split_from_qty, y la foto
// con la línea ya partida en ×(N-1) y ×1.
func TestEffect_NoteAddedSplit(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2", "3", "3", "2") // Café ×3 → solo para 1
	_, effs, _ := driveEffects(t, m, vars, "sin azúcar")
	e := effectNamed(t, effs, cart.EffectNoteAdded)
	if got := jsonOf(t, e.PublicPayload()); got != `{"scope":"item","sku":"CAFE","split_from_qty":3,"text":"sin azúcar"}` {
		t.Errorf("payload público = %s", got)
	}
	want := `[{"label":"Café","qty":2,"sku":"CAFE","unit_price":2.5},` +
		`{"customization":"sin azúcar","label":"Café","qty":1,"sku":"CAFE","unit_price":2.5}]`
	if got := jsonOf(t, e.Payload["items"]); got != want {
		t.Errorf("foto = %s\nquiero %s", got, want)
	}
}

// note_added, ámbito "order": el LARGO en runas y jamás el texto (REQ-33g).
func TestEffect_NoteAddedOrderScopeCarriesLengthNotText(t *testing.T) {
	m := cart.New()
	vars := walk(t, m, seededVars(), "1", "1", "2", "1", "2", "3") // resumen → texto del pedido
	_, effs, _ := driveEffects(t, m, vars, "Dejarlo en portería, ñandú")
	if len(effs) != 1 {
		t.Fatalf("efectos = %v, quiero solo note_added", effectNames(effs))
	}
	e := effs[0]
	if got := jsonOf(t, e.PublicPayload()); got != `{"len":26,"scope":"order"}` {
		t.Errorf("payload público = %s, quiero el largo en RUNAS (26) y ningún texto", got)
	}
	for key, v := range e.Payload {
		if s, ok := v.(string); ok && s != "order" {
			t.Errorf("la clave %q lleva texto: %q", key, s)
		}
	}
	if !reflect.DeepEqual(e.PrivateKeys, []string{"items"}) {
		t.Errorf("PrivateKeys = %v, quiero [items]", e.PrivateKeys)
	}
}

// buyer_data_captured: Kind privado, {key, value}, y nada más que publicar.
func TestEffect_BuyerDataCapturedIsPrivate(t *testing.T) {
	m := cart.New()
	vars := seededVars()
	vars[cart.VarBuyerFields] = []store.BuyerField{{Key: "rut", Label: "RUT", Required: true}, {Key: "dir", Label: "dirección", Required: true}}
	vars = walk(t, m, vars, "1", "1", "2", "1", "2", "1") // … confirmar ⇒ checklist
	_, effs, _ := driveEffects(t, m, vars, "12.345.678-5")
	if len(effs) != 1 {
		t.Fatalf("efectos = %v, quiero solo buyer_data_captured", effectNames(effs))
	}
	e := effs[0]
	if e.Name != cart.EffectBuyerDataCaptured || e.Kind != modules.KindPrivate {
		t.Fatalf("efecto = %+v, quiero buyer_data_captured de Kind %q", e, modules.KindPrivate)
	}
	if got := jsonOf(t, e.Payload); got != `{"key":"rut","value":"12.345.678-5"}` {
		t.Errorf("payload = %s", got)
	}
}
