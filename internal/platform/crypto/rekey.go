package crypto

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

// DefaultRekeyBatch es el tamaño de batch por defecto de la rotación cuando el
// llamador no especifica uno (<= 0). Acota memoria y duración de la transacción.
const DefaultRekeyBatch = 500

// Report resume una pasada de rotación (re-wrap incremental) para auditoría
// (§10.H). NO contiene contenido sensible (número/value/JID), solo contadores y
// key_ids del keyring.
type Report struct {
	// Processed es el total de filas re-envueltas a la KEK current en esta pasada.
	Processed int
	// CurrentKeyID es el key_id de la KEK current hacia la que se re-envolvió.
	CurrentKeyID string
	// PendingByKeyID cuenta, al terminar la pasada, cuántas filas siguen envueltas
	// por cada key_id != current (0 filas de un key_id ⇒ esa KEK ya no se referencia
	// y es retirable del keyring, §10.F). Es un mapa vacío (no nil) si no hay
	// pendientes: la rotación quedó completa.
	PendingByKeyID map[string]int
}

// Rekey re-envuelve por batch todas las value_dek de public.contacts que aún no
// están envueltas por la KEK current, SIN re-cifrar el dato (§7). Para cada fila:
// UnwrapDEK(value_dek, value_kek_id) → WrapDEK con la current → UPDATE de
// value_dek + value_kek_id + updated_at, dejando value_enc/value_bidx/contact_id/PK
// INTACTOS. El value NUNCA se descifra (solo la DEK, en memoria).
//
// Es REANUDABLE e IDEMPOTENTE (§10.D): el criterio de selección es el propio
// estado (value_kek_id <> current), así que reejecutar tras una interrupción
// retoma donde quedó y reejecutar tras terminar es un no-op (0 filas). Cada batch
// es ATÓMICO (una transacción con FOR UPDATE SKIP LOCKED + UPDATE por PK): un
// fallo de ReWrap aborta el batch sin dejar filas a medias y sin corromper
// (fail-safe §10.J). SKIP LOCKED permite ejecutar la rotación de forma segura en
// paralelo con lecturas y con otra instancia de Rekey.
//
// Al terminar, Report.PendingByKeyID indica cuántas filas siguen en cada KEK vieja
// (para decidir el retiro seguro a 0 referencias, §10.F). Los logs NO llevan
// contenido (§10.H).
func Rekey(ctx context.Context, db *sql.DB, cipher *FieldCipher, kp KeyProvider, batch int) (Report, error) {
	if batch <= 0 {
		batch = DefaultRekeyBatch
	}
	current := kp.CurrentKeyID()
	report := Report{CurrentKeyID: current, PendingByKeyID: map[string]int{}}

	for {
		n, err := rekeyBatch(ctx, db, cipher, current, batch)
		if err != nil {
			return report, err
		}
		if n == 0 {
			break
		}
		report.Processed += n
		// Auditoría SIN contenido (§10.H): solo contadores y el key_id current.
		log.Printf("[wapp][crypto][INFO] rekey batch: processed=%d current=%q", report.Processed, current)
	}

	pending, err := PendingByKeyID(ctx, db, current)
	if err != nil {
		return report, err
	}
	report.PendingByKeyID = pending
	return report, nil
}

// pendingRow es una fila pendiente de re-wrap. bidx es el índice ciego (hex del
// HMAC), parte de la PK: identificador, NO contenido (§10.H). Nunca se lee ni se
// toca value_enc: la rotación no descifra el value.
type pendingRow struct {
	tenantID string
	kind     string
	bidx     string
	dek      []byte
	oldKeyID string
}

// rekeyBatch procesa un batch en UNA transacción: toma hasta batch filas con
// value_kek_id <> current (bloqueándolas con FOR UPDATE SKIP LOCKED), re-envuelve
// su DEK a la current y las actualiza por PK. Devuelve cuántas filas procesó (0
// cuando ya no quedan pendientes o todas las restantes están bloqueadas por otro
// worker). Todo o nada: cualquier error hace rollback (defer) sin commit.
func rekeyBatch(ctx context.Context, db *sql.DB, cipher *FieldCipher, current string, batch int) (n int, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("rekey: iniciar transacción: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rerr := tx.Rollback(); rerr != nil {
				log.Printf("[wapp][crypto][WARN] rekey: rollback del batch: %v", rerr)
			}
		}
	}()

	batchRows, err := selectPending(ctx, tx, current, batch)
	if err != nil {
		return 0, err
	}
	if len(batchRows) == 0 {
		return 0, nil
	}

	for _, r := range batchRows {
		newDEK, newID, rwErr := cipher.ReWrap(r.dek, r.oldKeyID)
		if rwErr != nil {
			// Fail-safe §10.J/§10.H: reporta key_id + PK (tenant/kind/bidx), NUNCA el
			// value/número/JID. El rollback (defer) deja el batch intacto.
			return 0, fmt.Errorf("rekey: ReWrap de (tenant=%s kind=%s value_bidx=%s) con key_id %q: %w",
				r.tenantID, r.kind, r.bidx, r.oldKeyID, rwErr)
		}
		if _, uerr := tx.ExecContext(ctx, `
			UPDATE public.contacts
			SET value_dek = $1, value_kek_id = $2, updated_at = now()
			WHERE tenant_id = $3 AND kind = $4 AND value_bidx = $5
		`, newDEK, newID, r.tenantID, r.kind, r.bidx); uerr != nil {
			return 0, fmt.Errorf("rekey: actualizar fila (tenant=%s kind=%s value_bidx=%s): %w",
				r.tenantID, r.kind, r.bidx, uerr)
		}
	}

	if cerr := tx.Commit(); cerr != nil {
		return 0, fmt.Errorf("rekey: confirmar batch: %w", cerr)
	}
	committed = true
	return len(batchRows), nil
}

// selectPending toma y bloquea (FOR UPDATE SKIP LOCKED) hasta batch filas cuyo
// value_kek_id != current, dentro de la transacción tx. El cursor se cierra al
// volver: en una misma tx no puede haber un rows abierto mientras se emiten los
// UPDATE. Los locks se mantienen hasta el commit/rollback, no hasta el Close.
func selectPending(ctx context.Context, tx *sql.Tx, current string, batch int) (rowsOut []pendingRow, err error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT tenant_id::text, kind, value_bidx, value_dek, value_kek_id
		FROM public.contacts
		WHERE value_kek_id <> $1
		ORDER BY tenant_id, kind, value_bidx
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`, current, batch)
	if err != nil {
		return nil, fmt.Errorf("rekey: seleccionar batch: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("rekey: cerrar filas: %w", cerr)
			rowsOut = nil
		}
	}()

	for rows.Next() {
		var r pendingRow
		if serr := rows.Scan(&r.tenantID, &r.kind, &r.bidx, &r.dek, &r.oldKeyID); serr != nil {
			return nil, fmt.Errorf("rekey: escanear fila: %w", serr)
		}
		rowsOut = append(rowsOut, r)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("rekey: iterar filas: %w", rerr)
	}
	return rowsOut, nil
}

// PendingByKeyID devuelve, por key_id != currentKeyID, cuántas filas de
// public.contacts siguen envueltas por esa KEK. Un key_id con 0 filas (ausente del
// mapa) ya no se referencia y su KEK es retirable del keyring (§10.F); mientras
// tenga > 0 pendientes NO debe retirarse (retirarla haría fallar claro las
// lecturas de esas filas, §10.J — nunca corrupción, pero sí indisponibilidad).
// Devuelve un mapa vacío (no nil) cuando la rotación está completa.
func PendingByKeyID(ctx context.Context, db *sql.DB, currentKeyID string) (pending map[string]int, err error) {
	rows, err := db.QueryContext(ctx, `
		SELECT value_kek_id, COUNT(*)
		FROM public.contacts
		WHERE value_kek_id <> $1
		GROUP BY value_kek_id
	`, currentKeyID)
	if err != nil {
		return nil, fmt.Errorf("rekey: consultar pendientes por key_id: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("rekey: cerrar pendientes: %w", cerr)
			pending = nil
		}
	}()

	pending = make(map[string]int)
	for rows.Next() {
		var (
			keyID string
			count int
		)
		if serr := rows.Scan(&keyID, &count); serr != nil {
			return nil, fmt.Errorf("rekey: escanear pendientes: %w", serr)
		}
		pending[keyID] = count
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("rekey: iterar pendientes: %w", rerr)
	}
	return pending, nil
}
