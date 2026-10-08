// Porta internal/intakes/customernote.go @ 64c181a

// postgres_customernote.go es la lectura de la indicación del cliente de una
// solicitud. En el paquete viejo era lo único que llevaba customernote.go, que por
// eso no nace: el método nace con el adaptador (D-F6-6 ampliada).

package intakes

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// GetCustomerNote lee la indicación del cliente (customer_note) de UNA solicitud del
// tenant. Es una lectura suelta.
//
//   - la solicitud existe en ese tenant ⇒ (nota, true, nil); la nota puede ser "" y
//     sigue siendo found=true;
//   - no existe, no es de ese tenant, o intakeID ni siquiera es un UUID (este caso,
//     sin tocar la base) ⇒ ("", false, nil). Los tres son el mismo 404 opaco de Get
//     (INV-8): quien pregunta por una solicitud ajena no distingue «no es tuya» de
//     «no existe». Al revés que Get, aquí «no existe» NO es un error;
//   - fallo de la base ⇒ ("", false, err) con
//     "intakes: leer la indicación del cliente de la solicitud <intakeID>: ".
//
// FILTRA POR TENANT, al contrario que la lectura de los datos del comprador (cuya
// tabla no tiene tenant_id): el llamante es el worker, que tiene el tenant de la fila
// del outbox y el intake_id de un payload, y poner los dos en el WHERE cierra la
// puerta a que un id equivocado devuelva la nota de otra empresa.
//
// 🔴 La nota NO aparece en ningún error de este método: un error que la citara
// acabaría en el log del worker, que es justo donde no puede estar.
func (p *Postgres) GetCustomerNote(ctx context.Context, tenantID, intakeID string) (string, bool, error) {
	panic(pendiente.Implementar("intakes.Postgres.GetCustomerNote"))
}
