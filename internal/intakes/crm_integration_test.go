package intakes_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// leerReflejo devuelve las tres columnas del reflejo más los dos timestamps que este
// test vigila. Se lee por SQL DIRECTO y no por el store: lo que se comprueba es lo
// que quedó ESCRITO, no lo que el store dice que escribió.
func leerReflejo(ctx context.Context, t *testing.T, db *sql.DB, id string) (crmStatus, extRef sql.NullString,
	syncedAt sql.NullTime, updatedAt time.Time) {
	t.Helper()
	if err := db.QueryRowContext(ctx, `
		SELECT crm_status, crm_external_ref, crm_synced_at, updated_at
		FROM public.intakes WHERE id = $1
	`, id).Scan(&crmStatus, &extRef, &syncedAt, &updatedAt); err != nil {
		t.Fatalf("leyendo el reflejo de %s: %v", id, err)
	}
	return
}

// TestReflectCRMStatus_AplicaYEsIdempotente es el criterio de T4.3 contra Postgres
// real, en la secuencia que de verdad ocurre: llega un estado, llega el MISMO otra
// vez (un puente con reintentos), y llega uno distinto.
//
// Lo que separa este test de "el UPDATE funciona" son los dos timestamps: se mueven
// por separado y a propósito, y ninguna implementación que los trate igual pasa.
func TestReflectCRMStatus_AplicaYEsIdempotente(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	tenant := seedTenantPG(t, db)
	id := seedDescartePG(t, db, tenant, "se-crm", "ct-crm", "confirmed")
	ctx := context.Background()

	var updated1 time.Time
	var syncedAt sql.NullTime

	t.Run("el primer callback refleja y marca las dos horas", func(t *testing.T) {
		syncedAt, updated1 = primerReflejo(ctx, t, db, store, tenant, id)
	})
	t.Run("el MISMO estado otra vez es un no-op de negocio", func(t *testing.T) {
		reflejoRepetido(ctx, t, db, store, tenant, id, syncedAt, updated1)
	})
	t.Run("un estado distinto sí cambia el negocio", func(t *testing.T) {
		reflejoDistinto(ctx, t, db, store, tenant, id, updated1)
	})
}

// primerReflejo aplica el primer callback y comprueba lo que deja escrito. Devuelve
// las dos marcas que las fases siguientes comparan.
func primerReflejo(ctx context.Context, t *testing.T, db *sql.DB, store *intakes.Postgres,
	tenant, id string) (sql.NullTime, time.Time) {
	t.Helper()
	t0 := time.Now().UTC().Truncate(time.Millisecond)
	ref, err := store.ReflectCRMStatus(ctx, tenant, id, intakes.CRMStatusPaid, "F-2026-0001", t0)
	if err != nil {
		t.Fatalf("primer reflejo: %v", err)
	}
	if !ref.Found || !ref.Changed {
		t.Fatalf("el primer reflejo debía encontrar y cambiar: %+v", ref)
	}
	crmStatus, extRef, syncedAt, updated1 := leerReflejo(ctx, t, db, id)
	if crmStatus.String != intakes.CRMStatusPaid {
		t.Fatalf("crm_status = %q, esperaba paid", crmStatus.String)
	}
	if extRef.String != "F-2026-0001" {
		t.Fatalf("crm_external_ref = %q: la referencia del CRM se guarda VERBATIM", extRef.String)
	}
	if !syncedAt.Valid {
		t.Fatal("crm_synced_at quedó NULL: sin ella no se puede detectar una integración muda")
	}

	// El estado del DUEÑO no se toca. Es la mitad que hace disjuntos los vocabularios:
	// wApp jamás pisa su propio ciclo de vida con lo que diga un CRM (ADR-0031/D-042.6).
	var statusDueño string
	if err := db.QueryRowContext(ctx, `SELECT status FROM public.intakes WHERE id=$1`, id).
		Scan(&statusDueño); err != nil {
		t.Fatalf("leyendo el status del dueño: %v", err)
	}
	if statusDueño != "confirmed" {
		t.Fatalf("el reflejo del CRM pisó intakes.status: %q — los dos vocabularios son DISJUNTOS", statusDueño)
	}
	return syncedAt, updated1
}

// reflejoRepetido es el reintento del puente: mismo estado, mismos datos.
func reflejoRepetido(ctx context.Context, t *testing.T, db *sql.DB, store *intakes.Postgres,
	tenant, id string, syncedAt sql.NullTime, updated1 time.Time) {
	t.Helper()
	time.Sleep(10 * time.Millisecond) // que los relojes puedan distinguirse
	t1 := time.Now().UTC().Truncate(time.Millisecond)
	ref2, err := store.ReflectCRMStatus(ctx, tenant, id, intakes.CRMStatusPaid, "F-2026-0001", t1)
	if err != nil {
		t.Fatalf("segundo reflejo: %v", err)
	}
	if !ref2.Found {
		t.Fatal("la solicitud sigue existiendo")
	}
	if ref2.Changed {
		t.Fatal("un callback idéntico NO cambia nada: si dice que cambió, se avisará al cliente otra vez")
	}
	_, _, syncedAt2, updated2 := leerReflejo(ctx, t, db, id)

	if !updated2.Equal(updated1) {
		t.Fatalf("updated_at se movió sin que cambiara nada (%v → %v): un puente con reintentos "+
			"dejaría toda la bandeja del dueño «recién tocada»", updated1, updated2)
	}
	if !syncedAt2.Time.After(syncedAt.Time) {
		t.Fatalf("crm_synced_at NO avanzó (%v → %v): un puente que repite el mismo estado está VIVO, "+
			"y congelarle la marca lo haría parecer caído", syncedAt.Time, syncedAt2.Time)
	}
}

// reflejoDistinto cierra la secuencia con un estado nuevo, que sí es un cambio.
func reflejoDistinto(ctx context.Context, t *testing.T, db *sql.DB, store *intakes.Postgres,
	tenant, id string, updated1 time.Time) {
	t.Helper()
	t2 := time.Now().UTC().Truncate(time.Millisecond)
	ref3, err := store.ReflectCRMStatus(ctx, tenant, id, intakes.CRMStatusDelivered, "", t2)
	if err != nil {
		t.Fatalf("tercer reflejo: %v", err)
	}
	if !ref3.Changed {
		t.Fatal("un estado distinto SÍ es un cambio")
	}
	crmStatus3, extRef3, _, updated3 := leerReflejo(ctx, t, db, id)
	if crmStatus3.String != intakes.CRMStatusDelivered {
		t.Fatalf("crm_status = %q, esperaba delivered", crmStatus3.String)
	}
	if extRef3.String != "F-2026-0001" {
		t.Fatalf("un external_ref VACÍO significa «no me pronuncio», no «bórrala»: quedó %q", extRef3.String)
	}
	if !updated3.After(updated1) {
		t.Fatal("un cambio real SÍ debe mover updated_at")
	}
}

// TestReflectCRMStatus_DeOtroTenant_NoEncuentraNiToca es INV-8 en la vuelta del
// puente: el tenant acota el UPDATE, así que una solicitud ajena es indistinguible de
// una inexistente Y —lo que más importa— no se toca.
func TestReflectCRMStatus_DeOtroTenant_NoEncuentraNiToca(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	dueño := seedTenantPG(t, db)
	ajeno := seedTenantPG(t, db)
	id := seedDescartePG(t, db, dueño, "se-crm", "ct-crm", "confirmed")
	ctx := context.Background()

	// El tenant legítimo refleja primero, para que haya algo que estropear.
	if _, err := store.ReflectCRMStatus(ctx, dueño, id, intakes.CRMStatusPaid, "F-1", time.Now().UTC()); err != nil {
		t.Fatalf("reflejo del dueño: %v", err)
	}
	antes, extAntes, _, updatedAntes := leerReflejo(ctx, t, db, id)

	// Ahora el vecino, con el id correcto pero su propio tenant.
	ref, err := store.ReflectCRMStatus(ctx, ajeno, id, intakes.CRMStatusRejected, "HACK", time.Now().UTC())
	if err != nil {
		t.Fatalf("el intento ajeno no es un error de infraestructura: %v", err)
	}
	if ref.Found {
		t.Fatal("una solicitud de otro tenant NO puede encontrarse: eso permitiría sondear ids ajenos")
	}

	después, extDespués, _, updatedDespués := leerReflejo(ctx, t, db, id)
	if después.String != antes.String || extDespués.String != extAntes.String ||
		!updatedDespués.Equal(updatedAntes) {
		t.Fatalf("el tenant ajeno ESCRIBIÓ sobre la solicitud: %q/%q → %q/%q",
			antes.String, extAntes.String, después.String, extDespués.String)
	}
}

// TestReflectCRMStatus_EstadoNoCanonico_NoEscribe es defensa en profundidad: la
// frontera ya valida contra el schema publicado, así que si esto salta es porque
// apareció un llamador nuevo que se la saltó. Lo que no puede pasar es que un estado
// inventado llegue a la columna y reviente el CHECK de la 0048 a mitad de camino.
func TestReflectCRMStatus_EstadoNoCanonico_NoEscribe(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	tenant := seedTenantPG(t, db)
	id := seedDescartePG(t, db, tenant, "se-crm", "ct-crm", "confirmed")

	ctx := context.Background()
	if _, err := store.ReflectCRMStatus(ctx, tenant, id, "shipped", "", time.Now().UTC()); err == nil {
		t.Fatal("un estado fuera de los cuatro canónicos debía rechazarse en el dominio")
	}
	crmStatus, _, syncedAt, _ := leerReflejo(ctx, t, db, id)
	if crmStatus.Valid || syncedAt.Valid {
		t.Fatalf("un estado no canónico dejó rastro: crm_status=%v crm_synced_at=%v", crmStatus, syncedAt)
	}
}
