// Porta internal/intakeahead/calentamiento.go @ 56097aa

package intakeahead

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ════════════════════════════════════════════════════════════════════════════
// EL CALENTAMIENTO DE LA CACHÉ DE PREFIJO (Plan 044 · Ola 1.7 · T1.7-4, D7-c)
// ════════════════════════════════════════════════════════════════════════════
//
// # Por qué vive en ESTE paquete y no en uno suyo
//
// Porque el calentamiento y la P1 real tienen que producir el MISMO PREFIJO, y la
// única forma de que eso sea verdad por construcción —y no por disciplina— es que los
// dos lo armen con la misma función. Esa función es `input`, y es privada de aquí.
// Un paquete aparte habría tenido que copiarla o exportarla, y ese es exactamente el
// molde de fallo que esta casa ya conoce: dos caminos que hacen lo mismo, divergen en
// un dato, y el síntoma es que nada falla — el calentamiento calentaría un prompt que
// nadie pide, sin un solo error y sin que la latencia mejore.
//
// # Los dos disparadores, y por qué son esos
//
//  1. **Al conectar el Edge.** Su Ollama acaba de aparecer (o de reiniciarse) y la
//     primera inferencia real pagaría el prefill frío.
//  2. **Al publicar catálogo/intents** (`ConfigUpdate`). 🔴 Este es el importante y el
//     menos evidente: el catálogo ES el prefijo. Publicarlo INVALIDA el que estuviera
//     caliente, así que sin calentar, el siguiente mensaje del cliente vuelve a pagar
//     los ~50 s aunque el Edge lleve horas conectado.
//
// # Lo que este fichero NO hace, a propósito
//
//   - **No usa los workers del pool.** Un calentamiento de ~50 s ocupando uno de los
//     workers del adelanto sería robarle a las clasificaciones reales el recurso que
//     el adelanto existe para tener. Va en su propia goroutine.
//   - **No recuerda a quién calentó ni tiene cooldown.** No hace falta: repetir un
//     calentamiento sobre una caché YA caliente cuesta el prefill caliente (0,07–0,55 s
//     medidos), no los 50 s. Solo el primero de cada prefijo es caro. Una memoria de
//     «a este ya lo calenté» sería estado que caduca solo —el runner de Ollama muere a
//     los 5 min de silencio en la máquina de un cliente— para ahorrar un segundo.
//   - **No decide si el tenant tiene algo que calentar.** Eso es preguntar por la vía y
//     se pregunta en un solo sitio (el Warm del selector, ADR-0044 §C2).

// DefaultWarmTimeout es el presupuesto de UN calentamiento.
//
// LA ARITMÉTICA, que es lo único que justifica el número. Un prefill FRÍO de un P1 de
// UAT son ~50 s (2.354 tokens a 21,6 ms/token), más la generación acotada a 16 tokens
// (~2 s). El adaptador local le resta su margen del veredicto (7 s) para pedirle al
// Edge un `timeout_ms` de ~103 s, que queda por debajo del TECHO que el Edge acepta
// (120 s): pedir más sería pedir un plazo que el Edge recorta.
//
// Es GENEROSO a propósito y no cuesta lo que parece: nadie espera detrás. Lo que un
// plazo corto compraría —liberar antes una goroutine— no vale abandonar a los 40 s un
// prefill que iba a terminar en 52 y dejar la caché a medias, que es lo peor de los dos
// mundos: se pagó el tiempo y no quedó nada caliente.
const DefaultWarmTimeout = 110 * time.Second

// Warmer (antes `Calentador`) emite el calentamiento de la caché de prefijo de un
// Edge. Lo satisface `*llmvia.Selector` de forma estructural.
//
// 🔴 ES UNA INTERFAZ APARTE Y NO UN MÉTODO MÁS DE ProviderSelector, y la razón no es
// de gusto: ProviderSelector es lo que el pipeline necesita para PEDIR INFERENCIAS, y
// todos sus dobles lo implementan. Meter aquí un método de mantenimiento obligaría a
// cada uno de ellos a crecer un método que no ejercen. Opcional además por lo de
// siempre: sin cable, el calentamiento no ocurre y nada más se rompe.
type Warmer interface {
	Warm(ctx context.Context, tenantID, sessionID string, in llm.ClassifyRequestInput) error
}

// WithWarmer (antes `WithCalentador`) inyecta el emisor del calentamiento. Sin él —o
// con nil—, `Warm` es un no-op silencioso: se pierde el precalentado (la primera
// inferencia de cada prefijo vuelve a pagar el prefill frío) y NADA MÁS — el pipeline
// funciona igual, solo más lento en su primer mensaje.
func WithWarmer(w Warmer) Option {
	panic(pendiente.Implementar("intakeahead.WithWarmer"))
}

// WithWarmup (antes `WithCalentamiento`) enciende o apaga el precalentado. Por defecto
// está ENCENDIDO (New lo materializa), así que un Pool construido sin esta opción
// precalienta.
//
// 🔴 EXISTE POR EL MÉTODO DE LA PRUEBA DE CAMPO, no por prudencia. El criterio (a) de
// T1.7-4 exige un control A/B EN LA MISMA TANDA: la primera inferencia de un tenant con
// calentamiento y sin él. Si apagarlo exigiera recompilar, no habría «misma tanda»
// —harían falta dos binarios— y lo que se compararía serían dos despliegues, no el
// mecanismo. El arranque le pasa `cfg.LLM.WarmupEnabled`.
//
// Apagado, `Warm` no hace nada: nadie precalienta y la primera inferencia de cada
// prefijo nuevo paga el prefill frío.
func WithWarmup(on bool) Option {
	panic(pendiente.Implementar("intakeahead.WithWarmup"))
}

// WithWarmTimeout fija el presupuesto de un calentamiento (ver DefaultWarmTimeout). Un
// valor <= 0 se ignora y queda el que hubiera.
func WithWarmTimeout(d time.Duration) Option {
	panic(pendiente.Implementar("intakeahead.WithWarmTimeout"))
}

// Warm dispara UN calentamiento de la caché de prefijo del Edge `edgeID` del tenant,
// que sale por la sesión `sessionID`. NO BLOQUEA y NO DEVUELVE ERROR: sus dos
// llamantes son el registro de una sesión en el bucle Recv del gateway
// (`gw.OnWarmup`) y el fan-out de un `ConfigUpdate`, y ninguno puede esperar ~50 s.
// Funciona SIN `Run`: no pasa por la cola ni ocupa un worker, va en su propia
// goroutine; tampoco necesita `sel` ni `sink`.
//
// `kind` es el del ConfigUpdate recién empujado, o vacío si el aviso viene del
// handshake. 🔴 AQUÍ SE FILTRA, Y ES DONDE TIENE QUE FILTRARSE: el gateway empuja TRES
// kinds y solo `intents` (`intentcfg.Kind`) forma el prefijo del prompt. Sin este
// filtro, cada rotación de JWKS —que no cambia un byte del prompt— costaría un prefill
// frío de ~50 s de la plaza única en la máquina de cada cliente.
//
// No hace NADA —ni lee el catálogo, ni loguea— cuando:
//
//   - el pool es nil, el precalentado está apagado (WithWarmup(false)), no hay Warmer
//     o no hay ConfigStore;
//   - `tenantID` o `sessionID` es "" (`edgeID` vacío sí se admite);
//   - `kind` no es "" ni `intentcfg.Kind` (`jwks`, `filters`…);
//   - YA hay un calentamiento en vuelo para ese `(tenantID, edgeID)`: el cerrojo es
//     «uno en vuelo por Edge», no por sesión, y es el caso normal cuando un
//     ConfigUpdate sale hacia varias sesiones a la vez. Otro Edge, u otro tenant, no
//     espera a este.
//
// En otro caso, en segundo plano y con su propio presupuesto (WithWarmTimeout):
//
//  1. Arma la MISMA entrada que armaría una P1 real de ese tenant —mismo catálogo
//     aplanado, mismo vocabulario, misma etiqueta de desconocido— con `Text` VACÍO.
//     Sin catálogo no hay prefijo que calentar: no se emite y no se loguea. Los fallos
//     de lectura o de validación dejan los mismos `Warn` que en Request, con el rótulo
//     `calentamiento: ` en vez de `adelanto: `.
//  2. Se la entrega a `Warmer.Warm(ctx, tenantID, sessionID, entrada)`, UNA vez.
//  3. Error ⇒ `Debug` `calentamiento: no se emitió` con el error (no afirma que algo
//     se rompió: en vía API «no se emitió» es la respuesta correcta). Sin error ⇒
//     `Debug` `calentamiento: emitido contra el Edge`.
//
// Al terminar, salga como salga, el Edge vuelve a admitir calentamientos: el cerrojo
// es «uno en vuelo», no «uno para siempre», y no hay memoria ni cooldown.
//
// No acepta `ctx` por el mismo motivo que `Request`, y aquí es todavía más marcado: el
// ctx del handshake muere con el registro de la sesión y el del PUT de intents, con la
// respuesta HTTP. Heredar cualquiera de los dos mataría TODO calentamiento antes de
// empezar. El reloj sale del presupuesto propio y de nada más.
//
// ⚠️ Consecuencia aceptada: un calentamiento en vuelo NO se cancela al cancelar el ctx
// de `Run` ni al apagar el proceso. Dura poco de todos modos —cuando el stream del Edge
// cae, la inferencia vuelve en el acto con edge_offline—.
//
// ⚠️ Heredado tal cual: `Warm` no comprueba `log`. Un pool construido con `log` nil y
// con Warmer no está contemplado (el arranque siempre lo da).
func (p *Pool) Warm(tenantID, edgeID, sessionID, kind string) {
	panic(pendiente.Implementar("intakeahead.Pool.Warm"))
}
