package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/time/rate"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"
)

// RateLimitObserver recibe una señal por cada rechazo de rate-limit (para
// métricas). Lo satisface *metrics.Metrics (RateLimitHit). Se declara aquí para
// no acoplar httpapi al paquete de métricas; nil desactiva la observación.
type RateLimitObserver interface {
	RateLimitHit(scope string)
}

// Limiter es el token-bucket EN MEMORIA por clave (credencial o IP). El tipo
// vive en internal/platform/ratelimit (neutro) para que lo compartan httpapi y el
// runtime del Motor de Flujos sin ciclo de imports; aquí se re-exporta por alias
// para no tocar los llamantes (main.go, tests).
type Limiter = ratelimit.Limiter

// NewLimiter construye un Limiter de r peticiones/seg con ráfaga burst (delega en
// el paquete ratelimit). burst<=0 se normaliza a 1.
func NewLimiter(r rate.Limit, burst int) *Limiter {
	return ratelimit.NewLimiter(r, burst)
}

// PublicRateLimit envuelve el mux de la API pública (:8103) con rate-limit por
// credencial (hash del Bearer; sin credencial, cae a la IP).
//
// El freno por IP del login se retiró con el propio login (identity Plan 003 ·
// Ola 5): la fuerza bruta de contraseñas se frena donde se validan, que desde la
// Ola 3 es identity-core. Mantener aquí un cubo para una ruta que responde 404
// solo daba la impresión de que wApp seguía protegiendo algo.
//
// Al exceder responde 429 con Retry-After y registra el hit en el observer. NO
// limita /healthz ni /metrics (viven en el listener admin, no en este).
func PublicRateLimit(next http.Handler, public *Limiter, obs RateLimitObserver, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const scope = "public"
		lim := public
		key := credentialKey(r)
		if lim != nil && !lim.Allow(key) {
			if obs != nil {
				obs.RateLimitHit(scope)
			}
			if log != nil {
				// Sin PII: solo ámbito, método y ruta (nunca la credencial ni la IP).
				log.Debug("rate-limit excedido", "scope", scope, "method", r.Method, "path", r.URL.Path)
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(lim)))
			writeAuthError(w, http.StatusTooManyRequests, "demasiadas peticiones")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// retryAfterSeconds estima el tiempo de espera sugerido a partir de la tasa (al
// menos 1s). Es orientativo (el cliente puede reintentar antes si hay ráfaga).
func retryAfterSeconds(l *Limiter) int {
	if l == nil || l.Rate() <= 0 {
		return 1
	}
	secs := int(1 / float64(l.Rate()))
	if secs < 1 {
		return 1
	}
	return secs
}

// credentialKey deriva una clave OPACA de la credencial del request: SHA256 del
// Bearer. Se hashea para NO retener el secreto en claro como clave del mapa
// (higiene zero-knowledge). Sin credencial, cae a la IP.
func credentialKey(r *http.Request) string {
	if tok, ok := bearerToken(r); ok {
		return "b:" + hashKey(tok)
	}
	return "ip:" + clientIP(r)
}

// hashKey devuelve el hex del SHA256 truncado (suficiente para diferenciar cubos
// sin exponer el secreto).
func hashKey(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

// clientIP extrae la IP del cliente. Prefiere el primer salto de
// X-Forwarded-For (si el despliegue va tras proxy); si no, el host de RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
