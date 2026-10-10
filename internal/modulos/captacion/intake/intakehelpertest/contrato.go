// Package intakehelpertest son las suites de contrato de la tabla `intake_jobs` y el doble en
// memoria de su máquina. Ningún código de producción lo importa: arrastra "testing".
//
// Tres suites, una por puerta de la tabla:
//
//   - ContratoQueue sobre intake.JobStore (la COLA: abrir o ampliar la ventana, cerrarla,
//     listar las vivas, escribir el sobre del literal). En la spec de F7 se llamaba
//     `ContratoCola`.
//   - ContratoMachine sobre intake.PipelineStore (la MÁQUINA: los dos reclamos y las cinco
//     transiciones). En la spec de F7, `ContratoMaquina`.
//   - ContratoReanalysis sobre ReanalysisStore (el SEGUNDO PRODUCTOR de jobs: la pregunta por
//     el job vivo de un evento y la apertura del job del re-análisis), que en el paquete viejo
//     solo tenía tests del texto del SQL.
//
// Las corren las implementaciones en memoria en unitario —intake.MemoryStore la de la cola
// (memory_test.go); MachineMemory, el doble de este paquete, las otras dos (D-F7-5)— e
// intake.Postgres las tres en los procesos de F9, con el arnés de testcontainers.
//
// Nuevo: no tiene fichero viejo. Los casos salen del contrato de los puertos y de los tests de
// integración viejos de internal/intake, leídos y NO portados (05 E-8): postgres_, machine_,
// backoff_, retry_ y despertar_integration_test.go (25) y reanalisis_internal_test.go. De ellos
// quedan FUERA los que no son del puerto sino de la tabla o del motor: la forma de las columnas
// de la 0078 y el repoblado de las filas viejas, el índice único parcial a mano (dos ventanas
// vivas por INSERT directo), que `attempts + 1` lo calcula el motor sobre una fila que cambió
// por detrás, y `FOR UPDATE SKIP LOCKED` (la carrera de dos reclamos a la vez). Son de F9.
//
// Un fichero por tema; los de casos y de apoyo terminan en _contrato.go:
//   - contrato.go: la entrada. Row, los tres Montajes, las tres suites y sus tablas de casos.
//   - fixtures_contrato.go: las siembras, la marca de estado (la fila ENTERA) y sus aserciones.
//   - queue_contrato.go, queue_put_contrato.go y queue_key_contrato.go (la clave de ventana
//     incompleta): los casos de la cola.
//   - queue_close_contrato.go: los de su quinta operación, CloseWithSourceText. Mientras está en
//     rojo tienen entrada propia, ContratoQueueClose; en el verde pasan a la tabla de la cola.
//   - machine_contrato.go y machine_transitions_contrato.go: los de la máquina.
//   - reanalysis_contrato.go: los del segundo productor.
//
// Y el doble: machine_memory.go (PipelineStore) y reanalysis_memory.go (ReanalysisStore).
//
// Para añadir un caso: escribe su función en el fichero de su tema y añade su fila a la tabla.
package intakehelpertest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Row es una fila ENTERA de `intake_jobs`: todas las columnas que alguna operación de los tres
// puertos puede tocar. Es lo que un Montaje siembra y lo que devuelve al leer, y es también la
// fila que guarda MachineMemory (antes `pipeline.Fila`).
//
// Los NULL de la tabla son aquí el valor cero: Stage "", MessageTS cero, el sobre vacío, Error
// "", IntakeID "" y el Reanalysis cero (con From 0 para `reanalyzed_from` NULL).
type Row struct {
	// ID es el identificador del job. Al sembrar se ignora si viene vacío: lo pone la
	// implementación (un UUID en Postgres).
	ID string
	// Key es la tupla de ventana. EventID es un UUID.
	Key intake.WindowKey
	// Status es `aggregating`, `pending`, `processing`, `done` o `failed`.
	Status string
	// Stage es la última etapa persistida, "" si ninguna.
	Stage string
	// MessageTS es el instante del PRIMER mensaje de la ventana (D-044.9).
	MessageTS time.Time
	// SourceRefs son los `wa_message_id` de la ventana.
	SourceRefs []string
	// SourceText es el sobre del literal: las tres columnas.
	SourceText intake.SourceText
	// Artifacts es lo persistido por etapa.
	Artifacts map[string]json.RawMessage
	// Error es la causa de muerte de un job `failed`.
	Error string
	// IntakeID es el borrador (un UUID).
	IntakeID string
	// Attempts son los intentos ya consumidos (columna `attempts` de la 0078).
	Attempts int
	// NextAttemptAt es la marca del backoff.
	NextAttemptAt time.Time
	// CreatedAt y UpdatedAt son las dos marcas de la fila.
	CreatedAt, UpdatedAt time.Time
	// Reanalysis son las cuatro columnas de la 0080.
	Reanalysis intake.Reanalysis
}

// QueueMontaje es lo que una implementación de intake.JobStore entrega a ContratoQueue para UN
// caso: ContratoQueue llama a nuevo una vez por caso. Todos los campos son obligatorios.
//
// Casi todo estado que la cola puede producir se alcanza por el propio puerto; Seed está para
// el que no (queue_put_contrato.go: las marcas cruzadas de dos ventanas cerradas).
type QueueMontaje struct {
	// Store es la implementación bajo prueba. Llega con la tabla VACÍA: ListAggregating no
	// filtra por tenant, y una ventana viva de otro caso saldría en su lista.
	Store intake.JobStore
	// TenantA y TenantB son dos tenant_id distintos y no vacíos (tenant_id es TEXT sin clave
	// foránea: no hay fila que sembrar).
	TenantA, TenantB string
	// Rows devuelve TODAS las filas del tenant, enteras, en cualquier orden. Las columnas que
	// la implementación no guarda (las de la máquina, en el gemelo de la cola) salen a cero.
	Rows func(t *testing.T, tenantID string) []Row
	// Seed inserta la fila TAL CUAL y devuelve su id: es el Table.Seed de las otras dos suites,
	// con su misma semántica. La suite siembra siempre con Key, Status, CreatedAt y UpdatedAt.
	Seed func(t *testing.T, r Row) (id string)
	// Advance deja pasar el reloj con el que la implementación fecha lo que escribe: promete
	// que lo escrito después de la llamada lleva un instante ESTRICTAMENTE posterior a lo
	// escrito antes. En memoria adelanta el reloj inyectado; contra Postgres espera,
	// preguntándoselo a la base, a que su reloj pase del microsegundo en que estaba.
	Advance func(t *testing.T)
}

// Table es lo que las suites de la máquina y del segundo productor necesitan de la tabla además
// del puerto: dos tenants, sembrar una fila en un estado dado, leerla entera y el reloj. Todos
// los campos son obligatorios.
type Table struct {
	// TenantA y TenantB son dos tenant_id distintos y no vacíos.
	TenantA, TenantB string
	// Seed inserta la fila TAL CUAL y devuelve su id (el que ponga la implementación si Row.ID
	// viene vacío). Las suites siembran siempre con Key, Status, CreatedAt, UpdatedAt y
	// NextAttemptAt puestos; los valores cero de lo demás son los NULL de la tabla. Si no
	// puede, falla el test.
	Seed func(t *testing.T, r Row) (id string)
	// Row devuelve la fila entera de ese id. Si no existe, falla el test.
	Row func(t *testing.T, id string) Row
	// Now devuelve el instante del reloj de la implementación: en memoria el inyectado; en
	// Postgres, el now() de la base (los reclamos comparan `next_attempt_at` contra él).
	Now func(t *testing.T) time.Time
	// Advance deja pasar ese reloj (ver QueueMontaje.Advance).
	Advance func(t *testing.T)
}

// MachineMontaje es lo que una implementación de intake.PipelineStore entrega a ContratoMachine
// para UN caso.
type MachineMontaje struct {
	// Store es la implementación bajo prueba. Llega con la tabla VACÍA: ClaimNext no filtra por
	// tenant, y un `pending` de otro caso se lo llevaría el reclamo de este.
	Store intake.PipelineStore
	Table
}

// ReanalysisStore es la puerta del SEGUNDO PRODUCTOR de jobs. No es un puerto del paquete intake
// —allí son dos métodos sueltos de *intake.Postgres, fuera de JobStore y de PipelineStore a
// propósito—: se nombra aquí para que la suite pueda correr contra el adaptador y contra el
// doble.
type ReanalysisStore interface {
	// LiveJobOfEvent devuelve el id del job no terminal de ese evento en ese tenant.
	LiveJobOfEvent(ctx context.Context, tenantID, eventID string) (string, bool, error)
	// OpenReanalysis abre el job del re-análisis y devuelve su id.
	OpenReanalysis(ctx context.Context, s intake.ReanalysisRequest) (string, error)
}

// ReanalysisMontaje es lo que una implementación de ReanalysisStore entrega a
// ContratoReanalysis para UN caso.
type ReanalysisMontaje struct {
	// Store es la implementación bajo prueba.
	Store ReanalysisStore
	Table
}

// ContratoQueue ejecuta las promesas de intake.JobStore contra la implementación que devuelve
// nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta nada.
//
// 🔴 LA MARCA DE ESTADO ES LA FILA ENTERA (hallazgo 35). De cada ventana la suite guarda Row
// completo y lo compara antes y después: un no-op tiene que dejarla IDÉNTICA —`updated_at`
// incluido, que es lo que separa «idempotente» de «no toca la fila»—; una escritura, idéntica
// salvo lo que el caso dice que cambia. Cada caso que escribe deja además un testigo en el otro
// tenant con la MISMA sesión, contacto y evento, y comprueba al final que no se movió.
//
// La clave de ventana incompleta SÍ se afirma, texto del error incluido (queue_key_contrato.go):
// el gemelo viejo no la validaba y abría la ventana; desde el hallazgo 7 de F7 la rechaza igual
// que intake.Postgres.
//
// Lo que la suite NO afirma, a propósito:
//
//   - la ventana que se reabre tras un job `failed` o `done`: por este puerto no se llega a un
//     terminal. El índice es parcial, así que es el mismo camino que tras un `pending`;
//   - el orden de ListAggregating entre dos ventanas creadas en el mismo instante;
//   - el valor concreto de una marca que pone la implementación: solo cuándo se conserva y
//     cuándo avanza.
//
// Los instantes se comparan con Equal: Postgres guarda microsegundos y devuelve la zona de la
// sesión. Los que siembra la suite son segundos enteros.
func ContratoQueue(t *testing.T, nuevo func(t *testing.T) QueueMontaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("intakehelpertest.ContratoQueue: nuevo es nil; hace falta una función que devuelva un QueueMontaje")
	}
	for _, c := range queueCases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateQueueMontaje(t, m)
			c.run(t, m)
		})
	}
}

// ContratoMachine ejecuta las promesas de intake.PipelineStore contra la implementación que
// devuelve nuevo, con un Montaje limpio por caso. No salta nada.
//
// 🔴 LA MARCA DE ESTADO ES LA FILA ENTERA, igual que en ContratoQueue: una transición que no
// aplica —`(false, nil)`— o que se rechaza con error deja la fila IDÉNTICA; una que aplica, idéntica
// salvo las columnas que su caso nombra, y con `updated_at` estrictamente posterior. Los casos de
// las transiciones siembran dos testigos en `processing` —un hermano del mismo tenant y uno del
// otro— y comprueban al final que ninguno se movió: es lo que delata una sentencia sin su
// `WHERE id =`.
//
// Lo que la suite NO afirma, a propósito:
//
//   - la carrera de dos reclamos simultáneos (`FOR UPDATE SKIP LOCKED`): se afirma la secuencia
//     (el segundo no se lleva el mismo job). La carrera es del proceso de F9;
//   - que `attempts + 1` se calcule sobre el valor de la base y no sobre el del claim: se afirma
//     el resultado desde un valor sembrado distinto de cero;
//   - el texto de los errores: son de cada implementación (los del adaptador los fija su test);
//   - un id de job que no sea un UUID: Postgres lo rechaza en el cast y el doble no lo
//     encuentra. Los ids desconocidos de la suite son UUID bien formados.
func ContratoMachine(t *testing.T, nuevo func(t *testing.T) MachineMontaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("intakehelpertest.ContratoMachine: nuevo es nil; hace falta una función que devuelva un MachineMontaje")
	}
	for _, c := range machineCases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateMachineMontaje(t, m)
			c.run(t, m)
		})
	}
}

// ContratoReanalysis ejecuta las promesas del segundo productor de jobs contra la
// implementación que devuelve nuevo, con un Montaje limpio por caso. No salta nada.
//
// La marca de estado es la fila entera, como en las otras dos: ninguna de las dos operaciones
// toca una fila que ya existía, y cada caso lo comprueba con sus testigos.
//
// Lo que la suite NO afirma: un tenant o un evento que no sean UUID donde la columna lo es (el
// cast de Postgres falla y el doble no), ni el orden entre dos jobs vivos creados en el mismo
// instante.
func ContratoReanalysis(t *testing.T, nuevo func(t *testing.T) ReanalysisMontaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("intakehelpertest.ContratoReanalysis: nuevo es nil; hace falta una función que devuelva un ReanalysisMontaje")
	}
	for _, c := range reanalysisCases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			if m.Store == nil {
				t.Fatal("ReanalysisMontaje.Store es nil")
			}
			validateTable(t, m.Table)
			c.run(t, m)
		})
	}
}

// contractCase es una promesa de un puerto: su nombre (el del t.Run) y la función que la afirma.
type contractCase[M any] struct {
	name string
	run  func(t *testing.T, m M)
}

// queueCases es la tabla de ContratoQueue: un caso, una función.
func queueCases() []contractCase[QueueMontaje] {
	return []contractCase[QueueMontaje]{
		// OpenOrAppend (queue_contrato.go).
		{"OpenOrAppend_NewKey_OpensAnAggregatingWindow", caseOpenNewWindow},
		{"OpenOrAppend_LiveWindow_AppendsRefsFlatAndKeepsTheFirstMessageTS", caseAppendToLiveWindow},
		{"OpenOrAppend_NoRefsAndNoMessageTS_AreLegitimate", caseOpenWithoutRefs},
		{"OpenOrAppend_AfterClose_OpensASecondWindowAndLeavesTheClosedOne", caseReopenAfterClose},
		{"OpenOrAppend_EachPieceOfTheKey_IsItsOwnWindow", caseKeyIsTheFourColumns},
		{"OpenOrAppend_IncompleteKey_ErrorAndNothingWritten", caseOpenIncompleteKey},
		// CloseWindow.
		{"CloseWindow_LiveWindow_TrueOnceThenFalseAndUntouched", caseCloseOnce},
		{"CloseWindow_NoLiveWindowForTheKey_FalseAndNothingTouched", caseCloseWithoutLiveWindow},
		{"CloseWindow_IncompleteKey_ErrorAndNothingTouched", caseCloseIncompleteKey},
		// ListAggregating.
		{"ListAggregating_EmptyTable_NothingAndNoError", caseListEmpty},
		{"ListAggregating_OnlyLiveWindows_OldestFirstWithBothAnchors", caseListLiveWindows},
		{"ListAggregating_Limit_CutsTheTail_NonPositiveIsEmpty", caseListLimit},
		// PutSourceText (queue_put_contrato.go).
		{"PutSourceText_ClosedWindow_WritesTheThreeOnce", casePutOnce},
		{"PutSourceText_SeveralClosedWindows_OnlyTheMostRecentOne", casePutPicksTheLatestPending},
		{"PutSourceText_CrossedMarks_LatestUpdateWins_CreationBreaksTies", casePutCrossedMarks},
		{"PutSourceText_NoClosedWindow_FalseAndNothingTouched", casePutWithoutPending},
		{"PutSourceText_IncompleteEnvelope_ErrorAndNothingWritten", casePutIncompleteEnvelope},
		// La clave de ventana incompleta (queue_key_contrato.go).
		{"PutSourceText_IncompleteKey_ErrorBeforeTheEnvelopeAndNothingWritten", casePutIncompleteKey},
	}
}

// machineCases es la tabla de ContratoMachine.
func machineCases() []contractCase[MachineMontaje] {
	return []contractCase[MachineMontaje]{
		// Los dos reclamos (machine_contrato.go).
		{"ClaimNext_EmptyQueue_FalseAndNoError", caseClaimEmptyQueue},
		{"ClaimNext_DuePending_MovesToProcessingAndReturnsTheWholeJob", caseClaimReturnsEverything},
		{"ClaimNext_JobThatNeverRan_ComesWithZeroValues", caseClaimZeroValues},
		{"ClaimNext_OnlyPending_OtherStatusesAreNotTaken", caseClaimOnlyPending},
		{"ClaimNext_ClaimedJob_IsNotClaimedAgain", caseClaimTwice},
		{"ClaimNext_FutureMark_IsNotClaimableUntilDue", caseClaimRespectsTheMark},
		{"ClaimNext_OldestMarkWins_CreationBreaksTies", caseClaimOrder},
		{"ClaimNextIgnoringBackoff_FutureMark_IsTakenAnyway", caseWakeIgnoresTheMark},
		{"ClaimNextIgnoringBackoff_OnlyThatTenant", caseWakeOnlyItsTenant},
		{"ClaimNextIgnoringBackoff_OnlyPending_OldestMarkFirst", caseWakeOnlyPendingInOrder},
		{"ClaimNextIgnoringBackoff_EmptyTenantOrNothingToTake_FalseAndNoError", caseWakeNothing},
		// Las cinco transiciones (machine_transitions_contrato.go).
		{"SaveStage_Processing_WritesTheStageAndMergesItsArtifact", caseSaveStageMerges},
		{"SaveStage_SameStageAgain_ReplacesOnlyItsArtifact", caseSaveStageRepeats},
		{"SaveStage_Backwards_FalseAndUntouched", caseSaveStageNeverGoesBack},
		{"SaveStage_InvalidArtifact_ErrorAndNothingWritten", caseSaveStageInvalidArtifact},
		{"SaveStage_NotProcessing_FalseAndUntouched", caseSaveStageNotProcessing},
		{"Release_Processing_BackToPendingWithNothingElseTouched", caseRelease},
		{"Release_NotProcessing_FalseAndUntouched", caseReleaseNotProcessing},
		{"Retry_Processing_PendingWithOneMoreAttemptAndTheMarkPushed", caseRetry},
		{"Retry_PastMark_IsWrittenAsGivenAndClaimableAtOnce", caseRetryPastMark},
		{"Retry_ZeroInstant_ErrorAndNothingWritten", caseRetryZeroInstant},
		{"Retry_NotProcessing_FalseAndUntouched", caseRetryNotProcessing},
		{"Finish_Processing_DoneEmptiesTheEnvelopeAndWritesTheIntake", caseFinish},
		{"Finish_EmptyIntakeID_KeepsTheOneItHad", caseFinishKeepsIntake},
		{"Finish_NotProcessing_FalseAndUntouched", caseFinishNotProcessing},
		{"Fail_Processing_FailedWithItsReasonEmptiesTheEnvelopeAndKeepsTheStage", caseFail},
		{"Fail_EmptyReason_ErrorAndNothingWritten", caseFailWithoutReason},
		{"Fail_NotProcessing_FalseAndUntouched", caseFailNotProcessing},
		{"Terminals_AreAbsorbing_NoTransitionOrClaimApplies", caseTerminalsAbsorb},
		{"Transitions_EmptyJobID_ErrorAndNothingTouched", caseEmptyJobID},
	}
}

// reanalysisCases es la tabla de ContratoReanalysis (reanalysis_contrato.go).
func reanalysisCases() []contractCase[ReanalysisMontaje] {
	return []contractCase[ReanalysisMontaje]{
		{"LiveJobOfEvent_NoJobOrOnlyTerminalOnes_FalseAndNoError", caseLiveJobNone},
		{"LiveJobOfEvent_EachLiveStatus_ReturnsItsID", caseLiveJobEachStatus},
		{"LiveJobOfEvent_AsksByEventAndTenant_NotByIntake", caseLiveJobByEventAndTenant},
		{"LiveJobOfEvent_SeveralLive_TheNewestWins", caseLiveJobNewest},
		{"LiveJobOfEvent_MissingTenantOrEvent_Error", caseLiveJobBadCall},
		{"OpenReanalysis_IsBornPendingWithItsContextAndNoEnvelope", caseOpenReanalysisRow},
		{"OpenReanalysis_InheritsTheMessageTSOfTheFirstJobOfTheEvent", caseOpenReanalysisInheritsTS},
		{"OpenReanalysis_NoPreviousJob_MessageTSIsNowAndFromZeroIsNull", caseOpenReanalysisWithoutHistory},
		{"OpenReanalysis_IsNotIdempotent_TwoCallsTwoJobs", caseOpenReanalysisTwice},
		{"OpenReanalysis_IncompleteRequest_ErrorAndNothingWritten", caseOpenReanalysisIncomplete},
	}
}

// validateQueueMontaje exige lo que ContratoQueue da por hecho de un Montaje.
func validateQueueMontaje(t *testing.T, m QueueMontaje) {
	t.Helper()
	switch {
	case m.Store == nil:
		t.Fatal("QueueMontaje.Store es nil")
	case m.Rows == nil || m.Seed == nil || m.Advance == nil:
		t.Fatal("QueueMontaje: Rows, Seed y Advance son obligatorios")
	}
	validateTenants(t, m.TenantA, m.TenantB)
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		if rows := m.Rows(t, tenant); len(rows) != 0 {
			t.Fatalf("QueueMontaje: el tenant %q trae %d filas; tiene que venir sin ninguna", tenant, len(rows))
		}
	}
}

// validateMachineMontaje exige lo que ContratoMachine da por hecho de un Montaje. La cola vacía
// se comprueba reclamando: sobre una tabla vacía no toca nada, y sobre una que no lo está el
// caso tiene que fallar igualmente.
func validateMachineMontaje(t *testing.T, m MachineMontaje) {
	t.Helper()
	if m.Store == nil {
		t.Fatal("MachineMontaje.Store es nil")
	}
	validateTable(t, m.Table)
	if job, ok, err := m.Store.ClaimNext(context.Background()); err != nil || ok {
		t.Fatalf("MachineMontaje: la tabla tiene que venir vacía y ClaimNext = (%+v, %v, %v)", job, ok, err)
	}
}

// validateTable exige los cuatro ganchos y los dos tenants.
func validateTable(t *testing.T, tb Table) {
	t.Helper()
	if tb.Seed == nil || tb.Row == nil || tb.Now == nil || tb.Advance == nil {
		t.Fatal("Montaje: Seed, Row, Now y Advance son obligatorios")
	}
	validateTenants(t, tb.TenantA, tb.TenantB)
}

// validateTenants exige dos tenants distintos y no vacíos.
func validateTenants(t *testing.T, a, b string) {
	t.Helper()
	if a == "" || b == "" {
		t.Fatalf("Montaje: TenantA (%q) y TenantB (%q) no pueden ser vacíos", a, b)
	}
	if a == b {
		t.Fatalf("Montaje: TenantA y TenantB son el mismo (%q); deben ser distintos", a)
	}
}
