package entitlements_test

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
)

// shortTTL es el TTL con el que se construyen los Resolvers de este archivo: la
// caché por (tenant, feature) NO debe influir en lo que se está midiendo (la
// RESOLUCIÓN), y ningún caso puede quedar a merced del TTL real (60 s). Además
// cada subtest usa su propio tenant y su propio Resolver, así que no hay entrada
// de caché compartida entre casos.
const shortTTL = time.Millisecond

// TestIntegration_PlanTaxonomy_Resolucion ejercita la resolución de ADR-0022
// sobre la TAXONOMÍA sembrada por la migración 0039 (Plan 040 · T1.3): una
// feature del plan enciende; un override enabled=false apaga una que el plan sí
// trae; un override enabled=true enciende una que el plan NO trae; y una clave
// desconocida devuelve false sin error (REQ-13).
//
// Va contra PostgreSQL real (mismo mecanismo que postgres_integration_test.go:
// openTestDB migra la BD de test y hace skip si no hay DSN), porque lo que se
// verifica es precisamente el contenido sembrado por la migración: un mock del
// lookup no probaría nada del seed.
func TestIntegration_PlanTaxonomy_Resolucion(t *testing.T) {
	db := openTestDB(t)

	newResolver := func() entitlements.Resolver {
		return entitlements.NewPostgres(db, entitlements.WithTTL(shortTTL))
	}

	t.Run("feature del plan enciende (commerce ⇒ catalog_import)", func(t *testing.T) {
		tid := seedTenant(t, db)
		mustExec(t, db, `UPDATE public.tenants SET plan_id='commerce' WHERE id=$1`, tid)
		assertFeature(t, newResolver(), tid, "catalog_import", true)
	})

	t.Run("override enabled=false apaga una feature del plan (commerce ∖ menu)", func(t *testing.T) {
		tid := seedTenant(t, db)
		mustExec(t, db, `UPDATE public.tenants SET plan_id='commerce' WHERE id=$1`, tid)
		mustExec(t, db, `INSERT INTO public.tenant_features (tenant_id, feature, enabled) VALUES ($1,$2,false)`,
			tid, "menu")
		assertFeature(t, newResolver(), tid, "menu", false)
	})

	t.Run("override enabled=true enciende una feature fuera del plan (basic ∪ stt_audio)", func(t *testing.T) {
		tid := seedTenant(t, db)
		mustExec(t, db, `UPDATE public.tenants SET plan_id='basic' WHERE id=$1`, tid)
		// Sanity: sin el override, el plan basic NO trae stt_audio.
		assertFeature(t, newResolver(), tid, "stt_audio", false)
		mustExec(t, db, `INSERT INTO public.tenant_features (tenant_id, feature, enabled) VALUES ($1,$2,true)`,
			tid, "stt_audio")
		assertFeature(t, newResolver(), tid, "stt_audio", true)
	})

	t.Run("clave desconocida ⇒ false sin error", func(t *testing.T) {
		tid := seedTenant(t, db)
		// Plan con TODAS las features (laboratorio): ni así una clave inexistente
		// resuelve, y no es un error de infraestructura sino un false limpio.
		mustExec(t, db, `UPDATE public.tenants SET plan_id='pro' WHERE id=$1`, tid)
		assertFeature(t, newResolver(), tid, "feature_que_no_existe", false)
	})
}

// assertFeature comprueba Has(tenant, feature) contra el valor esperado y exige
// ausencia de error (el "no la tiene" es false, nil — nunca un error).
func assertFeature(t *testing.T, res entitlements.Resolver, tenantID, feature string, want bool) {
	t.Helper()
	got, err := res.Has(context.Background(), tenantID, feature)
	if err != nil {
		t.Fatalf("Has(%s, %q): error inesperado: %v", tenantID, feature, err)
	}
	if got != want {
		t.Fatalf("Has(%s, %q) = %v, quería %v", tenantID, feature, got, want)
	}
}
