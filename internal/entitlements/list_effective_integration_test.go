package entitlements_test

import (
	"context"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
)

// TestIntegration_ListEffective ejercita la consulta de ListEffective contra
// PostgreSQL real (Plan 040 · T2.2): plan efectivo, composición sembrada por la
// migración 0039, y las DOS direcciones del override — el que enciende una
// feature fuera del plan y, sobre todo, el que APAGA una que el plan sí trae y
// debe quedar EXCLUIDA de la lista (no basta con que Has devuelva false).
//
// Va contra BD real y no contra un mock porque lo que se verifica es el SQL: el
// anti-join del override es precisamente lo que un stub no probaría. openTestDB
// hace skip si no hay DSN (mismo mecanismo que el resto de integración).
func TestIntegration_ListEffective(t *testing.T) {
	db := openTestDB(t)

	// Resolver nuevo por caso: la caché por tenant no debe teñir la resolución.
	newResolver := func() entitlements.Resolver {
		return entitlements.NewPostgres(db, entitlements.WithTTL(shortTTL))
	}
	list := func(t *testing.T, tenantID string) (string, []string) {
		t.Helper()
		plan, features, err := newResolver().ListEffective(context.Background(), tenantID)
		if err != nil {
			t.Fatalf("ListEffective(%s): %v", tenantID, err)
		}
		return plan, features
	}

	t.Run("plan NULL ⇒ basic con las features del paquete Básico", func(t *testing.T) {
		tid := seedTenant(t, db)
		plan, features := list(t, tid)
		if plan != "basic" {
			t.Fatalf("plan = %q, quería basic (plan_id NULL)", plan)
		}
		want := []string{"cart_basic", "intakes_export", "menu"}
		if !slices.Equal(features, want) {
			t.Fatalf("features = %v, quería %v (0039, orden alfabético)", features, want)
		}
	})

	t.Run("plan advisor_ai lista su composición completa", func(t *testing.T) {
		tid := seedTenant(t, db)
		mustExec(t, db, `UPDATE public.tenants SET plan_id='advisor_ai' WHERE id=$1`, tid)
		plan, features := list(t, tid)
		if plan != "advisor_ai" {
			t.Fatalf("plan = %q, quería advisor_ai", plan)
		}
		want := []string{"cart_basic", "catalog_import", "crm_bridge", "intakes_export", "llm_intent", "menu"}
		if !slices.Equal(features, want) {
			t.Fatalf("features = %v, quería %v", features, want)
		}
	})

	// EL caso que da nombre a la tarea: enabled=false EXCLUYE de la lista.
	t.Run("override enabled=false EXCLUYE la feature del plan", func(t *testing.T) {
		tid := seedTenant(t, db)
		mustExec(t, db, `UPDATE public.tenants SET plan_id='commerce' WHERE id=$1`, tid)
		mustExec(t, db, `INSERT INTO public.tenant_features (tenant_id, feature, enabled) VALUES ($1,$2,false)`,
			tid, "menu")

		plan, features := list(t, tid)
		if plan != "commerce" {
			t.Fatalf("plan = %q, quería commerce (el override apaga features, no cambia el plan)", plan)
		}
		if slices.Contains(features, "menu") {
			t.Fatalf("features = %v: 'menu' está apagada por override y NO debe listarse", features)
		}
		want := []string{"cart_basic", "catalog_import", "crm_bridge", "intakes_export"}
		if !slices.Equal(features, want) {
			t.Fatalf("features = %v, quería %v", features, want)
		}
	})

	t.Run("override enabled=true SUMA una feature fuera del plan", func(t *testing.T) {
		tid := seedTenant(t, db)
		mustExec(t, db, `UPDATE public.tenants SET plan_id='basic' WHERE id=$1`, tid)
		mustExec(t, db, `INSERT INTO public.tenant_features (tenant_id, feature, enabled) VALUES ($1,$2,true)`,
			tid, "stt_audio")

		_, features := list(t, tid)
		want := []string{"cart_basic", "intakes_export", "menu", "stt_audio"}
		if !slices.Equal(features, want) {
			t.Fatalf("features = %v, quería %v (basic ∪ override)", features, want)
		}
	})

	t.Run("una feature encendida por override NO se duplica", func(t *testing.T) {
		tid := seedTenant(t, db)
		mustExec(t, db, `UPDATE public.tenants SET plan_id='basic' WHERE id=$1`, tid)
		// 'menu' YA viene en basic: el override redundante no debe duplicar la fila.
		mustExec(t, db, `INSERT INTO public.tenant_features (tenant_id, feature, enabled) VALUES ($1,$2,true)`,
			tid, "menu")

		_, features := list(t, tid)
		want := []string{"cart_basic", "intakes_export", "menu"}
		if !slices.Equal(features, want) {
			t.Fatalf("features = %v, quería %v (sin duplicados)", features, want)
		}
	})

	t.Run("ListEffective y Has coinciden feature a feature", func(t *testing.T) {
		tid := seedTenant(t, db)
		mustExec(t, db, `UPDATE public.tenants SET plan_id='advisor_ai' WHERE id=$1`, tid)
		mustExec(t, db, `INSERT INTO public.tenant_features (tenant_id, feature, enabled) VALUES ($1,$2,false)`,
			tid, "llm_intent")

		res := newResolver()
		_, features, err := res.ListEffective(context.Background(), tid)
		if err != nil {
			t.Fatalf("ListEffective: %v", err)
		}
		// Lo listado tiene que dar true en Has; lo apagado, false. Es la garantía
		// de que la UI no promete una capacidad que el gate luego niega.
		for _, f := range features {
			assertFeature(t, res, tid, f, true)
		}
		assertFeature(t, res, tid, "llm_intent", false)
		if slices.Contains(features, "llm_intent") {
			t.Fatalf("features = %v: llm_intent está apagada por override", features)
		}
	})
}
