package llmvia_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/degradation"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// ---------------------------------------------------------------- dobles de test

type storeFake struct {
	fila      tenantllm.Config
	hay       bool
	clave     string
	errGet    error
	errClave  error
	pedidasLa int // cuántas veces se pidió la CREDENCIAL
}

func (s *storeFake) Get(context.Context, string) (tenantllm.Config, bool, error) {
	return s.fila, s.hay, s.errGet
}

func (s *storeFake) APIKey(context.Context, string) (string, error) {
	s.pedidasLa++
	if s.errClave != nil {
		return "", s.errClave
	}
	return s.clave, nil
}

type avisoFake struct {
	reason degradation.Reason
	via    string
	tenant string
	n      int
}

func (a *avisoFake) Record(_ context.Context, tenantID string, r degradation.Reason, via string, _ time.Time) (bool, error) {
	a.n++
	a.tenant, a.reason, a.via = tenantID, r, via
	return true, nil
}

// frameFake es el transporte de la vía local: devuelve lo que se le diga.
type frameFake struct {
	out string
	err error
}

func (f *frameFake) Infer(context.Context, string, gatewaygrpc.InferRequest) (string, error) {
	return f.out, f.err
}

// transporteError finge el error del transporte con su motivo, igual que el gateway.
type transporteError struct{ m string }

func (e transporteError) Error() string  { return "transporte: " + e.m }
func (e transporteError) Motivo() string { return e.m }

func selector(t *testing.T, store llmvia.Store, opts ...llmvia.SelectorOption) *llmvia.Selector {
	t.Helper()
	s, err := llmvia.NewSelector(store, logger.New(logger.WithWriter(io.Discard)), opts...)
	if err != nil {
		t.Fatalf("NewSelector: %v", err)
	}
	return s
}

func entrada() llm.ClassifyRequestInput {
	return llm.ClassifyRequestInput{
		Text:    "quiero tres pizzas",
		Catalog: []llm.IntentSpec{{Name: "intake_request", Description: "pide productos"}},
	}
}

// ---------------------------------------------------------------- la selección

// TestFor_LaViaDecideElAdaptador: los tres estados de la fila y a qué adaptador
// llevan. La afirmación se hace por una CONSECUENCIA OBSERVABLE de cada vía y no por
// el tipo devuelto: la vía local usa el frame (y el fake lo registra), la vía API
// pide la credencial (y el fake lo cuenta). Preguntar por el tipo concreto ataría el
// test a una decisión de implementación que el puerto existe para esconder.
func TestFor_LaViaDecideElAdaptador(t *testing.T) {
	t.Parallel()

	t.Run("sin fila ⇒ vía LOCAL, que es el default del producto (REQ-33)", func(t *testing.T) {
		t.Parallel()
		store := &storeFake{hay: false}
		f := &frameFake{out: `{"version":1,"intent":"intake_request","confidence":0.9,"evidence":"pizzas"}`}
		p, err := selector(t, store, llmvia.WithFrame(f)).For(context.Background(), "t1", "")
		if err != nil {
			t.Fatalf("For: %v", err)
		}
		if _, err := p.ClassifyRequest(context.Background(), entrada(), llm.Options{}); err != nil {
			t.Fatalf("ClassifyRequest: %v", err)
		}
		// 🔴 Y NO SE PIDIÓ LA CREDENCIAL. No es cosmético: la vía local no llama a
		// ningún tercero, así que descifrar una clave para ella sería sacar del sobre
		// un secreto que nadie va a usar.
		if store.pedidasLa != 0 {
			t.Fatalf("la vía local pidió la credencial %d veces", store.pedidasLa)
		}
	})

	t.Run("fila con via=local ⇒ vía LOCAL", func(t *testing.T) {
		t.Parallel()
		store := &storeFake{hay: true, fila: tenantllm.Config{TenantID: "t1", Via: tenantllm.ViaLocal}}
		f := &frameFake{out: `{"version":1,"intent":"intake_request","confidence":0.9,"evidence":"pizzas"}`}
		p, err := selector(t, store, llmvia.WithFrame(f)).For(context.Background(), "t1", "")
		if err != nil {
			t.Fatalf("For: %v", err)
		}
		if _, err := p.ClassifyRequest(context.Background(), entrada(), llm.Options{}); err != nil {
			t.Fatalf("ClassifyRequest: %v", err)
		}
		if store.pedidasLa != 0 {
			t.Fatalf("la vía local pidió la credencial %d veces", store.pedidasLa)
		}
	})

	t.Run("fila con via=api ⇒ vía API, y AHÍ sí se pide la credencial", func(t *testing.T) {
		t.Parallel()
		store := &storeFake{
			hay:   true,
			fila:  tenantllm.Config{TenantID: "t1", Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: "claude-x"},
			clave: "sk-ant-de-prueba-larga",
		}
		if _, err := selector(t, store).For(context.Background(), "t1", ""); err != nil {
			t.Fatalf("For: %v", err)
		}
		if store.pedidasLa != 1 {
			t.Fatalf("la vía API pidió la credencial %d veces, quiero 1", store.pedidasLa)
		}
	})
}

// TestFor_UnaViaDesconocidaNO_ELIGE_POR_TI: si la columna trae un valor fuera del
// vocabulario, se devuelve error. Adivinar la vía de un tenant es exactamente lo que
// REQ-33 prohíbe: «mientras un tenant tenga una vía configurada, el sistema jamás
// deberá usar la otra».
//
// 🔬 MUTACIÓN: cambiar el `default` del switch por `prov = local.New(...)» —el
// «arreglo» tentador, «si no sé, tira de local que es gratis»— ⇒ rojo aquí.
func TestFor_UnaViaDesconocidaNO_ELIGE_POR_TI(t *testing.T) {
	t.Parallel()
	store := &storeFake{hay: true, fila: tenantllm.Config{TenantID: "t1", Via: "vertex"}}
	_, err := selector(t, store, llmvia.WithFrame(&frameFake{})).For(context.Background(), "t1", "")
	if !errors.Is(err, llmvia.ErrViaDesconocida) {
		t.Fatalf("quiero ErrViaDesconocida, llegó: %v", err)
	}
}

// TestFor_ElFalloDeLaBaseNoSeConfundeConUnaVia: un SELECT que falla no es «este
// tenant está en local». Tratarlo así mandaría al Edge del cliente el trabajo de
// todos los tenants cuya fila no se pudo leer.
func TestFor_ElFalloDeLaBaseNoSeConfundeConUnaVia(t *testing.T) {
	t.Parallel()
	boom := errors.New("la base no está")
	store := &storeFake{errGet: boom}
	_, err := selector(t, store, llmvia.WithFrame(&frameFake{})).For(context.Background(), "t1", "")
	if !errors.Is(err, boom) {
		t.Fatalf("quiero que propague el error de la base, llegó: %v", err)
	}
}

// ---------------------------------------------------------------- la degradación

// TestElFalloAlConsumirElAdaptadorAVISA_AlDueño es REQ-38 de punta a punta por la
// vía local: el frame vuelve con error nombrado ⇒ se escribe UNA notificación con su
// motivo y su vía, y el error sigue llegando al llamante intacto.
func TestElFalloAlConsumirElAdaptadorAVISA_AlDueño(t *testing.T) {
	t.Parallel()
	store := &storeFake{hay: true, fila: tenantllm.Config{TenantID: "t1", Via: tenantllm.ViaLocal}}
	avisos := &avisoFake{}
	f := &frameFake{err: transporteError{"breaker_open"}}

	p, err := selector(t, store, llmvia.WithFrame(f), llmvia.WithNotifier(avisos)).For(context.Background(), "t1", "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	_, err = p.ClassifyRequest(context.Background(), entrada(), llm.Options{})
	if err == nil {
		t.Fatal("quiero que el error del adaptador llegue al llamante")
	}
	if avisos.n != 1 {
		t.Fatalf("avisos = %d, quiero 1", avisos.n)
	}
	if avisos.reason != degradation.ReasonBreakerOpen {
		t.Fatalf("reason = %q, quiero %q", avisos.reason, degradation.ReasonBreakerOpen)
	}
	if avisos.via != tenantllm.ViaLocal {
		t.Fatalf("via = %q, quiero %q", avisos.via, tenantllm.ViaLocal)
	}
	if avisos.tenant != "t1" {
		t.Fatalf("tenant = %q, quiero t1", avisos.tenant)
	}
	// El error tiene que seguir trayendo su motivo: el decorador NO lo envuelve.
	var m interface{ Motivo() string }
	if !errors.As(err, &m) || m.Motivo() != "breaker_open" {
		t.Fatalf("el decorador alteró el error: %v", err)
	}
}

// TestElExitoNoAVISA_NADA: la otra mitad, y la que protege el canal. Si una llamada
// que va bien escribiera fila, la tabla dejaría de significar «el LLM se cayó».
func TestElExitoNoAVISA_NADA(t *testing.T) {
	t.Parallel()
	store := &storeFake{hay: true, fila: tenantllm.Config{TenantID: "t1", Via: tenantllm.ViaLocal}}
	avisos := &avisoFake{}
	f := &frameFake{out: `{"version":1,"intent":"intake_request","confidence":0.9,"evidence":"pizzas"}`}

	p, err := selector(t, store, llmvia.WithFrame(f), llmvia.WithNotifier(avisos)).For(context.Background(), "t1", "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if _, err := p.ClassifyRequest(context.Background(), entrada(), llm.Options{}); err != nil {
		t.Fatalf("ClassifyRequest: %v", err)
	}
	if avisos.n != 0 {
		t.Fatalf("avisos = %d, quiero 0: el funcionamiento correcto NO se notifica", avisos.n)
	}
}

// TestLaCalidadNoAVISA: el modelo respondió y su salida no valía. Es un fallo, pero
// del MODELO, no de la vía: el caller reintenta a 0.3 y el dueño no tiene nada que
// hacer. Avisarle le mandaría a reiniciar Ollama por un JSON mal cerrado.
//
// 🔬 MUTACIÓN: quitar la rama de llm.ErrLLMQuality de motivoDe ⇒ este caso caería en
// `default` y seguiría sin avisar... salvo que el error venga envuelto por un
// transporte con motivo. Por eso la rama va la PRIMERA y por eso este test existe.
func TestLaCalidadNoAVISA(t *testing.T) {
	t.Parallel()
	store := &storeFake{hay: true, fila: tenantllm.Config{TenantID: "t1", Via: tenantllm.ViaLocal}}
	avisos := &avisoFake{}
	f := &frameFake{out: "no puedo ayudarte con eso"} // el modelo no devolvió JSON

	p, err := selector(t, store, llmvia.WithFrame(f), llmvia.WithNotifier(avisos)).For(context.Background(), "t1", "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if _, err := p.ClassifyRequest(context.Background(), entrada(), llm.Options{}); !errors.Is(err, llm.ErrLLMQuality) {
		t.Fatalf("quiero ErrLLMQuality, llegó: %v", err)
	}
	if avisos.n != 0 {
		t.Fatalf("avisos = %d, quiero 0: la calidad de la salida no es una degradación", avisos.n)
	}
}

// TestLaCredencialRotaAVISA_AlCONSTRUIR: el fallo al ARMAR el adaptador de la vía API
// también es un fallo de la vía. Para el dueño, «tu credencial ya no vale» y «tu
// proveedor devolvió 500» son el mismo problema visto en dos momentos.
func TestLaCredencialRotaAVISA_AlCONSTRUIR(t *testing.T) {
	t.Parallel()
	store := &storeFake{
		hay:      true,
		fila:     tenantllm.Config{TenantID: "t1", Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: "claude-x"},
		errClave: tenantllm.ErrNotConfigured,
	}
	avisos := &avisoFake{}

	_, err := selector(t, store, llmvia.WithNotifier(avisos)).For(context.Background(), "t1", "")
	if err == nil {
		t.Fatal("quiero error al construir la vía API sin credencial")
	}
	if avisos.n != 1 || avisos.reason != degradation.ReasonCredencial {
		t.Fatalf("avisos = %d, reason = %q; quiero 1 y %q", avisos.n, avisos.reason, degradation.ReasonCredencial)
	}
	if avisos.via != tenantllm.ViaAPI {
		t.Fatalf("via = %q, quiero %q", avisos.via, tenantllm.ViaAPI)
	}
}

// TestElAvisoSobreviveAlContextoMUERTO es el caso que se pierde solo. Cuando el
// llamante se rinde —la ventana se cerró, el proceso se apaga— el ctx llega aquí YA
// CANCELADO. Sin desacoplarlo (context.WithoutCancel), la escritura del aviso
// fallaría exactamente en los casos en los que hay algo que contar.
//
// 🔬 MUTACIÓN: quitar el context.WithoutCancel de Selector.avisar ⇒ el Record
// recibiría un ctx cancelado; el fake de aquí lo detecta y el test se pone rojo.
func TestElAvisoSobreviveAlContextoMUERTO(t *testing.T) {
	t.Parallel()
	store := &storeFake{hay: true, fila: tenantllm.Config{TenantID: "t1", Via: tenantllm.ViaLocal}}
	avisos := &ctxSpy{}
	f := &frameFake{err: transporteError{"timeout"}}

	p, err := selector(t, store, llmvia.WithFrame(f), llmvia.WithNotifier(avisos)).For(context.Background(), "t1", "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // el llamante ya se rindió
	if _, err := p.ClassifyRequest(ctx, entrada(), llm.Options{}); err == nil {
		t.Fatal("quiero el error del adaptador")
	}

	if avisos.n != 1 {
		t.Fatalf("avisos = %d, quiero 1", avisos.n)
	}
	if avisos.ctxMuerto {
		t.Fatal("el aviso se escribió con el contexto del llamante ya cancelado: se perdería justo cuando hace falta")
	}
}

// ctxSpy mira el ESTADO del contexto con el que se le llama.
type ctxSpy struct {
	n         int
	ctxMuerto bool
}

func (c *ctxSpy) Record(ctx context.Context, _ string, _ degradation.Reason, _ string, _ time.Time) (bool, error) {
	c.n++
	c.ctxMuerto = ctx.Err() != nil
	return true, nil
}

// TestSinNotificadorNoSeEnvuelve: una envoltura que no hace nada solo añade un marco
// en los stack traces. Y, más importante: el sistema tiene que degradar igual sin
// notificador cableado (la conducta de Nivel A no depende de esto), así que la
// llamada tiene que funcionar y el error llegar intacto.
func TestSinNotificadorNoSeEnvuelve(t *testing.T) {
	t.Parallel()
	store := &storeFake{hay: true, fila: tenantllm.Config{TenantID: "t1", Via: tenantllm.ViaLocal}}
	f := &frameFake{err: transporteError{"ollama_down"}}

	p, err := selector(t, store, llmvia.WithFrame(f)).For(context.Background(), "t1", "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if _, err := p.ClassifyRequest(context.Background(), entrada(), llm.Options{}); err == nil {
		t.Fatal("quiero el error del adaptador")
	}
}

// TestNewSelectorExigeElStore: fallo de arranque, no de llamada.
func TestNewSelectorExigeElStore(t *testing.T) {
	t.Parallel()
	if _, err := llmvia.NewSelector(nil, logger.New(logger.WithWriter(io.Discard))); !errors.Is(err, llmvia.ErrSinConfig) {
		t.Fatalf("quiero ErrSinConfig, llegó: %v", err)
	}
}
