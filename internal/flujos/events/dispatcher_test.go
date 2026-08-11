package events_test

import (
	"context"
	"errors"
	"go/build"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// ── Dobles de laboratorio ─────────────────────────────────────────────────────

// vivosFake es el AliveLister de los tests: devuelve lo sembrado y RECUERDA con
// qué terna se le preguntó, para poder afirmar el aislamiento (INV-8) en vez de
// suponerlo.
type vivosFake struct {
	eventos []events.Event
	// rescatables es lo que devuelve ListRescuable. Se siembra APARTE de eventos a
	// propósito: desde T3.6 los dos listados devuelven conjuntos distintos (el
	// carrito cuyo pedido descartó el dueño sigue vivo y ya no es rescatable), y un
	// fake que los colapsara haría imposible escribir esa diferencia.
	rescatables []events.Rescuable
	err         error
	errRescate  error
	pedido      events.ConversationRef
	// limitePedido es el tope con que se llamó a ListRescuable, para afirmar que se
	// pide un lote acotado y no la lista entera.
	limitePedido int
}

func (f *vivosFake) ListAlive(_ context.Context, tenantID, sessionID, contactID string) ([]events.Event, error) {
	f.pedido = events.ConversationRef{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	return f.eventos, f.err
}

func (f *vivosFake) ListRescuable(_ context.Context, tenantID, sessionID, contactID string, limit int) ([]events.Rescuable, error) {
	f.pedido = events.ConversationRef{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	f.limitePedido = limit
	if f.errRescate != nil {
		return nil, f.errRescate
	}
	if limit > 0 && len(f.rescatables) > limit {
		return f.rescatables[:limit], nil // el fake respeta el LIMIT, como la BD
	}
	return f.rescatables, nil
}

// rescatable fabrica un rescatable VIVO con id e history_id reconocibles.
func rescatable(kind, id, historyID string) events.Rescuable {
	return events.Rescuable{Event: vivo(kind, id, historyID)}
}

// ofertaFake es el KindOffer de los tests.
type ofertaFake struct {
	kinds []string
	err   error
}

func (f ofertaFake) OfferedKinds(_ context.Context, _, _ string) ([]string, error) {
	return f.kinds, f.err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// refDeTest es la conversación de laboratorio.
func refDeTest() events.ConversationRef {
	return events.ConversationRef{
		TenantID:  "11111111-1111-1111-1111-111111111111",
		SessionID: "sesion-A",
		ContactID: "22222222-2222-2222-2222-222222222222",
	}
}

// vivo fabrica un evento VIVO con id e history_id reconocibles: los dos son lo
// que el render NO puede enseñar.
func vivo(kind, id, historyID string) events.Event {
	return events.Event{ID: id, Kind: kind, HistoryID: historyID, Status: events.StatusOpen}
}

// conFeatures construye el Resolver de entitlements con las features dadas
// encendidas para el tenant de test.
func conFeatures(features ...string) *entitlements.Fake {
	f := entitlements.NewFake()
	for _, feature := range features {
		f.Enable(refDeTest().TenantID, feature)
	}
	return f
}

// construir arma el menú o falla el test.
func construir(t *testing.T, d *events.Dispatcher) events.Menu {
	t.Helper()
	m, err := d.Build(context.Background(), refDeTest())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return m
}

// lineaDe devuelve la línea del menú que empieza por el prefijo dado.
func lineaDe(t *testing.T, texto, prefijo string) string {
	t.Helper()
	for _, l := range strings.Split(texto, "\n") {
		if strings.HasPrefix(l, prefijo) {
			return l
		}
	}
	t.Fatalf("no hay línea que empiece por %q; texto:\n%s", prefijo, texto)
	return ""
}

// kindsDe extrae los tipos del menú en orden, para comparar sin escribir el
// número a mano en cada assert.
func kindsDe(m events.Menu) []string {
	out := make([]string, 0, len(m.Options))
	for _, o := range m.Options {
		out = append(out, string(o.Action)+":"+o.Kind)
	}
	return out
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestBuild_CriterioDeAceptacionCartVivoMasEncuesta es el criterio LITERAL de
// T2.3: tenant con cart y encuesta ofrecidos y un cart VIVO ⇒ el render lista
// opciones numeradas y en el texto no aparece NI UN identificador.
//
// El assert que importa es el negativo, y es fuerte a propósito: se busca en la
// cadena el UUID, el history_id, el «carrito_001» del enunciado y hasta los
// nombres técnicos de los tipos («cart» y «survey», que no son subcadena de
// «pedido» ni de «encuesta»). Si cualquiera se cuela, el cliente estaría
// leyendo un identificador nuestro.
func TestBuild_CriterioDeAceptacionCartVivoMasEncuesta(t *testing.T) {
	elCart := vivo("cart", "9f1d1b7e-0000-4000-8000-0000000000aa", "cart-2026-08-09-1830")
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{{Event: elCart}}},
		ofertaFake{kinds: []string{"cart", "survey"}},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey),
	)

	m := construir(t, d)
	if got := kindsDe(m); len(got) != 3 || got[0] != "start:cart" || got[1] != "start:survey" || got[2] != "resume:cart" {
		t.Fatalf("el menú debe ser [pedir un pedido, pedir encuesta, retomar el pedido vivo]; got %v", got)
	}

	texto := m.Render()
	for _, quiero := range []string{"1. ", "2. ", "3. ", "pedido", "encuesta"} {
		if !strings.Contains(texto, quiero) {
			t.Fatalf("el menú debe contener %q; texto:\n%s", quiero, texto)
		}
	}
	for _, prohibido := range []string{elCart.ID, elCart.HistoryID, "carrito_001", "cart", "survey"} {
		if strings.Contains(texto, prohibido) {
			t.Fatalf("el menú NO puede enseñar %q (E-3: por nombre de tipo, nunca por identificador); texto:\n%s",
				prohibido, texto)
		}
	}
}

// TestBuild_ElegirElCarritoConmuta es la otra mitad del criterio: elegir la
// opción de RETOMAR el carrito vivo devuelve su id, que es lo que el runtime
// necesita para conmutar flow_state.event_id (T2.2).
func TestBuild_ElegirElCarritoConmuta(t *testing.T) {
	elCart := vivo("cart", "9f1d1b7e-0000-4000-8000-0000000000aa", "cart-2026-08-09-1830")
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{{Event: elCart}}},
		ofertaFake{kinds: []string{"cart", "survey"}},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey),
	)

	elec, ok := construir(t, d).Resolve("3")
	if !ok {
		t.Fatalf("«3» debe resolver contra el menú")
	}
	if elec.Action != events.ActionResume || elec.EventID != elCart.ID || elec.Kind != "cart" {
		t.Fatalf("elegir el carrito vivo debe CONMUTAR a ese evento; got %+v", elec)
	}
}

// TestBuild_UnTipoConVivoApareceDosVeces es la norma del ADR-0029 §E-9.3: «1)
// Hacer un pedido» y «4) Retomar algo que dejaste a medias» conviven en la misma
// lista.
//
// Este test existe porque la implementación anterior hacía lo contrario —un tipo
// ocupado salía SOLO como rescate— y eso es la salida (iii) que el design
// planteó y que NO se eligió: le quitaba al cliente la posibilidad de pedir algo
// mientras tuviera uno vivo de ese tipo.
func TestBuild_UnTipoConVivoApareceDosVeces(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{rescatable("cart", "id-cart", "cart-2026-08-09-1830")}},
		ofertaFake{kinds: []string{"cart"}},
		conFeatures(entitlements.FeatureCartBasic),
	)

	m := construir(t, d)
	if got := kindsDe(m); len(got) != 2 || got[0] != "start:cart" || got[1] != "resume:cart" {
		t.Fatalf("el tipo ocupado se ofrece para pedir Y para retomar; got %v", got)
	}

	texto := m.Render()
	if strings.Count(texto, "\n1. ") != 1 || strings.Count(texto, "\n2. ") != 1 {
		t.Fatalf("las dos opciones deben verse numeradas; texto:\n%s", texto)
	}
	// Se comparan las FRASES, no las líneas: con el número delante nunca serían
	// iguales y el assert no miraría nada (lo destapó la rotura deliberada R9).
	uno := strings.TrimPrefix(lineaDe(t, texto, "1. "), "1. ")
	dos := strings.TrimPrefix(lineaDe(t, texto, "2. "), "2. ")
	if uno == dos {
		t.Fatalf("las dos opciones del mismo tipo deben LEERSE distinto (%q); texto:\n%s", uno, texto)
	}
}

// TestBuild_PedirUnTipoOcupadoLlegaComoStart es la condición que el ejecutor
// necesita: pedir un tipo que ya tiene un evento vivo llega como ActionStart y
// SIN EventID. El despachador no reencamina por su cuenta a lo vivo — quién
// decide entre cerrar el suspendido y conmutar al que está en ventana es el
// ejecutor con E-11, y para poder decidirlo tiene que ver la petición tal como
// la hizo el cliente.
func TestBuild_PedirUnTipoOcupadoLlegaComoStart(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{rescatable("cart", "id-cart", "cart-2026-08-09-1830")}},
		ofertaFake{kinds: []string{"cart"}},
		conFeatures(entitlements.FeatureCartBasic),
	)

	elec, ok := construir(t, d).Resolve("1")
	if !ok {
		t.Fatalf("«1» debe resolver")
	}
	if elec.Action != events.ActionStart || elec.Kind != "cart" || elec.EventID != "" {
		t.Fatalf("pedir un tipo ocupado llega como start sin evento; got %+v", elec)
	}
}

// TestBuild_UnaOfertaRepetidaEsUnaSolaOpcion: el adapter de triggers ya
// deduplica, pero KindOffer es un puerto y otro implementador podría no hacerlo.
// Dos veces el mismo tipo es una línea del menú, no dos idénticas.
func TestBuild_UnaOfertaRepetidaEsUnaSolaOpcion(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{},
		ofertaFake{kinds: []string{"cart", "cart"}},
		conFeatures(entitlements.FeatureCartBasic),
	)

	if got := kindsDe(construir(t, d)); len(got) != 1 || got[0] != "start:cart" {
		t.Fatalf("una oferta repetida no duplica la opción; got %v", got)
	}
}

// TestBuild_FiltraLosTiposSinFeature comprueba el gate del Plan 040: el tenant
// ofrece media pero no la tiene contratada ⇒ no se le ofrece a su cliente.
func TestBuild_FiltraLosTiposSinFeature(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{},
		ofertaFake{kinds: []string{"cart", "media", "survey"}},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey),
	)

	if got := kindsDe(construir(t, d)); len(got) != 2 || got[0] != "start:cart" || got[1] != "start:survey" {
		t.Fatalf("media sin feature no se ofrece; got %v", got)
	}
}

// TestBuild_ElFiltroAlcanzaTambienALosVivos fija la decisión fail-closed: un
// evento VIVO de un tipo que el tenant ya no tiene contratado tampoco se ofrece.
// La fila sigue open en la BD (nada muere por esto) y vuelve al menú el día que
// el tenant recupere la feature.
func TestBuild_ElFiltroAlcanzaTambienALosVivos(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{rescatable("media", "id-media", "media-2026-08-09-1830")}},
		ofertaFake{kinds: []string{"cart"}},
		conFeatures(entitlements.FeatureCartBasic),
	)

	if got := kindsDe(construir(t, d)); len(got) != 1 || got[0] != "start:cart" {
		t.Fatalf("un vivo de un tipo sin feature no se ofrece; got %v", got)
	}
}

// TestBuild_TaxonomiaVaciaListaSinFiltro es la excepción que pide el plan: si el
// Plan 040 no pobló taxonomía para el tenant, el menú lista TODO en vez de
// quedarse vacío — y lo reporta, para que quede anotado en vez de pasar
// inadvertido.
func TestBuild_TaxonomiaVaciaListaSinFiltro(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{},
		ofertaFake{kinds: []string{"cart", "media", "survey"}},
		entitlements.NewFake(), // ni una feature: taxonomía sin sembrar
	)

	m := construir(t, d)
	if len(m.Options) != 3 {
		t.Fatalf("sin taxonomía el menú lista sin filtro; got %v", kindsDe(m))
	}
	if !m.Unfiltered {
		t.Fatalf("el menú sin filtrar debe reportarlo (Unfiltered) para que quede anotado")
	}
}

// TestBuild_ConFeaturesElMenuNoSeMarcaSinFiltro es el contrapunto del anterior:
// un tenant con derechos NO puede venir marcado como «sin filtro», o la anomalía
// dejaría de significar nada.
func TestBuild_ConFeaturesElMenuNoSeMarcaSinFiltro(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{},
		ofertaFake{kinds: []string{"cart"}},
		conFeatures(entitlements.FeatureCartBasic),
	)

	if construir(t, d).Unfiltered {
		t.Fatalf("con features resueltas el menú NO está sin filtrar")
	}
}

// TestBuild_ElMenuNoSeOfreceASiMismo: el despachador es un evento kind='menu'
// (D-043.3). Ofrecer «ver el menú» dentro del menú es un bucle, y da igual que
// venga de la oferta del tenant o de un evento menu vivo.
func TestBuild_ElMenuNoSeOfreceASiMismo(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{rescatable("menu", "id-menu", "menu-2026-08-09-1830")}},
		ofertaFake{kinds: []string{"cart", "menu"}},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureMenu),
	)

	if got := kindsDe(construir(t, d)); len(got) != 1 || got[0] != "start:cart" {
		t.Fatalf("el menú no se lista a sí mismo; got %v", got)
	}
}

// TestBuild_SinNadaQueOfrecerElMenuEstaVacio: sin tipos ofrecidos ni eventos
// vivos no hay menú, y Render devuelve cadena vacía para que nadie mande una
// pregunta sin respuestas.
func TestBuild_SinNadaQueOfrecerElMenuEstaVacio(t *testing.T) {
	d := events.NewDispatcher(&vivosFake{}, ofertaFake{}, conFeatures(entitlements.FeatureCartBasic))

	m := construir(t, d)
	if !m.Empty() || m.Render() != "" {
		t.Fatalf("un menú sin opciones no se envía; got %q", m.Render())
	}
}

// TestBuild_ElResolverCaidoNoAbreElMenu: un fallo de infraestructura del
// Resolver corta (fail-closed). No se lista «por si acaso»: el llamante no debe
// poder confundir «no pude averiguarlo» con «lo tiene contratado».
func TestBuild_ElResolverCaidoNoAbreElMenu(t *testing.T) {
	caido := entitlements.NewFake()
	caido.Err = errors.New("BD de derechos caída")
	d := events.NewDispatcher(&vivosFake{}, ofertaFake{kinds: []string{"cart"}}, caido)

	if _, err := d.Build(context.Background(), refDeTest()); err == nil {
		t.Fatalf("con el resolver caído Build debe fallar, no listar")
	}
}

// TestBuild_SinResolverNoHayMenu: cablear el despachador sin Resolver es un
// error de montaje, y se comporta como el resolver caído.
func TestBuild_SinResolverNoHayMenu(t *testing.T) {
	d := events.NewDispatcher(&vivosFake{}, ofertaFake{kinds: []string{"cart"}}, nil)

	_, err := d.Build(context.Background(), refDeTest())
	if !errors.Is(err, events.ErrNoResolver) {
		t.Fatalf("sin Resolver quiero ErrNoResolver; got %v", err)
	}
}

// TestBuild_PreguntaPorLaConversacionExacta afirma el aislamiento en vez de
// suponerlo: el despachador consulta los vivos de la terna que recibió, no de
// una parcial.
func TestBuild_PreguntaPorLaConversacionExacta(t *testing.T) {
	vistos := &vivosFake{}
	d := events.NewDispatcher(vistos, ofertaFake{}, conFeatures(entitlements.FeatureCartBasic))

	construir(t, d)
	if vistos.pedido != refDeTest() {
		t.Fatalf("los vivos deben pedirse por (tenant, sesión, contacto); got %+v", vistos.pedido)
	}
}

// TestBuild_ElOrdenPonePrimeroLoQueSePuedePedir sigue al ejemplo del ADR (lo
// rescatable va al final de la lista). Su efecto práctico: los números de lo que
// se puede pedir no se mueven porque el cliente tenga algo abierto ese día.
func TestBuild_ElOrdenPonePrimeroLoQueSePuedePedir(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{rescatable("survey", "id-survey", "survey-2026-08-09-1830")}},
		ofertaFake{kinds: []string{"cart", "survey"}},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey),
	)

	got := kindsDe(construir(t, d))
	if len(got) != 3 || got[0] != "start:cart" || got[1] != "start:survey" || got[2] != "resume:survey" {
		t.Fatalf("primero lo que se puede pedir, al final lo rescatable; got %v", got)
	}
}

// TestBuild_UnVivoDeUnTipoQueYaNoSeOfreceSigueSiendoRescatable: el tenant retiró
// la palabra con que se pedía la encuesta, pero la que el cliente dejó a medias
// no se evapora por un cambio de configuración. Ojo: esto es distinto de perder
// la FEATURE, que sí lo retira del menú (ver el test del filtro).
func TestBuild_UnVivoDeUnTipoQueYaNoSeOfreceSigueSiendoRescatable(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{rescatable("survey", "id-survey", "survey-2026-08-09-1830")}},
		ofertaFake{kinds: []string{"cart"}},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey),
	)

	got := kindsDe(construir(t, d))
	if len(got) != 2 || got[0] != "start:cart" || got[1] != "resume:survey" {
		t.Fatalf("lo dejado a medias se sigue pudiendo retomar; got %v", got)
	}
}

// ── T3.6 · El automensaje de RESCATE ─────────────────────────────────────────

// rescatar arma el automensaje de rescate o falla el test.
func rescatar(t *testing.T, d *events.Dispatcher) events.Offering {
	t.Helper()
	o, err := d.BuildRescue(context.Background(), refDeTest())
	if err != nil {
		t.Fatalf("BuildRescue: %v", err)
	}
	return o
}

// abrir arma la entrada de conversación sin evento o falla el test.
func abrir(t *testing.T, d *events.Dispatcher) events.Offering {
	t.Helper()
	o, err := d.BuildOpening(context.Background(), refDeTest())
	if err != nil {
		t.Fatalf("BuildOpening: %v", err)
	}
	return o
}

// TestBuildRescue_CriterioT36CarritoYEncuesta es el criterio LITERAL de T3.6: con
// un cart y una survey vivos y la inactividad vencida, el automensaje trae LAS DOS
// opciones numeradas, la del carrito primero (es la más reciente: el store las
// devuelve por last_activity_at DESC), y en el string no aparece ningún
// identificador (E-3).
func TestBuildRescue_CriterioT36CarritoYEncuesta(t *testing.T) {
	elCart := rescatable("cart", "9f1d1b7e-0000-4000-8000-0000000000aa", "cart-2026-08-09-1830")
	laEncuesta := rescatable("survey", "9f1d1b7e-0000-4000-8000-0000000000bb", "survey-2026-08-08-0900")
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{elCart, laEncuesta}},
		ofertaFake{kinds: []string{"cart", "survey"}},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey),
	)

	o := rescatar(t, d)
	if o.Empty() {
		t.Fatalf("con dos rescatables el automensaje SÍ se emite")
	}
	if got := kindsDe(o.Menu); len(got) != 2 || got[0] != "resume:cart" || got[1] != "resume:survey" {
		t.Fatalf("el rescate lista lo último tocado primero; got %v", got)
	}
	if !strings.Contains(o.Text, "1. ") || !strings.Contains(o.Text, "2. ") {
		t.Fatalf("las dos opciones van numeradas; texto:\n%s", o.Text)
	}
	if !strings.Contains(o.Text, "pedido") || !strings.Contains(o.Text, "encuesta") {
		t.Fatalf("cada opción se nombra por tipo; texto:\n%s", o.Text)
	}
	// El assert que de verdad protege E-3: ni el UUID, ni el history_id, ni el
	// nombre técnico del tipo pueden asomar en lo que lee el cliente. Y «carrito»
	// tampoco: de cara al cliente la palabra es «pedido» (decisión de producto).
	for _, prohibido := range []string{
		elCart.ID, elCart.HistoryID, laEncuesta.ID, laEncuesta.HistoryID,
		"cart", "survey", "carrito", "evento",
	} {
		if strings.Contains(o.Text, prohibido) {
			t.Fatalf("el automensaje NO puede contener %q; texto:\n%s", prohibido, o.Text)
		}
	}
	// Elegir «1» tiene que devolver el carrito con su id: es lo que el runtime
	// necesita para reactivarlo (y lo que hace que «la del carrito primero» no sea
	// solo una frase del render).
	elec, ok := o.Menu.Resolve("1")
	if !ok || elec.Action != events.ActionResume || elec.EventID != elCart.ID || elec.Kind != "cart" {
		t.Fatalf("«1» debe rescatar el carrito (%s); got %+v (ok=%v)", elCart.ID, elec, ok)
	}
}

// TestBuildRescue_SinRescatablesNoSeEmite es la otra mitad de T3.6: sin nada que
// retomar NO hay automensaje. El caso vacío es distinguible por Empty() y además
// el texto es vacío, para que un llamante distraído no mande una cabecera huérfana.
func TestBuildRescue_SinRescatablesNoSeEmite(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{},
		ofertaFake{kinds: []string{"cart"}},
		conFeatures(entitlements.FeatureCartBasic),
	)

	o := rescatar(t, d)
	if !o.Empty() || o.Text != "" {
		t.Fatalf("sin rescatables no se emite nada; got Empty=%v texto=%q", o.Empty(), o.Text)
	}
}

// TestBuildRescue_ElFiltroDeFeaturesAlcanzaAlRescate: un tipo que el tenant ya no
// tiene contratado no se ofrece para retomar. Es el mismo gate del menú (T2.3), y
// aquí importa igual: ofrecer retomar algo que la plataforma ya no le sirve sería
// prometer lo que no se puede cumplir.
func TestBuildRescue_ElFiltroDeFeaturesAlcanzaAlRescate(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{
			rescatable("cart", "id-cart", "cart-2026-08-09-1830"),
			rescatable("survey", "id-survey", "survey-2026-08-08-0900"),
		}},
		ofertaFake{},
		conFeatures(entitlements.FeatureCartBasic), // sin FeatureSurvey
	)

	if got := kindsDe(rescatar(t, d).Menu); len(got) != 1 || got[0] != "resume:cart" {
		t.Fatalf("sin la feature, la encuesta no se ofrece para retomar; got %v", got)
	}
}

// TestBuildRescue_SeisRescatablesEnsenanCincoYAvisanDelResto es el criterio (e) de
// T3.8 sobre la lista: el tope es 5 y el sexto no se pierde en silencio.
//
// Seis tipos distintos y no seis carritos: el índice único parcial (E-2) solo deja
// UN vivo por tipo y conversación, así que seis rescatables son seis capacidades.
func TestBuildRescue_SeisRescatablesEnsenanCincoYAvisanDelResto(t *testing.T) {
	seis := make([]events.Rescuable, 0, 6)
	for _, k := range []string{"cart", "survey", "media", "taller", "cita", "reserva"} {
		seis = append(seis, rescatable(k, "id-"+k, k+"-2026-08-09-1830"))
	}
	d := events.NewDispatcher(
		&vivosFake{rescatables: seis},
		ofertaFake{},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey, entitlements.FeatureMedia),
	)

	o := rescatar(t, d)
	if n := len(o.Menu.Options); n != 5 {
		t.Fatalf("el tope de la lista es 5; se enseñaron %d", n)
	}
	if !strings.Contains(o.Text, "…y 1 más") {
		t.Fatalf("con más de los que caben hay que avisar del resto; texto:\n%s", o.Text)
	}
	// El sexto no se enseña, pero tampoco se puede resolver: un número que no está
	// en la lista no es una elección.
	if _, ok := o.Menu.Resolve("6"); ok {
		t.Fatalf("«6» no es una opción de esta lista")
	}
}

// TestBuildRescue_ElLoteQueSePideEsUnoMasQueElTope: el «…y 1 más» sale de pedir un
// elemento de más, no de contar la tabla entera. Si alguien cambia el lote al tope
// exacto, la lista dejaría de poder saber que había más y este test lo dice.
func TestBuildRescue_ElLoteQueSePideEsUnoMasQueElTope(t *testing.T) {
	vistos := &vivosFake{}
	d := events.NewDispatcher(vistos, ofertaFake{}, conFeatures(entitlements.FeatureCartBasic))

	rescatar(t, d)
	if vistos.limitePedido != 6 {
		t.Fatalf("el lote pedido a la BD debe ser tope+1 = 6; got %d", vistos.limitePedido)
	}
}

// ── T3.8 · La conversación sin evento se abre OFRECIENDO ─────────────────────

// TestBuildOpening_TresTiposMasLaEntradaDeHuerfanos es el criterio (a) de T3.8: la
// entrada lista los tipos habilitados y añade UNA sola entrada final con la cuenta
// de lo que se dejó a medias.
func TestBuildOpening_TresTiposMasLaEntradaDeHuerfanos(t *testing.T) {
	elCart := rescatable("cart", "9f1d1b7e-0000-4000-8000-0000000000aa", "cart-2026-08-09-1830")
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{elCart}},
		ofertaFake{kinds: []string{"cart", "media", "survey"}},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey, entitlements.FeatureMedia),
	)

	o := abrir(t, d)
	if n := len(o.Menu.Options); n != 4 {
		t.Fatalf("3 tipos + 1 entrada de huérfanos = 4 opciones; got %d (%v)", n, kindsDe(o.Menu))
	}
	ultima := o.Menu.Options[3]
	if ultima.Action != events.ActionRescue || ultima.Count != 1 {
		t.Fatalf("la 4.ª opción es la de huérfanos con la cuenta; got %+v", ultima)
	}
	if !strings.Contains(o.Text, "4. Retomar algo que dejaste a medias (1)") {
		t.Fatalf("la entrada final debe leerse con su cuenta; texto:\n%s", o.Text)
	}
	// La entrada NO enumera lo pendiente: eso es la lista de rescate, y llega al
	// elegirla. Si el tipo del rescatable asomara aquí, el mismo pedido saldría dos
	// veces con dos sentidos antes de que el cliente diga nada.
	if strings.Count(o.Text, "pedido") != 1 {
		t.Fatalf("«pedido» sale una vez (la opción de empezar), no dos; texto:\n%s", o.Text)
	}
	elec, ok := o.Menu.Resolve("4")
	if !ok || elec.Action != events.ActionRescue {
		t.Fatalf("«4» pide la lista de lo dejado a medias; got %+v (ok=%v)", elec, ok)
	}
	if elec.EventID != "" {
		t.Fatalf("la entrada de huérfanos NO decide qué evento se retoma; got %q", elec.EventID)
	}
}

// TestBuildOpening_SinRescatablesNoHayEntradaFinal es el criterio (d): contacto
// limpio ⇒ ni entrada de huérfanos ni mención de nada.
func TestBuildOpening_SinRescatablesNoHayEntradaFinal(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{},
		ofertaFake{kinds: []string{"cart", "survey"}},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey),
	)

	o := abrir(t, d)
	if n := len(o.Menu.Options); n != 2 {
		t.Fatalf("sin rescatables la entrada son solo los tipos; got %d (%v)", n, kindsDe(o.Menu))
	}
	if strings.Contains(o.Text, "dejaste a medias") {
		t.Fatalf("sin rescatables no se menciona nada pendiente; texto:\n%s", o.Text)
	}
}

// TestBuildOpening_CasoVacioEsDistinguible es el contrato con el runtime (T3.8 ·
// punto 4, INV-20): sin tipos habilitados y sin rescatables NO hay lista, y quien
// llama lo distingue con Empty() para conservar el fallback del tenant. Sin este
// caso vacío, el fallback quedaría inalcanzable y la regresión sería invisible.
func TestBuildOpening_CasoVacioEsDistinguible(t *testing.T) {
	d := events.NewDispatcher(&vivosFake{}, ofertaFake{}, conFeatures(entitlements.FeatureCartBasic))

	o := abrir(t, d)
	if !o.Empty() || o.Text != "" {
		t.Fatalf("sin tipos ni rescatables la entrada es vacía y distinguible; got Empty=%v texto=%q",
			o.Empty(), o.Text)
	}
}

// TestBuildOpening_SinTiposPeroConRescatablesSiOfrece: el caso vacío es «ni una
// sola opción», no «ningún tipo». Un tenant que retiró todas sus palabras pero
// cuyo cliente dejó un pedido a medias SÍ recibe la entrada.
func TestBuildOpening_SinTiposPeroConRescatablesSiOfrece(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{rescatable("cart", "id-cart", "cart-2026-08-09-1830")}},
		ofertaFake{},
		conFeatures(entitlements.FeatureCartBasic),
	)

	o := abrir(t, d)
	if o.Empty() || len(o.Menu.Options) != 1 || o.Menu.Options[0].Action != events.ActionRescue {
		t.Fatalf("con algo que retomar la entrada no está vacía; got %+v", o.Menu.Options)
	}
}

// TestBuildTagline_NombraPorTipoYNuncaPorIdentificador es el punto 2 de T3.8: la
// coletilla del camino CON clasificador.
func TestBuildTagline_NombraPorTipoYNuncaPorIdentificador(t *testing.T) {
	casos := []struct {
		nombre string
		resc   []events.Rescuable
		quiero string
	}{
		{"sin nada que retomar no hay coletilla", nil, ""},
		{
			"uno",
			[]events.Rescuable{rescatable("cart", "9f1d-aa", "cart-2026-08-09-1830")},
			"Por cierto, tu pedido sigue a medias — dime si quieres retomarlo.",
		},
		{
			"dos",
			[]events.Rescuable{
				rescatable("cart", "9f1d-aa", "cart-2026-08-09-1830"),
				rescatable("survey", "9f1d-bb", "survey-2026-08-08-0900"),
			},
			"Por cierto, tu pedido y tu encuesta siguen a medias — dime si quieres retomarlos.",
		},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			d := events.NewDispatcher(
				&vivosFake{rescatables: c.resc},
				ofertaFake{},
				conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey),
			)
			got, err := d.BuildTagline(context.Background(), refDeTest())
			if err != nil {
				t.Fatalf("BuildTagline: %v", err)
			}
			if got != c.quiero {
				t.Fatalf("coletilla = %q; quiero %q", got, c.quiero)
			}
			for _, r := range c.resc {
				if strings.Contains(got, r.ID) || strings.Contains(got, r.HistoryID) {
					t.Fatalf("la coletilla nombra por tipo, jamás por identificador; got %q", got)
				}
			}
		})
	}
}

// TestRescate_ElResolverCaidoNoAbreCapacidades: el fail-closed del gate alcanza a
// los tres constructores nuevos. Un error de infraestructura que listara lo que el
// tenant no tiene contratado sería peor que un mensaje que no sale.
func TestRescate_ElResolverCaidoNoAbreCapacidades(t *testing.T) {
	roto := entitlements.NewFake()
	roto.Err = errors.New("resolver caído")
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{rescatable("cart", "id-cart", "cart-2026-08-09-1830")}},
		ofertaFake{kinds: []string{"cart"}},
		roto,
	)
	ctx := context.Background()

	if _, err := d.BuildRescue(ctx, refDeTest()); err == nil {
		t.Fatalf("BuildRescue con el resolver caído debe fallar, no listar")
	}
	if _, err := d.BuildOpening(ctx, refDeTest()); err == nil {
		t.Fatalf("BuildOpening con el resolver caído debe fallar, no listar")
	}
	if _, err := d.BuildTagline(ctx, refDeTest()); err == nil {
		t.Fatalf("BuildTagline con el resolver caído debe fallar, no mencionar")
	}
}

// TestRescate_ElErrorDeLaConsultaSePropaga: si la lectura falla, no se inventa una
// lista vacía —que el llamante leería como «no tiene nada» y usaría para caer al
// fallback—, se propaga.
func TestRescate_ElErrorDeLaConsultaSePropaga(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{errRescate: errors.New("la BD dijo que no")},
		ofertaFake{kinds: []string{"cart"}},
		conFeatures(entitlements.FeatureCartBasic),
	)

	if _, err := d.BuildRescue(context.Background(), refDeTest()); err == nil {
		t.Fatalf("el error de la consulta de rescatables debe propagarse")
	}
}

// TestRescate_SinClasificadorNiDependencia es REQ-21 en su forma más fuerte que se
// puede escribir: en vez de un fake que hace t.Fatal si lo llaman —que solo prueba
// el camino que el test recorre—, se comprueba que el paquete NO IMPORTA nada del
// clasificador. Así ninguna rama futura puede llamarlo sin que esto se ponga rojo.
func TestRescate_SinClasificadorNiDependencia(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("leer los imports del paquete: %v", err)
	}
	for _, imp := range pkg.Imports {
		for _, prohibido := range []string{"intent", "llm", "ollama", "classif"} {
			if strings.Contains(strings.ToLower(imp), prohibido) {
				t.Fatalf("el paquete events importa %q: el despachador decide SIN comprender (REQ-21)", imp)
			}
		}
	}
	t.Logf("imports del paquete (sin tests): %v", pkg.Imports)
}

// TestBuildRescue_ElTextoLiteralDelAutomensaje fija la cadena ENTERA, como el test
// del menú del despachador. Es el mensaje que un cliente lee en su WhatsApp: si
// cambia, que cambie a la vista y no de refilón.
func TestBuildRescue_ElTextoLiteralDelAutomensaje(t *testing.T) {
	d := events.NewDispatcher(
		&vivosFake{rescatables: []events.Rescuable{
			rescatable("cart", "id-cart", "cart-2026-08-09-1830"),
			rescatable("survey", "id-survey", "survey-2026-08-08-0900"),
		}},
		ofertaFake{},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey),
	)

	quiero := "Pasó un rato sin novedades, así que ahora mismo no tenemos nada en curso. " +
		"Si quieres, puedes retomar lo que dejaste a medias — responde con el número:\n" +
		"\n" +
		"1. Retomar el pedido que dejaste a medias\n" +
		"2. Continuar la encuesta que dejaste a medias\n" +
		"\n" +
		"Si prefieres otra cosa, escríbelo y te ayudamos."
	if got := rescatar(t, d).Text; got != quiero {
		t.Fatalf("automensaje de rescate:\n%q\nquiero:\n%q", got, quiero)
	}
}

// ── INV-17 · «no se menciona»: el menú tampoco ofrece lo descartado ──────────

// TestBuild_ElMenuNoOfreceLoQueYaNoEsRescatable es el test del CABLEADO: el
// despachador compone la mitad de «retomar» desde la consulta FILTRADA, no desde
// los vivos a secas.
//
// El fake siembra las dos listas en desacuerdo a propósito —un carrito vivo que ya
// no es rescatable, que es exactamente lo que deja el descarte del dueño mientras
// el evento sigue open— y el menú no puede mencionarlo. Si alguien devuelve Build a
// ListAlive, esto se pone rojo aquí y no tres semanas después en la conversación de
// un cliente al que le ofrecimos retomar un pedido que ya no existe.
func TestBuild_ElMenuNoOfreceLoQueYaNoEsRescatable(t *testing.T) {
	elDescartado := vivo("cart", "id-cart-descartado", "cart-2026-08-09-1830")
	d := events.NewDispatcher(
		&vivosFake{eventos: []events.Event{elDescartado}}, // vivo, pero NO rescatable
		ofertaFake{kinds: []string{"cart"}},
		conFeatures(entitlements.FeatureCartBasic),
	)

	m := construir(t, d)
	if got := kindsDe(m); len(got) != 1 || got[0] != "start:cart" {
		t.Fatalf("un pedido descartado no se menciona en el menú (INV-17); got %v", got)
	}
	if texto := m.Render(); strings.Contains(texto, "dejaste a medias") {
		t.Fatalf("el menú no puede ofrecer retomar lo descartado; texto:\n%s", texto)
	}
}

// TestBuild_ElOrdenDeRetomarEsElDeNacimiento fija la propiedad que el cambio de
// fuente podría haberse llevado por delante: el menú enseña lo rescatable en el
// orden en que APARECIÓ, no en el de última actividad (que es el que trae la
// consulta). Sin esto, los números que el cliente tiene delante bailarían entre dos
// mensajes solo porque escribió en uno de sus eventos.
func TestBuild_ElOrdenDeRetomarEsElDeNacimiento(t *testing.T) {
	viejo := rescatable("cart", "id-cart", "cart-2026-08-09-1000")
	viejo.CreatedAt = time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	nuevo := rescatable("survey", "id-survey", "survey-2026-08-09-1800")
	nuevo.CreatedAt = time.Date(2026, 8, 9, 18, 0, 0, 0, time.UTC)

	d := events.NewDispatcher(
		// La consulta los devuelve por última actividad: el más nuevo primero.
		&vivosFake{rescatables: []events.Rescuable{nuevo, viejo}},
		ofertaFake{},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey),
	)

	got := kindsDe(construir(t, d))
	if len(got) != 2 || got[0] != "resume:cart" || got[1] != "resume:survey" {
		t.Fatalf("el menú ordena por nacimiento (primero el carrito, que nació antes); got %v", got)
	}
}
