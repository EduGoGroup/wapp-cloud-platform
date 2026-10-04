// Porta internal/gateway/lease/lease.go @ 8896f13

// Package lease gestiona, del lado de la Plataforma Cloud, la emisión,
// renovación y revocación de los leases del kill-switch anti-clon (ADR-0007).
//
// Es la MITAD SERVIDORA de la doble llave. ADR-0007: «dos secretos disjuntos y
// hacen falta los dos para despachar: la DEK la custodia el cliente y la nube
// nunca la ve; el lease lo emite y revoca el servidor y es el kill-switch
// anti-clon. Un clon del `.db` sin lease es inútil».
//
// El trabajo criptográfico (firma Ed25519 del blob) lo hace el Issuer público
// de wapp-cloudlink (paquete lease, importado aquí como cllease). Este paquete
// añade lo que el Gateway necesita por encima de él:
//   - política de TTL y de counter (inicial=1, renovación=heartbeatCounter+1),
//   - persistencia del estado de autorización en la tabla leases (interface
//     Repository, con PostgresRepository para prod; el doble en memoria de los
//     tests vive en leasehelpertest, D-F3-1),
//   - resolución de la clave privada de firma a partir de configuración de dev.
//
// El lease NUNCA contiene la DEK ni ninguna llave privada (ADR-0007/0009): el
// blob firmado solo autoriza a operar; lo que se persiste aquí son metadatos
// (R-L8).
//
// Las reglas que este paquete promete (plan/F3-edge/diseno.md §4), cada una en
// el comentario del símbolo que la cumple:
//
//   - R-L1 IssueInitial → counter 1; Renew(hb) → hb+1; TTL 15 min salvo WithTTL(d>0).
//   - R-L2 Revocar y luego IssueInitial/Renew → sigue revocado; Upsert no resucita.
//   - R-L3 Tenant revocado gana sobre un Edge nunca visto; tenant activo + Edge nuevo → vigente.
//   - R-L4 RestoreTenant desbloquea la emisión futura sin tocar leases;
//     SignTenantRevocation firma sin persistir por Edge (dos sujetos de corte).
//   - R-L5 Fail-closed: error leyendo el estado → error, ningún lease.
//   - R-L6 Revoke no depende del counter: el kill-switch se dispara siempre.
//   - R-L7 NewManager con clave inválida o repo nil → error.
//   - R-L8 El lease no contiene la DEK ni llaves privadas.
package lease

import (
	"context"
	"crypto/ed25519"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// DefaultTTL es la vigencia de un lease emitido, renovado en cada Heartbeat (el
// Edge late cada 30s). Nació en 5 minutos (Plan 005 · T4); D-055.7 (Plan 055,
// 2026-08-13, Jhoan) lo sube a 15: cubre un blip normal de wifi/4G que 5 no
// cubría, ya sin ser vía de escape para un Edge revocado (issueAndPersist
// consulta el estado persistido antes de emitir). R-L1.
const DefaultTTL = 15 * time.Minute

// Manager emite, renueva y revoca leases y persiste su estado. Es seguro para
// uso concurrente (el Issuer no muta estado y el repo debe serlo). Lo único que
// firma y persiste son metadatos de autorización: ni la DEK ni llave privada
// alguna entran en un lease (R-L8).
type Manager struct{}

// Option configura el Manager.
type Option func(*Manager)

// WithTTL fija el TTL de los leases emitidos. Si d <= 0 se ignora (se conserva
// DefaultTTL). R-L1.
func WithTTL(d time.Duration) Option {
	panic(pendiente.Implementar("lease.WithTTL"))
}

// NewManager construye un Manager sobre la clave privada Ed25519 del servidor y
// el repositorio de persistencia dado. Devuelve error si la clave es inválida
// ("lease: construir issuer: …") o si el repositorio es nil ("lease:
// repositorio nil"); la clave se comprueba antes que el repositorio (R-L7).
// Sin opciones, el TTL es DefaultTTL.
func NewManager(priv ed25519.PrivateKey, repo Repository, opts ...Option) (*Manager, error) {
	panic(pendiente.Implementar("lease.NewManager"))
}

// PublicKey devuelve la clave pública Ed25519 con la que el Edge construye su
// Validator.
func (m *Manager) PublicKey() ed25519.PublicKey {
	panic(pendiente.Implementar("lease.Manager.PublicKey"))
}

// PublicKeyBase64 devuelve la clave pública en base64 estándar, lista para
// pegarla en la configuración del Edge (T6) o registrarla en log al arrancar.
func (m *Manager) PublicKeyBase64() string {
	panic(pendiente.Implementar("lease.Manager.PublicKeyBase64"))
}

// IssueInitial emite el primer lease de un Edge (counter=1, TTL configurado) y
// persiste su estado. Se invoca cuando el Edge abre su sesión CloudLink.
//
// Promesas: counter 1 (R-L1). Si el Edge o su tenant ya están revocados NO
// emite un lease vigente ni escribe nada: devuelve el LeaseUpdate de
// revocación (R-L2); un tenant revocado gana incluso sobre un Edge que nunca
// se ha visto (R-L3). Si no se puede leer el estado previo devuelve el error y
// ningún LeaseUpdate (fail-closed, R-L5).
func (m *Manager) IssueInitial(ctx context.Context, tenantID, edgeID string) (*cloudlinkv1.LeaseUpdate, error) {
	panic(pendiente.Implementar("lease.Manager.IssueInitial"))
}

// Renew emite un lease renovado a partir del counter reportado por el Heartbeat
// del Edge: counter = heartbeatCounter + 1 (monótono, anti-replay). Persiste el
// estado. R-L1; y las mismas guardas que IssueInitial: revocado sigue revocado
// (R-L2, R-L3) y un error de lectura no emite nada (R-L5).
func (m *Manager) Renew(ctx context.Context, tenantID, edgeID string, heartbeatCounter int64) (*cloudlinkv1.LeaseUpdate, error) {
	panic(pendiente.Implementar("lease.Manager.Renew"))
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
// método solo persiste el estado de autorización. R-L3, R-L4.
func (m *Manager) RevokeTenant(ctx context.Context, tenantID string) error {
	panic(pendiente.Implementar("lease.Manager.RevokeTenant"))
}

// RestoreTenant reactiva un tenant previamente revocado (revoked_at = NULL).
// No re-emite leases vigentes por sí mismo ni toca leases.revoked de ninguna
// instalación: el siguiente IssueInitial/Renew de cada Edge pasa por
// wasRevoked, que ya verá el tenant activo y (si el Edge en sí nunca estuvo
// revocado individualmente) volverá a emitir vigente. R-L4.
func (m *Manager) RestoreTenant(ctx context.Context, tenantID string) error {
	panic(pendiente.Implementar("lease.Manager.RestoreTenant"))
}

// SignTenantRevocation firma el LeaseUpdate de revocación para UN Edge
// concreto, SIN persistir su estado individual en leases (a diferencia de
// Revoke, que sí marca leases.revoked=true para ESE edge). La usa el fan-out
// de gatewaygrpc.Server.RevokeTenant para notificar en vivo a cada
// instalación conocida del tenant ya revocado (RevokeTenant, arriba, es
// quien persiste el corte real vía tenants.revoked_at): el blob firmado aquí
// es solo la notificación push, la autorización real la sigue decidiendo
// wasRevoked contra el estado del tenant en cada emisión futura. R-L4.
func (m *Manager) SignTenantRevocation(edgeID, tenantID string) (*cloudlinkv1.LeaseUpdate, error) {
	panic(pendiente.Implementar("lease.Manager.SignTenantRevocation"))
}

// Revoke emite un LeaseUpdate de revocación (kill-switch) para el Edge y marca
// el estado como revocado de forma pegajosa. No depende del counter: un
// kill-switch debe poder dispararse siempre (R-L6): tampoco lee el estado
// previo, así que funciona sobre un Edge nunca visto y con la lectura caída.
func (m *Manager) Revoke(ctx context.Context, tenantID, edgeID string) (*cloudlinkv1.LeaseUpdate, error) {
	panic(pendiente.Implementar("lease.Manager.Revoke"))
}
