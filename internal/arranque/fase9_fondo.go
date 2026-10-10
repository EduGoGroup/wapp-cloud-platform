// Copia de internal/bootstrap/arranque/fase9_fondo.go @ 80807ba (F0 · 05 §6): cableaba paquetes VIEJOS,
// salvo el worker del puente CRM, que desde F6 (T6.24, conmutar(solicitudes)) es el de
// internal/modulos/solicitudes/integrations.
// 🔀 F8 · conmutar(conversacion): ya no cablea ninguno. El agregador que arranca (3/5) es el de
// internal/modulos/conversacion/runtime; su sentencia `go` es la MISMA, en el mismo sitio y sin
// envolver (T-4: la huella de goroutines la reconoce por su nombre).
package arranque

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics/flowlifecycle"
)

// faseFondo arranca las CINCO goroutines de larga vida del proceso.
//
// # LO QUE HAY QUE SABER DE ESTA FASE ENTERA
//
// 🔴 LAS CINCO SON «LA MITAD QUE NO SE VE», Y OLVIDAR CUALQUIERA FALLA EN SILENCIO.
// Los objetos que estas líneas ponen a correr ya están construidos y cableados desde
// las fases anteriores: sin su `Run`, el sistema arranca entero, los cuatro listeners
// levantan, no hay un solo error en el log — y el trabajo se acumula sin que nadie lo
// reclame. Es el modo de fallo más caro de este repo y ya ocurrió: ventanas que se
// abren y nunca cierran, jobs `pending` que nadie toma, presupuestos que no llegan.
//
// Las cinco corren sobre el MISMO ctx derivado de signal.NotifyContext que cierra todo
// lo demás: un solo Ctrl+C también las para, sin un segundo mecanismo de shutdown.
// Sin broker (ADR-0003): tickers de Go y tablas, no Redis ni RabbitMQ.
//
// ⚠️ Se lanzan «a pelo», sin supervisión ni reinicio: si una muere, muere en silencio y
// el proceso sigue vivo. Está anotado como deuda en documentations/deuda.md.
type faseFondo struct{}

func (faseFondo) nombre() string { return "goroutines de fondo" }

func (faseFondo) requiere() []string {
	return []string{"almacenes", "selector", "flujos"}
}

func (faseFondo) ejecutar(ctx context.Context, c *contenedor) error {
	// 1/5 · Worker del puente CRM (Plan 042 · Ola 3, D-042.4): primera goroutine de
	// polling de larga vida de este repo.
	// intakeStore entra aquí como TERCER completador del payload (Ola 5): la
	// indicación del cliente ya no viaja congelada en webhook_outbox.payload —era
	// PII en claro sobreviviendo a la entrega— y se lee de public.intakes justo
	// antes del POST, igual que buyerDataStore descifra buyer_data. Es el MISMO
	// store que ya usa la API pública: uno solo, no dos nombres para lo mismo.
	//
	// 🔀 F6 · conmutar(solicitudes): el worker es el NUEVO, con los tres almacenes nuevos y el
	// único almacén de variables del tenant (c.tenantVars, fase 3). La MISMA goroutine, una sola.
	webhookWorker := integrations.NewWorker(
		c.integrationsStore, c.buyerDataStore, c.intakeStore, c.tenantVars, c.log,
		integrations.WorkerConfig{
			PollInterval: c.cfg.Webhook.PollInterval,
			MaxAttempts:  c.cfg.Webhook.MaxAttempts,
			Timeout:      c.cfg.Webhook.Timeout,
		},
		c.mtx.WebhookDelivery,
	)
	go webhookWorker.Run(ctx)

	// 2/5 · Colector incremental de telemetría de eventos (Plan 043 · T6.5, cierra
	// MD-043.17): PRIMER consumidor de PRODUCCIÓN del outbox append-only
	// flow_events, no un assert de test (T6.2 ya lo lee desde un test y eso NO
	// cierra el hallazgo — ver tasks.md §T6.5). onCount es mtx.FlowEventLifecycle: el
	// colector (internal/platform/metrics/flowlifecycle) NUNCA importa prometheus ni
	// el motor de flujos, mismo desacoplo que receiptSink/webhookWorker.
	flowLifecycleCollector := flowlifecycle.NewCollector(c.db, c.mtx.FlowEventLifecycle, c.log)
	go flowLifecycleCollector.Run(ctx)

	// 3/5 · BARRIDO DE VENTANAS DE CAPTACIÓN (Plan 044 · Ola 1 · T1.2/T1.7).
	//
	// 🔴 SIN ESTA LÍNEA EL 044 NO FUNCIONA: el IntakeAggregator cableado en la fase de
	// flujos solo ABRE ventanas; quien las CIERRA —y quien recupera al arrancar las que
	// vencieron mientras el proceso no estaba— es este Run. Retirarla dejaría jobs en
	// `aggregating` para siempre sin un solo error en el log.
	//
	// Y por eso mismo la recuperación NO es un paso aparte: Run empieza barriendo
	// (RecoverAtBoot), porque el estado de una ventana vive en `intake_jobs` y no en
	// este proceso. Un despliegue en medio de una ráfaga no pierde el job.
	go c.intakeAggregator.Run(ctx)

	// 4/5 · EL POOL QUE PIDE LAS CLASIFICACIONES (Plan 044 · Ola 1.6 · T1.6-4).
	//
	// 🔴 ES LA OTRA MITAD INVISIBLE, y el fallo de olvidarla también es MUDO: sin este
	// Run el agregador encolaría peticiones, la cola se llenaría y a partir de ahí
	// todo se descartaría en silencio. El sistema seguiría funcionando —las ventanas
	// cierran por su reloj— y nadie vería un error; simplemente no habría adelanto
	// nunca.
	go c.intakeAhead.Run(ctx)

	// 5/5 · EL WORKER DEL PIPELINE P2→P4 (Plan 044 · Ola 2).
	//
	// 🔴 UNA SOLA, Y LA CUENTA IMPORTA (W = 1, ver el cableado del aforo en la fase de
	// captación). Duplicar esta línea no daría ningún error: dos workers reclamarían
	// sin pisarse —el claim va con `FOR UPDATE SKIP LOCKED`— y se bloquearían el uno al
	// otro en la única plaza del Edge, cada uno reteniendo un job. Lo custodia
	// TestPipelineCaptacionCableado, que cuenta las apariciones de esta línea en el AST
	// del paquete.
	//
	// 🔴 Y ES LA TERCERA MITAD INVISIBLE de esta ola, con el mismo fallo MUDO que las
	// dos de arriba: sin este Run, las ventanas cerrarían, los jobs quedarían `pending`
	// y nadie los reclamaría nunca. Ni un error en el log — solo presupuestos que no
	// llegan, que es exactamente el 7 h 28 min que el plan existe para borrar.
	go c.intakePipeline.Run(ctx)

	return nil
}
