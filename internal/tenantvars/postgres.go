package tenantvars

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// Postgres persiste las variables de empresa en public.tenant_variables (0043).
type Postgres struct {
	db *sql.DB
}

// NewPostgres construye el store sobre el *sql.DB ya abierto.
func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

// List devuelve las variables del tenant ordenadas por clave. Sin filas devuelve
// un slice vacío, no un error: un tenant sin variables es el caso normal.
func (p *Postgres) List(ctx context.Context, tenantID string) (out []Variable, err error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT key, value, updated_at
		FROM public.tenant_variables
		WHERE tenant_id = $1
		ORDER BY key
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("tenantvars: listar variables: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("tenantvars: cerrar filas de variables: %w", cerr)
		}
	}()

	out = []Variable{}
	for rows.Next() {
		var v Variable
		if err := rows.Scan(&v.Key, &v.Value, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("tenantvars: leer variable: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tenantvars: recorrer variables: %w", err)
	}
	return out, nil
}

// Replace deja el conjunto del tenant EXACTAMENTE igual a vars: inserta las
// nuevas, actualiza las que cambiaron y BORRA las que no vienen. Las dos
// sentencias van en UNA transacción (postgres.WithTx, que además reintenta ante
// deadlock/serialización): un lector nunca ve el conjunto a medio reemplazar, ni
// vacío entre el borrado y el alta.
//
// updated_at solo se mueve cuando el valor CAMBIA (el `WHERE ... IS DISTINCT
// FROM` del upsert). Reescribir el mismo conjunto es entonces un no-op observable:
// la marca sigue diciendo cuándo cambió la variable, no cuándo se guardó la
// pantalla — que es lo que el Plan 042 necesita para decidir si refresca.
//
// wApp NO interpreta claves ni valores (D-041.1): aquí no hay validación
// semántica, solo persistencia. Los límites de forma (cuántas, cuán largas) son
// del transporte, no del store.
func (p *Postgres) Replace(ctx context.Context, tenantID string, vars map[string]string) error {
	// Slices NO-NIL a propósito: un []string nil viaja como NULL y `key <> ALL(NULL)`
	// es NULL, con lo que el DELETE no borraría NADA y "dejar el tenant sin
	// variables" quedaría silenciosamente sin efecto. Con make(...,0,n) viaja '{}'.
	keys := make([]string, 0, len(vars))
	values := make([]string, 0, len(vars))
	for k, v := range vars {
		keys = append(keys, k)
		values = append(values, v)
	}

	return postgres.WithTx(ctx, p.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM public.tenant_variables
			WHERE tenant_id = $1 AND key <> ALL($2::text[])
		`, tenantID, keys); err != nil {
			return fmt.Errorf("tenantvars: borrar variables retiradas: %w", err)
		}
		if len(keys) == 0 {
			return nil
		}
		// Las claves salen de un map ⇒ son únicas; ON CONFLICT DO UPDATE no puede
		// toparse dos veces con la misma fila.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO public.tenant_variables (tenant_id, key, value)
			SELECT $1, k, v FROM unnest($2::text[], $3::text[]) AS t(k, v)
			ON CONFLICT (tenant_id, key) DO UPDATE
			   SET value = EXCLUDED.value, updated_at = now()
			 WHERE public.tenant_variables.value IS DISTINCT FROM EXCLUDED.value
		`, tenantID, keys, values); err != nil {
			return fmt.Errorf("tenantvars: guardar variables: %w", err)
		}
		return nil
	})
}
