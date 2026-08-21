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

// seedFleetSessionStateRolePn siembra una fila de fleet_sessions con state, rol y
// self_pn explícitos (extiende el patrón de seedFleetSession con las columnas
// 0025/0028/0029). El STATE pesa tanto como el rol: la query separa la sesión
// RETIRADA (loggedout, no vuelve sin re-QR) de la meramente desconectada (offline,
// recuperable).
//
// Desde el Plan 046 · T1.1 la fila lleva TAMBIÉN `profile`, derivado del rol con el
// MISMO CASE que el backfill de la 0063 (bot⇒active, resto⇒passive): la query bajo
// prueba ya decide por `profile`, y dejar la columna al DEFAULT —que es pasivo—
// convertiría en pasiva a toda sesión sembrada como bot. Es sembrado, no aserción:
// los casos de prueba y lo que afirman no cambian.
func seedFleetSessionStateRolePn(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID, state, role, selfPn string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, role, profile, self_pn, last_connected_at, last_seen_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, CASE $5::text WHEN 'bot' THEN 'active' ELSE 'passive' END, $6, now(), now(), now())
		ON CONFLICT (tenant_id, edge_id, session_id)
			DO UPDATE SET state = EXCLUDED.state, role = EXCLUDED.role,
			              profile = EXCLUDED.profile, self_pn = EXCLUDED.self_pn
	`, tenantID, edgeID, sessionID, state, role, selfPn)
	if err != nil {
		t.Fatalf("sembrar fleet_sessions (state=%s role=%s): %v", state, role, err)
	}
}

// seedFleetSessionRolePn siembra una sesión VIVA (online) con rol y self_pn dados.
func seedFleetSessionRolePn(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID, role, selfPn string) {
	t.Helper()
	seedFleetSessionStateRolePn(t, db, tenantID, edgeID, sessionID, "online", role, selfPn)
}

// contiene dice si el número está en el conjunto devuelto por SelfNumbers.
func contiene(nums []string, pn string) bool {
	for _, n := range nums {
		if n == pn {
			return true
		}
	}
	return false
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

// Una sesión RETIRADA (loggedout) no aporta su número: es un zombi que no vuelve
// sin re-emparejar, así que no puede auto-responder y no puede cerrar un bucle.
// Muerde el filtro state <> 'loggedout': sin él, la fila fantasma bloquearía.
func TestIntegration_PostgresSelfNumbers_SesionRetiradaNoBloquea(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)

	suffix := time.Now().UnixNano()
	zombiPn := fmt.Sprintf("57303%010d", suffix%1e10)
	seedFleetSessionStateRolePn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-zombi-%d", suffix),
		"loggedout", "bot", zombiPn)

	nums, err := runtime.NewPostgresSelfNumbers(db).SelfNumbers(ctx, tenantID)
	if err != nil {
		t.Fatalf("SelfNumbers: %v", err)
	}
	if contiene(nums, zombiPn) {
		t.Fatalf("una sesión loggedout NO debe aportar su self_pn (no auto-responde ⇒ no hay bucle que cerrar), devolvió %v", nums)
	}
}

// EL BUG QUE MOTIVA EL CAMBIO: el número está en DOS filas —la sesión viva marcada
// passive y la fila muerta (loggedout) de un emparejamiento anterior, que quedó en
// bot—. Marcar passive desde la consola tiene que surtir efecto: el número NO debe
// bloquear, o el bot nunca podrá atender al teléfono personal del tenant.
func TestIntegration_PostgresSelfNumbers_PassiveVivaConZombiBotDelMismoNumero(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)

	suffix := time.Now().UnixNano()
	pn := fmt.Sprintf("57304%010d", suffix%1e10)
	// La sesión VIVA de ese número, ya marcada passive por el operador.
	seedFleetSessionStateRolePn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-viva-%d", suffix),
		"online", "passive", pn)
	// El fantasma del emparejamiento anterior: MISMO número, retirado, aún en bot.
	seedFleetSessionStateRolePn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-muerta-%d", suffix),
		"loggedout", "bot", pn)

	nums, err := runtime.NewPostgresSelfNumbers(db).SelfNumbers(ctx, tenantID)
	if err != nil {
		t.Fatalf("SelfNumbers: %v", err)
	}
	if contiene(nums, pn) {
		t.Fatalf("una fila loggedout en bot NO debe mantener bloqueado el número de una sesión viva passive "+
			"(el cambio de rol quedaría sin efecto), devolvió %v", nums)
	}
}

// ⚠️ NO-REGRESIÓN CONTRA LA "OPTIMIZACIÓN" EQUIVOCADA: una sesión bot en OFFLINE
// sigue bloqueando. offline es el stream CloudLink caído y RECUPERABLE —el socket
// de WhatsApp sigue vivo y la sesión auto-responde al reconectar, drenando el
// outbox—, así que su número sí puede cerrar un bucle. Este test falla si alguien
// estrecha el filtro a state = 'online'.
func TestIntegration_PostgresSelfNumbers_SesionOfflineSigueBloqueando(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)

	suffix := time.Now().UnixNano()
	offlinePn := fmt.Sprintf("57305%010d", suffix%1e10)
	seedFleetSessionStateRolePn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-off-%d", suffix),
		"offline", "bot", offlinePn)

	nums, err := runtime.NewPostgresSelfNumbers(db).SelfNumbers(ctx, tenantID)
	if err != nil {
		t.Fatalf("SelfNumbers: %v", err)
	}
	if !contiene(nums, offlinePn) {
		t.Fatalf("una sesión bot OFFLINE (stream caído pero recuperable) SÍ debe seguir bloqueando su número "+
			"—filtrar por state='online' reabriría el bucle—, devolvió %v", nums)
	}
}

// La decisión es por NÚMERO, no por fila: el mismo self_pn repartido en dos edges
// se devuelve UNA sola vez. Muerde la agregación (sin GROUP BY salía duplicado, y
// el consumidor recorre el conjunto por cada entrante).
func TestIntegration_PostgresSelfNumbers_DeduplicaElMismoNumeroEnVariosEdges(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)

	suffix := time.Now().UnixNano()
	pn := fmt.Sprintf("57306%010d", suffix%1e10)
	seedFleetSessionRolePn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-a-%d", suffix), "bot", pn)
	seedFleetSessionRolePn(t, db, tenantID, "edge-B", fmt.Sprintf("sess-b-%d", suffix), "bot", pn)

	nums, err := runtime.NewPostgresSelfNumbers(db).SelfNumbers(ctx, tenantID)
	if err != nil {
		t.Fatalf("SelfNumbers: %v", err)
	}
	var veces int
	for _, n := range nums {
		if n == pn {
			veces++
		}
	}
	if veces != 1 {
		t.Fatalf("el mismo self_pn en dos edges debe devolverse UNA vez (agregado por número), apareció %d veces en %v", veces, nums)
	}
}
