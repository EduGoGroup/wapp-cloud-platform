// Package publicapi expone la cara PÚBLICA de wApp para terceros (Plan 018 · T5):
// las rutas /api/v1 de operación (enviar mensajes, publicar/leer/arrancar flujos)
// sobre el MISMO listener :8103 y el MISMO middleware que la fase IAM (T3) ya
// construyó. No reimplementa lógica de negocio: envuelve el gateway CloudLink
// (SendText), el motor de flujos (InsertDefinition/Start) y el store de
// definiciones con autenticación M2M (api-key/service-token) + autorización por
// scope (glob RBAC) + auditoría de escrituras.
//
// SEGURIDAD (INV-8, R6): TODA operación se acota al tenant de la Identity del token
// (httpapi.IdentityFromContext), NUNCA a un tenant del cuerpo. Un tercero con
// api-key del tenant A no puede tocar recursos del tenant B: las lecturas/escrituras
// filtran por tenant en el store, y el envío por session_id valida que la sesión
// pertenezca al tenant (sessionBelongsToTenant). Zero-knowledge: jamás se loguean
// api-keys/secretos ni se audita PII (AuditMiddleware fija action/resource opacos).
package publicapi

import (
	"context"
	"encoding/json"
	"net/http"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	flowadmin "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// MessageSender empuja un SendText hacia una sesión viva del Edge y espera su Ack.
// Lo satisface *gatewaygrpc.Server (mismo método que reusa /admin/messages/send).
type MessageSender interface {
	SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error)
}

// SessionLister lista las sesiones (durables) de un tenant. Lo satisface
// *fleet.PostgresRepository (su método List). Se usa para el aislamiento por tenant
// del envío por session_id (INV-8): una sesión que no aparece en la lista del tenant
// del token NO es suya y el envío se rechaza (404).
type SessionLister interface {
	List(ctx context.Context, tenantID string) ([]fleet.Session, error)
}

// FlowStore agrupa las operaciones sobre definiciones de flujo que consume la API
// pública. Todas son tenant-scoped. Lo satisface *store.PostgresRepository (y el
// MemoryRepository en tests). InsertDefinition encaja además con
// flowadmin.DefinitionStore (se reusa DefinitionHandler tal cual).
type FlowStore interface {
	InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (version int, err error)
	LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error)
	ListDefinitions(ctx context.Context, tenantID string) ([]store.FlowSummary, error)
}

// AuditReader lista la bitácora de auditoría de un tenant (Plan 018 · T10, R11).
// Lo satisface *iamusecase.AuditService (ListAudit). Se declara aquí para no
// acoplar publicapi al usecase concreto.
type AuditReader interface {
	ListAudit(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error)
}

// Deps agrupa las dependencias de negocio que la API pública envuelve. Se
// construyen una sola vez en cmd/server (los MISMOS objetos que sirven a
// gRPC/admin): esta capa solo añade transporte + autorización pública.
type Deps struct {
	Sender   MessageSender              // gateway CloudLink (SendText)
	Sessions SessionLister              // fleet: sesiones del tenant (aislamiento)
	Flows    FlowStore                  // store de definiciones (CRUD lectura/alta)
	Modules  flowadmin.ModuleTypeSource // tipos de nodo de módulos (validación del alta)
	Starter  flowadmin.Starter          // motor de flujos (arranque de conversación)
	Media    PresignUploader            // presign R2 (upload-url, Plan 017/018 · T6)
	Content  TenantContentStore         // blobs JSONB por-tenant (tenant_content, T6)
	Audit    AuditReader                // bitácora de auditoría (GET /api/v1/audit, T10)
	Triggers flowadmin.TriggerStore     // reglas de disparo (CRUD /api/v1/triggers, Plan 019 T5)
	// SessionRoles administra el rol bot|passive de una sesión (Plan 020 · T1).
	// Lo satisface *fleet.PostgresRepository (SetRole). nil ⇒ no se monta la ruta.
	SessionRoles flowadmin.SessionRoleStore
	// SessionStatus administra el estatus offline|loggedout de una sesión, para
	// retirar/limpiar un zombie (Plan 020 · T3). Lo satisface *fleet.PostgresRepository
	// (SetState). nil ⇒ no se monta la ruta.
	SessionStatus flowadmin.SessionStatusStore
}

// Register monta las rutas /api/v1 de operación pública en el mux del listener
// público (:8103), reutilizando el middleware de T3 (mw) y el auditor de T4. Cada
// ruta pasa por Authenticate → RequirePermission(scope); las ESCRITURAS añaden
// AuditMiddleware (las lecturas no se auditan: son idempotentes y sin efecto). Los
// patrones método+ruta (Go 1.22+) devuelven 405 automáticamente ante otro método y
// extraen {id} con r.PathValue. No colisiona con las rutas IAM (/api/v1/auth,
// /api/v1/users, …) ya montadas en el mismo mux.
//
// Extensible: T6 añadirá /api/v1/media/upload-url y /api/v1/tenant-content aquí
// mismo (mismo patrón protect/protectRead), sin tocar lo existente.
func Register(mux *http.ServeMux, d Deps, mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger) {
	// Envío de mensajes (escritura auditada). Reusa el gateway; añade el guardia
	// session→tenant que /admin/messages/send (T4) no tenía.
	mux.Handle("POST /api/v1/messages", protect(mw, auditor, log,
		"messages.send", "message", messagesHandler(d.Sender, d.Sessions)))

	// Publicar definición de flujo (escritura auditada). Reusa TAL CUAL el handler
	// de /admin/flows: ya toma el tenant del token y valida el esquema.
	mux.Handle("POST /api/v1/flows", protect(mw, auditor, log,
		"flows.create", "flow", flowadmin.DefinitionHandler(d.Flows, d.Modules)))

	// Listar / leer definiciones (lecturas, sin auditoría).
	mux.Handle("GET /api/v1/flows", protectRead(mw,
		"flows.read", listFlowsHandler(d.Flows)))
	mux.Handle("GET /api/v1/flows/{id}", protectRead(mw,
		"flows.read", getFlowHandler(d.Flows)))

	// Arrancar una conversación de un flujo (escritura auditada). flow_id va en la
	// ruta (design.md §8); el resto (session_id, contacto) en el cuerpo. Reusa el
	// motor de flujos (Starter) que también sirve /admin/flows/start.
	mux.Handle("POST /api/v1/flows/{id}/start", protect(mw, auditor, log,
		"flows.start", "flow", startFlowHandler(d.Starter)))

	// Media por API (escritura auditada, R7): presigna una URL PUT de corta vida
	// para subir a R2 un archivo (PDF/imagen) que luego se referencia en un flujo
	// (nodo media / tenant_content). Reusa el PresignClient del Plan 017; el objeto
	// se namespacea por tenant (INV-8). CERO PII en la auditoría (action/resource).
	mux.Handle("POST /api/v1/media/upload-url", protect(mw, auditor, log,
		"media.upload", "media", uploadURLHandler(d.Media)))

	// CRUD de contenido dinámico por-tenant (tenant_content, R7): blobs JSONB que
	// alimentan el adapter content.JSON del Motor (source:json,ref) o una ref de
	// media por-tenant. Escrituras auditadas (content.write); lecturas sin auditoría
	// (content.read). Todo acotado al tenant del token (INV-8). Sin cambios en el Motor.
	mux.Handle("PUT /api/v1/tenant-content/{ref}", protect(mw, auditor, log,
		"content.write", "tenant_content", upsertTenantContentHandler(d.Content)))
	mux.Handle("POST /api/v1/tenant-content/{ref}", protect(mw, auditor, log,
		"content.write", "tenant_content", upsertTenantContentHandler(d.Content)))
	mux.Handle("DELETE /api/v1/tenant-content/{ref}", protect(mw, auditor, log,
		"content.write", "tenant_content", deleteTenantContentHandler(d.Content)))
	mux.Handle("GET /api/v1/tenant-content", protectRead(mw,
		"content.read", listTenantContentHandler(d.Content)))
	mux.Handle("GET /api/v1/tenant-content/{ref}", protectRead(mw,
		"content.read", getTenantContentHandler(d.Content)))

	// CRUD de reglas de disparo (Plan 019 · T5): keyword/fallback/escape por-tenant
	// que alimentan al ConfigResolver del Motor. Escrituras auditadas
	// (triggers.create/delete); lectura sin auditoría (triggers.read). Todo acotado
	// al tenant del token (INV-8); reusa los MISMOS handlers que /admin/triggers.
	if d.Triggers != nil {
		mux.Handle("POST /api/v1/triggers", protect(mw, auditor, log,
			"triggers.create", "trigger", flowadmin.CreateTriggerHandler(d.Triggers)))
		mux.Handle("GET /api/v1/triggers", protectRead(mw,
			"triggers.read", flowadmin.ListTriggersHandler(d.Triggers)))
		mux.Handle("DELETE /api/v1/triggers/{id}", protect(mw, auditor, log,
			"triggers.delete", "trigger", flowadmin.DeleteTriggerHandler(d.Triggers)))
	}

	// Rol de sesión bot|passive (Plan 020 · T1): una sesión passive escucha/transporta
	// pero NO dispara triggers ni auto-responde. Escritura auditada (sessions.write),
	// acotada al tenant del token (INV-8); reusa el MISMO handler que /admin/sessions.
	if d.SessionRoles != nil {
		mux.Handle("POST /api/v1/sessions/{id}/role", protect(mw, auditor, log,
			"sessions.write", "session", flowadmin.SetSessionRoleHandler(d.SessionRoles)))
	}

	// Estatus de sesión (Plan 020 · T3): retirar/limpiar un zombie (loggedout) o
	// dejar offline. Escritura auditada (sessions.write), acotada al tenant del token
	// (INV-8); reusa el MISMO handler que /admin/sessions/{id}/status.
	if d.SessionStatus != nil {
		mux.Handle("POST /api/v1/sessions/{id}/status", protect(mw, auditor, log,
			"sessions.write", "session", flowadmin.SetSessionStatusHandler(d.SessionStatus)))
	}

	// Lectura de la bitácora de auditoría (Plan 018 · T10, R11). Paginada, acotada
	// al tenant del token (INV-8); scope audit.read (o *.read del rol viewer).
	// Lectura sin auditoría (no tiene efecto). Los eventos ya son OPACOS (CERO PII).
	if d.Audit != nil {
		mux.Handle("GET /api/v1/audit", protectRead(mw,
			"audit.read", listAuditHandler(d.Audit)))
	}
}

// protect compone la cadena de una ESCRITURA pública: Authenticate → identidad del
// token; RequirePermission(perm) → scope glob; AuditMiddleware → bitácora sin PII
// (action=perm, resource). Espeja adminHandler de cmd/server (T4) para /api/v1.
func protect(mw *httpapi.Middleware, auditor httpapi.AuditRecorder, log sharedlogger.Logger, perm, resource string, h http.Handler) http.Handler {
	h = httpapi.AuditMiddleware(auditor, perm, resource, log)(h)
	h = mw.RequirePermission(perm)(h)
	return mw.Authenticate(h)
}

// protectRead compone la cadena de una LECTURA pública: Authenticate →
// RequirePermission(perm). No audita (lectura sin efecto).
func protectRead(mw *httpapi.Middleware, perm string, h http.Handler) http.Handler {
	return mw.Authenticate(mw.RequirePermission(perm)(h))
}

// writeJSON serializa v como JSON con el código dado (mismo patrón que
// httpapi/flujos-admin). Ante fallo de codificación responde 500.
func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "codificando respuesta", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if _, werr := w.Write(body); werr != nil {
		return
	}
}
