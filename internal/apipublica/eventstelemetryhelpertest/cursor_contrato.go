package eventstelemetryhelpertest

// cursor_contrato.go — los casos del cursor (CursorAt, CursorID) y de la paginación.

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
)

// after arma el filtro «lo que va después de (at, id)», sin `since` y sin límite que estorbe.
func after(at time.Time, id int64) apipublica.EventTelemetryFilter {
	return apipublica.EventTelemetryFilter{CursorAt: at, CursorID: id, Limit: wideLimit}
}

// caseCursorIsStrict: el cursor deja las filas ESTRICTAMENTE posteriores a (CursorAt, CursorID):
// la fila del cursor no se repite, y entre filas con el mismo instante manda el id.
func caseCursorIsStrict(t *testing.T, m Montaje) {
	t0, t1, t2 := baseInstant, baseInstant.Add(time.Minute), baseInstant.Add(2*time.Minute)
	first := seed(t, m, m.TenantA, "event_started", 0)
	// Tres filas con el MISMO instante t1.
	tie1 := seed(t, m, m.TenantA, "event_switched", time.Minute)
	tie2 := seed(t, m, m.TenantA, "event_switched", time.Minute)
	tie3 := seed(t, m, m.TenantA, "event_switched", time.Minute)
	last := seed(t, m, m.TenantA, "event_closed", 2*time.Minute)
	wantGrowing(t, first, tie1, tie2, tie3, last)

	wantIDs(t, "cursor en la primera fila", list(t, m, m.TenantA, after(t0, first)), tie1, tie2, tie3, last)
	wantIDs(t, "cursor en la primera del empate", list(t, m, m.TenantA, after(t1, tie1)), tie2, tie3, last)
	wantIDs(t, "cursor en la del medio del empate", list(t, m, m.TenantA, after(t1, tie2)), tie3, last)
	wantIDs(t, "cursor en la última del empate", list(t, m, m.TenantA, after(t1, tie3)), last)
	wantIDs(t, "cursor en la última fila", list(t, m, m.TenantA, after(t2, last)))
	// El cursor no tiene que nombrar una fila que exista: es una posición en el orden.
	wantIDs(t, "cursor con id 0 en el instante del empate", list(t, m, m.TenantA, after(t1, 0)), tie1, tie2, tie3, last)
	wantIDs(t, "cursor con id negativo en el instante del empate", list(t, m, m.TenantA, after(t1, -5)), tie1, tie2, tie3, last)
	wantIDs(t, "cursor con el id máximo en el instante del empate", list(t, m, m.TenantA, after(t1, math.MaxInt64)), last)
	wantIDs(t, "cursor entre dos instantes", list(t, m, m.TenantA, after(t1.Add(time.Second), 0)), last)
	wantIDs(t, "cursor un microsegundo antes del empate", list(t, m, m.TenantA, after(t1.Add(-time.Microsecond), math.MaxInt64)), tie1, tie2, tie3, last)
	wantIDs(t, "cursor posterior a todo", list(t, m, m.TenantA, after(t2.Add(time.Hour), 0)))
	// Con límite, el cursor da las PRIMERAS de lo que queda.
	limited := after(t1, tie1)
	limited.Limit = 2
	wantIDs(t, "cursor con Limit 2", list(t, m, m.TenantA, limited), tie2, tie3)
}

// caseCursorComparesThePair: (created_at, id) > (CursorAt, CursorID) se compara como PAR, no
// parte a parte: una fila posterior con id MENOR que el del cursor entra, y una anterior con id
// MAYOR no.
func caseCursorComparesThePair(t *testing.T, m Montaje) {
	t1 := baseInstant.Add(time.Minute)
	// Insertadas con el id al revés que el instante.
	lateSmallID := seed(t, m, m.TenantA, "event_closed", 2*time.Minute)
	midA := seed(t, m, m.TenantA, "event_switched", time.Minute)
	midB := seed(t, m, m.TenantA, "event_switched", time.Minute)
	earlyBigID := seed(t, m, m.TenantA, "event_started", 0)
	wantGrowing(t, lateSmallID, midA, midB, earlyBigID)

	// Cursor en midB: lateSmallID tiene un id menor que el del cursor y aun así va después.
	wantIDs(t, "una fila posterior con id menor que el del cursor", list(t, m, m.TenantA, after(t1, midB)), lateSmallID)
	// Cursor en midA: earlyBigID tiene un id mayor que el del cursor y aun así va antes.
	wantIDs(t, "una fila anterior con id mayor que el del cursor", list(t, m, m.TenantA, after(t1, midA)), midB, lateSmallID)
	// Cursor en la más antigua, que lleva el id más alto: quedan todas las demás.
	wantIDs(t, "cursor en la fila más antigua, la de id más alto", list(t, m, m.TenantA, after(baseInstant, earlyBigID)), midA, midB, lateSmallID)
}

// caseCursorZeroInstant: el cursor lo enciende CursorAt; con CursorAt en cero, CursorID no
// cuenta, valga lo que valga.
func caseCursorZeroInstant(t *testing.T, m Montaje) {
	r0 := seed(t, m, m.TenantA, "event_started", 0)
	r1 := seed(t, m, m.TenantA, "event_closed", time.Second)
	for _, id := range []int64{0, r0, r1, math.MaxInt64, -1} {
		rows := list(t, m, m.TenantA, after(time.Time{}, id))
		wantIDs(t, "CursorAt cero", rows, r0, r1)
	}
}

// caseSinceAndCursor: `since` y el cursor se aplican los dos (Y, no O), cada uno por su lado.
func caseSinceAndCursor(t *testing.T, m Montaje) {
	r0 := seed(t, m, m.TenantA, "event_started", 0)
	r1 := seed(t, m, m.TenantA, "event_switched", time.Minute)
	r2 := seed(t, m, m.TenantA, "event_switched", 2*time.Minute)
	r3 := seed(t, m, m.TenantA, "event_closed", 3*time.Minute)
	at := func(minutes int) time.Time { return baseInstant.Add(time.Duration(minutes) * time.Minute) }

	both := func(since time.Time, cursorAt time.Time, cursorID int64) []apipublica.EventTelemetryRow {
		return list(t, m, m.TenantA, apipublica.EventTelemetryFilter{Since: since, CursorAt: cursorAt, CursorID: cursorID, Limit: wideLimit})
	}
	wantIDs(t, "since más estrecho que el cursor", both(at(2), at(0), r0), r2, r3)
	wantIDs(t, "cursor más estrecho que since", both(at(1), at(2), r2), r3)
	wantIDs(t, "since en la fila del cursor", both(at(1), at(1), r1), r2, r3)
	wantIDs(t, "since posterior a todo con cursor al principio", both(at(9), at(0), r0))
	wantIDs(t, "cursor al final con since al principio", both(at(0), at(3), r3))
}

// casePagingWalk: paginar con el cursor de la última fila de cada página recorre TODAS las filas
// exactamente una vez y en orden, con empates que cruzan el borde de página; y leer no cambia
// nada (la lectura entera de antes y la de después son iguales).
func casePagingWalk(t *testing.T, m Montaje) {
	// Diez filas en cuatro instantes: 1, 4, 3 y 2 por instante. Con páginas de 3, el empate de
	// cuatro y el de tres quedan partidos entre dos páginas.
	offsets := []time.Duration{
		0,
		time.Minute, time.Minute, time.Minute, time.Minute,
		2 * time.Minute, 2 * time.Minute, 2 * time.Minute,
		3 * time.Minute, 3 * time.Minute,
	}
	want := make([]int64, 0, len(offsets))
	for _, offset := range offsets {
		want = append(want, seed(t, m, m.TenantA, "event_started", offset))
	}
	wantGrowing(t, want...)
	// Ruido del otro tenant intercalado en los mismos instantes.
	seed(t, m, m.TenantB, "event_started", time.Minute)
	seed(t, m, m.TenantB, "event_started", 2*time.Minute)

	before := list(t, m, m.TenantA, apipublica.EventTelemetryFilter{Limit: wideLimit})
	wantIDs(t, "lectura entera", before, want...)

	for _, size := range []int{1, 3, 4, 10, 11} {
		var walked []int64
		filter := apipublica.EventTelemetryFilter{Limit: size}
		for page := 0; ; page++ {
			if page > len(want) {
				t.Fatalf("páginas de %d: más de %d páginas para %d filas; el cursor no avanza (%v)", size, page, len(want), walked)
			}
			rows := list(t, m, m.TenantA, filter)
			if len(rows) > size {
				t.Fatalf("páginas de %d: la página %d trae %d filas", size, page, len(rows))
			}
			walked = append(walked, idsOf(rows)...)
			if len(rows) < size {
				break
			}
			tail := rows[len(rows)-1]
			filter.CursorAt, filter.CursorID = tail.CreatedAt, tail.ID
		}
		if !slices.Equal(walked, want) {
			t.Errorf("páginas de %d: recorrido %v; quiero %v, cada fila una vez y en orden", size, walked, want)
		}
	}

	afterWalk := list(t, m, m.TenantA, apipublica.EventTelemetryFilter{Limit: wideLimit})
	if !slices.EqualFunc(before, afterWalk, func(a, b apipublica.EventTelemetryRow) bool {
		return a.ID == b.ID && a.Name == b.Name && a.EventKind == b.EventKind &&
			string(a.Payload) == string(b.Payload) && a.CreatedAt.Equal(b.CreatedAt)
	}) {
		t.Errorf("leer cambió lo leído:\n  antes   %+v\n  después %+v", before, afterWalk)
	}
}
