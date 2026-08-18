// Package fleettest ofrece dobles de prueba para fleet.Repository. Hoy contiene
// SlowRepository, un decorador que inyecta latencia ANTES de delegar cada llamada,
// para ejercitar los caminos de deadline/cancelación de los handlers que consultan
// la flota (Plan 050 · T5.1-bis).
//
// NINGÚN código de producción importa este paquete: solo lo consumen tests. Vive
// fuera de un fichero _test.go a propósito, porque lo comparten los tests de DOS
// paquetes distintos (internal/publicapi e internal/gateway/grpc) y un helper en
// _test.go no es visible fuera de su propio paquete. Por eso tampoco importa
// "testing": así el paquete no arrastra el runtime de test y `go build ./...` lo
// compila limpio.
package fleettest

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
)

// SlowRepository decora un fleet.Repository añadiendo una latencia ajustable antes
// de delegar cada llamada. La espera es CANCELABLE: si el contexto muere durante
// la latencia, el método devuelve el error del contexto y NO toca el repositorio
// envuelto. Eso simula una consulta que el ctx mata a medias (el fallo que las
// pruebas de deadline quieren ver), no una consulta lenta que igual termina bien.
//
// Es seguro para uso concurrente en la medida en que lo sea el repositorio
// envuelto: la latencia se guarda en un atomic.Int64 (nanosegundos), así que
// SetDelay puede llamarse desde otra goroutine con llamadas en vuelo sin data race
// (el CI corre con -race).
type SlowRepository struct {
	inner   fleet.Repository
	delayNs atomic.Int64
}

// NewSlow devuelve un fleet.Repository que espera d antes de delegar cada llamada
// en inner. Una d <= 0 desactiva la latencia (el decorador queda como paso a
// través, salvo por la comprobación del contexto).
func NewSlow(inner fleet.Repository, d time.Duration) *SlowRepository {
	s := &SlowRepository{inner: inner}
	s.delayNs.Store(int64(d))
	return s
}

// SetDelay ajusta la latencia en caliente. Seguro para uso concurrente.
func (s *SlowRepository) SetDelay(d time.Duration) {
	s.delayNs.Store(int64(d))
}

// delay devuelve la latencia vigente con una lectura atómica.
func (s *SlowRepository) delay() time.Duration {
	return time.Duration(s.delayNs.Load())
}

// wait bloquea la latencia vigente o hasta que el contexto muera, lo que ocurra
// antes. Devuelve nil si la espera se completó y el error del contexto si este se
// canceló o venció; el llamante DEBE retornar ese error sin tocar el repositorio
// envuelto.
//
// Con latencia <= 0 devolvemos ctx.Err() (y no nil directamente) para que el
// decorador se comporte igual con y sin latencia: un contexto ya muerto corta la
// llamada en ambos casos. Si el contexto está vivo, ctx.Err() es nil y la llamada
// se delega con normalidad, así que el caso feliz no cambia.
//
// Ojo con lo que esto NO demuestra. Que el repositorio real tampoco toque la BD
// con un contexto ya muerto no lo garantiza este doble: lo garantiza
// database/sql, que con un ctx cancelado devuelve su error ANTES de pedir
// conexión (DB.conn, src/database/sql/sql.go). Aquí solo cortamos antes por
// coherencia.
//
// La divergencia que SÍ existe está en el otro caso: con d > 0 y un contexto que
// vence DURANTE la latencia, este doble garantiza que no hubo escritura ninguna,
// mientras que el repositorio real puede haber commiteado ya y devolver error
// igualmente (el ctx muere después de que Postgres aplicara el UPDATE). Un test
// que afirme «tras el deadline no se escribió nada» quedaría VERDE con el doble
// y sería flaky contra la BD: esa afirmación hay que verificarla contra Postgres,
// no contra este decorador.
func (s *SlowRepository) wait(ctx context.Context) error {
	d := s.delay()
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// MarkOnline implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) MarkOnline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.inner.MarkOnline(ctx, tenantID, edgeID, sessionID)
}

// MarkOffline implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) MarkOffline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.inner.MarkOffline(ctx, tenantID, edgeID, sessionID)
}

// MarkLoggedOut implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) MarkLoggedOut(ctx context.Context, tenantID, edgeID, sessionID string) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.inner.MarkLoggedOut(ctx, tenantID, edgeID, sessionID)
}

// SetState implementa fleet.Repository tras la latencia inyectada. Si el contexto
// muere durante la espera devuelve found=false y el error del contexto.
func (s *SlowRepository) SetState(ctx context.Context, tenantID, sessionID string, state fleet.State) (bool, error) {
	if err := s.wait(ctx); err != nil {
		return false, err
	}
	return s.inner.SetState(ctx, tenantID, sessionID, state)
}

// CountLiveBySelfPn implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) CountLiveBySelfPn(ctx context.Context, tenantID, selfPn string) (int, error) {
	if err := s.wait(ctx); err != nil {
		return 0, err
	}
	return s.inner.CountLiveBySelfPn(ctx, tenantID, selfPn)
}

// SaveHealth implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h fleet.HealthSnapshot) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.inner.SaveHealth(ctx, tenantID, edgeID, sessionID, h)
}

// Get implementa fleet.Repository tras la latencia inyectada. Si el contexto muere
// durante la espera devuelve la sesión cero, found=false y el error del contexto.
func (s *SlowRepository) Get(ctx context.Context, tenantID, edgeID, sessionID string) (fleet.Session, bool, error) {
	if err := s.wait(ctx); err != nil {
		return fleet.Session{}, false, err
	}
	return s.inner.Get(ctx, tenantID, edgeID, sessionID)
}

// List implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) List(ctx context.Context, tenantID string) ([]fleet.Session, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.inner.List(ctx, tenantID)
}

// SetSelfPn implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) SetSelfPn(ctx context.Context, tenantID, edgeID, sessionID, selfPn string) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.inner.SetSelfPn(ctx, tenantID, edgeID, sessionID, selfPn)
}

// SetRole implementa fleet.Repository tras la latencia inyectada. Si el contexto
// muere durante la espera devuelve found=false y el error del contexto.
func (s *SlowRepository) SetRole(ctx context.Context, tenantID, sessionID string, role fleet.Role) (bool, error) {
	if err := s.wait(ctx); err != nil {
		return false, err
	}
	return s.inner.SetRole(ctx, tenantID, sessionID, role)
}

var _ fleet.Repository = (*SlowRepository)(nil)
