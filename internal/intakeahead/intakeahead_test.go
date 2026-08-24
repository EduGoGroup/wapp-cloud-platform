package intakeahead_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakeahead"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
)

// ---------------------------------------------------------------------------
// Banco de pruebas
// ---------------------------------------------------------------------------

const (
	tenant  = "t-ahead"
	sesion  = "s-ahead"
	cliente = "c-ahead"
	evento  = "e-ahead"
)

// catalogoPublicado es el catálogo tal y como está PUBLICADO EN CAMPO (versión
// db60b90651d5, T1.3): `intake_request` con `params: []` — la lista VACÍA — y sus
// ejemplos. No se «arregla» aquí: esa forma es D-044.20 y es la correcta.
const catalogoPublicado = `{
  "version": "v-campo",
  "umbral_confianza": 0.6,
  "vocabulario": ["sillas", "mesas"],
  "intents": [
    {
      "name": "intake_request",
      "descripcion": "El cliente pide un presupuesto o hace un pedido",
      "params": [],
      "ejemplos": [
        {"mensaje": "quiero 200 sillas para el sábado"},
        {"mensaje": "me pasas precio de 3 mesas"}
      ]
    },
    {
      "name": "consulta",
      "descripcion": "El cliente pregunta algo que no es un pedido",
      "params": [],
      "ejemplos": [{"mensaje": "a qué hora abren"}]
    }
  ]
}`

func clave() intake.WindowKey {
	return intake.WindowKey{TenantID: tenant, SessionID: sesion, ContactID: cliente, EventID: evento}
}

func log(t *testing.T) logger.Logger {
	t.Helper()
	return logger.New(logger.WithWriter(io.Discard))
}

func configSembrada(t *testing.T, blob string) *intentcfg.MemoryStore {
	t.Helper()
	st := intentcfg.NewMemoryStore()
	if err := st.Upsert(context.Background(), tenant, "v-campo", []byte(blob)); err != nil {
		t.Fatalf("sembrar catálogo: %v", err)
	}
	return st
}

// provFake es un llm.LLMProvider de mentira. Solo ClassifyRequest hace algo: las
// otras cuatro etapas no las llama este paquete y devolver un error explícito es lo
// que haría ruido si algún día alguien las llamara desde aquí por error.
type provFake struct {
	mu sync.Mutex
	// respuestas se consumen en orden; la última se repite si hay más llamadas.
	respuestas []respuesta
	llamadas   []llamada
	// antesDeResponder, si no es nil, se ejecuta DENTRO de la llamada: es el gancho
	// con el que un test bloquea la inferencia para observar la concurrencia.
	antesDeResponder func()
}

type respuesta struct {
	raw string
	err error
}

type llamada struct {
	in   llm.ClassifyRequestInput
	temp float64
}

func (p *provFake) ClassifyRequest(_ context.Context, in llm.ClassifyRequestInput, opts llm.Options) (json.RawMessage, error) {
	p.mu.Lock()
	n := len(p.llamadas)
	p.llamadas = append(p.llamadas, llamada{in: in, temp: opts.Temperature})
	gancho := p.antesDeResponder
	p.mu.Unlock()
	if gancho != nil {
		gancho()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.respuestas) == 0 {
		return nil, errors.New("provFake sin respuestas configuradas")
	}
	r := p.respuestas[min(n, len(p.respuestas)-1)]
	return json.RawMessage(r.raw), r.err
}

// setGancho cambia el gancho BAJO EL CANDADO: el worker lo lee desde su goroutine y
// escribirlo a pelo desde la del test es una carrera que `-race` caza.
func (p *provFake) setGancho(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.antesDeResponder = f
}

func (p *provFake) veces() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.llamadas)
}

func (p *provFake) ultima() llamada {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.llamadas[len(p.llamadas)-1]
}

func (p *provFake) ExtractMainIdeas(context.Context, llm.ExtractMainIdeasInput, llm.Options) (json.RawMessage, error) {
	return nil, errors.New("el adelanto no llama a P2")
}

func (p *provFake) ExtractItemSpecs(context.Context, llm.ExtractItemSpecsInput, llm.Options) (json.RawMessage, error) {
	return nil, errors.New("el adelanto no llama a P3")
}

func (p *provFake) NormalizeQuantities(context.Context, llm.NormalizeQuantitiesInput, llm.Options) (json.RawMessage, error) {
	return nil, errors.New("el adelanto no llama a P4")
}

func (p *provFake) GenerateQuoteText(context.Context, llm.GenerateQuoteTextInput, llm.Options) (json.RawMessage, error) {
	return nil, errors.New("el adelanto no llama a P5")
}

// selFake es el selector de vía. Cuenta las veces que se le pide provider y con qué
// sesión de origen: la vía local la usa para elegir el stream, así que perderla sería
// perder el enrutado.
type selFake struct {
	prov    llm.LLMProvider
	err     error
	mu      sync.Mutex
	origins []string
}

func (s *selFake) For(_ context.Context, _, originSessionID string) (llm.LLMProvider, error) {
	s.mu.Lock()
	s.origins = append(s.origins, originSessionID)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.prov, nil
}

func (s *selFake) veces() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.origins)
}

func (s *selFake) origen(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.origins[i]
}

// sinkFake recoge lo clasificado.
type sinkFake struct {
	mu    sync.Mutex
	recib []recibido
	ch    chan struct{}
}

type recibido struct {
	key   intake.WindowKey
	name  string
	conf  float64
	orden int
}

func nuevoSink() *sinkFake { return &sinkFake{ch: make(chan struct{}, 64)} }

func (s *sinkFake) OnClassified(key intake.WindowKey, intent string, confidence float64) {
	s.mu.Lock()
	s.recib = append(s.recib, recibido{key: key, name: intent, conf: confidence, orden: len(s.recib)})
	s.mu.Unlock()
	s.ch <- struct{}{}
}

func (s *sinkFake) todo() []recibido {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recibido(nil), s.recib...)
}

// esperaUno bloquea hasta que el sink recibe algo o vence el plazo.
func (s *sinkFake) esperaUno(t *testing.T) recibido {
	t.Helper()
	select {
	case <-s.ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("el sink no recibió ninguna clasificación en 3 s")
	}
	todo := s.todo()
	return todo[len(todo)-1]
}

// artefacto arma un artefacto P1 válido.
func artefacto(intent string, conf float64, evidencia string, params map[string]string) string {
	b, err := json.Marshal(map[string]any{
		"version": 1, "intent": intent, "confidence": conf,
		"evidence": evidencia, "params": params,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// arranca monta el pool, lo pone a correr y devuelve sus piezas. El ctx se cancela al
// terminar el test, que es lo que para los workers.
func arranca(t *testing.T, cfg intakeahead.ConfigStore, sel intakeahead.ProviderSelector,
	sink intakeahead.Sink, opts ...intakeahead.Option) *intakeahead.Pool {
	t.Helper()
	p := intakeahead.New(log(t), cfg, sel, sink, opts...)
	ctx, cancel := context.WithCancel(context.Background())
	listo := make(chan struct{})
	go func() {
		defer close(listo)
		p.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-listo:
		case <-time.After(3 * time.Second):
			t.Errorf("Run no volvió tras cancelar el contexto: los workers no respetan ctx.Done()")
		}
	})
	return p
}

// ---------------------------------------------------------------------------
// El camino feliz y la forma del prompt
// ---------------------------------------------------------------------------

// TestPide_ClasificaYEntrega es el camino completo de T1.6-4: llega un texto, se pide
// P1 por la vía configurada y la clasificación llega al sink.
//
// Fija además las DOS cosas del prompt que el criterio de la tarea nombra:
//
//   - el catálogo sale de `intent_configs` TAL CUAL, con su `params: []` (D-044.20) y
//     su vocabulario;
//   - la etiqueta de escape es la reservada del contrato (`intents.ReservedUnknown`),
//     que NO está declarada como intención y no puede estarlo.
func TestPide_ClasificaYEntrega(t *testing.T) {
	prov := &provFake{respuestas: []respuesta{{
		raw: artefacto("intake_request", 0.91, "quiero 200 sillas", nil),
	}}}
	sel := &selFake{prov: prov}
	sink := nuevoSink()
	p := arranca(t, configSembrada(t, catalogoPublicado), sel, sink)

	p.Request(clave(), "Hola, quiero 200 sillas para el sábado")
	got := sink.esperaUno(t)

	if got.name != "intake_request" || got.conf != 0.91 {
		t.Fatalf("el sink debe recibir la clasificación tal cual: %+v", got)
	}
	if got.key != clave() {
		t.Fatalf("la clasificación debe volver con SU ventana: %+v", got.key)
	}

	ultima := prov.ultima()
	assertPromptDelCatalogoPublicado(t, ultima.in)
	if sel.origen(0) != sesion {
		t.Fatalf("la sesión de origen debe viajar al selector (la vía local elige stream con ella), got %q", sel.origen(0))
	}
	if ultima.temp != llm.TemperatureGreedy {
		t.Fatalf("la primera pasada va greedy (%v), got %v", llm.TemperatureGreedy, ultima.temp)
	}
}

// assertPromptDelCatalogoPublicado comprueba que el prompt se armó desde
// `intent_configs` y NADA se retocó por el camino.
func assertPromptDelCatalogoPublicado(t *testing.T, in llm.ClassifyRequestInput) {
	t.Helper()
	if in.Text != "Hola, quiero 200 sillas para el sábado" {
		t.Fatalf("el prompt debe llevar el texto del cliente tal cual: %q", in.Text)
	}
	if len(in.Catalog) != 2 {
		t.Fatalf("el catálogo del prompt debe traer las DOS intenciones publicadas: %+v", in.Catalog)
	}
	primera := in.Catalog[0]
	if primera.Name != "intake_request" || primera.Description == "" {
		t.Fatalf("la intención debe viajar con nombre y descripción: %+v", primera)
	}
	if len(primera.Params) != 0 {
		t.Fatalf("D-044.20: el intent publicado declara `params: []` y así debe viajar, sin rellenar nada; got %+v",
			primera.Params)
	}
	if len(primera.Examples) != 2 || primera.Examples[0].Message == "" {
		t.Fatalf("los ejemplos (few-shot) deben viajar: pesan más que las instrucciones en un modelo de 1-2B; got %+v",
			primera.Examples)
	}
	assertEtiquetaDeEscape(t, in)
	if len(in.Vocabulary) != 2 {
		t.Fatalf("el vocabulario del tenant debe anclar la extracción: %+v", in.Vocabulary)
	}
}

// assertEtiquetaDeEscape fija la etiqueta reservada del contrato y —lo que de verdad
// importa— que NO viaja además declarada como una intención más: el contrato lo prohíbe
// y el parser la acepta sin estar en el catálogo.
func assertEtiquetaDeEscape(t *testing.T, in llm.ClassifyRequestInput) {
	t.Helper()
	if in.UnknownLabel != "desconocido" {
		t.Fatalf("la etiqueta de escape debe ser la reservada del contrato, got %q", in.UnknownLabel)
	}
	for _, spec := range in.Catalog {
		if spec.Name == in.UnknownLabel {
			t.Fatalf("la etiqueta reservada NO puede estar declarada como intención del catálogo")
		}
	}
}

// ---------------------------------------------------------------------------
// La ráfaga: las tres preguntas del diseño
// ---------------------------------------------------------------------------

// TestRafaga_CincuentaMensajesNoSonCincuentaInferencias es la respuesta al «¿y si
// llegan 50 mensajes de golpe?».
//
// El cerrojo es «UNA petición EN VUELO por ventana», no «una por ventana», y este test
// fija las dos mitades de esa frase en una sola escena:
//
//  1. mientras la primera inferencia está BLOQUEADA, los 49 mensajes siguientes de la
//     MISMA ventana no encolan nada ⇒ el proveedor se llamó UNA vez;
//  2. en cuanto esa inferencia termina, el cerrojo se suelta y un mensaje nuevo vuelve
//     a preguntar ⇒ la segunda llamada existe.
//
// La mitad (2) no es un detalle: sin ella, el mensaje que abre una ráfaga —casi siempre
// un «hola» que no clasifica nada— se llevaría la única pregunta de la ventana y el
// adelanto quedaría atado al peor candidato.
//
// 💥 MUTACIÓN: cambiar el `default:` del select de Request por un envío bloqueante no
// pone esto rojo (no es lo que mide). Lo que sí lo pone rojo es retirar `marcar/soltar`:
// entonces las 50 peticiones entran a la cola y `veces()` sube muy por encima de 1.
func TestRafaga_CincuentaMensajesNoSonCincuentaInferencias(t *testing.T) {
	suelta := make(chan struct{})
	entro := make(chan struct{}, 1)
	prov := &provFake{respuestas: []respuesta{{raw: artefacto("consulta", 0.5, "hola", nil)}}}
	prov.setGancho(func() {
		select {
		case entro <- struct{}{}:
		default:
		}
		<-suelta
	})
	sink := nuevoSink()
	p := arranca(t, configSembrada(t, catalogoPublicado), &selFake{prov: prov}, sink)

	p.Request(clave(), "hola")
	select {
	case <-entro:
	case <-time.After(3 * time.Second):
		t.Fatalf("la primera inferencia no llegó a arrancar")
	}
	for i := range 49 {
		p.Request(clave(), fmt.Sprintf("mensaje %d de la ráfaga", i))
	}
	if got := prov.veces(); got != 1 {
		t.Fatalf("con una petición EN VUELO, los 49 mensajes siguientes de la MISMA ventana no piden nada; hubo %d inferencias", got)
	}

	// Se suelta la primera inferencia: el cerrojo de la ventana tiene que caer con ella.
	prov.setGancho(nil)
	close(suelta)
	sink.esperaUno(t)

	// Y un mensaje nuevo vuelve a preguntar. El Request se repite dentro del sondeo a
	// propósito: el cerrojo se libera en un `defer` que corre DESPUÉS de entregar al
	// sink, así que el primer intento puede llegar un pelo antes. Repetirlo no falsea
	// nada —si el cerrojo no cayera, ninguno de los intentos pediría nada—.
	esperaHasta(t, func() bool {
		p.Request(clave(), "quiero 200 sillas para el sábado")
		return prov.veces() >= 2
	})
}

// TestVentanasDistintas_NoSeEstorban: el cerrojo es POR VENTANA. Dos conversaciones
// distintas preguntan las dos, y eso es lo que impide que una ráfaga de un tenant deje
// mudos a los demás.
func TestVentanasDistintas_NoSeEstorban(t *testing.T) {
	prov := &provFake{respuestas: []respuesta{{raw: artefacto("intake_request", 0.9, "quiero 200 sillas", nil)}}}
	sink := nuevoSink()
	p := arranca(t, configSembrada(t, catalogoPublicado), &selFake{prov: prov}, sink)

	otra := clave()
	otra.ContactID = "otro-cliente"
	p.Request(clave(), "quiero 200 sillas")
	p.Request(otra, "quiero 200 sillas")

	sink.esperaUno(t)
	sink.esperaUno(t)
	vistas := map[intake.WindowKey]bool{}
	for _, r := range sink.todo() {
		vistas[r.key] = true
	}
	if len(vistas) != 2 {
		t.Fatalf("dos ventanas distintas tienen que preguntar las dos; ventanas atendidas: %d", len(vistas))
	}
}

// TestColaLlena_DescartaElAdelantoYNoBloquea responde al «¿y si no da abasto?».
//
// Con la cola llena, `Request` DESCARTA y vuelve. No bloquea, y esa es la propiedad que
// importa: `Request` corre en línea con el mensaje del cliente (REQ-35, INV-10), así que
// bloquear aquí sería meter la espera de la inferencia en el turno por la puerta de
// atrás. Lo que se pierde al descartar es un ADELANTO; la ventana cierra igual por su
// reloj.
//
// El test se monta con CERO workers a propósito: sin nadie que consuma, la cola se llena
// de verdad y no por una carrera.
func TestColaLlena_DescartaElAdelantoYNoBloquea(t *testing.T) {
	prov := &provFake{respuestas: []respuesta{{raw: artefacto("consulta", 0.5, "hola", nil)}}}
	// Sin Run: nadie consume la cola.
	p := intakeahead.New(log(t), configSembrada(t, catalogoPublicado), &selFake{prov: prov}, nuevoSink(),
		intakeahead.WithQueueSize(2))

	hecho := make(chan struct{})
	go func() {
		defer close(hecho)
		for i := range 20 {
			k := clave()
			k.ContactID = fmt.Sprintf("cliente-%d", i)
			p.Request(k, "quiero 200 sillas")
		}
	}()
	select {
	case <-hecho:
	case <-time.After(3 * time.Second):
		t.Fatalf("Request BLOQUEÓ con la cola llena: eso mete la espera de la inferencia en el turno del cliente (REQ-35)")
	}
	if got := prov.veces(); got != 0 {
		t.Fatalf("sin workers no puede haber corrido ninguna inferencia; hubo %d", got)
	}
}

// TestSinTexto_NoPregunta: un mensaje de solo media no tiene nada que clasificar. Es un
// motivo SANO (REQ-38) y por eso no hay ni inferencia ni aviso.
func TestSinTexto_NoPregunta(t *testing.T) {
	sel := &selFake{prov: &provFake{}}
	p := arranca(t, configSembrada(t, catalogoPublicado), sel, nuevoSink())

	p.Request(clave(), "")
	time.Sleep(50 * time.Millisecond)
	if sel.veces() != 0 {
		t.Fatalf("sin texto no se pide provider ni inferencia; se pidió %d veces", sel.veces())
	}
}

// TestClaveIncompleta_NoPregunta: sin evento vivo no hay ventana que adelantar, así que
// tampoco hay nada que preguntar. Es la misma guarda barata que `Observe`.
func TestClaveIncompleta_NoPregunta(t *testing.T) {
	sel := &selFake{prov: &provFake{}}
	p := arranca(t, configSembrada(t, catalogoPublicado), sel, nuevoSink())

	k := clave()
	k.EventID = ""
	p.Request(k, "quiero 200 sillas")
	time.Sleep(50 * time.Millisecond)
	if sel.veces() != 0 {
		t.Fatalf("una clave sin evento no puede pedir nada; se pidió %d veces", sel.veces())
	}
}

// ---------------------------------------------------------------------------
// REQ-35: el fallo degrada SOLO el adelanto
// ---------------------------------------------------------------------------

// TestViaCaida_NoAdelantaYNoRevienta es REQ-35 escrito como test: con la vía caída
// —aquí, el selector que no puede armar el provider— NO hay pista, no hay pánico y el
// pool sigue vivo para la siguiente petición.
//
// El aviso al dueño NO se comprueba aquí y no es un olvido: lo escribe el decorador de
// `llmvia` (T1.6-6) y este paquete no debe conocer el mapeo error→motivo. Duplicarlo
// aquí sería tener dos vocabularios que un día dirán cosas distintas.
func TestViaCaida_NoAdelantaYNoRevienta(t *testing.T) {
	sel := &selFake{err: errors.New("la vía del tenant no está disponible")}
	sink := nuevoSink()
	p := arranca(t, configSembrada(t, catalogoPublicado), sel, sink)

	p.Request(clave(), "quiero 200 sillas")
	esperaHasta(t, func() bool { return sel.veces() == 1 })
	time.Sleep(50 * time.Millisecond)
	if got := len(sink.todo()); got != 0 {
		t.Fatalf("con la vía caída no puede llegar ninguna pista al agregador; llegaron %d", got)
	}

	// Y el pool sigue vivo: la segunda petición se atiende.
	otra := clave()
	otra.ContactID = "otro"
	p.Request(otra, "quiero 200 sillas")
	esperaHasta(t, func() bool { return sel.veces() == 2 })
}

// TestSinCatalogo_NoGastaUnaInferencia: un tenant sin config de intents publicada es un
// estado NORMAL —hoy lo está la mayoría—, y preguntar sin catálogo es tirar ocho
// segundos de inferencia: `ParseClassification` rechaza TODO artefacto cuando el
// catálogo va vacío, por contrato suyo.
func TestSinCatalogo_NoGastaUnaInferencia(t *testing.T) {
	sel := &selFake{prov: &provFake{}}
	// Store vacío: el tenant no tiene fila.
	p := arranca(t, intentcfg.NewMemoryStore(), sel, nuevoSink())

	p.Request(clave(), "quiero 200 sillas")
	time.Sleep(100 * time.Millisecond)
	if sel.veces() != 0 {
		t.Fatalf("sin catálogo no se pide provider ni inferencia; se pidió %d veces", sel.veces())
	}
}

// TestCatalogoQueNoValida_NoPregunta: el blob se valida al publicarlo, así que un blob
// roto en la tabla significa que alguien escribió la fila por fuera del API. Se dice y
// no se pregunta.
func TestCatalogoQueNoValida_NoPregunta(t *testing.T) {
	sel := &selFake{prov: &provFake{}}
	p := arranca(t, configSembrada(t, `{"version":"","intents":[]}`), sel, nuevoSink())

	p.Request(clave(), "quiero 200 sillas")
	time.Sleep(100 * time.Millisecond)
	if sel.veces() != 0 {
		t.Fatalf("con el catálogo inválido no se pide inferencia; se pidió %d veces", sel.veces())
	}
}

// ---------------------------------------------------------------------------
// REQ-02/REQ-03: el reintento por CALIDAD, y solo uno
// ---------------------------------------------------------------------------

// TestCalidad_ReintentaUnaVezATemperaturaDeReintento fija el contrato del caller: la
// salida no interpretable se reintenta UNA vez a TemperatureRetry, y solo una.
//
// Que el reintento sea del caller no es una elección de este paquete: los dos
// adaptadores lo dicen en su docstring y ninguno reintenta por su cuenta.
func TestCalidad_ReintentaUnaVezATemperaturaDeReintento(t *testing.T) {
	prov := &provFake{respuestas: []respuesta{
		{raw: "esto no es JSON ni de lejos"},
		{raw: artefacto("intake_request", 0.88, "quiero 200 sillas", nil)},
	}}
	sink := nuevoSink()
	p := arranca(t, configSembrada(t, catalogoPublicado), &selFake{prov: prov}, sink)

	p.Request(clave(), "quiero 200 sillas")
	got := sink.esperaUno(t)

	if got.name != "intake_request" {
		t.Fatalf("el reintento debe poder salvar la clasificación: %+v", got)
	}
	if prov.veces() != 2 {
		t.Fatalf("un fallo de calidad se reintenta UNA vez: hubo %d llamadas", prov.veces())
	}
	if temp := prov.ultima().temp; temp != llm.TemperatureRetry {
		t.Fatalf("el reintento va a TemperatureRetry (%v), got %v", llm.TemperatureRetry, temp)
	}
}

// TestCalidad_DosFallosSeguidos_SeRinde: el segundo fallo de calidad seguido no es mala
// suerte, es que el modelo no sabe responder esto. Una tercera pasada solo gasta
// ventana.
func TestCalidad_DosFallosSeguidos_SeRinde(t *testing.T) {
	prov := &provFake{respuestas: []respuesta{{raw: "basura"}}}
	sink := nuevoSink()
	p := arranca(t, configSembrada(t, catalogoPublicado), &selFake{prov: prov}, sink)

	p.Request(clave(), "quiero 200 sillas")
	esperaHasta(t, func() bool { return prov.veces() == 2 })
	time.Sleep(100 * time.Millisecond)

	if got := prov.veces(); got != 2 {
		t.Fatalf("como mucho DOS pasadas (una y su reintento); hubo %d", got)
	}
	if got := len(sink.todo()); got != 0 {
		t.Fatalf("sin clasificación interpretable no llega ninguna pista; llegaron %d", got)
	}
}

// TestFalloDeVia_NoSeReintenta: el reintento es por CALIDAD, no por vía. Reintentar un
// Edge caído sería pagar dos veces el mismo timeout dentro de la ventana.
func TestFalloDeVia_NoSeReintenta(t *testing.T) {
	prov := &provFake{respuestas: []respuesta{{err: errors.New("el Edge no responde")}}}
	sink := nuevoSink()
	p := arranca(t, configSembrada(t, catalogoPublicado), &selFake{prov: prov}, sink)

	p.Request(clave(), "quiero 200 sillas")
	esperaHasta(t, func() bool { return prov.veces() == 1 })
	time.Sleep(100 * time.Millisecond)

	if got := prov.veces(); got != 1 {
		t.Fatalf("un fallo de VÍA no se reintenta; hubo %d llamadas", got)
	}
	if got := len(sink.todo()); got != 0 {
		t.Fatalf("con la vía caída no llega ninguna pista; llegaron %d", got)
	}
}

// ---------------------------------------------------------------------------
// El saneo contra el texto original
// ---------------------------------------------------------------------------

// TestSaneo_LaEvidenciaInventadaTumbaLaClasificacion es el allowlist haciendo su
// trabajo: si la frase que supuestamente justifica la intención no está en el mensaje,
// el modelo se la inventó y no hay nada que creerle.
//
// 💥 MUTACIÓN: en saneo.go, devolver `true` en vez de `contieneFrase(...)` ⇒ este test
// se pone rojo y el de abajo (el camino feliz) sigue verde, que es lo que prueba que el
// saneo no está simplemente rechazándolo todo.
func TestSaneo_LaEvidenciaInventadaTumbaLaClasificacion(t *testing.T) {
	prov := &provFake{respuestas: []respuesta{{
		raw: artefacto("intake_request", 0.99, "quiero 500 alfombras persas", nil),
	}}}
	sink := nuevoSink()
	p := arranca(t, configSembrada(t, catalogoPublicado), &selFake{prov: prov}, sink)

	p.Request(clave(), "quiero 200 sillas para el sábado")
	esperaHasta(t, func() bool { return prov.veces() >= 1 })
	time.Sleep(100 * time.Millisecond)

	if got := len(sink.todo()); got != 0 {
		t.Fatalf("una evidencia que no está en el mensaje descarta la clasificación entera; llegaron %d pistas", got)
	}
}

// TestSaneo_LaEvidenciaSeComparaSinMayusculasYSinImportarLosBlancos: el modelo
// capitaliza a su gusto y copia con saltos de línea donde el original tenía un espacio.
// Eso NO es inventarse nada y tiene que pasar.
func TestSaneo_LaEvidenciaSeComparaSinMayusculasYSinImportarLosBlancos(t *testing.T) {
	prov := &provFake{respuestas: []respuesta{{
		raw: artefacto("intake_request", 0.93, "QUIERO   200\nSILLAS", nil),
	}}}
	sink := nuevoSink()
	p := arranca(t, configSembrada(t, catalogoPublicado), &selFake{prov: prov}, sink)

	p.Request(clave(), "Hola, quiero 200 sillas para el sábado")
	if got := sink.esperaUno(t); got.name != "intake_request" {
		t.Fatalf("mayúsculas y blancos no son una invención: la evidencia debe pasar; got %+v", got)
	}
}

// TestSaneo_ElAcentoNoSeNormaliza_YEsUnaDECISION documenta el lado del que se decidió
// errar, porque no es obvio y el día que alguien lo cambie tiene que enterarse de lo que
// está eligiendo.
//
// La evidencia es, por contrato, una COPIA LITERAL. Un modelo que escribe «sabado»
// donde el cliente escribió «sábado» está reescribiendo, no copiando. El coste es real
// —alguna evidencia legítima se rechaza— y el beneficio es que el allowlist sirve para
// algo. Rechazar aquí solo significa NO ADELANTAR, que es lo mismo que pasa hoy cuando
// no llega ninguna señal (REQ-35); el error caro es el contrario.
func TestSaneo_ElAcentoNoSeNormaliza_YEsUnaDECISION(t *testing.T) {
	prov := &provFake{respuestas: []respuesta{{
		raw: artefacto("intake_request", 0.95, "200 sillas para el sabado", nil),
	}}}
	sink := nuevoSink()
	p := arranca(t, configSembrada(t, catalogoPublicado), &selFake{prov: prov}, sink)

	p.Request(clave(), "quiero 200 sillas para el sábado")
	esperaHasta(t, func() bool { return prov.veces() >= 1 })
	time.Sleep(100 * time.Millisecond)

	if got := len(sink.todo()); got != 0 {
		t.Fatalf("DECISIÓN VIVA: el saneo NO normaliza acentos, así que «sabado» por «sábado» se rechaza. "+
			"Si has hecho que pase a propósito, cambia también el docstring de saneo.go; llegaron %d pistas", got)
	}
}

// TestSaneo_LosParamsInventadosSeCaenYLaIntencionSOBREVIVE es la otra mitad de la regla,
// y la asimetría es deliberada: la evidencia sostiene la respuesta, los params no.
//
// 🔴 Y hay que decirlo aquí porque el test parece afirmar más de lo que afirma: la
// política de disparo NI SIQUIERA MIRA los params (D-044.20) y el catálogo publicado
// declara `params: []`, así que hoy lo único que consume este saneo es un contador del
// log. Lo que este test fija es que un param inventado NO puede tumbar una intención
// buena — que es lo que pasaría si se le aplicara la regla de la evidencia.
func TestSaneo_LosParamsInventadosSeCaenYLaIntencionSOBREVIVE(t *testing.T) {
	prov := &provFake{respuestas: []respuesta{{
		raw: artefacto("intake_request", 0.9, "quiero 200 sillas", map[string]string{
			"producto":  "alfombras persas", // no está en el texto: se cae
			"cantidad":  "200",              // sí está: sobrevive
			"sin_valor": "",                 // vacío = «el cliente no lo dijo»: pasa
		}),
	}}}
	sink := nuevoSink()
	p := arranca(t, configSembrada(t, catalogoPublicado), &selFake{prov: prov}, sink)

	p.Request(clave(), "quiero 200 sillas para el sábado")
	got := sink.esperaUno(t)
	if got.name != "intake_request" || got.conf != 0.9 {
		t.Fatalf("un param inventado NO puede tumbar una intención con evidencia buena; got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Nil-safety y ciclo de vida
// ---------------------------------------------------------------------------

// TestPoolAMedioCablear_EsUnNoOpSeguro: un arranque parcial no puede tumbar el turno de
// nadie. Sin sink, sin selector o sin config el pool descarta y `Run` vuelve.
func TestPoolAMedioCablear_EsUnNoOpSeguro(t *testing.T) {
	casos := map[string]*intakeahead.Pool{
		"sin config":   intakeahead.New(log(t), nil, &selFake{}, nuevoSink()),
		"sin selector": intakeahead.New(log(t), intentcfg.NewMemoryStore(), nil, nuevoSink()),
		"sin sink":     intakeahead.New(log(t), intentcfg.NewMemoryStore(), &selFake{}, nil),
	}
	for nombre, p := range casos {
		t.Run(nombre, func(t *testing.T) {
			p.Request(clave(), "quiero 200 sillas")
			hecho := make(chan struct{})
			go func() {
				defer close(hecho)
				p.Run(context.Background())
			}()
			select {
			case <-hecho:
			case <-time.After(time.Second):
				t.Fatalf("Run de un pool a medio cablear debe volver en el acto, no quedarse esperando")
			}
		})
	}
}

// TestElLogNoLlevaElTextoDelCliente es INV-6 puesto donde se puede romper solo: este
// paquete es el ÚNICO del pipeline que tiene el literal del cliente en memoria fuera de
// un sobre cifrado, así que un `"texto", pet.texto` de más en cualquier log de aquí es
// una fuga de PII directa al fichero de log del VPS.
//
// Se comprueba sobre el FUENTE y no sobre una corrida porque el fallo que importa es
// «alguien añade un campo de log nuevo», y una corrida solo ejercita las ramas que ese
// día pasan.
func TestElLogNoLlevaElTextoDelCliente(t *testing.T) {
	// Lo que NUNCA puede aparecer en la MISMA línea que una llamada al log: el texto
	// del cliente, la frase que el modelo copió de él, y los valores de los params.
	prohibidos := []string{"pet.texto", "c.Evidence", "c.Params", "in.Text", ".texto)"}
	for _, f := range []string{"intakeahead.go", "saneo.go"} {
		for n, linea := range strings.Split(leerFuente(t, f), "\n") {
			if !strings.Contains(linea, "p.log.") {
				continue
			}
			for _, mal := range prohibidos {
				if strings.Contains(linea, mal) {
					t.Fatalf("%s:%d — %q en una línea de log es PII del cliente en el fichero de log del VPS (INV-6):\n\t%s",
						f, n+1, mal, strings.TrimSpace(linea))
				}
			}
		}
	}
}

// TestLosLogsMultilineaTampoco cierra el hueco del test de arriba: una llamada al log
// partida en varias líneas —que es como se escriben aquí, con los pares clave/valor
// debajo— deja los valores en líneas SIN `p.log.`, donde el barrido de arriba no mira.
//
// Se cubre desde el otro lado: los identificadores prohibidos solo pueden aparecer en
// las líneas donde de verdad se usan, y esas están contadas.
func TestLosLogsMultilineaTampoco(t *testing.T) {
	usosLegitimos := map[string]int{
		// El texto del cliente: al armar el prompt y al sanear. Y punto.
		"pet.texto": 2,
		// La evidencia: SOLO en el saneo, que la compara contra el texto.
		"c.Evidence": 1,
		// Los params: SOLO en el saneo, que los recorre y descarta los inventados.
		"c.Params": 2,
	}
	junto := codigoSinComentarios(t, "intakeahead.go") + codigoSinComentarios(t, "saneo.go")
	for ident, esperados := range usosLegitimos {
		if got := strings.Count(junto, ident); got != esperados {
			t.Fatalf("%q aparece %d veces y se esperaban %d. Si has añadido un uso legítimo, súbelo aquí "+
				"—y comprueba antes que NO es un campo de log: este paquete es el único del pipeline con el "+
				"literal del cliente fuera de un sobre cifrado (INV-6)", ident, got, esperados)
		}
	}
}

// ---------------------------------------------------------------------------
// Utilidades del banco
// ---------------------------------------------------------------------------

// esperaHasta sondea `cond` hasta 3 s. Sondear —y no dormir un rato fijo— es lo que
// evita que el test dependa de lo rápida que vaya la máquina.
func esperaHasta(t *testing.T, cond func() bool) {
	t.Helper()
	limite := time.Now().Add(3 * time.Second)
	for time.Now().Before(limite) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("la condición no se cumplió en 3 s")
}

// codigoSinComentarios devuelve el fuente SIN las líneas de comentario. Contar sobre
// el fichero entero ataría el test a la redacción de los docstrings: mencionar
// `c.Params` en una frase subiría el contador y pondría rojo un test que no vigila la
// prosa.
func codigoSinComentarios(t *testing.T, nombre string) string {
	t.Helper()
	var b strings.Builder
	for _, l := range strings.Split(leerFuente(t, nombre), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "//") {
			continue
		}
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}

// leerFuente lee un fichero de ESTE paquete. El test que lo usa mira el código, no una
// corrida: el fallo que persigue es «alguien añade un campo de log nuevo», y una
// corrida solo ejercita las ramas que ese día pasan.
func leerFuente(t *testing.T, nombre string) string {
	t.Helper()
	// El silenciado de gosec de la línea de abajo: `nombre` sale de una lista LITERAL
	// escrita en este mismo fichero (los dos .go del paquete). No hay entrada de
	// usuario por ningún lado, y no la puede haber: es un test que lee su propio código.
	b, err := os.ReadFile(nombre) //nolint:gosec // ruta literal del propio paquete
	if err != nil {
		t.Fatalf("leer %s: %v", nombre, err)
	}
	return string(b)
}
