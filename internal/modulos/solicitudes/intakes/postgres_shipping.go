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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
	panic(pendiente.Implementar("intakes.Postgres.EnsureShippingLine"))
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
	panic(pendiente.Implementar("intakes.Postgres.ShippingZones"))
}
