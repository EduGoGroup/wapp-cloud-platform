package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

	identityjwt "github.com/EduGoGroup/identity-shared/auth/jwt"
	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	iamidentity "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/identity"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// defaultES256Kid es el `kid` por defecto cuando WAPP_JWT_KID está vacío (solo
// dev; en producción se define un kid con la convención es256-YYYYMMDD).
const defaultES256Kid = "es256-dev"

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
	// m2mClient habla con identity-api como máquina para aprovisionamiento (Plan 056).
	m2mClient *iamidentity.M2MClient
}

// userJWTBundle agrupa el material de tokens de USUARIO (ADR-0019, Plan 028).
// Tras el retiro de HS256 del plano de usuario (T4), ES256 es el único emisor:
// reúne el emisor ES256 (con `kid`) y el material derivado que necesita el
// MultiVerifier del middleware (la pública ES256 y el `kid` para su entrada).
// El secreto HS256 (WAPP_JWT_SECRET) se retiró del todo con el plano M2M
// (identity Plan 003 · Ola 5 §7): wApp ya no firma nada en HS256.
type userJWTBundle struct {
	es256 *sharedjwt.JWTManager // emisor ES256 con `kid` estampado (único emisor de usuario).
	esPub *ecdsa.PublicKey      // pública ES256 derivada (entrada `kid` del MultiVerifier).
	kid   string                // key id activo ES256.
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
			iampostgres.NewMembershipRepo(db),
			iampostgres.NewRoleRepo(db),
			iampostgres.NewGrantRepo(db),
			iampostgres.NewAuditRepo(db),
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

// buildJWTManagers construye el material del emisor del Context Token (ES256,
// ADR-0019) a partir de la config. Zero-knowledge: la clave sale de env, NUNCA
// se hardcodea ni se loguea. La clave EC (WAPP_JWT_EC_PRIVATE_KEY_FILE) es
// obligatoria en prod (fail-fast) y efímera con warning en dev.
//
// El secreto HS256 (WAPP_JWT_SECRET) desapareció con el plano M2M en la Ola 5
// (identity Plan 003 · design.md Ola 5 §7): era lo único que seguía firmando en
// simétrico y llevaba dos olas sobreviviendo a su fecha de muerte declarada.
func buildJWTManagers(cfg config.AppConfig, log sharedlogger.Logger) (*userJWTBundle, error) {
	// Par ES256 (ADR-0019): el emisor asimétrico, hoy único.
	priv, err := buildES256Key(cfg, log)
	if err != nil {
		return nil, err
	}
	kid := cfg.JWT.Kid
	if kid == "" {
		// Con ES256 como único emisor de usuario (T4), el `kid` es obligatorio en
		// prod: es lo que ata el token a su entrada de verificación en el rotado.
		if cfg.Env == "prod" {
			return nil, errors.New("WAPP_JWT_KID es obligatorio en prod (ADR-0019: ES256 es el único emisor de usuario)")
		}
		kid = defaultES256Kid
		log.Warn("WAPP_JWT_KID vacío: usando kid por defecto \"" + defaultES256Kid + "\" (define uno con convención es256-YYYYMMDD)")
	}
	es256Mgr, err := sharedjwt.NewJWTManagerES256(priv, cfg.JWT.Issuer)
	if err != nil {
		return nil, fmt.Errorf("construyendo emisor ES256: %w", err)
	}
	es256Mgr = es256Mgr.WithKid(kid)

	return &userJWTBundle{
		es256: es256Mgr,
		esPub: &priv.PublicKey,
		kid:   kid,
	}, nil
}

// buildES256Key resuelve la clave privada EC P-256 que firma los tokens de
// usuario en ES256 (ADR-0019, Plan 028). Reglas por entorno: con
// WAPP_JWT_EC_PRIVATE_KEY_FILE lee el PEM, en prod exige permisos
// <=0600, parsea PKCS#8 o SEC1 y valida curva P-256; en prod sin archivo (o
// inválido/permisos laxos) hace fail-fast; en dev sin archivo genera un par
// EFÍMERO en memoria con warning (permite `go run` sin fricción).
func buildES256Key(cfg config.AppConfig, log sharedlogger.Logger) (*ecdsa.PrivateKey, error) {
	path := cfg.JWT.ECPrivateKeyFile
	if path == "" {
		if cfg.Env == "prod" {
			return nil, errors.New("WAPP_JWT_EC_PRIVATE_KEY_FILE es obligatorio en prod (ADR-0019: emisión ES256 sin default)")
		}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generando par ES256 efímero de dev: %w", err)
		}
		log.Warn("clave ES256 EFÍMERA de dev: cambia en cada arranque; los tokens no sobreviven a un reinicio (no apto para producción)")
		return key, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("leyendo la clave ES256 %q: %w", path, err)
	}
	// En prod exige permisos estrictos (<=0600): cualquier bit de grupo/otros
	// delata una clave privada expuesta.
	if cfg.Env == "prod" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("permisos laxos en la clave ES256 %q: %#o (exige <=0600 en prod)", path, info.Mode().Perm())
	}
	pemBytes, err := os.ReadFile(path) // #nosec G304 -- ruta provista por la config de confianza del operador
	if err != nil {
		return nil, fmt.Errorf("leyendo la clave ES256 %q: %w", path, err)
	}
	key, err := parseECP256PrivateKeyPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("clave ES256 %q: %w", path, err)
	}
	return key, nil
}

// parseECP256PrivateKeyPEM decodifica un PEM con una clave privada EC en formato
// PKCS#8 o SEC1 y exige la curva P-256 (la de ES256). Función pura (sin E/S) para
// poder testear el parseo y la validación de curva de forma aislada.
func parseECP256PrivateKeyPEM(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no contiene un bloque PEM válido")
	}
	// PKCS#8 primero (formato del openssl pkcs8 -topk8 documentado); si no, SEC1.
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("la clave PKCS#8 no es ECDSA (es %T)", parsed)
		}
		return validateP256(ec)
	}
	ec, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("no es una clave EC PKCS#8 ni SEC1: %w", err)
	}
	return validateP256(ec)
}

// validateP256 comprueba que la clave EC use la curva P-256 (obligatoria para
// ES256, ADR-0019); cualquier otra curva se rechaza.
func validateP256(ec *ecdsa.PrivateKey) (*ecdsa.PrivateKey, error) {
	if ec.Curve != elliptic.P256() {
		return nil, fmt.Errorf("curva %q no soportada: ES256 exige P-256", ec.Curve.Params().Name)
	}
	return ec, nil
}

// buildJWKSConfig arma la config kind:"jwks" (ADR-0025 dec.2) que se empuja al Edge
// por ConfigUpdate: un JWK Set estándar con la pública ES256 del emisor de usuario
// y su `kid`. Con ella el Edge verifica OFFLINE los access tokens del operador
// (mismo MultiVerifier por `kid` de wapp-shared/auth). La llave pública NO es
// secreta. Se versiona por `kid` (Version=kid): una rotación de llave ⇒ nuevo kid ⇒
// nueva version ⇒ el push idempotente del Edge la adopta.
func buildJWKSConfig(pub *ecdsa.PublicKey, kid string) (gatewaygrpc.ConfigPayload, error) {
	// Bytes() devuelve el punto sin comprimir: 0x04 || X(32) || Y(32) (P-256).
	uncompressed, err := pub.Bytes()
	if err != nil {
		return gatewaygrpc.ConfigPayload{}, fmt.Errorf("serializando llave pública EC: %w", err)
	}
	xb := uncompressed[1:33]
	yb := uncompressed[33:65]
	jwks := map[string]any{
		"keys": []map[string]any{{
			"kty": "EC",
			"crv": "P-256",
			"x":   base64.RawURLEncoding.EncodeToString(xb),
			"y":   base64.RawURLEncoding.EncodeToString(yb),
			"kid": kid,
			"use": "sig",
			"alg": "ES256",
		}},
	}
	payload, err := json.Marshal(jwks)
	if err != nil {
		return gatewaygrpc.ConfigPayload{}, fmt.Errorf("serializando JWKS: %w", err)
	}
	return gatewaygrpc.ConfigPayload{Kind: "jwks", Version: kid, Payload: payload}, nil
}

// jwksConfigProvider entrega SIEMPRE la config kind:"jwks" (la pública ES256 del
// emisor, ADR-0025 dec.2) a TODO Edge que conecta y delega el resto de kinds al
// provider siguiente (intents). La llave ES256 es GLOBAL del emisor (un solo par
// por proceso, no por-tenant): el mismo JWKS vale para todos los tenants, así que
// se entrega sin mirar el tenantID.
type jwksConfigProvider struct {
	jwks gatewaygrpc.ConfigPayload
	next gatewaygrpc.ConfigProvider
}

func (p jwksConfigProvider) ConfigsForConnect(ctx context.Context, tenantID string) ([]gatewaygrpc.ConfigPayload, error) {
	out := []gatewaygrpc.ConfigPayload{p.jwks}
	if p.next != nil {
		rest, err := p.next.ConfigsForConnect(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		out = append(out, rest...)
	}
	return out, nil
}

// intentsConfigProvider adapta el store de config de intents + los entitlements al
// puerto gatewaygrpc.ConfigProvider (ADR-0021): al conectar un Edge, entrega la
// config de intents vigente del tenant SOLO si tiene la feature llm_intent (gate de
// verdad, ADR-0022) y hay config persistida. Es el ÚNICO punto que ata el kind
// "intents" al push al conectar; el Gateway permanece genérico (no conoce kinds).
type intentsConfigProvider struct {
	store *intentcfg.PostgresStore
	ents  *entitlements.Postgres
}

// ConfigsForConnect resuelve el gate y devuelve la config de intents del tenant, o
// nil si no aplica (sin feature o sin config). Un fallo de infraestructura se
// propaga para que el Gateway lo loguee (no se empuja config a medias).
func (p intentsConfigProvider) ConfigsForConnect(ctx context.Context, tenantID string) ([]gatewaygrpc.ConfigPayload, error) {
	has, err := p.ents.Has(ctx, tenantID, entitlements.FeatureLLMIntent)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, nil
	}
	cfg, err := p.store.Get(ctx, tenantID)
	if err != nil {
		if errors.Is(err, intentcfg.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return []gatewaygrpc.ConfigPayload{{Kind: intentcfg.Kind, Version: cfg.Version, Payload: cfg.Blob}}, nil
}
