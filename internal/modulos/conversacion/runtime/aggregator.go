// Porta internal/flujos/runtime/aggregator.go @ e0159171

// aggregator.go — EL AGREGADOR DE VENTANAS del pipeline de captación por LLM
// (Plan 044 · Ola 1 · T1.1, T1.2 y T1.7; design §6.2, D-044.26, D-044.20).
//
// Un cliente no pide un presupuesto en un mensaje: lo pide en cinco seguidos. El
// agregador junta esos cinco en UNA ventana —una fila `intake_jobs` en estado
// `aggregating`— y la cierra (`aggregating` → `pending`) cuando le toca, para que el
// pipeline caro corra UNA vez y no cinco.
//
// # Las ocho reglas (diseno.md §4.1), y dónde las promete este contrato
//
//	AG-1  Observe: 1 sentencia SQL, 0 SELECT, 0 cripto, 0 red; guardas baratas primero ... Observe
//	AG-2  Observe no devuelve error; un fallo se loguea y el turno sigue (INV-10) ......... Observe
//	AG-3  la ventana de silencio es el camino PRINCIPAL; el intent solo adelanta ......... Sweep, OnClassified
//	AG-4  la pista: anotar bajo candado Y DESPUÉS avisar (no bloqueante, buffer 1) ....... OnClassified
//	AG-5  `seen` NO se borra al cerrar la ventana ...................................... Observe
//	AG-6  sin timer por ventana; el plazo se recalcula en cada barrido; cierre idempotente Sweep, RecoverAtBoot, Run
//	AG-7  tres llamantes del puente con el motor ....................................... (abajo: ola de `incoming`)
//	AG-8  sin compositor → noop; sin AheadRequester → siempre por reloj ................. WithSourceComposer, WithAheadRequester
//
// # AG-7 · El puente con el motor NO está en este contrato
//
// En el viejo este fichero termina con observeForAggregation (aggregator.go:962), un
// método NO exportado del Runtime: el ÚNICO puente entre el motor y el agregador.
// Nace en el verde y se prueba con el Runtime (ola de `incoming`), no aquí. Lo que
// promete, para quien lo escriba:
//
//   - Tiene TRES puntos de llamada, EXCLUYENTES entre sí (por entrante corre como
//     mucho uno): (1) el TURNO NORMAL sobre una conversación viva, junto a la escritura
//     del hilo del turno; (2) EL MENSAJE QUE ARRANCA EL EVENTO —el «quiero presupuesto
//     de X» que abre la ráfaga—, con el event_id recién nacido; (3) el REINICIO POR
//     REANUDACIÓN consumado (el mensaje con el que el cliente reabre un pedido caducado).
//   - 🔴 La invariante que los gobierna: TODO MENSAJE QUE ENTRA EN `source_refs` TIENE
//     SU LITERAL EN EL HILO. Los caminos que deciden NO escribir el literal del turno
//     (el corte por fallo del sink durable, el cupo anti-loop agotado, la elección
//     numérica en el despachador, el salto por tipo sobre conversación viva) NO
//     observan.
//   - Es nil-safe (sin agregador o sin mensaje no hace nada) y no devuelve error.
//   - Construye el IncomingRef con la tupla (tenant, sesión, contacto, evento), el
//     wa_message_id y el texto del entrante, y MessageTS = el ts_unix del mensaje en
//     UTC; si ts_unix no es > 0, el reloj del runtime (WithClock): se prefiere un
//     instante escrito y explicable a un NULL.
//
// # E-13
//
// El viejo mide 987 líneas. El verde PARTE este fichero por tema, solo moviendo
// declaraciones y con el sufijo del origen (aggregator_<tema>.go: el barrido y sus
// plazos por un lado, el puente con el motor por otro); cada parte con su test.
// aggregator_test.go ya nace partido así.

package runtime

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// IntentIntakeRequest es el ÚNICO nombre de intención que adelanta un flush
// (D-044.20). Lo define la config del tenant (T1.3) y lo devuelve la clasificación
// que el Cloud PIDE (T1.6-4); aquí solo se compara. Literal observable.
const IntentIntakeRequest = "intake_request"

// IntentHint es lo ÚNICO que la política de disparo mira de una clasificación: su
// nombre y su confianza.
//
// 🔴 NO LLEVA `params` A PROPÓSITO (D-044.20), y la garantía es el TIPO: tiene DOS
// campos y ninguno más. La política se dispara con la SEÑAL, no con los ítems: no lee
// params, no espera una lista de productos y no cambia de comportamiento según lo que
// el intent traiga dentro. Quien descompone en ítems es el pipeline P2–P4, aguas
// abajo y sobre el texto acumulado. Si alguien añade un campo aquí para «mejorar el
// disparo», está deshaciendo D-044.20.
type IntentHint struct {
	Name       string
	Confidence float64
}

// IncomingRef es lo mínimo que el agregador necesita de UN entrante.
//
// 🔴 LO QUE ENTRA A `intake_jobs` SON REFERENCIAS OPACAS Y NUNCA CONTENIDO
// (D-044.26). Un `wa_message_id` no es PII; el texto sí, y por eso el literal NO
// viaja a ninguna sentencia SQL desde aquí: el sobre del `source_text` nace NULL y se
// llena AL FLUSH (T1.4).
type IncomingRef struct {
	// Key es la tupla de la ventana: tenant, sesión, contacto y evento VIVO.
	Key intake.WindowKey
	// WaMessageID es el identificador opaco del entrante. Es la primera referencia
	// que entra a `source_refs` y, además, la clave con la que el agregador descarta
	// un mismo mensaje observado dos veces.
	WaMessageID string
	// MediaRefs son las referencias de audio/foto del mensaje, SIN DESCARGAR NADA
	// (T1.1). Entran a `source_refs` detrás del WaMessageID, en su orden. Hoy llegan
	// vacías: el entrante de CloudLink no trae un identificador de media separado del
	// `wa_message_id`, y una foto entra por su propio `wa_message_id`.
	MediaRefs []string
	// MessageTS es el instante del mensaje del CLIENTE (`ts_unix` del entrante), no
	// el reloj del servidor. Es la BASE DE FECHAS del presupuesto (D-044.9): «para el
	// jueves» se resuelve contra este instante. Solo cuenta si este entrante ABRE la
	// ventana; NO decide ningún plazo de cierre.
	MessageTS time.Time
	// Text es el literal del mensaje del cliente, y existe para UNA cosa: alimentar
	// la petición de clasificación (T1.6-4).
	//
	// 🔴 NO SE PERSISTE Y NO SE LOGUEA. No entra en el intake.Append, no toca ninguna
	// sentencia SQL y no aparece en un solo campo de log. Vacío es normal: un mensaje
	// de solo media no tiene texto que clasificar, y entonces no se pide nada.
	Text string
}

// AggregationSettings es lo mínimo que el barrido necesita de la config del tenant.
// Interfaz local (ISP): la satisfacen los dos repositorios de conversacion/store.
//
// ⚠️ Se consulta SOLO desde el barrido. Llamarla desde Observe sería el `SELECT` en
// línea con el mensaje que D-044.26 prohíbe.
type AggregationSettings interface {
	GetTenantSettings(ctx context.Context, tenantID string) (store.TenantSettings, error)
}

// SourceComposer ES EL PUNTO DE EXTENSIÓN DE T1.4: al cerrar una ventana alguien
// compone el literal (`source_text`) leyendo el hilo del evento y guarda su sobre. El
// compositor real es *SourceTextComposer (source_composer.go) y lo cablea el arranque
// con WithSourceComposer. Lo que vive AQUÍ es el hueco y el MOMENTO en que se llama
// (ver Sweep).
type SourceComposer interface {
	// ComposeAtFlush compone y persiste el `source_text` de la ventana recién
	// cerrada. Devolver error NO reabre la ventana ni corta nada: el job queda en
	// `pending` con el sobre vacío.
	ComposeAtFlush(ctx context.Context, key intake.WindowKey) error
}

// AheadRequester PIDE la clasificación que antes llegaba adjunta al mensaje (T1.6-4,
// D-044.31: el push murió, hoy es pull). Lo satisface el pool de clasificación
// adelantada de captación.
//
// La firma es corta y las tres cosas que NO tiene son el contrato:
//
//   - **no devuelve error**, porque corre en línea con el mensaje y no hay nada que el
//     agregador pueda hacer con un fallo salvo tragárselo (INV-10);
//   - **no devuelve la clasificación**, porque tarda segundos y esperarla aquí sería
//     justo lo que REQ-35 prohíbe — la respuesta vuelve por OnClassified;
//   - **no acepta `ctx`**: el contexto del turno se cancela en milisegundos y la
//     inferencia dura segundos, así que pasárselo mataría toda petición sin dar un solo
//     error. La firma impide el error en vez de advertirlo.
//
// Request tiene que volver enseguida (es un encolado, no una llamada al modelo).
type AheadRequester interface {
	Request(key intake.WindowKey, text string)
}

// IntakeAggregator acumula entrantes en ventanas y las cierra. Es seguro para uso
// concurrente: Observe corre desde la goroutine de cada entrante, OnClassified desde
// la del pool de clasificación y Sweep desde la de Run.
//
// NO es un EventSink y no cuelga del fan-out de efectos (en el viejo se llamó
// AggregatorSink hasta el 2026-08-22): EffectContext no lleva `wa_message_id`, y un
// turno puede producir CERO efectos —una ráfaga de texto libre pidiendo un
// presupuesto es justo eso—. Se alimenta del ENTRANTE.
//
// El estado de una ventana vive en `intake_jobs`, NO en este proceso: aquí no hay un
// mapa de ventanas que salvar (hallazgo 1 de F8). Lo que SÍ vive en memoria, y muere
// con el proceso sin perder ningún job, son las PISTAS de adelanto (ver OnClassified)
// y la memoria del último mensaje visto por tupla (ver Observe).
//
// En el rojo no lleva campos; nacen en el verde.
type IntakeAggregator struct{}

// AggregatorOption configura el agregador al construirlo.
type AggregatorOption func(*IntakeAggregator)

// WithAggregatorClock inyecta el reloj con el que el barrido decide si un plazo
// venció. nil se ignora (se queda time.Now). Los tests lo inyectan SIEMPRE: sin él,
// «silencio ⇒ flush a los 45 s» solo se podría probar durmiendo 45 segundos.
func WithAggregatorClock(now func() time.Time) AggregatorOption {
	panic(pendiente.Implementar("runtime.WithAggregatorClock"))
}

// WithSweepInterval fija cada cuánto barre Run. Un valor <= 0 se ignora y queda el
// default de plataforma: 5 segundos.
//
// El intervalo fija el GRANO del cierre, no su plazo: una ventana de 45 s se cierra
// entre los 45 y los 45+5 s.
func WithSweepInterval(d time.Duration) AggregatorOption {
	panic(pendiente.Implementar("runtime.WithSweepInterval"))
}

// WithSweepBatch fija cuántas ventanas vivas pide un barrido al almacén. Un valor
// <= 0 se ignora y queda el default de plataforma: 200.
//
// Es un TECHO DE TRABAJO POR PASADA, no un límite de negocio: lo que no entra sale en
// la pasada siguiente (las más antiguas van primero) y no se pierde nada.
func WithSweepBatch(n int) AggregatorOption {
	panic(pendiente.Implementar("runtime.WithSweepBatch"))
}

// WithIntentConfidence fija el umbral de confianza a partir del cual un
// IntentIntakeRequest adelanta el flush. Un valor <= 0 se ignora y queda el default de
// plataforma: 0.7. El valor inyectado SUSTITUYE al default, no se suma a él.
//
// 🔴 Es un default de PLATAFORMA y no una config por tenant, por D-044.26: leer un
// umbral por tenant en línea con el mensaje sería el `SELECT` prohibido. Errar por lo
// bajo no rompe nada (cierra una ventana antes de tiempo y el cliente puede abrir
// otra); errar por lo alto tampoco (la ventana cierra igual por silencio).
func WithIntentConfidence(threshold float64) AggregatorOption {
	panic(pendiente.Implementar("runtime.WithIntentConfidence"))
}

// WithSourceComposer inyecta el compositor del literal (T1.4). nil se ignora.
//
// AG-8: SIN esta opción el agregador usa un compositor VACÍO y documentado: las
// ventanas cierran igual y el job queda `pending` con el sobre a NULL (jobs sin
// texto), la forma que la migración 0072 permite a propósito.
//
// ⚠️ Si ese vacío corre en producción, el pipeline se queda sin texto Y NO HAY ERROR:
// el cable del arranque es lo único que lo impide (trampa T-4 de la fase: es del
// candado de cableado).
func WithSourceComposer(c SourceComposer) AggregatorOption {
	panic(pendiente.Implementar("runtime.WithSourceComposer"))
}

// WithAheadRequester inyecta quien PIDE la clasificación (T1.6-4). nil se ignora.
//
// AG-8: SIN esta opción el agregador no pide nada, así que no adelanta NUNCA por su
// cuenta y toda ventana cierra por su reloj — que es una forma legítima y el camino
// garantizado de T1.7, no una avería.
func WithAheadRequester(a AheadRequester) AggregatorOption {
	panic(pendiente.Implementar("runtime.WithAheadRequester"))
}

// NewIntakeAggregator construye el agregador. Nunca devuelve nil. Las opciones se
// aplican en orden sobre los defaults: reloj time.Now, barrido cada 5 s, 200 ventanas
// por pasada, umbral 0.7, compositor vacío y sin AheadRequester. No lee ni escribe
// nada al construir, ni arranca ninguna goroutine (eso es Run).
//
// Qué pasa con cada dependencia a nil:
//
//   - log, jobs o ents nil: Observe es un no-op seguro y Sweep devuelve 0;
//   - settings nil: NO apaga nada. Observe funciona igual (no la usa) y el barrido
//     cierra con los plazos de plataforma (45 s / 120 s). (El comentario del viejo,
//     aggregator.go:457-460, dice que «con cualquiera de los tres a nil Observe es un
//     no-op»; su código, :576 y :877, no trata así a settings. Manda el código.)
func NewIntakeAggregator(log logger.Logger, jobs intake.JobStore, settings AggregationSettings,
	ents entitlements.Resolver, opts ...AggregatorOption) *IntakeAggregator {
	panic(pendiente.Implementar("runtime.NewIntakeAggregator"))
}

// Observe mete UN entrante en su ventana. Es lo ÚNICO de este fichero que corre en
// línea con el mensaje del cliente.
//
// # AG-2 · No devuelve error (INV-10)
//
// La firma no tiene valor de error: un fallo de aquí NUNCA tumba el turno del
// cliente. Cualquier fallo se LOGUEA y Observe vuelve con normalidad, sin panic.
// Perder una ventana de captación es perder un presupuesto automático; cortar el turno
// es dejar al cliente sin respuesta.
//
// # AG-1 · El presupuesto de I/O (D-044.26), por entrante admitido
//
//   - EXACTAMENTE 1 escritura: jobs.OpenOrAppend, que abre la ventana si no existía y
//     le añade las referencias si ya existía. Lleva la Key, el MessageTS y las
//     referencias [WaMessageID, MediaRefs...] en ese orden. NUNCA el Text.
//   - CERO lecturas de `intake_jobs` (ni ListAggregating ni CloseWindow ni
//     PutSourceText), CERO lecturas de `tenant_settings`, cero cripto y cero red. El
//     cierre —también el adelantado por intent— lo ejecuta el barrido, nunca Observe.
//   - COMO MUCHO 1 pregunta al resolver de derechos (que cachea con TTL).
//
// El orden de los pasos es parte del contrato:
//
//  1. GUARDAS BARATAS, sin preguntarle nada a nadie: receptor nil, agregador sin log,
//     jobs o ents, WaMessageID "" (un entrante sin identificador no puede aportar una
//     referencia opaca), o Key incompleta (sin evento vivo no hay ventana: un saludo
//     suelto, el LIMBO, no abre nada). Vuelve sin escribir y SIN consultar el resolver.
//  2. EL GATE: ents.Has(ctx, tenant, entitlements.FeatureLLMIntake), UNA vez. Es el
//     ÚNICO gate de este camino: no se consulta `api_llm` (ADR-0044, D-044.28: la vía
//     —local o API— no es asunto de la ventana) ni ninguna otra feature. Sin la
//     feature vuelve en silencio: cero ventanas, cero jobs. Si el resolver FALLA es
//     fail-closed: no escribe y deja en Warn "agregador: no se pudo resolver la feature
//     llm_intake; el entrante no entra en ninguna ventana", con "error", "tenant_id" y
//     "session_id".
//  3. EL MISMO MENSAJE DOS VECES: si el WaMessageID es el ÚLTIMO que se observó para
//     esa misma Key, vuelve sin escribir (red SECUNDARIA; la primera es el dedupe
//     persistente de ingesta). Sin ella, un doble Observe duplicaría la referencia: el
//     UPSERT concatena a ciegas.
//  4. LA SENTENCIA: OpenOrAppend. Si falla, deja en Error "agregador: no se pudo
//     abrir/ampliar la ventana de captación; el turno sigue", con "error",
//     "tenant_id", "session_id" y "wa_message_id", y vuelve sin pedir nada.
//  5. EL ADELANTO, lo último: si hay AheadRequester y Text no es "", llama a
//     Request(Key, Text) UNA vez. Va DESPUÉS de que la ventana exista de verdad. Sin
//     texto (un mensaje de solo media) no se pide: es un motivo SANO (REQ-38).
//
// # AG-5 · La memoria del último mensaje NO se borra al cerrar la ventana
//
// La memoria del paso 3 guarda UN id por Key (el último) y sobrevive al cierre de la
// ventana. Consecuencias que el contrato promete:
//
//   - una RE-ENTREGA del mismo wa_message_id DESPUÉS del flush NO reabre ventana ni
//     escribe nada (trampa T-10: borrarla «por limpieza» reabriría ventanas con
//     mensajes ya procesados);
//   - un mensaje DISTINTO sí pasa y abre la ventana SIGUIENTE sobre el mismo evento
//     (el índice único de la 0072 es PARCIAL a propósito: un cliente puede volver a
//     pedir);
//   - vive en el proceso: un agregador NUEVO sobre el mismo almacén no la tiene.
//
// ⚠️ Rareza portada (aggregator.go:511-525): el id se anota como visto ANTES de la
// sentencia, así que si OpenOrAppend falla, la re-entrega inmediata de ESE MISMO id se
// descarta en el paso 3 y no reintenta la escritura.
//
// # PII
//
// Ninguna línea de log lleva el Text ni las MediaRefs: solo tenant_id, session_id y
// wa_message_id.
func (s *IntakeAggregator) Observe(ctx context.Context, ref IncomingRef) {
	panic(pendiente.Implementar("runtime.IntakeAggregator.Observe"))
}

// OnClassified recibe la clasificación que se pidió y aplica la política de disparo
// (T1.6-4). Es seguro sobre un receptor nil (no hace nada).
//
// # AG-3 · El intent solo ADELANTA
//
// La política mira el NOMBRE y la CONFIANZA y nada más (IntentHint): adelanta si y
// solo si intent == IntentIntakeRequest Y confidence >= umbral (0.7, o el de
// WithIntentConfidence). Por debajo del umbral, o con cualquier otro nombre, NO PASA
// NADA: ni pista, ni log, ni aviso. Adelantar es anotar una PISTA en memoria para esa
// Key: el cierre lo ejecuta el PRÓXIMO barrido, que la cierra sin esperar a ningún
// plazo. La pista no es estado: si el proceso muere con pistas dentro no se pierde
// ningún job (la ventana cierra por su reloj).
//
// No comprueba que la ventana siga viva (costaría un SELECT para no hacer nada). Por
// eso una respuesta TARDÍA, que llega cuando la ventana ya cerró, es INOCUA: su pista
// no casa con ninguna ventana viva y el barrido siguiente la tira sin tocar la fila
// cerrada. ⚠️ ACEPTADO: la pista es por Key, no por job, así que si entre tanto el
// cliente abrió OTRA ventana sobre el mismo evento, la pista tardía cierra esa ventana
// nueva antes de tiempo. No pierde mensajes ni duplica jobs.
//
// # AG-4 · Anotar bajo candado y DESPUÉS avisar
//
// Además de anotar, DESPIERTA al barrido de Run para que cierre YA y no en el
// siguiente tick (T1.8-1 (h): un reloj no sustituye a un evento que ya llegó). El
// orden no es intercambiable: primero se anota la pista, protegida, y DESPUÉS se
// avisa; quien recibe el aviso ve ya la pista. El aviso es NO BLOQUEANTE y de buffer
// 1: OnClassified vuelve enseguida aunque nadie esté escuchando (Run sin arrancar) y
// aunque se la llame mil veces seguidas; los avisos de más se descartan sin perder
// trabajo, porque el barrido que consume el aviso pendiente se lleva TODAS las pistas
// acumuladas.
func (s *IntakeAggregator) OnClassified(key intake.WindowKey, intent string, confidence float64) {
	panic(pendiente.Implementar("runtime.IntakeAggregator.OnClassified"))
}

// RecoverAtBoot es la RECUPERACIÓN DEL REINICIO (T1.1, AG-6): las ventanas que
// vencieron mientras no había proceso pasan a `pending`. Es literalmente UN Sweep, y
// eso es el diseño: el estado de una ventana vive en `intake_jobs`, así que arrancar
// no es «restaurar» nada, es mirar la tabla. Devuelve cuántas cerró.
//
// Si cerró alguna deja en Info "agregador: ventanas vencidas cerradas al arrancar"
// con la clave "jobs" (el número); si no cerró ninguna no loguea nada.
func (s *IntakeAggregator) RecoverAtBoot(ctx context.Context) int {
	panic(pendiente.Implementar("runtime.IntakeAggregator.RecoverAtBoot"))
}

// Run arranca el barrido periódico y BLOQUEA hasta que ctx se cancele; entonces
// vuelve. Sin broker (ADR-0003): un ticker de Go, ni cron ni cola externa. Sobre un
// receptor nil o un agregador sin jobs vuelve en el acto. Lo arranca el arranque del
// proceso en su propia goroutine (trampa T-4: olvidarlo deja las ventanas abiertas
// para siempre, en silencio).
//
// Hace, en orden: UN RecoverAtBoot al entrar y, después, un Sweep por cada una de dos
// señales, hasta la cancelación:
//
//   - EL TICK, cada intervalo de barrido (5 s, o WithSweepInterval): es quien vigila
//     los dos plazos de la ventana, que no tienen evento que los anuncie, y quien cubre
//     un aviso perdido. En producción NADIE llama a Sweep a mano: lo llama este tick.
//   - EL DESPERTADOR de OnClassified (AG-4): un intent seguro cierra su ventana AHORA,
//     sin esperar al tick.
//
// AG-6: NO hay un timer por ventana. Un timer vivo en memoria es lo que un despliegue
// se lleva por delante; el plazo se recalcula en cada barrido desde las fechas de la
// fila. Si el proceso muere con una ventana abierta no se pierde nada: el proceso
// nuevo arranca, barre y cierra lo que venció.
//
// # La parada no es un error (D-F9-10)
//
// Contexto cancelado → Run vuelve SIN loguear a ERROR. Con ctx ya cancelado, un fallo
// del almacén (listar o cerrar) o del compositor NO se registra en ERROR, ni en el
// RecoverAtBoot inicial ni en un barrido que la cancelación pilló a medias: no es una
// avería, es el proceso apagándose. (El viejo sí las registraba: «agregador: no se
// pudieron listar las ventanas vivas».) Con el contexto VIVO, esos mismos fallos SÍ
// van a ERROR, como dice Sweep.
func (s *IntakeAggregator) Run(ctx context.Context) {
	panic(pendiente.Implementar("runtime.IntakeAggregator.Run"))
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
// Quien cierra es jobs.CloseWindow, cuyo guard de estado hace que dos barridos
// solapados —o dos procesos— no puedan producir dos jobs. Si CloseWindow contesta
// false sin error (otro llegó antes), la ventana NO se cuenta, NO se compone y no es
// un error. Un segundo Sweep sin que nada cambie devuelve 0 y no toca ninguna fila.
//
// # Las pistas
//
// Cada pasada se lleva TODAS las pistas acumuladas y las consume, casen o no con una
// ventana de la pasada: la de una ventana ya cerrada se tira; la de una ventana que el
// `batch` dejó fuera se pierde y esa ventana cierra por su reloj. Si listar falla, las
// pistas NO se consumen.
//
// # El compositor (T1.4, AG-8)
//
// Tras CADA cierre que hizo esta llamada, y solo entonces, llama UNA vez a
// ComposeAtFlush(ctx, Key). Su fallo NO revierte el cierre: la ventana cuenta como
// cerrada y el job queda `pending` con el sobre vacío.
//
// ⚠️ DEUDA CONOCIDA, D-F7-9 (hallazgo 17 de F7). Lo que el viejo promete HOY sobre
// ese orden es exactamente esto y nada más: PRIMERO la transición `aggregating →
// pending` (aggregator.go:892), DESPUÉS la composición y la escritura del sobre
// (aggregator.go:906 → source_composer.go:347-377), en sentencias separadas y SIN
// atomicidad; el fallo de la segunda no deshace la primera. NO promete que el job sea
// invisible para el worker hasta tener sobre: en ese hueco el worker puede reclamar un
// job `pending` sin `source_text`. Este contrato NO arregla ni promete otra cosa: que
// cierre y sobre sean un solo acto, o que el sobre preceda a la visibilidad, lo decide
// el verde (F8-05), en su propio commit.
//
// # Logs
//
//   - listar falla: Error "agregador: no se pudieron listar las ventanas vivas", con
//     "error"; devuelve 0.
//   - cerrar falla: Error "agregador: no se pudo cerrar la ventana de captación", con
//     "error", "tenant_id", "session_id" y "job_id"; esa ventana no cuenta y la pasada
//     sigue con las demás.
//   - el compositor falla: Error "agregador: la ventana se cerró pero el literal no se
//     pudo componer (T1.4)", con "error", "tenant_id" y "job_id".
//   - cada ventana cerrada: Debug "agregador: ventana cerrada", con "tenant_id",
//     "session_id" y "job_id".
//
// Los tres Error callan si ctx ya está cancelado (D-F9-10, ver Run).
func (s *IntakeAggregator) Sweep(ctx context.Context) int {
	panic(pendiente.Implementar("runtime.IntakeAggregator.Sweep"))
}
