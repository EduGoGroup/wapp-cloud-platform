// Porta internal/intakes/crm.go @ 64c181a

// postgres_crm.go es la sentencia que aplica el REFLEJO del CRM sobre una solicitud.
// En el paquete viejo el método vivía en crm.go, junto al vocabulario; aquí nace con
// el adaptador (D-F6-6 ampliada) y crm.go se queda con lo puro.

package intakes

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

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
	panic(pendiente.Implementar("intakes.Postgres.ReflectCRMStatus"))
}
