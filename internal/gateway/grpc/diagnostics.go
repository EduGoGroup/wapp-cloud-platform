package gatewaygrpc

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/diagnostics"
)

// RequestDiagnostics empuja un DiagnosticsRequest (ADR-0023 capa 3, Plan 031 · T5) a
// la sesión dada por el stream CloudLink. El commandID lo genera y persiste el
// llamante (la solicitud pendiente) ANTES de llamar aquí, para que el bundle que
// devuelva el Edge se correlacione sin carrera. scope acota qué pide (p. ej. "full");
// el Edge ignora un scope que no reconoce (compat aditiva). No espera el bundle: este
// sube más tarde por el demux (storeDiagnosticsBundle). Un error propaga el de Push
// (ErrSessionOffline si la sesión no tiene stream vivo).
func (s *Server) RequestDiagnostics(_ context.Context, sessionID, commandID, scope string) error {
	msg := &cloudlinkv1.CloudToEdge{
		CommandId: commandID,
		SessionId: sessionID,
		Payload: &cloudlinkv1.CloudToEdge_DiagnosticsRequest{
			DiagnosticsRequest: &cloudlinkv1.DiagnosticsRequest{
				CommandId: commandID,
				SessionId: sessionID,
				Scope:     scope,
			},
		},
	}
	return s.registry.Push(sessionID, msg)
}

// storeDiagnosticsBundle correlaciona un DiagnosticsBundle recibido del Edge con su
// solicitud pendiente (por command_id, acotado por el tenant+sesión de la identidad
// mTLS del stream) y lo almacena (Plan 031 · T5). Best-effort: sin sink, sin identidad,
// sin session_id o sin bundle es un no-op silencioso; un bundle HUÉRFANO (sin solicitud
// pendiente que case — llegó tarde/duplicado, venció, o vino de otra sesión) se IGNORA
// con log y NO tumba el stream. El bundle es material operativo saneado por el Edge (gate
// ZK, T8): aquí solo se persiste OPACO (CERO llaves/DEK/credenciales/PII).
func (s *Server) storeDiagnosticsBundle(ctx context.Context, cc connCtx, db *cloudlinkv1.DiagnosticsBundle) {
	if s.diag == nil || !cc.hasIdentity || cc.sessionID == "" || db == nil {
		return
	}
	found, err := s.diag.SaveBundle(ctx, cc.tenantID, cc.sessionID, db.GetCommandId(), diagnostics.Bundle{
		LogTail:        db.GetLogTail(),
		GoroutineDump:  db.GetGoroutineDump(),
		SubsystemsJSON: db.GetSubsystemsJson(),
	})
	if err != nil {
		s.log.Error("diagnóstico: persistir bundle", "error", err,
			"edge_id", cc.edgeID, "session_id", cc.sessionID, "command_id", db.GetCommandId())
		return
	}
	if !found {
		s.log.Warn("diagnóstico: bundle sin solicitud pendiente; ignorado",
			"session_id", cc.sessionID, "command_id", db.GetCommandId())
	}
}
