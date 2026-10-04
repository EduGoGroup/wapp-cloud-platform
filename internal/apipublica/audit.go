// Porta internal/publicapi/audit.go @ 9a77307 (listAuditHandler, toAuditDTOs, parseIntQuery) y
// el registro de C1 (internal/publicapi/publicapi.go @ 9a77307, línea 604).
//
// audit.go — LA BITÁCORA DE LA EMPRESA DEL TOKEN (mapa §2.3, C1).

package apipublica

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// AuditReader es el puerto de lectura de la bitácora que consume C1: el subconjunto de
// in.Auditor (módulo acceso nuevo) que hace falta aquí. En el arranque es el MISMO servicio que
// graba las escrituras (Common.Auditor).
type AuditReader interface {
	// ListAudit devuelve los eventos de tenantID, los más recientes primero, paginados.
	ListAudit(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error)
}

// AuditDeps es lo que C1 necesita.
type AuditDeps struct {
	// Audit lee la bitácora. nil = C1 no se monta.
	Audit AuditReader
}

// MountAudit registra en c "GET /api/v1/audit" (C1), solo si d.Audit no es nil (sin él, 404 de
// ruta inexistente), con cadena R y permiso "audit.read" (ver Common: 401 sin token, 403 sin el
// permiso o con un token sin empresa, y ningún registro de auditoría).
//
// La respuesta:
//
//   - el tenant que recibe ListAudit es SIEMPRE el del token (INV-8); un tenant en la query no
//     cuenta;
//   - paginación por ?limit y ?offset: enteros no negativos; si faltan, no son enteros o son
//     negativos valen 100 y 0; un limit por encima de 500 se recorta a 500; un 0 explícito es 0;
//   - 200 {"events":[…]}, en el orden que da el puerto, y [] (nunca null) si no hay eventos.
//     Cada evento: "id", "actor", "action", "resource", "result", "meta" (se omite si está
//     vacío) y "at" (UTC, RFC 3339 con segundos: "2006-01-02T15:04:05Z"). El tenant NO viaja
//     (es el del token). Todo es opaco: CERO PII (INV-5);
//   - un fallo de ListAudit ⇒ 500 {"error":"no se pudo listar la auditoría"}.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil con d.Audit no nil hace panic AL MONTAR (ver Common).
func MountAudit(c *Cara, k Common, d AuditDeps) {
	panic(pendiente.Implementar("apipublica.MountAudit"))
}
