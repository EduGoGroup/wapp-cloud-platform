// Parte de internal/arranque/auth.go (copia de internal/bootstrap/arranque/auth.go @ 80807ba), T2.34: los JWT del plano de usuario (ES256, su clave y su JWKS).
package arranque

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

// defaultES256Kid es el `kid` por defecto cuando WAPP_JWT_KID está vacío (solo
// dev; en producción se define un kid con la convención es256-YYYYMMDD).
const defaultES256Kid = "es256-dev"

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
func buildJWKSConfig(pub *ecdsa.PublicKey, kid string) (edgegrpc.ConfigPayload, error) {
	// Bytes() devuelve el punto sin comprimir: 0x04 || X(32) || Y(32) (P-256).
	uncompressed, err := pub.Bytes()
	if err != nil {
		return edgegrpc.ConfigPayload{}, fmt.Errorf("serializando llave pública EC: %w", err)
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
		return edgegrpc.ConfigPayload{}, fmt.Errorf("serializando JWKS: %w", err)
	}
	return edgegrpc.ConfigPayload{Kind: "jwks", Version: kid, Payload: payload}, nil
}
