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
		{"deadlock envuelto", fmt.Errorf("store: cerrar orden: %w", &pgconn.PgError{Code: "40P01"}), true},
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
