// Porta internal/flujos/runtime/runtime_engine.go @ e0159171

package runtime

import "time"

// runtime_engine_pool.go es el corte por tema de runtime_engine.go (05 E-13): el plazo y el
// cupo con que OnIncoming acota los entrantes reactivos (RT-3). Solo se movieron
// declaraciones; New (runtime_engine.go) es quien materializa el plazo y el semáforo.

// defaultMaxConcurrentIncoming acota cuántos entrantes reactivos procesa el runtime
// A LA VEZ (Plan 027 · Ola 1 · T5, cierra H5). Antes OnIncoming lanzaba una
// goroutine por entrante SIN techo: bajo una inundación de historial esto arranca
// cientos de HandleIncoming en paralelo (cada uno con su transacción de contactos y
// su SendText), saturando la BD y el gRPC. Un semáforo acotado limita la
// concurrencia REAL; el resto de goroutines esperan cupo baratas. 64 es holgado
// para el piloto. Se sobreescribe con WithMaxConcurrentIncoming (env
// WAPP_FLOW_MAX_CONCURRENT_INCOMING); un valor negativo lo desactiva (sin techo).
const defaultMaxConcurrentIncoming = 64

// defaultIncomingTimeout acota el procesamiento de CADA entrante reactivo (Plan
// 027 · Ola 0 · T1, cierra H1). OnIncoming despacha HandleIncoming en una
// goroutine con context.Background() (desacoplado del stream); sin deadline, el
// SendText interno espera el Ack contra un ctx.Done() que nunca dispara ⇒ la
// goroutine se fuga para siempre reteniendo el keyedMutex y cuñando la
// conversación. 30s es holgado para un round-trip Cloud→Edge→WhatsApp→Ack y a la
// vez guarantees que un Edge mudo libere la clave. Se sobreescribe con
// WithIncomingTimeout (env WAPP_FLOW_INCOMING_TIMEOUT).
const defaultIncomingTimeout = 30 * time.Second

// WithIncomingTimeout fija el plazo con que OnIncoming acota CADA entrante (Plan 027 · Ola 0
// · T1, cierra H1; variable WAPP_FLOW_INCOMING_TIMEOUT).
//
// RT-3 · Un valor <= 0 equivale a no pasarla: New deja 30 s. El camino caliente nunca queda
// sin plazo.
func WithIncomingTimeout(d time.Duration) Option {
	return func(rt *Runtime) { rt.incomingTimeout = d }
}

// WithMaxConcurrentIncoming fija cuántos entrantes procesa OnIncoming A LA VEZ (Plan 027 ·
// Ola 1 · T5, cierra H5; variable WAPP_FLOW_MAX_CONCURRENT_INCOMING).
//
// RT-3 · n == 0 equivale a no pasarla: 64. n < 0 quita el techo (sin semáforo). n > 0 es el
// cupo.
func WithMaxConcurrentIncoming(n int) Option {
	return func(rt *Runtime) { rt.maxConcurrentIncoming = n }
}
