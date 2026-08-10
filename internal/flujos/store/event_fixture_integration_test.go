// event_fixture_integration_test.go — la cadena tenant→evento que la 0054 exige
// (Ola 4.5): desde D-043.21 toda solicitud/respuesta NUEVA declara a su padre
// (`event_id`, CHECK NOT VALID + FK a conversation_events, que a su vez tiene FK a
// tenants), así que los tests de este paquete que siembran intakes o
// survey_results contra Postgres REAL necesitan primero un evento de verdad.
//
// Sin limpieza a propósito: los seeds de este paquete nunca la tuvieron (tenant y
// contacto son únicos por corrida) y wapp_test es desechable. El prefijo `t45i-`
// deja las filas rastreables.
package store_test

import (
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

// seedTenantEventoPG crea un tenant real y un evento conversacional suyo en el
// estado dado, y devuelve (tenantID, eventID). El evento usa sesión/contacto
// únicos para no chocar con el índice «uno vivo por tipo» (E-2); terminal ⇒
// closed_at sellado, como escribe transitionSQL.
func seedTenantEventoPG(t *testing.T, db *sql.DB, status string) (tenantID, eventID string) {
	t.Helper()
	tenantID = uuid.NewString()
	if _, err := db.Exec(`
		INSERT INTO public.tenants (id, slug, display_name)
		VALUES ($1, $2, 'Ola 4.5 store')
	`, tenantID, "t45i-"+tenantID); err != nil {
		t.Fatalf("sembrando tenant: %v", err)
	}
	eventID = seedEventoDePG(t, db, tenantID, status)
	return tenantID, eventID
}

// seedEventoDePG crea OTRO evento del mismo tenant (una solicitud por evento:
// índice único parcial intakes_event_id_uidx).
func seedEventoDePG(t *testing.T, db *sql.DB, tenantID, status string) string {
	t.Helper()
	var eventID string
	if err := db.QueryRow(`
		INSERT INTO public.conversation_events
			(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version, closed_at)
		VALUES ($1, 't45i-sess-' || gen_random_uuid(), gen_random_uuid(), 'cart',
		        't45i-' || gen_random_uuid(), $2, 'flujo-w45', 1,
		        CASE WHEN $2 = 'open' THEN NULL ELSE now() END)
		RETURNING id::text
	`, tenantID, status).Scan(&eventID); err != nil {
		t.Fatalf("sembrando evento (%s): %v", status, err)
	}
	return eventID
}
