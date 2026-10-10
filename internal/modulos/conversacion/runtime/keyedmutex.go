// Porta internal/flujos/runtime/keyedmutex.go @ e0159171

package runtime

// keyedmutex.go no exporta nada, ni en el viejo ni aquí: su contrato en rojo es este
// comentario. Los tipos y funciones no exportados nacen en el verde (F8-04b), con sus
// tests (lista en keyedmutex_test.go).
//
// # Qué hará este fichero
//
// Un candado POR CONVERSACIÓN (single-flight, design.md §6/§8; sin broker, ADR-0003),
// indexado por store.Key —la terna (tenant, sesión, contacto)—. Se indexa por el struct,
// no por su String(): el mapa no sale de este proceso.
//
// Piezas (nombres del viejo, que ya estaban en inglés y se conservan):
//
//   - keyedMutex: un mutex global que protege un mapa store.Key → *refLock.
//   - refLock: el mutex de UNA clave más su conteo de referencias (titular + los que
//     esperan).
//   - newKeyedMutex() *keyedMutex: listo para usar, con el mapa vacío.
//   - (*keyedMutex).lock(key store.Key) func(): adquiere el candado de la clave y devuelve
//     la función que lo libera. Patrón de uso: unlock := km.lock(key); defer unlock().
//
// # Promesas
//
//   - KM-1 · Exclusión por clave: dos operaciones sobre la MISMA store.Key se excluyen
//     mutuamente; la segunda espera a que la primera llame a su unlock.
//   - KM-2 · Paralelismo entre claves: claves DISTINTAS progresan en paralelo; tener
//     tomada una no bloquea a otra. Basta con que difiera UNO de los tres campos de la
//     terna (el mismo contacto en otra sesión es otra conversación).
//   - KM-3 · El mapa no crece sin límite: la entrada de una clave se BORRA cuando su
//     conteo de referencias llega a 0, es decir, cuando ya no hay titular ni nadie a la
//     espera. Tras lock+unlock sin contendientes, el mapa vuelve a estar vacío.
//   - KM-4 · Nadie borra la entrada bajo los pies de quien espera: el conteo se
//     incrementa bajo el candado global ANTES de adquirir el candado de la clave. Con un
//     titular y un contendiente esperando, el unlock del titular NO borra la entrada
//     (ref pasa de 2 a 1); la borra el unlock del contendiente.
//   - KM-5 · El candado global no se retiene mientras se espera el de la clave: si se
//     retuviera, un contendiente de la clave A pararía a todos los de la clave B (KM-2).
//   - KM-6 · El unlock libera primero el candado de la clave y después, bajo el global,
//     decrementa y borra si llegó a 0. Es de UN solo uso: el viejo no protege contra una
//     segunda llamada (desbloquear un sync.Mutex libre es un fatal de Go) y no se añade
//     aquí esa protección.
//
// # Quién lo usa (ola siguiente)
//
// El Runtime guarda UNO (trampa T-1 de reglas.md: dos instancias del runtime parten el
// candado sin dar error) y lo toma en los cuatro caminos que tocan el estado de una
// conversación: el entrante, el arranque por API y las dos operaciones de ciclo de vida
// del evento (internal/flujos/runtime/incoming.go:138, start.go:114,
// event_lifecycle.go:333 y :408).
