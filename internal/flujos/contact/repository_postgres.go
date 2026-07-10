package contact

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// PostgresResolver implementa Resolver con SQL raw sobre public.contacts (y, en
// la fusión, public.flow_state). Toda una Resolve corre en UNA transacción: así
// la fusión (re-apuntar refs + migrar flow_state del huérfano al canónico) es
// atómica (design.md §5, §10.D).
//
// El identificador del contacto va CIFRADO en reposo (Plan 011, ADR-0017): la
// fila guarda value_bidx (índice ciego para buscar/deduplicar), value_enc
// (envelope) y value_dek (DEK envuelta). El value en claro solo vive en memoria
// en el borde de la app. cipher cifra/descifra; kp calcula el índice ciego.
type PostgresResolver struct {
	db     *sql.DB
	cipher *crypto.FieldCipher
	kp     crypto.KeyProvider
}

// NewPostgresResolver construye el resolver sobre el pool dado. cipher y kp
// aportan el cifrado en reposo del value (design.md §5): cipher hace el envelope
// encrypt/decrypt y kp calcula el índice ciego (value_bidx).
func NewPostgresResolver(db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider) *PostgresResolver {
	return &PostgresResolver{db: db, cipher: cipher, kp: kp}
}

// Resolve implementa Resolver (design.md §4, §5) de forma atómica, con REINTENTO
// ante deadlock (Plan 026 · T4).
//
// Bajo inundación de historial (número nuevo → WhatsApp vuelca su historial) el
// procesado de entrantes lanza una goroutine por mensaje (runtime OnIncoming) y
// cada una llama a Resolve FUERA del keyedMutex por conversación (ese lock se toma
// DESPUÉS, con el contact_id ya resuelto). Así, N transacciones de contactos
// corren de verdad en paralelo. El deadlock 40P01 aparece cuando dos entrantes del
// MISMO contacto llegan con identidad PARCIAL y DISJUNTA (whatsmeow enriquece
// desigual: uno trae solo from_pn, otro solo from_lid): cada transacción bloquea
// con FOR UPDATE solo la fila de SU ref (lookupContactIDs), pero el UPDATE de
// push_name (`WHERE contact_id = X`) toca TODAS las filas del contacto en orden de
// scan → una retiene la fila phone y pide la lid, la otra al revés → ciclo.
//
// No hay clave común conocida a priori para serializarlas (la identidad unificada
// solo se descubre a mitad de transacción), así que ni ordenar refs ni un lock
// previo lo evita: el remedio canónico es dejar que Postgres rompa el ciclo (aborta
// una) y REINTENTAR la transacción, que converge porque el upsert es idempotente
// (ON CONFLICT) y atómico. El guard `IS DISTINCT FROM` en el UPDATE de push_name
// (resolveExisting) reduce la ventana: en el estado estable de la ráfaga (mismo
// push_name repetido) el UPDATE no toca ninguna fila y no toma locks.
func (r *PostgresResolver) Resolve(ctx context.Context, tenantID string, refs []Ref, pushName string) (string, error) {
	refs = dedupeRefs(refs)
	if len(refs) == 0 {
		return "", ErrNoRefs
	}
	// Toda la resolución corre en UNA transacción con retry ante deadlock/serialización
	// vía el helper único postgres.WithTx (Plan 027 · Ola 1 · T4, cierra H7/H8): el
	// rollback es inmune a panic (flag committed) y el reintento es seguro porque la
	// resolución es atómica e idempotente (ON CONFLICT). Antes esto vivía en un retry
	// artesanal con rollback condicionado a `err != nil` (no cubría panic).
	var contactID string
	err := postgres.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		found, lErr := r.lookupContactIDs(ctx, tx, tenantID, refs)
		if lErr != nil {
			return lErr
		}
		if len(found) == 0 {
			contactID, lErr = r.insertNewContact(ctx, tx, tenantID, refs, pushName)
		} else {
			contactID, lErr = r.resolveExisting(ctx, tx, tenantID, found, refs, pushName)
		}
		return lErr
	})
	if err != nil {
		return "", err
	}
	return contactID, nil
}

// lookupContactIDs devuelve, en orden estable, los contact_id distintos ya
// mapeados por alguna ref. Busca por el índice ciego (value_bidx), no por el
// value en claro (que ya no vive en la fila). Bloquea las filas encontradas
// (FOR UPDATE) para serializar fusiones concurrentes del mismo contacto.
func (r *PostgresResolver) lookupContactIDs(ctx context.Context, tx *sql.Tx, tenantID string, refs []Ref) ([]string, error) {
	seen := make(map[string]struct{})
	var ids []string
	for _, ref := range refs {
		var cid string
		err := tx.QueryRowContext(ctx, `
			SELECT contact_id::text FROM public.contacts
			WHERE tenant_id = $1 AND kind = $2 AND value_bidx = $3
			FOR UPDATE
		`, tenantID, ref.Kind, r.kp.BlindIndex(tenantID, ref.Value)).Scan(&cid)
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
// ref (cifrada) y ata las restantes al mismo id. El INSERT es idempotente sobre
// la PK (tenant_id, kind, value_bidx): si otra transacción ya insertó la ref
// entre nuestro lookup y este INSERT (carrera get-or-create; p.ej. el Start del
// flujo y un entrante concurrentes), ON CONFLICT DO UPDATE devuelve el
// contact_id existente en lugar de fallar con duplicate key (23505), igual que
// el hermano attachRef.
func (r *PostgresResolver) insertNewContact(ctx context.Context, tx *sql.Tx, tenantID string, refs []Ref, pushName string) (string, error) {
	bidx, enc, dek, kekID, err := r.encodeRef(tenantID, refs[0])
	if err != nil {
		return "", err
	}
	var cid string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO public.contacts (tenant_id, kind, value_bidx, value_enc, value_dek, value_kek_id, push_name)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, kind, value_bidx) DO UPDATE SET updated_at = now()
		RETURNING contact_id::text
	`, tenantID, refs[0].Kind, bidx, enc, dek, kekID, nullStr(pushName)).Scan(&cid)
	if err != nil {
		return "", fmt.Errorf("contact: insertar contacto: %w", err)
	}
	for _, ref := range refs[1:] {
		if err := r.attachRef(ctx, tx, tenantID, cid, ref, pushName); err != nil {
			return "", err
		}
	}
	return cid, nil
}

// encodeRef prepara las columnas cifradas de una ref: value_bidx (índice ciego),
// value_enc (envelope), value_dek (DEK envuelta) y kekID (el key_id de la KEK que
// envolvió la DEK, para persistir en value_kek_id y saber con qué KEK desenvolver
// tras una rotación, design.md §5, §6). El value en claro no sale de aquí.
func (r *PostgresResolver) encodeRef(tenantID string, ref Ref) (bidx string, enc, dek []byte, kekID string, err error) {
	bidx = r.kp.BlindIndex(tenantID, ref.Value)
	enc, dek, kekID, err = r.cipher.Encrypt(ref.Value)
	if err != nil {
		return "", nil, nil, "", fmt.Errorf("contact: cifrar value: %w", err)
	}
	return bidx, enc, dek, kekID, nil
}

// resolveExisting reusa (un solo contact_id) o funde (varios) los contact_id ya
// existentes en el canónico, ata las refs faltantes y actualiza el push_name.
func (r *PostgresResolver) resolveExisting(ctx context.Context, tx *sql.Tx, tenantID string, found []string, refs []Ref, pushName string) (string, error) {
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
		if err := r.attachRef(ctx, tx, tenantID, canonical, ref, pushName); err != nil {
			return "", err
		}
	}
	if pushName != "" {
		// El guard `IS DISTINCT FROM` evita tomar row-locks cuando el push_name no
		// cambia: en la ráfaga de historial el mismo contacto repite su push_name en
		// cada entrante, así que tras el primero este UPDATE no toca ninguna fila
		// (cero locks) y no puede entrar en el ciclo de deadlock (Plan 026 · T4).
		if _, err := tx.ExecContext(ctx, `
			UPDATE public.contacts SET push_name = $1, updated_at = now()
			WHERE tenant_id = $2 AND contact_id = $3 AND push_name IS DISTINCT FROM $1
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

// attachRef ata una ref (cifrada) al contact_id dado; si ya existe (dedup por
// (tenant, kind, value_bidx)) no hace nada.
func (r *PostgresResolver) attachRef(ctx context.Context, tx *sql.Tx, tenantID, contactID string, ref Ref, pushName string) error {
	bidx, enc, dek, kekID, err := r.encodeRef(tenantID, ref)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO public.contacts (tenant_id, kind, value_bidx, value_enc, value_dek, value_kek_id, contact_id, push_name)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tenant_id, kind, value_bidx) DO NOTHING
	`, tenantID, ref.Kind, bidx, enc, dek, kekID, contactID, nullStr(pushName))
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
		SELECT kind, value_enc, value_dek, value_kek_id FROM public.contacts
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
		var (
			kind     string
			enc, dek []byte
			kekID    string
		)
		if serr := rows.Scan(&kind, &enc, &dek, &kekID); serr != nil {
			return Ref{}, fmt.Errorf("contact: escanear ref: %w", serr)
		}
		// Descifra el value SOLO en memoria (borde de la app) para armar el
		// destino enviable (design.md §5). No se loguea (§8). Se desenvuelve con la
		// KEK que envolvió ESTA fila (value_kek_id), no con la current: tras una
		// rotación parcial coexisten filas de varias KEK (design.md §6, §8, §10.F).
		// Si esa KEK no está en el keyring, Decrypt falla claro (fail-safe §10.J).
		value, derr := r.cipher.Decrypt(enc, dek, kekID)
		if derr != nil {
			return Ref{}, fmt.Errorf("contact: descifrar value: %w", derr)
		}
		refs = append(refs, Ref{Kind: kind, Value: value})
	}
	if rerr := rows.Err(); rerr != nil {
		return Ref{}, fmt.Errorf("contact: iterar refs: %w", rerr)
	}
	if len(refs) == 0 {
		return Ref{}, fmt.Errorf("%w: %q", ErrContactNotFound, contactID)
	}
	return pickDestino(refs)
}
