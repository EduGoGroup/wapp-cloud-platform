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
//
// Desde T4.2 (Plan 046) el push_name TAMBIÉN va cifrado, en un sobre de TRES
// piezas —push_name_enc, push_name_dek, push_name_kek_id— y SIN índice ciego:
// nadie busca contactos por su nombre de perfil, así que no hay nada que indexar
// (migración 0069). La columna en claro push_name ya no existe: la retiró la 0070
// (T5.4, D-046.17). Mientras existió, contar filas con push_name no nulo era la
// prueba de que el backfill había hecho su trabajo; hoy no hay nada que contar
// porque no hay dónde escribirlo.
//
// 🔴 NO HAY MÉTODO DE LECTURA DEL push_name, Y ES DELIBERADO. Se comprobó antes
// de escribir T4.2: push_name no aparece en NINGÚN SELECT de este repositorio y
// contact.Contact.PushName (contact.go:59-60) no se puebla en ningún sitio —el
// struct Contact no se instancia en todo el repo—. Añadir un Decrypt sin llamador
// sería el hallazgo que este repo ya tiene registrado: un paquete verde puede
// probar código que nadie ejecuta. El día que aparezca un lector de verdad
// (consola, export), el patrón está a la vista en Destino (línea 437): añade
// las tres columnas a su SELECT y llama a cipher.Decrypt(enc, dek, kekID). Lo que
// hace eso seguro es una invariante que ya se cumple hoy: el sobre se escribe con
// EL MISMO FieldCipher y EL MISMO KeyProvider que ya abren value_enc, y el kek_id
// viaja EN LA FILA, así que cada fila se abre con la KEK que la cerró aunque haya
// habido una rotación parcial (design.md §6, §10.F).
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
// (ON CONFLICT) y atómico. El guard del UPDATE de push_name (resolveExisting)
// reduce la ventana, y desde T4.2 ese guard es un CENTINELA (push_name_enc IS
// NULL) en vez de una comparación de valores. El cambio no relaja nada, aprieta:
// comparar valores ya NO es posible —dos cifrados del mismo texto nunca son
// iguales, con DEK fresca y nonce fresco, así que un guard por valor casaría
// SIEMPRE y tomaría row-locks en CADA entrante, reabriendo justo este deadlock— y
// el centinela deja de casar antes que la comparación: no en el estado estable de
// la ráfaga, sino ya en el SEGUNDO entrante del contacto (MD-046.5).
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
//
// El push_name entra ya CIFRADO (T4.2), y desde la 0070 (T5.4) no hay ninguna otra
// forma de que entre: la columna en claro no existe. Con el nombre vacío las tres
// columnas del sobre van a NULL —la invariante de la fila es «las tres o ninguna»—,
// que es lo que hacía nullStr con la columna en claro y ahora hace pushNameEnvelope.
func (r *PostgresResolver) insertNewContact(ctx context.Context, tx *sql.Tx, tenantID string, refs []Ref, pushName string) (string, error) {
	bidx, enc, dek, kekID, err := r.encodeRef(tenantID, refs[0])
	if err != nil {
		return "", err
	}
	pnEnc, pnDek, pnKekID, err := r.pushNameEnvelope(pushName)
	if err != nil {
		return "", err
	}
	var cid string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO public.contacts (tenant_id, kind, value_bidx, value_enc, value_dek, value_kek_id,
		                             push_name_enc, push_name_dek, push_name_kek_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (tenant_id, kind, value_bidx) DO UPDATE SET updated_at = now()
		RETURNING contact_id::text
	`, tenantID, refs[0].Kind, bidx, enc, dek, kekID, pnEnc, pnDek, nullStr(pnKekID)).Scan(&cid)
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

// pushNameEnvelope prepara las TRES columnas cifradas del push_name: enc (el
// envelope), dek (la DEK envuelta) y kekID (el key_id de la KEK que la envolvió,
// que se persiste en push_name_kek_id). Es el ÚNICO sitio donde se cifra un
// push_name: lo comparten los dos INSERT, el UPDATE de resolveExisting y el
// barrido de BackfillPushName, para que no haya dos versiones de esta lógica.
//
// Es el gemelo de encodeRef con dos diferencias, y las dos son de REGLA, no de
// nombre:
//
//   - SIN índice ciego. Nadie busca ni deduplica por el nombre de perfil (el
//     lookup va siempre por value_bidx, ver lookupContactIDs), así que no hay nada
//     que indexar; un bidx sobre texto libre solo añadiría una superficie de
//     correlación sin un lector que la justifique.
//   - SIN normalización. El push_name es texto libre que el dueño del teléfono
//     escribe en su perfil, no una referencia con formato: Normalize solo sabe de
//     teléfonos, LIDs y usernames, y aplicarla aquí destruiría el dato. Por eso
//     este sobre NO tiene el desenlace de fallo por fila que sí tiene el de las
//     refs: aquí cifrar solo puede fallar por el stack de claves, que es un fallo
//     de TODAS las filas, no de esta.
//
// Un pushName vacío devuelve las tres piezas vacías SIN cifrar nada, y las
// escrituras las mandan a NULL. No se cifra la cadena vacía a propósito, y el
// motivo no es el ahorro: un sobre de cadena vacía dejaría push_name_enc NO NULO
// y el centinela de MD-046.5 no volvería a casar jamás, así que el nombre real que
// llegara después se perdería para siempre. Un valor que no es un valor ganaría la
// carrera del primer nombre no vacío (el razonamiento largo, en BackfillPushName).
//
// CERO PII: el error no lleva el nombre, solo el hecho de que no se pudo cifrar.
func (r *PostgresResolver) pushNameEnvelope(pushName string) (enc, dek []byte, kekID string, err error) {
	if pushName == "" {
		return nil, nil, "", nil
	}
	enc, dek, kekID, err = r.cipher.Encrypt(pushName)
	if err != nil {
		return nil, nil, "", fmt.Errorf("contact: cifrar push_name: %w", err)
	}
	return enc, dek, kekID, nil
}

// resolveExisting reusa (un solo contact_id) o funde (varios) los contact_id ya
// existentes en el canónico, ata las refs faltantes y sella el push_name.
//
// ── SOBRE «NO SELLAR EN BALDE» (bullet de MD-046.5), EVALUADO Y DESCARTADO ─────
// La instrucción decía: resolveExisting ya corre en transacción y ya hace su
// SELECT, así que ahí se mira si el sobre está vacío y solo entonces se cifra. Se
// fue a buscar ese SELECT y NO SIRVE, por tres razones que están en el código:
//
//  1. lookupContactIDs (líneas 115-137) selecciona SOLO contact_id::text. Añadirle
//     push_name_enc daría un booleano POR REF PRESENTE en esta llamada, no por
//     contacto.
//  2. Ese lookup bloquea únicamente las filas de las refs que trae ESTE entrante
//     (por eso existe el deadlock del Plan 026), mientras que el UPDATE de abajo
//     va por contact_id y toca TODAS las filas del contacto, incluidas las que el
//     lookup no vio. Un contacto con dos refs entra aquí con una sola.
//  3. El canónico ni siquiera se conoce en el momento del lookup: lo elige
//     pickCanonicalDB (líneas 353-371) y la fusión RE-APUNTA filas de otros
//     contact_id al canónico (fuseDB, línea 381). La respuesta correcta solo
//     existe DESPUÉS de fundir.
//
// O sea que el remedio limpio sería un SELECT EXISTS extra, después de la fusión.
// NO SE AÑADE: sería un round-trip más a Postgres en el camino caliente del
// historial (una goroutine por mensaje entrante, runtime OnIncoming), mientras que
// lo que ahorraría es un cifrado LOCAL —AES-GCM sobre un nombre de perfil más un
// WrapDEK que ni con el KMS sale del proceso (keyprovider_kms.go:60: cero llamadas
// al KMS por operación)—: microsegundos de CPU contra una ida y vuelta de red. El
// remedio sale más caro que el mal, así que se sella en balde a sabiendas. Lo que
// sí se respeta es lo que de verdad importaba de ese bullet: el que no casa el
// centinela NO TOMA ROW-LOCKS, y eso lo garantiza el WHERE, no el sellado.
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
		// El guard es un CENTINELA (push_name_enc IS NULL), no una comparación de
		// valores, y esa es la decisión MD-046.5. Dos cifrados del mismo texto NUNCA
		// son iguales —DEK fresca y nonce fresco por escritura—, así que el viejo
		// `IS DISTINCT FROM` casaría SIEMPRE y tomaría row-locks en CADA entrante de
		// la ráfaga de historial, reabriendo el deadlock que protege
		// deadlock_integration_test.go:29-33. Con el centinela, tras el PRIMER nombre
		// no vacío del contacto este UPDATE no casa ninguna fila: cero locks.
		//
		// Precio aceptado: GANA EL PRIMER NOMBRE NO VACÍO. Si el cliente se cambia el
		// nombre en WhatsApp, la fila conserva el primero. Es un dato de negocio
		// auxiliar y hoy nadie lo lee (ver el docstring del tipo).
		//
		// 🔧 EL SET LLEVABA ADEMÁS `push_name = NULL` HASTA T5.4, y merece la pena saber
		// por qué estaba y por qué ya no. Se añadió como enmienda al literal de MD-046.5
		// para tapar un agujero real de las filas LEGACY: si un entrante tocaba una fila
		// anterior a la 0069 ANTES de que el backfill la viera, este UPDATE le escribía
		// el sobre y le dejaba el nombre EN CLARO al lado; desde ese instante la fila ya
		// no casaba el centinela `push_name_enc IS NULL` del backfill, así que nadie la
		// volvía a mirar y ese nombre se quedaba en claro para siempre.
		//
		// La 0070 (T5.4) retiró la columna en claro y con ella el backfill: no hay fila
		// legacy que tapar ni columna que vaciar. Lo que queda es lo que siempre fue el
		// fondo del asunto — el CENTINELA `push_name_enc IS NULL` del WHERE, que es la
		// decisión de MD-046.5 y sigue intacta.
		pnEnc, pnDek, pnKekID, encErr := r.pushNameEnvelope(pushName)
		if encErr != nil {
			return "", encErr
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE public.contacts
			   SET push_name_enc = $1, push_name_dek = $2, push_name_kek_id = $3,
			       updated_at = now()
			 WHERE tenant_id = $4 AND contact_id = $5 AND push_name_enc IS NULL
		`, pnEnc, pnDek, pnKekID, tenantID, canonical); err != nil {
			return "", fmt.Errorf("contact: actualizar push_name: %w", err)
		}
	}
	return canonical, nil
}

// nullStr convierte la cadena vacía en NULL para columnas opcionales. Desde T4.2
// su único uso es push_name_kek_id en los dos INSERT: enc y dek son []byte y su
// nil ya viaja como NULL, pero el key_id es texto y sin esto un contacto sin
// nombre guardaría una cadena vacía donde la invariante pide NULL («las tres
// columnas del sobre o ninguna»).
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// attachRef ata una ref (cifrada) al contact_id dado; si ya existe (dedup por
// (tenant, kind, value_bidx)) no hace nada. El push_name viaja en su sobre de tres
// columnas, igual que en insertNewContact, y con el nombre vacío van las tres a
// NULL.
func (r *PostgresResolver) attachRef(ctx context.Context, tx *sql.Tx, tenantID, contactID string, ref Ref, pushName string) error {
	bidx, enc, dek, kekID, err := r.encodeRef(tenantID, ref)
	if err != nil {
		return err
	}
	pnEnc, pnDek, pnKekID, err := r.pushNameEnvelope(pushName)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO public.contacts (tenant_id, kind, value_bidx, value_enc, value_dek, value_kek_id, contact_id,
		                             push_name_enc, push_name_dek, push_name_kek_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (tenant_id, kind, value_bidx) DO NOTHING
	`, tenantID, ref.Kind, bidx, enc, dek, kekID, contactID, pnEnc, pnDek, nullStr(pnKekID))
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
