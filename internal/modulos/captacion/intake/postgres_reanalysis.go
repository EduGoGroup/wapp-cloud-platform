// Porta internal/intake/reanalisis.go @ 8d875ab

// postgres_reanalysis.go es el SQL del SEGUNDO PRODUCTOR DE JOBS (ver reanalysis.go,
// que lleva el porqué entero): la pregunta por el job vivo de un evento y la
// apertura del job del re-análisis. En el paquete viejo los dos métodos vivían en
// reanalisis.go; aquí nacen con el adaptador (D-F6-6 ampliada).
//
// 🔴 NINGUNO DE LOS DOS ES DE JobStore NI DE PipelineStore, a propósito: el sink no
// puede tener delante una lectura (D-044.26) y el worker no abre jobs. Los consume
// el endpoint del re-análisis por su propio puerto estrecho.

package intake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// nonTerminalStatuses (antes `estadosNoTerminales`) son los tres estados desde los que un job todavía puede
// producir una revisión: la ventana abierta, la cerrada esperando worker y la que
// un worker está corriendo. Se nombran aquí —y no se escriben como literales en la
// sentencia— porque son EXACTAMENTE «lo que no es terminal» (ver IsTerminal), y el
// día que la máquina gane un cuarto estado vivo el sitio donde mirar es este.
var nonTerminalStatuses = []string{StatusAggregating, StatusPending, StatusProcessing}

// liveJobSQL (antes `jobNoTerminalSQL`) responde «¿hay ya un job vivo para este evento?», que es la
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
const liveJobSQL = `
SELECT id::text
  FROM public.intake_jobs
 WHERE tenant_id = $1 AND event_id = $2::uuid
   AND status = ANY($3)
 ORDER BY created_at DESC
 LIMIT 1
`

// LiveJobOfEvent (antes `JobNoTerminalDeEvento`) devuelve el id del job vivo de ese
// evento, si lo hay.
//
// `(", false, nil)` significa «no hay ninguno»: NO es un error, es el caso normal —un
// evento cuyo pipeline ya terminó es exactamente el que se puede re-analizar.
func (p *Postgres) LiveJobOfEvent(ctx context.Context, tenantID, eventID string) (string, bool, error) {
	if p == nil || p.db == nil {
		return "", false, nil
	}
	if tenantID == "" || eventID == "" {
		// No es «no hay ninguno»: es una llamada mal hecha, y dejarla pasar
		// convertiría la guarda de concurrencia en un sí incondicional.
		return "", false, fmt.Errorf("intake: hacen falta tenant y evento para preguntar por el job vivo")
	}
	var id string
	err := p.db.QueryRowContext(ctx, liveJobSQL, tenantID, eventID, nonTerminalStatuses).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("intake: buscar el job vivo del evento %s: %w", eventID, err)
	}
	return id, true, nil
}

// openReanalysisSQL (antes `abrirReanalisisSQL`) abre el job del re-análisis. UNA sentencia, y las cuatro piezas
// que merecen explicación:
//
//   - `'pending'` EXPLÍCITO, contra el DEFAULT `'aggregating'` de la columna. Es la
//     decisión entera de este fichero (ver la cabecera de reanalysis.go): fuera del
//     índice único parcial, y reclamable ya porque nace con su literal.
//
//   - LAS TRES COLUMNAS DEL SOBRE VAN EN ESTE MISMO INSERT (`$10, $11, $12`).
//     Divergencia deliberada del viejo (D-F7-9, D-F8-13), T8.40: el viejo insertaba la
//     fila `pending` SIN sobre y el compositor lo escribía después con
//     `PutSourceText`; entre las dos sentencias el worker podía reclamar un job sin
//     literal y matarlo. Aquí la fila es visible ya con su sobre: una sentencia, sin
//     transacción y sin migración (las columnas son las de la 0072). Con el sobre
//     vacío entero los tres parámetros viajan como NULL.
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
const openReanalysisSQL = `
INSERT INTO public.intake_jobs
       (tenant_id, session_id, contact_id, event_id, status, message_ts, source_refs,
        intake_id, requested_by, reanalysis_via, reanalysis_source, reanalyzed_from,
        source_text_enc, source_text_dek, source_text_kek_id)
VALUES ($1, $2, $3, $4::uuid, 'pending',
        COALESCE((SELECT j0.message_ts
                    FROM public.intake_jobs j0
                   WHERE j0.tenant_id = $1 AND j0.event_id = $4::uuid
                     AND j0.message_ts IS NOT NULL
                   ORDER BY j0.created_at
                   LIMIT 1), now()),
        '[]'::jsonb,
        $5::uuid, $6, $7, $8, $9,
        $10, $11, $12)
RETURNING id::text
`

// OpenReanalysis (antes `AbrirReanalisis`) crea el job del re-análisis, YA con el sobre
// de la petición (ReanalysisRequest.SourceText), y devuelve su id.
//
// Se rechaza antes de tocar la base, y en este orden: (1) la petición incompleta
// (`!Valid()`), con su texto; (2) el sobre A MEDIAS, con el mismo texto que
// CloseWithSourceText. El sobre va completo o vacío entero; vacío es el
// hilo sin mensajes y el job nace con las tres columnas a NULL.
//
// NO es idempotente y no puede serlo: dos re-análisis del mismo pedido son dos actos
// distintos y tienen que dejar dos revisiones. Quien impide el duplicado accidental
// es LiveJobOfEvent, arriba, y la comprobación va antes de llamar aquí.
func (p *Postgres) OpenReanalysis(ctx context.Context, s ReanalysisRequest) (string, error) {
	if p == nil || p.db == nil {
		return "", nil
	}
	if !s.Valid() {
		// El error dice QUÉ falta sin volcar la estructura: la clave de ventana lleva
		// el contact_id opaco y no hay razón para pasearlo por un log.
		return "", fmt.Errorf("intake: solicitud de re-análisis incompleta (ventana=%t intake=%t dueño=%t)",
			s.Key.Valid(), s.IntakeID != "", s.Context.IsFromOwner())
	}
	// COMPLETO O VACÍO ENTERO, como en CloseWithSourceText. Vacío viaja como tres NULL
	// —no como un bytea de longitud cero ni un kek_id "", que dejarían una fila que
	// parece tener sobre—. A medias es una fila indescifrable: se rechaza aquí, DESPUÉS
	// de la petición (con las dos cosas mal manda el texto de la petición), y el error
	// dice qué falta sin citar el contenido.
	var enc, dek, kekID any
	switch env := s.SourceText; {
	case env.Complete():
		enc, dek, kekID = env.Enc, env.DEK, env.KEKID
	case !env.Empty():
		return "", fmt.Errorf("intake: sobre del literal incompleto (enc=%d dek=%d kek_id=%t): son las tres o ninguna",
			len(env.Enc), len(env.DEK), env.KEKID != "")
	}
	var id string
	err := p.db.QueryRowContext(ctx, openReanalysisSQL,
		s.Key.TenantID, s.Key.SessionID, s.Key.ContactID, s.Key.EventID,
		s.IntakeID, s.Context.RequestedBy, s.Context.Via, s.Context.Source,
		nullableInt(s.Context.From),
		enc, dek, kekID,
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
