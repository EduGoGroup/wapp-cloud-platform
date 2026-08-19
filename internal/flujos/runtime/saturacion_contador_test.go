package runtime_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// El descarte por saturación del pool de entrantes (OnIncoming: no hubo cupo dentro
// del incomingTimeout) es el ÚNICO camino del contador que no responde a una
// política: un mensaje real de un cliente que SÍ debía entrar al motor se tira. Vivía
// solo en el log, así que nadie podía saber si estaba pasando sin leer logs a mano.
// Aquí se fija que se CUENTA, con su propio motivo y sin confundirse con los cortes
// deliberados.

// resolverAtascado es un TenantResolver que RETIENE la primera llamada hasta que el
// test la suelta, y responde normal a partir de la segunda. Sirve para dejar el
// semáforo lleno de forma determinista: no mira el ctx a propósito, así el primer
// entrante conserva el cupo aunque su deadline expire. Cuenta las llamadas, que es
// la prueba de que el entrante descartado NUNCA llegó al motor.
type resolverAtascado struct {
	entro  chan struct{} // se cierra cuando el primer entrante ya tomó el cupo.
	soltar chan struct{} // lo cierra el test para dejar salir al primer entrante.
	salio  chan struct{} // se cierra cuando el primer entrante sale del resolver.
	mu     sync.Mutex
	n      int
}

func nuevoResolverAtascado() *resolverAtascado {
	return &resolverAtascado{
		entro:  make(chan struct{}),
		soltar: make(chan struct{}),
		salio:  make(chan struct{}),
	}
}

func (r *resolverAtascado) ResolveTenant(_ context.Context, _ string) (string, string, error) {
	r.mu.Lock()
	r.n++
	primera := r.n == 1
	r.mu.Unlock()
	if primera {
		close(r.entro)
		<-r.soltar
		close(r.salio)
	}
	return testTenant, "", nil
}

func (r *resolverAtascado) llamadas() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// esperarCorte sondea el contador hasta que registre n cortes con ese motivo. El
// descarte ocurre en la goroutine que lanza OnIncoming (el test no la ve terminar),
// de ahí el sondeo con techo generoso: bajo mutación —quitar la llamada a
// countReactiveBlocked en la rama del semáforo— es este helper el que pone el test
// en rojo.
func esperarCorte(t *testing.T, cnt *contadorCortes, motivo string, n int) {
	t.Helper()
	limite := time.Now().Add(3 * time.Second)
	for time.Now().Before(limite) {
		if cnt.total(motivo) >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("el contador no registró %d corte(s) con motivo %q (registró %d): el entrante descartado por saturación no se está contando",
		n, motivo, cnt.total(motivo))
}

// Con el pool lleno y sin cupo a tiempo, el entrante se descarta —igual que antes—
// pero ahora se cuenta con motivo saturation.
func TestReactiveBlocked_SaturacionDelPoolSeCuenta(t *testing.T) {
	cnt := nuevoContadorCortes()
	repo := store.NewMemoryRepository()
	res := nuevoResolverAtascado()
	sender := &fakeSender{}
	// Pool de UN cupo y deadline corto: inyectados aquí, sin tocar los defaults de
	// producción (defaultMaxConcurrentIncoming=64, defaultIncomingTimeout=30s), con
	// los que este escenario tardaría medio minuto en reproducirse.
	rt := runtime.New(repo, newEngine(), sender, res, contact.NewMemoryResolver(repo), discardLogger(),
		runtime.WithMaxConcurrentIncoming(1),
		runtime.WithIncomingTimeout(50*time.Millisecond),
		runtime.WithReactiveBlockedHook(cnt.registrar))

	// El primer entrante ocupa el único cupo y se queda dentro.
	rt.OnIncoming(testSession, incoming(testContact, "hola", "wamid.sat.1"))
	select {
	case <-res.entro:
	case <-time.After(3 * time.Second):
		t.Fatal("el primer entrante nunca tomó el cupo del pool: el escenario no se montó")
	}
	defer func() {
		close(res.soltar)
		<-res.salio
	}()

	// El segundo encuentra el pool lleno y agota su deadline esperando cupo.
	rt.OnIncoming(testSession, incoming(testContact, "¿hay alguien?", "wamid.sat.2"))

	esperarCorte(t, cnt, "saturation", 1)

	if got := res.llamadas(); got != 1 {
		t.Fatalf("el entrante saturado no debe llegar al motor (no-regresión: se sigue descartando); ResolveTenant se llamó %d veces, quiero 1", got)
	}
	if got := sender.count(); got != 0 {
		t.Fatalf("el entrante saturado no debe generar envíos, envió %d", got)
	}
	// La pérdida no debe disfrazarse de decisión: los tres motivos deliberados quedan
	// a cero.
	for _, deliberado := range []string{"passive", "self_loop", "rate_limit"} {
		if got := cnt.total(deliberado); got != 0 {
			t.Fatalf("la saturación es una PÉRDIDA, no una política: se contó %d vez/veces como %q", got, deliberado)
		}
	}
}
