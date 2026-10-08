// Porta internal/intakes/postgres.go @ 64c181a

// postgres_status.go es la TRANSICIÓN de estado del puerto Store: un
// compare-and-swap y los dos efectos que cuelgan de él, todo en una transacción.

package intakes

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// UpdateStatus implementa Store.UpdateStatus: mueve la solicitud a `to` si y solo si
// su estado ALMACENADO es uno de `expected`, y devuelve la cabecera ya movida.
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
	panic(pendiente.Implementar("intakes.Postgres.UpdateStatus"))
}
