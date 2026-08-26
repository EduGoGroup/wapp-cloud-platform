// consulta.go — EL RE-ENTRY: el engine resuelve lo que el módulo PURO no puede
// preguntar (Plan 044 · Ola 3.5 · T3.5-2).
//
// El contrato (qué es una Consulta, qué es un Veredicto y por qué el texto va por
// un lado y el veredicto por otro) vive en modules/consulta.go. Aquí está el
// MECANISMO: el puerto, su cableado por Option y la segunda pasada.
//
// EL SITIO NO ES CASUAL. engine.Step ya tiene ctx en la misma función y ya siembra
// datos en Vars antes de llamar al módulo (VarContentRaw, engine.go). Resolver la
// consulta aquí no abre una costura nueva: usa la que lleva abierta desde el Plan
// 016. El módulo sigue sin ctx, sin puerto y sin I/O.
package engine

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// ConsultaResolver resuelve la Consulta que un módulo elevó. La interfaz la
// declara el CONSUMIDOR (el engine), no el adaptador: quien la implemente —el
// resolutor contra el LLM local— no tiene que importar nada de aquí, igual que
// pasa con content.Source. Por eso los dos identificadores viajan como STRINGS
// SUELTOS y no como un struct de este paquete: un tipo nuestro en la firma
// obligaría al implementador a importarnos, que es justo lo que esta frase promete
// que no hace falta.
//
// 🔴 POR QUÉ tenantID Y sessionID ESTÁN EN LA FIRMA, si el módulo no los conoce.
// Porque el resolutor real es una inferencia, y una inferencia en este ecosistema
// NO EXISTE sin tenant: la vía (local o api) es una fila por tenant (REQ-33) y el
// aislamiento es INV-7/INV-8. La sesión es la otra mitad y tampoco es decorado —
// es la que decide POR QUÉ EDGE sale la pregunta, o sea qué máquina la atiende y
// qué caché de prefijo la sirve caliente (llmvia.Selector.For). Los dos los pone
// el ENGINE desde la Conversation que ya tiene en la mano, no el módulo: el módulo
// sigue siendo puro y sigue sin saber que existe una nube.
//
// CONTRATO DEL IMPLEMENTADOR, y las tres partes importan:
//
//  1. Devolver un Veredicto con un Codigo que esté EN Consulta.Opciones (o unos
//     dígitos, para ClaseCantidad). El módulo valida lo que le llega contra el
//     catálogo que él mismo ofreció y descarta lo que no cuadre, así que inventar
//     no rompe nada — pero tampoco sirve de nada.
//  2. NO devolver texto del cliente en el Veredicto. Ese campo no existe, y no es
//     un olvido (modules/consulta.go).
//  3. RESPETAR el ctx. Esto corre DENTRO de un turno de WhatsApp: lo que tarde el
//     resolutor lo espera una persona mirando el teléfono. Un resolutor sin plazo
//     propio convierte cada mensaje raro en un turno colgado.
type ConsultaResolver interface {
	ResolverConsulta(ctx context.Context, tenantID, sessionID string, c modules.Consulta) (modules.Veredicto, error)
}

// ObservadorConsulta recibe el DESENLACE de cada consulta que llega a resolverse.
// Es un CALLBACK y no una métrica ni un logger, por la misma razón que
// cart.WithMatchHook y receipts.Sink: el engine es el núcleo PURO de la máquina de
// estados —no importa prometheus, no tiene logger y no debería tenerlo— y aun así
// esto no puede fallar en silencio.
//
// 🔴 LOS TRES ARGUMENTOS SON DE CARDINALIDAD ACOTADA POR CONSTRUCCIÓN: la clase y
// el nivel los pone el módulo (enums cerrados, modules/consulta.go) y el desenlace
// sale de las constantes Desenlace* de abajo. El TEXTO del cliente no sale por
// aquí jamás; esto acaba en una etiqueta de Prometheus o en una línea de log.
type ObservadorConsulta func(clase, nivel, desenlace string)

// Desenlaces posibles de una consulta. Los tres del medio son degradaciones —el
// módulo recibe un veredicto explícito de «no resuelto» y sigue— y el último es un
// BUG DEL MÓDULO.
const (
	// DesenlaceResuelto dice que el resolutor devolvió un código. Que el módulo lo
	// acepte o lo descarte por no estar en su catálogo ya no se ve desde aquí.
	DesenlaceResuelto = "resuelto"
	// DesenlaceNoConcluyente dice que el resolutor respondió y no supo decidir.
	DesenlaceNoConcluyente = "no_concluyente"
	// DesenlaceSinResolutor dice que no hay resolutor inyectado. Con el mecanismo
	// recién construido y el resolutor real todavía sin cablear, este es el desenlace
	// NORMAL — y es justo el que hay que poder ver desde fuera para saber que un
	// módulo está pidiendo ayuda que nadie le da.
	DesenlaceSinResolutor = "sin_resolutor"
	// DesenlaceFallo dice que el resolutor devolvió error (timeout, red, modelo caído).
	DesenlaceFallo = "fallo"
	// DesenlaceBucle dice que la SEGUNDA pasada volvió a pedir consulta. El engine no
	// obedece. Ver reentrarConVeredicto.
	DesenlaceBucle = "bucle"
)

// WithConsultaResolver inyecta el resolutor de consultas. Es OPCIONAL: SIN él el
// engine se comporta como el día antes de esta tarea —un módulo que pida consulta
// recibe un veredicto «sin_resolutor» y su segunda pasada produce la pantalla de
// siempre—. Un resolutor nil se ignora (misma disciplina que WithLogger del
// carrito): cablear a medias no puede dejar el engine en un estado peor que no
// cablear.
func WithConsultaResolver(r ConsultaResolver) Option {
	return func(e *Engine) {
		if r != nil {
			e.consulta = r
		}
	}
}

// WithConsultaObserver inyecta el observador de desenlaces. Sin él el mecanismo
// funciona igual, en silencio — pero entonces una degradación es INDISTINGUIBLE de
// un turno normal, que es exactamente el defecto que este plan no quiere repetir.
func WithConsultaObserver(fn ObservadorConsulta) Option {
	return func(e *Engine) {
		if fn != nil {
			e.obsConsulta = fn
		}
	}
}

// reentrarConVeredicto ejecuta la SEGUNDA (y última) pasada del módulo.
//
// ════════════════════════════════════════════════════════════════════════════
// LAS TRES REGLAS, Y LO QUE PASA SI SE ROMPE CADA UNA
// ════════════════════════════════════════════════════════════════════════════
//
//  1. EL PRIMER Result SE DESCARTA ENTERO, efectos incluidos, y se re-entra con
//     las Vars ORIGINALES más la clave del veredicto. Se puede hacer porque el
//     módulo que pide NO ha mutado nada: cart clona sus Vars y devuelve la
//     petición ANTES de tocar su flag Started y su contador de inválidos (un test
//     estructural sobre el AST lo fija, orden_consulta_ast_test.go). Si un módulo
//     pidiera DESPUÉS de declarar un efecto, ese efecto se perdería en la primera
//     pasada y volvería a declararse en la segunda: lo primero es invisible, lo
//     segundo duplica.
//
//  2. EXACTAMENTE UNA RE-ENTRADA. Si la segunda pasada vuelve a pedir, no se
//     obedece: se ignora la petición, se deja rastro (DesenlaceBucle) y se
//     devuelve lo que el módulo produjo. NO se re-entra por tercera vez ni se
//     inventa una pantalla —el engine no conoce el dominio y no sabría qué decir—.
//     Un bucle aquí no es un test rojo: es un turno de WhatsApp colgado y una
//     persona mirando el teléfono.
//
//  3. SE DEGRADA, NO SE ABORTA. Sin resolutor, con error o con un no-concluyente,
//     el módulo recibe un veredicto EXPLÍCITO de «no resuelto» y se le llama
//     igual. Que no haya LLM no puede dejar a nadie sin respuesta: la segunda
//     pasada produce la misma pantalla que produciría hoy.
//
// Y la clave del veredicto se BORRA de las Vars que salen (StripConsultaVeredicto)
// aunque el módulo no la haya tocado: si sobreviviera al turno, en el mensaje
// SIGUIENTE el módulo la leería como «ya preguntaste» y no volvería a pedir nunca
// más, sin un solo error.
func (e *Engine) reentrarConVeredicto(ctx context.Context, mod modules.Module, node model.Node, st model.Conversation, texto string, c modules.Consulta) modules.Result {
	st.Vars = modules.ConVeredicto(st.Vars, e.resolverConsulta(ctx, st.TenantID, st.SessionID, c))
	res := mod.Step(node, st, texto)
	if res.Consulta != nil {
		e.observaConsulta(c, DesenlaceBucle)
		res.Consulta = nil
	}
	res.Vars = modules.StripConsultaVeredicto(res.Vars)
	return res
}

// resolverConsulta pregunta al resolutor y traduce cualquier desenlace —incluido
// «no hay resolutor»— a un Veredicto que el módulo pueda leer. NUNCA devuelve
// error: la degradación es el contrato, no una excepción.
func (e *Engine) resolverConsulta(ctx context.Context, tenantID, sessionID string, c modules.Consulta) modules.Veredicto {
	if e.consulta == nil {
		e.observaConsulta(c, DesenlaceSinResolutor)
		return modules.Veredicto{Motivo: modules.MotivoSinResolutor}
	}
	v, err := e.consulta.ResolverConsulta(ctx, tenantID, sessionID, c)
	if err != nil {
		// El error NO se propaga hacia arriba a propósito: abortar el Step dejaría a
		// la clienta sin respuesta por un servicio auxiliar que solo iba a MEJORAR la
		// interpretación de su mensaje. Se degrada al camino de siempre y el fallo se
		// ve por el observador, que es donde tiene que verse.
		e.observaConsulta(c, DesenlaceFallo)
		return modules.Veredicto{Motivo: modules.MotivoFallo}
	}
	if !v.Resuelto() {
		e.observaConsulta(c, DesenlaceNoConcluyente)
		// Se respeta el motivo que declare el resolutor y solo se rellena el que
		// falte: «no supe» es una respuesta legítima y el módulo puede querer
		// distinguirla de un fallo.
		if v.Motivo == "" {
			v.Motivo = modules.MotivoNoConcluyente
		}
		return v
	}
	e.observaConsulta(c, DesenlaceResuelto)
	// El Codigo llega SIN validar contra Consulta.Opciones: valida el MÓDULO, que
	// es el dueño de su catálogo y el único que sabe qué es admisible en su nivel.
	// El engine no interpreta el dominio ni aquí ni en ningún otro sitio.
	return v
}

// observaConsulta avisa al observador si lo hay. Un observador que entre en pánico
// se lleva el turno por delante: es el mismo trato que reciben los demás hooks del
// repo, y la alternativa —tragarse el pánico— escondería un bug de quien observa
// dentro del camino de quien vende.
func (e *Engine) observaConsulta(c modules.Consulta, desenlace string) {
	if e.obsConsulta == nil {
		return
	}
	e.obsConsulta(string(c.Clase), c.Nivel, desenlace)
}
