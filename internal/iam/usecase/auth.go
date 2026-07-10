package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/EduGoGroup/wapp-shared/auth"
)

// tokenTypeBearer es el único esquema de autorización que emite el IAM.
const tokenTypeBearer = "Bearer"

// TokenValidator valida un access token de usuario y devuelve sus claims. Lo
// satisfacen *auth.JWTManager (emisor único) y *auth.MultiVerifier (validación
// dual-alg durante la coexistencia HS256↔ES256, ADR-0019). El AuthService lo
// mantiene DESACOPLADO del emisor (s.jwt) para que Verify acepte en la ventana
// dual exactamente los mismos tokens que el middleware del :8103 (mismo
// MultiVerifier inyectado), incluso cuando el emisor ya cortó a ES256.
type TokenValidator interface {
	ValidateToken(token string) (*auth.Claims, error)
}

// AuthService implementa in.Authenticator: login/refresh/logout/verify de
// usuario. Resuelve los grants efectivos al emitir (resolveEffectiveGrants),
// firma con el JWTManager de wapp-shared/auth (s.jwt, emisor) y valida los
// access tokens con s.validator (dual-alg), y persiste el hash del refresh.
type AuthService struct {
	users     out.UserRepo
	roles     out.RoleRepo
	grants    out.GrantRepo
	refresh   out.RefreshRepo
	audit     out.AuditRepo
	jwt       *auth.JWTManager
	validator TokenValidator
	cfg       Config
}

// compile-time: AuthService satisface el puerto de entrada.
var _ in.Authenticator = (*AuthService)(nil)

// NewAuthService construye el servicio de autenticación. Valida deps nil
// (fail-fast en el arranque). Los TTLs en cero de cfg toman sus defaults.
func NewAuthService(
	users out.UserRepo,
	roles out.RoleRepo,
	grants out.GrantRepo,
	refresh out.RefreshRepo,
	audit out.AuditRepo,
	jwt *auth.JWTManager,
	validator TokenValidator,
	cfg Config,
) (*AuthService, error) {
	if users == nil || roles == nil || grants == nil || refresh == nil || audit == nil {
		return nil, errors.New("iam: AuthService requiere todos los repositorios")
	}
	if jwt == nil {
		return nil, errors.New("iam: AuthService requiere un JWTManager emisor")
	}
	if validator == nil {
		return nil, errors.New("iam: AuthService requiere un TokenValidator")
	}
	return &AuthService{
		users:     users,
		roles:     roles,
		grants:    grants,
		refresh:   refresh,
		audit:     audit,
		jwt:       jwt,
		validator: validator,
		cfg:       cfg.withDefaults(),
	}, nil
}

// Login valida credenciales y emite el par de tokens. El tenant NO se inventa:
// se deriva de la fila del usuario. Registra auditoría (CERO PII: actor = id
// opaco del usuario, o "unknown" en fallo pre-identidad).
func (s *AuthService) Login(ctx context.Context, req in.LoginInput) (domain.AuthResult, error) {
	if req.Email == "" || req.Password == "" {
		return domain.AuthResult{}, domain.ErrInvalidInput
	}

	var (
		u   domain.User
		err error
	)
	if req.TenantID != "" {
		u, err = s.users.GetByEmail(ctx, req.TenantID, req.Email)
	} else {
		u, err = s.users.FindByEmail(ctx, req.Email)
	}
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.record(ctx, "", "unknown", "auth.login", "auth", "error")
			return domain.AuthResult{}, domain.ErrInvalidCredentials
		}
		return domain.AuthResult{}, err
	}

	if !u.IsActive || u.DeletedAt != nil {
		s.record(ctx, u.TenantID, u.ID, "auth.login", "auth", "error")
		return domain.AuthResult{}, domain.ErrUserInactive
	}

	if verr := auth.VerifyPassword(u.PasswordHash, req.Password); verr != nil {
		s.record(ctx, u.TenantID, u.ID, "auth.login", "auth", "error")
		return domain.AuthResult{}, domain.ErrInvalidCredentials
	}

	result, err := s.issue(ctx, u)
	if err != nil {
		return domain.AuthResult{}, err
	}
	s.record(ctx, u.TenantID, u.ID, "auth.login", "auth", "ok")
	return result, nil
}

// Refresh rota el refresh token: valida (hash/no revocado/no expirado),
// RE-RESUELVE los grants (para reflejar cambios de rol desde el login), emite un
// access nuevo, revoca el refresh usado y persiste uno nuevo.
func (s *AuthService) Refresh(ctx context.Context, req in.RefreshInput) (domain.AuthResult, error) {
	if req.RefreshToken == "" {
		return domain.AuthResult{}, domain.ErrInvalidInput
	}

	hash := auth.HashToken(req.RefreshToken)
	rec, err := s.refresh.GetByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.AuthResult{}, domain.ErrRefreshInvalid
		}
		return domain.AuthResult{}, err
	}
	if rec.RevokedAt != nil || time.Now().After(rec.ExpiresAt) {
		return domain.AuthResult{}, domain.ErrRefreshInvalid
	}

	u, err := s.users.GetByID(ctx, rec.UserID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.AuthResult{}, domain.ErrRefreshInvalid
		}
		return domain.AuthResult{}, err
	}
	if !u.IsActive || u.DeletedAt != nil {
		return domain.AuthResult{}, domain.ErrUserInactive
	}

	result, err := s.issue(ctx, u)
	if err != nil {
		return domain.AuthResult{}, err
	}
	// Rotación: invalida el refresh usado (el nuevo ya se persistió en issue).
	if rerr := s.refresh.Revoke(ctx, hash); rerr != nil {
		return domain.AuthResult{}, rerr
	}
	s.record(ctx, u.TenantID, u.ID, "auth.refresh", "auth", "ok")
	return result, nil
}

// Logout revoca refresh tokens. Idempotente: revocar un hash inexistente no es
// error.
func (s *AuthService) Logout(ctx context.Context, req in.LogoutInput) error {
	if req.AllSessions && req.UserID != "" {
		return s.refresh.RevokeAllForUser(ctx, req.UserID)
	}
	if req.RefreshToken == "" {
		return domain.ErrInvalidInput
	}
	return s.refresh.Revoke(ctx, auth.HashToken(req.RefreshToken))
}

// Verify valida un access token. Un token inválido/expirado devuelve
// Valid=false SIN error (semántica del endpoint /verify, design.md §8); solo
// los errores inesperados se propagan.
func (s *AuthService) Verify(_ context.Context, accessToken string) (in.VerifyResult, error) {
	claims, err := s.validator.ValidateToken(accessToken)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidToken) || errors.Is(err, auth.ErrTokenExpired) {
			return in.VerifyResult{Valid: false}, nil
		}
		return in.VerifyResult{Valid: false}, err
	}
	var expiresAt time.Time
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time
	}
	return in.VerifyResult{
		Valid:     true,
		TenantID:  claims.TenantID,
		Subject:   claims.UserID,
		Roles:     claims.Roles,
		ExpiresAt: expiresAt,
	}, nil
}

// issue resuelve grants efectivos, firma el access token y persiste un refresh
// nuevo. Compartido por Login y Refresh.
func (s *AuthService) issue(ctx context.Context, u domain.User) (domain.AuthResult, error) {
	grants, roleNames, err := resolveEffectiveGrants(ctx, s.roles, s.grants, u.ID)
	if err != nil {
		return domain.AuthResult{}, err
	}

	access, expiresAt, err := s.jwt.GenerateToken(u.ID, u.TenantID, roleNames, grants, s.cfg.AccessTTL)
	if err != nil {
		return domain.AuthResult{}, err
	}

	rt, err := auth.GenerateRefreshToken(s.cfg.RefreshTTL)
	if err != nil {
		return domain.AuthResult{}, err
	}
	if serr := s.refresh.Save(ctx, domain.RefreshToken{
		UserID:    u.ID,
		TokenHash: rt.TokenHash,
		ExpiresAt: rt.ExpiresAt,
	}); serr != nil {
		return domain.AuthResult{}, serr
	}

	return domain.AuthResult{
		AccessToken:  access,
		RefreshToken: rt.Token,
		TokenType:    tokenTypeBearer,
		ExpiresAt:    expiresAt,
		Context: domain.IdentityContext{
			TenantID: u.TenantID,
			UserID:   u.ID,
			Roles:    roleNames,
		},
	}, nil
}

// record escribe un evento de auditoría best-effort (un fallo de auditoría no
// aborta la operación de negocio). CERO PII: actor/resource son ids opacos.
func (s *AuthService) record(ctx context.Context, tenantID, actor, action, resource, result string) {
	var tid *string
	if tenantID != "" {
		tid = &tenantID
	}
	if err := s.audit.Record(ctx, domain.AuditEvent{
		TenantID: tid,
		Actor:    actor,
		Action:   action,
		Resource: resource,
		Result:   result,
	}); err != nil {
		_ = err // best-effort: un fallo de auditoría no aborta la operación
	}
}
