// Porta internal/gateway/enroll/server.go @ 8896f13

package enroll

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Server implementa cloudlinkv1.EnrollmentServer: termina el RPC EnrollEdge sobre
// el Service de dominio y mapea sus errores a códigos gRPC. Se sirve sobre TLS de
// servidor (el Edge aún no tiene cert); el cert emitido le permite después abrir
// Connect con mTLS contra la MISMA CA.
type Server struct {
	cloudlinkv1.UnimplementedEnrollmentServer
}

// ServerOption configura el Server de enrolamiento al construirlo.
type ServerOption func(*Server)

// WithCloudEncPubkey inyecta la pública X25519 de cifrado de la nube que se
// publica al Edge en el enrolamiento (Plan 011 §6.4). Sin ella, la respuesta no
// incluye cloud_enc_pubkey.
func WithCloudEncPubkey(pub []byte) ServerOption {
	panic(pendiente.Implementar("enroll.WithCloudEncPubkey"))
}

// WithLeasePubKey inyecta la pública Ed25519 de la clave de firma del lease
// (kill-switch, ADR-0007) que se publica al Edge en el enrolamiento (Plan 055 ·
// T4.2, D-055.5). Sin ella, la respuesta no incluye lease_pubkey y el gate de
// kill-switch queda desactivado en el Edge (H-5, comportamiento actual).
func WithLeasePubKey(pub []byte) ServerOption {
	panic(pendiente.Implementar("enroll.WithLeasePubKey"))
}

// NewServer construye el servidor de enrolamiento sobre el Service y el logger.
// Sin opciones no publica ninguna clave en la respuesta.
func NewServer(svc *Service, log logger.Logger, opts ...ServerOption) *Server {
	panic(pendiente.Implementar("enroll.NewServer"))
}

// Register registra este servidor en el ServiceRegistrar gRPC dado.
func (s *Server) Register(reg grpc.ServiceRegistrar) {
	panic(pendiente.Implementar("enroll.Server.Register"))
}

// EnrollEdge valida el código de activación y el CSR, y devuelve el cert de Edge
// firmado por la CA. Mapeo de errores: CSR ausente/inválido -> InvalidArgument;
// código inválido/expirado/usado -> PermissionDenied; cualquier otro -> Internal.
// No se filtran secretos ni la causa exacta del rechazo del código.
//
// Textos, literales: "csr_pem requerido" y "CSR inválido" (InvalidArgument),
// "código de activación inválido" (PermissionDenied, el MISMO para
// desconocido, expirado, usado e inválido) y "enrolamiento falló" (Internal).
// El CSR vacío se rechaza ANTES de tocar el código (no lo quema). El código de
// activación llega al Service tal cual, sin normalizar. La respuesta lleva
// cloud_enc_pubkey y lease_pubkey solo si se configuraron con sus opciones.
func (s *Server) EnrollEdge(ctx context.Context, req *cloudlinkv1.EnrollEdgeRequest) (*cloudlinkv1.EnrollEdgeResponse, error) {
	panic(pendiente.Implementar("enroll.Server.EnrollEdge"))
}
