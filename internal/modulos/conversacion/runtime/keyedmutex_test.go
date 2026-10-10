package runtime

import (
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Tests de keyedmutex.go, uno por promesa de su comentario-contrato. Van en el paquete
// INTERNO porque el fichero no exporta nada. Ninguno duerme: que alguien «está esperando»
// se observa leyendo el conteo `ref` bajo el candado global (es exactamente lo que KM-4
// promete que se incrementa ANTES de esperar), y que alguien «entró» o «terminó», con un
// canal. kmWatchdog solo acota cuánto se aguarda un suceso que TIENE que ocurrir: con el
// código correcto no vence nunca, y con un mutante da un fallo legible en vez de un cuelgue.
//
// Mutantes (nivel complejo), aplicados a mano sobre keyedmutex.go con -race (F8-04b):
//
//   - quitar el `rl.ref++` → muere TestKeyedMutex_EntryRemovedWhenLastHolderLeaves (el
//     conteo baja a -1 y la entrada no se borra) y, por vigilante, los que esperan ref = 2.
//   - mover el `rl.ref++` detrás del `rl.mu.Lock()` → muere
//     TestKeyedMutex_EntryKeptWhileSomeoneWaits (el contendiente espera sin estar contado:
//     ref nunca llega a 2) y -race denuncia la escritura de ref fuera del global en
//     TestKeyedMutex_SameKeyNeverOverlapsUnderLoad.
//   - quitar el `delete` → muere TestKeyedMutex_EntryRemovedWhenLastHolderLeaves.
//   - `rl.ref == 0` por `rl.ref <= 1` → muere TestKeyedMutex_EntryKeptWhileSomeoneWaits
//     (el unlock del titular borra la entrada con el contendiente esperando).
//   - retener el global mientras se espera la clave (`rl.mu.Lock()` antes del
//     `k.mu.Unlock()`, añadido en el verde) → el binario entero se cuelga y lo mata
//     `-timeout` (la primera lectura de ref de un test ya no consigue el global): cuenta
//     como muerto, sin test que lo nombre.

// kmWatchdog es el tope de espera de un suceso obligado. No es una pausa: nadie duerme
// este tiempo, solo es cuánto se tarda en declarar muerto un test que iba a colgarse.
const kmWatchdog = 10 * time.Second

func kmKey(tenant, session, contact string) store.Key {
	return store.Key{TenantID: tenant, SessionID: session, ContactID: contact}
}

// kmEntry devuelve, leído bajo el candado global, el refLock de la clave (nil si no hay
// entrada), su conteo y el tamaño del mapa.
func kmEntry(km *keyedMutex, key store.Key) (rl *refLock, ref, entries int) {
	km.mu.Lock()
	defer km.mu.Unlock()
	rl = km.locks[key]
	if rl != nil {
		ref = rl.ref
	}
	return rl, ref, len(km.locks)
}

// kmAwaitRef cede el procesador hasta que el conteo de la clave vale want. No duerme.
func kmAwaitRef(t *testing.T, km *keyedMutex, key store.Key, want int) {
	t.Helper()
	deadline := time.After(kmWatchdog)
	for {
		_, ref, _ := kmEntry(km, key)
		if ref == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("el conteo de la clave no llegó a %d (vale %d)", want, ref)
		default:
			runtime.Gosched()
		}
	}
}

// kmAwait espera a que se cierre el canal de un suceso que tiene que ocurrir.
func kmAwait(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(kmWatchdog):
		t.Fatalf("no ocurrió: %s", what)
	}
}

func kmClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// kmContend lanza una goroutine que pide la clave, cierra entered al entrar, y suelta
// cuando se cierra release. done se cierra cuando ya soltó.
func kmContend(km *keyedMutex, key store.Key, release <-chan struct{}) (entered, done chan struct{}) {
	entered, done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		unlock := km.lock(key)
		close(entered)
		<-release
		unlock()
	}()
	return entered, done
}

// KM-1 · Con la clave tomada, quien la pide después NO entra hasta el unlock del titular,
// y entra después.
func TestKeyedMutex_SameKeyIsMutuallyExclusive(t *testing.T) {
	km := newKeyedMutex()
	key := kmKey("t-1", "s-1", "c-1")

	unlock := km.lock(key)
	release := make(chan struct{})
	close(release)
	entered, done := kmContend(km, key, release)

	// ref = 2: el contendiente ya está contado, es decir, ya pidió la clave.
	kmAwaitRef(t, km, key, 2)
	for range 100 {
		runtime.Gosched()
	}
	if kmClosed(entered) {
		t.Fatal("el contendiente entró con la clave todavía tomada por el titular")
	}

	unlock()
	kmAwait(t, entered, "el contendiente entra tras el unlock del titular")
	kmAwait(t, done, "el contendiente suelta")
}

// KM-1 · N goroutines sobre la misma clave tocan un contador SIN sincronizar dentro de la
// sección crítica: el total es N y -race no salta.
func TestKeyedMutex_SameKeyNeverOverlapsUnderLoad(t *testing.T) {
	const goroutines, rounds = 16, 200
	km := newKeyedMutex()
	key := kmKey("t-1", "s-1", "c-1")

	counter, inside, overlaps := 0, 0, 0
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range rounds {
				unlock := km.lock(key)
				inside++
				if inside != 1 {
					overlaps++
				}
				counter++
				inside--
				unlock()
			}
		})
	}
	wg.Wait()

	if counter != goroutines*rounds {
		t.Errorf("contador = %d, quería %d: se perdieron incrementos", counter, goroutines*rounds)
	}
	if overlaps != 0 {
		t.Errorf("%d veces hubo dos goroutines dentro de la sección crítica", overlaps)
	}
	if _, _, entries := kmEntry(km, key); entries != 0 {
		t.Errorf("tras la carga quedan %d entradas en el mapa, quería 0", entries)
	}
}

// KM-2 · Con una clave tomada, otra que difiere en UN solo campo de la terna se toma y se
// suelta sin esperar.
func TestKeyedMutex_DifferentKeysDoNotBlock(t *testing.T) {
	held := kmKey("t-1", "s-1", "c-1")
	cases := []struct {
		name  string
		other store.Key
	}{
		{"differs only in TenantID", kmKey("t-2", "s-1", "c-1")},
		{"differs only in SessionID", kmKey("t-1", "s-2", "c-1")},
		{"differs only in ContactID", kmKey("t-1", "s-1", "c-2")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			km := newKeyedMutex()
			unlock := km.lock(held)
			defer unlock()

			release := make(chan struct{})
			entered, done := kmContend(km, tc.other, release)
			kmAwait(t, entered, "la otra clave se toma con la primera todavía tomada")
			if _, _, entries := kmEntry(km, held); entries != 2 {
				t.Errorf("con dos claves tomadas hay %d entradas, quería 2", entries)
			}
			close(release)
			kmAwait(t, done, "la otra clave se suelta")
		})
	}
}

// KM-3 · Tras lock+unlock sin contendientes el mapa queda vacío; tras M claves tomadas a
// la vez y soltadas, también.
func TestKeyedMutex_EntryRemovedWhenLastHolderLeaves(t *testing.T) {
	km := newKeyedMutex()
	key := kmKey("t-1", "s-1", "c-1")

	unlock := km.lock(key)
	if rl, ref, entries := kmEntry(km, key); rl == nil || ref != 1 || entries != 1 {
		t.Fatalf("con la clave tomada: entrada=%v ref=%d entradas=%d, quería entrada con ref 1 y 1 entrada",
			rl != nil, ref, entries)
	}
	unlock()
	if rl, _, entries := kmEntry(km, key); rl != nil || entries != 0 {
		t.Fatalf("tras lock+unlock quedan %d entradas (la de la clave: %v), quería 0", entries, rl != nil)
	}

	const many = 50
	unlocks := make([]func(), 0, many)
	for i := range many {
		unlocks = append(unlocks, km.lock(kmKey("t-1", "s-1", "c-"+strconv.Itoa(i))))
	}
	if _, _, entries := kmEntry(km, key); entries != many {
		t.Fatalf("con %d claves tomadas hay %d entradas", many, entries)
	}
	for _, u := range unlocks {
		u()
	}
	if _, _, entries := kmEntry(km, key); entries != 0 {
		t.Fatalf("tras soltar %d claves quedan %d entradas, quería 0", many, entries)
	}
}

// KM-4 · Con un contendiente esperando (ref = 2), el unlock del titular deja la entrada
// (ref = 1) y el contendiente entra con el MISMO refLock; su unlock la borra.
func TestKeyedMutex_EntryKeptWhileSomeoneWaits(t *testing.T) {
	km := newKeyedMutex()
	key := kmKey("t-1", "s-1", "c-1")

	unlock := km.lock(key)
	original, _, _ := kmEntry(km, key)
	release := make(chan struct{})
	entered, done := kmContend(km, key, release)
	kmAwaitRef(t, km, key, 2)

	unlock()
	kmAwait(t, entered, "el contendiente entra tras el unlock del titular")
	// El contendiente entra en cuanto el titular suelta el candado de la clave, que es
	// ANTES de que el titular decremente (KM-6): se aguarda a que el conteo asiente.
	kmAwaitRef(t, km, key, 1)
	rl, ref, entries := kmEntry(km, key)
	if rl == nil {
		t.Fatal("el unlock del titular borró la entrada con el contendiente dentro")
	}
	if rl != original {
		t.Error("el contendiente entró con un refLock DISTINTO del que tenía el titular")
	}
	if ref != 1 || entries != 1 {
		t.Errorf("tras el unlock del titular: ref=%d entradas=%d, quería 1 y 1", ref, entries)
	}

	// Un tercero que llega ahora tiene que esperar al contendiente: comparte su candado.
	lateEntered, lateDone := kmContend(km, key, release)
	kmAwaitRef(t, km, key, 2)
	if kmClosed(lateEntered) {
		t.Fatal("un tercero entró con el contendiente todavía dentro")
	}

	close(release)
	kmAwait(t, done, "el contendiente suelta")
	kmAwait(t, lateDone, "el tercero entra y suelta")
	kmAwaitRef(t, km, key, 0)
	if rl, _, entries := kmEntry(km, key); rl != nil || entries != 0 {
		t.Errorf("tras el último unlock quedan %d entradas, quería 0", entries)
	}
}

// KM-5 · Con un contendiente bloqueado en la clave A, tomar y soltar la clave B termina:
// el candado global no se retiene durante la espera.
func TestKeyedMutex_GlobalLockNotHeldWhileWaiting(t *testing.T) {
	km := newKeyedMutex()
	keyA, keyB := kmKey("t-1", "s-1", "c-A"), kmKey("t-1", "s-1", "c-B")

	unlockA := km.lock(keyA)
	release := make(chan struct{})
	close(release)
	_, waiterDone := kmContend(km, keyA, release)
	kmAwaitRef(t, km, keyA, 2)

	otherDone := make(chan struct{})
	go func() {
		defer close(otherDone)
		km.lock(keyB)()
	}()
	kmAwait(t, otherDone, "lock+unlock de la clave B con un contendiente esperando en la A")

	unlockA()
	kmAwait(t, waiterDone, "el contendiente de la clave A entra y suelta")
}
