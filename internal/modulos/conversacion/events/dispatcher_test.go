//go:build pendiente

package events

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
)

// rescuableFake es un RescuableLister que devuelve lo que se le dé y apunta lo que le piden.
type rescuableFake struct {
	rescuable []Rescuable
	err       error
	asked     []string
}

func (f *rescuableFake) ListRescuable(_ context.Context, tenantID, sessionID, contactID string, limit int) ([]Rescuable, error) {
	f.asked = append(f.asked, tenantID+"|"+sessionID+"|"+contactID+"|"+strconv.Itoa(limit))
	if f.err != nil {
		return nil, f.err
	}
	// Como la base: el tope acota el lote.
	if limit > 0 && len(f.rescuable) > limit {
		return f.rescuable[:limit], nil
	}
	return f.rescuable, nil
}

// offerFake es un KindOffer que devuelve lo que se le dé.
type offerFake struct {
	kinds []string
	err   error
	asked []string
}

func (f *offerFake) OfferedKinds(_ context.Context, tenantID, sessionID string) ([]string, error) {
	f.asked = append(f.asked, tenantID+"|"+sessionID)
	return f.kinds, f.err
}

// Aserciones de compilación: quién satisface los puertos del despachador.
var (
	_ RescuableLister = (*Store)(nil)
	_ RescuableLister = (*rescuableFake)(nil)
	_ KindOffer       = (*TriggerKindOffer)(nil)
	_ KindOffer       = (*offerFake)(nil)
	_                 = (*Dispatcher).Build
)

var testRef = ConversationRef{TenantID: "t-1", SessionID: "s-1", ContactID: "c-1"}

// withFeatures es un resolver en el que el tenant de testRef tiene esas features.
func withFeatures(features ...string) *entitlementshelpertest.Fake {
	f := entitlementshelpertest.NewFake()
	for _, feature := range features {
		f.Enable(testRef.TenantID, feature)
	}
	return f
}

// allKindFeatures son las cuatro features que habilitan un tipo de evento.
func allKindFeatures() *entitlementshelpertest.Fake {
	return withFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey, entitlements.FeatureMedia, entitlements.FeatureMenu)
}

// alive es un rescatable de ese tipo, con un id y un history_id reconocibles y nacido `born`
// minutos después de una hora fija.
func alive(kind string, born int) Rescuable {
	at := time.Date(2026, 8, 9, 18, 0, 0, 0, time.UTC).Add(time.Duration(born) * time.Minute)
	return Rescuable{Event: Event{
		ID: "id-" + kind + "-" + strconv.Itoa(born), TenantID: testRef.TenantID, SessionID: testRef.SessionID,
		ContactID: testRef.ContactID, Kind: kind, HistoryID: HistoryID(kind, at), Status: StatusOpen, CreatedAt: at,
	}}
}

// shape resume un menú como "acción:tipo" por opción, en orden, y comprueba que la numeración es
// densa y arranca en 1.
func shape(t *testing.T, m Menu) []string {
	t.Helper()
	out := make([]string, 0, len(m.Options))
	for i, o := range m.Options {
		if o.Number != i+1 {
			t.Errorf("la opción %d lleva el número %d: la numeración no es densa desde 1", i, o.Number)
		}
		out = append(out, string(o.Action)+":"+o.Kind)
	}
	return out
}

func requireShape(t *testing.T, what string, m Menu, want ...string) {
	t.Helper()
	got := shape(t, m)
	if len(got) != len(want) {
		t.Fatalf("%s: opciones = %v, quería %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: opciones = %v, quería %v", what, got, want)
		}
	}
}

// TestBuild_OfferedKindsThenResumables: primero lo que se puede pedir, en el orden de la oferta, y
// después lo rescatable, una opción por evento con su id. Un tipo con evento vivo aparece DOS
// veces, y pedirlo llega como start (sin evento).
func TestBuild_OfferedKindsThenResumables(t *testing.T) {
	cart := alive("cart", 0)
	events := &rescuableFake{rescuable: []Rescuable{cart}}
	offer := &offerFake{kinds: []string{"cart", "survey"}}
	d := NewDispatcher(events, offer, allKindFeatures())

	m, err := d.Build(t.Context(), testRef)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	requireShape(t, "Build", m, "start:cart", "start:survey", "resume:cart")
	if m.Unfiltered {
		t.Error("con features el menú no va sin filtrar")
	}
	if m.Options[0].EventID != "" || m.Options[2].EventID != cart.ID {
		t.Errorf("eventos de las opciones = (%q, %q), quería (\"\", %q)", m.Options[0].EventID, m.Options[2].EventID, cart.ID)
	}
	if choice, ok := m.Resolve("3"); !ok || choice != (Choice{Action: ActionResume, Kind: "cart", EventID: cart.ID}) {
		t.Errorf("Resolve(\"3\") = (%+v, %v), quería retomar el pedido", choice, ok)
	}
	if len(events.asked) != 1 || events.asked[0] != "t-1|s-1|c-1|6" {
		t.Errorf("rescatables pedidos = %v, quería una lectura de la conversación exacta con un lote de 6", events.asked)
	}
	if len(offer.asked) != 1 || offer.asked[0] != "t-1|s-1" {
		t.Errorf("oferta pedida = %v, quería una lectura del tenant y la sesión", offer.asked)
	}
}

// TestBuild_Composition: la oferta repetida es una sola opción, el menú no se ofrece a sí mismo
// (ni para pedir ni para retomar), un vivo de un tipo que ya no se ofrece sigue siendo rescatable,
// lo rescatable va por NACIMIENTO aunque llegue por última actividad, y sin nada el menú es vacío.
func TestBuild_Composition(t *testing.T) {
	cases := []struct {
		name      string
		rescuable []Rescuable
		offered   []string
		want      []string
	}{
		{"offer repeated is one option", nil, []string{"cart", "survey", "cart"}, []string{"start:cart", "start:survey"}},
		{"menu is never offered", []Rescuable{alive("menu", 0), alive("cart", 1)}, []string{"menu", "cart"}, []string{"start:cart", "resume:cart"}},
		{"alive of a kind no longer offered", []Rescuable{alive("media", 0)}, []string{"cart"}, []string{"start:cart", "resume:media"}},
		{"resumables by birth", []Rescuable{alive("survey", 5), alive("cart", 1), alive("media", 3)}, nil, []string{"resume:cart", "resume:media", "resume:survey"}},
		{"same instant ties by id", []Rescuable{alive("survey", 2), alive("cart", 2)}, nil, []string{"resume:cart", "resume:survey"}},
		{"nothing at all", nil, nil, nil},
		{"only the menu", []Rescuable{alive("menu", 0)}, []string{"menu"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := NewDispatcher(&rescuableFake{rescuable: c.rescuable}, &offerFake{kinds: c.offered}, allKindFeatures())
			m, err := d.Build(t.Context(), testRef)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			requireShape(t, "Build", m, c.want...)
			if len(c.want) == 0 && (!m.Empty() || m.Options != nil) {
				t.Errorf("sin nada que ofrecer, Options = %#v; quería nil", m.Options)
			}
		})
	}
}

// TestBuild_FeatureGate: con features, un tipo pasa si el tenant tiene la SUYA —para pedirlo y
// para retomarlo— y un tipo sin feature asociada pasa siempre; sin NINGUNA feature efectiva se
// lista sin filtro y se avisa con Unfiltered.
func TestBuild_FeatureGate(t *testing.T) {
	rescuable := []Rescuable{alive("cart", 0), alive("media", 1), alive("reserva", 2)}
	offered := []string{"cart", "media", "reserva", "survey"}
	cases := []struct {
		name       string
		feats      *entitlementshelpertest.Fake
		want       []string
		unfiltered bool
	}{
		{"cart only", withFeatures(entitlements.FeatureCartBasic),
			[]string{"start:cart", "start:reserva", "resume:cart", "resume:reserva"}, false},
		{"media and survey", withFeatures(entitlements.FeatureMedia, entitlements.FeatureSurvey),
			[]string{"start:media", "start:reserva", "start:survey", "resume:media", "resume:reserva"}, false},
		{"a feature that enables no kind still filters", withFeatures(entitlements.FeatureLLMIntake),
			[]string{"start:reserva", "resume:reserva"}, false},
		{"no effective feature lists unfiltered", withFeatures(),
			[]string{"start:cart", "start:media", "start:reserva", "start:survey", "resume:cart", "resume:media", "resume:reserva"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := NewDispatcher(&rescuableFake{rescuable: rescuable}, &offerFake{kinds: offered}, c.feats)
			m, err := d.Build(t.Context(), testRef)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			requireShape(t, "Build", m, c.want...)
			if m.Unfiltered != c.unfiltered {
				t.Errorf("Unfiltered = %v, quería %v", m.Unfiltered, c.unfiltered)
			}
		})
	}
}

// TestBuilders_FailClosed: sin resolver, ErrNoResolver; con el resolver caído, con la oferta caída
// o con la consulta caída, el error se propaga envuelto y NO sale nada que ofrecer. Vale para los
// cuatro constructores.
func TestBuilders_FailClosed(t *testing.T) {
	if want := "events: el despachador necesita un Resolver de entitlements (fail-closed)"; ErrNoResolver.Error() != want {
		t.Errorf("ErrNoResolver = %q, quería %q", ErrNoResolver.Error(), want)
	}
	boom := errors.New("boom")
	down := allKindFeatures()
	down.Err = boom
	rescuable := []Rescuable{alive("cart", 0)}
	cases := []struct {
		name   string
		d      *Dispatcher
		prefix string
		cause  error
		// offerUsed: el constructor consulta la oferta (BuildRescue y BuildTagline no).
		onlyWithOffer bool
	}{
		{"no resolver", NewDispatcher(&rescuableFake{rescuable: rescuable}, &offerFake{kinds: []string{"cart"}}, nil),
			ErrNoResolver.Error(), ErrNoResolver, false},
		{"resolver down", NewDispatcher(&rescuableFake{rescuable: rescuable}, &offerFake{kinds: []string{"cart"}}, down),
			"events: resolver los derechos del tenant para el menú: ", boom, false},
		{"query down", NewDispatcher(&rescuableFake{err: boom}, &offerFake{kinds: []string{"cart"}}, allKindFeatures()),
			"events: listar los eventos rescatables del contacto: ", boom, false},
		{"offer down", NewDispatcher(&rescuableFake{rescuable: rescuable}, &offerFake{err: boom}, allKindFeatures()),
			"events: listar los tipos que ofrece el tenant: ", boom, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			check := func(what string, err error, empty bool) {
				t.Helper()
				if err == nil || !errors.Is(err, c.cause) || !strings.HasPrefix(err.Error(), c.prefix) {
					t.Errorf("%s: err = %v, quería %q", what, err, c.prefix)
				}
				if !empty {
					t.Errorf("%s: con el error salió algo que ofrecer", what)
				}
			}
			m, err := c.d.Build(t.Context(), testRef)
			check("Build", err, m.Empty() && !m.Unfiltered)
			o, err := c.d.BuildOpening(t.Context(), testRef)
			check("BuildOpening", err, o.Empty() && o.Text == "")
			if c.onlyWithOffer {
				return
			}
			o, err = c.d.BuildRescue(t.Context(), testRef)
			check("BuildRescue", err, o.Empty() && o.Text == "")
			tag, err := c.d.BuildTagline(t.Context(), testRef)
			check("BuildTagline", err, tag == "")
		})
	}
}

// TestOffering_Empty: una oferta está vacía cuando su menú no tiene opciones.
func TestOffering_Empty(t *testing.T) {
	if !(Offering{}).Empty() {
		t.Error("la oferta cero no dice Empty")
	}
	if (Offering{Text: "x", Menu: Menu{Options: []MenuOption{{Number: 1, Action: ActionStart, Kind: "cart"}}}}).Empty() {
		t.Error("una oferta con una opción dice Empty")
	}
}

// TestBuildRescue_TheLiteralSelfMessage: el automensaje literal, con lo rescatable en el orden en
// que llega (última actividad primero), numerado, nombrado por tipo y nunca por identificador.
func TestBuildRescue_TheLiteralSelfMessage(t *testing.T) {
	cart, survey := alive("cart", 0), alive("survey", 1)
	d := NewDispatcher(&rescuableFake{rescuable: []Rescuable{cart, survey}}, &offerFake{kinds: []string{"media"}},
		withFeatures(entitlements.FeatureCartBasic, entitlements.FeatureSurvey))

	o, err := d.BuildRescue(t.Context(), testRef)
	if err != nil {
		t.Fatalf("BuildRescue: %v", err)
	}
	want := "Pasó un rato sin novedades, así que ahora mismo no tenemos nada en curso. " +
		"Si quieres, puedes retomar lo que dejaste a medias — responde con el número:\n" +
		"\n" +
		"1. Retomar el pedido que dejaste a medias\n" +
		"2. Continuar la encuesta que dejaste a medias\n" +
		"\n" +
		"Si prefieres otra cosa, escríbelo y te ayudamos."
	if o.Text != want {
		t.Errorf("automensaje:\n%q\n--- quería ---\n%q", o.Text, want)
	}
	requireShape(t, "BuildRescue", o.Menu, "resume:cart", "resume:survey")
	if choice, ok := o.Menu.Resolve("1"); !ok || choice != (Choice{Action: ActionResume, Kind: "cart", EventID: cart.ID}) {
		t.Errorf("Resolve(\"1\") = (%+v, %v), quería retomar el pedido", choice, ok)
	}
	for _, banned := range []string{cart.ID, cart.HistoryID, survey.ID, survey.HistoryID, "cart", "survey", "carrito", "evento"} {
		if strings.Contains(o.Text, banned) {
			t.Errorf("el automensaje contiene %q:\n%s", banned, o.Text)
		}
	}
}

// TestBuildRescue_EmptyGateAndCap: sin rescatables la oferta es VACÍA (sin texto); el gate de
// features alcanza al rescate; y con más de cinco se enseñan cinco y se avisa del resto con lo
// que reveló el lote de seis. El aviso va pegado al cierre: solo hay línea en blanco ANTES de él.
func TestBuildRescue_EmptyGateAndCap(t *testing.T) {
	empty, err := NewDispatcher(&rescuableFake{}, &offerFake{kinds: []string{"cart"}}, allKindFeatures()).BuildRescue(t.Context(), testRef)
	if err != nil || !empty.Empty() || empty.Text != "" {
		t.Errorf("sin rescatables: (%+v, %v), quería la oferta vacía y sin texto", empty, err)
	}

	gated, err := NewDispatcher(&rescuableFake{rescuable: []Rescuable{alive("cart", 0), alive("media", 1), alive("menu", 2)}},
		&offerFake{}, withFeatures(entitlements.FeatureCartBasic)).BuildRescue(t.Context(), testRef)
	if err != nil {
		t.Fatalf("BuildRescue: %v", err)
	}
	requireShape(t, "con el gate", gated.Menu, "resume:cart")

	many := make([]Rescuable, 0, 8)
	for i := range 8 {
		many = append(many, alive("tipo"+strconv.Itoa(i), i))
	}
	events := &rescuableFake{rescuable: many}
	capped, err := NewDispatcher(events, &offerFake{}, allKindFeatures()).BuildRescue(t.Context(), testRef)
	if err != nil {
		t.Fatalf("BuildRescue: %v", err)
	}
	if len(capped.Menu.Options) != 5 || capped.Menu.Options[4].EventID != many[4].ID {
		t.Fatalf("se enseñan %d opciones, quería las cinco primeras", len(capped.Menu.Options))
	}
	if !strings.Contains(capped.Text, "\n5. Retomar: tipo4\n\n…y 1 más\nSi prefieres otra cosa, escríbelo y te ayudamos.") {
		t.Errorf("el automensaje no avisa del resto:\n%s", capped.Text)
	}
	if events.asked[0] != "t-1|s-1|c-1|6" {
		t.Errorf("lote pedido = %v, quería 6 (uno más que el tope)", events.asked)
	}

	exact, err := NewDispatcher(&rescuableFake{rescuable: many[:5]}, &offerFake{}, allKindFeatures()).BuildRescue(t.Context(), testRef)
	if err != nil || len(exact.Menu.Options) != 5 || strings.Contains(exact.Text, "más\n") {
		t.Errorf("con cinco justos: %d opciones y el texto\n%s", len(exact.Menu.Options), exact.Text)
	}
}

// TestBuildOpening_KindsPlusOneRescueEntry: los tipos ofrecidos y, solo si hay algo que retomar,
// UNA entrada final con la cuenta, sin tipo ni evento; el texto es el render de ese menú y no
// enumera lo pendiente.
func TestBuildOpening_KindsPlusOneRescueEntry(t *testing.T) {
	d := NewDispatcher(&rescuableFake{rescuable: []Rescuable{alive("cart", 0), alive("survey", 1)}},
		&offerFake{kinds: []string{"cart", "media", "survey"}}, allKindFeatures())
	o, err := d.BuildOpening(t.Context(), testRef)
	if err != nil {
		t.Fatalf("BuildOpening: %v", err)
	}
	requireShape(t, "BuildOpening", o.Menu, "start:cart", "start:media", "start:survey", "rescue:")
	if last := o.Menu.Options[3]; last.Count != 2 || last.EventID != "" {
		t.Errorf("la entrada final = %+v, quería la cuenta 2 y ningún evento", last)
	}
	want := "¿Qué quieres hacer? Responde con el número de la opción:\n\n" +
		"1. Hacer un pedido\n" +
		"2. Ver los documentos\n" +
		"3. Responder una encuesta\n" +
		"4. Retomar algo que dejaste a medias (2)\n\n" +
		"Si prefieres otra cosa, escríbelo y te ayudamos."
	if o.Text != want || o.Text != o.Menu.Render() {
		t.Errorf("entrada:\n%s\n--- quería ---\n%s", o.Text, want)
	}
	if choice, ok := o.Menu.Resolve("4"); !ok || choice != (Choice{Action: ActionRescue}) {
		t.Errorf("Resolve(\"4\") = (%+v, %v), quería pedir la lista de rescate", choice, ok)
	}
}

// TestBuildOpening_Cases: sin rescatables no hay entrada final; sin tipos pero con rescatables sí
// ofrece; sin nada es vacío (el llamante conserva su fallback, INV-20); la cuenta es la del lote
// ya pasado por el gate, como mucho 6.
func TestBuildOpening_Cases(t *testing.T) {
	seven := make([]Rescuable, 0, 7)
	for i := range 7 {
		seven = append(seven, alive("tipo"+strconv.Itoa(i), i))
	}
	cases := []struct {
		name      string
		rescuable []Rescuable
		offered   []string
		feats     *entitlementshelpertest.Fake
		want      []string
		count     int
	}{
		{"clean contact", nil, []string{"cart", "survey"}, allKindFeatures(), []string{"start:cart", "start:survey"}, 0},
		{"no kinds but something to resume", []Rescuable{alive("cart", 0)}, nil, allKindFeatures(), []string{"rescue:"}, 1},
		{"nothing", nil, nil, allKindFeatures(), nil, 0},
		{"gate drops kinds and resumables", []Rescuable{alive("cart", 0), alive("media", 1)}, []string{"cart", "media"},
			withFeatures(entitlements.FeatureMedia), []string{"start:media", "rescue:"}, 1},
		{"count is what the batch revealed", seven, nil, allKindFeatures(), []string{"rescue:"}, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := NewDispatcher(&rescuableFake{rescuable: c.rescuable}, &offerFake{kinds: c.offered}, c.feats).BuildOpening(t.Context(), testRef)
			if err != nil {
				t.Fatalf("BuildOpening: %v", err)
			}
			requireShape(t, "BuildOpening", o.Menu, c.want...)
			if len(c.want) == 0 {
				if !o.Empty() || o.Text != "" {
					t.Errorf("sin nada: %+v, quería la oferta vacía y sin texto", o)
				}
				return
			}
			if last := o.Menu.Options[len(o.Menu.Options)-1]; last.Action == ActionRescue && last.Count != c.count {
				t.Errorf("cuenta de la entrada final = %d, quería %d", last.Count, c.count)
			}
		})
	}
}

// TestBuildTagline_NamesByKindNeverByIdentifier: la coletilla literal: singular, plural con comas
// y «y», y «y algo más» pasado el tope; vacía sin nada que retomar; pasa por el gate.
func TestBuildTagline_NamesByKindNeverByIdentifier(t *testing.T) {
	six := []Rescuable{alive("cart", 0), alive("survey", 1), alive("media", 2), alive("reserva", 3), alive("turno", 4), alive("vale", 5)}
	cases := []struct {
		name      string
		rescuable []Rescuable
		feats     *entitlementshelpertest.Fake
		want      string
	}{
		{"nothing", nil, allKindFeatures(), ""},
		{"one", []Rescuable{alive("cart", 0)}, allKindFeatures(), "Por cierto, tu pedido sigue a medias — dime si quieres retomarlo."},
		{"two", []Rescuable{alive("cart", 0), alive("survey", 1)}, allKindFeatures(),
			"Por cierto, tu pedido y tu encuesta siguen a medias — dime si quieres retomarlos."},
		{"three", []Rescuable{alive("cart", 0), alive("survey", 1), alive("media", 2)}, allKindFeatures(),
			"Por cierto, tu pedido, tu encuesta y tu documentos siguen a medias — dime si quieres retomarlos."},
		{"six names five and something else", six, allKindFeatures(),
			"Por cierto, tu pedido, tu encuesta, tu documentos, tu reserva y tu turno y algo más siguen a medias — dime si quieres retomarlos."},
		{"the gate and the menu drop out", []Rescuable{alive("cart", 0), alive("survey", 1), alive("menu", 2)},
			withFeatures(entitlements.FeatureSurvey), "Por cierto, tu encuesta sigue a medias — dime si quieres retomarlo."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NewDispatcher(&rescuableFake{rescuable: c.rescuable}, &offerFake{}, c.feats).BuildTagline(t.Context(), testRef)
			if err != nil || got != c.want {
				t.Errorf("BuildTagline = (%q, %v)\nquería %q", got, err, c.want)
			}
			for _, r := range c.rescuable {
				if strings.Contains(got, r.ID) || strings.Contains(got, r.HistoryID) {
					t.Errorf("la coletilla nombra un identificador: %q", got)
				}
			}
		})
	}
}
