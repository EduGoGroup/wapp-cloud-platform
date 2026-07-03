package httpapi

import (
	"context"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// AuditRecorder registra un evento de auditoría. Es el subconjunto de
// in.Auditor (solo Record) que necesita el middleware; lo satisface
// *usecase.AuditService. Se declara aquí para que httpapi no dependa del
// usecase concreto (solo del DTO in.AuditInput, ya parte del contrato de
// entrada del IAM).
type AuditRecorder interface {
	Record(ctx context.Context, in in.AuditInput) error
}

// statusRecorder captura el código de estado que el handler escribió, para
// clasificar el resultado de la auditoría (success/failure) sin leer el cuerpo.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// AuditMiddleware registra en audit_events cada operación /admin/* que atraviesa.
// Debe montarse DESPUÉS de Authenticate (necesita la Identity en el contexto para
// el tenant/actor). REGLA DURA (INV-5, zero-knowledge): CERO PII. action/resource
// son etiquetas FIJAS de la ruta (p. ej. "flows.create"/"flow"); Actor es el
// subject OPACO del token (user_id/client_id); Meta lleva solo el status HTTP.
// NUNCA se registra el cuerpo del request, número, JID ni secreto alguno.
//
// El fallo al auditar NO altera la respuesta al cliente (best-effort): se loguea a
// Warn (sin PII) y se sigue. El tenant sale SIEMPRE de la Identity (INV-8).
func AuditMiddleware(rec AuditRecorder, action, resource string, log sharedlogger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sr, r)

			if rec == nil {
				return
			}
			id, _ := IdentityFromContext(r.Context())
			result := "success"
			if sr.status >= http.StatusBadRequest {
				result = "failure"
			}
			if err := rec.Record(r.Context(), in.AuditInput{
				TenantID: id.TenantID,
				Actor:    id.Subject,
				Action:   action,
				Resource: resource,
				Result:   result,
				Meta:     map[string]any{"status": sr.status},
			}); err != nil && log != nil {
				// Sin PII: solo la etiqueta de la acción y el error del repositorio.
				log.Warn("no se pudo registrar la auditoría admin", "action", action, "error", err)
			}
		})
	}
}
