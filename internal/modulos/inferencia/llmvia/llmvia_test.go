package llmvia_test

// Las promesas de llmvia.go: la construcción del selector, la selección (For), las opciones
// del arranque y el uso concurrente. Lo que no cabe aquí (E-13) está partido por tema: el
// aviso visto desde For en llmvia_notice_test.go, Warm en llmvia_warm_test.go y PlazaDe en
// llmvia_plaza_test.go. Los dobles viven en helpers_test.go.
//
// Sin red, sin BD y sin clave real (T-16): la vía api se prueba solo con api.New FALLANDO.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/llm/api"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm/tenantllmhelpertest"
)

// Los dos puertos los satisfacen los tipos de producción (y el doble), sin adaptador.
var (
	_ llmvia.Store    = (*tenantllm.Postgres)(nil)
	_ llmvia.Store    = (*tenantllmhelpertest.Memoria)(nil)
	_ llmvia.Notifier = (*degradation.Notifier)(nil)
)

// Los consumidores reciben el selector POR INTERFAZ (arquitectura §4), cada uno la suya:
// las etapas del pipeline y la cotización piden For, el adelanto de ventana pide Warm y el
// aforo pide PlazaDe. Si una firma cambiara, esto no compila.
var _ interface {
	For(ctx context.Context, tenantID, originSessionID string) (llm.LLMProvider, error)
	Warm(ctx context.Context, tenantID, sessionID string, in llm.ClassifyRequestInput) error
	PlazaDe(ctx context.Context, tenantID, originSessionID string) (string, bool, error)
} = (*llmvia.Selector)(nil)

// warnNoRouter es el literal del aviso de arranque, byte a byte (diseno §4).
const warnNoRouter = "llmvia: el transporte de la vía local no sabe decir qué Edge atiende; " +
	"el aforo de plaza del pipeline de lote (T2.7) quedará INERTE para este proceso"

// ---------------------------------------------------------------- la construcción

// TestNewSelector_RequiresTheStore: fallo de arranque, no de llamada.
func TestNewSelector_RequiresTheStore(t *testing.T) {
	t.Parallel()
	s, err := llmvia.NewSelector(nil, logger.New(logger.WithWriter(&bytes.Buffer{})))
	if !errors.Is(err, llmvia.ErrSinConfig) || s != nil {
		t.Fatalf("NewSelector(nil) = (%v, %v), quería (nil, ErrSinConfig)", s, err)
	}
	if got, want := llmvia.ErrSinConfig.Error(), "llmvia: el selector necesita el store de tenant_llm"; got != want {
		t.Errorf("texto de ErrSinConfig = %q, quería %q", got, want)
	}
}

// TestNewSelector_WarnsOnceWhenTheFrameCannotRoute: un frame que no sabe decir qué Edge
// atiende dejaría el aforo de plaza INERTE sin un solo error. El aviso sale al arrancar, una
// vez por proceso, y no escondido en el camino caliente.
func TestNewSelector_WarnsOnceWhenTheFrameCannotRoute(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		opts  []llmvia.SelectorOption
		warns int
	}{
		{"frame that cannot route", []llmvia.SelectorOption{llmvia.WithFrame(&fakeFrame{})}, 1},
		{"frame that can route", []llmvia.SelectorOption{llmvia.WithFrame(&routerFrame{})}, 0},
		{"no frame", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logs := &bytes.Buffer{}
			s, err := llmvia.NewSelector(tenantllmhelpertest.NewMemoria(), logger.New(logger.WithWriter(logs)), tc.opts...)
			if err != nil || s == nil {
				t.Fatalf("NewSelector = (%v, %v)", s, err)
			}
			if got := strings.Count(logs.String(), warnNoRouter); got != tc.warns {
				t.Errorf("avisos de arranque = %d, quería %d; log: %s", got, tc.warns, logs)
			}
			if tc.warns > 0 && !strings.Contains(logs.String(), "WARN") {
				t.Errorf("el aviso de arranque no salió como Warn: %s", logs)
			}
		})
	}

	// Sin logger no hay a quién avisar, y eso no rompe la construcción.
	t.Run("nil logger", func(t *testing.T) {
		t.Parallel()
		s, err := llmvia.NewSelector(tenantllmhelpertest.NewMemoria(), nil, llmvia.WithFrame(&fakeFrame{}))
		if err != nil || s == nil {
			t.Fatalf("NewSelector sin logger = (%v, %v), quería un selector", s, err)
		}
	})
}

// ---------------------------------------------------------------- la selección

// TestFor_TheRowDecidesTheAdapter: los tres estados de la fila y a qué adaptador llevan. La
// afirmación se hace por una CONSECUENCIA OBSERVABLE de cada vía y no por el tipo devuelto:
// la vía local usa el frame (y el doble lo registra), la vía api pide la credencial (y el
// doble lo cuenta).
func TestFor_TheRowDecidesTheAdapter(t *testing.T) {
	t.Parallel()

	for name, store := range map[string]*tenantllmhelpertest.Memoria{
		// 🔴 R4.4.b / REQ-33: sin fila es la vía LOCAL, que hoy es la de todos.
		"no row means the local route": tenantllmhelpertest.NewMemoria(),
		"local row":                    rowStore(t, tenantllm.Config{Via: tenantllm.ViaLocal}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			frame := &fakeFrame{out: validOutput}
			p, err := newSelector(t, store, llmvia.WithFrame(frame)).For(context.Background(), testTenant, "")
			if err != nil {
				t.Fatalf("For: %v", err)
			}
			if err := classify(context.Background(), p); err != nil {
				t.Fatalf("P1: %v", err)
			}
			if call := frame.only(t); call.tenant != testTenant {
				t.Errorf("tenant del frame = %q, quería %q", call.tenant, testTenant)
			}
			// 🔴 Y NO SE PIDIÓ LA CREDENCIAL: la vía local no llama a ningún tercero, y
			// descifrar una clave para ella sería sacar del sobre un secreto sin uso.
			if n := store.APIKeyCalls(); n != 0 {
				t.Errorf("la vía local pidió la credencial %d veces", n)
			}
		})
	}

	// La vía api se reconoce por lo único que hace sin red: pedir la credencial, UNA vez, y
	// dársela a api.New — que aquí falla porque la fila no trae modelo.
	t.Run("api row asks for the credential", func(t *testing.T) {
		t.Parallel()
		store := rowStore(t, tenantllm.Config{Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic})
		frame := &fakeFrame{out: validOutput}
		p, err := newSelector(t, store, llmvia.WithFrame(frame)).For(context.Background(), testTenant, "sess-1")
		if !errors.Is(err, api.ErrInvalidConfig) || p != nil {
			t.Fatalf("For = (%v, %v), quería (nil, api.ErrInvalidConfig): la fila no trae modelo", p, err)
		}
		if n := store.APIKeyCalls(); n != 1 {
			t.Errorf("la vía api pidió la credencial %d veces, quería 1", n)
		}
		frame.requireUntouched(t)
	})
}

// TestFor_AnUnknownRouteDoesNotChooseForYou: si la columna trae un valor fuera del
// vocabulario, se devuelve error. Adivinar la vía de un tenant es exactamente lo que REQ-33
// prohíbe. El «arreglo» tentador —«si no sé, tira de local que es gratis»— pone esto rojo.
func TestFor_AnUnknownRouteDoesNotChooseForYou(t *testing.T) {
	t.Parallel()
	store := &stubStore{found: true, row: tenantllm.Config{TenantID: testTenant, Via: "vertex"}}
	frame := &fakeFrame{out: validOutput}
	book, falls := newNoticeBook(), &fallCounter{}
	s := newSelector(t, store, llmvia.WithFrame(frame), book.option(), llmvia.WithDegradacionObservada(falls.count))

	p, err := s.For(context.Background(), testTenant, "")
	if !errors.Is(err, llmvia.ErrViaDesconocida) || p != nil {
		t.Fatalf("For = (%v, %v), quería (nil, ErrViaDesconocida)", p, err)
	}
	want := `llmvia: vía fuera del vocabulario cerrado (local|api): "vertex" (tenant ` + testTenant + `)`
	if err.Error() != want {
		t.Errorf("texto = %q, quería %q", err, want)
	}
	frame.requireUntouched(t)
	if store.keyCalls != 0 {
		t.Errorf("se pidió la credencial %d veces para una vía que no existe", store.keyCalls)
	}
	requireSilence(t, book, falls)
}

// TestFor_AStoreFailureIsNotARoute: un SELECT que falla no es «este tenant está en local».
// Tratarlo así mandaría al Edge del cliente el trabajo de todos los tenants cuya fila no se
// pudo leer.
func TestFor_AStoreFailureIsNotARoute(t *testing.T) {
	t.Parallel()
	boom := errors.New("la base no está")
	store := &stubStore{getErr: boom}
	frame := &fakeFrame{out: validOutput}
	book, falls := newNoticeBook(), &fallCounter{}
	s := newSelector(t, store, llmvia.WithFrame(frame), book.option(), llmvia.WithDegradacionObservada(falls.count))

	p, err := s.For(context.Background(), testTenant, "")
	if !errors.Is(err, boom) || p != nil {
		t.Fatalf("For = (%v, %v), quería (nil, el error de la base)", p, err)
	}
	if want := storeReadPrefix + boom.Error(); err.Error() != want {
		t.Errorf("texto = %q, quería %q", err, want)
	}
	frame.requireUntouched(t)
	if store.keyCalls != 0 {
		t.Errorf("se pidió la credencial %d veces sin saber la vía", store.keyCalls)
	}
	requireSilence(t, book, falls)
}

// TestFor_WithoutFrameFailsWithAnUntypedNil (T-5): un tenant en vía local sin cable falla al
// construir, con el error de siempre, y lo que vuelve es un nil DE INTERFAZ. Un
// (*local.Provider)(nil) metido en un llm.LLMProvider dejaría de comparar igual a nil y el
// siguiente `if p == nil` se llevaría una sorpresa en la primera llamada.
func TestFor_WithoutFrameFailsWithAnUntypedNil(t *testing.T) {
	t.Parallel()
	book, falls := newNoticeBook(), &fallCounter{}
	s := newSelector(t, tenantllmhelpertest.NewMemoria(), book.option(), llmvia.WithDegradacionObservada(falls.count))

	p, err := s.For(context.Background(), testTenant, "")
	if !errors.Is(err, local.ErrSinTransporte) {
		t.Fatalf("For = %v, quería local.ErrSinTransporte", err)
	}
	if p != nil {
		t.Fatalf("For devolvió un provider no nil (%T) junto al error: es un nil tipado", p)
	}
	// Un selector sin cable es un fallo de arranque, no una degradación de la vía.
	requireSilence(t, book, falls)
}

// TestFor_TheOriginSessionTravelsInTheFrame: el parámetro que For recibe LLEGA al frame. No
// es cosmética de trazabilidad: ese campo es lo que hace que el gateway elija el Edge que
// recibió el mensaje en vez del primero por orden alfabético. Con el dato vacío —como viajó
// una ola entera, compilando y con los gates en verde— un tenant con dos Edges mandaba
// SIEMPRE la inferencia al mismo, y el otro no calentaba nunca su caché de prefijos.
func TestFor_TheOriginSessionTravelsInTheFrame(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		boot        []llmvia.SelectorOption
		session     string
		wantSession string
		wantFormat  string
	}{
		{name: "the session asked of For is the one in the frame", session: "sess-B", wantSession: "sess-B", wantFormat: local.DefaultFormat},
		// Rellenarla con cualquier cosa convertiría un dato de trazabilidad en una
		// coincidencia sin significado, y le quitaría al gateway su fallback.
		{name: "empty travels empty", session: "", wantSession: "", wantFormat: local.DefaultFormat},
		{
			name:    "it is ADDED to the boot options, it does not replace them",
			boot:    []llmvia.SelectorOption{llmvia.WithLocalOptions(local.WithFormat("json_schema"))},
			session: "sess-B", wantSession: "sess-B", wantFormat: "json_schema",
		},
		{
			name:    "a boot option cannot pin another session",
			boot:    []llmvia.SelectorOption{llmvia.WithLocalOptions(local.WithOriginSession("sess-del-arranque"))},
			session: "sess-B", wantSession: "sess-B", wantFormat: local.DefaultFormat,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			frame := &fakeFrame{out: validOutput}
			opts := append([]llmvia.SelectorOption{llmvia.WithFrame(frame)}, tc.boot...)
			p, err := newSelector(t, tenantllmhelpertest.NewMemoria(), opts...).For(context.Background(), testTenant, tc.session)
			if err != nil {
				t.Fatalf("For: %v", err)
			}
			if err := classify(context.Background(), p); err != nil {
				t.Fatalf("P1: %v", err)
			}
			req := frame.only(t).req
			if req.OriginSessionID != tc.wantSession || req.Format != tc.wantFormat {
				t.Errorf("frame = {origin_session_id:%q, format:%q}, quería {%q, %q}",
					req.OriginSessionID, req.Format, tc.wantSession, tc.wantFormat)
			}
			if req.TargetSessionID != "" || req.Warmup {
				t.Errorf("frame = {target_session_id:%q, warmup:%v}: una etapa del pipeline no fija destino ni calienta",
					req.TargetSessionID, req.Warmup)
			}
		})
	}
}

// ---------------------------------------------------------------- las opciones del arranque

// TestWithLocalOptions_AccumulatesAcrossCalls (T-3): DOS llamadas separadas dentro de la
// MISMA construcción deben ACUMULARSE, no pisarse. Reproduce el cableado real del arranque:
// local.ConPlantillas por un lado y local.WithMaxOutputTokens por otro. Cuando asignaba, la
// segunda pisaba a la primera y la palanca WAPP_LLM_PROMPTS_DIR estuvo muerta en silencio.
//
// Se mide por CONDUCTA: las dos opciones llegan al provider a la vez —la plantilla ajustada
// en el prompt Y el techo apagado en el frame—, en los dos órdenes.
func TestWithLocalOptions_AccumulatesAcrossCalls(t *testing.T) {
	t.Parallel()
	const mark = "INSTRUCCION-AJUSTADA-DE-TEST-QUE-NO-EXISTE-EN-LA-COMPILADA"
	templates := llmvia.WithLocalOptions(local.ConPlantillas(map[llm.Etapa]llm.Plantilla{
		llm.EtapaP2: {Instruccion: mark, Esquema: "ESQUEMA-AJUSTADO-DE-TEST"},
	}))
	ceilingOff := llmvia.WithLocalOptions(local.WithMaxOutputTokens(false))

	for name, boot := range map[string][]llmvia.SelectorOption{
		"templates first, as the boot does": {templates, ceilingOff},
		"ceiling first":                     {ceilingOff, templates},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			frame := &fakeFrame{out: `{"version":1,"wants":[{"idea":"x","evidence":"y"}]}`}
			opts := append([]llmvia.SelectorOption{llmvia.WithFrame(frame)}, boot...)
			p, err := newSelector(t, tenantllmhelpertest.NewMemoria(), opts...).For(context.Background(), testTenant, "")
			if err != nil {
				t.Fatalf("For: %v", err)
			}
			if _, err := p.ExtractMainIdeas(context.Background(), llm.ExtractMainIdeasInput{SourceText: "hola"}, llm.Options{}); err != nil {
				t.Fatalf("P2: %v", err)
			}
			req := frame.only(t).req
			if !strings.Contains(req.Prompt, mark) {
				t.Errorf("la plantilla de ConPlantillas NO llegó al prompt: la otra llamada a WithLocalOptions la pisó. Prompt: %q", req.Prompt)
			}
			if req.MaxOutputTokens != 0 {
				t.Errorf("max_output_tokens = %d, quería 0: la otra llamada a WithLocalOptions pisó a WithMaxOutputTokens(false)", req.MaxOutputTokens)
			}
		})
	}
}

// TestWithNotifier_WithoutItTheProviderIsNotWrapped: una envoltura que no hace nada solo
// añade un marco en los stack traces. Sin notificador —o con uno nil, que se admite a
// propósito— For devuelve el adaptador TAL CUAL; con notificador, envuelto. Y el sistema
// degrada igual en los dos casos: el error llega intacto.
func TestWithNotifier_WithoutItTheProviderIsNotWrapped(t *testing.T) {
	t.Parallel()
	var none llmvia.Notifier
	for _, tc := range []struct {
		name    string
		opts    []llmvia.SelectorOption
		wrapped bool
	}{
		{"no notifier", nil, false},
		{"nil notifier", []llmvia.SelectorOption{llmvia.WithNotifier(none)}, false},
		{"with notifier", []llmvia.SelectorOption{newNoticeBook().option()}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			failure := &transportError{"ollama_down"}
			opts := append([]llmvia.SelectorOption{llmvia.WithFrame(&fakeFrame{err: failure})}, tc.opts...)
			p, err := newSelector(t, tenantllmhelpertest.NewMemoria(), opts...).For(context.Background(), testTenant, "")
			if err != nil {
				t.Fatalf("For: %v", err)
			}
			if _, bare := p.(*local.Provider); bare == tc.wrapped {
				t.Errorf("provider = %T; ¿envuelto? quería %v", p, tc.wrapped)
			}
			if got := classify(context.Background(), p); got != error(failure) { //nolint:errorlint // se afirma la IDENTIDAD: sin envolver
				t.Errorf("P1 = %v, quería el error del transporte intacto", got)
			}
		})
	}
}

// TestWithClock_StampsTheInstantOfTheFailure: el instante del aviso lo pone el reloj del
// selector, y de él depende la ventana del dedupe.
func TestWithClock_StampsTheInstantOfTheFailure(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 14, 15, 9, 26, 0, time.FixedZone("UTC-5", -5*60*60))
	book := newNoticeBook()
	s := newSelector(t, tenantllmhelpertest.NewMemoria(),
		llmvia.WithFrame(&fakeFrame{err: &transportError{"timeout"}}),
		book.option(),
		llmvia.WithClock(func() time.Time { return at }),
	)
	p, err := s.For(context.Background(), testTenant, "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if err := classify(context.Background(), p); err == nil {
		t.Fatal("quería el error del transporte")
	}
	rows := book.rows.Rows(testTenant)
	if len(rows) != 1 {
		t.Fatalf("avisos = %v, quería 1", book.written())
	}
	if n := rows[0]; !n.LastSeenAt.Equal(at) || n.WindowStart.After(at) || !n.WindowEnd.After(at) {
		t.Errorf("aviso visto en %s, ventana [%s, %s); quería el instante del reloj (%s) dentro de su ventana",
			n.LastSeenAt, n.WindowStart, n.WindowEnd, at)
	}
}

// ---------------------------------------------------------------- el calentamiento

// TestWarm_OnlyTheLocalRouteHasACacheToWarm.
//
// 🔴 EL CASO API ES EL QUE PAGA ESTE TEST, y su coste es real y ajeno: si el calentamiento
// se emitiera sin mirar la vía, el Edge de un tenant que clasifica por API gastaría ~50 s
// de la CPU de SU máquina y ~250 MB de su caché de prefijo por un prompt que nadie va a
// volver a pedir. La otra mitad —que la vía local SÍ emita— es la que evita que el arreglo
// sea «no calentar nunca», que también pasaría un test escrito solo con el caso API.
func TestWarm_OnlyTheLocalRouteHasACacheToWarm(t *testing.T) {
	t.Parallel()
	boom := errors.New("la base no está")
	in := classifyInput()
	in.Text = "esto se ignora"

	for _, tc := range []struct {
		name  string
		store llmvia.Store
		emits bool
		want  error
	}{
		{name: "local row: emits the frame, marked and targeted", store: rowStore(t, tenantllm.Config{Via: tenantllm.ViaLocal}), emits: true},
		{name: "no row: REQ-33 says local, so it warms too", store: tenantllmhelpertest.NewMemoria(), emits: true},
		{name: "api row: no cache of ours to fill, the Edge is NOT touched", store: rowStore(t, apiRow()), want: llmvia.ErrViaSinCalentamiento},
		{name: "route outside the vocabulary", store: &stubStore{found: true, row: tenantllm.Config{Via: "inventada"}}, want: llmvia.ErrViaDesconocida},
		{name: "the row cannot be read", store: &stubStore{getErr: boom}, want: boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			frame := &fakeFrame{out: `{"vers`} // truncada: el calentamiento no mira la salida
			s := newSelector(t, tc.store, llmvia.WithFrame(frame), llmvia.WithLocalOptions(local.WithFormat("json_schema")))

			err := s.Warm(context.Background(), testTenant, "s-9", in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Warm = %v, quería %v", err, tc.want)
			}
			if !tc.emits {
				frame.requireUntouched(t)
				return
			}
			req := frame.only(t).req
			if !req.Warmup || req.TargetSessionID != "s-9" || req.OriginSessionID != "" {
				t.Errorf("frame = {warmup:%v, target:%q, origin:%q}, quería {true, s-9, vacío}",
					req.Warmup, req.TargetSessionID, req.OriginSessionID)
			}
			if req.Format != "json_schema" {
				t.Errorf("format = %q: las opciones del arranque no llegaron al calentamiento", req.Format)
			}
		})
	}

	if got, want := llmvia.ErrViaSinCalentamiento.Error(), "llmvia: la vía del tenant no tiene caché de prefijo que calentar"; got != want {
		t.Errorf("texto de ErrViaSinCalentamiento = %q, quería %q", got, want)
	}
}

// TestWarm_NeverNotifiesNorCounts (T-10): un calentamiento que falla NO es una degradación
// de la vía del dueño. Nadie lo pidió, su fallo no le quita nada al cliente, y avisarle
// sería mandarlo a revisar un equipo que está perfectamente.
func TestWarm_NeverNotifiesNorCounts(t *testing.T) {
	t.Parallel()
	failure := &transportError{"ollama_down"}
	book, falls := newNoticeBook(), &fallCounter{}
	s := newSelector(t, tenantllmhelpertest.NewMemoria(),
		llmvia.WithFrame(&fakeFrame{err: failure}), book.option(), llmvia.WithDegradacionObservada(falls.count))

	if err := s.Warm(context.Background(), testTenant, "s-1", classifyInput()); err != error(failure) { //nolint:errorlint // se afirma la IDENTIDAD: sin envolver
		t.Fatalf("Warm = %v, quería el error del transporte intacto", err)
	}
	requireSilence(t, book, falls)

	// Y sin cable es el fallo de arranque de siempre, tampoco un aviso.
	bare := newSelector(t, tenantllmhelpertest.NewMemoria(), book.option())
	if err := bare.Warm(context.Background(), testTenant, "s-1", classifyInput()); !errors.Is(err, local.ErrSinTransporte) {
		t.Fatalf("Warm sin cable = %v, quería local.ErrSinTransporte", err)
	}
	requireSilence(t, book, falls)
}

// ---------------------------------------------------------------- el uso concurrente

// TestSelector_ConcurrentCallsDoNotCrossSessions (T-4): el Selector se comparte entre
// goroutines y ninguna entrada lo muta. Si For (o Warm) hiciera append sobre el slice de
// opciones del Selector, escribiría en SU array subyacente en cuanto tuviera capacidad de
// sobra —aquí la tiene: tres opciones añadidas de una en una— y dos peticiones concurrentes
// se pisarían la sesión: un cruce de trazabilidad entre tenants, una de cada muchas.
//
// Está pensado para `-race`, que es quien ve la escritura compartida; sin él, lo que se ve
// es el cruce: cada tenant tiene que salir SIEMPRE con su sesión.
func TestSelector_ConcurrentCallsDoNotCrossSessions(t *testing.T) {
	t.Parallel()
	frame := &fakeFrame{out: validOutput}
	s := newSelector(t, tenantllmhelpertest.NewMemoria(),
		llmvia.WithFrame(frame),
		llmvia.WithLocalOptions(local.WithFormat("json_schema")),
		llmvia.WithLocalOptions(local.WithTimeout(time.Minute)),
		llmvia.WithLocalOptions(local.WithMaxOutputTokens(true)),
	)

	const workers, rounds = 16, 40
	failures := make(chan error, workers*rounds)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tenant, session := fmt.Sprintf("tenant-%02d", w), fmt.Sprintf("sess-%02d", w)
			for range rounds {
				if w%2 == 1 {
					if err := s.Warm(context.Background(), tenant, session, classifyInput()); err != nil {
						failures <- fmt.Errorf("Warm de %s: %w", tenant, err)
					}
					continue
				}
				p, err := s.For(context.Background(), tenant, session)
				if err != nil {
					failures <- fmt.Errorf("For de %s: %w", tenant, err)
					continue
				}
				if err := classify(context.Background(), p); err != nil {
					failures <- fmt.Errorf("P1 de %s: %w", tenant, err)
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	calls := frame.calls()
	if len(calls) != workers*rounds {
		t.Fatalf("peticiones al transporte = %d, quería %d", len(calls), workers*rounds)
	}
	for _, call := range calls {
		want := "sess-" + strings.TrimPrefix(call.tenant, "tenant-")
		got := call.req.OriginSessionID
		if call.req.Warmup {
			got = call.req.TargetSessionID
		}
		if got != want || call.req.Format != "json_schema" {
			t.Fatalf("petición de %s (warmup=%v): sesión %q y formato %q; quería %q y json_schema: dos peticiones se cruzaron",
				call.tenant, call.req.Warmup, got, call.req.Format, want)
		}
	}
}
