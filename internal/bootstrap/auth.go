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
	"github.com/EduGoGroup/wapp-cloud-platform/internal/filtercfg"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	iamidentity "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/identity"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
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
// provider siguiente (la cadena intents+filters). La llave ES256 es GLOBAL del emisor
// (un solo par por proceso, no por-tenant): el mismo JWKS vale para todos los
// tenants, así que se entrega sin mirar el tenantID.
//
// 🔴 «SIEMPRE» es literal desde la corrección del code review 2026-08-21: antes, un
// error del eslabón siguiente hacía `return nil, err` y se llevaba por delante el
// jwks, que YA ESTABA EN LA MANO y no había podido fallar (es un valor calculado al
// arrancar, sin I/O). Un hipo de Neon en el connect dejaba al Edge sin jwks, sin
// intents y sin filters de una vez. Ahora el error del siguiente se LOGUEA y el jwks
// viaja igual. Ver el porqué completo en chainConfigProvider.
type jwksConfigProvider struct {
	jwks gatewaygrpc.ConfigPayload
	next gatewaygrpc.ConfigProvider
	log  sharedlogger.Logger
}

func (p jwksConfigProvider) ConfigsForConnect(ctx context.Context, tenantID string) ([]gatewaygrpc.ConfigPayload, error) {
	if p.next == nil {
		return []gatewaygrpc.ConfigPayload{p.jwks}, nil
	}
	rest, err := p.next.ConfigsForConnect(ctx, tenantID)
	if err != nil {
		// No se propaga: perder el jwks por un fallo de otro kind es exactamente el
		// modo de fallo que esta función existe para no tener.
		logConfigLinkError(p.log, "jwks:next", tenantID, err)
	}
	// El jwks va SIEMPRE el primero de la lista, haya fallado o no el eslabón siguiente.
	out := make([]gatewaygrpc.ConfigPayload, 0, 1+len(rest))
	out = append(out, p.jwks)
	return append(out, rest...), nil
}

// intentConfigStore es el puerto de LECTURA que consume intentsConfigProvider: solo
// el Get del blob de intents del tenant. Lo satisface *intentcfg.PostgresStore
// (producción) y *intentcfg.MemoryStore (tests).
//
// Se declara como interfaz —y no se guarda el tipo concreto— para que la CADENA REAL
// que arma buildConfigProvider se pueda ejercer en un test sin PostgreSQL. Ese test
// es el criterio (d) del Plan 046 · T2.1 y no existía: el que había le daba al
// Gateway una lista de configs escrita a mano, así que gatear `filters` por
// entitlement —la regla 1, la que el plan más protege— lo habría dejado en VERDE.
type intentConfigStore interface {
	Get(ctx context.Context, tenantID string) (intentcfg.Config, error)
}

// intentsConfigProvider adapta el store de config de intents + los entitlements al
// puerto gatewaygrpc.ConfigProvider (ADR-0021): al conectar un Edge, entrega la
// config de intents vigente del tenant SOLO si tiene la feature llm_intent (gate de
// verdad, ADR-0022) y hay config persistida. Es el ÚNICO punto que ata el kind
// "intents" al push al conectar; el Gateway permanece genérico (no conoce kinds).
type intentsConfigProvider struct {
	store intentConfigStore
	ents  entitlements.Resolver
}

// ConfigsForConnect resuelve el gate y devuelve la config de intents del tenant, o
// nil si no aplica (sin feature o sin config). Un fallo de infraestructura se devuelve
// al llamante; quien lo trata es chainConfigProvider, que lo LOGUEA con el kind y
// sigue con los demás eslabones (best-effort por eslabón): que Neon tosa al resolver
// la feature no puede costarle al Edge el jwks ni los filtros.
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

// filtersConfigProvider adapta la foto de perfiles del tenant (fleet_sessions.profile)
// al puerto gatewaygrpc.ConfigProvider: al conectar un Edge, le entrega el
// kind:"filters" vigente (Plan 046 · T2.1, ADR-0027). Es el TERCER eslabón de la
// cadena, junto a jwks e intents.
//
// 🔴 Se parece a intentsConfigProvider en la forma y se le opone en el fondo, en dos
// puntos que NO son estilo:
//
//  1. NO consulta entitlements. `passive_profiles` está declarada y NO gatea en v1.
//     Gatear aquí haría que un tenant sin el add-on SUBIERA a la nube el tráfico de
//     sus sesiones pasivas: el fallo exacto que este plan viene a cerrar. Por eso
//     este struct no tiene campo `ents` — no es que esté sin usar, es que no existe.
//  2. NUNCA devuelve (nil, nil) en el camino feliz. intents sí lo hace («sin feature o
//     sin config no hay nada que empujar»); aquí un mapa todo-`active` ES la
//     información que hace converger al Edge cuando una sesión deja de ser pasiva, y
//     callarse dejaría al Edge con el mapa anterior y una sesión reactivada muda.
//
// Comparte con el hook en caliente (filtercfg.Pusher) la MISMA función de armado,
// filtercfg.ForTenant, para que las dos vías no puedan divergir.
type filtersConfigProvider struct {
	src filtercfg.Source
}

// ConfigsForConnect devuelve SIEMPRE una config (salvo error de infraestructura, que
// se devuelve al llamante). Ese error NO tumba la entrega de los otros kinds:
// chainConfigProvider lo loguea con el kind "filters" y sigue. El Edge se queda con el
// mapa de filtros que ya tenía —su last-known-good de ESTE kind, que sigue siendo
// válido— y lo reconcilia en la próxima conexión o en el próximo cambio de perfil.
func (p filtersConfigProvider) ConfigsForConnect(ctx context.Context, tenantID string) ([]gatewaygrpc.ConfigPayload, error) {
	version, payload, err := filtercfg.ForTenant(ctx, p.src, tenantID)
	if err != nil {
		return nil, err
	}
	return []gatewaygrpc.ConfigPayload{{Kind: filtercfg.Kind, Version: version, Payload: payload}}, nil
}

// chainLink es un eslabón de la cadena con el KIND que aporta. El kind no se usa para
// decidir nada: es la etiqueta con la que se loguea su fallo. Sin ella, el operador ve
// «un eslabón falló» y tiene que adivinar cuál, que es justo lo que no puede hacer a
// las 3 de la mañana.
type chainLink struct {
	kind     string
	provider gatewaygrpc.ConfigProvider
}

// chainConfigProvider concatena N proveedores en el orden dado y devuelve la unión de
// sus configs. Existe porque el encadenado por campo `next` no escala a tres eslabones
// sin una trampa: intentsConfigProvider tiene DOS `return nil, nil` tempranos (sin
// feature, sin config persistida), así que colgarle un `next` habría hecho que un
// tenant sin llm_intent se quedara TAMBIÉN sin filters —silenciosamente, y con todos
// los tests de cada provider en verde—.
//
// 🔴 BEST-EFFORT POR ESLABÓN (corrección del code review 2026-08-21). Un eslabón que
// falla LOGUEA su error CON SU KIND y la cadena SIGUE con los demás: nunca se pierde
// un kind por culpa de otro. La versión anterior propagaba el primer error y abortaba
// entera, con este razonamiento —«media config es peor que ninguna: el Edge conserva
// su last-known-good COMPLETO»— que suena bien y es falso en los dos extremos:
//
//   - No hay «last-known-good completo» que conservar. Los kinds son INDEPENDIENTES
//     en el Edge: cada uno tiene su propia versión y su propia persistencia. Que
//     `filters` no llegue no invalida el `intents` que sí llegó, ni al revés.
//   - Y el modo de fallo real era el contrario del que se temía: un hipo de Neon en
//     el connect dejaba al Edge SIN JWKS —que no había fallado, y que ni siquiera
//     hace I/O—, sin intents y sin filters de una vez, con UN solo Error en el log de
//     la nube. Con T2.1 hay una query MÁS a Neon en cada connect, así que la
//     probabilidad de ese hipo sube justamente ahora.
//
// El Edge, además, ya sabe convivir con una config que no llega: conserva la que
// tiene. Lo que no sabe hacer es adivinar la que nunca le mandaron.
//
// Un eslabón nil se salta.
type chainConfigProvider struct {
	links []chainLink
	log   sharedlogger.Logger
}

func (c chainConfigProvider) ConfigsForConnect(ctx context.Context, tenantID string) ([]gatewaygrpc.ConfigPayload, error) {
	var out []gatewaygrpc.ConfigPayload
	for _, l := range c.links {
		if l.provider == nil {
			continue
		}
		cfgs, err := l.provider.ConfigsForConnect(ctx, tenantID)
		if err != nil {
			logConfigLinkError(c.log, l.kind, tenantID, err)
			continue
		}
		out = append(out, cfgs...)
	}
	return out, nil
}

// logConfigLinkError registra el fallo de UN eslabón de la cadena de config sin
// tumbarla. Tolera log nil (los tests montan la cadena sin logger).
//
// Es Error y no Warn a propósito: que un tenant se quede sin un kind al conectar es
// una degradación real, no una curiosidad. Lo que cambió con el best-effort no es la
// severidad, es el ALCANCE — antes un Error se llevaba los tres kinds por delante.
func logConfigLinkError(log sharedlogger.Logger, kind, tenantID string, err error) {
	if log == nil {
		return
	}
	log.Error("config push: un eslabón de la cadena falló; se entregan los demás",
		"kind", kind, "tenant_id", tenantID, "error", err)
}

// buildConfigProvider arma la cadena COMPLETA de config al conectar (ADR-0021), en
// este orden y con estas reglas:
//
//  1. jwks    — SIEMPRE, y sobrevive al fallo de cualquier otro (no hace I/O).
//  2. intents — solo si el tenant tiene llm_intent Y hay config persistida.
//  3. filters — SIEMPRE (Plan 046 · T2.1): el mapa de perfiles del tenant, SIN gate
//     de entitlement y también cuando no hay ni una sesión pasiva.
//
// 🔴 Existe como FUNCIÓN, y no inline en Run(), por una razón concreta: el criterio
// (d) de T2.1 exige un test sobre la cadena REAL —dos tenants, uno con llm_intent y
// otro sin ella, tres kinds vs dos— y una cadena armada dentro de Run() no se puede
// ejercer sin levantar el proceso entero. El test vive en filters_config_test.go y se
// pone ROJO si alguien le cuelga a `filters` un gate por entitlement.
func buildConfigProvider(
	jwks gatewaygrpc.ConfigPayload,
	intents intentConfigStore,
	ents entitlements.Resolver,
	profiles filtercfg.Source,
	log sharedlogger.Logger,
) gatewaygrpc.ConfigProvider {
	return jwksConfigProvider{
		jwks: jwks,
		log:  log,
		next: chainConfigProvider{
			log: log,
			links: []chainLink{
				{kind: intentcfg.Kind, provider: intentsConfigProvider{store: intents, ents: ents}},
				{kind: filtercfg.Kind, provider: filtersConfigProvider{src: profiles}},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Plano de roles del tenant (Plan 047 · Ola 1.0 · T1.0-4)
// ---------------------------------------------------------------------------

// rolePlane agrupa los dos casos de uso que abren el plano 2 del ADR-0033 —la
// administración de RBAC y la de membresía de la PROPIA empresa— para que
// bootstrap.go los pase a publicapi.Deps con una sola pieza. Sin ellos, las
// rutas /api/v1/roles y /api/v1/members no se montan y responden 404 de ruta
// inexistente: es exactamente el modo de fallo mudo que vigila
// roleplane_cableado_test.go.
type rolePlane struct {
	roles   in.RoleAdmin
	members in.MembershipAdmin
	// invitations es la incorporación POR CÓDIGO (Plan 047 · Ola A). Va en la
	// misma pieza que las otras dos porque comparte con ellas el CallerResolver y
	// los repositorios, y porque las tres abren el mismo plano: quién está en la
	// empresa y quién puede entrar.
	invitations in.InvitationAdmin
}

// buildRolePlane cablea los casos de uso del plano de roles sobre el *sql.DB ya
// abierto.
//
// 🔑 EL CALLERRESOLVER ES LA PIEZA QUE IMPORTA, y es de aquí de donde tenía que
// salir. Los usecases no llaman a httpapi.IdentityFromContext ellos mismos por
// dirección de dependencias (internal/platform/httpapi ya importa iam/ports/in;
// que el usecase importara el transporte invertiría la flecha), así que reciben
// este puerto. Es el ÚNICO origen del tenant_id en todo el plano: ningún Input de
// in.* tiene campo TenantID, y esa ausencia es INV-04 escrita en el tipo. Un
// contexto sin Identity devuelve ok=false y el usecase falla con
// domain.ErrNoTenant antes de tocar un repositorio.
//
// Los cuatro repositorios son los MISMOS adaptadores que ya usa el canje
// (buildAuthStack): no hay una segunda implementación de estas tablas. El
// MembershipRepo se construye UNA vez y se comparte entre los dos servicios —
// RoleService lo necesita para requireMember (acotar las operaciones sobre
// personas) y MembershipService para el alta y la baja.
//
// `systems` es el cliente M2M de identity (authStack.m2mClient) y ADMITE nil: en
// un despliegue sin WAPP_IDENTITY_API_KEY el plano se construye igual, la
// lectura de miembros sigue sirviendo y solo el alta contesta 503 (ver
// iamusecase.NewMembershipService). Llega como interfaz y no como puntero
// concreto por la misma razón que el campo del que sale: un *M2MClient nil
// metido en un parámetro de interfaz produce un valor NO nil, y entonces la
// guarda del usecase daría siempre false.
//
// `features` es el resolver de derechos comerciales, y llega hasta aquí por UNA
// sola razón: el alta de un miembro pregunta por el entitlement `multi_empresa`
// antes de decidir si alguien que ya está en otra empresa puede entrar en ésta
// (Plan 047 · Ola 5 · T5.2). 🔴 NO gatea las rutas de este plano — administrar
// miembros y roles sigue siendo capacidad base de cualquier empresa (D-047.10):
// lo que la feature decide es el desenlace de un alta concreta, no si la puerta
// existe. Si algún día aparece aquí un RequireFeature, es un defecto.
//
// `log` es el MISMO del proceso, y no es decorativo: cuando identity rechaza la
// credencial M2M de wApp el llamante se lleva un 500 genérico —es un fallo del
// servidor y no le incumbe—, así que el rastro es el único sitio donde queda
// escrito qué scope hay que reemitir.
func buildRolePlane(db *sql.DB, systems out.UserSystemsClient, features iampostgres.FeatureResolver, log sharedlogger.Logger) (rolePlane, error) {
	caller := in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
		id, ok := httpapi.IdentityFromContext(ctx)
		return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, ok
	})
	members := iampostgres.NewMembershipRepo(db, features)
	roles := iampostgres.NewRoleRepo(db)
	roleSvc, err := iamusecase.NewRoleService(
		caller,
		roles,
		iampostgres.NewGrantRepo(db),
		members,
	)
	if err != nil {
		return rolePlane{}, fmt.Errorf("construyendo RoleService (IAM): %w", err)
	}
	memberSvc, err := iamusecase.NewMembershipService(caller, members, systems, log)
	if err != nil {
		return rolePlane{}, fmt.Errorf("construyendo MembershipService (IAM): %w", err)
	}
	// El servicio de invitaciones comparte el MISMO RoleRepo que RoleService, y no
	// es reutilización por comodidad: lo usa para una sola cosa —comprobar que el
	// rol prometido en la invitación es visible para la empresa que la emite— y
	// esa comprobación tiene que dar el mismo veredicto que la de RoleService.
	// Con dos adaptadores distintos, un día darían dos.
	invitationSvc, err := iamusecase.NewInvitationService(caller, iampostgres.NewInvitationRepo(db), roles)
	if err != nil {
		return rolePlane{}, fmt.Errorf("construyendo InvitationService (IAM): %w", err)
	}
	return rolePlane{roles: roleSvc, members: memberSvc, invitations: invitationSvc}, nil
}

// ---------------------------------------------------------------------------
// El CANJE de una invitación (Plan 047 · Ola A · T-A3/T-A4/T-A5)
// ---------------------------------------------------------------------------

// buildInvitationRedeem cablea el canje: la otra mitad de la invitación, la que
// usa el INVITADO.
//
// 🔴 SE CONSTRUYE APARTE DE rolePlane, Y NO ES DESORDEN. Aquellos tres servicios
// son el plano de administración de la empresa: los usa quien YA está dentro y
// todos exigen un token CON empresa (su CallerResolver resuelve el tenant y sin
// él fallan con domain.ErrNoTenant). El canje es lo contrario: lo usa quien
// todavía no está en ninguna, con un Context Token SIN empresa, y su ruta se
// monta fuera de registerRolePlane por eso mismo — ver el montaje en http.go.
// Meterlo en la misma pieza sugeriría que comparte esa precondición, y no la
// comparte.
//
// El CallerResolver es el MISMO puerto que el de buildRolePlane y se declara
// igual, con una diferencia que no está aquí sino en el usecase: RedeemService
// mira `UserID` y NO mira `TenantID`. La función de aquí no puede expresar esa
// diferencia (devuelve los dos campos, como la otra), así que no se intenta:
// donde vive la regla es en RedeemService.RedeemInvitation, con su porqué.
//
// No recibe `systems` ni `log`: el canje no llama a identity (quien canjea ya
// pasó su System Gate: si no, no tendría Context Token con el que llegar) y no
// tiene un fallo que el llamante no pueda ver, que era lo que el log salvaba en
// MembershipService.
//
// SÍ recibe `features`, y por la misma razón que el plano de roles: canjear DA DE
// ALTA, y desde el Plan 047 · Ola 5 · T5.2 el alta de quien ya es miembro de otra
// empresa depende del entitlement `multi_empresa` del tenant que invitó. El
// canje no lo consulta: solo lo lleva hasta GrantTenantAccess, que es quien
// pregunta.
func buildInvitationRedeem(db *sql.DB, features iampostgres.FeatureResolver) (in.InvitationRedeemer, error) {
	caller := in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
		id, ok := httpapi.IdentityFromContext(ctx)
		return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, ok
	})
	svc, err := iamusecase.NewRedeemService(caller, iampostgres.NewInvitationRedeemRepo(db, features))
	if err != nil {
		return nil, fmt.Errorf("construyendo RedeemService (IAM): %w", err)
	}
	return svc, nil
}

// ---------------------------------------------------------------------------
// La ELECCIÓN de empresa (Plan 047 · Ola 5 · T5.1)
// ---------------------------------------------------------------------------

// buildActiveTenantPlane cablea las DOS puertas de la empresa del sujeto: LEER
// entre cuáles puede elegir (GET /api/v1/auth/tenants) y ESCRIBIR cuál elige
// (POST /api/v1/auth/active-tenant).
//
// 🔴 DEVUELVE EL SERVICIO CONCRETO Y NO UN PUERTO, y aquí es lo correcto:
// satisface DOS puertos (in.TenantLister e in.ActiveTenantSelector) y devolver
// uno solo obligaría a un type-assert o a construirlo dos veces. Quien recibe
// sigue tomando interfaces —el handler declara los dos puertos por separado—, que
// es donde la frontera importa; este helper es privado y de cableado.
//
// 🔴 SE CONSTRUYE APARTE DE rolePlane POR LA MISMA RAZÓN QUE EL CANJE, y es la
// razón entera de la tarea: aquellos servicios son el plano de administración de
// una empresa y todos exigen un token CON empresa (su CallerResolver resuelve el
// tenant y sin él fallan con domain.ErrNoTenant). Ésta es lo contrario: la usa
// quien todavía no tiene ninguna empresa en su token —dos membresías y ninguna
// elegida ⇒ token sin tenant y sin grants— y su ruta se monta fuera de
// registerRolePlane por eso mismo (ver el montaje en http.go).
//
// El MembershipRepo es el mismo adaptador que usan el canje y el plano de roles:
// la comprobación «¿es miembro de esta empresa?» tiene que dar el mismo veredicto
// que la que hace el canje al leer la empresa activa. Con dos adaptadores
// distintos, un día darían dos.
//
// No recibe `systems` ni `log`: elegir empresa no llama a identity (quien elige
// ya pasó su System Gate: si no, no tendría Context Token con el que llegar) y no
// tiene ningún fallo que el llamante no pueda ver.
func buildActiveTenantPlane(db *sql.DB) (*iamusecase.ActiveTenantService, error) {
	caller := in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
		id, ok := httpapi.IdentityFromContext(ctx)
		return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, ok
	})
	// nil de resolver por lo mismo que en el canje del exchange: elegir empresa
	// LEE membresías (UserTenants) y no da de alta a nadie.
	svc, err := iamusecase.NewActiveTenantService(caller, iampostgres.NewMembershipRepo(db, nil), iampostgres.NewActiveTenantRepo(db))
	if err != nil {
		return nil, fmt.Errorf("construyendo ActiveTenantService (IAM): %w", err)
	}
	return svc, nil
}
