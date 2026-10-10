package runtime

import (
	"testing"
	"time"
)

// Tests de streak.go que van del TOPE del mapa (ST-13 a ST-16) y de Max (ST-23 a ST-26).
// Partidos de streak_test.go por tamaño (E-13); los dobles y el registro de mutantes
// están allí.

// ST-13, ST-14 · Con el mapa lleno y alguna vencida, la clave nueva purga TODAS las
// vencidas, las reporta con su longitud y no desaloja a ninguna viva.
func TestStreak_FullMapSweepsExpiredFirst(t *testing.T) {
	c, obs := stCounter(30*time.Minute, 3)
	expiredA, expiredB, alive := stContact("expired-a"), stContact("expired-b"), stContact("alive")
	stIncN(c, expiredA, 2, stT0)
	stIncN(c, expiredB, 3, stT0.Add(time.Second))
	now := stT0.Add(40 * time.Minute)
	// La viva es la MÁS ANTIGUA de las supervivientes: si además de purgar se desalojara,
	// saldría ella.
	stIncN(c, alive, 5, now)

	if got := c.Inc(stContact("new"), now.Add(time.Second)); got != 1 {
		t.Fatalf("la clave nueva entra con racha 1, devolvió %d", got)
	}
	stWantSeen(t, obs, 2, 3)
	if got := c.Inc(alive, now.Add(2*time.Second)); got != 6 {
		t.Errorf("la viva no se desaloja: su racha sigue en 6, devolvió %d", got)
	}
	// Las purgadas ya no están: vuelven a empezar y nadie las reporta otra vez.
	if got := c.Inc(expiredA, now.Add(3*time.Second)); got != 1 {
		t.Errorf("la vencida purgada arranca de nuevo en 1, devolvió %d", got)
	}
	stWantSeen(t, obs, 2, 3)
}

// ST-15 · Con el mapa lleno y ninguna vencida, sale la de lastSeen más antiguo, se reporta
// con su longitud y la clave nueva entra con 1.
func TestStreak_FullMapEvictsOldestAndReportsIt(t *testing.T) {
	// La más antigua ocupa cada posición de inserción: el desalojo no depende del orden.
	for _, oldestAt := range []int{0, 1, 2} {
		c, obs := stCounter(30*time.Minute, 3)
		keys := []struct {
			name string
			n    int
		}{{"a", 2}, {"b", 3}, {"c", 4}}
		for i, k := range keys {
			at := stT0.Add(time.Duration(i+1) * time.Minute)
			if i == oldestAt {
				at = stT0
			}
			stIncN(c, stContact(k.name), k.n, at)
		}

		now := stT0.Add(10 * time.Minute)
		if got := c.Inc(stContact("new"), now); got != 1 {
			t.Fatalf("la clave nueva entra con racha 1, devolvió %d", got)
		}
		stWantSeen(t, obs, keys[oldestAt].n)
		for i, k := range keys {
			if i == oldestAt {
				continue // desalojada: ya no está en el mapa.
			}
			want := k.n + 1
			if got := c.Inc(stContact(k.name), now); got != want {
				t.Errorf("más antigua=%s: la superviviente %s debe seguir (%d), devolvió %d",
					keys[oldestAt].name, k.name, want, got)
			}
		}
		if got := c.Inc(stContact("new"), now); got != 2 {
			t.Errorf("la clave nueva sigue viva (2), devolvió %d", got)
		}
		stWantSeen(t, obs, keys[oldestAt].n)
	}
}

// ST-16 · Con el mapa lleno, un Inc sobre una clave que YA existe no desaloja ni purga; y
// con hueco libre, una clave nueva tampoco, aunque haya vencidas.
func TestStreak_ExistingKeyOnFullMapEvictsNothing(t *testing.T) {
	c, obs := stCounter(30*time.Minute, 2)
	a, b := stContact("a"), stContact("b")
	c.Inc(a, stT0)
	stIncN(c, b, 2, stT0.Add(20*time.Minute))

	// Mapa lleno; `a` está vencida a los 40 min, pero el Inc es sobre `b`, que existe.
	if got := c.Inc(b, stT0.Add(40*time.Minute)); got != 3 {
		t.Fatalf("Inc sobre una clave existente acumula (3), devolvió %d", got)
	}
	stWantSeen(t, obs)

	// Con hueco libre (tope 3, dos entradas), la clave nueva no purga la vencida.
	roomy, roomyObs := stCounter(30*time.Minute, 3)
	roomy.Inc(a, stT0)
	roomy.Inc(b, stT0.Add(40*time.Minute))
	if got := roomy.Inc(stContact("new"), stT0.Add(40*time.Minute)); got != 1 {
		t.Fatalf("la clave nueva entra con racha 1, devolvió %d", got)
	}
	stWantSeen(t, roomyObs)
}

// ST-23 · Max es la mayor de las rachas vivas —ni la suma ni la última—, baja cuando esa
// conversación se cierra, y vale 0 sin rachas.
func TestStreak_MaxReturnsLongestAlive(t *testing.T) {
	c, obs := stCounter(0, 0)
	if got := c.Max(stT0); got != 0 {
		t.Fatalf("sin rachas Max = %d, quería 0", got)
	}

	short, long, last := stContact("short"), stContact("long"), stContact("last")
	stIncN(c, short, 2, stT0)
	stIncN(c, long, 7, stT0)
	stIncN(c, last, 3, stT0.Add(time.Second))
	if got := c.Max(stT0.Add(time.Second)); got != 7 {
		t.Fatalf("Max = %d, quería 7 (la mayor; ni la suma 12 ni la última 3)", got)
	}

	c.Close(long, stT0)
	if got := c.Max(stT0.Add(time.Second)); got != 3 {
		t.Fatalf("cerrada la más larga, Max = %d, quería 3", got)
	}
	c.Close(last, stT0)
	c.Close(short, stT0)
	if got := c.Max(stT0.Add(time.Second)); got != 0 {
		t.Fatalf("cerradas todas, Max = %d, quería 0", got)
	}
	stWantSeen(t, obs, 7, 3, 2)
}

// ST-24, ST-25 · Max barre las vencidas: la vencida más larga no es el máximo, se reporta
// y desaparece del mapa.
func TestStreak_MaxSweepsAndReportsExpired(t *testing.T) {
	c, obs := stCounter(30*time.Minute, 0)
	fossilA, fossilB, alive := stContact("fossil-a"), stContact("fossil-b"), stContact("alive")
	stIncN(c, fossilA, 30, stT0)
	stIncN(c, fossilB, 12, stT0.Add(time.Minute))
	now := stT0.Add(time.Hour)
	stIncN(c, alive, 4, now)

	if got := c.Max(now); got != 4 {
		t.Fatalf("Max = %d, quería 4: una racha vencida no cuenta aunque sea la más larga", got)
	}
	stWantSeen(t, obs, 30, 12)
	// Desaparecieron: el Inc siguiente no encuentra nada que cerrar y arranca en 1.
	if got := c.Inc(fossilA, now); got != 1 {
		t.Errorf("la vencida barrida arranca de nuevo en 1, devolvió %d", got)
	}
	stWantSeen(t, obs, 30, 12)
}

// ST-26 · Lo que Max barre lo borra: dos Max seguidos no reportan dos veces la misma racha.
func TestStreak_MaxNeverReportsTheSameStreakTwice(t *testing.T) {
	c, obs := stCounter(30*time.Minute, 0)
	key := stContact("abandoned")
	stIncN(c, key, 9, stT0)
	now := stT0.Add(time.Hour)

	for i := range 3 {
		if got := c.Max(now.Add(time.Duration(i) * time.Minute)); got != 0 {
			t.Fatalf("Max #%d = %d, quería 0", i+1, got)
		}
	}
	c.Close(key, now)
	stWantSeen(t, obs, 9)
}
