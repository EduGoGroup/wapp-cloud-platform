package llmvia_test

import (
	"context"
	"errors"
	"testing"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// plaza_test.go — QUÉ PLAZA OCUPA UN TENANT (Plan 044 · Ola 2 · T2.7).
//
// Lo que se custodia aquí es UNA frase del enunciado que ningún test del pipeline
// puede fijar, porque el worker no sabe de vías a propósito: **por vía API el entero
// NO APLICA**. Allí el tope es de precio, no de capacidad, y serializar dos cadenas de
// lote sería una restricción inventada.

// frameEnrutador es el transporte que SÍ sabe decir qué Edge atiende — como el
// *gatewaygrpc.Server de producción. Cuenta las veces que se lo preguntan, que es la
// única forma de demostrar que por vía API NO se le pregunta.
type frameEnrutador struct {
	frameFake
	edge      string
	hay       bool
	preguntas int
}

func (f *frameEnrutador) PlazaDe(_, _ string) (string, bool) {
	f.preguntas++
	return f.edge, f.hay
}

// TestPlazaDe_LaViaLocalOcupaLaPlazaDeSuEdge es el camino normal, y hoy el de todos:
// un tenant SIN fila está en vía local (REQ-33) y su plaza es la de su Edge.
//
// 🔬 MUTACIÓN EJECUTADA (roja): devolver `("", false, nil)` en la rama local ⇒ ningún
// job ocuparía plaza jamás y el aforo del pipeline quedaría inerte sin un solo error.
func TestPlazaDe_LaViaLocalOcupaLaPlazaDeSuEdge(t *testing.T) {
	t.Parallel()
	fr := &frameEnrutador{edge: "edge-7", hay: true}
	s := selector(t, &storeFake{}, llmvia.WithFrame(fr))

	edge, ok, err := s.PlazaDe(context.Background(), "t-1", "sess-1")
	if err != nil {
		t.Fatalf("PlazaDe: %v", err)
	}
	if !ok || edge != "edge-7" {
		t.Fatalf("plaza = (%q,%v); un tenant sin fila está en vía local y ocupa la plaza de su Edge", edge, ok)
	}
}

// TestPlazaDe_LaViaAPINoOcupaPlazaNiPregunta es la frase del enunciado, y se afirma
// por partida doble: no hay plaza, Y NI SIQUIERA SE PREGUNTA por el Edge.
//
// Lo segundo importa tanto como lo primero: un `PlazaDe` que preguntara y luego tirara
// la respuesta pasaría este test a medias y dejaría escrito en el código que la vía API
// tiene algo que ver con los Edges del tenant, que es precisamente lo que no tiene.
//
// 🔬 MUTACIÓN EJECUTADA (roja): quitar el `case tenantllm.ViaAPI` y dejar que caiga a
// la rama local ⇒ el tenant de API ocupa la plaza de su Edge y `preguntas` vale 1.
func TestPlazaDe_LaViaAPINoOcupaPlazaNiPregunta(t *testing.T) {
	t.Parallel()
	fr := &frameEnrutador{edge: "edge-7", hay: true}
	store := &storeFake{hay: true, fila: tenantllm.Config{Via: tenantllm.ViaAPI}}
	s := selector(t, store, llmvia.WithFrame(fr))

	edge, ok, err := s.PlazaDe(context.Background(), "t-1", "sess-1")
	if err != nil {
		t.Fatalf("PlazaDe: %v", err)
	}
	if ok || edge != "" {
		t.Fatalf("plaza = (%q,%v) por vía API; allí el tope es de precio, no de capacidad", edge, ok)
	}
	if fr.preguntas != 0 {
		t.Fatalf("se preguntó %d vez/veces por el Edge de un tenant en vía API; esa pregunta no tiene sentido ahí",
			fr.preguntas)
	}
}

// TestPlazaDe_UnTransporteQueNoSabeNoRompeNada cubre el tercer origen legítimo del
// `ok = false`: el frame no tiene la capacidad (o no hay frame).
//
// No es un error: el pipeline sigue sin aforo, y el aviso de que eso pasa sale UNA vez
// al construir el selector, no una vez por job. Que no reviente aquí es lo que permite
// que los tests y los arranques parciales del repo —decenas de ellos con un frame
// falso— sigan funcionando.
func TestPlazaDe_UnTransporteQueNoSabeNoRompeNada(t *testing.T) {
	t.Parallel()
	for nombre, opts := range map[string][]llmvia.SelectorOption{
		"frame que no sabe": {llmvia.WithFrame(&frameFake{})},
		"sin frame":         nil,
	} {
		t.Run(nombre, func(t *testing.T) {
			t.Parallel()
			s := selector(t, &storeFake{}, opts...)
			edge, ok, err := s.PlazaDe(context.Background(), "t-1", "sess-1")
			if err != nil || ok || edge != "" {
				t.Fatalf("plaza = (%q,%v,%v); sin quien responda no hay plaza, y no es un fallo", edge, ok, err)
			}
		})
	}
}

// TestPlazaDe_UnaViaInventadaEsUnError fija que el vocabulario cerrado se respeta
// igual que en For: una fila corrupta NO se degrada a «sin plaza».
//
// Degradarla sería esconder una fila rota detrás de una conducta que parece normal —el
// job correría sin aforo y nadie se enteraría—, y es exactamente lo que el ADR-0044
// prohíbe («mientras un tenant tenga una vía configurada, el sistema jamás deberá usar
// la otra»).
func TestPlazaDe_UnaViaInventadaEsUnError(t *testing.T) {
	t.Parallel()
	store := &storeFake{hay: true, fila: tenantllm.Config{Via: "carísima"}}
	s := selector(t, store, llmvia.WithFrame(&frameEnrutador{edge: "edge-7", hay: true}))

	if _, ok, err := s.PlazaDe(context.Background(), "t-1", "sess-1"); ok || !errors.Is(err, llmvia.ErrViaDesconocida) {
		t.Fatalf("vía inventada ⇒ (ok=%v, err=%v); quiero ErrViaDesconocida", ok, err)
	}
}

// TestPlazaDe_SiLaConfigNoSeLeeEsUnError: la base caída es un error, no un «sin
// plaza». Lo que haga el llamante con él es cosa suya —el worker sigue sin aforo y lo
// dice en un Warn—, pero la información no se pierde por el camino.
func TestPlazaDe_SiLaConfigNoSeLeeEsUnError(t *testing.T) {
	t.Parallel()
	rota := errors.New("la base no contesta")
	s := selector(t, &storeFake{errGet: rota}, llmvia.WithFrame(&frameEnrutador{}))

	if _, _, err := s.PlazaDe(context.Background(), "t-1", "sess-1"); !errors.Is(err, rota) {
		t.Fatalf("err = %v; el fallo de lectura tiene que llegar arriba", err)
	}
}

// El transporte de producción satisface la capacidad, y el compilador lo comprueba
// desde el propio paquete (var _ en plaza.go). Aquí se comprueba lo OTRO: que la
// firma que el gateway publica es exactamente la que este paquete consume.
var _ = func(s *gatewaygrpc.Server) (string, bool) { return s.PlazaDe("", "") }
