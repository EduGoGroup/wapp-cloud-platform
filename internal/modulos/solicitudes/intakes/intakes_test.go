package intakes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

// Las salidas esperadas de este fichero son LITERALES calculados con el fichero
// viejo (internal/intakes/intakes.go @ 64c181a): el candado de fronteras impide
// importarlo desde aquí, así que la equivalencia viejo ↔ nuevo se fija con lo que
// el viejo devolvió para este mismo corpus.

// TestSentinels_TextIsLiteral: los tres centinelas son textos observables, byte a byte.
func TestSentinels_TextIsLiteral(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"ErrNotFound", ErrNotFound, "solicitud no encontrada"},
		{"ErrConflict", ErrConflict, "la solicitud cambió de estado durante la transición"},
		{"ErrTooLarge", ErrTooLarge, "el filtro abarca demasiadas solicitudes para un solo export"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%s.Error() = %q, quería %q", c.name, got, c.want)
		}
	}
}

// TestSentinels_AreDistinctAndSurviveWrapping: cada centinela se reconoce con errors.Is también
// envuelto, y ninguno se confunde con otro (404, 409 y 413 son respuestas distintas).
func TestSentinels_AreDistinctAndSurviveWrapping(t *testing.T) {
	t.Parallel()
	sentinels := []error{ErrNotFound, ErrConflict, ErrTooLarge}
	for i, sentinel := range sentinels {
		wrapped := fmt.Errorf("get: %w", sentinel)
		for j, other := range sentinels {
			if got, want := errors.Is(wrapped, other), i == j; got != want {
				t.Errorf("errors.Is(%v, %v) = %v, quería %v", wrapped, other, got, want)
			}
		}
	}
}

// TestLimits_AreTheDocumentedNumbers: página por defecto 50, cota 200 y 5000 solicitudes por
// export o summary.
func TestLimits_AreTheDocumentedNumbers(t *testing.T) {
	t.Parallel()
	if DefaultPageSize != 50 {
		t.Errorf("DefaultPageSize = %d, quería 50", DefaultPageSize)
	}
	if MaxPageSize != 200 {
		t.Errorf("MaxPageSize = %d, quería 200", MaxPageSize)
	}
	if MaxExportIntakes != 5000 {
		t.Errorf("MaxExportIntakes = %d, quería 5000", MaxExportIntakes)
	}
}

// TestSortKeys_AreTheWireKeys: las dos claves de orden viajan en el query string.
func TestSortKeys_AreTheWireKeys(t *testing.T) {
	t.Parallel()
	if SortNewest != "newest" {
		t.Errorf("SortNewest = %q, quería \"newest\"", SortNewest)
	}
	if SortOldest != "oldest" {
		t.Errorf("SortOldest = %q, quería \"oldest\"", SortOldest)
	}
}

// TestIsSort_OnlyTheTwoExactKeys: lo que el borde HTTP usa para contestar 400.
func TestIsSort_OnlyTheTwoExactKeys(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"newest", "oldest"} {
		if !IsSort(key) {
			t.Errorf("IsSort(%q) = false, quería true", key)
		}
	}
	for _, key := range []string{"", "NEWEST", "Oldest", "asc", "desc", "antiguas", " newest", "oldest "} {
		if IsSort(key) {
			t.Errorf("IsSort(%q) = true, quería false", key)
		}
	}
}

// TestFilterNormalized_Pagination: página ≥ 1 y tamaño en [1, MaxPageSize], con sus límites
// exactos (200 se respeta, 201 se recorta).
func TestFilterNormalized_Pagination(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name               string
		page, size         int
		wantPage, wantSize int
	}{
		{"zero value", 0, 0, 1, 50},
		{"negative page and size", -3, -1, 1, 50},
		{"smallest valid", 1, 1, 1, 1},
		{"size at the cap", 7, 200, 7, 200},
		{"size one over the cap", 7, 201, 7, 200},
		{"huge size", 2, 100000, 2, 200},
		{"ordinary", 3, 20, 3, 20},
	}
	for _, c := range cases {
		got := Filter{Page: c.page, PageSize: c.size}.Normalized()
		if got.Page != c.wantPage || got.PageSize != c.wantSize {
			t.Errorf("%s: Normalized() = (página %d, tamaño %d), quería (%d, %d)", c.name, got.Page, got.PageSize, c.wantPage, c.wantSize)
		}
	}
}

// TestFilterNormalized_SortFallsBackToNewest: sin orden, o con uno que no es ninguno de los dos,
// se sirve el del Plan 041; `oldest` exacto se respeta.
func TestFilterNormalized_SortFallsBackToNewest(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", "newest"},
		{"newest", "newest"},
		{"oldest", "oldest"},
		{"OLDEST", "newest"},
		{" oldest", "newest"},
		{"antiguas", "newest"},
	}
	for _, c := range cases {
		if got := (Filter{Sort: c.in}).Normalized().Sort; got != c.want {
			t.Errorf("Filter{Sort: %q}.Normalized().Sort = %q, quería %q", c.in, got, c.want)
		}
	}
}

// TestFilterNormalized_Statuses: resuelve el alias, tira los vacíos, ordena y colapsa; no valida
// (lo desconocido viaja intacto) y, si no queda nada, deja nil.
func TestFilterNormalized_Statuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"alias, empty and repeated", []string{"open", "", "closed", "open"}, []string{"confirmed", "open"}},
		{"already sorted pair", []string{"needs_info", "pending_approval"}, []string{"needs_info", "pending_approval"}},
		{"same pair reversed", []string{"pending_approval", "needs_info"}, []string{"needs_info", "pending_approval"}},
		{"lookalikes are not the alias", []string{"CLOSED", " closed", "en_camino", "closed", "confirmed"}, []string{" closed", "CLOSED", "confirmed", "en_camino"}},
		{"a blank is not empty", []string{" "}, []string{" "}},
		{"only empties", []string{"", ""}, nil},
		{"empty list", []string{}, nil},
		{"nil list", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := Filter{Statuses: c.in}.Normalized().Statuses
			if !slices.Equal(got, c.want) {
				t.Errorf("Statuses = %q, quería %q", got, c.want)
			}
			if c.want == nil && got != nil {
				t.Errorf("Statuses = %q (no nil), quería nil: es «sin filtro por estado»", got)
			}
		})
	}
}

// TestFilterNormalized_IsIdempotent: normalizar lo ya normalizado no cambia nada.
func TestFilterNormalized_IsIdempotent(t *testing.T) {
	t.Parallel()
	once := Filter{Statuses: []string{"open", "", "closed", "open"}, Sort: "antiguas", Page: -1, PageSize: 999}.Normalized()
	twice := once.Normalized()
	if twice.Page != 1 || twice.PageSize != 200 || twice.Sort != "newest" || !slices.Equal(twice.Statuses, []string{"confirmed", "open"}) {
		t.Errorf("segunda pasada = %+v, quería (1, 200, newest, [confirmed open])", twice)
	}
	if once.Page != twice.Page || once.PageSize != twice.PageSize || once.Sort != twice.Sort || !slices.Equal(once.Statuses, twice.Statuses) {
		t.Errorf("Normalized no es idempotente: %+v y luego %+v", once, twice)
	}
}

// TestFilterNormalized_LeavesTheRestAndTheCallerAlone: no toca From, To, SessionID ni Orphan, y
// no modifica ni el filtro ni la lista de estados del llamante.
func TestFilterNormalized_LeavesTheRestAndTheCallerAlone(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	statuses := []string{"open", "", "closed", "open"}
	original := Filter{From: from, To: to, Statuses: statuses, SessionID: "s1", Orphan: true}

	got := original.Normalized()
	if !got.From.Equal(from) || !got.To.Equal(to) || got.SessionID != "s1" || !got.Orphan {
		t.Errorf("Normalized() = %+v: cambió From, To, SessionID u Orphan", got)
	}
	if !slices.Equal(statuses, []string{"open", "", "closed", "open"}) {
		t.Errorf("la lista del llamante quedó %q: Normalized no debe modificarla", statuses)
	}
	if original.Page != 0 || original.PageSize != 0 || original.Sort != "" {
		t.Errorf("el filtro del llamante quedó %+v: Normalized devuelve una copia", original)
	}
}

// TestFilterOffset_IsPageMinusOneTimesSize: sobre un filtro normalizado es el OFFSET de SQL;
// sobre uno crudo no sanea y devuelve la cuenta tal cual.
func TestFilterOffset_IsPageMinusOneTimesSize(t *testing.T) {
	t.Parallel()
	cases := []struct{ page, size, want int }{
		{1, 50, 0},
		{3, 20, 40},
		{7, 200, 1200},
		{0, 0, 0},
		{5, 0, 0},
		{0, 50, -50},
		{-2, 10, -30},
	}
	for _, c := range cases {
		if got := (Filter{Page: c.page, PageSize: c.size}).Offset(); got != c.want {
			t.Errorf("Filter{Page: %d, PageSize: %d}.Offset() = %d, quería %d", c.page, c.size, got, c.want)
		}
	}
	if got := (Filter{Page: 7, PageSize: 201}).Normalized().Offset(); got != 1200 {
		t.Errorf("Offset() tras normalizar (7, 201) = %d, quería 1200", got)
	}
}

// TestDetail_EmbedsTheHeader: el detalle ES la cabecera más sus líneas y revisiones; los campos
// de Intake se leen directamente sobre Detail.
func TestDetail_EmbedsTheHeader(t *testing.T) {
	t.Parallel()
	added := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	d := Detail{
		Intake:    Intake{ID: "i1", ContactID: "opaque", Status: "confirmed", Total: 12.5, CustomerNote: "dejarlo en portería"},
		Items:     []Item{{SKU: "A1", Label: "Perro", Customization: "sin cebolla", Qty: 2, UnitPrice: 6.25, AddedAt: added}},
		Revisions: []Revision{},
	}
	if d.ID != "i1" || d.ContactID != "opaque" || d.Status != "confirmed" || d.Total != 12.5 || d.CustomerNote != "dejarlo en portería" {
		t.Errorf("Detail no expone la cabecera embebida: %+v", d.Intake)
	}
	if len(d.Items) != 1 || d.Items[0].Customization != "sin cebolla" || d.Items[0].Qty != 2 || !d.Items[0].AddedAt.Equal(added) {
		t.Errorf("Items = %+v, quería la línea sembrada", d.Items)
	}
	if d.BuyerDataPresent {
		t.Error("BuyerDataPresent = true en un detalle que nadie pobló, quería false")
	}
}

// TestIntake_ZeroValueMeansNothingHappenedYet: el cero de la nota es «sin indicación» y el de
// las tres marcas de tiempo, «no se pidió seña / nunca se recordó».
func TestIntake_ZeroValueMeansNothingHappenedYet(t *testing.T) {
	t.Parallel()
	var in Intake
	if in.CustomerNote != "" {
		t.Errorf("CustomerNote = %q, quería vacía", in.CustomerNote)
	}
	if !in.DepositDueAt.IsZero() || !in.DepositRemindedAt.IsZero() || !in.ExpiryRemindedAt.IsZero() {
		t.Errorf("las marcas de un Intake cero no son cero: %+v", in)
	}
}

// TestPage_CarriesTheFilterTotal: Total es el de coincidencias del filtro, no el de la página.
func TestPage_CarriesTheFilterTotal(t *testing.T) {
	t.Parallel()
	p := Page{Intakes: []Intake{{ID: "a"}, {ID: "b"}}, Page: 2, PageSize: 2, Total: 7}
	if len(p.Intakes) != 2 || p.Page != 2 || p.PageSize != 2 || p.Total != 7 {
		t.Errorf("Page = %+v, quería 2 cabeceras de 7, página 2 de tamaño 2", p)
	}
}

// recordingStore es un Store mínimo: apunta con qué tenant se le llamó y devuelve lo sembrado.
// Que compile contra el puerto fija las nueve firmas.
type recordingStore struct {
	rows    []Intake
	tenants []string
}

var _ Store = (*recordingStore)(nil)

func (s *recordingStore) List(_ context.Context, tenantID string, _ Filter) ([]Intake, int, error) {
	s.tenants = append(s.tenants, tenantID)
	return s.rows, len(s.rows), nil
}

func (s *recordingStore) Get(_ context.Context, tenantID, _ string) (Detail, error) {
	s.tenants = append(s.tenants, tenantID)
	return Detail{}, ErrNotFound
}

func (s *recordingStore) ListDetails(_ context.Context, tenantID string, _ Filter, limit int) ([]Detail, error) {
	s.tenants = append(s.tenants, tenantID)
	if len(s.rows) > limit {
		return nil, ErrTooLarge
	}
	return nil, nil
}

func (s *recordingStore) UpdateStatus(_ context.Context, tenantID, _, _ string, _ []string) (Intake, error) {
	s.tenants = append(s.tenants, tenantID)
	return Intake{}, ErrConflict
}

func (s *recordingStore) EnsureShippingLine(_ context.Context, tenantID, _ string, _ ShippingPolicy) error {
	s.tenants = append(s.tenants, tenantID)
	return nil
}

func (s *recordingStore) ReplaceItems(_ context.Context, tenantID, _ string, items []Item, _ []string, _ EditMode) (Detail, error) {
	s.tenants = append(s.tenants, tenantID)
	return Detail{Items: items}, nil
}

func (s *recordingStore) ApplyRevalidation(_ context.Context, tenantID, _ string, _ Revalidation, _ string, _ []string) (Detail, error) {
	s.tenants = append(s.tenants, tenantID)
	return Detail{}, nil
}

func (s *recordingStore) Discard(_ context.Context, tenantID, _ string, _ []string) (DiscardOutcome, error) {
	s.tenants = append(s.tenants, tenantID)
	return DiscardOutcome{}, nil
}

func (s *recordingStore) AbandonByEvent(_ context.Context, tenantID, _ string) error {
	s.tenants = append(s.tenants, tenantID)
	return nil
}

// TestStore_IsUsableThroughThePort: un consumidor que solo conoce el puerto opera por él con el
// tenant como argumento de CADA llamada (INV-8) y recibe los centinelas del paquete.
func TestStore_IsUsableThroughThePort(t *testing.T) {
	t.Parallel()
	impl := &recordingStore{rows: []Intake{{ID: "a"}, {ID: "b"}}}
	var store Store = impl
	ctx := context.Background()

	rows, total, err := store.List(ctx, "t1", Filter{})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Errorf("List = (%+v, %d, %v), quería las 2 cabeceras sembradas", rows, total, err)
	}
	if _, err := store.Get(ctx, "t1", "ajena"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v, quería ErrNotFound", err)
	}
	if _, err := store.ListDetails(ctx, "t1", Filter{}, 1); !errors.Is(err, ErrTooLarge) {
		t.Errorf("ListDetails = %v, quería ErrTooLarge", err)
	}
	if _, err := store.UpdateStatus(ctx, "t1", "a", "confirmed", []string{"open"}); !errors.Is(err, ErrConflict) {
		t.Errorf("UpdateStatus = %v, quería ErrConflict", err)
	}
	if err := store.EnsureShippingLine(ctx, "t1", "a", ShippingPolicy(0)); err != nil {
		t.Errorf("EnsureShippingLine: error inesperado %v", err)
	}
	detail, err := store.ReplaceItems(ctx, "t1", "a", []Item{{SKU: "A1", Qty: 1}}, []string{"pending_approval"}, EditMode(0))
	if err != nil || len(detail.Items) != 1 || detail.Items[0].SKU != "A1" {
		t.Errorf("ReplaceItems = (%+v, %v), quería la línea entregada", detail, err)
	}
	if _, err := store.ApplyRevalidation(ctx, "t1", "a", Revalidation{}, "texto", []string{"open"}); err != nil {
		t.Errorf("ApplyRevalidation: error inesperado %v", err)
	}
	if _, err := store.Discard(ctx, "t1", "a", []string{"open", "expired"}); err != nil {
		t.Errorf("Discard: error inesperado %v", err)
	}
	if err := store.AbandonByEvent(ctx, "t1", "e1"); err != nil {
		t.Errorf("AbandonByEvent: error inesperado %v", err)
	}
	if want := slices.Repeat([]string{"t1"}, 9); !slices.Equal(impl.tenants, want) {
		t.Errorf("tenants recibidos = %q, quería \"t1\" en las 9 operaciones", impl.tenants)
	}
}
