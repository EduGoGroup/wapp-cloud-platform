// Package lease gestiona, del lado de la Plataforma Cloud, la emisión,
// renovación y revocación de los leases del kill-switch anti-clon (ADR-0007).
//
// El trabajo criptográfico (firma Ed25519 del blob) lo hace el Issuer público
// de wapp-cloudlink (paquete lease, importado aquí como cllease). Este paquete
// añade lo que el Gateway necesita por encima de él:
//   - política de TTL y de counter (inicial=1, renovación=heartbeatCounter+1),
//   - persistencia del estado de autorización en la tabla leases (interface
//     Repository, con impl memory para tests y postgres para prod),
//   - resolución de la clave privada de firma a partir de configuración de dev.
//
// El lease NUNCA contiene la DEK ni ninguna llave privada (ADR-0007/0009): el
// blob firmado solo autoriza a operar; lo que se persiste aquí son metadatos.
package lease

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
)

// DefaultTTL es la vigencia de un lease emitido. Decisión de Plan 005 · T4:
// 5 minutos, renovado en cada Heartbeat (el Edge late cada 30s), de modo que un
// Edge sano siempre tiene lease fresco y la ventana offline máxima es de 5 min.
const DefaultTTL = 5 * time.Minute

// initialCounter es el counter del primer lease de un Edge. El Validator del
// Edge exige counter estrictamente creciente; arrancar en 1 deja 0 como
// "nunca emitido".
const initialCounter int64 = 1

// Manager emite, renueva y revoca leases y persiste su estado. Es seguro para
// uso concurrente (el Issuer no muta estado y el repo debe serlo).
type Manager struct {
	issuer *cllease.Issuer
	repo   Repository
	ttl    time.Duration
}

// Option configura el Manager.
type Option func(*Manager)

// WithTTL fija el TTL de los leases emitidos. Si d <= 0 se ignora (se conserva
// DefaultTTL).
func WithTTL(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.ttl = d
		}
	}
}

// NewManager construye un Manager sobre la clave privada Ed25519 del servidor y
// el repositorio de persistencia dado. Devuelve error si la clave es inválida.
func NewManager(priv ed25519.PrivateKey, repo Repository, opts ...Option) (*Manager, error) {
	issuer, err := cllease.NewIssuer(priv)
	if err != nil {
		return nil, fmt.Errorf("lease: construir issuer: %w", err)
	}
	if repo == nil {
		return nil, errors.New("lease: repositorio nil")
	}
	m := &Manager{issuer: issuer, repo: repo, ttl: DefaultTTL}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

// PublicKey devuelve la clave pública Ed25519 con la que el Edge construye su
// Validator.
func (m *Manager) PublicKey() ed25519.PublicKey { return m.issuer.PublicKey() }

// PublicKeyBase64 devuelve la clave pública en base64 estándar, lista para
// pegarla en la configuración del Edge (T6) o registrarla en log al arrancar.
func (m *Manager) PublicKeyBase64() string {
	return base64.StdEncoding.EncodeToString(m.issuer.PublicKey())
}

// IssueInitial emite el primer lease de un Edge (counter=1, TTL configurado) y
// persiste su estado. Se invoca cuando el Edge abre su sesión CloudLink.
func (m *Manager) IssueInitial(ctx context.Context, tenantID, edgeID string) (*cloudlinkv1.LeaseUpdate, error) {
	return m.issueAndPersist(ctx, tenantID, edgeID, initialCounter)
}

// Renew emite un lease renovado a partir del counter reportado por el Heartbeat
// del Edge: counter = heartbeatCounter + 1 (monótono, anti-replay). Persiste el
// estado.
func (m *Manager) Renew(ctx context.Context, tenantID, edgeID string, heartbeatCounter int64) (*cloudlinkv1.LeaseUpdate, error) {
	return m.issueAndPersist(ctx, tenantID, edgeID, heartbeatCounter+1)
}

// issueAndPersist firma un lease vigente con el counter dado y persiste su
// estado (revoked=false). Devuelve el LeaseUpdate firmado para empujarlo al Edge.
func (m *Manager) issueAndPersist(ctx context.Context, tenantID, edgeID string, counter int64) (*cloudlinkv1.LeaseUpdate, error) {
	lu, err := m.issuer.Issue(edgeID, tenantID, m.ttl, counter)
	if err != nil {
		return nil, fmt.Errorf("lease: emitir: %w", err)
	}
	state := State{
		TenantID:  tenantID,
		EdgeID:    edgeID,
		Counter:   counter,
		ExpiresAt: time.Unix(lu.GetExpiresUnix(), 0).UTC(),
		Revoked:   false,
	}
	if err := m.repo.Upsert(ctx, state); err != nil {
		return nil, fmt.Errorf("lease: persistir emisión: %w", err)
	}
	return lu, nil
}

// Revoke emite un LeaseUpdate de revocación (kill-switch) para el Edge y marca
// el estado como revocado de forma pegajosa. No depende del counter: un
// kill-switch debe poder dispararse siempre.
func (m *Manager) Revoke(ctx context.Context, tenantID, edgeID string) (*cloudlinkv1.LeaseUpdate, error) {
	lu, err := m.issuer.Revoke(edgeID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("lease: revocar: %w", err)
	}
	if err := m.repo.MarkRevoked(ctx, tenantID, edgeID, time.Unix(lu.GetExpiresUnix(), 0).UTC()); err != nil {
		return nil, fmt.Errorf("lease: persistir revocación: %w", err)
	}
	return lu, nil
}
