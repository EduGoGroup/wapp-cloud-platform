package runtime_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
)

// Tests de integración de PostgresSelfNumbers (Plan 020 · T2, consciente del rol):
// la exclusión de sesiones passive vive en la QUERY, así que se verifica contra
// fleet_sessions real. Reutilizan openTestDB/seedTenant (mismo gate WAPP_TEST_DB_DSN
// que el resto de la integración: sin DSN se saltan en local, corren en CI/e2e).

// seedFleetSessionRolePn siembra una fila de fleet_sessions con rol y self_pn
// explícitos (extiende el patrón de seedFleetSession con las columnas 0025/0028).
func seedFleetSessionRolePn(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID, role, selfPn string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, role, self_pn, last_connected_at, last_seen_at, updated_at)
		VALUES ($1, $2, $3, 'online', $4, $5, now(), now(), now())
		ON CONFLICT (tenant_id, edge_id, session_id)
			DO UPDATE SET state = 'online', role = EXCLUDED.role, self_pn = EXCLUDED.self_pn
	`, tenantID, edgeID, sessionID, role, selfPn)
	if err != nil {
		t.Fatalf("sembrar fleet_sessions (role=%s): %v", role, err)
	}
}

// El self_pn de una sesión BOT sí cuenta como número propio (bloquea el
// self-loop); el de una sesión PASSIVE se EXCLUYE (un passive nunca
// auto-responde ⇒ sin riesgo de loop, y así una sesión bot puede atender al
// número personal del mismo tenant).
func TestIntegration_PostgresSelfNumbers_ExcluyePassive(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)

	suffix := time.Now().UnixNano()
	botPn := fmt.Sprintf("57300%010d", suffix%1e10)
	passivePn := fmt.Sprintf("57301%010d", suffix%1e10)
	seedFleetSessionRolePn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-bot-%d", suffix), "bot", botPn)
	seedFleetSessionRolePn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-pas-%d", suffix), "passive", passivePn)

	nums, err := runtime.NewPostgresSelfNumbers(db).SelfNumbers(ctx, tenantID)
	if err != nil {
		t.Fatalf("SelfNumbers: %v", err)
	}
	if len(nums) != 1 || nums[0] != botPn {
		t.Fatalf("SelfNumbers debería devolver SOLO el número de la sesión bot (%s), devolvió %v", botPn, nums)
	}
	for _, n := range nums {
		if n == passivePn {
			t.Fatalf("el self_pn de una sesión passive NO debe contar como número propio, apareció en %v", nums)
		}
	}
}

// Un tenant cuyas ÚNICAS sesiones con self_pn son passive devuelve conjunto
// vacío: la guarda anti-self-loop no bloquea nada (isSelfLoop ⇒ false para
// cualquier remitente, incluido el número passive del propio tenant).
func TestIntegration_PostgresSelfNumbers_SoloPassiveConjuntoVacio(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)

	suffix := time.Now().UnixNano()
	seedFleetSessionRolePn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-pas-%d", suffix),
		"passive", fmt.Sprintf("57302%010d", suffix%1e10))

	nums, err := runtime.NewPostgresSelfNumbers(db).SelfNumbers(ctx, tenantID)
	if err != nil {
		t.Fatalf("SelfNumbers: %v", err)
	}
	if len(nums) != 0 {
		t.Fatalf("un tenant solo-passive debería devolver conjunto vacío, devolvió %v", nums)
	}
}
