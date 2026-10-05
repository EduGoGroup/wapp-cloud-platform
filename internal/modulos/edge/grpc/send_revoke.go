// Porta internal/gateway/grpc/send.go @ c851591 (la familia de la revocación:
// RevokeLease, RevokeTenant, RestoreTenant). Trozo de send.go, partido por E-13.

package grpc

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// RevokeLease dispara el kill-switch del Edge: persiste la revocación y empuja
// el LeaseUpdate(Revoked) a TODAS sus sesiones vivas, en paralelo y esperando a que
// todos los empujes terminen. Devuelve "gatewaygrpc: lease no configurado" si no se
// inyectó el gestor de leases, o el error del gestor tal cual si la revocación no se
// pudo persistir (y entonces no empuja nada). Un empuje que falla NO es error de
// RevokeLease: la revocación ya está persistida y los push son notificación
// best-effort.
//
// 🔴 RELOJ (Plan 050 · Ola 3 · T3.4). Las TRES entradas de esta familia
// —RevokeLease, RevokeTenant y RestoreTenant— reciben el contexto PELADO del
// handler admin, que no trae deadline: sin el plazo de abajo, una base atascada
// dejaba al handler colgado sin techo. El reloj se pone AQUÍ DENTRO y no en el
// handler a propósito: es otro paquete y otra superficie, y el gateway es quien sabe
// cuánto vale su propia unidad de trabajo.
//
// Van las tres, no solo la que se notó (REQ-050.12): son la misma familia con el
// mismo defecto, y dejar una fuera es exactamente el error contra el que ese
// requisito avisa. La que se notó es RevokeTenant, por su fleet.List —el SELECT sin
// LIMIT sobre todas las sesiones del tenant—.
//
// Presupuesto: el de una unidad de trabajo del carril (WithWorkTimeout,
// WAPP_GATEWAY_WORK_TIMEOUT, 5 s por defecto). Sin variable propia.
//
// ⚠️ El plazo es COMPARTIDO por la persistencia y por los push, igual que en el
// handshake y en el job del latido: si la escritura del lease se come el reloj, los
// push salen con un ctx ya vencido y no llegan. No es pérdida de la revocación —esa
// YA está persistida cuando se empuja, y los push son notificación best-effort— sino
// retraso: la instalación se entera en su siguiente Heartbeat, porque el gestor
// consulta el estado en cada Renew.
//
// ⚠️ Efecto lateral que conviene ver escrito: Registry.Push tiene su propio techo
// (sendTimeout, 10 s), y por esta vía el ctx pasa a ser el más corto de los dos. No
// se movió NINGÚN timeout (INV-050.6): WAPP_GRPC_PUSH_TIMEOUT sigue valiendo lo
// mismo y sigue siendo el techo para los llamantes que traen un ctx sin deadline.
func (s *Server) RevokeLease(_ context.Context, _, _ string) error {
	panic(pendiente.Implementar("grpc.Server.RevokeLease"))
}

// RevokeTenant dispara el kill-switch COMERCIAL de un tenant completo
// (D-055.2, Plan 055 · T3.3): persiste el corte (tenants.revoked_at, vía
// lease.Manager.RevokeTenant) y empuja el LeaseUpdate(Revoked) a las sesiones
// VIVAS de TODAS las instalaciones YA CONOCIDAS de ese tenant, y a ninguna de otro
// (R-G21). "Conocidas" se resuelve con fleet.Repository.List (persistente, sobrevive
// reinicios y cubre instalaciones offline en este momento pero registradas alguna
// vez); las instalaciones NUEVAS que ese tenant abra después no necesitan push --
// nacen revocadas solas porque el gestor consulta tenants.revoked_at en cada
// IssueInitial (T3.2). Sin fleet inyectado no hay instalaciones que notificar: el
// corte se persiste y no se empuja nada.
//
// NO marca leases.revoked de cada instalación (a diferencia de RevokeLease):
// eso dejaría sin retorno a un RestoreTenant posterior (D-055.2, ver el
// comentario de lease.Manager.RevokeTenant). El blob firmado que sí viaja por
// sesión lo produce SignTenantRevocation, que firma sin persistir por-edge.
//
// Errores: "gatewaygrpc: lease no configurado" sin gestor de leases; el error del
// gestor tal cual si el corte no se pudo persistir (y entonces ni lista ni empuja);
// "gatewaygrpc: listar instalaciones del tenant: …" envolviendo el error de
// fleet.List (el corte YA quedó persistido). Un empuje que falla no es error.
// Mismo reloj que RevokeLease, que aquí cubre además el fleet.List.
func (s *Server) RevokeTenant(_ context.Context, _ string) error {
	panic(pendiente.Implementar("grpc.Server.RevokeTenant"))
}

// RestoreTenant reactiva un tenant previamente revocado (Plan 055 · T3.3,
// reverso de RevokeTenant): delega en lease.Manager.RestoreTenant
// (tenants.revoked_at = NULL) y devuelve su error tal cual. NO re-emite leases
// vigentes ni empuja ningún push por sí mismo -- las instalaciones ya conectadas
// seguirán viendo su LeaseUpdate(Revoked) previo hasta su siguiente
// Heartbeat/Renew, momento en el que el gestor ya verá el tenant activo (mismo
// comportamiento que la reconexión tras un RevokeLease individual: la revocación
// previa no se retracta con un push, se deja de reafirmar en la siguiente emisión).
//
// Devuelve "gatewaygrpc: lease no configurado" sin gestor de leases. Mismo reloj
// que RevokeLease: un solo viaje, pero entra igual — es la hermana de las otras dos
// y la excepción de hoy es el defecto de mañana (REQ-050.12).
func (s *Server) RestoreTenant(_ context.Context, _ string) error {
	panic(pendiente.Implementar("grpc.Server.RestoreTenant"))
}
