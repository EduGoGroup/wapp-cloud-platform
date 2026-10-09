// Package storehelpertest es la suite de contrato de la persistencia del motor de flujos: las 13
// interfaces de store (ConversationStore, DefinitionReader, DefinitionStore, SurveyResultStore,
// FlowEventStore, TenantContentReader, IntakeReader, IntakeWriter, IntakeStore,
// TenantSettingsReader, WelcomeStore, Repository y TenantContentVersioner) y los cuatro métodos
// que los dos adaptadores ofrecen fuera de ellas. Ningún código de producción lo importa: arrastra
// "testing".
//
// La corren las dos implementaciones: store.MemoryRepository en unitario
// (repository_memory_test.go) y store.PostgresRepository en los procesos de F9, con el arnés de
// testcontainers (test/procesos/flowstore_contrato_test.go).
//
// Nuevo: no tiene fichero viejo. Los casos salen del contrato de los puertos y de los 18 tests
// viejos de internal/flujos/store, leídos y NO portados (05 E-8). De ellos quedan FUERA los que no
// son del puerto sino del esquema (la forma de las columnas en information_schema, los CHECK
// `>= 0`, los backfills y el full-replay de las migraciones, el NULL crudo de una columna) y el
// render del motor sobre tenant_content, que es de `content`.
//
// Un fichero por tema; los de casos y de apoyo terminan en _contrato.go:
//   - contrato.go: la entrada. Port, Montaje, Contrato y la tabla de casos.
//   - fixtures_contrato.go: las claves, las siembras y los valores que comparten los casos.
//   - snapshot_contrato.go y compare_contrato.go: la marca de estado (TODO lo observable) y sus
//     comparaciones.
//   - conversation_contrato.go: Exists, Load, Save y Delete.
//   - definitions_contrato.go: InsertDefinition, GetDefinition, LatestDefinition, ListDefinitions.
//   - events_contrato.go: InsertFlowEvent, InsertResults y ListResults.
//   - tenant_content_contrato.go: Get/Upsert/List/DeleteTenantContent.
//   - versions_contrato.go: ReplaceTenantContentVersioned.
//   - intakes_contrato.go: UpsertIntake, GetOpenIntake, GetIntakeByEvent y MarkIntakeStatus.
//   - intake_items_contrato.go: ReplaceIntakeItems y ListIntakeItems.
//   - close_contrato.go: CloseIntake.
//   - settings_contrato.go: GetTenantSettings.
//   - welcome_contrato.go: TouchContact y MarkWelcomed.
//   - concurrency_contrato.go: las tres carreras (versionado, cierre, bienvenida).
//
// Para añadir un caso: escribe su función en el fichero de su tema y añade su fila a cases().
package storehelpertest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Port es TODO lo que una persistencia del motor de flujos ofrece: lo satisfacen
// *store.MemoryRepository y *store.PostgresRepository. Va junto porque son diez tablas vistas por
// trece puertas, y lo que la suite vigila es justamente que una puerta no estropee lo que lee otra.
type Port interface {
	store.ConversationStore
	store.DefinitionReader
	store.DefinitionStore
	store.SurveyResultStore
	store.FlowEventStore
	store.TenantContentReader
	store.IntakeReader
	store.IntakeWriter
	store.IntakeStore
	store.TenantSettingsReader
	store.WelcomeStore
	store.Repository
	store.TenantContentVersioner
	// ListDefinitions resume cada flujo del tenant (flow_id y última versión), por flow_id.
	ListDefinitions(ctx context.Context, tenantID string) ([]store.FlowSummary, error)
	// UpsertTenantContent crea o sustituye el blob de (tenant, ref) SIN archivar versión.
	UpsertTenantContent(ctx context.Context, tenantID, ref string, blob []byte) error
	// ListTenantContent da las cabeceras (ref y marcas de tiempo) de los blobs del tenant, por ref.
	ListTenantContent(ctx context.Context, tenantID string) ([]store.TenantContentSummary, error)
	// DeleteTenantContent borra el blob de (tenant, ref); ErrTenantContentNotFound si no existía.
	DeleteTenantContent(ctx context.Context, tenantID, ref string) error
}

// Los tipos del puerto que cruzan el Montaje, con nombre de la suite. Están aquí para el Montaje
// de test/procesos, que del paquete store solo puede nombrar los constructores `New…` (candado
// ProcessImports, regla 3b): sin estos alias no podría escribir la firma de un observador
// (precedente: fleethelpertest.TenantProfiles, hallazgo 75 de F3). Son alias, no tipos nuevos.
type (
	// FlowEvent es store.FlowEvent.
	FlowEvent = store.FlowEvent
	// SurveyResult es store.SurveyResult.
	SurveyResult = store.SurveyResult
	// ContentVersion es store.TenantContentVersion.
	ContentVersion = store.TenantContentVersion
	// Intake es store.Intake.
	Intake = store.Intake
	// IntakeItem es store.IntakeItem.
	IntakeItem = store.IntakeItem
	// Settings es store.TenantSettings.
	Settings = store.TenantSettings
	// WelcomeMark es store.WelcomeMark.
	WelcomeMark = store.WelcomeMark
)

// Montaje es lo que cada implementación entrega a la suite para UN caso: Contrato llama a nuevo
// una vez por caso y no limpia nada entre llamadas. Todos los campos son obligatorios.
//
// Los observadores existen porque el puerto no deja ver lo que escribe: no hay lectura de
// flow_events ni de tenant_content_versions, ListResults no trae el event_id, las dos lecturas de
// cabecera de una solicitud no traen la nota ni las solicitudes que no se saben pedir, y la
// bienvenida solo se ve al tocarla. En memoria salen de los miradores del gemelo; contra Postgres,
// de SQL directo.
type Montaje struct {
	// Store es la implementación bajo prueba.
	Store Port
	// TenantA y TenantB son dos tenants distintos, UUID bien formados, VACÍOS: sin conversaciones,
	// definiciones, efectos, respuestas, contenido, solicitudes, bienvenidas ni fila de
	// configuración. Con Postgres son filas reales de public.tenants (flow_state, flow_definitions
	// y conversation_welcomes los exigen por clave foránea).
	TenantA, TenantB string
	// NewEvent crea un evento conversacional del tenant y devuelve su id (un UUID): es el padre que
	// una solicitud y una respuesta de encuesta tienen que declarar, y el dueño al que puede
	// apuntar una conversación. Con Postgres es una fila real de public.conversation_events (las
	// tres columnas tienen clave foránea); en memoria basta un UUID nuevo. Cada llamada da uno
	// distinto, y la suite nunca le cuelga dos solicitudes al mismo (el índice único parcial
	// intakes_event_id_uidx lo prohíbe).
	NewEvent func(t *testing.T, tenantID string) (eventID string)
	// SetSettings deja al tenant s.TenantID con EXACTAMENTE esa configuración, como un INSERT (o
	// un UPDATE) de public.tenant_settings que nombra las diez columnas que el puerto lee: no
	// rellena nada con valores por defecto. Las duraciones son segundos enteros.
	SetSettings func(t *testing.T, s Settings)
	// FlowEvents devuelve los efectos guardados del tenant, en el orden en que se escribieron. Un
	// efecto que se guardó sin payload puede salir con el mapa vacío o nil: la suite no los
	// distingue. Los números del payload pueden salir como float64 (JSONB).
	FlowEvents func(t *testing.T, tenantID string) []FlowEvent
	// SurveyResults devuelve TODAS las respuestas de encuesta del tenant, en el orden en que se
	// escribieron, con su EventID y su CreatedAt.
	SurveyResults func(t *testing.T, tenantID string) []SurveyResult
	// ContentVersions devuelve las versiones archivadas de (tenant, ref), por número de versión.
	ContentVersions func(t *testing.T, tenantID, ref string) []ContentVersion
	// Intakes devuelve TODAS las solicitudes del tenant, en cualquier orden y en cualquier estado,
	// con la fila entera: los diez campos de la cabecera que el puerto enseña más CustomerNote.
	Intakes func(t *testing.T, tenantID string) []Intake
	// IntakeItems devuelve las líneas de la solicitud en el orden en que las ve el cliente
	// (added_at, id), con sus siete campos. Sin líneas, o con un id desconocido, ninguna.
	IntakeItems func(t *testing.T, intakeID string) []IntakeItem
	// Welcome devuelve la fila de la bienvenida de esa conversación. Sin fila, la marca cero (una
	// fila siempre tiene LastIncomingAt).
	Welcome func(t *testing.T, tenantID, sessionID, contactID string) WelcomeMark
	// Now devuelve el instante del reloj con el que la implementación fecha lo que escribe: en
	// memoria el reloj inyectado con SetClock; en Postgres, el de la base. Tiene que ser posterior
	// al 2026-09-01: los instantes que la suite pone a mano (los de la bienvenida) son de agosto de
	// 2026.
	Now func(t *testing.T) time.Time
	// Advance deja pasar ese reloj: promete que lo que se escriba después de la llamada lleva un
	// instante ESTRICTAMENTE posterior a lo escrito antes de ella. En memoria adelanta el reloj
	// inyectado; contra Postgres espera, preguntándoselo a la base, a que su reloj pase del
	// microsegundo en que estaba. La suite no duerme ni mira el reloj de pared.
	Advance func(t *testing.T)
}

// Contrato ejecuta las promesas de la persistencia del motor de flujos contra la implementación
// que devuelve nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta
// nada: un caso que no aplica a una implementación es un defecto del puerto, no de la suite.
//
// 🔴 LA MARCA DE ESTADO ES TODO LO OBSERVABLE (hallazgo 35 de F1). Antes y después de cada
// escritura la suite saca la foto ENTERA de los dos tenants (snapshot_contrato.go): cada
// conversación vigilada con sus once columnas, cada versión de cada definición, cada efecto, cada
// respuesta, cada blob con sus marcas y sus versiones, cada solicitud con sus once campos y cada
// línea con los siete suyos, la configuración y cada bienvenida vigilada. Un rechazo tiene que
// dejarla IDÉNTICA; una escritura, idéntica salvo la fila que el caso dice que cambia, y de esa
// fila el caso afirma TODAS las columnas. Y cada caso que escribe siembra testigos —otra fila del
// mismo tenant y una del otro, que difieren de la tocada en UNA sola componente de la clave— y
// comprueba al final que ninguno se movió: es lo que mata al mutante que le quita un predicado a
// un WHERE.
//
// Lo que la suite NO afirma, a propósito:
//
//   - El byte a byte de un JSON: Postgres guarda vars, definiciones, payloads y blobs en JSONB,
//     que reordena las claves y normaliza los números. Se compara su contenido.
//   - Lo que solo cabe en el gemelo: la solicitud sin evento padre (la fila legada pre-0054) y dos
//     solicitudes con el mismo evento (lo prohíben el NOT NULL y el índice único de la base), los
//     ids que no son UUID y el instante propio que una fila le trae a una escritura. Lo cubre el
//     test propio de MemoryRepository.
//   - Lo que solo cabe en la base: los CHECK, las claves foráneas y los valores por defecto de las
//     columnas. Lo cubren los procesos de F9.
//   - FlowSummary.CreatedAt (el gemelo no lo rastrea), el EventID que devuelve ListResults (el
//     adaptador Postgres no lo lee) y el texto de ErrDefinitionNotFound más allá de `flow=<id>`
//     (el gemelo solo añade `version=` si el flujo existe).
//   - El valor concreto de una fecha que pone la implementación: solo que cae entre dos lecturas
//     de Now, y cuándo avanza y cuándo no.
//   - Que una lista vacía sea nil o no.
//
// Los instantes se comparan con Equal: Postgres guarda microsegundos y devuelve la zona de la
// sesión. Los que pone la suite son segundos enteros.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("storehelpertest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
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

// cases es la tabla de la suite: un caso, una función.
func cases() []contractCase {
	return []contractCase{
		// El estado conversacional (conversation_contrato.go).
		{"Conversation_UnknownKey_NotFoundWithoutError", caseConversationUnknown},
		{"Save_RoundTripsEveryColumnAndStampsUpdatedAt", caseSaveRoundTrip},
		{"Save_Upsert_ReplacesTheWholeRow", caseSaveUpsert},
		{"Save_EventPointers_OnRelayAndOff", caseSaveEventPointers},
		{"Save_TerminalNodeAndNilVars_RoundTrip", caseSaveTerminalAndNilVars},
		{"Save_Load_NeverShareMemoryWithTheCaller", caseSaveLoadCopies},
		{"Delete_RemovesOnlyThatKey_Idempotent", caseDelete},
		// Las definiciones (definitions_contrato.go).
		{"InsertDefinition_NumbersPerTenantAndFlowFromOne", caseInsertDefinitionNumbers},
		{"GetDefinition_ExactVersion_PublishingNeverRewritesAnOlderOne", caseGetDefinitionExact},
		{"GetDefinition_UnknownFlowVersionOrTenant_ErrDefinitionNotFound", caseGetDefinitionNotFound},
		{"LatestDefinition_HighestVersion_UnknownIsErrDefinitionNotFound", caseLatestDefinition},
		{"ListDefinitions_OneRowPerFlowOrderedByFlowID", caseListDefinitions},
		// Los efectos y las respuestas de encuesta (events_contrato.go).
		{"InsertFlowEvent_AppendsInOrderWithEveryColumn", caseInsertFlowEvent},
		{"InsertFlowEvent_NilPayloadIsEmpty_PayloadIsCopied", caseInsertFlowEventPayload},
		{"InsertResults_Empty_NoOp", caseInsertResultsEmpty},
		{"InsertResults_AppendsTheBatchInOrderWithEveryColumn", caseInsertResults},
		{"ListResults_ChronologicalScopedToTenantContactAndFlow", caseListResults},
		// El contenido por tenant (tenant_content_contrato.go).
		{"GetTenantContent_Unknown_ErrTenantContentNotFound", caseGetContentNotFound},
		{"UpsertTenantContent_CreatesThenReplacesKeepingCreatedAt", caseUpsertContent},
		{"GetTenantContent_ReturnsACopy", caseGetContentCopy},
		{"ListTenantContent_OrderedByRefScopedToTenant", caseListContent},
		{"DeleteTenantContent_RemovesOnlyThatRef_SecondTimeNotFound", caseDeleteContent},
		// El versionado del contenido (versions_contrato.go).
		{"ReplaceVersioned_FirstWrite_ArchivesNothing", caseReplaceFirstWrite},
		{"ReplaceVersioned_ArchivesTheOldBlobAndNumbersFromOne", caseReplaceArchives},
		{"ReplaceVersioned_OverAPlainUpsert_ArchivesIt", caseReplaceOverUpsert},
		{"ReplaceVersioned_InvalidSource_ErrInvalidVersionSourceWritesNothing", caseReplaceInvalidSource},
		{"ReplaceVersioned_NumbersPerTenantAndRef", caseReplaceNumbersPerRef},
		{"ReplaceVersioned_AfterDelete_ArchivesNothingAndKeepsNumbering", caseReplaceAfterDelete},
		// Las solicitudes (intakes_contrato.go).
		{"UpsertIntake_CreatesWithEveryColumn", caseUpsertIntakeCreates},
		{"UpsertIntake_Update_KeepsCreatedAtDeclaredEventAndNote", caseUpsertIntakeUpdates},
		{"GetOpenIntake_OnlyOpenScopedToTenantAndContact", caseGetOpenIntakeScope},
		{"GetOpenIntake_SeveralOpen_NewestWins", caseGetOpenIntakeNewest},
		{"GetIntakeByEvent_IgnoresStatus_ScopedToTenant", caseGetIntakeByEvent},
		{"GetIntakeByEvent_UnknownEmptyOrMalformed_NotFoundWithoutError", caseGetIntakeByEventNotFound},
		{"MarkIntakeStatus_SetsStatusAndTotal_RefreshesUpdatedAt", caseMarkIntakeStatus},
		{"MarkIntakeStatus_UnknownID_NoOp", caseMarkIntakeStatusUnknown},
		// Las líneas de una solicitud (intake_items_contrato.go).
		{"ReplaceIntakeItems_MirrorsTheSetInOrder", caseReplaceItemsMirrors},
		{"ReplaceIntakeItems_Empty_DeletesCustomerLines", caseReplaceItemsEmpty},
		{"ReplaceIntakeItems_KeepsPlatformLines", caseReplaceItemsPlatform},
		{"ListIntakeItems_NoLinesIsEmpty_MalformedIDIsAnError", caseListItems},
		// El cierre (close_contrato.go).
		{"CloseIntake_ClosesTheOpenOne_ReplacingItsLines", caseCloseOpen},
		{"CloseIntake_NoOpenOne_CreatesAClosedRow", caseCloseCreates},
		{"CloseIntake_IgnoresRowsThatAreNotOpen", caseCloseIgnoresNonOpen},
		{"CloseIntake_SeveralOpen_ClosesTheNewest", caseCloseNewest},
		{"CloseIntake_EmptyItems_KeepsOnlyPlatformLines", caseCloseEmptyItems},
		{"CloseIntake_AfterClose_NeverRewritesTheClosedOne", caseCloseAfterClose},
		// La configuración (settings_contrato.go).
		{"GetTenantSettings_NoRow_PlatformDefaults", caseSettingsDefaults},
		{"GetTenantSettings_RowWins_ZerosIncluded", caseSettingsZeros},
		{"GetTenantSettings_RowValues_ReadVerbatim", caseSettingsValues},
		// La bienvenida única (welcome_contrato.go).
		{"TouchContact_FirstTime_ZeroMarkAndCreatesTheRow", caseTouchFirstTime},
		{"TouchContact_ReturnsThePreviousState", caseTouchPrevious},
		{"TouchContact_NeverTouchesWelcomedAt", caseTouchKeepsWelcomed},
		{"TouchContact_OnlyThatKey", caseTouchKeyIsolation},
		{"MarkWelcomed_MatchingWitness_SealsOnlyWelcomedAt", caseMarkMatching},
		{"MarkWelcomed_StaleWitness_FalseWritesNothing", caseMarkStale},
		{"MarkWelcomed_SecondWelcome_ComparesTheCurrentMark", caseMarkSecond},
		{"MarkWelcomed_NoRow_FalseAndDoesNotCreateIt", caseMarkNoRow},
		{"MarkWelcomed_OnlyThatKey", caseMarkKeyIsolation},
		// Las carreras (concurrency_contrato.go).
		{"ReplaceVersioned_Concurrent_NumbersWithoutGapsAndLosesNoBlob", caseReplaceConcurrent},
		{"CloseIntake_Concurrent_OnlyOneClosesTheOpenOne", caseCloseConcurrent},
		{"MarkWelcomed_Concurrent_OnlyOneWins", caseMarkConcurrent},
	}
}

// handSetNotAfter es la cota que Montaje.Now tiene que superar: los instantes que la suite pone a
// mano (los de la bienvenida) son anteriores.
var handSetNotAfter = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	requireMontajeFields(t, m)
	if now := m.Now(t); !now.After(handSetNotAfter) {
		t.Fatalf("Montaje.Now = %v; tiene que ser posterior a %v", now, handSetNotAfter)
	}
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		requireEmptyTenant(t, m, tenant)
	}
}

// requireMontajeFields exige que el Montaje traiga todos sus campos y dos tenants distintos.
func requireMontajeFields(t *testing.T, m Montaje) {
	t.Helper()
	missing := map[string]bool{
		"Store":           m.Store == nil,
		"NewEvent":        m.NewEvent == nil,
		"SetSettings":     m.SetSettings == nil,
		"FlowEvents":      m.FlowEvents == nil,
		"SurveyResults":   m.SurveyResults == nil,
		"ContentVersions": m.ContentVersions == nil,
		"Intakes":         m.Intakes == nil,
		"IntakeItems":     m.IntakeItems == nil,
		"Welcome":         m.Welcome == nil,
		"Now":             m.Now == nil,
		"Advance":         m.Advance == nil,
	}
	for field, isNil := range missing {
		if isNil {
			t.Fatalf("Montaje.%s es nil: todos los campos son obligatorios", field)
		}
	}
	if m.TenantA == m.TenantB {
		t.Fatalf("Montaje: TenantA y TenantB deben ser distintos y son %q", m.TenantA)
	}
}

// requireEmptyTenant exige que el tenant sea un UUID bien formado y llegue vacío y sin fila de
// configuración.
func requireEmptyTenant(t *testing.T, m Montaje, tenant string) {
	t.Helper()
	if _, err := uuid.Parse(tenant); err != nil {
		t.Fatalf("Montaje: el tenant %q no es un UUID bien formado: %v", tenant, err)
	}
	mark := takeTenant(t, m, tenant)
	if n := len(mark.definitions) + len(mark.flowEvents) + len(mark.surveyResults) +
		len(mark.content) + len(mark.intakes); n != 0 {
		t.Fatalf("Montaje: el tenant %q tiene que venir vacío y trae %d cosas: %+v", tenant, n, mark)
	}
	if !sameSettings(mark.settings, store.DefaultTenantSettings(tenant)) {
		t.Fatalf("Montaje: el tenant %q tiene que venir sin fila de configuración y trae %+v", tenant, mark.settings)
	}
}
