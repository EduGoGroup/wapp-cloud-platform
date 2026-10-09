package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// Aserciones de compilación de lo que promete repository_memory_intakes.go.
var (
	_ func(*store.MemoryRepository, context.Context, store.Intake) error                         = (*store.MemoryRepository).UpsertIntake
	_ func(*store.MemoryRepository, context.Context, string, string) (store.Intake, bool, error) = (*store.MemoryRepository).GetOpenIntake
	_ func(*store.MemoryRepository, context.Context, string, string) (store.Intake, bool, error) = (*store.MemoryRepository).GetIntakeByEvent
	_ func(*store.MemoryRepository, context.Context, string) ([]store.IntakeItem, error)         = (*store.MemoryRepository).ListIntakeItems
	_ func(*store.MemoryRepository, context.Context, string, []store.IntakeItem) error           = (*store.MemoryRepository).ReplaceIntakeItems
	_ func(*store.MemoryRepository, context.Context, string, string, float64) error              = (*store.MemoryRepository).MarkIntakeStatus
	_ func(*store.MemoryRepository, context.Context, store.IntakeClose) (string, error)          = (*store.MemoryRepository).CloseIntake
	_ func(*store.MemoryRepository) []store.Intake                                               = (*store.MemoryRepository).Intakes
	_ func(*store.MemoryRepository, string) []store.IntakeItem                                   = (*store.MemoryRepository).IntakeItems
)

// intakeByID busca una solicitud en lo que devuelve el mirador Intakes.
func intakeByID(t *testing.T, repo *store.MemoryRepository, id string) store.Intake {
	t.Helper()
	for _, in := range repo.Intakes() {
		if in.ID == id {
			return in
		}
	}
	t.Fatalf("la solicitud %s no está en Intakes()", id)
	return store.Intake{}
}

// mustUpsert guarda la cabecera o falla el test.
func mustUpsert(t *testing.T, repo *store.MemoryRepository, in store.Intake) {
	t.Helper()
	if err := repo.UpsertIntake(ctx, in); err != nil {
		t.Fatalf("UpsertIntake(%s): %v", in.ID, err)
	}
}

// TestMemoryRepository_LegacyIntakeWithoutEvent: lo que la base ya no deja sembrar. Una solicitud
// SIN evento padre (la fila legada pre-0054) se acepta, y su padre vacío SÍ se estampa: con el
// primer UpsertIntake que lo traiga, o con el cierre. Una vez declarado, nada lo pisa.
func TestMemoryRepository_LegacyIntakeWithoutEvent(t *testing.T) {
	repo, _ := newMemoryRepository()

	mustUpsert(t, repo, store.Intake{ID: "solicitud-1", TenantID: "t1", ContactID: "c1", Status: "open"})
	if got := intakeByID(t, repo, "solicitud-1").EventID; got != "" {
		t.Fatalf("la solicitud legada nació con EventID %q, quería vacío", got)
	}
	mustUpsert(t, repo, store.Intake{ID: "solicitud-1", TenantID: "t1", ContactID: "c1", Status: "open", EventID: "e-1"})
	mustUpsert(t, repo, store.Intake{ID: "solicitud-1", TenantID: "t1", ContactID: "c1", Status: "open", EventID: "e-2"})
	mustUpsert(t, repo, store.Intake{ID: "solicitud-1", TenantID: "t1", ContactID: "c1", Status: "open"})
	if got := intakeByID(t, repo, "solicitud-1").EventID; got != "e-1" {
		t.Errorf("EventID = %q, quería el primero que se declaró (e-1): ni otro valor ni el vacío lo pisan", got)
	}

	mustUpsert(t, repo, store.Intake{ID: "solicitud-2", TenantID: "t1", ContactID: "c2", Status: "open"})
	closed, err := repo.CloseIntake(ctx, store.IntakeClose{TenantID: "t1", ContactID: "c2", EventID: "e-cierre"})
	if err != nil || closed != "solicitud-2" {
		t.Fatalf("CloseIntake = (%q, %v), quería cerrar la legada", closed, err)
	}
	if got := intakeByID(t, repo, "solicitud-2").EventID; got != "e-cierre" {
		t.Errorf("el cierre dejó EventID = %q, quería que rellenara el vacío con el suyo", got)
	}
}

// TestMemoryRepository_GetIntakeByEvent_TieBreakAndOpaqueIDs: aquí ningún índice impide que dos
// solicitudes declaren el mismo evento. Ante varias se devuelve la MÁS ANTIGUA (created_at, y a
// igual fecha el id menor): la que ya estaba. Los ids se comparan como cadenas opacas —uno que no
// es un UUID se encuentra—, y la cadena vacía no busca nada aunque haya filas legadas sin evento.
func TestMemoryRepository_GetIntakeByEvent_TieBreakAndOpaqueIDs(t *testing.T) {
	repo, clock := newMemoryRepository()
	mustUpsert(t, repo, store.Intake{ID: "legada", TenantID: "t1", ContactID: "c0", Status: "open"})
	mustUpsert(t, repo, store.Intake{ID: "b-misma-fecha", TenantID: "t1", ContactID: "c1", Status: "closed", EventID: "evento-opaco"})
	mustUpsert(t, repo, store.Intake{ID: "a-misma-fecha", TenantID: "t1", ContactID: "c2", Status: "open", EventID: "evento-opaco"})
	clock.Advance(time.Minute)
	mustUpsert(t, repo, store.Intake{ID: "0-posterior", TenantID: "t1", ContactID: "c3", Status: "open", EventID: "evento-opaco"})

	for range 20 { // el recorrido de un mapa cambia de una vuelta a otra: el desempate no.
		got, found, err := repo.GetIntakeByEvent(ctx, "t1", "evento-opaco")
		if err != nil || !found || got.ID != "a-misma-fecha" {
			t.Fatalf("GetIntakeByEvent = (%q, %v, %v), quería la más antigua y, a igual fecha, el id menor", got.ID, found, err)
		}
	}
	if got, found, err := repo.GetIntakeByEvent(ctx, "t1", ""); err != nil || found || got.ID != "" {
		t.Errorf("GetIntakeByEvent con la cadena vacía = (%q, %v, %v), quería (vacío, false, nil)", got.ID, found, err)
	}
	if _, found, err := repo.GetIntakeByEvent(ctx, "t2", "evento-opaco"); err != nil || found {
		t.Errorf("GetIntakeByEvent desde t2 = (found %v, %v), quería (false, nil): la solicitud es de t1", found, err)
	}
}

// seedClosedAndPending deja dos solicitudes: "de-t1", cerrada con nota y dos líneas, y "de-t2",
// pendiente de aprobación en otro tenant.
func seedClosedAndPending(t *testing.T, repo *store.MemoryRepository) {
	t.Helper()
	mustUpsert(t, repo, store.Intake{ID: "de-t1", TenantID: "t1", ContactID: "c1", Status: "open", EventID: "e-1"})
	mustUpsert(t, repo, store.Intake{ID: "de-t2", TenantID: "t2", ContactID: "c1", Status: "pending_approval", EventID: "e-2"})
	if _, err := repo.CloseIntake(ctx, store.IntakeClose{TenantID: "t1", ContactID: "c1", Total: 5, CustomerNote: "Portería",
		Items: []store.IntakeItem{{SKU: "CAFE", Qty: 2, UnitPrice: 2.5}, {SKU: "TE", Qty: 1}}}); err != nil {
		t.Fatalf("CloseIntake: %v", err)
	}
}

// TestMemoryRepository_Intakes_Mirror: Intakes devuelve TODAS las solicitudes de todos los tenants
// con la fila entera —la nota que puso el cierre incluida, que las lecturas del puerto no traen—, y
// una copia.
func TestMemoryRepository_Intakes_Mirror(t *testing.T) {
	repo, _ := newMemoryRepository()
	seedClosedAndPending(t, repo)

	all := repo.Intakes()
	requireEqual(t, "solicitudes de los dos tenants", len(all), 2)
	closed := intakeByID(t, repo, "de-t1")
	requireEqual(t, "estado de la cerrada", closed.Status, "closed")
	requireEqual(t, "total de la cerrada", closed.Total, 5)
	requireEqual(t, "nota de la cerrada, por el mirador", closed.CustomerNote, "Portería")

	header, found, err := repo.GetIntakeByEvent(ctx, "t1", "e-1")
	if err != nil || !found {
		t.Fatalf("GetIntakeByEvent = (found %v, %v), quería la cerrada", found, err)
	}
	requireEqual(t, "nota de la cerrada, por el puerto", header.CustomerNote, "")

	all[0].Status = "mutado"
	all[1].Status = "mutado"
	requireEqual(t, "estado guardado tras mutar lo que devuelve Intakes", intakeByID(t, repo, "de-t2").Status, "pending_approval")
}

// TestMemoryRepository_IntakeItems_Mirror: IntakeItems devuelve las líneas de la solicitud en su
// orden, con su solicitud y su fecha, y una copia. De una solicitud desconocida, ninguna.
func TestMemoryRepository_IntakeItems_Mirror(t *testing.T) {
	repo, clock := newMemoryRepository()
	seedClosedAndPending(t, repo)

	items := repo.IntakeItems("de-t1")
	requireEqual(t, "líneas del cierre", len(items), 2)
	requireEqual(t, "primera línea", items[0], store.IntakeItem{IntakeID: "de-t1", SKU: "CAFE", Qty: 2, UnitPrice: 2.5, AddedAt: clock.Now()})
	requireEqual(t, "segunda línea", items[1], store.IntakeItem{IntakeID: "de-t1", SKU: "TE", Qty: 1, AddedAt: clock.Now()})

	items[0].SKU = "mutado"
	requireEqual(t, "sku guardado tras mutar lo que devuelve IntakeItems", repo.IntakeItems("de-t1")[0].SKU, "CAFE")
	requireEqual(t, "líneas de una solicitud desconocida", len(repo.IntakeItems("no-existe")), 0)
}

// TestMemoryRepository_Items_NonUUIDAndOwnTimestamps: el reemplazo de líneas acepta un id que no es
// un UUID (ListIntakeItems, en cambio, lo rechaza como el Postgres), respeta el AddedAt que traiga
// una línea y fecha con el reloj las que no; MarkIntakeStatus tampoco exige UUID.
func TestMemoryRepository_Items_NonUUIDAndOwnTimestamps(t *testing.T) {
	repo, clock := newMemoryRepository()
	own := time.Date(2020, 5, 5, 0, 0, 0, 0, time.UTC)
	mustUpsert(t, repo, store.Intake{ID: "solicitud-1", TenantID: "t1", ContactID: "c1", Status: "open"})
	if err := repo.ReplaceIntakeItems(ctx, "solicitud-1", []store.IntakeItem{
		{SKU: "_shipping", Label: "Envío", Qty: 1, UnitPrice: 3000, AddedAt: own},
		{SKU: "CAFE", Qty: 1, UnitPrice: 2.5},
	}); err != nil {
		t.Fatalf("ReplaceIntakeItems: %v", err)
	}
	clock.Advance(time.Minute)
	if err := repo.ReplaceIntakeItems(ctx, "solicitud-1", []store.IntakeItem{{SKU: "TE", Qty: 2}}); err != nil {
		t.Fatalf("segundo ReplaceIntakeItems: %v", err)
	}

	items := repo.IntakeItems("solicitud-1")
	if len(items) != 2 || items[0].SKU != "_shipping" || items[1].SKU != "TE" {
		t.Fatalf("IntakeItems = %+v, quería la línea de la plataforma al frente y la foto nueva", items)
	}
	if !items[0].AddedAt.Equal(own) || !items[1].AddedAt.Equal(clock.Now()) {
		t.Errorf("fechas = (%v, %v), quería la propia de la línea y la del reloj", items[0].AddedAt, items[1].AddedAt)
	}
	if _, err := repo.ListIntakeItems(ctx, "solicitud-1"); err == nil {
		t.Error("ListIntakeItems aceptó un id que no es un UUID; quería el mismo error que da Postgres")
	}

	if err := repo.MarkIntakeStatus(ctx, "solicitud-1", "expired", 9); err != nil {
		t.Fatalf("MarkIntakeStatus: %v", err)
	}
	if got := intakeByID(t, repo, "solicitud-1"); got.Status != "expired" || got.Total != 9 {
		t.Errorf("tras MarkIntakeStatus = %+v, quería (expired, 9)", got)
	}
	if _, found, err := repo.GetOpenIntake(ctx, "t1", "c1"); err != nil || found {
		t.Errorf("GetOpenIntake tras expirar = (found %v, %v), quería (false, nil)", found, err)
	}
}

// TestMemoryRepository_ReservedPrefix_IsTheOneOfIntakes: el prefijo reservado de este almacén es
// el MISMO que el del dominio de solicitudes, de quien es la regla. El literal se repite aquí en
// vez de importarse (el almacén del motor de flujos no depende de solicitudes para escribir una
// tabla), y lo que impide que los dos diverjan es este test: las líneas que solicitudes considera
// de la plataforma —la de envío y cualquier sku con su prefijo— sobreviven a una foto del carrito.
func TestMemoryRepository_ReservedPrefix_IsTheOneOfIntakes(t *testing.T) {
	repo, _ := newMemoryRepository()
	other := intakes.ReservedSKUPrefix + "otra"
	if err := repo.ReplaceIntakeItems(ctx, "solicitud-1", []store.IntakeItem{
		{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1, UnitPrice: 3000},
		{SKU: other, Qty: 1},
		{SKU: "CAFE", Qty: 1},
	}); err != nil {
		t.Fatalf("ReplaceIntakeItems: %v", err)
	}
	if err := repo.ReplaceIntakeItems(ctx, "solicitud-1", []store.IntakeItem{{SKU: "TE", Qty: 2}}); err != nil {
		t.Fatalf("segundo ReplaceIntakeItems: %v", err)
	}

	items := repo.IntakeItems("solicitud-1")
	requireEqual(t, "líneas tras la foto del carrito", len(items), 3)
	for i, want := range []string{intakes.ShippingSKU, other, "TE"} {
		requireEqual(t, "sku de la línea", items[i].SKU, want)
	}
}
