//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
)

// Este fichero prueba lo ÚNICO de los dos reclamos de intake.Postgres que las suites de contrato
// no pueden ver: el `FOR UPDATE SKIP LOCKED` de su subconsulta. No es una promesa del puerto —el
// doble en memoria no tiene filas bloqueadas, y con una sola conexión la cláusula no se observa—,
// es una propiedad del adaptador contra Postgres, y por eso vive aquí y no en intakehelpertest.
//
// Reutiliza el montaje de intake_contrato_test.go (intakeContractNewTable): una base clonada por
// caso y UN *sql.DB, del que salen tanto la transacción que bloquea la fila como la conexión con
// la que reclama el adaptador. Del paquete intake solo se nombra NewPostgres (candado
// ProcessImports, regla 3b).

// intakeClaimWait acota cada reclamo. Un reclamo que respeta `SKIP LOCKED` vuelve en
// milisegundos; uno que no, se queda esperando la fila bloqueada hasta que vence este plazo, y
// eso es lo que pone el test en rojo.
const intakeClaimWait = 5 * time.Second

// TestIntakeClaim_SkipsLockedRows_Postgres: con la fila que ganaría el reclamo BLOQUEADA por otra
// transacción, cada uno de los dos reclamos se lleva el job siguiente sin esperar; con solo la
// bloqueada en `pending`, no encuentra nada (y tampoco espera); y al soltarse el bloqueo, se la
// lleva. Es lo que deja que varias réplicas del worker reclamen a la vez sin pisarse ni frenarse.
func TestIntakeClaim_SkipsLockedRows_Postgres(t *testing.T) {
	claims := map[string]func(db *sql.DB, tenantID string) func(context.Context) (string, bool, error){
		"ClaimNext": func(db *sql.DB, _ string) func(context.Context) (string, bool, error) {
			store := intake.NewPostgres(db)
			return func(ctx context.Context) (string, bool, error) {
				job, ok, err := store.ClaimNext(ctx)
				return job.ID, ok, err
			}
		},
		"ClaimNextIgnoringBackoff": func(db *sql.DB, tenantID string) func(context.Context) (string, bool, error) {
			store := intake.NewPostgres(db)
			return func(ctx context.Context) (string, bool, error) {
				job, ok, err := store.ClaimNextIgnoringBackoff(ctx, tenantID)
				return job.ID, ok, err
			}
		},
	}
	for name, build := range claims {
		t.Run(name, func(t *testing.T) {
			db, table := intakeContractNewTable(t)
			claim := build(db, table.TenantA)

			// Dos `pending` vencidos del mismo tenant. `first` gana por los dos criterios del
			// orden (marca más antigua y creado antes): sin bloqueo, sería el reclamado.
			now := table.Now(t).Truncate(time.Second)
			first := intakeClaimSeedPending(t, table, now.Add(-10*time.Minute))
			second := intakeClaimSeedPending(t, table, now.Add(-5*time.Minute))

			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatalf("abrir la transacción que bloquea la fila: %v", err)
			}
			// Red de seguridad si el caso corta antes de soltar: una transacción ya cerrada no es
			// un fallo.
			t.Cleanup(func() {
				if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
					t.Logf("deshacer la transacción que bloquea la fila: %v", rerr)
				}
			})
			var locked string
			if err := tx.QueryRowContext(t.Context(),
				`SELECT id::text FROM public.intake_jobs WHERE id = $1::uuid FOR UPDATE`, first).Scan(&locked); err != nil {
				t.Fatalf("bloquear la fila del job %s: %v", first, err)
			}

			intakeClaimRequire(t, claim, "con el primero bloqueado", second)
			intakeClaimRequire(t, claim, "con solo el bloqueado en pending", "")

			if err := tx.Rollback(); err != nil {
				t.Fatalf("soltar el bloqueo: %v", err)
			}
			intakeClaimRequire(t, claim, "con el bloqueo ya suelto", first)
		})
	}
}

// intakeClaimSeedPending siembra un job `pending` del tenant A, creado y con la marca del backoff
// en ese instante (ya vencida), y devuelve su id.
func intakeClaimSeedPending(t *testing.T, table intakehelpertest.Table, at time.Time) string {
	t.Helper()
	var r intakehelpertest.Row
	r.Key.TenantID = table.TenantA
	r.Key.SessionID = "session-" + uuid.NewString()
	r.Key.ContactID = "contact-" + uuid.NewString()
	r.Key.EventID = uuid.NewString()
	r.Status = "pending"
	r.CreatedAt, r.UpdatedAt, r.NextAttemptAt = at, at, at
	return table.Seed(t, r)
}

// intakeClaimRequire reclama con el plazo intakeClaimWait y exige que se lleve ESE job; con
// `want` vacío exige (_, false, nil). Un error —el plazo vencido es el que importa— corta el test:
// significa que el reclamo se quedó esperando una fila bloqueada en vez de saltársela.
func intakeClaimRequire(t *testing.T, claim func(context.Context) (string, bool, error), when, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakeClaimWait)
	defer cancel()
	start := time.Now()
	id, ok, err := claim(ctx)
	if err != nil {
		t.Fatalf("reclamo %s: error a los %v (¿esperó la fila bloqueada?): %v", when, time.Since(start).Round(time.Millisecond), err)
	}
	if ok != (want != "") || id != want {
		t.Fatalf("reclamo %s = (%q, %v, nil), quería (%q, %v, nil)", when, id, ok, want, want != "")
	}
}
