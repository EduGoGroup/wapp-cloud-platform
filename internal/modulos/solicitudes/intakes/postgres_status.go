// Porta internal/intakes/postgres.go @ 64c181a

// postgres_status.go es la TRANSICIÓN de estado del puerto Store: un
// compare-and-swap y los dos efectos que cuelgan de él, todo en una transacción.
//
// Lleva además los auxiliares de transacción que UpdateStatus comparte con otros
// ficheros del adaptador: casStatusTx y recomputeTotalTx, y —porque UpdateStatus no
// compila sin ellos y este fichero se pasó a verde antes que postgres_shipping.go—
// ensureShippingTx y shippingZonesOf, que son la materialización de la línea de envío
// que describe la cabecera de postgres_shipping.go.

package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// UpdateStatus implementa Store.UpdateStatus: mueve la solicitud a `to` si y solo si
// su estado ALMACENADO es uno de `expected`, y devuelve la cabecera ya movida.
//
// Es un COMPARE-AND-SWAP: la escritura solo ocurre si el estado almacenado sigue
// siendo uno de los esperados. Sin esa condición, dos operadores simultáneos podrían
// encadenar dos transiciones que por separado eran válidas y juntas saltan un paso
// del ciclo.
//
// Va en TRANSACCIÓN porque entrar a `pending_approval` arrastra la línea de envío
// (D-041.11) y las dos cosas son un solo hecho: o la solicitud queda por aprobar CON
// su envío, o no queda por aprobar. El CAS toma el candado de la fila, así que el paso
// del envío no necesita bloquearla otra vez. Entrar a `deposit_requested` arrastra lo
// suyo por el MISMO motivo (D-041.12, T4.4): «toda solicitud con la seña pedida tiene
// su plazo» es un invariante del estado, no un efecto que se dispara después — y el
// texto que sale hacia el cliente lleva esa fecha dentro ({fecha_limite}), así que
// fijarla en un segundo paso dejaría un hueco en el que el aviso se manda sin fecha.
//
// `to` y `expected` llegan tal cual a la base: es el llamante quien expande
// `expected` a las variantes almacenadas. Un intakeID que no es un UUID ⇒ ErrNotFound
// sin tocar la base.
//
// Todo va en UNA transacción (postgres.WithTx, que reintenta la transacción entera
// ante un deadlock o un fallo de serialización). Dentro:
//
//  1. EL CAS: un UPDATE condicionado al tenant, al id y a `status = ANY(expected)`.
//     Si no toca ninguna fila se RELEE para distinguir dos silencios que no son el
//     mismo: la solicitud no existe en ese tenant ⇒ ErrNotFound; existe y otro
//     operador se adelantó ⇒ ErrConflict. Los dos vuelven sin envolver y la
//     transacción se revierte.
//  2. Si `to` normalizado es StatusDepositRequested: se fija la FECHA LÍMITE DE LA
//     SEÑA en la misma transacción (T4.4). Se lee deposit_due_days de
//     tenant_settings —sin fila, o con un valor que no es positivo,
//     DefaultDepositDueDays— y se pone deposit_due_at = now() + días, se LIMPIA
//     deposit_reminded_at (un plazo nuevo merece su recordatorio) y se devuelve la
//     cabecera con la fecha puesta. El reloj es el de la base.
//  3. Si `to` normalizado es StatusPendingApproval: se MATERIALIZA la línea de envío
//     con la política ShippingAlways (ver EnsureShippingLine) y, solo si esa línea
//     cambió algo, se recalcula el total y se devuelve la cabecera con él.
//  4. Cualquier otro destino: nada más; se devuelve la cabecera del CAS.
//
// Si algo falla después del CAS, la transacción entera se revierte: la solicitud NO
// queda movida a medias.
//
// Errores de la base, envueltos con %w y devolviendo Intake{}:
//
//   - "intakes: leer solicitud: " — la fila que devuelve el CAS (o el UPDATE de la
//     seña, o el del total) no se puede leer;
//   - "intakes: verificar solicitud: " — falla la relectura del CAS;
//   - "intakes: leer el plazo de la seña del tenant: " — falla la lectura del plazo;
//   - los de la línea de envío, que documenta postgres_shipping.go;
//   - los de postgres.WithTx al abrir o confirmar la transacción.
func (p *Postgres) UpdateStatus(ctx context.Context, tenantID, intakeID, to string, expected []string) (Intake, error) {
	if _, err := uuid.Parse(intakeID); err != nil {
		return Intake{}, ErrNotFound
	}

	// Se declara FUERA de la clausura porque WithTx puede REEJECUTARLA ante un
	// deadlock: cada intento la reasigna y vale la del intento que confirmó.
	var out Intake
	err := postgres.WithTx(ctx, p.db, func(tx *sql.Tx) error {
		updated, err := casStatusTx(ctx, tx, tenantID, intakeID, to, expected)
		if err != nil {
			return err
		}
		out = updated
		switch NormalizeStatus(to) {
		case StatusDepositRequested:
			out, err = setDepositDueTx(ctx, tx, tenantID, intakeID)
			return err
		case StatusPendingApproval:
			changed, err := ensureShippingTx(ctx, tx, tenantID, intakeID, ShippingAlways)
			if err != nil || !changed {
				return err
			}
			out, err = recomputeTotalTx(ctx, tx, tenantID, intakeID)
			return err
		default:
			return nil
		}
	})
	if err != nil {
		return Intake{}, err
	}
	return out, nil
}

// setDepositDueTx fija la fecha límite de la seña de la solicitud que ACABA de
// entrar en `deposit_requested`. Corre bajo el candado que ya tomó el CAS.
//
// La base del cálculo es el now() de la TRANSACCIÓN, no un instante que venga de la
// aplicación: la fecha se compara después contra otros tiempos de esta misma base y
// una plataforma con varios procesos no tiene un reloj común. (El reloj INYECTABLE
// de T4.4 es el del recordatorio, que es quien tiene que poder viajar en el tiempo
// para probarse; el plazo se fija una vez y no se re-evalúa.)
//
// Limpia deposit_reminded_at para que el estado quede COHERENTE consigo mismo: si
// algún día se pudiera volver a pedir seña sobre la misma solicitud, una marca vieja
// dejaría al cliente sin recordatorio del plazo nuevo, en silencio. Hoy no hay
// camino de vuelta a `deposit_requested` (status.go), así que esto no cambia nada;
// mañana evita un fallo mudo.
func setDepositDueTx(ctx context.Context, tx *sql.Tx, tenantID, intakeID string) (Intake, error) {
	days, err := depositDueDaysTx(ctx, tx, tenantID)
	if err != nil {
		return Intake{}, err
	}
	return scanIntake(tx.QueryRowContext(ctx, `
		UPDATE public.intakes
		SET deposit_due_at = now() + make_interval(days => $3::int),
		    deposit_reminded_at = NULL,
		    updated_at = now()
		WHERE tenant_id = $1 AND id = $2
		RETURNING `+intakeCols,
		tenantID, intakeID, days))
}

// depositDueDaysTx lee el plazo de la seña del tenant. Un tenant SIN fila de config
// no es un error (mismo criterio que shippingZonesOf y NotifySettings): es el plazo
// por defecto. La regla de "valor no positivo ⇒ default" se aplica con el MISMO
// helper que usa el marcador {plazo} del texto, para no prometer un plazo y fijar
// otro.
func depositDueDaysTx(ctx context.Context, tx *sql.Tx, tenantID string) (int, error) {
	var days int
	err := tx.QueryRowContext(ctx,
		`SELECT deposit_due_days FROM public.tenant_settings WHERE tenant_id = $1`,
		tenantID).Scan(&days)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return DefaultDepositDueDays, nil
	case err != nil:
		return 0, fmt.Errorf("intakes: leer el plazo de la seña del tenant: %w", err)
	}
	return depositDueDays(days), nil
}

// casStatusTx ejecuta el compare-and-swap del estado dentro de la transacción y
// distingue "no es del tenant" (ErrNotFound) de "otro operador se adelantó"
// (ErrConflict) con una relectura: sin ella, las dos serían el mismo silencio.
func casStatusTx(ctx context.Context, tx *sql.Tx, tenantID, intakeID, to string, expected []string) (Intake, error) {
	updated, err := scanIntake(tx.QueryRowContext(ctx, `
		UPDATE public.intakes
		SET status = $3, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND status = ANY($4)
		RETURNING `+intakeCols,
		tenantID, intakeID, to, expected))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var exists bool
		if qerr := tx.QueryRowContext(ctx,
			`SELECT true FROM public.intakes WHERE tenant_id = $1 AND id = $2`,
			tenantID, intakeID).Scan(&exists); qerr != nil {
			if errors.Is(qerr, sql.ErrNoRows) {
				return Intake{}, ErrNotFound
			}
			return Intake{}, fmt.Errorf("intakes: verificar solicitud: %w", qerr)
		}
		return Intake{}, ErrConflict
	case err != nil:
		return Intake{}, err
	}
	return updated, nil
}

// ensureShippingTx deja EXACTAMENTE una línea de envío en la solicitud y dice si
// escribió algo. No recalcula el total: eso lo hace el llamante, que es quien sabe
// si necesita la cabecera de vuelta.
//
// El orden importa: primero la política —si no aplica no se toca nada y no se lee
// una línea que no va a cambiar— y solo después la fila.
func ensureShippingTx(ctx context.Context, tx *sql.Tx, tenantID, intakeID string, policy ShippingPolicy) (bool, error) {
	zones, err := shippingZonesOf(ctx, tx, tenantID)
	if err != nil {
		return false, err
	}
	if !policy.applies(zones) {
		return false, nil
	}
	desired := DesiredShippingLine(zones)

	var (
		rowID  int64
		stored Item
	)
	err = tx.QueryRowContext(ctx, `
		SELECT id, label, qty, unit_price
		FROM public.intake_items
		WHERE intake_id = $1 AND sku = $2
	`, intakeID, ShippingSKU).Scan(&rowID, &stored.Label, &stored.Qty, &stored.UnitPrice)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		it := desired.item()
		if _, ierr := tx.ExecContext(ctx, `
			INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price)
			VALUES ($1, $2, $3, '', $4, $5)
		`, intakeID, it.SKU, it.Label, it.Qty, it.UnitPrice); ierr != nil {
			return false, fmt.Errorf("intakes: insertar la línea de envío: %w", ierr)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("intakes: leer la línea de envío: %w", err)
	}

	if !desired.Supersedes(stored) {
		return false, nil
	}
	// Se ACTUALIZA la fila en vez de borrarla e insertar otra: así la línea conserva
	// su added_at y su sitio en el pedido, y en ningún instante hay dos envíos.
	it := desired.item()
	if _, uerr := tx.ExecContext(ctx, `
		UPDATE public.intake_items SET label = $2, qty = $3, unit_price = $4 WHERE id = $1
	`, rowID, it.Label, it.Qty, it.UnitPrice); uerr != nil {
		return false, fmt.Errorf("intakes: actualizar la línea de envío: %w", uerr)
	}
	return true, nil
}

// shippingZonesOf lee tenant_settings.shipping_zones. Un tenant SIN fila de config
// no es un error: es un tenant que no configuró nada (mismo criterio que
// GetTenantSettings del módulo de flujos) y por tanto no tiene zonas. Era
// shippingZonesDe en el viejo.
//
// 🔴 TOMA UN `querier` Y NO UN `*sql.Tx` PARA QUE HAYA UNA SOLA SENTENCIA. Sus dos
// llamantes leen la misma columna con propósitos distintos —EnsureShippingLine
// dentro del CAS del carrito numérico, ShippingZones fuera de toda transacción para
// el pipeline de captación— y con dos copias del SELECT bastaría con que alguien
// añadiera un filtro a una para que el borrador y el pedido cerrado cotizaran envíos
// distintos sin que nada diera error.
func shippingZonesOf(ctx context.Context, q querier, tenantID string) ([]ShippingZone, error) {
	var raw []byte
	err := q.QueryRowContext(ctx,
		`SELECT shipping_zones FROM public.tenant_settings WHERE tenant_id = $1`,
		tenantID).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("intakes: leer las zonas de envío del tenant: %w", err)
	}
	return ParseShippingZones(raw)
}

// recomputeTotalTx recalcula el total de la cabecera como la SUMA de sus líneas y
// devuelve la solicitud ya coherente. Se recalcula entero en vez de sumarle el
// envío: sumar deja el total dependiendo de cuántas veces se llamó, y el criterio
// de esta tarea es justamente que llamar N veces dé lo mismo que llamar una.
func recomputeTotalTx(ctx context.Context, tx *sql.Tx, tenantID, intakeID string) (Intake, error) {
	return scanIntake(tx.QueryRowContext(ctx, `
		UPDATE public.intakes i
		SET total = COALESCE((
			    SELECT SUM(it.qty * it.unit_price)
			    FROM public.intake_items it
			    WHERE it.intake_id = i.id), 0),
		    updated_at = now()
		WHERE i.tenant_id = $1 AND i.id = $2
		RETURNING `+intakeCols,
		tenantID, intakeID))
}
