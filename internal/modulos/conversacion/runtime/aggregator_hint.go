// Porta internal/flujos/runtime/aggregator.go @ e0159171

package runtime

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Trozo de aggregator.go (E-13): el adelanto por intent. OnClassified aplica la política
// de disparo, anota la pista y despierta al barrido; el barrido se lleva las pistas.

// OnClassified recibe la clasificación que se pidió y aplica la política de disparo
// (T1.6-4). Es seguro sobre un receptor nil (no hace nada).
//
// # AG-3 · El intent solo ADELANTA
//
// La política mira el NOMBRE y la CONFIANZA y nada más (IntentHint): adelanta si y
// solo si intent == IntentIntakeRequest Y confidence >= umbral (0.7, o el de
// WithIntentConfidence). Por debajo del umbral, o con cualquier otro nombre, NO PASA
// NADA: ni pista, ni log, ni aviso. Adelantar es anotar una PISTA en memoria para esa
// Key: el cierre lo ejecuta el PRÓXIMO barrido, que la cierra sin esperar a ningún
// plazo. La pista no es estado: si el proceso muere con pistas dentro no se pierde
// ningún job (la ventana cierra por su reloj).
//
// No comprueba que la ventana siga viva (costaría un SELECT para no hacer nada). Por
// eso una respuesta TARDÍA, que llega cuando la ventana ya cerró, es INOCUA: su pista
// no casa con ninguna ventana viva y el barrido siguiente la tira sin tocar la fila
// cerrada. ⚠️ ACEPTADO: la pista es por Key, no por job, así que si entre tanto el
// cliente abrió OTRA ventana sobre el mismo evento, la pista tardía cierra esa ventana
// nueva antes de tiempo. No pierde mensajes ni duplica jobs.
//
// # AG-4 · Anotar bajo candado y DESPUÉS avisar
//
// Además de anotar, DESPIERTA al barrido de Run para que cierre YA y no en el
// siguiente tick (T1.8-1 (h): un reloj no sustituye a un evento que ya llegó). El
// orden no es intercambiable: primero se anota la pista, protegida, y DESPUÉS se
// avisa; quien recibe el aviso ve ya la pista. El aviso es NO BLOQUEANTE y de buffer
// 1: OnClassified vuelve enseguida aunque nadie esté escuchando (Run sin arrancar) y
// aunque se la llame mil veces seguidas; los avisos de más se descartan sin perder
// trabajo, porque el barrido que consume el aviso pendiente se lleva TODAS las pistas
// acumuladas.
func (s *IntakeAggregator) OnClassified(key intake.WindowKey, intent string, confidence float64) {
	if s == nil {
		return
	}
	// 🔴 LA POLÍTICA VIVE AQUÍ Y NO EN QUIEN CLASIFICA, y es deliberado: el pool sabe
	// hablar con un modelo, no sabe qué significa `intake_request` ni qué umbral tiene
	// esta plataforma. Repartir la decisión entre los dos es como acaban diciendo cosas
	// distintas.
	//
	// Por debajo del umbral NO PASA NADA: ni pista, ni log, ni aviso al dueño. Un
	// «desconocido» con confianza baja es el sistema funcionando —REQ-38 lo nombra
	// entre los motivos SANOS—, y contarlo como incidencia mataría el canal de avisos.
	if !s.intentTriggers(IntentHint{Name: intent, Confidence: confidence}) {
		return
	}
	s.hintDueNow(key)
}

// intentTriggers aplica D-044.20 ENTERA: mira `Name` y `Confidence` y NADA MÁS del
// intent. No lee `params`, no espera una lista de productos y no cambia de
// comportamiento según lo que el intent traiga dentro.
func (s *IntakeAggregator) intentTriggers(hint IntentHint) bool {
	return hint.Name == IntentIntakeRequest && hint.Confidence >= s.intentThreshold
}

// hintDueNow anota que esa ventana debe cerrarse, y DESPIERTA al barrido para que lo
// haga ya (T1.8-1 (h)). Antes solo anotaba, y el efecto esperaba al siguiente tick.
//
// 🔴 EL ORDEN DE LAS DOS MITADES NO ES INTERCAMBIABLE: primero se anota bajo candado y
// DESPUÉS se avisa. Al revés habría una carrera real —el barrido despertaría, llamaría
// a `takeHints` y encontraría el mapa todavía vacío, y la pista se quedaría esperando
// al tick igual que antes—. Con este orden, cualquiera que reciba el aviso ve ya la
// pista.
//
// El aviso va FUERA del candado y es NO BLOQUEANTE: quien llama a esto es
// `OnClassified`, o sea la goroutine del pool de clasificación al terminar una
// inferencia. No puede quedarse esperando a que el barrido esté escuchando, y tampoco
// puede quedarse esperando a `s.mu` mientras un barrido largo lo tiene tomado.
func (s *IntakeAggregator) hintDueNow(k intake.WindowKey) {
	s.mu.Lock()
	s.dueNow[k] = struct{}{}
	s.mu.Unlock()
	// El `default` es lo que hace que esto no bloquee NUNCA: si ya hay un aviso
	// pendiente, este se descarta y no pasa nada (ver el porqué en el campo `wake`).
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// takeHints se lleva las pistas acumuladas y deja el mapa vacío. Se llevan TODAS
// aunque el barrido no llegue a mirar la ventana de alguna (el `limit` recorta):
// esa ventana no se pierde, se cierra por su reloj en un tick posterior — que es
// justo lo que T1.7 dice que tiene que pasar cuando el adelanto no llega.
func (s *IntakeAggregator) takeHints() map[intake.WindowKey]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dueNow) == 0 {
		return nil
	}
	out := s.dueNow
	s.dueNow = make(map[intake.WindowKey]struct{})
	return out
}
