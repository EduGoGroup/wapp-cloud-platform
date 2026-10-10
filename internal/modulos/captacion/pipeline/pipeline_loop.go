// Porta internal/intake/pipeline/pipeline.go @ 56097aa (el bucle: Despertar, Run y los drenajes)

package pipeline

import (
	"context"
)

// pipeline_loop.go — EL BUCLE del worker: cuándo se pregunta por trabajo (el tic, el
// flanco a READY) y cuánto se drena cada vez. Lo que le pasa a UN job una vez reclamado
// está escrito en RunOnce, que es su contrato; su código vive en `pipeline_chain.go` y
// `pipeline_outcome.go`.

// Wake (antes `Despertar`) es EL DISPARADOR POR EVENTO de la cadena de lote (D-044.43): el
// Edge `edgeID` del tenant `tenantID` acaba de pasar a READY, así que los jobs `pending`
// de ese tenant se reanudan EN EL ACTO, sin esperar a que venza su backoff (ver
// DrainAwake, que es lo que Run hace con cada aviso).
//
// 🔴 NO BLOQUEA NUNCA, y esa es su única exigencia de contrato. Lo llama el hook
// `OnEdgeReady` del gateway, que corre INLINE en la goroutine del bucle Recv del stream:
// bloquear ahí pararía la recepción de TODOS los frames de ese Edge.
//
//   - El buzón guarda hasta 32 avisos. Lleno ⇒ el aviso se DESCARTA y se dice en Debug
//     («pipeline: aviso de Edge READY descartado (buzón lleno); lo recogerá el backoff»,
//     con `tenant_id` y `edge_id`): el barrendero sigue siendo el backoff.
//   - Una dirección a medias (`tenantID` o `edgeID` vacío) se descarta sin tocar el buzón
//     y sin log.
//   - Es seguro llamarlo desde cualquier goroutine y antes de que Run arranque: los avisos
//     esperan en el buzón.
func (w *Worker) Wake(tenantID, edgeID string) {
	s := Slot{TenantID: tenantID, EdgeID: edgeID}
	if !s.Valid() {
		return
	}
	select {
	case w.wakes <- s:
	default:
		w.log.Debug("pipeline: aviso de Edge READY descartado (buzón lleno); lo recogerá el backoff",
			"tenant_id", tenantID, "edge_id", edgeID)
	}
}

// Run bloquea hasta que `ctx` se cancele. Se arranca con `go w.Run(ctx)` sobre el MISMO
// ctx que cierra el resto del proceso: un solo Ctrl+C también para el worker. Se lanza UNA
// vez por proceso (R-01; ver la cabecera del paquete).
//
// En este orden:
//
//  1. Crea el ticker con la cadencia de Config (WithTicker) y lo para al volver.
//  2. Deja el Info «pipeline: worker arrancado» con `cadencia`, `max_intentos_calidad`,
//     `max_intentos_infra`, `backoff_base`, `backoff_tope` (los valores YA resueltos) y
//     `aforo_por_edge` (si hay aforo).
//  3. Sin aforo, un Warn «pipeline: worker SIN aforo por Edge (T2.7); dos cadenas de lote
//     del mismo Edge pueden solaparse»; sin lector de zonas, un Warn «pipeline: worker SIN
//     lector de zonas de envío (T3.8); todo borrador saldrá con la línea de envío SIN
//     precio». Los dos llevan `consecuencia`. 🔴 Van en el arranque y en Warn porque
//     ninguna de las dos ausencias da otro síntoma: el sistema no falla, sirve peor y en
//     silencio.
//  4. Drena una vez (Drain) sin esperar al primer tic.
//  5. Y entonces atiende, hasta que `ctx` muera: cada tic ⇒ Drain; cada aviso de Wake ⇒
//     DrainAwake con esa dirección.
//  6. Con `ctx` cancelado deja el Info «pipeline: worker apagando (contexto cancelado)» y
//     vuelve. No lanza goroutines propias: no deja ninguna detrás.
//
// 🔴 EL TICKER NO ES EL BACKOFF. Marca cada cuánto se PREGUNTA si hay trabajo; el castigo
// de un job concreto vive en su `next_attempt_at`, en la base. Un worker que durmiera el
// castigo aquí dejaría parados también a los jobs sanos.
//
// # La parada no es un error (D-F9-10)
//
// Contexto cancelado → Run vuelve SIN loguear a ERROR. Si la cancelación corta un reclamo
// (el de Drain o el de DrainAwake) y el store devuelve error, ese error NO se registra: no
// es una avería, es el proceso apagándose, y un arranque-parada limpio no puede dejar
// líneas a ERROR. (El worker viejo sí lo registraba: es la única conducta que cambia
// frente a él.) Con el contexto vivo, el mismo fallo SÍ va a ERROR. Los DESENLACES no
// entran en esta regla: se escriben con un contexto que sobrevive a la cancelación (ver
// RunOnce), así que si fallan es una avería de verdad y se dice.
func (w *Worker) Run(ctx context.Context) {
	ticks, stop := w.newTicker(w.cfg.Cadence)
	defer stop()

	w.log.Info("pipeline: worker arrancado",
		"cadencia", w.cfg.Cadence.String(),
		"max_intentos_calidad", w.cfg.MaxQualityAttempts,
		"max_intentos_infra", w.cfg.MaxInfraAttempts,
		"backoff_base", w.cfg.BackoffBase.String(),
		"backoff_tope", w.cfg.BackoffCap.String(),
		"aforo_por_edge", w.capacity != nil)

	// 🔴 EL AVISO VA EN EL ARRANQUE Y EN Warn PORQUE UN AFORO AUSENTE NO DA NINGÚN OTRO
	// SÍNTOMA. Sin él, el sistema no falla: sirve, y de vez en cuando dos cadenas del
	// mismo Edge se pisan y un turno interactivo espera el doble. Eso no deja rastro en
	// ningún log, así que el rastro se pone aquí.
	if w.capacity == nil {
		w.log.Warn("pipeline: worker SIN aforo por Edge (T2.7); dos cadenas de lote del mismo Edge pueden solaparse",
			"consecuencia", "la espera de un turno interactivo deja de estar acotada a UNA llamada de lote")
	}
	// 🔴 MISMO MOTIVO QUE EL DE ARRIBA: un lector de zonas ausente no falla, sirve peor y
	// en silencio.
	if w.zones == nil {
		w.log.Warn("pipeline: worker SIN lector de zonas de envío (T3.8); todo borrador saldrá con la línea de envío SIN precio",
			"consecuencia", "el tenant con UNA zona configurada pierde su tarifa plana y el dueño la precifica a mano sin saber que ya estaba puesta")
	}

	w.Drain(ctx)
	for {
		select {
		case <-ctx.Done():
			w.log.Info("pipeline: worker apagando (contexto cancelado)")
			return
		case s := <-w.wakes:
			// El flanco a READY: se atiende ANTES de que venza ningún backoff, que es todo
			// el propósito. Ver DrainAwake.
			w.DrainAwake(ctx, s)
		case <-ticks:
			w.Drain(ctx)
		}
	}
}

// DrainAwake (antes `DrenarDespierto`) atiende un flanco a READY: procesa los jobs
// `pending` del tenant de `s` SIN MIRAR SU BACKOFF (reclama con
// `ClaimNextIgnoringBackoff(s.TenantID)`), cada uno hasta su desenlace como en RunOnce, y
// devuelve cuántos procesó.
//
//   - Deja al entrar el Info «pipeline: el Edge acaba de poder servir inferencia; se
//     reanudan sus jobs sin esperar al backoff» con `tenant_id` y `edge_id`.
//   - 🔴 CADA JOB SE PROCESA COMO MUCHO UNA VEZ POR FLANCO. Si el reclamo devuelve un job
//     que este mismo flanco ya procesó (tropezó y volvió a la cola), el flanco terminó: el
//     job se devuelve SIN castigo (`Release`: ni se le cobra intento ni se le mueve la
//     marca; el backoff que le puso su propio tropiezo sigue mandando) y la función
//     vuelve. Ese job NO cuenta dos veces.
//   - Para cuando no queda nada que reclamar, y en cuanto `ctx` muere.
//   - Si el reclamo falla, deja un Error «pipeline: no se pudo reclamar trabajo tras el
//     flanco a READY» (con `tenant_id`, `edge_id` y `error`) y vuelve con lo procesado
//     hasta ahí. Con `ctx` ya cancelado ese Error NO se emite (D-F9-10, ver Run).
//
// # POR QUÉ EL RECLAMO ES POR TENANT SI LA PLAZA ES POR EDGE — Y NO ES UNA INCOHERENCIA
//
// 🔴 `intake_jobs` NO TIENE COLUMNA `edge_id`: el job no nace de un Edge, nace de una
// VENTANA de conversación. «Los jobs pending de ESE EDGE se reanudan» no es escribible tal
// cual, y esto es lo más cercano que sí lo es. No es una aproximación floja: el enrutado
// manda el job de un tenant al Edge que esté VIVO, así que «los jobs que este Edge acaba
// de desbloquear» y «los jobs pendientes de este tenant» coinciden siempre que el tenant
// tenga un Edge. Donde no coincidan, el que sobra se encuentra la plaza ocupada y espera.
// El `edge_id` del flanco NO se tira: viaja al log, que es donde un operador necesita leer
// qué máquina desatascó qué cola.
//
// # POR QUÉ HAY UN CONJUNTO DE VISTOS Y NO SE PUEDE QUITAR
//
// 🔴 PORQUE ESTE BUCLE NO TIENE OTRO FRENO. Drain termina porque un job que tropieza sale
// con su `next_attempt_at` en el futuro y deja de ser reclamable; aquí el reclamo IGNORA
// esa marca a propósito, así que un job que falle se reencolaría castigado y el reclamo
// volvería a llevárselo EN EL ACTO, a la velocidad del error. (Medido: sin el conjunto,
// un job consume su techo entero de intentos en un solo flanco y muere.)
func (w *Worker) DrainAwake(ctx context.Context, s Slot) int {
	w.log.Info("pipeline: el Edge acaba de poder servir inferencia; se reanudan sus jobs sin esperar al backoff",
		"tenant_id", s.TenantID, "edge_id", s.EdgeID)

	seen := make(map[string]struct{})
	n := 0
	for ctx.Err() == nil {
		job, found, err := w.store.ClaimNextIgnoringBackoff(ctx, s.TenantID)
		if err != nil {
			w.logClaimError(ctx, "pipeline: no se pudo reclamar trabajo tras el flanco a READY",
				"tenant_id", s.TenantID, "edge_id", s.EdgeID, "error", err)
			return n
		}
		if !found {
			return n
		}
		if _, repeated := seen[job.ID]; repeated {
			// Ya se procesó en ESTE flanco y volvió a la cola: el flanco terminó su
			// trabajo. Se suelta sin castigo —no ha fallado nada nuevo— y el backoff que
			// le puso su propio tropiezo sigue mandando.
			w.releaseUnpunished(ctx, job, "ya procesado en este mismo flanco a READY")
			return n
		}
		seen[job.ID] = struct{}{}
		w.process(ctx, job)
		n++
	}
	return n
}

// Drain (antes `Drenar`) procesa jobs (RunOnce) hasta que la cola se queda sin nada
// reclamable, y devuelve cuántos procesó. Es lo que hace que un backlog no tarde
// `n × cadencia` en salir.
//
//   - 🔴 PARA EN CUANTO `ctx` MUERE: con el ctx ya cancelado no reclama nada. Sin esa
//     comprobación un apagado con la cola llena se quedaría atrapado procesando jobs.
//   - Si el reclamo falla, deja un Error «pipeline: no se pudo reclamar trabajo» (con
//     `error`) y vuelve con lo procesado hasta ahí. Con `ctx` ya cancelado ese Error NO se
//     emite (D-F9-10, ver Run).
//
// ⚠️ NO HAY TECHO DE VUELTAS, Y ESO SIGNIFICA QUE **LA TERMINACIÓN DE ESTE BUCLE DESCANSA
// ENTERA EN EL BACKOFF**. Un job que tropieza sale de aquí con su `next_attempt_at` en el
// futuro y deja de ser reclamable; si alguien rompiera esa mitad —devolver el job con
// `Release` en vez de con `Retry`—, Drain giraría para siempre reclamando y fallando el
// mismo job (medido: cuelga el test hasta el `-timeout`). No se pone un techo aquí porque
// convertiría un backlog legítimo de N jobs en N/techo pasadas.
func (w *Worker) Drain(ctx context.Context) int {
	n := 0
	for ctx.Err() == nil {
		found, err := w.RunOnce(ctx)
		if err != nil {
			w.logClaimError(ctx, "pipeline: no se pudo reclamar trabajo", "error", err)
			return n
		}
		if !found {
			return n
		}
		n++
	}
	return n
}

// RunOnce (antes `UnaVuelta`) reclama UN job (`ClaimNext`: el `pending` cuyo backoff ya
// venció) y lo lleva hasta su desenlace. Devuelve `false` sin error cuando no había nada
// que reclamar, que es el estado normal del worker la mayor parte del tiempo.
//
// El error que devuelve es SOLO el del reclamo. Los fallos del job NO salen por aquí,
// porque no son fallos del worker: se resuelven dentro (backoff o `failed`) y el resultado
// es `(true, nil)`. Devolverlos haría que un job envenenado parase el drenaje.
//
// # Lo que le pasa a un job reclamado
//
// Siempre acaba en uno de cuatro sitios, y ninguno deja el job en `processing`: `done`,
// `pending` con castigo, `failed`, o nada (ya lo terminó otro).
//
//  1. **El sobre.** Incompleto ⇒ `failed` sin reintento, CauseInvalidJob, con el texto
//     «(el compositor del flush no llegó a escribir el sobre)» (D-F7-9: la conducta se
//     conserva; la carrera de la ventana que la disparaba está arreglada en F8-06b y la del
//     re-análisis, pendiente de T8.40 — ver la cabecera del paquete). Entero pero no
//     descifra ⇒ tropiezo de CauseInfra («descifrar el literal del job: …»; puede ser un KMS
//     caído). Descifra a cadena vacía
//     ⇒ `failed` sin reintento («(el sobre descifró a cadena vacía)»). En los tres casos
//     NO se llama a ninguna etapa: un prompt sin texto del cliente tira 22–32 s de la
//     plaza única para nada.
//  2. **La plaza** (solo con WithCapacity), DESPUÉS de abrir el sobre y ANTES de la
//     cadena, y se suelta al acabar el job, salga como salga: el entero cuenta CADENAS de
//     lote, no peticiones. La dirección es `(job.Key.TenantID, edge)` con el Edge que
//     diga `Slots.PlazaDe(tenant, job.Key.SessionID)`. Si está ocupada el job ESPERA: no
//     falla, no se le cobra intento. Solo cuando YA hay otras cadenas esperando se dice,
//     en un Info «…la plaza del Edge está ocupada; este job ESPERA…» con `job_id`, `plaza`
//     y `esperando` (emitirlo siempre lo volvería ruido). Tres caminos SIN plaza, y
//     ninguno para el job: no hay aforo; `ok = false` o dirección a medias (vía API, o sin
//     Edge vivo; un Debug); y `PlazaDe` con error (un Warn «…la cadena sigue SIN aforo»).
//     Si `ctx` muere ESPERANDO plaza, el job vuelve a `pending` SIN castigo (`Release`) y
//     no se llama a ninguna etapa.
//  3. **La cadena**, en orden: P2 → P3 → P4 → match → draft. Cada etapa recibe lo que dejó
//     la anterior: P3 las ideas de P2; P4 los ítems de P3 y la pista de entrega de P2;
//     `match` las cantidades de P4, el índice del catálogo (leído UNA vez por job, con
//     `job.Key.TenantID`), las zonas de envío y `stages.NoOrderNote`; `draft` el artefacto
//     del match, el literal descifrado, la fecha de entrega de P4, y `Media` y `Analysis`
//     en cero. El ctx de las etapas NO lleva plazo: el plazo es por llamada y lo pone cada
//     etapa.
//  4. **Reanudación por estado**: la etapa cuyo artefacto ya está en `job.Artifacts` NO se
//     ejecuta; su artefacto se decodifica (JSON, sin volver a pasar el validador de
//     calidad) y se usa. Vale para las cinco, y en `draft` no es un ahorro sino
//     corrección: repetirla escribiría OTRA revisión. Con `match` saltada tampoco se lee
//     el catálogo. Un artefacto persistido ILEGIBLE no mata el job: se rehace la etapa,
//     con un Warn «…no se pudo decodificar; la etapa se rehace» que no cita el artefacto.
//  5. **Cero ideas** tras P2 NO corta la cadena: se avisa (Warn «pipeline: P2 no dejó ni
//     una idea viva…», con `causa=indeterminada_desde_aqui` y `donde_mirar`) y se sigue.
//  6. **El catálogo que no se lee** es el error de la etapa `match` («match: leer el
//     catálogo del tenant: …»): la etapa no corre. **Las zonas que no se leen NO**: un
//     Warn y `match` recibe cero zonas; sin lector, lo mismo sin Warn. La unidad de daño
//     es el dato que falló, no el pedido.
//  7. **Cada etapa ejecutada deja su línea con `elapsed_ms`** (del reloj de WithClock),
//     por los DOS caminos: el Info «pipeline: etapa completada» (`job_id`, `stage`,
//     `elapsed_ms`, `intento`) y el Warn del tropiezo. En `match` el cronómetro incluye
//     las dos lecturas.
//  8. **El tropiezo** (una etapa devolvió error, o el sobre no abrió) se resuelve con la
//     política de `backoff.go`: `failed` en el acto si la causa es CauseInvalidJob;
//     `failed` si se agotó el techo («agotados los N intentos: …»); si no, `Retry` con la
//     marca `now() + castigo` y un Warn «…el job vuelve a la cola con backoff» (`causa`,
//     `stage`, `elapsed_ms`, `intento`, `tope`, `next_attempt_at`). El motivo de muerte es
//     `causa=<causa> stage=<etapa>: <error>`, con `ninguna` por etapa si murió antes de
//     entrar en una; y deja un Error «pipeline: job FAILED». Tras un tropiezo las etapas
//     siguientes NO se ejecutan. `stages.ErrJobNotProcessing` no es un tropiezo: otro
//     terminó el job; se dice en Info y no se escribe nada. **Tampoco lo es la parada del
//     worker** (✎ D-F7-10): si `ctx` está cancelado cuando la etapa falla, el job vuelve
//     a `pending` SIN castigo (`Release`: ni intento ni marca) con el Info «pipeline: job
//     devuelto a la cola SIN castigo» y `motivo` «el worker se apagó durante una etapa»;
//     ni Warn del tropiezo ni Error. Solo el job inválido muere igual, apagándose o no.
//  9. **El cierre**: con la cadena entera, `Finish` con el `intake_id` que devolvió
//     `draft` (Info «pipeline: job DONE»). Si viene vacío se cierra igual, con un Warn
//     «…termina SIN intake_id…».
//  10. **Todo desenlace se escribe con un contexto que SOBREVIVE a la cancelación** de
//     `ctx` (acotado a 5 s): un apagado durante una etapa deja el job devuelto, no en
//     `processing` para siempre. Y ninguno es mudo: si la escritura falla, un Error
//     «…queda en processing»; si no aplica (`(false, nil)`), un Info «…no aplicó (el job
//     ya no estaba en processing)».
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	job, found, err := w.store.ClaimNext(ctx)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	w.process(ctx, job)
	return true, nil
}

// logClaimError registra en ERROR un fallo del reclamo SALVO que el contexto ya esté
// cancelado (D-F9-10, el molde es D-F6-7): con el proceso apagándose, que el reclamo
// devuelva "context canceled" no es una avería que alguien tenga que mirar.
func (w *Worker) logClaimError(ctx context.Context, msg string, args ...any) {
	if ctx.Err() != nil {
		return
	}
	w.log.Error(msg, args...)
}
