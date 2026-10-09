// Porta internal/flujos/trigger/store_postgres.go @ c0c0c03

package trigger

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// PostgresStore implementa TriggerStore con SQL raw sobre public.flow_triggers.
// Todas las queries están parametrizadas por tenant_id (INV-8).
//
// Su verdad la da triggerhelpertest.Contrato contra un Postgres de verdad
// (test/procesos/trigger_contrato_test.go); el unitario del paquete solo mira lo
// que no necesita base: las sentencias que emite, sus argumentos, el mapeo de las
// filas y la envoltura de los errores.
//
// Mapeo de columnas, igual en los cinco métodos: keyword, flow_id, message,
// session_id y event_kind son nullable y "" viaja como NULL (y NULL se lee como "");
// kind y match_type viajan tal cual, también vacíos; created_at y updated_at los
// pone la base y el puerto no los enseña.
//
// Todo fallo de la base sale envuelto (%w) con un prefijo "trigger: …" propio de
// la operación, salvo «no hay fila», que es ErrTriggerNotFound sin envolver.
type PostgresStore struct{}

// NewPostgresStore construye el store sobre el pool dado. No consulta la base.
func NewPostgresStore(_ *sql.DB) *PostgresStore {
	panic(pendiente.Implementar("trigger.NewPostgresStore"))
}

// Insert persiste una regla nueva; el trigger_id lo asigna Postgres
// (gen_random_uuid, RETURNING). r.TriggerID del argumento se ignora. Devuelve la
// regla del argumento con su TriggerID; si falla, la regla cero y
// "trigger: insertar regla: …".
func (s *PostgresStore) Insert(_ context.Context, _ Rule) (Rule, error) {
	panic(pendiente.Implementar("trigger.PostgresStore.Insert"))
}

// List devuelve todas las reglas del tenant, ordenadas por trigger_id; sin reglas,
// un slice vacío no nil. Errores: "trigger: listar reglas: …" si la consulta falla,
// "trigger: escanear regla: …" si una fila no se puede leer y
// "trigger: iterar reglas: …" si el recorrido se corta; con error devuelve nil.
//
// Deuda D-17, que se porta tal cual: el fallo de cerrar las filas se descarta.
func (s *PostgresStore) List(_ context.Context, _ string) ([]Rule, error) {
	panic(pendiente.Implementar("trigger.PostgresStore.List"))
}

// ListByKind devuelve las reglas del tenant de un kind dado aplicables a la sesión:
// session_id = $3 (específica) O session_id IS NULL (global). sessionID vacío ("")
// ⇒ solo las globales (Plan 020 · T4). Mismo orden y mismos errores que List, salvo
// el de la consulta: "trigger: listar reglas por kind: …".
func (s *PostgresStore) ListByKind(_ context.Context, _, _ string, _ Kind) ([]Rule, error) {
	panic(pendiente.Implementar("trigger.PostgresStore.ListByKind"))
}

// Get devuelve una regla por (tenant_id, trigger_id); ErrTriggerNotFound si no
// existe (o es de otro tenant, INV-8). Cualquier otro fallo: "trigger: leer regla: …".
func (s *PostgresStore) Get(_ context.Context, _, _ string) (Rule, error) {
	panic(pendiente.Implementar("trigger.PostgresStore.Get"))
}

// Delete borra una regla por (tenant_id, trigger_id); ErrTriggerNotFound si no
// existía (ninguna fila afectada). Errores: "trigger: borrar regla: …" si la
// sentencia falla y "trigger: filas afectadas: …" si el driver no sabe contarlas.
func (s *PostgresStore) Delete(_ context.Context, _, _ string) error {
	panic(pendiente.Implementar("trigger.PostgresStore.Delete"))
}
