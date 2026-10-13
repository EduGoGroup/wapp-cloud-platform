package triggerhelpertest

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// state es la MARCA DE ESTADO de la suite: los dos tenants enteros, con todo lo que una operación
// del puerto puede tocar (hallazgo 35 de F1). Una marca que vigilara una sola columna dejaría
// pasar la escritura que acierta en esa y estropea la de al lado.
type state struct {
	// rules son las reglas de cada tenant con sus once campos, en el orden de List (trigger_id).
	rules map[string][]trigger.Rule
	// hidden es, por tenant, lo que Montaje.Hidden da de cada regla. Vacío si Hidden es nil.
	hidden map[string]map[string]string
}

// take saca la marca de estado de los dos tenants del Montaje.
func take(t *testing.T, m Montaje) state {
	t.Helper()
	s := state{rules: map[string][]trigger.Rule{}, hidden: map[string]map[string]string{}}
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		s.rules[tenant] = list(t, m, tenant)
		s.hidden[tenant] = hidden(t, m, tenant)
	}
	return s
}

// hidden lee lo oculto de un tenant; sin Montaje.Hidden, un mapa vacío. Exige una entrada por
// regla: un observador que se dejara filas no vigilaría nada.
func hidden(t *testing.T, m Montaje, tenant string) map[string]string {
	t.Helper()
	if m.Hidden == nil {
		return map[string]string{}
	}
	got := m.Hidden(t, tenant)
	rules := list(t, m, tenant)
	if len(got) != len(rules) {
		t.Fatalf("Montaje.Hidden(%q) da %d filas y List da %d reglas; quería una por regla", tenant, len(got), len(rules))
	}
	for _, r := range rules {
		if _, ok := got[r.TriggerID]; !ok {
			t.Fatalf("Montaje.Hidden(%q) no trae la regla %s", tenant, r.TriggerID)
		}
	}
	return got
}

// add apunta en la marca esperada una regla que el caso acaba de guardar: la coloca en su sitio
// (por trigger_id) y adopta lo oculto que la implementación le puso, que la suite no puede
// predecir. Lo de las demás filas NO se relee: sigue siendo lo que había.
func (s *state) add(t *testing.T, m Montaje, r trigger.Rule) {
	t.Helper()
	if r.TriggerID == "" {
		t.Fatalf("state.add: la regla %+v no trae TriggerID", r)
	}
	s.rules[r.TenantID] = append(s.rules[r.TenantID], r)
	slices.SortFunc(s.rules[r.TenantID], func(a, b trigger.Rule) int { return strings.Compare(a.TriggerID, b.TriggerID) })
	if m.Hidden == nil {
		return
	}
	mark, ok := m.Hidden(t, r.TenantID)[r.TriggerID]
	if !ok {
		t.Fatalf("Montaje.Hidden(%q) no trae la regla recién guardada %s", r.TenantID, r.TriggerID)
	}
	s.hidden[r.TenantID][r.TriggerID] = mark
}

// remove quita de la marca esperada la regla que el caso acaba de borrar.
func (s *state) remove(tenant, triggerID string) {
	s.rules[tenant] = slices.DeleteFunc(s.rules[tenant], func(r trigger.Rule) bool { return r.TriggerID == triggerID })
	delete(s.hidden[tenant], triggerID)
}

// seedWitnesses siembra las filas testigo y devuelve la marca de estado con ellas: en cada tenant,
// una keyword global y un escape acotado a una sesión, con el MISMO contenido en los dos. Las de
// TenantA son el testigo «del mismo tenant» de quien opera en A; las de TenantB, el del otro.
func seedWitnesses(t *testing.T, m Montaje) state {
	t.Helper()
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		insert(t, m, trigger.Rule{
			TenantID: tenant, Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact,
			FlowID: "witness", Priority: 2, Enabled: true,
		})
		insert(t, m, trigger.Rule{
			TenantID: tenant, Kind: trigger.KindEscape, Keyword: "salir", MatchType: trigger.MatchContains,
			Priority: 1, Enabled: true, Message: "witness", SessionID: "session-w",
		})
	}
	s := take(t, m)
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		if len(s.rules[tenant]) != 2 {
			t.Fatalf("los testigos de %q quedaron en %d reglas, quería 2: %+v", tenant, len(s.rules[tenant]), s.rules[tenant])
		}
	}
	return s
}

// requireState afirma que los dos tenants están exactamente como dice want: las mismas reglas, en
// el mismo orden, con sus once campos, y lo oculto de cada una sin moverse.
func requireState(t *testing.T, what string, m Montaje, want state) {
	t.Helper()
	got := take(t, m)
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		name := "TenantA"
		if tenant == m.TenantB {
			name = "TenantB"
		}
		if !slices.Equal(got.rules[tenant], want.rules[tenant]) {
			t.Errorf("%s: reglas de %s =\n%+v\nquería\n%+v", what, name, got.rules[tenant], want.rules[tenant])
		}
		if !maps.Equal(got.hidden[tenant], want.hidden[tenant]) {
			t.Errorf("%s: lo que el puerto no enseña de %s =\n%v\nquería\n%v (una fila que no debía tocarse se movió)",
				what, name, got.hidden[tenant], want.hidden[tenant])
		}
	}
}

// fullRule es una regla con los once campos distintos del valor cero (TriggerID lo pone Insert).
func fullRule(tenant string) trigger.Rule {
	return trigger.Rule{
		TenantID: tenant, Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchContains,
		FlowID: "carrito-flow", Priority: 42, Enabled: true, Message: "aviso", SessionID: "session-full",
		EventKind: trigger.EventKindCart,
	}
}

// insert guarda la regla o falla el test.
func insert(t *testing.T, m Montaje, r trigger.Rule) trigger.Rule {
	t.Helper()
	out, err := m.Store.Insert(context.Background(), r)
	if err != nil {
		t.Fatalf("Insert(%+v): error inesperado %v", r, err)
	}
	return out
}

// get lee la regla o falla el test.
func get(t *testing.T, m Montaje, tenant, triggerID string) trigger.Rule {
	t.Helper()
	got, err := m.Store.Get(context.Background(), tenant, triggerID)
	if err != nil {
		t.Fatalf("Get(%q, %q): error inesperado %v", tenant, triggerID, err)
	}
	return got
}

// list lee las reglas del tenant o falla el test.
func list(t *testing.T, m Montaje, tenant string) []trigger.Rule {
	t.Helper()
	got, err := m.Store.List(context.Background(), tenant)
	if err != nil {
		t.Fatalf("List(%q): error inesperado %v", tenant, err)
	}
	return got
}

// listByKind lee las reglas de un kind aplicables a la sesión o falla el test.
func listByKind(t *testing.T, m Montaje, tenant, session string, k trigger.Kind) []trigger.Rule {
	t.Helper()
	got, err := m.Store.ListByKind(context.Background(), tenant, session, k)
	if err != nil {
		t.Fatalf("ListByKind(%q, %q, %s): error inesperado %v", tenant, session, k, err)
	}
	return got
}

// requireSorted afirma que las reglas vienen por trigger_id estrictamente ascendente.
func requireSorted(t *testing.T, what string, rules []trigger.Rule) {
	t.Helper()
	for i := 1; i < len(rules); i++ {
		if rules[i-1].TriggerID >= rules[i].TriggerID {
			t.Errorf("%s: la regla %d (%s) no va después de la %d (%s); quería orden ascendente por trigger_id",
				what, i, rules[i].TriggerID, i-1, rules[i-1].TriggerID)
		}
	}
}

// requireIDs afirma que got son exactamente las reglas want —enteras, no solo su id—, en orden
// ascendente de trigger_id.
func requireIDs(t *testing.T, what string, got []trigger.Rule, want ...trigger.Rule) {
	t.Helper()
	sorted := slices.Clone(want)
	slices.SortFunc(sorted, func(a, b trigger.Rule) int { return strings.Compare(a.TriggerID, b.TriggerID) })
	if !slices.Equal(got, sorted) {
		t.Errorf("%s:\n%+v\nquería\n%+v", what, got, sorted)
	}
}

// requireNotFound afirma que err es trigger.ErrTriggerNotFound y que el centinela dice lo que el
// llamante ve.
func requireNotFound(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, trigger.ErrTriggerNotFound) {
		t.Fatalf("%s: err = %v, quería ErrTriggerNotFound", what, err)
	}
	if got := trigger.ErrTriggerNotFound.Error(); got != notFoundText {
		t.Errorf("ErrTriggerNotFound dice %q, quería %q", got, notFoundText)
	}
}
