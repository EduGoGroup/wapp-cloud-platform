// Porta internal/intakes/buyerdata.go @ 64c181a

// buyerdata_postgres.go es el adaptador que guarda los datos del comprador
// CIFRADOS EN REPOSO en public.intake_buyer_data (D-F6-6: el SQL y el cifrado
// salen del fichero puro buyerdata.go).
//
// EL PATRÓN ES EL DE public.contacts (migraciones 0006/0007, Plan 011), copiado y
// no reinventado: un sobre de tres piezas —el dato cifrado con una llave fresca
// por fila (data_enc), esa llave envuelta por la KEK del keyring (data_dek) y el
// key_id de esa KEK (data_kek_id)—. Las tres columnas son necesarias: sin
// data_dek no hay con qué descifrar, y sin data_kek_id la fila queda FUERA de la
// rotación de KEK del Plan 012, porque nadie sabría con cuál desenvolverla.
//
// 🔴 HOMÓNIMO (T-1): esa llave por fila es la del SOBRE DE PII DE NEGOCIO que
// custodia esta pieza con su KEK. NO es la DEK del ADR-0007 (la del almacén de
// whatsmeow, que custodia el Edge y jamás cruza el contrato). Ver buyerdata.go.

package intakes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// PostgresBuyerData lee y escribe public.intake_buyer_data con el sobre por fila.
//
// R-11: los datos del comprador los escribe SOLO este tipo. Es un tipo aparte del
// store del resto del dominio y no un par de métodos suyos, por contención: es el
// único componente del paquete que necesita material criptográfico, y tenerlo
// separado hace que quien construya el store normal NO pueda cifrar ni descifrar
// nada por accidente.
//
// Las tres sentencias se portan BYTE A BYTE del paquete viejo —el test afirma el
// texto exacto de cada una contra constantes escritas aparte— y no se «mejoran»
// al portar. Que ese SQL haga en un Postgres de verdad lo que aquí se promete lo
// prueban los procesos de F9.
//
// El VALOR de un campo no aparece en NINGÚN error de este tipo: un error que lo
// incluyera acabaría en el log del dispatcher, que es justo donde no puede estar.
// Por eso los fallos de (de)serialización NO envuelven su causa —el mensaje de
// encoding/json cita el fragmento que no supo leer, y ese fragmento es el dato en
// claro—.
type PostgresBuyerData struct {
	db     *sql.DB
	cipher *crypto.FieldCipher
}

// NewPostgresBuyerData construye el adaptador sobre el pool y el cifrador de
// campo dados. El cifrador es OBLIGATORIO: un escritor sin él guardaría datos
// personales en claro, que es exactamente lo que esta tabla existe para impedir.
// Es el MISMO crypto.FieldCipher —un único keyring, una única rotación— que usan
// contactos, integraciones y tenantllm.
//
// No abre ni comprueba la conexión, ni cifra nada: construir no emite ninguna
// sentencia.
func NewPostgresBuyerData(db *sql.DB, cipher *crypto.FieldCipher) *PostgresBuyerData {
	return &PostgresBuyerData{db: db, cipher: cipher}
}

// PutBuyerField fusiona UN campo del checklist en la fila cifrada de la
// solicitud, creándola si no existía. Es la única escritura de datos personales
// del ciclo de la solicitud.
//
// Con key vacía devuelve ErrBuyerFieldEmpty SIN envolver y no toca la base: ni
// abre transacción ni cifra. Un VALOR vacío sí se guarda.
//
// POR QUÉ FUSIONA Y NO SUSTITUYE. El checklist se rellena campo a campo, un
// mensaje de WhatsApp por campo: cada llamada solo conoce el suyo. Sustituir la
// fila borraría los anteriores. Reescribir una clave que ya estaba corrige ESA y
// deja las demás en paz.
//
// POR QUÉ UNA TRANSACCIÓN CON `SELECT … FOR UPDATE` y no un ON CONFLICT: para
// mezclar hay que descifrar lo que ya había, y entre leer y escribir no puede
// colarse otra escritura de la misma solicitud —perdería un campo en silencio, el
// peor final posible para un dato que el cliente ya tecleó—. Todo va por la MISMA
// transacción (`*sql.Tx`), en este orden:
//
//  1. BEGIN.
//  2. SELECT data_enc, data_dek, data_kek_id … WHERE intake_id = $1 FOR UPDATE.
//     Sin fila, se parte de un checklist vacío. Con fila, se descifra con la KEK
//     que envolvió ESA fila (data_kek_id), no con la current: tras una rotación
//     parcial coexisten filas de varias KEK (Plan 012).
//  3. Se cifra el checklist fusionado ENTERO, como un objeto JSON, con llave de
//     fila nueva, nonce nuevo y la KEK current. Es más trabajo que actualizar una
//     clave suelta y es deliberado: un blob por fila no filtra ni cuántos campos
//     hay ni cuánto mide cada uno. SE CIFRA ANTES DE ESCRIBIR: a la base solo
//     llegan intake_id, data_enc, data_dek y data_kek_id; ni la clave ni el valor
//     viajan como argumento de ninguna sentencia.
//  4. Si la fila existía, UPDATE (… updated_at = now() WHERE intake_id = $1); si
//     no, INSERT. Argumentos, en orden: intake_id, data_enc, data_dek,
//     data_kek_id.
//  5. COMMIT.
//
// Cualquier fallo después del BEGIN revierte la transacción y NO escribe. En
// particular, un blob que no se puede descifrar o que no es un objeto JSON (un
// `null` tampoco lo es: el viejo entraba en pánico con él)
// devuelve ERROR y no un checklist vacío: seguir adelante sobrescribiría con un
// solo campo lo que fuera que hubiera ahí, y «no lo entiendo» nunca puede
// resolverse borrando.
//
// Errores, con su texto (los que llevan «: %w» envuelven la causa; <id> es el
// intakeID):
//
//	«intakes: abrir transacción de datos del comprador: %w»
//	«intakes: leer los datos del comprador de la solicitud <id>: %w»
//	«intakes: descifrando los datos del comprador de la solicitud <id>: %w»
//	«intakes: los datos del comprador de la solicitud <id> no son un objeto JSON»   (sin causa)
//	«intakes: serializando los datos del comprador de la solicitud <id>»            (sin causa)
//	«intakes: cifrando los datos del comprador: %w»
//	«intakes: guardando los datos del comprador de la solicitud <id>: %w»
//	«intakes: confirmando los datos del comprador: %w»
//
// Si además falla el ROLLBACK (y no es porque la transacción ya estuviera
// cerrada, sql.ErrTxDone), el error devuelto une los dos con errors.Join: el
// original y «intakes: rollback de datos del comprador: %w».
//
// NO filtra por tenant: el llamante (el proyector del carrito) obtuvo el intakeID
// resolviendo la solicitud ABIERTA de (tenant, contacto), y la FK garantiza que
// la fila es de esa solicitud. Ningún camino recibe un intakeID de fuera.
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

// GetBuyerData lee y DESCIFRA el checklist de UNA solicitud (Plan 042 · Ola 3 ·
// T3.3). Su consumidor es el worker del puente CRM, que la llama justo antes del
// POST para completar `buyer_data` (D-042.9: se descifra dentro del worker, nunca
// en línea con el mensaje, INV-02).
//
// Va FUERA de transacción y SIN `FOR UPDATE`: el worker solo LEE, nunca fusiona,
// y no hay carrera que cuidar. Es UNA consulta por el pool.
//
//   - Sin fila: (BuyerData vacío y NO nil, false, nil). El checklist quedó vacío.
//   - Con fila: (el checklist, true, nil), descifrado con la KEK que envolvió ESA
//     fila (data_kek_id), no con la current.
//   - Con error: (nil, false, err), con los mismos textos que PutBuyerField:
//     «intakes: leer los datos del comprador de la solicitud <id>: %w»,
//     «intakes: descifrando los datos del comprador de la solicitud <id>: %w» e
//     «intakes: los datos del comprador de la solicitud <id> no son un objeto
//     JSON» (este último sin causa, para no citar el dato en claro).
func (p *PostgresBuyerData) GetBuyerData(ctx context.Context, intakeID string) (BuyerData, bool, error) {
	var (
		enc, dek []byte
		kekID    string
	)
	err := p.db.QueryRowContext(ctx, `
		SELECT data_enc, data_dek, data_kek_id
		FROM public.intake_buyer_data
		WHERE intake_id = $1
	`, intakeID).Scan(&enc, &dek, &kekID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return BuyerData{}, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("intakes: leer los datos del comprador de la solicitud %s: %w", intakeID, err)
	}

	// Misma regla que currentBuyerData: se descifra con la KEK que envolvió ESTA
	// fila (data_kek_id), no con la current.
	plain, err := p.cipher.Decrypt(enc, dek, kekID)
	if err != nil {
		return nil, false, fmt.Errorf("intakes: descifrando los datos del comprador de la solicitud %s: %w", intakeID, err)
	}
	data := BuyerData{}
	// `|| data == nil`: misma guarda que currentBuyerData para el JSON `null`.
	if err := json.Unmarshal([]byte(plain), &data); err != nil || data == nil {
		return nil, false, fmt.Errorf("intakes: los datos del comprador de la solicitud %s no son un objeto JSON", intakeID)
	}
	return data, true, nil
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
	// `|| data == nil`: un JSON `null` NO da error de unmarshal y deja el mapa en nil.
	// El viejo lo dejaba pasar y PutBuyerField moría con un pánico al escribir en él,
	// con la transacción y su FOR UPDATE abiertos (hallazgos 18 y 32 de F6). `null`
	// no es un objeto: mismo error que cualquier otro JSON que no lo sea.
	if err := json.Unmarshal([]byte(plain), &data); err != nil || data == nil {
		// El error de unmarshal se DESCARTA (no se envuelve): su mensaje cita el
		// fragmento que no supo leer, y ese fragmento es el dato en claro.
		return nil, false, fmt.Errorf("intakes: los datos del comprador de la solicitud %s no son un objeto JSON", intakeID)
	}
	return data, true, nil
}
