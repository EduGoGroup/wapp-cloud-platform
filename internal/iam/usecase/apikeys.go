package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// apiKeySecretBytes es la entropía del secreto de una api-key: 32 bytes (256
// bits) de crypto/rand, fuera del alcance de cualquier búsqueda exhaustiva.
const apiKeySecretBytes = 32

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
	secret, err := generateAPIKeySecret()
	if err != nil {
		return in.IssueAPIKeyResult{}, err
	}
	created, err := s.apikeys.Create(ctx, domain.APIKey{
		TenantID:  req.TenantID,
		ClientID:  req.ClientID,
		KeyHash:   hashAPIKeySecret(secret),
		Scopes:    req.Scopes,
		IsActive:  true,
		ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		return in.IssueAPIKeyResult{}, err
	}
	return in.IssueAPIKeyResult{APIKey: created, Secret: secret}, nil
}

// generateAPIKeySecret produce el secreto opaco de una api-key: apiKeySecretBytes
// de crypto/rand en base64 URL-safe.
//
// El secreto de una credencial M2M es una primitiva PROPIA del IAM de wApp, no un
// refresh token: no se canjea, no rota y no lleva el prefijo `rft_` con que
// identity-shared marca los suyos. Antes se tomaba prestado su CSPRNG pasándole un
// TTL nominal que se descartaba; que el préstamo funcionara no lo hacía correcto:
// ataba el formato del secreto a la evolución de otro concepto.
func generateAPIKeySecret() (string, error) {
	buf := make([]byte, apiKeySecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("iam: no se pudo generar entropía para el secreto de la api-key: %w", err)
	}
	return base64.URLEncoding.EncodeToString(buf), nil
}

// hashAPIKeySecret devuelve el SHA256 hex del secreto: lo ÚNICO que se persiste
// (key_hash), de modo que un volcado de la tabla no entregue credenciales usables.
// Es la contrapartida de generateAPIKeySecret y la usa el lookup de m2m.go.
func hashAPIKeySecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
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
