// Porta internal/casebank/postgres.go @ 8d875ab

package casebank

import (
	"context"
	"database/sql"
	"fmt"
)

// Postgres es la implementación real de Store sobre database/sql, contra
// public.intake_case_bank (0082): SQL raw con placeholders $1..$n, sin ORM. Las
// reglas del puerto las fija la suite casebankhelpertest.Contrato, que corre
// contra él en los procesos de F9; su test de fichero afirma, con un driver de
// mentira, el SQL que emite, sus argumentos y el mapeo de filas y errores.
//
// 🔴 NO LLEVA FieldCipher, y a diferencia de `degradation.Postgres` —donde la
// ausencia significa «aquí no hay nada sensible»— aquí significa otra cosa y peor
// de entender: SÍ hay texto de un cliente, y lo que lo hace publicable no es el
// cifrado sino que YA VIENE ANONIMIZADO desde `Service.Insert`. Si algún día
// alguien escribe por este store sin pasar por el servicio, esta tabla es PII en
// claro (ADR-0034) y no hay cifrado que lo tape. Ver el COMMENT de la 0082.
type Postgres struct {
	db *sql.DB
}

// NewPostgres construye el store sobre el *sql.DB ya abierto. No lo consulta.
func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

var _ Store = (*Postgres)(nil)

// insertSQL escribe la fila y devuelve su id. `consented` es el $2, no el literal
// `true`: ver Insert.
const insertSQL = `
INSERT INTO public.intake_case_bank (tenant_id, consented, source_text, expected)
VALUES ($1, $2, $3, $4)
RETURNING id`

// existsSQL (antes `existeSQL`) es el guard de idempotencia de la siembra.
const existsSQL = `
SELECT EXISTS (
    SELECT 1 FROM public.intake_case_bank
     WHERE tenant_id = $1 AND source_text = $2
)`

// Insert escribe el caso con UN `INSERT … RETURNING id` sobre el pool, sin
// transacción, y devuelve el id que devuelve la base. Recibe el `source_text` YA
// ANONIMIZADO: este tipo no sabe anonimizar, y darle esa responsabilidad
// significaría que hay dos sitios donde puede olvidarse.
//
// Los cuatro argumentos, en orden: tenant_id, consented, source_text, expected.
//
//   - `consented` VIAJA COMO PARÁMETRO y no como el literal `true`, aunque el
//     servicio ya lo haya validado. Es lo que mantiene vivo al CHECK: con `true`
//     cableado aquí, la constraint de la base no podría fallar NUNCA por esta
//     puerta y su caso de la suite estaría probando una rama muerta. El store
//     escribe lo que le dan; quien decide es el servicio, y quien tiene la última
//     palabra es la base;
//   - `expected` viaja como []byte, o como NULL de SQL cuando viene vacío (nil o
//     de longitud cero): el caso «sin interpretación curada» tiene que llegar a
//     la base como NULL de verdad y no como el literal JSON vacío, que es un
//     valor distinto y mentiría — `null` de JSONB dice «hay interpretación y es
//     nula», NULL de SQL dice «aún no se curó».
//
// Si la sentencia falla o no devuelve fila, devuelve 0 y el error envuelto (%w)
// con el prefijo "insertando en intake_case_bank: ".
func (p *Postgres) Insert(ctx context.Context, c Case) (int64, error) {
	// `any` y no `[]byte`: un []byte nil viajaría como valor vacío, no como NULL.
	var expected any
	if len(c.Expected) > 0 {
		expected = []byte(c.Expected)
	}
	var id int64
	if err := p.db.QueryRowContext(ctx, insertSQL,
		c.TenantID, c.Consented, c.SourceText, expected).Scan(&id); err != nil {
		return 0, fmt.Errorf("insertando en intake_case_bank: %w", err)
	}
	return id, nil
}

// Exists dice si ese tenant ya tiene ese literal en el banco, con UN `SELECT
// EXISTS` de argumentos (tenant_id, source_text). Compara el literal EXACTO —ya
// anonimizado— y por eso puede usar el índice idx_intake_case_bank_tenant para
// acotar por tenant antes de comparar el texto.
//
// Si la consulta falla, devuelve false y el error envuelto (%w) con el prefijo
// "consultando intake_case_bank: ".
func (p *Postgres) Exists(ctx context.Context, tenantID, sourceText string) (bool, error) {
	var found bool
	if err := p.db.QueryRowContext(ctx, existsSQL, tenantID, sourceText).Scan(&found); err != nil {
		return false, fmt.Errorf("consultando intake_case_bank: %w", err)
	}
	return found, nil
}
