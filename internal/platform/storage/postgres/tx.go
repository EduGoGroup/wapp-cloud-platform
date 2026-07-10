package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLSTATE de fallos TRANSITORIOS que justifican reintentar la transacción
// completa: Postgres aborta una de las transacciones en conflicto y la
// reejecución converge (los upserts del motor son idempotentes por ON CONFLICT).
const (
	sqlstateDeadlock      = "40P01" // deadlock_detected
	sqlstateSerialization = "40001" // serialization_failure
)

// maxTxAttempts acota los reintentos de WithTx ante un fallo transitorio. 8
// intentos con backoff exponencial acotado convergen de sobra en la práctica;
// agotarlos devuelve el último error envuelto (no se cuelga indefinidamente).
const maxTxAttempts = 8

// IsSerializationFailure reporta si err (o alguno envuelto) es un deadlock (40P01)
// o un fallo de serialización (40001) de Postgres. El driver pgx expone el código
// vía *pgconn.PgError; los repos envuelven con %w, así que se recupera con
// errors.As.
func IsSerializationFailure(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == sqlstateDeadlock || pg.Code == sqlstateSerialization)
}

// WithTx ejecuta fn dentro de UNA transacción y REINTENTA la transacción completa
// ante un deadlock/serialización (40P01/40001), con backoff exponencial acotado +
// jitter que respeta la cancelación del ctx. Es el helper ÚNICO y reutilizable que
// cierra H8 (antes el retry vivía aislado en contact/): lo adoptan contact.Resolve
// (fusión de contactos) y store.CloseOrder (cierre atómico de orden+líneas).
//
// Seguridad ante panic (cierra H7): el rollback se dispara con un flag
// `committed bool` en un defer, NO condicionado a `err != nil`. Si fn hace panic,
// el defer revierte la transacción y el panic se PROPAGA (no se reintenta ni se
// traga). El patrón `if err != nil { rollback }` dejaba la tx SIN revertir en un
// panic (el named return seguía nil); este es el patrón inmune de rekey.go, ahora
// único.
//
// Contrato de fn: NO debe hacer Commit/Rollback (los gestiona WithTx) y debe ser
// IDEMPOTENTE, pues puede reejecutarse ante un reintento.
func WithTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	var lastErr error
	for attempt := 0; attempt < maxTxAttempts; attempt++ {
		err := runOnce(ctx, db, fn)
		if err == nil {
			return nil
		}
		if !IsSerializationFailure(err) {
			return err
		}
		lastErr = err
		if werr := backoffBeforeRetry(ctx, attempt); werr != nil {
			return werr
		}
	}
	return fmt.Errorf("postgres: transacción tras %d intentos (último deadlock/serialización): %w", maxTxAttempts, lastErr)
}

// runOnce ejecuta UNA transacción con rollback inmune a panic (flag committed).
func runOnce(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: iniciar transacción: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// Se ejecuta también durante el unwinding de un panic (rollback garantizado).
		// sql.ErrTxDone significa que la tx ya se cerró (commit/rollback): no es un
		// error real que reportar.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, rerr)
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("postgres: confirmar transacción: %w", err)
	}
	committed = true
	return nil
}

// backoffBeforeRetry espera antes del reintento: exponencial (1ms<<attempt)
// acotado a 50ms + jitter 0-4ms del reloj (de-correlaciona a dos perdedores que
// reintentarían en lockstep; no exige un RNG criptográfico). Respeta la
// cancelación del ctx.
func backoffBeforeRetry(ctx context.Context, attempt int) error {
	d := time.Duration(1<<attempt) * time.Millisecond
	if d > 50*time.Millisecond {
		d = 50 * time.Millisecond
	}
	jitter := time.Duration(time.Now().UnixNano()%5) * time.Millisecond
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d + jitter):
		return nil
	}
}
