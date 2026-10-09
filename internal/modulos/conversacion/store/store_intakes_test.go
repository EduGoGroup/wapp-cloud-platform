package store_test

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// projectionStore es lo que el proyector del carrito pide del almacén: solo la escritura.
type projectionStore interface {
	store.IntakeWriter
}

// Aserciones de compilación: los dos adaptadores leen y escriben solicitudes; IntakeStore es
// exactamente la suma de las dos caras, y quien solo escribe puede pedir solo IntakeWriter.
var (
	_ store.IntakeStore  = (*store.MemoryRepository)(nil)
	_ store.IntakeStore  = (*store.PostgresRepository)(nil)
	_ store.IntakeReader = store.IntakeStore(nil)
	_ store.IntakeWriter = store.IntakeStore(nil)
	_ projectionStore    = (*store.MemoryRepository)(nil)
	_ store.IntakeStore  = interface {
		store.IntakeReader
		store.IntakeWriter
	}(nil)
)

// TestIntakeTypes_CarryTheirFields: la cabecera, la línea y la entrada del cierre llevan los
// campos que el puerto promete.
func TestIntakeTypes_CarryTheirFields(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	item := store.IntakeItem{IntakeID: "i", SKU: "CAFE", Label: "Café", Customization: "sin azúcar",
		Qty: 2, UnitPrice: 2.5, AddedAt: at}
	header := store.Intake{ID: "i", TenantID: "t", ContactID: "c", SessionID: "s", Status: "open", Total: 5,
		EventID: "e", CreatedAt: at, UpdatedAt: at, ExpiresAt: at, CustomerNote: "Portería"}
	closing := store.IntakeClose{TenantID: "t", ContactID: "c", SessionID: "s", Total: 5,
		CustomerNote: "Portería", EventID: "e", Items: []store.IntakeItem{item}}

	requireEqual(t, "IntakeItem.Customization", item.Customization, "sin azúcar")
	requireEqual(t, "IntakeItem.UnitPrice", item.UnitPrice, 2.5)
	requireEqual(t, "IntakeItem.AddedAt", item.AddedAt, at)
	requireEqual(t, "Intake.EventID", header.EventID, "e")
	requireEqual(t, "Intake.CustomerNote", header.CustomerNote, "Portería")
	requireEqual(t, "Intake.ExpiresAt", header.ExpiresAt, at)
	requireEqual(t, "IntakeClose.CustomerNote", closing.CustomerNote, "Portería")
	requireEqual(t, "IntakeClose.EventID", closing.EventID, "e")
	requireEqual(t, "IntakeClose.Items", closing.Items[0], item)
}
