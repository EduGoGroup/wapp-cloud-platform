package degradationhelpertest

// Los casos de List —lista vacía, orden, filtro «sin leer», página y sus techos— y el
// aislamiento por tenant (INV-7).

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
)

// requireList afirma que List con ese filtro devuelve exactamente want, en ese orden.
func requireList(t *testing.T, m Montaje, tenant string, f degradation.ListFilter, want ...degradation.Notice) {
	t.Helper()
	if got := list(t, m, tenant, f); !sameNotices(got, want) {
		t.Errorf("List(%s, %+v):%s\nquería, en este orden:%s", tenant, f, describe(got), describe(want))
	}
}

// seedWindows deja al tenant con count avisos, uno por ventana (de la 0 a la count-1), y los
// devuelve como los enseña List: el de la ventana más reciente primero.
func seedWindows(t *testing.T, m Montaje, tenant string, count int) []degradation.Notice {
	t.Helper()
	for i := range count {
		mustCreate(t, m, failure(tenant, degradation.ReasonTimeout, degradation.ViaLocal, windowAt(i), windowAt(i).Add(time.Minute)))
	}
	newestFirst := list(t, m, tenant, degradation.ListFilter{Limit: count})
	if len(newestFirst) != min(count, 200) {
		t.Fatalf("tras sembrar %d ventanas, List devolvió %d avisos", count, len(newestFirst))
	}
	for i, n := range newestFirst {
		if want := windowAt(count - 1 - i); !n.WindowStart.Equal(want) {
			t.Fatalf("el aviso %d de la lista es de la ventana %s, quería %s (la más reciente primero)", i, n.WindowStart, want)
		}
	}
	return newestFirst
}

// caseListWithoutNoticesEmptyNotNil: un tenant sin avisos no es un error ni un nil: es una lista
// vacía, con cualquier filtro, para que quien la serialice produzca `[]` y no `null`.
func caseListWithoutNoticesEmptyNotNil(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	for _, f := range []degradation.ListFilter{
		{}, {SoloSinLeer: true}, {Limit: 10, Offset: 5}, {Limit: -1, Offset: -1}, {SoloSinLeer: true, Limit: 500},
	} {
		if got := list(t, m, tenant, f); len(got) != 0 {
			t.Errorf("List(%+v) de un tenant sin avisos devolvió %d:%s", f, len(got), describe(got))
		}
	}
	if rows := m.Rows(t, tenant); len(rows) != 0 {
		t.Errorf("un tenant recién sembrado tiene %d filas: el Montaje no vino limpio, o List escribió", len(rows))
	}
}

// caseListReturnsWholeNotice: List enseña el aviso entero —las diez columnas, con el id que le
// puso el store— y un aviso recién escrito llega SIN leer: nada escribe la lectura, tampoco
// listar, con ningún filtro.
func caseListReturnsWholeNotice(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	n := failure(tenant, degradation.ReasonAPIError, degradation.ViaAPI, baseWindow, baseWindow.Add(time.Minute))
	mustCreate(t, m, n)
	mustCollapse(t, m, failure(tenant, degradation.ReasonAPIError, degradation.ViaAPI, baseWindow, baseWindow.Add(4*time.Minute)))

	want := born(n)
	want.Occurrences, want.LastSeenAt = 2, baseWindow.Add(4*time.Minute)
	row := requireOnlyRow(t, m, tenant, want)
	before := captureState(t, m, tenant)

	for _, f := range []degradation.ListFilter{{}, {SoloSinLeer: true}, {Limit: 1}, {SoloSinLeer: true, Limit: 200}} {
		requireList(t, m, tenant, f, row)
	}
	if got := list(t, m, tenant, degradation.ListFilter{}); len(got) == 1 && !got[0].ReadAt.IsZero() {
		t.Errorf("un aviso recién escrito llegó leído (ReadAt = %s): nada debe escribir read_at", got[0].ReadAt)
	}
	requireSameState(t, m, tenant, before)
}

// caseListNewestFirst: el más reciente primero: por inicio de ventana descendente y, dentro de
// la misma ventana, por nacimiento descendente. El orden no depende del orden de escritura ni
// del último visto: un aviso viejo que sigue acumulando fallos no adelanta a uno más nuevo.
func caseListNewestFirst(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	oldest := failure(tenant, degradation.ReasonTimeout, degradation.ViaLocal, windowAt(0), windowAt(0).Add(time.Minute))
	middleEarly := failure(tenant, degradation.ReasonOllamaDown, degradation.ViaLocal, windowAt(1), windowAt(1).Add(time.Minute))
	middleLate := failure(tenant, degradation.ReasonCredencial, degradation.ViaAPI, windowAt(1), windowAt(1).Add(5*time.Minute))
	newest := failure(tenant, degradation.ReasonTimeout, degradation.ViaLocal, windowAt(2), windowAt(2).Add(time.Minute))

	for _, n := range []degradation.Notice{middleEarly, oldest, newest, middleLate} { // desordenados
		mustCreate(t, m, n)
	}
	requireList(t, m, tenant, degradation.ListFilter{},
		withID(t, m, born(newest)), withID(t, m, born(middleLate)), withID(t, m, born(middleEarly)), withID(t, m, born(oldest)))

	// El más viejo sigue viendo fallos, hasta más tarde que ninguno: no cambia de sitio. Y el
	// que nació antes dentro de su ventana tampoco adelanta al que nació después.
	stillFailing := oldest
	stillFailing.LastSeenAt = windowAt(3)
	mustCollapse(t, m, stillFailing)
	earlyAgain := middleEarly
	earlyAgain.LastSeenAt = windowAt(1).Add(9 * time.Minute)
	mustCollapse(t, m, earlyAgain)

	wantOldest, wantEarly := born(oldest), born(middleEarly)
	wantOldest.Occurrences, wantOldest.LastSeenAt = 2, stillFailing.LastSeenAt
	wantEarly.Occurrences, wantEarly.LastSeenAt = 2, earlyAgain.LastSeenAt
	requireList(t, m, tenant, degradation.ListFilter{},
		withID(t, m, born(newest)), withID(t, m, born(middleLate)), withID(t, m, wantEarly), withID(t, m, wantOldest))
}

// withID devuelve want con el id que tiene en el store la fila de su misma clave (tenant,
// motivo, vía, inicio de ventana). La busca comparando la clave entera, como un valor.
func withID(t *testing.T, m Montaje, want degradation.Notice) degradation.Notice {
	t.Helper()
	for _, row := range m.Rows(t, want.TenantID) {
		if sortKey(row) == sortKey(want) {
			want.ID = row.ID
			return want
		}
	}
	t.Fatalf("el store no tiene el aviso (%s)", name(want))
	return want
}

// caseListOnlyUnread: SoloSinLeer deja fuera los avisos ya leídos y conserva el orden del resto;
// sin el filtro salen todos, y el leído dice cuándo se leyó.
func caseListOnlyUnread(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	all := seedWindows(t, m, tenant, 4) // [3, 2, 1, 0]
	requireList(t, m, tenant, degradation.ListFilter{SoloSinLeer: true}, all...)

	m.MarkRead(t, tenant, all[1].ID, readInstant)
	m.MarkRead(t, tenant, all[3].ID, readInstant.Add(time.Hour))
	all[1].ReadAt, all[3].ReadAt = readInstant, readInstant.Add(time.Hour)

	requireList(t, m, tenant, degradation.ListFilter{}, all...)
	requireList(t, m, tenant, degradation.ListFilter{SoloSinLeer: true}, all[0], all[2])
	// El filtro se aplica ANTES que la página: la segunda página de los sin leer es el segundo
	// sin leer, no el segundo aviso.
	requireList(t, m, tenant, degradation.ListFilter{SoloSinLeer: true, Limit: 1, Offset: 1}, all[2])

	m.MarkRead(t, tenant, all[0].ID, readInstant)
	m.MarkRead(t, tenant, all[2].ID, readInstant)
	requireList(t, m, tenant, degradation.ListFilter{SoloSinLeer: true})
}

// caseListPages: Limit y Offset recorren la lista sin saltar ni repetir un aviso; pasado el
// final, la página es vacía (y no nil).
func caseListPages(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	all := seedWindows(t, m, tenant, 5)

	requireList(t, m, tenant, degradation.ListFilter{Limit: 2}, all[0:2]...)
	requireList(t, m, tenant, degradation.ListFilter{Limit: 2, Offset: 2}, all[2:4]...)
	requireList(t, m, tenant, degradation.ListFilter{Limit: 2, Offset: 4}, all[4:5]...)
	requireList(t, m, tenant, degradation.ListFilter{Limit: 2, Offset: 5})
	requireList(t, m, tenant, degradation.ListFilter{Limit: 2, Offset: 99})
	requireList(t, m, tenant, degradation.ListFilter{Limit: 1, Offset: 3}, all[3])
	requireList(t, m, tenant, degradation.ListFilter{Limit: 5}, all...)
	requireList(t, m, tenant, degradation.ListFilter{Limit: 100, Offset: 1}, all[1:]...)
}

// caseListBoundsThePage: los techos de la página. Sin límite (o con uno <= 0) son 50 avisos; por
// encima de 200 se RECORTA a 200 en silencio —no es un error: quien pide «todo» es una pantalla,
// y dejarla en blanco por pedir 500 no le sirve—; un desplazamiento negativo es cero.
func caseListBoundsThePage(t *testing.T, m Montaje) {
	const total = 205
	tenant := seedTenant(t, m)
	for i := range total {
		mustCreate(t, m, failure(tenant, degradation.ReasonTimeout, degradation.ViaLocal, windowAt(i), windowAt(i).Add(time.Minute)))
	}
	cases := []struct {
		name      string
		filter    degradation.ListFilter
		wantLen   int
		wantFirst int // la ventana del primer aviso de la página
	}{
		{"no limit is fifty", degradation.ListFilter{}, 50, total - 1},
		{"negative limit is fifty", degradation.ListFilter{Limit: -8}, 50, total - 1},
		{"fifty one is fifty one", degradation.ListFilter{Limit: 51}, 51, total - 1},
		{"the cap itself", degradation.ListFilter{Limit: 200}, 200, total - 1},
		{"just over the cap is trimmed", degradation.ListFilter{Limit: 201}, 200, total - 1},
		{"far over the cap is trimmed", degradation.ListFilter{Limit: 5000}, 200, total - 1},
		{"negative offset is zero", degradation.ListFilter{Offset: -7}, 50, total - 1},
		{"the default page moves with the offset", degradation.ListFilter{Offset: 50}, 50, total - 51},
		{"what is left after the cap", degradation.ListFilter{Limit: 5000, Offset: 200}, 5, 4},
		{"only unread has the same bounds", degradation.ListFilter{SoloSinLeer: true, Limit: 5000, Offset: -1}, 200, total - 1},
	}
	for _, c := range cases {
		got := list(t, m, tenant, c.filter)
		if len(got) != c.wantLen {
			t.Errorf("%s: List(%+v) devolvió %d avisos, quería %d", c.name, c.filter, len(got), c.wantLen)
			continue
		}
		for i, n := range got {
			if want := windowAt(c.wantFirst - i); !n.WindowStart.Equal(want) {
				t.Errorf("%s: el aviso %d de List(%+v) es de la ventana %s, quería %s", c.name, i, c.filter, n.WindowStart, want)
				break
			}
		}
	}
}

// caseNoticesIsolatedByTenant (INV-7): cada tenant solo ve y solo toca lo suyo. El mismo fallo
// en el mismo instante de dos tenants son dos avisos, no uno colapsado; List de uno no trae los
// del otro; y ni los Save ni las lecturas marcadas del ajeno cambian una columna del primero.
func caseNoticesIsolatedByTenant(t *testing.T, m Montaje) {
	owner, other := seedTenant(t, m), seedTenant(t, m)
	if owner == other {
		t.Fatalf("Montaje.SeedTenant devolvió dos veces el mismo tenant, %s", owner)
	}
	at := baseWindow.Add(time.Minute)
	mine := failure(owner, degradation.ReasonAPIError, degradation.ViaAPI, baseWindow, at)
	mustCreate(t, m, mine)
	mustCreate(t, m, failure(owner, degradation.ReasonTimeout, degradation.ViaLocal, windowAt(1), windowAt(1)))
	ownerState := captureState(t, m, owner)
	ownerList := list(t, m, owner, degradation.ListFilter{})

	// El ajeno no ve nada.
	requireList(t, m, other, degradation.ListFilter{})
	requireRows(t, m, other)

	// El ajeno tiene el MISMO fallo en el mismo instante: nace, no colapsa sobre el del primero.
	theirs := failure(other, degradation.ReasonAPIError, degradation.ViaAPI, baseWindow, at)
	mustCreate(t, m, theirs)
	requireSameState(t, m, owner, ownerState)

	// El ajeno acumula fallos y lee su aviso: el primero no se entera.
	theirs.LastSeenAt = baseWindow.Add(9 * time.Minute)
	mustCollapse(t, m, theirs)
	wantTheirs := born(failure(other, degradation.ReasonAPIError, degradation.ViaAPI, baseWindow, at))
	wantTheirs.Occurrences, wantTheirs.LastSeenAt = 2, theirs.LastSeenAt
	theirRow := requireOnlyRow(t, m, other, wantTheirs)
	m.MarkRead(t, other, theirRow.ID, readInstant)
	requireSameState(t, m, owner, ownerState)

	// Cada lista trae lo de su tenant y nada más (list ya falla si ve un aviso de otro).
	requireList(t, m, owner, degradation.ListFilter{}, ownerList...)
	requireList(t, m, owner, degradation.ListFilter{SoloSinLeer: true}, ownerList...)
	theirRow.ReadAt = readInstant
	requireList(t, m, other, degradation.ListFilter{}, theirRow)
	requireList(t, m, other, degradation.ListFilter{SoloSinLeer: true})
}
