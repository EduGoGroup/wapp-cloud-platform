//go:build pendiente

package apipublica_test

// intakes_filter_test.go — LA QUERY de G1 (GET /api/v1/intakes) del contrato de MountIntakes: qué
// filtro llega al servicio y qué se rechaza con 400 en vez de servir otra cosa en silencio. Con
// entradas adversarias. Los dobles están en intakes_test.go.

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// intakeWantFilter compara el filtro que recibió el servicio campo a campo (los instantes, como
// instantes: la zona con la que se parsearon no cuenta).
func intakeWantFilter(t *testing.T, query string, got, want intakes.Filter) {
	t.Helper()
	if !got.From.Equal(want.From) || got.From.IsZero() != want.From.IsZero() {
		t.Errorf("%q: From = %s, quiero %s", query, got.From, want.From)
	}
	if !got.To.Equal(want.To) || got.To.IsZero() != want.To.IsZero() {
		t.Errorf("%q: To = %s, quiero %s", query, got.To, want.To)
	}
	if !slices.Equal(got.Statuses, want.Statuses) {
		t.Errorf("%q: Statuses = %q, quiero %q", query, got.Statuses, want.Statuses)
	}
	if got.SessionID != want.SessionID || got.Orphan != want.Orphan || got.Sort != want.Sort {
		t.Errorf("%q: session/orphan/sort = %q/%v/%q, quiero %q/%v/%q",
			query, got.SessionID, got.Orphan, got.Sort, want.SessionID, want.Orphan, want.Sort)
	}
	if got.Page != want.Page || got.PageSize != want.PageSize {
		t.Errorf("%q: page/page_size = %d/%d, quiero %d/%d", query, got.Page, got.PageSize, want.Page, want.PageSize)
	}
}

// TestMountIntakes_ListQuery: cada parámetro llega al filtro como promete el contrato. El caso
// base (sin query) es Page 1 y PageSize 50, y cada fila dice solo en qué se aparta de él.
func TestMountIntakes_ListQuery(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 8, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		query string
		want  intakes.Filter
		// zeroPaging: la fila espera Page y PageSize en 0 (un 0 explícito es 0, no el defecto).
		zeroPaging bool
	}{
		{query: "", want: intakes.Filter{}},
		// Una fecha suelta en `to` es «hasta el final de ESE día»: se le suma uno.
		{query: "?from=2026-08-01&to=2026-08-04", want: intakes.Filter{From: day(1), To: day(5)}},
		{query: "?to=2026-08-31", want: intakes.Filter{To: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}},
		// Un RFC 3339 se respeta tal cual, en `from` y en `to`.
		{query: "?from=2026-08-01T10:00:00-03:00&to=2026-08-04T10:00:00Z", want: intakes.Filter{
			From: time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC), To: time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)}},
		{query: "?from=&to=", want: intakes.Filter{}},
		// `status` se repite: llegan todos, en su orden y normalizados.
		{query: "?status=open", want: intakes.Filter{Statuses: []string{"open"}}},
		{query: "?status=open&status=cancelled", want: intakes.Filter{Statuses: []string{"open", "cancelled"}}},
		{query: "?status=cancelled&status=closed", want: intakes.Filter{Statuses: []string{"cancelled", "confirmed"}}},
		{query: "?status=open&status=open", want: intakes.Filter{Statuses: []string{"open", "open"}}},
		{query: "?status=expired&status=abandoned", want: intakes.Filter{Statuses: []string{"expired", "abandoned"}}},
		{query: "?status=", want: intakes.Filter{}},
		{query: "?status=&status=needs_info&status=", want: intakes.Filter{Statuses: []string{"needs_info"}}},
		{query: "?sort=oldest", want: intakes.Filter{Sort: "oldest"}},
		{query: "?sort=newest", want: intakes.Filter{Sort: "newest"}},
		{query: "?sort=", want: intakes.Filter{}},
		{query: "?orphan=true", want: intakes.Filter{Orphan: true}},
		{query: "?orphan=1", want: intakes.Filter{Orphan: true}},
		{query: "?orphan=t", want: intakes.Filter{Orphan: true}},
		{query: "?orphan=TRUE", want: intakes.Filter{Orphan: true}},
		{query: "?orphan=false", want: intakes.Filter{}},
		{query: "?orphan=0", want: intakes.Filter{}},
		{query: "?orphan=", want: intakes.Filter{}},
		{query: "?session=sess-a", want: intakes.Filter{SessionID: "sess-a"}},
		{query: "?session=%20a%2Fb%20", want: intakes.Filter{SessionID: " a/b "}},
		{query: "?page=3&page_size=20", want: intakes.Filter{Page: 3, PageSize: 20}},
		// Lo ilegible cae a su defecto; el techo de page_size lo pone el servicio, no la query.
		{query: "?page=abc&page_size=-5", want: intakes.Filter{}},
		{query: "?page=1.5&page_size=%D9%A3", want: intakes.Filter{}},
		{query: "?page=99999999999999999999", want: intakes.Filter{}},
		{query: "?page_size=100000", want: intakes.Filter{PageSize: 100000}},
		{query: "?page=0&page_size=0", want: intakes.Filter{}, zeroPaging: true},
		{query: "?tenant_id=bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb&intake_id=x", want: intakes.Filter{}},
		{query: "?status=pending_approval&status=needs_info&sort=oldest&orphan=true&session=s&from=2026-08-02&page=2",
			want: intakes.Filter{From: day(2), Statuses: []string{"pending_approval", "needs_info"}, Sort: "oldest", Orphan: true, SessionID: "s", Page: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			want := tc.want
			if !tc.zeroPaging {
				if want.Page == 0 {
					want.Page = 1
				}
				if want.PageSize == 0 {
					want.PageSize = intakes.DefaultPageSize
				}
			}
			svc := &intakeServiceSpy{}
			wantCode(t, tc.query, intakeDo(t, svc, http.MethodGet, intakesTarget+tc.query, ""), http.StatusOK)
			intakeWantCalls(t, tc.query, svc, "List")
			intakeWantFilter(t, tc.query, svc.filter, want)
		})
	}
}

// TestMountIntakes_ListQueryRejects: un typo en el filtro se DICE. Servir la bandeja entera ante
// `status=confirmadas`, otro orden ante `sort=antiguos` o todo ante `orphan=si` —la vista desde
// la que se descarta sin vuelta atrás— sería peor que un error. Ningún 400 llama al servicio.
func TestMountIntakes_ListQueryRejects(t *testing.T) {
	const (
		badFrom   = "from inválido: usa YYYY-MM-DD o RFC3339"
		badTo     = "to inválido: usa YYYY-MM-DD o RFC3339"
		badStatus = "status desconocido"
		badSort   = "sort desconocido: usa newest u oldest"
		badOrphan = "orphan inválido: usa true o false"
	)
	cases := []struct{ query, msg string }{
		{"?from=ayer", badFrom},
		{"?from=2026-13-01", badFrom},
		{"?from=2026-08-01T10:00", badFrom},
		{"?from=2026-08-01%20", badFrom},
		{"?from=01/08/2026", badFrom},
		{"?from=%D9%A2%D9%A0%D9%A2%D9%A6-08-01", badFrom},
		{"?to=2026-02-30", badTo},
		{"?to=2026-08-04T10:00:00", badTo},
		{"?to=ma%C3%B1ana", badTo},
		{"?status=confirmadas", badStatus},
		{"?status=OPEN", badStatus},
		{"?status=%20open", badStatus},
		{"?status=open%0A", badStatus},
		// La forma con comas no es un estado: sale por el 400 de siempre, no por un descarte mudo.
		{"?status=open,cancelled", badStatus},
		{"?status=open,,cancelled", badStatus},
		// El desconocido se dice aunque venga entre dos buenos.
		{"?status=open&status=nope&status=cancelled", badStatus},
		{"?status=open&status=cancelled&status=CLOSED", badStatus},
		{"?sort=antiguos", badSort},
		{"?sort=NEWEST", badSort},
		{"?sort=asc", badSort},
		{"?sort=%20oldest", badSort},
		{"?orphan=si", badOrphan},
		{"?orphan=yes", badOrphan},
		{"?orphan=2", badOrphan},
		{"?orphan=true%20", badOrphan},
		{"?orphan=verdadero", badOrphan},
		// El orden de las comprobaciones: from, to, status, sort, orphan.
		{"?orphan=si&sort=bad&status=nope&to=x&from=x", badFrom},
		{"?orphan=si&sort=bad&status=nope&to=x&from=2026-08-01", badTo},
		{"?orphan=si&sort=bad&status=nope", badStatus},
		{"?orphan=si&sort=bad&status=open", badSort},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			svc := &intakeServiceSpy{}
			rec := intakeDo(t, svc, http.MethodGet, intakesTarget+tc.query, "")
			wantCode(t, tc.query, rec, http.StatusBadRequest)
			wantErrorBody(t, tc.query, rec, tc.msg)
			intakeWantCalls(t, tc.query, svc)
		})
	}
}
