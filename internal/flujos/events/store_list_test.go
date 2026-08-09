package events

// Tests INTERNOS del listado del dueño (T3.9b): afirman sobre la FORMA del SQL,
// que es lo que un test de caja negra no puede mirar. Lo que la consulta devuelve
// contra Postgres real vive en store_list_integration_test.go.

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
)

// whereSegments devuelve el texto de CADA cláusula WHERE de la consulta (la del
// listado tiene dos: la de la subconsulta y la del vencido). Corta en el primer
// terminador que aparezca, para que el SELECT de la subconsulta —donde el
// vencimiento SÍ se calcula— no se cuele dentro de un WHERE por accidente.
func whereSegments(sql string) []string {
	var out []string
	rest := sql
	for {
		i := strings.Index(rest, "WHERE")
		if i < 0 {
			return out
		}
		rest = rest[i+len("WHERE"):]
		fin := len(rest)
		for _, marca := range []string{"ORDER BY", "\n) e", "LIMIT"} {
			if j := strings.Index(rest, marca); j >= 0 && j < fin {
				fin = j
			}
		}
		out = append(out, rest[:fin])
	}
}

// TestINV19_ElListadoDelDuenoNoComparaFechasEnNingunWHERE es el test de regresión
// heredado de T3.9a, aplicado a la consulta que T3.9b añade.
//
// Es el fallo que más fácil sería cometer aquí: el endpoint acepta `stale=true`, y
// la forma «obvia» de servirlo es meter `now() - last_activity_at > ttl` en el
// WHERE. Eso rompería INV-19 en la consulta del listado y —peor— en la del rescate
// si alguien «unificara» las dos: el vencido dejaría de informar para empezar a
// decidir, y un pedido de hace dos horas desaparecería de la vista de quien tiene
// que limpiarlo.
//
// Se miran las DOS consultas y TODOS sus WHERE. `last_activity_at` está prohibido
// dentro de un WHERE y permitido fuera: el ORDER BY es suyo.
func TestINV19_ElListadoDelDuenoNoComparaFechasEnNingunWHERE(t *testing.T) {
	prohibidos := []string{"last_activity_at", "now(", "make_interval", "$4", "interval",
		"ttl", "event_inactivity"}

	for _, c := range []struct{ nombre, sql string }{
		{"listEventsSQL", listEventsSQL},
		{"countEventsSQL", countEventsSQL},
	} {
		segs := whereSegments(c.sql)
		if len(segs) != 2 {
			t.Fatalf("%s: %d cláusulas WHERE, quiero 2 (la del filtro y la del vencido):\n%s",
				c.nombre, len(segs), c.sql)
		}
		for i, where := range segs {
			for _, prohibido := range prohibidos {
				if strings.Contains(where, prohibido) {
					t.Fatalf("%s: el WHERE %d contiene %q: el vencimiento marca, no filtra (INV-19). WHERE:\n%s",
						c.nombre, i, prohibido, where)
				}
			}
		}
		t.Logf("%s · WHERE del filtro:%s\n%s · WHERE del vencido:%s",
			c.nombre, segs[0], c.nombre, segs[1])
	}
}

// TestListado_ElVencidoFiltraSobreLaColumnaCalculada es la otra mitad del test
// anterior: no basta con que el WHERE no compare fechas, tiene que filtrar por la
// marca YA CALCULADA. Sin esta comprobación, borrar el filtro entero —y devolver
// vencidos y no vencidos ante `stale=true`— pasaría el test de INV-19 tan campante.
func TestListado_ElVencidoFiltraSobreLaColumnaCalculada(t *testing.T) {
	segs := whereSegments(listEventsSQL)
	vencido := segs[len(segs)-1]
	if !strings.Contains(vencido, "e.stale = $7") {
		t.Fatalf("el filtro de vencido debe leer la COLUMNA calculada (e.stale = $7); WHERE:\n%s", vencido)
	}
	// Y tri-estado: ausente NO puede significar «los no vencidos».
	if !strings.Contains(vencido, "$7::boolean IS NULL OR") {
		t.Fatalf("sin `$7 IS NULL OR`, no pedir el filtro dejaría fuera a los vencidos; WHERE:\n%s", vencido)
	}
}

// TestListado_ReusaLasPiezasDelRescate fija lo que REQ-28 exige: que esta consulta
// no sea una SEGUNDA consulta, sino la misma en otro orden.
//
// Lo que este test SÍ caza —y se comprobó rompiéndolo— es la DIVERGENCIA: con una
// copia del FROM pegada aquí, el día que alguien toque `rescuableFrom` (un JOIN
// más, una condición nueva) las dos dejan de coincidir y el test lo dice. Lo que
// NO puede ver es una copia recién hecha byte a byte, porque el string compuesto
// sale idéntico; contra eso solo hay revisión. Se deja escrito para que nadie le
// atribuya una garantía que no da.
func TestListado_ReusaLasPiezasDelRescate(t *testing.T) {
	for nombre, pieza := range map[string]string{
		"rescuableFrom":  rescuableFrom,
		"rescuableStale": rescuableStale,
		"rescuableOrder": rescuableOrder,
		"contentNone":    contentNone,
		"contentAlive":   contentAlive,
	} {
		if !strings.Contains(listEventsSQL, pieza) {
			t.Fatalf("listEventsSQL no reusa %s TAL CUAL (hay una segunda consulta escrita a mano):\n%s",
				nombre, listEventsSQL)
		}
	}
	// Y la del rescate sigue componiendo su condición de contenido con las MISMAS
	// dos mitades: partirla en piezas no puede haber cambiado lo que filtra.
	if !strings.Contains(selectRescuableSQL, "(e.intake_id IS NULL OR i.status = 'open')") {
		t.Fatalf("la condición de contenido del rescate cambió al partirla:\n%s", selectRescuableSQL)
	}
}

// TestListado_LaCuentaLlegaHastaOcho protege un fallo que solo aparece contra
// Postgres y con un mensaje que no señala la causa («bind message supplies 10
// parameters, but prepared statement requires 8»): la consulta de conteo no lleva
// paginación, así que se invoca con OCHO argumentos y el listado con DIEZ. Si
// alguien mete un `$9` en la parte común, el conteo revienta en producción y aquí
// se ve al instante.
//
// El número lo fija listArgs, así que se comprueba contra ÉL y no contra un 8
// escrito a mano: añadir un filtro y olvidar su argumento (o al revés) es
// exactamente el descuido que este test existe para cazar.
func TestListado_LaCuentaLlegaHastaOcho(t *testing.T) {
	args := (&Store{now: time.Now}).listArgs("t", ListFilter{}.Normalized())
	if len(args) != 8 {
		t.Fatalf("listArgs devuelve %d argumentos; el SQL del conteo espera 8", len(args))
	}
	for _, p := range []string{"$9", "$10"} {
		if strings.Contains(countEventsSQL, p) {
			t.Fatalf("countEventsSQL referencia %s (paginación): se invoca con 8 args:\n%s",
				p, countEventsSQL)
		}
		if !strings.Contains(listEventsSQL, p) {
			t.Fatalf("listEventsSQL no usa %s: falta la paginación:\n%s", p, listEventsSQL)
		}
	}
	for i := 1; i <= len(args); i++ {
		p := "$" + strconv.Itoa(i)
		if !strings.Contains(countEventsSQL, p) {
			t.Fatalf("countEventsSQL no usa %s: los 8 argumentos tienen que estar TODOS "+
				"(Postgres rechaza los que sobran):\n%s", p, countEventsSQL)
		}
	}
}

// TestListado_ElFiltroDeTiposEsCERRADO fija en el SQL la decisión del 2026-08-09 y,
// sobre todo, su lado peligroso: el filtro por los tipos del plan tiene que poder
// significar «ninguno».
//
// La mitad `IS NULL` es «no filtres»; una lista VACÍA tiene que dejar fuera a todo
// (`kind = ANY('{}')` es falso). Si alguien «simplifica» listArgs a `len(f.Kinds) > 0`,
// un tenant sin ninguna feature vería la bandeja ENTERA — y el gate no lo taparía,
// porque quien llame al store sin pasar por HTTP se salta el middleware.
func TestListado_ElFiltroDeTiposEsCERRADO(t *testing.T) {
	if !strings.Contains(listEventsWhere, "$8::text[] IS NULL OR e.kind = ANY($8)") {
		t.Fatalf("falta el filtro por tipos habilitados en el WHERE:\n%s", listEventsWhere)
	}
	st := &Store{now: time.Now}
	sinFiltro := st.listArgs("t", ListFilter{}.Normalized())
	if sinFiltro[7] != nil {
		t.Fatalf("Kinds nil debe ir como NULL (sin filtro), y fue %#v", sinFiltro[7])
	}
	vacío := st.listArgs("t", ListFilter{Kinds: []string{}}.Normalized())
	if vacío[7] == nil {
		t.Fatalf("una lista VACÍA se convirtió en NULL: eso enseña TODO justo al tenant " +
			"que no puede ver nada")
	}
}

// TestListado_ContentAnyEsElPredicadoDelRescate: `any` NO es «sin filtro de
// contenido». Es rescuableContent —las dos mitades vivas— y por eso un evento cuya
// solicitud se descartó desaparece de la bandeja (INV-17, criterio de T3.9).
//
// Sin esta comprobación, «arreglar» el listado para que `any` no filtre nada pasaría
// todos los demás tests del SQL y rompería el invariante en la única superficie
// donde se ve.
func TestListado_ContentAnyEsElPredicadoDelRescate(t *testing.T) {
	quiero := "$6::text = '" + string(ContentAny) + "' AND " + rescuableContent
	if !strings.Contains(listEventsWhere, quiero) {
		t.Fatalf("`content=any` debe componer %s; WHERE:\n%s", quiero, listEventsWhere)
	}
}

// TestListado_NormalizedAcotaLaPagina: los defaults y el tope del contrato
// (REQ-28), incluida la idempotencia — el handler normaliza y el store vuelve a
// hacerlo, así que normalizar dos veces no puede cambiar nada.
func TestListado_NormalizedAcotaLaPagina(t *testing.T) {
	casos := []struct {
		nombre        string
		in            ListFilter
		quieroTamaño  int
		quieroPágina  int
		quieroEstado  Status
		quieroContent ContentFilter
	}{
		{"vacío", ListFilter{}, DefaultPageSize, 1, StatusOpen, ContentAny},
		{"tope", ListFilter{PageSize: 100000}, MaxPageSize, 1, StatusOpen, ContentAny},
		{"negativos", ListFilter{Page: -3, PageSize: -1}, DefaultPageSize, 1, StatusOpen, ContentAny},
		{"respeta lo pedido", ListFilter{Page: 3, PageSize: 10, Status: StatusCancelled,
			Content: ContentNone}, 10, 3, StatusCancelled, ContentNone},
	}
	for _, c := range casos {
		got := c.in.Normalized()
		if got.PageSize != c.quieroTamaño || got.Page != c.quieroPágina ||
			got.Status != c.quieroEstado || got.Content != c.quieroContent {
			t.Fatalf("%s: Normalized=%+v; quiero page=%d size=%d status=%s content=%s",
				c.nombre, got, c.quieroPágina, c.quieroTamaño, c.quieroEstado, c.quieroContent)
		}
		// La comparación va campo a campo y no con `!=`: ListFilter lleva un slice
		// desde que existe el filtro por tipos del plan, así que ya no es comparable.
		if segunda := got.Normalized(); segunda.Page != got.Page || segunda.PageSize != got.PageSize ||
			segunda.Status != got.Status || segunda.Content != got.Content ||
			segunda.Kind != got.Kind || segunda.ContactID != got.ContactID ||
			segunda.Stale != got.Stale || !slices.Equal(segunda.Kinds, got.Kinds) {
			t.Fatalf("%s: Normalized NO es idempotente: %+v → %+v", c.nombre, got, segunda)
		}
	}
	// Y el offset, que es de donde sale el OFFSET del SQL.
	if got := (ListFilter{Page: 3, PageSize: 10}).Offset(); got != 20 {
		t.Fatalf("Offset de la página 3 con tamaño 10 = %d, quiero 20", got)
	}
}

// TestListado_ValidadoresRechazanElTypo: los dos validadores que el transporte usa
// para contestar 400 en vez de devolver una lista que no es la que se pidió.
func TestListado_ValidadoresRechazanElTypo(t *testing.T) {
	for _, v := range []string{"open", "closed", "cancelled"} {
		if !IsStatus(v) {
			t.Fatalf("IsStatus(%q) = false; es uno de los tres del ciclo de vida", v)
		}
	}
	for _, v := range []string{"", "abiertos", "OPEN", "expired", "paused"} {
		if IsStatus(v) {
			t.Fatalf("IsStatus(%q) = true; solo hay tres estados y `expired` no es uno", v)
		}
	}
	for _, v := range []string{"any", "none", "alive"} {
		if !IsContentFilter(v) {
			t.Fatalf("IsContentFilter(%q) = false", v)
		}
	}
	for _, v := range []string{"", "todos", "ANY", "null"} {
		if IsContentFilter(v) {
			t.Fatalf("IsContentFilter(%q) = true", v)
		}
	}
}

// ── Los tipos que el tenant PUEDE ver ────────────────────────────────────────

// TestKindFeatures_SonLasDelMapaDelDespachador: la lista con la que se gatea el
// listado sale del MISMO featurePorTipo que arma el menú del cliente. Si alguien
// añade un quinto tipo al mapa, el gate lo incluye solo — y este test lo dice
// contando, no repitiendo la lista a mano.
func TestKindFeatures_SonLasDelMapaDelDespachador(t *testing.T) {
	got := KindFeatures()
	if len(got) != len(featurePorTipo) {
		t.Fatalf("KindFeatures devuelve %d claves y el mapa tiene %d tipos: %v", len(got), len(featurePorTipo), got)
	}
	if !slices.IsSorted(got) {
		t.Fatalf("KindFeatures no viene ordenada (%v): el orden tiene que ser determinista", got)
	}
	for _, f := range featurePorTipo {
		if !slices.Contains(got, f) {
			t.Fatalf("KindFeatures no incluye %q, que sí habilita un tipo: %v", f, got)
		}
	}
}

// TestAllowedKinds_SoloLosQueElTenantTiene es la mitad de contenido de la decisión
// del 2026-08-09: entrar por una feature no da derecho a ver los tipos de las otras.
func TestAllowedKinds_SoloLosQueElTenantTiene(t *testing.T) {
	fake := entitlements.NewFake()
	fake.Enable("t1", entitlements.FeatureSurvey)
	fake.Enable("t1", entitlements.FeatureMenu)

	got, err := AllowedKinds(context.Background(), fake, "t1")
	if err != nil {
		t.Fatalf("AllowedKinds: %v", err)
	}
	if !slices.Equal(got, []string{"menu", "survey"}) {
		t.Fatalf("tipos=%v; quiero [menu survey] (los de sus dos features, en orden)", got)
	}

	// Un tenant sin NINGUNA devuelve la lista VACÍA, no nil: es la diferencia entre
	// «ningún tipo pasa» y «no filtres», y en el store significan cosas opuestas.
	vacío, err := AllowedKinds(context.Background(), entitlements.NewFake(), "t2")
	if err != nil {
		t.Fatalf("AllowedKinds sin features: %v", err)
	}
	if vacío == nil || len(vacío) != 0 {
		t.Fatalf("tipos del tenant sin features = %#v; quiero una lista vacía NO nil", vacío)
	}
}

// TestAllowedKinds_ElFalloSePROPAGA: un resolver caído no se traduce en «no tienes
// ninguno». El llamante decide qué hacer (el handler responde 500), pero lo que no
// puede pasar es que la bandeja se vacíe en silencio.
func TestAllowedKinds_ElFalloSePROPAGA(t *testing.T) {
	roto := entitlements.NewFake()
	roto.Err = errors.New("la BD de entitlements se cayó")
	if _, err := AllowedKinds(context.Background(), roto, "t1"); err == nil {
		t.Fatal("AllowedKinds se tragó el fallo del resolver: la lista vacía mentiría")
	}
	if _, err := AllowedKinds(context.Background(), nil, "t1"); err == nil {
		t.Fatal("AllowedKinds sin resolver debe fallar, no devolver «ningún tipo»")
	}
}
