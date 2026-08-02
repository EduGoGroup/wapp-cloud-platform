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

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// defaultES256Kid es el `kid` por defecto cuando WAPP_JWT_KID está vacío (solo
// dev; en producción se define un kid con la convención es256-YYYYMMDD).
const defaultES256Kid = "es256-dev"

// authStack agrupa el material del plano de autenticación de usuario del IAM que
// hoy consumen DOS piezas: el servidor de la API pública (:8103) y el gateway
// CloudLink (Plan 033 · T2.2, ADR-0025 — RPCs UserLogin/Refresh/Logout del Edge).
// Se construye UNA vez en run() para que ambos planos compartan EXACTAMENTE el
// mismo emisor/validador ES256, el mismo AuthService y el mismo auditor.
type authStack struct {
	jwtBundle *userJWTBundle
	validator *sharedjwt.MultiVerifier
	auditor   *iamusecase.AuditService
	authSvc   *iamusecase.AuthService
	m2mSvc    *iamusecase.M2MService
	authMW    *httpapi.Middleware
}

// userJWTBundle agrupa el material de tokens de USUARIO del IAM (ADR-0019, Plan
// 028). Tras el retiro de HS256 del plano de usuario (T4), ES256 es el único
// emisor: reúne el emisor ES256 (con `kid`) y el material derivado que necesita
// el MultiVerifier del middleware (la pública ES256 y el `kid` para su entrada).
// El secreto HS256 (WAPP_JWT_SECRET) ya NO forma parte del plano de usuario;
// sobrevive solo para el ServiceJWTManager M2M (ver buildJWTManagers).
type userJWTBundle struct {
	es256 *sharedjwt.JWTManager // emisor ES256 con `kid` estampado (único emisor de usuario).
	esPub *ecdsa.PublicKey      // pública ES256 derivada (entrada `kid` del MultiVerifier).
	kid   string                // key id activo ES256.
}

// buildAuthStack cablea el material de auth de usuario del IAM (Plan 018 · T3,
// ADR-0019) sobre el *sql.DB ya abierto. Antes vivía embebido en
// buildPublicAPIServer; se extrajo (Plan 033 · T2.2) para poder inyectar el mismo
// AuthService/auditor en el gateway CloudLink, que se construye antes que el
// servidor público.
func buildAuthStack(cfg config.AppConfig, db *sql.DB, log sharedlogger.Logger) (*authStack, error) {
	jwtBundle, svcJWTMgr, err := buildJWTManagers(cfg, log)
	if err != nil {
		return nil, err
	}
	// EMISOR DEL PLANO DE USUARIO (Plan 028 · T3/T4, ADR-0019): ES256 con `kid`
	// (jwtBundle.es256). El emisor HS256 legacy quedó RETIRADO del plano de usuario
	// (T4): WAPP_JWT_SECRET solo sobrevive para el ServiceJWTManager M2M.
	userTokenIssuer := jwtBundle.es256
	// Validación del :8103 (Plan 028 · T4, ADR-0019): un MultiVerifier con la ÚNICA
	// entrada ES256 por su `kid` (pública derivada) y SIN default, de modo que un
	// token HS256 de usuario (con o sin `kid`) se RECHAZA. *sharedjwt.MultiVerifier
	// satisface la interface UserTokenValidator del middleware y el TokenValidator
	// del AuthService: una sola política de aceptación para el :8103 y el IAM.
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
	authSvc, err := iamusecase.NewAuthService(
		iampostgres.NewUserRepo(db),
		iampostgres.NewRoleRepo(db),
		iampostgres.NewGrantRepo(db),
		iampostgres.NewRefreshRepo(db),
		iampostgres.NewAuditRepo(db),
		userTokenIssuer,
		userValidator,
		iamusecase.Config{},
	)
	if err != nil {
		return nil, fmt.Errorf("construyendo AuthService (IAM): %w", err)
	}
	m2mSvc, err := iamusecase.NewM2MService(iampostgres.NewAPIKeyRepo(db), svcJWTMgr, iamusecase.Config{})
	if err != nil {
		return nil, fmt.Errorf("construyendo M2MService (IAM): %w", err)
	}
	authMW := httpapi.NewMiddleware(userValidator, m2mSvc, log)
	return &authStack{
		jwtBundle: jwtBundle,
		validator: userValidator,
		auditor:   auditor,
		authSvc:   authSvc,
		m2mSvc:    m2mSvc,
		authMW:    authMW,
	}, nil
}

// buildJWTManagers construye el material de tokens de usuario (emisor ES256) y el
// ServiceJWTManager M2M del IAM (Plan 018 §6, ADR-0019) a partir de la config.
// Zero-knowledge: los secretos/claves salen de env, NUNCA se hardcodean ni se
// loguean. La clave EC (WAPP_JWT_EC_PRIVATE_KEY_FILE) firma los tokens de usuario
// y el secreto HS256 (WAPP_JWT_SECRET) firma el service token M2M; ambos son
// obligatorios en prod (fail-fast) y efímeros con warning en dev. El service
// token exige `aud` propia (aísla los planos usuario/M2M). Tras T4 el secreto
// HS256 NO firma ni valida tokens de usuario: es exclusivo del plano M2M.
func buildJWTManagers(cfg config.AppConfig, log sharedlogger.Logger) (*userJWTBundle, *sharedjwt.ServiceJWTManager, error) {
	secret := cfg.JWT.Secret
	if secret == "" {
		if cfg.Env == "prod" {
			return nil, nil, errors.New("WAPP_JWT_SECRET es obligatorio en prod (zero-knowledge: sin default)")
		}
		gen, err := randomSecret()
		if err != nil {
			return nil, nil, fmt.Errorf("generando secreto JWT de dev: %w", err)
		}
		secret = gen
		log.Warn("secreto JWT EFÍMERO de dev: cambia en cada arranque; los tokens no sobreviven a un reinicio (no apto para producción)")
	}

	// Par ES256 (F1, ADR-0019): emisor asimétrico que convive con HS256. En T1 se
	// construye pero NO corta la emisión todavía (ver punto de conmutación).
	priv, err := buildES256Key(cfg, log)
	if err != nil {
		return nil, nil, err
	}
	kid := cfg.JWT.Kid
	if kid == "" {
		// Con ES256 como único emisor de usuario (T4), el `kid` es obligatorio en
		// prod: es lo que ata el token a su entrada de verificación en el rotado.
		if cfg.Env == "prod" {
			return nil, nil, errors.New("WAPP_JWT_KID es obligatorio en prod (ADR-0019: ES256 es el único emisor de usuario)")
		}
		kid = defaultES256Kid
		log.Warn("WAPP_JWT_KID vacío: usando kid por defecto \"" + defaultES256Kid + "\" (define uno con convención es256-YYYYMMDD)")
	}
	es256Mgr, err := sharedjwt.NewJWTManagerES256(priv, cfg.JWT.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("construyendo emisor ES256: %w", err)
	}
	es256Mgr = es256Mgr.WithKid(kid)

	bundle := &userJWTBundle{
		es256: es256Mgr,
		esPub: &priv.PublicKey,
		kid:   kid,
	}
	// El secreto HS256 ya no firma tokens de usuario (T4): solo el service token M2M.
	svcMgr := sharedjwt.NewServiceJWTManager(secret, cfg.JWT.Issuer, cfg.JWT.ServiceAudience)
	return bundle, svcMgr, nil
}

// randomSecret genera 32 bytes aleatorios en base64 (secreto HS256 efímero de
// dev). No apto para producción: no persiste entre arranques.
func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// buildES256Key resuelve la clave privada EC P-256 que firma los tokens de
// usuario en ES256 (ADR-0019, Plan 028). Reglas por entorno (espejo del secreto
// HS256): con WAPP_JWT_EC_PRIVATE_KEY_FILE lee el PEM, en prod exige permisos
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
