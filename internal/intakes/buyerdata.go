// buyerdata.go guarda los DATOS DEL COMPRADOR de una solicitud CIFRADOS EN REPOSO
// (Plan 041 · T4.5, D-041.13 / ADR-0031 §5 / ADR-0017).
//
// Es la ÚNICA parte del dominio de solicitudes que toca PII almacenada, y por eso
// vive aparte: el resto del paquete mueve datos de negocio en claro (ADR-0009) y
// mezclarlos daría a entender que la separación es opcional. No lo es: la fila de
// public.intake_buyer_data es la razón por la que intakes, intake_items e
// intake_revisions pueden ir en claro sin remordimiento.
//
// EL PATRÓN ES EL DE public.contacts (migraciones 0006/0007, Plan 011), copiado y
// no reinventado: envelope encryption de tres piezas —dato cifrado con una DEK
// fresca por fila, DEK envuelta por la KEK del keyring, y el key_id de esa KEK—.
// Las tres columnas son necesarias: sin data_dek no hay con qué descifrar, y sin
// data_kek_id la fila queda FUERA de la rotación de KEK del Plan 012, porque nadie
// sabría con cuál desenvolverla. Ese es el motivo por el que la tabla no siguió el
// `data_encrypted TEXT` que esbozaba el design §3.
//
// LO QUE ESTE ARCHIVO NO TIENE, a propósito: una lectura en claro. El descifrado
// existe —lo hace el propio cipher— pero está CUSTODIADO y llega con su consumidor
// real (el puente del Plan 042 o una pantalla), no antes. Publicar hoy un
// `GetBuyerData` sin nadie que lo use sería abrir la puerta y dejarla abierta. Lo
// que sí se publica es si la fila EXISTE (buyer_data_present, ver Store.Get).
package intakes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// ErrBuyerFieldEmpty lo devuelve PutBuyerField cuando la clave del campo viene
// vacía: sin clave, el valor no se podría volver a leer ni distinguir de otro, y
// escribirlo igualmente solo dejaría un dato personal inalcanzable en la base.
var ErrBuyerFieldEmpty = errors.New("intakes: campo del comprador sin clave")

// BuyerData es el checklist del comprador de UNA solicitud, en claro. Vive SOLO en
// memoria, en el borde de la app, entre el descifrado y el cifrado de la fila.
// Nunca se devuelve a un llamante ni se serializa fuera del blob cifrado.
type BuyerData map[string]string

// PostgresBuyerData escribe public.intake_buyer_data con envelope encryption.
//
// Es un tipo aparte de Postgres (el store del resto del dominio) y no un par de
// métodos suyos, por una razón de contención: este es el único componente del
// paquete que necesita material criptográfico, y tenerlo separado hace que quien
// construya el store normal NO pueda cifrar ni descifrar nada por accidente. El
// cipher se inyecta —igual que en contact.PostgresResolver— y sin él este tipo no
// se puede construir.
type PostgresBuyerData struct {
	db     *sql.DB
	cipher *crypto.FieldCipher
}

// NewPostgresBuyerData construye el escritor sobre el pool y el cipher dados. El
// cipher es OBLIGATORIO: un escritor sin él guardaría datos personales en claro,
// que es exactamente lo que esta tabla existe para impedir.
func NewPostgresBuyerData(db *sql.DB, cipher *crypto.FieldCipher) *PostgresBuyerData {
	return &PostgresBuyerData{db: db, cipher: cipher}
}

// PutBuyerField fusiona UN campo del checklist en la fila cifrada de la solicitud,
// creándola si no existía. Es la única escritura de datos personales del ciclo de
// la solicitud.
//
// POR QUÉ FUSIONA Y NO SUSTITUYE. El checklist se rellena campo a campo, un mensaje
// de WhatsApp por campo: cada llamada solo conoce el suyo. Sustituir la fila
// borraría los anteriores. Y por qué la fusión va en UNA transacción con SELECT …
// FOR UPDATE en vez de un ON CONFLICT: para mezclar hay que descifrar lo que ya
// había, y entre leer y escribir no puede colarse otra escritura de la misma
// solicitud — perdería un campo en silencio, que es el peor final posible para un
// dato que el cliente ya tecleó.
//
// Se re-cifra la fila ENTERA en cada campo (DEK nueva, nonce nuevo, KEK current).
// Es más trabajo que actualizar una clave suelta y es deliberado: un blob por fila
// no filtra ni cuántos campos hay ni cuánto mide cada uno, y el coste —dos o tres
// campos por pedido— no se nota en ningún sitio.
//
// NO filtra por tenant, igual que itemsOf: el llamante (el proyector del carrito)
// obtuvo el intakeID resolviendo la solicitud ABIERTA de (tenant, contacto), y la
// FK garantiza que la fila es de esa solicitud. Ningún camino recibe un intakeID
// de fuera.
//
// El valor NO aparece en ningún error de esta función. Un error que lo incluyera
// acabaría en el log del dispatcher, que es justo donde no puede estar.
func (p *PostgresBuyerData) PutBuyerField(ctx context.Context, intakeID, key, value string) (err error) {
	if key == "" {
		return ErrBuyerFieldEmpty
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("intakes: abrir transacción de datos del comprador: %w", err)
	}
	defer func() {
		if err != nil {
			if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
				err = errors.Join(err, fmt.Errorf("intakes: rollback de datos del comprador: %w", rerr))
			}
		}
	}()

	data, existed, err := p.currentBuyerData(ctx, tx, intakeID)
	if err != nil {
		return err
	}
	data[key] = value

	blob, err := json.Marshal(data)
	if err != nil {
		// El error de json.Marshal NO se envuelve con %w ni se propaga tal cual: su
		// mensaje puede citar el valor que no supo serializar. Un map[string]string
		// no puede fallar aquí, pero la regla se sostiene igual.
		return fmt.Errorf("intakes: serializando los datos del comprador de la solicitud %s", intakeID)
	}
	enc, dek, kekID, err := p.cipher.Encrypt(string(blob))
	if err != nil {
		return fmt.Errorf("intakes: cifrando los datos del comprador: %w", err)
	}

	if existed {
		_, err = tx.ExecContext(ctx, `
			UPDATE public.intake_buyer_data
			SET data_enc = $2, data_dek = $3, data_kek_id = $4, updated_at = now()
			WHERE intake_id = $1
		`, intakeID, enc, dek, kekID)
	} else {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO public.intake_buyer_data (intake_id, data_enc, data_dek, data_kek_id)
			VALUES ($1, $2, $3, $4)
		`, intakeID, enc, dek, kekID)
	}
	if err != nil {
		return fmt.Errorf("intakes: guardando los datos del comprador de la solicitud %s: %w", intakeID, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("intakes: confirmando los datos del comprador: %w", err)
	}
	return nil
}

// currentBuyerData lee y DESCIFRA la fila dentro de la transacción, bloqueándola
// (FOR UPDATE) para que la fusión sea atómica. Sin fila devuelve un mapa vacío y
// existed=false.
//
// Un blob que no se puede descifrar o que no es un JSON de objeto devuelve ERROR y
// no un mapa vacío: seguir adelante sobrescribiría con un solo campo lo que fuera
// que hubiera ahí, y "no lo entiendo" nunca puede resolverse borrando.
func (p *PostgresBuyerData) currentBuyerData(ctx context.Context, tx *sql.Tx, intakeID string) (BuyerData, bool, error) {
	var (
		enc, dek []byte
		kekID    string
	)
	err := tx.QueryRowContext(ctx, `
		SELECT data_enc, data_dek, data_kek_id
		FROM public.intake_buyer_data
		WHERE intake_id = $1
		FOR UPDATE
	`, intakeID).Scan(&enc, &dek, &kekID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return BuyerData{}, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("intakes: leer los datos del comprador de la solicitud %s: %w", intakeID, err)
	}

	// Se descifra con la KEK que envolvió ESTA fila (data_kek_id), no con la
	// current: tras una rotación parcial coexisten filas de varias KEK (Plan 012).
	plain, err := p.cipher.Decrypt(enc, dek, kekID)
	if err != nil {
		return nil, false, fmt.Errorf("intakes: descifrando los datos del comprador de la solicitud %s: %w", intakeID, err)
	}
	data := BuyerData{}
	if err := json.Unmarshal([]byte(plain), &data); err != nil {
		// El error de unmarshal se DESCARTA (no se envuelve): su mensaje cita el
		// fragmento que no supo leer, y ese fragmento es el dato en claro.
		return nil, false, fmt.Errorf("intakes: los datos del comprador de la solicitud %s no son un objeto JSON", intakeID)
	}
	return data, true, nil
}
