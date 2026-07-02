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

	// cloudEncPubkey es la pública X25519 (32B) del par de cifrado de tránsito de
	// la nube (Plan 011 §10.F): se entrega al Edge en EnrollEdgeResponse para que
	// selle los campos sensibles del ingreso. Vacía = no se publica (compat §10.H:
	// el Edge sube en claro, el mTLS sigue protegiendo el canal).
	cloudEncPubkey []byte
}

// ServerOption configura el Server de enrolamiento al construirlo.
type ServerOption func(*Server)

// WithCloudEncPubkey inyecta la pública X25519 de cifrado de la nube que se
// publica al Edge en el enrolamiento (Plan 011 §6.4). Sin ella, la respuesta no
// incluye cloud_enc_pubkey.
func WithCloudEncPubkey(pub []byte) ServerOption {
	return func(s *Server) { s.cloudEncPubkey = pub }
}

// NewServer construye el servidor de enrolamiento sobre el Service y el logger.
func NewServer(svc *Service, log logger.Logger, opts ...ServerOption) *Server {
	s := &Server{svc: svc, log: log}
	for _, opt := range opts {
		opt(s)
	}
	return s
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
		EdgeCertPem:    edgeCertPEM,
		CaChainPem:     caChainPEM,
		TenantId:       tenantID,
		CloudEncPubkey: s.cloudEncPubkey,
	}, nil
}
