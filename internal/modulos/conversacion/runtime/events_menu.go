// Porta internal/flujos/runtime/events.go @ e0159171

package runtime

import (
	"context"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Trozo de events.go (E-13): el MENÚ del despachador, la OFERTA de entrada, la lista de lo que
// se puede retomar y el rescate (EV-7, RT-12). Solo se movieron declaraciones.

// varPendingMenu es la clave de Vars donde vive el menú YA RENDERIZADO mientras se
// espera la elección del cliente. Se persiste el menú entero —no solo un «hay menú
// pendiente»— porque los números que el cliente ve tienen que seguir significando lo
// mismo en el mensaje siguiente: reconstruirlo al vuelo lo recalcularía sobre un
// estado que pudo cambiar, y el «2» del cliente elegiría otra cosa.
const varPendingMenu = "dispatcher_menu"

// varPendingMenuEventID es el SELLO del menú pendiente (E-2): el evento sobre el que
// se armó, "" incluido (sendOffer arma la oferta SIN evento a propósito). Mismo
// patrón EXACTO que la Ola 5 usa para el menú de salida (modules.ExitMenuEventVar) y
// para los contadores de reprompt (modules.RepromptEventKey): la caducidad es POR
// LECTURA (menuChoice compara el sello contra st.EventID en cada turno) y no exige
// que nadie se acuerde de borrar nada al cambiar de evento — el mismo motivo por el
// que aquellos dos no se implementaron con un delete-al-consumir.
//
// Sin el sello, un «1» tecleado sobre una conversación SIN evento activo (o con OTRO
// distinto del que pintó la lista) se resolvía contra las opciones de un menú que ya
// no manda —incluso uno cuyo evento está `closed`— y despachaba sobre el tipo
// equivocado: saveMenuState (más abajo) CONSERVA las Vars al cambiar de evento
// —crea/hereda el flow_state sin borrar dispatcher_menu—, así que un menú armado
// para el evento A sobrevivía sin más marca que la de estar en Vars, y menuChoice no
// tenía cómo distinguirlo de uno recién pintado.
const varPendingMenuEventID = "dispatcher_menu_event_id"

// presentMenu construye el menú del despachador, lo envía y lo DEJA PENDIENTE en el
// estado para poder interpretar la respuesta del mensaje siguiente.
//
// Un menú vacío no se manda: si el tenant no ofrece nada y el contacto no tiene nada
// que retomar, mandar una lista sin opciones sería peor que callar.
func (rt *Runtime) presentMenu(ctx context.Context, key store.Key, sessionID, eventID string) error {
	if rt.dispatcher == nil {
		rt.log.Debug("runtime: sin despachador cableado; el menú no presenta nada", "session_id", sessionID)
		return nil
	}
	menu, err := rt.dispatcher.Build(ctx, events.ConversationRef{
		TenantID: key.TenantID, SessionID: sessionID, ContactID: key.ContactID,
	})
	if err != nil {
		return fmt.Errorf("runtime: construir el menú del despachador: %w", err)
	}
	if menu.Empty() {
		return nil
	}
	raw, err := menu.Encode()
	if err != nil {
		return fmt.Errorf("runtime: serializar el menú pendiente: %w", err)
	}
	if err := rt.saveMenuState(ctx, key, eventID, string(raw)); err != nil {
		return err
	}
	to, err := rt.destination(ctx, key.TenantID, key.ContactID)
	if err != nil {
		return err
	}
	_, err = rt.send(ctx, sessionID, to, key, []engine.Output{{Text: menu.Render()}})
	return err
}

// saveMenuState deja el estado apuntando al evento del menú y con el menú pendiente
// en Vars. Crea el flow_state si no existía: un contacto puede pedir el menú sin
// tener ninguna conversación abierta, y ese estado NO tiene flujo — es legítimo, y
// por eso advanceLive comprueba que haya definición antes de ir al motor.
//
// Un estado TERMINAL cuenta como «ninguna conversación abierta» y se trata igual que
// la ausencia (Plan 043 · Ola 4). No es cosmético: este es el ÚNICO camino que estampa
// el puntero sin pasar por pointStateAtEvent —el del `if flowID != ""` borra el estado
// previo, y este no—, así que heredar el flujo YA ACABADO dejaba el menú colgando de
// él, y el cierre natural (T4.1) mataba el evento del MENÚ en el siguiente entrante
// creyendo que ese flujo terminado era suyo: `closed` sin que ningún flujo suyo
// terminara —el menú ni siquiera tiene flujo (D-043.3)— y un `event_closed` falso en
// la telemetría. Verificado por sonda antes de escribir esto.
//
// Se conserva LastWaMessageID: es la dedupe del entrante, no pertenece al flujo que
// acabó, y perderla reabriría la puerta a reprocesar el mismo mensaje.
func (rt *Runtime) saveMenuState(ctx context.Context, key store.Key, eventID, raw string) error {
	st, ok, err := rt.store.Load(ctx, key)
	if err != nil {
		return fmt.Errorf("runtime: cargar estado para el menú: %w", err)
	}
	if !ok || st.Finished() {
		st = model.Conversation{
			TenantID: key.TenantID, SessionID: key.SessionID, ContactID: key.ContactID,
			LastWaMessageID: st.LastWaMessageID,
		}
	}
	if st.Vars == nil {
		st.Vars = map[string]any{}
	}
	st.Vars[varPendingMenu] = raw
	st.Vars[varPendingMenuEventID] = eventID
	st.EventID = eventID
	if err := rt.store.Save(ctx, st); err != nil {
		return fmt.Errorf("runtime: guardar el menú pendiente: %w", err)
	}
	return nil
}

// menuChoice interpreta la respuesta del cliente contra el menú pendiente. Devuelve
// true si el turno se consumió.
//
// Solo actúa si el evento ACTIVO es aquel para el que se pintó el menú (E-2, sello
// varPendingMenuEventID): en cuanto el cliente elige, o el evento cambia por
// cualquier otro camino que conserve Vars —cancelación desde la app, event_stop—,
// el activo pasa a ser otro y los números dejan de significar nada. Sin esa guarda,
// el «2» con el que alguien elige un artículo del carrito se leería como la opción 2
// de un menú que ya nadie tiene delante, y un «1» sobre una conversación sin evento
// (o con uno `closed`) se despachaba contra la lista de un menú que ya no manda.
func (rt *Runtime) menuChoice(ctx context.Context, key store.Key, sessionID string, st model.Conversation, m *cloudlinkv1.IncomingMessage) (bool, error) {
	raw, ok := st.Vars[varPendingMenu].(string)
	if !ok || raw == "" {
		return false, nil
	}
	// El aserto comprueba `ok` (y no se descarta con blank) porque este repo activa
	// errcheck.check-type-assertions (.golangci.yml): seal=="" si la clave falta o
	// no es string, igual de válido para la comparación de abajo.
	seal, sok := st.Vars[varPendingMenuEventID].(string)
	if !sok {
		seal = ""
	}
	if seal != st.EventID {
		// El menú es de OTRO evento (o de ninguno): se ignora en silencio, igual que
		// un menú ilegible más abajo — el atajo caduca, no bloquea la conversación.
		return false, nil
	}
	menu, err := events.DecodeMenu([]byte(raw))
	if err != nil {
		// Un menú ilegible no bloquea la conversación: se descarta y el texto sigue su
		// camino normal.
		rt.log.Warn("runtime: menú pendiente ilegible; se ignora", "error", err, "session_id", sessionID)
		return false, nil
	}
	choice, ok := menu.Resolve(m.GetText())
	if !ok {
		// No era un número del menú. El menú es un atajo, no una cárcel.
		return false, nil
	}
	switch choice.Action {
	case events.ActionResume:
		return true, rt.resumeEvent(ctx, key, sessionID, choice.EventID)
	case events.ActionRescue:
		// «Retomar algo que dejaste a medias» (T3.8). Elegirla NO rescata todavía: abre
		// la lista de QUÉ retomar, y el número siguiente ya llega como ActionResume con
		// su EventID. Sin este case la opción existía en la lista y no hacía nada.
		return rt.presentRescue(ctx, key, sessionID)
	case events.ActionStart:
		flowID, ferr := rt.flowForKind(ctx, key.TenantID, sessionID, choice.Kind)
		if ferr != nil {
			return false, ferr
		}
		dec := trigger.Decision{Action: trigger.StartEvent, EventKind: choice.Kind, FlowID: flowID}
		// Gesto NUEVO: es la única puerta por la que E-11 puede cerrar un vencido.
		// El `event_id` se DESCARTA (Plan 044), por lo mismo que en StartNewOfKind: el
		// texto de este turno es el NÚMERO de una lista que pintamos nosotros, no lo que
		// el cliente quiere pedir. La ventana la abre el mensaje siguiente.
		// Y openingTurn{} en el mismo acto y por lo mismo (Plan 044 · T1.4): un número de
		// menú no es el literal con el que se abre un pedido, así que no entra al hilo.
		_, done, berr := rt.beginEvent(ctx, key, sessionID, dec, gestureNew, st.EventID, openingTurn{})
		return done, berr
	default:
		return false, nil
	}
}

// presentRescue enseña la lista de lo que este contacto puede RETOMAR y la deja
// pendiente para interpretar el número siguiente. Devuelve si el turno se consumió.
//
// El caso vacío NO consume el turno, y no es un detalle: entre que se pintó la entrada
// y el cliente pulsó su número, el dueño pudo descartar el último pedido que quedaba.
// Devolver `false` deja que el texto siga su camino —acabará en el fallback, que le
// volverá a ofrecer lo que sí puede hacer— en vez de dejarlo mirando un silencio. No
// hay bucle posible: sin rescatables, la entrada que se le ofrece ya no trae esta
// opción.
func (rt *Runtime) presentRescue(ctx context.Context, key store.Key, sessionID string) (bool, error) {
	if rt.opening == nil {
		return false, nil
	}
	offer, err := rt.opening.BuildRescue(ctx, events.ConversationRef{
		TenantID: key.TenantID, SessionID: sessionID, ContactID: key.ContactID,
	})
	if err != nil {
		return false, fmt.Errorf("runtime: construir la lista de lo que se puede retomar: %w", err)
	}
	if offer.Empty() {
		rt.log.Info("runtime: no queda nada que retomar cuando el cliente lo pidió; el texto sigue su camino",
			"session_id", sessionID)
		return false, nil
	}
	return true, rt.sendOffer(ctx, key, sessionID, offer)
}

// sendOffer habla y deja el menú de la oferta PENDIENTE para el mensaje siguiente.
//
// Las dos mitades de un Offering van a sitios distintos y las dos hacen falta: Text es
// lo que el cliente lee, y Menu es lo que permite entender el número con que conteste.
// Persistir solo el texto convertiría cualquier lista en decorado.
//
// El token anti-loop se cobra aquí, justo antes de renderizar. Es el sitio por
// defecto del camino de la oferta que lo cobra (Plan 020 · T0); el ÚNICO llamante que
// NO pasa por aquí para cobrarlo es degradeDurableStart (incoming.go, Plan 054 ·
// T2.4), que ya cobró SU token en startPlainFlow antes de descubrir que el flujo
// exige evento y usa sendOfferNow para no cobrar dos veces por un solo saliente (ver
// su docstring — el mismo doble cobro que este comentario evitaba, reintroducido por
// ese camino nuevo y corregido aquí).
func (rt *Runtime) sendOffer(ctx context.Context, key store.Key, sessionID string, offer events.Offering) error {
	if !rt.replyAllowed(key) {
		return nil
	}
	return rt.sendOfferNow(ctx, key, sessionID, offer)
}

// sendOfferNow es el CUERPO de sendOffer sin su cobro del token anti-loop: existe
// para degradeDurableStart (incoming.go, D1 del review de Plan 054 · F2), el único
// llamante cuyo token YA se cobró antes de llegar aquí (startPlainFlow lo cobra vía
// replyAllowed ANTES de intentar startLocked). Cobrarlo de nuevo aquí sería el doble
// cobro por un solo mensaje saliente que el docstring de openWithOffer ya advertía:
// con el cupo justo (burst 1), el segundo cobro fallaría y la degradación se quedaría
// MUDA aunque el WARN que la precede diga que sí se ofrece. NINGÚN otro llamante debe
// usar esta función directamente: si necesitas enviar una oferta y no has cobrado
// tú mismo el token, usa sendOffer.
func (rt *Runtime) sendOfferNow(ctx context.Context, key store.Key, sessionID string, offer events.Offering) error {
	raw, err := offer.Menu.Encode()
	if err != nil {
		return fmt.Errorf("runtime: serializar la oferta pendiente: %w", err)
	}
	// eventID vacío A PROPÓSITO: esta conversación no tiene evento —de eso trata la
	// tarea— y estamparle uno sería inventar un puntero a algo que no ha nacido.
	if err := rt.saveMenuState(ctx, key, "", string(raw)); err != nil {
		return err
	}
	to, err := rt.destination(ctx, key.TenantID, key.ContactID)
	if err != nil {
		return err
	}
	_, err = rt.send(ctx, sessionID, to, key, []engine.Output{{Text: offer.Text}})
	return err
}

// resumeEvent retoma un evento elegido por su número en el menú. Se RELEE en vez de
// fiarse del id que venía en el menú, porque entre que se pintó la lista y que el
// cliente contestó pudo pasar cualquier cosa: que el dueño cerrara el evento desde su
// app, o —lo que este código arregla— que descartara el pedido del que colgaba.
//
// La relectura va por ListRescuable y NO por ListAlive, y ahí está toda la tarea
// (INV-17, REQ-26c). «Vivo» y «rescatable» no son lo mismo: un evento cuyo pedido fue
// descartado sigue `open` —nada lo cierra por tiempo, D-041.18— y aun así no puede
// retomarse. Con ListAlive, un menú pintado un segundo antes del descarte seguía
// pudiendo resucitarlo, y el invariante que dice «no se lista, NO SE RESCATA y no se
// menciona» se cumplía solo en sus dos tercios visibles.
//
// limit 0 = sin tope, a propósito: aquí no se pinta una lista sino que se comprueba
// una PERTENENCIA, y recortar la consulta podría dejar fuera justo el que el cliente
// eligió.
func (rt *Runtime) resumeEvent(ctx context.Context, key store.Key, sessionID, eventID string) error {
	rescuables, err := rt.events.ListRescuable(ctx, key.TenantID, sessionID, key.ContactID, 0)
	if err != nil {
		return fmt.Errorf("runtime: releer los rescatables al retomar: %w", err)
	}
	for _, r := range rescuables {
		if r.ID == eventID {
			// UN solo token para los DOS salientes (resumen + pantalla del flujo): al
			// cliente que vuelve se le está dando UNA respuesta, servida en dos mensajes.
			// Cobrar dos dejaría mudo el rescate justo cuando el cupo va justo. El
			// resumen lo envía switchToEvent, por donde pasan los dos caminos de vuelta.
			if !rt.replyAllowed(key) {
				return nil
			}
			return rt.switchToEvent(ctx, key, sessionID, r.Event)
		}
	}
	rt.log.Info("runtime: el evento elegido ya no se puede retomar; no se rescata",
		"session_id", sessionID)
	return nil
}

// flowForKind resuelve el flujo que arranca un tipo. Sin puerto cableado, o sin regla
// para ese tipo, devuelve "" y el evento nace sin flujo (el caso del menú).
func (rt *Runtime) flowForKind(ctx context.Context, tenantID, sessionID, kind string) (string, error) {
	if rt.flows == nil {
		return "", nil
	}
	flowID, err := rt.flows.FlowForKind(ctx, tenantID, sessionID, kind)
	if err != nil {
		return "", fmt.Errorf("runtime: resolver el flujo del tipo %q: %w", kind, err)
	}
	return flowID, nil
}
