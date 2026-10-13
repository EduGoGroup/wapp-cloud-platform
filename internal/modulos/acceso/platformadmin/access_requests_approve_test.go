package platformadmin_test

// Parte de access_requests_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): las reglas de ApproveAccessRequest (aprobación, unión de systems y reintento).

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

// Paso 1: requestID, tenantID o role vacíos ⇒ ErrInvalidInput SIN tocar los almacenes.
func TestApproveAccessRequest_EmptyArguments_InvalidInputWithoutTouchingTheStore(t *testing.T) {
	b := newInbox(t)
	failAll(b.f)
	m2m := &fakeM2M{}
	for _, c := range []struct{ name, request, tenant, role string }{
		{"EmptyRequestID", "", b.tenantA, "operator"},
		{"EmptyTenantID", uuid.NewString(), "", "operator"},
		{"EmptyRole", uuid.NewString(), b.tenantA, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := b.approve(c.request, c.tenant, c.role, []string{"wapp.bff"}, m2m); !errors.Is(err, platformadmin.ErrInvalidInput) {
				t.Fatalf("err = %v, quiero ErrInvalidInput sin tocar el almacén", err)
			}
		})
	}
	if m2m.getCalls != 0 || len(m2m.replaced) != 0 {
		t.Fatal("una aprobación inválida no habla con identity")
	}
}

// Paso 2 (R-A6): wapp.platform no se concede desde la bandeja, ni solo ni acompañado, y se
// rechaza ANTES de tocar nada.
func TestApproveAccessRequest_PlatformSystem_ForbiddenWithoutTouchingAnything(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	failAll(b.f)
	m2m := &fakeM2M{}
	for _, systems := range [][]string{{"wapp.platform"}, {"wapp.bff", "wapp.platform"}} {
		if err := b.approve(id, b.tenantA, "operator", systems, m2m); !errors.Is(err, platformadmin.ErrPlatformSystemForbidden) {
			t.Fatalf("systems %v: err = %v, quiero ErrPlatformSystemForbidden", systems, err)
		}
	}
	if m2m.getCalls != 0 || len(m2m.replaced) != 0 {
		t.Fatal("wapp.platform se rechaza sin hablar con identity")
	}
	for _, m := range storeMethods {
		b.f.Fail(m, nil)
	}
	b.wantUntouched(t, id, user, b.tenantA)
}

// Paso 3: la solicitud tiene que existir.
func TestApproveAccessRequest_MissingRequest_NotFound(t *testing.T) {
	b := newInbox(t)
	if err := b.approve(uuid.NewString(), b.tenantA, "operator", nil, nil); !errors.Is(err, platformadmin.ErrNotFound) {
		t.Fatalf("err = %v, quiero ErrNotFound", err)
	}
}

// Paso 4: un rol que no existe ⇒ ErrInvalidInput, sin escribir nada.
func TestApproveAccessRequest_UnknownRole_InvalidInputNothingWritten(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	if err := b.approve(id, b.tenantA, "no_such_role", nil, nil); !errors.Is(err, platformadmin.ErrInvalidInput) {
		t.Fatalf("err = %v, quiero ErrInvalidInput", err)
	}
	b.wantUntouched(t, id, user, b.tenantA)
}

// Paso 5, pending (R-A2 del cuerpo): una empresa que no existe ⇒ ErrTenantNotFound, sin escribir.
func TestApproveAccessRequest_MissingTenant_TenantNotFoundNothingWritten(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	missing := uuid.NewString()
	if err := b.approve(id, missing, "operator", nil, nil); !errors.Is(err, platformadmin.ErrTenantNotFound) {
		t.Fatalf("err = %v, quiero ErrTenantNotFound", err)
	}
	b.wantUntouched(t, id, user, missing)
}

// Paso 5, pending: un fallo al comprobar la empresa sale envuelto con su prefijo, y no es un 404.
func TestApproveAccessRequest_ExistsTenantFails_WrappedNotTenantNotFound(t *testing.T) {
	b := newInbox(t)
	id := b.pendingRequest(t, uuid.NewString())
	boom := errors.New("bd caída")
	b.f.Fail("ExistsTenant", boom)
	err := b.approve(id, b.tenantA, "operator", nil, nil)
	if !errors.Is(err, boom) || errors.Is(err, platformadmin.ErrTenantNotFound) {
		t.Fatalf("err = %v, quiero el fallo envuelto y no ErrTenantNotFound", err)
	}
	if !strings.HasPrefix(err.Error(), "platformadmin: comprobar existencia de tenant: ") {
		t.Fatalf("texto = %q, quiero el prefijo «platformadmin: comprobar existencia de tenant: »", err.Error())
	}
}

// Paso 5, pending: el rol se resuelve por nombre O por id; sin systems, m2m nil no es error
// (R-A5: nada que conceder ⇒ 204).
func TestApproveAccessRequest_Pending_WritesLocally_NoSystemsNoM2M(t *testing.T) {
	for _, role := range []string{roleOperator.Name, roleOperator.ID} {
		t.Run(role, func(t *testing.T) {
			b := newInbox(t)
			user := uuid.NewString()
			id := b.pendingRequest(t, user)
			if err := b.approve(id, b.tenantA, role, nil, nil); err != nil {
				t.Fatalf("err = %v, quiero nil", err)
			}
			b.wantApprovedLocally(t, id, user, b.tenantA, roleOperator.ID)
		})
	}
}

// Paso 5, pending: hacia una segunda empresa sin multi_empresa ⇒ ErrConflict, sin escribir y sin
// hablar con identity.
func TestApproveAccessRequest_SecondCompany_ConflictWithoutIdentity(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	first := b.pendingRequest(t, user)
	if err := b.approve(first, b.tenantA, "operator", nil, nil); err != nil {
		t.Fatalf("primera aprobación: %v", err)
	}
	second := b.pendingRequest(t, user)
	m2m := &fakeM2M{}
	if err := b.approve(second, b.tenantB, "operator", []string{"wapp.bff"}, m2m); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("err = %v, quiero ErrConflict", err)
	}
	b.wantUntouched(t, second, user, b.tenantB)
	if m2m.getCalls != 0 || len(m2m.replaced) != 0 {
		t.Fatal("un conflicto local no habla con identity")
	}
}

// Paso 5: una solicitud rechazada no se aprueba (ErrConflict) y no se habla con identity.
func TestApproveAccessRequest_Rejected_Conflict(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	if err := b.f.RejectAccessRequest(context.Background(), id, "no", operatorSubject); err != nil {
		t.Fatalf("RejectAccessRequest: %v", err)
	}
	m2m := &fakeM2M{}
	if err := b.approve(id, b.tenantA, "operator", []string{"wapp.bff"}, m2m); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("err = %v, quiero ErrConflict", err)
	}
	if m2m.getCalls != 0 || len(m2m.replaced) != 0 {
		t.Fatal("una solicitud rechazada no habla con identity")
	}
	if member, _ := b.f.Access(t, user, b.tenantA); member {
		t.Fatal("aprobar una rechazada dio acceso")
	}
}

// Paso 6 (R-A5, 1.1): con systems y sin cliente M2M ⇒ ErrIdentityM2MUnavailable, con lo local
// escrito igual.
func TestApproveAccessRequest_SystemsWithoutM2M_UnavailableLocalWritten(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	if err := b.approve(id, b.tenantA, "operator", []string{"wapp.bff"}, nil); !errors.Is(err, platformadmin.ErrIdentityM2MUnavailable) {
		t.Fatalf("err = %v, quiero ErrIdentityM2MUnavailable", err)
	}
	b.wantApprovedLocally(t, id, user, b.tenantA, roleOperator.ID)
}

// Paso 6 (R-A5, D): se declara la UNIÓN de lo vigente (en el orden de identity) y lo pedido, sin
// repetir; leyendo una vez y escribiendo una vez. El arreglo que devolvió identity no se toca,
// tampoco más allá de su longitud.
func TestApproveAccessRequest_DeclaresTheUnion(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	current := make([]string, 2, 8) // capacidad de sobra: un append en sitio se vería aquí
	current[0], current[1] = "wapp.edge", "wapp.platform"
	m2m := &fakeM2M{current: current}
	if err := b.approve(id, b.tenantA, "operator", []string{"wapp.bff", "wapp.edge", "wapp.bff"}, m2m); err != nil {
		t.Fatalf("err = %v, quiero nil", err)
	}
	if m2m.getCalls != 1 {
		t.Fatalf("GetUserSystems se llamó %d veces, quiero 1 (leer antes de declarar)", m2m.getCalls)
	}
	want := []string{"wapp.edge", "wapp.platform", "wapp.bff"}
	if len(m2m.replaced) != 1 || !slices.Equal(m2m.replaced[0], want) {
		t.Fatalf("declarado %v, quiero UNA vez %v (unión; wapp.platform se conserva, no se concede)", m2m.replaced, want)
	}
	// Se mira hasta la capacidad: un append en sitio no cambia la longitud del de identity, pero
	// escribe en su arreglo de respaldo.
	backing := current[:cap(current)]
	if !slices.Equal(backing, []string{"wapp.edge", "wapp.platform", "", "", "", "", "", ""}) {
		t.Fatalf("el arreglo de identity se modificó: %q", backing)
	}
	b.wantApprovedLocally(t, id, user, b.tenantA, roleOperator.ID)
}

// Paso 6: si la unión no añade nada, identity no recibe un PUT que no cambia nada.
func TestApproveAccessRequest_NothingToAdd_NoWrite(t *testing.T) {
	b := newInbox(t)
	id := b.pendingRequest(t, uuid.NewString())
	m2m := &fakeM2M{current: []string{"wapp.bff", "wapp.edge"}}
	if err := b.approve(id, b.tenantA, "operator", []string{"wapp.edge"}, m2m); err != nil {
		t.Fatalf("err = %v, quiero nil", err)
	}
	if m2m.getCalls != 1 || len(m2m.replaced) != 0 {
		t.Fatalf("lecturas %d, escrituras %v; quiero 1 y ninguna", m2m.getCalls, m2m.replaced)
	}
}

// Paso 6 (R-A5): sin poder LEER no se declara NADA (ErrSystemsUnionUnavailable, que envuelve el
// fallo de la lectura), y lo local queda escrito.
func TestApproveAccessRequest_ReadFails_UnionUnavailableNothingDeclared(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	readErr := errors.New("identity no contesta")
	m2m := &fakeM2M{getErr: readErr}
	err := b.approve(id, b.tenantA, "operator", []string{"wapp.bff"}, m2m)
	if !errors.Is(err, platformadmin.ErrSystemsUnionUnavailable) || !errors.Is(err, readErr) {
		t.Fatalf("err = %v, quiero ErrSystemsUnionUnavailable envolviendo el fallo de la lectura", err)
	}
	if len(m2m.replaced) != 0 {
		t.Fatalf("sin leer se declaró %v", m2m.replaced)
	}
	b.wantApprovedLocally(t, id, user, b.tenantA, roleOperator.ID)
}

// Paso 6 (C-04): si la escritura en identity falla ⇒ ErrSystemsSyncFailed envolviendo el fallo,
// con lo local escrito.
func TestApproveAccessRequest_WriteFails_SyncFailedLocalWritten(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	writeErr := errors.New("identity 503")
	m2m := &fakeM2M{replaceErr: writeErr}
	err := b.approve(id, b.tenantA, "operator", []string{"wapp.bff"}, m2m)
	if !errors.Is(err, platformadmin.ErrSystemsSyncFailed) || !errors.Is(err, writeErr) {
		t.Fatalf("err = %v, quiero ErrSystemsSyncFailed envolviendo el fallo de identity", err)
	}
	b.wantApprovedLocally(t, id, user, b.tenantA, roleOperator.ID)
}

// R-A5 (C-04): el reintento tras un fallo de identity CONVERGE: no vuelve a escribir lo local y
// solo reintenta la mitad que falló.
func TestApproveAccessRequest_RetryAfterIdentityFailure_Converges(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	failing := &fakeM2M{replaceErr: errors.New("identity caído")}
	if err := b.approve(id, b.tenantA, "operator", []string{"wapp.bff"}, failing); !errors.Is(err, platformadmin.ErrSystemsSyncFailed) {
		t.Fatalf("primera pasada: %v", err)
	}
	before := b.f.Request(t, id)
	b.f.Fail("ExecuteApprovalTx", errStoreTouched) // el reintento no puede volver a escribir lo local
	m2m := &fakeM2M{}
	if err := b.approve(id, b.tenantA, roleOperator.ID, []string{"wapp.bff"}, m2m); err != nil {
		t.Fatalf("el reintento tiene que converger: %v", err)
	}
	if len(m2m.replaced) != 1 || !slices.Equal(m2m.replaced[0], []string{"wapp.bff"}) {
		t.Fatalf("el reintento declaró %v, quiero [[wapp.bff]]", m2m.replaced)
	}
	if after := b.f.Request(t, id); !after.DecidedAt.Equal(*before.DecidedAt) {
		t.Fatal("el reintento volvió a resolver la solicitud")
	}
	b.wantApprovedLocally(t, id, user, b.tenantA, roleOperator.ID)
}

// R-A6 (1.2): un reintento que pide OTRO rol no converge: ErrRetryRoleMismatch, sin tocar el rol
// ni hablar con identity.
func TestApproveAccessRequest_RetryWithAnotherRole_MismatchNothingTouched(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	if err := b.approve(id, b.tenantA, "operator", nil, nil); err != nil {
		t.Fatalf("primera aprobación: %v", err)
	}
	m2m := &fakeM2M{}
	if err := b.approve(id, b.tenantA, "viewer", []string{"wapp.bff"}, m2m); !errors.Is(err, platformadmin.ErrRetryRoleMismatch) {
		t.Fatalf("err = %v, quiero ErrRetryRoleMismatch", err)
	}
	if m2m.getCalls != 0 || len(m2m.replaced) != 0 {
		t.Fatal("un reintento que no converge no habla con identity")
	}
	b.wantApprovedLocally(t, id, user, b.tenantA, roleOperator.ID)
	// El rol se resuelve ANTES de bifurcar: un rol que no existe tampoco converge.
	if err := b.approve(id, b.tenantA, "no_such_role", nil, nil); !errors.Is(err, platformadmin.ErrInvalidInput) {
		t.Fatalf("reintento con un rol inexistente = %v, quiero ErrInvalidInput", err)
	}
}

// Un reintento hacia OTRA empresa no converge: ErrConflict.
func TestApproveAccessRequest_RetryToAnotherTenant_Conflict(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	if err := b.approve(id, b.tenantA, "operator", nil, nil); err != nil {
		t.Fatalf("primera aprobación: %v", err)
	}
	if err := b.approve(id, b.tenantB, "operator", nil, &fakeM2M{}); !errors.Is(err, platformadmin.ErrConflict) {
		t.Fatalf("err = %v, quiero ErrConflict", err)
	}
}
