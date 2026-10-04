// Porta internal/gateway/enroll/store_postgres.go @ 8896f13

package enroll

import (
	"context"
	"database/sql"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// PostgresCodeStore es la implementación de CodeStore (y repositorio de
// enrollment_codes) sobre PostgreSQL con SQL raw. Vive junto al dominio de
// enrolamiento porque debe hablar sus errores sentinela (ErrCodeInvalid) e
// implementar CodeStore; toma un *sql.DB directamente (sin acoplarse al paquete
// platform/storage/postgres).
type PostgresCodeStore struct{}

// NewPostgresCodeStore construye el store sobre el pool dado. No abre ni
// comprueba la conexión.
func NewPostgresCodeStore(db *sql.DB) *PostgresCodeStore {
	panic(pendiente.Implementar("enroll.NewPostgresCodeStore"))
}

// Create siembra un código de activación para un tenant (lo emite la plataforma;
// en dev/tests se usa para preparar el escenario). tenantID debe ser el UUID de
// un tenant existente (FK). expiresAt es el vencimiento del código. El código
// se guarda TAL CUAL, sin normalizar. Un fallo del driver vuelve envuelto como
// "enroll: sembrando enrollment_code: …".
func (s *PostgresCodeStore) Create(ctx context.Context, code, tenantID string, expiresAt time.Time) error {
	panic(pendiente.Implementar("enroll.PostgresCodeStore.Create"))
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
//
// El código viaja TAL CUAL como $1: sin TrimSpace, sin cambiar mayúsculas y
// sin rechazar el vacío. Las dos condiciones del WHERE son la promesa «un solo
// uso» (used_at IS NULL) y «no vencido» (expires_at > now()); su test afirma
// el texto exacto de la sentencia. Otro fallo del driver vuelve envuelto como
// "enroll: consumiendo enrollment_code: …", con tenant vacío.
func (s *PostgresCodeStore) Consume(ctx context.Context, code string) (string, error) {
	panic(pendiente.Implementar("enroll.PostgresCodeStore.Consume"))
}
