// Porta internal/gateway/enroll/edgecert.go @ 8896f13

package enroll

import (
	"context"
	"database/sql"
	"fmt"
	"time"
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
type PostgresEdgeCertRepository struct {
	db *sql.DB
}

// NewPostgresEdgeCertRepository construye el repo sobre el pool dado. No abre
// ni comprueba la conexión.
func NewPostgresEdgeCertRepository(db *sql.DB) *PostgresEdgeCertRepository {
	return &PostgresEdgeCertRepository{db: db}
}

// Create inserta los metadatos del cert emitido en edge_certs: un INSERT, con
// los siete campos del registro en su orden y CertPEM como texto. Un fallo del
// driver vuelve envuelto como "enroll: persistiendo edge_cert: …".
func (r *PostgresEdgeCertRepository) Create(ctx context.Context, rec EdgeCertRecord) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.edge_certs
			(tenant_id, subject_cn, serial_number, fingerprint, not_before, not_after, cert_pem)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, rec.TenantID, rec.SubjectCN, rec.SerialNumber, rec.Fingerprint, rec.NotBefore, rec.NotAfter, string(rec.CertPEM))
	if err != nil {
		return fmt.Errorf("enroll: persistiendo edge_cert: %w", err)
	}
	return nil
}
