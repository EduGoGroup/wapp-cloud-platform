//go:build pendiente

package grpc

// El contrato de auth.go por su cara exportada: las dos opciones que inyectan el puerto de
// autenticación y el auditor. Lo que el gateway HACE con ellos (login, refresh y logout en
// banda, R-G9; quién firma cada evento, R-G10; dos Edge a la vez, R3.4.a) no tiene cara
// exportada hasta que exista Connect: se afirma por dentro, sobre lo que llamará el carril,
// en auth_inband_test.go, auth_audit_test.go y auth_multiedge_test.go, que nacen con el verde.

import (
	"context"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// scriptedAuth es un in.Authenticator de respuestas programadas que apunta lo que le piden.
// Sin función programada, cada operación devuelve su cero sin error.
type scriptedAuth struct {
	login   func(in.LoginInput) (domain.AuthResult, error)
	refresh func(in.RefreshInput) (domain.AuthResult, error)
	logout  func(in.LogoutInput) error

	mu       sync.Mutex
	logins   []in.LoginInput
	refreshs []in.RefreshInput
	logouts  []in.LogoutInput
}

var _ in.Authenticator = (*scriptedAuth)(nil)

func (a *scriptedAuth) Login(_ context.Context, req in.LoginInput) (domain.AuthResult, error) {
	a.mu.Lock()
	a.logins = append(a.logins, req)
	a.mu.Unlock()
	if a.login == nil {
		return domain.AuthResult{}, nil
	}
	return a.login(req)
}

func (a *scriptedAuth) Refresh(_ context.Context, req in.RefreshInput) (domain.AuthResult, error) {
	a.mu.Lock()
	a.refreshs = append(a.refreshs, req)
	a.mu.Unlock()
	if a.refresh == nil {
		return domain.AuthResult{}, nil
	}
	return a.refresh(req)
}

func (a *scriptedAuth) Logout(_ context.Context, req in.LogoutInput) error {
	a.mu.Lock()
	a.logouts = append(a.logouts, req)
	a.mu.Unlock()
	if a.logout == nil {
		return nil
	}
	return a.logout(req)
}

func (*scriptedAuth) Verify(context.Context, string) (in.VerifyResult, error) {
	return in.VerifyResult{}, nil
}

// calls dice cuántas veces se llamó al puerto, sumando las tres operaciones.
func (a *scriptedAuth) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.logins) + len(a.refreshs) + len(a.logouts)
}

// auditLog es un in.Auditor que guarda los eventos que recibe y puede fallar.
type auditLog struct {
	err error

	mu     sync.Mutex
	events []in.AuditInput
}

var _ in.Auditor = (*auditLog)(nil)

func (l *auditLog) Record(_ context.Context, ev in.AuditInput) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
	return l.err
}

func (*auditLog) ListAudit(context.Context, string, int, int) ([]domain.AuditEvent, error) {
	return nil, nil
}

func (l *auditLog) recorded() []in.AuditInput {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]in.AuditInput(nil), l.events...)
}

// WithAuthenticator es una Option que New acepta, con puerto o con nil.
func TestWithAuthenticatorIsAnOptionNewAccepts(t *testing.T) {
	t.Parallel()
	for name, authn := range map[string]in.Authenticator{"with authenticator": &scriptedAuth{}, "nil authenticator": nil} {
		opt := WithAuthenticator(authn)
		if opt == nil {
			t.Fatalf("%s: WithAuthenticator devolvió una Option nil", name)
		}
		if srv := New(session.NewRegistry(), quietLog(), opt); srv == nil {
			t.Fatalf("%s: New devolvió nil", name)
		}
	}
}

// WithAuthAuditor es una Option que New acepta, con auditor o con nil.
func TestWithAuthAuditorIsAnOptionNewAccepts(t *testing.T) {
	t.Parallel()
	for name, auditor := range map[string]in.Auditor{"with auditor": &auditLog{}, "nil auditor": nil} {
		opt := WithAuthAuditor(auditor)
		if opt == nil {
			t.Fatalf("%s: WithAuthAuditor devolvió una Option nil", name)
		}
		if srv := New(session.NewRegistry(), quietLog(), opt); srv == nil {
			t.Fatalf("%s: New devolvió nil", name)
		}
	}
}
