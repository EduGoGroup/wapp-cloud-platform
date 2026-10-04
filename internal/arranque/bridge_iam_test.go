package arranque

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	viejodomain "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	viejoin "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/usecase"
)

// Los tests de este fichero se derivan de los comentarios de authenticatorBridge y auditorBridge
// (una aserción por promesa). Sin BD ni red: el adaptador se prueba sobre dobles de los puertos
// nuevos, y los constructores y un caso de punta a punta, sobre los servicios nuevos reales. El
// ctx marcado (markedCtx, ctxMarker) es el de bridge_contact_test.go, que muere después que este.

// fakeAuthenticator es el doble del in.Authenticator nuevo: captura lo que recibe y devuelve lo
// que se le dice.
type fakeAuthenticator struct {
	gotCtx     context.Context
	gotLogin   in.LoginInput
	gotRefresh in.RefreshInput
	gotLogout  in.LogoutInput
	gotToken   string

	result domain.AuthResult
	verify in.VerifyResult
	err    error
}

var _ in.Authenticator = (*fakeAuthenticator)(nil)

func (f *fakeAuthenticator) Login(ctx context.Context, req in.LoginInput) (domain.AuthResult, error) {
	f.gotCtx, f.gotLogin = ctx, req
	return f.result, f.err
}

func (f *fakeAuthenticator) Refresh(ctx context.Context, req in.RefreshInput) (domain.AuthResult, error) {
	f.gotCtx, f.gotRefresh = ctx, req
	return f.result, f.err
}

func (f *fakeAuthenticator) Logout(ctx context.Context, req in.LogoutInput) error {
	f.gotCtx, f.gotLogout = ctx, req
	return f.err
}

func (f *fakeAuthenticator) Verify(ctx context.Context, accessToken string) (in.VerifyResult, error) {
	f.gotCtx, f.gotToken = ctx, accessToken
	return f.verify, f.err
}

// fakeAuditor es el doble del in.Auditor nuevo.
type fakeAuditor struct {
	gotCtx      context.Context
	gotInput    in.AuditInput
	gotTenantID string
	gotLimit    int
	gotOffset   int

	events []domain.AuditEvent
	err    error
}

var _ in.Auditor = (*fakeAuditor)(nil)

func (f *fakeAuditor) Record(ctx context.Context, req in.AuditInput) error {
	f.gotCtx, f.gotInput = ctx, req
	return f.err
}

func (f *fakeAuditor) ListAudit(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error) {
	f.gotCtx, f.gotTenantID, f.gotLimit, f.gotOffset = ctx, tenantID, limit, offset
	return f.events, f.err
}

// iamFixedTime es un instante fijo: los tests no leen el reloj.
var iamFixedTime = time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)

// fullAuthResult rellena TODOS los campos del AuthResult nuevo con valores distintos entre sí.
func fullAuthResult() domain.AuthResult {
	return domain.AuthResult{
		AccessToken:  "ctx-token",
		RefreshToken: "refresh-token",
		TokenType:    "Bearer",
		ExpiresAt:    iamFixedTime,
		Context: domain.IdentityContext{
			TenantID: "tenant-1",
			UserID:   "user-1",
			Roles:    []string{"owner", "agent"},
		},
	}
}

// wantOldAuthResult es fullAuthResult escrito a mano en el tipo viejo.
func wantOldAuthResult() viejodomain.AuthResult {
	return viejodomain.AuthResult{
		AccessToken:  "ctx-token",
		RefreshToken: "refresh-token",
		TokenType:    "Bearer",
		ExpiresAt:    iamFixedTime,
		Context: viejodomain.IdentityContext{
			TenantID: "tenant-1",
			UserID:   "user-1",
			Roles:    []string{"owner", "agent"},
		},
	}
}

// assertMarked comprueba que el ctx que vio el servicio nuevo llevaba la marca de markedCtx.
func assertMarked(t *testing.T, marker any) {
	t.Helper()
	if marker != "marker" {
		t.Errorf("el ctx no llegó al servicio nuevo: marca = %v", marker)
	}
}

// Promesa: Login copia Email, Password y TenantID, delega con el mismo ctx y devuelve el
// AuthResult con todos sus campos; sin error, el error es nil.
func TestAuthenticatorBridge_Login_CopiesInputAndResult(t *testing.T) {
	next := &fakeAuthenticator{result: fullAuthResult()}
	b := &authenticatorBridge{next: next}

	got, err := b.Login(markedCtx(t), viejoin.LoginInput{Email: "a@b.co", Password: "s3cr3t", TenantID: "tenant-9"})
	if err != nil {
		t.Fatalf("Login devolvió error %v, se esperaba nil", err)
	}
	assertMarked(t, ctxMarker(next.gotCtx))
	if want := (in.LoginInput{Email: "a@b.co", Password: "s3cr3t", TenantID: "tenant-9"}); next.gotLogin != want {
		t.Errorf("LoginInput recibido = %+v, se esperaba %+v", next.gotLogin, want)
	}
	if !reflect.DeepEqual(got, wantOldAuthResult()) {
		t.Errorf("AuthResult = %+v, se esperaba %+v", got, wantOldAuthResult())
	}
}

// Promesa: Refresh copia RefreshToken, delega con el mismo ctx y devuelve el AuthResult entero.
func TestAuthenticatorBridge_Refresh_CopiesInputAndResult(t *testing.T) {
	next := &fakeAuthenticator{result: fullAuthResult()}
	b := &authenticatorBridge{next: next}

	got, err := b.Refresh(markedCtx(t), viejoin.RefreshInput{RefreshToken: "rt-1"})
	if err != nil {
		t.Fatalf("Refresh devolvió error %v, se esperaba nil", err)
	}
	assertMarked(t, ctxMarker(next.gotCtx))
	if next.gotRefresh.RefreshToken != "rt-1" {
		t.Errorf("RefreshToken recibido = %q, se esperaba %q", next.gotRefresh.RefreshToken, "rt-1")
	}
	if !reflect.DeepEqual(got, wantOldAuthResult()) {
		t.Errorf("AuthResult = %+v, se esperaba %+v", got, wantOldAuthResult())
	}
}

// Promesa: Logout copia RefreshToken, UserID y AllSessions y delega con el mismo ctx.
func TestAuthenticatorBridge_Logout_CopiesInput(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprintf("all_sessions_%v", all), func(t *testing.T) {
			next := &fakeAuthenticator{}
			b := &authenticatorBridge{next: next}

			if err := b.Logout(markedCtx(t), viejoin.LogoutInput{RefreshToken: "rt-2", UserID: "user-7", AllSessions: all}); err != nil {
				t.Fatalf("Logout devolvió error %v, se esperaba nil", err)
			}
			assertMarked(t, ctxMarker(next.gotCtx))
			if want := (in.LogoutInput{RefreshToken: "rt-2", UserID: "user-7", AllSessions: all}); next.gotLogout != want {
				t.Errorf("LogoutInput recibido = %+v, se esperaba %+v", next.gotLogout, want)
			}
		})
	}
}

// Promesa: Verify delega con el mismo ctx y token y devuelve el VerifyResult con todos sus campos.
func TestAuthenticatorBridge_Verify_CopiesResult(t *testing.T) {
	next := &fakeAuthenticator{verify: in.VerifyResult{
		Valid: true, TenantID: "tenant-1", Subject: "user-1", Roles: []string{"owner"}, ExpiresAt: iamFixedTime,
	}}
	b := &authenticatorBridge{next: next}

	got, err := b.Verify(markedCtx(t), "access-token")
	if err != nil {
		t.Fatalf("Verify devolvió error %v, se esperaba nil", err)
	}
	assertMarked(t, ctxMarker(next.gotCtx))
	if next.gotToken != "access-token" {
		t.Errorf("token recibido = %q, se esperaba %q", next.gotToken, "access-token")
	}
	want := viejoin.VerifyResult{Valid: true, TenantID: "tenant-1", Subject: "user-1", Roles: []string{"owner"}, ExpiresAt: iamFixedTime}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("VerifyResult = %+v, se esperaba %+v", got, want)
	}
}

// authCall ejecuta un método del adaptador y devuelve si el resultado que acompaña al error salió
// vacío, y el error.
type authCall struct {
	name string
	run  func(t *testing.T, b *authenticatorBridge) (zeroResult bool, err error)
}

var authCalls = []authCall{
	{"login", func(t *testing.T, b *authenticatorBridge) (bool, error) {
		res, err := b.Login(t.Context(), viejoin.LoginInput{Email: "a@b.co", Password: "x"})
		return reflect.DeepEqual(res, viejodomain.AuthResult{}), err
	}},
	{"refresh", func(t *testing.T, b *authenticatorBridge) (bool, error) {
		res, err := b.Refresh(t.Context(), viejoin.RefreshInput{RefreshToken: "rt"})
		return reflect.DeepEqual(res, viejodomain.AuthResult{}), err
	}},
	{"logout", func(t *testing.T, b *authenticatorBridge) (bool, error) {
		return true, b.Logout(t.Context(), viejoin.LogoutInput{RefreshToken: "rt"})
	}},
	{"verify", func(t *testing.T, b *authenticatorBridge) (bool, error) {
		res, err := b.Verify(t.Context(), "tok")
		return reflect.DeepEqual(res, viejoin.VerifyResult{}), err
	}},
}

// Promesa: los cuatro centinelas nuevos salen como un error que casa con el nuevo, con el viejo
// equivalente y con el original, con el texto del original byte a byte (también envuelto), y
// junto a un resultado vacío.
func TestAuthenticatorBridge_TranslatesSentinels(t *testing.T) {
	sentinels := []struct {
		name     string
		current  error
		old      error
		wantText string
	}{
		{"invalid_credentials", domain.ErrInvalidCredentials, viejodomain.ErrInvalidCredentials, "iam: credenciales inválidas"},
		{"user_inactive", domain.ErrUserInactive, viejodomain.ErrUserInactive, "iam: usuario inactivo"},
		{"refresh_invalid", domain.ErrRefreshInvalid, viejodomain.ErrRefreshInvalid, "iam: refresh token inválido"},
		{"invalid_input", domain.ErrInvalidInput, viejodomain.ErrInvalidInput, "iam: entrada inválida"},
	}
	for _, s := range sentinels {
		for _, wrapped := range []bool{false, true} {
			original := s.current
			if wrapped {
				original = fmt.Errorf("identity: %w", s.current)
			}
			for _, call := range authCalls {
				t.Run(fmt.Sprintf("%s/wrapped_%v/%s", s.name, wrapped, call.name), func(t *testing.T) {
					zero, err := call.run(t, &authenticatorBridge{next: &fakeAuthenticator{result: fullAuthResult(), err: original}})
					assertTranslated(t, err, original, s.current, s.old)
					if !zero {
						t.Errorf("con error, el resultado debía salir vacío")
					}
				})
			}
		}
		// El texto del centinela nuevo es el literal del viejo: es lo que acaba en el log.
		if s.current.Error() != s.wantText || s.old.Error() != s.wantText {
			t.Errorf("%s: textos nuevo %q y viejo %q, se esperaba %q en los dos", s.name, s.current.Error(), s.old.Error(), s.wantText)
		}
	}
}

func assertTranslated(t *testing.T, err, original, current, old error) {
	t.Helper()
	if err == nil {
		t.Fatalf("se esperaba un error, salió nil")
	}
	if !errors.Is(err, old) {
		t.Errorf("errors.Is(err, centinela VIEJO %q) = false: el gateway viejo no lo clasificaría", old)
	}
	if !errors.Is(err, current) {
		t.Errorf("errors.Is(err, centinela nuevo %q) = false", current)
	}
	if !errors.Is(err, original) {
		t.Errorf("errors.Is(err, original) = false")
	}
	if err.Error() != original.Error() {
		t.Errorf("Error() = %q, se esperaba el del original %q, byte a byte", err.Error(), original.Error())
	}
}

// Promesa: un error que no es uno de los cuatro centinelas sale tal cual (el mismo valor), sin
// casar con ningún centinela viejo.
func TestAuthenticatorBridge_ForeignErrorPassesThrough(t *testing.T) {
	foreign := []error{
		errors.New("identity: no disponible"),
		context.Canceled,
		domain.ErrIdentityUnavailable,
	}
	for i, ferr := range foreign {
		for _, call := range authCalls {
			t.Run(fmt.Sprintf("%d/%s", i, call.name), func(t *testing.T) {
				zero, err := call.run(t, &authenticatorBridge{next: &fakeAuthenticator{result: fullAuthResult(), err: ferr}})
				if err != ferr { //nolint:errorlint // la promesa es el MISMO valor, no que case
					t.Errorf("error = %v (%T), se esperaba el mismo valor %v", err, err, ferr)
				}
				for _, p := range iamSentinelPairs {
					if errors.Is(err, p.old) {
						t.Errorf("un error ajeno casa con el centinela viejo %q", p.old)
					}
				}
				if !zero {
					t.Errorf("con error, el resultado debía salir vacío")
				}
			})
		}
	}
}

// Promesa (punta a punta, servicio nuevo real): el ErrInvalidInput que da el DelegatedAuthService
// con credenciales vacías llega al gateway viejo como el centinela viejo.
func TestAuthenticatorBridge_RealServiceInvalidInputIsOldSentinel(t *testing.T) {
	b := newAuthenticatorBridge(&usecase.DelegatedAuthService{})
	if _, err := b.Login(t.Context(), viejoin.LoginInput{}); !errors.Is(err, viejodomain.ErrInvalidInput) {
		t.Errorf("Login vacío = %v, se esperaba que casara con el ErrInvalidInput viejo", err)
	}
	if err := b.Logout(t.Context(), viejoin.LogoutInput{}); !errors.Is(err, viejodomain.ErrInvalidInput) {
		t.Errorf("Logout vacío = %v, se esperaba que casara con el ErrInvalidInput viejo", err)
	}
}

// Promesa: con svc nil el constructor devuelve una interfaz nil DE VERDAD; con svc, un adaptador
// que delega en ese mismo servicio.
func TestNewAuthenticatorBridge_NilServiceIsRealNil(t *testing.T) {
	if got := newAuthenticatorBridge(nil); got != nil {
		t.Errorf("newAuthenticatorBridge(nil) = %#v, se esperaba una interfaz nil de verdad", got)
	}
	svc := &usecase.DelegatedAuthService{}
	b, ok := newAuthenticatorBridge(svc).(*authenticatorBridge)
	if !ok {
		t.Fatalf("newAuthenticatorBridge(svc) no devolvió un *authenticatorBridge")
	}
	if b.next != in.Authenticator(svc) {
		t.Errorf("el adaptador no delega en el servicio recibido")
	}
}

// Promesa: Record delega con el mismo ctx y el AuditInput intacto (es el mismo tipo); sin error,
// nil.
func TestAuditorBridge_Record_PassesInputIntact(t *testing.T) {
	next := &fakeAuditor{}
	b := &auditorBridge{next: next}
	input := viejoin.AuditInput{
		TenantID: "tenant-1", Actor: "user-1", Action: "edge.auth.login", Resource: "edge-1", Result: "ok",
		Meta: map[string]any{"canal": "grpc"},
	}

	if err := b.Record(markedCtx(t), input); err != nil {
		t.Fatalf("Record devolvió error %v, se esperaba nil", err)
	}
	assertMarked(t, ctxMarker(next.gotCtx))
	if !reflect.DeepEqual(next.gotInput, input) {
		t.Errorf("AuditInput recibido = %+v, se esperaba %+v", next.gotInput, input)
	}
}

// Promesa: ListAudit delega tenantID, limit y offset con el mismo ctx y devuelve cada evento con
// todos sus campos, en el mismo orden; nil sale nil y vacía sale vacía.
func TestAuditorBridge_ListAudit_ConvertsEveryField(t *testing.T) {
	tid := "tenant-1"
	events := []domain.AuditEvent{
		{ID: 7, TenantID: &tid, Actor: "user-1", Action: "edge.auth.login", Resource: "edge-1", Result: "ok",
			Meta: map[string]any{"k": "v"}, At: iamFixedTime},
		{ID: 3, TenantID: nil, Actor: "anon", Action: "edge.auth.refresh", Resource: "", Result: "denied",
			Meta: nil, At: iamFixedTime.Add(-time.Hour)},
	}
	next := &fakeAuditor{events: events}
	b := &auditorBridge{next: next}

	got, err := b.ListAudit(markedCtx(t), "tenant-1", 50, 10)
	if err != nil {
		t.Fatalf("ListAudit devolvió error %v, se esperaba nil", err)
	}
	assertMarked(t, ctxMarker(next.gotCtx))
	if next.gotTenantID != "tenant-1" || next.gotLimit != 50 || next.gotOffset != 10 {
		t.Errorf("argumentos recibidos = (%q, %d, %d), se esperaba (%q, 50, 10)", next.gotTenantID, next.gotLimit, next.gotOffset, "tenant-1")
	}
	want := []viejodomain.AuditEvent{
		{ID: 7, TenantID: &tid, Actor: "user-1", Action: "edge.auth.login", Resource: "edge-1", Result: "ok",
			Meta: map[string]any{"k": "v"}, At: iamFixedTime},
		{ID: 3, TenantID: nil, Actor: "anon", Action: "edge.auth.refresh", Resource: "", Result: "denied",
			Meta: nil, At: iamFixedTime.Add(-time.Hour)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("eventos = %+v, se esperaba %+v", got, want)
	}

	for _, tc := range []struct {
		name string
		in   []domain.AuditEvent
	}{{"nil_stays_nil", nil}, {"empty_stays_empty", []domain.AuditEvent{}}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (&auditorBridge{next: &fakeAuditor{events: tc.in}}).ListAudit(t.Context(), "t", 1, 0)
			if err != nil || (got == nil) != (tc.in == nil) || len(got) != 0 {
				t.Errorf("ListAudit = (%#v, %v), se esperaba (%#v, nil)", got, err, tc.in)
			}
		})
	}
}

// Promesa: los errores del auditor se traducen igual (centinela → viejo y nuevo, texto intacto;
// ajeno → mismo valor), y ListAudit devuelve nil junto al error.
func TestAuditorBridge_TranslatesErrors(t *testing.T) {
	foreign := errors.New("postgres: caído")
	b := &auditorBridge{next: &fakeAuditor{err: domain.ErrInvalidInput, events: []domain.AuditEvent{{ID: 1}}}}
	assertTranslated(t, b.Record(t.Context(), viejoin.AuditInput{}), domain.ErrInvalidInput, domain.ErrInvalidInput, viejodomain.ErrInvalidInput)
	got, err := b.ListAudit(t.Context(), "t", 1, 0)
	assertTranslated(t, err, domain.ErrInvalidInput, domain.ErrInvalidInput, viejodomain.ErrInvalidInput)
	if got != nil {
		t.Errorf("con error, ListAudit debía devolver nil, devolvió %#v", got)
	}

	b = &auditorBridge{next: &fakeAuditor{err: foreign}}
	if err := b.Record(t.Context(), viejoin.AuditInput{}); err != foreign { //nolint:errorlint // mismo valor
		t.Errorf("Record = %v, se esperaba el mismo error ajeno", err)
	}
	if _, err := b.ListAudit(t.Context(), "t", 1, 0); err != foreign { //nolint:errorlint // mismo valor
		t.Errorf("ListAudit = %v, se esperaba el mismo error ajeno", err)
	}
}

// Promesa (punta a punta, servicio nuevo real): el AuditService rechaza Action vacía con un error
// que el consumidor viejo reconoce como su ErrInvalidInput.
func TestAuditorBridge_RealServiceInvalidInputIsOldSentinel(t *testing.T) {
	b := newAuditorBridge(&usecase.AuditService{})
	if err := b.Record(t.Context(), viejoin.AuditInput{}); !errors.Is(err, viejodomain.ErrInvalidInput) {
		t.Errorf("Record sin Action = %v, se esperaba que casara con el ErrInvalidInput viejo", err)
	}
}

// Promesa: con svc nil el constructor devuelve una interfaz nil DE VERDAD; con svc, un adaptador
// que delega en ese mismo servicio.
func TestNewAuditorBridge_NilServiceIsRealNil(t *testing.T) {
	if got := newAuditorBridge(nil); got != nil {
		t.Errorf("newAuditorBridge(nil) = %#v, se esperaba una interfaz nil de verdad", got)
	}
	svc := &usecase.AuditService{}
	b, ok := newAuditorBridge(svc).(*auditorBridge)
	if !ok {
		t.Fatalf("newAuditorBridge(svc) no devolvió un *auditorBridge")
	}
	if b.next != in.Auditor(svc) {
		t.Errorf("el adaptador no delega en el servicio recibido")
	}
}
