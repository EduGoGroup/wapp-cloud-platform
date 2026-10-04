package iamidentity_test

// Parte de m2m_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): las promesas del service token (R-I3, R-I4, R-I5); gemelo de m2m_token.go.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

// --- El Service Token: canje, caché y caducidad (R-I3, R-I4) ---

// TestM2M_ExchangesOnceAndReusesServiceToken (R-I3): dos operaciones, un canje, el mismo
// portador; la API key nunca es portador.
func TestM2M_ExchangesOnceAndReusesServiceToken(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusCreated, body: ensureOK})
	c := f.client(t, newFakeClock())

	for i := range 3 {
		if err := ensure(c); err != nil {
			t.Fatalf("EnsureUser #%d: %v", i+1, err)
		}
	}
	requireExchanges(t, f, 1, "el token se cachea hasta que expira")
	for _, b := range f.bearers() {
		if b != "svc-token-1" {
			t.Errorf("portador = %q, quería svc-token-1", b)
		}
	}
}

// TestM2M_ExpiredTokenIsExchangedAgain (R-I3): pasada su vida, la siguiente llamada canjea de
// nuevo y usa el token nuevo.
func TestM2M_ExpiredTokenIsExchangedAgain(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	clock := newFakeClock()
	c := f.client(t, clock)

	if err := ensure(c); err != nil {
		t.Fatalf("primera EnsureUser: %v", err)
	}
	clock.Advance(15 * time.Minute)
	if err := ensure(c); err != nil {
		t.Fatalf("segunda EnsureUser: %v", err)
	}
	requireExchanges(t, f, 2, "el token venció entre las dos llamadas")
	if b := f.bearers(); len(b) != 2 || b[0] != "svc-token-1" || b[1] != "svc-token-2" {
		t.Errorf("portadores = %v, quería [svc-token-1 svc-token-2]", b)
	}
}

// TestM2M_TokenLifetimeKeepsSafetyMargin (R-I3): la caché da por vencido el token 30 s ANTES de
// expires_in, salvo que eso lo dejara nacido muerto (expires_in <= 60 s): entonces, la mitad.
func TestM2M_TokenLifetimeKeepsSafetyMargin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		expiresIn int
		usable    time.Duration
	}{
		{name: "long_token_minus_margin", expiresIn: 900, usable: 870 * time.Second},
		{name: "just_above_twice_the_margin", expiresIn: 61, usable: 31 * time.Second},
		{name: "twice_the_margin_uses_half", expiresIn: 60, usable: 30 * time.Second},
		{name: "short_token_uses_half", expiresIn: 40, usable: 20 * time.Second},
		{name: "one_second_token_uses_half", expiresIn: 1, usable: 500 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeM2M(t)
			f.set(func(f *fakeM2M) { f.expiresIn = tt.expiresIn })
			clock := newFakeClock()
			c := f.client(t, clock)

			if err := ensure(c); err != nil {
				t.Fatalf("EnsureUser: %v", err)
			}
			clock.Advance(tt.usable - time.Millisecond)
			if err := ensure(c); err != nil {
				t.Fatalf("EnsureUser: %v", err)
			}
			requireExchanges(t, f, 1, "un milisegundo antes de su vida usable el token sigue valiendo")
			clock.Advance(time.Millisecond)
			if err := ensure(c); err != nil {
				t.Fatalf("EnsureUser: %v", err)
			}
			requireExchanges(t, f, 2, "al cumplir su vida usable el token se da por vencido")
		})
	}
}

// TestM2M_ExchangeCarriesKeyInBody (R-I4): la key viaja en el cuerpo como `api_key`, el canje no
// lleva Authorization ni campos inventados, y la key no es nunca portador de negocio.
func TestM2M_ExchangeCarriesKeyInBody(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	if err := ensure(f.client(t, newFakeClock())); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	body, auth := f.tokenBodyAndAuth()
	if len(body) != 1 || body["api_key"] != m2mAPIKey {
		t.Errorf("cuerpo del canje = %v, quería solo api_key=%q", body, m2mAPIKey)
	}
	for _, extra := range []string{"grant_type", "client_id", "scope"} {
		if _, present := body[extra]; present {
			t.Errorf("el canje lleva %q: identity no lo pide", extra)
		}
	}
	if auth != "" {
		t.Errorf("Authorization del canje = %q, quería ninguna: la key se canjea, no se presenta", auth)
	}
	for _, b := range f.bearers() {
		if strings.Contains(b, m2mAPIKey) {
			t.Error("la API key viajó como portador de negocio")
		}
	}
}

// --- Token rechazado (R-I3) ---

// rejectToken hace que la ruta de negocio conteste 401 al token dado y 201 al resto.
func rejectToken(token string) func(m2mRequest) m2mReply {
	return func(req m2mRequest) m2mReply {
		if req.bearer == token {
			return m2mReply{status: http.StatusUnauthorized, code: "UNAUTHORIZED"}
		}
		return m2mReply{status: http.StatusCreated, body: ensureOK}
	}
}

// TestM2M_RejectedTokenIsReexchangedOnceAndRetriedOnce (R-I3): con un 401 perpetuo, un recanje y
// un reintento, no un bucle; el reintento usa el token recién canjeado.
func TestM2M_RejectedTokenIsReexchangedOnceAndRetriedOnce(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.respondWith(m2mReply{status: http.StatusUnauthorized, code: "UNAUTHORIZED"})

	err := ensure(f.client(t, newFakeClock()))
	if !errors.Is(err, domain.ErrMachineCredentialInvalid) {
		t.Fatalf("err = %v, quería ErrMachineCredentialInvalid", err)
	}
	requireExchanges(t, f, 2, "uno inicial y uno tras el 401")
	if b := f.bearers(); len(b) != 2 || b[0] != "svc-token-1" || b[1] != "svc-token-2" {
		t.Errorf("portadores = %v, quería [svc-token-1 svc-token-2]: un intento y un reintento", b)
	}
}

// TestM2M_RejectedTokenRecoversWithFreshToken (R-I3): si el token nuevo vale, la operación sale
// bien y el nuevo queda en la caché.
func TestM2M_RejectedTokenRecoversWithFreshToken(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.set(func(f *fakeM2M) { f.reply = rejectToken("svc-token-1") })
	c := f.client(t, newFakeClock())

	for i := range 2 {
		if err := ensure(c); err != nil {
			t.Fatalf("EnsureUser #%d: %v", i+1, err)
		}
	}
	requireExchanges(t, f, 2, "un recanje y después se reutiliza el nuevo")
	if b := f.bearers(); len(b) != 3 || b[2] != "svc-token-2" {
		t.Errorf("portadores = %v, quería [svc-token-1 svc-token-2 svc-token-2]", b)
	}
}

// TestM2M_ConcurrentRejectionsReexchangeOnce (R-I3): dos llamadas que se comen el 401 del MISMO
// token a la vez producen UN recanje, no dos.
func TestM2M_ConcurrentRejectionsReexchangeOnce(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	var (
		mu       sync.Mutex
		rejected int
		bothIn   = make(chan struct{})
	)
	reject := rejectToken("svc-token-1")
	f.set(func(f *fakeM2M) {
		f.reply = func(req m2mRequest) m2mReply {
			if req.bearer == "svc-token-1" {
				// Las dos se retienen hasta que AMBAS han presentado svc-token-1.
				mu.Lock()
				rejected++
				if rejected == 2 {
					close(bothIn)
				}
				mu.Unlock()
				select {
				case <-bothIn:
				case <-time.After(2 * time.Second):
					t.Error("las dos llamadas no llegaron a presentar el mismo token a la vez")
				}
			}
			return reject(req)
		}
	})
	c := f.client(t, newFakeClock())

	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- ensure(c) }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("EnsureUser: %v", err)
		}
	}
	requireExchanges(t, f, 2, "el canje inicial y UN recanje compartido")
}

// TestM2M_FailedReexchangeEvictsRejectedToken (R-I3): si el recanje falla, el token rechazado
// sale de la caché y la siguiente llamada canjea en vez de volver a presentarlo.
func TestM2M_FailedReexchangeEvictsRejectedToken(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.set(func(f *fakeM2M) { f.reply = rejectToken("svc-token-1") })
	clock := newFakeClock()
	c := f.client(t, clock)

	if err := ensure(c); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	// Ahora identity rechaza svc-token-2 y además el canje se cae.
	f.set(func(f *fakeM2M) {
		f.reply = rejectToken("svc-token-2")
		f.tokenStatus = http.StatusServiceUnavailable
	})
	clock.Advance(time.Minute)
	if err := ensure(c); !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Fatalf("err = %v, quería ErrIdentityUnavailable (el recanje se cayó)", err)
	}
	f.set(func(f *fakeM2M) { f.tokenStatus = http.StatusOK })
	clock.Advance(2 * time.Second) // fuera de la caché negativa
	if err := ensure(c); err != nil {
		t.Fatalf("EnsureUser tras recuperarse identity: %v", err)
	}
	// Sin desalojo, la tercera llamada presentaría otra vez svc-token-2 y se comería un 401 más.
	want := []string{"svc-token-1", "svc-token-2", "svc-token-2", "svc-token-4"}
	if b := f.bearers(); strings.Join(b, ",") != strings.Join(want, ",") {
		t.Errorf("portadores = %v, quería %v: no se vuelve a presentar el token que identity rechazó", b, want)
	}
	requireExchanges(t, f, 4, "inicial, recanje de svc-token-1, recanje caído y canje nuevo")
}

// --- Caché negativa ---

// TestM2M_FailedExchangeIsRememberedBriefly: un canje caído se recuerda 1 s; dentro de esa
// ventana nadie vuelve a canjear y todos reciben el mismo error; pasada, se canjea de nuevo.
func TestM2M_FailedExchangeIsRememberedBriefly(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	f.set(func(f *fakeM2M) { f.tokenStatus = http.StatusServiceUnavailable })
	clock := newFakeClock()
	c := f.client(t, clock)

	first := ensure(c)
	if !errors.Is(first, domain.ErrIdentityUnavailable) {
		t.Fatalf("err = %v, quería ErrIdentityUnavailable", first)
	}
	clock.Advance(time.Second - time.Millisecond)
	if err := ensure(c); !errors.Is(err, domain.ErrIdentityUnavailable) {
		t.Errorf("dentro de la ventana: err = %v, quería el mismo ErrIdentityUnavailable", err)
	}
	requireExchanges(t, f, 1, "dentro de la ventana el fallo se sirve de memoria")

	f.set(func(f *fakeM2M) { f.tokenStatus = http.StatusOK })
	clock.Advance(time.Millisecond)
	if err := ensure(c); err != nil {
		t.Fatalf("pasada la ventana: %v", err)
	}
	requireExchanges(t, f, 2, "pasada la ventana se vuelve a canjear")
}

// TestM2M_CanceledExchangeIsNotRemembered: un canje que falla porque el contexto de quien
// canjeaba se canceló no se recuerda; el siguiente canjea sin mover el reloj.
func TestM2M_CanceledExchangeIsNotRemembered(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	f.set(func(f *fakeM2M) {
		f.onExchange = func(r *http.Request) {
			arrived <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	})
	c := f.client(t, newFakeClock())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.EnsureUser(ctx, m2mEmail, "Ana", "Pérez")
		done <- err
	}()
	<-arrived
	cancel()
	if err := <-done; err == nil {
		t.Fatal("la llamada cancelada a mitad del canje devolvió nil")
	}
	f.set(func(f *fakeM2M) { f.onExchange = nil })
	close(release)

	if err := ensure(c); err != nil {
		t.Fatalf("EnsureUser tras la cancelación ajena: %v (se cacheó una prisa ajena)", err)
	}
	requireExchanges(t, f, 2, "la cancelación no se recuerda")
}

// --- El candado (R-I5) ---

// TestM2M_ColdBurstProducesSingleExchange (R-I5): con la caché fría, N llamadas concurrentes
// producen UN canje y todas usan su token.
func TestM2M_ColdBurstProducesSingleExchange(t *testing.T) {
	t.Parallel()
	f := newFakeM2M(t)
	arrived := make(chan struct{}, 64)
	release := make(chan struct{})
	f.set(func(f *fakeM2M) {
		f.onExchange = func(*http.Request) {
			arrived <- struct{}{}
			<-release
		}
	})
	c := f.client(t, newFakeClock())

	const burst = 20
	errs := make(chan error, burst)
	for range burst {
		go func() { errs <- ensure(c) }()
	}
	// El primer canje queda retenido; se da margen a que una estampida se viera antes de soltarlo.
	<-arrived
	time.Sleep(50 * time.Millisecond)
	close(release)
	for range burst {
		if err := <-errs; err != nil {
			t.Fatalf("EnsureUser: %v", err)
		}
	}
	requireExchanges(t, f, 1, "una ráfaga fría no es una estampida")
	b := f.bearers()
	if len(b) != burst {
		t.Errorf("llamadas de negocio = %d, quería %d", len(b), burst)
	}
	for _, bearer := range b {
		if bearer != "svc-token-1" { //nolint:gosec // token de mentira de un test
			t.Errorf("portador = %q, quería el único token canjeado", bearer)
		}
	}
}

// TestM2M_CanceledContextDoesNotWaitForExchange (R-I5): con el contexto ya muerto no se toca
// identity; cancelado mientras otro canjea, se vuelve en el acto sin esperar ese canje.
func TestM2M_CanceledContextDoesNotWaitForExchange(t *testing.T) {
	t.Parallel()
	t.Run("already_canceled_never_reaches_identity", func(t *testing.T) {
		t.Parallel()
		f := newFakeM2M(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := f.client(t, newFakeClock())
		// Se repite: con el turno libre y el ctx muerto, un select sin la comprobación previa
		// elegiría al azar entre los dos casos, y una sola llamada acertaría por suerte la mitad
		// de las veces.
		for i := range 64 {
			_, err := c.EnsureUser(ctx, m2mEmail, "Ana", "Pérez")
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("llamada #%d: err = %v, quería context.Canceled", i+1, err)
			}
		}
		if exchanges, calls := f.snapshot(); exchanges != 0 || len(calls) != 0 {
			t.Errorf("canjes = %d, llamadas = %d; quería ninguno con el ctx ya muerto", exchanges, len(calls))
		}
	})
	t.Run("canceled_while_another_exchanges_returns_at_once", func(t *testing.T) {
		t.Parallel()
		f := newFakeM2M(t)
		arrived := make(chan struct{}, 1)
		release := make(chan struct{})
		f.set(func(f *fakeM2M) {
			f.onExchange = func(*http.Request) {
				arrived <- struct{}{}
				<-release
			}
		})
		c := f.client(t, newFakeClock())

		holder := make(chan error, 1)
		go func() { holder <- ensure(c) }()
		<-arrived // el primero tiene el turno y está canjeando

		ctx, cancel := context.WithCancel(context.Background())
		waiter := make(chan error, 1)
		go func() {
			_, err := c.EnsureUser(ctx, m2mEmail, "Ana", "Pérez")
			waiter <- err
		}()
		time.Sleep(20 * time.Millisecond) // que el segundo llegue a esperar el turno
		cancel()
		select {
		case err := <-waiter:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, quería context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("la llamada cancelada siguió esperando el canje ajeno")
		}
		close(release)
		if err := <-holder; err != nil {
			t.Fatalf("EnsureUser del que canjeaba: %v", err)
		}
		requireExchanges(t, f, 1, "el cancelado no canjeó")
	})
}
