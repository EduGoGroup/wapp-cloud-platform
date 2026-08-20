// Rachas de auto-respuestas por conversación (Plan 049 · Opción A, OBSERVAR).
//
// Una racha es el número de auto-respuestas CONSECUTIVAS que el motor emite en una
// conversación dentro de un mismo EPISODIO. Un episodio termina cuando (a) el estado
// de la conversación se destruye —flujo terminado, escape o vencimiento por TTL— o
// (b) pasan streakIdleTTL sin una sola auto-respuesta.
//
// 🔴 QUIÉN MATERIALIZA EL CIERRE (b). Nadie llama a nada cuando NO pasa nada: el
// cierre por inactividad no tiene disparador propio, así que solo puede detectarse
// cuando alguien vuelve a mirar el mapa. Hay dos miradas y las dos son perezosas
// (ADR-0003: ni goroutine ni ticker de fondo):
//
//   - el siguiente Inc SOBRE ESA MISMA CLAVE — solo sirve si la conversación vuelve;
//   - la barrida del SCRAPE, dentro de Max — es la que cubre el caso mayoritario, la
//     conversación que simplemente se abandona y no vuelve nunca.
//
// La segunda es la que hace que la métrica no mienta, y por eso Max recibe `now` y
// tiene efectos: ver su comentario. Sin ella el histograma solo vería episodios de
// conversaciones vivas —sesgo no cuantificado en la muestra que alimenta el p99 del
// §9— y el gauge se quedaría clavado en rachas fosilizadas.
//
// 🔴 Un entrante del contacto NO cierra el episodio, y esa es la decisión deliberada
// de este contador. En un motor reactivo TODA auto-respuesta es la respuesta a un
// entrante: si el entrante reiniciara la cuenta, la racha valdría siempre 1 y la
// métrica sería ciega justo a lo que existe para ver. Los dos fenómenos que hay que
// distinguir producen exactamente la misma secuencia entrante→saliente:
//
//   - el recorrido legítimo largo — el catálogo pagina de 5 en 5 (Plan 049 §5), así
//     que un pedido real produce 20-30 auto-respuestas seguidas, todas pedidas por
//     una persona;
//   - el bucle contra un autorespondedor de terceros (§2) — que produce cientos,
//     ninguna pedida por nadie.
//
// Lo que los separa no es ningún mensaje concreto —en ambos casos hay un entrante
// antes de cada saliente— sino CUÁNTO DURA EL EPISODIO y a qué ritmo avanza. Por eso
// se mide el episodio y no el mensaje: contar mensajes con reinicio por entrante
// devolvería 1 en los dos casos y no habría nada que observar.
//
// 🔴 Lo que suma 1 es una EMISIÓN del motor —una llamada a send—, NO un turno
// conversacional, y conviene tenerlo presente al leer el número. Un mismo entrante
// puede provocar varias emisiones (sendResumeSummary manda el resumen del rescate y
// después la pantalla del flujo; el menú y el flujo van por el mismo patrón), y
// también emiten los avisos de error del sistema. Por eso la racha ACOTA POR ARRIBA
// el número de turnos: nunca se queda corta, pero puede ir por delante. Afinarla a
// turnos reales exigiría un concepto de «turno» que hoy el runtime no tiene, y la
// Opción A no lo inventa (ver el comentario de send.go).
//
// Este contador OBSERVA; NUNCA DECIDE. No hay umbral, no corta, no silencia a nadie:
// cuenta y publica. Cortar es la Opción B del Plan 049, aplazada hasta tener 2-4
// semanas de estos datos con los que calibrar el umbral (§9). Fijar el umbral hoy,
// sin datos, es cómo se deja mudo a un cliente a mitad de un pedido (§5, §6).
//
// Estado EN MEMORIA y sin broker (ADR-0003): si un reinicio lo borra no pasa nada,
// porque solo observa. Sin goroutine ni ticker de fondo — la evicción es PEREZOSA,
// mismo patrón que platform/ratelimit: la disparan el Inc que encuentra el mapa lleno
// y el scrape de Prometheus (Max), que son los dos únicos momentos en los que alguien
// ya está mirando el mapa.

package runtime

import (
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// streakIdleTTL es la inactividad tras la cual se da por TERMINADO el episodio: una
// conversación que lleva media hora sin recibir una auto-respuesta ya no está en la
// misma racha, y lo que venga después es un episodio nuevo. Es holgado a propósito
// (el recorrido legítimo del §5 avanza cada pocos segundos, jamás roza este techo) y
// solo sirve para que una racha abandonada no se sume a la siguiente.
const streakIdleTTL = 30 * time.Minute

// streakMaxEntries acota el número de conversaciones vivas en el mapa: al superarlo
// se hace una barrida perezosa (evita crecimiento no acotado ante muchas claves,
// mismo tope que el token-bucket de platform/ratelimit).
const streakMaxEntries = 10000

// streakCounter cuenta rachas de auto-respuestas consecutivas por conversación,
// indexadas por store.Key (tenant|sesión|contacto) igual que el keyedMutex: se indexa
// por el struct, no por su String(), porque el mapa no sale de este proceso.
//
// Es seguro para uso concurrente: HandleIncoming corre una goroutine por entrante y
// dos entrantes de conversaciones distintas tocan el mismo mapa a la vez.
type streakCounter struct {
	mu     sync.Mutex
	rachas map[store.Key]*streakEntry

	idleTTL    time.Duration
	maxEntries int

	// onClose recibe la longitud de cada racha que se CIERRA. Es el hook hacia
	// Prometheus (el histograma de rachas) y, como el resto de hooks del runtime, es
	// OPCIONAL: sin él el contador se comporta igual. Observar es opcional; decidir no.
	onClose func(racha int)
}

type streakEntry struct {
	n        int       // auto-respuestas emitidas en el episodio vivo.
	lastSeen time.Time // instante de la última auto-respuesta; base de la evicción perezosa.
}

// newStreakCounter crea el contador. onClose se invoca con la longitud de cada racha
// que se CIERRA (>0), y SIEMPRE fuera del mutex.
//
// idleTTL y maxEntries no nulos se normalizan a las constantes del paquete para que
// un llamante que pase el cero-valor no acabe con un contador que cierra el episodio
// en cada respuesta (idleTTL=0) o que desaloja en cada MISS (maxEntries=0).
func newStreakCounter(idleTTL time.Duration, maxEntries int, onClose func(racha int)) *streakCounter {
	if idleTTL <= 0 {
		idleTTL = streakIdleTTL
	}
	if maxEntries <= 0 {
		maxEntries = streakMaxEntries
	}
	return &streakCounter{
		rachas:     make(map[store.Key]*streakEntry),
		idleTTL:    idleTTL,
		maxEntries: maxEntries,
		onClose:    onClose,
	}
}

// Inc registra UNA auto-respuesta emitida y devuelve la racha viva tras incrementar.
//
// Tres casos, y el del medio es el que hace falta explicar:
//   - no hay entrada: empieza un episodio, racha = 1.
//   - hay entrada pero su lastSeen quedó fuera del idleTTL: esa racha HABÍA TERMINADO
//     por inactividad y nadie estaba ahí para cerrarla (nada dispara un cierre cuando
//     no pasa nada). Se cierra AHORA, se reporta, y se empieza una nueva en 1. Sin
//     esto, una conversación retomada al día siguiente sumaría sobre la racha de ayer
//     y la métrica publicaría episodios que nunca existieron.
//   - la entrada está viva: +1 y se refresca lastSeen.
//
// Devuelve siempre la racha VIVA (la nueva), nunca la que se acaba de cerrar. Sobre un
// receptor nil devuelve 0: el llamante no tiene que saber si hay contador cableado.
func (c *streakCounter) Inc(key store.Key, now time.Time) int {
	if c == nil {
		return 0
	}

	// cerradas acumula, DENTRO de la sección crítica, las longitudes que hay que
	// reportar; el hook se invoca al final, ya sin el candado. Ver report().
	var cerradas []int
	var viva int

	c.mu.Lock()
	corte := now.Add(-c.idleTTL)
	e, ok := c.rachas[key]
	switch {
	case !ok:
		// MISS: es el ÚNICO punto del camino caliente donde se evalúa el tope. La
		// evicción es perezosa (sin goroutine ni ticker, ADR-0003) y solo cuesta
		// cuando el mapa está lleno. UN SOLO recorrido, no dos: ver evictLocked.
		if len(c.rachas) >= c.maxEntries {
			cerradas = append(cerradas, c.evictLocked(corte)...)
		}
		e = &streakEntry{n: 1}
		c.rachas[key] = e
	case e.lastSeen.Before(corte):
		// Vencida por inactividad: se cierra la vieja y arranca la nueva en 1. El
		// límite es estricto (Before): a exactamente idleTTL la racha sigue viva.
		cerradas = append(cerradas, e.n)
		e.n = 1
	default:
		e.n++
	}
	e.lastSeen = now
	viva = e.n
	c.mu.Unlock()

	c.report(cerradas)
	return viva
}

// Close cierra el episodio de esa conversación. Si había racha viva, la reporta por
// onClose y borra la entrada. Es idempotente y nil-safe.
//
// Lo llama el runtime cuando el estado conversacional se destruye: flujo terminado,
// escape o vencimiento del TTL conversacional. El segundo Close sobre la misma clave
// no encuentra entrada y no hace nada, así que puede colgarse de varios caminos de
// cierre sin llevar la cuenta de cuál llegó primero.
//
// Si la entrada estaba VENCIDA por inactividad se reporta IGUAL: la racha existió, solo
// que terminó antes de que llegara este cierre. Descartarla perdería el dato justo en
// las conversaciones que se abandonan a medias, que son las que interesa ver.
//
// now se acepta por simetría con Inc y para que la firma no cambie si un día el cierre
// necesita distinguir el instante; hoy el cierre no depende del reloj —cerrar es
// cerrar—, por eso el parámetro no se usa.
func (c *streakCounter) Close(key store.Key, now time.Time) {
	if c == nil {
		return
	}

	var cerradas []int

	c.mu.Lock()
	if e, ok := c.rachas[key]; ok {
		delete(c.rachas, key)
		if e.n > 0 {
			cerradas = append(cerradas, e.n)
		}
	}
	c.mu.Unlock()

	c.report(cerradas)
}

// Max BARRE las rachas vencidas por inactividad y devuelve la mayor de las que
// SIGUEN VIVAS (fuente del gauge). 0 si no queda ninguna.
//
// 🔴 SÍ, UN GETTER CON EFECTOS, Y ES DELIBERADO. Este es el sitio donde se
// materializa el cierre por inactividad —el caso (b) de la cabecera— y sin él la
// métrica MIENTE EN LAS DOS DIRECCIONES:
//
//   - por abajo, el histograma. Los seis Close cuelgan de destrucciones del estado
//     conversacional, y las seis viven en caminos que arrancan en un ENTRANTE. La
//     conversación que se abandona —que es la mayoría— no vuelve a producir ningún
//     entrante, así que su racha nunca se cerraría y nunca llegaría a Observe: el
//     histograma solo recogería episodios de conversaciones que siguen vivas, con un
//     sesgo no cuantificado justo en la muestra que el §9 quiere usar para el p99.
//     (El TTL conversacional no salva esto: viene DESACTIVADO por defecto —con
//     ConversationTTL<=0, conversationExpired devuelve false— y también se evalúa
//     desde el entrante.)
//   - por arriba, el gauge. Sin barrido, un catálogo legítimo de 30 que el cliente
//     abandonó deja su entrada en el mapa PARA SIEMPRE y el gauge se queda clavado
//     en 30: no distinguiría «bucle vivo AHORA» de «racha vieja fosilizada», que es
//     exactamente la distinción para la que existe (pregunta 3 del §9: «¿ocurre
//     siquiera un bucle con terceros?»).
//
// 🔴 POR QUÉ AQUÍ Y NO EN UNA GOROUTINE DE FONDO. Un ticker que barriera cada minuto
// sería lo natural en otro repo; aquí no, porque el ADR-0003 proscribe los procesos
// de fondo en este camino y porque no hacen falta: el scrape de Prometheus YA pasa
// cada 15-60 s y YA recorre el mapa entero para calcular el máximo. Colgar el barrido
// de ese recorrido no añade ni un escaneo: es el MISMO O(conversaciones vivas) que ya
// se pagaba, y se paga fuera del camino caliente. La contrapartida honesta es que sin
// scrape no hay cierre por inactividad —si nadie raspa /metrics, las rachas
// abandonadas se quedan en el mapa hasta que el tope las desaloje—, y es aceptable
// porque quien no raspa tampoco está mirando la métrica.
//
// Las barridas se reportan por onClose FUERA del mutex, como todo lo demás (ver
// report). Nil-safe: sobre un receptor nil devuelve 0.
func (c *streakCounter) Max(now time.Time) int {
	if c == nil {
		return 0
	}

	var cerradas []int
	mayor := 0

	c.mu.Lock()
	corte := now.Add(-c.idleTTL)
	for k, e := range c.rachas {
		// Mismo criterio ESTRICTO que Inc (Before): a exactamente idleTTL sigue viva.
		if e.lastSeen.Before(corte) {
			if e.n > 0 {
				cerradas = append(cerradas, e.n)
			}
			delete(c.rachas, k)
			continue
		}
		if e.n > mayor {
			mayor = e.n
		}
	}
	c.mu.Unlock()

	// Borrada del mapa, la racha ya no puede reportarse dos veces: un segundo Max
	// seguido no encuentra la entrada y no observa nada. Lo fija un test.
	c.report(cerradas)
	return mayor
}

// report invoca onClose para cada racha cerrada.
//
// 🔴 SIEMPRE FUERA DEL MUTEX, y esto no es cosmético. onClose acaba en Prometheus
// —código de terceros que el runtime no controla— y un hook que reentrara al contador
// (directamente, o publicando un gauge que a su vez llame a Max()) se quedaría colgado
// esperando un candado que él mismo tiene tomado: sync.Mutex no es reentrante y eso es
// un deadlock, no una espera. Por eso las longitudes se acumulan en un slice local
// dentro de la sección crítica y se emiten aquí, con el candado ya soltado.
//
// ⚠️ Y desde que Max barre, ese escenario DEJÓ DE SER HIPOTÉTICO: el GaugeFunc de
// wapp_flow_autoreply_streak_max llama a Max DURANTE el Gather de Prometheus, así que
// este onClose —un Observe sobre el histograma hermano— se ejecuta dentro del scrape.
// Funciona porque (a) el candado del contador ya está suelto cuando se llega aquí, y
// (b) Observe toca OTRA métrica del registry, no reentra al gauge que está colectando.
// Si alguien cambia el hook por algo que vuelva a preguntarle al contador —o que
// colecte el propio gauge—, el orden «acumular dentro, emitir fuera» es lo único que
// impide el interbloqueo. No lo toques sin releer esto.
//
// Nil-safe: sin hook cableado no hay nada que emitir y el contador sigue contando.
func (c *streakCounter) report(rachas []int) {
	if c.onClose == nil {
		return
	}
	for _, n := range rachas {
		if n > 0 {
			c.onClose(n)
		}
	}
}

// evictLocked hace hueco en un mapa lleno y devuelve las longitudes desalojadas para
// que el llamante las reporte FUERA del mutex. Debe llamarse con el lock tomado.
// Borrar durante el recorrido del mapa es legal en Go.
//
// Dos políticas, UN SOLO RECORRIDO, y lo segundo es el punto:
//
//  1. purga las entradas cuya última auto-respuesta quedó antes de corte (vencidas
//     por inactividad, igual que el caso del medio de Inc);
//  2. si la purga no liberó NADA —todas vivas— desaloja la MÁS ANTIGUA por lastSeen
//     y la REPORTA igual: una racha desalojada es una racha observada, no una racha
//     perdida. La alternativa (dejar crecer el mapa) convierte un contador de
//     observación en una fuga de memoria, y la otra (descartar la conversación nueva
//     en silencio) sesga la métrica hacia las conversaciones viejas justo cuando hay
//     más tráfico del normal.
//
// 🔴 La candidata a desalojo se calcula MIENTRAS se purga, no en una segunda pasada.
// Antes eran dos funciones y, con el mapa lleno y ninguna entrada vencida —el caso
// normal bajo carga—, CADA MISS pagaba DOS escaneos completos de hasta maxEntries
// (10.000) bajo el mutex global, dentro del camino de send. Ahora paga UNO. El coste
// que QUEDA es ese O(n) único: sigue siendo el peor caso del camino caliente y sigue
// serializando contra todos los Inc/Close en vuelo, así que el tope no es un número
// decorativo — subirlo encarece este escaneo linealmente. Solo se paga en el MISS y
// solo con el mapa lleno; con hueco libre, Inc no llama aquí.
//
// Nota sobre la candidata: solo se consideran SUPERVIVIENTES (las vencidas ya se
// borraron en este mismo bucle), lo cual es exactamente correcto porque el desalojo
// solo ocurre cuando no hubo ninguna vencida y, por tanto, todas eran supervivientes.
func (c *streakCounter) evictLocked(corte time.Time) []int {
	var (
		cerradas []int
		vieja    store.Key
		masVieja *streakEntry
	)
	for k, e := range c.rachas {
		if e.lastSeen.Before(corte) {
			cerradas = append(cerradas, e.n)
			delete(c.rachas, k)
			continue
		}
		if masVieja == nil || e.lastSeen.Before(masVieja.lastSeen) {
			vieja, masVieja = k, e
		}
	}
	if len(cerradas) > 0 || masVieja == nil {
		return cerradas
	}
	delete(c.rachas, vieja)
	return []int{masVieja.n}
}
