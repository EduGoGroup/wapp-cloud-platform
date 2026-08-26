package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Estados CANÓNICOS del contrato wapp-crm-v1 (verbo intake.status, D-042.6). Son un
// vocabulario PROPIO de la frontera con el CRM y NO estados del ciclo de vida de
// wApp: viven en su columna (intakes.crm_status) y jamás pisan intakes.status.
//
// Ojo con `rejected`: existe en los DOS vocabularios y significa cosas distintas.
// Aquí es «el CRM del cliente rechazó el pedido»; en status.go es una transición del
// ciclo de vida que decide el dueño desde su bandeja. Que compartan literal es una
// coincidencia del castellano, no un puente entre las dos máquinas.
const (
	CRMStatusPaid      = "paid"
	CRMStatusPreparing = "preparing"
	CRMStatusDelivered = "delivered"
	CRMStatusRejected  = "rejected"
)

// crmCanonicalStatuses es la lista cerrada, en el mismo orden que el enum del
// schema publicado (intake.status.schema.json) y que el CHECK de la migración 0048.
// Los tres sitios dicen lo mismo a propósito: el schema rechaza en la frontera, esto
// rechaza en el dominio y el CHECK rechaza en la base. Ninguno sustituye a otro.
var crmCanonicalStatuses = []string{
	CRMStatusPaid, CRMStatusPreparing, CRMStatusDelivered, CRMStatusRejected,
}

// IsCRMStatus dice si un estado pertenece al vocabulario canónico del CRM.
func IsCRMStatus(status string) bool {
	return slices.Contains(crmCanonicalStatuses, status)
}

// CRMReflection es el resultado de reflejar un estado del CRM sobre una solicitud.
//
// Found y Changed son preguntas DISTINTAS y las dos importan: sin Found no se puede
// responder 404 a una solicitud ajena o inexistente, y sin Changed no se sabe si hay
// que avisar al cliente —un puente con reintentos manda el mismo estado muchas veces
// y el cliente no puede recibir el mismo mensaje una vez por reintento—.
type CRMReflection struct {
	Found   bool
	Changed bool
	// Intake es la solicitud YA reflejada. Solo tiene contenido con Found: es lo que
	// necesita el notificador (contacto y sesión) sin volver a consultar.
	Intake Intake
}

// reflectCRMStatusQuery aplica el reflejo en UNA sentencia y devuelve, a la vez, si
// la solicitud existía y si el reflejo cambió algo.
//
// POR QUÉ EN UNA SOLA SENTENCIA Y NO CON UN SELECT PREVIO: entre un SELECT y el
// UPDATE cabe otro callback (misma doctrina que markDepositRemindedQuery). El CTE
// `prev` bloquea la fila con FOR UPDATE y captura el valor ANTERIOR, que es lo único
// que permite distinguir «cambió» de «llegó igual» — un UPDATE ... RETURNING solo
// devuelve valores nuevos.
//
// LOS DOS TIMESTAMPS SE MUEVEN POR SEPARADO, y no es un descuido:
//
//   - crm_synced_at se escribe SIEMPRE que llega un callback válido, aunque no cambie
//     nada. Su comentario en la 0048 dice que es «el momento del último reflejo
//     recibido» y que sirve para «detectar integraciones mudas»: un puente que repite
//     el mismo estado está VIVO, y congelarle la marca lo haría parecer caído.
//   - updated_at (el timestamp de NEGOCIO) solo se mueve si de verdad cambió el
//     estado o la referencia. Si no, un puente con reintentos agresivos dejaría toda
//     la bandeja del dueño «recién tocada» por cosas que nadie hizo.
//
// external_ref VACÍO significa «no me pronuncio», no «bórrala»: el contrato la
// declara opcional, así que un callback sin ella conserva la que hubiera. Es la
// referencia que una persona usa para cruzar la solicitud con su CRM y perderla por
// un callback escueto sería peor que ignorarlo.
const reflectCRMStatusQuery = `
	WITH prev AS (
		SELECT id, crm_status, crm_external_ref
		FROM public.intakes
		WHERE tenant_id = $1 AND id = $2
		FOR UPDATE
	), upd AS (
		UPDATE public.intakes i
		SET crm_status       = $3,
		    crm_external_ref = CASE WHEN $4 <> '' THEN $4 ELSE i.crm_external_ref END,
		    crm_synced_at    = $5,
		    updated_at       = CASE
		                         WHEN p.crm_status IS DISTINCT FROM $3
		                           OR ($4 <> '' AND p.crm_external_ref IS DISTINCT FROM $4)
		                         THEN now() ELSE i.updated_at
		                       END
		FROM prev p
		WHERE i.id = p.id
		RETURNING (p.crm_status IS DISTINCT FROM $3
		           OR ($4 <> '' AND p.crm_external_ref IS DISTINCT FROM $4)) AS changed
	)
	SELECT (SELECT count(*) FROM prev), COALESCE((SELECT changed FROM upd), false)
`

// ReflectCRMStatus aplica el estado canónico del CRM sobre una solicitud del tenant
// (ADR-0031: «cuando HAY CRM, el CRM manda»).
//
// El tenant NO es un parámetro de conveniencia: acota el UPDATE, y por eso una
// solicitud de OTRO tenant sale con Found=false igual que una inexistente. El
// llamante responde lo mismo a las dos, que es lo que impide usar el callback como
// oráculo de qué ids existen (INV-8).
//
// syncedAt lo pone el llamante y NO se toma de `occurred_at` del cuerpo: aquel es el
// instante del hecho EN EL CRM —dato del puente, que puede venir con el reloj torcido
// o directamente mentir— y esta columna afirma cuándo lo recibimos NOSOTROS. Mezclarlos
// dejaría «actualizado hace X» a merced del reloj ajeno.
func (p *Postgres) ReflectCRMStatus(ctx context.Context, tenantID, intakeID, status, externalRef string,
	syncedAt time.Time) (out CRMReflection, err error) {
	if !IsCRMStatus(status) {
		// Defensa en profundidad: la frontera ya validó contra el schema publicado. Si
		// esto salta, el que se saltó el schema es un llamador nuevo, no un puente.
		return CRMReflection{}, fmt.Errorf("intakes: %q no es un estado canónico del CRM", status)
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return CRMReflection{}, fmt.Errorf("intakes: abrir transacción del reflejo: %w", err)
	}
	defer func() {
		if err != nil {
			if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
				err = errors.Join(err, fmt.Errorf("intakes: rollback del reflejo: %w", rerr))
			}
		}
	}()

	var found int
	var changed bool
	if err := tx.QueryRowContext(ctx, reflectCRMStatusQuery,
		tenantID, intakeID, status, externalRef, syncedAt).Scan(&found, &changed); err != nil {
		return CRMReflection{}, fmt.Errorf("intakes: reflejar el estado del CRM: %w", err)
	}
	if found == 0 {
		// 🔴 ESTE ROLLBACK NO ES CEREMONIA: sin él la transacción queda ABANDONADA.
		// Es el único camino que sale de aquí con `err == nil` y sin llegar al
		// Commit, así que el `defer` de arriba —que solo revierte cuando hay error—
		// no lo cubre, y la conexión se queda «idle in transaction» reteniendo su
		// ACCESS SHARE sobre public.intakes. En producción lo tapa a medias el
		// `awaitDone` de database/sql (al cancelarse el ctx de la petición la
		// revierte), pero con un ctx que no se cancela —una tarea de fondo, o un
		// test con context.Background()— la conexión NO VUELVE AL POOL NUNCA: 25
		// callbacks de un intake ajeno bastan para agotarlo.
		//
		// Lo destapó T2.8 (Plan 044 · Ola 2): su migración necesita un ACCESS
		// EXCLUSIVE sobre `intakes` y se quedaba bloqueada para siempre detrás de la
		// sesión que dejaba TestReflectCRMStatus_DeOtroTenant_NoEncuentraNiToca. Es
		// la primera vez que alguien en este repo pide ese lock sobre esta tabla, y
		// por eso el defecto llevaba desde el Plan 042 sin verse.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			return CRMReflection{}, fmt.Errorf("intakes: cerrar la transacción de un reflejo sin destinatario: %w", rerr)
		}
		return CRMReflection{}, nil
	}

	// La solicitud ya reflejada, en la MISMA transacción: quien avisa al cliente
	// necesita su contacto y su sesión, y leerla fuera abriría una ventana en la que
	// otro callback la deja distinta de la que se acaba de aplicar.
	intake, err := scanIntake(tx.QueryRowContext(ctx,
		`SELECT `+intakeCols+` FROM public.intakes WHERE tenant_id = $1 AND id = $2`,
		tenantID, intakeID))
	if err != nil {
		return CRMReflection{}, fmt.Errorf("intakes: releer la solicitud reflejada: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CRMReflection{}, fmt.Errorf("intakes: confirmar el reflejo: %w", err)
	}
	return CRMReflection{Found: true, Changed: changed, Intake: intake}, nil
}
