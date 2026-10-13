package enroll_test

// Las ayudas que comparten los tests del paquete. Van en un fichero sin etiqueta de compilación
// para que cada fichero de test pueda pasar a verde por separado. Todo el material criptográfico
// se genera en el test: ninguna clave ni certificado del repositorio.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll/enrollhelpertest"
)

const (
	testTenant = "8f6f3c1e-2d55-4b0a-9a57-0c1d2e3f4a5b"
	testCode   = "ACT-Code-123"
	testEdgeCN = "edge-test-1"
)

// newCSR genera una clave P-256 y un CSR en PEM con ese CommonName y, si se da, esa Organization.
func newCSR(t *testing.T, commonName string, organization ...string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generando la clave del Edge: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName, Organization: organization},
	}, key)
	if err != nil {
		t.Fatalf("creando el CSR del test: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), key
}

// parseCertPEM decodifica un certificado en PEM.
func parseCertPEM(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, rest := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("no es un PEM de CERTIFICATE: %q", certPEM)
	}
	if len(rest) != 0 {
		t.Fatalf("sobran %d bytes tras el PEM del certificado", len(rest))
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parseando el certificado: %v", err)
	}
	return cert
}

// newDevCA es una CA efímera con certs hoja de una hora.
func newDevCA(t *testing.T) *enroll.CA {
	t.Helper()
	ca, err := enroll.NewDevCA("wapp-test-ca", time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("NewDevCA: error inesperado %v", err)
	}
	return ca
}

// spyCodeStore envuelve un CodeStore: apunta cada código que le llega, TAL CUAL, y el orden de
// las escrituras en el diario compartido.
type spyCodeStore struct {
	enroll.CodeStore
	journal *journal
	err     error // si no es nil, Consume falla con él sin tocar el store
}

func (s *spyCodeStore) Consume(ctx context.Context, code string) (string, error) {
	s.journal.add("consume:" + code)
	if s.err != nil {
		return "", s.err
	}
	return s.CodeStore.Consume(ctx, code)
}

// spyCertRepo envuelve un EdgeCertRepository: apunta cada alta en el diario y puede fallar.
type spyCertRepo struct {
	enroll.EdgeCertRepository
	journal *journal
	err     error
}

func (r *spyCertRepo) Create(ctx context.Context, rec enroll.EdgeCertRecord) error {
	r.journal.add("create:" + rec.SubjectCN)
	if r.err != nil {
		return r.err
	}
	return r.EdgeCertRepository.Create(ctx, rec)
}

// journal es el diario, en orden, de lo que el Service hizo contra sus dos almacenes.
type journal struct {
	mu      sync.Mutex
	entries []string
}

func (j *journal) add(entry string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = append(j.entries, entry)
}

func (j *journal) list() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.entries...)
}

// rig es un Service cableado con los dobles en memoria, espiados, y un código válido sembrado
// (testCode → testTenant).
type rig struct {
	svc     *enroll.Service
	ca      *enroll.CA
	codes   *enrollhelpertest.MemoriaCodeStore
	certs   *enrollhelpertest.MemoriaEdgeCertRepository
	store   *spyCodeStore
	repo    *spyCertRepo
	journal *journal
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		ca:      newDevCA(t),
		codes:   enrollhelpertest.NewMemoriaCodeStore(),
		certs:   enrollhelpertest.NewMemoriaEdgeCertRepository(),
		journal: &journal{},
	}
	r.codes.Add(testCode, testTenant, time.Now().Add(time.Hour))
	r.store = &spyCodeStore{CodeStore: r.codes, journal: r.journal}
	r.repo = &spyCertRepo{EdgeCertRepository: r.certs, journal: r.journal}
	r.svc = enroll.NewService(r.store, r.ca, r.repo)
	return r
}
