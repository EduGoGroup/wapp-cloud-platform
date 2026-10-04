// Porta internal/bootstrap/arranque/http.go @ 9a77307 (líneas 80-171: el registro de A1–A7 que
// el arranque viejo hacía fuera de publicapi) e internal/iam/transport/http/auth.go @ 9a77307
// (Register, líneas 49-50).
//
// auth.go — LAS SIETE RUTAS DE AUTENTICACIÓN Y EMPRESA (mapa §2.1, A1–A7). En la spec FX era
// `autenticacion.go`, `DepsAutenticacion` y `MontarAutenticacion` (05 E-11).
//
// Son las rutas de quien TODAVÍA no tiene empresa en su token, o ni siquiera token: por eso
// ninguna lleva RequirePermission ni auditoría, y por eso viven aparte de las áreas de negocio.

package apipublica

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// AuthDeps son los puertos de A1–A7, todos del módulo acceso NUEVO. Cada campo dice qué ruta
// enciende; los que pueden faltar lo dicen.
type AuthDeps struct {
	// Verifier inspecciona Context Tokens (A1). Obligatorio.
	Verifier in.TokenVerifier
	// Exchanger canjea Identity Tokens por Context Tokens (A2). Puede ser nil: es el modo dual
	// APAGADO (sin WAPP_IDENTITY_JWKS_URL); A2 sigue montada y responde 503.
	Exchanger in.Exchanger
	// Redeemer canjea una invitación de un solo uso (A4).
	Redeemer in.InvitationRedeemer
	// TenantSelector y TenantLister son las dos mitades de la empresa activa (A5 y A6). En el
	// arranque son el MISMO servicio.
	TenantSelector in.ActiveTenantSelector
	TenantLister   in.TenantLister
	// SignupRequests siembra la solicitud de acceso pendiente del alta pública (A7). Obligatorio
	// si M2M no es nil.
	SignupRequests platformadmin.AccessRequestStore
	// M2M es el cliente de identity con el que el alta pública registra a la persona (A7). nil =
	// despliegue sin WAPP_IDENTITY_API_KEY: A7 queda montada con un 503 fijo.
	M2M out.IdentityM2MClient
	// SignupTrustProxy decide la IP del limitador de A7 (cfg.RateLimit.TrustProxy): con false,
	// siempre la de socket; con true, la primera de X-Forwarded-For (ver SignupHandler).
	SignupTrustProxy bool
}

// MountAuth registra en c las rutas A1–A7 con los patrones EXACTOS del mapa §2.1 (byte a byte:
// son la etiqueta `route` de las métricas) y sus condiciones de montaje:
//
//   - A1 "/api/v1/auth/verify" y A2 "/api/v1/auth/exchange", SIN método en el patrón (el método
//     lo comprueba cada handler), SIEMPRE, públicas (sin Authenticate): los handlers de
//     iamhttp.NewAuthHandler(d.Verifier, d.Exchanger, k.Log). Con d.Exchanger nil, A2 responde
//     503 {"error":"modo dual apagado: identity no está configurado en este despliegue"};
//   - A3 "/api/v1/auth/whoami", sin método, SIEMPRE: k.MW.Authenticate(httpapi.WhoAmIHandler()).
//     Un token SIN empresa la atraviesa (200 con "tenant_id" vacío, D-056.12); sin token, 401;
//   - A4 "POST /api/v1/invitations/accept", solo si d.Redeemer no es nil;
//   - A5 "POST /api/v1/auth/active-tenant" y A6 "GET /api/v1/auth/tenants", las DOS juntas y
//     solo si d.TenantSelector y d.TenantLister no son nil (en el arranque viejo nacían del
//     mismo servicio, mapa §2.1 «Monta si»);
//   - A7 "POST /api/v1/signup", SIEMPRE, pública, con una de dos ramas según d.M2M.
//
// A3–A6 llevan k.MW.Authenticate A SECAS (cadena «A»): sin RequirePermission —quien las
// necesita es justo quien no tiene empresa ni grants en su token, y cualquier permiso le daría
// 403— y sin AuditMiddleware —la bitácora es por tenant y quien llama no trae ninguno; el
// rastro del canje queda en la invitación y el de la elección en user_active_tenant—. Así: sin
// token, 401; con un token SIN empresa, la petición llega al puerto. Ninguna de las siete deja
// registro de auditoría ni línea de access-log (como en el arranque viejo).
//
// A7: con d.M2M no nil, platformadmin.SignupHandler(d.SignupRequests, d.M2M, limitador,
// d.SignupTrustProxy, k.Log), con un limitador PROPIO de esta ruta y de esta llamada a
// MountAuth —ratelimit.NewLimiter(rate.Every(time.Minute), 5): por IP, una alta por minuto con
// ráfaga de 5 (A-06a)—, distinto del limitador público del arranque: la sexta alta seguida
// desde la misma IP es 429 «demasiadas solicitudes desde esta IP». Con d.M2M nil (C-02), un
// handler fijo que, sea cual sea el cuerpo, responde 503 con el texto plano de http.Error
// «registro no disponible» (cuerpo "registro no disponible\n") sin tocar d.SignupRequests; y,
// si k.Log no es nil, MountAuth deja al montar un Warn con el mensaje literal
// "POST /api/v1/signup: falta WAPP_IDENTITY_API_KEY; el registro público responde 503 (servicio no disponible)".
//
// Fallos de cableado, con panic AL MONTAR y un mensaje propio: k.MW nil (ver Common), d.Verifier
// nil, o d.M2M no nil con d.SignupRequests nil.
//
// Solapes (mapa §4.2): A4 convive en la misma cara con "DELETE /api/v1/invitations/{id}" (B14,
// MountRolePlane) sin conflicto; las dos se mudan juntas en F2.
func MountAuth(c *Cara, k Common, d AuthDeps) {
	panic(pendiente.Implementar("apipublica.MountAuth"))
}
