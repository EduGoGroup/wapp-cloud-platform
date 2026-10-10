// Porta internal/flujos/store/repository_postgres.go @ c0c0c03
//
// Trozo de repository_postgres.go (05 E-13): public.tenant_content y
// public.tenant_content_versions. Las reglas comunes del adaptador están en la
// cabecera de repository_postgres.go.
//
// 🔴 MUTANTES (nivel complejo): ReplaceTenantContentVersioned lleva las guardas que la
// suite tiene que morder una a una — la procedencia validada ANTES de abrir la
// transacción, el FOR UPDATE sobre la fila vigente, el MAX(version)+1 acotado por
// (tenant_id, ref), el archivado del blob VIEJO y no del nuevo, y el «sin vigente no
// se archiva».

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// GetTenantContent devuelve el blob JSON crudo de public.tenant_content para
// (tenantID, ref) (Plan 015 · T2). Firma EXACTA de content.Store (structural
// typing). Devuelve ErrTenantContentNotFound si la ref no existe. Cero pánico.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: tenant=%s ref=%s"
//   - "store: leer contenido de tenant: %w"
func (r *PostgresRepository) GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error) {
	var content []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT content
		FROM public.tenant_content
		WHERE tenant_id = $1 AND ref = $2
	`, tenantID, ref).Scan(&content)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("%w: tenant=%s ref=%s", ErrTenantContentNotFound, tenantID, ref)
	case err != nil:
		return nil, fmt.Errorf("store: leer contenido de tenant: %w", err)
	}
	return content, nil
}

// UpsertTenantContent inserta o actualiza (upsert por PK (tenant_id, ref)) el blob
// de contenido de negocio en public.tenant_content (Plan 018 · T6, ADR-0009). El
// blob se persiste como JSONB (debe ser JSON válido; lo valida el transporte).
// created_at usa el DEFAULT now() en el alta; updated_at se refresca en cada
// escritura. Acotado al tenant (INV-8).
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: upsert contenido de tenant: %w"
func (r *PostgresRepository) UpsertTenantContent(ctx context.Context, tenantID, ref string, blob []byte) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.tenant_content (tenant_id, ref, content, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now())
		ON CONFLICT (tenant_id, ref) DO UPDATE
		SET content = EXCLUDED.content, updated_at = now()
	`, tenantID, ref, blob)
	if err != nil {
		return fmt.Errorf("store: upsert contenido de tenant: %w", err)
	}
	return nil
}

// ReplaceTenantContentVersioned implementa TenantContentVersioner sobre Postgres:
// archiva el blob vigente en public.tenant_content_versions y escribe el nuevo en
// public.tenant_content DENTRO DE UNA SOLA TRANSACCIÓN (Plan 041 · T3.3,
// D-041.8), vía el helper postgres.WithTx (rollback inmune a panic + retry
// 40P01/40001).
//
// EL ORDEN NO ES LO QUE DA LA ATOMICIDAD, LA TRANSACCIÓN SÍ. Fuera de ella no hay
// secuencia buena: archivar-y-caer deja una versión de un catálogo que sigue
// vigente; escribir-y-caer pierde para siempre el que se sustituyó.
//
// Bloquea la fila vigente con FOR UPDATE antes de calcular MAX(version)+1: dos
// imports simultáneos sobre la MISMA (tenant, ref) se serializan y numeran 1 y 2,
// en vez de pelearse por el mismo número y violar la PK. Si no hay fila vigente no
// hay nada que bloquear ni que archivar —el upsert final resuelve la carrera— y se
// devuelve archived=0.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: %q"
//   - "store: leer contenido vigente para versionar: %w"
//   - "store: calcular siguiente versión de contenido: %w"
//   - "store: archivar versión de contenido: %w"
//   - "store: escribir contenido versionado: %w"
func (r *PostgresRepository) ReplaceTenantContentVersioned(ctx context.Context, tenantID, ref string, blob []byte, source string) (int, error) {
	if !validVersionSource(source) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidVersionSource, source)
	}
	var archived int
	err := postgres.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		archived = 0 // WithTx reintenta ante deadlock: el acumulador se recalcula entero.
		var current []byte
		err := tx.QueryRowContext(ctx, `
			SELECT content
			FROM public.tenant_content
			WHERE tenant_id = $1 AND ref = $2
			FOR UPDATE
		`, tenantID, ref).Scan(&current)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Sin contenido vigente no se versiona nada (D-041.8): la versión 1
			// nacerá del PRÓXIMO import, con lo que este escriba.
		case err != nil:
			return fmt.Errorf("store: leer contenido vigente para versionar: %w", err)
		default:
			var next int
			if verr := tx.QueryRowContext(ctx, `
				SELECT COALESCE(MAX(version), 0) + 1
				FROM public.tenant_content_versions
				WHERE tenant_id = $1 AND ref = $2
			`, tenantID, ref).Scan(&next); verr != nil {
				return fmt.Errorf("store: calcular siguiente versión de contenido: %w", verr)
			}
			if _, ierr := tx.ExecContext(ctx, `
				INSERT INTO public.tenant_content_versions
					(tenant_id, ref, version, content, source)
				VALUES ($1, $2, $3, $4, $5)
			`, tenantID, ref, next, current, source); ierr != nil {
				return fmt.Errorf("store: archivar versión de contenido: %w", ierr)
			}
			archived = next
		}

		if _, uerr := tx.ExecContext(ctx, `
			INSERT INTO public.tenant_content (tenant_id, ref, content, created_at, updated_at)
			VALUES ($1, $2, $3, now(), now())
			ON CONFLICT (tenant_id, ref) DO UPDATE
			SET content = EXCLUDED.content, updated_at = now()
		`, tenantID, ref, blob); uerr != nil {
			return fmt.Errorf("store: escribir contenido versionado: %w", uerr)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return archived, nil
}

// ListTenantContent devuelve las cabeceras (ref + timestamps) de los blobs de
// public.tenant_content del tenant, ordenadas por ref (Plan 018 · T6). NO trae el
// blob (se obtiene con GetTenantContent). Acotado al tenant (INV-8): el WHERE
// tenant_id garantiza que un blob ajeno nunca aparece.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: listar contenido de tenant: %w"
//   - "store: cerrar filas: %w"
//   - "store: escanear contenido de tenant: %w"
//   - "store: iterar contenido de tenant: %w"
func (r *PostgresRepository) ListTenantContent(ctx context.Context, tenantID string) (out []TenantContentSummary, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT ref, created_at, updated_at
		FROM public.tenant_content
		WHERE tenant_id = $1
		ORDER BY ref
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: listar contenido de tenant: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("store: cerrar filas: %w", cerr)
		}
	}()

	out = make([]TenantContentSummary, 0)
	for rows.Next() {
		var s TenantContentSummary
		if scanErr := rows.Scan(&s.Ref, &s.CreatedAt, &s.UpdatedAt); scanErr != nil {
			return nil, fmt.Errorf("store: escanear contenido de tenant: %w", scanErr)
		}
		out = append(out, s)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("store: iterar contenido de tenant: %w", rowsErr)
	}
	return out, nil
}

// DeleteTenantContent borra el blob (tenant_id, ref) de public.tenant_content
// (Plan 018 · T6). Devuelve ErrTenantContentNotFound si no existía (simetría con
// GetTenantContent → 404 en el transporte). Acotado al tenant (INV-8).
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: borrar contenido de tenant: %w"
//   - "store: filas afectadas al borrar contenido: %w"
//   - "%w: tenant=%s ref=%s"
func (r *PostgresRepository) DeleteTenantContent(ctx context.Context, tenantID, ref string) error {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM public.tenant_content
		WHERE tenant_id = $1 AND ref = $2
	`, tenantID, ref)
	if err != nil {
		return fmt.Errorf("store: borrar contenido de tenant: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: filas afectadas al borrar contenido: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: tenant=%s ref=%s", ErrTenantContentNotFound, tenantID, ref)
	}
	return nil
}
