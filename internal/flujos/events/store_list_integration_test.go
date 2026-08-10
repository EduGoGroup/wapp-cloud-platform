package events_test

// Integración del listado del dueño (T3.9b, REQ-28) contra Postgres REAL.
//
// Aquí es donde se prueba lo que el plan pide de verdad: que `content=none&
// stale=true` devuelva el survey abandonado a medias. Un test con doble no serviría
// —«vencido» lo calcula la BD con el TTL del tenant, y «sin contenido» sale de un
// LEFT JOIN—, así que lo que se afirma es el resultado de la consulta, no el de un
// fake que ya sabía la respuesta.
//
// Reusa los helpers de store_integration_test.go (mismo paquete): openTestDB,
// seedTenant, nuevoStore, mustCrear, insertarIntake, ponerStatusIntake, fijarTTL y
// forzarStatusEvento.

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// ── La escena de Jhoan: 1 de enero → 15 de enero ─────────────────────────────

// enero1 y enero15 son los dos instantes del ejemplo del design (§(f)): el evento
// nace el 1 de enero y el dueño mira el 15. Con un TTL de 1 h, todo lo del día 1
// está vencido de sobra el día 15 — y sigue en la lista, que es el punto.
var (
	enero1  = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	enero15 = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
)

// Los contactos de la escena. Son UUID porque conversation_events.contact_id lo es
// (identificador OPACO, ADR-0017): en esta tabla no cabe un nombre ni aunque el test
// lo quisiera, y por eso los nombres viven en el nombre de la constante.
const (
	contactoMarta    = "cccccccc-0000-4000-8000-000000000a01"
	contactoHerminia = "cccccccc-0000-4000-8000-000000000b02"
	contactoAjeno    = "cccccccc-0000-4000-8000-000000000c03"
)

// bandeja es el escenario común: un tenant con cuatro eventos que cubren las tres
// respuestas de `content` y las dos de `stale`, más un quinto de OTRO tenant.
type bandeja struct {
	db      *sql.DB
	store   *events.Store
	reloj   *relojFijo
	tenant  string
	otro    string
	survey  string // sin intake, del 1 de enero  ⇒ content=none, stale=true
	menu    string // sin intake, de HOY          ⇒ content=none, stale=false
	cart    string // intake OPEN, del 1 de enero ⇒ content=alive, stale=true
	cartH   string // intake OPEN, de otro contacto ⇒ content=alive
	descart string // intake ABANDONED            ⇒ NO se lista (INV-17)
	ajeno   string // del otro tenant
}

// montarBandeja siembra la escena y devuelve el contexto con el que consultarla. El
// reloj arranca el 1 de enero y se deja en el 15: así los eventos nacen con la fecha
// del ejemplo y la consulta se hace desde «hoy», sin depender de la hora real de la
// máquina.
//
// El ctx se CREA aquí en vez de recibirse porque openTestDB y seedTenant traen el
// suyo (context.Background), y una función que recibe un ctx y llama a otras que se
// lo inventan es justo lo que contextcheck señala: el de fuera no gobernaría nada.
func montarBandeja(t *testing.T) (context.Context, bandeja) {
	t.Helper()
	db := openTestDB(t)
	st, reloj := nuevoStore(t, db, enero1)
	tenant, otro := seedTenant(t, db), seedTenant(t, db)
	ctx := context.Background()
	fijarTTL(ctx, t, db, tenant, 3600) // 1 hora, el TTL del ejemplo
	fijarTTL(ctx, t, db, otro, 3600)

	// El contenido lo declara el HIJO (D-043.21): primero nace el evento, después
	// la solicitud apunta a él con su event_id.
	b := bandeja{db: db, store: st, reloj: reloj, tenant: tenant, otro: otro}
	b.survey = mustCrear(ctx, t, st, nuevoEvento(tenant, "sess-a", contactoMarta, "survey")).ID
	b.cart = mustCrear(ctx, t, st, nuevoEvento(tenant, "sess-a", contactoMarta, "cart")).ID
	insertarIntake(ctx, t, db, tenant, "sess-a", contactoMarta, "open", b.cart)
	b.descart = mustCrear(ctx, t, st, nuevoEvento(tenant, "sess-b", contactoHerminia, "cart")).ID
	insertarIntake(ctx, t, db, tenant, "sess-b", contactoHerminia, "abandoned", b.descart)
	// Herminia tiene ADEMÁS un carrito vivo. Sin él, su filtro por contacto
	// devolvería la lista vacía y no se distinguiría «lo suyo está tapado por el
	// predicado» de «este contacto no tiene nada».
	b.cartH = mustCrear(ctx, t, st, nuevoEvento(tenant, "sess-c", contactoHerminia, "cart")).ID
	insertarIntake(ctx, t, db, tenant, "sess-c", contactoHerminia, "open", b.cartH)
	b.ajeno = mustCrear(ctx, t, st, nuevoEvento(otro, "sess-x", contactoAjeno, "survey")).ID

	// El menú nace el 15: es el único que NO está vencido, y su papel es demostrar
	// que `stale=true` filtra de verdad en vez de devolverlo todo.
	reloj.t = enero15
	b.menu = mustCrear(ctx, t, st, nuevoEvento(tenant, "sess-a", contactoMarta, "menu")).ID
	return ctx, b
}

// idsPagina extrae los ids de la página, en orden.
func idsPagina(p events.EventPage) []string {
	out := make([]string, 0, len(p.Events))
	for _, ev := range p.Events {
		out = append(out, ev.ID)
	}
	return out
}

// mustListar ejecuta el listado o falla.
func mustListarEventos(ctx context.Context, t *testing.T, st *events.Store,
	tenantID string, f events.ListFilter) events.EventPage {
	t.Helper()
	p, err := st.ListEvents(ctx, tenantID, f)
	if err != nil {
		t.Fatalf("ListEvents(%+v): %v", f, err)
	}
	return p
}

func verdadero() *bool { v := true; return &v }
func falso() *bool     { v := false; return &v }

// TestIntegration_ListadoContentNoneStaleTrueEsElSurveyAMedias es EL criterio de
// T3.9b: `content=none&stale=true` devuelve el survey abandonado a medias.
//
// Los otros tres eventos del tenant están ahí para que el acierto no pueda ser
// casualidad: el menú es «sin contenido» pero NO está vencido, el carrito está
// vencido pero SÍ tiene contenido, y el descartado no es ninguna de las dos cosas.
// Con cualquiera de las dos mitades del filtro rota, la lista trae compañía.
func TestIntegration_ListadoContentNoneStaleTrueEsElSurveyAMedias(t *testing.T) {
	ctx, b := montarBandeja(t)

	got := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{
		Content: events.ContentNone, Stale: verdadero(),
	})
	if ids := idsPagina(got); !slices.Equal(ids, []string{b.survey}) {
		t.Fatalf("content=none&stale=true devolvió %v; quiero SOLO el survey (%s)", ids, b.survey)
	}
	if got.Total != 1 {
		t.Fatalf("total=%d, quiero 1: el conteo tiene que filtrar igual que la página", got.Total)
	}
	if !got.Events[0].Stale {
		t.Fatalf("el survey llega SIN la marca «vencido» pese a filtrarse por ella: %+v", got.Events[0])
	}
}

// TestIntegration_ListadoElVencidoSigueEnLaLista es INV-19 medido sobre datos: sin
// pedir `stale`, el evento del 1 de enero SIGUE saliendo el 15 y llega MARCADO.
//
// Es la mitad que un test del SQL no puede dar: que la marca informe y no filtre se
// ve mirando el WHERE, pero que el vencido siga apareciendo catorce días después
// solo se ve preguntándoselo a la BD.
func TestIntegration_ListadoElVencidoSigueEnLaLista(t *testing.T) {
	ctx, b := montarBandeja(t)

	got := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{})
	ids := idsPagina(got)
	if len(ids) != 4 || got.Total != 4 {
		t.Fatalf("sin filtros: %v (total=%d); quiero los CUATRO abiertos del tenant", ids, got.Total)
	}
	// Orden: última actividad DESC. El menú es de hoy, los otros tres del 1 de enero.
	if ids[0] != b.menu {
		t.Fatalf("el primero es %s; con ORDER BY last_activity_at DESC tiene que ser el menú (%s)",
			ids[0], b.menu)
	}
	marca := map[string]bool{}
	for _, ev := range got.Events {
		marca[ev.ID] = ev.Stale
	}
	if !marca[b.survey] || !marca[b.cart] || !marca[b.cartH] {
		t.Fatalf("los tres del 1 de enero tienen que llegar VENCIDOS: %v", marca)
	}
	if marca[b.menu] {
		t.Fatalf("el menú es de hoy y llega marcado vencido: %v", marca)
	}
}

// TestIntegration_ListadoContentReparteLasTresRespuestas: `none`, `alive` y `any`
// son tres conjuntos distintos, y el que los distingue es el LEFT JOIN con la
// vista event_content.
//
// El evento con la solicitud DESCARTADA es el que da sentido a la diferencia: no
// tiene contenido vivo, pero tampoco es «sin contenido» — y el dueño tiene que poder
// verlo, porque sigue abierto y solo …/cancel lo cierra.
func TestIntegration_ListadoContentReparteLasTresRespuestas(t *testing.T) {
	ctx, b := montarBandeja(t)

	casos := []struct {
		content events.ContentFilter
		quiero  []string
	}{
		{events.ContentNone, []string{b.menu, b.survey}},
		{events.ContentAlive, []string{b.cart, b.cartH}},
		// `any` son las DOS mitades vivas, no «todo»: el descartado no está en
		// ninguna de las tres respuestas (INV-17).
		{events.ContentAny, []string{b.menu, b.survey, b.cart, b.cartH}},
	}
	for _, c := range casos {
		got := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{Content: c.content})
		ids := idsPagina(got)
		slices.Sort(ids)
		quiero := slices.Clone(c.quiero)
		slices.Sort(quiero)
		if !slices.Equal(ids, quiero) {
			t.Fatalf("content=%s devolvió %v; quiero %v", c.content, ids, quiero)
		}
	}
	// Y `stale=false` es la otra mitad del tri-estado: solo el que NO está vencido.
	got := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{Stale: falso()})
	if ids := idsPagina(got); !slices.Equal(ids, []string{b.menu}) {
		t.Fatalf("stale=false devolvió %v; quiero solo el menú (%s)", ids, b.menu)
	}
}

// TestIntegration_ListadoAisladoPorTenant: el listado de un tenant NO alcanza al
// otro NI CON el mismo filtro que allí acierta (INV-8).
//
// La segunda mitad es la que hace que la primera signifique algo: el evento del otro
// tenant EXISTE y casa con el filtro —se comprueba pidiéndoselo a su dueño—, así que
// su ausencia en la lista del primero es el aislamiento y no un fixture vacío.
func TestIntegration_ListadoAisladoPorTenant(t *testing.T) {
	ctx, b := montarBandeja(t)
	filtro := events.ListFilter{Content: events.ContentNone}

	mío := mustListarEventos(ctx, t, b.store, b.tenant, filtro)
	if slices.Contains(idsPagina(mío), b.ajeno) {
		t.Fatalf("la lista del tenant trae el evento ajeno %s: %v", b.ajeno, idsPagina(mío))
	}
	ajena := mustListarEventos(ctx, t, b.store, b.otro, filtro)
	if ids := idsPagina(ajena); !slices.Equal(ids, []string{b.ajeno}) {
		t.Fatalf("con el filtro que acierta, su dueño ve %v; quiero [%s] — si esto sale vacío, "+
			"el test de aislamiento no probaba nada", ids, b.ajeno)
	}
}

// TestIntegration_ListadoStatusYKind: los otros dos filtros de REQ-28, y el default
// `open` — lo que se limpia es lo que sigue abierto, no lo que ya se cerró.
func TestIntegration_ListadoStatusYKind(t *testing.T) {
	ctx, b := montarBandeja(t)

	// Se cancela el survey: desaparece del default y aparece pidiéndolo por estado.
	mustTransitar(ctx, t, b.store, b.survey, events.StatusCancelled)

	abiertos := idsPagina(mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{}))
	if slices.Contains(abiertos, b.survey) {
		t.Fatalf("el survey cancelado sigue en el default (`open`): %v", abiertos)
	}
	cancelados := mustListarEventos(ctx, t, b.store, b.tenant,
		events.ListFilter{Status: events.StatusCancelled})
	if ids := idsPagina(cancelados); !slices.Equal(ids, []string{b.survey}) {
		t.Fatalf("status=cancelled devolvió %v; quiero el survey (%s)", ids, b.survey)
	}

	// Y el tipo: dos carritos abiertos, ninguno más.
	carritos := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{Kind: "cart"})
	ids := idsPagina(carritos)
	slices.Sort(ids)
	quiero := []string{b.cart, b.cartH}
	slices.Sort(quiero)
	if !slices.Equal(ids, quiero) {
		t.Fatalf("kind=cart devolvió %v; quiero %v", ids, quiero)
	}
	// Un tipo que nadie usa devuelve la lista vacía y total 0, no un error.
	vacío := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{Kind: "no-existe"})
	if len(vacío.Events) != 0 || vacío.Total != 0 {
		t.Fatalf("kind inexistente devolvió %+v; quiero vacío", vacío)
	}
}

// TestIntegration_ListadoContactoYPaginación: el filtro por contacto y el LIMIT/
// OFFSET, con el TOTAL diciendo cuántos hay en TODO el filtro y no en la página —
// que es justo para lo que la consola lo necesita.
func TestIntegration_ListadoContactoYPaginación(t *testing.T) {
	ctx, b := montarBandeja(t)

	// marta tiene tres (menu, survey, cart); herminia uno (el descartado).
	deHerminia := mustListarEventos(ctx, t, b.store, b.tenant,
		events.ListFilter{ContactID: contactoHerminia})
	if ids := idsPagina(deHerminia); !slices.Equal(ids, []string{b.cartH}) {
		t.Fatalf("contact_id=<herminia> devolvió %v; quiero [%s] — el descartado (%s) "+
			"no se lista ni filtrando por su contacto", ids, b.cartH, b.descart)
	}

	// Página a página, con tamaño 1 y el orden de la lista completa.
	completa := idsPagina(mustListarEventos(ctx, t, b.store, b.tenant,
		events.ListFilter{ContactID: contactoMarta}))
	if len(completa) != 3 {
		t.Fatalf("marta tiene %d eventos abiertos, quiero 3: %v", len(completa), completa)
	}
	for i, quiero := range completa {
		pág := mustListarEventos(ctx, t, b.store, b.tenant,
			events.ListFilter{ContactID: contactoMarta, Page: i + 1, PageSize: 1})
		if ids := idsPagina(pág); !slices.Equal(ids, []string{quiero}) {
			t.Fatalf("página %d devolvió %v; quiero [%s]", i+1, ids, quiero)
		}
		if pág.Total != 3 {
			t.Fatalf("página %d: total=%d; el total es del FILTRO, no de la página", i+1, pág.Total)
		}
	}
	// Una página más allá del final es vacía, no un error.
	fuera := mustListarEventos(ctx, t, b.store, b.tenant,
		events.ListFilter{ContactID: contactoMarta, Page: 99, PageSize: 1})
	if len(fuera.Events) != 0 || fuera.Total != 3 {
		t.Fatalf("página 99 devolvió %d filas y total=%d; quiero 0 y 3", len(fuera.Events), fuera.Total)
	}
}

// TestIntegration_ListadoTTLCeroNoVenceNada: el override «sin vencimiento» del
// tenant (E-6) manda también aquí. Con TTL 0, `stale=true` no devuelve NADA aunque
// los eventos lleven catorce días quietos — y `stale=false` los devuelve todos.
//
// Es el mismo `> 0` que protege la marca en el rescate, medido desde el listado:
// un tenant que apagó el reloj no puede ver su bandeja entera pintada de rojo.
func TestIntegration_ListadoTTLCeroNoVenceNada(t *testing.T) {
	ctx, b := montarBandeja(t)
	fijarTTL(ctx, t, b.db, b.tenant, 0)

	vencidos := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{Stale: verdadero()})
	if len(vencidos.Events) != 0 || vencidos.Total != 0 {
		t.Fatalf("con TTL 0 hay %d vencidos (total=%d); 0 es «sin vencimiento», no «vence al instante»",
			len(vencidos.Events), vencidos.Total)
	}
	vivos := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{Stale: falso()})
	if vivos.Total != 4 {
		t.Fatalf("con TTL 0, stale=false trae %d; quiero los cuatro", vivos.Total)
	}
}

// TestIntegration_ListadoElRelojEsElINYECTADO: la marca sale del reloj del store, no
// del de la BD. Se comprueba retrocediendo el reloj al 1 de enero: los mismos datos,
// la misma consulta y NINGÚN vencido.
//
// Sin esto, un `now()` de servidor pasaría todos los tests anteriores (los datos son
// viejos de verdad) y el criterio del plan —«el 15 de enero sigue en la lista»— no
// sería afirmable: dependería del día en que se corre el test.
func TestIntegration_ListadoElRelojEsElINYECTADO(t *testing.T) {
	ctx, b := montarBandeja(t)

	b.reloj.t = enero1.Add(30 * time.Minute) // media hora después de nacer, TTL de 1 h
	got := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{})
	for _, ev := range got.Events {
		if ev.Stale {
			t.Fatalf("con el reloj en el 1 de enero, %s llega vencido: la marca está mirando "+
				"el reloj de la BD y no el inyectado", ev.ID)
		}
	}
	vencidos := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{Stale: verdadero()})
	if len(vencidos.Events) != 0 {
		t.Fatalf("con el reloj en el 1 de enero hay %d vencidos, quiero 0", len(vencidos.Events))
	}
}

// TestIntegration_ListadoElDescartadoDESAPARECE es el criterio literal de T3.9:
// «tras POST /api/v1/intakes/discard … desaparece de la lista».
//
// Se hace con el carrito VIVO y a mitad de test —descartando su solicitud como lo
// hace el Plan 041, sin tocar el evento— para que la desaparición sea un CAMBIO
// observado y no el estado inicial de un fixture: antes está en las tres respuestas
// de `content`, después en ninguna. Lo tapa el predicado, no el otro plan: la fila
// sigue `open` en la tabla, intacta (INV-09).
func TestIntegration_ListadoElDescartadoDESAPARECE(t *testing.T) {
	ctx, b := montarBandeja(t)

	antes := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{})
	if !slices.Contains(idsPagina(antes), b.cart) {
		t.Fatalf("el carrito vivo no estaba en la bandeja ANTES de descartarlo: %v", idsPagina(antes))
	}

	// La ligadura vive del lado del HIJO (D-043.21): la solicitud del carrito se
	// encuentra por su event_id, no por ninguna columna del evento.
	var intakeID string
	if err := b.db.QueryRowContext(ctx,
		`SELECT id FROM public.intakes WHERE event_id = $1`, b.cart).
		Scan(&intakeID); err != nil {
		t.Fatalf("leer la solicitud del carrito: %v", err)
	}
	ponerStatusIntake(ctx, t, b.db, intakeID, "abandoned")

	for _, c := range []events.ContentFilter{events.ContentAny, events.ContentAlive, events.ContentNone} {
		got := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{Content: c})
		if slices.Contains(idsPagina(got), b.cart) {
			t.Fatalf("content=%s sigue enseñando el carrito descartado: %v (INV-17: "+
				"no se lista, no se rescata, no se menciona)", c, idsPagina(got))
		}
	}

	// Y NO se ha borrado nada: la fila sigue viva en la tabla, que es la otra mitad
	// del invariante. Lo que cambió es quién la ve.
	var status string
	if err := b.db.QueryRowContext(ctx,
		`SELECT status FROM public.conversation_events WHERE id = $1`, b.cart).
		Scan(&status); err != nil {
		t.Fatalf("releer el evento: %v", err)
	}
	if status != string(events.StatusOpen) {
		t.Fatalf("el evento quedó en %q: descartar la solicitud NO cierra el evento (eso es …/cancel)", status)
	}
}

// TestIntegration_ListadoSoloLosTiposHabilitados es la mitad de CONTENIDO de la
// decisión del 2026-08-09, medida contra la BD: con los tipos del plan acotados a
// `survey`, la bandeja trae la encuesta y nada más.
//
// El segundo caso es el que de verdad protege: una lista VACÍA de tipos no puede
// significar «no filtres». Si `kind = ANY('{}')` se convirtiera en «sin filtro», el
// tenant sin ninguna feature vería la bandeja entera.
func TestIntegration_ListadoSoloLosTiposHabilitados(t *testing.T) {
	ctx, b := montarBandeja(t)

	soloEncuestas := mustListarEventos(ctx, t, b.store, b.tenant,
		events.ListFilter{Kinds: []string{"survey"}})
	if ids := idsPagina(soloEncuestas); !slices.Equal(ids, []string{b.survey}) {
		t.Fatalf("con Kinds=[survey] la bandeja trae %v; quiero solo la encuesta (%s)", ids, b.survey)
	}
	if soloEncuestas.Total != 1 {
		t.Fatalf("total=%d con Kinds=[survey]; el conteo filtra igual que la página", soloEncuestas.Total)
	}

	ninguno := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{Kinds: []string{}})
	if len(ninguno.Events) != 0 || ninguno.Total != 0 {
		t.Fatalf("con Kinds=[] salen %d filas (total=%d): una lista vacía es «ningún tipo», "+
			"no «sin filtro»", len(ninguno.Events), ninguno.Total)
	}
	// Y nil sigue siendo «sin filtro»: los cuatro visibles.
	sinFiltro := mustListarEventos(ctx, t, b.store, b.tenant, events.ListFilter{Kinds: nil})
	if sinFiltro.Total != 4 {
		t.Fatalf("con Kinds=nil salen %d; quiero los cuatro listables del tenant", sinFiltro.Total)
	}
}
