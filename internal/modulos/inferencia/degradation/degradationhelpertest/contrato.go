// Package degradationhelpertest es la suite de contrato del puerto degradation.Store y su doble
// en memoria, Memoria. Ningún código de producción lo importa: arrastra "testing".
//
//   - contrato.go: la entrada. Montaje, Contrato, la tabla de casos, la marca de estado y las
//     ayudas.
//   - save_contrato.go: los casos de Save (el dedupe, la ventana siguiente, `creado`, qué toca
//     el colapso y la concurrencia).
//   - list_contrato.go: los casos de List (lista vacía, orden, filtro «sin leer», página) y el
//     aislamiento por tenant.
//   - memoria.go: Memoria, el Store en memoria.
//
// La suite la corren las dos implementaciones del puerto: Memoria en unitario (memoria_test.go)
// y degradation.Postgres en los procesos de F9 (test/procesos), con el arnés de testcontainers.
//
// Los casos salen de plan/F4-inferencia/diseno.md §2 y de los tests viejos de
// internal/degradation/postgres_integration_test.go @ ebf4eb7, leídos, no portados. De sus seis,
// aquí está la conducta de cuatro (N fallos una fila, escrituras concurrentes, ventana siguiente,
// lista acotada al tenant) y media de un quinto (los ocho motivos entran); la otra mitad y
// TestElCheckDeMotivosEsLaRedDeAbajo afirman el CHECK de la tabla por SQL crudo y van a F9.
//
// Para añadir un caso: escribe su función en el fichero de su tema y añade su fila a cases().
package degradationhelpertest

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso. Tiene que venir limpio
// —sin avisos de los tenants que siembre— porque Contrato llama a nuevo una vez por caso.
type Montaje struct {
	// Store es la implementación bajo prueba.
	Store degradation.Store
	// SeedTenant devuelve el id de un tenant que la suite puede usar: tiene forma de UUID, es
	// distinto en cada llamada y NO tiene avisos. public.owner_degradation_notices guarda el
	// tenant como TEXT sin clave foránea, así que hoy ni Postgres ni Memoria necesitan crear
	// nada; va por función para que un montaje pueda sembrar el tenant el día que la tabla lo
	// exija. Falla el test t si no puede.
	SeedTenant func(t *testing.T) string
	// Rows es el observador de estado: TODAS las filas del tenant, con sus diez columnas, sin
	// filtro, sin página y en cualquier orden. Es independiente de List —que es lo que se
	// prueba— para que la suite pueda contar filas y afirmar «esto no escribió» sin fiarse de
	// la lectura bajo prueba. `read_at` NULL llega como instante cero. Con Postgres es un
	// SELECT de la tabla por tenant_id; con Memoria, Memoria.Rows. Falla el test t si no puede
	// leer.
	Rows func(t *testing.T, tenantID string) []degradation.Notice
	// MarkRead pone read_at = at al aviso id del tenant. El puerto no tiene esa operación (hoy
	// nada marca un aviso como leído; lo pide el Plan 045/047): la suite la necesita para
	// probar el filtro «solo sin leer» y que Save no pisa la lectura. Con Postgres es un UPDATE
	// por tenant_id e id; con Memoria, Memoria.MarkRead. Falla el test t si el aviso no existe.
	MarkRead func(t *testing.T, tenantID, id string, at time.Time)
}

// Contrato ejecuta las promesas de degradation.Store contra la implementación que devuelve
// nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta nada.
//
// La marca de estado (captureState) con la que la suite afirma «esto no escribió» o «el colapso
// solo tocó esto» vigila TODAS las columnas que Save puede tocar, no solo la que el caso mira
// (hallazgo 35 de F1): las diez de cada fila del tenant —id, tenant, motivo, vía, la ventana
// (inicio y fin), occurrences, read_at, created_at y last_seen_at— y cuántas filas hay.
//
// Lo que la suite NO afirma, a propósito, porque las dos implementaciones divergen o porque el
// puerto no lo deja ver:
//   - los CHECK de la tabla (owner_degradation_notices_reason_check, …_via_check,
//     …_ventana_check, …_occurrences_check): el puerto NO valida el vocabulario, así que con un
//     motivo sano o una vía inventada Postgres devuelve el error del CHECK y Memoria guarda la
//     fila. Por eso la suite solo usa motivos y vías del vocabulario y ventanas con fin
//     posterior al inicio. Quien custodia el vocabulario es degradation.Notifier (su test lo
//     afirma con Memoria.Saves) y, debajo, el CHECK, que F9 prueba por SQL.
//   - QUIÉN arbitra el dedupe bajo concurrencia: el caso de escrituras concurrentes pasa en las
//     dos, pero en Memoria demuestra un Mutex y en Postgres el índice único
//     ux_owner_degradation_notices_ventana, que es lo que aguanta dos réplicas. Solo la pasada
//     contra Postgres dice algo del índice.
//   - el desempate final del orden de List (`id`): en Postgres el id es un UUID aleatorio y en
//     Memoria uno secuencial. La suite nunca deja dos avisos del mismo tenant con la misma
//     ventana Y el mismo nacimiento, y no afirma qué id tiene cada aviso: solo que tiene forma
//     de UUID, que es único y que no cambia.
//   - la precisión por debajo del microsegundo (un timestamptz no la guarda; Memoria sí) y la
//     zona horaria de los instantes devueltos: la suite usa segundos enteros y compara con Equal.
//   - los textos de los errores, los fallos de infraestructura y el contexto cancelado: Memoria
//     no falla.
//   - cuántas veces se llamó a Save: el contador Memoria.Saves no es del puerto.
//
// Los ficheros de la suite NO comparan por vía con `==` ni con `switch`: afirman la vía dentro
// de un valor entero (un Notice esperado). Así no entran en la lista de permitidos del candado
// C2 (I-CP-3: la vía se pregunta en un solo sitio).
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("degradationhelpertest.Contrato: nuevo es nil; hace falta una función que devuelva un Montaje")
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
		{"Save_FirstFailure_CreatesTheNotice", caseSaveFirstFailureCreates},                 // creado=true; la fila entera
		{"Save_IgnoresIDOccurrencesReadAtAndCreatedAt", caseSaveIgnoresStoreOwnedFields},    // esos cuatro los decide el store
		{"Save_NFailuresSameWindow_OneNoticeCountsThem", caseSaveSameWindowCollapses},       // R4.5.c: N fallos, una fila
		{"Save_Collapse_TouchesOnlyCounterAndLastSeen", caseSaveCollapseTouchesOnlyTwo},     // hallazgo 35: ni window_end, ni created_at, ni read_at
		{"Save_LastSeenAt_NeverGoesBack", caseSaveLastSeenNeverGoesBack},                    // GREATEST: escrituras fuera de orden
		{"Save_ZeroLastSeenAt_UsesTheWindowEnd", caseSaveZeroLastSeenUsesWindowEnd},         // la fila no depende del reloj
		{"Save_NextWindow_OpensANewNotice", caseSaveNextWindowOpensNewNotice},               // el aviso es por ventana, no eterno
		{"Save_KeyIsTenantReasonViaAndWindowStart", caseSaveKeyHasFourColumns},              // las cuatro columnas del índice único
		{"Save_SameInstantInAnotherZone_SameNotice", caseSaveSameInstantOtherZoneCollapses}, // la clave es el instante, no su zona
		{"Save_AcceptsTheEightReasonsOnBothVias", caseSaveAcceptsWholeVocabulary},           // dieciséis avisos en una ventana
		{"ConcurrentSaves_OneNoticeAndOneCreated", caseConcurrentSaves},                     // exactamente un `creado`
		{"List_TenantWithoutNotices_EmptyNotNil", caseListWithoutNoticesEmptyNotNil},        // `[]`, no `null`
		{"List_ReturnsTheWholeNotice_AndDoesNotWrite", caseListReturnsWholeNotice},          // las diez columnas; leer no marca
		{"List_NewestFirst_ByWindowThenByBirth", caseListNewestFirst},                       // window_start DESC, created_at DESC
		{"List_OnlyUnread_LeavesOutTheReadOnes", caseListOnlyUnread},                        // SoloSinLeer
		{"List_LimitAndOffset_PageWithoutGapsOrRepeats", caseListPages},                     // la página
		{"List_BoundsThePage_Default50Cap200", caseListBoundsThePage},                       // techos; desplazamiento < 0 ⇒ 0
		{"Notices_AreIsolatedByTenant", caseNoticesIsolatedByTenant},                        // INV-7
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Store == nil:
		t.Fatal("Montaje.Store es nil")
	case m.SeedTenant == nil:
		t.Fatal("Montaje.SeedTenant es nil: la suite necesita tenants")
	case m.Rows == nil:
		t.Fatal("Montaje.Rows es nil: la suite necesita observar las filas")
	case m.MarkRead == nil:
		t.Fatal("Montaje.MarkRead es nil: la suite necesita marcar avisos como leídos")
	}
}

// windowSize es la ventana con la que la suite arma sus avisos: la del default del escritor.
const windowSize = 15 * time.Minute

// Los instantes de la suite van en segundos enteros y en UTC para que sobrevivan sin pérdida a
// un timestamptz de Postgres. baseWindow cae en un borde de ventana; readInstant es el de las
// lecturas que la suite marca.
var (
	baseWindow  = time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC)
	readInstant = time.Date(2031, 3, 5, 8, 30, 0, 0, time.UTC)

	uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// allReasons son los ocho motivos, por sus constantes: la suite nace completa en el commit rojo,
// cuando degradation.Reasons todavía es un contrato sin lógica.
func allReasons() []degradation.Reason {
	return []degradation.Reason{
		degradation.ReasonOllamaDown, degradation.ReasonBreakerOpen, degradation.ReasonEdgeOffline,
		degradation.ReasonTimeout, degradation.ReasonAPIError, degradation.ReasonCredencial,
		degradation.ReasonLeaseInvalid, degradation.ReasonEdgeSinCapacidad,
	}
}

// windowAt devuelve el inicio de la ventana número i contando desde baseWindow (puede ser < 0).
func windowAt(i int) time.Time { return baseWindow.Add(time.Duration(i) * windowSize) }

// seedTenant pide un tenant y comprueba que su id es un UUID bien formado.
func seedTenant(t *testing.T, m Montaje) string {
	t.Helper()
	tenant := m.SeedTenant(t)
	if !uuidShape.MatchString(tenant) {
		t.Fatalf("Montaje.SeedTenant devolvió %q, que no es un UUID bien formado", tenant)
	}
	return tenant
}

// failure es el aviso de un fallo tal como lo escribiría el escritor: la ventana que empieza en
// start, de windowSize, y el instante del fallo como último visto.
func failure(tenant string, reason degradation.Reason, via string, start, at time.Time) degradation.Notice {
	return degradation.Notice{TenantID: tenant, Reason: reason, Via: via, WindowStart: start, WindowEnd: start.Add(windowSize), LastSeenAt: at}
}

// born es la fila que deja el PRIMER Save de n: un fallo, sin leer, nacida en su último visto.
func born(n degradation.Notice) degradation.Notice {
	return degradation.Notice{
		TenantID: n.TenantID, Reason: n.Reason, Via: n.Via, WindowStart: n.WindowStart, WindowEnd: n.WindowEnd,
		Occurrences: 1, CreatedAt: n.LastSeenAt, LastSeenAt: n.LastSeenAt,
	}
}

// save llama a Save y falla el test si devuelve error.
func save(t *testing.T, m Montaje, n degradation.Notice) (created bool) {
	t.Helper()
	created, err := m.Store.Save(context.Background(), n)
	if err != nil {
		t.Fatalf("Save(%s): error inesperado %v", name(n), err)
	}
	return created
}

// mustCreate es save cuando el aviso tiene que NACER.
func mustCreate(t *testing.T, m Montaje, n degradation.Notice) {
	t.Helper()
	if !save(t, m, n) {
		t.Fatalf("Save(%s): creado = false, quería un aviso nuevo", name(n))
	}
}

// mustCollapse es save cuando el aviso tiene que COLAPSAR sobre uno que ya estaba.
func mustCollapse(t *testing.T, m Montaje, n degradation.Notice) {
	t.Helper()
	if save(t, m, n) {
		t.Fatalf("Save(%s): creado = true, quería el colapso sobre el aviso de esa ventana", name(n))
	}
}

// list llama a List y exige lo que vale para toda lectura: sin error, lista no nil y solo avisos
// del tenant pedido.
func list(t *testing.T, m Montaje, tenant string, f degradation.ListFilter) []degradation.Notice {
	t.Helper()
	got, err := m.Store.List(context.Background(), tenant, f)
	if err != nil {
		t.Fatalf("List(%s, %+v): error inesperado %v", tenant, f, err)
	}
	if got == nil {
		t.Fatalf("List(%s, %+v) devolvió nil: sin avisos es una lista vacía, para que se serialice como []", tenant, f)
	}
	for _, n := range got {
		if n.TenantID != tenant {
			t.Fatalf("List(%s, %+v) devolvió un aviso de %q (INV-7)", tenant, f, n.TenantID)
		}
	}
	return got
}

// withoutInstants devuelve n sin sus cinco instantes, para comparar el resto como un valor: los
// instantes se comparan aparte, con Equal, porque la zona con la que vuelven no es del contrato.
func withoutInstants(n degradation.Notice) degradation.Notice {
	n.WindowStart, n.WindowEnd, n.ReadAt, n.CreatedAt, n.LastSeenAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}
	return n
}

// sameNotice dice si a y b son la misma fila, columna a columna (las diez).
func sameNotice(a, b degradation.Notice) bool {
	return withoutInstants(a) == withoutInstants(b) &&
		a.WindowStart.Equal(b.WindowStart) && a.WindowEnd.Equal(b.WindowEnd) && a.ReadAt.Equal(b.ReadAt) &&
		a.CreatedAt.Equal(b.CreatedAt) && a.LastSeenAt.Equal(b.LastSeenAt)
}

// sameNotices dice si las dos listas tienen las mismas filas en el mismo orden.
func sameNotices(a, b []degradation.Notice) bool {
	return slices.EqualFunc(a, b, sameNotice)
}

// name nombra un aviso por su clave: tenant, motivo/vía e inicio de ventana.
//
// El motivo se pasa a cadena con una conversión y NO con un verbo de fmt (%s, %v): fmt llamaría a
// Reason.String, y la suite nace completa en el commit rojo, cuando String todavía es un contrato
// sin lógica. Vale para toda la suite: ningún Notice ni Reason va directo a un verbo de fmt.
func name(n degradation.Notice) string {
	return fmt.Sprintf("%s, %s/%s, ventana %s", n.TenantID, string(n.Reason), n.Via, n.WindowStart.UTC().Format(time.RFC3339))
}

// describe pinta una lista de avisos, uno por línea y con sus diez columnas, para los mensajes
// de fallo.
func describe(notices []degradation.Notice) string {
	var b strings.Builder
	for _, n := range notices {
		fmt.Fprintf(&b, "\n  {%s; id %q; fin %s; fallos %d; leído %s; nacido %s; último visto %s}", name(n), n.ID,
			n.WindowEnd.UTC().Format(time.RFC3339), n.Occurrences, n.ReadAt.UTC().Format(time.RFC3339),
			n.CreatedAt.UTC().Format(time.RFC3339), n.LastSeenAt.UTC().Format(time.RFC3339))
	}
	if b.Len() == 0 {
		return " (ninguno)"
	}
	return b.String()
}

// sortKey ordena filas por las cuatro columnas del índice único, que no dependen del id.
func sortKey(n degradation.Notice) string {
	return fmt.Sprintf("%s|%020d|%s|%s", n.TenantID, n.WindowStart.Unix(), string(n.Reason), n.Via)
}

// captureState es la marca de estado: TODAS las filas del tenant, enteras, en un orden estable.
// Con ella se afirma que una operación no escribió nada, en ninguna columna (hallazgo 35).
func captureState(t *testing.T, m Montaje, tenant string) []degradation.Notice {
	t.Helper()
	rows := slices.Clone(m.Rows(t, tenant))
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.TenantID != tenant {
			t.Fatalf("Montaje.Rows(%s) devolvió una fila de %q", tenant, row.TenantID)
		}
		if !uuidShape.MatchString(row.ID) || ids[row.ID] {
			t.Fatalf("el aviso de %s tiene el id %q: quería un UUID, y distinto en cada fila", tenant, row.ID)
		}
		ids[row.ID] = true
	}
	slices.SortFunc(rows, func(a, b degradation.Notice) int { return strings.Compare(sortKey(a), sortKey(b)) })
	return rows
}

// requireSameState afirma que el tenant sigue exactamente como en before.
func requireSameState(t *testing.T, m Montaje, tenant string, before []degradation.Notice) {
	t.Helper()
	if after := captureState(t, m, tenant); !sameNotices(after, before) {
		t.Errorf("las filas de %s cambiaron:%s\nantes:%s", tenant, describe(after), describe(before))
	}
}

// requireRows afirma que el tenant tiene exactamente esas filas (en cualquier orden). El id de
// cada want se ignora si viene vacío: lo pone el store. Devuelve las filas leídas, en el orden
// de sortKey.
func requireRows(t *testing.T, m Montaje, tenant string, want ...degradation.Notice) []degradation.Notice {
	t.Helper()
	got := captureState(t, m, tenant)
	want = slices.Clone(want)
	slices.SortFunc(want, func(a, b degradation.Notice) int { return strings.Compare(sortKey(a), sortKey(b)) })
	if len(got) != len(want) {
		t.Fatalf("%s tiene %d filas, quería %d:%s\nquería:%s", tenant, len(got), len(want), describe(got), describe(want))
	}
	for i := range want {
		if want[i].ID == "" {
			want[i].ID = got[i].ID
		}
	}
	if !sameNotices(got, want) {
		t.Fatalf("filas de %s:%s\nquería:%s", tenant, describe(got), describe(want))
	}
	return got
}

// requireOnlyRow es requireRows para un tenant con un solo aviso; lo devuelve.
func requireOnlyRow(t *testing.T, m Montaje, tenant string, want degradation.Notice) degradation.Notice {
	t.Helper()
	return requireRows(t, m, tenant, want)[0]
}
