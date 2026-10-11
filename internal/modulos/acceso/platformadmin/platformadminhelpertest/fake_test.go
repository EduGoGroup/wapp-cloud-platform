package platformadminhelpertest_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
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

// Fail vale para CADA método de los dos puertos, no solo para los dos de TestFake_Fail: el método
// nombrado devuelve el error con sus demás resultados a cero, y el estado queda como estaba. Los
// consumidores prueban sus ramas de error sobre esto: una clave mal escrita en un método haría
// que su test de fallo ejercitara, sin avisar, el camino feliz.
func TestFake_Fail_EveryPortMethod(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("caído")
	cases := []struct {
		method string
		// call devuelve si los demás resultados salieron a cero, y el error.
		call func(f *platformadminhelpertest.Fake, m platformadminhelpertest.Montaje, requestID, user string) (bool, error)
	}{
		{"GetTenant", func(f *platformadminhelpertest.Fake, m platformadminhelpertest.Montaje, _, _ string) (bool, error) {
			d, err := f.GetTenant(ctx, m.TenantA)
			return d.ID == "" && d.Features == nil, err
		}},
		{"ExistsTenant", func(f *platformadminhelpertest.Fake, m platformadminhelpertest.Montaje, _, _ string) (bool, error) {
			ok, err := f.ExistsTenant(ctx, m.TenantA)
			return !ok, err
		}},
		{"CreateTenant", func(f *platformadminhelpertest.Fake, _ platformadminhelpertest.Montaje, _, _ string) (bool, error) {
			created, err := f.CreateTenant(ctx, "tenant-c", "Empresa C", nil)
			return created == platformadmin.CreatedTenant{}, err
		}},
		{"ListInstallations", func(f *platformadminhelpertest.Fake, m platformadminhelpertest.Montaje, _, _ string) (bool, error) {
			items, err := f.ListInstallations(ctx, m.TenantA)
			return items == nil, err
		}},
		{"ListAccessRequests", func(f *platformadminhelpertest.Fake, _ platformadminhelpertest.Montaje, _, _ string) (bool, error) {
			items, err := f.ListAccessRequests(ctx, "pending")
			return items == nil, err
		}},
		{"RejectAccessRequest", func(f *platformadminhelpertest.Fake, _ platformadminhelpertest.Montaje, requestID, _ string) (bool, error) {
			return true, f.RejectAccessRequest(ctx, requestID, "motivo", uuid.NewString())
		}},
		{"LookupAccessRequestStatus", func(f *platformadminhelpertest.Fake, _ platformadminhelpertest.Montaje, requestID, _ string) (bool, error) {
			userID, status, err := f.LookupAccessRequestStatus(ctx, requestID)
			return userID == "" && status == "", err
		}},
		{"ResolveRoleID", func(f *platformadminhelpertest.Fake, _ platformadminhelpertest.Montaje, _, _ string) (bool, error) {
			id, err := f.ResolveRoleID(ctx, roleOperator.Name)
			return id == "", err
		}},
		{"CheckRetryApproved", func(f *platformadminhelpertest.Fake, m platformadminhelpertest.Montaje, _, user string) (bool, error) {
			return true, f.CheckRetryApproved(ctx, user, m.TenantB, roleViewer.ID)
		}},
		{"ExecuteApprovalTx", func(f *platformadminhelpertest.Fake, m platformadminhelpertest.Montaje, requestID, user string) (bool, error) {
			return true, f.ExecuteApprovalTx(ctx, requestID, m.TenantA, user, roleOperator.ID, uuid.NewString())
		}},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			f, m := newFake(t)
			// user ya es miembro de B con el rol viewer (sin Fail, CheckRetryApproved daría nil), y
			// A admite a quien ya está en otra empresa (sin Fail, ExecuteApprovalTx daría nil).
			user := uuid.NewString()
			f.SeedMembership(t, user, m.TenantB, roleViewer.ID)
			f.GrantMultiCompany(m.TenantA)
			if err := f.CreateAccessRequest(ctx, user, "a@x.com", "bff"); err != nil {
				t.Fatalf("CreateAccessRequest: %v", err)
			}
			requestID := pending(ctx, t, f)[0].ID

			f.Fail(c.method, boom)
			zero, err := c.call(f, m, requestID, user)
			if !errors.Is(err, boom) || !zero {
				t.Fatalf("%s con Fail = (cero: %v, %v), quiero resultados a cero y boom", c.method, zero, err)
			}
			if row := f.Request(t, requestID); row.Status != "pending" || row.DecidedAt != nil {
				t.Errorf("el fallo tocó la solicitud: %+v", row)
			}
			if member, roles := f.Access(t, user, m.TenantA); member || len(roles) != 0 {
				t.Errorf("el fallo dio acceso: (%v, %v)", member, roles)
			}
			f.Fail(c.method, nil)
			if tenants, lerr := f.ListTenants(ctx, 0, 0); lerr != nil || len(tenants) != 2 {
				t.Errorf("el fallo creó o perdió empresas: %d, %v", len(tenants), lerr)
			}
			if _, err := c.call(f, m, requestID, user); err != nil {
				t.Fatalf("tras Fail(nil) %s tiene que volver a funcionar: %v", c.method, err)
			}
		})
	}
}

// ListTenants acota el límite superior como lo promete el puerto (limit > 500 ⇒ 500). La suite
// del contrato lo deja fuera a propósito —501 empresas por caso contra Postgres no compensan—;
// en memoria es barato, y es el Fake quien tiene que contestar igual que el adaptador.
func TestFake_ListTenants_LimitAbove500IsClampedTo500(t *testing.T) {
	f, _ := newFake(t)
	ctx := context.Background()
	for i := range 499 {
		mustCreate(t, f, "extra-"+strconv.Itoa(i))
	}
	for _, limit := range []int{500, 501, 100000} {
		items, err := f.ListTenants(ctx, limit, 0)
		if err != nil || len(items) != 500 {
			t.Fatalf("ListTenants(limit=%d) con 501 empresas = %d filas, %v; quiero 500", limit, len(items), err)
		}
	}
	if items, err := f.ListTenants(ctx, 100000, 500); err != nil || len(items) != 1 {
		t.Fatalf("la segunda página tiene %d filas, %v; quiero la 501ª", len(items), err)
	}
}
