//go:build pendiente

package fleethelpertest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

// errSpy es el error que devuelve el espía: verlo salir del decorador prueba que la llamada se
// delegó y que su resultado no se tocó.
var errSpy = errors.New("spy: resultado del repositorio envuelto")

// Lo que el espía devuelve en sus lecturas.
var (
	spySession  = fleet.Session{TenantID: "t", EdgeID: "e", SessionID: "s", State: fleet.StateOnline}
	spySessions = []fleet.Session{spySession}
)

const spyCount = 3

// spyRepository es un fleet.Repository que apunta cada llamada con sus argumentos y devuelve
// valores reconocibles junto a errSpy.
type spyRepository struct {
	calls []string
}

var _ fleet.Repository = (*spyRepository)(nil)

func (s *spyRepository) record(method string, args ...any) {
	s.calls = append(s.calls, fmt.Sprintf("%s%v", method, args))
}

func (s *spyRepository) MarkOnline(_ context.Context, tenantID, edgeID, sessionID string) error {
	s.record("MarkOnline", tenantID, edgeID, sessionID)
	return errSpy
}

func (s *spyRepository) MarkOffline(_ context.Context, tenantID, edgeID, sessionID string) error {
	s.record("MarkOffline", tenantID, edgeID, sessionID)
	return errSpy
}

func (s *spyRepository) MarkLoggedOut(_ context.Context, tenantID, edgeID, sessionID string) error {
	s.record("MarkLoggedOut", tenantID, edgeID, sessionID)
	return errSpy
}

func (s *spyRepository) SetState(_ context.Context, tenantID, sessionID string, state fleet.State) (bool, error) {
	s.record("SetState", tenantID, sessionID, state)
	return true, errSpy
}

func (s *spyRepository) CountLiveBySelfPn(_ context.Context, tenantID, selfPn string) (int, error) {
	s.record("CountLiveBySelfPn", tenantID, selfPn)
	return spyCount, errSpy
}

func (s *spyRepository) SaveHealth(_ context.Context, tenantID, edgeID, sessionID string, h fleet.HealthSnapshot) error {
	s.record("SaveHealth", tenantID, edgeID, sessionID, h.WhatsappState)
	return errSpy
}

func (s *spyRepository) Get(_ context.Context, tenantID, edgeID, sessionID string) (fleet.Session, bool, error) {
	s.record("Get", tenantID, edgeID, sessionID)
	return spySession, true, errSpy
}

func (s *spyRepository) List(_ context.Context, tenantID string) ([]fleet.Session, error) {
	s.record("List", tenantID)
	return spySessions, errSpy
}

func (s *spyRepository) SetSelfPn(_ context.Context, tenantID, edgeID, sessionID, selfPn string) error {
	s.record("SetSelfPn", tenantID, edgeID, sessionID, selfPn)
	return errSpy
}

func (s *spyRepository) SetProfile(_ context.Context, tenantID, sessionID string, profile fleet.Profile) (bool, error) {
	s.record("SetProfile", tenantID, sessionID, profile)
	return true, errSpy
}

// slowCall es uno de los diez métodos del decorador: cómo se le llama, lo que el envuelto tiene
// que haber recibido, lo que sale cuando se delega y lo que sale (los ceros) cuando el contexto
// corta la llamada.
type slowCall struct {
	name      string
	call      func(ctx context.Context, repo fleet.Repository) (result any, err error)
	forwarded string
	delegated any
	cut       any
}

// noResult es el «resto de resultados» de los métodos que solo devuelven error.
type noResult struct{}

// getResult junta los dos primeros resultados de Get.
type getResult struct {
	session fleet.Session
	found   bool
}

// slowCalls es la tabla de los diez métodos de fleet.Repository.
func slowCalls() []slowCall {
	return []slowCall{
		{"MarkOnline", func(ctx context.Context, r fleet.Repository) (any, error) {
			return noResult{}, r.MarkOnline(ctx, "t", "e", "s")
		}, "MarkOnline[t e s]", noResult{}, noResult{}},
		{"MarkOffline", func(ctx context.Context, r fleet.Repository) (any, error) {
			return noResult{}, r.MarkOffline(ctx, "t", "e", "s")
		}, "MarkOffline[t e s]", noResult{}, noResult{}},
		{"MarkLoggedOut", func(ctx context.Context, r fleet.Repository) (any, error) {
			return noResult{}, r.MarkLoggedOut(ctx, "t", "e", "s")
		}, "MarkLoggedOut[t e s]", noResult{}, noResult{}},
		{"SetState", func(ctx context.Context, r fleet.Repository) (any, error) {
			return r.SetState(ctx, "t", "s", fleet.StateLoggedOut)
		}, "SetState[t s loggedout]", true, false},
		{"CountLiveBySelfPn", func(ctx context.Context, r fleet.Repository) (any, error) {
			return r.CountLiveBySelfPn(ctx, "t", "573001112233")
		}, "CountLiveBySelfPn[t 573001112233]", spyCount, 0},
		{"SaveHealth", func(ctx context.Context, r fleet.Repository) (any, error) {
			return noResult{}, r.SaveHealth(ctx, "t", "e", "s", fleet.HealthSnapshot{WhatsappState: "dead"})
		}, "SaveHealth[t e s dead]", noResult{}, noResult{}},
		{"Get", func(ctx context.Context, r fleet.Repository) (any, error) {
			s, found, err := r.Get(ctx, "t", "e", "s")
			return getResult{s, found}, err
		}, "Get[t e s]", getResult{spySession, true}, getResult{}},
		{"List", func(ctx context.Context, r fleet.Repository) (any, error) {
			return r.List(ctx, "t")
		}, "List[t]", spySessions, []fleet.Session(nil)},
		{"SetSelfPn", func(ctx context.Context, r fleet.Repository) (any, error) {
			return noResult{}, r.SetSelfPn(ctx, "t", "e", "s", "573001112233")
		}, "SetSelfPn[t e s 573001112233]", noResult{}, noResult{}},
		{"SetProfile", func(ctx context.Context, r fleet.Repository) (any, error) {
			return r.SetProfile(ctx, "t", "s", fleet.ProfileActive)
		}, "SetProfile[t s active]", true, false},
	}
}

// TestSlowCalls_CoverTheWholePort: la tabla nombra los diez métodos del puerto, ni uno menos.
// Si fleet.Repository gana un método, este test obliga a darle su fila.
func TestSlowCalls_CoverTheWholePort(t *testing.T) {
	port := reflect.TypeFor[fleet.Repository]()
	covered := make(map[string]bool)
	for _, c := range slowCalls() {
		covered[c.name] = true
	}
	for i := range port.NumMethod() {
		if name := port.Method(i).Name; !covered[name] {
			t.Errorf("la tabla de SlowRepository no cubre fleet.Repository.%s", name)
		}
	}
	if len(covered) != port.NumMethod() {
		t.Errorf("la tabla tiene %d métodos y el puerto %d", len(covered), port.NumMethod())
	}
}

// requireDelegated afirma que la llamada llegó al envuelto, una vez y con sus argumentos, y que
// lo que sale del decorador es exactamente lo que el envuelto devolvió.
func requireDelegated(t *testing.T, c slowCall, repo *SlowRepository, spy *spyRepository) {
	t.Helper()
	spy.calls = nil
	got, err := c.call(context.Background(), repo)
	if !errors.Is(err, errSpy) {
		t.Errorf("err = %v, quería el del repositorio envuelto", err)
	}
	if !reflect.DeepEqual(got, c.delegated) {
		t.Errorf("resultado = %#v, quería el del repositorio envuelto, %#v", got, c.delegated)
	}
	if want := []string{c.forwarded}; !reflect.DeepEqual(spy.calls, want) {
		t.Errorf("el envuelto recibió %v, quería %v", spy.calls, want)
	}
}

// requireCut afirma que un contexto muerto corta la llamada: sale su error con los ceros y el
// envuelto no se entera.
func requireCut(ctx context.Context, t *testing.T, c slowCall, repo *SlowRepository, spy *spyRepository, wantErr error) {
	t.Helper()
	spy.calls = nil
	got, err := c.call(ctx, repo)
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, quería %v", err, wantErr)
	}
	if !reflect.DeepEqual(got, c.cut) {
		t.Errorf("resultado = %#v, quería los ceros, %#v", got, c.cut)
	}
	if len(spy.calls) != 0 {
		t.Errorf("el envuelto recibió %v con el contexto muerto: no se le toca", spy.calls)
	}
}

// TestSlowRepository_LiveContextWithoutDelay_Delegates: sin latencia (0 o negativa) el
// decorador es un paso a través: cada uno de los diez métodos delega con sus argumentos y
// devuelve lo que devuelva el envuelto, error incluido.
func TestSlowRepository_LiveContextWithoutDelay_Delegates(t *testing.T) {
	for _, delay := range []time.Duration{0, -time.Second} {
		for _, c := range slowCalls() {
			t.Run(fmt.Sprintf("%s/delay=%s", c.name, delay), func(t *testing.T) {
				spy := &spyRepository{}
				requireDelegated(t, c, NewSlow(spy, delay), spy)
			})
		}
	}
}

// TestSlowRepository_DeadContext_ReturnsItsErrorWithoutDelegating: con el contexto ya muerto
// —cancelado o vencido— cada método devuelve el error del contexto y sus ceros, y el envuelto
// no recibe la llamada. Igual sin latencia que con una de una hora: la espera es cancelable y
// no se llega a esperar nada.
func TestSlowRepository_DeadContext_ReturnsItsErrorWithoutDelegating(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, release := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer release()
	contexts := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"canceled", canceled, context.Canceled},
		{"expired", expired, context.DeadlineExceeded},
	}
	for _, delay := range []time.Duration{0, time.Hour} {
		for _, dead := range contexts {
			for _, c := range slowCalls() {
				t.Run(fmt.Sprintf("%s/%s/delay=%s", c.name, dead.name, delay), func(t *testing.T) {
					spy := &spyRepository{}
					requireCut(dead.ctx, t, c, NewSlow(spy, delay), spy, dead.want)
				})
			}
		}
	}
}

// TestSlowRepository_SetDelay_AppliesToLaterCalls: SetDelay cambia la conducta en caliente. Un
// decorador que nació con una hora de latencia delega al instante en cuanto se le quita, y
// vuelve a cortar con el contexto muerto cuando se le repone.
func TestSlowRepository_SetDelay_AppliesToLaterCalls(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range slowCalls() {
		t.Run(c.name, func(t *testing.T) {
			spy := &spyRepository{}
			repo := NewSlow(spy, time.Hour)
			requireCut(canceled, t, c, repo, spy, context.Canceled)
			// Si SetDelay no surtiera efecto, la llamada siguiente esperaría la hora entera.
			repo.SetDelay(0)
			requireDelegated(t, c, repo, spy)
			repo.SetDelay(time.Hour)
			requireCut(canceled, t, c, repo, spy, context.Canceled)
		})
	}
}

// TestSlowRepository_PositiveDelay_DelegatesAfterWaiting: con latencia positiva y el contexto
// vivo, la llamada se delega cuando la espera termina. La latencia es de un nanosegundo: el
// test no mide tiempo ni duerme, solo recorre la rama en la que vence el temporizador.
func TestSlowRepository_PositiveDelay_DelegatesAfterWaiting(t *testing.T) {
	for _, c := range slowCalls() {
		t.Run(c.name, func(t *testing.T) {
			spy := &spyRepository{}
			requireDelegated(t, c, NewSlow(spy, time.Nanosecond), spy)
		})
	}
}
