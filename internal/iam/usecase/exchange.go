package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	identityauth "github.com/EduGoGroup/identity-shared/auth"
	identityjwt "github.com/EduGoGroup/identity-shared/auth/jwt"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// Aplicaciones de wApp en el catálogo de identity (identity ADR-0001: el
// namespace es `<ecosistema>.<app>`). Son las DOS únicas para las que wApp
// canjea: un Identity Token emitido para una aplicación de otro ecosistema está
// perfectamente firmado y no vale aquí.
const (
	// SystemWappBFF es la aplicación web de wApp (guardian-bff).
	SystemWappBFF = "wapp.bff"
	// SystemWappEdge es la consola local del operador, que el Edge relaya.
	SystemWappEdge = "wapp.edge"
	// SystemWappPlatform es la consola de plataforma de wApp (wapp-platform-console).
	SystemWappPlatform = "wapp.platform"
)

// acceptedSystems son las aplicaciones cuyo Identity Token se canjea, en el
// orden en que se prueban.
var acceptedSystems = []string{SystemWappBFF, SystemWappEdge, SystemWappPlatform}

// auditActionExchange es la acción con la que el canje entra en la bitácora.
const auditActionExchange = "auth.exchange"

// IdentityTokenVerifier valida un Identity Token emitido por identity-core y
// devuelve sus claims. Lo satisface *identityjwt.MultiVerifier (el que construye
// el bootstrap contra el JWKS de identity).
//
// Es un verificador DISTINTO del TokenValidator de este mismo paquete, y no por
// duplicación: aquel mira Context Tokens de wApp (tenant/roles/grants, clave
// local) y este mira Identity Tokens (system/email/token_version, clave remota).
// Cada tipo devuelve claims de un tipo distinto, así que no hay verificador
// único posible.
type IdentityTokenVerifier interface {
	ValidateIdentityToken(tokenString, expectedSystem string) (*identityjwt.Claims, error)
}

// ExchangeService implementa in.Exchanger: canjea el Identity Token del SSO del
// grupo por el Context Token de wApp (identity Plan 003 · design.md Ola 3 §3).
//
// La división de trabajo es la frontera del ADR-0001 de identity: identity
// acredita QUIÉN es la persona —y nada más, no conoce tenants— y wApp le pone
// encima SU contexto de negocio (el tenant de la membresía) y SUS grants
// efectivos. Aquí no se validan contraseñas ni se emiten refresh tokens: eso ya
// no es competencia de wApp.
type ExchangeService struct {
	verifier IdentityTokenVerifier
	members  out.MembershipRepo
	roles    out.RoleRepo
	grants   out.GrantRepo
	audit    out.AuditRepo
	// active es la EMPRESA ACTIVA de quien pertenece a varias (Plan 047 · Ola 5
	// · T5.1, D-047.14). Solo se consulta en el caso «dos o más membresías»: con
	// cero o una, el canje ni la mira, y esa es la forma de que la regresión de
	// los casos que ya funcionaban no dependa de leer código nuevo.
	active out.ActiveTenantRepo
	jwt    *sharedjwt.JWTManager
	cfg    Config
}

// compile-time: ExchangeService satisface el puerto de entrada.
var _ in.Exchanger = (*ExchangeService)(nil)

// NewExchangeService construye el servicio de canje. Valida deps nil (fail-fast
// en el arranque); los TTLs en cero de cfg toman sus defaults.
//
// El verificador es OBLIGATORIO: sin él no hay canje posible, y el modo dual
// apagado (WAPP_IDENTITY_JWKS_URL vacía) se expresa NO construyendo este
// servicio, no construyéndolo a medias.
func NewExchangeService(
	verifier IdentityTokenVerifier,
	members out.MembershipRepo,
	roles out.RoleRepo,
	grants out.GrantRepo,
	audit out.AuditRepo,
	active out.ActiveTenantRepo,
	jwt *sharedjwt.JWTManager,
	cfg Config,
) (*ExchangeService, error) {
	if verifier == nil {
		return nil, errors.New("iam: ExchangeService requiere un verificador de Identity Tokens")
	}
	// `active` entra en la MISMA guarda que los otros cuatro y no en una
	// opcional: un despliegue sin él no es un despliegue «sin multi-empresa»,
	// es uno donde quien tiene dos empresas se queda sin ninguna en silencio.
	// Que falte es un error de cableado y se dice al arrancar, no al canjear.
	if members == nil || roles == nil || grants == nil || audit == nil || active == nil {
		return nil, errors.New("iam: ExchangeService requiere todos los repositorios")
	}
	if jwt == nil {
		return nil, errors.New("iam: ExchangeService requiere un JWTManager emisor")
	}
	return &ExchangeService{
		verifier: verifier,
		members:  members,
		roles:    roles,
		grants:   grants,
		audit:    audit,
		active:   active,
		jwt:      jwt,
		cfg:      cfg.withDefaults(),
	}, nil
}

// Exchange valida el Identity Token, resuelve el sujeto y su tenant y emite el
// Context Token con los grants efectivos del usuario en wApp.
func (s *ExchangeService) Exchange(ctx context.Context, req in.ExchangeInput) (in.ExchangeResult, error) {
	if req.IdentityToken == "" {
		return in.ExchangeResult{}, domain.ErrInvalidInput
	}

	claims, err := s.validate(req.IdentityToken)
	if err != nil {
		s.record(ctx, "", "unknown", "error")
		return in.ExchangeResult{}, err
	}
	userID := claims.Subject
	if userID == "" {
		s.record(ctx, "", "unknown", "error")
		return in.ExchangeResult{}, domain.ErrIdentityTokenInvalid
	}

	// Aquí NO se comprueba que la persona esté activa, y no es un olvido
	// (design.md Ola 5 §2): quien la acredita es identity, y un usuario
	// desactivado allí no obtiene Identity Token, así que no llega hasta este
	// punto. Repetir la comprobación exigiría una bandera propia, o sea DOS
	// sitios donde desactivar a alguien — y desactivarlo en uno no lo
	// desactivaría en el otro, con el fallo en la dirección peligrosa: se cree
	// cerrado un acceso que sigue abierto.
	//
	// Lo que wApp sí decide es la pertenencia, y eso lo dice la membresía. Que
	// NO haya ninguna ya no corta el canje (Plan 056 · D-056.12): el tenant sale
	// vacío y el token se emite igual, sin empresa y sin un solo grant.
	tenantID, err := s.resolveTenant(ctx, userID)
	if err != nil {
		s.record(ctx, "", userID, "error")
		return in.ExchangeResult{}, err
	}

	effective, roleNames, err := s.resolveGrants(ctx, userID, tenantID)
	if err != nil {
		return in.ExchangeResult{}, err
	}

	identityExp, err := identityExpiry(claims)
	if err != nil {
		s.record(ctx, tenantID, userID, "error")
		return in.ExchangeResult{}, err
	}
	ttl, err := s.contextTTL(identityExp)
	if err != nil {
		s.record(ctx, tenantID, userID, "error")
		return in.ExchangeResult{}, err
	}

	token, expiresAt, err := s.sign(userID, tenantID, roleNames, effective, ttl)
	if err != nil {
		return in.ExchangeResult{}, err
	}
	// La regla del §3 en forma de guard, no de comentario: el `exp` que viaja en
	// el token se cuenta en segundos, y el emisor lo calcula con SU reloj, no con
	// el que se usó para el TTL. Si por lo que sea el resultado sobrepasara al
	// del Identity Token, la visa duraría más que el pasaporte y eso no sale de
	// aquí (REQ-A2: identity no puede hacer cumplir esto, nunca ve este token).
	if expiresAt.Unix() > identityExp.Unix() {
		return in.ExchangeResult{}, fmt.Errorf(
			"iam: el context token expiraría después del identity token (%d > %d)",
			expiresAt.Unix(), identityExp.Unix())
	}

	s.record(ctx, tenantID, userID, "ok")
	return in.ExchangeResult{
		ContextToken: token,
		ExpiresAt:    expiresAt,
		Context: domain.IdentityContext{
			TenantID: tenantID,
			UserID:   userID,
			Roles:    roleNames,
		},
	}, nil
}

// validate acepta el token si vale para ALGUNA de las aplicaciones de wApp. El
// verificador exige un `expectedSystem` concreto (un token de `edugo.kmp` no se
// cuela por el de `wapp.bff`), y wApp tiene dos aplicaciones en el catálogo, así
// que se prueban las dos. No es un oráculo: la firma se comprueba igual en ambos
// intentos y el rechazo es el mismo error opaco.
func (s *ExchangeService) validate(token string) (*identityjwt.Claims, error) {
	for _, system := range acceptedSystems {
		claims, err := s.verifier.ValidateIdentityToken(token, system)
		if err == nil {
			return claims, nil
		}
		switch {
		case errors.Is(err, identityauth.ErrTokenExpired):
			// Expirado lo está para todas: reintentar con otra aplicación no lo
			// resucita.
			return nil, domain.ErrIdentityTokenInvalid
		case errors.Is(err, identityauth.ErrJWKSUnavailable):
			// Sin claves frescas no se decide NADA: no se sabe si el token es
			// bueno, así que no se contesta que sea malo.
			return nil, domain.ErrIdentityUnavailable
		}
	}
	return nil, domain.ErrIdentityTokenInvalid
}

// resolveTenant traduce el sujeto de identity al tenant de wApp por la tabla de
// membresías. Son TRES casos y ninguno es un error desde el Plan 047 · Ola 5.
//
// CERO membresías ya NO era un error (Plan 056 · D-056.12). Antes devolvía
// ErrUserNotMigrated y el canje se cortaba ahí, lo que dejaba a quien acaba de
// registrarse sin poder entrar siquiera a ver que su acceso está en revisión.
// Ahora devuelve tenant VACÍO y sin error: el llamante emite un Context Token
// sin empresa y sin grants, que es el estado «en espera» — se entra, y no se
// puede hacer nada. Esa misma rama es la que muestra el selector de empresa
// ahora que el multi-empresa existe: es el mismo token, emitido por el mismo
// sitio, y no hubo que construir un tercer estado.
//
// Que el tenant vacío no sea un error NO lo convierte en comodín: aguas abajo
// nadie lo trata como "cualquier tenant" (ver [ExchangeService.sign], que emite
// un token sin un solo grant, y los guardas `id.TenantID == ""` de cada handler
// del :8103).
//
// 🔴 VARIAS MEMBRESÍAS YA NO FALLA (Plan 047 · Ola 5 · T5.1, D-047.14). Hasta
// hoy devolvía un sentinel propio y esa persona NO PODÍA ENTRAR. Ahora manda la
// EMPRESA ACTIVA que ella misma eligió por POST /api/v1/auth/active-tenant y que
// vive en el SERVIDOR (tabla user_active_tenant, migración 0086), nunca en el
// cuerpo del canje.
//
// Lo que este cambio NO hace, dicho para que nadie lo lea de más: NO elige por
// nadie. Sin empresa activa —o con una que ya no es suya— el desenlace es el
// token SIN empresa, y la consola pinta el selector. Elegir la primera en
// silencio sigue estando PROHIBIDO, y sigue habiendo un test que lo vigila
// (TestExchange_ConVariasEmpresasYSinElegirNoEligePorTi).
//
// El cierre de este caso NO tiene número de invariante: era una FRASE DE DISEÑO
// del Plan 056 —«este plan no abre el multi-empresa», design.md §5.3— que ese
// mismo documento atribuyó por error a INV-056.9. INV-056.9 es otra cosa y sigue
// vigente: «el administrador nunca conoce ni asigna la clave de nadie»
// (requirements.md del 056). Quien levanta la frase de diseño es este plan.
func (s *ExchangeService) resolveTenant(ctx context.Context, userID string) (string, error) {
	tenants, err := s.members.TenantsOfUser(ctx, userID)
	if err != nil {
		return "", err
	}
	// Las tres ramas viven en tenantEfectivo, COMPARTIDAS con el listado que pinta
	// el selector (ActiveTenantService.TenantsOfCaller). No es factorización por
	// gusto: si el canje y el listado decidieran por separado, el selector podría
	// marcar una empresa distinta de la que el token acaba llevando, y el síntoma
	// —«la consola dice que estoy en A y no me deja hacer nada en A»— no señalaría
	// a ninguno de los dos.
	//
	// La empresa activa se pasa como FUNCIÓN, así que con cero o una membresía no
	// se consulta: la regresión del caso que hoy corre en producción está en la
	// firma, no en un comentario.
	return tenantEfectivo(tenants, func() (string, bool, error) {
		return s.active.ActiveTenantOf(ctx, userID)
	})
}

// 🪦 tenantActivo VIVIÓ AQUÍ y se mudó a usecase.tenantEfectivo cuando el listado
// del selector (T5.1, in.TenantLister) pasó a necesitar la MISMA decisión. Lo que
// decía sigue vigente palabra por palabra y está escrito allí; se resume por si
// alguien llega buscándolo:
//
//   - La empresa guardada se CONTRASTA SIEMPRE contra las membresías, en el
//     instante de leerla. Guardar una empresa activa NO concede nada.
//   - Por eso la baja de un miembro no toca user_active_tenant: la fila
//     sobreviviente es inerte, y una revocación que dependiera de acordarse de
//     limpiar otra tabla se olvidaría el día que naciera una segunda vía de baja.
//   - Un fallo del repositorio NO es «no hay empresa activa»: sube y corta.

// resolveGrants resuelve los grants efectivos del sujeto, salvo cuando no hay
// tenant: un usuario sin empresa sale SIN un solo grant, se le hayan asignado
// roles o no.
//
// No es una optimización, es la mitad de seguridad del cambio D-056.12. Puede
// haber filas en iam_user_roles de un sujeto sin fila en tenant_members —el
// propio TestExchange_SinMembresiaEsUsuarioNoMigrado sembraba justo esa
// combinación—, y resolverlas aquí metería permisos en un token que no tiene
// tenant al que acotarlos. Tener permisos no es pertenecer.
//
// Con tenant, la llamada es LITERALMENTE la de antes, en el mismo punto del
// flujo: la regresión del caso de 1 membresía no depende de leer esta función,
// depende de que su rama sea la misma expresión.
func (s *ExchangeService) resolveGrants(ctx context.Context, userID, tenantID string) (sharedjwt.Grants, []string, error) {
	if tenantID == "" {
		return sharedjwt.Grants{Allow: []string{}, Deny: []string{}}, []string{}, nil
	}
	return resolveEffectiveGrants(ctx, s.roles, s.grants, userID, tenantID)
}

// sign emite el Context Token. Con tenant es la MISMA llamada de siempre; sin
// tenant usa el emisor que ni siquiera acepta roles ni grants como parámetros
// (wapp-shared/auth/jwt), de modo que un token sin empresa no puede salir de
// aquí llevando permisos aunque alguien se equivocara aguas arriba.
//
// Existe la bifurcación porque GenerateToken RECHAZA el tenant vacío
// (ErrInvalidInput) — y ese rechazo se conserva a propósito: sigue siendo un
// error pasarle "" al emisor normal.
func (s *ExchangeService) sign(
	userID, tenantID string,
	roleNames []string,
	effective sharedjwt.Grants,
	ttl time.Duration,
) (string, time.Time, error) {
	if tenantID == "" {
		return s.jwt.GenerateTenantlessToken(userID, ttl)
	}
	return s.jwt.GenerateToken(userID, tenantID, roleNames, effective, ttl)
}

// contextTTL aplica la regla del `exp` (design.md Ola 3 §3):
// `context.exp = min(now + TTL_contexto, identity.exp)`.
//
// El TTL del Context Token deja de ser autónomo —hoy lo decide wApp y nadie lo
// acota— y pasa a estar acotado por la vida de la identidad que lo originó.
// Cuando lo que queda no llega al mínimo emitible, no se emite: un token más
// largo que su origen es exactamente lo que la regla prohíbe.
func (s *ExchangeService) contextTTL(identityExp time.Time) (time.Duration, error) {
	ttl := s.cfg.AccessTTL
	if remaining := time.Until(identityExp); remaining < ttl {
		ttl = remaining
	}
	if ttl < minContextTTL {
		return 0, domain.ErrIdentityTokenExpiring
	}
	return ttl, nil
}

// minContextTTL es el mínimo que el emisor de wapp-shared admite (rechaza
// cualquier ttl inferior a un minuto). Se nombra aquí para que el motivo del
// rechazo sea legible en vez de un error genérico de firma.
const minContextTTL = time.Minute

// identityExpiry lee el `exp` del Identity Token. Un token sin `exp` no debería
// llegar hasta aquí (el verificador lo exige), pero el claim es un puntero y no
// se desreferencia a ciegas.
func identityExpiry(claims *identityjwt.Claims) (time.Time, error) {
	if claims.ExpiresAt == nil {
		return time.Time{}, domain.ErrIdentityTokenInvalid
	}
	return claims.ExpiresAt.Time, nil
}

// record escribe el evento de canje en la bitácora (best-effort, CERO PII: el
// actor es el UUID opaco del sujeto).
func (s *ExchangeService) record(ctx context.Context, tenantID, actor, result string) {
	var tid *string
	if tenantID != "" {
		tid = &tenantID
	}
	if err := s.audit.Record(ctx, domain.AuditEvent{
		TenantID: tid,
		Actor:    actor,
		Action:   auditActionExchange,
		Resource: "auth",
		Result:   result,
	}); err != nil {
		_ = err // best-effort: un fallo de auditoría no aborta el canje
	}
}
