// Package intakeshelpertest es la suite de contrato de la persistencia de solicitudes: el puerto
// intakes.Store y los que la misma implementación satisface (RevisionWriter, DepositStore,
// ExpiryStore, SettingsReader y las dos lecturas sueltas). Ningún código de producción lo importa:
// arrastra "testing".
//
// La corren las dos implementaciones: intakes.MemoryStore en unitario (memory_test.go) e
// intakes.Postgres en los procesos de F9, con el arnés de testcontainers (test/procesos/).
//
// Nuevo: no tiene fichero viejo. Los casos salen del contrato de los puertos y de los tests de
// integración viejos, leídos y NO portados (05 E-8): postgres_integration_test.go (sus 16),
// revisions_, shipping_, edit_, revalidate_, discard_, deposit_, aprobadas_ y literal_
// integration_test.go, event_pair_pg_test.go y vencimiento_cas_test.go. De ellos quedan FUERA los
// que no son del puerto sino de la tabla: el CHECK de `kind`, la FK de la revisión, el índice único
// de `_shipping`, el índice parcial de la seña y el cifrado del literal.
//
// Un fichero por tema; los de casos y de apoyo terminan en _contrato.go:
//   - contrato.go: la entrada. Montaje, Port, Seed, Contrato y la tabla de casos.
//   - fixtures_contrato.go: las siembras y los valores que comparten los casos.
//   - snapshot_contrato.go: la marca de estado (la fila ENTERA) y sus aserciones.
//   - read_contrato.go: Get, List y ListDetails.
//   - status_contrato.go: UpdateStatus.
//   - revisions_contrato.go: InsertRevision y el historial aprobado.
//   - shipping_contrato.go: EnsureShippingLine y la configuración del tenant.
//   - edit_contrato.go: ReplaceItems y ApplyRevalidation.
//   - discard_contrato.go: Discard y AbandonByEvent.
//   - reminders_contrato.go: los dos recordatorios.
//
// Para añadir un caso: escribe su función en el fichero de su tema y añade su fila a cases().
package intakeshelpertest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Port es TODO lo que una persistencia de solicitudes ofrece: lo satisfacen *intakes.MemoryStore y
// *intakes.Postgres. Va junto porque es UNA tabla vista por varias puertas, y lo que la suite
// vigila es justamente que una puerta no estropee lo que lee otra.
type Port interface {
	intakes.Store
	intakes.RevisionWriter
	intakes.DepositStore
	intakes.ExpiryStore
	intakes.SettingsReader
	// ShippingZones lee las zonas de envío del tenant (lo consume la etapa `match`).
	ShippingZones(ctx context.Context, tenantID string) ([]intakes.ShippingZone, error)
	// ApprovedRenderedTexts lee el historial de cotizaciones aprobadas (aprobadas.go).
	ApprovedRenderedTexts(ctx context.Context, tenantID string, limit int) ([]string, error)
}

// Seed es una solicitud que la suite pide sembrar. El puerto no tiene alta —las solicitudes las
// pare el proyector del carrito o el pipeline, por SQL—, así que la siembra es del Montaje.
type Seed struct {
	// Intake es la cabecera ENTERA, y se guarda tal cual: ID (un UUID que pone la suite),
	// ContactID, SessionID, Total, CreatedAt, UpdatedAt, CustomerNote y las tres fechas de los
	// recordatorios (cero = NULL). Status es la clave ALMACENADA, sin normalizar: puede ser la
	// legada `closed`.
	Intake intakes.Intake
	// Items son sus líneas, con su AddedAt (distinto en cada una y creciente: es su orden).
	Items []intakes.Item
	// EventStatus es el estado del evento conversacional PROPIO que la solicitud declara como
	// padre: `open`, `closed` o `cancelled`. Nunca viene vacío: desde la migración 0054 no se puede
	// insertar una solicitud sin padre, así que la fila legada sin ligadura queda fuera de la suite
	// (la cubre el test propio de MemoryStore).
	EventStatus string
}

// Montaje es lo que cada implementación entrega a la suite para UN caso: Contrato llama a nuevo
// una vez por caso y no limpia nada entre llamadas. Todos los campos son obligatorios.
type Montaje struct {
	// Store es la implementación bajo prueba.
	Store Port
	// TenantA y TenantB son dos tenants distintos, UUID bien formados, SIN solicitudes, sin zonas
	// de envío y sin configuración de la seña. Con Postgres son filas reales de public.tenants.
	TenantA, TenantB string
	// Seed siembra la solicitud en el tenant, crea su evento padre en el estado pedido y devuelve
	// el id de ese evento (un UUID). Si no puede, falla el test.
	Seed func(t *testing.T, tenantID string, s Seed) (eventID string)
	// SetShippingZones deja al tenant con EXACTAMENTE esas zonas (ninguna = sin zonas), como un
	// UPDATE de tenant_settings.shipping_zones.
	SetShippingZones func(t *testing.T, tenantID string, zones ...intakes.ShippingZone)
	// SetDepositTemplate deja al tenant con esa plantilla de seña y ese plazo en días, como un
	// UPDATE de tenant_settings.deposit_template / deposit_due_days.
	SetDepositTemplate func(t *testing.T, tenantID, template string, dueDays int)
	// StoredStatus devuelve la clave de estado tal como está GUARDADA, sin normalizar. El puerto
	// solo la enseña normalizada, y sin esto la suite no distinguiría `closed` de `confirmed`.
	StoredStatus func(t *testing.T, tenantID, intakeID string) string
	// EventStatus devuelve el estado del evento ("" si no existe). Es lo que deja ver que una
	// operación no tocó el contenedor.
	EventStatus func(t *testing.T, eventID string) string
	// Now devuelve el instante del reloj con el que la implementación fecha lo que escribe: en
	// memoria el reloj inyectado con SetClock; en Postgres, el now() de la base. Tiene que ser
	// posterior al 2026-09-01: las siembras de la suite son de agosto de 2026 y los casos afirman
	// que una escritura deja una fecha posterior a la sembrada.
	Now func(t *testing.T) time.Time
	// Advance deja pasar ese reloj: promete que lo que se escriba después de la llamada lleva un
	// instante ESTRICTAMENTE posterior a lo escrito antes de ella. En memoria adelanta el reloj
	// inyectado; contra Postgres espera, preguntándoselo a la base, a que su reloj pase del
	// microsegundo en que estaba. La suite no duerme ni mira el reloj de pared.
	Advance func(t *testing.T)
}

// WithLiteralCipher es intakes.WithLiteralCipher, reexportada: la opción que le da al adaptador
// Postgres la llave del literal de nivel 2. Está aquí para el Montaje de test/procesos, que del
// paquete del puerto solo puede nombrar los constructores `New…` (candado ProcessImports, regla
// 3b) y sin ella construiría un store que se niega, con razón, a escribir un literal: el caso
// InsertRevision_LiteralLeavesThePayloadAndReturnsOnRead no podría pasar (hallazgo 15 de F6,
// decidido por Jhoan el 2026-10-08: se reexporta y el candado no se toca).
//
// No añade nada a la opción: un Montaje la pasa a intakes.NewPostgres con un crypto.FieldCipher
// de keyring propio. El de memoria no la usa (MemoryStore no cifra).
func WithLiteralCipher(c *crypto.FieldCipher) intakes.PostgresOption {
	return intakes.WithLiteralCipher(c)
}

// Contrato ejecuta las promesas de la persistencia de solicitudes contra la implementación que
// devuelve nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta
// nada: un caso que no aplica a una implementación es un defecto del puerto, no de la suite.
//
// 🔴 LA MARCA DE ESTADO ES LA FILA ENTERA (hallazgo 35 de F1). De cada solicitud la suite guarda
// TODO lo que una operación puede tocar —las once columnas de la cabecera que el puerto enseña, la
// clave de estado almacenada, cada línea con sus seis campos, cada revisión con los suyos, la
// presencia de datos del comprador y el estado de su evento— y la compara completa antes y después
// (snapshot_contrato.go). Un rechazo tiene que dejarla IDÉNTICA; una escritura, idéntica salvo lo
// que el caso dice que cambia. Y cada caso que escribe siembra dos testigos —una hermana del mismo
// tenant y una solicitud del otro— y comprueba al final que ninguna se movió.
//
// De ahí sale la regla que el doble viejo no cumplía: UpdatedAt SE REFRESCA en toda escritura de
// la cabecera (el CAS del estado, el recálculo del total, el descarte, el abandono por evento) y NO
// en los recordatorios ni en una llamada que no cambia nada.
//
// Lo que la suite NO afirma, a propósito:
//
//   - El orden que dependa de la colación: las únicas cadenas que la suite ordena son UUID en
//     minúsculas (dígitos y a–f ASCII), que Go compara byte a byte igual que Postgres compara el
//     tipo uuid. Sesiones, contactos y skus son ASCII y nunca deciden un orden (hallazgo 8).
//   - TransitionError y qué transiciones son válidas: son de Service.SetStatus. El puerto solo
//     promete el compare-and-swap (ErrConflict), y eso sí se afirma.
//   - La fila legada sin evento padre (no se puede sembrar en Postgres) ni el cierre del contenedor
//     open→cancelled al descartar: por el puerto no se llega con el evento `open`, porque la guarda
//     `live_event` corta antes. Lo cubre el proceso de F9.
//   - La poda del literal por TTL: exige envejecer una revisión doce meses. La cubren los tests
//     propios de cada implementación. Que el literal sale del payload al escribir y vuelve al leer
//     SÍ se afirma.
//   - BuyerDataPresent en true: los datos del comprador entran por otro adaptador
//     (buyerdata_postgres.go). Aquí solo se vigila que nadie lo encienda.
//   - El valor concreto de una fecha que pone la implementación (UpdatedAt, CreatedAt de una
//     revisión, AddedAt de una línea nueva): solo que no es cero y cuándo avanza. El plazo de la
//     seña sí se acota, contra Now.
//   - El byte a byte de un payload: Postgres lo guarda en jsonb, que reordena las claves. Se
//     compara su contenido.
//   - La concurrencia de los compare-and-swap: se afirma la secuencia (el segundo no gana); la
//     carrera es del proceso de F9.
//
// Los instantes se comparan con Equal: Postgres guarda microsegundos y devuelve la zona de la
// sesión. Las siembras usan segundos enteros.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("intakeshelpertest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
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
		// Lecturas (read_contrato.go).
		{"Get_ReturnsHeaderLinesAndRevisions", caseGetReturnsEverything},
		{"Get_OtherTenantUnknownOrMalformedID_ErrNotFound", caseGetNotFound},
		{"List_NoFilter_NewestFirstWithTotal", caseListNewestFirst},
		{"List_SortOldest_IDBreaksTiesInTheSameDirection", caseListSortAndTieBreak},
		{"List_StatusFilter_ReachesLegacyClosed", caseListStatusFilter},
		{"List_DateRange_FromInclusiveToExclusive", caseListDateRangeAndSession},
		{"List_Pagination_TotalCountsTheFilter", caseListPagination},
		{"List_Orphan_OnlyThoseWithoutLiveEvent", caseListOrphan},
		{"List_OtherTenant_SeesNothing", caseListTenantIsolation},
		{"ListDetails_HeadersWithTheirLines", caseListDetailsLines},
		{"ListDetails_LimitCutsHeaders_SamePredicateAsList", caseListDetailsLimitAndFilter},
		{"Reads_SameHeaderOnTheThreePaths", caseSameHeaderOnThreePaths},
		// UpdateStatus (status_contrato.go).
		{"UpdateStatus_CASOverLegacyRow_WritesAndNormalizes", caseUpdateStatusOverLegacy},
		{"UpdateStatus_UnexpectedState_ErrConflictWritesNothing", caseUpdateStatusConflict},
		{"UpdateStatus_OtherTenantOrUnknown_ErrNotFoundWritesNothing", caseUpdateStatusNotFound},
		{"UpdateStatus_ToPendingApproval_EnsuresTheShippingLine", caseUpdateStatusPendingApproval},
		{"UpdateStatus_ToDepositRequested_SetsDueDateClearsReminder", caseUpdateStatusDepositRequested},
		{"UpdateStatus_ToAbandoned_KeepsLinesAndRevisions", caseUpdateStatusAbandoned},
		// Revisiones e historial aprobado (revisions_contrato.go).
		{"InsertRevision_NumbersPerIntakeFromOne", caseInsertRevisionNumbers},
		{"InsertRevision_EmptyPayload_ErrEmptyRevisionPayload", caseInsertRevisionEmptyPayload},
		{"InsertRevision_LiteralLeavesThePayloadAndReturnsOnRead", caseInsertRevisionLiteral},
		{"ApprovedRenderedTexts_NewestFirstOnlyApprovedWithText", caseApprovedTexts},
		{"ApprovedRenderedTexts_LimitAndTenant", caseApprovedTextsLimitAndTenant},
		// Envío y configuración (shipping_contrato.go).
		{"EnsureShippingLine_Idempotent_OneLineSquaredTotal", caseShippingIdempotent},
		{"EnsureShippingLine_NoZones_PolicyDecides", caseShippingPolicyWithoutZones},
		{"EnsureShippingLine_ZoneChange_ReplacesInPlace", caseShippingZoneChange},
		{"EnsureShippingLine_NoZone_KeepsOwnerPrice", caseShippingKeepsOwnerPrice},
		{"EnsureShippingLine_OtherTenantOrUnknown_ErrNotFound", caseShippingNotFound},
		{"ShippingZones_ReturnsTheConfigured", caseShippingZonesRead},
		{"NotifySettings_DefaultsAndConfigured", caseNotifySettings},
		// Edición y revalidación (edit_contrato.go).
		{"ReplaceItems_ReplacesCustomerLinesKeepsSystemOnes", caseReplaceItems},
		{"ReplaceItems_AsCorrection_CarriesTheSignal", caseReplaceItemsAsCorrection},
		{"ReplaceItems_Repeated_NeverDuplicatesShipping", caseReplaceItemsRepeated},
		{"ReplaceItems_ConflictOrOtherTenant_WritesNothing", caseReplaceItemsRejected},
		{"ApplyRevalidation_SurgicalWrite", caseApplyRevalidation},
		{"ApplyRevalidation_NeverTouchesPlatformLines", caseApplyRevalidationPlatformLines},
		{"ApplyRevalidation_ConflictOrOtherTenant_WritesNothing", caseApplyRevalidationRejected},
		// Descarte y abandono (discard_contrato.go).
		{"Discard_AbandonsAndAudits", caseDiscard},
		{"Discard_LiveEvent_WritesNothing", caseDiscardLiveEvent},
		{"Discard_NotDiscardable_StateIsCheckedBeforeTheEvent", caseDiscardNotDiscardable},
		{"Discard_Twice_OneRevision", caseDiscardTwice},
		{"Discard_OtherTenantOrUnknown_ErrNotFound", caseDiscardNotFound},
		{"AbandonByEvent_AbandonsTheOpenOne_Idempotent", caseAbandonByEvent},
		{"AbandonByEvent_LeavesNonOpenAlone", caseAbandonByEventNotOpen},
		{"AbandonByEvent_OtherTenantOrUnknownEvent_TouchesNothing", caseAbandonByEventForeign},
		// Recordatorios (reminders_contrato.go).
		{"MarkDepositReminded_OnlyTheFirstWins", caseDepositRemindedOnce},
		{"MarkDepositReminded_NotItsTurn_FalseWritesNothing", caseDepositRemindedNotItsTurn},
		{"PendingDepositReminders_ByContactMostOverdueFirst", casePendingDepositReminders},
		{"MarkExpiryReminded_OnlyTheFirstWins_KeepsUpdatedAt", caseExpiryRemindedOnce},
		{"MarkExpiryReminded_NotItsTurn_FalseWritesNothing", caseExpiryRemindedNotItsTurn},
	}
}

// seedsNotBefore es la cota que Montaje.Now tiene que superar: todas las siembras son anteriores.
var seedsNotBefore = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Store == nil:
		t.Fatal("Montaje.Store es nil")
	case m.Seed == nil || m.SetShippingZones == nil || m.SetDepositTemplate == nil:
		t.Fatal("Montaje: Seed, SetShippingZones y SetDepositTemplate son obligatorios")
	case m.StoredStatus == nil || m.EventStatus == nil:
		t.Fatal("Montaje: StoredStatus y EventStatus son obligatorios (la marca de estado los necesita)")
	case m.Now == nil || m.Advance == nil:
		t.Fatal("Montaje: Now y Advance son obligatorios (la suite no mira el reloj de pared)")
	case m.TenantA == m.TenantB:
		t.Fatalf("Montaje: TenantA y TenantB deben ser distintos y son %q", m.TenantA)
	}
	validateTenants(t, m)
	if now := m.Now(t); !now.After(seedsNotBefore) {
		t.Fatalf("Montaje.Now = %v; tiene que ser posterior a %v, las siembras son anteriores", now, seedsNotBefore)
	}
}

// validateTenants comprueba que los dos tenants del montaje son UUID bien formados y
// llegan sin solicitudes.
func validateTenants(t *testing.T, m Montaje) {
	t.Helper()
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		if _, err := uuid.Parse(tenant); err != nil {
			t.Fatalf("Montaje: el tenant %q no es un UUID bien formado: %v", tenant, err)
		}
		got, total, err := m.Store.List(context.Background(), tenant, intakes.Filter{})
		if err != nil || total != 0 || len(got) != 0 {
			t.Fatalf("Montaje: el tenant %q tiene que venir sin solicitudes (total=%d, err=%v)", tenant, total, err)
		}
	}
}
