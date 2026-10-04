// Parte de internal/arranque/auth.go (copia de internal/bootstrap/arranque/auth.go @ 80807ba), T2.34: la pila de autenticación (authStack, M2M y delegación a identity, verificador JWKS).
package arranque

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	identityjwt "github.com/EduGoGroup/identity-shared/auth/jwt"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	iamidentity "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/identity"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// identityTokenIssuer es el `iss` que identity-core estampa en sus Identity
// Tokens y que el verificador exige (identity ADR-0002). Es el nombre del emisor
// del grupo, no un parámetro de despliegue: lo que cambia entre ambientes es la
// URL del JWKS (WAPP_IDENTITY_JWKS_URL), no quién firma.
const identityTokenIssuer = "identity-core"

// authStack agrupa el material del plano de autenticación de usuario que hoy
// consumen DOS piezas: el servidor de la API pública (:8103) y el gateway
// CloudLink (Plan 033 · T2.2, ADR-0025 — RPCs UserLogin/Refresh/Logout del Edge).
// Se construye UNA vez en run() para que ambos planos compartan EXACTAMENTE el
// mismo emisor/validador ES256 y el mismo auditor.
type authStack struct {
	jwtBundle *userJWTBundle
	validator *sharedjwt.MultiVerifier
	auditor   *iamusecase.AuditService
	// contextTokens inspecciona los Context Tokens que emite wApp: es lo único
	// que sirve el :8103 por su cuenta desde la Ola 5 (/api/v1/auth/verify).
	contextTokens *iamusecase.ContextTokenService
	authMW        *httpapi.Middleware
	// identityVerifier valida los Identity Tokens que emite identity-core, con
	// las claves de su JWKS. Es nil mientras WAPP_IDENTITY_JWKS_URL esté vacía.
	//
	// Son DOS verificadores por TIPO de token, no uno fusionado (Plan 003 de
	// identity, design.md Ola 1 §6): `validator` mira Context Tokens de wApp
	// (tenant/roles/grants, clave local) y este mira Identity Tokens de identity
	// (system/email/token_version, clave remota). Cada tipo devuelve claims de un
	// tipo distinto, así que no hay verificador único posible.
	identityVerifier *identityjwt.MultiVerifier
	// exchangeSvc canjea Identity Tokens por Context Tokens (Plan 003 de
	// identity · T3.1). Es el consumidor que la Ola 1 dejó pendiente: nil
	// exactamente cuando identityVerifier es nil, y entonces
	// /api/v1/auth/exchange responde 503.
	exchangeSvc *iamusecase.ExchangeService
	// edgeAuthSvc es el autenticador DELEGADO que atiende las RPCs de auth del
	// Edge (Plan 003 de identity · T3.3): valida las credenciales en identity y
	// canjea. Desde la Ola 5 NO hay alternativa local a la que caer —el IAM
	// propio se eliminó—, así que sin WAPP_IDENTITY_URL el relé se queda SIN
	// autenticador y el gateway contesta "auth no disponible". Es la respuesta
	// correcta: un despliegue sin identity es un despliegue donde nadie puede
	// autenticarse, y decirlo es mejor que tener un camino propio de reserva.
	edgeAuthSvc *iamusecase.DelegatedAuthService
	// m2mClient habla con identity-api como máquina para aprovisionamiento
	// (Plan 056). Es una INTERFAZ (out.IdentityM2MClient), NO el puntero
	// concreto *iamidentity.M2MClient (C-02): un *M2MClient nil convertido a
	// un parámetro de tipo interfaz produce un valor de interfaz CON TIPO, y
	// entonces las guardas `m2m == nil` de signup.go y access_requests.go dan
	// SIEMPRE false. Como campo de interfaz, dejarlo sin asignar (el zero
	// value de wireIdentityM2M cuando falta la config) es un nil de interfaz
	// de verdad.
	m2mClient out.IdentityM2MClient
}

// buildAuthStack cablea el material de auth de usuario (Plan 018 · T3,
// ADR-0019) sobre el *sql.DB ya abierto. Antes vivía embebido en
// buildPublicAPIServer; se extrajo (Plan 033 · T2.2) para poder inyectar el
// mismo material en el gateway CloudLink, que se construye antes que el servidor
// público.
func buildAuthStack(cfg config.AppConfig, db *sql.DB, log sharedlogger.Logger) (*authStack, error) {
	jwtBundle, err := buildJWTManagers(cfg, log)
	if err != nil {
		return nil, err
	}
	// EMISOR DEL CONTEXT TOKEN (Plan 028 · T3/T4, ADR-0019): ES256 con `kid`
	// (jwtBundle.es256). Es el ÚNICO emisor que le queda a wApp: las credenciales
	// las valida identity y lo que wApp firma es el contexto de negocio.
	userTokenIssuer := jwtBundle.es256
	// Validación del :8103 (Plan 028 · T4, ADR-0019): un MultiVerifier con la ÚNICA
	// entrada ES256 por su `kid` (pública derivada) y SIN default, de modo que un
	// token HS256 de usuario (con o sin `kid`) se RECHAZA. *sharedjwt.MultiVerifier
	// satisface la interface UserTokenValidator del middleware y el TokenValidator
	// del usecase: una sola política de aceptación para todo el proceso.
	userValidator, err := sharedjwt.NewMultiVerifier(
		cfg.JWT.Issuer,
		map[string]sharedjwt.VerifierKey{jwtBundle.kid: sharedjwt.ES256VerifierKey(jwtBundle.esPub)},
		sharedjwt.VerifierKey{},
	)
	if err != nil {
		return nil, fmt.Errorf("construyendo MultiVerifier de usuario (ES256): %w", err)
	}
	auditor, err := iamusecase.NewAuditService(iampostgres.NewAuditRepo(db))
	if err != nil {
		return nil, fmt.Errorf("construyendo AuditService (IAM): %w", err)
	}
	contextTokens, err := iamusecase.NewContextTokenService(userValidator)
	if err != nil {
		return nil, fmt.Errorf("construyendo ContextTokenService (IAM): %w", err)
	}
	authMW := httpapi.NewMiddleware(userValidator, log)
	stack := &authStack{
		jwtBundle:     jwtBundle,
		validator:     userValidator,
		auditor:       auditor,
		contextTokens: contextTokens,
		authMW:        authMW,
	}
	// Puerta del modo dual (Plan 003 de identity · T1.2): con la variable vacía,
	// cloud-platform arranca exactamente como hasta ahora.
	if jwksURL := strings.TrimSpace(cfg.Identity.JWKSURL); jwksURL != "" {
		identityVerifier, ierr := buildIdentityVerifier(jwksURL)
		if ierr != nil {
			return nil, ierr
		}
		stack.identityVerifier = identityVerifier
		// El canje (T3.1) es el consumidor del verificador. Se construye SOLO
		// aquí: sin verificador no hay canje posible, y el endpoint prefiere
		// declararse indisponible a existir a medias.
		exchangeSvc, xerr := iamusecase.NewExchangeService(
			identityVerifier,
			// nil de resolver de features, y es correcto: el canje solo LEE
			// membresías (TenantsOfUser) y nunca llama a Add. El resolver que
			// exige el constructor es el de la guarda del ALTA (multi_empresa,
			// Plan 047 · Ola 5 · T5.2), y por este camino no se da de alta a
			// nadie. Si algún día el exchange escribiera, esto es un defecto: el
			// alta quedaría gateada por un resolver que dice que no a todo.
			iampostgres.NewMembershipRepo(db, nil),
			iampostgres.NewRoleRepo(db),
			iampostgres.NewGrantRepo(db),
			iampostgres.NewAuditRepo(db),
			// La EMPRESA ACTIVA (Plan 047 · Ola 5 · T5.1). Sin este repositorio
			// el constructor falla y el arranque se aborta: no hay modo
			// «sin multi-empresa» — quien tenga dos empresas se quedaría sin
			// ninguna, en silencio, y eso es peor que no arrancar.
			iampostgres.NewActiveTenantRepo(db),
			userTokenIssuer,
			iamusecase.Config{},
		)
		if xerr != nil {
			return nil, fmt.Errorf("construyendo ExchangeService (IAM): %w", xerr)
		}
		stack.exchangeSvc = exchangeSvc
		log.Info("verificador de Identity Tokens activo; canje /api/v1/auth/exchange habilitado",
			"jwks_url", jwksURL,
			"issuer", identityTokenIssuer,
			"kids", stack.identityVerifier.Kids())
	} else {
		log.Info("modo dual con identity APAGADO: WAPP_IDENTITY_JWKS_URL vacía (wApp arranca sin identity-core; /api/v1/auth/exchange responde 503)")
	}

	// Segunda puerta (Plan 003 de identity · T3.3): con URL de identity, el relé
	// del Edge deja de resolver credenciales aquí y delega en el SSO del grupo.
	if err := stack.wireDelegatedAuth(cfg, userValidator, log); err != nil {
		return nil, err
	}

	// Cliente M2M hacia identity-api (Plan 056 · T2.4 / T3.2 / T3.4).
	if err := stack.wireIdentityM2M(cfg, log); err != nil {
		return nil, err
	}

	return stack, nil
}

// wireIdentityM2M construye el cliente M2M hacia identity-api para aprovisionamiento (Plan 056).
func (s *authStack) wireIdentityM2M(cfg config.AppConfig, log sharedlogger.Logger) error {
	identityURL := strings.TrimSpace(cfg.Identity.URL)
	apiKey := strings.TrimSpace(cfg.Identity.APIKey)
	if identityURL == "" || apiKey == "" {
		log.Warn("WAPP_IDENTITY_URL o WAPP_IDENTITY_API_KEY vacías: cliente M2M de identity no inicializado (alta pública y asignación de sistemas no disponibles)")
		// s.m2mClient se queda en su zero value: un nil de interfaz DE
		// VERDAD (C-02). buildPublicAPIServer usa justo esa nulidad para NO
		// cablear POST /api/v1/signup con un handler que no puede operar.
		return nil
	}
	m2m, err := iamidentity.NewM2M(identityURL, apiKey, cfg.Identity.Timeout)
	if err != nil {
		return fmt.Errorf("construyendo cliente M2M de identity-api: %w", err)
	}
	s.m2mClient = m2m
	log.Info("cliente M2M de identity-api ACTIVO para aprovisionamiento de plataforma", "identity_url", identityURL)
	return nil
}

// wireDelegatedAuth construye el autenticador delegado que usará el gateway
// CloudLink cuando haya URL de identity (identity Plan 003 · design.md Ola 3 §6).
//
// Las dos variables son ejes distintos de la misma transición y por eso se
// comprueban juntas: JWKS_URL enseña a wApp a VERIFICAR lo que identity firma, y
// URL le dice a quién PREGUNTAR por las credenciales. Delegar sin poder verificar
// es imposible —el canje necesita el verificador—, así que esa combinación no
// arranca en vez de fallar en el primer login.
func (s *authStack) wireDelegatedAuth(cfg config.AppConfig, validator iamusecase.TokenValidator, log sharedlogger.Logger) error {
	identityURL := strings.TrimSpace(cfg.Identity.URL)
	if identityURL == "" {
		log.Warn("WAPP_IDENTITY_URL vacía: el relé de auth del Edge se queda SIN autenticador (el IAM local se eliminó en la Ola 5; login/refresh/logout del operador responderán \"auth no disponible\")")
		return nil
	}
	if s.exchangeSvc == nil {
		return errors.New("WAPP_IDENTITY_URL exige WAPP_IDENTITY_JWKS_URL: sin verificador no hay canje, y sin canje la delegación no puede emitir Context Tokens")
	}
	client, err := iamidentity.New(identityURL, cfg.Identity.Timeout)
	if err != nil {
		return fmt.Errorf("construyendo el cliente de identity-api: %w", err)
	}
	delegated, err := iamusecase.NewDelegatedAuthService(client, s.exchangeSvc, validator, iamusecase.SystemWappEdge, log)
	if err != nil {
		return fmt.Errorf("construyendo el autenticador delegado del Edge: %w", err)
	}
	s.edgeAuthSvc = delegated
	log.Info("delegación de la auth del Edge ACTIVA: login/refresh/logout del operador van a identity-core",
		"identity_url", identityURL,
		"system", iamusecase.SystemWappEdge)
	return nil
}

// edgeAuthenticator devuelve el autenticador que atiende las RPCs de auth del
// Edge: el delegado, o un nil DE VERDAD si no hay delegación (mismo cuidado que
// exchanger con las interfaces nil de Go). El gateway ya sabe responder "auth no
// disponible" ante un puerto ausente, que es lo honesto: sin identity no hay
// quien valide credenciales, y wApp dejó de tener un camino propio.
func (s *authStack) edgeAuthenticator() in.Authenticator {
	if s.edgeAuthSvc == nil {
		return nil
	}
	return s.edgeAuthSvc
}

// exchanger expone el canje como puerto in, o un nil DE VERDAD cuando el modo
// dual está apagado. Existe por el clásico tropiezo de Go: asignar un puntero
// nil a una interface produce una interface NO nil, y el handler decide el 503
// comparando contra nil. El chequeo se hace una vez, aquí.
func (s *authStack) exchanger() in.Exchanger {
	if s.exchangeSvc == nil {
		return nil
	}
	return s.exchangeSvc
}

// buildIdentityVerifier construye el verificador de los Identity Tokens de
// identity-core (Plan 003 de identity · T1.2) contra su JWKS. Solo se llama con
// una URL no vacía: la puerta la abre el llamador.
//
// Que la puerta exista NO es comodidad. El constructor JWKS de identity-shared
// es EAGER y FAIL-CLOSED —hace el primer fetch en el arranque y falla si no
// puede completarlo, para no nacer nunca con cero claves—, así que sin la puerta
// un identity-api apagado dejaría a cloud-platform sin arrancar. La Ola 1 no
// puede introducir esa dependencia de arranque: eso llega en la Ola 3, cuando
// wApp de verdad delegue la autenticación. Con la variable puesta, en cambio, el
// fallo de arranque es el comportamiento QUERIDO: quien la define está
// declarando que identity tiene que estar ahí.
func buildIdentityVerifier(jwksURL string) (*identityjwt.MultiVerifier, error) {
	mv, err := identityjwt.NewMultiVerifierFromJWKS(identityTokenIssuer, identityjwt.JWKSOptions{URL: jwksURL})
	if err != nil {
		return nil, fmt.Errorf("construyendo el verificador de Identity Tokens contra %s: %w", jwksURL, err)
	}
	return mv, nil
}
