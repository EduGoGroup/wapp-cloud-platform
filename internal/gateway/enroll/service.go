package enroll

import (
	"context"
	"fmt"
)

// Service orquesta el enrolamiento del lado servidor: valida el CSR, consume el
// código de un solo uso, firma con la CA y persiste el cert emitido. Es agnóstico
// al transporte; el mapeo a códigos gRPC vive en Server (EnrollEdge).
type Service struct {
	codes CodeStore
	ca    *CA
	certs EdgeCertRepository
}

// NewService cablea el store de códigos, la CA firmante y el repo de certs. La CA
// inyectada debe ser la misma cuyo Pool() alimenta mtls.ServerCreds(ClientCAs) en
// T4.
func NewService(codes CodeStore, ca *CA, certs EdgeCertRepository) *Service {
	return &Service{codes: codes, ca: ca, certs: certs}
}

// CA expone la CA firmante (para construir el endpoint mTLS con la misma CA).
func (s *Service) CA() *CA { return s.ca }

// Enroll valida, firma y persiste. Orden deliberado: primero verifica el CSR (si
// es inválido NO se quema el código), luego consume el código de un solo uso, a
// continuación emite el cert y por último lo persiste. Devuelve el cert del Edge
// y la cadena de la CA (PEM) más el tenantID, o un error sentinela
// (ErrInvalidCSR / ErrCode*).
func (s *Service) Enroll(ctx context.Context, activationCode string, csrPEM []byte) (edgeCertPEM, caChainPEM []byte, tenantID string, err error) {
	if _, err = ParseAndVerifyCSR(csrPEM); err != nil {
		return nil, nil, "", err
	}
	tenantID, err = s.codes.Consume(ctx, activationCode)
	if err != nil {
		return nil, nil, "", err
	}
	signed, err := s.ca.SignCSR(csrPEM, tenantID)
	if err != nil {
		return nil, nil, "", err
	}
	rec := EdgeCertRecord{
		TenantID:     tenantID,
		SubjectCN:    signed.SubjectCN,
		SerialNumber: signed.SerialNumber,
		Fingerprint:  signed.Fingerprint,
		NotBefore:    signed.NotBefore,
		NotAfter:     signed.NotAfter,
		CertPEM:      signed.EdgeCertPEM,
	}
	if err = s.certs.Create(ctx, rec); err != nil {
		return nil, nil, "", fmt.Errorf("enroll: persistir cert emitido: %w", err)
	}
	return signed.EdgeCertPEM, signed.CAChainPEM, tenantID, nil
}
