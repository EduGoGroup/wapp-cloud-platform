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

// M2MService implementa in.M2MAuthenticator: autenticación máquina-a-máquina por
// api-key (lookup por key_hash) y por service token (ServiceJWTManager de
// wapp-shared/auth). La autorización por scope reutiliza el matcher glob
// (auth.PermissionMatches), sin re-implementarlo.
type M2MService struct {
	apikeys out.APIKeyRepo
	svcJWT  *auth.ServiceJWTManager
	cfg     Config
}

// compile-time: M2MService satisface el puerto de entrada.
var _ in.M2MAuthenticator = (*M2MService)(nil)

// NewM2MService construye el servicio M2M. Valida deps nil.
func NewM2MService(apikeys out.APIKeyRepo, svcJWT *auth.ServiceJWTManager, cfg Config) (*M2MService, error) {
	if apikeys == nil {
		return nil, errors.New("iam: M2MService requiere el repositorio de api-keys")
	}
	if svcJWT == nil {
		return nil, errors.New("iam: M2MService requiere un ServiceJWTManager")
	}
	return &M2MService{apikeys: apikeys, svcJWT: svcJWT, cfg: cfg.withDefaults()}, nil
}

// AuthenticateAPIKey valida el secreto en claro de una api-key: busca por su
// hash SHA256, comprueba estado (activa, no revocada, no expirada), refresca
// last_used (best-effort) y devuelve la identidad+scopes.
func (s *M2MService) AuthenticateAPIKey(ctx context.Context, rawKey string) (in.ServiceIdentity, error) {
	if rawKey == "" {
		return in.ServiceIdentity{}, domain.ErrAPIKeyInvalid
	}
	k, err := s.apikeys.GetByHash(ctx, auth.HashToken(rawKey))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return in.ServiceIdentity{}, domain.ErrAPIKeyInvalid
		}
		return in.ServiceIdentity{}, err
	}
	if !k.IsActive || k.RevokedAt != nil {
		return in.ServiceIdentity{}, domain.ErrAPIKeyInvalid
	}
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		return in.ServiceIdentity{}, domain.ErrAPIKeyInvalid
	}
	// Telemetría best-effort: un fallo aquí no invalida la autenticación.
	if terr := s.apikeys.TouchLastUsed(ctx, k.ID); terr != nil {
		_ = terr
	}
	return in.ServiceIdentity{TenantID: k.TenantID, ClientID: k.ClientID, Scopes: k.Scopes}, nil
}

// IssueServiceToken firma un service token de corta vida acotado a un tenant.
func (s *M2MService) IssueServiceToken(_ context.Context, req in.IssueServiceTokenInput) (in.ServiceTokenResult, error) {
	token, expiresAt, err := s.svcJWT.GenerateServiceToken(req.ClientID, req.TenantID, req.Scopes, s.cfg.ServiceTTL)
	if err != nil {
		return in.ServiceTokenResult{}, err
	}
	return in.ServiceTokenResult{Token: token, ExpiresAt: expiresAt}, nil
}

// VerifyServiceToken valida un service token (firma+iss+aud+exp+token_use) y
// devuelve su identidad+scopes.
func (s *M2MService) VerifyServiceToken(_ context.Context, token string) (in.ServiceIdentity, error) {
	claims, err := s.svcJWT.ValidateServiceToken(token)
	if err != nil {
		return in.ServiceIdentity{}, err
	}
	return in.ServiceIdentity{TenantID: claims.TenantID, ClientID: claims.ClientID, Scopes: claims.Scopes}, nil
}

// AuthorizeScope decide si algún scope concedido cubre el permiso pedido usando
// el mismo matcher glob que los grants (allow-only: los scopes no llevan deny).
func (s *M2MService) AuthorizeScope(scopes []string, required string) bool {
	for _, sc := range scopes {
		if auth.PermissionMatches(sc, required) {
			return true
		}
	}
	return false
}
