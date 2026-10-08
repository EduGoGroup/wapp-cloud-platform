// Porta internal/intakes/crm.go @ 64c181a

// postgres_crm.go es la sentencia que aplica el REFLEJO del CRM sobre una solicitud.
// En el paquete viejo el método vivía en crm.go, junto al vocabulario; aquí nace con
// el adaptador (D-F6-6 ampliada) y crm.go se queda con lo puro.

package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

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
// (ADR-0031: «cuando HAY CRM, el CRM manda») y devuelve qué pasó.
//
// `status` tiene que ser un estado canónico (IsCRMStatus). Si no lo es ⇒ error
// `intakes: "<status>" no es un estado canónico del CRM` (con %q), SIN tocar la base:
// es defensa en profundidad, la frontera ya validó contra el schema publicado.
//
// 🔴 intakeID NO se valida como UUID aquí, al revés que en el resto del adaptador: el
// paquete viejo no lo hacía y se conserva. Lo valida la frontera.
//
// Abre su PROPIA transacción (BeginTx, sin el reintento de postgres.WithTx) y dentro
// lanza dos sentencias:
//
//  1. EL REFLEJO, una sola sentencia que bloquea la fila del tenant, escribe
//     crm_status y crm_synced_at = syncedAt, y devuelve cuántas filas encontró y si
//     algo CAMBIÓ. Tres reglas viajan en ella: `externalRef` vacío NO borra la
//     referencia que hubiera; updated_at solo se mueve si el estado o la referencia
//     cambian de verdad (un callback repetido no reordena la bandeja); y el tenant
//     acota el UPDATE.
//  2. Solo si encontró la solicitud: la RELECTURA de la cabecera, en la misma
//     transacción (quien avisa al cliente necesita su contacto y su sesión, y leerla
//     fuera dejaría sitio a que otro callback la cambie).
//
// Resultados:
//
//   - la solicitud no existe o es de OTRO tenant ⇒ (CRMReflection{}, nil): Found=false
//     para las dos, que es lo que impide usar el callback como oráculo de ids
//     (INV-8). 🔴 La transacción se REVIERTE explícitamente antes de volver: es el
//     único camino que sale sin error y sin confirmar, y sin ese rollback la conexión
//     quedaría «idle in transaction» reteniendo su candado sobre public.intakes hasta
//     agotar el pool;
//   - la encontró ⇒ (CRMReflection{Found: true, Changed: <cambió>, Intake: <cabecera>}, nil),
//     con la transacción confirmada. Un callback idéntico al anterior da Changed=false.
//
// syncedAt lo pone el llamante y NO es el occurred_at del cuerpo: aquel es el instante
// del hecho en el CRM, con un reloj ajeno; esta columna afirma cuándo lo recibimos.
//
// Errores, envueltos con %w y devolviendo CRMReflection{}; salvo el del commit, todos
// dejan la transacción revertida:
//
//   - "intakes: abrir transacción del reflejo: " — no se puede abrir;
//   - "intakes: reflejar el estado del CRM: " — falla la sentencia del reflejo;
//   - "intakes: releer la solicitud reflejada: " — falla la relectura (envuelve el
//     "intakes: leer solicitud: " de la fila, o sql.ErrNoRows si desapareció);
//   - "intakes: confirmar el reflejo: " — falla el commit;
//   - "intakes: cerrar la transacción de un reflejo sin destinatario: " — falla el
//     rollback del camino Found=false;
//   - si además de uno de los anteriores falla su rollback, los dos viajan unidos
//     (errors.Join) y el segundo lleva "intakes: rollback del reflejo: ".
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
