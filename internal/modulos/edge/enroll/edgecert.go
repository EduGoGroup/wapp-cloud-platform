// Porta internal/gateway/enroll/edgecert.go @ 8896f13

package enroll

import (
	"context"
	"database/sql"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// EdgeCertRecord son los metadatos de un certificado de Edge emitido que se
// persisten en edge_certs. Solo material PÚBLICO (cert PEM, identidad, validez);
// NUNCA la clave privada del Edge ni la DEK (ADR-0009).
type EdgeCertRecord struct {
	// TenantID es el UUID del tenant dueño del cert (Organization del Subject).
	TenantID string
	// SubjectCN es el CommonName del Edge (identidad).
	SubjectCN string
	// SerialNumber es el serial del cert en hexadecimal.
	SerialNumber string
	// Fingerprint es el SHA-256 del DER en hexadecimal (único).
	Fingerprint string
	// NotBefore y NotAfter delimitan la validez del cert.
	NotBefore time.Time
	NotAfter  time.Time
	// CertPEM es el cert hoja emitido en PEM (público).
	CertPEM []byte
}

// EdgeCertRepository persiste los certificados de Edge emitidos.
// Implementaciones: PostgresEdgeCertRepository y el doble
// enrollhelpertest.MemoriaEdgeCertRepository (en el paquete viejo,
// MemoryEdgeCertRepository, que vivía aquí; D-F3-1). Las dos corren
// enrollhelpertest.ContratoEdgeCertRepository.
type EdgeCertRepository interface {
	// Create graba los metadatos de un cert recién emitido.
	Create(ctx context.Context, rec EdgeCertRecord) error
}

// PostgresEdgeCertRepository implementa EdgeCertRepository con SQL raw sobre
// *sql.DB.
type PostgresEdgeCertRepository struct{}

// NewPostgresEdgeCertRepository construye el repo sobre el pool dado. No abre
// ni comprueba la conexión.
func NewPostgresEdgeCertRepository(db *sql.DB) *PostgresEdgeCertRepository {
	panic(pendiente.Implementar("enroll.NewPostgresEdgeCertRepository"))
}

// Create inserta los metadatos del cert emitido en edge_certs: un INSERT, con
// los siete campos del registro en su orden y CertPEM como texto. Un fallo del
// driver vuelve envuelto como "enroll: persistiendo edge_cert: …".
func (r *PostgresEdgeCertRepository) Create(ctx context.Context, rec EdgeCertRecord) error {
	panic(pendiente.Implementar("enroll.PostgresEdgeCertRepository.Create"))
}
