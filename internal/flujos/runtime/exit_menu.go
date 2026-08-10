package runtime

// exit_menu.go resuelve el MENÚ DE SALIDA del reprompt acotado (Plan 043 · T5.2,
// D-043.10). El módulo lo ARMA (modules.ArmExitMenu) al agotar sus intentos dentro
// de un evento; aquí se interpreta la respuesta. Todo numérico: cero LLM (REQ-12).

import (
	"context"
	"fmt"
	"strings"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// exitMenuChoice interpreta la respuesta del cliente contra el menú de salida
// ARMADO por un módulo. Devuelve true si el turno se consumió.
//
// La guarda de st.EventID no es cosmética: el menú de salida solo tiene sentido
// DENTRO de un evento, y si el evento se apagó entre el menú y la respuesta (una
// cancelación desde la app, un event_stop) el «2» vuelve a ser del módulo.
//
// Las CUATRO ramas, y por qué las tres primeras persisten antes de actuar: el estado
// se guarda con la marca ya borrada para que ningún camino posterior —stopEvent
// guarda su propia copia, enterEventFlow relee de la BD— resucite un menú consumido.
func (rt *Runtime) exitMenuChoice(ctx context.Context, key store.Key, sessionID string, st model.Conversation, m *cloudlinkv1.IncomingMessage) (bool, error) {
	if rt.events == nil || st.EventID == "" || st.Vars == nil {
		return false, nil
	}
	if _, armado := st.Vars[modules.ExitMenuVar].(string); !armado {
		return false, nil
	}
	if modules.ExitMenuArmedOn(st.Vars) != st.EventID {
		// El marcador es de OTRO evento: entre el menú y esta respuesta la conversación
		// cambió de contexto por un camino que conserva las Vars (saveMenuState, el
		// menú del despachador). Se desarma en silencio y el texto sigue su camino: un
		// «2» que ya no es del carrito no puede desactivar el evento que ahora manda.
		// Es la misma norma que menuChoice se aplica a sí mismo (events.go).
		if _, err := rt.disarmExitMenu(ctx, st); err != nil {
			return false, err
		}
		return false, nil
	}
	entrada := strings.TrimSpace(m.GetText())
	if entrada != modules.ExitMenuKeepTrying && entrada != modules.ExitMenuStop && entrada != modules.ExitMenuDispatcher {
		// No era una elección: se DESARMA y el texto sigue su camino hacia el módulo,
		// que lo tratará como una entrada más del nodo (contador desde cero).
		if _, err := rt.disarmExitMenu(ctx, st); err != nil {
			return false, err
		}
		return false, nil
	}
	pantalla, err := rt.disarmExitMenu(ctx, st)
	if err != nil {
		return false, err
	}
	switch entrada {
	case modules.ExitMenuKeepTrying:
		// Re-emite la pantalla tal cual. El contador ya lo reinició el módulo al armar.
		return true, rt.resendScreen(ctx, key, sessionID, pantalla)
	case modules.ExitMenuStop:
		// Es EXACTAMENTE el event_stop de D-043.2: resumen del abandono, puntero
		// apagado, status intacto en `open`, y confirmación POR NOMBRE DE TIPO sin
		// identificador (stopNotice, events.go:614). No se duplica ni una línea.
		return true, rt.stopEvent(ctx, key, sessionID, st)
	default: // modules.ExitMenuDispatcher
		// El menú es un evento más (D-043.3). gestureGoTo y no gestureNew: «ver el
		// menú» es ir al menú, no empezar uno nuevo, y solo el gesto «nuevo» puede
		// cerrar un vencido (E-11).
		dec := trigger.Decision{Action: trigger.StartEvent, EventKind: trigger.EventKindMenu}
		return rt.beginEvent(ctx, key, sessionID, dec, gestureGoTo, st.EventID)
	}
}

// disarmExitMenu borra la marca del estado EN MEMORIA y lo persiste, devolviendo la
// pantalla guardada. Muta st.Vars, que es un mapa (tipo referencia): el llamante y
// quien reciba st después ven el borrado.
func (rt *Runtime) disarmExitMenu(ctx context.Context, st model.Conversation) (string, error) {
	pantalla := modules.DisarmExitMenu(st.Vars)
	if err := rt.store.Save(ctx, st); err != nil {
		return "", fmt.Errorf("runtime: desarmar el menú de salida: %w", err)
	}
	return pantalla, nil
}

// resendScreen re-emite la pantalla del nodo donde el cliente se atascó («1) Seguir
// intentando»). Respeta la red anti-loop: agotado el cupo, el turno se consume sin
// responder, igual que cualquier otra auto-respuesta (Plan 020 · T0).
func (rt *Runtime) resendScreen(ctx context.Context, key store.Key, sessionID, pantalla string) error {
	if pantalla == "" || !rt.replyAllowed(key) {
		return nil
	}
	to, err := rt.destino(ctx, key.TenantID, key.ContactID)
	if err != nil {
		return err
	}
	_, err = rt.send(ctx, sessionID, to, []engine.Output{{Text: pantalla}})
	return err
}
