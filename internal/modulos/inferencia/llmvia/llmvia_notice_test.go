//go:build pendiente

package llmvia_test

// El aviso al dueño visto desde For (REQ-38, R4.5.e): cuándo se escribe y cuándo no. Es la
// parte de los tests de llmvia.go que no cabe en llmvia_test.go (E-13). La tabla completa de
// motivos y la mecánica de la escritura (contexto, log, contador) están en notify_test.go.

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm/tenantllmhelpertest"
)

// requireOneNotice afirma UN aviso escrito y UNA caída contada, con ese origen, esa vía y
// ese motivo.
func requireOneNotice(t *testing.T, book *noticeBook, falls *fallCounter, origin, route string, reason degradation.Reason) {
	t.Helper()
	wantRow := [2]string{string(reason), route}
	if rows := book.written(); len(rows) != 1 || rows[0] != wantRow {
		t.Errorf("avisos escritos = %v, quería exactamente %v", rows, wantRow)
	}
	wantFall := [3]string{origin, route, string(reason)}
	if got := falls.all(); len(got) != 1 || got[0] != wantFall {
		t.Errorf("caídas contadas = %v, quería exactamente %v", got, wantFall)
	}
}

// TestFor_AFailureWhileConsumingTheAdapterNotifiesTheOwner es REQ-38 de punta a punta por
// la vía local: el frame vuelve con error nombrado ⇒ se escribe UNA notificación con su
// motivo y su vía, para ESE tenant, y el error sigue llegando al llamante intacto.
func TestFor_AFailureWhileConsumingTheAdapterNotifiesTheOwner(t *testing.T) {
	t.Parallel()
	failure := &transportError{"breaker_open"}
	book, falls := newNoticeBook(), &fallCounter{}
	s := newSelector(t, rowStore(t, tenantllm.Config{Via: tenantllm.ViaLocal}),
		llmvia.WithFrame(&fakeFrame{err: failure}), book.option(), llmvia.WithDegradacionObservada(falls.count))

	p, err := s.For(context.Background(), testTenant, "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	got := classify(context.Background(), p)
	// El error tiene que seguir trayendo su motivo: el decorador NO lo envuelve.
	var withReason interface{ Motivo() string }
	if got != error(failure) || !errors.As(got, &withReason) || withReason.Motivo() != "breaker_open" {
		t.Fatalf("P1 = %v, quería el error del transporte intacto, con su motivo", got)
	}
	requireOneNotice(t, book, falls, llmvia.OrigenPipeline, tenantllm.ViaLocal, degradation.ReasonBreakerOpen)
	if other := book.rows.Rows("otro-tenant"); len(other) != 0 {
		t.Errorf("el aviso se escribió para otro tenant: %d filas", len(other))
	}
}

// TestFor_SuccessNotifiesNothing: la otra mitad, y la que protege el canal. Si una llamada
// que va bien escribiera fila, la tabla dejaría de significar «el LLM se cayó».
func TestFor_SuccessNotifiesNothing(t *testing.T) {
	t.Parallel()
	book, falls := newNoticeBook(), &fallCounter{}
	s := newSelector(t, tenantllmhelpertest.NewMemoria(),
		llmvia.WithFrame(&fakeFrame{out: validOutput}), book.option(), llmvia.WithDegradacionObservada(falls.count))

	p, err := s.For(context.Background(), testTenant, "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	out, err := p.ClassifyRequest(context.Background(), classifyInput(), llm.Options{})
	if err != nil || string(out) != validOutput {
		t.Fatalf("P1 = (%s, %v), quería la salida del modelo", out, err)
	}
	requireSilence(t, book, falls)
}

// TestFor_QualityDoesNotNotify: el modelo respondió y su salida no valía. Es un fallo, pero
// del MODELO, no de la vía: el llamante reintenta a 0,3 y el dueño no tiene nada que hacer.
// Avisarle le mandaría a reiniciar Ollama por un JSON mal cerrado.
func TestFor_QualityDoesNotNotify(t *testing.T) {
	t.Parallel()
	book, falls := newNoticeBook(), &fallCounter{}
	frame := &fakeFrame{out: "no puedo ayudarte con eso"} // el modelo no devolvió JSON
	s := newSelector(t, tenantllmhelpertest.NewMemoria(),
		llmvia.WithFrame(frame), book.option(), llmvia.WithDegradacionObservada(falls.count))

	p, err := s.For(context.Background(), testTenant, "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if got := classify(context.Background(), p); !errors.Is(got, llm.ErrLLMQuality) {
		t.Fatalf("P1 = %v, quería llm.ErrLLMQuality", got)
	}
	requireSilence(t, book, falls)
}

// TestFor_ABrokenCredentialNotifiesWhileBuilding: el fallo al ARMAR el adaptador de la vía
// api también es un fallo de la vía. Para el dueño, «tu credencial ya no vale» y «tu
// proveedor devolvió 500» son el mismo problema visto en dos momentos.
func TestFor_ABrokenCredentialNotifiesWhileBuilding(t *testing.T) {
	t.Parallel()
	store := &stubStore{
		found:  true,
		row:    tenantllm.Config{TenantID: testTenant, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: "un-modelo"},
		keyErr: tenantllm.ErrNotConfigured,
	}
	frame := &fakeFrame{out: validOutput}
	book, falls := newNoticeBook(), &fallCounter{}
	s := newSelector(t, store, llmvia.WithFrame(frame), book.option(), llmvia.WithDegradacionObservada(falls.count))

	p, err := s.For(context.Background(), testTenant, "")
	if !errors.Is(err, tenantllm.ErrNotConfigured) || p != nil {
		t.Fatalf("For = (%v, %v), quería (nil, tenantllm.ErrNotConfigured)", p, err)
	}
	// 🔴 El texto no repite la clave ni su longitud: acaba en un log.
	if want := "llmvia: credencial del tenant no disponible: " + tenantllm.ErrNotConfigured.Error(); err.Error() != want {
		t.Errorf("texto = %q, quería %q", err, want)
	}
	if store.keyCalls != 1 {
		t.Errorf("la credencial se pidió %d veces, quería 1", store.keyCalls)
	}
	frame.requireUntouched(t)
	requireOneNotice(t, book, falls, llmvia.OrigenSeleccion, tenantllm.ViaAPI, degradation.ReasonCredencial)
}
