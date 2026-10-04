//go:build pendiente

package platformadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

// Este test es EXTERNO (package platformadmin_test): usa el doble de platformadminhelpertest,
// que importa platformadmin, y un test interno que lo importara daría un ciclo. Las ayudas
// comunes del paquete viven en handlers_test.go, el primer test que pasó a verde (hallazgo 20);
// aquí, las de la bandeja y el doble de identity (fakeM2M), que también usa signup_test.go.

// ── ayudas de la bandeja de solicitudes ──────────────────────────────────────────────────

// pendingRequest crea una solicitud pendiente de user y devuelve su id.
func (b inbox) pendingRequest(t *testing.T, user string) string {
	t.Helper()
	ctx := context.Background()
	if err := b.f.CreateAccessRequest(ctx, user, "ana@x.com", "bff"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	items, err := b.f.ListAccessRequests(ctx, "pending")
	if err != nil {
		t.Fatalf("ListAccessRequests: %v", err)
	}
	for _, it := range items {
		if it.UserID == user {
			return it.ID
		}
	}
	t.Fatalf("no encuentro la solicitud pendiente de %s", user)
	return ""
}

// fakeM2M es el doble de out.IdentityM2MClient de los tests del paquete: programable y con
// registro de llamadas. Seguro en paralelo.
type fakeM2M struct {
	mu sync.Mutex
	// current es lo que devuelve GetUserSystems; getErr, su fallo.
	current  []string
	getErr   error
	getCalls int
	// replaced registra cada conjunto declarado con ReplaceUserSystems; replaceErr, su fallo.
	replaced   [][]string
	replaceErr error
	// signupUserID es el id que devuelve Signup; signupErr, su fallo; signupEmails, los correos
	// con que se llamó.
	signupUserID string
	signupErr    error
	signupEmails []string
	ensureCalls  int
}

var _ out.IdentityM2MClient = (*fakeM2M)(nil)

func (m *fakeM2M) EnsureUser(_ context.Context, email, _, _ string) (domain.IdentityUser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureCalls++
	return domain.IdentityUser{ID: uuid.NewString(), Email: email, Created: true}, nil
}

func (m *fakeM2M) GetUserSystems(_ context.Context, _ string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.current, nil
}

func (m *fakeM2M) ReplaceUserSystems(_ context.Context, _ string, systems []string) (domain.IdentitySystemsDiff, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replaced = append(m.replaced, slices.Clone(systems))
	if m.replaceErr != nil {
		return domain.IdentitySystemsDiff{}, m.replaceErr
	}
	return domain.IdentitySystemsDiff{Systems: systems}, nil
}

func (m *fakeM2M) Signup(_ context.Context, email, _, _, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signupEmails = append(m.signupEmails, email)
	if m.signupErr != nil {
		return "", m.signupErr
	}
	return m.signupUserID, nil
}

// jsonBody codifica v como cuerpo de petición.
func jsonBody(t *testing.T, v any) *strings.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return strings.NewReader(string(b))
}

// Los seis centinelas de la bandeja conservan su texto, byte a byte (diseño F2 §5): el de
// ErrSystemsSyncFailed viaja tal cual en el cuerpo del 502 (ApprovePartialResult.Reason).
func TestAccessRequestSentinels_LiteralTexts(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"ErrPlatformSystemForbidden", platformadmin.ErrPlatformSystemForbidden,
			"platformadmin: wapp.platform no se concede desde la bandeja de solicitudes de acceso"},
		{"ErrSystemsUnionUnavailable", platformadmin.ErrSystemsUnionUnavailable,
			"platformadmin: no se puede unir con los systems actuales del usuario en identity (sin lectura)"},
		{"ErrIdentityM2MUnavailable", platformadmin.ErrIdentityM2MUnavailable,
			"platformadmin: no hay cliente M2M configurado hacia identity; no se pudieron conceder los systems solicitados"},
		{"ErrRetryRoleMismatch", platformadmin.ErrRetryRoleMismatch,
			"platformadmin: el reintento pide un rol distinto del ya aprobado la primera vez; no converge"},
		{"ErrTenantNotFound", platformadmin.ErrTenantNotFound,
			"platformadmin: el tenant_id de la aprobación no existe"},
		{"ErrSystemsSyncFailed", platformadmin.ErrSystemsSyncFailed,
			"platformadmin: fallo al sincronizar systems en identity tras aprobar localmente"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.err.Error() != c.want {
				t.Fatalf("%s = %q, quiero %q", c.name, c.err.Error(), c.want)
			}
		})
	}
}

// Los tags JSON de los DTO de la bandeja son contrato con la consola de plataforma.
func TestAccessRequestDTOs_JSONShape(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	item := platformadmin.AccessRequestItem{
		ID: "r1", UserID: "u1", Email: "ana@x.com", Origin: "bff", Status: "pending", CreatedAt: at,
		Systems: []string{}, SystemsKnown: false,
	}
	const itemJSON = `{"id":"r1","user_id":"u1","email":"ana@x.com","origin":"bff","status":"pending",` +
		`"created_at":"2026-10-04T12:00:00Z","systems":[],"systems_known":false}`
	for _, c := range []struct {
		name string
		v    any
		want string
	}{
		{"AccessRequestItem", item, itemJSON},
		{"ListAccessRequestsResponse", platformadmin.ListAccessRequestsResponse{Items: []platformadmin.AccessRequestItem{item}},
			`{"items":[` + itemJSON + `]}`},
		{"ApprovePartialResult", platformadmin.ApprovePartialResult{Local: "ok", Identity: "failed", Reason: "r"},
			`{"local":"ok","identity":"failed","reason":"r"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := json.Marshal(c.v)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(got) != c.want {
				t.Fatalf("JSON = %s\nquiero  %s", got, c.want)
			}
		})
	}
}

// Los cuerpos de aprobar y rechazar se leen con los nombres de campo de la consola.
func TestAccessRequestBodies_JSONFieldNames(t *testing.T) {
	var approve platformadmin.ApproveAccessRequestRequest
	if err := json.Unmarshal([]byte(`{"tenant_id":"t1","role":"operator","systems":["wapp.bff"]}`), &approve); err != nil {
		t.Fatalf("json.Unmarshal(approve): %v", err)
	}
	if approve.TenantID != "t1" || approve.Role != "operator" || len(approve.Systems) != 1 || approve.Systems[0] != "wapp.bff" {
		t.Fatalf("ApproveAccessRequestRequest = %+v", approve)
	}
	var reject platformadmin.RejectAccessRequestRequest
	if err := json.Unmarshal([]byte(`{"reason":"duplicada"}`), &reject); err != nil {
		t.Fatalf("json.Unmarshal(reject): %v", err)
	}
	if reject.Reason != "duplicada" {
		t.Fatalf("RejectAccessRequestRequest = %+v", reject)
	}
}

// ── ApproveAccessRequest ─────────────────────────────────────────────────────────────────────

// approve llama a ApproveAccessRequest sobre la bandeja con el operador de los tests.
func (b inbox) approve(requestID, tenantID, role string, systems []string, m2m out.IdentityM2MClient) error {
	return platformadmin.ApproveAccessRequest(context.Background(), b.f, b.f, requestID, tenantID, role, operatorSubject, systems, m2m)
}

// wantApprovedLocally exige que lo local quedó escrito: solicitud aprobada por el operador y la
// persona miembro de la empresa con ese rol (y solo ese).
func (b inbox) wantApprovedLocally(t *testing.T, requestID, user, tenant, roleID string) {
	t.Helper()
	row := b.f.Request(t, requestID)
	if row.Status != "approved" || row.DecidedBy == nil || *row.DecidedBy != operatorSubject {
		t.Fatalf("solicitud = %+v, quiero approved por %s", row, operatorSubject)
	}
	member, roles := b.f.Access(t, user, tenant)
	if !member || !slices.Equal(roles, []string{roleID}) {
		t.Fatalf("acceso = (%v, %v), quiero (true, [%s])", member, roles, roleID)
	}
}

// wantUntouched exige que la solicitud siga pendiente y la persona sin acceso a la empresa.
func (b inbox) wantUntouched(t *testing.T, requestID, user, tenant string) {
	t.Helper()
	if row := b.f.Request(t, requestID); row.Status != "pending" || row.DecidedAt != nil {
		t.Fatalf("la solicitud se tocó: %+v", row)
	}
	if member, roles := b.f.Access(t, user, tenant); member || len(roles) != 0 {
		t.Fatalf("la persona recibió acceso: (%v, %v)", member, roles)
	}
}

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
	if got := current[:3]; got[0] != "wapp.edge" || got[1] != "wapp.platform" || got[2] != "" {
		t.Fatalf("el arreglo de identity se modificó: %q", got)
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

// ── handlers ─────────────────────────────────────────────────────────────────────────────────

const (
	listPattern    = "GET /admin/access-requests"
	approvePattern = "POST /admin/access-requests/{id}/approve"
	rejectPattern  = "POST /admin/access-requests/{id}/reject"
)

// R-A1: los tres handlers de la bandeja cortan con EnforcePlatformCaller ANTES de tocar el
// almacén: 401 sin identidad (o sin tenant), 403 con un tenant que no es el de plataforma.
func TestAccessRequestHandlers_PlatformFence_BeforeTheStore(t *testing.T) {
	b := newInbox(t)
	failAll(b.f)
	id := uuid.NewString()
	handlers := []struct {
		name, pattern, method, path string
		h                           http.Handler
	}{
		{"List", listPattern, http.MethodGet, "/admin/access-requests", platformadmin.ListAccessRequestsHandler(b.f, platformTenant)},
		{"Approve", approvePattern, http.MethodPost, "/admin/access-requests/" + id + "/approve",
			platformadmin.ApproveAccessRequestHandler(b.f, b.f, &fakeM2M{}, platformTenant)},
		{"Reject", rejectPattern, http.MethodPost, "/admin/access-requests/" + id + "/reject",
			platformadmin.RejectAccessRequestHandler(b.f, platformTenant)},
	}
	for _, h := range handlers {
		for _, c := range []struct {
			name, tenant string
			code         int
		}{
			{"NoIdentity", "", http.StatusUnauthorized},
			{"OtherTenant", b.tenantA, http.StatusForbidden},
		} {
			t.Run(h.name+"_"+c.name, func(t *testing.T) {
				body := `{"tenant_id":"` + b.tenantA + `","role":"operator","reason":"x"}`
				r := asCaller(httptest.NewRequest(h.method, h.path, strings.NewReader(body)), c.tenant)
				rec, _ := serve(t, h.pattern, h.h, r)
				if rec.Code != c.code {
					t.Fatalf("status = %d, quiero %d (y sin tocar el almacén)", rec.Code, c.code)
				}
			})
		}
	}
}

// R-A2: un {id} vacío es 400 y uno que no es UUID es 404, sin consultar el almacén.
func TestAccessRequestHandlers_RequestID_EmptyIs400_NotUUIDIs404(t *testing.T) {
	b := newInbox(t)
	failAll(b.f)
	approve := platformadmin.ApproveAccessRequestHandler(b.f, b.f, &fakeM2M{}, platformTenant)
	reject := platformadmin.RejectAccessRequestHandler(b.f, platformTenant)
	body := `{"tenant_id":"` + b.tenantA + `","role":"operator","reason":"x"}`
	for _, c := range []struct {
		name, pattern, path string
		h                   http.Handler
	}{
		{"Approve", approvePattern, "/admin/access-requests/no-es-un-uuid/approve", approve},
		{"Reject", rejectPattern, "/admin/access-requests/no-es-un-uuid/reject", reject},
	} {
		t.Run(c.name+"_NotUUID", func(t *testing.T) {
			r := asCaller(httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(body)), platformTenant)
			rec, _ := serve(t, c.pattern, c.h, r)
			wantText(t, rec, http.StatusNotFound, "solicitud no encontrada")
		})
		t.Run(c.name+"_Empty", func(t *testing.T) {
			// Sin ServeMux no hay {id}: PathValue devuelve "".
			r := asCaller(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), platformTenant)
			rec := httptest.NewRecorder()
			c.h.ServeHTTP(rec, r)
			wantText(t, rec, http.StatusBadRequest, "id de solicitud requerido")
		})
	}
}

// El listado: status por defecto pending, ?status= filtra, items nunca null y el fallo del
// almacén es un 500 con su texto.
func TestListAccessRequestsHandler(t *testing.T) {
	b := newInbox(t)
	h := platformadmin.ListAccessRequestsHandler(b.f, platformTenant)
	get := func(path string) *httptest.ResponseRecorder {
		rec, _ := serve(t, listPattern, h, asCaller(httptest.NewRequest(http.MethodGet, path, nil), platformTenant))
		return rec
	}
	wantJSON(t, get("/admin/access-requests"), http.StatusOK, `{"items":[]}`)

	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	var resp platformadmin.ListAccessRequestsResponse
	rec := get("/admin/access-requests")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("respuesta = %d %s (%v)", rec.Code, rec.Body.String(), err)
	}
	if len(resp.Items) != 1 || resp.Items[0].ID != id || resp.Items[0].Status != "pending" {
		t.Fatalf("items = %+v, quiero la pendiente %s", resp.Items, id)
	}
	wantJSON(t, get("/admin/access-requests?status=rejected"), http.StatusOK, `{"items":[]}`)

	b.f.Fail("ListAccessRequests", errors.New("bd caída"))
	wantText(t, get("/admin/access-requests"), http.StatusInternalServerError, "error al listar solicitudes de acceso")
}

// El cuerpo de aprobar: JSON inválido y campos que faltan son 400, sin tocar el almacén.
func TestApproveAccessRequestHandler_Body(t *testing.T) {
	b := newInbox(t)
	failAll(b.f)
	h := platformadmin.ApproveAccessRequestHandler(b.f, b.f, &fakeM2M{}, platformTenant)
	path := "/admin/access-requests/" + uuid.NewString() + "/approve"
	for _, c := range []struct{ name, body, text string }{
		{"NotJSON", `{`, "cuerpo JSON inválido"},
		{"NoTenant", `{"role":"operator"}`, "tenant_id y role son requeridos"},
		{"NoRole", `{"tenant_id":"` + b.tenantA + `"}`, "tenant_id y role son requeridos"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := asCaller(httptest.NewRequest(http.MethodPost, path, strings.NewReader(c.body)), platformTenant)
			rec, _ := serve(t, approvePattern, h, r)
			wantText(t, rec, http.StatusBadRequest, c.text)
		})
	}
}

// approveSetup es el escenario de un desenlace del handler de aprobar.
type approveSetup struct {
	b      inbox
	id     string
	tenant string
	role   string
	extra  map[string]any
	m2m    out.IdentityM2MClient
}

// Cada desenlace de ApproveAccessRequest tiene su código y su cuerpo, literales, y el handler
// publica el tenant_id del cuerpo como tenant objetivo de la auditoría.
func TestApproveAccessRequestHandler_Outcomes(t *testing.T) {
	for _, c := range []struct {
		name    string
		arrange func(t *testing.T, s *approveSetup)
		code    int
		text    string // http.Error
		json    string // writeJSON
	}{
		{"Approved_204", func(*testing.T, *approveSetup) {}, http.StatusNoContent, "", ""},
		{"MissingRequest_404", func(_ *testing.T, s *approveSetup) { s.id = uuid.NewString() },
			http.StatusNotFound, "solicitud no encontrada", ""},
		{"MissingTenant_404", func(_ *testing.T, s *approveSetup) { s.tenant = uuid.NewString() },
			http.StatusNotFound, "empresa no encontrada", ""},
		{"Rejected_409", func(t *testing.T, s *approveSetup) {
			if err := s.b.f.RejectAccessRequest(context.Background(), s.id, "no", operatorSubject); err != nil {
				t.Fatalf("RejectAccessRequest: %v", err)
			}
		}, http.StatusConflict, "la solicitud ya fue resuelta o la persona ya pertenece a otra empresa", ""},
		{"UnknownRole_400", func(_ *testing.T, s *approveSetup) { s.role = "no_such_role" },
			http.StatusBadRequest, "datos de solicitud o rol inválidos", ""},
		{"PlatformSystem_400", func(_ *testing.T, s *approveSetup) {
			s.extra = map[string]any{"systems": []string{"wapp.platform"}}
		}, http.StatusBadRequest, "wapp.platform no se concede desde la bandeja de solicitudes de acceso", ""},
		{"RetryAnotherRole_409", func(t *testing.T, s *approveSetup) {
			if err := platformadmin.ApproveAccessRequest(context.Background(), s.b.f, s.b.f, s.id, s.tenant, "viewer", operatorSubject, nil, nil); err != nil {
				t.Fatalf("primera aprobación: %v", err)
			}
		}, http.StatusConflict, "la solicitud ya fue aprobada con un rol distinto; el reintento no converge", ""},
		{"UnionUnavailable_409", func(_ *testing.T, s *approveSetup) {
			s.extra = map[string]any{"systems": []string{"wapp.bff"}}
			s.m2m = &fakeM2M{getErr: errors.New("x")}
		}, http.StatusConflict, "", `{"local":"ok","identity":"skipped","reason":"no se pudo leer el conjunto actual de systems del usuario en identity; para no reemplazarlo por accidente no se tocó nada en identity"}`},
		{"SyncFailed_502", func(_ *testing.T, s *approveSetup) {
			s.extra = map[string]any{"systems": []string{"wapp.bff"}}
			s.m2m = &fakeM2M{replaceErr: errors.New("identity 503")}
		}, http.StatusBadGateway, "", `{"local":"ok","identity":"failed","reason":"platformadmin: fallo al sincronizar systems en identity tras aprobar localmente: identity 503"}`},
		{"NoM2M_503", func(_ *testing.T, s *approveSetup) {
			s.extra = map[string]any{"systems": []string{"wapp.bff"}}
			s.m2m = nil
		}, http.StatusServiceUnavailable, "", `{"local":"ok","identity":"skipped","reason":"no hay cliente M2M configurado hacia identity en este despliegue; lo local (empresa y rol) quedó escrito pero los systems solicitados NO se concedieron"}`},
		{"StoreFails_500", func(_ *testing.T, s *approveSetup) { s.b.f.Fail("LookupAccessRequestStatus", errors.New("bd caída")) },
			http.StatusInternalServerError, "error al aprobar solicitud", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newInbox(t)
			s := approveSetup{b: b, id: b.pendingRequest(t, uuid.NewString()), tenant: b.tenantA, role: "operator", m2m: &fakeM2M{}}
			c.arrange(t, &s)
			body := map[string]any{"tenant_id": s.tenant, "role": s.role}
			for k, v := range s.extra {
				body[k] = v
			}
			h := platformadmin.ApproveAccessRequestHandler(s.b.f, s.b.f, s.m2m, platformTenant)
			r := asCaller(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+s.id+"/approve", jsonBody(t, body)), platformTenant)
			rec, audit := serve(t, approvePattern, h, r)
			switch {
			case c.json != "":
				wantJSON(t, rec, c.code, c.json)
			case c.text != "":
				wantText(t, rec, c.code, c.text)
			case rec.Code != c.code || rec.Body.Len() != 0:
				t.Fatalf("respuesta = %d %q, quiero %d sin cuerpo", rec.Code, rec.Body.String(), c.code)
			}
			if got := audit.target(t); got != s.tenant {
				t.Fatalf("tenant objetivo auditado = %q, quiero el tenant_id del cuerpo %q", got, s.tenant)
			}
		})
	}
}

// Aprobar por HTTP guarda como decided_by el Subject de la identidad del operador.
func TestApproveAccessRequestHandler_OperatorIsTheSubject(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	h := platformadmin.ApproveAccessRequestHandler(b.f, b.f, nil, platformTenant)
	body := `{"tenant_id":"` + b.tenantA + `","role":"operator"}`
	r := asCaller(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+id+"/approve", strings.NewReader(body)), platformTenant)
	if rec, _ := serve(t, approvePattern, h, r); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	b.wantApprovedLocally(t, id, user, b.tenantA, roleOperator.ID)
}

// postReject manda POST …/reject con ese cuerpo (sin cuerpo si body es "") como operador de
// plataforma.
func postReject(t *testing.T, b inbox, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+id+"/reject", nil)
	if body != "" {
		r = httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+id+"/reject", strings.NewReader(body))
	}
	rec, _ := serve(t, rejectPattern, platformadmin.RejectAccessRequestHandler(b.f, platformTenant), asCaller(r, platformTenant))
	return rec
}

// Rechazar: motivo obligatorio (R-A6), cuerpo opcional pero JSON si viene, y cada desenlace con
// su código y su texto.
func TestRejectAccessRequestHandler(t *testing.T) {
	t.Run("Rejected_204_ReasonAndOperatorSaved", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		if rec := postReject(t, b, id, `{"reason":"no es cliente"}`); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("respuesta = %d %q, quiero 204", rec.Code, rec.Body.String())
		}
		row := b.f.Request(t, id)
		if row.Status != "rejected" || row.Reason == nil || *row.Reason != "no es cliente" || row.DecidedBy == nil || *row.DecidedBy != operatorSubject {
			t.Fatalf("fila = %+v", row)
		}
	})
	t.Run("NoBody_400_ReasonRequired", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		wantText(t, postReject(t, b, id, ""), http.StatusBadRequest, "entrada inválida")
		if row := b.f.Request(t, id); row.Status != "pending" {
			t.Fatalf("sin motivo no se rechaza: %+v", row)
		}
	})
	t.Run("BlankReason_400", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		wantText(t, postReject(t, b, id, `{"reason":"   "}`), http.StatusBadRequest, "entrada inválida")
	})
	t.Run("NotJSON_400", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		wantText(t, postReject(t, b, id, `{`), http.StatusBadRequest, "cuerpo JSON inválido")
	})
	t.Run("Missing_404", func(t *testing.T) {
		b := newInbox(t)
		wantText(t, postReject(t, b, uuid.NewString(), `{"reason":"x"}`), http.StatusNotFound, "solicitud no encontrada")
	})
	t.Run("AlreadyDecided_409", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		if rec := postReject(t, b, id, `{"reason":"uno"}`); rec.Code != http.StatusNoContent {
			t.Fatalf("primer rechazo: %d", rec.Code)
		}
		wantText(t, postReject(t, b, id, `{"reason":"dos"}`), http.StatusConflict, "la solicitud ya fue resuelta")
	})
	t.Run("StoreFails_500", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		b.f.Fail("RejectAccessRequest", errors.New("bd caída"))
		wantText(t, postReject(t, b, id, `{"reason":"x"}`), http.StatusInternalServerError, "error al rechazar solicitud")
	})
}
