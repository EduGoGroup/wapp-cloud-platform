// Porta internal/intake/pipeline/pipeline.go @ 56097aa (los desenlaces, la reanudación y el cronómetro)

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// pipeline_outcome.go — CÓMO ACABA UNA ETAPA Y CÓMO ACABA UN JOB. Sin exportados: el
// contrato es el de RunOnce (pipeline_loop.go).

// closeTimeout (antes `plazoDeCierre`) es cuánto se le da a la escritura del DESENLACE
// (Finish/Fail/Retry/Release) cuando el worker se está apagando. Ver closingCtx.
const closeTimeout = 5 * time.Second

// outcome (antes `desenlace`) es el punto ÚNICO por el que pasa el resultado de toda etapa
// ejecutada, y existe para que el `elapsed` no se pueda colgar solo del camino feliz.
//
// 🔴 §5.2·bis EN UNA FUNCIÓN. El cronómetro se para ANTES de mirar el error, y el número
// sale por los DOS caminos: en el Info de la etapa que salió y en el Warn/Error del
// tropiezo. Una etapa que muere por timeout es precisamente la que más falta hace medir
// —es la que dice cuánto tardó el plazo en morder— y es la que un `log.Info` al final del
// camino feliz nunca vería.
//
// Es función libre y no método porque lleva parámetro de tipo, y Go no admite métodos
// genéricos. El `ctx` va PRIMERO porque lo exigen `revive/context-as-argument` y
// `contextcheck`.
func outcome[T any](ctx context.Context, w *Worker, job intake.ClaimedJob, stage string,
	start time.Time, out *T, err error) (*T, error) {
	elapsed := w.now().Sub(start)
	if err != nil {
		w.stumble(ctx, job, stage, elapsed, err)
		return nil, err
	}
	w.log.Info("pipeline: etapa completada",
		"job_id", job.ID, "stage", stage, "elapsed_ms", elapsed.Milliseconds(),
		"intento", job.Attempts+1)
	return out, nil
}

// resume (antes `reanudar`) es LA REANUDACIÓN POR ESTADO: si el artefacto de la etapa ya
// está en la fila, se decodifica y la etapa NO se ejecuta. Es lo que hace que una
// redelivery no vuelva a pagar los 22–32 s de una llamada que ya se pagó.
//
// # POR QUÉ `json.Unmarshal` Y NO EL `llm.Parse*` DE LA ETAPA
//
// Porque lo que hay en la fila NO es la salida cruda del modelo: es el artefacto que la
// etapa ya validó, ancló y volvió a serializar. Pasarlo otra vez por el parser de calidad
// haría que un artefacto legítimo —uno con `wants` vacío tras el anclaje, o uno cuyo
// `Parse*` se vuelva más estricto en una versión futura— se rechazara a sí mismo, y el job
// se quedaría reintentando para siempre una etapa que ya había salido bien.
//
// # UN ARTEFACTO ILEGIBLE NO MATA EL JOB: SE REHACE LA ETAPA
//
// Es lo conservador. La alternativa —fallar— tiraría un pedido real por un JSON que
// nosotros mismos escribimos mal, y rehacerla como mucho cuesta una llamada. El aviso va a
// Warn porque, si aparece, algo escribió en `artifacts` una forma que este código no
// entiende, y eso hay que mirarlo.
func resume[T any](w *Worker, job intake.ClaimedJob, stage string) (*T, bool) {
	raw, ok := job.Artifacts[stage]
	if !ok || len(raw) == 0 {
		return nil, false
	}
	var art T
	if err := json.Unmarshal(raw, &art); err != nil {
		// El error NO cita `raw`: el artefacto lleva frases del cliente (ADR-0034).
		w.log.Warn("pipeline: el artefacto persistido no se pudo decodificar; la etapa se rehace",
			"job_id", job.ID, "stage", stage, "error", err.Error())
		return nil, false
	}
	w.log.Debug("pipeline: etapa saltada, su artefacto ya estaba persistido",
		"job_id", job.ID, "stage", stage)
	return &art, true
}

// stumble (antes `tropiezo`) es la decisión backoff-vs-failed, y el ÚNICO sitio donde se
// toma.
//
// `ErrJobNotProcessing` se aparta antes que nada: no es un tropiezo nuestro, es que otro
// worker terminó el job mientras esta cadena corría. Intentar Retry o Fail sobre él
// afectaría 0 filas y el log diría «no aplicó» sin explicar por qué. Se dice lo que pasó y
// se suelta.
//
// El orden importa: job que se movió, job inválido, parada del worker (D-F7-10) y, solo
// entonces, la curva de reintentos.
func (w *Worker) stumble(ctx context.Context, job intake.ClaimedJob, stage string,
	elapsed time.Duration, err error) {
	if errors.Is(err, stages.ErrJobNotProcessing) {
		w.log.Info("pipeline: el job se movió bajo los pies (ya no estaba en processing); esta cadena lo suelta",
			"job_id", job.ID, "stage", stage, "elapsed_ms", elapsed.Milliseconds())
		return
	}
	cause := causeOf(err)
	if cause == CauseInvalidJob {
		// NI UNA VEZ. Es la definición de la causa: el job no puede salir bien por muchas
		// veces que se intente. No hay techo que consultar porque no hay curva.
		w.kill(ctx, job, stage, cause, err)
		return
	}
	if ctx.Err() != nil {
		// ✎ D-F7-10: el worker se está apagando y el error es el eco de esa parada, no un
		// fallo del job. Cobrarle el intento era castigar al pedido por un despliegue; y si
		// era el último, matarlo con una línea a ERROR. Se devuelve SIN castigo. `Release`
		// deja el job reclamable en el acto, y aquí es correcto por lo mismo que en sus
		// otros dos llamantes: con el ctx muerto, el bucle sale sin volver a reclamar.
		// Va DESPUÉS del job inválido: ese no mejora por reintentar, se apague o no.
		w.releaseUnpunished(ctx, job, "el worker se apagó durante una etapa")
		return
	}

	attempt := job.Attempts + 1
	ceiling := w.cfg.ceilingOf(cause)
	if attempt >= ceiling {
		w.kill(ctx, job, stage, cause, fmt.Errorf("agotados los %d intentos: %w", ceiling, err))
		return
	}

	mark := w.now().Add(backoffFor(attempt, w.cfg.BackoffBase, w.cfg.BackoffCap))
	closing, cancel := w.closingCtx(ctx)
	defer cancel()
	applied, rerr := w.store.Retry(closing, job.ID, mark)
	if rerr != nil {
		// 🔴 SE DICE, y en Error. Si esto falla, el job se queda en `processing` para
		// siempre y no lo rescata nadie (ver closingCtx). Un fallo silencioso aquí sería un
		// job perdido sin una sola línea de log.
		w.log.Error("pipeline: no se pudo reencolar el job con backoff; queda en processing",
			"job_id", job.ID, "stage", stage, "causa", cause, "error", rerr.Error())
		return
	}
	if !applied {
		w.log.Info("pipeline: el reencolado no aplicó (el job ya no estaba en processing)",
			"job_id", job.ID, "stage", stage, "causa", cause)
		return
	}
	w.log.Warn("pipeline: la etapa falló; el job vuelve a la cola con backoff",
		"job_id", job.ID, "stage", stage, "causa", cause,
		"elapsed_ms", elapsed.Milliseconds(),
		"intento", attempt, "tope", ceiling,
		"next_attempt_at", mark.UTC().Format(time.RFC3339),
		"error", err.Error())
}

// kill (antes `matar`) lleva el job a `failed` con su causa escrita.
//
// 🔴 EL `reason` ES TEXTO DE OPERADOR Y NO PUEDE LLEVAR LITERAL DEL CLIENTE (ADR-0034). Lo
// que se compone son la causa, la etapa y el error de la cadena — y los errores de este
// pipeline están escritos para no citar ni el prompt ni la salida del modelo.
func (w *Worker) kill(ctx context.Context, job intake.ClaimedJob, stage, cause string, err error) {
	reason := fmt.Sprintf("causa=%s stage=%s: %v", cause, stageOrNone(stage), err)
	closing, cancel := w.closingCtx(ctx)
	defer cancel()
	applied, ferr := w.store.Fail(closing, job.ID, reason)
	if ferr != nil {
		w.log.Error("pipeline: no se pudo marcar el job como failed; queda en processing",
			"job_id", job.ID, "stage", stage, "causa", cause, "error", ferr.Error())
		return
	}
	if !applied {
		w.log.Info("pipeline: el fallo no aplicó (el job ya no estaba en processing)",
			"job_id", job.ID, "stage", stage, "causa", cause)
		return
	}
	w.log.Error("pipeline: job FAILED",
		"job_id", job.ID, "stage", stageOrNone(stage), "causa", cause,
		"intento", job.Attempts+1, "error", err.Error())
}

// stageOrNone (antes `etapaOTodas`) nombra la etapa en un mensaje. La cadena vacía
// significa «murió antes de entrar en ninguna» (el job sin literal), y decir `stage=""` en
// un log obligaría a quien lo lea a adivinar si es eso o si alguien se dejó el campo.
func stageOrNone(stage string) string {
	if stage == "" {
		return "ninguna"
	}
	return stage
}

// finish (antes `terminar`) cierra el job en `done` CON el id del borrador que acaba de
// nacer.
//
// 🔴 ES EL ÚNICO SITIO DONDE EL `intake_id` PUEDE ENTRAR, y por eso viaja hasta aquí desde
// `draft` en vez de escribirlo la etapa: `done` es absorbente, así que un UPDATE posterior
// afectaría 0 filas y el job quedaría terminado sin apuntar a su solicitud — sin error, y
// sin nada que lo dijera.
//
// ⚠️ INV-13 SIGUE MORDIENDO IGUAL: `Finish` vacía el sobre del literal en la misma
// sentencia, así que un job `done` ya no tiene texto que reanalizar. Lo que le queda son
// sus cinco artefactos y su solicitud, que es lo que un `/reanalyze` necesita para
// escribir una revisión más sobre el MISMO borrador.
func (w *Worker) finish(ctx context.Context, job intake.ClaimedJob, draft *stages.DraftArtifact) {
	intakeID := ""
	if draft != nil {
		intakeID = draft.IntakeID
	}
	if intakeID == "" {
		// 🔴 ESTO ES UNA ANOMALÍA, no el caso normal. Se dice porque `Finish` hace
		// `COALESCE`: con la cadena entera corrida, un id vacío cierra el job igual y en
		// silencio, y la bandeja se quedaría sin la solicitud sin que nada fallara. La
		// causa más probable es un artefacto `draft` persistido por una versión anterior y
		// recuperado por `resume`.
		w.log.Warn("pipeline: el job termina SIN intake_id; su solicitud no quedará enlazada al job",
			"job_id", job.ID, "tenant_id", job.Key.TenantID,
			"donde_mirar", "artifacts.draft de este job_id: si no trae `intake_id`, lo escribió una versión anterior a T3.8")
	}
	closing, cancel := w.closingCtx(ctx)
	defer cancel()
	applied, err := w.store.Finish(closing, job.ID, intakeID)
	if err != nil {
		w.log.Error("pipeline: no se pudo terminar el job; queda en processing",
			"job_id", job.ID, "error", err.Error())
		return
	}
	if !applied {
		w.log.Info("pipeline: el cierre no aplicó (el job ya no estaba en processing)",
			"job_id", job.ID)
		return
	}
	w.log.Info("pipeline: job DONE",
		"job_id", job.ID, "tenant_id", job.Key.TenantID,
		"intake_id", intakeID, "intento", job.Attempts+1)
}

// closingCtx (antes `cierre`) da el ctx con el que se escriben los DESENLACES (Finish,
// Fail, Retry, Release).
//
// 🔴 SOBREVIVE A LA CANCELACIÓN DEL WORKER, `context.WithoutCancel`, y ese detalle es lo
// que evita el modo de fallo peor de todo este paquete: con el ctx del llamante, un apagado
// (o un despliegue) durante una etapa cancelaría también la escritura del desenlace, y el
// job se quedaría en `processing` PARA SIEMPRE — el reclamo solo mira `pending`, así que
// nadie lo volvería a tocar y el cliente se quedaría sin presupuesto sin que nada diera
// error. Con esto, el apagado ordenado deja el job reencolado.
//
// ⚠️ LO QUE ESTO NO CUBRE: un SIGKILL o una caída dura del proceso sí deja el job en
// `processing` sin rescate. `intake_jobs` no tiene el `claimed_at` que `webhook_outbox`
// usa para recuperar huérfanos, y añadirlo es una migración con una decisión dentro
// —cuánto puede durar legítimamente un `processing`—. Con el tope en 10 ítems y
// CallTimeoutFloor en 48 s la cadena tiene un techo aritmético (≈ 9 min de peor caso), y
// con el aforo N cadenas del mismo Edge se ponen en fila: quien decida ese `claimed_at`
// tiene que contar la espera, no solo la cadena.
func (w *Worker) closingCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
}
