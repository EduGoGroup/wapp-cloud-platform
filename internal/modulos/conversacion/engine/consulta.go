// Porta internal/flujos/engine/consulta.go @ 42117b5

// consulta.go — EL RE-ENTRY: el engine resuelve lo que el módulo PURO no puede
// preguntar (Plan 044 · Ola 3.5 · T3.5-2).
//
// El contrato (qué es una Query, qué es un Verdict y por qué el texto va por
// un lado y el veredicto por otro) vive en modules/consulta.go. Aquí está el
// MECANISMO: el puerto, su cableado por Option y la segunda pasada.
//
// EL SITIO NO ES CASUAL. engine.Step ya tiene ctx en la misma función y ya siembra
// datos en Vars antes de llamar al módulo (VarContentRaw, engine.go). Resolver la
// consulta aquí no abre una costura nueva: usa la que lleva abierta desde el Plan
// 016. El módulo sigue sin ctx, sin puerto y sin I/O.
//
// Los símbolos que en el viejo tenían nombre en español llevan aquí nombre en inglés
// (E-11), y cada uno dice en su comentario cuál era. Los VALORES que salen por el
// observador (las etiquetas de desenlace) no cambian ni un byte.
//
// ════════════════════════════════════════════════════════════════════════════
// LAS TRES REGLAS DE LA SEGUNDA PASADA, Y LO QUE PASA SI SE ROMPE CADA UNA
// ════════════════════════════════════════════════════════════════════════════
//
//  1. EL PRIMER Result SE DESCARTA ENTERO, efectos incluidos, y se re-entra con
//     las Vars ORIGINALES más la clave del veredicto (en una COPIA: el mapa del
//     llamante no la ve) y con el MISMO texto del cliente. Se puede hacer porque
//     el módulo que pide NO ha mutado nada: cart clona sus Vars y devuelve la
//     petición ANTES de tocar su flag Started y su contador de inválidos (un test
//     estructural sobre el AST lo fija, orden_consulta_ast_test.go). Si un módulo
//     pidiera DESPUÉS de declarar un efecto, ese efecto se perdería en la primera
//     pasada y volvería a declararse en la segunda: lo primero es invisible, lo
//     segundo duplica.
//
//  2. EXACTAMENTE UNA RE-ENTRADA. Si la segunda pasada vuelve a pedir, no se
//     obedece: se ignora la petición —el resolutor NO se llama otra vez—, se deja
//     rastro (QueryOutcomeLoop) y se devuelve lo que el módulo produjo. NO se
//     re-entra por tercera vez ni se inventa una pantalla —el engine no conoce el
//     dominio y no sabría qué decir—. Un bucle aquí no es un test rojo: es un
//     turno de WhatsApp colgado y una persona mirando el teléfono.
//
//  3. SE DEGRADA, NO SE ABORTA. Sin resolutor, con error o con un no-concluyente,
//     el módulo recibe un veredicto EXPLÍCITO de «no resuelto» y se le llama
//     igual; Step no devuelve error por ello. Que no haya LLM no puede dejar a
//     nadie sin respuesta: la segunda pasada produce la misma pantalla que
//     produciría hoy.
//
// Y la clave del veredicto se BORRA de las Vars que salen (StripQueryVerdict)
// aunque el módulo no la haya tocado: si sobreviviera al turno, en el mensaje
// SIGUIENTE el módulo la leería como «ya preguntaste» y no volvería a pedir nunca
// más, sin un solo error.
//
// EL VEREDICTO QUE VE EL MÓDULO, según lo que pase con el resolutor:
//
//	no hay resolutor        →  Verdict{Reason: QueryReasonNoResolver}
//	devuelve error          →  Verdict{Reason: QueryReasonFailure}; lo que
//	                           acompañara al error se descarta
//	no trae nada aplicable  →  el suyo, con Reason = QueryReasonInconclusive solo
//	                           si no declaró uno
//	trae algo aplicable     →  el suyo, tal cual y SIN validar
package engine

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// QueryResolver (`ConsultaResolver` en el viejo; su método era `ResolverConsulta`)
// resuelve la Query que un módulo elevó. La interfaz la declara el CONSUMIDOR (el
// engine), no el adaptador: quien la implemente —el resolutor contra el LLM
// local— no tiene que importar nada de aquí, igual que pasa con content.Source.
// Por eso los dos identificadores viajan como STRINGS SUELTOS y no como un struct
// de este paquete: un tipo nuestro en la firma obligaría al implementador a
// importarnos, que es justo lo que esta frase promete que no hace falta.
//
// 🔴 POR QUÉ tenantID Y sessionID ESTÁN EN LA FIRMA, si el módulo no los conoce.
// Porque el resolutor real es una inferencia, y una inferencia en este ecosistema
// NO EXISTE sin tenant: la vía (local o api) es una fila por tenant (REQ-33) y el
// aislamiento es INV-7/INV-8. La sesión es la otra mitad y tampoco es decorado —
// es la que decide POR QUÉ EDGE sale la pregunta, o sea qué máquina la atiende y
// qué caché de prefijo la sirve caliente (llmvia.Selector.For). Los dos los pone
// el ENGINE desde la Conversation que ya tiene en la mano (TenantID y SessionID),
// no el módulo: el módulo sigue siendo puro y sigue sin saber que existe una nube.
// El ctx que recibe es el del Step.
//
// CONTRATO DEL IMPLEMENTADOR, y las tres partes importan:
//
//  1. Devolver un Verdict con un Code que esté EN Query.Options (o unos
//     dígitos, para QueryClassQuantity). El módulo valida lo que le llega contra el
//     catálogo que él mismo ofreció y descarta lo que no cuadre, así que inventar
//     no rompe nada — pero tampoco sirve de nada.
//  2. NO devolver texto del cliente en el Verdict. Ese campo no existe, y no es
//     un olvido (modules/consulta.go).
//  3. RESPETAR el ctx. Esto corre DENTRO de un turno de WhatsApp: lo que tarde el
//     resolutor lo espera una persona mirando el teléfono. Un resolutor sin plazo
//     propio convierte cada mensaje raro en un turno colgado.
type QueryResolver interface {
	ResolveQuery(ctx context.Context, tenantID, sessionID string, q modules.Query) (modules.Verdict, error)
}

// QueryObserver (`ObservadorConsulta` en el viejo) recibe el DESENLACE de cada
// consulta que llega a resolverse: una llamada por consulta, y una más, con
// QueryOutcomeLoop, si la segunda pasada vuelve a pedir. Un turno sin consulta no
// lo llama.
// Es un CALLBACK y no una métrica ni un logger, por la misma razón que
// cart.WithMatchHook y receipts.Sink: el engine es el núcleo PURO de la máquina de
// estados —no importa prometheus, no tiene logger y no debería tenerlo— y aun así
// esto no puede fallar en silencio.
//
// 🔴 LOS TRES ARGUMENTOS SON DE CARDINALIDAD ACOTADA POR CONSTRUCCIÓN: la clase y
// el nivel los pone el módulo (enums cerrados, modules/consulta.go; son Query.Class
// y Query.Level) y el desenlace sale de las constantes QueryOutcome* de abajo. El
// TEXTO del cliente no sale por aquí jamás —ni Query.Text ni Query.Chunks—; esto
// acaba en una etiqueta de Prometheus o en una línea de log.
//
// Un observador que entre en pánico se lleva el turno por delante: es el mismo
// trato que reciben los demás hooks del repo, y la alternativa —tragarse el
// pánico— escondería un bug de quien observa dentro del camino de quien vende.
type QueryObserver func(class, level, outcome string)

// Desenlaces posibles de una consulta (`Desenlace*` en el viejo). Los tres del
// medio son degradaciones —el módulo recibe un veredicto explícito de «no
// resuelto» y sigue— y el último es un BUG DEL MÓDULO.
const (
	// QueryOutcomeResolved (`DesenlaceResuelto`) dice que el resolutor devolvió un
	// código. Que el módulo lo acepte o lo descarte por no estar en su catálogo ya
	// no se ve desde aquí.
	QueryOutcomeResolved = "resuelto"
	// QueryOutcomeInconclusive (`DesenlaceNoConcluyente`) dice que el resolutor
	// respondió y no supo decidir.
	QueryOutcomeInconclusive = "no_concluyente"
	// QueryOutcomeNoResolver (`DesenlaceSinResolutor`) dice que no hay resolutor
	// inyectado. Con el mecanismo recién construido y el resolutor real todavía sin
	// cablear, este es el desenlace NORMAL — y es justo el que hay que poder ver
	// desde fuera para saber que un módulo está pidiendo ayuda que nadie le da.
	QueryOutcomeNoResolver = "sin_resolutor"
	// QueryOutcomeFailure (`DesenlaceFallo`) dice que el resolutor devolvió error
	// (timeout, red, modelo caído).
	QueryOutcomeFailure = "fallo"
	// QueryOutcomePartial (`DesenlaceParcial`) dice que una consulta CON TROZOS
	// (T3.5-3) resolvió algunos y no todos: se acabó el tope de llamadas, se agotó
	// el presupuesto del turno o el modelo no supo con alguno. NO es un fallo y NO
	// es un no-concluyente — el módulo recibe trabajo aplicable— y por eso tiene su
	// propio valor: mezclarlo con `resuelto` escondería justo el número que dice si
	// el tope está bien puesto.
	//
	// Se mide contra los TROZOS QUE SE PIDIERON (Query.Chunks), posición a
	// posición: es parcial en cuanto a un trozo le falta su código en Verdict.Codes
	// o lo trae vacío. Una consulta sin trozos nunca es parcial.
	QueryOutcomePartial = "parcial"
	// QueryOutcomeLoop (`DesenlaceBucle`) dice que la SEGUNDA pasada volvió a pedir
	// consulta. El engine no obedece. Ver la regla 2 de la cabecera.
	QueryOutcomeLoop = "bucle"
)

// WithQueryResolver (`WithConsultaResolver` en el viejo) inyecta el resolutor de
// consultas. Es OPCIONAL: SIN él el engine se comporta como el día antes de esta
// tarea —un módulo que pida consulta recibe un veredicto «sin_resolutor» y su
// segunda pasada produce la pantalla de siempre—. Un resolutor nil se ignora (misma
// disciplina que WithLogger del carrito): cablear a medias no puede dejar el engine
// en un estado peor que no cablear, ni quitar el que ya hubiera.
func WithQueryResolver(r QueryResolver) Option {
	panic(pendiente.Implementar("engine.WithQueryResolver"))
}

// WithQueryObserver (`WithConsultaObserver` en el viejo) inyecta el observador de
// desenlaces. Sin él el mecanismo funciona igual, en silencio — pero entonces una
// degradación es INDISTINGUIBLE de un turno normal, que es exactamente el defecto
// que este plan no quiere repetir. Un observador nil se ignora, igual que el
// resolutor.
func WithQueryObserver(fn QueryObserver) Option {
	panic(pendiente.Implementar("engine.WithQueryObserver"))
}
