package contact_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
)

// isDeadlock reporta si err (o algún error envuelto) es un deadlock de Postgres
// (SQLSTATE 40P01). El resolver envuelve los errores con %w, así que el
// *pgconn.PgError subyacente se recupera con errors.As.
func isDeadlock(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "40P01"
}

// TestIntegration_ContactResolve_ConcurrentUpsert_NoDeadlock reproduce el
// deadlock SQLSTATE 40P01 del procesado de entrantes bajo inundación de historial
// (Plan 026 · T4, journal 2026-07-09 "Follow-up del Cloud"):
//
// Un mismo contacto tiene DOS filas en public.contacts (phone_e164 + wa_lid, mismo
// contact_id). Bajo la ráfaga, whatsmeow enriquece la identidad de forma desigual:
// unos entrantes traen solo from_pn, otros solo from_lid, y TODOS con push_name.
// Cada Resolve:
//   - toma FOR UPDATE (lookupContactIDs) SOLO la fila de la ref presente
//     (phone-only → fila phone; lid-only → fila lid), y
//   - luego hace `UPDATE contacts SET push_name WHERE contact_id = X`, que bloquea
//     TODAS las filas del contacto (phone Y lid) en orden de scan.
//
// Resultado: la transacción phone-only retiene la fila phone y pide la lid; la
// lid-only retiene la lid y pide la phone → ciclo de locks → 40P01. El FOR UPDATE
// parcial y el UPDATE masivo adquieren locks en orden inconsistente: no lo cura
// ordenar refs (las refs son disjuntas), sí lo cura reintentar la transacción
// atómica ante 40P01 (el perdedor hace rollback limpio y reintenta tras el commit
// del ganador; el upsert es idempotente por ON CONFLICT).
//
// SIN el fix, algún Resolve devuelve 40P01 al llamante (test ROJO). CON el fix
// (retry acotado ante 40P01), ningún deadlock aflora (test VERDE). DSN-gated.
func TestIntegration_ContactResolve_ConcurrentUpsert_NoDeadlock(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	wipeContacts(t, db)
	tenant := seedTenant(t, db)

	r := newTestResolver(t, db)

	// Un contacto con AMBAS refs bajo el mismo contact_id (el estado tras varios
	// entrantes previos que fusionaron número y LID).
	phone := mustRef(t, contact.KindPhoneE164, "573001112233")
	lid := mustRef(t, contact.KindWALID, "998877665544")
	if _, err := r.Resolve(ctx, tenant, []contact.Ref{phone, lid}, "seed"); err != nil {
		t.Fatalf("sembrar contacto con ambas refs: %v", err)
	}

	// Ráfaga concurrente: mitad de los workers refrescan por phone-only, mitad por
	// lid-only, todos con push_name (dispara el UPDATE masivo que cruza los locks).
	const (
		workers = 16
		iters   = 60
	)
	var deadlocks, otherErrs int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := range workers {
		phoneOnly := w%2 == 0
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// push_name REALISTA: un contacto tiene UN push_name; todos los
				// entrantes lo repiten (distinto del "seed" inicial, así la primera
				// oleada concurrente sí lo cambia y ejercita el reintento; luego el
				// guard IS DISTINCT lo vuelve no-op). Las refs sí son disjuntas
				// (phone-only vs lid-only): eso es lo que cruza los locks.
				refs := []contact.Ref{phone}
				if !phoneOnly {
					refs = []contact.Ref{lid}
				}
				if _, err := r.Resolve(ctx, tenant, refs, "flood-name"); err != nil {
					if isDeadlock(err) {
						atomic.AddInt64(&deadlocks, 1)
					} else {
						atomic.AddInt64(&otherErrs, 1)
						t.Errorf("Resolve error no-deadlock: %v", err)
					}
				}
			}
		}()
	}
	wg.Wait()

	if deadlocks > 0 {
		t.Fatalf("Resolve devolvió %d deadlock(s) 40P01 al llamante: el procesado de "+
			"entrantes NO es robusto ante la ráfaga de historial (T4)", deadlocks)
	}
	if otherErrs > 0 {
		t.Fatalf("Resolve devolvió %d error(es) inesperados", otherErrs)
	}
}
