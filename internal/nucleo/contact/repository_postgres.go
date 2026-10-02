// Porta internal/flujos/contact/repository_postgres.go @ 77df20f
// cobertura: adaptador postgres (05 E-6)

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

// PostgresResolver implementa Resolver con SQL crudo sobre public.contacts y, en la fusión, sobre
// public.flow_state. Es la implementación que corre en producción: la identidad de los contactos
// vive en Postgres, con el identificador cifrado en reposo. Se construye con NewPostgresResolver y
// es seguro para uso concurrente (cada Resolve abre su propia transacción). La suite
// contacthelpertest.Contrato fija lo que promete junto a MemoryResolver; su SQL solo lo ejercita un
// Postgres real (esa misma suite contra esta implementación y los procesos de F9), no un test
// unitario.
//
// Atomicidad (R-31). Toda Resolve corre en UNA transacción, abierta con postgres.WithTx del paquete
// internal/platform/storage/postgres. Ese helper revierte aunque el cuerpo entre en pánico y
// REINTENTA la transacción entera, con una espera acotada, ante un deadlock (SQLSTATE 40P01) o un
// fallo de serialización (40001). Gracias a ella la fusión (re-apuntar las refs del huérfano al
// canónico y migrar su flow_state) es atómica: ocurre entera o no ocurre. PostgresResolver NO usa
// StateMigrator: esa migración la hace en SQL dentro de su misma transacción.
//
// Cifrado en reposo del identificador (R-23, R-24). La fila de una ref guarda value_bidx,
// value_enc, value_dek y value_kek_id, y no existe una columna con el value en claro: el value solo
// vive en memoria, en el borde de la app, y no se loguea.
//   - value_bidx es el índice ciego, kp.BlindIndex(tenant, value). Con él se busca y se deduplica,
//     y la clave de unicidad de la fila es (tenant_id, kind, value_bidx): el kind es parte de la
//     clave (N-02) y, como el índice depende del tenant, el mismo value en otro tenant da otro
//     índice y otro contacto (N-01).
//   - value_enc es el value cifrado; value_dek, la DEK de ESE valor envuelta con la KEK vigente, y
//     value_kek_id, el id de la KEK que la envolvió. Cada fila lleva su propio kek_id para que, tras
//     una rotación parcial de KEK, siga siendo legible con la KEK que la cerró. En modo compat
//     (una sola KEK maestra) ese id es "1".
//
// Homónimo: la «DEK» de value_dek y de push_name_dek es la del envelope de PII de negocio (una DEK
// fresca por valor, envuelta por la KEK del keyring de este repo). NO es la DEK del ADR-0007, la
// que descifra el almacén de whatsmeow, que custodia el cliente y jamás cruza el contrato.
//
// push_name (R-26, R-27). También va cifrado, en un sobre de TRES piezas (push_name_enc,
// push_name_dek y push_name_kek_id) y SIN índice ciego: nadie busca ni deduplica contactos por su
// nombre de perfil, así que no hay nada que indexar, y un índice sobre texto libre solo añadiría una
// superficie de correlación. Tampoco se normaliza: es texto libre que el dueño del teléfono escribe
// en su perfil, y Normalize destruiría el dato. La invariante de la fila es «las tres piezas o
// ninguna». Un pushName vacío NO se cifra y las tres piezas van a NULL. No es un ahorro: el sobre de
// la cadena vacía dejaría push_name_enc no nulo, el centinela de Resolve (más abajo) no volvería a
// casar y el nombre real que llegara después se perdería para siempre.
//
// No hay lector de push_name, y es deliberado (R-33). Ningún SELECT del repositorio lo trae y el
// nombre no se puebla en ningún tipo de dominio. Añadir un descifrador sin llamador sería probar
// código que nadie ejecuta: un paquete verde que cubre una ruta que ninguna ruta real atraviesa. El
// día que aparezca un lector de verdad (una consola, un export), el patrón es el de Destino: traer
// las tres columnas y abrirlas con el kek_id de LA FILA. Es seguro porque el sobre se escribe con el
// mismo FieldCipher y el mismo KeyProvider que ya abren value_enc.
type PostgresResolver struct {
	db     *sql.DB
	cipher *crypto.FieldCipher
	kp     crypto.KeyProvider
}

// NewPostgresResolver construye el resolver sobre el pool db. cipher cifra y descifra el value y el
// push_name (un envelope por valor) y kp calcula el índice ciego value_bidx.
//
// No valida sus argumentos: devuelve siempre un resolver y no tiene un error en la firma. Un nil se
// descubre en el primer uso que lo necesite, con un pánico, no aquí; Resolve con la lista vacía
// devuelve ErrNoRefs sin tocar ninguno de los tres.
//
// cipher y kp deben ser EL MISMO FieldCipher y EL MISMO KeyProvider que usa el resto del arranque
// (el cipher se construye sobre el kp con crypto.NewFieldCipher). Pasar otro KeyProvider no da
// ningún error: calcula otro value_bidx para la misma ref, así que no encuentra los contactos ya
// guardados y duplica contactos en silencio. Pasar otro keyring deja ilegibles, o hace fallar, las
// filas cerradas con una KEK que ese keyring no tiene.
func NewPostgresResolver(db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider) *PostgresResolver {
	return &PostgresResolver{db: db, cipher: cipher, kp: kp}
}

// Resolve implementa Resolver.Resolve en UNA transacción (ver PostgresResolver). Cumple lo que
// promete el puerto (deduplicación de refs, creación, reutilización y fusión, R-12 a R-17, y
// contactID "" con error) y, en Postgres, lo siguiente.
//
// Precondición: cada Ref viene de NewRef. Resolve NO la re-normaliza: normalizar otra vez cambiaría
// el value_bidx de las filas que ya existen, y con él a qué contacto pertenece cada ref.
//
// Sin refs (R-18). Con la lista vacía (nil o []Ref{}), tras deduplicar, devuelve ErrNoRefs ANTES de
// abrir la transacción: no toca la base de datos, ni el cifrado, ni el índice ciego. Es el único
// camino de Resolve que se prueba sin Postgres, con un resolver construido con argumentos nil.
//
// Búsqueda, creación y get-or-create (R-32). Busca cada ref por (tenant_id, kind, value_bidx) y
// bloquea con FOR UPDATE las filas que encuentra, para serializar fusiones concurrentes del mismo
// contacto. Si ninguna existe, inserta la primera ref con un contact_id nuevo (el que genera la
// base de datos por defecto) y ata las demás a ese mismo id. El INSERT de la primera ref lleva
// ON CONFLICT (tenant_id, kind, value_bidx) DO UPDATE ... RETURNING contact_id: si otra transacción
// insertó esa ref entre la búsqueda y el INSERT (la carrera get-or-create: el arranque de un flujo
// y un entrante simultáneos), devuelve el contact_id que ya existe en lugar de fallar con duplicate
// key (23505). Las refs que se atan después van con ON CONFLICT DO NOTHING. Por eso es seguro con
// concurrencia: N llamadas simultáneas con la misma ref terminan con UN solo contact_id.
//
// Fusión (R-16, R-17, N-03). Si las refs pertenecen a contact_id distintos, el canónico es el de
// created_at más antiguo (ante un empate, el id menor). Por cada huérfano, y en este orden: borra
// las filas de flow_state del huérfano cuya sesión ya tiene fila del canónico (política de
// conflicto: se CONSERVA la del canónico, la identidad autoritativa); pasa el resto de su
// flow_state al canónico, ya sin colisión de la clave (tenant, sesión, contact_id); y re-apunta al
// canónico las refs de public.contacts del huérfano. Después el huérfano ya no existe. Las tres
// sentencias comparten la transacción de Resolve.
//
// push_name (R-27, R-28). Con pushName no vacío lo sella: en la fila de cada ref que inserta y, para
// un contacto que la búsqueda encontró, con un UPDATE sobre las filas del contacto canónico cuyo
// WHERE lleva el CENTINELA push_name_enc IS NULL. Así el nombre que llega tarde a un contacto creado
// sin él (verdad de campo: «a veces el nombre no llega en los primeros eventos, llega posterior») se
// guarda entonces. Con pushName vacío no toca el nombre. Gana el primer nombre no vacío POR FILA: si
// el cliente se cambia el nombre en WhatsApp, la fila conserva el primero. Qué nombre sobrevive NO es
// parte del puerto (la memoria conserva el último) y pushName nunca cambia el contact_id (N-04).
//
// Una rama NO sella el nombre en lo que ya existía: la carrera get-or-create. Si la búsqueda no
// encontró ninguna ref y el INSERT de la primera choca con la fila que otra transacción acaba de
// insertar, su ON CONFLICT ... DO UPDATE solo toca updated_at, no escribe el sobre en esa fila; y ese
// camino, el de la creación, no ejecuta el UPDATE del centinela, que solo corre cuando la búsqueda
// encontró el contacto. El pushName de esa llamada queda entonces, como mucho, en las filas de las
// demás refs que ella misma inserte. La fila que ganó la carrera conserva lo que tuviera: si nació
// sin nombre, sigue sin él hasta la siguiente Resolve con pushName no vacío que sí encuentre el
// contacto, que lo sella porque el centinela sigue casando.
//
// Por qué un centinela y no una comparación de valores (MD-046.5). Dos cifrados del mismo texto
// nunca son iguales, porque cada escritura usa una DEK y un nonce frescos: un guard por valor
// casaría SIEMPRE y tomaría row-locks sobre todas las filas del contacto en CADA entrante,
// reabriendo el deadlock de más abajo. Con el centinela, tras el primer nombre no vacío el UPDATE
// no casa ninguna fila y no toma ningún lock. El precio es el de arriba, un nombre desactualizado en
// un dato de negocio auxiliar que nadie lee, y está aceptado: no se «arregla» ni se hace que la
// memoria copie esta regla.
//
// Tampoco se evita cifrar un nombre que quizá no se escriba (evaluado y descartado en MD-046.5).
// Saber si el contacto ya tiene sobre exigiría una consulta más después de la fusión, porque la
// búsqueda inicial solo trae el contact_id de las refs presentes en ESTA llamada y el canónico no
// se conoce hasta fundir. Eso es un viaje de ida y vuelta a Postgres más en el camino caliente del
// historial, para ahorrar un cifrado local (AES-GCM y envolver la DEK, sin ninguna llamada al KMS
// por operación) que cuesta microsegundos. Lo que sí se garantiza es lo que importaba: a quien no
// casa el centinela, el WHERE no le toma locks.
//
// Reintento y deadlock (R-29, R-31). Bajo una inundación de historial (un número nuevo hace que
// WhatsApp vuelque su historial) el runtime lanza una goroutine por mensaje entrante y cada una
// llama a Resolve ANTES de tomar el lock por conversación, que se toma después, con el contact_id ya
// resuelto: N transacciones de contactos corren de verdad en paralelo. El deadlock aparece cuando
// dos entrantes del MISMO contacto llegan con identidad parcial y disjunta, porque WhatsApp
// enriquece de forma desigual (uno trae solo el número, otro solo el LID): cada transacción bloquea
// con FOR UPDATE solo la fila de SU ref, pero el UPDATE del nombre va por contact_id y toca TODAS
// las filas del contacto. Una retiene la fila del número y pide la del LID, la otra al revés: ciclo.
//
// No hay una clave común, conocida de antemano, con la que serializarlas (la identidad unificada
// solo se descubre a mitad de la transacción), así que ni ordenar las refs ni un lock previo lo
// evitan. El remedio es dejar que Postgres rompa el ciclo (aborta una de las transacciones) y
// reintentar, y converge porque la transacción es atómica y el upsert idempotente. El centinela
// reduce la ventana: no la elimina. Para reproducir el ciclo en un test contra Postgres hay que
// sembrar el contacto SIN nombre; sembrado con nombre el centinela ya no casa, no hay locks que
// cruzar y el test queda verde y hueco. Hoy ningún test pone rojo la ausencia del reintento: es
// cosa del proceso «entrante a respuesta» de F9.
//
// Errores. Fuera de ErrNoRefs, que se inspecciona con errors.Is, todo error del cuerpo de la
// transacción va envuelto con %w y con el prefijo «contact: »; sus textos son observables y no
// cambian:
//   - «contact: buscar ref: %w», la búsqueda por value_bidx;
//   - «contact: cifrar value: %w» y «contact: cifrar push_name: %w», el cifrado, que solo puede
//     fallar por el stack de claves, esto es, para TODAS las filas y no para esta;
//   - «contact: insertar contacto: %w», el INSERT de la primera ref;
//   - «contact: adjuntar ref: %w», el INSERT de cada ref que se ata después;
//   - «contact: actualizar push_name: %w», el UPDATE del nombre de un contacto existente;
//   - «contact: created_at del contacto: %w», la elección del canónico;
//   - «contact: podar flow_state huérfano: %w», «contact: migrar flow_state en fusión: %w» y
//     «contact: re-apuntar refs en fusión: %w», los tres pasos de la fusión.
//
// Los errores de la transacción misma los devuelve postgres.WithTx, y Resolve no los vuelve a
// envolver ni les antepone «contact: ». Son de tres clases, y solo la primera lleva un prefijo propio:
//   - con el prefijo «postgres: »: «postgres: iniciar transacción: %w» (abrirla), «postgres:
//     confirmar transacción: %w» (el COMMIT) y, agotados los intentos ante un deadlock o un fallo de
//     serialización, «postgres: transacción tras %d intentos (último deadlock/serialización): %w»
//     (hoy, 8 intentos), que envuelve el último error del cuerpo: ahí el «contact: ...» del cuerpo
//     no abre el texto, va detrás de ese prefijo;
//   - el error del contexto DESNUDO, sin ningún prefijo (context.Canceled o
//     context.DeadlineExceeded tal cual), si el ctx se cancela durante la espera que sigue a un
//     intento abortado por deadlock o serialización: el error que provocó el reintento no aparece;
//   - un error compuesto con errors.Join, si además falla el ROLLBACK de un intento: une el error
//     que lo abortó (el del cuerpo, con su «contact: », o el de confirmar si el ctx se canceló justo
//     antes del COMMIT) y el del rollback, cada uno en su línea del texto y sin un prefijo común.
//     errors.Is y errors.As ven los dos.
//
// Un tenantID mal formado (no UUID) da el error de parseo de Postgres, nunca un centinela. Ningún
// error contiene el value ni el pushName (R1.4.d): son PII y los errores suben a los logs; los de
// cifrado dicen solo que no se pudo cifrar.
func (r *PostgresResolver) Resolve(ctx context.Context, tenantID string, refs []Ref, pushName string) (string, error) {
	refs = dedupeRefs(refs)
	if len(refs) == 0 {
		return "", ErrNoRefs
	}
	// Toda la resolución corre en UNA transacción con reintento ante deadlock o fallo de
	// serialización vía el helper único postgres.WithTx (Plan 027 · Ola 1 · T4, cierra H7/H8): el
	// rollback es inmune a un pánico (marca de confirmado) y el reintento es seguro porque la
	// resolución es atómica e idempotente (ON CONFLICT). Antes esto vivía en un reintento artesanal
	// con un rollback condicionado a `err != nil`, que no cubría el pánico.
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

// lookupContactIDs devuelve, en orden estable, los contact_id distintos ya mapeados por alguna
// ref. Busca por el índice ciego (value_bidx), no por el value en claro, que no vive en la fila.
// Bloquea las filas encontradas (FOR UPDATE) para serializar fusiones concurrentes del mismo
// contacto.
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

// insertNewContact crea un contact_id nuevo (UUID por DEFAULT) con la primera ref, cifrada, y ata
// las restantes al mismo id. El INSERT es idempotente sobre la PK (tenant_id, kind, value_bidx): si
// otra transacción ya insertó la ref entre nuestra búsqueda y este INSERT (la carrera
// get-or-create; p. ej. el arranque del flujo y un entrante simultáneos), ON CONFLICT DO UPDATE
// devuelve el contact_id existente en lugar de fallar con duplicate key (23505), igual que su
// hermano attachRef.
//
// El push_name entra ya CIFRADO, y no hay otra forma de que entre: la columna en claro no existe.
// Con el nombre vacío las tres columnas del sobre van a NULL —la invariante de la fila es «las tres
// o ninguna»—, que es lo que hace pushNameEnvelope.
func (r *PostgresResolver) insertNewContact(ctx context.Context, tx *sql.Tx, tenantID string, refs []Ref, pushName string) (string, error) {
	bidx, enc, dek, kekID, err := encodeRef(r.kp, r.cipher, tenantID, refs[0])
	if err != nil {
		return "", err
	}
	pnEnc, pnDek, pnKekID, err := pushNameEnvelope(r.cipher, pushName)
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

// encodeRef prepara las columnas cifradas de una ref: value_bidx (el índice ciego,
// kp.BlindIndex(tenant, value)), value_enc (el envelope), value_dek (la DEK envuelta) y kekID (el
// key_id de la KEK que envolvió la DEK, que se persiste en value_kek_id para saber con qué KEK
// desenvolver tras una rotación). El value en claro no sale de aquí, y el error de cifrado no lo
// lleva: solo dice «contact: cifrar value: %w».
func encodeRef(kp crypto.KeyProvider, cipher *crypto.FieldCipher, tenantID string, ref Ref) (bidx string, enc, dek []byte, kekID string, err error) {
	bidx = kp.BlindIndex(tenantID, ref.Value)
	enc, dek, kekID, err = cipher.Encrypt(ref.Value)
	if err != nil {
		return "", nil, nil, "", fmt.Errorf("contact: cifrar value: %w", err)
	}
	return bidx, enc, dek, kekID, nil
}

// pushNameEnvelope prepara las TRES columnas cifradas del push_name: enc (el envelope), dek (la DEK
// envuelta) y kekID (el key_id de la KEK que la envolvió, que se persiste en push_name_kek_id). Es
// el ÚNICO sitio donde se cifra un push_name: lo comparten los dos INSERT y el UPDATE de
// resolveExisting, para que no haya dos versiones de esta lógica.
//
// Es el gemelo de encodeRef con dos diferencias, y las dos son de REGLA, no de nombre:
//
//   - SIN índice ciego. Nadie busca ni deduplica por el nombre de perfil (la búsqueda va siempre por
//     value_bidx, ver lookupContactIDs), así que no hay nada que indexar; un bidx sobre texto libre
//     solo añadiría una superficie de correlación sin un lector que la justifique.
//   - SIN normalización. El push_name es texto libre que el dueño del teléfono escribe en su perfil,
//     no una referencia con formato: Normalize solo sabe de teléfonos, LIDs y usernames, y aplicarla
//     aquí destruiría el dato. Cifrar aquí solo puede fallar por el stack de claves, que es un fallo
//     de TODAS las filas, no de esta.
//
// Un pushName vacío devuelve las tres piezas vacías SIN cifrar nada, y las escrituras las mandan a
// NULL. No se cifra la cadena vacía a propósito, y el motivo no es el ahorro: un sobre de cadena
// vacía dejaría push_name_enc NO NULO y el centinela de MD-046.5 no volvería a casar jamás, así que
// el nombre real que llegara después se perdería para siempre. Un valor que no es un valor ganaría
// la carrera del primer nombre no vacío.
//
// CERO PII: el error no lleva el nombre, solo el hecho de que no se pudo cifrar.
func pushNameEnvelope(cipher *crypto.FieldCipher, pushName string) (enc, dek []byte, kekID string, err error) {
	if pushName == "" {
		return nil, nil, "", nil
	}
	enc, dek, kekID, err = cipher.Encrypt(pushName)
	if err != nil {
		return nil, nil, "", fmt.Errorf("contact: cifrar push_name: %w", err)
	}
	return enc, dek, kekID, nil
}

// resolveExisting reusa (un solo contact_id) o funde (varios) los contact_id ya existentes en el
// canónico, ata las refs que faltan y sella el push_name.
//
// ── SOBRE «NO SELLAR EN BALDE» (bullet de MD-046.5), EVALUADO Y DESCARTADO ─────
// La instrucción decía: resolveExisting ya corre en transacción y ya hace su SELECT, así que ahí se
// mira si el sobre está vacío y solo entonces se cifra. Se fue a buscar ese SELECT y NO SIRVE, por
// tres razones que están en el código:
//
//  1. lookupContactIDs selecciona SOLO contact_id::text. Añadirle push_name_enc daría un booleano
//     POR REF PRESENTE en esta llamada, no por contacto.
//  2. Esa búsqueda bloquea únicamente las filas de las refs que trae ESTE entrante (por eso existe
//     el deadlock del Plan 026), mientras que el UPDATE de abajo va por contact_id y toca TODAS las
//     filas del contacto, incluidas las que la búsqueda no vio. Un contacto con dos refs entra aquí
//     con una sola.
//  3. El canónico ni siquiera se conoce en el momento de la búsqueda: lo elige pickCanonicalDB y la
//     fusión (fuseDB) RE-APUNTA filas de otros contact_id al canónico. La respuesta correcta solo
//     existe DESPUÉS de fundir.
//
// O sea que el remedio limpio sería un SELECT EXISTS extra, después de la fusión. NO SE AÑADE: sería
// un viaje de ida y vuelta más a Postgres en el camino caliente del historial (una goroutine por
// mensaje entrante), mientras que lo que ahorraría es un cifrado LOCAL —AES-GCM sobre un nombre de
// perfil más un WrapDEK que ni con el KMS sale del proceso: cero llamadas al KMS por operación—:
// microsegundos de CPU contra una ida y vuelta de red. El remedio sale más caro que el mal, así que
// se sella en balde a sabiendas. Lo que sí se respeta es lo que de verdad importaba de ese bullet:
// el que no casa el centinela NO TOMA ROW-LOCKS, y eso lo garantiza el WHERE, no el sellado.
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
		// El guard es un CENTINELA (push_name_enc IS NULL), no una comparación de valores, y esa
		// es la decisión MD-046.5. Dos cifrados del mismo texto NUNCA son iguales —DEK fresca y
		// nonce fresco por escritura—, así que el viejo `IS DISTINCT FROM` casaría SIEMPRE y
		// tomaría row-locks en CADA entrante de la ráfaga de historial, reabriendo el deadlock del
		// Plan 026 (ver Resolve). Con el centinela, tras el PRIMER nombre no vacío del contacto
		// este UPDATE no casa ninguna fila: cero locks.
		//
		// Precio aceptado: GANA EL PRIMER NOMBRE NO VACÍO. Si el cliente se cambia el nombre en
		// WhatsApp, la fila conserva el primero. Es un dato de negocio auxiliar y hoy nadie lo lee
		// (ver el comentario del tipo).
		pnEnc, pnDek, pnKekID, encErr := pushNameEnvelope(r.cipher, pushName)
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

// nullStr convierte la cadena vacía en NULL para columnas opcionales. Su único uso es
// push_name_kek_id en los dos INSERT: enc y dek son []byte y su nil ya viaja como NULL, pero el
// key_id es texto y sin esto un contacto sin nombre guardaría una cadena vacía donde la invariante
// pide NULL («las tres columnas del sobre o ninguna»).
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// attachRef ata una ref (cifrada) al contact_id dado; si ya existe (dedup por (tenant, kind,
// value_bidx)) no hace nada. El push_name viaja en su sobre de tres columnas, igual que en
// insertNewContact, y con el nombre vacío van las tres a NULL.
func (r *PostgresResolver) attachRef(ctx context.Context, tx *sql.Tx, tenantID, contactID string, ref Ref, pushName string) error {
	bidx, enc, dek, kekID, err := encodeRef(r.kp, r.cipher, tenantID, ref)
	if err != nil {
		return err
	}
	pnEnc, pnDek, pnKekID, err := pushNameEnvelope(r.cipher, pushName)
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

// canonicalCandidate es un contact_id que entra en la elección del canónico de una fusión, con el
// created_at más antiguo de sus filas.
type canonicalCandidate struct {
	id        string
	createdAt time.Time
}

// pickCanonicalDB elige el canónico de la fusión entre los contact_id dados: trae de Postgres el
// MIN(created_at) de las filas de cada uno, en el orden de found, y delega la elección en
// pickCanonical. El primer fallo de la consulta corta con «contact: created_at del contacto: %w».
func pickCanonicalDB(ctx context.Context, tx *sql.Tx, tenantID string, found []string) (string, error) {
	cands := make([]canonicalCandidate, 0, len(found))
	for _, id := range found {
		var created time.Time
		err := tx.QueryRowContext(ctx, `
			SELECT MIN(created_at) FROM public.contacts
			WHERE tenant_id = $1 AND contact_id = $2
		`, tenantID, id).Scan(&created)
		if err != nil {
			return "", fmt.Errorf("contact: created_at del contacto: %w", err)
		}
		cands = append(cands, canonicalCandidate{id: id, createdAt: created})
	}
	return pickCanonical(cands), nil
}

// pickCanonical es la parte pura de la elección del canónico: el candidato con el createdAt más
// antiguo; ante un empate, el id menor (comparación de cadenas). No depende del orden de entrada.
// Sin candidatos devuelve "" (Resolve solo la usa con dos o más).
func pickCanonical(cands []canonicalCandidate) string {
	canonical := ""
	var best time.Time
	for _, c := range cands {
		if canonical == "" || c.createdAt.Before(best) || (c.createdAt.Equal(best) && c.id < canonical) {
			canonical = c.id
			best = c.createdAt
		}
	}
	return canonical
}

// fuseDB funde el contact_id huérfano en el canónico, dentro de la MISMA transacción (atomicidad):
//  1. Poda las filas de flow_state del huérfano cuya sesión YA tiene fila del canónico: política de
//     conflicto = CONSERVAR la del canónico (identidad autoritativa) y descartar la del huérfano.
//  2. Migra el resto de flow_state del huérfano al canónico (ya sin colisión de la PK (tenant,
//     session, contact_id)).
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

// Destino implementa Resolver.Destino con una lectura FUERA de transacción: trae kind, value_enc,
// value_dek y value_kek_id de todas las filas del contacto en el tenant, descifra el value de cada
// una y elige la ref enviable por la preferencia del puerto (phone_e164 > wa_username > wa_lid,
// entre las direccionables, R-19, R-20). Devuelve la Ref con el value NORMALIZADO tal como quedó
// guardado, no el crudo con el que llegó (R-23).
//
// Abre cada fila con SU value_kek_id, no con la KEK vigente (R-24): tras una rotación parcial
// coexisten filas cerradas por varias KEK, y la rotación (crypto.Rekey, de platform) solo vuelve a
// envolver la DEK y cambia value_kek_id, sin tocar value_enc ni value_bidx. Si la KEK de una fila no
// está en el keyring, falla claro, con «contact: descifrar value: %w», nunca con basura ni con un
// pánico; y la primera fila que no abre hace fallar la llamada entera, aunque otra fila del contacto
// sí abriera: no devuelve un destino parcial. El value en claro solo vive en memoria y no se loguea.
//
// Errores, con la Ref cero:
//   - ErrContactNotFound, envuelto con %w y el contactID entre comillas (%q), si no hay ninguna fila
//     del contacto en ese tenant: no existe, es de otro tenant (N-01) o es el de un huérfano que
//     una fusión ya borró (R-21, N-03);
//   - ErrNoDestino, sin envolver, si el contacto existe pero ninguna de sus refs es direccionable
//     (p. ej. solo un wa_username, R-20);
//   - «contact: leer refs del contacto: %w», «contact: escanear ref: %w», «contact: descifrar
//     value: %w» y «contact: iterar refs: %w», los fallos de la lectura, de cada fila y del cursor.
//
// Solo si no hubo ningún otro error, un fallo al cerrar el cursor se devuelve como «contact: cerrar
// filas: %w», y es la única excepción a la Ref cero: sale JUNTO con la Ref que ya se había elegido.
// Un tenantID o un contactID mal formados (no UUID) dan el error de parseo de Postgres
// envuelto en «leer refs del contacto», NO ErrContactNotFound (la memoria sí lo devuelve). Ningún
// error contiene el value (R1.4.d).
func (r *PostgresResolver) Destino(ctx context.Context, tenantID, contactID string) (ref Ref, err error) {
	rows, qerr := r.db.QueryContext(ctx, `
		SELECT kind, value_enc, value_dek, value_kek_id FROM public.contacts
		WHERE tenant_id = $1 AND contact_id = $2
	`, tenantID, contactID)
	if qerr != nil {
		return Ref{}, fmt.Errorf("contact: leer refs del contacto: %w", qerr)
	}
	// El fallo al cerrar el cursor solo se ve si no hubo otro error, y NO pone la Ref a cero: si
	// pickDestino ya eligió una, sale junto con «contact: cerrar filas» (el comportamiento del
	// viejo, que se conserva: ver closeRowsErr).
	defer func() { err = closeRowsErr(err, rows.Close()) }()

	refs, oerr := openRows(r.cipher, rows, contactID)
	if oerr != nil {
		return Ref{}, oerr
	}
	return pickDestino(refs)
}

// rowScanner es lo que openRows necesita de un *sql.Rows: avanzar, escanear y el error del cursor.
// Existe para probar la apertura de las filas sin base de datos.
type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

// openRows recorre las filas (kind, value_enc, value_dek, value_kek_id) de un contacto y devuelve
// sus refs con el value descifrado, en el orden de las filas. Descifra el value SOLO en memoria (el
// borde de la app) para armar el destino enviable, y no lo loguea. Cada fila se desenvuelve con la
// KEK que la cerró (SU value_kek_id), no con la vigente: tras una rotación parcial coexisten filas
// de varias KEK. Si esa KEK no está en el keyring, Decrypt falla claro (fail-safe), y la primera
// fila que no abre corta el recorrido entero: no hay destino parcial.
//
// Errores: «contact: escanear ref: %w», «contact: descifrar value: %w» y «contact: iterar refs:
// %w»; sin ninguna fila, ErrContactNotFound envuelto con el contactID entre comillas (%q).
func openRows(cipher *crypto.FieldCipher, rows rowScanner, contactID string) ([]Ref, error) {
	var refs []Ref
	for rows.Next() {
		var (
			kind     string
			enc, dek []byte
			kekID    string
		)
		if serr := rows.Scan(&kind, &enc, &dek, &kekID); serr != nil {
			return nil, fmt.Errorf("contact: escanear ref: %w", serr)
		}
		value, derr := cipher.Decrypt(enc, dek, kekID)
		if derr != nil {
			return nil, fmt.Errorf("contact: descifrar value: %w", derr)
		}
		refs = append(refs, Ref{Kind: kind, Value: value})
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("contact: iterar refs: %w", rerr)
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrContactNotFound, contactID)
	}
	return refs, nil
}

// closeRowsErr decide el error de Destino tras cerrar el cursor: si ya había uno, gana ése y el del
// cierre se descarta; si no, un fallo del cierre se devuelve como «contact: cerrar filas: %w»; sin
// ninguno de los dos, nil. No toca la Ref: Destino la devuelve tal cual estuviera.
func closeRowsErr(err, closeErr error) error {
	if closeErr != nil && err == nil {
		return fmt.Errorf("contact: cerrar filas: %w", closeErr)
	}
	return err
}
