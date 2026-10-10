// Copia de internal/bootstrap/arranque/flowforkind.go @ 80807ba (F0 · 05 §6).
// 🔀 F8 · conmutar(conversacion): el almacén de reglas es el de internal/modulos/conversacion/trigger.
package arranque

import (
	"context"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// flowForKind resuelve «qué flujo arranca este tipo de evento» leyendo las reglas
// kind='event_start' del tenant. Lo necesita la opción «empezar uno nuevo» del menú
// del despachador, que dice el TIPO pero no el flujo.
//
// La fuente es la misma que la de los tipos ofrecibles (events.TriggerKindOffer), y
// eso no es casualidad: un tipo está ofrecido porque el tenant le puso una palabra, y
// esa misma regla es la que dice a qué flujo lleva. Dos fuentes distintas podrían
// ofrecer un tipo que luego no arrancara nada.
type flowForKind struct{ rules *trigger.PostgresStore }

// FlowForKind devuelve el flow_id de la primera regla event_start HABILITADA de ese
// tipo, en el orden determinista del store. "" si no hay ninguna: no es un error —el
// tipo `menu` no tiene flujo por diseño (D-043.3).
func (f flowForKind) FlowForKind(ctx context.Context, tenantID, sessionID, kind string) (string, error) {
	rules, err := f.rules.ListByKind(ctx, tenantID, sessionID, trigger.KindEventStart)
	if err != nil {
		return "", fmt.Errorf("bootstrap: leer las reglas event_start: %w", err)
	}
	for _, r := range rules {
		if r.Enabled && r.EventKind == kind && r.FlowID != "" {
			return r.FlowID, nil
		}
	}
	return "", nil
}
