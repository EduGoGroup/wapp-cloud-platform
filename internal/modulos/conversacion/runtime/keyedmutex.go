// Porta internal/flujos/runtime/keyedmutex.go @ e0159171

package runtime

// keyedmutex.go no exporta nada, ni en el viejo ni aquí: su contrato es este comentario,
// y sus tests (keyedmutex_test.go) salen de él, uno por promesa.
//
// # Qué hace este fichero
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
// # Quién lo usa
//
// El Runtime guarda UNO (trampa T-1 de reglas.md: dos instancias del runtime parten el
// candado sin dar error) y lo toma en los cuatro caminos que tocan el estado de una
// conversación: el entrante, el arranque por API y las dos operaciones de ciclo de vida
// del evento (en el viejo, internal/flujos/runtime/incoming.go:138, start.go:114,
// event_lifecycle.go:333 y :408; aquí, HandleIncoming en incoming.go, Start en start.go y
// las dos de event_lifecycle_cancel.go).

import (
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// keyedMutex serializa el acceso por conversación (single-flight, design.md
// §6/§8; sin broker, ADR-0003): dos operaciones sobre la MISMA store.Key se
// excluyen mutuamente; claves distintas progresan en paralelo.
//
// Cada clave tiene su propio mutex con conteo de referencias; la entrada se
// elimina del mapa cuando ya no hay titulares ni a la espera, de modo que el
// mapa no crece de forma ilimitada con el tiempo. El conteo se incrementa bajo
// el lock global ANTES de adquirir el lock de la clave: así nadie borra la
// entrada mientras otra goroutine está esperando por ella.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[store.Key]*refLock
}

// refLock es el candado de UNA clave. ref cuenta al titular más los que esperan y solo
// se lee o escribe con keyedMutex.mu tomado.
type refLock struct {
	mu  sync.Mutex
	ref int
}

func newKeyedMutex() *keyedMutex {
	return &keyedMutex{locks: make(map[store.Key]*refLock)}
}

// lock adquiere el lock de la clave y devuelve la función para liberarlo. El
// patrón de uso es: unlock := km.lock(key); defer unlock().
func (k *keyedMutex) lock(key store.Key) func() {
	k.mu.Lock()
	rl, ok := k.locks[key]
	if !ok {
		rl = &refLock{}
		k.locks[key] = rl
	}
	rl.ref++
	k.mu.Unlock()

	// El global ya está suelto (KM-5): esperar aquí no para a las demás claves.
	rl.mu.Lock()

	return func() {
		rl.mu.Unlock()
		k.mu.Lock()
		rl.ref--
		if rl.ref == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
