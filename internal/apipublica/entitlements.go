// Porta internal/publicapi/entitlements.go @ 9a77307 (listEntitlementsHandler) y el registro de
// C2 (internal/publicapi/publicapi.go @ 9a77307, línea 559).
//
// entitlements.go — LOS DERECHOS COMERCIALES DE LA EMPRESA DEL TOKEN (mapa §2.3, C2; ADR-0022,
// Plan 040 · T2.2).

package apipublica

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// EntitlementsDeps es lo que C2 necesita.
type EntitlementsDeps struct {
	// Entitlements es el resolver de derechos del módulo acceso NUEVO: el MISMO, con su caché de
	// 60 s, que gatea el resto de la plataforma (arquitectura.md §4: una sola caché). nil = C2
	// no se monta.
	Entitlements entitlements.Resolver
}

// MountEntitlements registra en c "GET /api/v1/entitlements" (C2), solo si d.Entitlements no
// es nil (sin él, 404 de ruta inexistente), con cadena R y permiso "entitlements.read" (ver
// Common: 401 sin token, 403 sin el permiso, ningún registro de auditoría). El 403 lo da la
// cadena, antes del handler, sin filtrar qué features existen.
//
// La respuesta:
//
//   - ListEffective recibe SIEMPRE el tenant del token (INV-8);
//   - 200 {"plan": …, "features": […], "cache_ttl_seconds": …}: el plan y las features
//     ENCENDIDAS tal como las da el resolver (en orden alfabético), y el TTL de su caché en
//     segundos enteros, truncado (60 s ⇒ 60; un TTL de menos de un segundo ⇒ 0). Solo lista
//     las efectivas, no un mapa feature→bool (D-040.3); el tenant NO viaja;
//   - un tenant sin features responde "features": [] y nunca null;
//   - un fallo de ListEffective ⇒ 500 {"error":"no se pudieron resolver los derechos del tenant"}.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil con d.Entitlements no nil hace panic AL MONTAR (ver Common).
func MountEntitlements(c *Cara, k Common, d EntitlementsDeps) {
	panic(pendiente.Implementar("apipublica.MountEntitlements"))
}
