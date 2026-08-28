package casebank

import (
	"context"
	"database/sql"
	"fmt"
)

// Postgres es la implementación real de Store sobre database/sql (mismo estilo
// que internal/degradation/postgres.go y internal/tenantllm/postgres.go: SQL raw
// con placeholders $1..$n, sin ORM).
//
// 🔴 NO LLEVA FieldCipher, y a diferencia de `degradation.Postgres` —donde la
// ausencia significa «aquí no hay nada sensible»— aquí significa otra cosa y peor
// de entender: SÍ hay texto de un cliente, y lo que lo hace publicable no es el
// cifrado sino que YA VIENE ANONIMIZADO desde `Servicio.Insertar`. Si algún día
// alguien escribe por este store sin pasar por el servicio, esta tabla es PII en
// claro (ADR-0034) y no hay cifrado que lo tape. Ver el COMMENT de la 0082.
type Postgres struct {
	db *sql.DB
}

// NewPostgres construye el store.
func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

// insertSQL escribe la fila y devuelve su id.
//
// `consented` VIAJA COMO PARÁMETRO y no como el literal `true`, aunque el
// servicio ya lo haya validado. Es lo que mantiene vivo al CHECK: con `true`
// cableado aquí, la constraint de la base no podría fallar NUNCA por esta puerta
// y su test de integración estaría probando una rama muerta. El store escribe lo
// que le dan; quien decide es el servicio, y quien tiene la última palabra es la
// base.
const insertSQL = `
INSERT INTO public.intake_case_bank (tenant_id, consented, source_text, expected)
VALUES ($1, $2, $3, $4)
RETURNING id`

// existeSQL es el guard de idempotencia de la siembra. Compara el literal EXACTO
// —ya anonimizado— y por eso puede usar el índice idx_intake_case_bank_tenant
// para acotar por tenant antes de comparar el texto.
const existeSQL = `
SELECT EXISTS (
    SELECT 1 FROM public.intake_case_bank
     WHERE tenant_id = $1 AND source_text = $2
)`

// Insertar escribe el caso. Recibe el `source_text` YA ANONIMIZADO: este tipo no
// sabe anonimizar, y darle esa responsabilidad significaría que hay dos sitios
// donde puede olvidarse.
func (p *Postgres) Insertar(ctx context.Context, c Caso) (int64, error) {
	// El JSONB se pasa como `any` y no como `[]byte` para que el caso «sin
	// interpretación curada» llegue a la base como NULL de verdad y no como el
	// literal JSON vacío, que es un valor distinto y mentiría: `null` de JSONB
	// dice «hay interpretación y es nula», NULL de SQL dice «aún no se curó».
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

// Existe dice si ese tenant ya tiene ese literal en el banco.
func (p *Postgres) Existe(ctx context.Context, tenantID, sourceText string) (bool, error) {
	var ya bool
	if err := p.db.QueryRowContext(ctx, existeSQL, tenantID, sourceText).Scan(&ya); err != nil {
		return false, fmt.Errorf("consultando intake_case_bank: %w", err)
	}
	return ya, nil
}
