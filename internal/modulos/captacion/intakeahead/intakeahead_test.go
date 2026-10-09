//go:build pendiente

package intakeahead_test

// intakeahead_test.go — el contrato de intakeahead.go: New, Request, Run, las opciones y
// SinkFunc. Los workers de Run —su techo, su ctx y su presupuesto— están en
// intakeahead_run_test.go; lo que un worker hace con una petición —el prompt, los
// fallos, el reintento—, en intakeahead_classify_test.go; el saneo contra el texto, en
// intakeahead_evidence_test.go. Los dobles y el banco, en doubles_test.go.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intakeahead"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
)

// Los puertos, vistos desde quien los satisface. Los dos almacenes de `intentcfg` son
// los que el contrato de ConfigStore nombra.
var (
	_ intakeahead.ProviderSelector = (*fakeSelector)(nil)
	_ intakeahead.ConfigStore      = (*fakeConfig)(nil)
	_ intakeahead.ConfigStore      = (*intentcfg.MemoryStore)(nil)
	_ intakeahead.ConfigStore      = (*intentcfg.PostgresStore)(nil)
	_ intakeahead.Sink             = (*fakeSink)(nil)
	_ intakeahead.Sink             = intakeahead.SinkFunc(nil)
)

const queueFullMsg = "adelanto: cola de clasificación llena; la ventana cerrará por su reloj"

// TestDefaults: los tres números del contrato. Su efecto se prueba más abajo; aquí se
// fija el valor, que es lo que el arranque y el diseño citan.
func TestDefaults(t *testing.T) {
	if intakeahead.DefaultWorkers != 4 {
		t.Errorf("DefaultWorkers = %d, quiero 4", intakeahead.DefaultWorkers)
	}
	if intakeahead.DefaultQueue != 64 {
		t.Errorf("DefaultQueue = %d, quiero 64", intakeahead.DefaultQueue)
	}
	if intakeahead.DefaultTimeout != 45*time.Second {
		t.Errorf("DefaultTimeout = %v, quiero 45s", intakeahead.DefaultTimeout)
	}
}

// TestRequest_ClassifiesAndDeliversToTheSink: el camino entero. Llega un texto, se pide
// P1 por la vía del tenant y la clasificación llega al sink con SU ventana, el nombre y
// la confianza en crudo, una sola vez.
func TestRequest_ClassifiesAndDeliversToTheSink(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t)
		b.start()

		b.pool.Request(windowKey(), clientText)
		settle()

		got := b.sink.seen()
		if len(got) != 1 {
			t.Fatalf("entregas al sink = %d, quiero 1: %+v", len(got), got)
		}
		want := delivery{key: windowKey(), intent: "intake_request", confidence: 0.91}
		if got[0] != want {
			t.Errorf("entrega = %+v, quiero %+v", got[0], want)
		}
		asks := b.sel.seen()
		if len(asks) != 1 || asks[0].tenant != tenantID || asks[0].session != sessionID {
			t.Errorf("el proveedor se pide UNA vez con (tenant, sesión de la ventana): %+v", asks)
		}
		calls := b.prov.seen()
		if len(calls) != 1 || calls[0].temperature != llm.TemperatureGreedy {
			t.Errorf("quiero UNA inferencia y a TemperatureGreedy: %+v", calls)
		}
		if b.log.String() != "" {
			t.Errorf("el camino feliz no loguea nada:\n%s", b.log.String())
		}
	})
}

// TestRequest_DoesNothing: sin texto o con una clave incompleta no hay nada que
// preguntar. No se encola, no se lee el catálogo y no se loguea; y el pool sigue
// sirviendo.
func TestRequest_DoesNothing(t *testing.T) {
	withoutField := func(clear func(*intake.WindowKey)) intake.WindowKey {
		k := windowKey()
		clear(&k)
		return k
	}
	cases := []struct {
		name string
		key  intake.WindowKey
		text string
	}{
		{"empty text", windowKey(), ""},
		{"key without tenant", withoutField(func(k *intake.WindowKey) { k.TenantID = "" }), clientText},
		{"key without session", withoutField(func(k *intake.WindowKey) { k.SessionID = "" }), clientText},
		{"key without contact", withoutField(func(k *intake.WindowKey) { k.ContactID = "" }), clientText},
		{"key without event", withoutField(func(k *intake.WindowKey) { k.EventID = "" }), clientText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newBench(t)
				b.start()

				b.pool.Request(tc.key, tc.text)
				settle()

				if asked := b.cfg.seen(); len(asked) != 0 {
					t.Errorf("se leyó el catálogo %d veces; no había nada que preguntar", len(asked))
				}
				if len(b.sel.seen()) != 0 || len(b.prov.seen()) != 0 || len(b.sink.seen()) != 0 {
					t.Errorf("se pidió proveedor, inferencia o se entregó algo sin nada que clasificar")
				}
				if b.log.String() != "" {
					t.Errorf("no se loguea (motivo sano):\n%s", b.log.String())
				}

				b.pool.Request(windowKey(), clientText)
				settle()
				if len(b.sink.seen()) != 1 {
					t.Errorf("tras ignorar una petición el pool debe seguir sirviendo")
				}
			})
		})
	}
}

// TestPool_HalfWiredIsASafeNoOp: un arranque parcial no puede tumbar el turno de nadie.
// Sin log, catálogo, selector o sink —o con el pool nil— Request descarta y Run vuelve
// en el acto.
func TestPool_HalfWiredIsASafeNoOp(t *testing.T) {
	type deps struct {
		cfg  *fakeConfig
		sel  *fakeSelector
		sink *fakeSink
	}
	cases := []struct {
		name  string
		build func(log *syncBuffer, d deps) *intakeahead.Pool
	}{
		{"nil pool", func(*syncBuffer, deps) *intakeahead.Pool { return nil }},
		{"no log", func(_ *syncBuffer, d deps) *intakeahead.Pool {
			return intakeahead.New(nil, d.cfg, d.sel, d.sink)
		}},
		{"no config store", func(log *syncBuffer, d deps) *intakeahead.Pool {
			return intakeahead.New(debugLog(log), nil, d.sel, d.sink)
		}},
		{"no selector", func(log *syncBuffer, d deps) *intakeahead.Pool {
			return intakeahead.New(debugLog(log), d.cfg, nil, d.sink)
		}},
		{"no sink", func(log *syncBuffer, d deps) *intakeahead.Pool {
			return intakeahead.New(debugLog(log), d.cfg, d.sel, nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				log := &syncBuffer{}
				d := deps{
					cfg:  &fakeConfig{blobs: map[string]string{tenantID: publishedCatalog}},
					sel:  &fakeSelector{provider: &fakeProvider{replies: []reply{{raw: goodArtifact()}}}},
					sink: &fakeSink{},
				}
				pool := tc.build(log, d)
				if tc.name != "nil pool" && pool == nil {
					t.Fatal("New no devuelve nil nunca")
				}

				// Más peticiones de las que caben en la cola: ni se encolan ni bloquean.
				for i := range intakeahead.DefaultQueue + 2 {
					pool.Request(windowOf(fmt.Sprintf("c-%d", i)), clientText)
				}

				returned := make(chan struct{})
				go func() {
					defer close(returned)
					pool.Run(t.Context())
				}()
				settle()
				select {
				case <-returned:
				default:
					t.Error("Run de un pool a medio cablear debe volver en el acto")
				}
				if len(d.cfg.seen()) != 0 || len(d.sel.seen()) != 0 || len(d.sink.seen()) != 0 {
					t.Error("un pool a medio cablear no toca ninguna de sus piezas")
				}
				if log.String() != "" {
					t.Errorf("un pool a medio cablear no loguea (ni «cola llena»):\n%s", log.String())
				}
			})
		})
	}
}

// TestRequest_OneInFlightPerWindow: 50 mensajes de UNA ventana no son 50 inferencias. Y
// las dos mitades del cerrojo: mientras la primera corre nadie encola; cuando termina,
// el siguiente mensaje vuelve a preguntar («una EN VUELO», no «una por ventana»).
func TestRequest_OneInFlightPerWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t)
		b.prov.setReplies(reply{raw: artifact("consulta", 0.5, "hola", nil)}, reply{raw: goodArtifact()})
		g := b.blockProvider()
		b.start()

		b.pool.Request(windowKey(), "hola")
		settle()
		for i := range 49 {
			b.pool.Request(windowKey(), fmt.Sprintf("mensaje %d de la ráfaga", i))
		}
		settle()
		if calls := b.prov.seen(); len(calls) != 1 || calls[0].in.Text != "hola" {
			t.Fatalf("con una petición en vuelo, los 49 siguientes de la MISMA ventana no piden nada: %d inferencias", len(calls))
		}

		g.open()
		settle()
		// Nada de la ráfaga quedó encolado: al soltar la primera no corre ninguna más.
		if calls := b.prov.seen(); len(calls) != 1 {
			t.Fatalf("la ráfaga dejó peticiones en la cola: %d inferencias", len(calls))
		}
		if b.log.String() != "" {
			t.Errorf("el cerrojo no se loguea (ocurre 49 veces por ráfaga):\n%s", b.log.String())
		}

		b.pool.Request(windowKey(), clientText)
		settle()
		calls := b.prov.seen()
		if len(calls) != 2 || calls[1].in.Text != clientText {
			t.Fatalf("terminada la primera, el siguiente mensaje de la ventana vuelve a preguntar: %d inferencias", len(calls))
		}
		if len(b.sink.seen()) != 2 {
			t.Errorf("entregas = %d, quiero 2", len(b.sink.seen()))
		}
	})
}

// TestRequest_QueuedRequestHoldsTheWindow: «viva» es encolada O corriendo. Sin workers,
// la segunda petición de la ventana tampoco entra.
func TestRequest_QueuedRequestHoldsTheWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t)

		b.pool.Request(windowKey(), "primero")
		b.pool.Request(windowKey(), "segundo")
		b.start()
		settle()

		calls := b.prov.seen()
		if len(calls) != 1 || calls[0].in.Text != "primero" {
			t.Fatalf("una petición encolada ya toma el cerrojo de su ventana: %d inferencias", len(calls))
		}
	})
}

// TestRequest_WindowsDoNotWaitForEachOther: el cerrojo es por clave COMPLETA. Cambiar
// cualquiera de las cuatro columnas es otra ventana, y pregunta mientras la primera
// sigue en vuelo.
func TestRequest_WindowsDoNotWaitForEachOther(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t, intakeahead.WithWorkers(8))
		b.cfg.set(map[string]string{tenantID: publishedCatalog, "t-other": publishedCatalog}, nil)
		b.blockProvider()
		b.start()

		keys := []intake.WindowKey{windowKey(), windowKey(), windowKey(), windowKey(), windowKey()}
		keys[1].TenantID = "t-other"
		keys[2].SessionID = "s-other"
		keys[3].ContactID = "c-other"
		keys[4].EventID = "e-other"
		for _, k := range keys {
			b.pool.Request(k, clientText)
		}
		settle()

		if now, _ := b.prov.flying(); now != len(keys) {
			t.Fatalf("inferencias en vuelo = %d, quiero %d: cada ventana pregunta por su cuenta", now, len(keys))
		}
	})
}

// TestRequest_FullQueueDropsTheAdvanceAndFreesTheWindow es R-12: con la cola llena
// Request DESCARTA y vuelve —no bloquea: corre en línea con el mensaje del cliente—,
// suelta el cerrojo y lo dice por Debug. No es un error.
func TestRequest_FullQueueDropsTheAdvanceAndFreesTheWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t, intakeahead.WithQueueSize(1), intakeahead.WithWorkers(1))
		first, second := windowOf("c-1"), windowOf("c-2")

		// Sin Run nadie consume: la cola se llena de verdad y no por una carrera.
		b.pool.Request(first, clientText+" (uno)")
		b.pool.Request(second, clientText+" (dos, que no cabe)")

		dropped := b.log.lines("DEBUG", queueFullMsg)
		if len(dropped) != 1 {
			t.Fatalf("quiero UNA línea Debug %q; log:\n%s", queueFullMsg, b.log.String())
		}
		for _, field := range []string{"tenant_id=" + tenantID, "session_id=" + sessionID} {
			if !strings.Contains(dropped[0], field) {
				t.Errorf("a la línea le falta %s: %s", field, dropped[0])
			}
		}

		b.start()
		settle()
		if calls := b.prov.seen(); len(calls) != 1 || calls[0].in.Text != clientText+" (uno)" {
			t.Fatalf("lo descartado no se atiende después: %+v", calls)
		}

		// El cerrojo de la ventana descartada cayó: su siguiente mensaje sí pregunta.
		b.pool.Request(second, clientText+" (tres)")
		settle()
		calls := b.prov.seen()
		if len(calls) != 2 || calls[1].in.Text != clientText+" (tres)" {
			t.Fatalf("la ventana descartada quedó marcada como «preguntando» para siempre: %d inferencias", len(calls))
		}
		got := b.sink.seen()
		if len(got) != 2 || got[1].key != second {
			t.Errorf("la segunda entrega debe ser de la ventana que se descartó antes: %+v", got)
		}
	})
}

// TestWithQueueSize: caben N esperando worker y la siguiente se descarta; <= 0 se
// ignora y quedan las DefaultQueue.
func TestWithQueueSize(t *testing.T) {
	cases := []struct {
		name string
		opts []intakeahead.Option
		want int
	}{
		{"default", nil, intakeahead.DefaultQueue},
		{"two", []intakeahead.Option{intakeahead.WithQueueSize(2)}, 2},
		{"zero is ignored", []intakeahead.Option{intakeahead.WithQueueSize(0)}, intakeahead.DefaultQueue},
		{"negative is ignored", []intakeahead.Option{intakeahead.WithQueueSize(-1)}, intakeahead.DefaultQueue},
		{"ignored value keeps the previous one",
			[]intakeahead.Option{intakeahead.WithQueueSize(3), intakeahead.WithQueueSize(0)}, 3},
		{"last one wins", []intakeahead.Option{intakeahead.WithQueueSize(3), intakeahead.WithQueueSize(5)}, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newBench(t, tc.opts...)
				const extra = 3
				for i := range tc.want + extra {
					b.pool.Request(windowOf(fmt.Sprintf("c-%d", i)), clientText)
				}
				if got := len(b.log.lines("DEBUG", queueFullMsg)); got != extra {
					t.Fatalf("descartadas = %d, quiero %d (caben %d)", got, extra, tc.want)
				}
				b.start()
				settle()
				if got := len(b.prov.seen()); got != tc.want {
					t.Errorf("inferencias = %d, quiero las %d que cabían", got, tc.want)
				}
			})
		})
	}
}

// TestRequest_OneBudgetForTheWholeRequest: catálogo, proveedor e inferencia corren con
// UN deadline, el del presupuesto. En la burbuja no pasa el tiempo mientras alguien
// puede avanzar, así que lo que le queda al ctx al entrar es el presupuesto exacto.
func TestRequest_OneBudgetForTheWholeRequest(t *testing.T) {
	cases := []struct {
		name string
		opts []intakeahead.Option
		want time.Duration
	}{
		{"default", nil, intakeahead.DefaultTimeout},
		{"ten seconds", []intakeahead.Option{intakeahead.WithTimeout(10 * time.Second)}, 10 * time.Second},
		{"zero is ignored", []intakeahead.Option{intakeahead.WithTimeout(0)}, intakeahead.DefaultTimeout},
		{"negative is ignored", []intakeahead.Option{intakeahead.WithTimeout(-time.Second)}, intakeahead.DefaultTimeout},
		{"ignored value keeps the previous one",
			[]intakeahead.Option{intakeahead.WithTimeout(time.Minute), intakeahead.WithTimeout(0)}, time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newBench(t, tc.opts...)
				b.start()
				b.pool.Request(windowKey(), clientText)
				settle()

				if bounded := b.cfg.seenBounded(); len(bounded) != 1 || !bounded[0] {
					t.Errorf("la lectura del catálogo va dentro del presupuesto: %v", bounded)
				}
				asks := b.sel.seen()
				if len(asks) != 1 || !asks[0].bounded || asks[0].remaining != tc.want {
					t.Errorf("al elegir proveedor quedaban %+v, quiero %v", asks, tc.want)
				}
				calls := b.prov.seen()
				if len(calls) != 1 || !calls[0].bounded || calls[0].remaining != tc.want {
					t.Errorf("a la inferencia le quedaban %+v, quiero %v", calls, tc.want)
				}
			})
		})
	}
}

// TestSinkFunc_ForwardsTheDelivery: SinkFunc es la clausura con la que el arranque cose
// pool y agregador. Entrega los tres argumentos tal cual, una vez por llamada.
func TestSinkFunc_ForwardsTheDelivery(t *testing.T) {
	var got []delivery
	var sink intakeahead.Sink = intakeahead.SinkFunc(func(key intake.WindowKey, intent string, confidence float64) {
		got = append(got, delivery{key: key, intent: intent, confidence: confidence})
	})

	sink.OnClassified(windowKey(), "intake_request", 0.75)
	sink.OnClassified(windowOf("c-2"), "consulta", 0)

	want := []delivery{
		{key: windowKey(), intent: "intake_request", confidence: 0.75},
		{key: windowOf("c-2"), intent: "consulta", confidence: 0},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("entregas = %+v, quiero %+v", got, want)
	}
}

// TestSinkFunc_ResolvesItsTargetWhenCalled: la variable del destino se asigna DESPUÉS de
// construir el pool —así se cablea el agregador— y la entrega la encuentra.
func TestSinkFunc_ResolvesItsTargetWhenCalled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var target *fakeSink
		log := &syncBuffer{}
		prov := &fakeProvider{replies: []reply{{raw: goodArtifact()}}}
		var pool ahead = newPool(debugLog(log),
			&fakeConfig{blobs: map[string]string{tenantID: publishedCatalog}},
			&fakeSelector{provider: prov},
			intakeahead.SinkFunc(func(key intake.WindowKey, intent string, confidence float64) {
				target.OnClassified(key, intent, confidence)
			}))
		target = &fakeSink{}

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			pool.Run(ctx)
		}()
		pool.Request(windowKey(), clientText)
		settle()
		cancel()
		<-done

		if got := target.seen(); len(got) != 1 || got[0].intent != "intake_request" {
			t.Fatalf("la entrega no llegó al destino asignado tras construir el pool: %+v", got)
		}
	})
}
