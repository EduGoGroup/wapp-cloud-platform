//go:build pendiente

package llmvia_test

// QUÉ PLAZA OCUPA UN TENANT (Selector.PlazaDe · ADR-0046 Mecanismo 1). Es la parte de los
// tests de llmvia.go que no cabe en llmvia_test.go (E-13).
//
// Lo que se custodia aquí es UNA frase que ningún test del pipeline puede fijar, porque el
// worker no sabe de vías a propósito: **por vía API el entero NO APLICA**. Allí el tope es
// de precio, no de capacidad, y serializar dos cadenas de lote sería una restricción
// inventada.

import (
	"context"
	"errors"
	"testing"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm/tenantllmhelpertest"
)

// La firma que el gateway NUEVO publica es exactamente la que este paquete busca por
// aserción de tipo. Si alguien renombrara Server.PlazaDe, el aforo se apagaría entero sin
// un solo error; aquí deja de compilar.
var _ interface {
	PlazaDe(tenantID, originSessionID string) (string, bool)
} = (*edgegrpc.Server)(nil)

// TestPlazaDe_TheLocalRouteTakesTheSlotOfItsEdge es el camino normal, y hoy el de todos: un
// tenant SIN fila está en vía local (REQ-33) y su plaza es la de su Edge. Devolver «sin
// plaza» en la rama local dejaría el aforo del pipeline inerte sin un solo error.
func TestPlazaDe_TheLocalRouteTakesTheSlotOfItsEdge(t *testing.T) {
	t.Parallel()
	for name, store := range map[string]*tenantllmhelpertest.Memoria{
		"no row":    tenantllmhelpertest.NewMemoria(),
		"local row": rowStore(t, tenantllm.Config{Via: tenantllm.ViaLocal}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			frame := &routerFrame{edge: "edge-7", found: true}
			edge, ok, err := newSelector(t, store, llmvia.WithFrame(frame)).PlazaDe(context.Background(), testTenant, "sess-1")
			if err != nil || !ok || edge != "edge-7" {
				t.Fatalf("plaza = (%q, %v, %v); un tenant en vía local ocupa la plaza de su Edge", edge, ok, err)
			}
			// La pregunta llega al transporte con el tenant y la sesión tal cual.
			if want := [2]string{testTenant, "sess-1"}; len(frame.asked) != 1 || frame.asked[0] != want {
				t.Errorf("preguntas al transporte = %v, quería una: %v", frame.asked, want)
			}
			frame.requireUntouched(t) // preguntar por la plaza no es inferir
			if n := store.APIKeyCalls(); n != 0 {
				t.Errorf("se pidió la credencial %d veces para saber una plaza", n)
			}
		})
	}

	// El transporte sabe responder y dice que NO hay Edge: tampoco es un fallo.
	t.Run("no live edge", func(t *testing.T) {
		t.Parallel()
		frame := &routerFrame{}
		edge, ok, err := newSelector(t, tenantllmhelpertest.NewMemoria(), llmvia.WithFrame(frame)).
			PlazaDe(context.Background(), testTenant, "sess-1")
		if err != nil || ok || edge != "" {
			t.Fatalf("plaza = (%q, %v, %v); sin Edge vivo no hay plaza, y no es un fallo", edge, ok, err)
		}
	})
}

// TestPlazaDe_TheAPIRouteTakesNoSlotAndDoesNotEvenAsk es la frase del enunciado, y se
// afirma por partida doble: no hay plaza, Y NI SIQUIERA SE PREGUNTA por el Edge. Un PlazaDe
// que preguntara y luego tirara la respuesta dejaría escrito en el código que la vía API
// tiene algo que ver con los Edges del tenant, que es precisamente lo que no tiene.
func TestPlazaDe_TheAPIRouteTakesNoSlotAndDoesNotEvenAsk(t *testing.T) {
	t.Parallel()
	frame := &routerFrame{edge: "edge-7", found: true}
	store := rowStore(t, apiRow())

	edge, ok, err := newSelector(t, store, llmvia.WithFrame(frame)).PlazaDe(context.Background(), testTenant, "sess-1")
	if err != nil || ok || edge != "" {
		t.Fatalf("plaza = (%q, %v, %v) por vía API; allí el tope es de precio, no de capacidad", edge, ok, err)
	}
	if len(frame.asked) != 0 {
		t.Fatalf("se preguntó %d vez/veces por el Edge de un tenant en vía API; esa pregunta no tiene sentido ahí", len(frame.asked))
	}
	if n := store.APIKeyCalls(); n != 0 {
		t.Errorf("se pidió la credencial %d veces para saber una plaza", n)
	}
}

// TestPlazaDe_ATransportThatCannotAnswerBreaksNothing cubre el tercer origen legítimo del
// `ok = false`: el frame no tiene la capacidad, o no hay frame. No es un error: el pipeline
// sigue sin aforo, y el aviso de que eso pasa sale UNA vez al construir el selector.
func TestPlazaDe_ATransportThatCannotAnswerBreaksNothing(t *testing.T) {
	t.Parallel()
	for name, opts := range map[string][]llmvia.SelectorOption{
		"frame that cannot route": {llmvia.WithFrame(&fakeFrame{})},
		"no frame":                nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			edge, ok, err := newSelector(t, tenantllmhelpertest.NewMemoria(), opts...).PlazaDe(context.Background(), testTenant, "sess-1")
			if err != nil || ok || edge != "" {
				t.Fatalf("plaza = (%q, %v, %v); sin quien responda no hay plaza, y no es un fallo", edge, ok, err)
			}
		})
	}
}

// TestPlazaDe_AnInventedRouteIsAnError fija que el vocabulario cerrado se respeta igual que
// en For: una fila corrupta NO se degrada a «sin plaza». Degradarla sería esconder una fila
// rota detrás de una conducta que parece normal —el job correría sin aforo y nadie se
// enteraría—.
func TestPlazaDe_AnInventedRouteIsAnError(t *testing.T) {
	t.Parallel()
	store := &stubStore{found: true, row: tenantllm.Config{Via: "carísima"}}
	frame := &routerFrame{edge: "edge-7", found: true}

	edge, ok, err := newSelector(t, store, llmvia.WithFrame(frame)).PlazaDe(context.Background(), testTenant, "sess-1")
	if !errors.Is(err, llmvia.ErrViaDesconocida) || ok || edge != "" {
		t.Fatalf("vía inventada ⇒ (%q, %v, %v); quería (\"\", false, ErrViaDesconocida)", edge, ok, err)
	}
	if len(frame.asked) != 0 {
		t.Errorf("se preguntó al transporte %d veces por una vía que no existe", len(frame.asked))
	}
}

// TestPlazaDe_IfTheConfigCannotBeReadItIsAnError: la base caída es un error, no un «sin
// plaza». Lo que haga el llamante con él es cosa suya, pero la información no se pierde.
func TestPlazaDe_IfTheConfigCannotBeReadItIsAnError(t *testing.T) {
	t.Parallel()
	broken := errors.New("la base no contesta")
	frame := &routerFrame{edge: "edge-7", found: true}
	s := newSelector(t, &stubStore{getErr: broken}, llmvia.WithFrame(frame))

	edge, ok, err := s.PlazaDe(context.Background(), testTenant, "sess-1")
	if !errors.Is(err, broken) || ok || edge != "" {
		t.Fatalf("plaza = (%q, %v, %v); el fallo de lectura tiene que llegar arriba", edge, ok, err)
	}
	if want := storeReadPrefix + broken.Error(); err.Error() != want {
		t.Errorf("texto = %q, quería %q", err, want)
	}
}
