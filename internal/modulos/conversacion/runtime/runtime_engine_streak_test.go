//go:build pendiente

package runtime_test

// runtime_engine_streak_test.go prueba (*Runtime).MaxAutoreplyStreak, la fuente del gauge de
// rachas (RT-11; trampa T-7: el scrape no es inocuo, BARRE las rachas vencidas). El reloj es
// siempre el inyectado del guion.

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// streakHarness monta un guion cuyo «hola» arranca un menú en bucle: cada «1» es una emisión.
func streakHarness(t *testing.T, extra ...runtime.Option) *harness {
	t.Helper()
	h := newHarness(t, withOptions(func(*harness) []runtime.Option { return extra }))
	h.seedFlow(exitMenuFlow())
	h.seedRule(trigger.Rule{Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: exitMenuFlowID})
	return h
}

// streakTalk hace que ese teléfono provoque n auto-respuestas seguidas (el arranque y n-1
// pasos por el menú).
func streakTalk(h *harness, phone string, n int) {
	h.t.Helper()
	for i := range n {
		text := "1"
		if i == 0 {
			text = "hola"
		}
		id := fmt.Sprintf("wa-%s-%d-%d", phone, h.clock.Now().Unix(), i)
		if err := h.handle(h.incomingFrom(phone, id, text)); err != nil {
			h.t.Fatalf("HandleIncoming(%s, %q): %v", phone, text, err)
		}
	}
}

// streakClosed devuelve las rachas reportadas al hook, ordenadas.
func streakClosed(h *harness) []int {
	closed := h.closedStreaks()
	sort.Ints(closed)
	return closed
}

const streakOtherPhone = "573009998877"

// Sobre un *Runtime nil, o sobre uno que no salió de New, devuelve 0 sin pánico.
func TestMaxAutoreplyStreak_ZeroWithoutARuntime(t *testing.T) {
	var missing *runtime.Runtime
	if got := missing.MaxAutoreplyStreak(); got != 0 {
		t.Errorf("sobre un Runtime nil = %d, quería 0", got)
	}
	if got := (&runtime.Runtime{}).MaxAutoreplyStreak(); got != 0 {
		t.Errorf("sobre un Runtime que no salió de New = %d, quería 0", got)
	}
}

// Devuelve la racha VIVA más larga —ni la suma ni la última—, o 0 si no hay ninguna.
func TestMaxAutoreplyStreak_ReportsTheLongestLiveStreak(t *testing.T) {
	h := streakHarness(t)
	if got := h.rt.MaxAutoreplyStreak(); got != 0 {
		t.Fatalf("sin ninguna racha = %d, quería 0", got)
	}

	streakTalk(h, harnessPhone, 3)
	streakTalk(h, streakOtherPhone, 1)

	if got := h.rt.MaxAutoreplyStreak(); got != 3 {
		t.Errorf("racha máxima = %d, quería 3 (la más larga de las dos conversaciones)", got)
	}
	if got := h.closedStreaks(); len(got) != 0 {
		t.Errorf("rachas cerradas = %v, con las dos vivas no se reporta ninguna", got)
	}
}

// T-7 · En el mismo recorrido BARRE las rachas vencidas por inactividad (más de 30 min sin
// auto-respuesta, con el reloj inyectado): las reporta al hook una sola vez y no cuentan para
// el máximo. A exactamente 30 min siguen vivas.
func TestMaxAutoreplyStreak_SweepsExpiredStreaks(t *testing.T) {
	h := streakHarness(t)
	streakTalk(h, harnessPhone, 3)
	streakTalk(h, streakOtherPhone, 2)

	h.clock.Advance(30 * time.Minute)
	if got := h.rt.MaxAutoreplyStreak(); got != 3 || len(h.closedStreaks()) != 0 {
		t.Fatalf("a exactamente 30 min: máximo = %d, cerradas = %v; quería 3 y ninguna", got, h.closedStreaks())
	}

	h.clock.Advance(time.Second)
	if got := h.rt.MaxAutoreplyStreak(); got != 0 {
		t.Errorf("pasados los 30 min: máximo = %d, quería 0", got)
	}
	if got := streakClosed(h); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("rachas cerradas = %v, quería [2 3]: el scrape cierra los episodios abandonados", got)
	}
	if got := h.rt.MaxAutoreplyStreak(); got != 0 || len(h.closedStreaks()) != 2 {
		t.Errorf("segunda llamada: máximo = %d, cerradas = %v; no se reporta dos veces la misma", got, h.closedStreaks())
	}
}

// Una racha vencida no cuenta para el máximo aunque fuera la más larga.
func TestMaxAutoreplyStreak_ExpiredDoesNotCountEvenIfLongest(t *testing.T) {
	h := streakHarness(t)
	streakTalk(h, harnessPhone, 4)
	h.clock.Advance(20 * time.Minute)
	streakTalk(h, streakOtherPhone, 1)
	h.clock.Advance(10*time.Minute + time.Second)

	if got := h.rt.MaxAutoreplyStreak(); got != 1 {
		t.Errorf("máximo = %d, quería 1: la de 4 venció y no puede fosilizar el gauge", got)
	}
	if got := h.closedStreaks(); len(got) != 1 || got[0] != 4 {
		t.Errorf("rachas cerradas = %v, quería [4]", got)
	}
}

// Sin hook el contador sigue contando y el barrido no rompe.
func TestMaxAutoreplyStreak_WorksWithoutHook(t *testing.T) {
	h := streakHarness(t, runtime.WithAutoreplyStreakHook(nil))
	streakTalk(h, harnessPhone, 2)

	if got := h.rt.MaxAutoreplyStreak(); got != 2 {
		t.Errorf("máximo = %d, quería 2", got)
	}
	h.clock.Advance(31 * time.Minute)
	if got := h.rt.MaxAutoreplyStreak(); got != 0 {
		t.Errorf("máximo tras vencer = %d, quería 0", got)
	}
	if got := h.closedStreaks(); len(got) != 0 {
		t.Errorf("rachas reportadas = %v, sin hook nadie las recibe", got)
	}
}
