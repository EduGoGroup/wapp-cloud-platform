//go:build pendiente

package indice_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// cache_eviction_test.go es el trozo de cache_test.go (E-13) con la cota de memoria
// de la caché —el desalojo del menos usado— y la concurrencia.

// tenant es el nombre del tenant número i de un test de desalojo.
func tenant(i int) string { return "t" + strconv.Itoa(i) }

// ---------------------------------------------------------------------------
// LA COTA DE MEMORIA DE LA CACHÉ
// ---------------------------------------------------------------------------

// TestCache_EvictsTheLeastRecentlyUsed: sin tope, un proceso que atienda a muchos
// tenants acumula un índice por cada uno y no lo suelta nunca.
func TestCache_EvictsTheLeastRecentlyUsed(t *testing.T) {
	f := newFakeSource()
	c := newCache(t, f, 2)
	for i := range 3 {
		f.publish(tenant(i), docV1)
	}

	get(t, c, "t0")
	get(t, c, "t1")
	if c.Tamano() != 2 {
		t.Fatalf("Tamano() = %d con dos tenants y tope 2; se esperaban 2", c.Tamano())
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 2}, "llegar al tope no desaloja: se desaloja al SUPERARLO")

	get(t, c, "t2")
	if c.Tamano() != 2 {
		t.Errorf("Tamano() = %d; el tope de 2 se respeta", c.Tamano())
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 3, Desalojos: 1}, "el tercero desaloja a uno, y solo a uno")

	// t0 fue el menos usado: t2 y t1 siguen dentro.
	get(t, c, "t2")
	get(t, c, "t1")
	assertStats(t, c, indice.Estadisticas{Construcciones: 3, Aciertos: 2, Desalojos: 1}, "t1 y t2 seguían cacheados")

	get(t, c, "t0")
	assertStats(t, c, indice.Estadisticas{Construcciones: 4, Aciertos: 2, Desalojos: 2}, "t0 fue el desalojado: se reconstruye (y desaloja a su vez)")
}

// TestCache_DefaultBoundIs64_AndAHitRefreshes recorre el tope por defecto por sus
// dos lados —64 caben, el 65 desaloja— y comprueba QUIÉN sale: el menos usado
// RECIENTEMENTE, no el más antiguo. t0 es el primero que entró, pero un acierto lo
// refresca y el que sale es t1.
func TestCache_DefaultBoundIs64_AndAHitRefreshes(t *testing.T) {
	f := newFakeSource()
	c := newCache(t, f, 0)
	for i := range 65 {
		f.publish(tenant(i), docV1)
	}

	for i := range 64 {
		get(t, c, tenant(i))
	}
	if c.Tamano() != 64 {
		t.Fatalf("Tamano() = %d; 64 tenants caben en el tope por defecto", c.Tamano())
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 64}, "64 tenants no desalojan a nadie")

	get(t, c, "t0") // acierto: t0 pasa a ser el más reciente
	get(t, c, "t64")
	if c.Tamano() != 64 {
		t.Errorf("Tamano() = %d; el tenant 65 no hace crecer la caché", c.Tamano())
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 65, Aciertos: 1, Desalojos: 1}, "el tenant 65 desaloja exactamente a uno")

	// Todos menos t1 siguen dentro: son 64 aciertos y ninguna construcción.
	get(t, c, "t0")
	for i := 2; i < 65; i++ {
		get(t, c, tenant(i))
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 65, Aciertos: 65, Desalojos: 1}, "el acierto refrescó a t0: el desalojado es t1 y nadie más")

	get(t, c, "t1")
	assertStats(t, c, indice.Estadisticas{Construcciones: 66, Aciertos: 65, Desalojos: 2}, "t1 era el desalojado: se reconstruye")
}

// TestCache_NonPositiveMaxFallsBackTo64: la configuración nunca DESACTIVA el tope
// por accidente.
func TestCache_NonPositiveMaxFallsBackTo64(t *testing.T) {
	for _, limit := range []int{0, -1} {
		t.Run("max "+strconv.Itoa(limit), func(t *testing.T) {
			f := newFakeSource()
			c := newCache(t, f, limit)
			for i := range 66 {
				f.publish(tenant(i), docV1)
				get(t, c, tenant(i))
			}
			if c.Tamano() != 64 {
				t.Errorf("Tamano() = %d con max = %d; el tope cae a 64", c.Tamano(), limit)
			}
			assertStats(t, c, indice.Estadisticas{Construcciones: 66, Desalojos: 2}, "66 tenants con tope 64 son dos desalojos")
		})
	}
}

// TestCache_MaxOne: el tope más pequeño que se puede pedir se respeta tal cual.
func TestCache_MaxOne(t *testing.T) {
	f := newFakeSource()
	c := newCache(t, f, 1)
	f.publish("t0", docV1)
	f.publish("t1", docV1)

	get(t, c, "t0")
	last := get(t, c, "t1")
	if c.Tamano() != 1 {
		t.Errorf("Tamano() = %d con max = 1", c.Tamano())
	}
	if again := get(t, c, "t1"); again != last {
		t.Errorf("con max = 1 se desalojó al recién llegado en vez de al anterior")
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 2, Aciertos: 1, Desalojos: 1}, "sale el anterior, no el que acaba de entrar")
}

// TestCache_ReplacingATenantEvictsNobody: con la caché LLENA, el cambio de
// contenido de un tenant que ya estaba sustituye su índice y no desaloja a nadie.
func TestCache_ReplacingATenantEvictsNobody(t *testing.T) {
	f := newFakeSource()
	c := newCache(t, f, 2)
	f.publish("t0", docV1)
	f.publish("t1", docV1)
	get(t, c, "t0")
	get(t, c, "t1")

	f.publish("t0", docV2)
	if fresh := get(t, c, "t0"); fresh.Articulos() != 3 {
		t.Errorf("Articulos() = %d; se esperaba el índice del contenido nuevo", fresh.Articulos())
	}
	if c.Tamano() != 2 {
		t.Errorf("Tamano() = %d; siguen siendo dos tenants", c.Tamano())
	}
	get(t, c, "t1")
	assertStats(t, c, indice.Estadisticas{Construcciones: 3, Aciertos: 1}, "sustituir no desaloja: t1 sigue dentro")
}

// ---------------------------------------------------------------------------
// CONCURRENCIA (T-6): LECTURA FUERA DEL CANDADO, INDEXADO DENTRO
// ---------------------------------------------------------------------------

// TestObtener_ConcurrentSameTenant_ReadsOutsideTheLock_BuildsOnce lanza N jobs del
// MISMO tenant y los retiene a todos DENTRO de la lectura hasta que han llegado
// los N; luego los suelta a la vez.
//
//   - Que lleguen los N prueba que la lectura va FUERA del candado: si se leyera
//     con el candado tomado solo entraría uno y los demás esperarían fuera.
//   - Que al soltarlos haya UNA construcción y N-1 aciertos, todos con el mismo
//     puntero, prueba que el indexado va DENTRO: fuera, los N construirían a la vez.
//
// El documento es el de 2.000 artículos para que construir tarde lo bastante como
// para que un indexado fuera del candado se solape. El reloj de pared solo aparece
// como vigilante del fallo (un candado mal puesto colgaría el test); no decide
// ninguna aserción.
func TestObtener_ConcurrentSameTenant_ReadsOutsideTheLock_BuildsOnce(t *testing.T) {
	const jobs = 16

	f := newFakeSource()
	f.publish("t1", string(sizedDocument(t, 2000)))
	var arrived sync.WaitGroup
	arrived.Add(jobs)
	release := make(chan struct{})
	f.gate = func() {
		arrived.Done()
		<-release
	}
	c := newCache(t, f, 0)

	got := make([]*indice.Indice, jobs)
	errs := make([]error, jobs)
	var done sync.WaitGroup
	for i := range jobs {
		done.Go(func() {
			got[i], errs[i] = c.Obtener(context.Background(), "t1")
		})
	}

	allReading := make(chan struct{})
	go func() {
		arrived.Wait()
		close(allReading)
	}()
	select {
	case <-allReading:
	case <-time.After(30 * time.Second):
		close(release)
		t.Fatalf("no llegaron los %d jobs a la lectura: 🔴 la Fuente se está leyendo con el candado tomado (T-6)", jobs)
	}
	close(release)
	done.Wait()

	for i := range jobs {
		if errs[i] != nil {
			t.Fatalf("job %d: Obtener = %v", i, errs[i])
		}
		if got[i] != got[0] {
			t.Errorf("job %d recibió un índice distinto del job 0: se construyó más de una vez", i)
		}
	}
	if got[0].Articulos() != 2000 {
		t.Errorf("Articulos() = %d; se esperaban 2000", got[0].Articulos())
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 1, Aciertos: jobs - 1},
		"🔴 dos llamadas simultáneas del mismo tenant no construyen dos veces")
	if f.readCount() != jobs {
		t.Errorf("lecturas = %d; cada job lee una vez (%d)", f.readCount(), jobs)
	}
}

// TestCache_ConcurrentTenantsAndCounters mezcla jobs de varios tenants —más que el
// tope, para que haya desalojos— con lecturas de Estadisticas y Tamano. Con -race,
// un acceso al estado fuera del candado se delata aquí.
func TestCache_ConcurrentTenantsAndCounters(t *testing.T) {
	const tenants, rounds = 6, 20

	f := newFakeSource()
	for i := range tenants {
		f.publish(tenant(i), docV1)
	}
	c := newCache(t, f, 4)

	var done sync.WaitGroup
	for i := range tenants {
		done.Go(func() {
			for range rounds {
				idx, err := c.Obtener(context.Background(), tenant(i))
				if err != nil || idx.Articulos() != 2 {
					t.Errorf("Obtener(%s) = %v, %v", tenant(i), idx, err)
					return
				}
				if c.Tamano() > 4 {
					t.Errorf("Tamano() = %d; nunca pasa del tope (4)", c.Tamano())
				}
				c.Estadisticas()
			}
		})
	}
	done.Wait()

	st := c.Estadisticas()
	if st.Aciertos+st.Construcciones != tenants*rounds {
		t.Errorf("aciertos + construcciones = %d; cada Obtener es una cosa o la otra (%d)", st.Aciertos+st.Construcciones, tenants*rounds)
	}
	if c.Tamano() != 4 || st.Construcciones != st.Desalojos+4 {
		t.Errorf("Tamano() = %d y construcciones (%d) − desalojos (%d) ≠ 4; lo construido y no desalojado es lo que hay",
			c.Tamano(), st.Construcciones, st.Desalojos)
	}
}
