package iampostgres_test

// active_tenant_integration_test.go — EL ADAPTADOR DE LA EMPRESA ACTIVA CONTRA
// POSTGRES DE VERDAD (Plan 047 · Ola 5 · T5.1, migración 0086).
//
// Lo que solo se puede comprobar aquí y no con el doble en memoria: que el
// UPSERT por `user_id` de verdad REEMPLAZA en vez de fallar con unique_violation
// —el doble usa un mapa y no puede fallar aunque el SQL estuviera mal—, y que la
// FK hacia `tenants` rechaza una empresa inexistente.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
)

// TestIntegration_EmpresaActiva_AusenciaAltaYReemplazo recorre el ciclo entero
// contra la tabla real.
func TestIntegration_EmpresaActiva_AusenciaAltaYReemplazo(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewActiveTenantRepo(env.db)
	userID := uuid.NewString()

	// (1) LA AUSENCIA NO ES UN ERROR. Es el estado normal de quien todavía no ha
	// elegido, y traducirla a un error obligaría al canje a distinguirla de un
	// fallo real — que es justo la confusión peligrosa.
	if _, ok, err := repo.ActiveTenantOf(ctx, userID); err != nil || ok {
		t.Fatalf("sin elección previa: ok=%v, err=%v; quiero ok=false y err=nil", ok, err)
	}

	// (2) El alta.
	if err := repo.SetActiveTenant(ctx, userID, env.tenantID); err != nil {
		t.Fatalf("SetActiveTenant: %v", err)
	}
	activo, ok, err := repo.ActiveTenantOf(ctx, userID)
	if err != nil || !ok || activo != env.tenantID {
		t.Fatalf("tras el alta: %q ok=%v err=%v, quiero %q", activo, ok, err, env.tenantID)
	}

	// (3) EL REEMPLAZO, que es donde un INSERT a secas moriría con
	// unique_violation y donde un DELETE+INSERT abriría una ventana sin fila.
	otro := env.seedOtroTenant(t)
	if err := repo.SetActiveTenant(ctx, userID, otro); err != nil {
		t.Fatalf("SetActiveTenant (reemplazo): %v (¿un INSERT sin ON CONFLICT?)", err)
	}
	activo, ok, err = repo.ActiveTenantOf(ctx, userID)
	if err != nil || !ok || activo != otro {
		t.Fatalf("tras el reemplazo: %q ok=%v err=%v, quiero %q", activo, ok, err, otro)
	}

	// Y no acumuló: una sola fila por persona (la PK lo garantiza, pero si
	// alguien la cambiara a compuesta esto lo diría antes que el canje).
	var filas int
	if err := env.db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.user_active_tenant WHERE user_id = $1`, userID).Scan(&filas); err != nil {
		t.Fatalf("contando filas: %v", err)
	}
	if filas != 1 {
		t.Fatalf("el usuario tiene %d filas de empresa activa, quiero 1: el reemplazo acumuló", filas)
	}

	// (4) LA FK MUERDE: una empresa que no existe no se puede fijar. Sin esta
	// mitad, la tabla aceptaría preferencias hacia la nada.
	if err := repo.SetActiveTenant(ctx, userID, uuid.NewString()); err == nil {
		t.Error("se guardó una empresa INEXISTENTE: la FK hacia tenants no está haciendo su trabajo")
	}
}

// seedOtroTenant crea un segundo tenant para el mismo test.
func (e itEnv) seedOtroTenant(t *testing.T) string {
	t.Helper()
	var id string
	slug := "iam-it-activa-" + uuid.NewString()
	if err := e.db.QueryRowContext(context.Background(), `
		INSERT INTO public.tenants (slug, display_name) VALUES ($1, 'IAM IT activa')
		RETURNING id::text`, slug).Scan(&id); err != nil {
		t.Fatalf("sembrar segundo tenant: %v", err)
	}
	return id
}
