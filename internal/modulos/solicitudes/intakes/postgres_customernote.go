// Porta internal/intakes/customernote.go @ 64c181a

// postgres_customernote.go es la lectura de la indicación del cliente de una
// solicitud. En el paquete viejo era lo único que llevaba customernote.go, que por
// eso no nace: el método nace con el adaptador (D-F6-6 ampliada).

package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
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
	if !isUUID(intakeID) {
		return "", false, nil
	}

	var note string
	err := p.db.QueryRowContext(ctx, `
		SELECT customer_note
		FROM public.intakes
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, intakeID).Scan(&note)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("intakes: leer la indicación del cliente de la solicitud %s: %w", intakeID, err)
	}
	return note, true, nil
}

// isUUID envuelve uuid.Parse en un booleano. Existe por dos razones que apuntan al
// mismo sitio: la pregunta que se hace arriba no es «¿qué error dio?» sino «¿esto
// puede ser un id?», y con el error en el ámbito el `return nil` de la línea
// siguiente parece —también para el linter, nilerr— que se está tragando un fallo.
// No se traga ninguno: un id que no es UUID no puede existir en la tabla, y eso es
// un 404, no un error. Era esUUID en el viejo.
func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}
