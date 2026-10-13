package runtime

import "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"

// export_test.go abre a los tests del paquete EXTERNO (runtime_test, los que montan el arnés)
// lo único del interior que no se puede observar por conducta: cuántos hay contados en el
// candado de una conversación. Solo existe en el binario de test.

// ConversationLockRefs devuelve, leído bajo el candado global del keyedMutex del Runtime, el
// conteo de la clave: el titular más los que esperan (KM-4 lo incrementa ANTES de esperar).
// 0 significa que nadie tiene ni pide la conversación. Es la manera de saber, sin dormir, que
// un segundo entrante «ya está esperando el candado».
func (rt *Runtime) ConversationLockRefs(key store.Key) int {
	rt.locks.mu.Lock()
	defer rt.locks.mu.Unlock()
	if rl := rt.locks.locks[key]; rl != nil {
		return rl.ref
	}
	return 0
}
