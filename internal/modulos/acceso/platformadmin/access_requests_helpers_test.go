package platformadmin_test

// Parte de access_requests_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): el doble de M2M y los helpers de la bandeja.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
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
