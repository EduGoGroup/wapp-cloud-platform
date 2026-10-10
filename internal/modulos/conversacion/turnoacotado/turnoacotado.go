// Porta internal/turnoacotado/turnoacotado.go @ 42117b5

// Package turnoacotado implementa el TURNO ACOTADO del Nivel B: la pregunta suelta
// que el motor de flujos le hace al modelo del tenant cuando un módulo puro no pudo
// interpretar lo que el cliente escribió (Plan 044 · Ola 3.5 · T3.5-2, ADR-0044 §5).
//
// Los símbolos que en el viejo tenían nombre en español llevan aquí nombre en inglés
// (E-11), y cada uno dice en su comentario cuál era. Los VALORES observables —el
// texto de los dos errores, los motivos del veredicto y, sobre todo, el texto del
// prompt— no cambian ni un byte.
//
// # QUÉ ES, EN UNA FRASE
//
// Es el adaptador que cierra el re-entry: el carrito PIDE (modules.Query), el
// engine pregunta por su puerto (el resolutor de consultas) y esto es lo que hay al
// otro lado del puerto — arma el prompt, lo manda por la vía del tenant y traduce
// lo que vuelva a un Verdict que el módulo pueda aplicar.
//
// # LOS TRES SITIOS DONDE ESTE PAQUETE DICE QUE NO, Y POR QUÉ IMPORTA
//
// Un resolutor de este tipo tiene una tentación evidente: creerse al modelo. Aquí
// no se le cree en tres puntos distintos, y los tres son código Go, no prompt:
//
//  1. **El rango.** Ante un menú de 4 opciones, el modelo medido contesta
//     `usable:true, value:5` con toda tranquilidad. NO se arregla con el prompt (se
//     intentó): se valida en Go contra las Options que la propia Query trae. Un
//     value fuera de rango es un veredicto NO resuelto, punto.
//  2. **La forma.** Si lo que vuelve no es el JSON del esquema, el turno se degrada;
//     no se «interpreta con cariño» ni se busca un número dentro de la prosa.
//  3. **Y el módulo vuelve a decir que no.** Aunque las dos anteriores pasen, el
//     carrito revalida el código contra su propio catálogo (cart/consulta.go,
//     codigoAdmisible). Es defensa en profundidad a propósito: este paquete puede
//     equivocarse y el pedido de una persona no puede depender de que no lo haga.
//
// # 🔴 LO QUE NO SALE DE AQUÍ
//
// El Verdict no tiene un campo donde quepa una frase, y eso es del contrato
// (modules/consulta.go). Este paquete no lo estira: devuelve un código del catálogo
// o unos dígitos. Ni la respuesta del modelo, ni su `reason`, ni una «evidencia»
// legible cruzan hacia Vars, que es lo que se persiste en claro en flow_state.
package turnoacotado

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"

	"github.com/EduGoGroup/wapp-shared/llm"
)

// maxQuantity acota lo que se acepta como respuesta a una pregunta de cantidad.
//
// No es la regla de negocio —esa es de stepQuantity, que exige >= 1— sino una guarda
// contra un modelo que devuelva un número absurdo: cuatro dígitos es más de lo que
// nadie pide por WhatsApp, y coincide con el maxDigitosCantidad que el carrito ya
// aplica al otro lado. Los dos topes existen a propósito (ver los tres «no» de la
// cabecera): este evita gastar un turno en algo que el módulo va a rechazar igual.
const maxQuantity = 9999

// ErrUnknownClass (`ErrClaseDesconocida` en el viejo) indica que llegó una Query de
// una clase que este resolutor no sabe preguntar. Es un fallo de PROGRAMACIÓN —el
// vocabulario de modules.QueryClass es cerrado— y por eso se devuelve error en vez de
// degradar en silencio: una clase nueva que nadie enseñó a preguntar tiene que doler
// en el primer turno, no convertirse en un carrito que dejó de entender sin que nada
// lo dijera. El texto es observable y no cambia:
// "turnoacotado: clase de consulta fuera del vocabulario cerrado".
var ErrUnknownClass = errors.New("turnoacotado: clase de consulta fuera del vocabulario cerrado")

// Turner (`Turnero` en el viejo) es lo que este paquete necesita del selector de
// vía: mandar un prompt y un esquema, y recibir el texto crudo del modelo. Lo
// satisface *llmvia.Selector DIRECTAMENTE, sin adaptador: el método se llama Turno y
// recibe un llmvia.TurnoRequest porque esos son los nombres del selector (ya
// escritos, no se renombran).
//
// La interfaz la declara el CONSUMIDOR, como manda la casa, y es de UN método: no
// se pide aquí el llm.LLMProvider entero porque de sus cinco métodos no sirve
// ninguno —son las cinco etapas del pipeline— y porque un puerto ancho invita a que
// el día de mañana este paquete llame a una etapa «que se parece».
type Turner interface {
	Turno(ctx context.Context, tenantID, originSessionID string, t llmvia.TurnoRequest) (string, error)
}

// Resolver implementa el puerto del resolutor de consultas del engine contra el
// modelo del tenant.
//
// No implementa la interfaz por nombre —no importa el paquete engine— sino por
// FORMA, que es lo que el docstring de aquel puerto promete. Es inmutable tras
// construirse y seguro para uso concurrente: no guarda estado de llamada.
type Resolver struct {
	turner Turner
}

// ErrNoTurner (`ErrSinTurnero` en el viejo) indica que el Resolver se construyó sin
// con quién preguntar. Es un fallo de ARRANQUE y por eso New lo devuelve al
// construir, no en el primer turno: mismo criterio que local.New con su Frame. El
// texto es observable y no cambia:
// "turnoacotado: el resolutor necesita un turnero (el selector de vía)".
var ErrNoTurner = errors.New("turnoacotado: el resolutor necesita un turnero (el selector de vía)")

// New construye el resolutor sobre el selector de vía. Con t nil devuelve
// (nil, ErrNoTurner); con cualquier otro, (el resolutor, nil).
func New(t Turner) (*Resolver, error) {
	if t == nil {
		return nil, ErrNoTurner
	}
	return &Resolver{turner: t}, nil
}

// ResolveQuery (`ResolverConsulta` en el viejo) interpreta lo que el cliente
// escribió y devuelve un Verdict.
//
// 🔴 NUNCA DEVUELVE UN VEREDICTO A MEDIAS: o trae un código aplicable, o trae un
// motivo de por qué no. El engine degrada con cualquiera de los dos y el módulo
// vuelve a entrar igual (engine/consulta.go); lo que no puede es quedarse sin
// respuesta.
//
// El error se reserva para lo que de verdad lo es —la vía falló, el modelo no
// contestó, la clase no existe— porque un error aquí es lo que dispara el aviso al
// dueño y la métrica de caída a Nivel A. Un modelo que contesta a tiempo y elige
// mal NO es un error: es un veredicto no concluyente, y confundirlos mandaría al
// dueño a revisar un equipo que está perfectamente.
//
// # Antes de preguntar (en este orden, y sin gastar una inferencia)
//
//   - clase que no es QueryClassOption ni QueryClassQuantity ⇒ (Verdict{},
//     ErrUnknownClass), traiga o no Chunks;
//   - QueryClassOption sin Options ⇒ (Verdict{Reason: QueryReasonInconclusive}, nil),
//     traiga o no Chunks: sin catálogo no hay nada que elegir.
//
// # Con Chunks (len > 0): el troceado
//
// Una llamada por trozo, con tope y presupuesto; ver troceado.go. Vale para las dos
// clases: cada trozo se pregunta SIEMPRE como una elección sobre q.Options.
//
// # Sin Chunks: UNA llamada a Turno
//
// Con el tenantID y el sessionID recibidos, el ctx recibido, y un TurnoRequest cuyo
// Prompt y Formato son los de la clase (prompt.go). Después:
//
//   - Turno falla con un error que casa (errors.Is) con llmvia.ErrViaSinTurnoAcotado
//     ⇒ (Verdict{Reason: QueryReasonNoResolver}, nil): no es una avería;
//   - Turno falla con cualquier otro error ⇒ (Verdict{}, ese error INTACTO, sin
//     envolver);
//   - Turno contesta ⇒ (el veredicto validado en Go, nil). Es no concluyente
//     (Reason = QueryReasonInconclusive, Code vacío) si la salida no es el JSON del
//     esquema (se aísla con llm.ExtractJSON, que sabe de vallas de Markdown), si
//     `usable` no es true, si `value` falta o es null, o si `value` no es un entero
//     del rango: 1..len(Options) para una elección, 1..9999 para una cantidad. Si
//     pasa, Code es el Code de la opción en esa POSICIÓN (elección) o el número en
//     dígitos decimales (cantidad), con Reason vacío. El `reason` del modelo no se
//     mira nunca, y ningún texto del modelo ni del cliente sale en el Verdict.
func (r *Resolver) ResolveQuery(ctx context.Context, tenantID, sessionID string, q modules.Query) (modules.Verdict, error) {
	switch q.Class {
	case modules.QueryClassQuantity:
	case modules.QueryClassOption:
		if len(q.Options) == 0 {
			// Sin catálogo que ofrecer no hay nada que elegir, y preguntarlo gastaría una
			// plaza del Ollama del cliente para no poder usar la respuesta. El carrito ya
			// no pregunta en este caso (cart/consulta.go); esto es la red de abajo.
			return modules.Verdict{Reason: modules.QueryReasonInconclusive}, nil
		}
	default:
		return modules.Verdict{}, ErrUnknownClass
	}

	if len(q.Chunks) > 0 {
		// La consulta viene YA TROCEADA por el módulo (T3.5-3): una llamada chica por
		// trozo, con tope y presupuesto propios. Es la MISMA pregunta de elección de
		// abajo repetida N veces, no otro camino — ver troceado.go.
		return r.resolveChunks(ctx, tenantID, sessionID, q)
	}

	text, schema := prompt(q)
	raw, err := r.turner.Turno(ctx, tenantID, sessionID, llmvia.TurnoRequest{Prompt: text, Formato: schema})
	if err != nil {
		if errors.Is(err, llmvia.ErrViaSinTurnoAcotado) {
			// El tenant está en vía API: para él NO EXISTE este escalón, y eso no es una
			// avería de nadie. Se devuelve el motivo que lo dice —el mismo que usa el
			// engine cuando no hay resolutor cableado— en vez de un error, para que no
			// escriba un aviso de degradación al dueño ni cuente como caída a Nivel A.
			// ⚠️ El desenlace que el engine observará es `no_concluyente` y no
			// `sin_resolutor`, porque el engine solo distingue el segundo por su propio
			// campo nil; el MOTIVO que el módulo recibe sí es el exacto.
			return modules.Verdict{Reason: modules.QueryReasonNoResolver}, nil
		}
		return modules.Verdict{}, err
	}
	return verdictOf(q, raw), nil
}

// modelReply (`salida` en el viejo) es la respuesta del modelo tal como la fuerza el
// JSON Schema.
//
// Value ES UN PUNTERO Y NO UN int porque un int de Go no distingue la clave AUSENTE
// del valor 0, y aquí las dos cosas se originan en sucesos distintos: ausente (o
// null) es «el modelo dijo que no supo», que es el caso normal; 0 es un value que el
// modelo se inventó.
//
// ⚠️ HONESTIDAD SOBRE LO QUE ESTO COMPRA HOY: NADA OBSERVABLE, y se comprobó por
// mutación (cambiar el puntero por un int y tratar el ausente como 0 deja la suite
// EN VERDE). El motivo es que la validación de rango de abajo rechaza el 0 por su
// cuenta en las dos clases, así que los dos caminos acaban en el mismo veredicto no
// concluyente. Se conserva el puntero porque es la decodificación CORRECTA —la
// distinción existe en el JSON y perderla al leerlo es perderla para siempre— y
// porque el día que alguien admita un 0 legítimo en alguna clase, con un int el
// defecto sería silencioso y con esto no.
type modelReply struct {
	Usable bool `json:"usable"`
	Value  *int `json:"value"`
	//nolint:unused // Se decodifica a propósito y NO se lee: ver el ⚠️ de verdictOf().
	Reason string `json:"reason"`
}

// verdictOf (`veredicto` en el viejo) traduce la salida CRUDA del modelo a un Verdict
// ya validado en Go.
//
// ⚠️ `Reason` SE DECODIFICA Y NO SE USA, y es deliberado. Medido: el motivo que el
// modelo elige sale mal a menudo —dice `no_entendido` donde cualquiera diría
// `fuera_de_rango`— MIENTRAS `usable` y `value` son correctos. Es telemetría del
// modelo, no la decisión: colgar lógica de él sería tomar decisiones de negocio con
// el campo peor calibrado de la respuesta. Se deja en el struct para que quien lea
// esto vea que existe y por qué no se mira.
func verdictOf(q modules.Query, raw string) modules.Verdict {
	inconclusive := modules.Verdict{Reason: modules.QueryReasonInconclusive}

	// Aislar el JSON con el MISMO ExtractJSON que usan las dos vías del pipeline: es
	// quien sabe de vallas de Markdown, de ecos del esquema y de prosa previa. Un
	// fallo aquí es llm.ErrLLMQuality —el modelo respondió y su salida no era
	// interpretable— y eso NO es una degradación de la vía: no se propaga como error,
	// se degrada. Es la misma primera rama que motivoDe aplica en llmvia/notify.go.
	clean, err := llm.ExtractJSON(raw)
	if err != nil {
		return inconclusive
	}
	var reply modelReply
	if err := json.Unmarshal(clean, &reply); err != nil {
		return inconclusive
	}
	if !reply.Usable || reply.Value == nil {
		return inconclusive
	}
	v := *reply.Value

	// ════════════════════════════════════════════════════════════════════════
	// 🔴 EL RANGO SE VALIDA AQUÍ, Y ESTO HACE IRRELEVANTE UN FALLO CONOCIDO
	// ════════════════════════════════════════════════════════════════════════
	//
	// Con un menú de 4 opciones, el modelo medido responde `usable:true, value:5`
	// ante «quiero 5». Se intentó cerrarlo desde el prompt y no se cierra: el LLM no
	// es la última palabra sobre el rango, el código sí. Un value fuera de las
	// Options que la propia Query trae es un veredicto NO resuelto, y el carrito
	// repromptea como el día antes de esta tarea.
	//
	// 🔬 Y NO ES «defensa por si acaso»: con estas dos líneas quitadas, la mutación no
	// devuelve el artículo equivocado — ENTRA EN PÁNICO (`index out of range [4] with
	// length 4`, y `[-1]` con el value negativo). O sea que sin esto, el fallo conocido
	// del modelo tumba la goroutine que atiende el mensaje de una persona.
	if q.Class == modules.QueryClassQuantity {
		if v < 1 || v > maxQuantity {
			return inconclusive
		}
		// La cantidad viaja en DÍGITOS porque eso es lo que la sub-máquina del carrito
		// entiende (stepQuantity hace su propio Atoi). El Verdict no tiene un campo
		// numérico a propósito: su único hueco es un código del catálogo.
		return modules.Verdict{Code: strconv.Itoa(v)}
	}
	if v < 1 || v > len(q.Options) {
		return inconclusive
	}
	// El modelo eligió una POSICIÓN de la lista que se le enseñó; el carrito entiende
	// CÓDIGOS. La traducción es esta línea y es la razón por la que al modelo nunca
	// se le enseñan los códigos: no tendría cómo saber que «Volver» es `volver`.
	return modules.Verdict{Code: q.Options[v-1].Code}
}
