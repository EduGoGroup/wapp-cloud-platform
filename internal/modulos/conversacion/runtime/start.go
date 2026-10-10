// Porta internal/flujos/runtime/start.go @ e0159171

package runtime

import (
	"context"
	"errors"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// start.go es el ARRANQUE de una conversación: la puerta exportada de la API (Start) y, en el
// verde (F8-04b), el embudo no exportado por el que pasa TODO arranque —API, keyword,
// fallback y evento—, que es quien aplica las reglas de abajo. Los tests de la ola siguiente
// las prueban por Start y, las de los arranques reactivos, por HandleIncoming.
//
// # Reglas del embudo de arranque
//
// ST-A · Orden: definición vigente → guarda de contenido durable → guarda de existencia →
// engine.EnterPrimed → coletilla → fan-out de los efectos del pre-carga → Save → hilo del
// turno de apertura → destino → envío. RT-4: el Save va ANTES del envío.
//
// ST-B · Un rechazo no deja rastro: ErrDurableFlowNeedsEvent y ErrConversationExists salen
// antes de escribir estado, emitir efectos o enviar nada.
//
// ST-C · El estado nace FRESCO: tenant, sesión y contacto, sin Vars heredadas. Con parámetros
// de intención se siembran Vars[modules.VarIntentParams] y Vars[modules.VarIntentName] antes
// del primer paso; sin ellos, nada. (Camino sin productor hoy: ninguna regla lo dispara.)
//
// ST-D · El evento del arranque llega por PARÁMETRO, nunca del estado: el puntero se estampa
// después. Los efectos del pre-carga llevan ese evento en su EffectContext y Durable según el
// tipo del nodo INICIAL.
//
// ST-E · RT-10 en el arranque. Si el sink durable agota su reintento (resume.go), el arranque
// se CORTA antes del Save: no se guarda estado, no se escribe hilo, el cliente recibe el aviso
// de avería (el literal de incoming.go) y el resultado es el Ack de ese aviso SIN error. Log a
// ERROR «runtime: arranque cortado: el sink durable no pudo materializar el pre-carga tras el
// reintento acotado». Ese mensaje no entra en la ventana de captación.
//
// ST-F · La coletilla llega resuelta, se pega al final del ÚLTIMO texto con "\n\n" y solo si
// hay un texto al que pegarla; pegada, se marca Vars["tagline_offered"] = true en el MISMO
// Save. Con la respuesta terminada en adjunto no se pega ni se marca.
//
// ST-G · El hilo del turno de apertura (thread.go) se escribe solo si el arranque nació de un
// entrante del cliente Y trae evento: el literal del cliente y después las salidas, coletilla
// incluida. Start (API), el arranque plano y la conmuta no lo escriben.
//
// ST-H · El arranque cuenta como la primera auto-respuesta del episodio en la racha (RT-11),
// también cuando viene de Start.

// ErrConversationExists lo devuelve Start cuando ya hay una conversación viva para la clave
// (la cara HTTP lo traduce a 409). Se inspecciona con errors.Is.
//
// En el arranque reactivo (un disparo que pierde la carrera contra otro entrante) NO sube: se
// trata como carrera benigna, con Info «runtime: disparo abortado por conversación ya viva
// (carrera benigna)».
var ErrConversationExists = errors.New("ya existe una conversación viva para la clave")

// ErrDurableFlowNeedsEvent se devuelve cuando el flujo exige un evento padre —algún nodo suyo
// es de un módulo que produce contenido durable (cart, survey)— y la puerta por la que se
// intenta arrancar no trae uno: la API, una keyword o el fallback (D-054.5). Se inspecciona
// con errors.Is.
//
// Sin esta guarda el flujo arrancaba y, turnos después, perdía el pedido contra el NOT NULL
// de intakes.event_id sin que nada avisara (hallazgos 054 #001/#003).
//
// La CONSECUENCIA la decide cada llamante:
//
//   - Start lo devuelve tal cual; la cara HTTP lo traduce a 409 con un motivo distinto del de
//     ErrConversationExists.
//   - El arranque reactivo NUNCA se lo enseña al cliente: lo DEGRADA a la oferta del
//     despachador (ver HandleIncoming, RT-9b).
var ErrDurableFlowNeedsEvent = errors.New("el flujo exige un evento padre para arrancar por esta puerta")

// Start abre una conversación por API (design.md §6, decisión C) para el contacto ref en la
// sesión sessionID, con el flujo flowID del tenant.
//
// Pasos y resultado:
//
//  1. Resuelve ref a un contact_id OPACO (el motor opera por contact_id, no por el JID). Si
//     falla: error envuelto «runtime: resolver contacto: …», sin tocar nada más.
//  2. Toma el candado de la conversación (tenant, sesión, contacto): un Start y un entrante
//     de la misma clave no se solapan.
//  3. Lee la definición VIGENTE del flujo. Si falla: «runtime: definición vigente: …».
//  4. Flujo con contenido durable → ErrDurableFlowNeedsEvent (ST-B). Start nunca trae evento:
//     un flujo con `cart` o `survey` se abre por la puerta de eventos, no por aquí.
//  5. Ya hay estado para la clave → ErrConversationExists, INCONDICIONAL: no se consulta
//     ninguna política de reanudación ni se reinicia una conversación terminal (Plan 053 ·
//     Ola 7). El estado que había queda intacto.
//  6. Entra al nodo inicial, GUARDA el estado (flow_id, versión vigente, nodo) y después
//     ENVÍA las salidas en orden: texto por SendText, adjunto prefirmado por SendMedia.
//
// Devuelve el Ack de la ÚLTIMA salida enviada, o nil si el nodo inicial no produjo ninguna.
// Si el envío falla, el error sube con el estado YA guardado (RT-4): un segundo Start da
// ErrConversationExists. Un adjunto sin Presigner cableado es ese mismo caso, con un error
// controlado y sin pánico.
//
// Lo que Start NO hace, a propósito: no crea evento conversacional, no consume token del
// limitador de auto-respuestas (lo pulsa un humano, no es una reacción), no pega coletilla,
// no escribe hilo ni abre ventana de captación, y no pasa por las guardas del entrante
// (perfil pasivo, anti-self-loop, dedupe). Sí cuenta en la racha (ST-H).
func (rt *Runtime) Start(ctx context.Context, tenantID, flowID, sessionID string, ref contact.Ref) (*cloudlinkv1.Ack, error) {
	panic(pendiente.Implementar("runtime.Runtime.Start"))
}
