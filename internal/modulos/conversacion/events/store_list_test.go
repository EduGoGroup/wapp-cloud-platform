package events

import (
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
)

// pgRescuableRow es una fila de la consulta de rescatables: las doce columnas del evento más la
// marca «vencido» y el contenido derivado, en ese orden.
func pgRescuableRow(id, kind string, stale bool, contentState, contentRef string) []driver.Value {
	return append(pgEventRow(id, kind, StatusOpen, nil), stale, contentState, contentRef)
}

const pgOtherEvent = "44444444-4444-4444-8444-444444444444"

// requireRescuable compara un rescatable leído con el esperado.
func requireRescuable(t *testing.T, what string, got Rescuable, id, kind string, stale bool, contentState, contentRef string) {
	t.Helper()
	requireSameEvent(t, what, got.Event, pgEventOf(id, kind, StatusOpen, time.Time{}))
	if got.Stale != stale || got.ContentState != contentState || got.ContentRef != contentRef {
		t.Errorf("%s: (vencido=%v, %q, %q), quería (%v, %q, %q)", what, got.Stale, got.ContentState, got.ContentRef, stale, contentState, contentRef)
	}
}

// TestEventColumns_QualifiedListIsTheSameList: la lista de columnas cualificada con el alias `e`
// (la de las consultas con JOIN) es la MISMA que la que espera scanEvent, columna a columna y en
// el mismo orden. Si divergen no hay error de compilación: hay datos cambiados de sitio.
func TestEventColumns_QualifiedListIsTheSameList(t *testing.T) {
	plain := strings.Split(eventColumns, ",")
	qualified := strings.Split(eventColumnsE, ",")
	if len(plain) != 12 || len(qualified) != len(plain) {
		t.Fatalf("las listas tienen %d y %d columnas, quería 12 y 12", len(plain), len(qualified))
	}
	for i := range plain {
		column := strings.TrimSpace(plain[i])
		if strings.Contains(column, ".") {
			t.Errorf("la lista sin cualificar lleva un alias en %q", column)
		}
		if got := strings.TrimSpace(qualified[i]); got != "e."+column {
			t.Errorf("columna %d: cualificada %q, quería %q", i, got, "e."+column)
		}
	}
}

// TestListRescuable_MapsTheRowsAndPassesTheClockAndTheLimit: UNA consulta con la conversación, el
// instante del reloj inyectado (UTC) y el tope; cada fila sale con su evento, su marca y su
// contenido derivado. Un tope negativo viaja como 0 («sin tope»).
func TestListRescuable_MapsTheRowsAndPassesTheClockAndTheLimit(t *testing.T) {
	h := newFakeStore(t, pgReply{rows: [][]driver.Value{
		pgRescuableRow(pgEvent, "cart", true, "alive", "ref-1"),
		pgRescuableRow(pgOtherEvent, "survey", false, "", ""),
	}}, pgReply{}, pgReply{})

	rs, err := h.store.ListRescuable(t.Context(), pgTenant, pgSession, pgContact, 6)
	if err != nil || len(rs) != 2 {
		t.Fatalf("ListRescuable = (%+v, %v), quería dos rescatables", rs, err)
	}
	requireRescuable(t, "primero", rs[0], pgEvent, "cart", true, "alive", "ref-1")
	requireRescuable(t, "segundo", rs[1], pgOtherEvent, "survey", false, "", "")

	for _, limit := range []int{0, -3} {
		if rs, err = h.store.ListRescuable(t.Context(), pgTenant, pgSession, pgContact, limit); err != nil || len(rs) != 0 {
			t.Errorf("sin filas (tope %d): (%+v, %v), quería ninguno", limit, rs, err)
		}
	}

	stmts := requireStatements(t, h.fake, 3)
	requireArgs(t, "con tope", stmts[0], pgTenant, pgSession, pgContact, pgNow, 6)
	requireArgs(t, "tope cero", stmts[1], pgTenant, pgSession, pgContact, pgNow, 0)
	requireArgs(t, "tope negativo", stmts[2], pgTenant, pgSession, pgContact, pgNow, 0)
}

// TestListRescuable_QueryShape: la consulta acota por status open y por el contenido (sin
// contenido o vivo, INV-17), lee la vista event_content y la configuración del tenant con LEFT
// JOIN, ordena por última actividad descendente y NO filtra por fechas (INV-19): el instante solo
// aparece en el SELECT, calculando la marca.
func TestListRescuable_QueryShape(t *testing.T) {
	h := newFakeStore(t)
	if _, err := h.store.ListRescuable(t.Context(), pgTenant, pgSession, pgContact, 0); err != nil {
		t.Fatalf("ListRescuable: %v", err)
	}
	query := requireStatements(t, h.fake, 1)[0].query
	for _, want := range []string{
		"LEFT JOIN public.event_content c",
		"LEFT JOIN public.tenant_settings s",
		"e.status = 'open'",
		"(c.event_id IS NULL OR c.state = 'alive')",
		"COALESCE(s.event_inactivity_ttl_seconds, 7200)",
		"ORDER BY e.last_activity_at DESC, e.id",
		"LIMIT NULLIF($5::bigint, 0)",
	} {
		if !strings.Contains(query, want) {
			t.Errorf("la consulta de rescatables no contiene %q:\n%s", want, query)
		}
	}
	_, where, found := strings.Cut(query, "WHERE")
	if !found || strings.Contains(where, "$4") || strings.Contains(where, "last_activity_at >") || strings.Contains(where, "now()") {
		t.Errorf("INV-19: el WHERE de los rescatables compara fechas:\n%s", where)
	}
}

// TestListRescuable_Failures_Wrapped: el fallo de la consulta y los del recorrido, con su texto.
func TestListRescuable_Failures_Wrapped(t *testing.T) {
	row := pgRescuableRow(pgEvent, "cart", false, "", "")
	cases := []struct {
		name   string
		reply  pgReply
		prefix string
	}{
		{"query", pgFails(), "events: listar eventos rescatables: "},
		{"scan", pgOne("solo", "dos"), "events: leer fila de evento: "},
		{"iteration", pgReply{rows: [][]driver.Value{row}, endErr: errPgBoom}, "events: recorrer eventos vivos: "},
		{"close", pgReply{rows: [][]driver.Value{row}, closeErr: errPgBoom}, "events: cerrar filas de eventos: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFakeStore(t, c.reply)
			rs, err := h.store.ListRescuable(t.Context(), pgTenant, pgSession, pgContact, 0)
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) || rs != nil {
				t.Errorf("(%+v, %v), quería (nil, %q…)", rs, err, c.prefix)
			}
		})
	}
}

// TestKindFeatures_SortedDistinctAndFresh: las cuatro features que habilitan un tipo, en orden
// alfabético, en un slice nuevo cada vez.
func TestKindFeatures_SortedDistinctAndFresh(t *testing.T) {
	want := []string{entitlements.FeatureCartBasic, entitlements.FeatureMedia, entitlements.FeatureMenu, entitlements.FeatureSurvey}
	got := KindFeatures()
	if !slices.Equal(got, want) {
		t.Fatalf("KindFeatures = %v, quería %v", got, want)
	}
	got[0] = "manoseada"
	if again := KindFeatures(); !slices.Equal(again, want) {
		t.Errorf("KindFeatures comparte memoria entre llamadas: %v", again)
	}
}

// TestAllowedKinds_OnlyTheKindsTheTenantHas: los tipos cuya feature tiene ESE tenant, en orden
// alfabético; sin ninguna, la lista vacía (no nil) y sin error.
func TestAllowedKinds_OnlyTheKindsTheTenantHas(t *testing.T) {
	feats := entitlementshelpertest.NewFake()
	feats.Enable("t-1", entitlements.FeatureSurvey)
	feats.Enable("t-1", entitlements.FeatureCartBasic)
	feats.Enable("t-1", entitlements.FeatureLLMIntake) // no habilita ningún tipo
	feats.Enable("t-2", entitlements.FeatureMedia)
	feats.Enable("t-3", entitlements.FeatureMenu)
	feats.Enable("t-3", entitlements.FeatureMedia)
	feats.Enable("t-3", entitlements.FeatureSurvey)
	feats.Enable("t-3", entitlements.FeatureCartBasic)

	cases := []struct {
		tenant string
		want   []string
	}{
		{"t-1", []string{"cart", "survey"}},
		{"t-2", []string{"media"}},
		{"t-3", []string{"cart", "media", "menu", "survey"}},
		{"t-sin-nada", []string{}},
	}
	for _, c := range cases {
		got, err := AllowedKinds(t.Context(), feats, c.tenant)
		if err != nil || got == nil || !slices.Equal(got, c.want) {
			t.Errorf("AllowedKinds(%s) = (%#v, %v), quería %v", c.tenant, got, err, c.want)
		}
	}
}

// TestAllowedKinds_NoResolverOrFailure_Propagates: sin resolver es un error con su texto; un fallo
// del resolver se PROPAGA envuelto —no se devuelve una lista recortada—.
func TestAllowedKinds_NoResolverOrFailure_Propagates(t *testing.T) {
	got, err := AllowedKinds(t.Context(), nil, "t-1")
	if want := "events: sin resolver de features no se puede saber qué tipos ve el tenant"; err == nil || err.Error() != want || got != nil {
		t.Errorf("sin resolver: (%v, %v), quería (nil, %q)", got, err, want)
	}

	boom := errors.New("boom")
	feats := entitlementshelpertest.NewFake()
	feats.Enable("t-1", entitlements.FeatureCartBasic)
	feats.Err = boom
	got, err = AllowedKinds(t.Context(), feats, "t-1")
	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "events: resolver la feature ") || !strings.Contains(err.Error(), " del tenant: ") || got != nil {
		t.Errorf("con el resolver caído: (%v, %v), quería (nil, el fallo envuelto)", got, err)
	}
}

// TestListEvents_CountsThenPages_WithTheNormalizedFilter: DOS sentencias, la cuenta (ocho
// argumentos) y la página (esos ocho más tamaño y desplazamiento). El filtro cero viaja
// normalizado: open, any, sin tipo, sin contacto, sin vencido y sin conjunto de tipos (NULL), con
// el instante del reloj en $4.
func TestListEvents_CountsThenPages_WithTheNormalizedFilter(t *testing.T) {
	h := newFakeStore(t, pgOne(int64(7)), pgReply{rows: [][]driver.Value{
		pgRescuableRow(pgEvent, "cart", true, "alive", "ref-1"),
		pgRescuableRow(pgOtherEvent, "survey", false, "", ""),
	}})

	page, err := h.store.ListEvents(t.Context(), pgTenant, ListFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if page.Total != 7 || page.Page != 1 || page.PageSize != DefaultPageSize || len(page.Events) != 2 {
		t.Fatalf("página = %+v; quería Total 7, página 1 de %d y dos eventos", page, DefaultPageSize)
	}
	requireRescuable(t, "primero", page.Events[0], pgEvent, "cart", true, "alive", "ref-1")
	requireRescuable(t, "segundo", page.Events[1], pgOtherEvent, "survey", false, "", "")

	stmts := requireStatements(t, h.fake, 2)
	requireArgs(t, "la cuenta", stmts[0], pgTenant, "open", nil, pgNow, nil, "any", nil, nil)
	requireArgs(t, "la página", stmts[1], pgTenant, "open", nil, pgNow, nil, "any", nil, nil, DefaultPageSize, 0)
	if !strings.Contains(stmts[0].query, "count(*)") || !strings.Contains(stmts[1].query, "LIMIT $9 OFFSET $10") {
		t.Errorf("la primera sentencia no cuenta o la segunda no pagina:\n%s\n%s", stmts[0].query, stmts[1].query)
	}
}

// TestListEvents_PassesEveryFilter: cada filtro viaja en su hueco, con el desplazamiento de la
// página; un conjunto de tipos VACÍO viaja como lista vacía, no como NULL (ningún tipo pasa).
func TestListEvents_PassesEveryFilter(t *testing.T) {
	stale := true
	h := newFakeStore(t, pgOne(int64(0)), pgReply{}, pgOne(int64(0)), pgReply{})

	_, err := h.store.ListEvents(t.Context(), pgTenant, ListFilter{
		Status: StatusClosed, Kind: "cart", Kinds: []string{"cart", "media"}, Content: ContentNone,
		Stale: &stale, ContactID: pgContact, Page: 3, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	page, err := h.store.ListEvents(t.Context(), pgTenant, ListFilter{Kinds: []string{}, PageSize: MaxPageSize + 1})
	if err != nil || page.PageSize != MaxPageSize || len(page.Events) != 0 {
		t.Fatalf("ListEvents con el conjunto vacío = (%+v, %v)", page, err)
	}

	stmts := requireStatements(t, h.fake, 4)
	requireArgs(t, "la cuenta", stmts[0], pgTenant, "closed", "cart", pgNow, pgContact, "none", true, []string{"cart", "media"})
	requireArgs(t, "la página", stmts[1], pgTenant, "closed", "cart", pgNow, pgContact, "none", true, []string{"cart", "media"}, 10, 20)
	if kinds, ok := stmts[2].args[7].([]string); !ok || kinds == nil || len(kinds) != 0 {
		t.Errorf("el conjunto vacío viajó como %#v, quería una lista vacía que no sea NULL", stmts[2].args[7])
	}
	requireArgs(t, "la página saneada", stmts[3], pgTenant, "open", nil, pgNow, nil, "any", nil, []string{}, MaxPageSize, 0)
}

// TestListEvents_QueryShape: el listado reusa las piezas del rescate —el origen, la marca y el
// orden— y filtra «vencido» sobre la columna CALCULADA, fuera de la subconsulta (INV-19).
func TestListEvents_QueryShape(t *testing.T) {
	h := newFakeStore(t, pgOne(int64(0)))
	if _, err := h.store.ListEvents(t.Context(), pgTenant, ListFilter{}); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	query := requireStatements(t, h.fake, 2)[1].query
	for _, want := range []string{
		"LEFT JOIN public.event_content c",
		"e.tenant_id = $1",
		"e.status = $2",
		"($5::uuid IS NULL OR e.contact_id = $5)",
		"($8::text[] IS NULL OR e.kind = ANY($8))",
		"($7::boolean IS NULL OR e.stale = $7)",
		"ORDER BY e.last_activity_at DESC, e.id",
	} {
		if !strings.Contains(query, want) {
			t.Errorf("el listado no contiene %q:\n%s", want, query)
		}
	}
}

// TestListEvents_Failures_Wrapped: si falla la cuenta no se pide la página; si falla la página,
// tampoco hay página a medias.
func TestListEvents_Failures_Wrapped(t *testing.T) {
	h := newFakeStore(t, pgFails())
	page, err := h.store.ListEvents(t.Context(), pgTenant, ListFilter{})
	requireWrapped(t, "la cuenta", err, "events: contar los eventos del tenant: ", errPgBoom)
	if page.Events != nil || page.Total != 0 || page.Page != 0 {
		t.Errorf("página = %+v, quería la página cero", page)
	}
	requireStatements(t, h.fake, 1)

	h = newFakeStore(t, pgOne(int64(3)), pgFails())
	page, err = h.store.ListEvents(t.Context(), pgTenant, ListFilter{})
	requireWrapped(t, "la página", err, "events: listar los eventos del tenant: ", errPgBoom)
	if page.Events != nil || page.Total != 0 {
		t.Errorf("página = %+v, quería la página cero", page)
	}
}
