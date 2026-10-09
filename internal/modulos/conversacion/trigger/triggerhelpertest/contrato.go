// Package triggerhelpertest es la suite de contrato del puerto trigger.Store. Ningún código de
// producción lo importa: arrastra "testing".
//
// La suite la corren las dos implementaciones del puerto: trigger.MemoryStore en unitario
// (store_memory_test.go) y trigger.PostgresStore en los procesos de F9, con el arnés de
// testcontainers (test/procesos/trigger_contrato_test.go).
//
// Nuevo: no tiene fichero viejo. Los casos salen del contrato de trigger.Store y de los tests
// viejos, leídos y no portados: internal/flujos/trigger/store_memory_test.go (4) y
// store_postgres_test.go (5: InsertGetDelete, NullMappingAndList, SessionScope, TenantIsolation,
// EventKindRoundTrip).
package triggerhelpertest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// notFoundText es el texto de trigger.ErrTriggerNotFound, byte a byte.
const notFoundText = "regla de disparo no encontrada"

// Montaje es lo que cada implementación entrega a la suite para UN caso: Contrato llama a nuevo
// una vez por caso.
type Montaje struct {
	// Store es la implementación bajo prueba.
	Store trigger.Store
	// TenantA y TenantB son dos tenant_id distintos, con forma de UUID (flow_triggers.tenant_id es
	// UUID) y SIN reglas.
	TenantA, TenantB string
	// Hidden da, para un tenant, lo que la implementación guarda de cada regla y el puerto NO
	// enseña: trigger_id → un texto opaco con esas columnas (en Postgres, created_at y
	// updated_at). La suite no lo interpreta: solo exige que el de una fila que una operación no
	// debía tocar siga byte a byte igual. Es opcional: nil dice «no guardo nada fuera de Rule»,
	// que es el caso de la memoria.
	//
	// Por qué existe (hallazgo 35 de F1): la marca de estado tiene que vigilar TODAS las columnas
	// que una operación puede tocar. Rule proyecta once de las trece de flow_triggers; sin esto,
	// una escritura que reescribiera de más (un UPDATE sin acotar que refresca updated_at) pasaría
	// en verde.
	Hidden func(t *testing.T, tenantID string) map[string]string
}

// Contrato ejecuta las promesas de trigger.Store contra la implementación que devuelve nuevo, con
// un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta nada.
//
// La MARCA DE ESTADO (state) son los dos tenants enteros: cada regla con sus once campos, en el
// orden de List, más lo oculto de cada una (Montaje.Hidden). Todo caso siembra antes cuatro filas
// testigo —dos en TenantA, el que opera, y dos en TenantB— y al final compara la marca ENTERA con
// la esperada: la de antes, más o menos la regla que la operación debía tocar.
//
// Lo que la suite NO afirma, a propósito:
//
//   - nada con un tenant_id o un trigger_id que no tenga forma de UUID: Postgres devuelve un
//     error de sintaxis y la memoria, ErrTriggerNotFound. Los que la suite inventa son UUID;
//   - el valor concreto del trigger_id que asigna Insert, solo que no es vacío, que no es el del
//     argumento y que no se repite;
//   - ninguna validación de la regla (kind o match_type desconocidos, event_kind fuera del
//     vocabulario): el puerto guarda lo que le dan; validar es del CRUD.
//
// El orden por trigger_id se compara como texto: los dos lo dan en minúsculas canónicas, donde el
// orden del texto y el del UUID de Postgres coinciden.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("triggerhelpertest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
	}
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateMontaje(t, m)
			c.run(t, m)
		})
	}
}

// contractCase es una promesa del puerto: su nombre (el del t.Run) y la función que la afirma.
type contractCase struct {
	name string
	run  func(t *testing.T, m Montaje)
}

// cases es la tabla de la suite. El comentario de cada fila es la promesa que fija.
func cases() []contractCase {
	return []contractCase{
		{"Insert_AssignsTriggerID_IgnoresTheArgument", caseInsertAssignsID},           // el id lo pone el almacén
		{"Insert_ReturnsTheRuleItStored", caseInsertReturnsStored},                    // lo devuelto es lo guardado
		{"Insert_EveryKind_RoundTripsEveryColumn", caseRoundTrip},                     // los once campos, por Get
		{"Insert_EmptyOptionals_ReadBackEmpty", caseEmptyOptionals},                   // "" ⇔ NULL
		{"Insert_TouchesNoOtherRule", caseInsertTouchesNothingElse},                   // testigos intactos
		{"List_TenantWithoutRules_EmptyNotNil", caseEmptyList},                        // vacío no es nil ni error
		{"List_AllKindsAndSessions_SortedByTriggerID", caseListSorted},                // sin filtro, en orden
		{"ListByKind_OnlyThatKind", caseListByKindFiltersKind},                        // filtra por kind
		{"ListByKind_Session_SeesItsOwnAndTheGlobals", caseListByKindSession},         // sesión ⇒ suyas + globales
		{"ListByKind_EmptySession_OnlyGlobals", caseListByKindGlobalView},             // "" ⇒ solo globales
		{"ListByKind_NothingApplies_EmptyNotNil", caseListByKindEmpty},                // vacío no es nil
		{"Get_UnknownID_ErrTriggerNotFound", caseGetUnknown},                          // centinela y su texto
		{"Get_OtherTenant_ErrTriggerNotFound", caseGetOtherTenant},                    // INV-8
		{"Delete_RemovesOnlyThatRule", caseDeleteRemovesOne},                          // borra una, y solo esa
		{"Delete_Twice_SecondIsErrTriggerNotFound", caseDeleteTwice},                  // ya no existe
		{"Delete_UnknownID_ErrTriggerNotFound", caseDeleteUnknown},                    // no borra nada
		{"Delete_OtherTenant_ErrTriggerNotFound_KeepsTheRule", caseDeleteOtherTenant}, // INV-8
		{"TenantsWithTheSameRules_DoNotSeeEachOther", caseTenantIsolation},            // aislamiento (INV-8)
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Store == nil:
		t.Fatal("Montaje.Store es nil")
	case m.TenantA == m.TenantB:
		t.Fatalf("Montaje: TenantA y TenantB son el mismo (%q); deben ser distintos", m.TenantA)
	}
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		if _, err := uuid.Parse(tenant); err != nil {
			t.Fatalf("Montaje: el tenant %q no tiene forma de UUID: %v", tenant, err)
		}
		if got := list(t, m, tenant); len(got) != 0 {
			t.Fatalf("Montaje: el tenant %q trae %d reglas; tiene que venir sin ninguna", tenant, len(got))
		}
	}
}

func caseInsertAssignsID(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	given := uuid.NewString()
	in := trigger.Rule{
		TenantID: m.TenantA, TriggerID: given, Kind: trigger.KindKeyword, Keyword: "pedido",
		MatchType: trigger.MatchExact, FlowID: "carrito", Priority: 7, Enabled: true,
	}
	first := insert(t, m, in)
	second := insert(t, m, in)
	for _, got := range []trigger.Rule{first, second} {
		if got.TriggerID == "" || got.TriggerID == given {
			t.Errorf("Insert devolvió TriggerID %q; quería uno nuevo, ni vacío ni el del argumento (%q)", got.TriggerID, given)
		}
	}
	if first.TriggerID == second.TriggerID {
		t.Errorf("dos Insert dieron el mismo TriggerID %q", first.TriggerID)
	}
	if _, err := m.Store.Get(context.Background(), m.TenantA, given); !errors.Is(err, trigger.ErrTriggerNotFound) {
		t.Errorf("Get con el TriggerID del argumento: err = %v, quería ErrTriggerNotFound (el almacén lo ignora)", err)
	}
	want.add(t, m, first)
	want.add(t, m, second)
	requireState(t, "tras dos Insert", m, want)
}

func caseInsertReturnsStored(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	in := fullRule(m.TenantA)
	out := insert(t, m, in)
	in.TriggerID = out.TriggerID
	if out != in {
		t.Errorf("Insert devolvió %+v, quería el argumento con su TriggerID: %+v", out, in)
	}
	if got := get(t, m, m.TenantA, out.TriggerID); got != out {
		t.Errorf("Get = %+v, quería lo que devolvió Insert: %+v", got, out)
	}
	want.add(t, m, out)
	requireState(t, "tras el Insert", m, want)
}

// caseRoundTrip guarda una regla de cada kind con todos los campos que le tocan, más las que
// nadie interpreta (texto con espacios, acentos y emoji; kind y match_type desconocidos o vacíos;
// priority negativa), y las lee una a una: el almacén no normaliza ni valida nada.
func caseRoundTrip(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	a := m.TenantA
	rules := []trigger.Rule{
		{TenantID: a, Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Priority: 7, Enabled: true},
		{TenantID: a, Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchContains, FlowID: "saludo", SessionID: "session-x"},
		{TenantID: a, Kind: trigger.KindFallback, MatchType: trigger.MatchExact, FlowID: "menu", Priority: 3, Enabled: true},
		{TenantID: a, Kind: trigger.KindEscape, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true, Message: "Hasta pronto 👋"},
		{TenantID: a, Kind: trigger.KindLLM, Keyword: "pedir_encuesta", MatchType: trigger.MatchExact, FlowID: "encuesta", EventKind: trigger.EventKindSurvey, Enabled: true},
		{TenantID: a, Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchContains, EventKind: trigger.EventKindCart, Priority: 5, Enabled: true},
		{TenantID: a, Kind: trigger.KindEventStop, Keyword: "parar", MatchType: trigger.MatchExact, Enabled: true},
		{TenantID: a, Kind: trigger.KindKeyword, Keyword: "  MENÚ  del\tdía ", MatchType: trigger.MatchExact, FlowID: "menú/1", Priority: -4, Enabled: true},
		{TenantID: a, Kind: trigger.Kind("schedule"), Keyword: "x", MatchType: trigger.MatchType("regex"), FlowID: "f", EventKind: "carrrito"},
		{TenantID: a, Kind: trigger.KindKeyword, Keyword: "sin-match-type", MatchType: trigger.MatchType(""), FlowID: "f", Enabled: true},
		fullRule(a),
	}
	for _, in := range rules {
		out := insert(t, m, in)
		in.TriggerID = out.TriggerID
		if got := get(t, m, a, out.TriggerID); got != in {
			t.Errorf("Get de la regla %s %q = %+v, quería %+v", in.Kind, in.Keyword, got, in)
		}
		want.add(t, m, in)
	}
	requireState(t, "tras guardar una regla de cada clase", m, want)
}

// caseEmptyOptionals: los cinco campos opcionales vacíos vuelven vacíos (en Postgres viajan como
// NULL), y Enabled false y Priority 0 vuelven tal cual, no con el valor por defecto de la tabla.
func caseEmptyOptionals(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	out := insert(t, m, trigger.Rule{TenantID: m.TenantA, Kind: trigger.KindFallback, MatchType: trigger.MatchExact})
	got := get(t, m, m.TenantA, out.TriggerID)
	if got.Keyword != "" || got.FlowID != "" || got.Message != "" || got.SessionID != "" || got.EventKind != "" {
		t.Errorf("opcionales vacíos leídos como (%q, %q, %q, %q, %q); quería los cinco vacíos",
			got.Keyword, got.FlowID, got.Message, got.SessionID, got.EventKind)
	}
	if got.Enabled || got.Priority != 0 {
		t.Errorf("(Enabled, Priority) = (%v, %d), quería (false, 0): lo que se guardó", got.Enabled, got.Priority)
	}
	want.add(t, m, out)
	requireState(t, "tras el Insert", m, want)
}

// caseInsertTouchesNothingElse inserta en A reglas IGUALES a los testigos de A y de B (mismo
// kind, keyword y sesión): son filas nuevas, y las que había no se mueven.
func caseInsertTouchesNothingElse(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	for _, w := range slices.Concat(want.rules[m.TenantA], want.rules[m.TenantB]) {
		twin := w
		twin.TenantID, twin.TriggerID = m.TenantA, ""
		out := insert(t, m, twin)
		want.add(t, m, out)
		requireState(t, "tras insertar la gemela de "+w.TriggerID, m, want)
	}
}

func caseEmptyList(t *testing.T, m Montaje) {
	// El otro tenant SÍ tiene reglas: el vacío de A no es «la tabla está vacía».
	insert(t, m, fullRule(m.TenantB))
	got, err := m.Store.List(context.Background(), m.TenantA)
	if err != nil {
		t.Fatalf("List de un tenant sin reglas: error inesperado %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("List de un tenant sin reglas = %#v, quería un slice vacío no nil", got)
	}
}

func caseListSorted(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	for i, k := range []trigger.Kind{
		trigger.KindKeyword, trigger.KindFallback, trigger.KindEscape, trigger.KindLLM,
		trigger.KindEventStart, trigger.KindEventStop, trigger.KindKeyword, trigger.KindKeyword,
	} {
		r := trigger.Rule{TenantID: m.TenantA, Kind: k, Keyword: "k", MatchType: trigger.MatchExact, Priority: 9 - i, Enabled: i%2 == 0}
		if i >= 6 {
			r.SessionID = "session-x"
		}
		want.add(t, m, insert(t, m, r))
	}
	got := list(t, m, m.TenantA)
	if len(got) != 10 {
		t.Fatalf("List devolvió %d reglas, quería 10 (2 testigos + 8): todas, sin filtro de kind ni de sesión", len(got))
	}
	requireSorted(t, "List", got)
	requireState(t, "tras los Insert", m, want)
}

func caseListByKindFiltersKind(t *testing.T, m Montaje) {
	seedWitnesses(t, m)
	kinds := []trigger.Kind{
		trigger.KindKeyword, trigger.KindFallback, trigger.KindEscape,
		trigger.KindLLM, trigger.KindEventStart, trigger.KindEventStop,
	}
	// De cada kind, tantas reglas como su posición + 1: un filtro que se equivoque de kind no
	// acierta la cuenta por casualidad. Los testigos de A son una keyword y un escape de sesión.
	for i, k := range kinds {
		for range i + 1 {
			insert(t, m, trigger.Rule{TenantID: m.TenantA, Kind: k, Keyword: "k", MatchType: trigger.MatchExact, Enabled: true})
		}
	}
	for i, k := range kinds {
		wantCount := i + 1
		if k == trigger.KindKeyword {
			wantCount++ // el testigo global de A
		}
		got := listByKind(t, m, m.TenantA, "", k)
		if len(got) != wantCount {
			t.Errorf("ListByKind(%s) devolvió %d reglas, quería %d", k, len(got), wantCount)
		}
		for _, r := range got {
			if r.Kind != k || r.TenantID != m.TenantA {
				t.Errorf("ListByKind(%s) devolvió una regla %s del tenant %q", k, r.Kind, r.TenantID)
			}
		}
		requireSorted(t, "ListByKind("+string(k)+")", got)
	}
}

func caseListByKindSession(t *testing.T, m Montaje) {
	seedWitnesses(t, m)
	global := insert(t, m, trigger.Rule{TenantID: m.TenantA, Kind: trigger.KindEventStart, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "global", Enabled: true})
	inX := insert(t, m, trigger.Rule{TenantID: m.TenantA, Kind: trigger.KindEventStart, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "x", Enabled: true, SessionID: "session-x"})
	inY := insert(t, m, trigger.Rule{TenantID: m.TenantA, Kind: trigger.KindEventStart, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "y", SessionID: "session-y"})
	// La misma sesión en el otro tenant no cuenta.
	insert(t, m, trigger.Rule{TenantID: m.TenantB, Kind: trigger.KindEventStart, Keyword: "hola", MatchType: trigger.MatchExact, SessionID: "session-x"})

	requireIDs(t, "sesión session-x", listByKind(t, m, m.TenantA, "session-x", trigger.KindEventStart), global, inX)
	requireIDs(t, "sesión session-y", listByKind(t, m, m.TenantA, "session-y", trigger.KindEventStart), global, inY)
	requireIDs(t, "una sesión sin reglas propias", listByKind(t, m, m.TenantA, "session-z", trigger.KindEventStart), global)
}

func caseListByKindGlobalView(t *testing.T, m Montaje) {
	seedWitnesses(t, m)
	global := insert(t, m, trigger.Rule{TenantID: m.TenantA, Kind: trigger.KindEventStop, Keyword: "parar", MatchType: trigger.MatchExact, Enabled: true})
	insert(t, m, trigger.Rule{TenantID: m.TenantA, Kind: trigger.KindEventStop, Keyword: "parar", MatchType: trigger.MatchExact, Enabled: true, SessionID: "session-x"})
	requireIDs(t, "la vista global (sesión vacía)", listByKind(t, m, m.TenantA, "", trigger.KindEventStop), global)
}

func caseListByKindEmpty(t *testing.T, m Montaje) {
	seedWitnesses(t, m)
	// Hay un escape en A, pero acotado a otra sesión; y fallbacks, ninguno.
	for _, c := range []struct {
		session string
		kind    trigger.Kind
	}{{"", trigger.KindFallback}, {"", trigger.KindEscape}, {"session-z", trigger.KindEscape}} {
		got, err := m.Store.ListByKind(context.Background(), m.TenantA, c.session, c.kind)
		if err != nil {
			t.Fatalf("ListByKind(sesión %q, %s): error inesperado %v", c.session, c.kind, err)
		}
		if got == nil || len(got) != 0 {
			t.Errorf("ListByKind(sesión %q, %s) = %#v, quería un slice vacío no nil", c.session, c.kind, got)
		}
	}
}

func caseGetUnknown(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	got, err := m.Store.Get(context.Background(), m.TenantA, uuid.NewString())
	requireNotFound(t, "Get de un trigger_id que no existe", err)
	if got != (trigger.Rule{}) {
		t.Errorf("Get sin regla devolvió %+v, quería la regla cero", got)
	}
	requireState(t, "tras el Get fallido", m, want)
}

func caseGetOtherTenant(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	foreign := want.rules[m.TenantB][0]
	got, err := m.Store.Get(context.Background(), m.TenantA, foreign.TriggerID)
	requireNotFound(t, "Get de una regla de otro tenant", err)
	if got != (trigger.Rule{}) {
		t.Errorf("Get de una regla ajena devolvió %+v, quería la regla cero", got)
	}
	if own := get(t, m, m.TenantB, foreign.TriggerID); own != foreign {
		t.Errorf("su dueño lee %+v, quería %+v", own, foreign)
	}
}

func caseDeleteRemovesOne(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	// Tres reglas iguales: solo se va la del trigger_id pedido.
	twin := trigger.Rule{TenantID: m.TenantA, Kind: trigger.KindKeyword, Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: "carrito", Enabled: true}
	keptBefore, victim, keptAfter := insert(t, m, twin), insert(t, m, twin), insert(t, m, twin)
	want.add(t, m, keptBefore)
	want.add(t, m, keptAfter)
	if err := m.Store.Delete(context.Background(), m.TenantA, victim.TriggerID); err != nil {
		t.Fatalf("Delete: error inesperado %v", err)
	}
	_, err := m.Store.Get(context.Background(), m.TenantA, victim.TriggerID)
	requireNotFound(t, "Get tras el Delete", err)
	requireState(t, "tras el Delete", m, want)
}

func caseDeleteTwice(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	victim := insert(t, m, fullRule(m.TenantA))
	if err := m.Store.Delete(context.Background(), m.TenantA, victim.TriggerID); err != nil {
		t.Fatalf("primer Delete: error inesperado %v", err)
	}
	requireNotFound(t, "segundo Delete de la misma regla", m.Store.Delete(context.Background(), m.TenantA, victim.TriggerID))
	requireState(t, "tras los dos Delete", m, want)
}

func caseDeleteUnknown(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	requireNotFound(t, "Delete de un trigger_id que no existe", m.Store.Delete(context.Background(), m.TenantA, uuid.NewString()))
	requireState(t, "tras el Delete fallido", m, want)
}

func caseDeleteOtherTenant(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	for _, foreign := range want.rules[m.TenantB] {
		requireNotFound(t, "Delete de una regla de otro tenant", m.Store.Delete(context.Background(), m.TenantA, foreign.TriggerID))
	}
	requireState(t, "tras intentar borrar desde A las reglas de B", m, want)
}

// caseTenantIsolation: los dos tenants guardan la MISMA regla. Cada uno ve la suya por List, por
// ListByKind y por Get, y borrar la de A deja la de B donde estaba.
func caseTenantIsolation(t *testing.T, m Montaje) {
	want := seedWitnesses(t, m)
	same := trigger.Rule{Kind: trigger.KindEventStart, Keyword: "carrito", MatchType: trigger.MatchExact, EventKind: trigger.EventKindCart, Enabled: true}
	same.TenantID = m.TenantA
	inA := insert(t, m, same)
	same.TenantID = m.TenantB
	inB := insert(t, m, same)
	want.add(t, m, inA)
	want.add(t, m, inB)
	requireState(t, "tras guardar la misma regla en los dos tenants", m, want)

	requireIDs(t, "ListByKind de A", listByKind(t, m, m.TenantA, "", trigger.KindEventStart), inA)
	requireIDs(t, "ListByKind de B", listByKind(t, m, m.TenantB, "", trigger.KindEventStart), inB)
	for _, r := range list(t, m, m.TenantA) {
		if r.TenantID != m.TenantA {
			t.Errorf("List de A trae la regla %s del tenant %q", r.TriggerID, r.TenantID)
		}
	}

	if err := m.Store.Delete(context.Background(), m.TenantA, inA.TriggerID); err != nil {
		t.Fatalf("Delete en A: error inesperado %v", err)
	}
	want.remove(m.TenantA, inA.TriggerID)
	requireState(t, "tras borrar la de A", m, want)
}
