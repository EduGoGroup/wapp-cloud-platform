// Porta internal/gateway/fleet/repository_postgres.go @ 809345b

package fleet

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// SaveHealth persiste el snapshot de salud reportado en el Heartbeat (Plan 031 ·
// T3). UPDATE acotado por (tenant_id, edge_id, session_id): NO toca `state` (link
// CloudLink), solo las columnas de salud. degraded_since se calcula en SQL con un
// CASE que preserva el instante de entrada: al entrar en degradado usa el valor
// previo o now() (COALESCE) y al salir lo pone NULL — atómico contra el valor
// actual de la fila. Un UPDATE de 0 filas (sesión aún sin registrar) es válido.
// El bloque del WORKER (Plan 051 · T4.3, campos 9-15) se escribe en columnas
// NULLABLE: un puntero nil / un texto vacío / un mapa vacío se persisten como NULL
// («este Edge no lo sabe»), NUNCA como cero. Y se escriben SIEMPRE, también cuando
// son NULL: un snapshot que dejó de saber el taskset debe BORRAR el valor previo,
// porque conservar un "disjunta" viejo es publicar una salud inventada.
//
// Promesas que su test fija: la sentencia exacta y sus 19 argumentos ($12 es
// h.Degraded()); un fallo del driver vuelve envuelto como "fleet: persistir salud:
// …".
func (r *PostgresRepository) SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h HealthSnapshot) error {
	panic(pendiente.Implementar("fleet.PostgresRepository.SaveHealth"))
}
