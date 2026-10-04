// Porta internal/gateway/enroll/service.go @ 8896f13

package enroll

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Service orquesta el enrolamiento del lado servidor: valida el CSR, consume el
// código de un solo uso, firma con la CA y persiste el cert emitido. Es agnóstico
// al transporte; el mapeo a códigos gRPC vive en Server (EnrollEdge).
type Service struct{}

// NewService cablea el store de códigos, la CA firmante y el repo de certs. La CA
// inyectada debe ser la misma cuyo Pool() alimenta mtls.ServerCreds(ClientCAs) en
// T4. No valida sus argumentos.
func NewService(codes CodeStore, ca *CA, certs EdgeCertRepository) *Service {
	panic(pendiente.Implementar("enroll.NewService"))
}

// CA expone la CA firmante (para construir el endpoint mTLS con la misma CA).
func (s *Service) CA() *CA {
	panic(pendiente.Implementar("enroll.Service.CA"))
}

// Enroll valida, firma y persiste. Orden deliberado: primero verifica el CSR (si
// es inválido NO se quema el código), luego consume el código de un solo uso, a
// continuación emite el cert y por último lo persiste. Devuelve el cert del Edge
// y la cadena de la CA (PEM) más el tenantID, o un error sentinela
// (ErrInvalidCSR / ErrCode*).
//
// Promesas:
//   - CSR inválido ⇒ ErrInvalidCSR y NO se consume el código;
//   - el error de Consume vuelve TAL CUAL (el centinela del store, sin
//     envolver) y entonces ni se firma ni se registra nada;
//   - activationCode se pasa al store SIN normalizar (ni TrimSpace, ni
//     mayúsculas, ni rechazo del vacío): se compara tal cual;
//   - son DOS escrituras SIN transacción y su orden es promesa: primero se
//     consume el código y LUEGO se registra el certificado. Si registrar
//     falla, devuelve "enroll: persistir cert emitido: …" y el código YA está
//     quemado (no se devuelve), como hoy;
//   - en cualquier error devuelve nil, nil y "".
func (s *Service) Enroll(ctx context.Context, activationCode string, csrPEM []byte) (edgeCertPEM, caChainPEM []byte, tenantID string, err error) {
	panic(pendiente.Implementar("enroll.Service.Enroll"))
}
