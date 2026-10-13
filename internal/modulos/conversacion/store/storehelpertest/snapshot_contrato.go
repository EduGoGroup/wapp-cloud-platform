package storehelpertest

import (
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// world es la MARCA DE ESTADO de la suite: todo lo que una operación del puerto puede tocar, de
// una vez (hallazgo 35 de F1). Una marca que vigilara una sola tabla —o una sola columna— dejaría
// pasar la escritura que acierta en la suya y estropea la de al lado.
//
// Lo que se puede enumerar por tenant (definiciones, efectos, respuestas, contenido, solicitudes y
// configuración) se saca entero de los DOS tenants del Montaje. Lo que solo se puede pedir por
// clave (conversaciones y bienvenidas) se saca de las claves vigiladas, que son siempre las cuatro
// de la fixture: la tocada y sus tres testigos.
type world struct {
	conversations map[store.Key]conversationMark
	welcomes      map[store.Key]WelcomeMark
	tenants       map[string]tenantMark
}

// conversationMark es lo que Load dice de una clave: si hay fila, y la fila entera.
type conversationMark struct {
	found bool
	state model.Conversation
}

// tenantMark es todo lo enumerable de un tenant.
type tenantMark struct {
	// definitions: flow_id → sus versiones, de la 1 a la vigente, cada una leída con GetDefinition.
	definitions map[string][]model.Flow
	// flowEvents y surveyResults: las dos tablas append-only, en orden de escritura.
	flowEvents    []FlowEvent
	surveyResults []SurveyResult
	// content: ref → el blob vigente, sus marcas de tiempo y sus versiones archivadas.
	content map[string]contentMark
	// intakes: id → la cabecera entera y sus líneas.
	intakes map[string]intakeMark
	// settings: lo que GetTenantSettings devuelve.
	settings Settings
}

// contentMark es un blob vigente con todo lo suyo.
type contentMark struct {
	summary  store.TenantContentSummary
	blob     []byte
	versions []ContentVersion
}

// intakeMark es una solicitud con sus líneas.
type intakeMark struct {
	header Intake
	items  []IntakeItem
}

// take saca la marca de estado: los dos tenants del Montaje y las claves dadas.
func take(t *testing.T, m Montaje, keys ...store.Key) world {
	t.Helper()
	w := world{
		conversations: make(map[store.Key]conversationMark, len(keys)),
		welcomes:      make(map[store.Key]WelcomeMark, len(keys)),
		tenants:       make(map[string]tenantMark, 2),
	}
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		w.tenants[tenant] = takeTenant(t, m, tenant)
	}
	for _, k := range keys {
		state, found, err := m.Store.Load(ctx, k)
		if err != nil {
			t.Fatalf("marca de estado: Load(%s): %v", k, err)
		}
		w.conversations[k] = conversationMark{found: found, state: state}
		w.welcomes[k] = m.Welcome(t, k.TenantID, k.SessionID, k.ContactID)
	}
	return w
}

// takeTenant saca todo lo enumerable de un tenant.
func takeTenant(t *testing.T, m Montaje, tenant string) tenantMark {
	t.Helper()
	mark := tenantMark{
		definitions:   takeDefinitions(t, m, tenant),
		flowEvents:    m.FlowEvents(t, tenant),
		surveyResults: m.SurveyResults(t, tenant),
		content:       takeContent(t, m, tenant),
		intakes:       make(map[string]intakeMark),
	}
	for _, in := range m.Intakes(t, tenant) {
		if _, dup := mark.intakes[in.ID]; dup {
			t.Fatalf("marca de estado: Montaje.Intakes(%s) devuelve dos veces la solicitud %s", tenant, in.ID)
		}
		mark.intakes[in.ID] = intakeMark{header: in, items: m.IntakeItems(t, in.ID)}
	}
	settings, err := m.Store.GetTenantSettings(ctx, tenant)
	if err != nil {
		t.Fatalf("marca de estado: GetTenantSettings(%s): %v", tenant, err)
	}
	mark.settings = settings
	return mark
}

// takeDefinitions lee cada versión de cada flujo del tenant.
func takeDefinitions(t *testing.T, m Montaje, tenant string) map[string][]model.Flow {
	t.Helper()
	summaries, err := m.Store.ListDefinitions(ctx, tenant)
	if err != nil {
		t.Fatalf("marca de estado: ListDefinitions(%s): %v", tenant, err)
	}
	out := make(map[string][]model.Flow, len(summaries))
	for _, s := range summaries {
		for v := 1; v <= s.Version; v++ {
			f, err := m.Store.GetDefinition(ctx, tenant, s.FlowID, v)
			if err != nil {
				t.Fatalf("marca de estado: GetDefinition(%s, %s, %d): %v", tenant, s.FlowID, v, err)
			}
			out[s.FlowID] = append(out[s.FlowID], f)
		}
	}
	return out
}

// takeContent lee cada blob vigente del tenant con sus marcas y sus versiones.
func takeContent(t *testing.T, m Montaje, tenant string) map[string]contentMark {
	t.Helper()
	summaries, err := m.Store.ListTenantContent(ctx, tenant)
	if err != nil {
		t.Fatalf("marca de estado: ListTenantContent(%s): %v", tenant, err)
	}
	out := make(map[string]contentMark, len(summaries))
	for _, s := range summaries {
		blob, err := m.Store.GetTenantContent(ctx, tenant, s.Ref)
		if err != nil {
			t.Fatalf("marca de estado: GetTenantContent(%s, %s): %v", tenant, s.Ref, err)
		}
		out[s.Ref] = contentMark{summary: s, blob: blob, versions: m.ContentVersions(t, tenant, s.Ref)}
	}
	return out
}

// Los forget* le quitan a una marca la fila que un caso dice que cambia, para comparar TODO lo
// demás. La fila quitada la afirma el caso aparte, columna a columna.

// forgetConversation quita la conversación de la clave.
func forgetConversation(k store.Key) func(*world) {
	return func(w *world) { delete(w.conversations, k) }
}

// forgetWelcome quita la bienvenida de la clave.
func forgetWelcome(k store.Key) func(*world) {
	return func(w *world) { delete(w.welcomes, k) }
}

// forgetDefinitions quita las versiones de ese flujo del tenant.
func forgetDefinitions(tenant, flowID string) func(*world) {
	return func(w *world) { delete(w.tenants[tenant].definitions, flowID) }
}

// forgetFlowEvents quita los efectos del tenant.
func forgetFlowEvents(tenant string) func(*world) {
	return func(w *world) {
		mark := w.tenants[tenant]
		mark.flowEvents = nil
		w.tenants[tenant] = mark
	}
}

// forgetSurveyResults quita las respuestas del tenant.
func forgetSurveyResults(tenant string) func(*world) {
	return func(w *world) {
		mark := w.tenants[tenant]
		mark.surveyResults = nil
		w.tenants[tenant] = mark
	}
}

// forgetContent quita ese blob del tenant, con sus versiones.
func forgetContent(tenant, ref string) func(*world) {
	return func(w *world) { delete(w.tenants[tenant].content, ref) }
}

// forgetIntake quita esa solicitud del tenant, con sus líneas.
func forgetIntake(tenant, id string) func(*world) {
	return func(w *world) { delete(w.tenants[tenant].intakes, id) }
}

// requireUntouched afirma que una operación RECHAZADA (o que no tenía nada que hacer) dejó la marca
// IDÉNTICA.
func requireUntouched(t *testing.T, what string, before, after world) {
	t.Helper()
	requireSameWorld(t, what, after, before)
}

// requireRestUntouched afirma que after es before salvo las filas que quitan los forget: lo que el
// caso dice que cambia. Modifica las dos marcas, así que es lo ÚLTIMO que un caso hace con ellas.
func requireRestUntouched(t *testing.T, what string, before, after world, forget ...func(*world)) {
	t.Helper()
	for _, f := range forget {
		f(&before)
		f(&after)
	}
	requireSameWorld(t, what+": el resto", after, before)
}

// requireSameWorld compara dos marcas de estado ENTERAS.
func requireSameWorld(t *testing.T, what string, got, want world) {
	t.Helper()
	for _, k := range sameKeys(t, what+": conversaciones vigiladas", got.conversations, want.conversations) {
		g, w := got.conversations[k], want.conversations[k]
		where := fmt.Sprintf("%s: conversación %s", what, k)
		if g.found != w.found {
			t.Errorf("%s: found = %v, quería %v", where, g.found, w.found)
			continue
		}
		requireSameConversation(t, where, g.state, w.state)
	}
	for _, k := range sameKeys(t, what+": bienvenidas vigiladas", got.welcomes, want.welcomes) {
		requireSameWelcome(t, fmt.Sprintf("%s: bienvenida %s", what, k), got.welcomes[k], want.welcomes[k])
	}
	for _, tenant := range sameKeys(t, what+": tenants", got.tenants, want.tenants) {
		requireSameTenant(t, fmt.Sprintf("%s: tenant %s", what, tenant), got.tenants[tenant], want.tenants[tenant])
	}
}

// requireSameTenant compara todo lo enumerable de un tenant.
func requireSameTenant(t *testing.T, what string, got, want tenantMark) {
	t.Helper()
	for _, flowID := range sameKeys(t, what+": flujos", got.definitions, want.definitions) {
		g, w := got.definitions[flowID], want.definitions[flowID]
		if len(g) != len(w) {
			t.Errorf("%s: el flujo %q tiene %d versiones, quería %d", what, flowID, len(g), len(w))
			continue
		}
		for i := range w {
			requireSameFlow(t, fmt.Sprintf("%s: flujo %q versión %d", what, flowID, i+1), g[i], w[i])
		}
	}
	requireSameFlowEvents(t, what, got.flowEvents, want.flowEvents)
	requireSameSurveyResults(t, what, got.surveyResults, want.surveyResults)
	for _, ref := range sameKeys(t, what+": contenido", got.content, want.content) {
		requireSameContent(t, fmt.Sprintf("%s: contenido %q", what, ref), got.content[ref], want.content[ref])
	}
	for _, id := range sameKeys(t, what+": solicitudes", got.intakes, want.intakes) {
		where := fmt.Sprintf("%s: solicitud %s", what, id)
		requireSameIntake(t, where, got.intakes[id].header, want.intakes[id].header)
		requireSameItems(t, where, got.intakes[id].items, want.intakes[id].items)
	}
	if !sameSettings(got.settings, want.settings) {
		t.Errorf("%s: configuración = %+v, quería %+v", what, got.settings, want.settings)
	}
}

// sameKeys afirma que los dos mapas tienen las mismas claves y devuelve las comunes, para que
// quien llama compare sus valores.
func sameKeys[K comparable, V any](t *testing.T, what string, got, want map[K]V) []K {
	t.Helper()
	common := make([]K, 0, len(want))
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("%s: falta %v", what, k)
			continue
		}
		common = append(common, k)
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: sobra %v", what, k)
		}
	}
	return common
}
