package storehelpertest

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Los casos de ConversationStore: Exists, Load, Save y Delete sobre flow_state.

// requireExists afirma lo que Exists dice de una clave.
func requireExists(t *testing.T, m Montaje, k store.Key, want bool) {
	t.Helper()
	got, err := m.Store.Exists(ctx, k)
	if err != nil || got != want {
		t.Errorf("Exists(%s) = (%v, %v), quería (%v, nil)", k, got, err, want)
	}
}

// caseConversationUnknown: una clave sin conversación no es un error. Exists dice false y Load
// devuelve found=false con el estado cero.
func caseConversationUnknown(t *testing.T, m Montaje) {
	fx := newFixture(m)
	requireExists(t, m, fx.key, false)
	got, found, err := m.Store.Load(ctx, fx.key)
	if err != nil || found {
		t.Fatalf("Load de una clave sin conversación = (found %v, %v), quería (false, nil)", found, err)
	}
	requireSameConversation(t, "Load sin conversación", got, model.Conversation{})
}

// caseSaveRoundTrip: Save guarda las once columnas y Load las devuelve. updated_at lo pone la
// implementación con su reloj —el UpdatedAt del argumento se ignora—, y solo existe la clave
// guardada: ninguno de los tres testigos.
func caseSaveRoundTrip(t *testing.T, m Montaje) {
	fx := newFixture(m)
	before := fx.take(t)
	want := model.Conversation{
		TenantID: fx.key.TenantID, SessionID: fx.key.SessionID, ContactID: fx.key.ContactID,
		FlowID: flowOrder, FlowVersion: 3, CurrentNode: "ask",
		Vars: map[string]any{
			"reprompt": 2, "nombre": "Ana", "answers": map[string]any{"q1": "si"}, "items": []any{"CAFE", "TE"},
		},
		LastWaMessageID: "wamid.AAA",
		// event_id no tiene clave foránea; owner_event_id sí (un evento real).
		EventID:      uuid.NewString(),
		OwnerEventID: m.NewEvent(t, fx.key.TenantID),
		UpdatedAt:    time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	t0 := m.Now(t)
	mustSave(t, m, want)
	t1 := m.Now(t)

	requireExists(t, m, fx.key, true)
	for _, k := range fx.witnesses() {
		requireExists(t, m, k, false)
	}
	got := mustLoad(t, m, fx.key)
	requireStamped(t, "Save: UpdatedAt", got.UpdatedAt, t0, t1)
	want.UpdatedAt = got.UpdatedAt
	requireSameConversation(t, "Save y Load", got, want)
	requireRestUntouched(t, "Save", before, fx.take(t), forgetConversation(fx.key))
}

// caseSaveUpsert: el segundo Save de la misma clave REEMPLAZA la fila entera —lo que el estado
// nuevo no trae (una clave de Vars, el LastWaMessageID, los dos punteros de evento) desaparece— y
// refresca updated_at. Los tres testigos, que tienen su conversación, no se mueven.
func caseSaveUpsert(t *testing.T, m Montaje) {
	fx := newFixture(m)
	fx.seedWitnessConversations(t)
	first := conversationAt(fx.key, "root")
	first.Vars = map[string]any{"reprompt": 1, "nombre": "Ana"}
	first.EventID = uuid.NewString()
	first.OwnerEventID = m.NewEvent(t, fx.key.TenantID)
	mustSave(t, m, first)
	stored := mustLoad(t, m, fx.key)

	m.Advance(t)
	before := fx.take(t)
	second := model.Conversation{
		TenantID: fx.key.TenantID, SessionID: fx.key.SessionID, ContactID: fx.key.ContactID,
		FlowID: flowOrder, FlowVersion: 7, CurrentNode: "ventas",
		Vars: map[string]any{"reprompt": 0},
	}
	t0 := m.Now(t)
	mustSave(t, m, second)
	t1 := m.Now(t)

	got := mustLoad(t, m, fx.key)
	if !got.UpdatedAt.After(stored.UpdatedAt) {
		t.Errorf("el segundo Save no refrescó UpdatedAt: era %v y quedó %v", stored.UpdatedAt, got.UpdatedAt)
	}
	requireStamped(t, "segundo Save: UpdatedAt", got.UpdatedAt, t0, t1)
	second.UpdatedAt = got.UpdatedAt
	requireSameConversation(t, "segundo Save", got, second)
	if _, kept := got.Vars["nombre"]; kept {
		t.Errorf("el upsert conservó una clave de Vars que el estado nuevo no trae: %v", got.Vars)
	}
	requireRestUntouched(t, "segundo Save", before, fx.take(t), forgetConversation(fx.key))
}

// caseSaveEventPointers: los dos punteros de evento se encienden, se RELEVAN (el upsert escribe el
// valor nuevo, no conserva el viejo) y se apagan guardando la cadena vacía. Cada uno por separado:
// apagar uno no apaga el otro.
func caseSaveEventPointers(t *testing.T, m Montaje) {
	fx := newFixture(m)
	ownerA, ownerB := m.NewEvent(t, fx.key.TenantID), m.NewEvent(t, fx.key.TenantID)
	eventA, eventB := uuid.NewString(), uuid.NewString()

	steps := []struct {
		name         string
		event, owner string
	}{
		{"sin punteros", "", ""},
		{"los dos encendidos", eventA, ownerA},
		{"relevo del dueño", eventA, ownerB},
		{"relevo del evento activo", eventB, ownerB},
		{"evento activo apagado", "", ownerB},
		{"los dos encendidos otra vez", eventA, ownerA},
		{"dueño apagado", eventA, ""},
		{"los dos apagados", "", ""},
	}
	for _, step := range steps {
		c := conversationAt(fx.key, "root")
		c.EventID, c.OwnerEventID = step.event, step.owner
		mustSave(t, m, c)
		got := mustLoad(t, m, fx.key)
		if got.EventID != step.event || got.OwnerEventID != step.owner {
			t.Errorf("%s: (EventID, OwnerEventID) = (%q, %q), quería (%q, %q)",
				step.name, got.EventID, got.OwnerEventID, step.event, step.owner)
		}
	}
}

// caseSaveTerminalAndNilVars: el centinela de fin de conversación se guarda y vuelve tal cual (en
// su día llevó un byte nulo que la columna TEXT rechazaba), y unas Vars nil vuelven vacías.
func caseSaveTerminalAndNilVars(t *testing.T, m Montaje) {
	fx := newFixture(m)
	c := conversationAt(fx.key, model.NodeTerminal)
	c.Vars = nil
	c.LastWaMessageID = ""
	mustSave(t, m, c)

	got := mustLoad(t, m, fx.key)
	if got.CurrentNode != model.NodeTerminal || !got.Finished() {
		t.Errorf("CurrentNode = %q (Finished %v), quería el centinela terminal", got.CurrentNode, got.Finished())
	}
	if len(got.Vars) != 0 {
		t.Errorf("Vars = %v, quería vacías", got.Vars)
	}
	if got.LastWaMessageID != "" {
		t.Errorf("LastWaMessageID = %q, quería vacío", got.LastWaMessageID)
	}
}

// caseSaveLoadCopies: ni Save se queda con el mapa del llamante ni Load le entrega el suyo. Mutar
// uno u otro después no cambia lo guardado.
func caseSaveLoadCopies(t *testing.T, m Montaje) {
	fx := newFixture(m)
	c := conversationAt(fx.key, "root")
	nested := map[string]any{"n": "1"}
	c.Vars = map[string]any{"nombre": "Ana", "nested": nested}
	mustSave(t, m, c)
	c.Vars["nombre"] = "mutado tras guardar"
	nested["n"] = "mutado tras guardar"

	first := mustLoad(t, m, fx.key)
	first.Vars["nombre"] = "mutado tras leer"
	first.Vars["extra"] = true

	got := mustLoad(t, m, fx.key)
	want := map[string]any{"nombre": "Ana", "nested": map[string]any{"n": "1"}}
	if !sameMap(t, got.Vars, want) {
		t.Errorf("Vars = %v tras mutar los mapas del llamante, quería %v", got.Vars, want)
	}
}

// caseDelete: Delete quita la conversación de ESA clave y solo esa (los tres testigos siguen), y
// es idempotente: repetirlo, o borrar una clave que nunca existió, no es un error ni toca nada.
func caseDelete(t *testing.T, m Montaje) {
	fx := newFixture(m)
	fx.seedWitnessConversations(t)
	mustSave(t, m, conversationAt(fx.key, "root"))
	before := fx.take(t)

	if err := m.Store.Delete(ctx, fx.key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	requireExists(t, m, fx.key, false)
	if _, found, err := m.Store.Load(ctx, fx.key); err != nil || found {
		t.Errorf("Load tras Delete = (found %v, %v), quería (false, nil)", found, err)
	}
	afterDelete := fx.take(t)
	requireRestUntouched(t, "Delete", before, afterDelete, forgetConversation(fx.key))

	afterDelete = fx.take(t)
	if err := m.Store.Delete(ctx, fx.key); err != nil {
		t.Errorf("segundo Delete de la misma clave: %v, quería nil", err)
	}
	unknown := store.Key{TenantID: m.TenantA, SessionID: sessionOne, ContactID: uuid.NewString()}
	if err := m.Store.Delete(ctx, unknown); err != nil {
		t.Errorf("Delete de una clave que nunca existió: %v, quería nil", err)
	}
	requireUntouched(t, "Delete repetido", afterDelete, fx.take(t))

	// La clave quedó libre: un Save posterior vuelve a crearla.
	mustSave(t, m, conversationAt(fx.key, "again"))
	if got := mustLoad(t, m, fx.key); got.CurrentNode != "again" {
		t.Errorf("tras Delete y Save, CurrentNode = %q, quería %q", got.CurrentNode, "again")
	}
}
