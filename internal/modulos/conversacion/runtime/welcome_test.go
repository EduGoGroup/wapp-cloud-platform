package runtime_test

// welcome_test.go: la BIENVENIDA ÚNICA (welcome.go, WL-1…WL-16; RT-20). La mecánica no está
// exportada: se prueba por HandleIncoming y por Start, leyendo lo que sale por el Sender y la
// marca que queda en el almacén. El puerto WelcomeStore se ejercita con un espía que envuelve
// al gemelo en memoria.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// welcomeSpy es un runtime.WelcomeStore que envuelve al del guion: apunta cada llamada con su
// instante y su clave, deja inyectar fallos y puede mover el reloj DURANTE el toque (para ver
// si el runtime toma el instante una sola vez por turno).
type welcomeSpy struct {
	inner runtime.WelcomeStore

	mu        sync.Mutex
	touches   []welcomeCall
	marks     []welcomeCall
	touchErr  error
	markErr   error
	markLost  bool
	onTouched func()
}

// welcomeCall es una llamada al puerto: la clave, el instante y, en MarkWelcomed, el testigo.
type welcomeCall struct {
	key     store.Key
	at      time.Time
	witness store.WelcomeMark
}

var _ runtime.WelcomeStore = (*welcomeSpy)(nil)

// TouchContact implementa runtime.WelcomeStore.
func (s *welcomeSpy) TouchContact(ctx context.Context, key store.Key, now time.Time) (store.WelcomeMark, error) {
	s.mu.Lock()
	s.touches = append(s.touches, welcomeCall{key: key, at: now})
	err, after := s.touchErr, s.onTouched
	s.mu.Unlock()
	if err != nil {
		return store.WelcomeMark{}, err
	}
	mark, err := s.inner.TouchContact(ctx, key, now)
	if after != nil {
		after()
	}
	return mark, err
}

// MarkWelcomed implementa runtime.WelcomeStore.
func (s *welcomeSpy) MarkWelcomed(ctx context.Context, key store.Key, witness store.WelcomeMark, now time.Time) (bool, error) {
	s.mu.Lock()
	s.marks = append(s.marks, welcomeCall{key: key, at: now, witness: witness})
	err, lost := s.markErr, s.markLost
	s.mu.Unlock()
	if err != nil {
		return false, err
	}
	if lost {
		return false, nil
	}
	return s.inner.MarkWelcomed(ctx, key, witness, now)
}

func (s *welcomeSpy) set(change func(s *welcomeSpy)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s)
}

func (s *welcomeSpy) calls() (touches, marks []welcomeCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.touches), slices.Clone(s.marks)
}

// welcomeWithSpy cablea el espía sobre el almacén del guion, en lugar de este.
func welcomeWithSpy(spy *welcomeSpy) harnessOption {
	return withOptions(func(h *harness) []runtime.Option {
		spy.inner = h.repo
		return []runtime.Option{runtime.WithWelcomeStore(spy)}
	})
}

// welcomeHarness monta un guion con la bienvenida ENCENDIDA (`llm_intake`) y el espía
// cableado. Sin reglas de disparo: todo entrante cae en el limbo y el motor lo ignora, así
// que lo único que puede salir es la bienvenida.
func welcomeHarness(t *testing.T, opts ...harnessOption) (*harness, *welcomeSpy) {
	t.Helper()
	spy := &welcomeSpy{}
	h := newHarness(t, append([]harnessOption{welcomeWithSpy(spy)}, opts...)...)
	h.enableFeature(entitlements.FeatureLLMIntake)
	return h, spy
}

// welcomeCount cuenta cuántas veces salió ese texto por el Sender.
func welcomeCount(h *harness, text string) int {
	n := 0
	for _, sent := range h.texts() {
		if sent == text {
			n++
		}
	}
	return n
}

// welcomeWantCount exige que la bienvenida de plataforma haya salido n veces.
func welcomeWantCount(t *testing.T, h *harness, n int) {
	t.Helper()
	if got := welcomeCount(h, store.DefaultWelcomeText); got != n {
		t.Fatalf("bienvenidas enviadas = %d, quería %d (textos: %q)\nlog:\n%s", got, n, h.texts(), h.log.dump())
	}
}

// TestWelcome_TwoMessagesInARowOneWelcome: la bienvenida es un acuse de recibo, no un
// autorespondedor. Dos mensajes seguidos del mismo contacto reciben UNA, y la marca queda
// sellada con el instante del turno que la mandó (WL-8, WL-11, WL-12).
func TestWelcome_TwoMessagesInARowOneWelcome(t *testing.T) {
	h, _ := welcomeHarness(t)

	h.say("wa-1", "hola-qzx")
	h.clock.Advance(time.Minute)
	h.say("wa-2", "sigo aquí-qzx")

	incomingWantTexts(t, h, store.DefaultWelcomeText)
	mark := h.repo.Welcome(h.key())
	if !mark.WelcomedAt.Equal(harnessStart) || !mark.LastIncomingAt.Equal(harnessStart.Add(time.Minute)) {
		t.Errorf("marca = %+v, quería saludado en %v y último mensaje en %v", mark, harnessStart, harnessStart.Add(time.Minute))
	}
	lines := incomingWantLog(t, h, "info", "runtime: bienvenida entregada al contacto", 1)
	incomingWantFields(t, lines[0], map[string]any{"tenant_id": harnessTenant, "session_id": harnessSession, "contact_id": h.key().ContactID})
}

// TestWelcome_Gate_NeedsStoreResolverAndFeature: WL-1, WL-2 y RT-20. La mecánica solo está
// activa con las TRES piezas. Si falta cualquiera no se manda nada y NO SE ESCRIBE NADA: el
// gate va antes de TouchContact.
func TestWelcome_Gate_NeedsStoreResolverAndFeature(t *testing.T) {
	cases := []struct {
		name    string
		options func(spy *welcomeSpy) []runtime.Option
		feature bool
		active  bool
	}{
		{name: "all three", feature: true, active: true},
		{name: "without the feature", feature: false},
		{name: "without the store", feature: true, options: func(*welcomeSpy) []runtime.Option {
			return []runtime.Option{runtime.WithWelcomeStore(nil)}
		}},
		{name: "without the resolver", feature: true, options: func(*welcomeSpy) []runtime.Option {
			return []runtime.Option{runtime.WithEntitlements(nil)}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &welcomeSpy{}
			opts := []harnessOption{welcomeWithSpy(spy)}
			if tc.options != nil {
				opts = append(opts, withOptions(func(*harness) []runtime.Option { return tc.options(spy) }))
			}
			h := newHarness(t, opts...)
			if tc.feature {
				h.enableFeature(entitlements.FeatureLLMIntake)
			}

			h.say("wa-1", "hola-qzx")

			want := 0
			if tc.active {
				want = 1
			}
			welcomeWantCount(t, h, want)
			touches, marks := spy.calls()
			if len(touches) != want || len(marks) != want {
				t.Errorf("llamadas al almacén = %d toques y %d sellos, quería %d de cada", len(touches), len(marks), want)
			}
			if mark := h.repo.Welcome(h.key()); !tc.active && (!mark.LastIncomingAt.IsZero() || !mark.WelcomedAt.IsZero()) {
				t.Errorf("quedó escrita la marca %+v con el gate cerrado", mark)
			}
		})
	}
}

// TestWelcome_Gate_ResolverErrorIsFailClosed: WL-2. Si el resolver de features falla no se
// manda bienvenida ni se registra al contacto, y se avisa a WARN. El turno sigue.
func TestWelcome_Gate_ResolverErrorIsFailClosed(t *testing.T) {
	h, spy := welcomeHarness(t)
	h.features.Err = errors.New("entitlements-down-qzx")

	h.say("wa-1", "hola-qzx")

	incomingWantTexts(t, h)
	incomingWantLog(t, h, "warn", "runtime: no se pudo resolver la feature llm_intake; no se manda bienvenida", 1)
	if touches, _ := spy.calls(); len(touches) != 0 {
		t.Errorf("toques con el resolver caído = %d, quería 0", len(touches))
	}
}

// TestWelcome_TouchesTheContactOnEveryAdmittedIncoming: WL-3. Cada entrante que pasa las
// guardas registra al contacto, con la clave de la conversación y el instante del reloj del
// runtime: también los turnos que AVANZAN una conversación viva.
func TestWelcome_TouchesTheContactOnEveryAdmittedIncoming(t *testing.T) {
	h, spy := welcomeHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))

	h.say("wa-1", incomingKeyword)
	h.clock.Advance(3 * time.Minute)
	h.say("wa-2", "1")
	h.clock.Advance(4 * time.Minute)
	h.say("wa-3", "1")

	touches, marks := spy.calls()
	want := []time.Time{harnessStart, harnessStart.Add(3 * time.Minute), harnessStart.Add(7 * time.Minute)}
	if len(touches) != len(want) {
		t.Fatalf("toques = %d, quería uno por entrante (%d)", len(touches), len(want))
	}
	for i, touch := range touches {
		if touch.key != h.key() || !touch.at.Equal(want[i]) {
			t.Errorf("toque %d = (%+v, %v), quería la clave de la conversación en %v", i, touch.key, touch.at, want[i])
		}
	}
	if len(marks) != 1 {
		t.Errorf("sellos = %d, quería 1: solo el primer mensaje saluda", len(marks))
	}
	if got := h.repo.Welcome(h.key()).LastIncomingAt; !got.Equal(want[2]) {
		t.Errorf("último mensaje registrado = %v, quería el del turno que avanzó (%v)", got, want[2])
	}
}

// TestWelcome_TheInstantIsTakenOncePerTurn: WL-4. El instante del toque es el MISMO con el
// que se sella la bienvenida, aunque el reloj se mueva a mitad de turno; y el testigo del
// sello es la marca previa que devolvió el toque.
func TestWelcome_TheInstantIsTakenOncePerTurn(t *testing.T) {
	h, spy := welcomeHarness(t)
	spy.set(func(s *welcomeSpy) { s.onTouched = func() { h.clock.Advance(time.Second) } })

	h.say("wa-1", "hola-qzx")

	touches, marks := spy.calls()
	if len(touches) != 1 || len(marks) != 1 {
		t.Fatalf("llamadas = %d toques y %d sellos, quería 1 y 1", len(touches), len(marks))
	}
	if !touches[0].at.Equal(harnessStart) || !marks[0].at.Equal(touches[0].at) {
		t.Errorf("toque en %v y sello en %v, quería los dos en %v (un solo instante por turno)", touches[0].at, marks[0].at, harnessStart)
	}
	if marks[0].witness != (store.WelcomeMark{}) || marks[0].key != h.key() {
		t.Errorf("sello = %+v, quería la clave de la conversación y la marca previa (cero) como testigo", marks[0])
	}
}

// TestWelcome_TouchErrorSkipsTheWelcomeButNotTheTurn: WL-5 y WL-13. Si no se puede registrar
// la actividad del contacto no hay bienvenida, se avisa a WARN y el turno sigue hacia el
// disparo.
func TestWelcome_TouchErrorSkipsTheWelcomeButNotTheTurn(t *testing.T) {
	h, spy := welcomeHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	spy.set(func(s *welcomeSpy) { s.touchErr = errors.New("welcomes-down-qzx") })

	h.say("wa-1", incomingKeyword)

	incomingWantTexts(t, h, incomingRootPrompt)
	incomingWantLog(t, h, "warn", "runtime: no se pudo registrar la actividad del contacto; no se manda bienvenida", 1)
	if _, marks := spy.calls(); len(marks) != 0 {
		t.Errorf("sellos = %d, quería 0", len(marks))
	}
}

// TestWelcome_GoesBeforeTheTrigger: WL-6. En el limbo la bienvenida sale ANTES de resolver el
// disparo: primero el acuse de recibo, después la pantalla del flujo.
func TestWelcome_GoesBeforeTheTrigger(t *testing.T) {
	h, _ := welcomeHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))

	h.say("wa-1", incomingKeyword)

	incomingWantTexts(t, h, store.DefaultWelcomeText, incomingRootPrompt)
	incomingWantNode(t, h, "root")
}

// TestWelcome_ReturnsWhenTheClockReleasedTheConversation: WL-6. El segundo camino: el reloj
// soltó la conversación en este turno. Si además el contacto llevaba callado el umbral, se le
// vuelve a saludar antes del disparo. Vale para los dos relojes: el del evento y el TTL
// conversacional.
func TestWelcome_ReturnsWhenTheClockReleasedTheConversation(t *testing.T) {
	cases := []struct {
		name    string
		rule    trigger.Rule
		keyword string
	}{
		{name: "event inactivity", rule: incomingEventRule(incomingEventKeyword, trigger.EventKindCart, incomingFlowID), keyword: incomingEventKeyword},
		{name: "conversation ttl", rule: incomingKeywordRule(incomingFlowID), keyword: incomingKeyword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := welcomeHarness(t)
			h.seedFlow(incomingStepFlow())
			h.seedRule(tc.rule)
			h.seedSettings(func(s *store.TenantSettings) {
				s.EventInactivityTTL = 30 * time.Minute
				s.ConversationTTL = 30 * time.Minute
				s.WelcomeSilence = time.Hour
			})
			h.say("wa-1", tc.keyword)
			incomingWantTexts(t, h, store.DefaultWelcomeText, incomingRootPrompt)
			h.clock.Advance(2 * time.Hour)

			h.say("wa-2", "sigo interesado-qzx")

			incomingWantTexts(t, h, store.DefaultWelcomeText, incomingRootPrompt, store.DefaultWelcomeText)
			incomingWantNoState(t, h)
		})
	}
}

// TestWelcome_NeverOutsideLimboAndRestart: WL-7. Con el umbral en 0 —«sale siempre que
// toque»— la bienvenida sigue sin salir en un avance, en la suelta de un estado terminal a
// mitad de conversación ni en un Start por API.
func TestWelcome_NeverOutsideLimboAndRestart(t *testing.T) {
	h, _ := welcomeHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	h.seedSettings(func(s *store.TenantSettings) { s.WelcomeSilence = 0 })
	h.say("wa-1", incomingKeyword)
	welcomeWantCount(t, h, 1)

	h.say("wa-2", "1")               // avance.
	h.say("wa-3", "1")               // avance que termina el flujo.
	h.say("wa-4", incomingKeyword)   // suelta del estado terminal y disparo.
	h.say("wa-5", "texto libre-qzx") // avance con un texto que no es opción.

	welcomeWantCount(t, h, 1)
	incomingWantNode(t, h, "root")

	other := contact.RefsFrom(incomingOtherPhone, "", jid(incomingOtherPhone))
	if _, err := h.rt.Start(t.Context(), harnessTenant, incomingFlowID, harnessSession, other[0]); err != nil {
		t.Fatalf("Start por API = %v", err)
	}
	welcomeWantCount(t, h, 1)
}

// TestWelcome_SilenceIsMeasuredFromTheLastIncoming: WL-8. Vuelve a salir si el mensaje
// ANTERIOR del contacto queda al umbral o más; el ancla es su último mensaje, no la última
// bienvenida, y el borde es inclusivo. El umbral es el del tenant, no una constante.
func TestWelcome_SilenceIsMeasuredFromTheLastIncoming(t *testing.T) {
	const silence = 2 * time.Hour
	h, _ := welcomeHarness(t)
	h.seedSettings(func(s *store.TenantSettings) { s.WelcomeSilence = silence })

	h.say("wa-1", "uno-qzx")
	welcomeWantCount(t, h, 1)

	h.clock.Advance(silence - time.Second)
	h.say("wa-2", "dos-qzx")
	welcomeWantCount(t, h, 1)

	// A 2·N − 2 s de la bienvenida, pero a N − 1 s del último mensaje: NO toca.
	h.clock.Advance(silence - time.Second)
	h.say("wa-3", "tres-qzx")
	welcomeWantCount(t, h, 1)

	// A exactamente N del último mensaje: toca.
	h.clock.Advance(silence)
	h.say("wa-4", "cuatro-qzx")
	welcomeWantCount(t, h, 2)
}

// TestWelcome_ZeroSilenceWelcomesEveryTime: WL-8. Con WelcomeSilence 0 la bienvenida sale en
// cada mensaje que no avance una conversación viva.
func TestWelcome_ZeroSilenceWelcomesEveryTime(t *testing.T) {
	h, _ := welcomeHarness(t)
	h.seedSettings(func(s *store.TenantSettings) { s.WelcomeSilence = 0 })

	h.say("wa-1", "uno-qzx")
	h.say("wa-2", "dos-qzx")
	h.say("wa-3", "tres-qzx")

	welcomeWantCount(t, h, 3)
}

// TestWelcome_TextIsTheTenantsOrThePlatformDefault: WL-9. El texto es fijo: el del tenant o,
// si está vacío, el de plataforma. La cadena vacía no significa «sin bienvenida».
func TestWelcome_TextIsTheTenantsOrThePlatformDefault(t *testing.T) {
	cases := []struct {
		name, configured, want string
	}{
		{name: "tenant text", configured: "Bienvenida del tenant-qzx", want: "Bienvenida del tenant-qzx"},
		{name: "empty means the platform text", configured: "", want: store.DefaultWelcomeText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := welcomeHarness(t)
			h.seedSettings(func(s *store.TenantSettings) { s.WelcomeText = tc.configured })

			h.say("wa-1", "hola-qzx")

			incomingWantTexts(t, h, tc.want)
		})
	}
}

// TestWelcome_SettingsAreReadOnlyWhenTheWelcomeMayGoOut: WL-10. La configuración del tenant
// se lee en el limbo y en el reinicio; si no se puede leer no se saluda (WARN) y el turno
// sigue.
func TestWelcome_SettingsAreReadOnlyWhenTheWelcomeMayGoOut(t *testing.T) {
	h, _ := welcomeHarness(t)
	h.seedFlow(incomingStepFlow())
	h.seedRule(incomingKeywordRule(incomingFlowID))
	rt, flaky := incomingFlakyRuntime(h, nil)
	flaky.fail(func(s *incomingFlakyStore) { s.settingsErr = errors.New("settings-down-qzx") })

	if err := incomingHandleOn(h, rt, h.incoming("wa-1", incomingKeyword)); err != nil {
		t.Fatalf("HandleIncoming con la config ilegible = %v, quería nil", err)
	}

	incomingWantTexts(t, h, incomingRootPrompt)
	lines := incomingWantLog(t, h, "warn", "runtime: no se pudo leer la config del tenant; no se manda bienvenida", 1)
	incomingWantFields(t, lines[0], map[string]any{"tenant_id": harnessTenant, "session_id": harnessSession})
	if mark := h.repo.Welcome(h.key()); !mark.WelcomedAt.IsZero() {
		t.Errorf("marca = %+v, quería sin sellar: la bienvenida no salió", mark)
	}
	if reads := flaky.settingsReads(); reads == 0 {
		t.Errorf("la configuración del tenant no se leyó en el limbo")
	}
}
