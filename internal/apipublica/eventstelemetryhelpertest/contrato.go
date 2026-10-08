// Package eventstelemetryhelpertest es la suite de contrato del puerto
// apipublica.EventTelemetryReader y su doble en memoria (Memory). Ningún código de producción lo
// importa: arrastra "testing".
//
// La suite la corren las dos implementaciones del puerto: Memory en unitario (memory_test.go) y
// apipublica.PostgresEventTelemetryStore en los procesos de F9, con el arnés de testcontainers
// (test/procesos/eventstelemetry_contrato_test.go).
//
// Nuevo: no tiene fichero viejo. Los casos salen del contrato de EventTelemetryReader y de los
// tests viejos, leídos y NO portados (05 E-8): internal/publicapi/eventstelemetry_integration_test.go
// (el guion bajo escapado, el tenant y el orden con keyset). El EXPLAIN de
// eventstelemetry_internal_test.go no es del puerto: es del índice de la migración 0056.
//
// Dos ficheros de suite:
//   - contrato.go: la entrada. Montaje, FlowEvent, Contrato, la tabla de casos, los auxiliares y
//     los casos de columnas, filtros y orden.
//   - cursor_contrato.go: el cursor y la paginación.
//
// Para añadir un caso: escribe su función en el fichero de su tema y añade su fila a cases().
package eventstelemetryhelpertest

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
)

// FlowEvent es una fila de public.flow_events tal como la SIEMBRA un caso: las cinco columnas de
// las que depende la lectura. Las otras tres NOT NULL de la tabla (contact_id, flow_id,
// flow_version) no las mira el lector: el Montaje las rellena con lo que quiera.
type FlowEvent struct {
	// TenantID es la columna tenant_id (TEXT, sin clave foránea).
	TenantID string
	// Kind es la COLUMNA kind de la fila ("persist" | "event"). El lector no la devuelve nunca:
	// la suite la siembra para comprobar justo eso.
	Kind string
	// Name es la columna name.
	Name string
	// Payload es la columna payload: un documento JSON válido, en texto.
	Payload string
	// CreatedAt es la columna created_at. La suite lo da siempre explícito, distinto del cero y
	// en microsegundos enteros (la resolución de timestamptz).
	CreatedAt time.Time
}

// Montaje es lo que cada implementación entrega a la suite para UN caso: Contrato llama a nuevo
// una vez por caso y no limpia nada entre llamadas. Todos los campos son obligatorios.
type Montaje struct {
	// Reader es la implementación bajo prueba.
	Reader apipublica.EventTelemetryReader
	// TenantA y TenantB son dos tenant_id distintos, no vacíos y SIN ninguna fila en
	// flow_events. tenant_id es TEXT sin clave foránea: no hay fila de tenants que sembrar.
	TenantA, TenantB string
	// Insert escribe la fila TAL CUAL, sin pasar por el puerto (que es de solo lectura), con el
	// created_at que trae y no el now() de la tabla, y devuelve su id. Promete ids positivos y
	// CRECIENTES: una fila insertada después lleva un id mayor. Si no puede escribir, falla el
	// test.
	Insert func(t *testing.T, row FlowEvent) int64
}

// contractCase es un caso de la suite: su nombre (en inglés, dice la regla) y su función.
type contractCase struct {
	name string
	run  func(t *testing.T, m Montaje)
}

// cases es la tabla de casos, en el orden en que corren.
func cases() []contractCase {
	return []contractCase{
		{"every column of the row is returned", caseEveryColumn},
		{"event kind is the payload kind and never the row kind", caseEventKind},
		{"only names that start with event_ literally are read", caseOnlyLifecycleNames},
		{"a tenant never reads the rows of another", caseTenantIsolation},
		{"since is inclusive and its zero does not filter", caseSince},
		{"rows come ordered by instant and then by id", caseOrder},
		{"limit keeps the first rows of the order", caseLimit},
		{"a cancelled context is an error", caseCancelledContext},
		{"the cursor is strictly after its instant and id", caseCursorIsStrict},
		{"the cursor compares the pair and not each part", caseCursorComparesThePair},
		{"a cursor with the zero instant does not filter", caseCursorZeroInstant},
		{"since and cursor are both applied", caseSinceAndCursor},
		{"paging by cursor walks every row exactly once", casePagingWalk},
	}
}

// Contrato ejecuta las promesas de apipublica.EventTelemetryReader contra la implementación que
// devuelve nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta
// nada.
//
// Vigila las CINCO columnas que la lectura devuelve (id, name, event_kind, payload, created_at),
// el aislamiento por tenant, el filtro de nombre, `since`, el orden (created_at, id), el límite y
// el cursor —también con instantes iguales, donde desempata el id—.
//
// Lo que la suite NO afirma, a propósito (lo que diverge legítimamente entre memoria y Postgres
// no va aquí):
//
//   - el texto exacto del payload: Postgres lo guarda como jsonb y lo devuelve normalizado
//     (espacios, orden de claves). Se compara su CONTENIDO;
//   - que los id sean consecutivos: solo que crecen (lo promete Montaje.Insert);
//   - el event_kind de un payload cuyo "kind" es un objeto o una lista (jsonb lo devuelve con su
//     propio formato), ni el de un payload que no es un objeto;
//   - un límite negativo: Postgres lo rechaza con un error y el handler nunca lo pide (el mínimo
//     que manda es 2);
//   - la zona horaria de created_at: se compara el INSTANTE.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			checkMontaje(t, m)
			c.run(t, m)
		})
	}
}

// baseInstant es el instante de referencia de los casos: los demás se dan como desplazamientos.
var baseInstant = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

// wideLimit es un límite que ningún caso llena: «todas las filas».
const wideLimit = 1000

// checkMontaje comprueba lo que Montaje promete antes de usarlo: un Montaje mal armado daría
// casos verdes por accidente (dos tenants iguales pasan el aislamiento sin probarlo).
func checkMontaje(t *testing.T, m Montaje) {
	t.Helper()
	if m.Reader == nil || m.Insert == nil {
		t.Fatalf("Montaje incompleto: Reader nil = %t, Insert nil = %t", m.Reader == nil, m.Insert == nil)
	}
	if m.TenantA == "" || m.TenantB == "" || m.TenantA == m.TenantB {
		t.Fatalf("Montaje: TenantA = %q y TenantB = %q; quiero dos tenants distintos y no vacíos", m.TenantA, m.TenantB)
	}
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		if rows := list(t, m, tenant, apipublica.EventTelemetryFilter{Limit: wideLimit}); len(rows) != 0 {
			t.Fatalf("Montaje: el tenant %q ya trae %d filas; quiero 0", tenant, len(rows))
		}
	}
}

// list llama al puerto y falla el test si devuelve error.
func list(t *testing.T, m Montaje, tenantID string, f apipublica.EventTelemetryFilter) []apipublica.EventTelemetryRow {
	t.Helper()
	rows, err := m.Reader.ListEventTelemetry(t.Context(), tenantID, f)
	if err != nil {
		t.Fatalf("ListEventTelemetry(tenant %q, %+v) = error %v; quiero nil", tenantID, f, err)
	}
	return rows
}

// seed siembra una fila de ciclo de vida de tenantID con la columna kind "event" y un payload
// mínimo, a offset de baseInstant, y devuelve su id.
func seed(t *testing.T, m Montaje, tenantID, name string, offset time.Duration) int64 {
	t.Helper()
	return m.Insert(t, FlowEvent{
		TenantID: tenantID, Kind: "event", Name: name,
		Payload: `{"history_id":"h-1","kind":"cart"}`, CreatedAt: baseInstant.Add(offset),
	})
}

// wantGrowing falla si los ids no son positivos y estrictamente crecientes: es lo que promete
// Montaje.Insert, y los casos del cursor se apoyan en ello.
func wantGrowing(t *testing.T, ids ...int64) {
	t.Helper()
	prev := int64(0)
	for i, id := range ids {
		if id <= prev {
			t.Fatalf("Montaje.Insert: el id nº %d es %d y el anterior %d; quiero ids positivos y crecientes (%v)", i, id, prev, ids)
		}
		prev = id
	}
}

// idsOf devuelve los id de las filas, en su orden.
func idsOf(rows []apipublica.EventTelemetryRow) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// wantIDs exige que las filas sean EXACTAMENTE las de want y en ese orden.
func wantIDs(t *testing.T, what string, rows []apipublica.EventTelemetryRow, want ...int64) {
	t.Helper()
	if got := idsOf(rows); !slices.Equal(got, want) {
		t.Errorf("%s: ids %v; quiero %v, en ese orden", what, got, want)
	}
}

// sameJSON dice si dos documentos JSON tienen el mismo contenido, sin mirar espacios ni el orden
// de las claves.
func sameJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Errorf("el payload devuelto no es JSON: %v (%q)", err, got)
		return false
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("el payload sembrado no es JSON: %v (%q)", err, want)
	}
	return reflect.DeepEqual(g, w)
}

// caseEveryColumn: una fila sale con sus cinco columnas, la columna kind no se cuela, el payload
// viaja entero y el instante es el sembrado (con microsegundos y en otra zona).
func caseEveryColumn(t *testing.T, m Montaje) {
	const payload = `{"history_id":"h-77","kind":"cart","nested":{"steps":[1,2,3],"ok":true}}`
	at := baseInstant.Add(1234567 * time.Microsecond).In(time.FixedZone("-03", -3*3600))
	id := m.Insert(t, FlowEvent{TenantID: m.TenantA, Kind: "persist", Name: "event_started", Payload: payload, CreatedAt: at})

	rows := list(t, m, m.TenantA, apipublica.EventTelemetryFilter{Limit: wideLimit})
	if len(rows) != 1 {
		t.Fatalf("sembrada 1 fila, leídas %d: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.ID != id {
		t.Errorf("ID = %d; quiero el de la fila sembrada, %d", got.ID, id)
	}
	if got.Name != "event_started" {
		t.Errorf("Name = %q; quiero %q", got.Name, "event_started")
	}
	if got.EventKind != "cart" {
		t.Errorf("EventKind = %q; quiero %q (payload->>'kind'; la columna kind de la fila es \"persist\")", got.EventKind, "cart")
	}
	if !sameJSON(t, got.Payload, payload) {
		t.Errorf("Payload = %s; quiero el contenido de %s", got.Payload, payload)
	}
	if !got.CreatedAt.Equal(at) {
		t.Errorf("CreatedAt = %v; quiero el instante sembrado, %v", got.CreatedAt, at)
	}
}

// caseEventKind: event_kind es payload->>'kind' —la cadena tal cual, un número o un booleano
// como texto, y vacío si falta o es null—, sea cual sea la columna kind de la fila.
func caseEventKind(t *testing.T, m Montaje) {
	cases := []struct{ rowKind, payload, want string }{
		{"event", `{"kind":"survey"}`, "survey"},
		{"persist", `{"kind":"menu"}`, "menu"},
		{"event", `{"history_id":"h-1"}`, ""},
		{"event", `{"kind":null}`, ""},
		{"event", `{"kind":""}`, ""},
		{"event", `{}`, ""},
		{"persist", `{"kind":7}`, "7"},
		{"event", `{"kind":true}`, "true"},
		{"media", `{"kind":"media","history_id":"h-2"}`, "media"},
	}
	for i, c := range cases {
		m.Insert(t, FlowEvent{
			TenantID: m.TenantA, Kind: c.rowKind, Name: "event_started", Payload: c.payload,
			CreatedAt: baseInstant.Add(time.Duration(i) * time.Second),
		})
	}
	rows := list(t, m, m.TenantA, apipublica.EventTelemetryFilter{Limit: wideLimit})
	if len(rows) != len(cases) {
		t.Fatalf("sembradas %d filas, leídas %d", len(cases), len(rows))
	}
	for i, c := range cases {
		if rows[i].EventKind != c.want {
			t.Errorf("payload %s con columna kind %q: EventKind = %q; quiero %q", c.payload, c.rowKind, rows[i].EventKind, c.want)
		}
		if !sameJSON(t, rows[i].Payload, c.payload) {
			t.Errorf("payload %s: Payload = %s; quiero el mismo contenido", c.payload, rows[i].Payload)
		}
	}
}

// caseOnlyLifecycleNames: solo se leen los nombres que empiezan LITERALMENTE por "event_" (el
// guion bajo no es comodín), mirando el nombre y no la columna kind.
func caseOnlyLifecycleNames(t *testing.T, m Montaje) {
	names := []struct {
		name string
		read bool
	}{
		{"event_started", true},
		{"event_", true},
		{"event_closed_by_owner", true},
		{"event_%", true},
		{"eventXfoo", false},
		{"event-started", false},
		{"event", false},
		{"event%", false},
		{"EVENT_started", false},
		{"Event_started", false},
		{" event_started", false},
		{"cart_event_closed", false},
		{"survey_answer", false},
		{"", false},
	}
	var want []int64
	for i, n := range names {
		// La columna kind va "event" también en las que NO se leen: el filtro es por nombre.
		id := m.Insert(t, FlowEvent{
			TenantID: m.TenantA, Kind: "event", Name: n.name, Payload: `{"kind":"cart"}`,
			CreatedAt: baseInstant.Add(time.Duration(i) * time.Second),
		})
		if n.read {
			want = append(want, id)
		}
	}
	rows := list(t, m, m.TenantA, apipublica.EventTelemetryFilter{Limit: wideLimit})
	wantIDs(t, "filtro de nombre", rows, want...)
	for _, r := range rows {
		if !strings.HasPrefix(r.Name, "event_") {
			t.Errorf("se leyó la fila %d con nombre %q, que no empieza por \"event_\"", r.ID, r.Name)
		}
	}
}

// caseTenantIsolation: cada tenant lee solo lo suyo aunque compartan nombre e instante; un
// tenant sin filas y el tenant vacío no leen nada.
func caseTenantIsolation(t *testing.T, m Montaje) {
	a1 := seed(t, m, m.TenantA, "event_started", 0)
	b1 := seed(t, m, m.TenantB, "event_started", 0)
	a2 := seed(t, m, m.TenantA, "event_closed", time.Second)
	b2 := seed(t, m, m.TenantB, "event_closed", time.Second)

	all := apipublica.EventTelemetryFilter{Limit: wideLimit}
	wantIDs(t, "TenantA", list(t, m, m.TenantA, all), a1, a2)
	wantIDs(t, "TenantB", list(t, m, m.TenantB, all), b1, b2)
	wantIDs(t, "un tenant sin filas", list(t, m, m.TenantA+m.TenantB, all))
	wantIDs(t, "el tenant vacío", list(t, m, "", all))
	// Ni el cursor ni `since` abren la puerta a las filas del otro.
	narrowed := apipublica.EventTelemetryFilter{Since: baseInstant, CursorAt: baseInstant, CursorID: 0, Limit: wideLimit}
	wantIDs(t, "TenantA con since y cursor", list(t, m, m.TenantA, narrowed), a1, a2)
}

// caseSince: `since` deja las filas con created_at >= since (incluye el instante exacto) y su
// cero no filtra.
func caseSince(t *testing.T, m Montaje) {
	r0 := seed(t, m, m.TenantA, "event_started", 0)
	r1 := seed(t, m, m.TenantA, "event_switched", time.Hour)
	r2 := seed(t, m, m.TenantA, "event_closed", 2*time.Hour)

	since := func(at time.Time) apipublica.EventTelemetryFilter {
		return apipublica.EventTelemetryFilter{Since: at, Limit: wideLimit}
	}
	wantIDs(t, "since cero", list(t, m, m.TenantA, since(time.Time{})), r0, r1, r2)
	wantIDs(t, "since anterior a todo", list(t, m, m.TenantA, since(baseInstant.Add(-time.Hour))), r0, r1, r2)
	wantIDs(t, "since en el instante exacto de una fila", list(t, m, m.TenantA, since(baseInstant.Add(time.Hour))), r1, r2)
	wantIDs(t, "since un microsegundo después de una fila", list(t, m, m.TenantA, since(baseInstant.Add(time.Hour+time.Microsecond))), r2)
	wantIDs(t, "since en la última fila", list(t, m, m.TenantA, since(baseInstant.Add(2*time.Hour))), r2)
	wantIDs(t, "since posterior a todo", list(t, m, m.TenantA, since(baseInstant.Add(3*time.Hour))))
	// La misma hora dicha en otra zona es el mismo instante.
	zoned := baseInstant.Add(time.Hour).In(time.FixedZone("+05", 5*3600))
	wantIDs(t, "since en otra zona", list(t, m, m.TenantA, since(zoned)), r1, r2)
}

// caseOrder: las filas salen por created_at ascendente aunque se insertaran en otro orden, y a
// igual instante por id ascendente.
func caseOrder(t *testing.T, m Montaje) {
	// Insertadas al revés de su instante: quien devolviera el orden de inserción (o el del id)
	// fallaría.
	late := seed(t, m, m.TenantA, "event_closed", 2*time.Hour)
	mid := seed(t, m, m.TenantA, "event_switched", time.Hour)
	early := seed(t, m, m.TenantA, "event_started", 0)
	// Tres con el MISMO instante que mid: desempata el id.
	tie1 := seed(t, m, m.TenantA, "event_started", time.Hour)
	tie2 := seed(t, m, m.TenantA, "event_started", time.Hour)
	wantGrowing(t, late, mid, early, tie1, tie2)

	rows := list(t, m, m.TenantA, apipublica.EventTelemetryFilter{Limit: wideLimit})
	wantIDs(t, "orden (created_at, id)", rows, early, mid, tie1, tie2, late)
}

// caseLimit: Limit se queda con las PRIMERAS filas del orden; uno mayor que el total las da
// todas y 0 no da ninguna.
func caseLimit(t *testing.T, m Montaje) {
	r2 := seed(t, m, m.TenantA, "event_closed", 2*time.Second)
	r0 := seed(t, m, m.TenantA, "event_started", 0)
	r1 := seed(t, m, m.TenantA, "event_switched", time.Second)
	// Ruido que no cuenta para el límite: otro tenant y un nombre que no se lee, los dos antes.
	seed(t, m, m.TenantB, "event_started", -time.Hour)
	seed(t, m, m.TenantA, "survey_answer", -time.Hour)

	limit := func(n int) []apipublica.EventTelemetryRow {
		return list(t, m, m.TenantA, apipublica.EventTelemetryFilter{Limit: n})
	}
	wantIDs(t, "Limit 1", limit(1), r0)
	wantIDs(t, "Limit 2", limit(2), r0, r1)
	wantIDs(t, "Limit 3 (justo el total)", limit(3), r0, r1, r2)
	wantIDs(t, "Limit 4 (más que el total)", limit(4), r0, r1, r2)
	wantIDs(t, "Limit 0", limit(0))
}

// caseCancelledContext: con el contexto ya cancelado la lectura devuelve un error y ninguna fila.
func caseCancelledContext(t *testing.T, m Montaje) {
	seed(t, m, m.TenantA, "event_started", 0)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rows, err := m.Reader.ListEventTelemetry(ctx, m.TenantA, apipublica.EventTelemetryFilter{Limit: wideLimit})
	if err == nil {
		t.Errorf("con el contexto cancelado ListEventTelemetry devolvió %d filas y error nil; quiero un error", len(rows))
	}
	if len(rows) != 0 {
		t.Errorf("con el contexto cancelado ListEventTelemetry devolvió %d filas; quiero 0", len(rows))
	}
}
