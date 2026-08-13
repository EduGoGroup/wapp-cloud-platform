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

	// leasePubKey es la pública Ed25519 (32B crudos) de la clave de firma del
	// lease (kill-switch, ADR-0007): se entrega al Edge en EnrollEdgeResponse
	// para que valide offline la firma de cada lease (Plan 055 · T4.2, D-055.5).
	// Vacía = no se publica (el gate de kill-switch queda desactivado en el
	// Edge, mismo comportamiento que hoy — H-5).
	leasePubKey []byte
}

// ServerOption configura el Server de enrolamiento al construirlo.
type ServerOption func(*Server)

// WithCloudEncPubkey inyecta la pública X25519 de cifrado de la nube que se
// publica al Edge en el enrolamiento (Plan 011 §6.4). Sin ella, la respuesta no
// incluye cloud_enc_pubkey.
func WithCloudEncPubkey(pub []byte) ServerOption {
	return func(s *Server) { s.cloudEncPubkey = pub }
}

// WithLeasePubKey inyecta la pública Ed25519 de la clave de firma del lease
// (kill-switch, ADR-0007) que se publica al Edge en el enrolamiento (Plan 055 ·
// T4.2, D-055.5). Sin ella, la respuesta no incluye lease_pubkey y el gate de
// kill-switch queda desactivado en el Edge (H-5, comportamiento actual).
func WithLeasePubKey(pub []byte) ServerOption {
	return func(s *Server) { s.leasePubKey = pub }
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
		LeasePubkey:    s.leasePubKey,
	}, nil
}
