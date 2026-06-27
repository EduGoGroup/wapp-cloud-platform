package runtime

import (
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
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
