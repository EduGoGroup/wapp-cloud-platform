package usecase

import (
	"context"
	"errors"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// DelegatedAuthService implementa in.Authenticator delegando la autenticación en
// identity-core (identity Plan 003 · design.md Ola 3 §6).
//
// Es el MISMO puerto que AuthService, con otra cosa detrás, y eso es
// deliberado: quien lo consume —hoy el gateway CloudLink, que relaya el
// login/refresh/logout del operador— no tiene que enterarse de la delegación.
// Lo único que cambia es quién resuelve las credenciales, y esa decisión la
// toma el arranque según haya o no URL de identity.
//
// El reparto queda así: identity valida credenciales, custodia la sesión y
// emite el refresh; wApp canja esa identidad por un Context Token con el tenant
// y los grants que solo él conoce. El Identity Token NO se persiste: vive el
// instante server-side que dura el canje.
type DelegatedAuthService struct {
	identity out.IdentityClient
	exchange in.Exchanger
	// system es la aplicación del catálogo de identity con la que se hace el
	// login (wapp.edge para el relé del Edge, wapp.bff para la web). Es lo que
	// somete cada login al System Gate de la aplicación correcta.
	system string
	// validator valida Context Tokens de wApp: los emite wApp, así que Verify no
	// necesita salir a identity.
	validator TokenValidator
	// log deja rastro de los estados a medias que este servicio puede producir y
	// nadie más ve (ver closeRotatedSession). Es OPCIONAL —nil no loguea— porque
	// el resto del paquete no depende de un logger; aquí existe porque hay un
	// caso en el que el error devuelto no cuenta la historia completa. NUNCA
	// recibe tokens enteros (ver truncateSecret) ni contraseñas.
	log sharedlogger.Logger
}

// compile-time: el delegado es intercambiable con el AuthService local.
var _ in.Authenticator = (*DelegatedAuthService)(nil)

// NewDelegatedAuthService construye el autenticador delegado. log es opcional
// (nil = sin rastro); todo lo demás es obligatorio.
func NewDelegatedAuthService(
	identity out.IdentityClient,
	exchange in.Exchanger,
	validator TokenValidator,
	system string,
	log sharedlogger.Logger,
) (*DelegatedAuthService, error) {
	if identity == nil {
		return nil, errors.New("iam: DelegatedAuthService requiere un cliente de identity")
	}
	if exchange == nil {
		return nil, errors.New("iam: DelegatedAuthService requiere el canje (Exchanger)")
	}
	if validator == nil {
		return nil, errors.New("iam: DelegatedAuthService requiere un TokenValidator")
	}
	if system != SystemWappBFF && system != SystemWappEdge {
		return nil, errors.New("iam: DelegatedAuthService requiere una aplicación de wApp (wapp.bff o wapp.edge)")
	}
	return &DelegatedAuthService{
		identity:  identity,
		exchange:  exchange,
		validator: validator,
		system:    system,
		log:       log,
	}, nil
}

// Login autentica contra identity y canjea la identidad resultante por el
// Context Token de wApp, en un solo movimiento server-side.
//
// LoginInput.TenantID se IGNORA: identity no conoce tenants (INV-1 de su
// ADR-0001), así que el login no se puede acotar allí. El tenant del resultado
// sale de la membresía en wApp, y quien necesite comprobar que coincide con el
// de su canal —el gateway lo hace con el tenant del mTLS— sigue pudiendo
// hacerlo sobre el contexto devuelto.
func (s *DelegatedAuthService) Login(ctx context.Context, req in.LoginInput) (domain.AuthResult, error) {
	if req.Email == "" || req.Password == "" {
		return domain.AuthResult{}, domain.ErrInvalidInput
	}
	session, err := s.identity.Login(ctx, req.Email, req.Password, s.system)
	if err != nil {
		return domain.AuthResult{}, err
	}
	return s.exchangeSession(ctx, session)
}

// Refresh rota la sesión en identity y RE-CANJEA: los grants se resuelven otra
// vez, así que un cambio de rol en wApp entra en el siguiente Context Token sin
// que la persona vuelva a escribir su contraseña.
func (s *DelegatedAuthService) Refresh(ctx context.Context, req in.RefreshInput) (domain.AuthResult, error) {
	if req.RefreshToken == "" {
		return domain.AuthResult{}, domain.ErrInvalidInput
	}
	session, err := s.identity.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return domain.AuthResult{}, err
	}
	return s.exchangeSession(ctx, session)
}

// Logout revoca en identity. Sin AllSessions cierra SOLO la sesión de este
// refresh —la de esta aplicación—, que es el modelo Google: cerrar la consola
// del Edge no cierra la web, y al revés.
//
// Con AllSessions, la revocación global es competencia de identity y el titular
// tiene que salir de un Identity Token, nunca de un user_id transportado por
// quien pide el logout. Como aquí solo se presenta un refresh, se rota primero
// para obtener ese Identity Token y con él se revoca todo.
func (s *DelegatedAuthService) Logout(ctx context.Context, req in.LogoutInput) error {
	if req.RefreshToken == "" {
		return domain.ErrInvalidInput
	}
	if !req.AllSessions {
		return s.identity.Logout(ctx, req.RefreshToken)
	}
	session, err := s.identity.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return err
	}
	if err := s.identity.LogoutAll(ctx, session.IdentityToken); err != nil {
		s.closeRotatedSession(ctx, session.RefreshToken, err)
		return err
	}
	return nil
}

// closeRotatedSession cierra la ventana de fallo parcial de la revocación
// global: entre el refresh y el logout-all hay un instante en el que el refresh
// presentado YA se consumió en la rotación —identity lo revoca al rotar— y las
// demás sesiones siguen vivas. Si el logout-all falla ahí, quien pidió el
// logout se queda sin credencial para reintentar y con todo abierto.
//
// No se puede deshacer la rotación, pero sí cerrar lo único que aún se puede
// cerrar: la sesión recién rotada. Es best-effort —si también falla, el error
// que sale hacia fuera sigue siendo el del logout-all, que es el que describe
// lo que el usuario pidió y no consiguió— y deja rastro en el log del estado
// real en el que quedó la cuenta.
//
// Hacia el Edge esto NO se disfraza de éxito: una revocación global que no
// ocurrió tiene que verse. Silenciarla dejaría a una persona creyendo que cerró
// todas sus sesiones sin haber cerrado ninguna, que es peor que un error.
func (s *DelegatedAuthService) closeRotatedSession(ctx context.Context, rotatedRefresh string, cause error) {
	// El intento de cierre va SIEMPRE: es la mitigación, no la traza. Solo el
	// rastro depende de que haya logger.
	err := s.identity.Logout(ctx, rotatedRefresh)
	if s.log == nil {
		return
	}
	if err != nil {
		s.log.Error("revocación global fallida y la sesión rotada tampoco se pudo cerrar: quedan sesiones vivas",
			"causa", cause, "error_cierre", err, "refresh_rotado", truncateSecret(rotatedRefresh))
		return
	}
	s.log.Warn("revocación global fallida: solo se cerró la sesión rotada; las demás siguen vivas",
		"causa", cause, "refresh_rotado", truncateSecret(rotatedRefresh))
}

// secretLogPrefix es cuánto de un token se deja ver en un log. Lo justo para
// correlacionar dos líneas de la misma operación y ni un carácter más: un token
// completo en un log es un token filtrado.
const secretLogPrefix = 12

// truncateSecret recorta un token para poder nombrarlo en un log sin exponerlo.
func truncateSecret(token string) string {
	if len(token) <= secretLogPrefix {
		return "…"
	}
	return token[:secretLogPrefix] + "…"
}

// Verify valida un Context Token de wApp. No sale a identity a propósito: el
// token que se está mirando lo firmó wApp, y la persona ya fue acreditada
// cuando se emitió.
func (s *DelegatedAuthService) Verify(_ context.Context, accessToken string) (in.VerifyResult, error) {
	return verifyWithValidator(s.validator, accessToken)
}

// exchangeSession canjea la identidad recién obtenida por el Context Token y
// arma el resultado que espera cualquier consumidor del puerto: access =
// Context Token de wApp, refresh = el de identity, expiración = la del contexto
// (que ya viene acotada por la del Identity Token).
func (s *DelegatedAuthService) exchangeSession(ctx context.Context, session domain.IdentitySession) (domain.AuthResult, error) {
	res, err := s.exchange.Exchange(ctx, in.ExchangeInput{IdentityToken: session.IdentityToken})
	if err != nil {
		return domain.AuthResult{}, err
	}
	return domain.AuthResult{
		AccessToken:  res.ContextToken,
		RefreshToken: session.RefreshToken,
		TokenType:    tokenTypeBearer,
		ExpiresAt:    res.ExpiresAt,
		Context:      res.Context,
	}, nil
}
