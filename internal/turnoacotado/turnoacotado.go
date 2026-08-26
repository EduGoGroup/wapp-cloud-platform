// Package turnoacotado implementa el TURNO ACOTADO del Nivel B: la pregunta suelta
// que el motor de flujos le hace al modelo del tenant cuando un módulo puro no pudo
// interpretar lo que el cliente escribió (Plan 044 · Ola 3.5 · T3.5-2, ADR-0044 §5).
//
// # QUÉ ES, EN UNA FRASE
//
// Es el adaptador que cierra el re-entry: el carrito PIDE (modules.Consulta), el
// engine pregunta por su puerto (engine.ConsultaResolver) y esto es lo que hay al
// otro lado del puerto — arma el prompt, lo manda por la vía del tenant y traduce
// lo que vuelva a un Veredicto que el módulo pueda aplicar.
//
// # LOS TRES SITIOS DONDE ESTE PAQUETE DICE QUE NO, Y POR QUÉ IMPORTA
//
// Un resolutor de este tipo tiene una tentación evidente: creerse al modelo. Aquí
// no se le cree en tres puntos distintos, y los tres son código Go, no prompt:
//
//  1. **El rango.** Ante un menú de 4 opciones, el modelo medido contesta
//     `usable:true, value:5` con toda tranquilidad. NO se arregla con el prompt (se
//     intentó): se valida en Go contra las Opciones que la propia Consulta trae. Un
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
// El Veredicto no tiene un campo donde quepa una frase, y eso es del contrato
// (modules/consulta.go). Este paquete no lo estira: devuelve un código del catálogo
// o unos dígitos. Ni la respuesta del modelo, ni su `reason`, ni una «evidencia»
// legible cruzan hacia Vars, que es lo que se persiste en claro en flow_state.
package turnoacotado

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"

	"github.com/EduGoGroup/wapp-shared/llm"
)

// maxCantidad acota lo que se acepta como respuesta a una pregunta de cantidad.
//
// No es la regla de negocio —esa es de stepQuantity, que exige >= 1— sino una guarda
// contra un modelo que devuelva un número absurdo: cuatro dígitos es más de lo que
// nadie pide por WhatsApp, y coincide con el maxDigitosCantidad que el carrito ya
// aplica al otro lado. Los dos topes existen a propósito (ver los tres «no» de la
// cabecera): este evita gastar un turno en algo que el módulo va a rechazar igual.
const maxCantidad = 9999

// ErrClaseDesconocida indica que llegó una Consulta de una clase que este resolutor
// no sabe preguntar. Es un fallo de PROGRAMACIÓN —el vocabulario de ClaseConsulta es
// cerrado— y por eso se devuelve error en vez de degradar en silencio: una clase
// nueva que nadie enseñó a preguntar tiene que doler en el primer turno, no
// convertirse en un carrito que dejó de entender sin que nada lo dijera.
var ErrClaseDesconocida = errors.New("turnoacotado: clase de consulta fuera del vocabulario cerrado")

// Turnero es lo que este paquete necesita del selector de vía: mandar un prompt y
// un esquema, y recibir el texto crudo del modelo. Lo satisface *llmvia.Selector.
//
// La interfaz la declara el CONSUMIDOR, como manda la casa, y es de UN método: no
// se pide aquí el llm.LLMProvider entero porque de sus cinco métodos no sirve
// ninguno —son las cinco etapas del pipeline— y porque un puerto ancho invita a que
// el día de mañana este paquete llame a una etapa «que se parece».
type Turnero interface {
	Turno(ctx context.Context, tenantID, originSessionID string, t llmvia.TurnoRequest) (string, error)
}

// Resolver implementa engine.ConsultaResolver contra el modelo del tenant.
//
// No implementa la interfaz por nombre —no importa el paquete engine— sino por
// FORMA, que es lo que el docstring de ConsultaResolver promete. Es inmutable tras
// construirse y seguro para uso concurrente: no guarda estado de llamada.
type Resolver struct {
	turnero Turnero
}

// ErrSinTurnero indica que el Resolver se construyó sin con quién preguntar. Es un
// fallo de ARRANQUE y por eso New lo devuelve al construir, no en el primer turno:
// mismo criterio que local.New con su Frame.
var ErrSinTurnero = errors.New("turnoacotado: el resolutor necesita un turnero (el selector de vía)")

// New construye el resolutor sobre el selector de vía.
func New(t Turnero) (*Resolver, error) {
	if t == nil {
		return nil, ErrSinTurnero
	}
	return &Resolver{turnero: t}, nil
}

// ResolverConsulta interpreta lo que el cliente escribió y devuelve un Veredicto.
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
func (r *Resolver) ResolverConsulta(ctx context.Context, tenantID, sessionID string, c modules.Consulta) (modules.Veredicto, error) {
	switch c.Clase {
	case modules.ClaseCantidad:
	case modules.ClaseOpcion:
		if len(c.Opciones) == 0 {
			// Sin catálogo que ofrecer no hay nada que elegir, y preguntarlo gastaría una
			// plaza del Ollama del cliente para no poder usar la respuesta. El carrito ya
			// no pregunta en este caso (cart/consulta.go); esto es la red de abajo.
			return modules.Veredicto{Motivo: modules.MotivoNoConcluyente}, nil
		}
	default:
		return modules.Veredicto{}, ErrClaseDesconocida
	}

	texto, esquema := prompt(c)
	raw, err := r.turnero.Turno(ctx, tenantID, sessionID, llmvia.TurnoRequest{Prompt: texto, Formato: esquema})
	if err != nil {
		if errors.Is(err, llmvia.ErrViaSinTurnoAcotado) {
			// El tenant está en vía API: para él NO EXISTE este escalón, y eso no es una
			// avería de nadie. Se devuelve el motivo que lo dice —el mismo que usa el
			// engine cuando no hay resolutor cableado— en vez de un error, para que no
			// escriba un aviso de degradación al dueño ni cuente como caída a Nivel A.
			// ⚠️ El desenlace que el engine observará es `no_concluyente` y no
			// `sin_resolutor`, porque el engine solo distingue el segundo por su propio
			// campo nil; el MOTIVO que el módulo recibe sí es el exacto.
			return modules.Veredicto{Motivo: modules.MotivoSinResolutor}, nil
		}
		return modules.Veredicto{}, err
	}
	return veredicto(c, raw), nil
}

// salida es la respuesta del modelo tal como la fuerza el JSON Schema.
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
type salida struct {
	Usable bool `json:"usable"`
	Value  *int `json:"value"`
	//nolint:unused // Se decodifica a propósito y NO se lee: ver el ⚠️ de veredicto().
	Reason string `json:"reason"`
}

// veredicto traduce la salida CRUDA del modelo a un Veredicto ya validado en Go.
//
// ⚠️ `Reason` SE DECODIFICA Y NO SE USA, y es deliberado. Medido: el motivo que el
// modelo elige sale mal a menudo —dice `no_entendido` donde cualquiera diría
// `fuera_de_rango`— MIENTRAS `usable` y `value` son correctos. Es telemetría del
// modelo, no la decisión: colgar lógica de él sería tomar decisiones de negocio con
// el campo peor calibrado de la respuesta. Se deja en el struct para que quien lea
// esto vea que existe y por qué no se mira.
func veredicto(c modules.Consulta, raw string) modules.Veredicto {
	noConcluyente := modules.Veredicto{Motivo: modules.MotivoNoConcluyente}

	// Aislar el JSON con el MISMO ExtractJSON que usan las dos vías del pipeline: es
	// quien sabe de vallas de Markdown, de ecos del esquema y de prosa previa. Un
	// fallo aquí es llm.ErrLLMQuality —el modelo respondió y su salida no era
	// interpretable— y eso NO es una degradación de la vía: no se propaga como error,
	// se degrada. Es la misma primera rama que motivoDe aplica en llmvia/notify.go.
	limpio, err := llm.ExtractJSON(raw)
	if err != nil {
		return noConcluyente
	}
	var s salida
	if err := json.Unmarshal(limpio, &s); err != nil {
		return noConcluyente
	}
	if !s.Usable || s.Value == nil {
		return noConcluyente
	}
	v := *s.Value

	// ════════════════════════════════════════════════════════════════════════
	// 🔴 EL RANGO SE VALIDA AQUÍ, Y ESTO HACE IRRELEVANTE UN FALLO CONOCIDO
	// ════════════════════════════════════════════════════════════════════════
	//
	// Con un menú de 4 opciones, el modelo medido responde `usable:true, value:5`
	// ante «quiero 5». Se intentó cerrarlo desde el prompt y no se cierra: el LLM no
	// es la última palabra sobre el rango, el código sí. Un value fuera de las
	// Opciones que la propia Consulta trae es un veredicto NO resuelto, y el carrito
	// repromptea como el día antes de esta tarea.
	//
	// 🔬 Y NO ES «defensa por si acaso»: con estas dos líneas quitadas, la mutación no
	// devuelve el artículo equivocado — ENTRA EN PÁNICO (`index out of range [4] with
	// length 4`, y `[-1]` con el value negativo). O sea que sin esto, el fallo conocido
	// del modelo tumba la goroutine que atiende el mensaje de una persona.
	if c.Clase == modules.ClaseCantidad {
		if v < 1 || v > maxCantidad {
			return noConcluyente
		}
		// La cantidad viaja en DÍGITOS porque eso es lo que la sub-máquina del carrito
		// entiende (stepQuantity hace su propio Atoi). El Veredicto no tiene un campo
		// numérico a propósito: su único hueco es un código del catálogo.
		return modules.Veredicto{Codigo: strconv.Itoa(v)}
	}
	if v < 1 || v > len(c.Opciones) {
		return noConcluyente
	}
	// El modelo eligió una POSICIÓN de la lista que se le enseñó; el carrito entiende
	// CÓDIGOS. La traducción es esta línea y es la razón por la que al modelo nunca
	// se le enseñan los códigos: no tendría cómo saber que «Volver» es `volver`.
	return modules.Veredicto{Codigo: c.Opciones[v-1].Codigo}
}
