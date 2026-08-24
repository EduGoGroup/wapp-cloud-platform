package intakeahead

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
)

// ════════════════════════════════════════════════════════════════════════════
// EL CALENTAMIENTO DE LA CACHÉ DE PREFIJO (Plan 044 · Ola 1.7 · T1.7-4, D7-c)
// ════════════════════════════════════════════════════════════════════════════
//
// # Por qué vive en ESTE paquete y no en uno suyo
//
// Porque el calentamiento y la P1 real tienen que producir el MISMO PREFIJO, y la
// única forma de que eso sea verdad por construcción —y no por disciplina— es que los
// dos lo armen con la misma función. Esa función es `entrada`, y es privada de aquí.
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
//     se pregunta en un solo sitio (llmvia.Selector.Warm, ADR-0044 §C2).

// DefaultWarmTimeout es el presupuesto de UN calentamiento.
//
// LA ARITMÉTICA, que es lo único que justifica el número. Un prefill FRÍO de un P1 de
// UAT son ~50 s (2.354 tokens a 21,6 ms/token), más la generación acotada a 16 tokens
// (~2 s). El adaptador local le resta MargenVeredicto (7 s) para pedirle al Edge un
// `timeout_ms` de ~103 s, que queda por debajo del TECHO que el Edge acepta (120 s,
// cajero.DefaultMaxTimeoutMS): pedir más sería pedir un plazo que el Edge recorta.
//
// Es GENEROSO a propósito y no cuesta lo que parece: nadie espera detrás. Lo que un
// plazo corto compraría —liberar antes una goroutine— no vale abandonar a los 40 s un
// prefill que iba a terminar en 52 y dejar la caché a medias, que es lo peor de los dos
// mundos: se pagó el tiempo y no quedó nada caliente.
const DefaultWarmTimeout = 110 * time.Second

// Calentador emite el calentamiento de la caché de prefijo de un Edge. Lo satisface
// *llmvia.Selector.
//
// 🔴 ES UNA INTERFAZ APARTE Y NO UN MÉTODO MÁS DE ProviderSelector, y la razón no es
// de gusto: ProviderSelector es lo que el pipeline necesita para PEDIR INFERENCIAS, y
// todos sus fakes lo implementan. Meter aquí un método de mantenimiento obligaría a
// cada uno de ellos a crecer un método que no ejercen. Opcional además por lo de
// siempre: sin cable, el calentamiento no ocurre y nada más se rompe.
type Calentador interface {
	Warm(ctx context.Context, tenantID, sessionID string, in llm.ClassifyRequestInput) error
}

// WithCalentador inyecta el emisor del calentamiento. Sin él, `Warm` es un no-op
// silencioso: se pierde el precalentado (la primera inferencia de cada prefijo vuelve
// a pagar el prefill frío) y NADA MÁS — el pipeline funciona igual, solo más lento en
// su primer mensaje.
func WithCalentador(c Calentador) Option { return func(p *Pool) { p.calentador = c } }

// WithWarmTimeout fija el presupuesto de un calentamiento. <=0 se ignora.
func WithWarmTimeout(d time.Duration) Option {
	return func(p *Pool) {
		if d > 0 {
			p.warmTimeout = d
		}
	}
}

// edgeKey identifica la caché que se calienta. Es el EDGE y no la sesión, y esa
// elección es la que hace que el fan-out de un ConfigUpdate no sea una tormenta: un
// Edge multiplexa TODAS las sesiones del tenant sobre un stream (ADR-0008) y tiene UN
// Ollama, así que calentar «por sesión» dispararía N calentamientos idénticos contra
// la misma plaza única — N × 50 s por publicar un catálogo.
type edgeKey struct {
	tenantID string
	edgeID   string
}

// Warm dispara UN calentamiento de la caché de prefijo del Edge indicado. NO BLOQUEA
// y NO DEVUELVE ERROR: sus dos llamantes son el registro de una sesión en el bucle
// Recv del gateway y el fan-out de un `ConfigUpdate`, y ninguno de los dos puede
// esperar ~50 s.
//
// `kind` es el del ConfigUpdate recién empujado, o vacío si el aviso viene del
// handshake. 🔴 AQUÍ SE FILTRA, Y ES DONDE TIENE QUE FILTRARSE: el gateway empuja TRES
// kinds y solo `intents` forma el prefijo del prompt. Sin este filtro, cada rotación de
// JWKS —que no cambia un byte del prompt— costaría un prefill frío de ~50 s de la plaza
// única en la máquina de cada cliente. El gateway no puede decidirlo porque no sabe qué
// hay dentro de un prompt; este paquete sí, porque es quien lo arma.
//
// No acepta `ctx` por el mismo motivo que `Request`, y aquí es todavía más marcado: el
// ctx del handshake muere con el registro de la sesión (milisegundos) y el del PUT de
// intents, con la respuesta HTTP. Heredar cualquiera de los dos mataría TODO
// calentamiento antes de empezar, sin un solo error que lo delatara. El reloj sale de
// warmTimeout y de nada más.
//
// ⚠️ Consecuencia aceptada: un calentamiento en vuelo no se cancela al apagar el
// proceso. Dura poco de todos modos —cuando el stream del Edge cae, la inferencia
// vuelve en el acto con edge_offline— y atarlo al ctx del proceso exigiría que este
// pool guardara el de `Run`, que hoy no guarda y que no siempre existe: el
// calentamiento funciona sin `Run` porque no pasa por la cola.
func (p *Pool) Warm(tenantID, edgeID, sessionID, kind string) {
	if p == nil || p.calentador == nil || p.cfg == nil || tenantID == "" || sessionID == "" {
		return
	}
	if kind != "" && kind != intentcfg.Kind {
		return
	}
	k := edgeKey{tenantID: tenantID, edgeID: edgeID}
	if !p.marcarCalentamiento(k) {
		// Ya hay uno en vuelo para este Edge: el caso normal cuando un ConfigUpdate
		// sale hacia varias sesiones a la vez. No es un fallo y no se loguea.
		return
	}
	go func() {
		defer p.soltarCalentamiento(k)
		p.calentar(tenantID, sessionID)
	}()
}

// calentar hace el trabajo: lee el catálogo, arma la MISMA entrada que usaría una P1
// real y se la entrega al emisor con su propio reloj.
func (p *Pool) calentar(tenantID, sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), p.warmTimeoutFn())
	defer cancel()

	in, ok := p.entrada(ctx, "calentamiento", tenantID, "")
	if !ok {
		// Sin catálogo no hay prefijo que calentar, y es el estado NORMAL de un tenant
		// que no ha publicado intenciones. `entrada` ya dijo lo que había que decir.
		return
	}
	if err := p.calentador.Warm(ctx, tenantID, sessionID, in); err != nil {
		// 🔴 EL MENSAJE NO AFIRMA QUE ALGO SE ROMPIÓ, y no es prudencia: el desenlace
		// más frecuente aquí es un tenant en vía API, donde «no se emitió» es la
		// respuesta CORRECTA y no un fallo (llmvia.ErrViaSinCalentamiento). El error
		// concreto va en el campo y dice cuál de los dos fue; el rótulo no puede
		// decidirlo sin preguntar por la vía, que es lo que este paquete no hace.
		//
		// Y va en DEBUG, no en WARN: nadie estaba esperando esto. Un calentamiento que
		// no sale deja al tenant exactamente como estaba —su primera inferencia paga el
		// prefill frío, que es lo que pagaba antes de esta tarea—, así que subirlo de
		// nivel entrenaría a quien mire los logs a ignorar avisos que sí importan.
		p.log.Debug("calentamiento: no se emitió",
			"tenant_id", tenantID, "session_id", sessionID, "error", err)
		return
	}
	p.log.Debug("calentamiento: emitido contra el Edge",
		"tenant_id", tenantID, "session_id", sessionID)
}

// warmTimeoutFn resuelve el presupuesto. El default se aplica en el uso y no en el
// constructor, mismo criterio que ahoraFn de llmvia: un Pool armado con literal de
// struct en un test se comporta igual que uno construido con New.
func (p *Pool) warmTimeoutFn() time.Duration {
	if p.warmTimeout <= 0 {
		return DefaultWarmTimeout
	}
	return p.warmTimeout
}

// marcarCalentamiento toma el cerrojo «uno en vuelo por Edge». Devuelve false si ya
// había uno.
func (p *Pool) marcarCalentamiento(k edgeKey) bool {
	p.calMu.Lock()
	defer p.calMu.Unlock()
	if p.calEnVuelo == nil {
		p.calEnVuelo = make(map[edgeKey]struct{})
	}
	if _, ok := p.calEnVuelo[k]; ok {
		return false
	}
	p.calEnVuelo[k] = struct{}{}
	return true
}

// soltarCalentamiento libera el cerrojo. Corre siempre, por defer.
func (p *Pool) soltarCalentamiento(k edgeKey) {
	p.calMu.Lock()
	defer p.calMu.Unlock()
	delete(p.calEnVuelo, k)
}
