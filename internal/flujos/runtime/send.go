package runtime

import (
	"context"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// send empuja cada salida por el Sender en orden y devuelve el último Ack. Una
// salida con Media (nodo media, Plan 017 §4.2) se PRESIGNA y despacha por
// SendMedia; el resto por SendText. Ante el primer error corta y lo devuelve
// (con el último Ack logrado). El estado ya se persistió antes de llamar a send
// (orden Save-antes-de-Send), así que un fallo aquí NO corrompe el estado: se
// devuelve para que el llamante lo LOGUEE (OnIncoming) o lo surface (Start).
//
// # POR QUÉ LLEVA store.Key (Plan 049 · Opción A)
//
// Esta función es el embudo por el que sale toda auto-respuesta DEL MOTOR DE FLUJOS
// —arranque, avance, escape, menú, oferta, resumen del rescate, reinicio— y por eso
// es donde se cuenta la racha. La `key` no la necesita el envío: la necesita el
// contador, que indexa por conversación (tenant|sesión|contacto). Contar en
// sendReply, que sí la traía de serie, habría cubierto 3 de los ~10 puntos de
// emisión y habría publicado una métrica que SUBCUENTA — el Plan 049 §2.1 avisa
// explícitamente contra las métricas que mienten, y una racha que no cuenta el menú
// ni la oferta ni el rescate no sirve para calibrar ningún umbral después.
//
// ⚠️ PERO NO ES EL EMBUDO ÚNICO DE LA PLATAFORMA, y decirlo importa porque la métrica
// hereda el agujero. Fuera de este runtime hay tres SendText más:
//
//   - publicapi/messages.go y platform/httpapi/admin.go — envíos HUMANOS (alguien
//     teclea desde la API o desde la consola). Quedan fuera CON RAZÓN: no son
//     auto-respuestas y contarlos falsearía la racha hacia arriba.
//   - 🔴 intakes/notifier.go — NO es humano. Son las notificaciones de cambio de
//     estado del pedido: el motor hablándole AL MISMO CONTACTO de la conversación
//     (resuelve el destino con contacts.Destino(tenantID, in.ContactID), igual que
//     `destino` de aquí) sin que nadie haya tecleado nada. Es auto-respuesta por
//     naturaleza y NO SE CUENTA, así que la racha SUBCUENTA en las conversaciones con
//     pedido vivo. Cablear el contador en el Notifier es alcance nuevo (habría que
//     inyectárselo y darle una store.Key que hoy no maneja) y el Plan 049 · Opción A
//     no lo abre: se DOCUMENTA la exclusión —aquí y en el Help de la métrica— en vez
//     de dejar que el docstring afirme una cobertura que no existe.
func (rt *Runtime) send(ctx context.Context, sessionID, to string, key store.Key, outs []engine.Output) (*cloudlinkv1.Ack, error) {
	var last *cloudlinkv1.Ack
	emitió := false
	for _, out := range outs {
		if out.Media != nil {
			ack, err := rt.sendMedia(ctx, sessionID, to, out.Media)
			if err != nil {
				return last, err
			}
			last = ack
			emitió = true
			continue
		}
		ack, err := rt.sender.SendText(ctx, sessionID, to, out.Text)
		if err != nil {
			return last, fmt.Errorf("runtime: enviar texto: %w", err)
		}
		last = ack
		emitió = true
	}
	// 🔴 SE CUENTA UNA VEZ POR EMISIÓN —una llamada a `send`—, NO POR MENSAJE Y
	// TAMPOCO POR TURNO, y el matiz es el que da sentido a toda la métrica. Agrupar
	// aquí los varios engine.Output que puede llevar un solo `send` (el aviso del
	// reinicio + el nodo inicial, un texto + su adjunto, la página del catálogo…) sí
	// evita contar cada mensaje por separado DENTRO de una llamada; lo que NO evita es
	// que un mismo turno conversacional haga VARIAS llamadas a `send`: sendResumeSummary
	// (events.go) emite el resumen del rescate y, acto seguido, la pantalla del flujo, y
	// eso son dos incrementos para un único entrante — presentMenu y sendOfferNow tienen
	// el mismo patrón, y también emiten cosas que no son conversación (el aviso de fallo
	// del sink durable, el error de arranque de start.go). Consecuencia: la racha es una
	// COTA SUPERIOR del número de turnos. El §5 del plan estima el recorrido legítimo
	// más largo —catálogo paginado de 5 en 5— en «20-30 auto-respuestas», y esas son
	// 20-30 TURNOS, así que en esta métrica ese mismo recorrido puede salir algo más
	// alto: al leer el p99 contra los buckets finos del histograma (13-55) hay que
	// aplicar ese ajuste. Afinar el contador a turnos reales exige un concepto de
	// «turno» que hoy el runtime no tiene: es TRABAJO FUTURO, no de la Opción A.
	//
	// Y se cuenta DESPUÉS del envío y solo si de verdad salió algo: un `send` sin
	// salidas (len(outs)==0, p. ej. sendReply llamado con outs vacío) no es ninguna
	// auto-respuesta, y un intento fallido tampoco. ⚠️ Una emisión que falló A MEDIAS
	// —una salida enviada y la siguiente con error— tampoco cuenta: el `return` de
	// arriba se lleva la llamada antes de llegar aquí. Es una SUBCUENTA acotada y
	// asumida — un envío que falla no es un bucle, y el fenómeno que esta métrica
	// existe para ver (la racha que no para) es justo el que NO falla.
	if emitió {
		rt.countAutoreplyStreak(key)
	}
	return last, nil
}

// countAutoreplyStreak registra UNA auto-respuesta emitida en la racha de esa
// conversación (Plan 049 · Opción A) y deja rastro a Debug. Nil-safe: sin contador
// construido, streakCounter.Inc devuelve 0 y no hace nada.
//
// ⚠️ NO HAY UMBRAL AQUÍ, y no es un olvido: la Opción A OBSERVA y no decide. El valor
// que devuelve Inc se usa SOLO para el log; ningún `if racha > N` corta, silencia ni
// frena nada. Cortar es la Opción B del Plan 049, aplazada hasta tener 2-4 semanas de
// la distribución con la que calibrar el umbral (§9) — fijarlo hoy, a ojo, es cómo se
// deja mudo a un cliente a mitad de un pedido (§5, §6).
//
// El log va a DEBUG a propósito: es una línea por CADA auto-respuesta del sistema
// (~1 por turno de cada conversación viva), así que a Info inundaría el log igual que
// hacía el corte por passive antes de logPassiveSkip. Quien quiera la distribución
// mira /metrics; quien esté depurando UNA conversación concreta sube el nivel.
//
// PII: solo IDs OPACOS —session_id, contact_id, tenant_id—, jamás el número ni el
// texto. Es exactamente el juego de campos que ya loguea replyAllowed (incoming.go),
// que es el log hermano de este: mismo store.Key y mismo asunto (auto-respuestas de
// una conversación). Los tres salen de la MISMA `key`: pedir el session_id aparte
// abría la puerta a loguear uno y contar en otro.
//
// El instante sale del reloj INYECTABLE del runtime (rt.now, WithClock), no de
// time.Now: es el patrón del repo —lo mismo hace conversationExpired— y es lo que
// hace testeable el vencimiento por inactividad desde el motor. Con time.Now, un test
// que adelantara el reloj falso media hora vería que el contador sigue en hora de
// pared y el caso no se podría cubrir.
func (rt *Runtime) countAutoreplyStreak(key store.Key) {
	racha := rt.autoreplyStreaks.Inc(key, rt.now())
	if racha <= 0 {
		return
	}
	rt.log.Debug("runtime: auto-respuesta emitida (racha del episodio)",
		"racha", racha,
		"tenant_id", key.TenantID,
		"session_id", key.SessionID,
		"contact_id", key.ContactID,
	)
}

// sendMedia presigna la key del adjunto y lo despacha por Sender.SendMedia (Plan
// 017 §4.2/§9.C): el runtime presigna, el módulo no. Exige un Presigner cableado
// (WithPresignClient); su ausencia es un error de configuración explícito (un nodo
// media sin almacén), no un pánico. La URL prefirmada es un capability token de
// corta vida; el binario nunca viaja por la nube ni por gRPC (zero-knowledge).
func (rt *Runtime) sendMedia(ctx context.Context, sessionID, to string, ref *model.MediaRef) (*cloudlinkv1.Ack, error) {
	if rt.presigner == nil {
		return nil, fmt.Errorf("runtime: nodo media sin PresignClient configurado (usa WithPresignClient)")
	}
	url, _, err := rt.presigner.GenerateDownloadURL(ctx, ref.Key)
	if err != nil {
		return nil, fmt.Errorf("runtime: presignar media %q: %w", ref.Key, err)
	}
	ack, err := rt.sender.SendMedia(ctx, sessionID, to, url, ref.Filename, ref.Mime, ref.Caption, ref.Kind)
	if err != nil {
		return nil, fmt.Errorf("runtime: enviar media %q: %w", ref.Key, err)
	}
	return ack, nil
}

// sendReply auto-responde al avance de un entrante respetando el tope anti-loop
// (Plan 020 · T0): SOLO si hay salidas consume un token de la conversación antes de
// resolver el destino y enviar; agotado ⇒ no envía (corta el bucle; el estado ya
// avanzó y se persistió, así que no se corrompe). Sin salidas es un no-op que NO
// gasta cuota. Extraído de HandleIncoming para acotar su complejidad ciclomática.
func (rt *Runtime) sendReply(ctx context.Context, tenantID, sessionID, contactID string, key store.Key, outs []engine.Output) error {
	if len(outs) == 0 {
		return nil
	}
	if !rt.replyAllowed(key) {
		return nil
	}
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return err
	}
	_, err = rt.send(ctx, sessionID, to, key, outs)
	return err
}

// destino resuelve el contact_id a una cadena de destino DIRECCIONABLE por el
// Edge (design.md §10.E): desacopla el envío del JID entrante (doble rol, R4).
func (rt *Runtime) destino(ctx context.Context, tenantID, contactID string) (string, error) {
	dst, err := rt.contacts.Destino(ctx, tenantID, contactID)
	if err != nil {
		return "", fmt.Errorf("runtime: resolver destino: %w", err)
	}
	to, err := dst.Sendable()
	if err != nil {
		return "", fmt.Errorf("runtime: destino no direccionable: %w", err)
	}
	return to, nil
}
