// Porta internal/flujos/runtime/streak.go @ e0159171

package runtime

// streak.go no exporta nada, ni en el viejo ni aquí: su contrato en rojo es este
// comentario. Los tipos, constantes y funciones no exportados nacen en el verde (F8-04b),
// con sus tests y sus mutantes (lista en streak_test.go). La puerta exportada por la que
// el arranque lee el máximo —(*Runtime).MaxAutoreplyStreak— y el hook que publica cada
// racha cerrada son de runtime_engine.go (ola siguiente).
//
// # Qué hará este fichero: rachas de auto-respuestas (Plan 049 · Opción A, OBSERVAR)
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
// Piezas (nombres del viejo; los que estaban en español pasan a inglés en el verde, E-11):
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
