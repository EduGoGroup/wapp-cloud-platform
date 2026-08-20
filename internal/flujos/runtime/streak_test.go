// Tests del contador de rachas (Plan 049 · Opción A). Van en el paquete INTERNO
// —el resto del paquete testea desde runtime_test— porque streakCounter no está
// exportado: es una pieza de observación del motor, no contrato hacia fuera.
//
// Todos los tests inyectan un `now` EXPLÍCITO. Ninguno llama a time.Now(): un
// contador cuyo comportamiento depende del reloj de pared solo se puede testear si el
// reloj es un parámetro, y sin eso el caso del vencimiento por inactividad (media
// hora) sería intestable o intermitente.

package runtime

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

const t49Tenant = "t49-tenant"

// t49T0 es el instante base de todos los tests. Fijo y en UTC para que el
// desplazamiento de cada Inc sea legible en el propio test.
var t49T0 = time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

func t49Key(session, contact string) store.Key {
	return store.Key{TenantID: t49Tenant, SessionID: session, ContactID: contact}
}

// t49Observador recoge las longitudes que el contador reporta por onClose. El hook se
// invoca fuera del mutex del contador y puede llegar desde varias goroutines, así que
// el doble se sincroniza (el test de concurrencia corre con -race).
type t49Observador struct {
	mu     sync.Mutex
	vistas []int
}

func (o *t49Observador) registrar(racha int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.vistas = append(o.vistas, racha)
}

// observadas devuelve una COPIA de lo recogido: el test no debe poder mutar el estado
// del doble ni leerlo sin candado.
func (o *t49Observador) observadas() []int {
	o.mu.Lock()
	defer o.mu.Unlock()
	copia := make([]int, len(o.vistas))
	copy(copia, o.vistas)
	return copia
}

func (o *t49Observador) total() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.vistas)
}

// t49Contador arma un contador con el observador ya cableado y valores generosos de
// TTL y tope, para que los tests que no van de eso no los rocen sin querer.
func t49Contador() (*streakCounter, *t49Observador) {
	obs := &t49Observador{}
	return newStreakCounter(streakIdleTTL, streakMaxEntries, obs.registrar), obs
}

// t49MismoMultiset compara sin depender del orden: la evicción recorre un mapa de Go y
// el orden de recorrido es deliberadamente aleatorio.
func t49MismoMultiset(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	ca, cb := slices.Clone(a), slices.Clone(b)
	slices.Sort(ca)
	slices.Sort(cb)
	return slices.Equal(ca, cb)
}

// Lo básico: dentro de una misma conversación las auto-respuestas se ACUMULAN. Que el
// entrante del contacto haya llegado en medio de cada una no reinicia nada — es la
// decisión de diseño del contador, y este test la fija.
func TestRacha_IncAcumulaEnLaMismaConversacion(t *testing.T) {
	c, obs := t49Contador()
	k := t49Key("sess-1", "contacto-1")

	for i, quiero := range []int{1, 2, 3} {
		got := c.Inc(k, t49T0.Add(time.Duration(i)*time.Second))
		if got != quiero {
			t.Fatalf("Inc #%d debe devolver la racha viva %d, devolvió %d", i+1, quiero, got)
		}
	}
	if obs.total() != 0 {
		t.Fatalf("una racha que sigue viva no debe reportarse todavía, se reportaron %v", obs.observadas())
	}
}

// La clave es (tenant, sesión, contacto) entera: dos contactos de la misma sesión —o el
// mismo contacto en dos sesiones— son conversaciones DISTINTAS y no comparten racha. Si
// se mezclaran, la métrica sumaría el tráfico de un número muy hablado y publicaría
// rachas que ninguna conversación tuvo.
func TestRacha_ConversacionesDistintasNoSeMezclan(t *testing.T) {
	c, _ := t49Contador()

	// Difieren solo en ContactID.
	a := t49Key("sess-1", "contacto-A")
	b := t49Key("sess-1", "contacto-B")
	c.Inc(a, t49T0)
	c.Inc(a, t49T0.Add(time.Second))
	if got := c.Inc(b, t49T0.Add(2*time.Second)); got != 1 {
		t.Fatalf("otro contacto en la misma sesión arranca su propia racha en 1, devolvió %d", got)
	}
	if got := c.Inc(a, t49T0.Add(3*time.Second)); got != 3 {
		t.Fatalf("la racha del contacto A debe seguir en 3, devolvió %d", got)
	}

	// Difieren solo en SessionID.
	x := t49Key("sess-X", "contacto-Z")
	y := t49Key("sess-Y", "contacto-Z")
	c.Inc(x, t49T0.Add(4*time.Second))
	c.Inc(x, t49T0.Add(5*time.Second))
	c.Inc(x, t49T0.Add(6*time.Second))
	if got := c.Inc(y, t49T0.Add(7*time.Second)); got != 1 {
		t.Fatalf("el mismo contacto en otra sesión arranca su propia racha en 1, devolvió %d", got)
	}
	if got := c.Inc(x, t49T0.Add(8*time.Second)); got != 4 {
		t.Fatalf("la racha de la sesión X debe seguir en 4, devolvió %d", got)
	}
}

// Close es el cierre del EPISODIO: reporta la racha completa una sola vez y deja la
// conversación limpia para el siguiente episodio.
func TestRacha_CloseReportaYReinicia(t *testing.T) {
	c, obs := t49Contador()
	k := t49Key("sess-1", "contacto-1")

	for i := 0; i < 5; i++ {
		c.Inc(k, t49T0.Add(time.Duration(i)*time.Second))
	}
	c.Close(k, t49T0.Add(5*time.Second))

	if got := obs.observadas(); !slices.Equal(got, []int{5}) {
		t.Fatalf("al cerrar debe observarse exactamente la racha [5], se observó %v", got)
	}
	if got := c.Inc(k, t49T0.Add(6*time.Second)); got != 1 {
		t.Fatalf("tras cerrar, la conversación empieza un episodio nuevo en 1, devolvió %d", got)
	}
	if got := obs.total(); got != 1 {
		t.Fatalf("el Inc posterior no cierra nada: sigue habiendo 1 observación, hay %d", got)
	}
}

// Cerrar una conversación que nunca auto-respondió NO es una racha de 0: es que no hubo
// racha. Publicarla contaminaría el histograma con un pico en el cero.
func TestRacha_CloseSinRachaNoReporta(t *testing.T) {
	c, obs := t49Contador()

	c.Close(t49Key("sess-1", "jamas-visto"), t49T0)

	if got := obs.total(); got != 0 {
		t.Fatalf("cerrar una conversación nunca vista no debe observar nada, observó %v", obs.observadas())
	}
	if got := c.Max(t49T0); got != 0 {
		t.Fatalf("un Close no debe crear entradas: Max esperado 0, devolvió %d", got)
	}
}

// Close cuelga de varios caminos de cierre (flujo terminado, escape, TTL) y puede
// llegar dos veces sobre la misma conversación. La segunda no debe duplicar la
// observación: sería una racha inventada en el histograma.
func TestRacha_CloseEsIdempotente(t *testing.T) {
	c, obs := t49Contador()
	k := t49Key("sess-1", "contacto-1")

	c.Inc(k, t49T0)
	c.Inc(k, t49T0.Add(time.Second))
	c.Close(k, t49T0.Add(2*time.Second))
	c.Close(k, t49T0.Add(3*time.Second))

	if got := obs.observadas(); !slices.Equal(got, []int{2}) {
		t.Fatalf("dos Close seguidos deben dejar UNA sola observación [2], dejaron %v", got)
	}
}

// El episodio también termina por INACTIVIDAD, y nadie llama a Close cuando no pasa
// nada: el cierre se detecta perezosamente en el siguiente Inc. Sin esto, una
// conversación retomada horas después sumaría sobre la racha de antes.
func TestRacha_InactividadCierraElEpisodio(t *testing.T) {
	c, obs := t49Contador()
	k := t49Key("sess-1", "contacto-1")

	for i := 0; i < 4; i++ {
		c.Inc(k, t49T0.Add(time.Duration(i)*time.Second))
	}

	tarde := t49T0.Add(streakIdleTTL + time.Minute)
	if got := c.Inc(k, tarde); got != 1 {
		t.Fatalf("tras el idleTTL empieza un episodio nuevo en 1, devolvió %d", got)
	}
	if got := obs.observadas(); !slices.Equal(got, []int{4}) {
		t.Fatalf("la racha vencida por inactividad debe observarse como [4], se observó %v", got)
	}
	if got := c.Inc(k, tarde.Add(time.Second)); got != 2 {
		t.Fatalf("el episodio nuevo sigue acumulando: esperado 2, devolvió %d", got)
	}
}

// 🔴 EL TEST QUE EL PLAN PIDE EXPLÍCITAMENTE (§5, «racha larga legítima no falsea el
// dato»). Simula el catálogo paginado: alguien real tocando «siguiente» cada 20
// segundos hasta acumular 30 auto-respuestas —un pedido de varias líneas sobre un
// catálogo mediano, con sus avances de página, variantes, cantidad, confirmación y
// datos del comprador—. Todas son legítimas y todas van precedidas de un entrante suyo.
//
// Este escenario es exactamente el que distingue esta métrica de una que contara
// mensajes: si el entrante del contacto reiniciara la cuenta, aquí saldrían 30 rachas
// de 1 y la métrica no sabría que existe un recorrido de 30. Y sin saberlo, cualquier
// umbral que se fijara después (Opción B) cortaría a este cliente a mitad del pedido —
// que es precisamente el modo de fallo que el §6 no quiere estrenar a ciegas. La métrica
// tiene que poder VER el 30 para que alguien pueda decidir con datos.
func TestRacha_RachaLargaLegitimaNoSeFalsea(t *testing.T) {
	c, obs := t49Contador()
	k := t49Key("sess-catalogo", "comprador-real")

	const pasos = 30
	const cadencia = 20 * time.Second // una persona leyendo y tocando «siguiente».

	for i := 0; i < pasos; i++ {
		got := c.Inc(k, t49T0.Add(time.Duration(i)*cadencia))
		if got != i+1 {
			t.Fatalf("paso %d del catálogo: la racha debe ir por %d, va por %d", i+1, i+1, got)
		}
	}

	// Durante TODO el recorrido no se ha cerrado ni un episodio: 30 avances a 20 s son
	// 9 min 40 s, muy por debajo del idleTTL de 30 min. Un recorrido legítimo no debe
	// fragmentarse en trozos.
	if got := obs.total(); got != 0 {
		t.Fatalf("el recorrido legítimo no debe cerrar ningún episodio por el camino, cerró %v", obs.observadas())
	}
	// Max en el instante del último paso: nada ha vencido, así que no barre nada.
	if got := c.Max(t49T0.Add((pasos - 1) * cadencia)); got != pasos {
		t.Fatalf("la racha viva más larga debe ser %d, es %d", pasos, got)
	}

	// Y al terminar el pedido, el episodio se observa ENTERO: un único 30.
	c.Close(k, t49T0.Add(pasos*cadencia))
	if got := obs.observadas(); !slices.Equal(got, []int{pasos}) {
		t.Fatalf("al cerrar el pedido debe observarse [%d], se observó %v", pasos, got)
	}
}

// Max es la fuente del gauge: la racha viva más larga de todo el proceso, no la suma ni
// la última. Y baja cuando esa conversación se cierra.
func TestRacha_MaxDevuelveLaMayorViva(t *testing.T) {
	c, _ := t49Contador()

	corta := t49Key("sess-1", "corta")
	larga := t49Key("sess-1", "larga")
	media := t49Key("sess-2", "media")

	for i := 0; i < 2; i++ {
		c.Inc(corta, t49T0.Add(time.Duration(i)*time.Second))
	}
	for i := 0; i < 7; i++ {
		c.Inc(larga, t49T0.Add(time.Duration(i)*time.Second))
	}
	for i := 0; i < 4; i++ {
		c.Inc(media, t49T0.Add(time.Duration(i)*time.Second))
	}

	// Todas dentro del idleTTL: aquí Max solo mide, no barre (eso lo cubre
	// TestRacha_MaxBarreYObservaLasVencidas).
	if got := c.Max(t49T0.Add(10 * time.Second)); got != 7 {
		t.Fatalf("Max debe ser la mayor racha viva (7), devolvió %d", got)
	}

	c.Close(larga, t49T0.Add(10*time.Second))
	if got := c.Max(t49T0.Add(10 * time.Second)); got != 4 {
		t.Fatalf("cerrada la mayor, Max debe bajar a la siguiente (4), devolvió %d", got)
	}

	c.Close(media, t49T0.Add(11*time.Second))
	c.Close(corta, t49T0.Add(12*time.Second))
	if got := c.Max(t49T0.Add(12 * time.Second)); got != 0 {
		t.Fatalf("sin conversaciones vivas Max debe ser 0, devolvió %d", got)
	}
}

// 🔴 EL TEST DEL BARRIDO EN EL SCRAPE (arreglo de los bloqueantes 1+2 del review).
//
// Max no es un getter puro: barre las rachas vencidas por inactividad, las reporta al
// histograma y devuelve el máximo de las que SIGUEN VIVAS. Es el único sitio donde se
// materializa el cierre por inactividad de una conversación que se abandona y no
// vuelve — el otro camino (el Inc siguiente sobre la misma clave) exige justamente que
// vuelva. Sin esto pasaban las dos cosas a la vez: el histograma solo recibía episodios
// de conversaciones vivas (muestra sesgada para el p99 del §9) y el gauge se quedaba
// clavado en rachas fosilizadas.
func TestRacha_MaxBarreYObservaLasVencidas(t *testing.T) {
	c, obs := t49Contador()

	// La abandonada: un catálogo de 3 que el cliente dejó a medias y nunca cerró.
	abandonada := t49Key("sess-1", "se-fue")
	for i := 0; i < 3; i++ {
		c.Inc(abandonada, t49T0.Add(time.Duration(i)*time.Second))
	}
	// La viva: sigue conversando media hora después.
	ahora := t49T0.Add(streakIdleTTL + time.Minute)
	viva := t49Key("sess-1", "sigue-aqui")
	c.Inc(viva, ahora.Add(-time.Second))
	c.Inc(viva, ahora)

	// El scrape: barre la vencida, la observa, y mide solo sobre la superviviente.
	got := c.Max(ahora)
	if got != 2 {
		t.Fatalf("Max debe devolver la racha de la conversación VIVA (2), devolvió %d "+
			"(si devuelve 3, está midiendo un fósil: la abandonada ya no existe)", got)
	}
	if vistas := obs.observadas(); !slices.Equal(vistas, []int{3}) {
		t.Fatalf("la racha abandonada debe llegar al histograma como [3], llegó %v "+
			"(si está vacío, el episodio abandonado NUNCA se observa y la muestra del p99 "+
			"solo tiene conversaciones vivas)", vistas)
	}
	if _, sigue := c.rachas[abandonada]; sigue {
		t.Fatalf("la entrada vencida debe desaparecer del mapa: si no, el gauge la sigue viendo")
	}
	if len(c.rachas) != 1 {
		t.Fatalf("tras el barrido solo debe quedar la conversación viva, quedan %d", len(c.rachas))
	}
}

// El barrido BORRA lo que reporta, así que dos scrapes seguidos no pueden duplicar la
// misma racha en el histograma. Es el modo de fallo natural de meter efectos en un
// getter que Prometheus llama cada 15-60 s: si Max reportara sin borrar, una sola
// conversación abandonada inyectaría su longitud en CADA scrape y el histograma se
// llenaría de copias de las rachas muertas hasta que alguien reiniciara el proceso.
func TestRacha_MaxNoObservaDosVecesLaMismaRacha(t *testing.T) {
	c, obs := t49Contador()
	k := t49Key("sess-1", "se-fue")

	for i := 0; i < 4; i++ {
		c.Inc(k, t49T0.Add(time.Duration(i)*time.Second))
	}

	ahora := t49T0.Add(streakIdleTTL + time.Minute)
	if got := c.Max(ahora); got != 0 {
		t.Fatalf("barrida la única racha, no queda ninguna viva: Max esperado 0, devolvió %d", got)
	}
	if vistas := obs.observadas(); !slices.Equal(vistas, []int{4}) {
		t.Fatalf("el primer scrape debe observar [4], observó %v", vistas)
	}

	// Segundo scrape, más tarde todavía: no queda nada que cerrar.
	if got := c.Max(ahora.Add(time.Hour)); got != 0 {
		t.Fatalf("Max esperado 0 en el segundo scrape, devolvió %d", got)
	}
	if vistas := obs.observadas(); !slices.Equal(vistas, []int{4}) {
		t.Fatalf("el segundo scrape NO debe volver a observar nada: sigue esperándose [4], hay %v", vistas)
	}
}

// El contrato del repo es «Observa; NUNCA decide»: un consumidor que no cablee el
// contador no puede caerse por ello. Mismo trato que el hook onReactiveBlocked.
func TestRacha_NilNoRompe(t *testing.T) {
	var c *streakCounter
	k := t49Key("sess-1", "contacto-1")

	if got := c.Inc(k, t49T0); got != 0 {
		t.Fatalf("Inc sobre un contador nil debe devolver 0, devolvió %d", got)
	}
	c.Close(k, t49T0)
	if got := c.Max(t49T0); got != 0 {
		t.Fatalf("Max sobre un contador nil debe devolver 0, devolvió %d", got)
	}
}

// Contador construido SIN hook: cuenta igual, simplemente no publica. Cubre el arranque
// de cualquier consumidor que no tenga métricas cableadas.
func TestRacha_SinOnCloseNoRompe(t *testing.T) {
	c := newStreakCounter(streakIdleTTL, streakMaxEntries, nil)
	k := t49Key("sess-1", "contacto-1")

	for i := 0; i < 3; i++ {
		if got := c.Inc(k, t49T0.Add(time.Duration(i)*time.Second)); got != i+1 {
			t.Fatalf("sin hook la cuenta debe ser la misma: esperado %d, devolvió %d", i+1, got)
		}
	}
	c.Close(k, t49T0.Add(3*time.Second))
	if got := c.Max(t49T0.Add(3 * time.Second)); got != 0 {
		t.Fatalf("sin hook el cierre debe borrar la entrada igual: Max esperado 0, devolvió %d", got)
	}
}

// Una racha DESALOJADA por el tope es una racha OBSERVADA, no una racha perdida:
// tirarla en silencio sesgaría el histograma justo bajo carga alta, que es cuando más
// interesa mirarlo. Se cubren los dos caminos del tope: la barrida de vencidas y, si
// esa no libera nada, el desalojo de la más antigua.
func TestRacha_EvictionReportaLasDesalojadas(t *testing.T) {
	t.Run("barrida_de_vencidas", func(t *testing.T) {
		obs := &t49Observador{}
		c := newStreakCounter(streakIdleTTL, 4, obs.registrar)

		for _, contacto := range []string{"a", "b", "c", "d"} {
			c.Inc(t49Key("sess-1", contacto), t49T0)
		}

		// El quinto MISS llega con el mapa lleno y las cuatro vencidas por inactividad.
		tarde := t49T0.Add(streakIdleTTL + time.Minute)
		if got := c.Inc(t49Key("sess-1", "e"), tarde); got != 1 {
			t.Fatalf("la conversación nueva arranca en 1, devolvió %d", got)
		}
		if got := obs.observadas(); !t49MismoMultiset(got, []int{1, 1, 1, 1}) {
			t.Fatalf("las cuatro rachas desalojadas deben observarse, se observó %v", got)
		}
		if len(c.rachas) != 1 {
			t.Fatalf("tras la barrida solo debe quedar la conversación nueva, quedan %d", len(c.rachas))
		}
	})

	t.Run("desalojo_de_la_mas_antigua", func(t *testing.T) {
		obs := &t49Observador{}
		c := newStreakCounter(streakIdleTTL, 4, obs.registrar)

		// La más antigua por lastSeen lleva una racha de 3: si se perdiera, se perdería
		// justo el dato que la métrica existe para ver.
		vieja := t49Key("sess-1", "vieja")
		for i := 0; i < 3; i++ {
			c.Inc(vieja, t49T0.Add(time.Duration(i)*100*time.Millisecond))
		}
		for i, contacto := range []string{"b", "c", "d"} {
			c.Inc(t49Key("sess-1", contacto), t49T0.Add(time.Duration(i+1)*time.Second))
		}

		// Mapa lleno (4) y NINGUNA vencida: la barrida no libera nada y entra el
		// desalojo de la más antigua.
		if got := c.Inc(t49Key("sess-1", "e"), t49T0.Add(4*time.Second)); got != 1 {
			t.Fatalf("la conversación nueva arranca en 1, devolvió %d", got)
		}
		if got := obs.observadas(); !slices.Equal(got, []int{3}) {
			t.Fatalf("debe observarse la racha de la desalojada [3], se observó %v", got)
		}
		if _, sigue := c.rachas[vieja]; sigue {
			t.Fatalf("la conversación más antigua debe haber sido desalojada")
		}
		if len(c.rachas) != 4 {
			t.Fatalf("el mapa NUNCA debe pasar del tope (4), tiene %d", len(c.rachas))
		}
	})
}

// HandleIncoming corre una goroutine por entrante: dos conversaciones distintas tocan el
// mismo mapa a la vez y la misma conversación puede tener varios entrantes en vuelo.
// Este test está escrito para `go test -race`: si el contador o el reporte del hook
// tuvieran una carrera, saltaría aquí.
func TestRacha_ConcurrenciaCuentaBien(t *testing.T) {
	obs := &t49Observador{}
	c := newStreakCounter(streakIdleTTL, streakMaxEntries, obs.registrar)

	const goroutines = 50
	const porGoroutine = 20

	compartida := t49Key("sess-compartida", "contacto-compartido")

	var wg sync.WaitGroup
	// Todas contra la MISMA clave: el total debe ser exacto, sin incrementos perdidos.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < porGoroutine; i++ {
				c.Inc(compartida, t49T0)
			}
		}()
	}
	// Y en paralelo, una clave propia por goroutine: claves distintas no se estorban.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			propia := t49Key("sess-propia", "contacto-"+string(rune('A'+n%26))+string(rune('a'+n/26)))
			for i := 0; i < porGoroutine; i++ {
				c.Inc(propia, t49T0)
			}
		}(g)
	}
	// Y un «lector» concurrente, que es como lo verá el scrape de Prometheus. Entre
	// comillas porque Max ya no solo lee: BARRE el mapa y borra entradas bajo el mismo
	// candado (ver su docstring), así que aquí hay un escritor más compitiendo con los
	// 100 Inc en vuelo. Con `now` = t49T0 —el mismo instante que todos los Inc— nada
	// está vencido y no borra nada, pero el recorrido con delete potencial sí se
	// ejercita: si hubiera una carrera, -race la vería aquí.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = c.Max(t49T0)
		}
	}()
	wg.Wait()

	if obs.total() != 0 {
		t.Fatalf("ninguna racha se cerró durante la carga (mismo `now`, sin Close), se observó %v", obs.observadas())
	}

	esperado := goroutines * porGoroutine
	c.mu.Lock()
	e, ok := c.rachas[compartida]
	var vivo int
	if ok {
		vivo = e.n
	}
	c.mu.Unlock()
	if !ok || vivo != esperado {
		t.Fatalf("la clave compartida debe acumular exactamente %d, acumuló %d (presente=%v)", esperado, vivo, ok)
	}
	if got := c.Max(t49T0); got != esperado {
		t.Fatalf("Max debe ser la racha compartida (%d), devolvió %d", esperado, got)
	}

	// Las claves propias: 50 conversaciones distintas + la compartida.
	c.mu.Lock()
	total := len(c.rachas)
	c.mu.Unlock()
	if total != goroutines+1 {
		t.Fatalf("deben quedar %d conversaciones vivas (50 propias + 1 compartida), quedan %d", goroutines+1, total)
	}

	// El cierre concurrente también es idempotente: N goroutines cerrando la misma
	// conversación producen UNA sola observación.
	var wg2 sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			c.Close(compartida, t49T0)
		}()
	}
	wg2.Wait()

	if got := obs.observadas(); !slices.Equal(got, []int{esperado}) {
		t.Fatalf("el cierre concurrente debe observar UNA sola racha [%d], observó %v", esperado, got)
	}
}
