package fleet_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
)

// TestIntegration_ProfilesByTenant_FotoCompletaYVersionMonotonica ejerce contra
// Postgres real la ÚNICA lectura que alimenta el kind:"filters" (Plan 046 · T2.1):
//
//   - la foto trae TODAS las sesiones del tenant, activas incluidas (el Edge asume
//     `active` para la sesión AUSENTE, así que omitirlas coincidiría hoy y mentiría el
//     día que una pasiva se reactive);
//   - dos filas de la MISMA sesión bajo dos edge_id distintos colapsan a UNA entrada;
//   - un segundo cambio de perfil produce una version ESTRICTAMENTE MAYOR (criterio
//     (b) de T2.1: la aserción es sobre `>`, no sobre «cambió»);
//   - el aislamiento por tenant: la sesión de otro tenant no aparece.
//
// 🔴 Reutiliza openTestDB/seedTenant de integration_test.go (mismo paquete de test).
func TestIntegration_ProfilesByTenant_FotoCompletaYVersionMonotonica(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	otroTenant := seedTenant(t, db)

	repo, _ := repoDePrueba(t, db)
	// sess-activa vive en DOS Edges del mismo tenant: es el caso que obliga al
	// GROUP BY session_id (la clave física lleva edge_id, el payload no).
	for _, s := range []struct{ edge, sess string }{
		{"edge-a", "sess-activa"},
		{"edge-b", "sess-activa"},
		{"edge-a", "sess-pasiva"},
	} {
		if err := repo.MarkOnline(ctx, tenantID, s.edge, s.sess); err != nil {
			t.Fatalf("MarkOnline %s/%s: %v", s.edge, s.sess, err)
		}
	}
	if err := repo.MarkOnline(ctx, otroTenant, "edge-x", "sess-ajena"); err != nil {
		t.Fatalf("MarkOnline ajena: %v", err)
	}

	// Estado inicial: ambas nacen `passive` (DEFAULT de la 0063, D-07). Se activa una.
	activarPerfil(t, repo, tenantID, "sess-activa", "estado inicial")

	tp1 := leerPerfiles(t, repo, tenantID, "tras activar una")
	if len(tp1.Sessions) != 2 {
		t.Fatalf("la foto trae %d sesiones (%v), quiero 2: las dos filas de sess-activa "+
			"colapsan a UNA entrada y las activas viajan igual", len(tp1.Sessions), tp1.Sessions)
	}
	if tp1.Sessions["sess-activa"] != fleet.ProfileActive {
		t.Fatalf("sess-activa = %q, quiero active", tp1.Sessions["sess-activa"])
	}
	if tp1.Sessions["sess-pasiva"] != fleet.ProfilePassive {
		t.Fatalf("sess-pasiva = %q, quiero passive", tp1.Sessions["sess-pasiva"])
	}
	if _, hay := tp1.Sessions["sess-ajena"]; hay {
		t.Fatal("aislamiento roto: la sesión de otro tenant entró en la foto")
	}
	if tp1.Version <= 0 {
		t.Fatalf("version = %d, quiero el max(profile_updated_at) en microsegundos (> 0)", tp1.Version)
	}

	// Segundo cambio ⇒ version ESTRICTAMENTE mayor. Criterio (b) de T2.1: la
	// comparación es numérica, no «es distinta» — una version que solo cambiara sin
	// crecer haría que el Edge descartara el cambio al reordenarse dos pushes.
	activarPerfil(t, repo, tenantID, "sess-pasiva", "segundo cambio")
	tp2 := leerPerfiles(t, repo, tenantID, "tras el segundo cambio")
	if tp2.Version <= tp1.Version {
		t.Fatalf("version no creció: %d -> %d (debe ser ESTRICTAMENTE mayor)", tp1.Version, tp2.Version)
	}
	if tp2.Sessions["sess-pasiva"] != fleet.ProfileActive {
		t.Fatalf("el segundo cambio no se refleja: %v", tp2.Sessions)
	}
}

// TestIntegration_ProfilesByTenant_ElRuidoDeLaFilaNoMueveLaVersion es la razón de
// existir de la migración 0065, comprobada contra Postgres real.
//
// 🔴 EL BUG QUE CIERRA. Hasta el code review del 2026-08-21 la version salía de
// `max(updated_at)`, y `updated_at` es el reloj de LA FILA: lo mueve MarkOnline, lo
// mueve el SaveHealth de CADA heartbeat, lo mueve SetSelfPn. Con MarkOnline corriendo
// inmediatamente antes de pushConfigsOnConnect (connect.go:514 vs :523/:537), un Edge
// con N sesiones del tenant publicaba N versiones distintas y crecientes CON EL MAPA
// IDÉNTICO en CADA reconexión. El Edge las re-aplicaba, las re-persistía en su SQLite,
// y las que llegaran desordenadas le hacían emitir su WARN «versión anterior o igual»
// EN OPERACIÓN NORMAL — enterrando la única línea de log que delataría una anomalía
// real de versionado.
//
// ⚠️ La regla NO la impone el motor: no hay trigger. Es de CÓDIGO —solo SetProfile
// escribe profile_updated_at— y este test es lo único que la vigila del lado SQL. Su
// gemelo sobre el doble en memoria es
// TestMemoryProfilesByTenant_SoloSetProfileMueveLaVersion.
func TestIntegration_ProfilesByTenant_ElRuidoDeLaFilaNoMueveLaVersion(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo, _ := repoDePrueba(t, db)

	for _, s := range []string{"sess-1", "sess-2"} {
		if err := repo.MarkOnline(ctx, tenantID, "edge-1", s); err != nil {
			t.Fatalf("MarkOnline %s: %v", s, err)
		}
	}
	activarPerfil(t, repo, tenantID, "sess-1", "inicial")
	tp1 := leerPerfiles(t, repo, tenantID, "tras el cambio inicial")

	escribirRuidoEnPostgres(t, repo, tenantID)

	tp2 := leerPerfiles(t, repo, tenantID, "tras el ruido")
	if tp2.Version != tp1.Version {
		t.Fatalf("la version se movió sin que cambiara ningún perfil: %d -> %d. "+
			"Alguien añadió `profile_updated_at = now()` a un UPDATE que no es SetProfile "+
			"(o la version volvió a derivarse de updated_at)", tp1.Version, tp2.Version)
	}

	// Y el cambio de perfil SÍ la mueve: si no, este test pasaría con una columna
	// congelada, que es el otro modo de estar roto.
	activarPerfil(t, repo, tenantID, "sess-2", "el que sí debe mover")
	tp3 := leerPerfiles(t, repo, tenantID, "tras SetProfile")
	if tp3.Version <= tp2.Version {
		t.Fatalf("un cambio de perfil NO movió la version: %d -> %d", tp2.Version, tp3.Version)
	}
}

// escribirRuidoEnPostgres ejecuta contra Postgres real TODAS las escrituras que mueven
// `updated_at` y que NO deben mover la version. Es la gemela de escribirRuidoEnMemoria
// (fleet_test.go): si nace una escritura nueva sobre fleet_sessions, va en LAS DOS.
//
// La reconexión va primero a propósito: es el caso de campo que disparaba el bug —
// MarkOnline corre inmediatamente antes de pushConfigsOnConnect.
func escribirRuidoEnPostgres(t *testing.T, repo fleet.Repository, tenantID string) {
	t.Helper()
	ctx := context.Background()
	for _, s := range []string{"sess-1", "sess-2"} {
		if err := repo.MarkOnline(ctx, tenantID, "edge-1", s); err != nil {
			t.Fatalf("reconexión %s: %v", s, err)
		}
	}
	if err := repo.SaveHealth(ctx, tenantID, "edge-1", "sess-1", fleet.HealthSnapshot{}); err != nil {
		t.Fatalf("SaveHealth: %v", err)
	}
	if err := repo.SetSelfPn(ctx, tenantID, "edge-1", "sess-1", "+34600000000"); err != nil {
		t.Fatalf("SetSelfPn: %v", err)
	}
	if _, err := repo.SetState(ctx, tenantID, "sess-2", fleet.StateOffline); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := repo.MarkOffline(ctx, tenantID, "edge-1", "sess-1"); err != nil {
		t.Fatalf("MarkOffline: %v", err)
	}
}

// TestIntegration_ProfilesByTenant_TenantSinSesiones: un tenant sin ni una fila NO es
// un error — devuelve mapa VACÍO (nunca nil) y version 0, y el provider lo empuja
// igual (regla 2 de T2.1).
func TestIntegration_ProfilesByTenant_TenantSinSesiones(t *testing.T) {
	db := openTestDB(t)
	repo, _ := repoDePrueba(t, db)

	tp, err := repo.ProfilesByTenant(context.Background(), seedTenant(t, db))
	if err != nil {
		t.Fatalf("ProfilesByTenant: %v", err)
	}
	if tp.Sessions == nil {
		t.Fatal("Sessions es nil: debe ser un mapa vacío (el payload manda {} y no null)")
	}
	if len(tp.Sessions) != 0 || tp.Version != 0 {
		t.Fatalf("tenant sin sesiones: %+v", tp)
	}
}

// TestIntegration_ProfilesByTenant_FilasDiscordantes_GanaPassive. SetProfile escribe
// TODAS las filas de la sesión a la vez, así que discrepar no debería ocurrir; si
// ocurriera (un UPDATE a mano, un restore parcial), la lectura segura es `passive`:
// ante la duda, la sesión NO auto-responde y sus entrantes se filtran.
func TestIntegration_ProfilesByTenant_FilasDiscordantes_GanaPassive(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo, _ := repoDePrueba(t, db)

	for _, edge := range []string{"edge-a", "edge-b"} {
		if err := repo.MarkOnline(ctx, tenantID, edge, "sess-1"); err != nil {
			t.Fatalf("MarkOnline %s: %v", edge, err)
		}
	}
	if found, err := repo.SetProfile(ctx, tenantID, "sess-1", fleet.ProfileActive); err != nil || !found {
		t.Fatalf("SetProfile: found=%v err=%v", found, err)
	}
	// Se rompe la coherencia A MANO (no hay API que lo permita): solo una de las dos
	// filas vuelve a passive.
	if _, err := db.ExecContext(ctx, `
		UPDATE public.fleet_sessions SET profile = 'passive'
		WHERE tenant_id = $1 AND edge_id = 'edge-b' AND session_id = 'sess-1'
	`, tenantID); err != nil {
		t.Fatalf("desincronizar filas: %v", err)
	}

	tp, err := repo.ProfilesByTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("ProfilesByTenant: %v", err)
	}
	if tp.Sessions["sess-1"] != fleet.ProfilePassive {
		t.Fatalf("sess-1 = %q con filas discordantes, quiero passive (lectura segura)", tp.Sessions["sess-1"])
	}
}
