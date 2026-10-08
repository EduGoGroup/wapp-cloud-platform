// Porta internal/tenantvars/postgres.go @ 3a21138

package tenantvars

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Postgres persiste las variables de empresa en public.tenant_variables (0043).
// Las reglas del puerto las fija la suite tenantvarshelpertest.Contrato, que corre
// contra él en los procesos de F9; su test de fichero afirma, con un driver de
// mentira, el SQL que emite, sus argumentos, la transacción y el mapeo de filas y
// errores.
type Postgres struct{}

// NewPostgres construye el store sobre el *sql.DB ya abierto. No lo consulta.
func NewPostgres(db *sql.DB) *Postgres {
	panic(pendiente.Implementar("tenantvars.NewPostgres"))
}

var _ Store = (*Postgres)(nil)

// List devuelve las variables del tenant ordenadas por clave (ORDER BY key, con la
// colación de la base). Sin filas devuelve un slice vacío, no nil y sin error: un
// tenant sin variables es el caso normal. Es UNA consulta sobre el pool, sin
// transacción.
//
// Errores, todos envueltos con %w y sin filas a medias (el slice devuelto es nil):
//
//   - "tenantvars: listar variables: " — la consulta falla;
//   - "tenantvars: leer variable: " — una fila no se puede escanear;
//   - "tenantvars: recorrer variables: " — el recorrido de las filas falla;
//   - "tenantvars: cerrar filas de variables: " — falla el cierre de las filas
//     cuando lo demás fue bien (un error anterior no se pisa).
func (p *Postgres) List(ctx context.Context, tenantID string) ([]Variable, error) {
	panic(pendiente.Implementar("tenantvars.Postgres.List"))
}

// Replace deja el conjunto del tenant EXACTAMENTE igual a vars: inserta las
// nuevas, actualiza las que cambiaron y BORRA las que no vienen. Las dos
// sentencias (el DELETE de las retiradas y el upsert) van en UNA transacción
// (postgres.WithTx, que además reintenta ante deadlock/serialización): un lector
// nunca ve el conjunto a medio reemplazar, ni vacío entre el borrado y el alta. Si
// una de las dos falla, la transacción se revierte y no queda nada a medias.
//
// Con vars vacío o nil solo se ejecuta el DELETE, que deja al tenant sin
// variables; el upsert no se emite. Las claves viajan como un text[] NO nil: un
// []string nil viajaría como NULL, `key <> ALL(NULL)` es NULL y el DELETE no
// borraría NADA, con lo que «dejar el tenant sin variables» quedaría
// silenciosamente sin efecto.
//
// updated_at solo se mueve cuando el valor CAMBIA (el `WHERE ... IS DISTINCT
// FROM` del upsert). Reescribir el mismo conjunto es entonces un no-op observable:
// la marca sigue diciendo cuándo cambió la variable, no cuándo se guardó la
// pantalla — que es lo que el snapshot de `intake.push` necesita para decidir si
// refresca.
//
// wApp NO interpreta claves ni valores (D-041.1): aquí no hay validación
// semántica, solo persistencia. Los límites de forma (cuántas, cuán largas) son
// del transporte, no del store.
//
// Errores, envueltos con %w:
//
//   - "tenantvars: borrar variables retiradas: " — falla el DELETE;
//   - "tenantvars: guardar variables: " — falla el upsert.
//
// Un fallo al abrir o confirmar la transacción sale con el texto de postgres.WithTx.
func (p *Postgres) Replace(ctx context.Context, tenantID string, vars map[string]string) error {
	panic(pendiente.Implementar("tenantvars.Postgres.Replace"))
}
