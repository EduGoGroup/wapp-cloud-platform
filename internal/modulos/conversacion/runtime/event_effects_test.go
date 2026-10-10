package runtime

import "testing"

// event_effects.go solo declara datos (diez constantes): no tiene cuerpo que implementar, así
// que su test no lleva la etiqueta `pendiente` y pasa desde el primer día. Lo que fija son los
// literales, que se escriben en flow_events.name y en payload.reason de una bitácora append-only
// y de los que lee el colector de wapp_flow_event_lifecycle_total: un renombrado distraído no
// rompería nada más que esto.
//
// Lo que el runtime hace al EMITIRLOS (el kind "event" de la columna, el payload
// {history_id, kind}, la clave reason solo en event_escaped, el EffectContext sacado de la fila
// del evento) solo se ve a través del Runtime: se prueba en la ola de event_lifecycle/incoming.

// TestEventLifecycleEffects_Literals: los siete efectos de ciclo de vida, byte a byte.
func TestEventLifecycleEffects_Literals(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"started", EffectEventStarted, "event_started"},
		{"switched", EffectEventSwitched, "event_switched"},
		{"deactivated", EffectEventDeactivated, "event_deactivated"},
		{"inactivity expired", EffectEventInactivityExpired, "event_inactivity_expired"},
		{"closed", EffectEventClosed, "event_closed"},
		{"cancelled", EffectEventCancelled, "event_cancelled"},
		{"escaped", EffectEventEscaped, "event_escaped"},
	}
	seen := map[string]string{}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("efecto %s = %q, quería %q", c.name, c.got, c.want)
		}
		if other, dup := seen[c.got]; dup {
			t.Errorf("los efectos %s y %s comparten el literal %q: el embudo no los distinguiría", other, c.name, c.got)
		}
		seen[c.got] = c.name
	}
	if len(seen) != 7 {
		t.Errorf("hay %d efectos de ciclo de vida distintos, quería 7", len(seen))
	}
	if _, exists := seen["event_expired"]; exists {
		t.Error("existe el efecto event_expired: esa transición no existe (E-6 la derogó)")
	}
}

// TestEscapeReasons_Literals: las tres causas de event_escaped, byte a byte y distintas entre sí
// (viajan en payload.reason; si dos coincidieran, el embudo mediría dos fenómenos en una serie).
func TestEscapeReasons_Literals(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"owner flow finished", EscapeReasonOwnerFlowFinished, "owner_flow_finished"},
		{"orphan menu", EscapeReasonOrphanMenu, "orphan_menu"},
		{"client escape", EscapeReasonClientEscape, "client_escape"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("causa %s = %q, quería %q", c.name, c.got, c.want)
		}
		if c.got == "" {
			t.Errorf("la causa %s es vacía: una causa vacía no se escribe en el payload", c.name)
		}
		seen[c.got] = true
	}
	if len(seen) != 3 {
		t.Errorf("hay %d causas distintas, quería 3", len(seen))
	}
}
