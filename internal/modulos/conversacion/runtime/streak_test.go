package runtime

import (
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Tests de streak.go, uno por promesa de su comentario-contrato (ST-n). Van en el paquete
// INTERNO porque el fichero no exporta nada. Todos con un `now` EXPLÍCITO (ninguno lee el
// reloj ni duerme: ST-1), un observador de onClose sincronizado y comparación de
// longitudes como multiconjunto (el recorrido de un mapa es aleatorio). Los del tope y los
// de Max están en streak_sweep_test.go (E-13).
//
// Mutantes (nivel complejo), aplicados a mano sobre streak.go con -race (F8-04b):
//
//   - `Before(cutoff)` por `!After(cutoff)` en Inc → muere TestStreak_IdleLimitIsStrict/inc.
//   - lo mismo en Max → muere TestStreak_IdleLimitIsStrict/max.
//   - lo mismo en evictLocked (añadido en el verde) → muere
//     TestStreak_IdleLimitIsStrict/full_map.
//   - quitar el `delete` de Max → muere TestStreak_MaxNeverReportsTheSameStreakTwice (y
//     TestStreak_MaxSweepsAndReportsExpired).
//   - reportar dentro del candado, en Inc, en Close y en Max (tres mutantes) → muere
//     TestStreak_OnCloseRunsOutsideTheLock, en las filas del método mutado.
//   - quitar el reporte del desalojo → muere TestStreak_FullMapEvictsOldestAndReportsIt.
//   - `e.n = 1` por `e.n++` en la vencida → muere TestStreak_IdleEpisodeIsClosedByNextInc.
//   - quitar la normalización de idleTTL → muere
//     TestStreak_NonPositiveLimitsFallBackToDefaults (la emisión a 29 min no acumula).
//   - quitar la normalización de maxEntries → muere el mismo test (desaloja antes de
//     10.000).
//   - desalojar la más RECIENTE (`oldest.lastSeen.Before(e.lastSeen)`, añadido en el
//     verde) → muere TestStreak_FullMapEvictsOldestAndReportsIt.
//   - `e.n > longest` por `e.n >= longest` en Max → EQUIVALENTE, sobrevive y debe
//     sobrevivir: con e.n == longest la asignación deja el mismo valor.

// stT0 es el instante base de todos los tests: fijo, para que cada desplazamiento se lea
// en el propio test.
var stT0 = time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

func stKey(tenant, session, contact string) store.Key {
	return store.Key{TenantID: tenant, SessionID: session, ContactID: contact}
}

// stContact es una clave que solo varía en el contacto.
func stContact(contact string) store.Key { return stKey("t-1", "s-1", contact) }

// stObserver recoge las longitudes que el contador reporta por onClose. El hook llega
// fuera del mutex del contador y desde varias goroutines, así que se sincroniza.
type stObserver struct {
	mu   sync.Mutex
	seen []int
}

func (o *stObserver) record(streak int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, streak)
}

// lengths devuelve una copia ORDENADA de lo recogido (multiconjunto).
func (o *stObserver) lengths() []int {
	o.mu.Lock()
	defer o.mu.Unlock()
	sorted := slices.Clone(o.seen)
	slices.Sort(sorted)
	return sorted
}

// stCounter arma un contador con el observador cableado y los límites que se le pidan.
func stCounter(idleTTL time.Duration, maxEntries int) (*streakCounter, *stObserver) {
	obs := &stObserver{}
	return newStreakCounter(idleTTL, maxEntries, obs.record), obs
}

// stIncN emite n auto-respuestas seguidas sobre la clave, todas en `at`, y devuelve la
// racha viva tras la última.
func stIncN(c *streakCounter, key store.Key, n int, at time.Time) int {
	alive := 0
	for range n {
		alive = c.Inc(key, at)
	}
	return alive
}

// stWantSeen falla si lo observado no es, como multiconjunto, want.
func stWantSeen(t *testing.T, obs *stObserver, want ...int) {
	t.Helper()
	slices.Sort(want)
	if got := obs.lengths(); !slices.Equal(got, want) {
		t.Fatalf("rachas reportadas = %v, quería %v", got, want)
	}
}

// ST-4 · La clave es la terna entera: basta que difiera un campo para no compartir racha.
func TestStreak_DifferentConversationsDoNotMix(t *testing.T) {
	base := stKey("t-1", "s-1", "c-1")
	cases := []struct {
		name  string
		other store.Key
	}{
		{"differs only in tenant", stKey("t-2", "s-1", "c-1")},
		{"differs only in session", stKey("t-1", "s-2", "c-1")},
		{"differs only in contact", stKey("t-1", "s-1", "c-2")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, obs := stCounter(0, 0)
			stIncN(c, base, 2, stT0)
			if got := c.Inc(tc.other, stT0); got != 1 {
				t.Errorf("la otra conversación arranca su racha en 1, devolvió %d", got)
			}
			if got := c.Inc(base, stT0); got != 3 {
				t.Errorf("la racha de la primera sigue en 3, devolvió %d", got)
			}
			c.Close(tc.other, stT0)
			stWantSeen(t, obs, 1)
		})
	}
}

// ST-5 · Los límites no positivos caen a 30 min y 10.000, probado por conducta.
func TestStreak_NonPositiveLimitsFallBackToDefaults(t *testing.T) {
	for _, limit := range []int{0, -1} {
		t.Run("limit "+strconv.Itoa(limit), func(t *testing.T) {
			c, obs := stCounter(time.Duration(limit), limit)

			// 30 min: una emisión a 29 min de la anterior ACUMULA.
			idle := stContact("idle")
			c.Inc(idle, stT0)
			if got := c.Inc(idle, stT0.Add(29*time.Minute)); got != 2 {
				t.Fatalf("a 29 min la racha debe acumular (2), devolvió %d: idleTTL no es 30 min", got)
			}
			c.Close(idle, stT0)
			stWantSeen(t, obs, 2)

			// 10.000: caben 10.000 claves sin desalojar a nadie, y la 10.001 desaloja a la
			// más antigua (la primera, con racha 2).
			oldest := stContact("oldest")
			stIncN(c, oldest, 2, stT0)
			for i := 1; i < 10000; i++ {
				c.Inc(stContact("c-"+strconv.Itoa(i)), stT0.Add(time.Second))
			}
			stWantSeen(t, obs, 2)
			if got := c.Inc(stContact("one too many"), stT0.Add(2*time.Second)); got != 1 {
				t.Fatalf("la clave 10.001 entra con racha 1, devolvió %d", got)
			}
			stWantSeen(t, obs, 2, 2)
		})
	}
}

// ST-6 · Sin onClose el contador cuenta igual; solo no publica.
func TestStreak_WithoutOnCloseStillCounts(t *testing.T) {
	c := newStreakCounter(time.Minute, 1, nil)
	a, b := stContact("a"), stContact("b")

	if got := stIncN(c, a, 3, stT0); got != 3 {
		t.Errorf("sin hook, tres emisiones dan racha 3, devolvió %d", got)
	}
	if got := c.Max(stT0); got != 3 {
		t.Errorf("sin hook, Max = %d, quería 3", got)
	}
	// Los tres caminos que reportarían: desalojo, vencimiento en Inc y en Max, y Close.
	c.Inc(b, stT0.Add(time.Second))
	if got := c.Inc(b, stT0.Add(time.Hour)); got != 1 {
		t.Errorf("sin hook, la vencida reinicia en 1, devolvió %d", got)
	}
	if got := c.Max(stT0.Add(3 * time.Hour)); got != 0 {
		t.Errorf("sin hook, Max tras vencer todo = %d, quería 0", got)
	}
	c.Close(b, stT0)
}

// ST-7, ST-8 · Sin entrada devuelve 1; con entrada viva, acumula.
func TestStreak_IncAccumulatesWithinConversation(t *testing.T) {
	c, obs := stCounter(0, 0)
	key := stContact("c-1")
	for i, want := range []int{1, 2, 3} {
		if got := c.Inc(key, stT0.Add(time.Duration(i)*time.Second)); got != want {
			t.Fatalf("Inc #%d devolvió %d, quería la racha viva %d", i+1, got, want)
		}
	}
	stWantSeen(t, obs)
}

// ST-8, ST-11 · Un recorrido legítimo de 30 emisiones cada 20 s da 1…30 sin observar
// ninguna racha por el camino; el Close final observa UNA racha de 30.
func TestStreak_LongLegitimateStreakIsNotFalsified(t *testing.T) {
	c, obs := stCounter(0, 0)
	key := stContact("c-1")
	for i := range 30 {
		if got := c.Inc(key, stT0.Add(time.Duration(i)*20*time.Second)); got != i+1 {
			t.Fatalf("emisión %d: racha viva = %d, quería %d", i+1, got, i+1)
		}
	}
	stWantSeen(t, obs)
	c.Close(key, stT0)
	stWantSeen(t, obs, 30)
}

// ST-9 · El Inc que encuentra la entrada vencida cierra la racha vieja, la reporta con su
// longitud y devuelve 1 (la viva), nunca la cerrada.
func TestStreak_IdleEpisodeIsClosedByNextInc(t *testing.T) {
	c, obs := stCounter(30*time.Minute, 0)
	key := stContact("c-1")
	stIncN(c, key, 4, stT0)

	later := stT0.Add(31 * time.Minute)
	if got := c.Inc(key, later); got != 1 {
		t.Fatalf("tras 31 min arranca un episodio nuevo en 1, devolvió %d", got)
	}
	stWantSeen(t, obs, 4)
	// La nueva quedó sellada en `later`: la siguiente emisión acumula sobre ella.
	if got := c.Inc(key, later.Add(time.Minute)); got != 2 {
		t.Fatalf("la racha nueva debe acumular (2), devolvió %d", got)
	}
	stWantSeen(t, obs, 4)
}

// ST-10 · El límite es estricto y el MISMO en los tres sitios que lo evalúan: a
// exactamente idleTTL la racha sigue viva; un nanosegundo después, vencida.
func TestStreak_IdleLimitIsStrict(t *testing.T) {
	const ttl = 30 * time.Minute
	key := stContact("c-1")
	atLimit, pastLimit := stT0.Add(ttl), stT0.Add(ttl+time.Nanosecond)

	t.Run("inc", func(t *testing.T) {
		c, obs := stCounter(ttl, 0)
		stIncN(c, key, 2, stT0)
		if got := c.Inc(key, atLimit); got != 3 {
			t.Errorf("a exactamente idleTTL la racha acumula (3), devolvió %d", got)
		}
		stWantSeen(t, obs)

		c, obs = stCounter(ttl, 0)
		stIncN(c, key, 2, stT0)
		if got := c.Inc(key, pastLimit); got != 1 {
			t.Errorf("a idleTTL + 1 ns la racha está vencida (1), devolvió %d", got)
		}
		stWantSeen(t, obs, 2)
	})
	t.Run("max", func(t *testing.T) {
		c, obs := stCounter(ttl, 0)
		stIncN(c, key, 2, stT0)
		if got := c.Max(atLimit); got != 2 {
			t.Errorf("a exactamente idleTTL Max cuenta la racha (2), devolvió %d", got)
		}
		stWantSeen(t, obs)
		if got := c.Max(pastLimit); got != 0 {
			t.Errorf("a idleTTL + 1 ns Max no la cuenta (0), devolvió %d", got)
		}
		stWantSeen(t, obs, 2)
	})
	t.Run("full map", func(t *testing.T) {
		// Dos entradas a exactamente idleTTL NO están vencidas: el tope desaloja UNA (la
		// más antigua), no purga las dos.
		c, obs := stCounter(ttl, 2)
		stIncN(c, stContact("a"), 2, stT0)
		stIncN(c, stContact("b"), 2, stT0)
		c.Inc(stContact("new"), atLimit)
		stWantSeen(t, obs, 2)

		c, obs = stCounter(ttl, 2)
		stIncN(c, stContact("a"), 2, stT0)
		stIncN(c, stContact("b"), 2, stT0)
		c.Inc(stContact("new"), pastLimit)
		stWantSeen(t, obs, 2, 2)
	})
}

// ST-12, ST-22, ST-27 · Sobre un receptor nil nada entra en pánico: Inc y Max dan 0.
func TestStreak_NilReceiverIsSafe(t *testing.T) {
	var c *streakCounter
	if got := c.Inc(stContact("c-1"), stT0); got != 0 {
		t.Errorf("Inc sobre nil = %d, quería 0", got)
	}
	c.Close(stContact("c-1"), stT0)
	if got := c.Max(stT0); got != 0 {
		t.Errorf("Max sobre nil = %d, quería 0", got)
	}
}

// ST-17 · Close reporta la racha UNA vez con su longitud y deja la conversación limpia.
func TestStreak_CloseReportsOnceAndResets(t *testing.T) {
	c, obs := stCounter(0, 0)
	key := stContact("c-1")
	stIncN(c, key, 5, stT0)

	c.Close(key, stT0)
	stWantSeen(t, obs, 5)
	if got := c.Inc(key, stT0); got != 1 {
		t.Errorf("tras Close el Inc siguiente devuelve 1, devolvió %d", got)
	}
	stWantSeen(t, obs, 5)
}

// ST-18 · Cerrar una conversación que nunca auto-respondió no es una racha de 0.
func TestStreak_CloseWithoutStreakReportsNothing(t *testing.T) {
	c, obs := stCounter(0, 0)
	stIncN(c, stContact("other"), 2, stT0)
	c.Close(stContact("never replied"), stT0)
	stWantSeen(t, obs)
	if got := c.Max(stT0); got != 2 {
		t.Errorf("el Close de otra clave no toca la racha viva: Max = %d, quería 2", got)
	}
}

// ST-19 · El segundo Close no encuentra entrada y no duplica la observación.
func TestStreak_CloseIsIdempotent(t *testing.T) {
	c, obs := stCounter(0, 0)
	key := stContact("c-1")
	stIncN(c, key, 3, stT0)
	c.Close(key, stT0)
	c.Close(key, stT0)
	c.Close(key, stT0)
	stWantSeen(t, obs, 3)
}

// ST-20 · Una entrada vencida por inactividad se reporta igual al cerrarla.
func TestStreak_CloseReportsAnExpiredStreakToo(t *testing.T) {
	c, obs := stCounter(30*time.Minute, 0)
	key := stContact("c-1")
	stIncN(c, key, 6, stT0)
	c.Close(key, stT0.Add(24*time.Hour))
	stWantSeen(t, obs, 6)
}

// ST-21 · Close no usa `now`: el mismo resultado con el instante cero y un año después.
func TestStreak_CloseIgnoresNow(t *testing.T) {
	for name, now := range map[string]time.Time{
		"zero time":      {},
		"same instant":   stT0,
		"one year later": stT0.AddDate(1, 0, 0),
		"one year early": stT0.AddDate(-1, 0, 0),
	} {
		t.Run(name, func(t *testing.T) {
			c, obs := stCounter(30*time.Minute, 0)
			key := stContact("c-1")
			stIncN(c, key, 4, stT0)
			c.Close(key, now)
			stWantSeen(t, obs, 4)
			if got := c.Max(stT0); got != 0 {
				t.Errorf("tras Close no queda racha viva: Max = %d, quería 0", got)
			}
		})
	}
}

// ST-28 · onClose corre con el candado suelto: un hook que reentra al contador termina,
// sea cual sea el método que reporta y el método con el que reentra.
func TestStreak_OnCloseRunsOutsideTheLock(t *testing.T) {
	const ttl = 30 * time.Minute
	key, other := stContact("c-1"), stContact("other")
	expired := stT0.Add(time.Hour)

	reporters := map[string]func(c *streakCounter){
		"inc reports":   func(c *streakCounter) { c.Inc(key, expired) },
		"close reports": func(c *streakCounter) { c.Close(key, stT0) },
		"max reports":   func(c *streakCounter) { c.Max(expired) },
	}
	reentries := map[string]func(c *streakCounter){
		"hook calls inc":   func(c *streakCounter) { c.Inc(other, expired) },
		"hook calls close": func(c *streakCounter) { c.Close(other, expired) },
		"hook calls max":   func(c *streakCounter) { c.Max(expired) },
	}
	for reportName, reportFn := range reporters {
		for reentryName, reentryFn := range reentries {
			t.Run(reportName+"/"+reentryName, func(t *testing.T) {
				var c *streakCounter
				var seen []int
				c = newStreakCounter(ttl, 0, func(streak int) {
					seen = append(seen, streak)
					reentryFn(c)
				})
				stIncN(c, key, 3, stT0)

				done := make(chan struct{})
				go func() {
					defer close(done)
					reportFn(c)
				}()
				// El vigilante no es una pausa: con el código correcto nunca vence; solo
				// convierte el interbloqueo de un hook llamado bajo el candado en un fallo.
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("el hook que reentra al contador se quedó colgado: onClose corre bajo el candado")
				}
				if !slices.Equal(seen, []int{3}) {
					t.Errorf("rachas reportadas = %v, quería [3]", seen)
				}
			})
		}
	}
}

// ST-29 · Nunca se reporta una longitud <= 0, venga por el camino que venga. Una entrada
// así no puede nacer por la API (Inc siempre deja n >= 1): se planta a mano.
func TestStreak_NeverReportsNonPositiveLength(t *testing.T) {
	const ttl = 30 * time.Minute
	key := stContact("planted")
	plant := func(n int) (*streakCounter, *stObserver) {
		c, obs := stCounter(ttl, 1)
		c.mu.Lock()
		c.streaks[key] = &streakEntry{n: n, lastSeen: stT0}
		c.mu.Unlock()
		return c, obs
	}
	paths := map[string]func(c *streakCounter){
		"close":               func(c *streakCounter) { c.Close(key, stT0) },
		"max sweep":           func(c *streakCounter) { c.Max(stT0.Add(time.Hour)) },
		"inc on expired":      func(c *streakCounter) { c.Inc(key, stT0.Add(time.Hour)) },
		"full map sweep":      func(c *streakCounter) { c.Inc(stContact("new"), stT0.Add(time.Hour)) },
		"full map evict":      func(c *streakCounter) { c.Inc(stContact("new"), stT0) },
		"report called alone": func(c *streakCounter) { c.report([]int{0, -1}) },
	}
	for name, run := range paths {
		for _, n := range []int{0, -3} {
			t.Run(name+" n="+strconv.Itoa(n), func(t *testing.T) {
				c, obs := plant(n)
				run(c)
				stWantSeen(t, obs)
			})
		}
	}
}

// ST-3 · Uso concurrente: G goroutines emiten sobre una clave propia y una compartida,
// con Close y Max intercalados. Ninguna emisión se pierde ni se duplica: la suma de todo
// lo reportado al final es el total de emisiones. Para -race.
func TestStreak_ConcurrentUseCountsRight(t *testing.T) {
	const goroutines, rounds = 16, 200
	c, obs := stCounter(0, 0)
	shared := stContact("shared")

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			own := stContact("own-" + strconv.Itoa(g))
			for i := range rounds {
				if got := c.Inc(own, stT0); got != i+1 {
					t.Errorf("clave propia %d: emisión %d dio racha %d", g, i+1, got)
				}
				c.Inc(shared, stT0)
				if i%7 == 0 {
					c.Close(shared, stT0)
				}
				if i%11 == 0 {
					c.Max(stT0)
				}
			}
		})
	}
	wg.Wait()

	if got := c.Max(stT0); got < rounds {
		t.Errorf("Max = %d, quería al menos %d (la racha de cualquier clave propia)", got, rounds)
	}
	for g := range goroutines {
		c.Close(stContact("own-"+strconv.Itoa(g)), stT0)
	}
	c.Close(shared, stT0)

	total := 0
	for _, n := range obs.lengths() {
		total += n
	}
	if want := 2 * goroutines * rounds; total != want {
		t.Errorf("la suma de las rachas reportadas es %d, quería %d (una por emisión)", total, want)
	}
	if got := c.Max(stT0); got != 0 {
		t.Errorf("tras cerrar todo, Max = %d, quería 0", got)
	}
}
