package contact

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresResolver implementa Resolver con SQL raw sobre public.contacts (y, en
// la fusión, public.flow_state). Toda una Resolve corre en UNA transacción: así
// la fusión (re-apuntar refs + migrar flow_state del huérfano al canónico) es
// atómica (design.md §5, §10.D).
type PostgresResolver struct {
	db *sql.DB
}

// NewPostgresResolver construye el resolver sobre el pool dado.
func NewPostgresResolver(db *sql.DB) *PostgresResolver {
	return &PostgresResolver{db: db}
}

// Resolve implementa Resolver (design.md §4, §5) de forma atómica.
func (r *PostgresResolver) Resolve(ctx context.Context, tenantID string, refs []Ref, pushName string) (contactID string, err error) {
	refs = dedupeRefs(refs)
	if len(refs) == 0 {
		return "", ErrNoRefs
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("contact: iniciar transacción: %w", err)
	}
	defer func() {
		if err != nil {
			if rerr := tx.Rollback(); rerr != nil {
				err = errors.Join(err, rerr)
			}
		}
	}()

	found, err := lookupContactIDs(ctx, tx, tenantID, refs)
	if err != nil {
		return "", err
	}

	if len(found) == 0 {
		contactID, err = insertNewContact(ctx, tx, tenantID, refs, pushName)
	} else {
		contactID, err = resolveExisting(ctx, tx, tenantID, found, refs, pushName)
	}
	if err != nil {
		return "", err
	}

	if err = tx.Commit(); err != nil {
		return "", fmt.Errorf("contact: confirmar transacción: %w", err)
	}
	return contactID, nil
}

// lookupContactIDs devuelve, en orden estable, los contact_id distintos ya
// mapeados por alguna ref. Bloquea las filas encontradas (FOR UPDATE) para
// serializar fusiones concurrentes del mismo contacto.
func lookupContactIDs(ctx context.Context, tx *sql.Tx, tenantID string, refs []Ref) ([]string, error) {
	seen := make(map[string]struct{})
	var ids []string
	for _, ref := range refs {
		var cid string
		err := tx.QueryRowContext(ctx, `
			SELECT contact_id::text FROM public.contacts
			WHERE tenant_id = $1 AND kind = $2 AND value = $3
			FOR UPDATE
		`, tenantID, ref.Kind, ref.Value).Scan(&cid)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return nil, fmt.Errorf("contact: buscar ref: %w", err)
		}
		if _, ok := seen[cid]; !ok {
			seen[cid] = struct{}{}
			ids = append(ids, cid)
		}
	}
	return ids, nil
}

// insertNewContact crea un contact_id nuevo (UUID por DEFAULT) con la primera
// ref y ata las restantes al mismo id.
func insertNewContact(ctx context.Context, tx *sql.Tx, tenantID string, refs []Ref, pushName string) (string, error) {
	var cid string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO public.contacts (tenant_id, kind, value, push_name)
		VALUES ($1, $2, $3, $4)
		RETURNING contact_id::text
	`, tenantID, refs[0].Kind, refs[0].Value, nullStr(pushName)).Scan(&cid)
	if err != nil {
		return "", fmt.Errorf("contact: insertar contacto: %w", err)
	}
	for _, ref := range refs[1:] {
		if err := attachRef(ctx, tx, tenantID, cid, ref, pushName); err != nil {
			return "", err
		}
	}
	return cid, nil
}

// resolveExisting reusa (un solo contact_id) o funde (varios) los contact_id ya
// existentes en el canónico, ata las refs faltantes y actualiza el push_name.
func resolveExisting(ctx context.Context, tx *sql.Tx, tenantID string, found []string, refs []Ref, pushName string) (string, error) {
	canonical := found[0]
	if len(found) > 1 {
		var err error
		canonical, err = pickCanonicalDB(ctx, tx, tenantID, found)
		if err != nil {
			return "", err
		}
		for _, orphan := range found {
			if orphan == canonical {
				continue
			}
			if err := fuseDB(ctx, tx, tenantID, orphan, canonical); err != nil {
				return "", err
			}
		}
	}
	for _, ref := range refs {
		if err := attachRef(ctx, tx, tenantID, canonical, ref, pushName); err != nil {
			return "", err
		}
	}
	if pushName != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE public.contacts SET push_name = $1, updated_at = now()
			WHERE tenant_id = $2 AND contact_id = $3
		`, pushName, tenantID, canonical); err != nil {
			return "", fmt.Errorf("contact: actualizar push_name: %w", err)
		}
	}
	return canonical, nil
}

// nullStr convierte "" en NULL para columnas opcionales (push_name).
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// attachRef ata una ref al contact_id dado; si ya existe (dedup por (tenant,
// kind, value)) no hace nada.
func attachRef(ctx context.Context, tx *sql.Tx, tenantID, contactID string, ref Ref, pushName string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO public.contacts (tenant_id, kind, value, contact_id, push_name)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, kind, value) DO NOTHING
	`, tenantID, ref.Kind, ref.Value, contactID, nullStr(pushName))
	if err != nil {
		return fmt.Errorf("contact: adjuntar ref: %w", err)
	}
	return nil
}

// pickCanonicalDB elige el contact_id más antiguo entre los dados (MIN(created_at),
// desempate por id) como canónico de la fusión (design.md §5).
func pickCanonicalDB(ctx context.Context, tx *sql.Tx, tenantID string, found []string) (string, error) {
	canonical := ""
	var best time.Time
	for _, id := range found {
		var created time.Time
		err := tx.QueryRowContext(ctx, `
			SELECT MIN(created_at) FROM public.contacts
			WHERE tenant_id = $1 AND contact_id = $2
		`, tenantID, id).Scan(&created)
		if err != nil {
			return "", fmt.Errorf("contact: created_at del contacto: %w", err)
		}
		if canonical == "" || created.Before(best) || (created.Equal(best) && id < canonical) {
			canonical = id
			best = created
		}
	}
	return canonical, nil
}

// fuseDB funde el contact_id huérfano en el canónico, dentro de la MISMA
// transacción (atomicidad, design.md §5, §10.D):
//  1. Poda las filas de flow_state del huérfano cuya sesión YA tiene fila del
//     canónico: política de conflicto = CONSERVAR la del canónico (identidad
//     autoritativa) y descartar la del huérfano.
//  2. Migra el resto de flow_state del huérfano al canónico (ya sin colisión de
//     PK (tenant, session, contact_id)).
//  3. Re-apunta las refs (public.contacts) del huérfano al canónico.
func fuseDB(ctx context.Context, tx *sql.Tx, tenantID, orphan, canonical string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM public.flow_state o
		WHERE o.tenant_id = $1 AND o.contact_id = $2
		  AND EXISTS (
		      SELECT 1 FROM public.flow_state c
		      WHERE c.tenant_id = $1 AND c.contact_id = $3 AND c.session_id = o.session_id
		  )
	`, tenantID, orphan, canonical); err != nil {
		return fmt.Errorf("contact: podar flow_state huérfano: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE public.flow_state SET contact_id = $3
		WHERE tenant_id = $1 AND contact_id = $2
	`, tenantID, orphan, canonical); err != nil {
		return fmt.Errorf("contact: migrar flow_state en fusión: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE public.contacts SET contact_id = $3, updated_at = now()
		WHERE tenant_id = $1 AND contact_id = $2
	`, tenantID, orphan, canonical); err != nil {
		return fmt.Errorf("contact: re-apuntar refs en fusión: %w", err)
	}
	return nil
}

// Destino implementa Resolver (design.md §10.E).
func (r *PostgresResolver) Destino(ctx context.Context, tenantID, contactID string) (ref Ref, err error) {
	rows, qerr := r.db.QueryContext(ctx, `
		SELECT kind, value FROM public.contacts
		WHERE tenant_id = $1 AND contact_id = $2
	`, tenantID, contactID)
	if qerr != nil {
		return Ref{}, fmt.Errorf("contact: leer refs del contacto: %w", qerr)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("contact: cerrar filas: %w", cerr)
		}
	}()

	var refs []Ref
	for rows.Next() {
		var r Ref
		if serr := rows.Scan(&r.Kind, &r.Value); serr != nil {
			return Ref{}, fmt.Errorf("contact: escanear ref: %w", serr)
		}
		refs = append(refs, r)
	}
	if rerr := rows.Err(); rerr != nil {
		return Ref{}, fmt.Errorf("contact: iterar refs: %w", rerr)
	}
	if len(refs) == 0 {
		return Ref{}, fmt.Errorf("%w: %q", ErrContactNotFound, contactID)
	}
	return pickDestino(refs)
}
