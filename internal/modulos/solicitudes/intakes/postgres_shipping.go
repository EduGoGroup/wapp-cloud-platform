// Porta internal/intakes/postgres.go @ 64c181a

// postgres_shipping.go es la materialización de la LÍNEA DE ENVÍO (D-041.11) y la
// lectura de las zonas de envío del tenant. La misma materialización la usa
// UpdateStatus al entrar en StatusPendingApproval, dentro de su propia transacción.
//
// # La línea de envío (lo que el verde porta a este fichero)
//
// Dada una solicitud ya bloqueada y una política, deja EXACTAMENTE una línea con el
// SKU reservado ShippingSKU y dice si escribió algo. El orden importa: primero la
// política, y solo después la fila.
//
//  1. Se leen las zonas del tenant (tenant_settings.shipping_zones). Sin fila de
//     config no hay zonas.
//  2. Si la política no aplica con esas zonas (ShippingOnlyIfZones sin zonas), no se
//     toca nada ni se lee la línea.
//  3. Se calcula la línea deseada (DesiredShippingLine) y se lee la almacenada:
//     no hay ⇒ se INSERTA (customization vacía); hay y la deseada la supera
//     (ShippingLine.Supersedes) ⇒ se ACTUALIZAN label, qty y unit_price por el id de
//     la fila; hay y no la supera ⇒ no se escribe (un precio que el dueño ya puso a
//     mano no se pisa).
//
// No recalcula el total: eso lo hace el llamante, y solo si la línea cambió.
//
// Errores, envueltos con %w:
//
//   - "intakes: leer las zonas de envío del tenant: " — falla la lectura de las zonas;
//   - el error de ParseShippingZones tal cual, si el JSON guardado no es válido;
//   - "intakes: leer la línea de envío: " — falla la lectura de la línea almacenada;
//   - "intakes: insertar la línea de envío: " — falla el INSERT;
//   - "intakes: actualizar la línea de envío: " — falla el UPDATE.

package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// EnsureShippingLine implementa Store.EnsureShippingLine: deja la línea de envío
// puesta y el total cuadrado, en una transacción propia (postgres.WithTx).
//
// Un intakeID que no es un UUID ⇒ ErrNotFound sin tocar la base. Dentro de la
// transacción:
//
//  1. BLOQUEA la cabecera (SELECT … FOR UPDATE acotado por tenant): es el mismo
//     punto de serialización que toma el CAS de UpdateStatus. Dos llamadas
//     simultáneas sobre la misma solicitud se serializan; la segunda ve la línea de
//     la primera y no escribe otra. Sin fila ⇒ ErrNotFound, sin envolver.
//  2. Materializa la línea con la política dada (ver la cabecera de este fichero).
//  3. SOLO si la línea cambió algo, recalcula el total ENTERO desde las líneas
//     (nunca sumando o restando) y refresca updated_at. Si no cambió nada, no hay
//     ninguna escritura y updated_at no se mueve: es idempotente.
//
// Un fallo en cualquier paso revierte la transacción entera.
//
// Errores de la base, envueltos con %w:
//
//   - "intakes: bloquear la solicitud: " — falla el bloqueo de la cabecera;
//   - los de la línea de envío (cabecera de este fichero);
//   - "intakes: leer solicitud: " — la fila del total recalculado no se puede leer;
//   - los de postgres.WithTx al abrir o confirmar la transacción.
func (p *Postgres) EnsureShippingLine(ctx context.Context, tenantID, intakeID string, policy ShippingPolicy) error {
	if _, err := uuid.Parse(intakeID); err != nil {
		return ErrNotFound
	}
	return postgres.WithTx(ctx, p.db, func(tx *sql.Tx) error {
		var exists bool
		err := tx.QueryRowContext(ctx,
			`SELECT true FROM public.intakes WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
			tenantID, intakeID).Scan(&exists)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("intakes: bloquear la solicitud: %w", err)
		}

		changed, err := ensureShippingTx(ctx, tx, tenantID, intakeID, policy)
		if err != nil || !changed {
			return err
		}
		_, err = recomputeTotalTx(ctx, tx, tenantID, intakeID)
		return err
	})
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

// ShippingZones devuelve las zonas de envío que el tenant tiene configuradas en
// public.tenant_settings (columna shipping_zones, JSON), ya interpretadas por
// ParseShippingZones. Es UNA lectura suelta, la misma que usa la materialización de
// la línea: quien pinta el presupuesto y quien escribe la línea no pueden discrepar
// sobre qué zonas hay.
//
//   - tenant SIN fila de config ⇒ (nil, nil): no tener zonas no es un error;
//   - JSON guardado que ParseShippingZones rechaza ⇒ su error tal cual;
//   - fallo de la base ⇒ (nil, err) con "intakes: leer las zonas de envío del tenant: ".
func (p *Postgres) ShippingZones(ctx context.Context, tenantID string) ([]ShippingZone, error) {
	return shippingZonesOf(ctx, p.db, tenantID)
}
