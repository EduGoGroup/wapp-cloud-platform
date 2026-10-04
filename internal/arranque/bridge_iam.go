// Adaptador de tipos entre el IAM VIEJO (internal/iam/ports/in, internal/iam/domain) y el NUEVO
// (internal/modulos/acceso/iam). No porta ningún fichero: nace en F2 (T2.28, arquitectura §4 del
// plan de F2) para que el arranque nuevo entregue el autenticador y el auditor nuevos al gateway
// CloudLink viejo (internal/gateway/grpc, WithAuthenticator y WithAuthAuditor) sin tocarlo (E-1).
// Muere en F3, cuando el gateway nuevo recibe directamente el in.Authenticator nuevo.

package arranque

import (
	"context"
	"errors"

	viejodomain "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	viejoin "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/usecase"
)

// authenticatorBridge presenta el autenticador NUEVO (en producción, el
// *usecase.DelegatedAuthService) como el viejoin.Authenticator que pide el gateway viejo. Hace
// falta porque los DTOs y el AuthResult de los dos paquetes son tipos distintos para Go aunque
// tengan los mismos campos, y porque el gateway compara con errors.Is los centinelas VIEJOS
// (internal/gateway/grpc/auth.go, el switch de los cuatro): sin traducirlos, un login con
// credenciales malas saldría como error interno.
//
// Delega en next, el puerto nuevo; se guarda como puerto, no como el tipo concreto, para que el
// test lo ejercite con un doble y vea cada campo que llega.
//
// Promesas:
//   - Login copia viejoin.LoginInput en in.LoginInput campo a campo (Email, Password, TenantID) y
//     delega en next.Login con el mismo ctx. Refresh copia RefreshToken y delega en next.Refresh.
//     Logout copia RefreshToken, UserID y AllSessions y delega en next.Logout. Verify delega en
//     next.Verify con el mismo ctx y el mismo token. No valida ni normaliza nada: lo decide next.
//   - El domain.AuthResult nuevo se devuelve como viejodomain.AuthResult con todos sus campos
//     (AccessToken, RefreshToken, TokenType, ExpiresAt y Context con TenantID, UserID y Roles), y
//     el in.VerifyResult nuevo como viejoin.VerifyResult con todos los suyos (Valid, TenantID,
//     Subject, Roles, ExpiresAt). Roles se entrega tal cual, sin copiar el slice.
//   - Errores (los de los cuatro métodos): si casa (errors.Is) con domain.ErrInvalidCredentials,
//     ErrUserInactive, ErrRefreshInvalid o ErrInvalidInput, devuelve un bridgeError cuyo Error()
//     es EXACTAMENTE el del original y con el que errors.Is casa con el centinela nuevo, con el
//     viejo equivalente y con el propio original. Cualquier otro error sale tal cual (el mismo
//     valor). Junto a un error, Login y Refresh devuelven un AuthResult vacío y Verify un
//     VerifyResult vacío. Sin error, el error es nil.
//
// Vida: nace en F2 (T2.28) · muere en F3, con el gateway nuevo.
type authenticatorBridge struct {
	// next es el autenticador nuevo en el que se delega todo; el adaptador no guarda más estado.
	next in.Authenticator
}

// authenticatorBridge es un viejoin.Authenticator: si alguna de las dos firmas cambia, esto no
// compila.
var _ viejoin.Authenticator = (*authenticatorBridge)(nil)

// newAuthenticatorBridge envuelve el autenticador delegado nuevo para el gateway viejo. Con svc
// nil devuelve un nil DE VERDAD (interfaz nil, no un puntero nil dentro de una interfaz), el
// mismo cuidado que authStack.exchanger (y que el edgeAuthenticator viejo, al que sustituye en
// fase4_gateway.go): el gateway compara el puerto con nil para
// responder «auth no disponible», y un adaptador alrededor de nada lo engañaría.
func newAuthenticatorBridge(svc *usecase.DelegatedAuthService) viejoin.Authenticator {
	if svc == nil {
		return nil
	}
	return &authenticatorBridge{next: svc}
}

// Login implementa viejoin.Authenticator (ver authenticatorBridge).
func (b *authenticatorBridge) Login(ctx context.Context, req viejoin.LoginInput) (viejodomain.AuthResult, error) {
	res, err := b.next.Login(ctx, in.LoginInput{Email: req.Email, Password: req.Password, TenantID: req.TenantID})
	if err != nil {
		return viejodomain.AuthResult{}, translateIAMErr(err)
	}
	return toOldAuthResult(res), nil
}

// Refresh implementa viejoin.Authenticator (ver authenticatorBridge).
func (b *authenticatorBridge) Refresh(ctx context.Context, req viejoin.RefreshInput) (viejodomain.AuthResult, error) {
	res, err := b.next.Refresh(ctx, in.RefreshInput{RefreshToken: req.RefreshToken})
	if err != nil {
		return viejodomain.AuthResult{}, translateIAMErr(err)
	}
	return toOldAuthResult(res), nil
}

// Logout implementa viejoin.Authenticator (ver authenticatorBridge).
func (b *authenticatorBridge) Logout(ctx context.Context, req viejoin.LogoutInput) error {
	return translateIAMErr(b.next.Logout(ctx, in.LogoutInput{
		RefreshToken: req.RefreshToken,
		UserID:       req.UserID,
		AllSessions:  req.AllSessions,
	}))
}

// Verify implementa viejoin.TokenVerifier (ver authenticatorBridge). La conversión directa de
// struct deja de compilar si un lado gana o pierde un campo: es la guardia contra la deriva.
func (b *authenticatorBridge) Verify(ctx context.Context, accessToken string) (viejoin.VerifyResult, error) {
	res, err := b.next.Verify(ctx, accessToken)
	if err != nil {
		return viejoin.VerifyResult{}, translateIAMErr(err)
	}
	return viejoin.VerifyResult(res), nil
}

// toOldAuthResult copia el resultado nuevo al tipo viejo. AuthResult no se convierte de golpe
// porque su campo Context es de un tipo con nombre distinto en cada paquete; IdentityContext sí,
// y la conversión no compila si sus campos divergen.
func toOldAuthResult(r domain.AuthResult) viejodomain.AuthResult {
	return viejodomain.AuthResult{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		TokenType:    r.TokenType,
		ExpiresAt:    r.ExpiresAt,
		Context:      viejodomain.IdentityContext(r.Context),
	}
}

// auditorBridge presenta el auditor NUEVO (en producción, el *usecase.AuditService) como el
// viejoin.Auditor que pide el gateway viejo para los eventos edge.auth.*.
//
// Promesas:
//   - Record delega en next.Record con el mismo ctx y el MISMO AuditInput: los dos paquetes lo
//     declaran como alias de httpapi.AuditInput (F0 · D-F0-3), así que no hay copia que hacer.
//   - ListAudit delega en next.ListAudit con el mismo ctx, tenantID, limit y offset, y devuelve
//     cada domain.AuditEvent como viejodomain.AuditEvent con todos sus campos, en el mismo orden.
//     Una lista nil sale nil; una vacía, vacía.
//   - Errores: la misma traducción de centinelas que authenticatorBridge (Record devuelve
//     ErrInvalidInput con Action vacía); el resto sale tal cual. Junto a un error, ListAudit
//     devuelve nil.
//
// Vida: nace en F2 (T2.28) · muere en F3, con el gateway nuevo.
type auditorBridge struct {
	// next es el auditor nuevo en el que se delega todo; el adaptador no guarda más estado.
	next in.Auditor
}

// auditorBridge es un viejoin.Auditor: si alguna de las dos firmas cambia, esto no compila.
var _ viejoin.Auditor = (*auditorBridge)(nil)

// newAuditorBridge envuelve el auditor nuevo para el gateway viejo. Con svc nil devuelve un nil
// DE VERDAD, por la misma razón que newAuthenticatorBridge: sin auditor, el gateway funciona
// pero no audita, y lo decide comparando con nil.
func newAuditorBridge(svc *usecase.AuditService) viejoin.Auditor {
	if svc == nil {
		return nil
	}
	return &auditorBridge{next: svc}
}

// Record implementa viejoin.Auditor (ver auditorBridge).
func (b *auditorBridge) Record(ctx context.Context, req viejoin.AuditInput) error {
	return translateIAMErr(b.next.Record(ctx, req))
}

// ListAudit implementa viejoin.Auditor (ver auditorBridge).
func (b *auditorBridge) ListAudit(ctx context.Context, tenantID string, limit, offset int) ([]viejodomain.AuditEvent, error) {
	events, err := b.next.ListAudit(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, translateIAMErr(err)
	}
	return toOldAuditEvents(events), nil
}

// toOldAuditEvents copia cada evento al tipo viejo por conversión directa (no compila si los
// campos divergen). Conserva la diferencia entre nil y vacía.
func toOldAuditEvents(events []domain.AuditEvent) []viejodomain.AuditEvent {
	if events == nil {
		return nil
	}
	old := make([]viejodomain.AuditEvent, len(events))
	for i, e := range events {
		old[i] = viejodomain.AuditEvent(e)
	}
	return old
}

// iamSentinelPairs empareja cada centinela nuevo con el viejo que compara el gateway (los cuatro
// del switch de internal/gateway/grpc/auth.go). Los textos son los mismos en los dos paquetes.
var iamSentinelPairs = []struct{ current, old error }{
	{domain.ErrInvalidCredentials, viejodomain.ErrInvalidCredentials},
	{domain.ErrUserInactive, viejodomain.ErrUserInactive},
	{domain.ErrRefreshInvalid, viejodomain.ErrRefreshInvalid},
	{domain.ErrInvalidInput, viejodomain.ErrInvalidInput},
}

// translateIAMErr envuelve en un bridgeError (bridge_contact.go) el error que casa con un
// centinela nuevo de iamSentinelPairs, para que case también con el viejo; cualquier otro error
// (identity caído, el del ctx, nil) sale tal cual, porque el gateway no lo clasifica y envolverlo
// solo cambiaría su tipo.
func translateIAMErr(err error) error {
	for _, p := range iamSentinelPairs {
		if errors.Is(err, p.current) {
			return &bridgeError{original: err, oldSentinel: p.old}
		}
	}
	return err
}
