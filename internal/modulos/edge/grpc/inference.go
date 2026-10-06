// Porta internal/gateway/grpc/inference.go @ ec236b3 (el vocabulario de la inferencia:
// InferRequest, InferError, motivos, clases, centinelas y los dos plazos). Trozo de
// inference.go, partido por E-13: el despacho vive en inference_dispatch.go y la
// espera del resultado en inference_result.go.

package grpc

import (
	"errors"
	"fmt"
	"time"
)

// ============================================================================
// EL TRANSPORTE DE LA INFERENCIA LOCAL (Plan 044 · Ola 1.6 · T1.6-3, REQ-34,
// D-044.29, ADR-0045)
//
// El Cloud arma el prompt y el Ollama del EDGE lo ejecuta. Este fichero es el
// cable: empuja un InferenceRequest por el stream CloudLink del tenant y espera
// el InferenceResult correlacionado por command_id. Nada de este fichero sabe qué
// es un prompt, un catálogo o una intención — eso vive en el adaptador
// (internal/llmvia/local), que es quien consume Infer.
//
// Partido en tres por tamaño (E-13): aquí el VOCABULARIO (qué se pide, con qué
// palabras falla); en inference_dispatch.go, por qué stream sale; en
// inference_result.go, la espera y la lectura del resultado.
//
// # 🔴 LA DECISIÓN: LA CORRELACIÓN ES UN MAPA EN MEMORIA, NO UNA FILA EN LA BASE
//
// El molde que primero viene a la cabeza es DiagnosticsRequest/DiagnosticsBundle,
// que correlaciona por command_id contra una FILA `pending` en Postgres. Aquí NO
// se usa ese molde, y la evidencia es esta:
//
//  1. **El resultado vuelve por el MISMO stream por el que se pidió.** Y no es una
//     casualidad afortunada: Registry.Push solo puede empujar por un stream
//     REGISTRADO EN ESTA RÉPLICA (el Registry es un mapa en memoria del proceso),
//     así que la réplica que pide es, POR CONSTRUCCIÓN, la única que puede pedir y
//     la única a la que puede llegar la respuesta. Una fila en la base no compraría
//     nada de lo que se supone que compra: la réplica que NO tiene el stream no
//     puede mandar el frame, con fila o sin ella.
//  2. **El peticionario es SÍNCRONO y vive en este proceso.** llm.LLMProvider
//     devuelve `(json.RawMessage, error)`: quien llama está bloqueado esperando. Si
//     el proceso muere, muere con él — no hay nadie a quien entregarle después una
//     fila recuperada.
//  3. **Por qué diagnostics SÍ necesita la fila, que es lo que hace la diferencia.**
//     Su fila no está ahí por multi-réplica en el EMPUJE (RequestDiagnostics también
//     usa registry.Push y también exige el stream local): está ahí porque su lector
//     está desacoplado EN EL TIEMPO. El handler responde 202 con `status:"pending"`
//     y el bundle se consulta MÁS TARDE, en otra petición HTTP que sí puede caer en
//     otra réplica. Eso es persistencia para un lector futuro, no correlación.
//
// El precedente correcto es el otro: s.acks (send.go), donde SendText/SendMedia
// —llamantes síncronos— esperan un Ack correlacionado por command_id con un mapa en
// memoria y reloj propio. La inferencia es su gemela, y lo dice pendingInfer.
//
// ⚠️ LO QUE ESTA DECISIÓN CUESTA, dicho por su nombre: con N réplicas, una
// inferencia pedida desde la réplica que NO sostiene el stream del Edge falla con
// `edge_offline` en vez de servirse. Es un fallo HONESTO —el frame no puede salir de
// ahí— y degrada a Nivel A con su aviso, que es exactamente la conducta que REQ-38
// pide. Servirlo de verdad exigiría un despacho entre réplicas (LISTEN/NOTIFY, o una
// tabla que sondee la réplica dueña del stream), que es otro diseño y no lo pide
// ninguna tarea de este plan. Queda escrito para que quien monte la segunda réplica
// lo encuentre antes de que se lo cuente un dashboard.
// ============================================================================

// DefaultInferGrace es el margen que el Cloud espera POR ENCIMA del timeout_ms que
// le dio al Edge. Sin margen, los dos relojes vencerían a la vez y el Cloud se
// rendiría JUSTO cuando el Edge está mandando su INFERENCE_ERROR_TIMEOUT: se
// perdería el error NOMBRADO —el que dice qué pasó— y en su lugar se registraría un
// «no contestó» genérico. Cinco segundos es el ida y vuelta del frame más el tiempo
// del Edge en construir su respuesta, con holgura.
//
// 🔴 ESTÁ EXPORTADA PORQUE EL LLAMANTE TIENE QUE RESERVARLA DE SU PROPIO PLAZO, y
// eso no se puede hacer sin conocer el número. Quien deriva su Timeout del deadline
// que le dieron (internal/llmvia/local) resta un margen que debe cubrir ESTE, o el
// timer de awaitInference vencería DESPUÉS del ctx del llamante y el veredicto lo
// emitiría un `ctx.Done()` —sin motivo, sin aviso— en vez del Edge, que es quien
// sabe qué pasó. Es la misma aritmética que el MargenSocket del Edge, vista desde
// el otro extremo del cable: el plazo de DENTRO vence primero, siempre.
const DefaultInferGrace = 5 * time.Second

// defaultInferTimeout es el presupuesto de la inferencia cuando el llamante no fija
// uno. No pretende ser el bueno para nada en concreto: quien conoce su ventana es el
// llamante (los 45 s de agregación del Nivel C, el turno acotado del Nivel B), y por
// eso el timeout viaja en la petición. Este valor solo evita que un llamante
// descuidado deje una inferencia sin techo.
const defaultInferTimeout = 30 * time.Second

// Vocabulario CERRADO de motivos por los que una inferencia no dio salida.
//
// 🔴 SON LITERALMENTE LOS MISMOS VALORES QUE degradation.Reason, y esa coincidencia
// es el mecanismo: quien consume InferError.Motivo() lo convierte a un motivo de
// notificación sin tabla de traducción, así que no hay dos listas que se puedan
// desincronizar en silencio. Lo custodia un test de este paquete que compara este
// conjunto contra el vocabulario de degradación — el test vive en el lado del
// ESCRITOR (aquí), porque es aquí donde se escribiría el literal equivocado. (Mientras
// `degradation` siga en el árbol viejo, el test lo nombra con literales: R-G14.)
//
// En el paquete viejo se llamaban Motivo* (E-11); los VALORES no cambian.
//
// Los cinco primeros son 1:1 con el enum InferenceError del proto (menos
// UNSPECIFIED, que no viaja). El sexto no viene del Edge: lo produce el Cloud
// cuando no hay stream por donde preguntar, que es un fallo de la vía igual de real
// que los otros y que el dueño tiene que poder ver.
const (
	// ReasonOllamaDown — el proveedor local del Edge no responde.
	ReasonOllamaDown = "ollama_down"
	// ReasonBreakerOpen — el breaker del Edge está abierto (ADR-0042).
	ReasonBreakerOpen = "breaker_open"
	// ReasonTimeout — la inferencia no respondió dentro del plazo. Lo produce el
	// Edge (INFERENCE_ERROR_TIMEOUT) y también el Cloud cuando se le agota su propio
	// presupuesto sin que llegue ninguna respuesta.
	ReasonTimeout = "timeout"
	// ReasonLeaseInvalid — el Edge no tiene lease vigente (ADR-0007).
	ReasonLeaseInvalid = "lease_invalid"
	// ReasonEdgeSinCapacidad — el semáforo de concurrencia del Edge rechazó la
	// petición: la máquina del cliente está saturada.
	ReasonEdgeSinCapacidad = "edge_sin_capacidad"
	// ReasonEdgeOffline — no hay sesión viva del tenant por la que mandar el frame,
	// o el stream se cayó mientras se esperaba la respuesta. Es el ÚNICO motivo que
	// no viene del Edge: lo decide el Cloud, porque es el Cloud quien sabe que no
	// hay a quién preguntar.
	ReasonEdgeOffline = "edge_offline"
)

// inferenceReasons (en el paquete viejo, motivosInferencia) es el vocabulario en forma
// recorrible, para el test de simetría con el de degradación. No se exporta: quien lo necesita fuera usa el motivo
// que trae el error concreto, no la lista.
var inferenceReasons = []string{
	ReasonOllamaDown,
	ReasonBreakerOpen,
	ReasonTimeout,
	ReasonLeaseInvalid,
	ReasonEdgeSinCapacidad,
	ReasonEdgeOffline,
}

// Errores de inferencia SIN motivo de degradación, y esa ausencia es la decisión. (En
// el paquete viejo: ErrInferenceSinClaveDeCifrado, ErrInferenceSelladoIlegible,
// ErrInferenceSinSalida y ErrInferenceAbandonada; los TEXTOS no cambian.)
//
// 🔴 EL VOCABULARIO DE MOTIVOS ES CERRADO Y NO SE ENSANCHA PARA TAPAR ESTOS TRES.
// Los tres significan «el fallo es NUESTRO o del protocolo», no «la vía del tenant
// se degradó»: notificar al dueño con cualquiera de los seis motivos sería mentirle
// sobre la causa y mandarlo a mirar su equipo, que está perfectamente. Se devuelven
// como errores pelados (no *InferError), así que el decorador de notificación no
// encuentra motivo y NO escribe aviso — el fallo se ve en el log de ERROR, que es
// donde le toca verse a un defecto de la nube.
var (
	// ErrInferenceNoEncryptionKey — llegó una salida sellada y la nube no tiene
	// configurada su privada X25519. No se puede abrir, y no es culpa del Edge.
	ErrInferenceNoEncryptionKey = errors.New("gatewaygrpc: inferencia sellada pero la nube no tiene clave de cifrado")
	// ErrInferenceSealedUnreadable — el sobre no abre o no deserializa. Sellado
	// corrupto, claves cruzadas o un Edge que selló mal.
	ErrInferenceSealedUnreadable = errors.New("gatewaygrpc: la salida sellada de la inferencia no se pudo leer")
	// ErrInferenceNoOutput — el InferenceResult llegó con el oneof VACÍO: ni
	// enc_output ni error. Es una anomalía de protocolo (el contrato exige una de
	// las dos ramas), no una degradación.
	ErrInferenceNoOutput = errors.New("gatewaygrpc: InferenceResult sin salida ni error")
	// ErrInferenceAbandoned — el LLAMANTE se rindió: su contexto venció o lo
	// cancelaron antes de que llegara la respuesta. Viaja junto a ctx.Err() (doble
	// %w) y es el hermano de session.ErrPushAbandoned, con el mismo argumento
	// detrás (Plan 050, Enmienda 1, regla 2): «el llamante se rindió» y «el Edge no
	// contestó» son fallos DISTINTOS, y fundirlos borra la señal. Aquí, además,
	// decide si se avisa al dueño: la ventana de agregación que se cierra o el
	// proceso que se apaga NO son una degradación de la vía del tenant.
	ErrInferenceAbandoned = errors.New("gatewaygrpc: el llamante se rindió esperando la inferencia")
)

// InferError es el fallo de una inferencia CON motivo de degradación: el vocabulario
// cerrado de arriba, a mano del llamante.
//
// Se consume por DUCK-TYPING (`interface{ Motivo() string }`), igual que
// SendError.CommandID() y SendError.StreamCaido(), y por la misma razón escrita
// allí: el contrato es la interfaz anónima, no un tipo compartido, y ese desacople
// es lo que permite que el adaptador LLM y el escritor de notificaciones no tengan
// que importar el Gateway.
type InferError struct {
	commandID string
	sessionID string
	reason    string
	err       error
}

// Motivo devuelve el motivo del vocabulario cerrado. Es el método que el escritor de
// notificaciones consume por duck-typing, y por eso NO se renombra (E-11): quien lo
// lee (llmvia) lo busca por ese nombre, y un renombrado lo apagaría sin ningún rojo.
func (e *InferError) Motivo() string { return e.reason }

// CommandID devuelve el command_id de la inferencia que falló. Vacío si el fallo
// ocurrió ANTES de generarlo (no había sesión a la que preguntar).
func (e *InferError) CommandID() string { return e.commandID }

// SessionID devuelve la sesión por cuyo stream se pidió (o se iba a pedir).
func (e *InferError) SessionID() string { return e.sessionID }

// Error implementa error. NO incluye el prompt ni la salida: un log de error no es
// sitio para el texto del cliente (INV-6). El prefijo `gatewaygrpc:` es texto
// observable y se conserva aunque el paquete se llame grpc (T-15).
func (e *InferError) Error() string {
	return fmt.Sprintf("gatewaygrpc: inferencia %s por la sesión %s: %s: %v",
		e.commandID, e.sessionID, e.reason, e.err)
}

// Unwrap expone la causa para errors.Is/As.
func (e *InferError) Unwrap() error { return e.err }

// inferErr envuelve una causa con su motivo. Un err nil devuelve nil.
func inferErr(cmdID, sessionID, reason string, err error) error {
	if err == nil {
		return nil
	}
	return &InferError{commandID: cmdID, sessionID: sessionID, reason: reason, err: err}
}

// InferRequest es lo que el Cloud le pide al Edge. Es el frame del proto sin los dos
// campos que decide el transporte (command_id y session_id).
type InferRequest struct {
	// Prompt es el prompt YA CONSTRUIDO. El Edge lo entrega al modelo verbatim.
	Prompt string
	// Format es el formato esperado ("json" o un JSON Schema serializado). Viaja
	// opaco: ni el Cloud ni el Edge lo parsean aquí.
	Format string
	// Temperature es la temperatura de muestreo. Viaja SIEMPRE con presencia
	// explícita en el frame (el campo del proto es `optional` justo para esto): 0.0
	// es el valor que más se va a pedir y a la vez el cero del campo.
	Temperature float64
	// Timeout es el presupuesto de ESTA inferencia, el que el Edge respeta. <= 0 ⇒
	// 30 s (defaultInferTimeout). El Cloud espera este plazo MÁS DefaultInferGrace.
	Timeout time.Duration
	// OriginSessionID es, cuando el Cloud lo sabe, la sesión de WhatsApp cuya
	// conversación originó la pregunta. Es OPCIONAL y su papel es doble: viaja en el
	// frame como trazabilidad, y si esa sesión está viva se usa como sesión de
	// empuje (ver inferenceSession).
	OriginSessionID string
	// TargetSessionID fuerza POR DÓNDE SALE la petición, y NO viaja en el payload.
	//
	// 🔴 ES LA OTRA MITAD DE UNA DISTINCIÓN QUE ESTE FICHERO YA HACÍA, y por eso es
	// un campo aparte y no un segundo uso de OriginSessionID: el session_id del
	// ENVELOPE es el cable, el del PAYLOAD es la conversación que preguntó (ver el
	// ⚠️ de inferToCloud). Hasta T1.7-4 las dos cosas se pedían con el mismo campo
	// porque siempre coincidían — toda inferencia nacía de una conversación—. El
	// calentamiento rompe esa coincidencia: no lo originó ninguna conversación, pero
	// TIENE que salir por un Edge concreto (el que acaba de conectar, o cada uno de
	// los que recibieron el ConfigUpdate), porque la caché de prefijo que viene a
	// llenar es de ESE Ollama y de ningún otro. Rellenar OriginSessionID para
	// conseguir el enrutado pondría en el cable un dato de trazabilidad FALSO.
	//
	// Vacío ⇒ manda OriginSessionID, y si tampoco hay, la política de siempre.
	TargetSessionID string
	// MaxOutputTokens es el presupuesto de SALIDA de esta inferencia, en tokens
	// (campo 7 del frame, `optional`). Lo fija el CLOUD y lo fija POR TAREA, porque
	// es quien conoce el esquema de la respuesta que espera; el Edge lo traduce a
	// `num_predict`. <= 0 ⇒ NO se pone en el frame y el Edge aplica su default (hoy
	// 256), que es fail-closed hacia el lado barato.
	//
	// Los números por etapa, con su aritmética, viven en llmvia/local (ver `etapa`):
	// aquí solo viaja el que le pasen. Este paquete es el transporte y no tiene
	// opinión sobre cuánto ocupa la respuesta de una P3.
	MaxOutputTokens int32
	// Class es la naturaleza declarada de la petición (campo 8): ClassInteractive o
	// ClassBatch. El gateway no lo valida: viaja tal cual. Es SOLO TELEMETRÍA — rótulo de log y del parte del Edge.
	//
	// 🔴 PROHIBIDO DECIDIR CON ESTE CAMPO, y la prohibición es del contrato, no de
	// estilo (ver el proto). Quien necesite que el Edge EXCLUYA una petición del
	// breaker tiene el campo Warmup; quien necesite que el breaker sea más tolerante
	// con una petición lenta tiene su `timeout_ms`, que es de lo que el umbral por
	// petición se deriva (ADR-0042).
	Class string
	// Warmup marca esta inferencia como de CALENTAMIENTO de la caché de prefijo del
	// Edge (campo 10). Su salida SE DESCARTA: nadie la espera.
	//
	// Qué obliga al Edge: excluirla del breaker ANTES de evaluar, ni como fallo ni
	// como lentitud — un calentamiento paga prefill FRÍO por diseño (~50 s para un
	// P1 de UAT) y un breaker que lo mirara abriría el circuito por haber trabajado
	// bien. ⚠️ Lo que NO cambia: SÍ ocupa la plaza única y SÍ pasa por el aforo,
	// como cualquier otra inferencia.
	Warmup bool
}

// ClassInteractive y ClassBatch (en el paquete viejo, ClaseInteractivo y ClaseLote) son
// el vocabulario CERRADO del campo `class` (8). Los valores no cambian.
//
// Están aquí, junto a InferRequest, porque son vocabulario del CABLE y no de un
// llamante: el Edge los lee como rótulo y cualquier otro valor —o el vacío— se
// etiqueta `interactivo` sin error. Dos constantes evitan que el tercer llamante
// escriba "batch" y estrene una categoría fantasma que nadie sumará nunca.
const (
	// ClassInteractive: alguien espera al otro lado de WhatsApp.
	ClassInteractive = "interactivo"
	// ClassBatch: trabajo de fondo, sin nadie esperando el turno.
	ClassBatch = "lote"
)

// inferTimeout resuelve el presupuesto efectivo de la inferencia: un plazo <= 0 es
// defaultInferTimeout (D-F2-10: el cero nunca es «sin reloj»).
func inferTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return defaultInferTimeout
	}
	return d
}
