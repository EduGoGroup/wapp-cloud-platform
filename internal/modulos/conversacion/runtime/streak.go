// Porta internal/flujos/runtime/streak.go @ e0159171

package runtime

// streak.go no exporta nada, ni en el viejo ni aquí: su contrato es este comentario, y
// sus tests y mutantes (streak_test.go) salen de él. La puerta exportada por la que el
// arranque lee el máximo —(*Runtime).MaxAutoreplyStreak— y el hook que publica cada
// racha cerrada son de runtime_engine.go (ola siguiente).
//
// # Qué hace este fichero: rachas de auto-respuestas (Plan 049 · Opción A, OBSERVAR)
//
// Una racha es el número de auto-respuestas CONSECUTIVAS que el motor emite en una
// conversación dentro de un mismo EPISODIO. Un episodio termina cuando (a) el estado de
// la conversación se destruye —flujo terminado, escape o vencimiento por TTL— o (b)
// pasan 30 minutos sin una sola auto-respuesta.
//
// 🔴 Este contador OBSERVA; NUNCA DECIDE. No hay umbral, no corta, no silencia a nadie:
// cuenta y publica. Cortar es la Opción B del Plan 049, aplazada hasta tener datos con
// los que calibrar el umbral.
//
// 🔴 QUIÉN MATERIALIZA EL CIERRE (b). Nadie llama a nada cuando NO pasa nada: el cierre
// por inactividad no tiene disparador propio, así que solo puede detectarse cuando
// alguien vuelve a mirar el mapa. Hay dos miradas y las dos son perezosas: el siguiente
// Inc SOBRE ESA MISMA CLAVE (solo sirve si la conversación vuelve) y la barrida del
// SCRAPE, dentro de Max (la que cubre el caso mayoritario: la conversación que se
// abandona y no vuelve nunca). La segunda es la que hace que la métrica no mienta.
//
// 🔴 POR QUÉ SE MIDE EL EPISODIO Y NO EL MENSAJE. Los dos fenómenos que hay que
// distinguir producen la misma secuencia entrante→saliente: el recorrido legítimo largo
// (el catálogo pagina de 5 en 5, Plan 049 §5: un pedido real son 20-30 auto-respuestas
// seguidas, todas pedidas por una persona) y el bucle contra un autorespondedor de
// terceros (§2: cientos, ninguna pedida por nadie). Lo que los separa es CUÁNTO DURA EL
// EPISODIO y a qué ritmo avanza; contar con reinicio por entrante daría 1 en los dos.
//
// Piezas (nombres del viejo; los que estaban en español pasaron a inglés, E-11: el campo
// `rachas` es `streaks`, y los locales cerradas/viva/corte/mayor/vieja/masVieja son
// closed/alive/cutoff/longest/oldestKey/oldest):
//
//   - streakIdleTTL = 30 * time.Minute: la inactividad que da por TERMINADO el episodio.
//   - streakMaxEntries = 10000: tope de conversaciones vivas en el mapa (el mismo que el
//     token-bucket de platform/ratelimit).
//   - streakCounter: un mutex, el mapa store.Key → entrada (n auto-respuestas del episodio
//     vivo, lastSeen de la última), idleTTL, maxEntries y el hook onClose.
//   - newStreakCounter(idleTTL time.Duration, maxEntries int, onClose func(streak int)).
//   - (*streakCounter).Inc(key store.Key, now time.Time) int
//   - (*streakCounter).Close(key store.Key, now time.Time)
//   - (*streakCounter).Max(now time.Time) int
//
// # Promesas (regla RT-11; trampas T-7 y T-12 de reglas.md)
//
// Reloj y estado:
//
//   - ST-1 · Reloj INYECTADO: Inc, Close y Max reciben `now` por parámetro; nada de este
//     fichero llama a time.Now(). Es lo que hace testeable el vencimiento de media hora.
//   - ST-2 · Estado EN MEMORIA y sin broker (ADR-0003): si un reinicio lo borra no pasa
//     nada, porque solo observa. Sin goroutine ni ticker de fondo: toda evicción es
//     PEREZOSA y solo ocurre en los dos momentos en que alguien ya mira el mapa: el Inc
//     que lo encuentra lleno y el scrape (Max).
//   - ST-3 · Seguro para uso concurrente: una goroutine por entrante; conversaciones
//     distintas tocan el mismo mapa a la vez.
//   - ST-4 · La clave es la terna ENTERA (tenant, sesión, contacto): dos contactos de la
//     misma sesión, o el mismo contacto en dos sesiones, NO comparten racha.
//
// Constructor:
//
//   - ST-5 · idleTTL <= 0 se normaliza a 30 min y maxEntries <= 0 a 10.000: un llamante
//     que pase el valor cero no acaba con un contador que cierra el episodio en cada
//     respuesta ni con uno que desaloja en cada clave nueva. El Runtime lo construye con
//     (0, 0, hook).
//   - ST-6 · onClose es OPCIONAL: con nil el contador cuenta igual, solo que no publica.
//
// Inc (registra UNA auto-respuesta emitida y devuelve la racha VIVA tras incrementar):
//
//   - ST-7 · Sin entrada: empieza un episodio y devuelve 1.
//   - ST-8 · Entrada viva: +1 y refresca lastSeen a `now`. Las auto-respuestas se
//     ACUMULAN: 🔴 un entrante del contacto NO reinicia la racha (en un motor reactivo toda
//     auto-respuesta responde a un entrante; si el entrante reiniciara, la racha valdría
//     siempre 1). Un recorrido legítimo de 30 emisiones cada 20 s da 1, 2, …, 30 y no
//     observa ninguna racha por el camino.
//   - ST-9 · Entrada VENCIDA (lastSeen anterior a now − idleTTL): esa racha había terminado
//     por inactividad y nadie estaba ahí para cerrarla. Se cierra AHORA, se reporta con su
//     longitud, y arranca una nueva en 1. Devuelve 1 (la viva), nunca la cerrada.
//   - ST-10 · El límite es ESTRICTO: a exactamente idleTTL de la última emisión la racha
//     SIGUE viva (se usa «anterior a», no «anterior o igual»). Inc y Max usan el MISMO
//     criterio.
//   - ST-11 · Lo que suma 1 es una EMISIÓN del motor (una llamada a send), no un turno: un
//     entrante puede provocar varias, y también cuentan los avisos de error del sistema.
//     La racha ACOTA POR ARRIBA los turnos.
//   - ST-12 · Receptor nil: devuelve 0 sin entrar en pánico.
//
// Tope (solo se evalúa en el Inc de una clave NUEVA con el mapa lleno, len >= maxEntries):
//
//   - ST-13 · Un SOLO recorrido del mapa: purga las vencidas y, a la vez, localiza la
//     superviviente más antigua por lastSeen.
//   - ST-14 · Si la purga liberó algo, no se desaloja a nadie más; cada vencida se
//     reporta con su longitud.
//   - ST-15 · Si no había NINGUNA vencida, se desaloja la MÁS ANTIGUA y se REPORTA igual:
//     una racha desalojada es una racha observada, no una racha perdida. La clave nueva
//     entra siempre con racha 1.
//   - ST-16 · Con hueco libre, o sobre una clave que ya existe, Inc no recorre el mapa.
//
// Close (cierra el episodio de esa conversación):
//
//   - ST-17 · Con racha viva: la reporta UNA vez por onClose con su longitud y borra la
//     entrada. El Inc siguiente sobre esa clave devuelve 1.
//   - ST-18 · Sin entrada: no reporta nada (cerrar una conversación que nunca
//     auto-respondió no es una racha de 0).
//   - ST-19 · Idempotente: el segundo Close sobre la misma clave no encuentra entrada y
//     no duplica la observación.
//   - ST-20 · Una entrada VENCIDA por inactividad se reporta IGUAL: la racha existió.
//   - ST-21 · `now` NO se usa: se acepta por simetría con Inc (cerrar es cerrar).
//   - ST-22 · Receptor nil: no hace nada.
//
// Max (fuente del gauge; 🔴 un getter CON EFECTOS, y es deliberado — trampa T-7:
// `/metrics` no es inocuo):
//
//   - ST-23 · Devuelve la mayor de las rachas que SIGUEN VIVAS: ni la suma ni la última.
//     0 si no queda ninguna. Baja cuando esa conversación se cierra.
//   - ST-24 · BARRE las vencidas en el mismo recorrido: las borra y las reporta por
//     onClose. Es el ÚNICO sitio donde se materializa el cierre por inactividad de la
//     conversación que se abandona y no vuelve; sin scrape esos episodios no se cierran
//     (se quedan hasta que el tope los desaloje).
//   - ST-25 · Una vencida no cuenta para el máximo aunque fuera la más larga: el gauge no
//     se queda clavado en rachas fosilizadas.
//   - ST-26 · Lo que barre lo BORRA: dos Max seguidos no reportan dos veces la misma racha.
//   - ST-27 · Receptor nil: devuelve 0.
//
// Reporte:
//
//   - ST-28 · onClose se invoca SIEMPRE FUERA del mutex: las longitudes se acumulan dentro
//     de la sección crítica y se emiten con el candado ya suelto. El hook acaba en
//     Prometheus y el GaugeFunc llama a Max DURANTE el Gather: un hook que reentre al
//     contador (Inc, Close o Max) no puede interbloquear.
//   - ST-29 · Nunca se reporta una longitud <= 0.
//
// # Lo que este fichero NO garantiza y vigila otro
//
// RT-11 pide además que TODO store.Delete del runtime cierre la racha (6 caminos en el
// viejo). Eso lo vigila el candado AST streak_invariante_test.go (trampa T-12: un Delete
// nuevo sin su Close infla wapp_flow_autoreply_streak_max media hora, y cinco de los seis
// cierres se podían borrar con la suite en verde); nace con los ficheros que hacen los
// Delete, y su constante se RE-MIDE sobre el código nuevo.
//
// # Comentario del viejo que no casa con su código
//
// internal/flujos/runtime/streak.go:107 dice «idleTTL y maxEntries no nulos se
// normalizan»; el código normaliza los NO POSITIVOS (<= 0), que es lo que promete ST-5.

import (
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
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
// Es seguro para uso concurrente (ST-3): el entrante corre una goroutine por mensaje y
// dos entrantes de conversaciones distintas tocan el mismo mapa a la vez.
type streakCounter struct {
	mu sync.Mutex
	// streaks era `rachas` en el viejo (E-11).
	streaks map[store.Key]*streakEntry

	idleTTL    time.Duration
	maxEntries int

	// onClose recibe la longitud de cada racha que se CIERRA. Es el hook hacia
	// Prometheus (el histograma de rachas) y, como el resto de hooks del runtime, es
	// OPCIONAL: sin él el contador se comporta igual. Observar es opcional; decidir no.
	onClose func(streak int)
}

type streakEntry struct {
	n        int       // auto-respuestas emitidas en el episodio vivo.
	lastSeen time.Time // instante de la última auto-respuesta; base de la evicción perezosa.
}

// newStreakCounter crea el contador. onClose se invoca con la longitud de cada racha
// que se CIERRA (>0), y SIEMPRE fuera del mutex.
//
// idleTTL y maxEntries NO POSITIVOS se normalizan a las constantes del paquete (ST-5)
// para que un llamante que pase el valor cero no acabe con un contador que cierra el
// episodio en cada respuesta (idleTTL=0) o que desaloja en cada clave nueva
// (maxEntries=0).
func newStreakCounter(idleTTL time.Duration, maxEntries int, onClose func(streak int)) *streakCounter {
	if idleTTL <= 0 {
		idleTTL = streakIdleTTL
	}
	if maxEntries <= 0 {
		maxEntries = streakMaxEntries
	}
	return &streakCounter{
		streaks:    make(map[store.Key]*streakEntry),
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

	// closed (`cerradas` en el viejo) acumula, DENTRO de la sección crítica, las
	// longitudes que hay que reportar; el hook se invoca al final, ya sin el candado.
	// Ver report().
	var closed []int
	var alive int

	c.mu.Lock()
	cutoff := now.Add(-c.idleTTL)
	e, ok := c.streaks[key]
	switch {
	case !ok:
		// Clave nueva: es el ÚNICO punto del camino caliente donde se evalúa el tope.
		// La evicción es perezosa (sin goroutine ni ticker, ADR-0003) y solo cuesta
		// cuando el mapa está lleno. UN SOLO recorrido, no dos: ver evictLocked.
		if len(c.streaks) >= c.maxEntries {
			closed = append(closed, c.evictLocked(cutoff)...)
		}
		e = &streakEntry{n: 1}
		c.streaks[key] = e
	case e.lastSeen.Before(cutoff):
		// Vencida por inactividad: se cierra la vieja y arranca la nueva en 1. El
		// límite es estricto (Before): a exactamente idleTTL la racha sigue viva.
		closed = append(closed, e.n)
		e.n = 1
	default:
		e.n++
	}
	e.lastSeen = now
	alive = e.n
	c.mu.Unlock()

	c.report(closed)
	return alive
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
// El instante (`now` en el viejo) se acepta por simetría con Inc y para que la firma no
// cambie si un día el cierre necesita distinguirlo; hoy el cierre no depende del reloj
// —cerrar es cerrar—, por eso el parámetro no tiene nombre (ST-21).
func (c *streakCounter) Close(key store.Key, _ time.Time) {
	if c == nil {
		return
	}

	var closed []int

	c.mu.Lock()
	if e, ok := c.streaks[key]; ok {
		delete(c.streaks, key)
		if e.n > 0 {
			closed = append(closed, e.n)
		}
	}
	c.mu.Unlock()

	c.report(closed)
}

// Max BARRE las rachas vencidas por inactividad y devuelve la mayor de las que
// SIGUEN VIVAS (fuente del gauge). 0 si no queda ninguna.
//
// 🔴 SÍ, UN GETTER CON EFECTOS, Y ES DELIBERADO. Este es el sitio donde se
// materializa el cierre por inactividad —el caso (b) de la cabecera— y sin él la
// métrica MIENTE EN LAS DOS DIRECCIONES:
//
//   - por abajo, el histograma. Los Close cuelgan de destrucciones del estado
//     conversacional, y todas viven en caminos que arrancan en un ENTRANTE. La
//     conversación que se abandona —que es la mayoría— no vuelve a producir ningún
//     entrante, así que su racha nunca se cerraría y nunca llegaría a Observe: el
//     histograma solo recogería episodios de conversaciones que siguen vivas, con un
//     sesgo no cuantificado justo en la muestra que el §9 quiere usar para el p99.
//     (El TTL conversacional no salva esto: viene DESACTIVADO por defecto y también
//     se evalúa desde el entrante.)
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

	var closed []int
	longest := 0 // `mayor` en el viejo.

	c.mu.Lock()
	cutoff := now.Add(-c.idleTTL)
	for k, e := range c.streaks {
		// Mismo criterio ESTRICTO que Inc (Before): a exactamente idleTTL sigue viva.
		if e.lastSeen.Before(cutoff) {
			if e.n > 0 {
				closed = append(closed, e.n)
			}
			delete(c.streaks, k)
			continue
		}
		if e.n > longest {
			longest = e.n
		}
	}
	c.mu.Unlock()

	// Borrada del mapa, la racha ya no puede reportarse dos veces: un segundo Max
	// seguido no encuentra la entrada y no observa nada. Lo fija un test.
	c.report(closed)
	return longest
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
// Nunca emite una longitud <= 0 (ST-29).
func (c *streakCounter) report(lengths []int) {
	if c.onClose == nil {
		return
	}
	for _, n := range lengths {
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
//  1. purga las entradas cuya última auto-respuesta quedó antes de cutoff (vencidas
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
// normal bajo carga—, CADA clave nueva pagaba DOS escaneos completos de hasta
// maxEntries (10.000) bajo el mutex global, dentro del camino de send. Ahora paga UNO.
// El coste que QUEDA es ese O(n) único: sigue siendo el peor caso del camino caliente y
// sigue serializando contra todos los Inc/Close en vuelo, así que el tope no es un
// número decorativo — subirlo encarece este escaneo linealmente. Solo se paga en la
// clave nueva y solo con el mapa lleno; con hueco libre, Inc no llama aquí.
//
// Nota sobre la candidata: solo se consideran SUPERVIVIENTES (las vencidas ya se
// borraron en este mismo bucle), lo cual es exactamente correcto porque el desalojo
// solo ocurre cuando no hubo ninguna vencida y, por tanto, todas eran supervivientes.
func (c *streakCounter) evictLocked(cutoff time.Time) []int {
	var (
		closed    []int
		oldestKey store.Key    // `vieja` en el viejo.
		oldest    *streakEntry // `masVieja` en el viejo.
	)
	for k, e := range c.streaks {
		if e.lastSeen.Before(cutoff) {
			closed = append(closed, e.n)
			delete(c.streaks, k)
			continue
		}
		if oldest == nil || e.lastSeen.Before(oldest.lastSeen) {
			oldestKey, oldest = k, e
		}
	}
	if len(closed) > 0 || oldest == nil {
		return closed
	}
	delete(c.streaks, oldestKey)
	return []int{oldest.n}
}
