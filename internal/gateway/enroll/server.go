package enroll

import (
	"context"
	"errors"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implementa cloudlinkv1.EnrollmentServer: termina el RPC EnrollEdge sobre
// el Service de dominio y mapea sus errores a códigos gRPC. Se sirve sobre TLS de
// servidor (el Edge aún no tiene cert); el cert emitido le permite después abrir
// Connect con mTLS contra la MISMA CA.
type Server struct {
	cloudlinkv1.UnimplementedEnrollmentServer

	svc *Service
	log logger.Logger
}

// NewServer construye el servidor de enrolamiento sobre el Service y el logger.
func NewServer(svc *Service, log logger.Logger) *Server {
	return &Server{svc: svc, log: log}
}

// Register registra este servidor en el ServiceRegistrar gRPC dado.
func (s *Server) Register(reg grpc.ServiceRegistrar) {
	cloudlinkv1.RegisterEnrollmentServer(reg, s)
}

// EnrollEdge valida el código de activación y el CSR, y devuelve el cert de Edge
// firmado por la CA. Mapeo de errores: CSR ausente/inválido -> InvalidArgument;
// código inválido/expirado/usado -> PermissionDenied; cualquier otro -> Internal.
// No se filtran secretos ni la causa exacta del rechazo del código.
func (s *Server) EnrollEdge(ctx context.Context, req *cloudlinkv1.EnrollEdgeRequest) (*cloudlinkv1.EnrollEdgeResponse, error) {
	if len(req.GetCsrPem()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "csr_pem requerido")
	}

	edgeCertPEM, caChainPEM, tenantID, err := s.svc.Enroll(ctx, req.GetActivationCode(), req.GetCsrPem())
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidCSR):
			return nil, status.Error(codes.InvalidArgument, "CSR inválido")
		case errors.Is(err, ErrCodeNotFound),
			errors.Is(err, ErrCodeExpired),
			errors.Is(err, ErrCodeUsed),
			errors.Is(err, ErrCodeInvalid):
			return nil, status.Error(codes.PermissionDenied, "código de activación inválido")
		default:
			s.log.Error("enrolamiento falló", "error", err)
			return nil, status.Error(codes.Internal, "enrolamiento falló")
		}
	}

	s.log.Info("Edge enrolado", "tenant_id", tenantID)
	return &cloudlinkv1.EnrollEdgeResponse{
		EdgeCertPem: edgeCertPEM,
		CaChainPem:  caChainPEM,
		TenantId:    tenantID,
	}, nil
}
