package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/time/rate"
)

// maxBuckets acota el número de cubos vivos por Limiter: al superarlo se hace una
// barrida de los inactivos (evita crecimiento no acotado ante muchas claves).
const maxBuckets = 10000

// bucketIdleTTL es la inactividad tras la cual un cubo es candidato a evicción.
const bucketIdleTTL = 10 * time.Minute

// RateLimitObserver recibe una señal por cada rechazo de rate-limit (para
// métricas). Lo satisface *metrics.Metrics (RateLimitHit). Se declara aquí para
// no acoplar httpapi al paquete de métricas; nil desactiva la observación.
type RateLimitObserver interface {
	RateLimitHit(scope string)
}

// Limiter es un token-bucket EN MEMORIA (sin broker, INV-3 no aplica al cloud
// pero mantenemos el proceso simple) por clave (api-key/tenant o IP). Usa
// golang.org/x/time/rate: r tokens/seg con ráfaga burst. Es seguro para uso
// concurrente.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    rate.Limit
	burst   int
}

type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// NewLimiter construye un limiter de r peticiones/seg con ráfaga burst. burst<=0
// se normaliza a 1 (siempre permite al menos una).
func NewLimiter(r rate.Limit, burst int) *Limiter {
	if burst <= 0 {
		burst = 1
	}
	return &Limiter{buckets: map[string]*bucket{}, rate: r, burst: burst}
}

// Allow consume un token para la clave dada; false si el cubo está agotado.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxBuckets {
			l.evictStaleLocked()
		}
		b = &bucket{lim: rate.NewLimiter(l.rate, l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = time.Now()
	lim := b.lim
	l.mu.Unlock()
	return lim.Allow()
}

// evictStaleLocked borra los cubos inactivos más allá del TTL. Debe llamarse con
// el lock tomado.
func (l *Limiter) evictStaleLocked() {
	cutoff := time.Now().Add(-bucketIdleTTL)
	for k, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}

// PublicRateLimit envuelve el mux de la API pública (:8103) con rate-limit:
//   - /api/v1/auth/login → por IP (frena la fuerza bruta de credenciales).
//   - resto de /api/v1   → por credencial (api-key/tenant): hash de X-API-Key o
//     del Bearer; sin credencial, cae a la IP.
//
// Al exceder responde 429 con Retry-After y registra el hit en el observer. NO
// limita /healthz ni /metrics (viven en el listener admin, no en este). login y
// public son *Limiter independientes (defaults/env distintos).
func PublicRateLimit(next http.Handler, public, login *Limiter, obs RateLimitObserver, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope := "public"
		lim := public
		var key string
		if r.URL.Path == "/api/v1/auth/login" {
			scope = "login"
			lim = login
			key = clientIP(r)
		} else {
			key = credentialKey(r)
		}
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
	if l == nil || l.rate <= 0 {
		return 1
	}
	secs := int(1 / float64(l.rate))
	if secs < 1 {
		return 1
	}
	return secs
}

// credentialKey deriva una clave OPACA de la credencial del request: SHA256 de
// la api-key (X-API-Key) o del Bearer. Se hashea para NO retener el secreto en
// claro como clave del mapa (higiene zero-knowledge). Sin credencial, cae a la IP.
func credentialKey(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get("X-API-Key")); k != "" {
		return "k:" + hashKey(k)
	}
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
