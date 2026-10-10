// Porta internal/flujos/admin/doc.go @ 724f3035

// Package admin contiene los handlers HTTP del motor de flujos: publicar una
// definición (DefinitionHandler), abrir una conversación por API (StartHandler) y el
// CRUD de reglas de disparo (CreateTriggerHandler, ListTriggersHandler,
// DeleteTriggerHandler), con el puerto que les dice si un flujo tiene contenido
// durable (DurableFlowChecker).
//
// El paquete NO monta rutas: devuelve http.Handler y quien arranca decide el patrón y
// el mux. El `Register` del paquete viejo no se reconstruye (D-F8-2: solo lo llamaban
// tests, deuda D-7), y `sessions.go` tampoco vive aquí.
//
// SEGURIDAD (Plan 018 · T4): todos se montan DETRÁS de Authenticate →
// RequirePermission. El tenant_id sale de httpapi.IdentityFromContext (INV-8), NUNCA
// del cuerpo: un tenant_id en el JSON se IGNORA. La auditoría la aporta el
// AuditMiddleware.
//
// Dos formas de respuesta, comunes a todos los handlers y observables:
//
//   - un error sale por http.Error: texto plano (`Content-Type: text/plain;
//     charset=utf-8`), el mensaje LITERAL y un salto de línea final;
//   - un éxito con cuerpo sale como JSON (`Content-Type: application/json`), sin salto
//     de línea final. Si la escritura del cuerpo falla, el error se descarta (deuda
//     D-16, `writeJSON`: se porta como está, D-F8-6).
package admin
