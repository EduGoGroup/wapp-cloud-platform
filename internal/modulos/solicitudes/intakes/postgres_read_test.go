//go:build pendiente

package intakes

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// LO QUE EL VERDE AÑADIRÁ (F6-03): el texto byte a byte del count, de la página (con sus dos
// ORDER BY), de la CTE del export y de las cuatro lecturas de Get.

// pgItemRow es una fila de línea con sus seis columnas.
func pgItemRow(sku string, qty int64, price float64) []driver.Value {
	return []driver.Value{sku, "Etiqueta " + sku, "", qty, price, pgCreated}
}

// pgDetailRow es una fila del export: la cabecera (11 columnas) más su línea (6). item nil es la
// fila de una solicitud sin líneas: el LEFT JOIN trae las seis a NULL.
func pgDetailRow(id string, item []driver.Value) []driver.Value {
	row := pgIntakeRow(StatusConfirmed, 30)
	row[0] = id
	if item == nil {
		item = []driver.Value{nil, nil, nil, nil, nil, nil}
	}
	return append(row, item...)
}

// TestPostgres_Get_InvalidID_NotFoundWithoutQuerying: un id que no es UUID no puede existir.
func TestPostgres_Get_InvalidID_NotFoundWithoutQuerying(t *testing.T) {
	store, fake := newFakePostgres(t)
	if _, err := store.Get(t.Context(), pgTenant, "no-es-un-uuid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v, quería ErrNotFound", err)
	}
	requirePgUntouched(t, fake)
}

// TestPostgres_Get_NoHeader_NotFoundAndStops: sin cabecera en ese tenant es ErrNotFound, y no se
// lanzan las otras tres lecturas.
func TestPostgres_Get_NoHeader_NotFoundAndStops(t *testing.T) {
	store, fake := newFakePostgres(t)
	d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v, quería ErrNotFound", err)
	}
	if !reflect.DeepEqual(d, Detail{}) {
		t.Errorf("Get sin cabecera devolvió %+v, quería Detail{}", d)
	}
	requirePgKinds(t, fake, pgQuery)
}

// TestPostgres_Get_ReadsHeaderItemsRevisionsAndBuyerFlag: cuatro lecturas sueltas, en orden y
// fuera de transacción; el estado sale normalizado, las fechas NULL a cero, y las listas sin
// filas salen vacías y no nil.
func TestPostgres_Get_ReadsHeaderItemsRevisionsAndBuyerFlag(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(pgIntakeRow(StatusClosedLegacy, 30)...), pgReply{}, pgReply{}, pgOne(true))
	d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
	if err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	if want := pgIntake(StatusConfirmed, 30); d.Intake != want {
		t.Errorf("cabecera = %+v, quería %+v", d.Intake, want)
	}
	if d.Items == nil || len(d.Items) != 0 || d.Revisions == nil || len(d.Revisions) != 0 {
		t.Errorf("Items = %#v y Revisions = %#v, quería las dos vacías y no nil", d.Items, d.Revisions)
	}
	if !d.BuyerDataPresent {
		t.Error("BuyerDataPresent = false, quería true")
	}
	requirePgKinds(t, fake, pgQuery, pgQuery, pgQuery, pgQuery)
	stmts := fake.statements()
	if want := []driver.Value{pgTenant, pgIntakeID}; !reflect.DeepEqual(stmts[0].args, want) {
		t.Errorf("argumentos de la cabecera = %v, quería %v (acotada por tenant)", stmts[0].args, want)
	}
	for i, s := range stmts {
		if s.inTx {
			t.Errorf("la lectura %d salió dentro de una transacción", i)
		}
	}
}

// TestPostgres_Get_MapsItemsInOrder: las líneas salen con sus seis columnas y en el orden de la base.
func TestPostgres_Get_MapsItemsInOrder(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(pgIntakeRow(StatusOpen, 25)...),
		pgReply{rows: [][]driver.Value{pgItemRow("B", 2, 10), pgItemRow("A", 1, 5)}},
		pgReply{}, pgOne(false))
	d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
	if err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	want := []Item{
		{SKU: "B", Label: "Etiqueta B", Qty: 2, UnitPrice: 10, AddedAt: pgCreated},
		{SKU: "A", Label: "Etiqueta A", Qty: 1, UnitPrice: 5, AddedAt: pgCreated},
	}
	if !reflect.DeepEqual(d.Items, want) {
		t.Errorf("Items = %+v, quería %+v", d.Items, want)
	}
	if d.BuyerDataPresent {
		t.Error("BuyerDataPresent = true, quería false")
	}
}

// TestPostgres_Get_Errors: cada fallo de las lecturas de cabecera, líneas y datos del comprador
// sale envuelto con su prefijo byte a byte. El del cierre solo cuenta si lo demás fue bien.
func TestPostgres_Get_Errors(t *testing.T) {
	closeBoom := errors.New("cierre roto")
	head := pgOne(pgIntakeRow(StatusOpen, 1)...)
	badItem := pgItemRow("A", 1, 1)
	badItem[0] = nil
	cases := []struct {
		name   string
		script []pgReply
		prefix string
		cause  error
	}{
		{"header query fails", []pgReply{{err: errPgBoom}}, "intakes: leer solicitud: ", errPgBoom},
		{"items query fails", []pgReply{head, {err: errPgBoom}}, "intakes: listar líneas: ", errPgBoom},
		{"items iteration fails", []pgReply{head, {endErr: errPgBoom}}, "intakes: recorrer líneas: ", errPgBoom},
		{"items close fails", []pgReply{head, {closeErr: closeBoom}}, "intakes: cerrar filas de líneas: ", closeBoom},
		{"close does not mask iteration", []pgReply{head, {endErr: errPgBoom, closeErr: closeBoom}}, "intakes: recorrer líneas: ", errPgBoom},
		{"buyer data query fails", []pgReply{head, {}, {}, {err: errPgBoom}}, "intakes: comprobar datos del comprador: ", errPgBoom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.script...)
			d, err := store.Get(t.Context(), pgTenant, pgIntakeID)
			requirePgWrapped(t, err, tc.prefix, tc.cause)
			if !reflect.DeepEqual(d, Detail{}) {
				t.Errorf("Get con error devolvió %+v, quería Detail{}", d)
			}
		})
	}
	t.Run("item row cannot be scanned", func(t *testing.T) {
		store, fake := newFakePostgres(t)
		fake.script(head, pgReply{rows: [][]driver.Value{badItem}})
		_, err := store.Get(t.Context(), pgTenant, pgIntakeID)
		if err == nil || !strings.HasPrefix(err.Error(), "intakes: leer línea: ") {
			t.Errorf("error = %v, quería el prefijo %q", err, "intakes: leer línea: ")
		}
	})
}

// TestPostgres_List_ZeroTotal_SkipsThePage: con total 0 no se lanza la página; slice vacío no nil.
func TestPostgres_List_ZeroTotal_SkipsThePage(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(int64(0)))
	got, total, err := store.List(t.Context(), pgTenant, Filter{})
	if err != nil || total != 0 || got == nil || len(got) != 0 {
		t.Errorf("List = (%#v, %d, %v), quería ([]Intake{}, 0, nil)", got, total, err)
	}
	requirePgKinds(t, fake, pgQuery)
}

// TestPostgres_List_SamePredicateForCountAndPage: el count y la página llevan los MISMOS seis
// argumentos de filtro; la página añade el tamaño y el desplazamiento del filtro normalizado. Los
// estados viajan expandidos a sus variantes almacenadas y los filtros ausentes como NULL.
func TestPostgres_List_SamePredicateForCountAndPage(t *testing.T) {
	from, to := pgCreated, pgUpdated
	cases := []struct {
		name       string
		filter     Filter
		wantFilter []driver.Value
		wantPaging []driver.Value
	}{
		{"empty filter", Filter{}, []driver.Value{pgTenant, nil, nil, nil, nil, nil},
			[]driver.Value{DefaultPageSize, 0}},
		{"every filter set", Filter{From: from, To: to, Statuses: []string{StatusConfirmed}, SessionID: pgSession, Orphan: true, Page: 3, PageSize: 10},
			[]driver.Value{pgTenant, from, to, []string{StatusClosedLegacy, StatusConfirmed}, pgSession, true},
			[]driver.Value{10, 20}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(pgOne(int64(7)), pgOne(pgIntakeRow(StatusClosedLegacy, 30)...))
			got, total, err := store.List(t.Context(), pgTenant, tc.filter)
			if err != nil {
				t.Fatalf("List: error inesperado %v", err)
			}
			if total != 7 || len(got) != 1 || got[0] != pgIntake(StatusConfirmed, 30) {
				t.Errorf("List = (%+v, %d), quería una fila normalizada y total 7", got, total)
			}
			stmts := fake.statements()
			if len(stmts) != 2 || stmts[0].inTx || stmts[1].inTx {
				t.Fatalf("sentencias = %+v, quería dos consultas sueltas", stmts)
			}
			if !reflect.DeepEqual(stmts[0].args, tc.wantFilter) {
				t.Errorf("argumentos del count = %v, quería %v", stmts[0].args, tc.wantFilter)
			}
			if want := append(append([]driver.Value{}, tc.wantFilter...), tc.wantPaging...); !reflect.DeepEqual(stmts[1].args, want) {
				t.Errorf("argumentos de la página = %v, quería %v", stmts[1].args, want)
			}
		})
	}
}

// TestPostgres_List_SortChoosesTheOrderBy: el orden lo elige el filtro normalizado entre dos
// constantes; un sort desconocido es el de por defecto (más reciente primero).
func TestPostgres_List_SortChoosesTheOrderBy(t *testing.T) {
	for sort, want := range map[string]string{
		"":         " ORDER BY created_at DESC, id DESC",
		SortNewest: " ORDER BY created_at DESC, id DESC",
		SortOldest: " ORDER BY created_at ASC, id ASC",
		"'; DROP":  " ORDER BY created_at DESC, id DESC",
	} {
		store, fake := newFakePostgres(t)
		fake.script(pgOne(int64(1)), pgOne(pgIntakeRow(StatusOpen, 1)...))
		if _, _, err := store.List(t.Context(), pgTenant, Filter{Sort: sort}); err != nil {
			t.Fatalf("List(sort=%q): error inesperado %v", sort, err)
		}
		page := fake.statements()[1].query
		if !strings.Contains(page, want) || strings.Contains(page, "DROP") {
			t.Errorf("sort=%q: la página no lleva %q:\n%s", sort, want, page)
		}
	}
}

// TestPostgres_List_Errors: los fallos del count y de la página, con su prefijo y sin filas a medias.
func TestPostgres_List_Errors(t *testing.T) {
	closeBoom := errors.New("cierre roto")
	count := pgOne(int64(2))
	cases := []struct {
		name   string
		script []pgReply
		prefix string
		cause  error
	}{
		{"count fails", []pgReply{{err: errPgBoom}}, "intakes: contar solicitudes: ", errPgBoom},
		{"page query fails", []pgReply{count, {err: errPgBoom}}, "intakes: listar solicitudes: ", errPgBoom},
		{"page iteration fails", []pgReply{count, {endErr: errPgBoom}}, "intakes: recorrer solicitudes: ", errPgBoom},
		{"page close fails", []pgReply{count, {closeErr: closeBoom}}, "intakes: cerrar filas de solicitudes: ", closeBoom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.script...)
			got, total, err := store.List(t.Context(), pgTenant, Filter{})
			requirePgWrapped(t, err, tc.prefix, tc.cause)
			if got != nil || total != 0 {
				t.Errorf("List con error devolvió (%+v, %d), quería (nil, 0)", got, total)
			}
		})
	}
	t.Run("row cannot be scanned", func(t *testing.T) {
		store, fake := newFakePostgres(t)
		bad := pgIntakeRow(StatusOpen, 1)
		bad[0] = nil
		fake.script(count, pgOne(bad...))
		got, total, err := store.List(t.Context(), pgTenant, Filter{})
		if err == nil || !strings.HasPrefix(err.Error(), "intakes: leer solicitud: ") || got != nil || total != 0 {
			t.Errorf("List = (%v, %d, %v), quería (nil, 0, error con prefijo %q)", got, total, err, "intakes: leer solicitud: ")
		}
	})
}

// TestPostgres_ListDetails_NonPositiveLimit_DoesNotQuery: pedir cero no toca la base.
func TestPostgres_ListDetails_NonPositiveLimit_DoesNotQuery(t *testing.T) {
	for _, limit := range []int{0, -1} {
		store, fake := newFakePostgres(t)
		got, err := store.ListDetails(t.Context(), pgTenant, Filter{}, limit)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("ListDetails(limit=%d) = (%#v, %v), quería ([]Detail{}, nil)", limit, got, err)
		}
		requirePgUntouched(t, fake)
	}
}

// TestPostgres_ListDetails_GroupsRowsByIntake: UNA consulta (no 1+N) con los seis argumentos del
// filtro y el límite; las filas consecutivas de una solicitud se agrupan, y la que no tiene
// líneas sale con Items vacío y no nil. La paginación del filtro no viaja.
func TestPostgres_ListDetails_GroupsRowsByIntake(t *testing.T) {
	const second = "33333333-3333-4333-8333-333333333333"
	store, fake := newFakePostgres(t)
	fake.script(pgReply{rows: [][]driver.Value{
		pgDetailRow(pgIntakeID, pgItemRow("A", 1, 5)),
		pgDetailRow(pgIntakeID, pgItemRow("B", 2, 10)),
		pgDetailRow(second, nil),
	}})
	got, err := store.ListDetails(t.Context(), pgTenant, Filter{Page: 4, PageSize: 2}, 50)
	if err != nil {
		t.Fatalf("ListDetails: error inesperado %v", err)
	}
	if len(got) != 2 || got[0].ID != pgIntakeID || got[1].ID != second {
		t.Fatalf("ListDetails = %+v, quería dos solicitudes en el orden de la base", got)
	}
	if skus := []string{got[0].Items[0].SKU, got[0].Items[1].SKU}; len(got[0].Items) != 2 || skus[0] != "A" || skus[1] != "B" {
		t.Errorf("líneas de la primera = %+v, quería A y B en ese orden", got[0].Items)
	}
	if got[1].Items == nil || len(got[1].Items) != 0 {
		t.Errorf("Items de la solicitud sin líneas = %#v, quería vacío y no nil", got[1].Items)
	}
	if got[0].Revisions != nil || got[0].BuyerDataPresent {
		t.Errorf("el export rellenó Revisions o BuyerDataPresent: %+v", got[0])
	}
	stmts := fake.statements()
	if len(stmts) != 1 || stmts[0].kind != pgQuery || stmts[0].inTx {
		t.Fatalf("sentencias = %+v, quería una consulta suelta", stmts)
	}
	if want := []driver.Value{pgTenant, nil, nil, nil, nil, nil, 50}; !reflect.DeepEqual(stmts[0].args, want) {
		t.Errorf("argumentos del export = %v, quería %v", stmts[0].args, want)
	}
}

// TestPostgres_ListDetails_Errors: los cuatro fallos del export, con su prefijo.
func TestPostgres_ListDetails_Errors(t *testing.T) {
	closeBoom := errors.New("cierre roto")
	cases := []struct {
		name   string
		reply  pgReply
		prefix string
		cause  error
	}{
		{"query fails", pgReply{err: errPgBoom}, "intakes: listar solicitudes con líneas: ", errPgBoom},
		{"iteration fails", pgReply{endErr: errPgBoom}, "intakes: recorrer el export: ", errPgBoom},
		{"close fails", pgReply{closeErr: closeBoom}, "intakes: cerrar filas del export: ", closeBoom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, fake := newFakePostgres(t)
			fake.script(tc.reply)
			got, err := store.ListDetails(t.Context(), pgTenant, Filter{}, 5)
			requirePgWrapped(t, err, tc.prefix, tc.cause)
			if got != nil {
				t.Errorf("ListDetails con error devolvió %+v, quería nil", got)
			}
		})
	}
	t.Run("row cannot be scanned", func(t *testing.T) {
		store, fake := newFakePostgres(t)
		bad := pgDetailRow(pgIntakeID, nil)
		bad[5] = "no es una fecha"
		fake.script(pgOne(bad...))
		_, err := store.ListDetails(t.Context(), pgTenant, Filter{}, 5)
		if err == nil || !strings.HasPrefix(err.Error(), "intakes: leer fila del export: ") {
			t.Errorf("error = %v, quería el prefijo %q", err, "intakes: leer fila del export: ")
		}
	})
}
