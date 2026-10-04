// Porta internal/gateway/enroll/ca.go @ 8896f13

package enroll

import (
	"crypto"
	"crypto/x509"
	"errors"
	"net"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ErrInvalidCSR indica que el CSR no es PEM válido, no parsea o su firma no
// verifica.
var ErrInvalidCSR = errors.New("enroll: CSR inválido")

// DefaultEdgeCertTTL es la vida (corta, de dev) del cert hoja del Edge: 90
// días.
const DefaultEdgeCertTTL = 90 * 24 * time.Hour

// CA firma CSRs de Edges emitiendo certs hoja con EKU ClientAuth. La misma CA se
// expone como CertPool para inyectarla en mtls.ServerCreds(...ClientCAs) en T4:
// así el Edge enrolado conecta por mTLS contra esta plataforma con la MISMA CA.
type CA struct{}

// NewCA construye una CA firmante a partir de un cert+clave ya cargados. certTTL
// es la vida del cert hoja del Edge; si <=0 se usa DefaultEdgeCertTTL. No
// valida cert ni key.
func NewCA(cert *x509.Certificate, key crypto.Signer, certTTL time.Duration) *CA {
	panic(pendiente.Implementar("enroll.NewCA"))
}

// NewDevCA genera una CA autofirmada efímera (dev/tests). No toca disco. El
// certificado es una CA (IsCA, KeyUsage CertSign) con ese CommonName, válido
// desde un minuto antes de ahora hasta ahora+caTTL; cada llamada genera otra
// clave. edgeCertTTL es la vida de los certs hoja (<=0 ⇒ DefaultEdgeCertTTL).
func NewDevCA(commonName string, caTTL, edgeCertTTL time.Duration) (*CA, error) {
	panic(pendiente.Implementar("enroll.NewDevCA"))
}

// LoadCAFromPEM construye la CA desde PEM (cert + clave PKCS#8 o EC). Cableado de
// prod/dev a partir de archivos de la plataforma/tenant. Errores: el PEM del
// cert no es un CERTIFICATE ("enroll: PEM de CA no es un CERTIFICATE"), no
// parsea ("enroll: parsear cert CA: …"), el de la clave no es PEM ("enroll: PEM
// de clave CA inválido") o no es PKCS#8 ni EC ("enroll: no se pudo parsear la
// clave de la CA (PKCS#8 o EC)").
func LoadCAFromPEM(certPEM, keyPEM []byte, edgeCertTTL time.Duration) (*CA, error) {
	panic(pendiente.Implementar("enroll.LoadCAFromPEM"))
}

// Certificate devuelve el cert de la CA (para construir cadenas o pools).
func (c *CA) Certificate() *x509.Certificate {
	panic(pendiente.Implementar("enroll.CA.Certificate"))
}

// Pool devuelve un CertPool con la CA, listo para mtls.ServerCreds(ClientCAs) o
// como RootCAs del cliente. Es el puente enrolamiento<->mTLS con la MISMA CA.
func (c *CA) Pool() *x509.CertPool {
	panic(pendiente.Implementar("enroll.CA.Pool"))
}

// CAChainPEM devuelve la cadena de la CA en PEM (la que se entrega al Edge).
func (c *CA) CAChainPEM() []byte {
	panic(pendiente.Implementar("enroll.CA.CAChainPEM"))
}

// SignedCert agrupa el cert hoja emitido (PEM) y sus metadatos, para devolver al
// Edge y persistir en edge_certs sin volver a parsear el PEM.
type SignedCert struct {
	// EdgeCertPEM es el cert hoja del Edge en PEM (material público).
	EdgeCertPEM []byte
	// CAChainPEM es la cadena de la CA en PEM (la que confía el Edge).
	CAChainPEM []byte
	// SubjectCN es el CommonName del Subject (identidad del Edge).
	SubjectCN string
	// SerialNumber es el serial del cert en hexadecimal.
	SerialNumber string
	// Fingerprint es el SHA-256 del DER en hexadecimal.
	Fingerprint string
	// NotBefore y NotAfter son la ventana de validez del cert.
	NotBefore time.Time
	NotAfter  time.Time
}

// SignCSR parsea el CSR (PEM), verifica su firma y emite un cert hoja de Edge con
// EKU ClientAuth, identidad tomada del CSR (Subject) y vida corta. tenantID se
// graba en Subject.Organization para trazabilidad. Devuelve el cert del Edge y la
// cadena de la CA (ambos PEM) más los metadatos del cert emitido.
//
// Promesas: el cert hoja lleva EKU ClientAuth y solo ese, KeyUsage
// DigitalSignature y NO es CA; su Subject es el del CSR con Organization
// reemplazada por [tenantID]; lo firma esta CA (verifica contra Pool()); vale
// desde un minuto antes de ahora hasta ahora+certTTL (usa time.Now(), sin
// reloj inyectable); el serial es aleatorio (dos firmas del mismo CSR dan
// certs distintos). Un CSR inválido devuelve ErrInvalidCSR y un SignedCert
// cero.
func (c *CA) SignCSR(csrPEM []byte, tenantID string) (SignedCert, error) {
	panic(pendiente.Implementar("enroll.CA.SignCSR"))
}

// IssueServerCert emite un cert de servidor (EKU ServerAuth) firmado por esta CA.
// Útil para levantar el endpoint mTLS del Gateway en dev/tests con la misma CA
// que valida a los Edges enrolados (se usará en T4). EKU ServerAuth y solo
// ese; los dnsNames e ips dados van como SAN; la clave devuelta es PKCS#8 en
// PEM ("PRIVATE KEY") y corresponde al cert; misma ventana de validez que
// SignCSR.
func (c *CA) IssueServerCert(commonName string, dnsNames []string, ips []net.IP) (certPEM, keyPEM []byte, err error) {
	panic(pendiente.Implementar("enroll.CA.IssueServerCert"))
}

// ParseAndVerifyCSR decodifica el PEM del CSR, lo parsea y verifica su firma.
// Devuelve ErrInvalidCSR (envuelto) ante cualquier fallo, sin filtrar detalles
// sensibles más allá de la causa técnica. Casos: no es PEM, es PEM de otro
// tipo que CERTIFICATE REQUEST, no parsea, o la firma no verifica (un CSR
// manipulado).
func ParseAndVerifyCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	panic(pendiente.Implementar("enroll.ParseAndVerifyCSR"))
}
