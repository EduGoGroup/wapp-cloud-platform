package events

import "testing"

// TestPageSizes_AreTheContract: 50 por defecto y 200 de cota.
func TestPageSizes_AreTheContract(t *testing.T) {
	if DefaultPageSize != 50 || MaxPageSize != 200 {
		t.Errorf("DefaultPageSize = %d, MaxPageSize = %d; quería 50 y 200", DefaultPageSize, MaxPageSize)
	}
}

// TestContentFilter_Vocabulary: los tres valores del transporte, literales.
func TestContentFilter_Vocabulary(t *testing.T) {
	cases := []struct {
		got  ContentFilter
		want string
	}{{ContentAny, "any"}, {ContentNone, "none"}, {ContentAlive, "alive"}}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("ContentFilter = %q, quería %q", c.got, c.want)
		}
	}
}

// TestValidators_RejectTheTypo: solo los tres valores exactos de cada vocabulario; ni vacío, ni
// mayúsculas, ni el vocabulario del vecino.
func TestValidators_RejectTheTypo(t *testing.T) {
	cases := []struct {
		value       string
		wantContent bool
		wantStatus  bool
	}{
		{"any", true, false},
		{"none", true, false},
		{"alive", true, false},
		{"open", false, true},
		{"closed", false, true},
		{"cancelled", false, true},
		{"", false, false},
		{"Any", false, false},
		{"OPEN", false, false},
		{"abiertos", false, false},
		{"settled", false, false},
		{"discarded", false, false},
		{" open", false, false},
	}
	for _, c := range cases {
		if got := IsContentFilter(c.value); got != c.wantContent {
			t.Errorf("IsContentFilter(%q) = %v, quería %v", c.value, got, c.wantContent)
		}
		if got := IsStatus(c.value); got != c.wantStatus {
			t.Errorf("IsStatus(%q) = %v, quería %v", c.value, got, c.wantStatus)
		}
	}
}

// TestListFilter_Normalized: página >= 1, tamaño en [1, MaxPageSize] con 50 por defecto, estado
// open y contenido any por defecto; lo que el llamador puso se respeta.
func TestListFilter_Normalized(t *testing.T) {
	stale := true
	cases := []struct {
		name string
		in   ListFilter
		want ListFilter
	}{
		{"zero value gets every default", ListFilter{},
			ListFilter{Status: StatusOpen, Content: ContentAny, Page: 1, PageSize: DefaultPageSize}},
		{"negative page and size", ListFilter{Page: -3, PageSize: -1},
			ListFilter{Status: StatusOpen, Content: ContentAny, Page: 1, PageSize: DefaultPageSize}},
		{"size above the cap is capped", ListFilter{Page: 2, PageSize: MaxPageSize + 1},
			ListFilter{Status: StatusOpen, Content: ContentAny, Page: 2, PageSize: MaxPageSize}},
		{"size at the cap stays", ListFilter{Page: 1, PageSize: MaxPageSize},
			ListFilter{Status: StatusOpen, Content: ContentAny, Page: 1, PageSize: MaxPageSize}},
		{"size one stays", ListFilter{Page: 1, PageSize: 1},
			ListFilter{Status: StatusOpen, Content: ContentAny, Page: 1, PageSize: 1}},
		{"explicit values are kept",
			ListFilter{Status: StatusCancelled, Kind: "cart", Kinds: []string{"cart"}, Content: ContentNone, Stale: &stale, ContactID: "c-1", Page: 4, PageSize: 10},
			ListFilter{Status: StatusCancelled, Kind: "cart", Kinds: []string{"cart"}, Content: ContentNone, Stale: &stale, ContactID: "c-1", Page: 4, PageSize: 10}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.Normalized()
			requireSameFilter(t, got, c.want)
			// Idempotente: normalizar lo normalizado no cambia nada.
			requireSameFilter(t, got.Normalized(), c.want)
		})
	}
}

// TestListFilter_Normalized_KeepsNilAndEmptyKindsApart: nil es «sin filtro» y la lista vacía es
// «ninguno pasa»; normalizar no convierte una en la otra.
func TestListFilter_Normalized_KeepsNilAndEmptyKindsApart(t *testing.T) {
	if got := (ListFilter{}).Normalized().Kinds; got != nil {
		t.Errorf("Kinds nil pasó a %#v", got)
	}
	if got := (ListFilter{Kinds: []string{}}).Normalized().Kinds; got == nil || len(got) != 0 {
		t.Errorf("Kinds vacío pasó a %#v", got)
	}
}

// TestListFilter_Offset: (página − 1) × tamaño.
func TestListFilter_Offset(t *testing.T) {
	cases := []struct{ page, size, want int }{{1, 50, 0}, {2, 50, 50}, {3, 10, 20}, {1, 1, 0}}
	for _, c := range cases {
		if got := (ListFilter{Page: c.page, PageSize: c.size}).Offset(); got != c.want {
			t.Errorf("Offset(page=%d, size=%d) = %d, quería %d", c.page, c.size, got, c.want)
		}
	}
}

// TestEventPage_CarriesThePageAndTheTotal: los cuatro campos.
func TestEventPage_CarriesThePageAndTheTotal(t *testing.T) {
	p := EventPage{Events: []Rescuable{{Stale: true}}, Page: 2, PageSize: 10, Total: 11}
	if len(p.Events) != 1 || !p.Events[0].Stale || p.Page != 2 || p.PageSize != 10 || p.Total != 11 {
		t.Errorf("EventPage = %+v", p)
	}
}

// requireSameFilter compara dos filtros campo a campo (Stale por el valor apuntado).
func requireSameFilter(t *testing.T, got, want ListFilter) {
	t.Helper()
	sameStale := (got.Stale == nil) == (want.Stale == nil) && (got.Stale == nil || *got.Stale == *want.Stale)
	sameKinds := len(got.Kinds) == len(want.Kinds)
	for i := 0; sameKinds && i < len(want.Kinds); i++ {
		sameKinds = got.Kinds[i] == want.Kinds[i]
	}
	if got.Status != want.Status || got.Kind != want.Kind || got.Content != want.Content || got.ContactID != want.ContactID ||
		got.Page != want.Page || got.PageSize != want.PageSize || !sameStale || !sameKinds {
		t.Errorf("filtro = %+v, quería %+v", got, want)
	}
}
