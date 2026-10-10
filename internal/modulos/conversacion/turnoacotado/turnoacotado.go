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
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

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
type Resolver struct{}

// ErrNoTurner (`ErrSinTurnero` en el viejo) indica que el Resolver se construyó sin
// con quién preguntar. Es un fallo de ARRANQUE y por eso New lo devuelve al
// construir, no en el primer turno: mismo criterio que local.New con su Frame. El
// texto es observable y no cambia:
// "turnoacotado: el resolutor necesita un turnero (el selector de vía)".
var ErrNoTurner = errors.New("turnoacotado: el resolutor necesita un turnero (el selector de vía)")

// New construye el resolutor sobre el selector de vía. Con t nil devuelve
// (nil, ErrNoTurner); con cualquier otro, (el resolutor, nil).
func New(t Turner) (*Resolver, error) {
	panic(pendiente.Implementar("turnoacotado.New"))
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
	panic(pendiente.Implementar("turnoacotado.Resolver.ResolveQuery"))
}
