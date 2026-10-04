package enroll_test

// Los tests de enroll.Service: el orden de sus dos escrituras (sin transacción) y qué pasa en
// cada fallo. El montaje (rig, los espías y su diario) está en helpers_test.go.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll/enrollhelpertest"
)

// requireNothingIssued afirma que Enroll falló sin entregar nada.
func requireNothingIssued(t *testing.T, edgeCert, caChain []byte, tenant string) {
	t.Helper()
	if edgeCert != nil || caChain != nil || tenant != "" {
		t.Errorf("Enroll falló pero devolvió datos: cert de %d bytes, cadena de %d bytes, tenant %q",
			len(edgeCert), len(caChain), tenant)
	}
}

// mustEnroll enrola y falla el test si no puede; devuelve el tenant.
func mustEnroll(t *testing.T, svc *enroll.Service, code string, csrPEM []byte) string {
	t.Helper()
	_, _, tenant, err := svc.Enroll(context.Background(), code, csrPEM)
	if err != nil {
		t.Fatalf("Enroll(%q): error inesperado %v", code, err)
	}
	return tenant
}

// TestService_CA_IsTheInjectedCA: la CA que firma es la que se inyectó (la misma cuyo Pool
// alimenta el mTLS del gateway).
func TestService_CA_IsTheInjectedCA(t *testing.T) {
	ca := newDevCA(t)
	svc := enroll.NewService(enrollhelpertest.NewMemoriaCodeStore(), ca, enrollhelpertest.NewMemoriaEdgeCertRepository())
	if svc.CA() != ca {
		t.Error("Service.CA() no devuelve la CA con la que se construyó")
	}
}

// TestEnroll_ValidCode_IssuesAndRecords: código válido → cert del Edge firmado por la CA, la
// cadena, el tenant, y el registro del certificado con sus metadatos.
func TestEnroll_ValidCode_IssuesAndRecords(t *testing.T) {
	r := newRig(t)
	csrPEM, _ := newCSR(t, testEdgeCN)
	edgeCertPEM, caChainPEM, tenant, err := r.svc.Enroll(context.Background(), testCode, csrPEM)
	if err != nil {
		t.Fatalf("Enroll: error inesperado %v", err)
	}
	if tenant != testTenant {
		t.Errorf("tenant = %q, quería %q (el del código)", tenant, testTenant)
	}
	if !bytes.Equal(caChainPEM, r.ca.CAChainPEM()) {
		t.Error("la cadena devuelta no es la de la CA")
	}
	leaf := parseCertPEM(t, edgeCertPEM)
	opts := x509.VerifyOptions{Roots: r.ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if _, err := leaf.Verify(opts); err != nil {
		t.Errorf("el cert emitido no valida contra la CA como cert de cliente: %v", err)
	}
	if !slices.Equal(leaf.Subject.Organization, []string{testTenant}) || leaf.Subject.CommonName != testEdgeCN {
		t.Errorf("Subject = %v, quería CN %q y Organization [%s]", leaf.Subject, testEdgeCN, testTenant)
	}

	records := r.certs.Records()
	if len(records) != 1 {
		t.Fatalf("se registraron %d certificados, quería 1", len(records))
	}
	rec := records[0]
	block, _ := pem.Decode(edgeCertPEM)
	wantFingerprint := fmt.Sprintf("%x", sha256.Sum256(block.Bytes))
	if rec.TenantID != testTenant || rec.SubjectCN != testEdgeCN || rec.Fingerprint != wantFingerprint ||
		rec.SerialNumber != leaf.SerialNumber.Text(16) {
		t.Errorf("registro = (tenant %q, cn %q, serial %q, huella %q), no corresponde al cert emitido",
			rec.TenantID, rec.SubjectCN, rec.SerialNumber, rec.Fingerprint)
	}
	if !rec.NotBefore.Equal(leaf.NotBefore) || !rec.NotAfter.Equal(leaf.NotAfter) {
		t.Errorf("validez registrada [%v, %v], quería la del cert [%v, %v]", rec.NotBefore, rec.NotAfter, leaf.NotBefore, leaf.NotAfter)
	}
	if !bytes.Equal(rec.CertPEM, edgeCertPEM) {
		t.Error("el PEM registrado no es el cert que se devolvió al Edge")
	}
}

// TestEnroll_WritesInOrder_ConsumeThenRecord: son dos escrituras SIN transacción y el orden es
// promesa: primero se consume el código y LUEGO se registra el certificado. Una vez cada una.
func TestEnroll_WritesInOrder_ConsumeThenRecord(t *testing.T) {
	r := newRig(t)
	csrPEM, _ := newCSR(t, testEdgeCN)
	mustEnroll(t, r.svc, testCode, csrPEM)
	want := []string{"consume:" + testCode, "create:" + testEdgeCN}
	if got := r.journal.list(); !slices.Equal(got, want) {
		t.Errorf("escrituras = %q, quería %q, en ese orden", got, want)
	}
}

// TestEnroll_InvalidCSR_DoesNotBurnTheCode: el CSR se verifica ANTES de tocar nada. Un CSR
// inválido no consume el código, que después sirve.
func TestEnroll_InvalidCSR_DoesNotBurnTheCode(t *testing.T) {
	r := newRig(t)
	edgeCert, caChain, tenant, err := r.svc.Enroll(context.Background(), testCode, []byte("no es un CSR"))
	if !errors.Is(err, enroll.ErrInvalidCSR) {
		t.Fatalf("error = %v, quería ErrInvalidCSR", err)
	}
	requireNothingIssued(t, edgeCert, caChain, tenant)
	if got := r.journal.list(); len(got) != 0 {
		t.Errorf("con un CSR inválido hubo escrituras: %q", got)
	}

	// El código no se quemó: con un CSR bueno, sirve.
	csrPEM, _ := newCSR(t, testEdgeCN)
	if tenant := mustEnroll(t, r.svc, testCode, csrPEM); tenant != testTenant {
		t.Errorf("tenant = %q, quería %q", tenant, testTenant)
	}
}

// TestEnroll_RejectedCode_SignsAndRecordsNothing: si el código no se consume, el centinela del
// store vuelve TAL CUAL y no se firma ni se registra nada. Vale para las tres causas del doble y
// para el ErrCodeInvalid de Postgres.
func TestEnroll_RejectedCode_SignsAndRecordsNothing(t *testing.T) {
	csrPEM, _ := newCSR(t, testEdgeCN)
	cases := []struct {
		name    string
		prepare func(t *testing.T, r *rig) string // devuelve el código a presentar
		want    error
	}{
		{"unknown code", func(*testing.T, *rig) string { return "codigo-que-nadie-sembro" }, enroll.ErrCodeNotFound},
		{"expired code", func(_ *testing.T, r *rig) string {
			r.codes.Add("vencido", testTenant, time.Now().Add(-time.Hour))
			return "vencido"
		}, enroll.ErrCodeExpired},
		{"reused code", func(t *testing.T, r *rig) string {
			if _, err := r.codes.Consume(context.Background(), testCode); err != nil {
				t.Fatalf("consumiendo el código por adelantado: %v", err)
			}
			return testCode
		}, enroll.ErrCodeUsed},
		{"store says invalid", func(_ *testing.T, r *rig) string {
			r.store.err = enroll.ErrCodeInvalid
			return testCode
		}, enroll.ErrCodeInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			code := c.prepare(t, r)
			edgeCert, caChain, tenant, err := r.svc.Enroll(context.Background(), code, csrPEM)
			if !errors.Is(err, c.want) || errors.Unwrap(err) != nil {
				t.Fatalf("error = %v, quería el centinela %v sin envolver", err, c.want)
			}
			requireNothingIssued(t, edgeCert, caChain, tenant)
			if got, want := r.journal.list(), []string{"consume:" + code}; !slices.Equal(got, want) {
				t.Errorf("escrituras = %q, quería solo %q: tras un código rechazado no se registra nada", got, want)
			}
			if n := len(r.certs.Records()); n != 0 {
				t.Errorf("se registraron %d certificados con un código rechazado", n)
			}
		})
	}
}

// TestEnroll_RecordFailure_LeavesTheCodeBurned: como hoy. Si falla el registro del certificado
// (la segunda escritura), Enroll devuelve el error, NO entrega el cert, y el código ya está
// quemado: no hay transacción que lo devuelva.
func TestEnroll_RecordFailure_LeavesTheCodeBurned(t *testing.T) {
	r := newRig(t)
	cause := errors.New("base caída")
	r.repo.err = cause
	csrPEM, _ := newCSR(t, testEdgeCN)

	edgeCert, caChain, tenant, err := r.svc.Enroll(context.Background(), testCode, csrPEM)
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, quería uno que envuelva la causa", err)
	}
	if !strings.HasPrefix(err.Error(), "enroll: persistir cert emitido: ") {
		t.Errorf("error = %q, quería el prefijo %q", err, "enroll: persistir cert emitido: ")
	}
	requireNothingIssued(t, edgeCert, caChain, tenant)

	// El código quedó consumido: con el repositorio ya sano, el reintento es «ya utilizado».
	r.repo.err = nil
	if _, _, _, err := r.svc.Enroll(context.Background(), testCode, csrPEM); !errors.Is(err, enroll.ErrCodeUsed) {
		t.Errorf("reintento tras el fallo de registro: error %v, quería ErrCodeUsed (el código ya se quemó)", err)
	}
	if n := len(r.certs.Records()); n != 0 {
		t.Errorf("quedaron %d certificados registrados, quería 0", n)
	}
}

// TestEnroll_ActivationCodeIsNotNormalized: el corpus adversario. El Service pasa el código al
// store BYTE A BYTE —sin TrimSpace, sin cambiar la caja, sin rechazar el vacío por su cuenta— y
// cada variante es, como hoy, un código desconocido. Después, el código exacto sigue sirviendo.
func TestEnroll_ActivationCodeIsNotNormalized(t *testing.T) {
	r := newRig(t)
	csrPEM, _ := newCSR(t, testEdgeCN)
	for name, code := range enrollhelpertest.AdversarialCodes(testCode) {
		t.Run(name, func(t *testing.T) {
			before := len(r.journal.list())
			edgeCert, caChain, tenant, err := r.svc.Enroll(context.Background(), code, csrPEM)
			if !errors.Is(err, enroll.ErrCodeNotFound) {
				t.Fatalf("Enroll(%q): error %v, quería ErrCodeNotFound (código desconocido)", code, err)
			}
			requireNothingIssued(t, edgeCert, caChain, tenant)
			got := r.journal.list()[before:]
			if want := []string{"consume:" + code}; !slices.Equal(got, want) {
				t.Errorf("al store llegó %q, quería %q: el código tal cual, y nada más", got, want)
			}
		})
	}
	if n := len(r.certs.Records()); n != 0 {
		t.Fatalf("se registraron %d certificados con códigos adversarios", n)
	}
	if tenant := mustEnroll(t, r.svc, testCode, csrPEM); tenant != testTenant {
		t.Errorf("el código exacto tras el corpus enroló al tenant %q, quería %q", tenant, testTenant)
	}
}
