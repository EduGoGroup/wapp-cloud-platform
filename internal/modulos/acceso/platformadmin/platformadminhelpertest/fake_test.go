package platformadminhelpertest_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin/platformadminhelpertest"
)

var (
	_ platformadmin.TenantStore            = (*platformadminhelpertest.Fake)(nil)
	_ platformadmin.AccessRequestStore     = (*platformadminhelpertest.Fake)(nil)
	_ platformadminhelpertest.State        = (*platformadminhelpertest.Fake)(nil)
	_ func() *platformadminhelpertest.Fake = platformadminhelpertest.NewFake
)

// Los roles del Montaje del Fake: los mismos ids y nombres que las plantillas globales operator y
// viewer de la migración 0015, para que la suite vea lo mismo que con Postgres.
var (
	roleOperator = platformadminhelpertest.Role{ID: "10000000-0000-0000-0000-000000000002", Name: "operator"}
	roleViewer   = platformadminhelpertest.Role{ID: "10000000-0000-0000-0000-000000000003", Name: "viewer"}
)

// newMontaje arma un Fake con los dos roles y dos empresas recién creadas por el puerto.
func newMontaje(t *testing.T) platformadminhelpertest.Montaje {
	t.Helper()
	_, m := newFake(t)
	return m
}

// newFake es newMontaje devolviendo también el Fake, para los tests propios del doble.
func newFake(t *testing.T) (*platformadminhelpertest.Fake, platformadminhelpertest.Montaje) {
	t.Helper()
	f := platformadminhelpertest.NewFake()
	f.AddRole(roleOperator.ID, roleOperator.Name)
	f.AddRole(roleViewer.ID, roleViewer.Name)
	a := mustCreate(t, f, "tenant-a")
	b := mustCreate(t, f, "tenant-b")
	return f, platformadminhelpertest.Montaje{
		Tenants: f, Requests: f, State: f,
		TenantA: a, TenantB: b, MissingTenant: uuid.NewString(),
		RoleA: roleOperator, RoleB: roleViewer,
	}
}

// pending devuelve las solicitudes pendientes y falla el test si no puede.
func pending(ctx context.Context, t *testing.T, f *platformadminhelpertest.Fake) []platformadmin.AccessRequestItem {
	t.Helper()
	items, err := f.ListAccessRequests(ctx, "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	return items
}

func mustCreate(t *testing.T, f *platformadminhelpertest.Fake, slug string) string {
	t.Helper()
	created, err := f.CreateTenant(context.Background(), slug, "Empresa "+slug, nil)
	if err != nil {
		t.Fatalf("CreateTenant(%s): %v", slug, err)
	}
	return created.ID
}

// El Fake cumple la suite de los dos puertos (la misma que corre Repository contra Postgres).
func TestFake_Contrato(t *testing.T) {
	platformadminhelpertest.Contrato(t, newMontaje)
}

// Fail hace fallar SOLO el método nombrado, sin tocar el estado, y con nil deja de fallar.
func TestFake_Fail(t *testing.T) {
	f, m := newFake(t)
	boom := errors.New("caído")
	f.Fail("ListTenants", boom)
	if items, err := f.ListTenants(context.Background(), 10, 0); !errors.Is(err, boom) || items != nil {
		t.Fatalf("ListTenants con Fail = (%v, %v), quiero (nil, boom)", items, err)
	}
	if ok, err := f.ExistsTenant(context.Background(), m.TenantA); err != nil || !ok {
		t.Fatalf("Fail(ListTenants) no puede afectar a ExistsTenant: (%v, %v)", ok, err)
	}
	f.Fail("CreateAccessRequest", boom)
	user := uuid.NewString()
	if err := f.CreateAccessRequest(context.Background(), user, "a@x.com", "bff"); !errors.Is(err, boom) {
		t.Fatalf("CreateAccessRequest con Fail = %v, quiero boom", err)
	}
	f.Fail("CreateAccessRequest", nil)
	if err := f.CreateAccessRequest(context.Background(), user, "a@x.com", "bff"); err != nil {
		t.Fatalf("tras Fail(nil) tiene que volver a funcionar: %v", err)
	}
	items, err := f.ListAccessRequests(context.Background(), "pending")
	if err != nil || len(items) != 1 {
		t.Fatalf("el fallo no escribió nada y el reintento escribió una: %+v, %v", items, err)
	}
	f.Fail("ListTenants", nil)
	if _, err := f.ListTenants(context.Background(), 10, 0); err != nil {
		t.Fatalf("tras Fail(nil) ListTenants tiene que volver a funcionar: %v", err)
	}
}

// GrantMultiCompany abre la regla de una sola empresa SOLO en la empresa de destino.
func TestFake_GrantMultiCompany(t *testing.T) {
	f, m := newFake(t)
	ctx := context.Background()
	user := uuid.NewString()
	approve := func(tenant string) error {
		if err := f.CreateAccessRequest(ctx, user, "a@x.com", "bff"); err != nil {
			t.Fatalf("CreateAccessRequest: %v", err)
		}
		items := pending(ctx, t, f)
		return f.ExecuteApprovalTx(ctx, items[0].ID, tenant, user, roleOperator.ID, uuid.NewString())
	}
	if err := approve(m.TenantA); err != nil {
		t.Fatalf("primera empresa: %v", err)
	}
	f.GrantMultiCompany(m.TenantA) // la de ORIGEN no cuenta
	if err := approve(m.TenantB); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("multi_empresa en la de origen no abre la de destino: %v", err)
	}
	f.GrantMultiCompany(m.TenantB)
	items := pending(ctx, t, f)
	if err := f.ExecuteApprovalTx(ctx, items[0].ID, m.TenantB, user, roleViewer.ID, uuid.NewString()); err != nil {
		t.Fatalf("con multi_empresa en la de destino tiene que entrar: %v", err)
	}
	if member, roles := f.Access(t, user, m.TenantB); !member || !slices.Equal(roles, []string{roleViewer.ID}) {
		t.Fatalf("acceso a B = (%v, %v)", member, roles)
	}
}

// ExecuteApprovalTx hacia una empresa o un rol que no existen falla sin sentinela (en Postgres,
// la clave foránea) y sin escribir nada.
func TestFake_ExecuteApprovalTx_MissingTenantOrRole(t *testing.T) {
	f, m := newFake(t)
	ctx := context.Background()
	user := uuid.NewString()
	if err := f.CreateAccessRequest(ctx, user, "a@x.com", "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	items := pending(ctx, t, f)
	for _, c := range []struct{ name, tenant, role string }{
		{"MissingTenant", m.MissingTenant, roleOperator.ID},
		{"MissingRole", m.TenantA, uuid.NewString()},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := f.ExecuteApprovalTx(ctx, items[0].ID, c.tenant, user, c.role, uuid.NewString())
			if err == nil || errors.Is(err, platformadmin.ErrConflict) || errors.Is(err, platformadmin.ErrNotFound) {
				t.Fatalf("err = %v, quiero un error de clave foránea, no un centinela", err)
			}
			if row := f.Request(t, items[0].ID); row.Status != "pending" {
				t.Fatalf("el fallo escribió el status: %q", row.Status)
			}
		})
	}
}

// Lo que el Fake devuelve son copias: tocarlas no cambia su estado.
func TestFake_ReturnsCopies(t *testing.T) {
	f, _ := newFake(t)
	ctx := context.Background()
	plan := "pro"
	created, err := f.CreateTenant(ctx, "con-plan", "Con plan", &plan)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	plan = "cambiado"
	f.SetFeatures(created.ID, "b", "a", "b")
	d, err := f.GetTenant(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if d.PlanID == nil || *d.PlanID != "pro" {
		t.Fatalf("el plan guardado cambió con el puntero del llamante: %v", d.PlanID)
	}
	if !slices.Equal(d.Features, []string{"a", "b"}) {
		t.Fatalf("Features = %v, quiero [a b] (ordenadas y sin repetir)", d.Features)
	}
	*d.PlanID = "tocado"
	d.Features[0] = "tocado"
	again, err := f.GetTenant(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if *again.PlanID != "pro" || again.Features[0] != "a" {
		t.Fatalf("tocar lo devuelto cambió el estado: %+v", again)
	}
	user := uuid.NewString()
	if err := f.CreateAccessRequest(ctx, user, "a@x.com", "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	items := pending(ctx, t, f)
	if err := f.RejectAccessRequest(ctx, items[0].ID, "motivo", uuid.NewString()); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
	row := f.Request(t, items[0].ID)
	*row.Reason = "tocado"
	if again := f.Request(t, items[0].ID); *again.Reason != "motivo" {
		t.Fatalf("tocar la fila devuelta cambió el estado: %q", *again.Reason)
	}
}

// El Fake es seguro en paralelo: altas y lecturas concurrentes (lo vigila -race).
func TestFake_Concurrent(t *testing.T) {
	f, _ := newFake(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.CreateAccessRequest(ctx, uuid.NewString(), "a@x.com", "edge"); err != nil {
				t.Errorf("CreateAccessRequest %d: %v", i, err)
			}
			if _, err := f.ListTenants(ctx, 0, 0); err != nil {
				t.Errorf("ListTenants %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if items := pending(ctx, t, f); len(items) != 16 {
		t.Fatalf("hay %d solicitudes, quiero 16", len(items))
	}
}
