// Package iampostgres implementa los puertos out del IAM (UserRepo, RoleRepo,
// GrantRepo, RefreshRepo, APIKeyRepo, AuditRepo) con SQL raw sobre PostgreSQL,
// siguiendo el patrón de repos del repo (database/sql + driver pgx/v5 stdlib,
// placeholders $N, ExecContext/QueryRowContext, tenant_id::text en los SELECT).
// Mapea sql.ErrNoRows → domain.ErrNotFound y el unique_violation (23505) →
// domain.ErrConflict. CERO PII y CERO material de la doble llave viven aquí.
//
// El nombre de paquete (iampostgres) difiere del directorio (postgres) para no
// colisionar con internal/platform/storage/postgres al importar ambos, igual que
// gateway/grpc se declara package gatewaygrpc.
package iampostgres

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgUniqueViolation es el SQLSTATE de violación de unicidad de PostgreSQL.
const pgUniqueViolation = "23505"

// isUniqueViolation reporta si err es un unique_violation de Postgres.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

// timePtr convierte un sql.NullTime en *time.Time (nil si NULL).
func timePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time
	return &t
}

// strPtr convierte un sql.NullString en *string (nil si NULL).
func strPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

// nullTime construye un sql.NullTime desde *time.Time (NULL si nil).
func nullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

// nullString construye un sql.NullString desde *string (NULL si nil o vacío-nil).
func nullString(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

// encodeTextArray serializa []string a un literal de array de Postgres
// (`{"a","b"}`) con cada elemento entre comillas y escape de `"`/`\`. Se pasa
// como parámetro con cast `$n::text[]`. Vacío → `{}`.
func encodeTextArray(items []string) string {
	if len(items) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range it {
			if r == '"' || r == '\\' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// decodeCSV divide la salida de array_to_string(col, ',') en []string. Vacío →
// nil. Los scopes/grants (tokens glob) no contienen comas, así que el join por
// coma es seguro.
func decodeCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
