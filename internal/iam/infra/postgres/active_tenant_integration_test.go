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

// TestIntegration_UserTenants_SinMembresiasEsListaVaciaYNoNula. La no-nulidad
// importa hasta aquí: el transporte serializa lo que este método devuelva, y un
// nil se convierte en `null`, que el cliente no puede recorrer.
func TestIntegration_UserTenants_SinMembresiasEsListaVaciaYNoNula(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)

	vacia, err := iampostgres.NewMembershipRepo(env.db, nil).UserTenants(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("UserTenants sin membresías: %v (cero empresas no es un fallo)", err)
	}
	if vacia == nil {
		t.Error("la lista vacía llegó como nil: se serializaría como `null`")
	}
	if len(vacia) != 0 {
		t.Fatalf("devolvió %+v, quiero vacío", vacia)
	}
}

// TestIntegration_UserTenants_SoloLasSuyasYConNombre ejercita el JOIN contra la
// tabla real. Es lo que el doble en memoria NO puede probar: que `display_name`
// sale de verdad de public.tenants, que el JOIN no es ambiguo (las DOS tablas
// tienen `created_at`, así que un ORDER BY sin cualificar ni compila en Postgres)
// y —lo que importa— que una empresa AJENA no asoma.
func TestIntegration_UserTenants_SoloLasSuyasYConNombre(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewMembershipRepo(env.db, nil)
	userID, otro := uuid.NewString(), uuid.NewString()

	// Dos empresas suyas —con nombres distintos y reconocibles— y UNA AJENA, de
	// otra persona. Sin la ajena, el aserto de «solo las suyas» vigilaría una
	// pared.
	suyaA := env.seedTenantNombrado(t, "Panadería Doña Rosa")
	suyaB := env.seedTenantNombrado(t, "Catering del Sur")
	ajena := env.seedTenantNombrado(t, "EMPRESA AJENA")
	env.seedMembresia(t, userID, suyaA)
	env.seedMembresia(t, userID, suyaB)
	env.seedMembresia(t, otro, ajena)

	tenants, err := repo.UserTenants(ctx, userID)
	if err != nil {
		t.Fatalf("UserTenants: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("devolvió %d empresas, quiero 2: %+v", len(tenants), tenants)
	}
	for _, tn := range tenants {
		if tn.ID == ajena {
			t.Fatalf("se coló una empresa AJENA en el listado: %+v", tenants)
		}
	}
	// Los nombres, contra los que se sembraron: si el JOIN emparejara mal, esto
	// saldría cruzado y un aserto de «no vacío» no lo notaría.
	porID := map[string]string{tenants[0].ID: tenants[0].DisplayName, tenants[1].ID: tenants[1].DisplayName}
	if porID[suyaA] != "Panadería Doña Rosa" || porID[suyaB] != "Catering del Sur" {
		t.Fatalf("los nombres no corresponden a sus empresas: %+v", tenants)
	}

	// Y la otra persona ve LA SUYA y solo la suya: la simetría prueba que el
	// filtro es por user_id y no un recorte casual.
	deOtro, err := repo.UserTenants(ctx, otro)
	if err != nil {
		t.Fatalf("UserTenants (otro): %v", err)
	}
	if len(deOtro) != 1 || deOtro[0].ID != ajena {
		t.Fatalf("la otra persona vio %+v, quiero solo %q", deOtro, ajena)
	}
}

// TestIntegration_UserTenants_MismoOrdenQueTenantsOfUser: los dos métodos
// describen la misma lista, así que tienen que devolverla igual ordenada. Que el
// selector la pinte en un orden y el canje razone sobre otro es un defecto
// latente que solo se ve el día que alguien los compara.
func TestIntegration_UserTenants_MismoOrdenQueTenantsOfUser(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewMembershipRepo(env.db, nil)
	userID := uuid.NewString()
	for _, nombre := range []string{"Uno", "Dos", "Tres"} {
		env.seedMembresia(t, userID, env.seedTenantNombrado(t, nombre))
	}

	tenants, err := repo.UserTenants(ctx, userID)
	if err != nil {
		t.Fatalf("UserTenants: %v", err)
	}
	ids, err := repo.TenantsOfUser(ctx, userID)
	if err != nil {
		t.Fatalf("TenantsOfUser: %v", err)
	}
	if len(ids) != len(tenants) {
		t.Fatalf("TenantsOfUser devolvió %d y UserTenants %d", len(ids), len(tenants))
	}
	if len(ids) != 3 {
		t.Fatalf("se sembraron 3 membresías y salieron %d: la comparación no probaría nada", len(ids))
	}
	for i := range ids {
		if ids[i] != tenants[i].ID {
			t.Fatalf("los dos listados difieren en el orden: %v vs %+v", ids, tenants)
		}
	}
}

// seedMembresia da de alta la pareja (userID, tenantID) directamente en la tabla.
func (e itEnv) seedMembresia(t *testing.T, userID, tenantID string) {
	t.Helper()
	if _, err := e.db.ExecContext(context.Background(),
		`INSERT INTO public.tenant_members (user_id, tenant_id) VALUES ($1, $2)`, userID, tenantID); err != nil {
		t.Fatalf("sembrar membresía: %v", err)
	}
}

// seedTenantNombrado crea un tenant con el display_name dado.
func (e itEnv) seedTenantNombrado(t *testing.T, nombre string) string {
	t.Helper()
	var id string
	slug := "iam-it-lista-" + uuid.NewString()
	if err := e.db.QueryRowContext(context.Background(), `
		INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2)
		RETURNING id::text`, slug, nombre).Scan(&id); err != nil {
		t.Fatalf("sembrar tenant %q: %v", nombre, err)
	}
	return id
}
