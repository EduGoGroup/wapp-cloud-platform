package llmvia_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// TestWarm_SoloLaViaLocalTieneCacheQueCalentar.
//
// 🔴 EL CASO API ES EL QUE PAGA ESTE TEST, y su coste es real y ajeno: si el
// calentamiento se emitiera sin mirar la vía, el Edge de un tenant que clasifica por
// API gastaría ~50 s de la CPU de SU máquina y ~250 MB de su caché de prefijo por un
// prompt que nadie va a volver a pedir, compitiendo además con el clasificador que ese
// mismo Edge sí ejecuta. Es la máquina del cliente: no es un desperdicio nuestro.
//
// La otra mitad —que la vía local SÍ emita— es la que evita que el arreglo sea «no
// calentar nunca», que también pasaría un test escrito solo con el caso API.
//
// 🔬 MUTACIÓN: quitar la rama `case tenantllm.ViaAPI: return ErrViaSinCalentamiento`
// ⇒ rojo (el frame se toca con una fila `api`).
func TestWarm_SoloLaViaLocalTieneCacheQueCalentar(t *testing.T) {
	t.Parallel()
	in := llm.ClassifyRequestInput{
		Text:    "esto se ignora",
		Catalog: []llm.IntentSpec{{Name: "intake_request", Description: "pide productos"}},
	}

	for _, tc := range []struct {
		nombre  string
		fila    tenantllm.Config
		hay     bool
		emite   bool
		quiero  error
		warmup  bool
		destino string
	}{
		{
			nombre: "vía local: emite el frame marcado y dirigido",
			fila:   tenantllm.Config{Via: tenantllm.ViaLocal}, hay: true,
			emite: true, warmup: true, destino: "s-9",
		},
		{
			nombre: "sin fila: REQ-33 dice local, así que también calienta",
			emite:  true, warmup: true, destino: "s-9",
		},
		{
			nombre: "vía api: no hay caché nuestra que llenar y NO se toca el Edge",
			fila:   tenantllm.Config{Via: tenantllm.ViaAPI, Provider: "anthropic", Model: "m"}, hay: true,
			quiero: llmvia.ErrViaSinCalentamiento,
		},
		{
			nombre: "vía fuera del vocabulario: error, y tampoco se toca el Edge",
			fila:   tenantllm.Config{Via: "inventada"}, hay: true,
			quiero: llmvia.ErrViaDesconocida,
		},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			t.Parallel()
			f := &frameFake{out: `{"vers`}
			sel := selector(t, &storeFake{fila: tc.fila, hay: tc.hay}, llmvia.WithFrame(f))

			err := sel.Warm(context.Background(), "tenant-1", "s-9", in)
			if tc.quiero != nil {
				if !errors.Is(err, tc.quiero) {
					t.Fatalf("Warm = %v, quiero %v", err, tc.quiero)
				}
			} else if err != nil {
				t.Fatalf("Warm: %v", err)
			}

			if tocado := f.visto.Prompt != ""; tocado != tc.emite {
				t.Fatalf("¿se pidió inferencia al Edge? = %v, quiero %v", tocado, tc.emite)
			}
			if !tc.emite {
				return
			}
			if f.visto.Warmup != tc.warmup {
				t.Errorf("warmup = %v, quiero %v", f.visto.Warmup, tc.warmup)
			}
			if f.visto.TargetSessionID != tc.destino {
				t.Errorf("TargetSessionID = %q, quiero %q", f.visto.TargetSessionID, tc.destino)
			}
		})
	}
}

// TestWarm_NoEscribeAvisoDeDegradacion: un calentamiento que falla NO es una
// degradación de la vía del dueño. Nadie lo pidió, su fallo no le quita nada al
// cliente, y avisarle sería mandarlo a revisar un equipo que está perfectamente.
//
// Es la misma familia que ErrInferenceAbandonada, y por eso Warm NO envuelve el
// provider con el decorador que sí usa Selector.For.
//
// 🔬 MUTACIÓN: envolver en Warm el provider con s.notifying(...) —o llamar a
// s.avisar(...) ante el error— ⇒ rojo.
func TestWarm_NoEscribeAvisoDeDegradacion(t *testing.T) {
	t.Parallel()
	avisos := &avisoFake{}
	f := &frameFake{err: transporteError{m: "ollama_down"}}
	sel := selector(t, &storeFake{fila: tenantllm.Config{Via: tenantllm.ViaLocal}, hay: true},
		llmvia.WithFrame(f), llmvia.WithNotifier(avisos))

	if err := sel.Warm(context.Background(), "tenant-1", "s-1",
		llm.ClassifyRequestInput{Catalog: []llm.IntentSpec{{Name: "x"}}}); err == nil {
		t.Fatal("quiero el error del transporte")
	}
	if avisos.n != 0 {
		t.Fatalf("se escribieron %d avisos de degradación por un CALENTAMIENTO fallido: "+
			"el dueño recibiría una alarma por algo que nadie pidió y que no le quita nada", avisos.n)
	}
}
