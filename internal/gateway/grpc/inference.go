package gatewaygrpc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/protobuf/proto"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
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
// memoria y reloj propio. Este fichero es su gemelo, y lo dice pendingInfer.
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
// conjunto contra degradation.Reasons() — el test vive en el lado del ESCRITOR
// (aquí), porque es aquí donde se escribiría el literal equivocado.
//
// Los cinco primeros son 1:1 con el enum InferenceError del proto (menos
// UNSPECIFIED, que no viaja). El sexto no viene del Edge: lo produce el Cloud
// cuando no hay stream por donde preguntar, que es un fallo de la vía igual de real
// que los otros y que el dueño tiene que poder ver.
const (
	// MotivoOllamaDown — el proveedor local del Edge no responde.
	MotivoOllamaDown = "ollama_down"
	// MotivoBreakerOpen — el breaker del Edge está abierto (ADR-0042).
	MotivoBreakerOpen = "breaker_open"
	// MotivoTimeout — la inferencia no respondió dentro del plazo. Lo produce el
	// Edge (INFERENCE_ERROR_TIMEOUT) y también el Cloud cuando se le agota su propio
	// presupuesto sin que llegue ninguna respuesta.
	MotivoTimeout = "timeout"
	// MotivoLeaseInvalid — el Edge no tiene lease vigente (ADR-0007).
	MotivoLeaseInvalid = "lease_invalid"
	// MotivoEdgeSinCapacidad — el semáforo de concurrencia del Edge rechazó la
	// petición: la máquina del cliente está saturada.
	MotivoEdgeSinCapacidad = "edge_sin_capacidad"
	// MotivoEdgeOffline — no hay sesión viva del tenant por la que mandar el frame,
	// o el stream se cayó mientras se esperaba la respuesta. Es el ÚNICO motivo que
	// no viene del Edge: lo decide el Cloud, porque es el Cloud quien sabe que no
	// hay a quién preguntar.
	MotivoEdgeOffline = "edge_offline"
)

// motivosInferencia es el vocabulario en forma recorrible, para el test de simetría
// con degradation.Reasons(). No se exporta: quien lo necesita fuera usa el motivo
// que trae el error concreto, no la lista.
var motivosInferencia = []string{
	MotivoOllamaDown,
	MotivoBreakerOpen,
	MotivoTimeout,
	MotivoLeaseInvalid,
	MotivoEdgeSinCapacidad,
	MotivoEdgeOffline,
}

// Errores de inferencia SIN motivo de degradación, y esa ausencia es la decisión.
//
// 🔴 EL VOCABULARIO DE MOTIVOS ES CERRADO Y NO SE ENSANCHA PARA TAPAR ESTOS TRES.
// Los tres significan «el fallo es NUESTRO o del protocolo», no «la vía del tenant
// se degradó»: notificar al dueño con cualquiera de los seis motivos sería mentirle
// sobre la causa y mandarlo a mirar su equipo, que está perfectamente. Se devuelven
// como errores pelados (no *InferError), así que el decorador de notificación no
// encuentra motivo y NO escribe aviso — el fallo se ve en el log de ERROR, que es
// donde le toca verse a un defecto de la nube.
var (
	// ErrInferenceSinClaveDeCifrado — llegó una salida sellada y la nube no tiene
	// configurada su privada X25519. No se puede abrir, y no es culpa del Edge.
	ErrInferenceSinClaveDeCifrado = errors.New("gatewaygrpc: inferencia sellada pero la nube no tiene clave de cifrado")
	// ErrInferenceSelladoIlegible — el sobre no abre o no deserializa. Sellado
	// corrupto, claves cruzadas o un Edge que selló mal.
	ErrInferenceSelladoIlegible = errors.New("gatewaygrpc: la salida sellada de la inferencia no se pudo leer")
	// ErrInferenceSinSalida — el InferenceResult llegó con el oneof VACÍO: ni
	// enc_output ni error. Es una anomalía de protocolo (el contrato exige una de
	// las dos ramas), no una degradación.
	ErrInferenceSinSalida = errors.New("gatewaygrpc: InferenceResult sin salida ni error")
	// ErrInferenceAbandonada — el LLAMANTE se rindió: su contexto venció o lo
	// cancelaron antes de que llegara la respuesta. Viaja junto a ctx.Err() (doble
	// %w) y es el hermano de session.ErrPushAbandonado, con el mismo argumento
	// detrás (Plan 050, Enmienda 1, regla 2): «el llamante se rindió» y «el Edge no
	// contestó» son fallos DISTINTOS, y fundirlos borra la señal. Aquí, además,
	// decide si se avisa al dueño: la ventana de agregación que se cierra o el
	// proceso que se apaga NO son una degradación de la vía del tenant.
	ErrInferenceAbandonada = errors.New("gatewaygrpc: el llamante se rindió esperando la inferencia")
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
	motivo    string
	err       error
}

// Motivo devuelve el motivo del vocabulario cerrado. Es el método que el escritor de
// notificaciones consume por duck-typing.
func (e *InferError) Motivo() string { return e.motivo }

// CommandID devuelve el command_id de la inferencia que falló. Vacío si el fallo
// ocurrió ANTES de generarlo (no había sesión a la que preguntar).
func (e *InferError) CommandID() string { return e.commandID }

// SessionID devuelve la sesión por cuyo stream se pidió (o se iba a pedir).
func (e *InferError) SessionID() string { return e.sessionID }

// Error implementa error. NO incluye el prompt ni la salida: un log de error no es
// sitio para el texto del cliente (INV-6).
func (e *InferError) Error() string {
	return fmt.Sprintf("gatewaygrpc: inferencia %s por la sesión %s: %s: %v",
		e.commandID, e.sessionID, e.motivo, e.err)
}

// Unwrap expone la causa para errors.Is/As.
func (e *InferError) Unwrap() error { return e.err }

// inferErr envuelve una causa con su motivo. Un err nil devuelve nil.
func inferErr(cmdID, sessionID, motivo string, err error) error {
	if err == nil {
		return nil
	}
	return &InferError{commandID: cmdID, sessionID: sessionID, motivo: motivo, err: err}
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
	// defaultInferTimeout. El Cloud espera este plazo MÁS DefaultInferGrace.
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
	// Class es la naturaleza declarada de la petición (campo 8): ClaseInteractivo o
	// ClaseLote. Es SOLO TELEMETRÍA — rótulo de log y del parte del Edge.
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

// ClaseInteractivo y ClaseLote son el vocabulario CERRADO del campo `class` (8).
//
// Están aquí, junto a InferRequest, porque son vocabulario del CABLE y no de un
// llamante: el Edge los lee como rótulo y cualquier otro valor —o el vacío— se
// etiqueta `interactivo` sin error. Dos constantes evitan que el tercer llamante
// escriba "batch" y estrene una categoría fantasma que nadie sumará nunca.
const (
	// ClaseInteractivo: alguien espera al otro lado de WhatsApp.
	ClaseInteractivo = "interactivo"
	// ClaseLote: trabajo de fondo, sin nadie esperando el turno.
	ClaseLote = "lote"
)

// rutaPreferida resuelve la PRECEDENCIA de enrutado —cable primero, conversación
// después— sin tocar inferenceSession, que sigue respondiendo a la pregunta de
// siempre («¿por qué stream sale esto?») con un único candidato preferido.
func (r InferRequest) rutaPreferida() string {
	if r.TargetSessionID != "" {
		return r.TargetSessionID
	}
	return r.OriginSessionID
}

// Infer pide una inferencia al Edge del tenant y devuelve el JSON CRUDO tal cual lo
// produjo el modelo (Plan 044 · Ola 1.6 · T1.6-3, REQ-34).
//
// Devuelve el texto SIN interpretar: no lo parsea, no lo valida y no comprueba que
// sea JSON. El contrato del proto es explícito en que si el modelo devolvió algo que
// no es JSON, eso exactamente es lo que debe llegar arriba; quien extrae y valida es
// el caller (llm.ExtractJSON y los Parse... del módulo compartido).
//
// El error puede ser:
//
//   - *InferError, con Motivo() del vocabulario cerrado ⇒ la vía se degradó y el
//     dueño debe enterarse (REQ-38).
//   - ErrInferenceAbandonada / ErrInferenceSinClaveDeCifrado /
//     ErrInferenceSelladoIlegible / ErrInferenceSinSalida ⇒ SIN motivo, no se avisa
//     a nadie (ver el bloque de errores de arriba).
//
// ⚠️ DOS RELOJES, DISTINGUIBLES A PROPÓSITO, igual que en Registry.Push: el del
// llamante y el presupuesto propio (Timeout + inferGrace). Un select que los mezclara
// en un solo ctx no podría decir cuál venció, y de esa distinción depende si se avisa
// al dueño o no.
func (s *Server) Infer(ctx context.Context, tenantID string, req InferRequest) (string, error) {
	sessionID, ok := s.inferenceSession(tenantID, req.rutaPreferida())
	if !ok {
		return "", inferErr("", "", MotivoEdgeOffline,
			fmt.Errorf("%w: el tenant no tiene ninguna sesión viva en esta réplica", session.ErrSessionOffline))
	}

	cmdID, err := newCommandID()
	if err != nil {
		return "", err
	}

	ch := make(chan *cloudlinkv1.InferenceResult, 1)
	s.infersMu.Lock()
	s.infers[cmdID] = pendingInfer{ch: ch, sessionID: sessionID}
	s.infersMu.Unlock()
	defer s.clearInfer(cmdID)

	if pushErr := s.registry.Push(ctx, sessionID, inferToCloud(cmdID, sessionID, req)); pushErr != nil {
		if errors.Is(pushErr, session.ErrPushAbandonado) {
			// El llamante se rindió mientras se empujaba: no es una degradación de la
			// vía, es su propio reloj. Sin motivo ⇒ sin aviso.
			return "", fmt.Errorf("%w: %w", ErrInferenceAbandonada, pushErr)
		}
		return "", inferErr(cmdID, sessionID, motivoDePush(pushErr), pushErr)
	}

	return s.awaitInference(ctx, ch, cmdID, sessionID, req.Timeout)
}

// motivoDePush traduce el fallo del empuje. Solo dos desenlaces llegan aquí, porque
// ErrPushAbandonado lo filtra Infer antes:
//
//   - ErrSessionOffline ⇒ edge_offline. La sesión se fue entre la selección y el
//     empuje (carrera legítima: el Edge se desconectó en ese hueco).
//   - ErrPushTimeout ⇒ timeout. El Edge NO LEE su stream: está vivo para gRPC y
//     atascado de verdad, que es un fallo de la vía y no del llamante.
func motivoDePush(err error) string {
	if errors.Is(err, session.ErrSessionOffline) {
		return MotivoEdgeOffline
	}
	return MotivoTimeout
}

// inferenceSession elige POR QUÉ STREAM sale el frame.
//
// 🔴 EL PROBLEMA, PORQUE NO ES OBVIO: Registry.Push exige un session_id, pero
// InferenceRequest.session_id va normalmente VACÍO — la inferencia es de alcance
// EDGE (un proceso, un Ollama), no de una sesión de WhatsApp. Es decir: hay que
// elegir una sesión de la que solo se usa el CABLE.
//
// Que elegir cualquiera sea correcto lo garantiza el ADR-0008: un Edge multiplexa
// TODAS sus sesiones sobre UN SOLO stream, así que dos sesiones del mismo Edge son
// literalmente el mismo cable y el mismo proceso. Lo único que la elección decide de
// verdad es QUÉ EDGE atiende cuando un tenant tiene varias instalaciones — y para
// eso vale cualquiera: cada una tiene su propio Ollama.
//
// El criterio, en dos pasos, sobre el candidato que le pase el llamante (que
// InferRequest.rutaPreferida resuelve: TargetSessionID si lo hay —el cable que se
// exige, sin conversación detrás—, y si no OriginSessionID):
//
//  1. **El candidato, si está vivo.** En el caso normal es la conversación que
//     generó la pregunta, así que el frame sale por el mismo Edge que recibió el
//     mensaje del cliente y el session_id del frame dice algo cierto (trazabilidad,
//     que es justo para lo que el proto declara ese campo).
//
//  2. **Si no, el mejor candidato del tenant que NO haya dicho que no puede**
//     (Plan 057 · Ola 3, REQ-057.10). Se descartan los Edge que declararon
//     `INFERENCE_READINESS_DOWN` —mandarles el prompt solo podía volver como
//     `ollama_down` tras esperar el presupuesto entero— y se prefiere a los que
//     dijeron READY sobre los que no dicen nada.
//
//     El orden alfabético se conserva DENTRO de cada grupo, y por el motivo
//     original: el recorrido de un map de Go está ALEATORIZADO, así que sin ordenar,
//     dos peticiones seguidas del mismo tenant podrían irse a Edges distintos.
//     Ordenar las manda al mismo, que es lo que hace que el breaker del Edge
//     (ADR-0042) y el modelo ya cargado en memoria signifiquen algo. No se persigue
//     balancear: no hay medida que diga que haga falta, y un reparto aleatorio es
//     peor que uno estable.
//
// 🔴 EL PASO 1 NO MIRA LA READINESS, Y ESO ES DOCTRINA, NO UN OLVIDO. El Edge que
// sostiene la conversación es el único que la atiende: la inferencia jamás cruza
// entre nodos Edge. Si su Ollama está caído, lo correcto es un `ollama_down` honesto
// del Edge que de verdad tiene el hilo, no una respuesta fabricada por la máquina de
// otra instalación del mismo cliente, que no ha visto ni un mensaje de esa charla.
// El desempate del paso 2 solo entra cuando NO hay conversación detrás.
//
// El Registry NO sabe de tenants (es map[session_id]) y no hizo falta ampliarlo: el
// índice por tenant ya existía en el Server (edgeSessions → sessionsForTenant), que
// es el mismo que usa el push de config del ADR-0021.
func (s *Server) inferenceSession(tenantID, origin string) (string, bool) {
	if origin != "" && s.registry.Online(origin) {
		return origin, true
	}
	listas, mudas := s.candidatasPorReadiness(tenantID)
	for _, grupo := range [][]string{listas, mudas} {
		if len(grupo) > 0 {
			slices.Sort(grupo)
			return grupo[0], true
		}
	}
	return "", false
}

// candidatasPorReadiness reparte las sesiones vivas del tenant en los DOS grupos que
// pueden atender una inferencia, y deja fuera al tercero.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 POR QUÉ DOS LISTAS Y NO UN FILTRO POR READY
// ════════════════════════════════════════════════════════════════════════════
//
// `INFERENCE_READINESS_UNSPECIFIED` significa «este Edge no lo dice» —un Edge con el
// contrato anterior a v0.17.0, o uno recién arrancado que todavía no lo sabe—, NUNCA
// «no puede»; la distinción está escrita y razonada en anotaReadiness (readiness.go).
// Exigir READY para ser elegible, que es lo que pedía el análisis de origen de este
// plan, dejaría SIN INFERENCIA a toda la flota que no publica el campo, y sin un solo
// error: el gateway diría «este tenant no tiene a nadie vivo» de un cliente cuya única
// instalación está perfectamente sana. Por eso el filtro es en NEGATIVO —se descarta
// lo que dijo DOWN— y READY es una PREFERENCIA que ordena, no una puerta que cierra.
//
// Corre bajo `trackMu` —el mismo candado e índice que sessionsForTenant— y lee
// `edgeReadiness` DENTRO del mismo bloque, sin llamar a nadie con el candado tomado:
// la disciplina de anotaReadiness y unaSesionPorEdge.
func (s *Server) candidatasPorReadiness(tenantID string) (listas, mudas []string) {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	for k, set := range s.edgeSessions {
		if k.tenantID != tenantID {
			continue
		}
		destino := &mudas
		switch s.edgeReadiness[k] {
		case cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN:
			continue
		case cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY:
			destino = &listas
		}
		for sid := range set {
			*destino = append(*destino, sid)
		}
	}
	return listas, mudas
}

// inferToCloud envuelve la petición en un CloudToEdge dirigido a la sesión dada.
//
// ⚠️ El session_id del ENVELOPE y el del payload dicen cosas distintas y van así a
// propósito: el del envelope es POR DÓNDE sale (lo exige el multiplexado del
// ADR-0008), y el del payload es la conversación que originó la pregunta, que puede
// no existir. Rellenar el segundo con el primero convertiría un dato de trazabilidad
// en una coincidencia sin significado.
func inferToCloud(cmdID, sessionID string, req InferRequest) *cloudlinkv1.CloudToEdge {
	temp := float32(req.Temperature)
	frame := &cloudlinkv1.InferenceRequest{
		CommandId:   cmdID,
		SessionId:   req.OriginSessionID,
		Prompt:      req.Prompt,
		Format:      req.Format,
		Temperature: &temp,
		TimeoutMs:   inferTimeout(req.Timeout).Milliseconds(),
		Class:       req.Class,
		Warmup:      req.Warmup,
	}
	// PRESENCIA EXPLÍCITA, y solo cuando hay algo que decir: el campo es `optional`
	// porque «quiero 0» y «no dije nada» serían el mismo byte en el cable. Aquí el
	// Cloud NUNCA quiere 0 —una salida de cero tokens no es una respuesta—, así que
	// un valor no positivo significa «no lo fijo» y el Edge aplica su default.
	// Escribir un puntero a 0 le pediría al Edge un num_predict de cero.
	if req.MaxOutputTokens > 0 {
		tope := req.MaxOutputTokens
		frame.MaxOutputTokens = &tope
	}
	return &cloudlinkv1.CloudToEdge{
		CommandId: cmdID,
		SessionId: sessionID,
		Payload:   &cloudlinkv1.CloudToEdge_InferenceRequest{InferenceRequest: frame},
	}
}

// inferTimeout resuelve el presupuesto efectivo de la inferencia.
func inferTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return defaultInferTimeout
	}
	return d
}

// awaitInference espera el InferenceResult con RELOJ PROPIO, separado del ctx del
// llamante (ver el ⚠️ de Infer). Tres salidas:
//
//   - el resultado llega ⇒ se abre y se devuelve (o su error nombrado);
//   - el canal se CIERRA ⇒ el stream murió con la inferencia en vuelo, y no hay
//     nadie que pueda contestar: edge_offline en el acto, sin consumir el plazo
//     entero (misma lección que ErrStreamClosed en awaitAck);
//   - vence NUESTRO presupuesto ⇒ timeout. Que el Edge no haya mandado siquiera su
//     propio INFERENCE_ERROR_TIMEOUT (que llegaría antes, por el margen) no cambia
//     el motivo: la inferencia no respondió dentro del plazo, que es lo que
//     `timeout` significa para el dueño.
//   - el LLAMANTE se rinde ⇒ ErrInferenceAbandonada, SIN motivo: su reloj no dice
//     nada sobre la salud de la vía.
func (s *Server) awaitInference(ctx context.Context, ch <-chan *cloudlinkv1.InferenceResult,
	cmdID, sessionID string, timeout time.Duration,
) (string, error) {
	timer := time.NewTimer(inferTimeout(timeout) + s.inferGrace)
	defer timer.Stop()

	select {
	case res, ok := <-ch:
		if !ok {
			s.log.Warn("inferencia: cancelada porque el stream de la sesión cayó",
				"command_id", cmdID, "session_id", sessionID)
			return "", inferErr(cmdID, sessionID, MotivoEdgeOffline, ErrStreamClosed)
		}
		return s.readInference(res, cmdID, sessionID)
	case <-ctx.Done():
		return "", fmt.Errorf("%w: inferencia %s: %w", ErrInferenceAbandonada, cmdID, ctx.Err())
	case <-timer.C:
		s.log.Warn("inferencia: se agotó el presupuesto del Cloud sin respuesta del Edge",
			"command_id", cmdID, "session_id", sessionID,
			"budget", (inferTimeout(timeout) + s.inferGrace).String())
		return "", inferErr(cmdID, sessionID, MotivoTimeout, context.DeadlineExceeded)
	}
}

// readInference desdobla las dos ramas del oneof del resultado: el error nombrado
// (en claro, fuera del sobre) o la salida sellada.
//
// El error va en claro Y ESO ES DELIBERADO en el contrato: no lleva PII —es un
// vocabulario cerrado— y el Cloud necesita poder decidir su degradación aunque el
// sellado sea justamente lo que falló.
func (s *Server) readInference(res *cloudlinkv1.InferenceResult, cmdID, sessionID string) (string, error) {
	if res == nil {
		return "", ErrInferenceSinSalida
	}
	if e := res.GetError(); e != cloudlinkv1.InferenceError_INFERENCE_ERROR_UNSPECIFIED {
		return "", inferErr(cmdID, sessionID, motivoDeFrame(e), errors.New(e.String()))
	}
	enc := res.GetEncOutput()
	if len(enc) == 0 {
		return "", ErrInferenceSinSalida
	}
	return s.openInference(enc, cmdID, sessionID)
}

// openInference abre el sobre X25519 de la salida y saca el raw_json.
//
// El molde es el de decodeIncoming (connect.go) y la diferencia es el desenlace: allí
// un sellado corrupto DESCARTA el mensaje sin tumbar el stream, porque el mensaje era
// de un cliente y el stream sirve a muchos más; aquí se devuelve error al llamante,
// que está esperándolo, y el stream ni se entera.
func (s *Server) openInference(enc []byte, cmdID, sessionID string) (string, error) {
	if len(s.cloudEncPriv) == 0 {
		s.log.Error("inferencia: salida sellada pero la nube no tiene clave de cifrado",
			"command_id", cmdID, "session_id", sessionID)
		return "", ErrInferenceSinClaveDeCifrado
	}
	raw, err := envelope.OpenWith(s.cloudEncPriv, enc)
	if err != nil {
		s.log.Error("inferencia: no se pudo abrir la salida sellada",
			"command_id", cmdID, "session_id", sessionID, "error", err)
		return "", fmt.Errorf("%w: %w", ErrInferenceSelladoIlegible, err)
	}
	var out cloudlinkv1.InferenceOutput
	if err := proto.Unmarshal(raw, &out); err != nil {
		s.log.Error("inferencia: la salida sellada abrió pero no deserializa",
			"command_id", cmdID, "session_id", sessionID, "error", err)
		return "", fmt.Errorf("%w: %w", ErrInferenceSelladoIlegible, err)
	}
	// Observabilidad del sellado en tránsito, con el mismo criterio que el ingreso:
	// se registra el TAMAÑO, nunca el contenido. La salida del modelo puede llevar
	// texto literal del cliente reflejado.
	s.log.Info("inferencia: salida sellada abierta",
		"command_id", cmdID, "session_id", sessionID,
		"enc_output_bytes", len(enc), "raw_json_len", len(out.GetRawJson()))
	return out.GetRawJson(), nil
}

// motivoDeFrame traduce el enum del proto al vocabulario de motivos. Es la ÚNICA
// traducción del fichero, y es 1:1 salvo UNSPECIFIED —que no viaja nunca por el
// cable (cuando no hay error, la rama del oneof es enc_output) y por eso readInference
// lo usa como centinela de «no hay error»—.
//
// Un valor DESCONOCIDO —un Edge más nuevo que esta nube— cae a ollama_down y no a un
// motivo inventado: el dueño acaba mirando su proveedor local, que es el sitio
// correcto para todos los errores que este frame sabe nombrar.
func motivoDeFrame(e cloudlinkv1.InferenceError) string {
	switch e {
	case cloudlinkv1.InferenceError_INFERENCE_ERROR_BREAKER_OPEN:
		return MotivoBreakerOpen
	case cloudlinkv1.InferenceError_INFERENCE_ERROR_TIMEOUT:
		return MotivoTimeout
	case cloudlinkv1.InferenceError_INFERENCE_ERROR_LEASE_INVALID:
		return MotivoLeaseInvalid
	case cloudlinkv1.InferenceError_INFERENCE_ERROR_EDGE_SIN_CAPACIDAD:
		return MotivoEdgeSinCapacidad
	case cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN,
		cloudlinkv1.InferenceError_INFERENCE_ERROR_UNSPECIFIED:
		return MotivoOllamaDown
	default:
		return MotivoOllamaDown
	}
}

// deliverInference entrega un InferenceResult a la inferencia pendiente
// correlacionada por command_id, de forma no bloqueante, y limpia la entrada.
//
// 🔴 MISMA INVARIANTE DE CIERRE QUE deliverAck, y por la misma razón: RETIRA la
// entrada del mapa bajo infersMu y solo DESPUÉS escribe en el canal, de modo que un
// cierre de stream concurrente que logre retirar la misma entrada sabe con certeza
// que aquí no va a escribir nadie y puede cerrar el canal sin riesgo. El enunciado
// completo está en cancelSessionAcks (send.go) y no se repite.
//
// ⚠️ NO ABRE EL SOBRE. Corre en el bucle Recv y ahí solo cabe lo que es O(1) en
// memoria (ADR-0040 §Decisión.3): un lookup y un envío no bloqueante, exactamente lo
// mismo que hace el Ack. El descifrado X25519 y el Unmarshal los paga el LLAMANTE en
// su propia goroutine (openInference), que es quien está esperando y a quien le
// corresponde el coste.
func (s *Server) deliverInference(res *cloudlinkv1.InferenceResult) {
	id := res.GetCommandId()

	s.infersMu.Lock()
	p, ok := s.infers[id]
	if ok {
		delete(s.infers, id)
	}
	s.infersMu.Unlock()

	if !ok {
		// Huérfano: llegó tarde, duplicado, o su llamante ya se rindió. Se ignora con
		// log y NO tumba el stream — mismo criterio que el bundle de diagnóstico.
		s.log.Debug("inferencia sin petición pendiente", "command_id", id)
		return
	}

	select {
	case p.ch <- res:
	default:
	}
}

// clearInfer elimina la entrada pendiente si aún existe. NO cierra el canal, por el
// mismo motivo que clearAck: quien sale por aquí es el propio llamante de Infer, que
// ya dejó de leerlo, y una segunda mano capaz de cerrar el canal es justo lo que la
// invariante evita.
func (s *Server) clearInfer(cmdID string) {
	s.infersMu.Lock()
	delete(s.infers, cmdID)
	s.infersMu.Unlock()
}

// cancelSessionInfers cancela de golpe las inferencias en vuelo de una sesión: retira
// sus entradas y cierra sus canales, con lo que cada awaitInference despierta al
// instante con edge_offline en vez de agotar su presupuesto —que aquí es de DECENAS
// DE SEGUNDOS, no de ocho— esperando a un Edge que ya no está. Devuelve cuántas
// canceló.
//
// Es el gemelo exacto de cancelSessionAcks, invariante incluida, y existe por la
// misma lección medida del Plan 050 · Ola 2: el gateway sabía que el stream había
// caído y aun así hacía esperar el plazo entero al llamante.
func (s *Server) cancelSessionInfers(sessionID string) int {
	var cancelados []chan *cloudlinkv1.InferenceResult

	s.infersMu.Lock()
	for cmdID, p := range s.infers {
		if p.sessionID != sessionID {
			continue
		}
		delete(s.infers, cmdID)
		cancelados = append(cancelados, p.ch)
	}
	s.infersMu.Unlock()

	for _, ch := range cancelados {
		close(ch)
	}

	if len(cancelados) > 0 {
		s.log.Warn("gateway: el stream cayó con inferencias en vuelo",
			"session_id", sessionID, "cancelados", len(cancelados))
	}
	return len(cancelados)
}
