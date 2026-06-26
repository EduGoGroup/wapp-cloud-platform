package enroll

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresCodeStore es la implementación de CodeStore (y repositorio de
// enrollment_codes) sobre PostgreSQL con SQL raw. Vive junto al dominio de
// enrolamiento porque debe hablar sus errores sentinela (ErrCodeInvalid) e
// implementar CodeStore; toma un *sql.DB directamente (sin acoplarse al paquete
// platform/storage/postgres).
type PostgresCodeStore struct {
	db *sql.DB
}

// NewPostgresCodeStore construye el store sobre el pool dado.
func NewPostgresCodeStore(db *sql.DB) *PostgresCodeStore {
	return &PostgresCodeStore{db: db}
}

// Create siembra un código de activación para un tenant (lo emite la plataforma;
// en dev/tests se usa para preparar el escenario). tenantID debe ser el UUID de
// un tenant existente (FK). expiresAt es el vencimiento del código.
func (s *PostgresCodeStore) Create(ctx context.Context, code, tenantID string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO public.enrollment_codes (code, tenant_id, expires_at)
		VALUES ($1, $2, $3)
	`, code, tenantID, expiresAt)
	if err != nil {
		return fmt.Errorf("enroll: sembrando enrollment_code: %w", err)
	}
	return nil
}

// Consume implementa CodeStore de forma ATÓMICA en un único UPDATE condicional:
//
//	UPDATE enrollment_codes SET used_at = now()
//	WHERE code = $1 AND used_at IS NULL AND expires_at > now()
//	RETURNING tenant_id
//
// El UPDATE toma el lock de fila y reevalúa el WHERE bajo ese lock, de modo que
// dos consumos concurrentes del mismo código serializan: el segundo encuentra
// used_at ya poblado y no afecta filas. Si no se devuelve fila (ausente, expirado
// o ya usado) se traduce a ErrCodeInvalid sin distinguir la causa (no se filtra
// información; la respuesta de seguridad es la misma).
func (s *PostgresCodeStore) Consume(ctx context.Context, code string) (string, error) {
	var tenantID string
	err := s.db.QueryRowContext(ctx, `
		UPDATE public.enrollment_codes
		SET used_at = now()
		WHERE code = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING tenant_id::text
	`, code).Scan(&tenantID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", ErrCodeInvalid
	case err != nil:
		return "", fmt.Errorf("enroll: consumiendo enrollment_code: %w", err)
	}
	return tenantID, nil
}
