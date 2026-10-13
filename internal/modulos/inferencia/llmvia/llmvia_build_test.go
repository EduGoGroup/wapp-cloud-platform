package llmvia_test

// Lo que For le ENTREGA al constructor de cada adaptador (llmvia.go): la fila api completa y
// el orden de las opciones del arranque. Nacen de dos mutantes que el resto de los tests
// dejaba vivos; viven aparte porque llmvia_test.go ya está en su tope de tamaño (E-13).
//
// Sin red (T-16): api.New solo CONSTRUYE; aquí no se llama a ningún método del provider api.

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm/tenantllmhelpertest"
)

// TestFor_ACompleteAPIRowBuildsItsProvider: con el Provider y el Model de la fila y la clave
// del store, el constructor de la vía api ACEPTA. Si For se dejara por el camino cualquiera
// de los tres, api.New lo rechazaría (los demás tests de la vía api solo la ven fallar, y un
// For que no pasara el modelo fallaba «bien» en todos ellos).
func TestFor_ACompleteAPIRowBuildsItsProvider(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{tenantllm.ProviderAnthropic, tenantllm.ProviderGemini} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			store := &stubStore{
				found: true,
				row:   tenantllm.Config{TenantID: testTenant, Via: tenantllm.ViaAPI, Provider: provider, Model: "un-modelo"},
				key:   "clave-falsa-de-test",
			}
			frame := &fakeFrame{out: validOutput}
			book, falls := newNoticeBook(), &fallCounter{}
			s := newSelector(t, store, llmvia.WithFrame(frame), book.option(), llmvia.WithDegradacionObservada(falls.count))

			p, err := s.For(context.Background(), testTenant, "sess-1")
			if err != nil || p == nil {
				t.Fatalf("For = (%v, %v), quería el provider de la vía api: la fila está completa", p, err)
			}
			if _, isLocal := p.(*local.Provider); isLocal {
				t.Fatal("For devolvió el adaptador LOCAL para un tenant en vía api")
			}
			if store.keyCalls != 1 {
				t.Errorf("la credencial se pidió %d veces, quería 1", store.keyCalls)
			}
			frame.requireUntouched(t)
			requireSilence(t, book, falls)
		})
	}
}

// TestNewSelector_AppliesTheOptionsInOrder: las opciones se aplican en el orden en que se
// pasan, y las del adaptador local llegan al provider en ese mismo orden. Se ve con dos que
// se pisan: gana la última, como en cualquier lista de opciones.
func TestNewSelector_AppliesTheOptionsInOrder(t *testing.T) {
	t.Parallel()
	frame := &fakeFrame{out: validOutput}
	s := newSelector(t, tenantllmhelpertest.NewMemoria(),
		llmvia.WithFrame(frame),
		llmvia.WithLocalOptions(local.WithFormat("el-primero")),
		llmvia.WithLocalOptions(local.WithFormat("el-ultimo")),
	)
	p, err := s.For(context.Background(), testTenant, "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if _, err := p.ClassifyRequest(context.Background(), classifyInput(), llm.Options{}); err != nil {
		t.Fatalf("P1: %v", err)
	}
	if got := frame.only(t).req.Format; got != "el-ultimo" {
		t.Errorf("format = %q, quería el de la última opción pasada (el-ultimo)", got)
	}
}
