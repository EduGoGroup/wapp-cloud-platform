// Package integrationshelpertest es la suite de contrato del puerto integrations.Store y su doble
// en memoria (Memoria). Ningún código de producción lo importa: arrastra "testing".
//
// La suite la corren las dos implementaciones del puerto: Memoria en unitario (memoria_test.go) e
// integrations.Postgres en los procesos de F9, con el arnés de testcontainers
// (test/procesos/integrations_contrato_test.go).
//
// Nuevo: no tiene fichero viejo. Los casos salen del contrato de integrations.Store y de los tests
// viejos, leídos y NO portados (05 E-8): los cuatro *_integration_test.go de internal/integrations
// (postgres_, crud_, outbox_stats_ y payload_purge_) y los tres de worker_test.go que en realidad
// probaban el almacén (RecuperaSoloLosClaimsVencidos, ClaimLostSeReconoceEnvuelto y el claim
// sellado). De ellos quedan FUERA los que no son del puerto: CountOutbox y SecretFingerprint (son
// de *Postgres, D-F6-6: los afirma postgres_test.go), el cifrado en reposo del secreto (es de la
// tabla: lo mira el Montaje de Postgres) y la migración 0050 (es del runner de migraciones).
//
// Un fichero por tema; los de casos y de apoyo terminan en _contrato.go:
//   - contrato.go: la entrada. Montaje, las filas, Contrato y la tabla de casos.
//   - snapshot_contrato.go: la marca de estado (la fila ENTERA), los testigos y los auxiliares.
//   - outbox_contrato.go: EnqueueWebhook y ClaimWebhookBatch.
//   - close_contrato.go: las tres transiciones que cierran un claim y la valla (ErrClaimLost).
//   - recover_contrato.go: RecoverOrphanDeliveries.
//   - tenant_contrato.go: la configuración por tenant y su secreto.
//
// Para añadir un caso: escribe su función en el fichero de su tema y añade su fila a cases().
package integrationshelpertest

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// OutboxRow es una fila ENTERA de public.webhook_outbox: sus diez columnas son exactamente los
// diez campos de integrations.WebhookOutbox (last_error NULL es "" y claimed_at NULL es el
// instante cero). Es un alias para que el Montaje de test/procesos, que del paquete del puerto
// solo puede nombrar los constructores `New…` (candado ProcessImports, regla 3b), pueda devolverla.
type OutboxRow = integrations.WebhookOutbox

// IntegrationRow es una fila ENTERA de public.tenant_integrations tal como está GUARDADA, sin el
// secreto en claro: las siete columnas de configuración y, del sobre del secreto, solo si está y
// una marca opaca de su contenido.
type IntegrationRow struct {
	TenantID       string
	CatalogAdapter string
	EventsAdapter  string
	// EndpointURL es "" cuando la columna es NULL.
	EndpointURL string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// HasSecret dice si la fila guarda un secreto: las tres columnas del sobre (secret_enc,
	// secret_dek, secret_kek_id) rellenas. Un Montaje que encuentre el sobre a medias (alguna sí y
	// alguna no) tiene que fallar el test: ese estado no lo puede escribir el puerto.
	HasSecret bool
	// SecretSeal es una marca OPACA del secreto tal como está guardado: "" si no hay, y si lo hay
	// una cadena que cambia cada vez que el secreto se REESCRIBE y no cambia mientras no se toque.
	// Jamás contiene el secreto ni nada de lo que se pueda deducir. Contra Postgres es un resumen
	// de las tres columnas del sobre; en memoria, el número de orden de la escritura. Es lo que
	// deja ver que un upsert «sin secreto» no rozó el sobre.
	SecretSeal string
}

// Montaje es lo que cada implementación entrega a la suite para UN caso: Contrato llama a nuevo
// una vez por caso y no limpia nada entre llamadas. Todos los campos son obligatorios.
type Montaje struct {
	// Store es la implementación bajo prueba, con la cola VACÍA (ni una fila en webhook_outbox:
	// el reclamo es de toda la cola, no de un tenant, y una fila ajena al caso lo estropearía).
	Store integrations.Store
	// TenantA y TenantB son dos tenant_id distintos, no vacíos y SIN integración. tenant_id es
	// TEXT sin clave foránea en las dos tablas: no hay fila de tenants que sembrar.
	TenantA, TenantB string
	// Now devuelve el instante del reloj con el que la implementación decide (qué está vencido,
	// con qué sella un claim, qué fecha pone): en memoria el reloj inyectado con SetClock; contra
	// Postgres, el now() de la base.
	Now func(t *testing.T) time.Time
	// Advance deja pasar ese reloj: promete que lo que la implementación feche o selle después de
	// la llamada lleva un instante ESTRICTAMENTE posterior a lo de antes de ella. En memoria
	// adelanta el reloj inyectado; contra Postgres espera, preguntándoselo a la base, a que su
	// reloj pase del microsegundo en que estaba. La suite no duerme ni mira el reloj de pared.
	Advance func(t *testing.T)
	// OutboxRow lee, sin pasar por el puerto, la fila ENTERA de webhook_outbox con ese id. found
	// es false si no existe. El puerto no tiene lectura de la cola (solo el reclamo, que la
	// cambia): sin esto la suite no podría mirar una fila sin tocarla.
	OutboxRow func(t *testing.T, id int64) (row OutboxRow, found bool)
	// IntegrationRow lee, sin pasar por el puerto, la fila ENTERA de tenant_integrations del
	// tenant, sin el secreto en claro. found es false si no existe.
	IntegrationRow func(t *testing.T, tenantID string) (row IntegrationRow, found bool)
	// SetClaimedAt deja la fila con ese claimed_at, como un UPDATE crudo de esa sola columna (el
	// instante cero es NULL). Es la única siembra que el puerto no deja hacer, y hace falta para
	// dos cosas: un claim que venció hace horas, sin dormir el test, y la fila `delivering` sin
	// sello que dejaba el código anterior a la migración 0049. Si la fila no existe, falla el test.
	SetClaimedAt func(t *testing.T, id int64, at time.Time)
}

// Contrato ejecuta las promesas de integrations.Store contra la implementación que devuelve
// nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta nada.
//
// 🔴 LA MARCA DE ESTADO ES LA FILA ENTERA (hallazgo 35 de F1, R6.3.e). De cada entrega la suite
// guarda sus diez columnas (OutboxRow) y de cada integración sus siete de configuración, si tiene
// secreto, la marca de su sobre y el secreto que devuelve el puerto; y las compara completas antes
// y después (snapshot_contrato.go). Un rechazo tiene que dejar la fila IDÉNTICA; una escritura,
// idéntica salvo lo que el caso dice que cambia. Y cada caso siembra antes tres testigos —una
// entrega de TenantA y otra de TenantB, las dos en espera de reintento dentro de 24 h, y la
// integración de TenantB con su secreto— y comprueba al final que ninguno se movió: ni el reclamo
// ni el rescate ni un cierre pueden rozarlos.
//
// Lo que la suite NO afirma, a propósito (hallazgo 24 de F6: lo que diverge legítimamente entre
// memoria y Postgres no va aquí):
//
//   - El ORDEN de las filas dentro del lote que devuelve ClaimWebhookBatch: en Postgres salen de
//     un UPDATE … RETURNING, que no garantiza orden. Se afirma QUÉ filas elige el límite (las de
//     next_attempt_at más antiguo), no en qué orden las entrega; tampoco el desempate entre dos
//     filas con el mismo next_attempt_at (la suite siempre deja pasar el reloj entre ellas).
//   - Que los id sean consecutivos: en Postgres una inserción rechazada consume un valor de la
//     secuencia. Se afirma que crecen.
//   - El texto exacto del payload: Postgres lo guarda como jsonb y lo devuelve normalizado
//     (espacios, orden de claves). Se compara su CONTENIDO.
//   - La frontera exacta del lease (un claim sellado justo en ahora − lease): contra Postgres no
//     se puede fabricar ese instante. La fija el test propio de Memoria.
//   - Un límite negativo en ClaimWebhookBatch (Postgres lo rechaza; el worker nunca lo manda) ni
//     el vocabulario de los adaptadores (lo acota un CHECK de la tabla que el doble no tiene: la
//     suite solo usa `local`, `http` y `webhook`).
//   - La causa que envuelve el error de un payload que no es JSON: solo su prefijo.
//   - Fracciones de microsegundo: las fechas que la suite escribe son segundos enteros, y los
//     instantes se comparan con Equal (Postgres devuelve la zona de la sesión).
//   - La distinción entre last_error NULL y "": el puerto las da por la misma cosa.
//   - Nada de lo que no es del puerto: CountOutbox, SecretFingerprint, el cifrado en reposo.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("integrationshelpertest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
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
	queueAndConfig := []contractCase{
		// La cola: encolar.
		{"EnqueueWebhook_NewRow_PendingAndDueNow", caseEnqueueNewRow},                               // nace pending, sin intentos, sin claim y reclamable ya
		{"EnqueueWebhook_IDsGrowAcrossTenants", caseEnqueueIDsGrow},                                 // id creciente en el orden de encolado, sea cual sea el tenant
		{"EnqueueWebhook_InvalidJSON_RejectedNothingQueued", caseEnqueueInvalid},                    // un payload que no es JSON se rechaza y no deja fila
		{"EnqueueWebhook_KeepsItsOwnCopyOfThePayload", caseEnqueueCopiesPayload},                    // el almacén no comparte el slice del llamante
		{"ClaimWebhookBatch_NothingDue_NoRows", caseClaimNothingDue},                                // sin nada reclamable: cero filas y ningún error
		{"ClaimWebhookBatch_SealsTheClaim", caseClaimSeals},                                         // delivering + claimed_at; lo devuelto ES la fila; nada más cambia
		{"ClaimWebhookBatch_NeverReturnsAClaimedRow", caseClaimNotTwice},                            // una fila reclamada no vuelve a salir, ni con el tiempo
		{"ClaimWebhookBatch_HonorsLimit_OldestDueFirst", caseClaimLimit},                            // respeta el tamaño de lote y elige las más antiguas
		{"ClaimWebhookBatch_ZeroLimit_ClaimsNothing", caseClaimZeroLimit},                           // con límite 0 no reclama nada
		{"ClaimWebhookBatch_OrdersByNextAttemptNotByID", caseClaimOrder},                            // el orden es next_attempt_at, no el id
		{"ClaimWebhookBatch_SkipsRowsNotYetDue", caseClaimNotDue},                                   // respeta next_attempt_at: lo futuro espera, lo pasado sale
		{"ClaimWebhookBatch_ServesEveryTenant", caseClaimEveryTenant},                               // la cola es una: el reclamo no filtra por tenant
		{"ClaimWebhookBatch_ConcurrentClaimsNeverShareARow", caseClaimConcurrent},                   // dos reclamos a la vez nunca se llevan la misma fila
		{"MarkWebhookDelivered_ClosesAndEmptiesThePayload", caseDelivered},                          // delivered, sin claim, payload {} y sin contar intento
		{"MarkWebhookDelivered_AfterAFailure_KeepsAttemptsAndLastError", caseDeliveredAfterFailure}, // el recibo conserva lo que pasó antes
		{"MarkWebhookFailed_ReschedulesAndCountsTheAttempt", caseFailed},                            // pending, attempts+1, next_attempt_at y last_error dados
		{"MarkWebhookDead_TerminalKeepsThePayload", caseDead},                                       // dead, attempts+1, last_error, payload intacto
		{"MarkWebhook_ClosedRows_NeverClaimedNorRecovered", caseTerminalRowsStay},                   // delivered y dead no se reclaman ni se rescatan
		{"MarkWebhook_OnlyTouchesItsOwnRow", caseCloseOnlyOwnRow},                                   // cerrar una entrega no toca a su vecina en vuelo
		{"RecoverOrphanDeliveries_NothingInFlight_Zero", caseRecoverNothing},                        // pending, delivered y dead no son huérfanas
		{"RecoverOrphanDeliveries_LiveClaim_Untouched", caseRecoverLiveClaim},                       // un claim vigente no se toca y su dueño puede cerrarlo
		{"RecoverOrphanDeliveries_ExpiredClaims_BackToPendingAndCounted", caseRecoverExpired},       // SOLO las de lease vencido: pending, attempts+1, last_error literal
		{"RecoverOrphanDeliveries_UnsealedRow_ImmediateOrphan", caseRecoverUnsealed},                // delivering sin sello: huérfana inmediata
		{"RecoverOrphanDeliveries_ZeroLease_EveryClaimExpired", caseRecoverZeroLease},               // con lease 0 todo claim anterior está vencido
		{"RecoverOrphanDeliveries_NewHolderClosesOldHolderLoses", caseRecoverNewHolder},             // la rescatada se reclama con sello nuevo; el viejo ya no vale
		// La configuración por tenant.
		{"GetTenantIntegration_NoRow_NotFound", caseTenantNoRow},                           // sin fila: found=false, sin error, sin secreto
		{"UpsertTenantIntegration_WithSecret_CreatesAndNeverReturnsIt", caseTenantCreate},  // alta con secreto: HasSecret, y el secreto solo por GetTenantSecret
		{"UpsertTenantIntegration_WithoutSecret_CreatesWithoutSecret", caseTenantNoSecret}, // alta sin secreto: HasSecret=false, endpoint vacío
		{"UpsertTenantIntegration_EmptySecret_KeepsTheStoredOne", caseTenantKeepsSecret},   // secret "" no toca el secreto guardado
		{"UpsertTenantIntegration_NewSecret_ReplacesIt", caseTenantRotates},                // un secreto nuevo sustituye al anterior
		{"UpsertTenantIntegration_ReplacesTheWholeConfig", caseTenantReplacesConfig},       // las cuatro columnas se reemplazan; endpoint "" lo borra
		{"UpsertTenantIntegration_IgnoresReadOnlyFields", caseTenantReadOnlyFields},        // HasSecret, CreatedAt y UpdatedAt del llamante se ignoran
		{"UpsertTenantIntegration_SameConfig_OnlyUpdatedAtMoves", caseTenantSameConfig},    // created_at es el del alta; updated_at se refresca siempre
		{"DeleteTenantIntegration_RemovesRowAndSecret", caseTenantDelete},                  // borra la fila y con ella el secreto, que no vuelve
		{"DeleteTenantIntegration_NoRow_NoError", caseTenantDeleteNothing},                 // borrar lo que no hay no es un error
		{"TenantIntegration_IsolatedByTenant", caseTenantIsolation},                        // aislamiento por tenant (INV-8), secreto incluido
		{"TenantIntegration_DoesNotTouchTheQueue", caseTenantLeavesQueue},                  // configurar o borrar no toca las entregas encoladas
	}
	// La valla: cada una de las tres transiciones, ante cada forma de claim que no vale, devuelve
	// ErrClaimLost con su texto y deja la fila idéntica.
	fence := claimLostCases()
	all := make([]contractCase, 0, len(queueAndConfig)+len(fence))
	all = append(all, queueAndConfig...)
	return append(all, fence...)
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	requireCompleteMontaje(t, m)
	ctx := context.Background()
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		if _, found, err := m.Store.GetTenantIntegration(ctx, tenant); err != nil || found {
			t.Fatalf("Montaje: el tenant %q tiene que venir SIN integración (found=%v, err=%v)", tenant, found, err)
		}
		if _, found := m.IntegrationRow(t, tenant); found {
			t.Fatalf("Montaje: IntegrationRow ve una fila del tenant %q que el puerto no ve", tenant)
		}
	}
	// Con la cola vacía el reclamo no devuelve nada (y por eso no cambia nada).
	if batch, err := m.Store.ClaimWebhookBatch(ctx, 1); err != nil || len(batch) != 0 {
		t.Fatalf("Montaje: la cola tiene que venir VACÍA (reclamadas=%d, err=%v)", len(batch), err)
	}
}

// requireCompleteMontaje exige que el Montaje traiga todos sus campos y dos tenants distintos.
func requireCompleteMontaje(t *testing.T, m Montaje) {
	t.Helper()
	missing := map[string]bool{
		"Store":          m.Store == nil,
		"Now":            m.Now == nil,
		"Advance":        m.Advance == nil,
		"OutboxRow":      m.OutboxRow == nil,
		"IntegrationRow": m.IntegrationRow == nil,
		"SetClaimedAt":   m.SetClaimedAt == nil,
		"TenantA":        m.TenantA == "",
		"TenantB":        m.TenantB == "",
	}
	for field, absent := range missing {
		if absent {
			t.Fatalf("Montaje.%s viene vacío: todos los campos son obligatorios", field)
		}
	}
	if m.TenantA == m.TenantB {
		t.Fatalf("Montaje: TenantA y TenantB son el mismo (%q); deben ser distintos", m.TenantA)
	}
}
