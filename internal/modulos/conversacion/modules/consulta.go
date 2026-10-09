// Porta internal/flujos/modules/consulta.go @ c0c0c03

// consulta.go — EL CONTRATO DEL RE-ENTRY: un módulo PURO que necesita preguntar
// (Plan 044 · Ola 3.5 · T3.5-2).
//
// Los símbolos que en el viejo tenían nombre en español llevan aquí nombre en inglés
// (E-11), y cada uno dice en su comentario cuál era. Los VALORES que viajan fuera
// (la clave de Vars, las etiquetas de telemetría) no cambian ni un byte.
//
// ════════════════════════════════════════════════════════════════════════════
// QUÉ PROBLEMA RESUELVE, Y POR QUÉ NO SE RESOLVIÓ CAMBIANDO LA FIRMA
// ════════════════════════════════════════════════════════════════════════════
//
// Module.Step es PURO: sin ctx y sin puerto de I/O (registry.go). Eso es una
// decisión, no un descuido —el módulo declara efectos y nunca los ejecuta—, y
// además cambiarla costaría 4 implementadores, 8 mocks y 78 llamadas .Step( en
// 19 ficheros de test. Descartado por el dueño del producto.
//
// El mecanismo es OTRO: el módulo no consulta, PIDE. Devuelve su petición en un
// campo ADITIVO de Result (Result.Query) y se acaba su turno. Quien tiene ctx
// —engine.Step, en la MISMA función— la resuelve, siembra el VEREDICTO en Vars y
// vuelve a llamar a Step. El módulo sigue sin hacer I/O: lee un dato que ya está
// en su entrada, exactamente como lee VarContentRaw (registry.go).
//
// Los dos precedentes literales de este repo, que este fichero copia a propósito:
//
//   - CAMPO ADITIVO EN Result: «por eso el campo es ADITIVO y menu/survey/media no
//     cambian ni una línea» (docstring de Result.Outcome, registry.go). Un Result
//     que no rellene Query se comporta EXACTAMENTE como antes de esta tarea: nil es
//     «no pregunto nada».
//   - SEMBRAR EN Vars ANTES DE Step: VarContentRaw (registry.go), que el engine ya
//     siembra antes de cada Step con degradación.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LA REGLA DURA: LO QUE SE SIEMBRA EN Vars NO PUEDE LLEVAR TEXTO DEL CLIENTE
// ════════════════════════════════════════════════════════════════════════════
//
// Vars se serializa ENTERO al JSONB de public.flow_state (el runtime, al guardar).
// Lo que entre ahí queda EN CLARO y PARA SIEMPRE: es exactamente la fuga que
// cerró StripIntentSignal (registry.go, REQ-18), donde el texto extraído del
// mensaje del cliente se quedaba en el estado porque nadie lo consumía.
//
// De ahí el reparto ASIMÉTRICO de este contrato, que es la mitad del diseño:
//
//	Query   →  viaja en Result, hacia el resolutor. LLEVA TEXTO (es lo que hay
//	           que interpretar). NO se persiste: el engine DESCARTA el Result
//	           entero de la primera pasada, Query incluida.
//	Verdict →  viaja en Vars, hacia el módulo. NO LLEVA TEXTO: solo un CÓDIGO
//	           del catálogo cerrado que el propio módulo ofreció, y un motivo de
//	           un enum cerrado. Ni la frase del cliente, ni una «evidencia»
//	           legible, ni una explicación del modelo.
//
// Y encima de la regla hay una GARANTÍA MECÁNICA, porque una regla que depende de
// que todo el mundo se acuerde es el modo de fallo del REQ-18: el engine BORRA la
// clave del veredicto de las Vars que devuelve la re-entrada (StripQueryVerdict),
// consuma o no el módulo. Las dos cosas, no una: el tipo acotado impide que haya
// algo que filtrar, y el borrado impide que sobreviva al turno aunque un módulo lo
// copie a su estado sin pensarlo.

package modules

// VarQueryVerdict (`VarConsultaVeredicto` en el viejo) es la clave de
// Conversation.Vars bajo la que el ENGINE siembra el veredicto de una Query antes
// de la SEGUNDA llamada a Step. Su presencia es, además, la señal de «ya
// preguntaste»: un módulo que la vea NO debe volver a pedir consulta en ese turno
// (y si lo hace, el engine corta).
//
// No entra en StripIntentSignal —que barre las claves de la señal de intención en
// los ocho Save del runtime— y no le hace falta: esta clave la pone y la quita el
// engine dentro del MISMO Step, así que nunca llega viva a un Save. Si algún día
// alguien la sembrara desde fuera del engine, ese razonamiento dejaría de valer y
// habría que barrerla como a las otras dos.
const VarQueryVerdict = "consulta_veredicto"

// QueryClass (`ClaseConsulta` en el viejo) dice QUÉ tipo de pregunta se eleva. Es de
// cardinalidad ACOTADA a propósito: viaja a la telemetría del engine como etiqueta,
// igual que el escalón de la cascada del carrito.
type QueryClass string

const (
	// QueryClassOption (`ClaseOpcion`) es «¿cuál de estas opciones quiso decir?». La
	// respuesta admisible es UNO de los códigos de Query.Options y nada más.
	QueryClassOption QueryClass = "opcion"
	// QueryClassQuantity (`ClaseCantidad`) es «¿cuántas pidió?» («mejor dos» → 2). La
	// respuesta admisible es un número en dígitos; no hay catálogo que ofrecer.
	QueryClassQuantity QueryClass = "cantidad"
)

// QueryOption (`OpcionConsulta` en el viejo) es UNA respuesta admisible: el CÓDIGO
// (Code, antes `Codigo`) que la sub-máquina del módulo ya entiende y la ETIQUETA
// (Label, antes `Etiqueta`) que el cliente tiene delante en la pantalla. El
// resolutor compara contra la etiqueta y devuelve el código — el mismo truco que
// hace que la cascada determinista del carrito no obligue a tocar ni un step*.
//
// Es también el CATÁLOGO CERRADO contra el que el módulo valida lo que le
// devuelvan: un resolutor que invente un código (o que devuelva la frase del
// cliente) no cuela, porque su respuesta no está en esta lista.
type QueryOption struct {
	Code  string
	Label string
}

// Query (`Consulta` en el viejo) es la PETICIÓN de interpretación que un módulo
// eleva al engine cuando su camino determinista no resuelve y el nivel admite ayuda.
// Sus campos eran `Clase`, `Nivel`, `Texto`, `Opciones` y `Trozos`.
//
// 🔴 VIAJA EN Result, NUNCA EN Vars. Text es lo único de este contrato que lleva
// palabras del cliente, y por eso está en el lado que NO se persiste: el engine
// descarta el Result de la primera pasada entero (ver el reparto asimétrico en la
// cabecera de este fichero).
//
// Level es el contexto del módulo en que se pregunta (para el carrito, el nivel de
// su sub-máquina). Igual que Class, va a la telemetría: de cardinalidad ACOTADA,
// jamás derivado de lo que el cliente escribió.
type Query struct {
	Class   QueryClass
	Level   string
	Text    string
	Options []QueryOption
	// Chunks son los TROZOS del turno que el módulo ya descompuso y que su camino
	// determinista no supo resolver, en el orden en que el cliente los escribió
	// (Plan 044 · Ola 3.5 · T3.5-3). Vacío —el caso normal— significa «esta consulta
	// es sobre UN solo texto, el de Text», y entonces todo se comporta exactamente
	// como antes de esta tarea: el campo es ADITIVO, igual que lo fue Result.Query.
	//
	// 🔴 LLEVA TEXTO DEL CLIENTE, y por eso está AQUÍ y no en el Verdict. Un trozo
	// es literalmente una rebanada de Text: mismo dato, misma procedencia y por
	// tanto el MISMO lado del reparto asimétrico de este fichero — el lado que viaja
	// en Result y que el engine descarta entero tras la primera pasada. Lo que vuelve
	// (Verdict.Codes) son códigos del catálogo cerrado y nada más.
	//
	// El resolutor hace UNA llamada CHICA POR TROZO y jamás una llamada monstruo con
	// los N dentro: es la forma medida de la Ola 2 («Go descompone → LLM decide
	// chiquito → Go recompone»), y la razón de que sea así está medida en campo —un
	// turno con cuatro productos mandado de una sentada devolvió UNO y perdió tres
	// (journal 2026-08-17 §13.3)—. Quien acota cuántas de esas llamadas caben en un
	// turno es el resolutor, no este contrato (turnoacotado.MaxLlamadasPorTurno).
	Chunks []string
}

// QueryReason (`MotivoConsulta` en el viejo) explica por qué un Verdict NO trae
// código. Enum CERRADO: es dato de dominio, no una explicación en prosa del modelo.
type QueryReason string

const (
	// QueryReasonNoResolver (`MotivoSinResolutor`) dice que no hay resolutor
	// inyectado. El mecanismo está construido y nadie lo ha enchufado todavía.
	QueryReasonNoResolver QueryReason = "sin_resolutor"
	// QueryReasonFailure (`MotivoFallo`) dice que el resolutor devolvió error
	// (timeout, red, modelo caído).
	QueryReasonFailure QueryReason = "fallo"
	// QueryReasonInconclusive (`MotivoNoConcluyente`) dice que el resolutor respondió
	// y NO supo decidir. Es una respuesta legítima y la más importante de las tres:
	// adivinar mete el artículo equivocado en el pedido de alguien.
	QueryReasonInconclusive QueryReason = "no_concluyente"
)

// Verdict (`Veredicto` en el viejo) es la respuesta a una Query, y es lo ÚNICO de
// este contrato que se siembra en Vars. Sus campos eran `Codigo`, `Motivo` y
// `Codigos`.
//
// 🔴 SU FORMA ES LA GARANTÍA DE PRIVACIDAD, no un detalle de serialización: un
// código del catálogo cerrado que el propio módulo ofreció (o unos dígitos, para
// QueryClassQuantity) y un motivo de un enum cerrado. No hay ningún campo donde
// quepa una frase, y eso es deliberado — si mañana alguien quiere devolver la
// «evidencia» que convenció al modelo, tendrá que añadir un campo y tropezarse
// con este párrafo antes.
//
// Code vacío ⇒ no resuelto, y Reason dice por qué. El CERO del struct es
// «no resuelto, sin motivo declarado», que es exactamente lo que quiere decir.
type Verdict struct {
	Code   string
	Reason QueryReason
	// Codes es la respuesta a una consulta CON TROZOS: un código por trozo,
	// ALINEADO POR POSICIÓN con Query.Chunks (Plan 044 · Ola 3.5 · T3.5-3). La
	// cadena vacía en la posición i es «este trozo no se resolvió», y es un desenlace
	// normal: el trozo se quedó sin llamada por el tope, se agotó el presupuesto del
	// turno, o el modelo no supo.
	//
	// 🔴 ALINEADO POR POSICIÓN Y NO UN MAPA texto→código, que es lo que la privacidad
	// de este contrato prohíbe: un mapa tendría el trozo del cliente COMO CLAVE, o
	// sea el texto de la persona metido justo en el lado que SÍ se siembra en Vars y
	// que acaba en claro en el JSONB de flow_state. Con posiciones no hay dónde meter
	// una palabra suya ni por descuido.
	//
	// Vacío ⇒ consulta de un solo texto; se lee Code y nada cambia.
	Codes []string
}

// Resolved (`Resuelto` en el viejo) informa si el veredicto trae una respuesta de UN
// SOLO código que aplicar: Code no vacío. No mira Codes a propósito: quien pregunta
// por trozos pregunta con ResolvedAny.
func (v Verdict) Resolved() bool { return v.Code != "" }

// ResolvedAny (`ResueltoAlguno` en el viejo) informa si el veredicto trae ALGO
// aplicable, sea el código único o al menos uno no vacío de los códigos por trozo.
//
// Existe porque un troceado PARCIAL —dos productos resueltos de tres— no es «no
// resuelto»: es exactamente el desenlace que el tope y el presupuesto del turno
// producen a propósito, y tratarlo como un cero tiraría trabajo que ya se pagó con
// la plaza única del Edge.
func (v Verdict) ResolvedAny() bool {
	if v.Resolved() {
		return true
	}
	for _, c := range v.Codes {
		if c != "" {
			return true
		}
	}
	return false
}

// VerdictFrom (`VeredictoDe` en el viejo) lee el veredicto sembrado por el engine.
// El segundo valor distingue «no hay veredicto» (primera pasada) de «hay veredicto y
// no resolvió» (segunda pasada, degradada): son dos situaciones OPUESTAS para el
// módulo —en la primera puede preguntar, en la segunda no debe— y confundirlas es
// el bucle. Un valor bajo la clave que no sea un Verdict cuenta como «no hay».
func VerdictFrom(vars map[string]any) (Verdict, bool) {
	v, ok := vars[VarQueryVerdict].(Verdict)
	return v, ok
}

// WithVerdict (`ConVeredicto` en el viejo) devuelve una COPIA de vars con el
// veredicto sembrado bajo VarQueryVerdict. Copia y no mutación: el mapa original es
// el de la conversación viva que el llamante conserva, y la primera pasada tiene que
// poder descartarse sin dejar rastro. Con vars nil devuelve un mapa nuevo.
func WithVerdict(vars map[string]any, v Verdict) map[string]any {
	out := CloneVars(vars)
	out[VarQueryVerdict] = v
	return out
}

// StripQueryVerdict (`StripConsultaVeredicto` en el viejo) devuelve unas Vars SIN la
// clave del veredicto, sin mutar el mapa recibido. Lo llama el ENGINE tras la
// re-entrada, consuma o no el módulo.
//
// Hermana de StripIntentSignal y por el mismo motivo (REQ-18): el contrato dice
// que el módulo consume y limpia, pero un contrato solo se ejecuta cuando alguien
// lo cumple, y lo que no se limpia acaba en el JSONB de flow_state. Aquí el
// veredicto es dato acotado —no hay PII que filtrar— pero dejarlo vivo tendría un
// efecto peor que estético: en el turno SIGUIENTE el módulo lo leería como «ya
// preguntaste» y no volvería a pedir consulta nunca más, en silencio.
//
// Devuelve el MISMO mapa cuando no hay nada que barrer (el caso común: todo turno
// que no pasó por una consulta), así que no cuesta una copia por mensaje.
func StripQueryVerdict(vars map[string]any) map[string]any {
	if _, present := vars[VarQueryVerdict]; !present {
		return vars
	}
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		if k == VarQueryVerdict {
			continue
		}
		out[k] = v
	}
	return out
}
