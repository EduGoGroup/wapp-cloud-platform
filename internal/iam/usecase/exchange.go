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
)

// acceptedSystems son las aplicaciones cuyo Identity Token se canjea, en el
// orden en que se prueban.
var acceptedSystems = []string{SystemWappBFF, SystemWappEdge}

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
	users    out.UserRepo
	members  out.MembershipRepo
	roles    out.RoleRepo
	grants   out.GrantRepo
	audit    out.AuditRepo
	jwt      *sharedjwt.JWTManager
	cfg      Config
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
	users out.UserRepo,
	members out.MembershipRepo,
	roles out.RoleRepo,
	grants out.GrantRepo,
	audit out.AuditRepo,
	jwt *sharedjwt.JWTManager,
	cfg Config,
) (*ExchangeService, error) {
	if verifier == nil {
		return nil, errors.New("iam: ExchangeService requiere un verificador de Identity Tokens")
	}
	if users == nil || members == nil || roles == nil || grants == nil || audit == nil {
		return nil, errors.New("iam: ExchangeService requiere todos los repositorios")
	}
	if jwt == nil {
		return nil, errors.New("iam: ExchangeService requiere un JWTManager emisor")
	}
	return &ExchangeService{
		verifier: verifier,
		users:    users,
		members:  members,
		roles:    roles,
		grants:   grants,
		audit:    audit,
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

	// El sujeto tiene que existir en wApp: los UUID se preservaron en la
	// migración justamente para que el `sub` de identity sea la PK de aquí.
	u, err := s.users.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.record(ctx, "", userID, "error")
			return in.ExchangeResult{}, domain.ErrUserNotMigrated
		}
		return in.ExchangeResult{}, err
	}
	if !u.IsActive || u.DeletedAt != nil {
		s.record(ctx, "", userID, "error")
		return in.ExchangeResult{}, domain.ErrUserInactive
	}

	tenantID, err := s.resolveTenant(ctx, userID)
	if err != nil {
		s.record(ctx, "", userID, "error")
		return in.ExchangeResult{}, err
	}

	effective, roleNames, err := resolveEffectiveGrants(ctx, s.roles, s.grants, userID)
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

	token, expiresAt, err := s.jwt.GenerateToken(userID, tenantID, roleNames, effective, ttl)
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
// membresías. Hoy la relación es 1:1; más de un tenant es un caso que esta ola
// NO resuelve y que por tanto falla con nombre propio.
func (s *ExchangeService) resolveTenant(ctx context.Context, userID string) (string, error) {
	tenants, err := s.members.TenantsOfUser(ctx, userID)
	if err != nil {
		return "", err
	}
	switch len(tenants) {
	case 0:
		return "", domain.ErrUserNotMigrated
	case 1:
		return tenants[0], nil
	default:
		return "", domain.ErrMultipleTenants
	}
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
