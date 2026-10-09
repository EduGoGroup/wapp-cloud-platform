// Copia de internal/bootstrap/arranque/fase5_captacion.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS,
// salvo edge, que desde F3 (T3.28, conmutar(edge)) es internal/modulos/edge, e inferencia, que desde
// F4 (T4.24, conmutar(inferencia)) es internal/modulos/inferencia: el selector de vía, su adaptador
// local y el cargador de prompts son los nuevos. turnoacotado sigue viejo y recibe el selector
// nuevo detrás de bridge_inferencia.go. Desde F6 (T6.24, conmutar(solicitudes)) el generador de
// cotización es el de internal/modulos/solicitudes. Y desde F7 (T7.23, conmutar(captacion)) las
// cinco etapas, el aforo, el worker y el re-análisis son los de internal/modulos/captacion, y la
// caché del catálogo la de internal/modulos/catalogo/indice: leen del almacén NUEVO de solicitudes
// (c.intakeStore) y de la cola NUEVA (c.intakeJobStore). Lo único viejo de captación que queda
// aquí es el compositor del literal, de flujos/runtime (F8), cosido por bridge_captacion.go.
package arranque

import (
	"context"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/turnoacotado"
)

// EL PLAZO DE LA COTIZACIÓN SUGERIDA (G7), EN UN SOLO SITIO (FX mapa §4.3, reglas.md T-9).
//
// Son DOS relojes y el segundo sale del primero:
//
//   - quoteCallTimeout es el plazo de la llamada al modelo, el que recibe quotetext.WithTimeout:
//     el MISMO suelo por llamada que P2–P4, que desde F7 (conmutar(captacion)) se lee del
//     pipeline NUEVO, internal/modulos/captacion/pipeline (reglas de F7, T-13);
//   - quoteWriteDeadline es el plazo de ESCRITURA de la respuesta de G7, el que recibe la cara
//     nueva (IntakeReportsDeps.QuoteWriteDeadline): ese plazo más quoteWriteMargin.
//
// 🔴 DERIVADO, NO COPIADO. En la cara vieja la suma vivía en publicapi/plazoescritura.go
// (`pipeline.PlazoPorLlamadaSuelo + margenDeRedacción`, 48 s + 12 s) y la cara nueva no importa
// el pipeline: recibe el resultado. Si el suelo se mueve, G7 lo sigue sola; dos literales
// sueltos se separarían el primer día y la respuesta de una sugerencia que tarda se cortaría
// con el modelo todavía dentro de plazo. Lo fija TestCableado_TheQuoteWriteDeadlineIsDerived, y
// que el suelo sea el del pipeline nuevo, TestCableado_TheQuoteCallTimeoutIsTheNewPipelineFloor.
const (
	quoteCallTimeout = pipeline.CallTimeoutFloor
	// quoteWriteMargin cubre lo que el handler de G7 hace fuera de la llamada al modelo: los
	// 12 s de margenDeRedacción de la cara vieja, sin cambiar.
	quoteWriteMargin   = 12 * time.Second
	quoteWriteDeadline = quoteCallTimeout + quoteWriteMargin
)

// faseCaptacion arma TODO lo que el Plan 044 necesita para convertir una ventana de
// conversación en un presupuesto: el selector de vía y —colgando de él— las cinco
// etapas, la caché del catálogo, el aforo por Edge, el worker del pipeline, el
// resolutor del turno acotado y las dos puertas del dueño.
//
// # POR QUÉ VAN JUNTAS Y NO REPARTIDAS
//
// Porque son UNA pieza: el selector de vía es la dependencia de todo lo demás, y
// separarlos obligaría a pasarlo de un lado a otro solo para volver a bajarlo. Es el
// primero de los dos motivos que ya justificaban la función `nuevoStackLLMDeCaptacion`
// de la que sale esta fase.
//
// 🔧 EL SEGUNDO MOTIVO SE DISOLVIÓ AL PARTIR EL ARRANQUE, y se dice porque el
// comentario original lo daba como el que mandaba: aquella función existía sobre todo
// porque `Run` estaba EXACTAMENTE en el techo de complejidad ciclomática del lint (15
// de 15, medido) y cualquier `if err != nil` nuevo la rompía. Ya no hay ningún `Run`
// de 990 líneas que proteger. Lo que queda es el motivo bueno, que es el de arriba —y
// por eso el reparto interno de esta fase en tres constructores es por LEGIBILIDAD y
// no por aritmética del lint.
//
// 🔴 EL AFORO NO SALE AL CONTENEDOR. `aforoLote` se queda dentro de su
// constructor porque nadie más debe tocarlo: es el entero del ADR-0046 y su único
// dueño legítimo es el worker que lo recibe. Publicarlo invitaría a un segundo
// consumidor, y dos dueños de un aforo son dos ideas distintas de cuántas plazas hay.
type faseCaptacion struct{}

func (faseCaptacion) nombre() string { return "captación (stack LLM)" }

func (faseCaptacion) requiere() []string {
	return []string{"gateway", "almacenes", "cipher"}
}

func (faseCaptacion) ejecutar(_ context.Context, c *contenedor) error {
	// EL COMPOSITOR DEL LITERAL (T1.4). Corre AL FLUSH y nunca en línea con el
	// entrante: lee el hilo del evento por eventStore —descifrado en el borde,
	// REQ-10c—, separa el CONTEXTO (los `summary` y los salientes fuera de turno,
	// D-044.3b + D-044.24) del hilo literal, y guarda el sobre de tres piezas.
	// Reusa el MISMO cipher que el hilo: el texto sale de un sobre del keyring del
	// Plan 012 y entra en otro del mismo keyring, sin una tercera rotación.
	//
	// 🔴 ES EL MISMO OBJETO que consume `/reanalyze`: dos compositores serían dos
	// `source_text` que divergen en el primer rótulo que cambie.
	//
	// 🔀 F7 · conmutar(captacion): el compositor es de flujos/runtime y sigue VIEJO hasta F8
	// (reglas de F7, T-2), así que escribe por la instancia VIEJA de la cola
	// (c.legacyIntakeJobs, D-F7-1): su puerto SourceTextWriter nombra el WindowKey y el
	// SourceText del intake viejo. El re-análisis nuevo lo recibe detrás de composerBridge
	// (bridge_captacion.go), que es este mismo objeto y no otro (T-5).
	c.intakeComposer = flowruntime.NewSourceTextComposer(c.log, c.eventStore, c.legacyIntakeJobs, c.flowDeps.cipher)

	if err := construirSelectorDeVia(c); err != nil {
		return err
	}
	if err := construirWorkerDelPipeline(c); err != nil {
		return err
	}
	if err := construirPuertasDelDueno(c); err != nil {
		return err
	}

	c.marca("selector", "captacion")
	return nil
}

// construirSelectorDeVia arma el selector y el resolutor del turno acotado, que es su
// consumidor más pequeño y el que menos se ve.
func construirSelectorDeVia(c *contenedor) error {
	// EL SELECTOR DE VÍA (T1.6-3, ADR-0044 §C2, REQ-33/REQ-37): el ÚNICO sitio del
	// proceso que pregunta `local` o `api`. Todo lo que necesita una inferencia le
	// pide un provider a él y no vuelve a mirar la vía nunca más.
	//
	// El frame de la vía local ES el gateway, SIN adaptador: desde F4 (conmutar(inferencia))
	// el selector es el NUEVO y su local.Frame pide el InferRequest de internal/modulos/edge,
	// así que el *edgegrpc.Server lo satisface tal cual. Del mismo valor saca el selector, por
	// aserción de tipo, PlazaDe (sin ella el aforo por Edge se apaga en silencio, T-1). El
	// adaptador que hizo falta en F3 (bridge_gateway.go) murió aquí.
	plantillas, err := cargarPlantillasDePrompt(c.log, c.cfg.LLM.PromptsDir)
	if err != nil {
		return err
	}
	llmSelector, err := llmvia.NewSelector(c.tenantLLMStore, c.log,
		llmvia.WithFrame(c.gw),
		llmvia.WithNotifier(c.degradationNotifier),
		// LOS PROMPTS AJUSTABLES DE P2–P5 (WAPP_LLM_PROMPTS_DIR). Sin directorio esto
		// entrega las plantillas COMPILADAS y el proveedor se comporta igual que antes
		// de que existiera la palanca: el mapa siempre trae las cuatro etapas.
		llmvia.WithLocalOptions(local.ConPlantillas(plantillas)),
		// EL PRESUPUESTO DE SALIDA POR TAREA (T1.7-3), con su interruptor de campo.
		// Encendido, cada etapa fija su `max_output_tokens` (P1 192 … P4 1024) y una
		// P2/P3 de 265-293 tokens NO se trunca; apagado, el campo viaja ausente y el
		// Edge aplica su default de 256 — que es el lado B del criterio (c).
		llmvia.WithLocalOptions(local.WithMaxOutputTokens(c.cfg.LLM.MaxOutputTokensEnabled)),
		// EL CONTEO DE CAÍDAS A NIVEL A (T3.5-2, D-044.41). Es el otro extremo del
		// aviso al dueño: la tabla owner_degradation_notices es para que una PERSONA
		// lea un incidente suyo, deduplicado por ventana; esta serie es para que
		// NOSOTROS podamos decidir si hace falta el desalojo del Mecanismo 1 —que
		// captacion/pipeline/slot.go dice por escrito que no se construye
		// «hasta que exista la Ola 3.5 y un dato de campo»—. La fila que responde esa
		// pregunta es {origen="turno"}: un turno interactivo que se quedó sin
		// interpretación mientras, con K=1 por plaza, lo más probable es que una
		// cadena de lote ocupara el Ollama del cliente.
		llmvia.WithDegradacionObservada(c.mtx.LLMDegradacion))
	if err != nil {
		return fmt.Errorf("selector de vía LLM: %w", err)
	}
	c.llmSelector = llmSelector

	// EL RESOLUTOR DEL TURNO ACOTADO (T3.5-2). Es un consumidor MÁS del selector, como
	// las cinco etapas y el aforo — su único argumento ES el selector, detrás de
	// turneroBridge (bridge_inferencia.go): turnoacotado sigue siendo el paquete viejo
	// hasta F8 y pide el TurnoRequest y el centinela del llmvia viejo; el adaptador los
	// traduce y delega en ESTE selector, no en uno aparte (R4.7.b, R4.7.c).
	// Si falla es porque el selector vino nil, o sea un bug de este mismo arranque:
	// se aborta en vez de arrancar con el tercer escalón del carrito apagado.
	consultaResolver, err := turnoacotado.New(&turneroBridge{sel: c.llmSelector})
	if err != nil {
		return fmt.Errorf("resolutor del turno acotado: %w", err)
	}
	c.consultaResolver = consultaResolver
	return nil
}

// construirWorkerDelPipeline arma las cinco etapas, la caché del catálogo, el aforo y
// el worker que las encadena.
//
// ═══════════════════════════════════════════════════════════════════════════
// EL PIPELINE DE CAPTACIÓN P2 → P3 → P4 (Plan 044 · Ola 2). AQUÍ SE ENCIENDE.
// ═══════════════════════════════════════════════════════════════════════════
//
// La Ola 2 construyó las ocho piezas y las dejó APAGADAS a propósito: hasta esta
// función, NINGÚN fichero de producción importaba el `pipeline` ni las `stages` de
// captación, y `intake_jobs` acumulaba jobs `pending` que nadie reclamaba. Esto es su
// primer —y único— llamante de producción.
//
// 🔀 F7 · conmutar(captacion): las ocho son las de internal/modulos/captacion (y el índice,
// el de internal/modulos/catalogo/indice). Los nombres cambiaron con E-11 —`WithCallTimeout`,
// `DefaultZone`, `WithCRMPush`, `NewCapacity`, `WithCapacity`, `WithShippingZones`—; los
// valores, los textos y el orden de construcción, no.
//
// 🔴 W = 1, UNA SOLA GOROUTINE, Y ES LA DECISIÓN QUE GOBIERNA EL RESTO. El aforo
// de abajo reparte K = 1 plaza POR EDGE; en UAT hay UN Edge, así que con W > 1 los
// workers extra se bloquearían pidiendo el mismo asiento CON UN JOB YA RECLAMADO
// en la mano —bloqueo en cabeza, `slot.go` §«Esperar tiene dos precios»— sin
// procesar nada más rápido. La palanca para subir W es el número de Edges activos,
// no el volumen de trabajo. Quien arranca esa única goroutine es la fase de fondo.
//
// 🔴 NO HAY INTERRUPTOR DE CONFIGURACIÓN, y es deliberado: el gate que queda es UNO
// SOLO, la feature (T1.6). Un flag aquí sería un segundo sitio donde apagar lo
// mismo, y el día que el pipeline no corriera nadie sabría cuál de los dos manda.
// El camino ya está cerrado aguas arriba: sin la feature, el agregador no abre
// ventana, no hay job `pending` y este worker gira en vacío cada 5 s.
func construirWorkerDelPipeline(c *contenedor) error {
	// Las tres etapas comparten selector de vía y store, y las tres reciben el MISMO
	// plazo por llamada: `stages.WithCallTimeout` es obligatoria en producción —sin
	// ella el adaptador cae a sus 30 s, el umbral de lento baja a 24 s y una P3 caliente
	// de 27 s ya cuenta como lenta ante el breaker (deadline.go, «el default es COMPATIBLE,
	// no seguro»)—. `stages.DefaultZone` es UTC y es la decisión que P4 obliga a
	// escribir aquí en vez de heredar (DEUDA-044.11, sin dueño).
	plazoLlamada := stages.WithCallTimeout(pipeline.CallTimeoutFloor)
	etapaIdeas, err := stages.NewP2(c.log, c.llmSelector, c.intakeJobStore, plazoLlamada)
	if err != nil {
		return fmt.Errorf("pipeline de captación, etapa P2: %w", err)
	}
	etapaSpecs, err := stages.NewP3(c.log, c.llmSelector, c.intakeJobStore, plazoLlamada)
	if err != nil {
		return fmt.Errorf("pipeline de captación, etapa P3: %w", err)
	}
	etapaCantidades, err := stages.NewP4(c.log, c.llmSelector, c.intakeJobStore, stages.DefaultZone, plazoLlamada)
	if err != nil {
		return fmt.Errorf("pipeline de captación, etapa P4: %w", err)
	}
	// ═══════════════════════════════════════════════════════════════════════════
	// LAS DOS ETAPAS DE LA OLA 3 (T3.8). AQUÍ SE ENCIENDEN.
	// ═══════════════════════════════════════════════════════════════════════════
	//
	// La Ola 3 las escribió con sus tests y las dejó SIN LLAMANTE: `stages.NewMatch` y
	// `stages.NewDraft` no tenían un solo call-site de producción, `pipeline.go` no
	// nombraba `StageMatch` ni `StageDraft`, y cada job terminaba en `done` sin
	// `intake_id`. Es el MISMO defecto que la Ola 2 tuvo que cerrar con T2.9, y por eso
	// el test de AST de este paquete cuenta CINCO constructores y no tres.
	//
	// 🔴 NINGUNA DE LAS DOS LLEVA `stages.WithCallTimeout`, Y NO PUEDE LLEVARLA. No
	// es una decisión de calibración: `MatchOption` y `DraftOption` son tipos DISTINTOS
	// de `Option` —la de las etapas LLM— precisamente para que un «match con plazo por
	// llamada» no compile. El motivo de fondo es que ninguna de las dos llama al modelo
	// en este cableado: `match` corre la cascada determinista `Exact → Fuzzy(0,85)` y
	// `draft` solo escribe en la base. Un plazo de llamada aquí no acotaría nada.
	//
	// 🔴 Y EL MATCH VA SIN ZONA GRIS A PROPÓSITO (`stages.WithGrayZone` NO se pasa). Sin
	// ella la cascada es 100 % determinista y el ítem que los dos escalones no resuelven
	// cae a `unmatched` con su aviso — que es el renglón que el dueño precifica, no una
	// pérdida. El TERCER escalón es el caro: una llamada al LLM por ítem no cubierto,
	// que compite por la MISMA plaza única del Edge que P2/P3/P4 (ADR-0046) y que
	// llegará con su propio criterio de coste. Encenderla aquí metería llamadas al
	// modelo en una etapa que hoy se anuncia como determinista.
	etapaMatch, err := stages.NewMatch(c.log, c.intakeJobStore)
	if err != nil {
		return fmt.Errorf("pipeline de captación, etapa match: %w", err)
	}
	// `flowStore` satisface DOS de los tres puertos del draft (la cabecera de la
	// solicitud y el outbox de efectos) e `intakeStore` el tercero (la revisión). El
	// reparto no es caprichoso: la revisión es lo único que lleva literal del cliente
	// dentro del payload, y el store que la escribe lleva el cipher del literal
	// (T3.5). Escribirla por el otro persistiría texto en claro.
	//
	// 🔀 F7 · conmutar(captacion): la revisión la escribe el almacén NUEVO de solicitudes
	// (c.intakeStore, con el cipher del literal), ya no la instancia vieja de D-F6-1: el
	// puerto RevisionWriter nombra el intakes.Revision de internal/modulos/solicitudes. Los
	// otros dos puertos (IntakeStore, EventWriter) siguen en el flowStore VIEJO, y es un
	// puente declarado (stages → flujos/store): mueren en F8.
	//
	// 🔧 `stages.WithCRMPush` ES DE T4.6 (Plan 044 · Ola 4), y cierra T4.10 mitad 2.
	// Sin esta opción la etapa produce el borrador igual, pero un RE-ANÁLISIS pedido
	// por el dueño dejaría su revisión escrita y el CRM se quedaría con la versión
	// vieja del pedido — que es exactamente lo que D-044.19 existe para impedir. La
	// ausencia no sería muda (el draft lo grita con un Error), pero tampoco visible
	// hasta que alguien mirase el log, así que además la custodia el test de cableado
	// de este paquete.
	// El empuje va GATEADO dentro de la etapa por `intake_jobs.requested_by`: el
	// pipeline normal NO empuja, y eso es una decisión, no un olvido (ver draft_push.go).
	//
	// 🔴 EL NUDO DE CONSTRUCCIÓN DEL EMPUJE AL CRM (T4.6), dicho porque la clausura
	// parece un rodeo y no lo es: esta etapa nace AQUÍ —cuelga del selector de vía— y
	// el Service de solicitudes que la satisface nace UNA FASE MÁS TARDE, porque
	// necesita el notificador, que necesita el gateway. Se necesitan mutuamente y el
	// ciclo se corta con una clausura que se resuelve AL LLAMAR, no al construir: el
	// mismo patrón (y el mismo porqué) que `intakeahead.SinkFunc` con el agregador.
	// Cuando el pipeline llegue a empujar algo, el proceso lleva rato arrancado y
	// `c.intakeService` hace mucho que existe; y aunque fuera nil, PushRevisionByID es
	// nil-safe y calla.
	etapaDraft, err := stages.NewDraft(c.log, c.intakeJobStore, c.flowStore, c.intakeStore, c.flowStore,
		stages.WithCRMPush(stages.CRMPusherFunc(
			func(ctx context.Context, tenantID, intakeID string, revisionNo int) error {
				return c.intakeService.PushRevisionByID(ctx, tenantID, intakeID, revisionNo)
			})))
	if err != nil {
		return fmt.Errorf("pipeline de captación, etapa draft: %w", err)
	}
	// LA CACHÉ DEL CATÁLOGO (T3.7, D-044.44): el índice que `match` consulta por ítem,
	// construido UNA VEZ POR JOB por el worker e invalidado POR CONTENIDO.
	//
	// El normalizador es `textmatch.Normalize` y tiene que ser EXACTAMENTE el mismo que
	// usa la cascada del match: con dos distintos, «Café» dejaría de casar «cafe» y el
	// ítem saldría `unmatched` sin un solo error en el log. `NewCache` no se fía y lo
	// verifica caso a caso al arrancar (`indice.VerificarNormalizador`), así que un
	// normalizador equivocado no llega al primer job de la noche: no llega al arranque.
	//
	// `0` en el tope deja `indice.MaxTenantsEnCache` (64), la cota de memoria.
	//
	// 🔀 F7 · conmutar(captacion): es el índice de internal/modulos/catalogo/indice (F5), que
	// es el que nombra el puerto pipeline.Catalogs del worker nuevo. Con esto el arranque deja
	// de importar internal/intake/catalogo y `catalogo` entra en Conmutados (D-R-4).
	catalogos, err := indice.NewCache(indice.NewFuenteContenido(c.flowStore, ""), textmatch.Normalize, 0)
	if err != nil {
		return fmt.Errorf("pipeline de captación, caché del catálogo: %w", err)
	}
	// EL AFORO (T2.7, ADR-0046 · Mecanismo 1): una cadena de lote en vuelo por
	// `(tenant, Edge)`. Es lo único compartido entre workers —hoy uno— y va con el
	// MISMO `llmSelector` que resuelve la vía: él es quien sabe a qué Edge apunta una
	// inferencia, y quien sabe que por vía API no hay plaza que tomar. Un segundo
	// selector aquí sería una segunda verdad sobre lo mismo.
	//
	// ⚠️ ES DE PROCESO, NO DISTRIBUIDO: con dos réplicas del Cloud hay K = 2 por Edge.
	// Decisión escrita (ADR-0046), no descuido; hoy el Cloud corre en una réplica.
	aforoLote := pipeline.NewCapacity(pipeline.KPerSlot)
	// El worker usa `c.intakeJobStore` —el MISMO `*intake.Postgres` NUEVO que las cinco
	// etapas y que la puerta del re-análisis— y no una construcción propia: aquí entra por
	// `intake.PipelineStore` (la máquina de estados del worker, machine.go). El agregador
	// escribe en la MISMA tabla por la instancia vieja de la cola (D-F7-1, hasta F8): son
	// dos tipos sobre un solo *sql.DB, no dos pools.
	//
	// El cipher es el descifrador del sobre del literal, y es `c.flowDeps.cipher`: el
	// MISMO keyring del Plan 012 con el que el compositor cerró ese sobre al cerrar la
	// ventana. Con otro, cada job moriría con `source_text` ilegible.
	//
	// `pipeline.Config{}` deja los cinco valores en su default (cadencia 5 s, backoff
	// 30 s→5 min, 3 intentos por calidad y 10 por infra): no hay perilla de operador
	// que gobierne esto, así que no se lee ninguna variable de entorno que luego nadie
	// pueda confirmar.
	//
	// `pipeline.WithShippingZones(intakeStore)` cierra la última lectura por job de la
	// Ola 3: sin ella TODO borrador saldría con la línea de envío sin precio, también
	// el del tenant que tiene UNA zona configurada con su tarifa plana — y eso no da
	// error, solo un renglón que el dueño precifica a mano sin saber que ya estaba
	// puesto. Lee las MISMAS filas que la bandeja y el carrito numérico, así que las tres
	// vías leen la misma configuración.
	//
	// 🔀 F7 · conmutar(captacion): recibe el almacén NUEVO de solicitudes (c.intakeStore): el
	// puerto ShippingZones devuelve el []intakes.ShippingZone de internal/modulos/solicitudes.
	// La instancia vieja de D-F6-1 ya no llega aquí.
	intakePipeline, err := pipeline.NewWorker(c.log, c.intakeJobStore,
		etapaIdeas, etapaSpecs, etapaCantidades, etapaMatch, etapaDraft, catalogos,
		c.flowDeps.cipher, pipeline.Config{},
		pipeline.WithCapacity(aforoLote, c.llmSelector),
		pipeline.WithShippingZones(c.intakeStore))
	if err != nil {
		return fmt.Errorf("worker del pipeline de captación: %w", err)
	}
	c.intakePipeline = intakePipeline
	return nil
}

// construirPuertasDelDueno arma las dos entradas que el dueño abre a mano desde la
// bandeja, y que comparten con el pipeline el selector de vía y los almacenes.
func construirPuertasDelDueno(c *contenedor) error {
	// LA PUERTA DEL RE-ANÁLISIS (Plan 044 · Ola 4 · T4.6, D-044.15, design §8.1):
	// `POST /api/v1/intakes/{id}/reanalyze`. Seis dependencias y las SEIS son objetos
	// que ya existen — no se construye ni un store nuevo:
	//
	//   · intakeStore     → de qué evento cuelga la solicitud y por qué revisión iba. Desde
	//                       F7 es el almacén NUEVO de solicitudes: el puerto Intakes devuelve
	//                       el intakes.ReanalysisTarget de internal/modulos/solicitudes;
	//   · eventStore      → el hilo cifrado del evento, descifrado en el borde, y la
	//                       fila del texto que pega el dueño (origin='owner_pasted'). Es el
	//                       store VIEJO de flujos/events, por un puente declarado (F8);
	//   · intakeJobStore  → el MISMO *intake.Postgres NUEVO del worker: aquí satisface
	//                       OTRO puerto, el de esta puerta, que solo puede preguntar por
	//                       el job vivo y abrir el del re-análisis;
	//   · intakeComposer  → el MISMO compositor que corre al cerrar una ventana. Dos
	//                       compositores serían dos `source_text` que divergen en el
	//                       primer rótulo que cambie. Es el VIEJO de flujos/runtime (F8)
	//                       detrás de composerBridge (bridge_captacion.go), que solo
	//                       convierte el WindowKey nuevo en el viejo (T-3, T-5);
	//   · entResolver     → el MISMO resolver CACHEADO que gatea el resto del carril.
	//                       Un segundo sería una segunda caché y una segunda verdad
	//                       sobre el plan del tenant;
	//   · tenantLLMStore  → la vía configurada y si hay credencial. Entra por el puerto
	//                       recortado `reanalisis.LLMConfig`, que NO tiene `APIKey`:
	//                       esta puerta necesita saber SI hay clave, nunca cuál es.
	//                       Desde F7 es el almacén NUEVO tal cual: el servicio nuevo
	//                       pide el Config del tenantllm nuevo, y llmConfigBridge
	//                       (bridge_inferencia.go, F4) murió con este commit.
	//
	// Y un OCTAVO argumento que no es una dependencia: el límite del hilo. El servicio
	// nuevo no lo importa del runtime viejo (sería un puente más) ni trae un default (serían
	// dos verdades con la del compositor): lo RECIBE, y aquí llega legacyThreadLimit, que
	// bridge_captacion.go deriva de flowruntime.DefaultThreadLimit. Con ≤ 0 el constructor
	// falla, y el arranque con él.
	//
	// ⚠️ Si esto devuelve error, el arranque MUERE en vez de montar la ruta a medias:
	// un 500 a mitad de camino en una puerta que abre trabajo en la cola es peor que
	// no arrancar. Y si el servicio no llegara a `apipublica.ReanalyzeDeps.Reanalysis`, la
	// ruta sencillamente no se monta y responde 404 — lo custodia el test de cableado de
	// este paquete.
	reanalysisSvc, err := reanalisis.NewService(c.log, c.intakeStore, c.eventStore, c.intakeJobStore,
		&composerBridge{composer: c.intakeComposer}, c.entResolver, c.tenantLLMStore, legacyThreadLimit)
	if err != nil {
		return fmt.Errorf("re-análisis desde el origen: %w", err)
	}
	c.reanalysisSvc = reanalysisSvc

	// EL GENERADOR DE COTIZACIÓN CON LA VOZ DE LA DUEÑA (Plan 044 · Ola 5 · T5.1,
	// D-044.11): lo que sirve `POST /api/v1/intakes/{id}/quote-suggestion`.
	//
	// Las tres piezas, y de dónde salen:
	//   · `intakeStore` sirve por DOS puertos a la vez —la solicitud con sus líneas y
	//     el historial aprobado del tenant—, que es la misma fila y la misma base;
	//   · `flowStore` es el lector de `tenant_content` (ref `quote_style_examples`), y
	//     entra por la OPCIÓN porque la semilla es opcional de verdad: hoy no hay ni un
	//     tenant que la tenga escrita;
	//   · `llmSelector` es el MISMO que usan las etapas del pipeline. Este paquete no
	//     sabe qué vía le tocó al tenant y no debe saberlo (ADR-0044 §C2).
	//
	// El plazo es el MISMO suelo por llamada que reciben P2–P4, y por el mismo motivo
	// escrito allí: sin él el adaptador cae a sus 30 s propios y envenena el umbral de
	// lento del breaker. La diferencia con el pipeline es que aquí hay UNA llamada y
	// hay una PERSONA esperando la respuesta. De ESTE valor sale el plazo de escritura de
	// G7 (quoteWriteDeadline, arriba): no se escribe en dos sitios.
	//
	// 🔀 F6 · conmutar(solicitudes): el generador es el NUEVO y lee del almacén NUEVO
	// (su IntakeReader devuelve el intakes.Detail de internal/modulos/solicitudes).
	quoteSvc, err := quotetext.NewService(c.log, c.intakeStore, c.intakeStore, c.llmSelector,
		quotetext.WithSeed(c.flowStore),
		quotetext.WithTimeout(quoteCallTimeout))
	if err != nil {
		return fmt.Errorf("generador de cotización: %w", err)
	}
	c.quoteSvc = quoteSvc
	return nil
}
