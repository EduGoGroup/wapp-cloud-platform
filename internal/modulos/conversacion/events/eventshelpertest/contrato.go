// Package eventshelpertest es el doble en memoria del almacén del evento conversacional (Store) y
// la suite de contrato de ese almacén (Contrato). Ningún código de producción lo importa: arrastra
// "testing".
//
// La suite la corren las dos implementaciones: el doble de este paquete en unitario
// (store_test.go) y events.Store contra Postgres en los procesos de F9, con el arnés de
// testcontainers (test/procesos/events_contrato_test.go).
//
// Nuevo: no tiene fichero viejo. Los casos salen del contrato de events.Store y de los 14 ficheros
// de test viejos de internal/flujos/events, leídos y NO portados (05 E-8); los de integración
// (store_integration_test.go, store_list_integration_test.go, content_integration_test.go,
// get_for_tenant_integration_test.go) son el guion. De ellos quedan FUERA los que no son del
// almacén sino del esquema (los CHECK de grado, de rol y de origen de la 0051, que la base impone
// a quien escriba por fuera de las cinco puertas) y los que cruzan a otros dominios (la siembra de
// features del Plan 040 y la escena de la solicitud huérfana con sus líneas).
//
// Un fichero por tema; los de casos y de apoyo terminan en _contrato.go:
//   - contrato.go: la entrada. Port, Montaje, Contrato y la tabla de casos.
//   - fixtures_contrato.go: las conversaciones, las siembras y los testigos que comparten los casos.
//   - snapshot_contrato.go: la marca de estado (TODO lo observable) y sus comparaciones.
//   - lifecycle_contrato.go: CreateEvent, GetAliveByKind, GetEventForTenant, ListAlive,
//     TransitionEvent, Touch e IsSuspended.
//   - rescuable_contrato.go: ListRescuable.
//   - list_contrato.go: ListEvents.
//   - append_contrato.go: las cinco puertas de escritura del historial.
//   - thread_contrato.go: ListThread y ListPastedByOwner.
//   - thread_level_one_contrato.go: el texto de las entradas de nivel 1 en el hilo (ver abajo).
//   - concurrency_contrato.go: las cuatro carreras.
//
// Para añadir un caso: escribe su función en el fichero de su tema y añade su fila a cases().
package eventshelpertest

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// Port es TODO lo que el almacén del evento ofrece: lo satisfacen *events.Store y el doble de este
// paquete. Va junto porque son dos tablas vistas por dieciséis puertas, y lo que la suite vigila
// es justamente que una puerta no estropee lo que lee otra. Los puertos estrechos de sus
// consumidores (events.RescuableLister, events.SummaryAppender, y en el runtime EventStore,
// ThreadReader y DecisionAppender) son subconjuntos de este.
type Port interface {
	// El ciclo de vida (public.conversation_events).
	CreateEvent(ctx context.Context, in events.NewEvent) (events.Event, error)
	GetAliveByKind(ctx context.Context, tenantID, sessionID, contactID, kind string) (events.Event, bool, error)
	GetEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error)
	ListAlive(ctx context.Context, tenantID, sessionID, contactID string) ([]events.Event, error)
	TransitionEvent(ctx context.Context, eventID string, to events.Status) error
	Touch(ctx context.Context, eventID string) error
	IsSuspended(e events.Event, ttl time.Duration) bool
	// Los rescatables y el listado del dueño.
	ListRescuable(ctx context.Context, tenantID, sessionID, contactID string, limit int) ([]events.Rescuable, error)
	ListEvents(ctx context.Context, tenantID string, f events.ListFilter) (events.EventPage, error)
	// El historial (public.conversation_event_messages).
	AppendSummary(ctx context.Context, eventID string, body json.RawMessage) (int, error)
	AppendDecision(ctx context.Context, eventID string, payload []byte) error
	AppendMessage(ctx context.Context, eventID string, role events.Role, body string) (int, error)
	AppendOutOfTurnMessage(ctx context.Context, eventID string, body string) (int, error)
	AppendPastedMessage(ctx context.Context, eventID string, body string) (int, error)
	ListThread(ctx context.Context, eventID string, limit int) ([]events.ThreadEntry, error)
	ListPastedByOwner(ctx context.Context, eventID string) ([]string, error)
}

// Event es events.Event, con nombre de la suite. Está aquí para el Montaje de test/procesos, que
// del paquete events solo puede nombrar los constructores `New…` (candado ProcessImports, regla
// 3b): sin el alias no podría escribir la firma del observador Events.
type Event = events.Event

// ClockOption es events.WithClock con nombre de la suite, por el mismo candado: el Montaje de
// test/procesos construye el adaptador con events.NewStore(db, cipher, ClockOption(reloj)).
func ClockOption(now func() time.Time) events.Option { return events.WithClock(now) }

// Montaje es lo que cada implementación entrega a la suite para UN caso: Contrato llama a nuevo
// una vez por caso y no limpia nada entre llamadas. Todos los campos son obligatorios.
//
// Los observadores y las siembras existen porque el almacén no deja ver ni tocar todo lo que la
// suite necesita: no hay lectura de todos los eventos de un tenant en cualquier estado, el
// historial solo se lee ya descifrado y sin su origen, y el contenido del evento y el TTL de
// inactividad son de otras tablas. En memoria salen de los métodos del doble; contra Postgres, de
// SQL directo.
type Montaje struct {
	// Store es la implementación bajo prueba, construida CON cipher y con el reloj de Now/Advance.
	Store Port
	// NoCipher es la misma implementación sobre los MISMOS datos y el mismo reloj, construida sin
	// cipher: lo que Store escribe, NoCipher lo ve, y al revés.
	NoCipher Port
	// TenantA y TenantB son dos tenants distintos, UUID bien formados, sin ningún evento y sin fila
	// de configuración. Con Postgres son filas reales de public.tenants (conversation_events las
	// exige por clave foránea).
	TenantA, TenantB string
	// Events devuelve TODOS los eventos del tenant, en cualquier estado y en cualquier orden, con
	// sus doce columnas. closed_at en NULL sale a cero.
	Events func(t *testing.T, tenantID string) []Event
	// Entries devuelve el historial del evento, por seq, sin descifrar nada: las tres marcas de
	// cada fila, su payload (nil si es NULL) y si lleva el sobre cifrado completo. Sin entradas, o
	// con un id desconocido, ninguna.
	Entries func(t *testing.T, eventID string) []Entry
	// SetContent deja al evento con contenido en ese estado (ContentAlive, ContentSettled o
	// ContentDiscarded) en la vista public.event_content, y devuelve su ref. Llamarla otra vez
	// sobre el mismo evento cambia el estado y conserva el ref. Con Postgres es una solicitud real
	// que declara ese event_id.
	SetContent func(t *testing.T, eventID, state string) (ref string)
	// SetInactivityTTL deja al tenant con ESE event_inactivity_ttl_seconds, creando su fila de
	// public.tenant_settings si no la tenía.
	SetInactivityTTL func(t *testing.T, tenantID string, seconds int)
	// CorruptEntry estropea el sobre cifrado de la entrada seq del evento, de modo que ya no se
	// pueda descifrar. La entrada tiene que existir y ser de nivel 2.
	CorruptEntry func(t *testing.T, eventID string, seq int)
	// Now devuelve el instante del reloj INYECTADO en Store y en NoCipher. Tiene que ser un segundo
	// entero (Postgres guarda microsegundos) y posterior al 2026-01-01.
	Now func(t *testing.T) time.Time
	// Advance adelanta ese reloj la duración dada (la suite solo pide segundos enteros). Nada más
	// lo mueve: la suite no duerme ni mira el reloj de pared.
	Advance func(t *testing.T, d time.Duration)
}

// Contrato ejecuta las promesas del almacén del evento contra la implementación que devuelve
// nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta nada: un caso
// que no aplica a una implementación es un defecto del puerto, no de la suite.
//
// 🔴 LA MARCA DE ESTADO ES TODO LO OBSERVABLE (hallazgo 35 de F1). Antes y después de cada
// escritura la suite saca la foto ENTERA de los dos tenants (snapshot_contrato.go): cada evento
// con sus doce columnas y cada entrada de su historial con sus cinco marcas. Un rechazo tiene que
// dejarla IDÉNTICA; una escritura, idéntica salvo los eventos que el caso dice que cambian, y de
// esos el caso afirma TODAS las columnas. Y cada caso que escribe siembra testigos —eventos que
// difieren del tocado en UNA sola componente de la clave— y comprueba que ninguno se movió: es lo
// que mata al mutante que le quita un predicado a un WHERE.
//
// Lo que la suite NO afirma, a propósito:
//
//   - El byte a byte de un payload: Postgres lo guarda en JSONB, que reordena las claves y
//     normaliza los números. Se compara su contenido.
//   - El cifrado en sí (que body_enc no contenga el texto, la KEK que envolvió la fila): es de
//     internal/platform/crypto. Aquí se afirma que la fila va SELLADA, con payload NULL, y que el
//     texto vuelve entero por ListThread.
//   - Lo que solo cabe en la base: un tenant, un contacto o un evento que no es UUID (allí es un
//     error de sintaxis; el doble lo acepta), los CHECK y las claves foráneas vistos desde fuera
//     de las puertas. Lo cubren los procesos de F9.
//   - El texto de un error más allá del centinela y del prefijo que el contrato fija.
//   - Que una lista vacía sea nil o no.
//
// Los instantes se comparan con Equal: Postgres devuelve la zona de la sesión.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("eventshelpertest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
	}
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateMontaje(t, m)
			c.run(t, m)
		})
	}
}

// contractCase es una promesa del almacén: su nombre (el del t.Run) y la función que la afirma.
type contractCase struct {
	name string
	run  func(t *testing.T, m Montaje)
}

// cases es la tabla de la suite: un caso, una función.
func cases() []contractCase {
	base := []contractCase{
		// El ciclo de vida (lifecycle_contrato.go).
		{"CreateEvent_BornOpenWithEveryColumnFromTheClock", caseCreateBornOpen},
		{"CreateEvent_SecondAliveOfSameKind_ErrAliveExistsWritesNothing", caseCreateSecondAlive},
		{"CreateEvent_AliveSlotIsPerTenantSessionContactAndKind", caseCreateSlotIsTheWholeKey},
		{"CreateEvent_AfterTerminal_TheKindIsFreeAgain", caseCreateAfterTerminal},
		{"GetAliveByKind_OnlyTheOpenOneOfThatConversationAndKind", caseGetAliveByKind},
		{"GetEventForTenant_AnyStatus_ScopedToTenant", caseGetForTenant},
		{"GetEventForTenant_UnknownForeignOrMalformed_ErrEventNotFound", caseGetForTenantNotFound},
		{"ListAlive_OpenOnlyByBirth_ScopedToConversation", caseListAlive},
		{"TransitionEvent_ClosesOrCancels_SealsOnlyStatusAndClosedAt", caseTransition},
		{"TransitionEvent_NonTerminalTarget_ErrNotTerminalWritesNothing", caseTransitionNotTerminal},
		{"TransitionEvent_AlreadyTerminalOrUnknown_ErrNotOpenWritesNothing", caseTransitionNotOpen},
		{"Touch_RefreshesOnlyLastActivity_AlsoOnATerminalEvent", caseTouch},
		{"Touch_UnknownID_ErrEventMissingWritesNothing", caseTouchMissing},
		{"IsSuspended_UsesTheInjectedClock_WritesNothing", caseIsSuspended},
		// Los rescatables (rescuable_contrato.go).
		{"ListRescuable_ByLastActivityDesc_WithDerivedContent", caseRescuableOrderAndContent},
		{"ListRescuable_SettledOrDiscardedContent_NotOfferedButStillAlive", caseRescuableDeadContent},
		{"ListRescuable_StaleIsAMarkNotAFilter", caseRescuableStale},
		{"ListRescuable_StaleFollowsTheTenantTTL_ZeroNeverExpires", caseRescuableTTL},
		{"ListRescuable_LimitCapsTheBatch_NonPositiveIsNoCap", caseRescuableLimit},
		{"ListRescuable_ScopedToConversation", caseRescuableScope},
		// El listado del dueño (list_contrato.go).
		{"ListEvents_Defaults_OpenAnyFirstPageOfFifty", caseListDefaults},
		{"ListEvents_StatusKindAndContactFilters", caseListStatusKindContact},
		{"ListEvents_KindsNilIsNoFilter_EmptyIsNone", caseListKinds},
		{"ListEvents_ContentAnyNoneAlive_DeadContentInNone", caseListContent},
		{"ListEvents_StaleFiltersOverTheMark", caseListStale},
		{"ListEvents_Pagination_TotalIsOfTheWholeFilter", caseListPagination},
		{"ListEvents_ScopedToTenant", caseListTenantScope},
		// La escritura del historial (append_contrato.go).
		{"Append_NumbersFromOneWithoutGapsPerEvent", caseAppendNumbering},
		{"AppendSummary_LevelOneInClear_SystemRole", caseAppendSummary},
		{"AppendDecision_LevelOneInClear_ClientRole", caseAppendDecision},
		{"AppendSummaryAndDecision_NotJSON_ErrSummaryNotJSONWritesNothing", caseAppendNotJSON},
		{"AppendMessage_LevelTwoSealed_WithTheGivenRole", caseAppendMessage},
		{"AppendMessage_UnknownRole_ErrInvalidRoleWritesNothing", caseAppendInvalidRole},
		{"AppendOutOfTurnMessage_SealedBusinessOutOfTurn", caseAppendOutOfTurn},
		{"AppendPastedMessage_SealedClientOwnerPasted", caseAppendPasted},
		{"Append_WithoutCipher_TextDoorsFailStructureDoorsWork", caseAppendWithoutCipher},
		{"Append_UnknownEvent_FailsAndWritesNothing", caseAppendUnknownEvent},
		// La lectura del hilo (thread_contrato.go).
		{"ListThread_ChronologicalDecryptedWithKindAndRole", caseThreadReads},
		{"ListThread_LimitKeepsTheMostRecent", caseThreadLimit},
		{"ListThread_EmptyIDNonPositiveLimitOrNoEntries_NothingWithoutError", caseThreadNothing},
		{"ListThread_UnreadableLevelOnePayload_EmptyTextNoError", caseThreadUnreadablePayload},
		{"ListThread_UndecryptableEntry_AbortsTheRead", caseThreadUndecryptable},
		{"ListPastedByOwner_OnlyOwnerPastedOfThatEventInOrder", casePasted},
		{"ListPastedByOwner_UndecryptableEntry_AbortsTheRead", casePastedUndecryptable},
		{"Thread_WithoutCipher_ErrNoCipherOnBothReads", caseThreadWithoutCipher},
		// Las carreras (concurrency_contrato.go).
		{"CreateEvent_Concurrent_OnlyOneAlivePerKind", caseCreateConcurrent},
		{"TransitionEvent_Concurrent_OnlyOneWins", caseTransitionConcurrent},
		{"TouchAndTransition_Concurrent_NeitherOverwritesTheOther", caseTouchTransitionConcurrent},
		{"Append_Concurrent_NumbersWithoutGapsOrDuplicates", caseAppendConcurrent},
	}
	// El texto de las entradas de nivel 1 (thread_level_one_contrato.go).
	return slices.Concat(base, levelOneThreadCases())
}

// clockNotBefore es la cota que Montaje.Now tiene que superar.
var clockNotBefore = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	missing := map[string]bool{
		"Store":            m.Store == nil,
		"NoCipher":         m.NoCipher == nil,
		"Events":           m.Events == nil,
		"Entries":          m.Entries == nil,
		"SetContent":       m.SetContent == nil,
		"SetInactivityTTL": m.SetInactivityTTL == nil,
		"CorruptEntry":     m.CorruptEntry == nil,
		"Now":              m.Now == nil,
		"Advance":          m.Advance == nil,
	}
	for field, isNil := range missing {
		if isNil {
			t.Fatalf("Montaje.%s es nil: todos los campos son obligatorios", field)
		}
	}
	if m.TenantA == m.TenantB {
		t.Fatalf("Montaje: TenantA y TenantB deben ser distintos y son %q", m.TenantA)
	}
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		if _, err := uuid.Parse(tenant); err != nil {
			t.Fatalf("Montaje: el tenant %q no es un UUID bien formado: %v", tenant, err)
		}
		if evs := m.Events(t, tenant); len(evs) != 0 {
			t.Fatalf("Montaje: el tenant %q tiene que venir sin eventos y trae %d", tenant, len(evs))
		}
	}
	now := m.Now(t)
	if !now.After(clockNotBefore) || now.Nanosecond() != 0 {
		t.Fatalf("Montaje.Now = %v; tiene que ser un segundo entero posterior a %v", now, clockNotBefore)
	}
}
