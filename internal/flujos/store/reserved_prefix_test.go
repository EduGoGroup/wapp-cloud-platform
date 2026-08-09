package store

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// TestReservedSKUPrefix_EsElMismoQueElDeSolicitudes ata el literal que este paquete
// usa para NO borrar las líneas de la plataforma con el que las declara su dueño
// (intakes.ReservedSKUPrefix, D-041.11).
//
// El almacén del motor de flujos no importa el dominio de solicitudes a propósito
// —escribe una tabla, no habla su idioma—, así que el literal está repetido. Lo que
// impide que diverjan es este test: si alguien cambiara el prefijo en solicitudes,
// el DELETE de replaceIntakeItemsTx dejaría de reconocer la línea de envío y el
// siguiente cierre se la llevaría por delante, sin error y sin que nadie lo viera
// hasta el pedido mal cobrado.
func TestReservedSKUPrefix_EsElMismoQueElDeSolicitudes(t *testing.T) {
	if reservedSKUPrefix != intakes.ReservedSKUPrefix {
		t.Fatalf("prefijo reservado del almacén (%q) ≠ el de solicitudes (%q)",
			reservedSKUPrefix, intakes.ReservedSKUPrefix)
	}
}

// TestReplaceIntakeItems_ConservaLaLíneaDeLaPlataforma: el reemplazo se lleva las
// líneas del cliente y NO la que puso wApp. Hoy las dos no coinciden en el tiempo
// (la de envío se cuelga después del cierre), y precisamente por eso se prueba: es
// una salvaguarda para cuando ese orden cambie, y una salvaguarda que nadie ejercita
// es una que no se sabe si funciona.
func TestReplaceIntakeItems_ConservaLaLíneaDeLaPlataforma(t *testing.T) {
	r := NewMemoryRepository()
	ctx := t.Context()
	const id = "solicitud-1"

	if err := r.ReplaceIntakeItems(ctx, id, []IntakeItem{
		{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1, UnitPrice: 3000},
		{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5},
	}); err != nil {
		t.Fatalf("ReplaceIntakeItems (siembra): %v", err)
	}

	if err := r.ReplaceIntakeItems(ctx, id, []IntakeItem{
		{SKU: "TE", Label: "Té", Qty: 2, UnitPrice: 2.0},
	}); err != nil {
		t.Fatalf("ReplaceIntakeItems: %v", err)
	}

	items := r.IntakeItems(id)
	if len(items) != 2 {
		t.Fatalf("líneas: got %d, want 2 (envío + té)\n%+v", len(items), items)
	}
	if items[0].SKU != intakes.ShippingSKU {
		t.Errorf("la línea de la plataforma no sobrevivió al reemplazo: %+v", items)
	}
	if items[1].SKU != "TE" || items[1].Qty != 2 {
		t.Errorf("la línea de cliente no es la nueva: %+v", items[1])
	}
}

// TestReplaceIntakeItems_ConFotoVacíaBorraLasDelCliente: len(items)==0 NO es un
// no-op, es "el carrito no tiene líneas". Quien no sepa qué líneas hay no debe
// llamar (ver cart.Projector, que distingue la foto vacía de la ausencia de foto).
func TestReplaceIntakeItems_ConFotoVacíaBorraLasDelCliente(t *testing.T) {
	r := NewMemoryRepository()
	ctx := t.Context()
	const id = "solicitud-2"

	if err := r.ReplaceIntakeItems(ctx, id, []IntakeItem{{SKU: "CAFE", Qty: 1}}); err != nil {
		t.Fatalf("ReplaceIntakeItems (siembra): %v", err)
	}
	if err := r.ReplaceIntakeItems(ctx, id, nil); err != nil {
		t.Fatalf("ReplaceIntakeItems (vacío): %v", err)
	}
	if items := r.IntakeItems(id); len(items) != 0 {
		t.Fatalf("líneas tras la foto vacía: got %d, want 0 (%+v)", len(items), items)
	}
}
