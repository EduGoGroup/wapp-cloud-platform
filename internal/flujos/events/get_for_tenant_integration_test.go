// get_for_tenant_integration_test.go fija contra POSTGRES REAL la lectura acotada
// al tenant (Plan 043 · Ola 4 · T4.2): GetEventForTenant devuelve la fila del
// dueño y contesta el MISMO ErrEventNotFound a todo lo demás — id inexistente, id
// de OTRO tenant, id que ni siquiera es un UUID. Esa indistinción es el criterio
// («404 para un evento de otro tenant, nunca 403») escrito en la capa que lo
// garantiza: el WHERE con tenant_id.
package events_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// TestIntegration_GetEventForTenant_DevuelveLaFilaDelDueno: el camino feliz lee
// la fila entera, con los mismos campos que selló CreateEvent.
func TestIntegration_GetEventForTenant_DevuelveLaFilaDelDueno(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	ctx := context.Background()
	store, _ := nuevoStore(t, db, time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC))

	ev, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "t41-sesion-get", contactoA, "cart"))
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	got, err := store.GetEventForTenant(ctx, tenantID, ev.ID)
	if err != nil {
		t.Fatalf("GetEventForTenant: %v", err)
	}
	if got.ID != ev.ID || got.TenantID != tenantID || got.Kind != "cart" ||
		got.SessionID != "t41-sesion-get" || got.ContactID != contactoA ||
		got.Status != events.StatusOpen || got.HistoryID != ev.HistoryID {
		t.Fatalf("la fila leída no es la creada: %+v vs %+v", got, ev)
	}
}

// TestIntegration_GetEventForTenant_ElAjenoYElInexistenteSonLaMismaAusencia: un
// evento REAL de otro tenant y un UUID que no existe contestan el MISMO sentinela.
// Si esto divergiera, el endpoint podría filtrar existencia cruzada sin querer.
func TestIntegration_GetEventForTenant_ElAjenoYElInexistenteSonLaMismaAusencia(t *testing.T) {
	db := openTestDB(t)
	duenoA := seedTenant(t, db)
	duenoB := seedTenant(t, db)
	ctx := context.Background()
	store, _ := nuevoStore(t, db, time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC))

	deA, err := store.CreateEvent(ctx, nuevoEvento(duenoA, "t41-sesion-ajeno", contactoA, "cart"))
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	if _, err := store.GetEventForTenant(ctx, duenoB, deA.ID); !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("el evento de A leído por B debe ser ErrEventNotFound, y fue %v", err)
	}
	if _, err := store.GetEventForTenant(ctx, duenoA, "00000000-0000-4000-8000-00000000dead"); !errors.Is(err, events.ErrEventNotFound) {
		t.Fatalf("un UUID inexistente debe ser ErrEventNotFound, y fue %v", err)
	}
	// Y la fila de A sigue siendo legible por A: la ausencia era del PAR, no del id.
	if _, err := store.GetEventForTenant(ctx, duenoA, deA.ID); err != nil {
		t.Fatalf("A debe seguir leyendo lo suyo: %v", err)
	}
}

// TestIntegration_GetEventForTenant_UnIdQueNoEsUUIDNoEsUnError500: la guarda del
// UUID convierte la basura del path («abc», un history_id pegado por error) en el
// mismo not-found, no en el 22P02 de Postgres que el transporte traduciría a 500.
func TestIntegration_GetEventForTenant_UnIdQueNoEsUUIDNoEsUnError500(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	ctx := context.Background()
	store, _ := nuevoStore(t, db, time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC))

	for _, basura := range []string{"abc", "cart-2026-08-10-0900", ""} {
		if _, err := store.GetEventForTenant(ctx, tenantID, basura); !errors.Is(err, events.ErrEventNotFound) {
			t.Fatalf("GetEventForTenant(%q) debe ser ErrEventNotFound, y fue %v", basura, err)
		}
	}
}
