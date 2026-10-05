// Porta internal/gateway/fleet/fleettest/slowrepo.go @ 809345b

package fleethelpertest

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// SlowRepository decora un fleet.Repository añadiendo una latencia ajustable antes
// de delegar cada llamada, para ejercitar los caminos de deadline/cancelación de
// los handlers que consultan la flota (Plan 050 · T5.1-bis). La espera es
// CANCELABLE: si el contexto muere durante la latencia, el método devuelve el
// error del contexto y NO toca el repositorio envuelto. Eso simula una consulta
// que el ctx mata a medias (el fallo que las pruebas de deadline quieren ver), no
// una consulta lenta que igual termina bien.
//
// Lo que promete cada uno de sus diez métodos:
//   - con el contexto ya muerto devuelve ctx.Err() y los ceros del resto de sus
//     resultados (found=false, 0, nil, la Session cero), con latencia y sin ella,
//     y el repositorio envuelto no recibe la llamada;
//   - con el contexto vivo espera la latencia vigente y después delega: devuelve
//     exactamente lo que devuelva el envuelto, con los mismos argumentos;
//   - una latencia <= 0 no espera: el decorador queda como paso a través, salvo
//     por la comprobación del contexto.
//
// Es seguro para uso concurrente en la medida en que lo sea el repositorio
// envuelto: la latencia se guarda en un atomic.Int64 (nanosegundos), así que
// SetDelay puede llamarse desde otra goroutine con llamadas en vuelo sin data race
// (el CI corre con -race).
//
// Ojo con lo que esto NO demuestra. Que el repositorio real tampoco toque la BD
// con un contexto ya muerto no lo garantiza este doble: lo garantiza
// database/sql, que con un ctx cancelado devuelve su error ANTES de pedir
// conexión. Aquí solo se corta antes por coherencia.
//
// La divergencia que SÍ existe está en el otro caso: con d > 0 y un contexto que
// vence DURANTE la latencia, este doble garantiza que no hubo escritura ninguna,
// mientras que el repositorio real puede haber commiteado ya y devolver error
// igualmente (el ctx muere después de que Postgres aplicara el UPDATE). Un test
// que afirme «tras el deadline no se escribió nada» quedaría VERDE con el doble
// y sería flaky contra la BD: esa afirmación hay que verificarla contra Postgres,
// no contra este decorador.
//
// En el paquete viejo vivía en fleettest, que no importaba "testing"; aquí
// comparte paquete con la suite, que sí.
type SlowRepository struct{}

// SlowRepository cumple el puerto.
var _ fleet.Repository = (*SlowRepository)(nil)

// NewSlow devuelve un fleet.Repository que espera d antes de delegar cada llamada
// en inner. Una d <= 0 desactiva la latencia (el decorador queda como paso a
// través, salvo por la comprobación del contexto).
func NewSlow(inner fleet.Repository, d time.Duration) *SlowRepository {
	panic(pendiente.Implementar("fleethelpertest.NewSlow"))
}

// SetDelay ajusta la latencia en caliente: vale para las llamadas que empiecen
// después. Seguro para uso concurrente.
func (s *SlowRepository) SetDelay(d time.Duration) {
	panic(pendiente.Implementar("fleethelpertest.SlowRepository.SetDelay"))
}

// MarkOnline implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) MarkOnline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	panic(pendiente.Implementar("fleethelpertest.SlowRepository.MarkOnline"))
}

// MarkOffline implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) MarkOffline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	panic(pendiente.Implementar("fleethelpertest.SlowRepository.MarkOffline"))
}

// MarkLoggedOut implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) MarkLoggedOut(ctx context.Context, tenantID, edgeID, sessionID string) error {
	panic(pendiente.Implementar("fleethelpertest.SlowRepository.MarkLoggedOut"))
}

// SetState implementa fleet.Repository tras la latencia inyectada. Si el contexto
// muere durante la espera devuelve found=false y el error del contexto.
func (s *SlowRepository) SetState(ctx context.Context, tenantID, sessionID string, state fleet.State) (bool, error) {
	panic(pendiente.Implementar("fleethelpertest.SlowRepository.SetState"))
}

// CountLiveBySelfPn implementa fleet.Repository tras la latencia inyectada. Si el
// contexto muere durante la espera devuelve 0 y el error del contexto.
func (s *SlowRepository) CountLiveBySelfPn(ctx context.Context, tenantID, selfPn string) (int, error) {
	panic(pendiente.Implementar("fleethelpertest.SlowRepository.CountLiveBySelfPn"))
}

// SaveHealth implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h fleet.HealthSnapshot) error {
	panic(pendiente.Implementar("fleethelpertest.SlowRepository.SaveHealth"))
}

// Get implementa fleet.Repository tras la latencia inyectada. Si el contexto muere
// durante la espera devuelve la sesión cero, found=false y el error del contexto.
func (s *SlowRepository) Get(ctx context.Context, tenantID, edgeID, sessionID string) (fleet.Session, bool, error) {
	panic(pendiente.Implementar("fleethelpertest.SlowRepository.Get"))
}

// List implementa fleet.Repository tras la latencia inyectada. Si el contexto
// muere durante la espera devuelve nil y el error del contexto.
func (s *SlowRepository) List(ctx context.Context, tenantID string) ([]fleet.Session, error) {
	panic(pendiente.Implementar("fleethelpertest.SlowRepository.List"))
}

// SetSelfPn implementa fleet.Repository tras la latencia inyectada.
func (s *SlowRepository) SetSelfPn(ctx context.Context, tenantID, edgeID, sessionID, selfPn string) error {
	panic(pendiente.Implementar("fleethelpertest.SlowRepository.SetSelfPn"))
}

// SetProfile implementa fleet.Repository tras la latencia inyectada. Si el contexto
// muere durante la espera devuelve found=false y el error del contexto.
func (s *SlowRepository) SetProfile(ctx context.Context, tenantID, sessionID string, profile fleet.Profile) (bool, error) {
	panic(pendiente.Implementar("fleethelpertest.SlowRepository.SetProfile"))
}
