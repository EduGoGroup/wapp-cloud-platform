package intake

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// machine_postgres.go — el SQL de la máquina de estados (Plan 044 · Ola 2 · T2.1).
//
// Las CINCO sentencias de este fichero comparten forma, y la forma es el diseño:
// UNA sentencia por transición, con el estado de partida escrito en el `WHERE`.
// Nadie lee antes de escribir. Un `SELECT status … ; if status == "processing" {
// UPDATE … }` sería lo mismo salvo por el instante entre las dos sentencias, y ese
// instante es exactamente donde dos workers se pisan.

// claimNextSQL toma el job `pending` VENCIDO más antiguo y lo pasa a `processing`,
// devolviendo de una vez todo lo que el worker necesita. «Vencido» es la palabra que
// añadió la 0078: un `pending` cuyo backoff aún no llegó NO se toma (ver abajo).
//
// # DÓNDE ESTÁ EL GUARD, Y POR QUÉ NO ESTÁ TAMBIÉN FUERA
//
// El guard del ESTADO es el `WHERE status = 'pending'` del SELECT interno, y es el
// único que hay. Un segundo claim del MISMO job no lo encuentra —ya no está en
// `pending`— y se lleva 0 filas: eso es «doble-claim pierde uno».
//
// 🔴 NO se repite `AND j.status = 'pending'` en el UPDATE de fuera, y la omisión es
// deliberada: `FOR UPDATE SKIP LOCKED` ya BLOQUEÓ la fila, así que entre la
// subconsulta y el UPDATE nadie puede cambiarla, y con READ COMMITTED una fila que
// otra transacción hubiera modificado y confirmado se re-evalúa contra el predicado
// del SELECT antes de entregarse. Ese segundo `WHERE` sería una guarda sobre un
// camino que no existe: pasa la revisión, no falla nunca, y el día que alguien
// borrara el guard de VERDAD —el de la subconsulta— seguiría ahí dando la impresión
// de que algo custodia esto.
//
// `SKIP LOCKED` (no `NOWAIT`, no esperar) es lo que deja que varias réplicas del
// worker reclamen a la vez sin bloquearse: cada una se lleva un job distinto. Es la
// misma forma que `integrations.ClaimWebhookBatch`, que ya la tiene medida en campo.
//
// # LAS DOS MITADES DEL PREDICADO, Y POR QUÉ EL ORDER BY LLEVA DOS COLUMNAS
//
// `AND next_attempt_at <= now()` es la MITAD TEMPORAL del claim, y llegó con la
// 0078: sin ella un job devuelto a `pending` porque el provider está caído se
// vuelve a reclamar en el acto, vuelve a fallar, y el bucle gira a la velocidad
// del error. El backoff se implementa EMPUJANDO ESA MARCA, no durmiendo el worker
// (doctrina literal de la 0046, la gemela de webhook_outbox). La política que la
// empuja es de T2.5; aquí solo se la respeta.
//
// `ORDER BY next_attempt_at` sirve al job cuyo castigo venció antes, y su
// desempate `created_at` sirve al cliente que lleva más tiempo esperando su
// presupuesto, no al último que escribió — que era el criterio ÚNICO antes de la
// 0078. El desempate no es decorativo y no se puede quitar: la 0078 pobló las
// filas que ya estaban en `pending` con el `now()` de la migración, así que TODAS
// comparten marca al milisegundo y sin la segunda columna su orden relativo sería
// arbitrario. Para un job que nunca falló las dos coinciden (el DEFAULT `now()`
// se evalúa en el mismo INSERT que `created_at`), así que el orden de siempre se
// conserva intacto.
//
// # POR QUÉ EL RETURNING ES TAN ANCHO
//
// Porque la reanudación tiene que salir GRATIS: `stage` + `artifacts` dicen dónde
// se quedó y qué ya está hecho, y el sobre dice con qué texto trabajar. Pedirlo en
// una segunda consulta costaría un viaje y —peor— abriría una ventana entre el
// claim y la lectura en la que el job podría haber terminado.
//
// 🔧 `attempts` se sumó al RETURNING en T2.5 y es ADITIVO: la política de
// reintentos tiene que saber cuántos van consumidos ANTES de decidir si el
// tropiezo que acaba de ocurrir reintenta o mata el job, y esa decisión solo vale
// mientras el job sigue en `processing` (ver ClaimedJob.Attempts). No sale por
// COALESCE porque la 0078 la declaró `NOT NULL DEFAULT 0`: aquí nunca hay NULL.
//
// `stage` y `source_text_kek_id` salen por COALESCE a cadena vacía porque las dos
// son NULLables en la 0072 y "" es aquí un valor legítimo («ninguna etapa todavía»,
// «sin sobre»): un sql.NullString obligaría a todo llamante a distinguir dos casos
// que se tratan igual. `message_ts` NO: ahí el NULL sí significa algo distinto de la
// fecha cero —una ventana sin ts es una fila anómala, no una fila de 1970— y por eso
// se escanea con sql.NullTime.
const claimNextSQL = `
UPDATE public.intake_jobs AS j
   SET status = 'processing', updated_at = now()
  FROM (
        SELECT id
          FROM public.intake_jobs
         WHERE status = 'pending' AND next_attempt_at <= now()
         ORDER BY next_attempt_at, created_at
         LIMIT 1
         FOR UPDATE SKIP LOCKED
       ) AS c
 WHERE j.id = c.id
RETURNING j.id::text, j.tenant_id, j.session_id, j.contact_id, j.event_id::text,
          COALESCE(j.stage, ''), j.message_ts, j.source_refs, j.artifacts,
          j.source_text_enc, j.source_text_dek, COALESCE(j.source_text_kek_id, ''),
          j.attempts
`

// ClaimNext implementa PipelineStore.
func (p *Postgres) ClaimNext(ctx context.Context) (ClaimedJob, bool, error) {
	if p == nil || p.db == nil {
		return ClaimedJob{}, false, nil
	}
	var (
		j         ClaimedJob
		messageTS sql.NullTime
		refsRaw   []byte
		artRaw    []byte
	)
	err := p.db.QueryRowContext(ctx, claimNextSQL).Scan(
		&j.ID, &j.Key.TenantID, &j.Key.SessionID, &j.Key.ContactID, &j.Key.EventID,
		&j.Stage, &messageTS, &refsRaw, &artRaw,
		&j.SourceText.Enc, &j.SourceText.DEK, &j.SourceText.KEKID,
		&j.Attempts,
	)
	if errors.Is(err, sql.ErrNoRows) {
		// No hay nada que tomar. NO es un error: es la cola vacía, que es el estado
		// normal del worker la mayor parte del tiempo.
		return ClaimedJob{}, false, nil
	}
	if err != nil {
		return ClaimedJob{}, false, fmt.Errorf("intake: reclamar job del pipeline: %w", err)
	}
	if messageTS.Valid {
		j.MessageTS = messageTS.Time.UTC()
	}
	if uerr := json.Unmarshal(refsRaw, &j.SourceRefs); uerr != nil {
		return ClaimedJob{}, false, fmt.Errorf("intake: decodificar source_refs del job %s: %w", j.ID, uerr)
	}
	if uerr := json.Unmarshal(artRaw, &j.Artifacts); uerr != nil {
		return ClaimedJob{}, false, fmt.Errorf("intake: decodificar artifacts del job %s: %w", j.ID, uerr)
	}
	return j, true, nil
}

// saveStageSQL persiste el artefacto de una etapa y mueve `stage`, en una sentencia.
//
// # LOS DOS GUARDS, Y QUÉ IMPIDE CADA UNO
//
//   - `status = 'processing'`: solo un job TOMADO avanza. De aquí sale que los
//     terminales sean absorbentes: un job en `done` no es `processing`, así que un
//     SaveStage tardío —el worker que despertó después de que otro lo terminara—
//     afecta 0 filas en vez de escribirle un artefacto encima.
//   - `array_position(...)`: la máquina no RETROCEDE. Reanudar puede repetir la
//     etapa actual (`<=`, no `<`: un job que volvió a la cola a mitad de P3 vuelve a
//     producir P3 y tiene que poder guardarlo), pero no volver a P2 desde `draft`.
//     Sin este guard, un worker rezagado con un artefacto viejo dejaría `stage`
//     apuntando hacia atrás y la reanudación siguiente repetiría trabajo ya hecho.
//
// 🔴 EL ARRAY ESTÁ DUPLICADO respecto a `stageOrder` (machine.go) y no hay forma de
// no duplicarlo: `array_position` necesita el array DENTRO de la sentencia, y
// construir el SQL con fmt.Sprintf a partir del slice cambia una duplicación
// vigilada por una concatenación de SQL. La duplicación la custodia
// TestStageOrder_CoincideConElArrayDelSQL, que compara las dos listas.
//
// `artifacts = j.artifacts || jsonb_build_object(...)` AÑADE la clave de la etapa
// sin tocar las demás: es lo que conserva `{"p2":…}` cuando entra `{"p3":…}`. El
// `||` de jsonb sobre objetos FUSIONA (a diferencia de sobre arrays, donde
// concatena), y si la clave ya existía la reemplaza — que es justo lo que hace
// idempotente repetir una etapa.
const saveStageSQL = `
UPDATE public.intake_jobs AS j
   SET stage      = $2,
       artifacts  = j.artifacts || jsonb_build_object($2::text, $3::jsonb),
       updated_at = now()
 WHERE j.id = $1::uuid
   AND j.status = 'processing'
   AND (j.stage IS NULL
        OR array_position(ARRAY['p2','p3','p4','match','draft']::text[], j.stage)
           <= array_position(ARRAY['p2','p3','p4','match','draft']::text[], $2::text))
`

// sqlStageArray es EL MISMO array que lleva saveStageSQL, escrito una vez para que
// el test de simetría pueda buscarlo en la sentencia. No se usa para construir SQL.
const sqlStageArray = `ARRAY['p2','p3','p4','match','draft']`

// SaveStage implementa PipelineStore.
func (p *Postgres) SaveStage(ctx context.Context, jobID string, a Artifact) (bool, error) {
	if p == nil || p.db == nil {
		return false, nil
	}
	if jobID == "" {
		return false, fmt.Errorf("intake: guardar etapa sin id de job")
	}
	// 🔴 LA VALIDACIÓN VA ANTES DE TOCAR LA BASE, y ese orden ES el criterio «un
	// artefacto inválido JAMÁS se persiste». Dejarlo a Postgres no serviría: el
	// `::jsonb` rechazaría un payload roto, pero un objeto JSON perfectamente válido
	// y sin `version` entraría igual, y un array también.
	if err := a.Validate(); err != nil {
		return false, err
	}
	return p.execTransition(ctx, "guardar la etapa "+a.Stage+" del job "+jobID,
		saveStageSQL, jobID, a.Stage, string(a.Payload))
}

// releaseSQL devuelve un job tomado a la cola. NO toca el sobre, NO toca los
// artefactos y NO toca `stage`: es una devolución, no un reinicio. Lo que el job ya
// hizo sigue hecho, y eso es lo que hace que la reanudación se salte etapas.
const releaseSQL = `
UPDATE public.intake_jobs
   SET status = 'pending', updated_at = now()
 WHERE id = $1::uuid AND status = 'processing'
`

// Release implementa PipelineStore.
func (p *Postgres) Release(ctx context.Context, jobID string) (bool, error) {
	if p == nil || p.db == nil {
		return false, nil
	}
	if jobID == "" {
		return false, fmt.Errorf("intake: devolver a la cola sin id de job")
	}
	return p.execTransition(ctx, "devolver a la cola el job "+jobID, releaseSQL, jobID)
}

// retrySQL es la arista de vuelta CON CASTIGO: `pending` otra vez, un intento
// consumido y la marca empujada hasta `$2`. Es LA POLÍTICA DE BACKOFF EJECUTADA
// (la decide `pipeline`, T2.5) sobre la SEDE que dejó la 0078 (T2.1).
//
// # LAS TRES ESCRITURAS VAN EN LA MISMA SENTENCIA, Y ES EL PUNTO
//
// `status`, `attempts` y `next_attempt_at` se mueven juntas o no se mueve
// ninguna. Partirlas —volver a `pending` primero y empujar la marca después—
// deja una ventana en la que el job está `pending` con la marca VIEJA (vencida,
// porque es la que dejó pasar el claim anterior) y otro worker se lo lleva de
// inmediato: la tormenta de la 0078, reintroducida por el orden de dos UPDATE.
//
// `attempts = attempts + 1` se calcula EN EL MOTOR y no en Go: el valor que el
// worker leyó en el claim podría estar rancio si alguien tocó la fila, y sumar
// desde el valor de la base es la misma cuenta que hace `MarkWebhookFailed`
// (integrations/postgres.go:169), su gemela canónica.
//
// Guard `status = 'processing'`: solo se castiga a un job TOMADO. Un Retry sobre
// un job ya terminado afecta 0 filas y devuelve `(false, nil)` — que el worker
// tiene que LOGUEAR, porque significa que el job se le movió bajo los pies.
const retrySQL = `
UPDATE public.intake_jobs
   SET status          = 'pending',
       attempts        = attempts + 1,
       next_attempt_at = $2,
       updated_at      = now()
 WHERE id = $1::uuid AND status = 'processing'
`

// Retry implementa PipelineStore.
func (p *Postgres) Retry(ctx context.Context, jobID string, next time.Time) (bool, error) {
	if p == nil || p.db == nil {
		return false, nil
	}
	if jobID == "" {
		return false, fmt.Errorf("intake: reencolar con backoff sin id de job")
	}
	if next.IsZero() {
		// Una marca cero es el año 1, o sea `next_attempt_at` en el pasado: el job
		// volvería a ser reclamable EN EL ACTO y el backoff sería un no-op
		// silencioso. Se rechaza aquí porque desde fuera no se distingue de un
		// reintento inmediato legítimo.
		return false, fmt.Errorf("intake: reencolar el job %s sin marca de reintento: el backoff quedaría en el pasado", jobID)
	}
	return p.execTransition(ctx, "reencolar con backoff el job "+jobID, retrySQL, jobID, next.UTC())
}

// finishSQL es INV-13 escrito: `done` y el vaciado de LAS TRES columnas del sobre
// en la MISMA sentencia. No hay ningún instante en el que la fila esté `done` con el
// literal dentro, y esa es toda la diferencia entre esto y un barrido posterior —un
// barrido siempre deja esa ventana abierta.
//
// 🔴 LAS TRES, no solo `source_text_enc`. Media fila borrada —el sobre sin su DEK, o
// la DEK sin su sobre— es peor que la fila entera: no descifra, no está limpia, y
// pasa cualquier barrido que mire una sola columna. Además `source_text_kek_id` es
// lo que mete la fila en el censo del Rekey (`crypto.rekeyTargets`, índice parcial
// `idx_intake_jobs_kek`): dejarlo puesto haría que la rotación de KEK siguiera
// re-envolviendo una DEK que ya no protege nada.
//
// Lo que NO se toca: `source_refs` (rastro opaco, sobrevive), `artifacts` (el
// resultado del pipeline), `stage`, `error`, `message_ts`. Nada se borra salvo el
// literal.
//
// `intake_id = COALESCE($2::uuid, j.intake_id)`: el borrador de la Ola 3 se escribe
// AQUÍ, en la transición, porque después ya no se puede — `done` es absorbente y un
// UPDATE posterior afectaría 0 filas. Un `$2` nulo deja la columna como estaba: esta
// sentencia no borra un `intake_id` que ya existiera.
const finishSQL = `
UPDATE public.intake_jobs AS j
   SET status             = 'done',
       intake_id          = COALESCE($2::uuid, j.intake_id),
       source_text_enc    = NULL,
       source_text_dek    = NULL,
       source_text_kek_id = NULL,
       updated_at         = now()
 WHERE j.id = $1::uuid AND j.status = 'processing'
`

// Finish implementa PipelineStore.
func (p *Postgres) Finish(ctx context.Context, jobID, intakeID string) (bool, error) {
	if p == nil || p.db == nil {
		return false, nil
	}
	if jobID == "" {
		return false, fmt.Errorf("intake: terminar sin id de job")
	}
	// intakeID vacío viaja como NULL: `COALESCE(NULL, j.intake_id)` deja la columna
	// intacta. Mandar "" haría fallar el cast `::uuid` con un error del motor.
	var intake any
	if intakeID != "" {
		intake = intakeID
	}
	return p.execTransition(ctx, "terminar el job "+jobID, finishSQL, jobID, intake)
}

// failSQL es el otro terminal, y vacía el sobre EXACTAMENTE IGUAL que finishSQL: lo
// que dispara INV-13 es TERMINAR, no terminar bien. Un job envenenado conserva su
// literal tanto tiempo como uno feliz —o sea, ninguno.
//
// `stage` NO se limpia: es DÓNDE murió, y eso es lo primero que mira un operador.
// `artifacts` tampoco: las etapas que sí salieron son la mitad del diagnóstico.
const failSQL = `
UPDATE public.intake_jobs AS j
   SET status             = 'failed',
       error              = $2,
       source_text_enc    = NULL,
       source_text_dek    = NULL,
       source_text_kek_id = NULL,
       updated_at         = now()
 WHERE j.id = $1::uuid AND j.status = 'processing'
`

// Fail implementa PipelineStore.
func (p *Postgres) Fail(ctx context.Context, jobID, reason string) (bool, error) {
	if p == nil || p.db == nil {
		return false, nil
	}
	if jobID == "" {
		return false, fmt.Errorf("intake: fallar sin id de job")
	}
	if reason == "" {
		// Un job muerto sin causa no se puede diagnosticar, y la columna es NULLable
		// así que Postgres lo aceptaría sin rechistar.
		return false, fmt.Errorf("intake: fallar el job %s sin causa: `error` es para el operador y no puede ir vacío", jobID)
	}
	return p.execTransition(ctx, "fallar el job "+jobID, failSQL, jobID, reason)
}

// execTransition ejecuta una transición de la máquina y traduce «0 filas» a
// `(false, nil)`. Centraliza la convención del paquete —la misma que CloseWindow y
// PutSourceText— en un solo sitio: una transición que no aplica NO es un error, es
// que otro llegó antes o que el job ya estaba terminado.
//
// `what` es texto de operador para el error, ya compuesto por el llamante.
func (p *Postgres) execTransition(ctx context.Context, what, query string, args ...any) (bool, error) {
	res, err := p.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("intake: %s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("intake: contar filas al %s: %w", what, err)
	}
	return n > 0, nil
}
