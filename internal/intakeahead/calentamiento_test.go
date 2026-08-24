package intakeahead_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakeahead"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
)

// calFake registra los calentamientos que se le piden y puede bloquearse para simular
// uno en vuelo.
type calFake struct {
	mu       sync.Mutex
	vistos   []calentamiento
	err      error
	suelta   chan struct{} // si no es nil, Warm espera aquí antes de volver
	entrando chan struct{} // se cierra/emite al entrar en Warm
}

type calentamiento struct {
	tenantID  string
	sessionID string
	in        llm.ClassifyRequestInput
}

func (c *calFake) Warm(_ context.Context, tenantID, sessionID string, in llm.ClassifyRequestInput) error {
	c.mu.Lock()
	c.vistos = append(c.vistos, calentamiento{tenantID: tenantID, sessionID: sessionID, in: in})
	suelta, entrando, err := c.suelta, c.entrando, c.err
	c.mu.Unlock()
	if entrando != nil {
		entrando <- struct{}{}
	}
	if suelta != nil {
		<-suelta
	}
	return err
}

func (c *calFake) todos() []calentamiento {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]calentamiento(nil), c.vistos...)
}

func esperaCalentamientos(t *testing.T, c *calFake, n int) []calentamiento {
	t.Helper()
	limite := time.After(3 * time.Second)
	for {
		if v := c.todos(); len(v) >= n {
			return v
		}
		select {
		case <-limite:
			t.Fatalf("solo llegaron %d calentamientos de %d", len(c.todos()), n)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// TestWarm_UsaElMISMOCatalogoQueLaClasificacionReal.
//
// El calentamiento solo sirve si deja cacheado el prefijo que va a pedir la P1 real, y
// ese prefijo lo forma el catálogo aplanado, el vocabulario y la etiqueta de
// desconocido. Aquí se comprueba que salen del catálogo PUBLICADO y no de un armado
// paralelo — la garantía de fondo es que los dos caminos llaman a la misma `entrada`,
// y esto es lo que lo hace visible desde fuera del paquete.
//
// 🔬 MUTACIÓN: que `calentar` construya la entrada a mano (por ejemplo sin
// UnknownLabel o sin Vocabulary) ⇒ rojo.
func TestWarm_UsaElMISMOCatalogoQueLaClasificacionReal(t *testing.T) {
	t.Parallel()
	cal := &calFake{}
	p := intakeahead.New(log(t), configSembrada(t, catalogoPublicado), &selFake{}, nuevoSink(),
		intakeahead.WithCalentador(cal))

	p.Warm(tenant, "edge-1", sesion, intentcfg.Kind)

	v := esperaCalentamientos(t, cal, 1)[0]
	if v.tenantID != tenant || v.sessionID != sesion {
		t.Fatalf("calentamiento a (%q,%q), quiero (%q,%q)", v.tenantID, v.sessionID, tenant, sesion)
	}
	if len(v.in.Catalog) != 2 {
		t.Fatalf("el catálogo del calentamiento trae %d intenciones, el publicado tiene 2: "+
			"un prefijo distinto del real no calienta nada y no da ningún error", len(v.in.Catalog))
	}
	if v.in.UnknownLabel == "" {
		t.Error("sin UnknownLabel el prompt pierde una regla entera y el prefijo deja de coincidir")
	}
	if len(v.in.Vocabulary) != 2 {
		t.Errorf("el vocabulario del negocio (2 términos) no llegó al calentamiento: %v", v.in.Vocabulary)
	}
}

// TestWarm_UnoEnVueloPorEDGE: el fan-out de un ConfigUpdate llama a Warm una vez por
// Edge, pero un Edge puede recibir varias llamadas seguidas (dos sesiones que
// conectan, un ConfigUpdate encima). Un calentamiento dura ~50 s y ocupa la PLAZA
// ÚNICA del Ollama del cliente: solaparlos consigo mismo es apilar prefills fríos
// delante del tráfico real.
//
// 🔬 MUTACIÓN: quitar la llamada a `marcarCalentamiento` ⇒ rojo (llegan tres).
func TestWarm_UnoEnVueloPorEDGE(t *testing.T) {
	t.Parallel()
	suelta := make(chan struct{})
	// `entrando` va BUFFERADO para que un calentamiento que no debería existir pueda
	// entrar y registrarse igualmente: si bloqueara, la mutación que hay que cazar se
	// quedaría colgada antes de dejar rastro y el test la vería como «no pasó nada».
	entrando := make(chan struct{}, 8)
	cal := &calFake{suelta: suelta, entrando: entrando}
	p := intakeahead.New(log(t), configSembrada(t, catalogoPublicado), &selFake{}, nuevoSink(),
		intakeahead.WithCalentador(cal))

	// El primero entra y se queda dentro: a partir de aquí el Edge tiene uno en vuelo.
	p.Warm(tenant, "edge-1", sesion, intentcfg.Kind)
	<-entrando

	// Otros dos del MISMO Edge y uno de OTRO. Los dos primeros se descartan DENTRO de
	// Warm (ni siquiera nace su goroutine); el tercero no, porque su caché es otra.
	p.Warm(tenant, "edge-1", "s-2", intentcfg.Kind)
	p.Warm(tenant, "edge-1", "s-3", intentcfg.Kind)
	p.Warm(tenant, "edge-2", "s-4", intentcfg.Kind)
	<-entrando

	// 🔴 LA ESPERA ES LO QUE HACE QUE ESTE TEST MIRE. Sin ella el conteo se leería
	// mientras los descartados —si el cerrojo no existiera— todavía están arrancando, y
	// pasaría por carrera: un `esperaCalentamientos(…, 2)` vuelve en cuanto hay dos y no
	// puede distinguir «hay exactamente dos» de «hay dos por ahora».
	time.Sleep(150 * time.Millisecond)
	v := cal.todos()
	if len(v) != 2 {
		t.Fatalf("calentamientos = %d (%v), quiero 2: uno por Edge. Los del mismo Edge se "+
			"solaparían sobre su ÚNICA plaza de Ollama", len(v), v)
	}
	close(suelta)
	esperaCalentamientos(t, cal, 2)

	// Y cuando el primero termina, el Edge vuelve a admitir calentamientos: el cerrojo
	// es «uno en vuelo», no «uno para siempre».
	//
	// Se reintenta en bucle porque el cerrojo se suelta en el `defer` de la goroutine
	// del calentamiento y no hay forma de observar ese instante desde fuera: pedir una
	// sola vez justo después de desbloquear al fake mediría la carrera, no la conducta.
	cal.mu.Lock()
	cal.entrando, cal.suelta = nil, nil
	cal.mu.Unlock()
	limite := time.After(3 * time.Second)
	for len(cal.todos()) < 3 {
		p.Warm(tenant, "edge-1", sesion, intentcfg.Kind)
		select {
		case <-limite:
			t.Fatalf("el Edge no volvió a admitir calentamientos tras terminar el suyo: "+
				"el cerrojo «uno en vuelo» se quedó tomado (%d calentamientos)", len(cal.todos()))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestWarm_NoBloqueaAlLlamante. Sus dos llamantes son el bucle Recv del stream
// CloudLink y el fan-out del PUT de intents; si Warm esperase a la inferencia, un
// Edge conectándose retendría su propio bucle de recepción ~50 s.
//
// 🔬 MUTACIÓN: llamar a `p.calentar(...)` en línea en vez de en la goroutine ⇒ rojo
// (Warm tarda lo que el fake se quede bloqueado).
func TestWarm_NoBloqueaAlLlamante(t *testing.T) {
	t.Parallel()
	// El emisor se queda dentro UN SEGUNDO y luego sale. El plazo acotado no es
	// cosmética: si el bloqueo fuera indefinido, la mutación que hay que cazar —llamar
	// a `calentar` en línea en vez de en su goroutine— colgaría el test hasta el pánico
	// de los 10 minutos en vez de fallar la aserción, y un cuelgue no dice QUÉ se rompió.
	suelta := make(chan struct{})
	time.AfterFunc(time.Second, func() { close(suelta) })
	cal := &calFake{suelta: suelta}
	p := intakeahead.New(log(t), configSembrada(t, catalogoPublicado), &selFake{}, nuevoSink(),
		intakeahead.WithCalentador(cal))

	inicio := time.Now()
	p.Warm(tenant, "edge-1", sesion, intentcfg.Kind)
	if tardo := time.Since(inicio); tardo > 200*time.Millisecond {
		t.Fatalf("Warm tardó %v con el emisor bloqueado: retiene al bucle Recv del Edge", tardo)
	}
	<-suelta
}

// TestWarm_SinNadaQueHacerNoRevienta: los tres estados en los que no hay calentamiento
// posible y ninguno es un fallo. Un no-op silencioso es la conducta correcta —el
// pipeline sigue funcionando, solo paga el prefill frío como antes de esta tarea—.
func TestWarm_SinNadaQueHacerNoRevienta(t *testing.T) {
	t.Parallel()
	cal := &calFake{}
	for nombre, caso := range map[string]struct {
		pool             *intakeahead.Pool
		tenantID, sesion string
	}{
		"sin calentador cableado": {
			intakeahead.New(log(t), configSembrada(t, catalogoPublicado), &selFake{}, nuevoSink()),
			tenant, sesion,
		},
		"tenant sin catálogo publicado": {
			intakeahead.New(log(t), intentcfg.NewMemoryStore(), &selFake{}, nuevoSink(),
				intakeahead.WithCalentador(cal)),
			tenant, sesion,
		},
		"sin sesión por la que salir": {
			intakeahead.New(log(t), configSembrada(t, catalogoPublicado), &selFake{}, nuevoSink(),
				intakeahead.WithCalentador(cal)),
			tenant, "",
		},
	} {
		t.Run(nombre, func(t *testing.T) {
			caso.pool.Warm(caso.tenantID, "edge-1", caso.sesion, "")
		})
	}
	time.Sleep(50 * time.Millisecond)
	if v := cal.todos(); len(v) != 0 {
		t.Fatalf("se emitieron %d calentamientos sin nada que calentar: %v", len(v), v)
	}
}

// TestWarm_ElErrorDelEmisorNoSePropaga: Warm no devuelve error y no puede tumbar a
// nadie. Lo único que se pierde es el precalentado.
func TestWarm_ElErrorDelEmisorNoSePropaga(t *testing.T) {
	t.Parallel()
	cal := &calFake{err: errors.New("el Edge se fue")}
	p := intakeahead.New(log(t), configSembrada(t, catalogoPublicado), &selFake{}, nuevoSink(),
		intakeahead.WithCalentador(cal))
	p.Warm(tenant, "edge-1", sesion, intentcfg.Kind)
	esperaCalentamientos(t, cal, 1)
}

// TestWarm_SoloCalientaLoQueCAMBIA_ELPREFIJO.
//
// 🔴 EL CASO `jwks` ES EL QUE PAGA ESTE TEST. El gateway empuja TRES kinds de config
// (jwks, intents, filters) y solo `intents` forma el prompt. Sin este filtro, cada
// rotación de una JWKS —que no cambia un solo byte del prefijo— costaría un prefill
// FRÍO de ~50 s de la plaza única en la máquina de CADA cliente, y el síntoma sería un
// Edge inexplicablemente ocupado justo después de una rotación de claves.
//
// El kind vacío SÍ calienta y es la otra mitad: es el aviso del handshake, donde no se
// publicó nada porque lo que pasó es que el Edge no tiene nada cacheado todavía.
//
// 🔬 MUTACIÓN: quitar la guarda `if kind != "" && kind != intentcfg.Kind` ⇒ rojo.
func TestWarm_SoloCalientaLoQueCAMBIA_ELPREFIJO(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind     string
		calienta bool
	}{
		{"", true},             // handshake: nada cacheado
		{intentcfg.Kind, true}, // el catálogo ES el prefijo
		{"jwks", false},        // rotación de claves: el prompt ni se entera
		{"filters", false},     // perfiles de sesión: tampoco
	} {
		t.Run("kind="+tc.kind, func(t *testing.T) {
			cal := &calFake{}
			p := intakeahead.New(log(t), configSembrada(t, catalogoPublicado), &selFake{}, nuevoSink(),
				intakeahead.WithCalentador(cal))
			p.Warm(tenant, "edge-1", sesion, tc.kind)
			if tc.calienta {
				esperaCalentamientos(t, cal, 1)
				return
			}
			time.Sleep(50 * time.Millisecond)
			if v := cal.todos(); len(v) != 0 {
				t.Fatalf("un ConfigUpdate de kind %q disparó %d calentamientos: ese kind no toca el "+
					"prompt, así que son ~50 s de la plaza única del cliente por nada", tc.kind, len(v))
			}
		})
	}
}

// TestWarm_SeApagaPorCONFIGURACION_SinRecompilar es el requisito de MÉTODO del criterio
// (a) de T1.7-4: el A/B tiene que poder hacerse EN LA MISMA TANDA.
//
// 🔴 SI APAGARLO EXIGIERA RECOMPILAR, EL CRITERIO NO SE PUEDE EJERCER: harían falta dos
// binarios y lo que se compararía serían dos despliegues, no el efecto del
// calentamiento. Por eso el interruptor es un dato del Pool y no una constante.
//
// La primera mitad —encendido por defecto— es la que evita que el «arreglo» sea apagarlo
// todo: un interruptor que hay que acordarse de encender acaba apagado en producción.
//
// 🔬 MUTACIÓN: quitar `!p.calentamientoOn` de la guarda de Warm ⇒ rojo.
func TestWarm_SeApagaPorCONFIGURACION_SinRecompilar(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		nombre   string
		opts     []intakeahead.Option
		calienta bool
	}{
		{"por defecto ENCENDIDO (New lo materializa)", nil, true},
		{"encendido explícito", []intakeahead.Option{intakeahead.WithCalentamiento(true)}, true},
		{"apagado: nadie precalienta y la primera inferencia paga el prefill frío",
			[]intakeahead.Option{intakeahead.WithCalentamiento(false)}, false},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			cal := &calFake{}
			opts := append([]intakeahead.Option{intakeahead.WithCalentador(cal)}, tc.opts...)
			p := intakeahead.New(log(t), configSembrada(t, catalogoPublicado), &selFake{}, nuevoSink(), opts...)
			p.Warm(tenant, "edge-1", sesion, intentcfg.Kind)
			if tc.calienta {
				esperaCalentamientos(t, cal, 1)
				return
			}
			time.Sleep(50 * time.Millisecond)
			if v := cal.todos(); len(v) != 0 {
				t.Fatalf("se emitieron %d calentamientos con el interruptor APAGADO", len(v))
			}
		})
	}
}
