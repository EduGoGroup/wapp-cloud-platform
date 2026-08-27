// reanalisis.go — EL SEGUNDO PRODUCTOR DE JOBS (Plan 044 · Ola 4 · T4.6, D-044.15,
// design §8.1; migración 0080).
//
// # QUÉ ES ESTO, EN UNA FRASE
//
// `POST /api/v1/intakes/{id}/reanalyze` abre un `intake_job` sobre un evento que YA
// tiene su solicitud, para que el pipeline vuelva a interpretar el MISMO material y
// deje una revisión más. Es la segunda puerta por la que nace un job; la primera —y
// hasta hoy la única— es el agregador, que abre la ventana con el mensaje del
// cliente (D-044.26).
//
// # 🔴 POR QUÉ EL JOB NACE EN `pending` Y NO EN `aggregating`
//
// Las DOS razones son estructurales, no de estilo:
//
//  1. **El sobre del literal solo se puede escribir sobre una ventana `pending`.**
//     `PutSourceText` elige «la ÚLTIMA TOCADA de esa tupla en `pending`» y exige que
//     su sobre esté vacío. Un job nacido `aggregating` no sería elegible, así que el
//     compositor (`ComposeAtFlush`) escribiría en cualquier OTRA fila `pending` de la
//     tupla —una vieja, ya compuesta o no— o en ninguna. Naciendo `pending` y con su
//     `updated_at` recién puesto, el job del re-análisis es EXACTAMENTE la fila que
//     `PutSourceText` elige.
//  2. **`aggregating` es el único estado que entra en el índice único parcial**
//     `intake_jobs_ventana_viva_uidx`. Un re-análisis pedido mientras el cliente
//     sigue escribiendo chocaría contra la ventana viva de esa misma tupla, y el
//     fallo sería un 23505 en vez del `422 reanalysis_in_progress` que el contrato
//     manda. Naciendo `pending` no entra en el índice y la concurrencia la decide
//     JobNoTerminalDeEvento, que responde lo que el contrato pide.
//
// Y hay un tercer efecto, buscado: un job `pending` es reclamable YA. No hay ventana
// que esperar porque no hay nada que agregar — el material ya está escrito.
//
// # LO QUE ESTE FICHERO NO HACE
//
// No lee el hilo, no descifra, no cifra y no decide si hay material: eso es del
// endpoint (`internal/reanalisis`), que además lo comprueba ANTES de llamar aquí
// para no dejar jobs huérfanos que nadie va a poder correr. Aquí solo se abre la
// fila y se pregunta por la concurrencia.
package intake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SolicitudReanalisis es lo que hace falta para abrir el job del re-análisis: la
// clave de ventana del evento, la solicitud a la que se cuelga y el contexto de
// D-044.15 que el `draft` necesitará al otro extremo.
//
// `MessageTS` NO está aquí a propósito: lo resuelve la propia sentencia leyendo el
// PRIMER job del evento (ver abrirReanalisisSQL). Pedirlo por parámetro habría
// obligado al endpoint a una consulta más para copiar un valor que ya está en esta
// tabla.
type SolicitudReanalisis struct {
	// Key es la clave de ventana del evento que se re-analiza. Las cuatro columnas
	// son NOT NULL en la 0072, así que se valida antes de escribir.
	Key WindowKey
	// IntakeID es la solicitud EXISTENTE a la que este job le colgará una revisión
	// más. Se escribe ya, en el INSERT, y no al terminar: a diferencia del pipeline
	// normal —donde el borrador todavía no existe y el id lo devuelve `draft`—, aquí
	// la solicitud es el sujeto de la petición y se conoce desde el principio.
	IntakeID string
	// Contexto son las cuatro columnas de la 0080.
	Contexto Reanalisis
}

// Valid dice si la solicitud puede abrir un job.
func (s SolicitudReanalisis) Valid() bool {
	return s.Key.Valid() && s.IntakeID != "" && s.Contexto.EsDelDueño()
}

// estadosNoTerminales son los tres estados desde los que un job todavía puede
// producir una revisión: la ventana abierta, la cerrada esperando worker y la que
// un worker está corriendo. Se nombran aquí —y no se escriben como literales en la
// sentencia— porque son EXACTAMENTE «lo que no es terminal» (ver IsTerminal), y el
// día que la máquina gane un cuarto estado vivo el sitio donde mirar es este.
var estadosNoTerminales = []string{StatusAggregating, StatusPending, StatusProcessing}

// jobNoTerminalSQL responde «¿hay ya un job vivo para este evento?», que es la
// pregunta del `422 reanalysis_in_progress` (design §8.1, D-044.15 · concurrencia).
//
// # POR QUÉ POR EVENTO Y NO POR SOLICITUD
//
// Porque el job del pipeline NORMAL —el que abre el agregador mientras el cliente
// escribe— todavía no tiene `intake_id`: esa columna la escribe `Finish`, al final.
// Filtrando por `intake_id` ese job sería invisible y el re-análisis abriría un
// SEGUNDO job sobre el mismo evento; los dos correrían el pipeline y los dos
// escribirían una revisión. El `event_id` sí lo tienen los dos desde el INSERT, y es
// la columna por la que se cruzan las dos puertas.
//
// 🔴 ESO ES ADEMÁS LA GUARDA DE LA CARRERA CON LA VENTANA VIVA. Un re-análisis
// pedido en mitad de una ráfaga del cliente encuentra aquí el job `aggregating` y
// sale por el 422, que es la respuesta correcta: el material todavía se está
// escribiendo.
//
// `ORDER BY created_at DESC` devuelve el más reciente: si hubiera más de uno vivo
// —imposible por el índice único mientras sean `aggregating`, posible entre estados
// distintos— el útil para quien recibe el 422 es el que acaba de empezar.
const jobNoTerminalSQL = `
SELECT id::text
  FROM public.intake_jobs
 WHERE tenant_id = $1 AND event_id = $2::uuid
   AND status = ANY($3)
 ORDER BY created_at DESC
 LIMIT 1
`

// JobNoTerminalDeEvento devuelve el id del job vivo de ese evento, si lo hay.
//
// `(", false, nil)` significa «no hay ninguno»: NO es un error, es el caso normal —un
// evento cuyo pipeline ya terminó es exactamente el que se puede re-analizar.
func (p *Postgres) JobNoTerminalDeEvento(ctx context.Context, tenantID, eventID string) (string, bool, error) {
	if p == nil || p.db == nil {
		return "", false, nil
	}
	if tenantID == "" || eventID == "" {
		// No es «no hay ninguno»: es una llamada mal hecha, y dejarla pasar
		// convertiría la guarda de concurrencia en un sí incondicional.
		return "", false, fmt.Errorf("intake: hacen falta tenant y evento para preguntar por el job vivo")
	}
	var id string
	err := p.db.QueryRowContext(ctx, jobNoTerminalSQL, tenantID, eventID, estadosNoTerminales).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("intake: buscar el job vivo del evento %s: %w", eventID, err)
	}
	return id, true, nil
}

// abrirReanalisisSQL abre el job del re-análisis. UNA sentencia, y las tres piezas
// que merecen explicación:
//
//   - `'pending'` EXPLÍCITO, contra el DEFAULT `'aggregating'` de la columna. Es la
//     decisión entera de este fichero (ver la cabecera): fuera del índice único
//     parcial y dentro de lo que `PutSourceText` sabe elegir.
//
//   - `message_ts` SE COPIA DEL PRIMER JOB DEL EVENTO y solo cae a `now()` si no hay
//     ninguno. 🔴 Y esto no es cosmética: `message_ts` es la BASE DE FECHAS de P4
//     («el miércoles que viene» se resuelve contra ella, D-044.9). Poner el reloj de
//     HOY haría que un re-análisis pedido tres días después de la conversación
//     resolviera «mañana» a otro día que la revisión 1 — el mismo texto daría dos
//     fechas distintas, y la culpa no se vería en ninguna parte. El material que se
//     re-interpreta es el mismo, así que su base temporal tiene que ser la misma.
//     El `COALESCE` a `now()` cubre la solicitud que NO nació de un job (la del
//     carrito numérico del Plan 016/041, que no tiene fila aquí).
//     ⚠️ CONSECUENCIA ACEPTADA Y DICHA: el `elapsed_ms` que publica `draft` mide la
//     espera DEL CLIENTE desde que escribió, así que en un re-análisis sale enorme.
//     Es verdad, no un error — y por eso la métrica lleva `requested_by`, para que el
//     KPI «< 5 min» se pueda calcular sobre los jobs del pipeline normal.
//
//   - `intake_id` va en el INSERT. En el pipeline normal lo escribe `Finish` porque
//     el borrador no existe hasta el final; aquí la solicitud es el SUJETO de la
//     petición y ya existe, así que el job nace sabiendo a quién sirve. `Finish`
//     volverá a escribir el mismo valor y eso es idempotente.
//
// `source_refs` va explícito a `'[]'` aunque sea el DEFAULT: un re-análisis no aporta
// ningún `wa_message_id` nuevo —no hay mensaje entrante— y decirlo aquí evita que
// alguien lea el silencio como «se me olvidó».
const abrirReanalisisSQL = `
INSERT INTO public.intake_jobs
       (tenant_id, session_id, contact_id, event_id, status, message_ts, source_refs,
        intake_id, requested_by, reanalysis_via, reanalysis_source, reanalyzed_from)
VALUES ($1, $2, $3, $4::uuid, 'pending',
        COALESCE((SELECT j0.message_ts
                    FROM public.intake_jobs j0
                   WHERE j0.tenant_id = $1 AND j0.event_id = $4::uuid
                     AND j0.message_ts IS NOT NULL
                   ORDER BY j0.created_at
                   LIMIT 1), now()),
        '[]'::jsonb,
        $5::uuid, $6, $7, $8, $9)
RETURNING id::text
`

// AbrirReanalisis crea el job del re-análisis y devuelve su id.
//
// NO es idempotente y no puede serlo: dos re-análisis del mismo pedido son dos actos
// distintos y tienen que dejar dos revisiones. Quien impide el duplicado accidental
// es JobNoTerminalDeEvento, arriba, y la comprobación va antes de llamar aquí.
func (p *Postgres) AbrirReanalisis(ctx context.Context, s SolicitudReanalisis) (string, error) {
	if p == nil || p.db == nil {
		return "", nil
	}
	if !s.Valid() {
		// El error dice QUÉ falta sin volcar la estructura: la clave de ventana lleva
		// el contact_id opaco y no hay razón para pasearlo por un log.
		return "", fmt.Errorf("intake: solicitud de re-análisis incompleta (ventana=%t intake=%t dueño=%t)",
			s.Key.Valid(), s.IntakeID != "", s.Contexto.EsDelDueño())
	}
	var id string
	err := p.db.QueryRowContext(ctx, abrirReanalisisSQL,
		s.Key.TenantID, s.Key.SessionID, s.Key.ContactID, s.Key.EventID,
		s.IntakeID, s.Contexto.RequestedBy, s.Contexto.Via, s.Contexto.Source,
		nullableInt(s.Contexto.From),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("intake: abrir el job de re-análisis del evento %s: %w", s.Key.EventID, err)
	}
	return id, nil
}

// nullableInt manda NULL en vez de 0 a `reanalyzed_from`. «No había revisión
// anterior» y «la revisión anterior era la número cero» no son lo mismo, y la
// segunda no existe: los correlativos empiezan en 1. El contrato §7.4 publica ese
// caso como `null`, así que la columna guarda NULL y no un 0 que después habría que
// traducir.
func nullableInt(n int) any {
	if n <= 0 {
		return nil
	}
	return n
}
