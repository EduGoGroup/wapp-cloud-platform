// Porta internal/flujos/runtime/aggregator.go @ e0159171

package runtime

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Trozo de aggregator.go (E-13): el barrido. Sweep y RecoverAtBoot, los dos plazos de la
// ventana híbrida, el cierre CON su sobre —se compone antes y se cierra en una sentencia
// (D-F7-9, D-F8-13)— y la parada sin ERROR (D-F9-10).

// RecoverAtBoot es la RECUPERACIÓN DEL REINICIO (T1.1, AG-6): las ventanas que
// vencieron mientras no había proceso pasan a `pending`. Es literalmente UN Sweep, y
// eso es el diseño: el estado de una ventana vive en `intake_jobs`, así que arrancar
// no es «restaurar» nada, es mirar la tabla. Devuelve cuántas cerró.
//
// Si cerró alguna deja en Info "agregador: ventanas vencidas cerradas al arrancar"
// con la clave "jobs" (el número); si no cerró ninguna no loguea nada.
func (s *IntakeAggregator) RecoverAtBoot(ctx context.Context) int {
	n := s.Sweep(ctx)
	if n > 0 {
		s.log.Info("agregador: ventanas vencidas cerradas al arrancar", "jobs", n)
	}
	return n
}

// Sweep cierra las ventanas a las que les tocó y devuelve cuántas cerró ESTA llamada.
// Corre FUERA del camino del entrante, que es lo que le permite leer `intake_jobs` y
// `tenant_settings` sin violar D-044.26. Sobre un receptor nil, o sin jobs o sin log,
// devuelve 0 sin tocar nada.
//
// # AG-3 · La ventana de silencio es el camino PRINCIPAL y el único garantizado
//
// Una pasada pide al almacén hasta `batch` ventanas vivas (ListAggregating, UNA vez),
// lee el reloj UNA vez y cierra cada ventana que cumpla CUALQUIERA de tres
// condiciones (ventana HÍBRIDA, T1.8-1, D-044.43):
//
//  1. EL ADELANTO: hay una pista de OnClassified para su Key. Es un atajo.
//  2. EL SILENCIO: now >= LastActivity + aggregation_window (45 s por defecto). Se
//     mide desde el ÚLTIMO mensaje: una ráfaga tecleada despacio (t=0, 30, 60) es UNA
//     ventana que cierra a los 105 s, no dos jobs.
//  3. EL TECHO: now >= CreatedAt + aggregation_max (120 s por defecto). Es la red que
//     impide que el silencio no venza nunca: una conversación que gotea cada 40 s
//     cierra a los 120 s. El peor caso a primer borrador es este techo + el pipeline.
//
// Sin ningún intent —que es un caso normal, no una avería— la ventana cierra igual por
// 2 o por 3. El job resultante es INDISTINGUIBLE por los tres caminos: no lleva marca
// de por qué se disparó (T1.7 (d)).
//
// Las dos anclas (LastActivity y CreatedAt) son del reloj del ALMACÉN, nunca el
// MessageTS del cliente: restar dos relojes distintos cerraría ventanas antes de
// tiempo o nunca, sin error.
//
// # Los plazos salen de la config del tenant, tal cual
//
// Se leen con settings.GetTenantSettings, UNA vez por tenant y por pasada (aunque el
// tenant tenga N ventanas), y solo si hace falta (una ventana con pista no necesita
// plazos). No se cachean entre pasadas: un cambio de config se ve en la siguiente.
//
//   - 🔴 El 0 de CUALQUIERA de los dos es un override explícito y se respeta, no se
//     sustituye por el default: aggregation_window <= 0 es «flush inmediato» (cierra en
//     el primer barrido que la vea) y aggregation_max <= 0 es «vencido siempre» —NO
//     «sin techo», que no existe a propósito—.
//   - Si la lectura falla, o no hay settings, valen los defaults de plataforma
//     (store.DefaultAggregationWindow y store.DefaultAggregationMax) y NO «no cerrar»:
//     una config ilegible no puede dejar ventanas abiertas para siempre. El fallo deja
//     en Warn "agregador: no se pudieron leer los plazos de la ventana
//     (aggregation_window_seconds/aggregation_max_seconds); se usan los defaults de
//     plataforma", con "error" y "tenant_id".
//
// # AG-6 · Cierre idempotente, sin estado en memoria
//
// Quien cierra es jobs.CloseWithSourceText, cuyo guard de estado hace que dos barridos
// solapados —o dos procesos— no puedan producir dos jobs. Si contesta false sin error
// —otro llegó antes, o la ventana CAMBIÓ mientras se componía (entró un mensaje)—, la
// ventana NO se cuenta y no es un error: si sigue viva, la recoge el barrido siguiente.
// Un segundo Sweep sin que nada cambie devuelve 0 y no toca ninguna fila (una ventana ya
// cerrada no sale en ListAggregating, así que tampoco se vuelve a componer).
//
// # Las pistas
//
// Cada pasada se lleva TODAS las pistas acumuladas y las consume, casen o no con una
// ventana de la pasada: la de una ventana ya cerrada se tira; la de una ventana que el
// `batch` dejó fuera se pierde y esa ventana cierra por su reloj. Si listar falla, las
// pistas NO se consumen.
//
// # El compositor (T1.4, AG-8): PRIMERO el sobre, DESPUÉS el cierre, en una sentencia
//
// Por CADA ventana vencida, y solo por esas, llama UNA vez a compose.Compose(ctx, Key)
// ANTES de cerrarla, y cierra con el sobre que devuelve (jobs.CloseWithSourceText): la
// transición `aggregating → pending` y las tres columnas del sobre son UNA sentencia.
// Un sobre vacío (hilo sin mensajes, o sin compositor cableado) cierra igual, con el
// sobre a NULL. Si el compositor FALLA, la ventana se cierra SIN sobre con el
// jobs.CloseWindow de siempre: cuenta como cerrada y el job queda `pending` con el sobre
// vacío, como en el viejo.
//
// ✅ D-F7-9 (hallazgo 17 de F7) ARREGLADA en el binario nuevo (F8-06b, D-F8-13). El viejo
// hace PRIMERO la transición (aggregator.go:892) y DESPUÉS compone y escribe el sobre
// (aggregator.go:906 → source_composer.go:347-377), en sentencias separadas: en ese hueco
// el worker puede reclamar un job `pending` sin `source_text` y lo deja `failed`. Aquí
// ese hueco no existe: una ventana cuyo compositor devolvió sobre nunca es visible
// cerrada y sin él. Lo fija el proceso P4 (ningún job `failed` por falta de literal). El
// viejo conserva la carrera hasta su borrado en F10. Ver closeWindow.
//
// # Logs
//
//   - listar falla: Error "agregador: no se pudieron listar las ventanas vivas", con
//     "error"; devuelve 0.
//   - cerrar falla: Error "agregador: no se pudo cerrar la ventana de captación", con
//     "error", "tenant_id", "session_id" y "job_id"; esa ventana no cuenta y la pasada
//     sigue con las demás.
//   - el compositor falla: Error "agregador: la ventana se cerró pero el literal no se
//     pudo componer (T1.4)", con "error", "tenant_id" y "job_id" — solo si la ventana
//     se cerró (sin sobre) en esta llamada.
//   - el cierre contesta false sin error: nada, en ningún nivel.
//   - cada ventana cerrada: Debug "agregador: ventana cerrada", con "tenant_id",
//     "session_id" y "job_id".
//
// Los tres Error callan si ctx ya está cancelado (D-F9-10, ver Run).
func (s *IntakeAggregator) Sweep(ctx context.Context) int {
	if s == nil || s.jobs == nil || s.log == nil {
		return 0
	}
	open, err := s.jobs.ListAggregating(ctx, s.sweepBatch)
	if err != nil {
		s.logSweepError(ctx, "agregador: no se pudieron listar las ventanas vivas", "error", err)
		return 0
	}
	hints := s.takeHints()
	now := s.now()
	// Memo POR PASADA de los plazos de cada tenant: una ráfaga deja N ventanas del
	// mismo tenant y no hace falta preguntar N veces. Se descarta al terminar la
	// pasada a propósito — un cambio de config se ve en el barrido siguiente, no al
	// reiniciar.
	//
	// 🔴 GUARDA LOS DOS PLAZOS JUNTOS Y ES UNA SOLA ENTRADA, no dos memos ni dos
	// lecturas: `GetTenantSettings` devuelve el struct entero, así que separar silencio
	// y techo en dos cachés duplicaría las consultas a `tenant_settings` sin ganar nada
	// —y el presupuesto del barrido está medido: UNA lectura por pasada y por tenant.
	deadlines := make(map[string]windowDeadlines, 4)
	closed := 0
	for _, job := range open {
		if !s.due(ctx, job, hints, deadlines, now) {
			continue
		}
		if s.closeWindow(ctx, job) {
			closed++
		}
	}
	return closed
}

// logSweepError registra en ERROR un fallo del barrido —listar, cerrar o componer—
// SALVO que el contexto ya esté cancelado.
//
// Divergencia deliberada del viejo (D-F9-10): el viejo registraba los tres sin mirar
// (aggregator.go:741, :894 y :907). Con el proceso apagándose, que una llamada a la
// base o al compositor devuelva "context canceled" no es una avería que alguien tenga
// que mirar, y un arranque-parada limpio no puede dejar líneas a ERROR. Con el contexto
// VIVO esos mismos fallos SÍ van a ERROR. No calla el Warn de los plazos ni corta la
// pasada a medias: solo deja de llamar avería a la parada.
func (s *IntakeAggregator) logSweepError(ctx context.Context, msg string, args ...any) {
	if ctx.Err() != nil {
		return
	}
	s.log.Error(msg, args...)
}

// windowDeadlines son los DOS números que gobiernan el cierre de un tenant. Van
// juntos en un tipo y no sueltos porque son UNA regla: la ventana cierra por lo que
// llegue antes, así que leer uno sin el otro no responde ninguna pregunta.
type windowDeadlines struct {
	// silence es `aggregation_window_seconds` (45 s por defecto): cuánto se espera
	// desde el ÚLTIMO mensaje del cliente.
	silence time.Duration
	// ceiling es `aggregation_max_seconds` (120 s por defecto): cuánto se espera como
	// mucho desde que la ventana NACIÓ, pase lo que pase con el silencio.
	ceiling time.Duration
}

// due decide si a una ventana le tocó. TRES caminos, y los tres se dicen enteros:
//
//  1. el ADELANTO por intent (la pista), que es un atajo y llega por EVENTO;
//  2. el SILENCIO —45 s desde el último mensaje—, que es el camino principal;
//  3. el TECHO —120 s desde que la ventana nació—, que es la red que impide que 2
//     no cierre nunca.
//
// # POR QUÉ HACEN FALTA LOS DOS PLAZOS Y NO UNO (T1.8-1, D-044.43)
//
// Hasta esa tarea había uno solo y anclaba en el PRIMER mensaje, así que:
//
//   - una ráfaga tecleada despacio (t=0, 30, 60) SE PARTÍA EN DOS JOBS: a los 45 s el
//     barrido cerraba lo que hubiera y el tercer mensaje abría otra ventana. El
//     presupuesto salía incompleto y nadie veía un error.
//
// Y la cura obvia —anclar solo en el silencio— arregla eso y rompe lo otro:
//
//   - una conversación que gotea cada 40 s NUNCA alcanza 45 s de silencio, así que su
//     ventana no cerraría JAMÁS. Un job en `aggregating` que nadie recoge es un pedido
//     perdido, otra vez sin error.
//
// ⇒ los dos, y gana el primero que venza. Quitar el techo pone rojo el test del goteo;
// devolver el silencio al ancla vieja (`message_ts`) pone rojo el de la ráfaga lenta.
//
// El orden de evaluación no cambia el resultado —una ventana vencida se cierra haya o
// no pista, y por cualquiera de los dos plazos—, y por eso el job resultante es
// INDISTINGUIBLE por los tres caminos: aquí no se anota en ningún sitio cuál ganó
// (T1.7 (d)).
func (s *IntakeAggregator) due(ctx context.Context, job intake.OpenJob,
	hints map[intake.WindowKey]struct{}, memo map[string]windowDeadlines, now time.Time) bool {
	if _, hinted := hints[job.Key]; hinted {
		return true
	}
	d, ok := memo[job.Key.TenantID]
	if !ok {
		d = s.deadlinesFor(ctx, job.Key.TenantID)
		memo[job.Key.TenantID] = d
	}
	return s.silenceExpired(job, d, now) || s.ceilingExpired(job, d, now)
}

// silenceExpired es el plazo desde el ÚLTIMO mensaje del cliente.
//
// 🔴 EL ANCLA ES `LastActivity`, QUE ES `intake_jobs.updated_at`, Y NO `message_ts`.
// `updated_at` lo escribe el `DO UPDATE … updated_at = now()` del UPSERT, o sea el
// reloj de POSTGRES en cada mensaje de la ráfaga; `message_ts` lo pone el Edge con el
// reloj del CLIENTE. Medir un plazo restando dos relojes distintos es un fallo
// permanente y silencioso: con el teléfono adelantado la ventana no cerraría hasta que
// el desfase pasara, y con el reloj atrasado cerraría de inmediato. Ver el docstring de
// intake.OpenJob.
//
// `silence <= 0` es el override explícito «flush inmediato» del tenant (CHECK >= 0 de
// la 0072): la ventana se cierra en el primer barrido que la vea. No es un valor
// degenerado ni un «sin configurar» — ver el COMMENT de la columna.
func (s *IntakeAggregator) silenceExpired(job intake.OpenJob, d windowDeadlines, now time.Time) bool {
	if d.silence <= 0 {
		return true
	}
	return !now.Before(job.LastActivity.Add(d.silence))
}

// ceilingExpired es el plazo desde que la ventana NACIÓ (`intake_jobs.created_at`, otra
// vez el reloj de Postgres).
//
// 🔴 `ceiling <= 0` SIGNIFICA «VENCIDO SIEMPRE», IGUAL QUE EL 0 DE LA VENTANA — y NO
// «sin techo». La decisión está razonada en el COMMENT de la columna (0076) y se
// repite aquí porque es donde muerde: leer el 0 como «sin techo» pondría a dos
// columnas vecinas a significar cosas opuestas con el mismo número, y además
// construiría el interruptor del defecto que esa tarea cerró. No existe forma de
// apagar el techo, y es deliberado.
//
// La guarda no es solo por el 0: `Add` de una duración negativa daría un instante en
// el pasado y cerraría igual, pero decirlo explícito es lo que impide que alguien
// «simplifique» la línea y se lleve por delante la lectura del override.
func (s *IntakeAggregator) ceilingExpired(job intake.OpenJob, d windowDeadlines, now time.Time) bool {
	if d.ceiling <= 0 {
		return true
	}
	return !now.Before(job.CreatedAt.Add(d.ceiling))
}

// deadlinesFor resuelve los DOS plazos del tenant con UNA sola lectura de
// `tenant_settings`. Un fallo de settings cae a los DEFAULTS DE PLATAFORMA (45 s / 120
// s) y NO a «no cerrar»: una config ilegible no puede dejar ventanas abiertas para
// siempre, que sería un presupuesto que nunca llega y un job en `aggregating` que nadie
// recoge.
//
// 🔴 SE DEVUELVEN LOS DOS VALORES TAL CUAL, SIN PARCHEAR CEROS. El 0 de cualquiera de
// las dos columnas es un override explícito del tenant, y un `if x == 0 { x = Default }`
// aquí lo apagaría sin que nadie se entere — el defecto que el repositorio de settings
// prohíbe por escrito para la ventana, aplicado ahora a dos columnas en vez de una.
func (s *IntakeAggregator) deadlinesFor(ctx context.Context, tenantID string) windowDeadlines {
	defaults := windowDeadlines{
		silence: store.DefaultAggregationWindow,
		ceiling: store.DefaultAggregationMax,
	}
	if s.settings == nil {
		return defaults
	}
	cfg, err := s.settings.GetTenantSettings(ctx, tenantID)
	if err != nil {
		s.log.Warn("agregador: no se pudieron leer los plazos de la ventana (aggregation_window_seconds/aggregation_max_seconds); se usan los defaults de plataforma",
			"error", err, "tenant_id", tenantID)
		return defaults
	}
	return windowDeadlines{silence: cfg.AggregationWindow, ceiling: cfg.AggregationMax}
}

// closeWindow compone el sobre de la ventana y la cierra CON él. Devuelve si la cerró
// ESTA llamada.
//
// Divergencia deliberada del viejo (D-F7-9, D-F8-13): el viejo CIERRA y luego COMPONE
// (dos sentencias: CloseWindow y, después, PutSourceText), y entre las dos el job es
// visible para el worker `pending` y sin sobre. Aquí se compone ANTES —leer el hilo y
// cifrar no tocan `intake_jobs`— y se cierra con el sobre en UNA sentencia
// (CloseWithSourceText), y SOLO si la ventana no cambió desde que el barrido la leyó: si
// entró un mensaje mientras se componía, el sobre ya no la representa, el store contesta
// false y la ventana sigue viva para el barrido siguiente, que la compone entera.
//
// Lo que se CONSERVA del viejo, a propósito (con duda manda el viejo):
//
//   - el compositor FALLA ⇒ la ventana se cierra igual, SIN sobre, por el CloseWindow de
//     siempre, y el fallo va a Error con el mismo literal: un hilo ilegible no puede
//     dejar una ventana en `aggregating` para siempre. El worker dejará ese job `failed`
//     por falta de literal, que aquí es la verdad y no una carrera;
//   - el hilo no tiene mensajes (sobre vacío, error nil) ⇒ cierra con el sobre a NULL.
//
// D-F9-10 sigue intacta: con el ctx cancelado ninguna de las ramas loguea a ERROR
// (logSweepError). Y aquí NO se toca `seen` (AG-5, trampa T-10): ver el final de
// aggregator_observe.go.
func (s *IntakeAggregator) closeWindow(ctx context.Context, job intake.OpenJob) bool {
	// EL PUNTO DE EXTENSIÓN DE T1.4, y el único sitio desde donde se llama. Va ANTES
	// de la transición: lo que se cierra ya lleva su sobre.
	env, cerr := s.compose.Compose(ctx, job.Key)

	var (
		closed bool
		err    error
	)
	if cerr == nil {
		closed, err = s.jobs.CloseWithSourceText(ctx, job, env)
	} else {
		closed, err = s.jobs.CloseWindow(ctx, job.Key)
	}
	if err != nil {
		s.logSweepError(ctx, "agregador: no se pudo cerrar la ventana de captación",
			"error", err, "tenant_id", job.Key.TenantID, "session_id", job.Key.SessionID, "job_id", job.ID)
		return false
	}
	if !closed {
		// Otro barrido (u otro proceso) llegó antes, o la ventana cambió mientras se
		// componía. No es un error y no se reintenta aquí: el guard del store ya
		// garantizó que hay UN job y no dos, y una ventana que sigue viva sale en el
		// siguiente ListAggregating. No se loguea nada, en ningún nivel.
		return false
	}
	if cerr != nil {
		s.logSweepError(ctx, "agregador: la ventana se cerró pero el literal no se pudo componer (T1.4)",
			"error", cerr, "tenant_id", job.Key.TenantID, "job_id", job.ID)
	}
	s.log.Debug("agregador: ventana cerrada",
		"tenant_id", job.Key.TenantID, "session_id", job.Key.SessionID, "job_id", job.ID)
	return true
}
