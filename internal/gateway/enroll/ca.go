package enroll

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

// ErrInvalidCSR indica que el CSR no es PEM válido, no parsea o su firma no
// verifica.
var ErrInvalidCSR = errors.New("enroll: CSR inválido")

// DefaultEdgeCertTTL es la vida (corta, de dev) del cert hoja del Edge.
const DefaultEdgeCertTTL = 90 * 24 * time.Hour

const (
	pemTypeCert = "CERTIFICATE"
	pemTypeCSR  = "CERTIFICATE REQUEST"
)

// CA firma CSRs de Edges emitiendo certs hoja con EKU ClientAuth. La misma CA se
// expone como CertPool para inyectarla en mtls.ServerCreds(...ClientCAs) en T4:
// así el Edge enrolado conecta por mTLS contra esta plataforma con la MISMA CA.
type CA struct {
	cert    *x509.Certificate
	key     crypto.Signer
	certTTL time.Duration
}

// NewCA construye una CA firmante a partir de un cert+clave ya cargados. certTTL
// es la vida del cert hoja del Edge; si <=0 se usa DefaultEdgeCertTTL.
func NewCA(cert *x509.Certificate, key crypto.Signer, certTTL time.Duration) *CA {
	if certTTL <= 0 {
		certTTL = DefaultEdgeCertTTL
	}
	return &CA{cert: cert, key: key, certTTL: certTTL}
}

// NewDevCA genera una CA autofirmada efímera (dev/tests). No toca disco.
func NewDevCA(commonName string, caTTL, edgeCertTTL time.Duration) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("enroll: generar clave CA: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(caTTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("enroll: crear cert CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("enroll: parsear cert CA: %w", err)
	}
	return NewCA(cert, key, edgeCertTTL), nil
}

// LoadCAFromPEM construye la CA desde PEM (cert + clave PKCS#8 o EC). Cableado de
// prod/dev a partir de archivos de la plataforma/tenant.
func LoadCAFromPEM(certPEM, keyPEM []byte, edgeCertTTL time.Duration) (*CA, error) {
	cblk, _ := pem.Decode(certPEM)
	if cblk == nil || cblk.Type != pemTypeCert {
		return nil, errors.New("enroll: PEM de CA no es un CERTIFICATE")
	}
	cert, err := x509.ParseCertificate(cblk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("enroll: parsear cert CA: %w", err)
	}
	kblk, _ := pem.Decode(keyPEM)
	if kblk == nil {
		return nil, errors.New("enroll: PEM de clave CA inválido")
	}
	key, err := parsePrivateKey(kblk.Bytes)
	if err != nil {
		return nil, err
	}
	return NewCA(cert, key, edgeCertTTL), nil
}

func parsePrivateKey(der []byte) (crypto.Signer, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if signer, ok := k.(crypto.Signer); ok {
			return signer, nil
		}
		return nil, errors.New("enroll: clave CA no implementa crypto.Signer")
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	return nil, errors.New("enroll: no se pudo parsear la clave de la CA (PKCS#8 o EC)")
}

// Certificate devuelve el cert de la CA (para construir cadenas o pools).
func (c *CA) Certificate() *x509.Certificate { return c.cert }

// Pool devuelve un CertPool con la CA, listo para mtls.ServerCreds(ClientCAs) o
// como RootCAs del cliente. Es el puente enrolamiento<->mTLS con la MISMA CA.
func (c *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	return pool
}

// CAChainPEM devuelve la cadena de la CA en PEM (la que se entrega al Edge).
func (c *CA) CAChainPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: pemTypeCert, Bytes: c.cert.Raw})
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
func (c *CA) SignCSR(csrPEM []byte, tenantID string) (SignedCert, error) {
	csr, err := ParseAndVerifyCSR(csrPEM)
	if err != nil {
		return SignedCert{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return SignedCert{}, err
	}
	subject := csr.Subject
	subject.Organization = []string{tenantID}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               subject,
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(c.certTTL),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return SignedCert{}, fmt.Errorf("enroll: emitir cert de Edge: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return SignedCert{}, fmt.Errorf("enroll: parsear cert de Edge emitido: %w", err)
	}
	return SignedCert{
		EdgeCertPEM:  pem.EncodeToMemory(&pem.Block{Type: pemTypeCert, Bytes: der}),
		CAChainPEM:   c.CAChainPEM(),
		SubjectCN:    leaf.Subject.CommonName,
		SerialNumber: leaf.SerialNumber.Text(16),
		Fingerprint:  fmt.Sprintf("%x", sha256.Sum256(der)),
		NotBefore:    leaf.NotBefore,
		NotAfter:     leaf.NotAfter,
	}, nil
}

// IssueServerCert emite un cert de servidor (EKU ServerAuth) firmado por esta CA.
// Útil para levantar el endpoint mTLS del Gateway en dev/tests con la misma CA
// que valida a los Edges enrolados (se usará en T4).
func (c *CA) IssueServerCert(commonName string, dnsNames []string, ips []net.IP) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("enroll: generar clave de servidor: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(c.certTTL),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, nil, fmt.Errorf("enroll: emitir cert de servidor: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("enroll: serializar clave de servidor: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: pemTypeCert, Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// ParseAndVerifyCSR decodifica el PEM del CSR, lo parsea y verifica su firma.
// Devuelve ErrInvalidCSR (envuelto) ante cualquier fallo, sin filtrar detalles
// sensibles más allá de la causa técnica.
func ParseAndVerifyCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != pemTypeCSR {
		return nil, fmt.Errorf("%w: el PEM no es un %s", ErrInvalidCSR, pemTypeCSR)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidCSR, err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: firma no verifica: %w", ErrInvalidCSR, err)
	}
	return csr, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("enroll: generar serial: %w", err)
	}
	return serial, nil
}
