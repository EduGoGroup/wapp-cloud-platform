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

// DefaultTTL es la vigencia de un lease emitido, renovado en cada Heartbeat (el
// Edge late cada 30s). Nació en 5 minutos (Plan 005 · T4); D-055.7 (Plan 055,
// 2026-08-13, Jhoan) lo sube a 15: cubre un blip normal de wifi/4G que 5 no
// cubría, ya sin ser vía de escape para un Edge revocado (issueAndPersist
// consulta el estado persistido antes de emitir).
const DefaultTTL = 15 * time.Minute

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
// estado (revoked=false), PERO solo si el estado persistido del Edge no dice
// ya "revocado" (D-055.1): la revocación es pegajosa y ni IssueInitial ni
// Renew pueden des-revocar. Devuelve el LeaseUpdate firmado para empujarlo al
// Edge; si el Edge está revocado, el LeaseUpdate devuelto es el de
// revocación (el mismo tipo que construye Revoke), no uno vigente.
func (m *Manager) issueAndPersist(ctx context.Context, tenantID, edgeID string, counter int64) (*cloudlinkv1.LeaseUpdate, error) {
	revoked, err := m.wasRevoked(ctx, tenantID, edgeID)
	if err != nil {
		// Fail-closed (D-055.1): si no podemos leer el estado previo, NO
		// emitimos un lease vigente en su ausencia -- un fallo transitorio de
		// lectura no debe traducirse silenciosamente en autorización para
		// operar. El llamante (IssueInitial/Renew) recibe el error y no hay
		// LeaseUpdate que empujar; el Edge se queda sin lease fresco hasta
		// que la próxima renovación/heartbeat pueda leer el estado.
		return nil, err
	}
	if revoked {
		// El estado persistido dice revocado: no se emite un lease vigente
		// ni se toca Upsert. Se devuelve el mismo tipo de LeaseUpdate
		// revocado que construye Revoke (vía m.issuer.Revoke), para que el
		// Edge reciba una señal explícita de revocación en vez de silencio.
		lu, err := m.issuer.Revoke(edgeID, tenantID)
		if err != nil {
			return nil, fmt.Errorf("lease: re-emitir revocación: %w", err)
		}
		return lu, nil
	}

	lu, err := m.issuer.Issue(edgeID, tenantID, m.ttl, counter)
	if err != nil {
		return nil, fmt.Errorf("lease: emitir: %w", err)
	}
	// Revoked se deja en su cero por claridad, pero Upsert lo IGNORA en ambas
	// implementaciones (contrato de Repository.Upsert): no es este campo el que
	// impide des-revocar, sino la guarda wasRevoked de arriba más el hecho de
	// que Upsert nunca escribe el estado de revocación.
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

// wasRevoked consulta el estado persistido -- del TENANT primero (D-055.2,
// kill-switch COMERCIAL) y del Edge después (D-055.1, anti-clon) -- para
// decidir si IssueInitial/Renew deben abortar la emisión en favor de una
// revocación. Un tenant revocado gana incluso sobre un edge_id que NUNCA se
// ha visto (Get devolvería found=false): una instalación NUEVA de un tenant
// cortado nace revocada, no hace falta que exista fila en leases para eso.
// Si el tenant está activo y el Edge nunca se ha visto, no está revocado: se
// emite normal. Cualquier error se propaga sin envolver la decisión en un
// booleano ambiguo -- ver el comentario de fail-closed en issueAndPersist.
func (m *Manager) wasRevoked(ctx context.Context, tenantID, edgeID string) (bool, error) {
	tenantRevoked, err := m.repo.TenantRevoked(ctx, tenantID)
	if err != nil {
		return false, fmt.Errorf("lease: decidir corte por tenant: %w", err)
	}
	if tenantRevoked {
		return true, nil
	}

	st, found, err := m.repo.Get(ctx, tenantID, edgeID)
	if err != nil {
		return false, fmt.Errorf("lease: consultar estado previo: %w", err)
	}
	if !found {
		return false, nil
	}
	return st.Revoked, nil
}

// RevokeTenant dispara el kill-switch COMERCIAL (D-055.2): marca el TENANT
// como revocado, de forma pegajosa, independiente de cualquier lease
// individual. NO toca ninguna fila de public.leases -- si tocara
// leases.revoked de cada instalación, RestoreTenant no podría reactivarlas
// (leases no tiene reverso por-instalación, deuda ya documentada); dejar los
// dos sujetos de corte independientes es lo que permite que restaurar el
// tenant reactive TODAS sus instalaciones de una vez. La notificación viva a
// cada sesión conectada (LeaseUpdate empujado) es responsabilidad del
// llamante (gatewaygrpc.Server.RevokeTenant), que conoce el fleet; este
// método solo persiste el estado de autorización.
func (m *Manager) RevokeTenant(ctx context.Context, tenantID string) error {
	if err := m.repo.MarkTenantRevoked(ctx, tenantID); err != nil {
		return fmt.Errorf("lease: revocar tenant: %w", err)
	}
	return nil
}

// RestoreTenant reactiva un tenant previamente revocado (revoked_at = NULL).
// No re-emite leases vigentes por sí mismo ni toca leases.revoked de ninguna
// instalación: el siguiente IssueInitial/Renew de cada Edge pasa por
// wasRevoked, que ya verá el tenant activo y (si el Edge en sí nunca estuvo
// revocado individualmente) volverá a emitir vigente.
func (m *Manager) RestoreTenant(ctx context.Context, tenantID string) error {
	if err := m.repo.RestoreTenant(ctx, tenantID); err != nil {
		return fmt.Errorf("lease: restaurar tenant: %w", err)
	}
	return nil
}

// SignTenantRevocation firma el LeaseUpdate de revocación para UN Edge
// concreto, SIN persistir su estado individual en leases (a diferencia de
// Revoke, que sí marca leases.revoked=true para ESE edge). La usa el fan-out
// de gatewaygrpc.Server.RevokeTenant para notificar en vivo a cada
// instalación conocida del tenant ya revocado (RevokeTenant, arriba, es
// quien persiste el corte real vía tenants.revoked_at): el blob firmado aquí
// es solo la notificación push, la autorización real la sigue decidiendo
// wasRevoked contra el estado del tenant en cada emisión futura.
func (m *Manager) SignTenantRevocation(edgeID, tenantID string) (*cloudlinkv1.LeaseUpdate, error) {
	lu, err := m.issuer.Revoke(edgeID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("lease: firmar revocación de tenant para edge: %w", err)
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
