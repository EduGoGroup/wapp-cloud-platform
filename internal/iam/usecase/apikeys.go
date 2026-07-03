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

// apiKeySecretTTL es un TTL nominal (ignorado) que se pasa a
// auth.GenerateRefreshToken para reutilizar su CSPRNG como generador del secreto
// de la api-key: nos quedamos con el token opaco (secreto) y su SHA256 (key_hash),
// no con la expiración que calcula.
const apiKeySecretTTL = time.Hour

// APIKeyService implementa in.APIKeyManager: emisión (secreto devuelto UNA vez),
// listado y revocación de credenciales M2M. En BD solo vive el hash del secreto
// (zero-knowledge del secreto, design.md §8/§10).
type APIKeyService struct {
	apikeys out.APIKeyRepo
}

// compile-time: APIKeyService satisface el puerto de entrada.
var _ in.APIKeyManager = (*APIKeyService)(nil)

// NewAPIKeyService construye el servicio de api-keys. Valida deps nil.
func NewAPIKeyService(apikeys out.APIKeyRepo) (*APIKeyService, error) {
	if apikeys == nil {
		return nil, errors.New("iam: APIKeyService requiere el repositorio de api-keys")
	}
	return &APIKeyService{apikeys: apikeys}, nil
}

// IssueAPIKey emite una credencial M2M: genera un secreto opaco (CSPRNG),
// persiste solo su hash SHA256 y devuelve el secreto en CLARO UNA vez. El caller
// debe entregarlo al cliente y NO persistirlo.
func (s *APIKeyService) IssueAPIKey(ctx context.Context, req in.IssueAPIKeyInput) (in.IssueAPIKeyResult, error) {
	if req.TenantID == "" || req.ClientID == "" {
		return in.IssueAPIKeyResult{}, domain.ErrInvalidInput
	}
	gen, err := auth.GenerateRefreshToken(apiKeySecretTTL)
	if err != nil {
		return in.IssueAPIKeyResult{}, err
	}
	created, err := s.apikeys.Create(ctx, domain.APIKey{
		TenantID:  req.TenantID,
		ClientID:  req.ClientID,
		KeyHash:   gen.TokenHash,
		Scopes:    req.Scopes,
		IsActive:  true,
		ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		return in.IssueAPIKeyResult{}, err
	}
	return in.IssueAPIKeyResult{APIKey: created, Secret: gen.Token}, nil
}

// ListAPIKeys devuelve las api-keys del tenant (metadatos, sin secreto).
func (s *APIKeyService) ListAPIKeys(ctx context.Context, tenantID string) ([]domain.APIKey, error) {
	return s.apikeys.List(ctx, tenantID)
}

// RevokeAPIKey revoca una api-key del tenant (verifica pertenencia vía el filtro
// tenant_id del repo).
func (s *APIKeyService) RevokeAPIKey(ctx context.Context, tenantID, id string) error {
	return s.apikeys.Revoke(ctx, tenantID, id)
}
