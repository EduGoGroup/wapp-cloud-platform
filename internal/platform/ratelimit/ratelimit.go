// Package ratelimit ofrece un token-bucket EN MEMORIA por clave, neutro respecto
// del transporte: lo consumen tanto la API pública HTTP (internal/platform/httpapi)
// como el runtime del Motor de Flujos (auto-respuestas por conversación, Plan 020 ·
// T0). Vive en un paquete propio para que ambos lo importen sin ciclo de imports.
//
// Sin broker (ADR-0003): el estado es local al proceso; no hay Redis ni cola.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxBuckets acota el número de cubos vivos por Limiter: al superarlo se hace una
// barrida de los inactivos (evita crecimiento no acotado ante muchas claves).
const maxBuckets = 10000

// bucketIdleTTL es la inactividad tras la cual un cubo es candidato a evicción.
const bucketIdleTTL = 10 * time.Minute

// Limiter es un token-bucket EN MEMORIA (sin broker) por clave (api-key/tenant, IP
// o clave de conversación). Usa golang.org/x/time/rate: r tokens/seg con ráfaga
// burst. Es seguro para uso concurrente.
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

// Rate devuelve la tasa configurada (tokens/seg). La usa el llamante para estimar
// tiempos de reintento sin exponer el campo interno.
func (l *Limiter) Rate() rate.Limit {
	return l.rate
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
