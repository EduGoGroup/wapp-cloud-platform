package intakeshelpertest

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// seedInSession siembra una solicitud sin líneas en esa sesión, con el evento `cancelled`.
func seedInSession(t *testing.T, m Montaje, tenant, status, session string, d int) ref {
	t.Helper()
	in := header(status, d, nil)
	in.SessionID, in.Total = session, 18000
	return seedHeader(t, m, tenant, in, eventCancelled)
}

// seedTray siembra en TenantA cuatro solicitudes repartidas en fechas, estados y sesiones, de la
// más vieja a la más nueva, y una más en TenantB para que el aislamiento signifique algo:
//
//	[0] día 1 · `closed` (legada) · sess-a      [2] día 3 · `cancelled` · sess-b
//	[1] día 2 · `open`            · sess-a      [3] día 4 · `confirmed` · sess-a
func seedTray(t *testing.T, m Montaje) (tray [4]ref, foreign ref) {
	t.Helper()
	tray = [4]ref{
		seedInSession(t, m, m.TenantA, intakes.StatusClosedLegacy, sessionA, 1),
		seedInSession(t, m, m.TenantA, intakes.StatusOpen, sessionA, 2),
		seedInSession(t, m, m.TenantA, intakes.StatusCancelled, sessionB, 3),
		seedInSession(t, m, m.TenantA, intakes.StatusConfirmed, sessionA, 4),
	}
	return tray, seedInSession(t, m, m.TenantB, intakes.StatusConfirmed, sessionA, 2)
}

// fullHeader es una cabecera con TODOS sus campos poblados y distintos entre sí: un Scan corrido
// o un campo que se queda sin copiar se delata.
func fullHeader(status string, items []intakes.Item) intakes.Intake {
	in := header(status, 5, items)
	in.ContactID, in.SessionID = contactB, sessionB
	in.UpdatedAt = day(6)
	in.CustomerNote = "dejarlo en portería"
	in.DepositDueAt, in.DepositRemindedAt, in.ExpiryRemindedAt = day(7), day(8), day(9)
	return in
}

func caseGetReturnsEverything(t *testing.T, m Montaje) {
	lines := append(customerLines(), shippingLine("Envío — Providencia", 3000))
	in := fullHeader(intakes.StatusClosedLegacy, lines)
	r := seedHeader(t, m, m.TenantA, in, eventCancelled, lines...)
	first := insertRevision(t, m, r.id, intakes.RevisionKindCart, intakes.RevisionBySystem, "")
	m.Advance(t)
	second := insertRevision(t, m, r.id, intakes.RevisionKindInterpreted, intakes.RevisionBySystem, "Tu pedido: 2 panes")

	got := take(t, m, r)
	want := snapshot{stored: intakes.StatusClosedLegacy, event: eventCancelled}
	want.detail.Intake = in
	want.detail.Status = intakes.StatusConfirmed // el `closed` guardado se lee normalizado
	want.detail.Items = lines
	want.detail.Revisions = []intakes.Revision{first, second}
	requireSame(t, "Get", got, want)
	if first.RevisionNo != 1 || second.RevisionNo != 2 {
		t.Errorf("revisiones numeradas %d y %d, quería 1 y 2", first.RevisionNo, second.RevisionNo)
	}
}

func caseGetNotFound(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	for name, target := range map[string]ref{
		"de otro tenant":    {tenant: m.TenantA, id: w.foreign().id},
		"inexistente":       {tenant: m.TenantA, id: uuid.NewString()},
		"id que no es UUID": {tenant: m.TenantA, id: "no-soy-un-uuid"},
		"id vacío":          {tenant: m.TenantA, id: ""},
		"tenant sin nada":   {tenant: uuid.NewString(), id: w.foreign().id},
	} {
		got, err := m.Store.Get(bg(), target.tenant, target.id)
		if !errors.Is(err, intakes.ErrNotFound) {
			t.Errorf("Get de una solicitud %s: err = %v, quería ErrNotFound", name, err)
		}
		if got.ID != "" || len(got.Items) != 0 || len(got.Revisions) != 0 {
			t.Errorf("Get de una solicitud %s devolvió contenido: %+v", name, got)
		}
	}
	w.requireUntouched(t, m)
}

func caseListNewestFirst(t *testing.T, m Montaje) {
	tray, _ := seedTray(t, m)
	got, total, err := m.Store.List(bg(), m.TenantA, intakes.Filter{})
	if err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	if total != 4 || len(got) != 4 {
		t.Fatalf("List sin filtro: %d filas y total %d, quería 4 y 4", len(got), total)
	}
	wantStatus := []string{intakes.StatusConfirmed, intakes.StatusCancelled, intakes.StatusOpen, intakes.StatusConfirmed}
	for i, in := range got {
		if want := tray[3-i].id; in.ID != want {
			t.Errorf("fila %d = %s, quería %s (más recientes primero)", i, in.ID, want)
		}
		if in.Status != wantStatus[i] {
			t.Errorf("fila %d: Status = %q, quería %q (normalizado al leer)", i, in.Status, wantStatus[i])
		}
	}
}

// caseListSortAndTieBreak: dos solicitudes creadas en el MISMO instante entre una más vieja y una
// más nueva. El id desempata, y gira con el orden: descendente en `newest`, ascendente en `oldest`.
func caseListSortAndTieBreak(t *testing.T, m Montaje) {
	ids := sortedIDs(2)
	older := seedInSession(t, m, m.TenantA, intakes.StatusOpen, sessionA, 1)
	for _, id := range []string{ids[1], ids[0]} { // sembradas al revés: el orden no es el de alta
		in := header(intakes.StatusOpen, 5, nil)
		in.ID = id
		seedHeader(t, m, m.TenantA, in, eventCancelled)
	}
	newer := seedInSession(t, m, m.TenantA, intakes.StatusOpen, sessionA, 9)

	got, total := list(t, m, m.TenantA, intakes.Filter{Sort: intakes.SortNewest})
	requireIDs(t, "newest", got, total, 4, newer.id, ids[1], ids[0], older.id)
	got, total = list(t, m, m.TenantA, intakes.Filter{})
	requireIDs(t, "sin orden pedido (newest)", got, total, 4, newer.id, ids[1], ids[0], older.id)
	got, total = list(t, m, m.TenantA, intakes.Filter{Sort: intakes.SortOldest})
	requireIDs(t, "oldest", got, total, 4, older.id, ids[0], ids[1], newer.id)

	details, err := m.Store.ListDetails(bg(), m.TenantA, intakes.Filter{Sort: intakes.SortOldest}, 10)
	if err != nil || len(details) != 4 {
		t.Fatalf("ListDetails(oldest): %d solicitudes y err %v, quería 4", len(details), err)
	}
	for i, want := range []string{older.id, ids[0], ids[1], newer.id} {
		if details[i].ID != want {
			t.Errorf("ListDetails(oldest): posición %d = %s, quería %s (el mismo orden que List)", i, details[i].ID, want)
		}
	}
}

// caseListStatusFilter: filtrar por `confirmed` alcanza las filas guardadas como `closed`, y la
// expansión es de CADA estado de la lista (`confirmed` va el último a propósito).
func caseListStatusFilter(t *testing.T, m Montaje) {
	tray, _ := seedTray(t, m)
	got, total := list(t, m, m.TenantA, intakes.Filter{Statuses: []string{intakes.StatusConfirmed}})
	requireIDs(t, "confirmed", got, total, 2, tray[3].id, tray[0].id)
	got, total = list(t, m, m.TenantA, intakes.Filter{Statuses: []string{intakes.StatusOpen, intakes.StatusConfirmed}})
	requireIDs(t, "open + confirmed", got, total, 3, tray[3].id, tray[1].id, tray[0].id)
	got, total = list(t, m, m.TenantA, intakes.Filter{Statuses: []string{intakes.StatusClosedLegacy}})
	requireIDs(t, "closed (se normaliza a confirmed)", got, total, 2, tray[3].id, tray[0].id)
	got, total = list(t, m, m.TenantA, intakes.Filter{Statuses: []string{intakes.StatusAbandoned}})
	requireIDs(t, "abandoned (ninguna)", got, total, 0)
}

func caseListDateRangeAndSession(t *testing.T, m Montaje) {
	tray, _ := seedTray(t, m)
	// From cae EXACTAMENTE en la del día 2 (entra) y To EXACTAMENTE en la del día 4 (no entra).
	got, total := list(t, m, m.TenantA, intakes.Filter{From: day(2), To: day(4)})
	requireIDs(t, "[día 2, día 4)", got, total, 2, tray[2].id, tray[1].id)
	got, total = list(t, m, m.TenantA, intakes.Filter{From: day(2), To: day(4), SessionID: sessionA})
	requireIDs(t, "[día 2, día 4) de sess-a", got, total, 1, tray[1].id)
	got, total = list(t, m, m.TenantA, intakes.Filter{SessionID: sessionB})
	requireIDs(t, "sess-b", got, total, 1, tray[2].id)
	got, total = list(t, m, m.TenantA, intakes.Filter{To: day(1)})
	requireIDs(t, "antes del día 1 (ninguna)", got, total, 0)
}

// caseListPagination: el total es el de las coincidencias del filtro, no el de la página.
func caseListPagination(t *testing.T, m Montaje) {
	tray, _ := seedTray(t, m)
	got, total := list(t, m, m.TenantA, intakes.Filter{Page: 1, PageSize: 3})
	requireIDs(t, "página 1 de 3", got, total, 4, tray[3].id, tray[2].id, tray[1].id)
	got, total = list(t, m, m.TenantA, intakes.Filter{Page: 2, PageSize: 3})
	requireIDs(t, "página 2 de 3", got, total, 4, tray[0].id)
	got, total = list(t, m, m.TenantA, intakes.Filter{Page: 3, PageSize: 3})
	requireIDs(t, "página más allá del final", got, total, 4)
	got, total = list(t, m, m.TenantA, intakes.Filter{Page: 0, PageSize: 0})
	requireIDs(t, "página y tamaño a cero (saneados)", got, total, 4, tray[3].id, tray[2].id, tray[1].id, tray[0].id)
	got, total = list(t, m, m.TenantA, intakes.Filter{Page: 2, PageSize: 1, Statuses: []string{intakes.StatusConfirmed}})
	requireIDs(t, "página 2 del filtro por confirmed", got, total, 2, tray[0].id)
}

// caseListOrphan: huérfana es la que declara un evento que ya NO está `open`, sea cual sea su
// estado terminal. Sin pedir huérfanas salen todas: el filtro no se come filas.
func caseListOrphan(t *testing.T, m Montaje) {
	cancelled := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled)
	live := seed(t, m, m.TenantA, intakes.StatusOpen, 2, eventOpen)
	closed := seed(t, m, m.TenantA, intakes.StatusOpen, 3, eventClosed)

	got, total := list(t, m, m.TenantA, intakes.Filter{Orphan: true})
	requireIDs(t, "huérfanas", got, total, 2, closed.id, cancelled.id)
	got, total = list(t, m, m.TenantA, intakes.Filter{})
	requireIDs(t, "sin pedir huérfanas", got, total, 3, closed.id, live.id, cancelled.id)

	details, err := m.Store.ListDetails(bg(), m.TenantA, intakes.Filter{Orphan: true}, 50)
	if err != nil {
		t.Fatalf("ListDetails de huérfanas: error inesperado %v", err)
	}
	if len(details) != 2 || details[0].ID != closed.id || details[1].ID != cancelled.id {
		t.Errorf("ListDetails de huérfanas = %d solicitudes, quería las dos huérfanas en orden", len(details))
	}
}

func caseListTenantIsolation(t *testing.T, m Montaje) {
	_, foreign := seedTray(t, m)
	got, total := list(t, m, m.TenantB, intakes.Filter{})
	requireIDs(t, "el otro tenant", got, total, 1, foreign.id)
	got, total = list(t, m, uuid.NewString(), intakes.Filter{})
	requireIDs(t, "un tenant sin nada", got, total, 0)

	details, err := m.Store.ListDetails(bg(), m.TenantB, intakes.Filter{}, intakes.MaxExportIntakes)
	if err != nil || len(details) != 1 || details[0].ID != foreign.id {
		t.Errorf("ListDetails del otro tenant = %d solicitudes (err %v), quería solo la suya", len(details), err)
	}
	details, err = m.Store.ListDetails(bg(), uuid.NewString(), intakes.Filter{}, intakes.MaxExportIntakes)
	if err != nil || len(details) != 0 {
		t.Errorf("ListDetails de un tenant sin nada = %d solicitudes (err %v), quería 0", len(details), err)
	}
}

// seedTrayWithLines siembra tres solicitudes —una CON dos líneas y una revisión, dos SIN nada— y
// las devuelve de la más nueva a la más vieja, junto con las dos líneas.
func seedTrayWithLines(t *testing.T, m Montaje) (tray [3]ref, lines []intakes.Item) {
	t.Helper()
	lines = []intakes.Item{
		{SKU: "torta", Label: "Torta 10-12 porciones", Customization: "sin sal", Qty: 1, UnitPrice: 18000, AddedAt: lineAt(0)},
		{SKU: intakes.ShippingSKU, Label: "Envío — Providencia", Qty: 2, UnitPrice: 3000, AddedAt: lineAt(1)},
	}
	old := seedInSession(t, m, m.TenantA, intakes.StatusCancelled, sessionB, 1)
	bare := seedInSession(t, m, m.TenantA, intakes.StatusOpen, sessionA, 2)
	with := seed(t, m, m.TenantA, intakes.StatusClosedLegacy, 3, eventCancelled, lines...)
	insertRevision(t, m, with.id, intakes.RevisionKindCart, intakes.RevisionBySystem, "")
	return [3]ref{with, bare, old}, lines
}

// caseListDetailsLines: las solicitudes SIN líneas también salen, y cada línea cuelga de SU
// cabecera, en orden de alta. Ni revisiones ni nada del comprador viajan por este camino.
func caseListDetailsLines(t *testing.T, m Montaje) {
	tray, lines := seedTrayWithLines(t, m)
	got, err := m.Store.ListDetails(bg(), m.TenantA, intakes.Filter{}, intakes.MaxExportIntakes)
	if err != nil {
		t.Fatalf("ListDetails: error inesperado %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListDetails: %d solicitudes, quería 3 (las que no tienen líneas también salen)", len(got))
	}
	for i, d := range got {
		if d.ID != tray[i].id {
			t.Errorf("posición %d = %s, quería %s (más recientes primero)", i, d.ID, tray[i].id)
		}
		if len(d.Revisions) != 0 || d.BuyerDataPresent {
			t.Errorf("posición %d: trae %d revisiones y BuyerDataPresent=%v; el export no publica eso",
				i, len(d.Revisions), d.BuyerDataPresent)
		}
	}
	if got[0].Status != intakes.StatusConfirmed {
		t.Errorf("Status = %q, quería confirmed (el `closed` guardado se lee normalizado)", got[0].Status)
	}
	requireSameItems(t, "las líneas de la primera", got[0].Items, lines)
	if len(got[1].Items) != 0 || len(got[2].Items) != 0 {
		t.Errorf("solicitudes sin líneas con líneas colgadas: %+v / %+v", got[1].Items, got[2].Items)
	}
}

// caseListDetailsLimitAndFilter: el límite corta CABECERAS, no líneas; el predicado es el de List;
// Page y PageSize se ignoran.
func caseListDetailsLimitAndFilter(t *testing.T, m Montaje) {
	tray, lines := seedTrayWithLines(t, m)
	count := func(what string, f intakes.Filter, limit, want int) []intakes.Detail {
		t.Helper()
		got, err := m.Store.ListDetails(bg(), m.TenantA, f, limit)
		if err != nil {
			t.Fatalf("ListDetails (%s): error inesperado %v", what, err)
		}
		if got == nil || len(got) != want {
			t.Errorf("ListDetails (%s): %d solicitudes (nil=%v), quería %d y no nil", what, len(got), got == nil, want)
		}
		return got
	}
	if got := count("limit 1", intakes.Filter{}, 1, 1); len(got) == 1 {
		if got[0].ID != tray[0].id {
			t.Errorf("limit 1 devolvió %s, quería la más reciente, %s", got[0].ID, tray[0].id)
		}
		requireSameItems(t, "limit 1: la solicitud llega con TODAS sus líneas", got[0].Items, lines)
	}
	count("limit 2", intakes.Filter{}, 2, 2)
	count("de sess-a", intakes.Filter{SessionID: sessionA}, intakes.MaxExportIntakes, 2)
	count("por estado legado", intakes.Filter{Statuses: []string{intakes.StatusConfirmed}}, intakes.MaxExportIntakes, 1)
	count("Page y PageSize se ignoran", intakes.Filter{Page: 2, PageSize: 1}, intakes.MaxExportIntakes, 3)
	count("limit 0", intakes.Filter{}, 0, 0)
	count("limit negativo", intakes.Filter{}, -1, 0)
}

// caseSameHeaderOnThreePaths: la lista, el detalle y el export leen la MISMA cabecera. Los tres
// tienen su propia proyección, así que un campo puede faltar en uno solo: se prueban por separado,
// con una solicitud que los trae todos y otra que no trae ninguno de los opcionales.
func caseSameHeaderOnThreePaths(t *testing.T, m Montaje) {
	full := fullHeader(intakes.StatusClosedLegacy, nil)
	full.Total = 18000
	bare := header(intakes.StatusOpen, 1, nil)
	seedHeader(t, m, m.TenantA, full, eventCancelled)
	seedHeader(t, m, m.TenantA, bare, eventCancelled)
	full.Status = intakes.StatusConfirmed
	want := []intakes.Intake{full, bare} // más recientes primero: día 5 y día 1

	listed, total, err := m.Store.List(bg(), m.TenantA, intakes.Filter{})
	if err != nil || total != 2 || len(listed) != 2 {
		t.Fatalf("List: %d filas, total %d, err %v; quería 2 y 2", len(listed), total, err)
	}
	details, err := m.Store.ListDetails(bg(), m.TenantA, intakes.Filter{}, intakes.MaxExportIntakes)
	if err != nil || len(details) != 2 {
		t.Fatalf("ListDetails: %d solicitudes, err %v; quería 2", len(details), err)
	}
	for i, w := range want {
		requireSameHeader(t, "List", listed[i], w)
		requireSameHeader(t, "ListDetails", details[i].Intake, w)
		requireSameHeader(t, "Get", get(t, m, ref{tenant: m.TenantA, id: w.ID}).Intake, w)
	}
}
