//go:build pendiente

package runtime

// keyedmutex.go no tiene exportados y en el rojo no declara nada (los no exportados
// romperían el lint `unused`), así que aquí no hay test que compile contra un símbolo
// existente. Los tests nacen en el verde (F8-04b), uno por promesa del comentario de
// keyedmutex.go, sin time.Sleep (la espera se observa con canales) y con -race:
//
//   - KM-1 · TestKeyedMutex_SameKeyIsMutuallyExclusive: con la clave tomada, una segunda
//     goroutine que la pide NO entra hasta el unlock de la primera (se observa con un
//     canal que solo se cierra al entrar), y entra después.
//   - KM-1 · TestKeyedMutex_SameKeyNeverOverlapsUnderLoad: N goroutines sobre la misma
//     clave incrementan un contador SIN sincronizar dentro de la sección crítica; el total
//     es N y -race no salta.
//   - KM-2 · TestKeyedMutex_DifferentKeysDoNotBlock: con la clave A tomada, lock(B)
//     vuelve sin esperar; tabla con una clave que difiere solo en TenantID, solo en
//     SessionID y solo en ContactID.
//   - KM-3 · TestKeyedMutex_EntryRemovedWhenLastHolderLeaves: tras lock+unlock, el mapa
//     interno tiene 0 entradas; tras M claves distintas tomadas y soltadas, también.
//   - KM-4 · TestKeyedMutex_EntryKeptWhileSomeoneWaits: titular + un contendiente
//     esperando (ref = 2); el unlock del titular deja la entrada (ref = 1) y el
//     contendiente entra con el MISMO refLock; su unlock la borra.
//   - KM-5 · TestKeyedMutex_GlobalLockNotHeldWhileWaiting: con un contendiente bloqueado
//     en la clave A, lock(B) y su unlock terminan (si el global se retuviera, no).
//   - Mutantes (nivel complejo): quitar el `ref++`, mover el `ref++` detrás del
//     `rl.mu.Lock()`, quitar el `delete`, cambiar `ref == 0` por `ref <= 1`.
