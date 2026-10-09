package intakeahead_test

// warmup_test.go — el contrato de warmup.go: Warm, el puerto Warmer y sus tres opciones.

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intakeahead"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
)

var _ intakeahead.Warmer = (*fakeWarmer)(nil)

const (
	warmSentMsg    = "calentamiento: emitido contra el Edge"
	warmNotSentMsg = "calentamiento: no se emitió"
)

// warmCall es UN calentamiento que llegó al emisor.
type warmCall struct {
	tenant, session string
	in              llm.ClassifyRequestInput
	remaining       time.Duration
	bounded         bool
	ctx             context.Context
}

// fakeWarmer es el emisor del calentamiento: anota lo que le piden y puede quedarse
// dentro para simular uno en vuelo.
type fakeWarmer struct {
	mu       sync.Mutex
	calls    []warmCall
	inFlight int
	err      error
	// hold, si no es nil, es la barrera en la que Warm espera antes de volver.
	hold *gate
}

func (w *fakeWarmer) Warm(ctx context.Context, tenant, session string, in llm.ClassifyRequestInput) error {
	w.mu.Lock()
	call := warmCall{tenant: tenant, session: session, in: in, ctx: ctx}
	if deadline, ok := ctx.Deadline(); ok {
		call.bounded, call.remaining = true, time.Until(deadline)
	}
	w.calls = append(w.calls, call)
	w.inFlight++
	hold, err := w.hold, w.err
	w.mu.Unlock()

	if hold != nil {
		hold.wait()
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.inFlight--
	return err
}

func (w *fakeWarmer) seen() []warmCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]warmCall(nil), w.calls...)
}

func (w *fakeWarmer) sessions() []string {
	calls := w.seen()
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.session)
	}
	return out
}

func (w *fakeWarmer) flying() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inFlight
}

func (w *fakeWarmer) set(hold *gate, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hold, w.err = hold, err
}

// newWarmBench es el banco con un emisor cableado delante de las opciones del caso.
func newWarmBench(t *testing.T, opts ...intakeahead.Option) (*bench, *fakeWarmer) {
	t.Helper()
	w := &fakeWarmer{}
	return newBench(t, append([]intakeahead.Option{intakeahead.WithWarmer(w)}, opts...)...), w
}

// TestDefaultWarmTimeout: el número que el diseño razona (prefill frío de ~50 s, por
// debajo del techo de 120 s que acepta el Edge).
func TestDefaultWarmTimeout(t *testing.T) {
	if intakeahead.DefaultWarmTimeout != 110*time.Second {
		t.Errorf("DefaultWarmTimeout = %v, quiero 110s", intakeahead.DefaultWarmTimeout)
	}
}

// TestWarm_SendsTheSamePrefixAsARealClassification: el calentamiento solo sirve si deja
// cacheado el prefijo que va a pedir la P1 real. La entrada que recibe el emisor es la
// de una clasificación del mismo tenant, campo a campo, con el texto VACÍO.
func TestWarm_SendsTheSamePrefixAsARealClassification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, w := newWarmBench(t)
		b.start()

		b.pool.Warm(tenantID, "edge-1", sessionID, intentcfg.Kind)
		b.pool.Request(windowKey(), clientText)
		settle()

		warms, calls := w.seen(), b.prov.seen()
		if len(warms) != 1 || len(calls) != 1 {
			t.Fatalf("quiero un calentamiento y una clasificación: %d y %d", len(warms), len(calls))
		}
		got := warms[0]
		if got.tenant != tenantID || got.session != sessionID {
			t.Errorf("calentamiento a (%q, %q), quiero (%q, %q)", got.tenant, got.session, tenantID, sessionID)
		}
		if got.in.Text != "" {
			t.Errorf("el calentamiento no lleva texto de nadie: %q", got.in.Text)
		}
		real := calls[0].in
		real.Text = ""
		if !reflect.DeepEqual(got.in, real) {
			t.Errorf("el prefijo del calentamiento no es el de la P1 real:\n%+v\n%+v", got.in, real)
		}
		if len(got.in.Catalog) != 2 || got.in.UnknownLabel == "" || len(got.in.Vocabulary) != 2 {
			t.Errorf("al prefijo le falta catálogo, etiqueta de desconocido o vocabulario: %+v", got.in)
		}
		if len(b.log.lines("DEBUG", warmSentMsg)) != 1 {
			t.Errorf("quiero el Debug %q:\n%s", warmSentMsg, b.log.String())
		}
	})
}

// TestWarm_DoesNotBlockAndNeedsNoRun: sus llamantes son el bucle Recv del stream y el
// fan-out del PUT de intents. Warm vuelve con el emisor todavía dentro, sin que nadie
// haya llamado a Run y sin selector ni sink.
func TestWarm_DoesNotBlockAndNeedsNoRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hold := &gate{ch: make(chan struct{})}
		defer hold.open()
		w := &fakeWarmer{hold: hold}
		log := &syncBuffer{}
		var pool ahead = newPool(debugLog(log),
			&fakeConfig{blobs: map[string]string{tenantID: publishedCatalog}}, nil, nil,
			intakeahead.WithWarmer(w))

		returned := make(chan struct{})
		go func() {
			defer close(returned)
			pool.Warm(tenantID, "edge-1", sessionID, "")
		}()
		settle()

		select {
		case <-returned:
		default:
			t.Fatal("Warm se quedó esperando al emisor: retiene al bucle Recv del Edge")
		}
		if w.flying() != 1 {
			t.Fatalf("el calentamiento no llegó al emisor sin Run, sin selector y sin sink")
		}
	})
}

// TestWarm_DoesNotUseThePoolWorkers: un calentamiento de ~50 s no le roba un worker a
// las clasificaciones, ni espera a que haya uno libre.
func TestWarm_DoesNotUseThePoolWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, w := newWarmBench(t, intakeahead.WithWorkers(1))
		hold := b.gate()
		w.set(hold, nil)
		b.start()

		// Con el calentamiento dentro, el único worker sigue libre para clasificar.
		b.pool.Warm(tenantID, "edge-1", sessionID, "")
		b.pool.Request(windowKey(), clientText)
		settle()
		if w.flying() != 1 || len(b.sink.seen()) != 1 {
			t.Fatalf("el calentamiento ocupó el worker: en vuelo %d, entregas %d", w.flying(), len(b.sink.seen()))
		}

		// Y con el único worker ocupado, otro Edge se calienta igual.
		b.blockProvider()
		b.pool.Request(windowKey(), clientText)
		b.pool.Warm(tenantID, "edge-2", "s-2", "")
		settle()
		if now, _ := b.prov.flying(); now != 1 || w.flying() != 2 {
			t.Fatalf("el calentamiento esperó a un worker: inferencias %d, calentamientos en vuelo %d", now, w.flying())
		}
	})
}

// TestWarm_OneInFlightPerEdge: el fan-out de un ConfigUpdate sale hacia varias sesiones
// del MISMO Edge, que tiene UNA plaza de Ollama. El cerrojo es por (tenant, Edge), no
// por sesión; y es «uno en vuelo», no «uno para siempre».
func TestWarm_OneInFlightPerEdge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, w := newWarmBench(t)
		b.cfg.set(map[string]string{tenantID: publishedCatalog, "t-other": publishedCatalog}, nil)
		hold := b.gate()
		w.set(hold, nil)

		b.pool.Warm(tenantID, "edge-1", "s-1", intentcfg.Kind)
		settle()
		b.pool.Warm(tenantID, "edge-1", "s-2", intentcfg.Kind)  // mismo Edge: no
		b.pool.Warm(tenantID, "edge-1", "s-3", "")              // mismo Edge: no
		b.pool.Warm(tenantID, "edge-2", "s-4", intentcfg.Kind)  // otro Edge: sí
		b.pool.Warm("t-other", "edge-1", "s-5", intentcfg.Kind) // otro tenant: sí
		settle()

		got := w.sessions()
		if want := map[string]bool{"s-1": true, "s-4": true, "s-5": true}; len(got) != 3 ||
			!want[got[0]] || !want[got[1]] || !want[got[2]] {
			t.Fatalf("calentamientos por las sesiones %v, quiero s-1, s-4 y s-5: uno por Edge", got)
		}
		if b.log.String() != "" {
			t.Errorf("el cerrojo no se loguea:\n%s", b.log.String())
		}

		hold.open()
		settle()
		b.pool.Warm(tenantID, "edge-1", "s-6", intentcfg.Kind)
		settle()
		if got := w.sessions(); len(got) != 4 || got[3] != "s-6" {
			t.Fatalf("terminado el suyo, el Edge vuelve a admitir calentamientos: %v", got)
		}
	})
}

// TestWarm_OnlyWarmsWhatChangesThePrefix: el gateway empuja TRES kinds de config y solo
// `intents` forma el prompt. El kind vacío es el aviso del handshake y SÍ calienta.
//
// Cada caso llama dos veces sobre el MISMO Edge con el emisor retenido: si el kind
// filtrado tomara el cerrojo, el segundo —el bueno— no llegaría.
func TestWarm_OnlyWarmsWhatChangesThePrefix(t *testing.T) {
	cases := []struct {
		kind  string
		warms bool
	}{
		{"", true},
		{"intents", true},
		{"jwks", false},
		{"filters", false},
		{"INTENTS", false},
	}
	for _, tc := range cases {
		t.Run("kind="+tc.kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b, w := newWarmBench(t)
				w.set(b.gate(), nil)

				b.pool.Warm(tenantID, "edge-1", "s-case", tc.kind)
				settle()
				b.pool.Warm(tenantID, "edge-1", "s-control", intentcfg.Kind)
				settle()

				want := []string{"s-control"}
				if tc.warms {
					want = []string{"s-case"}
				}
				if got := w.sessions(); !reflect.DeepEqual(got, want) {
					t.Fatalf("kind %q: calentamientos por %v, quiero %v", tc.kind, got, want)
				}
				if !tc.warms && len(b.cfg.seen()) != 1 {
					t.Errorf("un kind que no toca el prompt ni siquiera lee el catálogo: %v", b.cfg.seen())
				}
			})
		})
	}
}

// TestWithWarmup: el interruptor de campo. ENCENDIDO por defecto —un interruptor que hay
// que acordarse de encender acaba apagado en producción— y apagable sin recompilar, que
// es lo que exige el A/B en la misma tanda.
func TestWithWarmup(t *testing.T) {
	cases := []struct {
		name  string
		opts  []intakeahead.Option
		warms bool
	}{
		{"on by default", nil, true},
		{"explicitly on", []intakeahead.Option{intakeahead.WithWarmup(true)}, true},
		{"off", []intakeahead.Option{intakeahead.WithWarmup(false)}, false},
		{"last one wins, off", []intakeahead.Option{intakeahead.WithWarmup(true), intakeahead.WithWarmup(false)}, false},
		{"last one wins, on", []intakeahead.Option{intakeahead.WithWarmup(false), intakeahead.WithWarmup(true)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b, w := newWarmBench(t, tc.opts...)

				b.pool.Warm(tenantID, "edge-1", sessionID, intentcfg.Kind)
				settle()

				if got := len(w.seen()); (got == 1) != tc.warms || got > 1 {
					t.Fatalf("calentamientos = %d, ¿debía calentar? %v", got, tc.warms)
				}
				if !tc.warms && (len(b.cfg.seen()) != 0 || b.log.String() != "") {
					t.Errorf("apagado, Warm no lee el catálogo ni loguea")
				}
			})
		})
	}
}

// TestWarm_DoesNothing: los estados en los que no hay calentamiento posible, y ninguno
// es un fallo: ni se lee el catálogo, ni se emite, ni se loguea.
func TestWarm_DoesNothing(t *testing.T) {
	type args struct{ tenant, session string }
	valid := args{tenantID, sessionID}
	cases := []struct {
		name  string
		build func(log *syncBuffer, cfg *fakeConfig, w *fakeWarmer) *intakeahead.Pool
		args  args
	}{
		{"nil pool", func(*syncBuffer, *fakeConfig, *fakeWarmer) *intakeahead.Pool { return nil }, valid},
		{"no warmer", func(log *syncBuffer, cfg *fakeConfig, _ *fakeWarmer) *intakeahead.Pool {
			return intakeahead.New(debugLog(log), cfg, &fakeSelector{}, &fakeSink{})
		}, valid},
		{"nil warmer", func(log *syncBuffer, cfg *fakeConfig, _ *fakeWarmer) *intakeahead.Pool {
			return intakeahead.New(debugLog(log), cfg, &fakeSelector{}, &fakeSink{}, intakeahead.WithWarmer(nil))
		}, valid},
		{"no config store", func(log *syncBuffer, _ *fakeConfig, w *fakeWarmer) *intakeahead.Pool {
			return intakeahead.New(debugLog(log), nil, &fakeSelector{}, &fakeSink{}, intakeahead.WithWarmer(w))
		}, valid},
		{"empty tenant", func(log *syncBuffer, cfg *fakeConfig, w *fakeWarmer) *intakeahead.Pool {
			return intakeahead.New(debugLog(log), cfg, &fakeSelector{}, &fakeSink{}, intakeahead.WithWarmer(w))
		}, args{"", sessionID}},
		{"empty session", func(log *syncBuffer, cfg *fakeConfig, w *fakeWarmer) *intakeahead.Pool {
			return intakeahead.New(debugLog(log), cfg, &fakeSelector{}, &fakeSink{}, intakeahead.WithWarmer(w))
		}, args{tenantID, ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				log, w := &syncBuffer{}, &fakeWarmer{}
				// El catálogo existe también para el tenant vacío: si la guarda faltara, el
				// calentamiento llegaría hasta el emisor.
				cfg := &fakeConfig{blobs: map[string]string{tenantID: publishedCatalog, "": publishedCatalog}}
				pool := tc.build(log, cfg, w)

				pool.Warm(tc.args.tenant, "edge-1", tc.args.session, intentcfg.Kind)
				settle()

				if len(w.seen()) != 0 || len(cfg.seen()) != 0 || log.String() != "" {
					t.Fatalf("Warm hizo algo sin nada que calentar: emisor %d, lecturas %d, log:\n%s",
						len(w.seen()), len(cfg.seen()), log.String())
				}
			})
		})
	}
}

// TestWarm_AcceptsAnEmptyEdgeID: el Edge vacío es una clave como otra cualquiera.
func TestWarm_AcceptsAnEmptyEdgeID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, w := newWarmBench(t)
		b.pool.Warm(tenantID, "", sessionID, "")
		settle()
		if len(w.seen()) != 1 {
			t.Fatalf("calentamientos = %d, quiero 1", len(w.seen()))
		}
	})
}

// TestWarm_WithoutAUsableCatalogSendsNothing: sin catálogo no hay prefijo que calentar
// —estado normal, en silencio—; los fallos de lectura y de validación se dicen por Warn
// con el rótulo del calentamiento. En los tres casos el Edge queda libre.
func TestWarm_WithoutAUsableCatalogSendsNothing(t *testing.T) {
	cases := []struct {
		name    string
		blobs   map[string]string
		err     error
		wantMsg string
	}{
		{"tenant without a catalog", nil, nil, ""},
		{"read failure", nil, errors.New("la base no responde"),
			"calentamiento: no se pudo leer el catálogo de intenciones del tenant"},
		{"catalog that does not validate", map[string]string{tenantID: `{"version":"","intents":[]}`}, nil,
			"calentamiento: el catálogo publicado del tenant no valida; no se pide inferencia"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b, w := newWarmBench(t)
				b.cfg.set(tc.blobs, tc.err)

				b.pool.Warm(tenantID, "edge-1", sessionID, intentcfg.Kind)
				settle()

				if len(b.cfg.seen()) != 1 || len(w.seen()) != 0 {
					t.Fatalf("lecturas %d y emisiones %d, quiero 1 y 0", len(b.cfg.seen()), len(w.seen()))
				}
				assertOnlyLog(t, b.log, "WARN", tc.wantMsg, "tenant_id="+tenantID)

				b.cfg.set(map[string]string{tenantID: publishedCatalog}, nil)
				b.pool.Warm(tenantID, "edge-1", sessionID, intentcfg.Kind)
				settle()
				if len(w.seen()) != 1 {
					t.Errorf("tras el intento fallido el Edge debe admitir otro calentamiento")
				}
			})
		})
	}
}

// TestWarm_TheWarmerErrorGoesNowhere: Warm no devuelve error y no tumba a nadie. Lo que
// se pierde es el precalentado; se dice por DEBUG —no por Warn: en vía API «no se
// emitió» es la respuesta correcta— y el Edge queda libre.
func TestWarm_TheWarmerErrorGoesNowhere(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, w := newWarmBench(t)
		w.set(nil, errors.New("la vía del tenant no admite calentamiento"))

		b.pool.Warm(tenantID, "edge-1", sessionID, intentcfg.Kind)
		settle()

		assertOnlyLog(t, b.log, "DEBUG", warmNotSentMsg,
			"tenant_id="+tenantID, "session_id="+sessionID, "la vía del tenant no admite calentamiento")

		w.set(nil, nil)
		b.pool.Warm(tenantID, "edge-1", sessionID, intentcfg.Kind)
		settle()
		if len(w.seen()) != 2 || len(b.log.lines("DEBUG", warmSentMsg)) != 1 {
			t.Errorf("tras el error el Edge debe admitir otro calentamiento, que sale bien:\n%s", b.log.String())
		}
	})
}

// TestWarm_HasItsOwnBudget: el reloj del calentamiento es el suyo y nada más. No es el
// presupuesto de una clasificación, y no cuelga del ctx de Run: cancelarlo no corta un
// calentamiento en vuelo (consecuencia aceptada).
func TestWarm_HasItsOwnBudget(t *testing.T) {
	cases := []struct {
		name string
		opts []intakeahead.Option
		want time.Duration
	}{
		{"default", nil, intakeahead.DefaultWarmTimeout},
		{"five seconds", []intakeahead.Option{intakeahead.WithWarmTimeout(5 * time.Second)}, 5 * time.Second},
		// Lo que se ignora es <= 0, no «lo pequeño»: un nanosegundo es un presupuesto.
		{"one nanosecond", []intakeahead.Option{intakeahead.WithWarmTimeout(time.Nanosecond)}, time.Nanosecond},
		{"zero is ignored", []intakeahead.Option{intakeahead.WithWarmTimeout(0)}, intakeahead.DefaultWarmTimeout},
		{"negative is ignored", []intakeahead.Option{intakeahead.WithWarmTimeout(-time.Second)}, intakeahead.DefaultWarmTimeout},
		{"ignored value keeps the previous one",
			[]intakeahead.Option{intakeahead.WithWarmTimeout(time.Minute), intakeahead.WithWarmTimeout(0)}, time.Minute},
		{"the request budget is another clock",
			[]intakeahead.Option{intakeahead.WithTimeout(3 * time.Second)}, intakeahead.DefaultWarmTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b, w := newWarmBench(t, tc.opts...)
				w.set(b.gate(), nil)
				b.start()

				b.pool.Warm(tenantID, "edge-1", sessionID, intentcfg.Kind)
				settle()

				calls := w.seen()
				if len(calls) != 1 || !calls[0].bounded || calls[0].remaining != tc.want {
					t.Fatalf("al emisor le quedaban %+v, quiero %v", calls, tc.want)
				}
				if bounded := b.cfg.seenBounded(); len(bounded) != 1 || !bounded[0] {
					t.Errorf("la lectura del catálogo va dentro del presupuesto del calentamiento")
				}

				b.stop()
				if err := calls[0].ctx.Err(); err != nil {
					t.Errorf("cancelar el ctx de Run cortó el calentamiento en vuelo: %v", err)
				}
				time.Sleep(tc.want - time.Nanosecond) // reloj de la burbuja
				if err := calls[0].ctx.Err(); err != nil {
					t.Errorf("el ctx del calentamiento venció antes de agotar su presupuesto: %v", err)
				}
				time.Sleep(2 * time.Nanosecond) // cruza el deadline
				settle()
				if err := calls[0].ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("agotado su presupuesto, el ctx del calentamiento debe vencer: %v", err)
				}
			})
		})
	}
}
