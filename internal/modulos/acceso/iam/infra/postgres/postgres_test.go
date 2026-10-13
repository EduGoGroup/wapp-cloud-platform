package iampostgres

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// postgres.go no tiene exportados: estos tests afirman las reglas de sus auxiliares, que todo el
// paquete usa para traducir lo que devuelve Postgres (23505 → conflicto, NULL → nil).

// TestIsUniqueViolation: solo el SQLSTATE 23505 es un unique_violation, venga suelto o envuelto;
// otro SQLSTATE (una FK, un CHECK) y los errores que no son de Postgres no lo son. Es lo que separa
// el ErrConflict de un error de infraestructura en Create.
func TestIsUniqueViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unique_violation", &pgconn.PgError{Code: "23505"}, true},
		{"wrapped_unique_violation", fmt.Errorf("iam: crear rol: %w", &pgconn.PgError{Code: "23505"}), true},
		{"foreign_key_violation", &pgconn.PgError{Code: "23503"}, false},
		{"check_violation", &pgconn.PgError{Code: "23514"}, false},
		{"not_a_postgres_error", errors.New("23505"), false},
		{"no_rows", sql.ErrNoRows, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isUniqueViolation(c.err); got != c.want {
				t.Errorf("isUniqueViolation(%v) = %v; quiere %v", c.err, got, c.want)
			}
		})
	}
}

// TestStrPtr: NULL → nil; un valor, también "", → puntero a ese valor.
func TestStrPtr(t *testing.T) {
	if got := strPtr(sql.NullString{}); got != nil {
		t.Errorf("strPtr(NULL) = %q; quiere nil", *got)
	}
	for _, v := range []string{"", "a1b2"} {
		got := strPtr(sql.NullString{String: v, Valid: true})
		if got == nil || *got != v {
			t.Errorf("strPtr(%q válido) = %v; quiere un puntero a %q", v, got, v)
		}
	}
}

// TestNullString: nil → NULL; un puntero, también a "", → ese valor, no NULL. Ida y vuelta con
// strPtr conserva el valor.
func TestNullString(t *testing.T) {
	if got := nullString(nil); got.Valid {
		t.Errorf("nullString(nil) = %+v; quiere NULL", got)
	}
	for _, v := range []string{"", "a1b2"} {
		got := nullString(&v)
		if !got.Valid || got.String != v {
			t.Errorf("nullString(&%q) = %+v; quiere {%q true}", v, got, v)
		}
		if back := strPtr(got); back == nil || *back != v {
			t.Errorf("strPtr(nullString(&%q)) = %v; quiere un puntero a %q", v, back, v)
		}
	}
}

// TestTimePtr: NULL → nil («todavía no pasó»); un instante válido, incluso el cero, → puntero a
// ese instante.
func TestTimePtr(t *testing.T) {
	if got := timePtr(sql.NullTime{}); got != nil {
		t.Errorf("timePtr(NULL) = %v; quiere nil", *got)
	}
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, v := range []time.Time{{}, at} {
		got := timePtr(sql.NullTime{Time: v, Valid: true})
		if got == nil || !got.Equal(v) {
			t.Errorf("timePtr(%v válido) = %v; quiere un puntero a ese instante", v, got)
		}
	}
}
