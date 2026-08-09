package events_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// ── Dobles de laboratorio ─────────────────────────────────────────────────────

// vivosFake es el AliveLister de los tests: devuelve lo sembrado y RECUERDA con
// qué terna se le preguntó, para poder afirmar el aislamiento (INV-8) en vez de
// suponerlo.
type vivosFake struct {
	eventos []events.Event
	err     error
	pedido  events.ConversationRef
}

func (f *vivosFake) ListAlive(_ context.Context, tenantID, sessionID, contactID string) ([]events.Event, error) {
	f.pedido = events.ConversationRef{TenantID: tenantID, SessionID: sessionID, ContactID: contactID}
	return f.eventos, f.err
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
		&vivosFake{eventos: []events.Event{elCart}},
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
		&vivosFake{eventos: []events.Event{elCart}},
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
		&vivosFake{eventos: []events.Event{vivo("cart", "id-cart", "cart-2026-08-09-1830")}},
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
		&vivosFake{eventos: []events.Event{vivo("cart", "id-cart", "cart-2026-08-09-1830")}},
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
		&vivosFake{eventos: []events.Event{vivo("media", "id-media", "media-2026-08-09-1830")}},
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
		&vivosFake{eventos: []events.Event{vivo("menu", "id-menu", "menu-2026-08-09-1830")}},
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
		&vivosFake{eventos: []events.Event{vivo("survey", "id-survey", "survey-2026-08-09-1830")}},
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
		&vivosFake{eventos: []events.Event{vivo("survey", "id-survey", "survey-2026-08-09-1830")}},
		ofertaFake{kinds: []string{"cart"}},
		conFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey),
	)

	got := kindsDe(construir(t, d))
	if len(got) != 2 || got[0] != "start:cart" || got[1] != "resume:survey" {
		t.Fatalf("lo dejado a medias se sigue pudiendo retomar; got %v", got)
	}
}
