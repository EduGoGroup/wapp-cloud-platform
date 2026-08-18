package gatewaygrpc

import (
	"io"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// TestNewMaterializaElCarrilDeTrabajo: el carril NUNCA arranca sin tope de cola ni
// sin reloj. Es el mismo criterio —y el mismo molde— que
// TestNewMaterializaElAckTimeout: un Server construido sin las opciones, o con
// valores absurdos, cae a los defaults en vez de quedarse en cero. Un cero aquí
// significaría cola sin tope (memoria sin techo por stream) o trabajo sin deadline,
// que es justo lo que el Plan 050 · Ola 1 viene a eliminar.
func TestNewMaterializaElCarrilDeTrabajo(t *testing.T) {
	t.Parallel()
	log := logger.New(logger.WithWriter(io.Discard))

	for _, tc := range []struct {
		nombre      string
		opts        []Option
		quieroCola  int
		quieroReloj time.Duration
	}{
		{"sin opción", nil, defaultWorkQueue, defaultWorkBudget},
		{"cero explícito", []Option{WithWorkQueue(0), WithWorkTimeout(0)}, defaultWorkQueue, defaultWorkBudget},
		{"negativo", []Option{WithWorkQueue(-1), WithWorkTimeout(-time.Second)}, defaultWorkQueue, defaultWorkBudget},
		{"configurado", []Option{WithWorkQueue(8), WithWorkTimeout(2 * time.Second)}, 8, 2 * time.Second},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			t.Parallel()
			srv := New(session.NewRegistry(), log, tc.opts...)
			if srv.workQueue != tc.quieroCola {
				t.Errorf("workQueue = %d, quiero %d", srv.workQueue, tc.quieroCola)
			}
			if srv.workBudget != tc.quieroReloj {
				t.Errorf("workBudget = %v, quiero %v", srv.workBudget, tc.quieroReloj)
			}
		})
	}
}

// TestDefaultsDelCarrilSonLosPactados fija los DOS NÚMEROS que el plan decidió, no
// solo su existencia: 64 (igualado al techo de entrantes concurrentes del runtime de
// flujos, para que ninguna de las dos colas sea el cuello por accidente) y 5s (el
// valor ya calibrado de offlinePersistTimeout). Si alguien los cambia aquí sin
// cambiar el otro extremo, este test lo cuenta.
func TestDefaultsDelCarrilSonLosPactados(t *testing.T) {
	t.Parallel()
	if defaultWorkQueue != 64 {
		t.Errorf("defaultWorkQueue = %d, quiero 64 (el techo de entrantes concurrentes)", defaultWorkQueue)
	}
	if defaultWorkBudget != 5*time.Second {
		t.Errorf("defaultWorkBudget = %v, quiero 5s (el valor ya calibrado de offlinePersistTimeout)", defaultWorkBudget)
	}
}
