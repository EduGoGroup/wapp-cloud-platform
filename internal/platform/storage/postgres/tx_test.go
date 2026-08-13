package postgres_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// TestIsSerializationFailure cubre la clasificación de errores que decide si
// WithTx reintenta la transacción: deadlock (40P01) y serialization_failure
// (40001), incluso envueltos con %w; cualquier otro código o error no-pg NO
// reintenta (Plan 027 · Ola 1 · T4).
func TestIsSerializationFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"deadlock 40P01", &pgconn.PgError{Code: "40P01"}, true},
		{"serialization 40001", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock envuelto", fmt.Errorf("store: cerrar solicitud: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"unique_violation 23505", &pgconn.PgError{Code: "23505"}, false},
		{"error no-pg", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := postgres.IsSerializationFailure(tc.err); got != tc.want {
				t.Fatalf("IsSerializationFailure(%v) = %v, quiero %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsPermanentFailure cubre la clasificación que decide, en el reintento
// acotado del fan-out de sinks durables (Plan 054 · T3, D-054.4), si vale la
// pena reintentar: toda violación de integridad (clase SQLSTATE 23) es
// PERMANENTE —reintentar la MISMA escritura vuelve a chocar—, mientras que un
// deadlock/serialización (clase 40, ya cubierta por IsSerializationFailure), un
// error no-pg (timeout, conexión caída) o nil se tratan como TRANSITORIOS.
func TestIsPermanentFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"not_null_violation 23502 (hallazgo #001/#003)", &pgconn.PgError{Code: "23502"}, true},
		{"unique_violation 23505", &pgconn.PgError{Code: "23505"}, true},
		{"foreign_key_violation 23503", &pgconn.PgError{Code: "23503"}, true},
		{"check_violation 23514", &pgconn.PgError{Code: "23514"}, true},
		{"23502 envuelto", fmt.Errorf("store: upsert intake: %w", &pgconn.PgError{Code: "23502"}), true},
		{"deadlock 40P01 (transitorio, no permanente)", &pgconn.PgError{Code: "40P01"}, false},
		{"serialization 40001 (transitorio, no permanente)", &pgconn.PgError{Code: "40001"}, false},
		{"connection_failure 08006 (transitorio)", &pgconn.PgError{Code: "08006"}, false},
		{"error no-pg (timeout/conexión)", errors.New("context deadline exceeded"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := postgres.IsPermanentFailure(tc.err); got != tc.want {
				t.Fatalf("IsPermanentFailure(%v) = %v, quiero %v", tc.err, got, tc.want)
			}
		})
	}
}
