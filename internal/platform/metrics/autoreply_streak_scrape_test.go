package metrics

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests de la topología PELIGROSA del Plan 049 · Opción A: el barrido de rachas
// vencidas se materializa dentro de la fuente del GaugeFunc, que Prometheus invoca
// DURANTE el Gather, y ese barrido OBSERVA sobre el histograma hermano. Es decir:
// se escribe una métrica mientras se está colectando otra del MISMO registry.
//
// 🔴 POR QUÉ ESTE FICHERO EXISTE, Y POR QUÉ NO BASTABAN LOS TESTS DEL CONTADOR.
// El handoff del 049 dejó este punto como «razonado, no ejecutado», y con razón: si
// el razonamiento fuera falso, el fallo NO sería un test rojo del contador —esos
// pasan todos con un onClose de mentira, que no toca Prometheus— sino un DEADLOCK
// en /metrics, que no aparece en ninguna suite y sí en producción semanas después.
// Lo que se prueba aquí no es el contador: es que la topología aguanta el scrape.
//
// Por eso el onClose de estos tests es el hook REAL (m.FlowAutoreplyStreak) y el
// scrape es el handler REAL (m.PromHandler), no un doble de ninguno de los dos. El
// contador de rachas no aparece: vive en otro paquete y su candado ya está suelto
// cuando corre onClose (streakCounter.report lo invoca fuera del mutex, a propósito
// y documentado ahí), así que el único candado que queda en juego —y el único que
// podría cerrarse sobre sí mismo— es el de Prometheus. Esa es justo la pieza que
// estos tests ejercitan.
//
// 🔴 EL SCRAPE DE RETRASO, que estos tests DESCUBRIERON y ahora fijan.
// No hay deadlock (medido, ver los tests de abajo), pero sí una consecuencia que el
// razonamiento no había previsto: prometheus.Registry.Gather() colecta cada colector
// EN PARALELO, así que el histograma puede serializar su estado sin esperar a que la
// fuente del gauge haya corrido. Cuando gana esa carrera, lo que el barrido observa
// durante el scrape N no sale en el cuerpo del scrape N: sale en el del N+1.
//
// Cuál de las dos goroutines llega antes NO está garantizado y cambia de ejecución a
// ejecución (se midió: con -race, que ensancha la ventana, el histograma gana a
// menudo; sin -race, casi siempre pierde). Así que la conducta que se puede afirmar
// —y la que se afirma abajo— es: la observación aparece en el scrape N o en el N+1,
// nunca se pierde y nunca se cuenta dos veces.
//
// No se pierde ningún dato y para una distribución que se acumula durante semanas el
// retraso da igual; lo que NO da igual es verificarlo en campo. Quien raspe UNA vez
// tras el vencimiento puede ver _count sin moverse y concluir que el cableado no
// funciona. 🔴 Hay que raspar DOS veces. Ese es el gate §4.3 del handoff del 049, y
// sin este matiz se falla por un motivo que no tiene nada que ver con el 049.

// scrapeConTimeout hace un scrape real y falla si no vuelve a tiempo.
//
// El timeout es el test: un deadlock no devuelve un error, se queda quieto. Sin
// esta envoltura el síntoma sería que `go test` se cuelga hasta el panic de los 10
// minutos, con un volcado de goroutines que hay que leerse entero para entender qué
// pasó. Con ella, el síntoma es una línea que dice exactamente qué se atascó.
func scrapeConTimeout(t *testing.T, m *Metrics, quien string) string {
	t.Helper()

	hecho := make(chan string, 1)
	go func() { hecho <- scrape(t, m) }()

	select {
	case cuerpo := <-hecho:
		return cuerpo
	case <-time.After(30 * time.Second):
		t.Fatalf("%s: el scrape de /metrics NO volvió en 30 s — DEADLOCK. "+
			"La fuente del gauge wapp_flow_autoreply_streak_max observa sobre el "+
			"histograma wapp_flow_autoreply_streak durante el Gather; si eso se "+
			"bloquea, /metrics queda colgado en producción y con él todo el scrape.", quien)
		return ""
	}
}

// TestAutoreplyStreak_ObservarDuranteElScrapeNoSeBloquea es el test central: la
// fuente del gauge escribe en el histograma hermano mientras Prometheus la colecta.
//
// Reproduce lo que hace el runtime en campo cuando un episodio abandonado vence por
// inactividad: nadie ha vuelto a escribir, así que el cierre solo puede ocurrir en el
// scrape, y al ocurrir manda la longitud de la racha al histograma.
func TestAutoreplyStreak_ObservarDuranteElScrapeNoSeBloquea(t *testing.T) {
	m := New()

	// Tres rachas vencidas que se barren en el PRIMER scrape, como haría el contador
	// real; después la fuente ya no barre nada, que es también lo que pasa en campo.
	const barridas = 3
	var unaVez sync.Once
	m.SetFlowAutoreplyStreakMaxSource(func() int {
		unaVez.Do(func() {
			for range barridas {
				m.FlowAutoreplyStreak(4)
			}
		})
		// Lo que devuelve la fuente es el máximo de las que SIGUEN vivas tras barrer.
		return 9
	})

	// Scrape 1: barre y observa. El gauge sale seguro; el histograma, según la carrera.
	primero := scrapeConTimeout(t, m, "scrape 1 (el que barre)")

	if !strings.Contains(primero, "wapp_flow_autoreply_streak_max 9") {
		t.Errorf("el gauge no publicó el máximo devuelto por la fuente:\n%s", extractoStreak(primero))
	}
	// En ESTE cuerpo el histograma puede traer ya las 3 o traer 0, y las dos cosas
	// son correctas: es una carrera legítima entre las goroutines de colecta, no un
	// fallo. Lo único inaceptable sería un valor intermedio —una observación a medio
	// registrar— o de más.
	if n, ok := serie(primero, "wapp_flow_autoreply_streak_count"); !ok || (n != 0 && n != barridas) {
		t.Errorf("_count = %d en el scrape que barre; solo 0 o %d son válidos "+
			"(el histograma se serializa en paralelo con la fuente del gauge):\n%s",
			n, barridas, extractoStreak(primero))
	}

	// Scrape 2: aquí sí. Esta es la aserción que prueba que el barrido llegó al
	// histograma de verdad — sin ella, un barrido que no observara nada sería
	// indistinguible de uno que funciona.
	segundo := scrapeConTimeout(t, m, "scrape 2 (el que publica)")

	if !contieneSerie(segundo, "wapp_flow_autoreply_streak_count", barridas) {
		t.Errorf("el histograma no recogió las %d rachas barridas durante el scrape anterior:\n%s",
			barridas, extractoStreak(segundo))
	}
	// Y en el bucket correcto: rachas de 4 caen en le="5", no en le="3".
	if !contieneSerie(segundo, `wapp_flow_autoreply_streak_bucket{le="3"}`, 0) ||
		!contieneSerie(segundo, `wapp_flow_autoreply_streak_bucket{le="5"}`, barridas) {
		t.Errorf("las rachas de 4 no cayeron en el bucket le=\"5\":\n%s", extractoStreak(segundo))
	}
}

// TestAutoreplyStreak_ScrapesConcurrentesNoSeBloquean cubre el caso que el de arriba
// deja fuera: dos colectas solapadas.
//
// En campo pasa sin hacer nada raro —un Prometheus que raspa mientras la petición
// anterior aún no ha terminado, o dos raspadores— y es el escenario en el que un
// candado mal puesto deja de ser teórico. Cada Gather lleva su propia escritura al
// histograma, así que compiten de verdad.
func TestAutoreplyStreak_ScrapesConcurrentesNoSeBloquean(t *testing.T) {
	m := New()

	m.SetFlowAutoreplyStreakMaxSource(func() int {
		m.FlowAutoreplyStreak(2)
		return 5
	})

	const scrapes = 12
	var wg sync.WaitGroup
	hecho := make(chan struct{})

	for range scrapes {
		wg.Go(func() { _ = scrape(t, m) })
	}
	go func() { wg.Wait(); close(hecho) }()

	select {
	case <-hecho:
	case <-time.After(60 * time.Second):
		t.Fatalf("%d scrapes concurrentes NO terminaron en 60 s — DEADLOCK bajo colecta solapada", scrapes)
	}

	// Y el recuento tiene que cuadrar: una observación por scrape, ni perdida ni
	// doble. Se comprueba en un scrape POSTERIOR a los 12 —por el desfase explicado
	// arriba— y se compara contra lo que la propia fuente dice haber observado, no
	// contra un número escrito a mano: el scrape de comprobación también pasa por la
	// fuente, y cuál de los dos valores acaba en el cuerpo depende de la carrera
	// entre las goroutines de colecta. Lo que NO puede pasar, y es lo que se afirma,
	// es que se pierda una observación o se cuente dos veces.
	cuerpo := scrapeConTimeout(t, m, "scrape de comprobación")
	publicadas, ok := serie(cuerpo, "wapp_flow_autoreply_streak_count")
	if !ok {
		t.Fatalf("no se publicó _count:\n%s", extractoStreak(cuerpo))
	}
	if publicadas != scrapes && publicadas != scrapes+1 {
		t.Errorf("_count = %d; se esperaban %d observaciones (una por scrape) o %d "+
			"(si el scrape de comprobación llegó a tiempo de incluir la suya):\n%s",
			publicadas, scrapes, scrapes+1, extractoStreak(cuerpo))
	}
}

// serie devuelve el valor de una serie del cuerpo del scrape.
func serie(cuerpo, nombre string) (int, bool) {
	for l := range strings.SplitSeq(cuerpo, "\n") {
		resto, hay := strings.CutPrefix(l, nombre+" ")
		if !hay {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(resto))
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// contieneSerie comprueba que una serie existe y vale exactamente lo esperado.
// Se prefiere a un strings.Contains sobre "nombre N" porque ese casaría también con
// un prefijo de otra serie (_count 1 contra _count 10) y daría un verde falso.
func contieneSerie(cuerpo, nombre string, quiero int) bool {
	v, ok := serie(cuerpo, nombre)
	return ok && v == quiero
}

// extractoStreak recorta el cuerpo del scrape a las líneas de la familia, para que
// un fallo no vomite el /metrics entero (que trae los Help largos del 049).
func extractoStreak(cuerpo string) string {
	var b strings.Builder
	for l := range strings.SplitSeq(cuerpo, "\n") {
		if strings.HasPrefix(l, "wapp_flow_autoreply_streak") {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
