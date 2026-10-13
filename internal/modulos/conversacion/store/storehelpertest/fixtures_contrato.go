package storehelpertest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// ctx es el contexto de todas las llamadas de la suite: ninguna promesa del puerto depende de él.
var ctx = context.Background()

// Los literales que comparten los casos. Sesiones, refs, flujos y skus son ASCII y ninguno decide
// un orden que dependa de la colación.
const (
	sessionOne = "sess-1"
	sessionTwo = "sess-2"
	flowMenu   = "menu"
	flowOrder  = "pedido"
	refCatalog = "catalogo"
	refMenu    = "menu"
	// platformSKU es un sku de LA PLATAFORMA: empieza por el prefijo reservado "_".
	platformSKU = "_shipping"
)

// fixture son las CUATRO claves conversacionales de un caso: la que el caso toca y tres testigos
// que difieren de ella en UNA sola componente. Si una sentencia pierde un predicado de su WHERE
// (el tenant, la sesión o el contacto), toca además a uno de los tres, y la marca de estado lo ve.
type fixture struct {
	m Montaje
	// key es la clave que el caso toca: (TenantA, sess-1, contacto).
	key store.Key
	// otherContact: mismo tenant y sesión, otro contacto.
	otherContact store.Key
	// otherSession: mismo tenant y contacto, otra sesión.
	otherSession store.Key
	// otherTenant: misma sesión y MISMO contacto, en TenantB.
	otherTenant store.Key
}

// newFixture arma las cuatro claves sobre los dos tenants del Montaje. Los contactos son UUID
// nuevos: las columnas son de tipo uuid y no tienen clave foránea.
func newFixture(m Montaje) fixture {
	contact := uuid.NewString()
	return fixture{
		m:            m,
		key:          store.Key{TenantID: m.TenantA, SessionID: sessionOne, ContactID: contact},
		otherContact: store.Key{TenantID: m.TenantA, SessionID: sessionOne, ContactID: uuid.NewString()},
		otherSession: store.Key{TenantID: m.TenantA, SessionID: sessionTwo, ContactID: contact},
		otherTenant:  store.Key{TenantID: m.TenantB, SessionID: sessionOne, ContactID: contact},
	}
}

// witnesses son las tres claves testigo.
func (f fixture) witnesses() []store.Key {
	return []store.Key{f.otherContact, f.otherSession, f.otherTenant}
}

// take saca la marca de estado vigilando las cuatro claves.
func (f fixture) take(t *testing.T) world {
	t.Helper()
	return take(t, f.m, append([]store.Key{f.key}, f.witnesses()...)...)
}

// conversationAt es un estado conversacional de esa clave en ese nodo, con todo lo demás relleno.
func conversationAt(k store.Key, node string) model.Conversation {
	return model.Conversation{
		TenantID: k.TenantID, SessionID: k.SessionID, ContactID: k.ContactID,
		FlowID: flowMenu, FlowVersion: 1, CurrentNode: node,
		Vars:            map[string]any{"node": node},
		LastWaMessageID: "wamid." + node,
	}
}

// mustSave guarda el estado o falla el test.
func mustSave(t *testing.T, m Montaje, c model.Conversation) {
	t.Helper()
	if err := m.Store.Save(ctx, c); err != nil {
		t.Fatalf("Save(%s|%s|%s): %v", c.TenantID, c.SessionID, c.ContactID, err)
	}
}

// mustLoad carga el estado de una clave que TIENE que existir.
func mustLoad(t *testing.T, m Montaje, k store.Key) model.Conversation {
	t.Helper()
	got, found, err := m.Store.Load(ctx, k)
	if err != nil || !found {
		t.Fatalf("Load(%s) = (found %v, %v), quería la conversación guardada", k, found, err)
	}
	return got
}

// seedWitnessConversations guarda una conversación en cada una de las tres claves testigo.
func (f fixture) seedWitnessConversations(t *testing.T) {
	t.Helper()
	for _, k := range f.witnesses() {
		mustSave(t, f.m, conversationAt(k, "witness"))
	}
}

// strPtr devuelve un puntero a s (el Next de un nodo de mensaje).
func strPtr(s string) *string { return &s }

// sampleFlow es una definición válida de ese flujo, con los tres tipos de nodo y una referencia de
// contenido. greeting la distingue de otra versión del mismo flujo.
func sampleFlow(flowID, greeting string) model.Flow {
	return model.Flow{
		FlowID:  flowID,
		Version: 99, // el repositorio la ignora: la versión la asigna él.
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {
				Type:    model.NodeTypeMenu,
				Prompt:  greeting,
				Options: map[string]string{"1": "ask", "2": "bye"},
				Content: &model.ContentRef{Source: "json", Ref: refCatalog},
			},
			"ask": {Type: model.NodeTypeSurveyQuestion, Prompt: "¿Volverías?", QuestionID: "q1",
				Options: map[string]string{"1": "bye"}},
			"bye":  {Type: model.NodeTypeMessage, Text: "Gracias.", Next: strPtr("done")},
			"done": {Type: model.NodeTypeMessage, Text: "Fin."},
		},
	}
}

// mustInsertDefinition publica la definición y devuelve la versión asignada.
func mustInsertDefinition(t *testing.T, m Montaje, tenant string, f model.Flow) int {
	t.Helper()
	version, err := m.Store.InsertDefinition(ctx, tenant, f)
	if err != nil {
		t.Fatalf("InsertDefinition(%s, %s): %v", tenant, f.FlowID, err)
	}
	return version
}

// blob es un documento JSON pequeño que se distingue por su etiqueta.
func blob(tag string) []byte {
	return []byte(`{"prompt": "` + tag + `", "items": [{"sku": "CAFE", "price": 2.5}]}`)
}

// mustUpsertContent escribe el blob sin versionar, o falla el test.
func mustUpsertContent(t *testing.T, m Montaje, tenant, ref string, raw []byte) {
	t.Helper()
	if err := m.Store.UpsertTenantContent(ctx, tenant, ref, raw); err != nil {
		t.Fatalf("UpsertTenantContent(%s, %s): %v", tenant, ref, err)
	}
}

// mustReplaceVersioned escribe el blob versionando y devuelve el número archivado.
func mustReplaceVersioned(t *testing.T, m Montaje, tenant, ref string, raw []byte, source string) int {
	t.Helper()
	archived, err := m.Store.ReplaceTenantContentVersioned(ctx, tenant, ref, raw, source)
	if err != nil {
		t.Fatalf("ReplaceTenantContentVersioned(%s, %s, %s): %v", tenant, ref, source, err)
	}
	return archived
}

// newIntake es la cabecera de una solicitud NUEVA de ese contacto en ese estado, con un id propio
// y un evento padre propio (Montaje.NewEvent). El contacto de una solicitud es texto opaco.
func newIntake(t *testing.T, m Montaje, tenant, contact, status string) Intake {
	t.Helper()
	return Intake{
		ID: uuid.NewString(), TenantID: tenant, ContactID: contact, SessionID: sessionOne,
		Status: status, Total: 4.5, EventID: m.NewEvent(t, tenant),
	}
}

// mustUpsertIntake guarda la cabecera o falla el test.
func mustUpsertIntake(t *testing.T, m Montaje, in Intake) {
	t.Helper()
	if err := m.Store.UpsertIntake(ctx, in); err != nil {
		t.Fatalf("UpsertIntake(%s): %v", in.ID, err)
	}
}

// seedIntake crea una solicitud de ese contacto en ese estado y devuelve su cabecera tal como la
// pidió la suite (sin las fechas, que las pone la implementación).
func seedIntake(t *testing.T, m Montaje, tenant, contact, status string) Intake {
	t.Helper()
	in := newIntake(t, m, tenant, contact, status)
	mustUpsertIntake(t, m, in)
	return in
}

// observedIntake devuelve la fila ENTERA de la solicitud, por el observador del Montaje.
func observedIntake(t *testing.T, m Montaje, tenant, id string) Intake {
	t.Helper()
	for _, in := range m.Intakes(t, tenant) {
		if in.ID == id {
			return in
		}
	}
	t.Fatalf("la solicitud %s no está entre las del tenant %s", id, tenant)
	return Intake{}
}

// line es una línea de cliente sin fecha ni solicitud: las pone la implementación.
func line(sku, label string, qty int, unitPrice float64) IntakeItem {
	return IntakeItem{SKU: sku, Label: label, Qty: qty, UnitPrice: unitPrice}
}

// mustReplaceItems deja las líneas de cliente de la solicitud en items, o falla el test.
func mustReplaceItems(t *testing.T, m Montaje, intakeID string, items ...IntakeItem) {
	t.Helper()
	if err := m.Store.ReplaceIntakeItems(ctx, intakeID, items); err != nil {
		t.Fatalf("ReplaceIntakeItems(%s): %v", intakeID, err)
	}
}

// requireLines afirma que las líneas guardadas de la solicitud son exactamente want, en orden, con
// el IntakeID puesto y una fecha que no es cero. No mira el valor de la fecha.
func requireLines(t *testing.T, m Montaje, what, intakeID string, want ...IntakeItem) []IntakeItem {
	t.Helper()
	got := m.IntakeItems(t, intakeID)
	if len(got) != len(want) {
		t.Fatalf("%s: %d líneas, quería %d: %+v", what, len(got), len(want), got)
	}
	for i := range want {
		w := want[i]
		w.IntakeID = intakeID
		if !sameLine(got[i], w) {
			t.Errorf("%s: línea %d = %+v, quería %+v", what, i, got[i], w)
		}
		if got[i].AddedAt.IsZero() {
			t.Errorf("%s: la línea %d (%s) trae AddedAt cero", what, i, w.SKU)
		}
	}
	return got
}

// at es un instante de agosto de 2026 en segundos enteros: el día d a las h horas (UTC). Son los
// que la suite le pasa a la bienvenida, cuyo reloj es el del llamante.
func at(d, h int) time.Time {
	return time.Date(2026, 8, d, h, 0, 0, 0, time.UTC)
}
