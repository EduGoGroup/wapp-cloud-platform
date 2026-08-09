// customernote.go publica la lectura suelta de la INDICACIÓN DEL CLIENTE
// (public.intakes.customer_note, migración 0045 · D-041.19).
//
// Vive en su propio archivo por la misma razón que buyerdata.go: es una lectura
// pensada para UN consumidor concreto y con un motivo que hay que poder leer sin
// bucear en el store entero. Ese consumidor es el worker del puente CRM
// (internal/integrations), que la completa en el payload justo antes del POST.
//
// POR QUÉ NO VIAJA YA EN LA PLANTILLA ENCOLADA. La nota es texto libre del cliente
// final —«dejarlo en portería, calle Mayor 14»—, o sea la PII de manual. El defecto
// A2 del Plan 041 la sacó de public.flow_events podándola por clave, y al hacerlo
// destapó que seguía congelada EN CLARO dentro de webhook_outbox.payload, una
// segunda tabla que además sobrevive a la entrega. Se cierra por el mismo camino
// que buyer_data y variables{}: fuera de lo que se persiste, dentro de lo que el
// worker arma en memoria (D-042.9/D-042.11, INV-02).
//
// El contrato wapp-crm-v1 NO cambia: `customer_note` sigue llegando al puente en el
// mismo sitio del JSON. Lo que cambia es CUÁNDO se rellena — y, como consecuencia,
// que refleja la nota AL MOMENTO DE LA ENTREGA, igual que `variables{}`.
package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// GetCustomerNote lee la indicación del cliente de UNA solicitud del tenant.
// found=false si la solicitud no existe, no es de ese tenant, o su id ni siquiera
// es un UUID — los tres son el mismo 404 opaco de Get (INV-8): quien pregunta por
// una solicitud ajena no puede distinguir "no es tuya" de "no existe".
//
// FILTRA POR TENANT, al contrario que GetBuyerData. No es una incoherencia: aquel
// no puede filtrar porque intake_buyer_data no tiene tenant_id y su llamante ya
// resolvió la solicitud abierta de (tenant, contacto). Aquí el llamante es el
// worker, que tiene el tenant de la fila del outbox a mano y el intake_id de un
// payload — poner las dos cosas en el WHERE cuesta cero y cierra la puerta a que un
// intake_id equivocado devuelva la nota de otra empresa.
//
// La nota NO aparece en ningún error de esta función, misma regla que buyerdata.go:
// un error que la citara acabaría en el log del worker, que es justo donde no puede
// estar.
func (p *Postgres) GetCustomerNote(ctx context.Context, tenantID, intakeID string) (string, bool, error) {
	if !esUUID(intakeID) {
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

// esUUID envuelve uuid.Parse en un booleano. Existe por dos razones que apuntan al
// mismo sitio: la pregunta que se hace arriba no es «¿qué error dio?» sino «¿esto
// puede ser un id?», y con el error en el ámbito el `return nil` de la línea
// siguiente parece —también para el linter, nilerr— que se está tragando un fallo.
// No se traga ninguno: un id que no es UUID no puede existir en la tabla, y eso es
// un 404, no un error.
func esUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}
