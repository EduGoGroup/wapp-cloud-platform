package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
)

// --- la fuente durable: el lector de líneas del pedido -----------------------

// peticionDeLineas es una llamada al lector, guardada para poder comprobar CON QUÉ
// CLAVE se pidieron las líneas.
type peticionDeLineas struct{ tenantID, sessionID, contactID string }

// lectorFake suplanta la fuente durable (`intake_items` de la solicitud abierta).
type lectorFake struct {
	lineas  []events.SummaryLine
	falla   error
	pedidas []peticionDeLineas
}

func (l *lectorFake) OpenIntakeLines(_ context.Context, tenantID, sessionID, contactID string) ([]events.SummaryLine, error) {
	l.pedidas = append(l.pedidas, peticionDeLineas{tenantID, sessionID, contactID})
	if l.falla != nil {
		return nil, l.falla
	}
	return l.lineas, nil
}

// lineasDelPedido son dos líneas de precio distinto: lo justo para comprobar que
// cada una conserva SU precio y no, por ejemplo, el de la última.
var lineasDelPedido = []events.SummaryLine{
	{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5},
	{SKU: "TE", Label: "Té", Qty: 1, UnitPrice: 2.0},
}

// respuestasFake suplanta la otra fuente durable (`survey_results`). Guarda el
// Event con el que se le preguntó, que es lo que permite comprobar que el puerto
// recibe con qué acotar la encuesta.
type respuestasFake struct {
	respuestas []events.SummaryAnswer
	falla      error
	pedidas    []events.Event
}

func (r *respuestasFake) SurveyAnswers(_ context.Context, ev events.Event) ([]events.SummaryAnswer, error) {
	r.pedidas = append(r.pedidas, ev)
	if r.falla != nil {
		return nil, r.falla
	}
	return r.respuestas, nil
}

// respuestasDadas llegan como las entrega la tabla: EN EL ORDEN EN QUE SE
// RESPONDIERON —no alfabético— y con `q_edad` DOS VECES, que es lo que deja una
// tabla append-only cuando el cliente rehace una respuesta. El resumen tiene que
// quedarse con la última («3») y ordenarlas.
var respuestasDadas = []events.SummaryAnswer{
	{QuestionID: "q_zona", AnswerCode: "norte"},
	{QuestionID: "q_edad", AnswerCode: "2"},
	{QuestionID: "q_medio", AnswerCode: "whatsapp"},
	{QuestionID: "q_edad", AnswerCode: "3"},
}

// eventoEncuesta es el evento de una encuesta, con lo que hace falta para acotarla
// en una tabla que no tiene ni `session_id` ni `event_id`: el flujo congelado y el
// instante de nacimiento.
func eventoEncuesta() events.Event {
	ev := eventoCarrito()
	ev.Kind = "survey"
	ev.FlowID = "flujo-encuesta"
	ev.FlowVersion = 7
	ev.CreatedAt = time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	return ev
}

// eventoCarrito es el evento que se abandona: un carrito de una conversación
// concreta (tenant, sesión, contacto — la identidad completa, REQ-18).
func eventoCarrito() events.Event {
	return events.Event{
		ID: "ev-1", TenantID: "t-1", SessionID: "s-1", ContactID: "c-1", Kind: "cart",
	}
}

// --- el nivel, que sí sale de flow_state.vars --------------------------------

// catalogoDePrueba es un catálogo mínimo para conducir el módulo cart real.
const catalogoDePrueba = `{
  "categories": [
    {"code": "1", "label": "Bebidas", "items": [
      {"code": "1", "sku": "CAFE", "label": "Café", "price": 2.5, "description": "Espresso doble"},
      {"code": "2", "sku": "TE",   "label": "Té",   "price": 2.0, "description": "Verde o negro"}
    ]}
  ]
}`

// varsDelCarritoReal conduce el módulo cart REAL hasta dejar la sub-máquina en el
// nivel «agregar más / finalizar», y devuelve el flow_state.vars resultante.
//
// Conducir el módulo en vez de escribir el JSON a mano es lo que hace que el
// nivel esté de verdad fijado: `events` redeclara la clave "cart" y el tag
// "level" porque no puede importar el módulo (sería invertir la dirección de la
// capa genérica), y un JSON escrito a mano solo probaría que el decodificador lee
// lo que el propio test escribió.
func varsDelCarritoReal(t *testing.T) map[string]any {
	t.Helper()

	var raw map[string]any
	if err := json.Unmarshal([]byte(catalogoDePrueba), &raw); err != nil {
		t.Fatalf("catálogo de prueba ilegible: %v", err)
	}
	// L1 categorías → Bebidas; L2 → Café; L3 → «Agregar al pedido»; L4 → cantidad 2.
	// Queda en L5 (continue).
	return pasosDelCarrito(t, map[string]any{modules.VarContentRaw: raw}, "1", "1", "2", "2")
}

// pasosDelCarrito aplica una secuencia de entradas al módulo cart encadenando los
// Vars de cada Step, igual que hace el engine entre dos mensajes de WhatsApp.
func pasosDelCarrito(t *testing.T, vars map[string]any, entradas ...string) map[string]any {
	t.Helper()
	m := cart.New()
	for i, in := range entradas {
		res := m.Step(model.Node{}, model.Conversation{Vars: vars}, in)
		if res.Vars == nil {
			t.Fatalf("paso %d (entrada %q): el módulo cart no devolvió Vars", i, in)
		}
		vars = res.Vars
	}
	return vars
}

// TestCartLevelFromVars_LoQueProduceElModuloReal fija el único dato que el resumen
// toma de flow_state.vars, contra el módulo que lo escribe.
func TestCartLevelFromVars_LoQueProduceElModuloReal(t *testing.T) {
	if got := events.CartLevelFromVars(varsDelCarritoReal(t)); got != "continue" {
		t.Errorf("nivel = %q, quiero %q (¿cambió la sub-máquina del carrito?)", got, "continue")
	}
	// Y el caso del RESCATE, que es el normal: el flow_state previo ya se borró.
	if got := events.CartLevelFromVars(nil); got != "" {
		t.Errorf("sin flow_state el nivel es %q, quiero vacío", got)
	}
}

// --- T3.3: el resumen determinista -------------------------------------------

// TestLoadSummary_ElPedidoSeResumeConSusLineasYSusPrecios es el criterio literal
// de T3.3: un pedido con dos líneas produce un resumen con ESAS dos líneas y sus
// precios originales, más el nivel de la sub-máquina.
func TestLoadSummary_ElPedidoSeResumeConSusLineasYSusPrecios(t *testing.T) {
	lector := &lectorFake{lineas: lineasDelPedido}

	s, err := events.LoadSummary(context.Background(), events.SummarySources{Lines: lector}, eventoCarrito(), varsDelCarritoReal(t))
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}

	if s.Kind != "cart" {
		t.Errorf("Kind = %q, quiero %q", s.Kind, "cart")
	}
	if s.Level != "continue" {
		t.Errorf("Level = %q, quiero %q", s.Level, "continue")
	}
	if len(s.Lines) != len(lineasDelPedido) {
		t.Fatalf("líneas = %d, quiero %d (%+v)", len(s.Lines), len(lineasDelPedido), s.Lines)
	}
	for i, q := range lineasDelPedido {
		if s.Lines[i] != q {
			t.Errorf("línea %d = %+v, quiero %+v", i, s.Lines[i], q)
		}
	}

	// Y el precio COPIADO llega hasta lo que el cliente lee.
	quiero := "Esto es lo que ya habías decidido en tu pedido:\n" +
		"Café x2  $5.00\n" +
		"Té x1  $2.00\n" +
		"TOTAL  $7.00\n" +
		"Te quedaste decidiendo si agregar algo más."
	if got := s.Render(); got != quiero {
		t.Errorf("Render():\n%s\n--- quiero ---\n%s", got, quiero)
	}
}

// TestLoadSummary_PideLasLineasConLaClaveDelEvento fija la costura con la fuente
// durable: se piden las líneas de ESTA conversación —tenant, SESIÓN y contacto—,
// no las del contacto a secas. Hoy el almacén resuelve la solicitud abierta sin
// sesión; que la sesión llegue hasta el puerto es lo que deja esa asimetría a la
// vista del adaptador en vez de enterrada aquí (REQ-18).
func TestLoadSummary_PideLasLineasConLaClaveDelEvento(t *testing.T) {
	lector := &lectorFake{lineas: lineasDelPedido}

	if _, err := events.LoadSummary(context.Background(), events.SummarySources{Lines: lector}, eventoCarrito(), nil); err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}

	quiero := []peticionDeLineas{{tenantID: "t-1", sessionID: "s-1", contactID: "c-1"}}
	if len(lector.pedidas) != 1 || lector.pedidas[0] != quiero[0] {
		t.Errorf("se pidieron las líneas con %+v, quiero %+v", lector.pedidas, quiero)
	}
}

// TestLoadSummary_SinNivelElResumenConservaSusLineas es el caso del RESCATE: el
// flow_state ya no existe, así que no hay nivel — y el resumen sigue diciendo lo
// único que de verdad importa, que es lo que el cliente había decidido.
func TestLoadSummary_SinNivelElResumenConservaSusLineas(t *testing.T) {
	s, err := events.LoadSummary(context.Background(), events.SummarySources{Lines: &lectorFake{lineas: lineasDelPedido}},
		eventoCarrito(), nil)
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}

	quiero := "Esto es lo que ya habías decidido en tu pedido:\n" +
		"Café x2  $5.00\n" +
		"Té x1  $2.00\n" +
		"TOTAL  $7.00"
	if got := s.Render(); got != quiero {
		t.Errorf("Render():\n%s\n--- quiero ---\n%s", got, quiero)
	}
	if strings.Contains(s.Render(), "Te quedaste") {
		t.Error("sin nivel no se puede decir «te quedaste…»: se lo estaría inventando")
	}
}

// TestLoadSummary_SinLectorNoInventaUnResumenVacio fija la distinción que evita
// borrar del historial lo que el cliente sí decidió: «no hay líneas» y «no pude
// leerlas» no son lo mismo.
func TestLoadSummary_SinLectorNoInventaUnResumenVacio(t *testing.T) {
	_, err := events.LoadSummary(context.Background(), events.SummarySources{}, eventoCarrito(), nil)
	if !errors.Is(err, events.ErrNoIntakeLineReader) {
		t.Errorf("err = %v, quiero ErrNoIntakeLineReader", err)
	}
}

// TestLoadSummary_ElFalloDelLectorSePropaga: si la fuente durable falla, el
// llamador se entera en vez de recibir un resumen vacío que parecería cierto.
func TestLoadSummary_ElFalloDelLectorSePropaga(t *testing.T) {
	roto := errors.New("la base dijo que no")

	_, err := events.LoadSummary(context.Background(), events.SummarySources{Lines: &lectorFake{falla: roto}}, eventoCarrito(), nil)
	if !errors.Is(err, roto) {
		t.Errorf("err = %v, quiero que envuelva %v", err, roto)
	}
}

// resumenesDePrueba son los casos de la tabla de determinismo. El estado efímero
// viaja EN JSON (se decodifica fresco en cada corrida, para que el orden de
// recorrido del mapa cambie entre corridas) y cada caso trae el texto EXACTO.
//
// El texto exacto no es adorno: comprobar «100 corridas dan lo mismo» pasa en
// verde con un constructor que siempre devuelva "". La igualdad contra un texto
// con las líneas y los precios dentro es lo que obliga a que además diga algo.
var resumenesDePrueba = []struct {
	nombre     string
	kind       string
	lineas     []events.SummaryLine
	respuestas []events.SummaryAnswer
	vars       string
	texto      string
}{
	{
		nombre: "pedido con dos líneas y una indicación",
		kind:   "cart",
		lineas: []events.SummaryLine{
			{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "sin azúcar"},
			{SKU: "FLAN", Label: "Flan", Qty: 1, UnitPrice: 3},
		},
		vars: `{"cart":{"level":"quantity"}}`,
		texto: "Esto es lo que ya habías decidido en tu pedido:\n" +
			"Café x2  $5.00\n" +
			"   ✏️ sin azúcar\n" +
			"Flan x1  $3.00\n" +
			"TOTAL  $8.00\n" +
			"Te quedaste eligiendo la cantidad.",
	},
	{
		// Cuatro filas y TRES preguntas: la rehecha no cuenta dos veces.
		nombre:     "encuesta con tres preguntas respondidas (una rehecha)",
		kind:       "survey",
		respuestas: respuestasDadas,
		vars:       `{}`,
		texto:      "Ya habías respondido 3 preguntas de tu encuesta.",
	},
	{
		nombre:     "encuesta con una sola respuesta",
		kind:       "survey",
		respuestas: []events.SummaryAnswer{{QuestionID: "q_edad", AnswerCode: "2"}},
		vars:       `{}`,
		texto:      "Ya habías respondido 1 pregunta de tu encuesta.",
	},
	{
		nombre: "menú: límite v1, no acumula nada",
		kind:   "menu",
		vars:   `{"menu":{"options":[{"n":1,"a":"start","k":"cart"}]}}`,
		texto:  "",
	},
}

// TestLoadSummary_MismoEstadoMismoResumen es el criterio de determinismo de T3.3:
// el mismo estado produce el mismo string byte a byte en 100 corridas —y el mismo
// payload, que es lo que de verdad se persiste—.
func TestLoadSummary_MismoEstadoMismoResumen(t *testing.T) {
	for _, c := range resumenesDePrueba {
		t.Run(c.nombre, func(t *testing.T) {
			ev := eventoCarrito()
			ev.Kind = c.kind

			var texto string
			var payload []byte
			for corrida := range 100 {
				s, err := events.LoadSummary(context.Background(), events.SummarySources{
					Lines:   &lectorFake{lineas: c.lineas},
					Answers: &respuestasFake{respuestas: c.respuestas},
				}, ev, varsDesdeJSON(t, c.vars))
				if err != nil {
					t.Fatalf("corrida %d: LoadSummary: %v", corrida, err)
				}
				got := s.Render()
				b, err := s.Encode()
				if err != nil {
					t.Fatalf("corrida %d: Encode: %v", corrida, err)
				}
				if corrida == 0 {
					texto, payload = got, b
					continue
				}
				if got != texto {
					t.Fatalf("corrida %d: el texto cambió:\n%s\n--- corrida 0 ---\n%s", corrida, got, texto)
				}
				if string(b) != string(payload) {
					t.Fatalf("corrida %d: el payload cambió:\n%s\n--- corrida 0 ---\n%s", corrida, b, payload)
				}
			}
			// Y lo que se repite 100 veces tiene que ser lo correcto, no el vacío.
			if texto != c.texto {
				t.Errorf("texto:\n%s\n--- quiero ---\n%s", texto, c.texto)
			}
		})
	}
}

// varsDesdeJSON decodifica un flow_state.vars como llega de la columna JSONB:
// map[string]any, con los números en float64. Se llama en CADA corrida a
// propósito, porque el orden de recorrido de un mapa recién construido es
// distinto cada vez y ahí es donde muere un resumen que no ordene.
func varsDesdeJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var vars map[string]any
	if err := json.Unmarshal([]byte(raw), &vars); err != nil {
		t.Fatalf("vars de prueba ilegibles: %v", err)
	}
	return vars
}

// --- la encuesta: su fuente durable y el rescate al vuelo -------------------

// TestLoadSummary_ElRescateAlVueloResumeLaEncuestaSINvars es EL test que decide
// este diseño.
//
// Al rescatar no hay fila persistida que leer y el flow_state previo YA SE BORRÓ:
// el resumen se arma al vuelo con `vars` nil. Si las respuestas salieran de
// `vars["answers"]`, aquí saldría VACÍO —sin error y en verde— justo en el
// escenario que da sentido al plan. Leerlas de `survey_results` es lo único que
// hace funcionar el rescate para los dos tipos.
func TestLoadSummary_ElRescateAlVueloResumeLaEncuestaSINvars(t *testing.T) {
	s, err := events.LoadSummary(context.Background(),
		events.SummarySources{Answers: &respuestasFake{respuestas: respuestasDadas}},
		eventoEncuesta(), nil) // ← vars nil: el rescate
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if s.Empty() {
		t.Fatal("la encuesta rescatada resume VACÍA: es exactamente el fallo que este puerto evita")
	}
	if got := s.Render(); got != "Ya habías respondido 3 preguntas de tu encuesta." {
		t.Errorf("Render() = %q", got)
	}
}

// TestLoadSummary_LaEncuestaSeResumeConSusRespuestasDurables comprueba el detalle:
// cuatro filas, TRES preguntas (la rehecha gana con su ÚLTIMA respuesta) y en
// orden estable aunque la fuente las dé por orden de respuesta.
func TestLoadSummary_LaEncuestaSeResumeConSusRespuestasDurables(t *testing.T) {
	s, err := events.LoadSummary(context.Background(),
		events.SummarySources{Answers: &respuestasFake{respuestas: respuestasDadas}},
		eventoEncuesta(), nil)
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}

	quiero := []events.SummaryAnswer{
		{QuestionID: "q_edad", AnswerCode: "3"},
		{QuestionID: "q_medio", AnswerCode: "whatsapp"},
		{QuestionID: "q_zona", AnswerCode: "norte"},
	}
	if len(s.Answers) != len(quiero) {
		t.Fatalf("respuestas = %d, quiero %d (%+v)", len(s.Answers), len(quiero), s.Answers)
	}
	for i, q := range quiero {
		if s.Answers[i] != q {
			t.Errorf("respuesta %d = %+v, quiero %+v", i, s.Answers[i], q)
		}
	}
}

// TestPersistSummary_LaEncuestaAbandonadaEscribeSuFila es el caso visto desde el
// abandono: UNA fila, con las respuestas dentro y en claro (nivel 1, ADR-0034).
func TestPersistSummary_LaEncuestaAbandonadaEscribeSuFila(t *testing.T) {
	h := &historialFake{}

	seq, escrito, err := events.PersistSummary(context.Background(), h,
		events.SummarySources{Answers: &respuestasFake{respuestas: respuestasDadas}},
		eventoEncuesta(), nil)
	if err != nil {
		t.Fatalf("PersistSummary: %v", err)
	}
	if !escrito || seq != 1 || len(h.filas) != 1 {
		t.Fatalf("escrito=%v seq=%d filas=%d, quiero true, 1 y 1", escrito, seq, len(h.filas))
	}
	quiero := `{"kind":"survey","answers":[` +
		`{"question_id":"q_edad","answer_code":"3"},` +
		`{"question_id":"q_medio","answer_code":"whatsapp"},` +
		`{"question_id":"q_zona","answer_code":"norte"}]}`
	if h.filas[0].body != quiero {
		t.Errorf("payload:\n%s\n--- quiero ---\n%s", h.filas[0].body, quiero)
	}
}

// TestPersistSummary_LaEncuestaSinResponderNoEscribeFila fija la otra mitad, que
// es donde la ola estuvo a punto de diagnosticar al revés: cero respuestas es
// «nada que resumir» —el caso normal—, no una fuente que no llega.
func TestPersistSummary_LaEncuestaSinResponderNoEscribeFila(t *testing.T) {
	h := &historialFake{}

	_, escrito, err := events.PersistSummary(context.Background(), h,
		events.SummarySources{Answers: &respuestasFake{}}, eventoEncuesta(), nil)
	if err != nil {
		t.Fatalf("PersistSummary: %v", err)
	}
	if escrito || len(h.filas) != 0 {
		t.Errorf("sin responder nada escribió %d filas: eso no es un agujero, es «nada que resumir»", len(h.filas))
	}
}

// TestLoadSummary_PideLasRespuestasConElEventoEntero fija POR QUÉ el puerto recibe
// el Event y no tres identificadores: `survey_results` no tiene `session_id` ni
// `event_id`, así que sin el flujo congelado y el instante de nacimiento el
// adaptador no puede separar esta encuesta de la que el mismo contacto respondió
// el mes pasado con el mismo flujo.
func TestLoadSummary_PideLasRespuestasConElEventoEntero(t *testing.T) {
	lector := &respuestasFake{respuestas: respuestasDadas}

	if _, err := events.LoadSummary(context.Background(),
		events.SummarySources{Answers: lector}, eventoEncuesta(), nil); err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}

	if len(lector.pedidas) != 1 {
		t.Fatalf("se preguntó %d veces, quiero 1", len(lector.pedidas))
	}
	if got := lector.pedidas[0]; got != eventoEncuesta() {
		t.Errorf("se preguntó con %+v, quiero el evento entero %+v", got, eventoEncuesta())
	}
}

// TestLoadSummary_SinLectorDeRespuestasNoInventaUnResumenVacio: la misma regla que
// para el pedido. No poder leer no es leer y no encontrar.
func TestLoadSummary_SinLectorDeRespuestasNoInventaUnResumenVacio(t *testing.T) {
	_, err := events.LoadSummary(context.Background(), events.SummarySources{}, eventoEncuesta(), nil)
	if !errors.Is(err, events.ErrNoSurveyAnswerReader) {
		t.Errorf("err = %v, quiero ErrNoSurveyAnswerReader", err)
	}
}

// TestLoadSummary_ElFalloDelLectorDeRespuestasSePropaga.
func TestLoadSummary_ElFalloDelLectorDeRespuestasSePropaga(t *testing.T) {
	roto := errors.New("la base dijo que no")

	_, err := events.LoadSummary(context.Background(),
		events.SummarySources{Answers: &respuestasFake{falla: roto}}, eventoEncuesta(), nil)
	if !errors.Is(err, roto) {
		t.Errorf("err = %v, quiero que envuelva %v", err, roto)
	}
}

// TestLoadSummary_VacioNoSeResume junta los casos en los que NO hay nada que
// resumir. Importan porque T3.4 no debe escribir una fila para ninguno de ellos.
func TestLoadSummary_VacioNoSeResume(t *testing.T) {
	casos := []struct {
		nombre string
		kind   string
		lineas []events.SummaryLine
		vars   map[string]any
	}{
		{"menú: límite v1 documentado", "menu", nil, map[string]any{}},
		{"tipo que no acumula decisiones (media)", "media", nil, map[string]any{}},
		{"pedido abierto y abandonado sin agregar nada", "cart", nil,
			map[string]any{"cart": map[string]any{"level": "articles"}}},
		{"encuesta sin responder aún", "survey", nil, map[string]any{}},
		{"nivel ilegible: el resumen no rompe la conversación", "cart", nil,
			map[string]any{"cart": "esto no es un estado"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			ev := eventoCarrito()
			ev.Kind = c.kind
			s, err := events.LoadSummary(context.Background(), events.SummarySources{
				Lines: &lectorFake{lineas: c.lineas}, Answers: &respuestasFake{}}, ev, c.vars)
			if err != nil {
				t.Fatalf("LoadSummary: %v", err)
			}
			if !s.Empty() {
				t.Errorf("Empty() = false, quiero true (%+v)", s)
			}
			if got := s.Render(); got != "" {
				t.Errorf("Render() = %q, quiero cadena vacía", got)
			}
		})
	}
}

// TestSummaryRender_HablaDePedidoYNuncaDeIdentificadores fija las dos reglas de
// vocabulario que gobiernan todo lo que el cliente lee: el `cart` se llama
// «pedido» (decisión de producto de Jhoan, 2026-08-09) y en el texto no aparece
// jamás un identificador (E-3).
func TestSummaryRender_HablaDePedidoYNuncaDeIdentificadores(t *testing.T) {
	s, err := events.LoadSummary(context.Background(), events.SummarySources{Lines: &lectorFake{lineas: lineasDelPedido}},
		eventoCarrito(), varsDelCarritoReal(t))
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	texto := s.Render()

	if !strings.Contains(texto, "pedido") {
		t.Errorf("el resumen no nombra el pedido:\n%s", texto)
	}
	for _, prohibido := range []string{"carrito", "cart", "ev-1", "c-1", "level", "continue"} {
		if strings.Contains(texto, prohibido) {
			t.Errorf("el resumen le enseña %q al cliente:\n%s", prohibido, texto)
		}
	}
}

// TestSummaryEncode_EstructuraEnClaroSinTotalDerivado fija la forma del payload
// contra el contrato escrito en la migración 0051 («Estructura, no prosa:
// {"lines":[{sku,label,qty,customization}]}»), incluido lo que NO va: el TOTAL no
// se serializa. Es derivado, y dos verdades sobre el mismo importe podrían acabar
// discrepando.
func TestSummaryEncode_EstructuraEnClaroSinTotalDerivado(t *testing.T) {
	s := events.BuildCartSummary(events.CartState{Level: "summary", Lines: []events.SummaryLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "sin azúcar"},
	}})

	b, err := s.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	quiero := `{"kind":"cart","level":"summary","lines":[` +
		`{"sku":"CAFE","label":"Café","qty":2,"unit_price":2.5,"customization":"sin azúcar"}]}`
	if string(b) != quiero {
		t.Errorf("payload:\n%s\n--- quiero ---\n%s", b, quiero)
	}
	if !json.Valid(b) {
		t.Error("el payload no es JSON válido: AppendSummary lo rechazaría")
	}
}

// TestSummaryRender_LaIndicacionNoTocaElTotal fija INV-13 en el renderizador: la
// indicación se ve pegada a la línea que describe y el TOTAL sale de qty ×
// unit_price y de nada más.
func TestSummaryRender_LaIndicacionNoTocaElTotal(t *testing.T) {
	con := events.BuildCartSummary(events.CartState{Level: "continue", Lines: []events.SummaryLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5, Customization: "sin azúcar"},
	}}).Render()
	sin := events.BuildCartSummary(events.CartState{Level: "continue", Lines: []events.SummaryLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5},
	}}).Render()

	if !strings.Contains(con, "\n   ✏️ sin azúcar\n") {
		t.Errorf("la indicación no aparece bajo su línea:\n%s", con)
	}
	if !strings.Contains(con, "TOTAL  $5.00") || !strings.Contains(sin, "TOTAL  $5.00") {
		t.Errorf("el total cambió con la indicación:\ncon:\n%s\nsin:\n%s", con, sin)
	}
}

// TestBuildCartSummary_NoCompartaMemoriaConElEstado comprueba que el resumen es
// una FOTO: quien siga usando las líneas después no puede cambiar lo que el
// resumen ya dijo.
func TestBuildCartSummary_NoCompartaMemoriaConElEstado(t *testing.T) {
	lineas := []events.SummaryLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}}
	s := events.BuildCartSummary(events.CartState{Level: "continue", Lines: lineas})

	lineas[0].Qty = 99
	if s.Lines[0].Qty != 2 {
		t.Errorf("el resumen cambió por detrás: Qty = %d, quiero 2", s.Lines[0].Qty)
	}
}

// --- T3.4: la fila con su marca ---------------------------------------------

// filaResumen es una fila escrita en el historial del evento.
type filaResumen struct {
	eventID string
	seq     int
	body    string
}

// historialFake suplanta al store: numera como él —MAX+1 DENTRO del evento, sin
// huecos— y guarda lo escrito para poder mirarlo. Numerar de verdad es lo que
// permite comprobar que un segundo resumen no pisa al primero sin levantar
// Postgres.
type historialFake struct {
	filas []filaResumen
	falla error
}

func (h *historialFake) AppendSummary(_ context.Context, eventID string, body json.RawMessage) (int, error) {
	if h.falla != nil {
		return 0, h.falla
	}
	seq := 0
	for _, f := range h.filas {
		if f.eventID == eventID && f.seq > seq {
			seq = f.seq
		}
	}
	seq++
	h.filas = append(h.filas, filaResumen{eventID: eventID, seq: seq, body: string(body)})
	return seq, nil
}

// TestPersistSummary_EscribeUnaSolaFilaConElResumen comprueba el abandono normal:
// UNA fila, con el resumen determinista dentro.
func TestPersistSummary_EscribeUnaSolaFilaConElResumen(t *testing.T) {
	h := &historialFake{}

	seq, escrito, err := events.PersistSummary(context.Background(), h,
		events.SummarySources{Lines: &lectorFake{lineas: lineasDelPedido}}, eventoCarrito(), varsDelCarritoReal(t))
	if err != nil {
		t.Fatalf("PersistSummary: %v", err)
	}
	if !escrito || seq != 1 {
		t.Fatalf("escrito=%v seq=%d, quiero true y 1", escrito, seq)
	}
	if len(h.filas) != 1 {
		t.Fatalf("%d filas escritas, quiero exactamente 1", len(h.filas))
	}
	if h.filas[0].eventID != "ev-1" {
		t.Errorf("la fila cuelga del evento %q, quiero %q", h.filas[0].eventID, "ev-1")
	}
	quiero := `{"kind":"cart","level":"continue","lines":[` +
		`{"sku":"CAFE","label":"Café","qty":2,"unit_price":2.5},` +
		`{"sku":"TE","label":"Té","qty":1,"unit_price":2}]}`
	if h.filas[0].body != quiero {
		t.Errorf("payload:\n%s\n--- quiero ---\n%s", h.filas[0].body, quiero)
	}
}

// TestPersistSummary_UnSegundoAbandonoNoPisaAlPrimero es el criterio literal de
// T3.4: un segundo salto añade una segunda fila sin tocar la primera. El
// historial es un registro, no un estado que se sobrescribe.
func TestPersistSummary_UnSegundoAbandonoNoPisaAlPrimero(t *testing.T) {
	h := &historialFake{}
	unaLinea := []events.SummaryLine{{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 2.5}}
	dosLineas := append(append([]events.SummaryLine{}, unaLinea...),
		events.SummaryLine{SKU: "TE", Label: "Té", Qty: 1, UnitPrice: 2})

	primera := persistirOFallar(t, h, unaLinea)
	cuerpoPrimera := h.filas[0].body
	segunda := persistirOFallar(t, h, dosLineas)

	if primera != 1 || segunda != 2 {
		t.Errorf("seqs = %d y %d, quiero 1 y 2", primera, segunda)
	}
	if len(h.filas) != 2 {
		t.Fatalf("%d filas, quiero 2", len(h.filas))
	}
	if h.filas[0].body != cuerpoPrimera {
		t.Errorf("la primera fila cambió:\n%s\n--- era ---\n%s", h.filas[0].body, cuerpoPrimera)
	}
	if strings.Contains(h.filas[0].body, "TE") {
		t.Errorf("la primera fila se contaminó con la línea de la segunda:\n%s", h.filas[0].body)
	}
	if !strings.Contains(h.filas[1].body, "TE") {
		t.Errorf("la segunda fila no trae lo nuevo:\n%s", h.filas[1].body)
	}
}

func persistirOFallar(t *testing.T, h *historialFake, lineas []events.SummaryLine) int {
	t.Helper()
	seq, escrito, err := events.PersistSummary(context.Background(), h,
		events.SummarySources{Lines: &lectorFake{lineas: lineas}}, eventoCarrito(), nil)
	if err != nil || !escrito {
		t.Fatalf("PersistSummary: escrito=%v err=%v", escrito, err)
	}
	return seq
}

// TestPersistSummary_SinNadaQueResumirNoEscribeFila fija la otra mitad de T3.4:
// el historial no se salpica de resúmenes vacíos. La comprobación vive en
// PersistSummary y no en cada llamador, que es donde se olvidaría.
func TestPersistSummary_SinNadaQueResumirNoEscribeFila(t *testing.T) {
	casos := []struct {
		nombre string
		kind   string
	}{
		{"menú (límite v1)", "menu"},
		{"pedido abandonado sin agregar nada", "cart"},
		{"encuesta sin responder", "survey"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			h := &historialFake{}
			ev := eventoCarrito()
			ev.Kind = c.kind

			seq, escrito, err := events.PersistSummary(context.Background(), h,
				events.SummarySources{Lines: &lectorFake{}, Answers: &respuestasFake{}}, ev, nil)
			if err != nil {
				t.Fatalf("PersistSummary: %v", err)
			}
			if escrito || seq != 0 {
				t.Errorf("escrito=%v seq=%d, quiero false y 0", escrito, seq)
			}
			if len(h.filas) != 0 {
				t.Errorf("escribió %d filas, quiero ninguna: %+v", len(h.filas), h.filas)
			}
		})
	}
}

// TestPersistSummary_ElFalloDelHistorialLlegaAlLlamador comprueba que un fallo al
// escribir no se traga: quien abandona el evento tiene que poder decidir qué hacer.
func TestPersistSummary_ElFalloDelHistorialLlegaAlLlamador(t *testing.T) {
	roto := errors.New("la base dijo que no")
	h := &historialFake{falla: roto}

	_, escrito, err := events.PersistSummary(context.Background(), h,
		events.SummarySources{Lines: &lectorFake{lineas: lineasDelPedido}}, eventoCarrito(), nil)

	if escrito {
		t.Error("dice que escribió cuando el historial falló")
	}
	if !errors.Is(err, roto) {
		t.Errorf("err = %v, quiero que envuelva %v", err, roto)
	}
}

// El store REAL tiene que servir de historial para PersistSummary. Se fija en
// TIEMPO DE COMPILACIÓN a propósito: si AppendSummary cambia de firma, esto deja
// de compilar aquí y no se descubre al cablear los tres abandonos.
var _ events.SummaryAppender = (*events.Store)(nil)

// --- cero LLM ----------------------------------------------------------------

// clasificadoresProhibidos son los fragmentos de ruta que delatan una dependencia
// del clasificador o de cualquier LLM. El clasificador corre en el EDGE
// (wapp-edge-intent, ADR-0020) y en la nube su soporte vive en `intentcfg`: nada
// de eso puede aparecer en el camino de importación de este paquete.
var clasificadoresProhibidos = []string{"intent", "llm", "ollama", "openai", "anthropic", "clasific", "classif"}

// TestPaqueteEventsNoDependeDelClasificador es el criterio «ningún camino del
// paquete importa ni invoca el clasificador» (T3.3, REQ-21) resuelto como assert
// de dependencias.
//
// Por qué un assert de dependencias y no un fake que haga t.Fatal: para espiar
// una llamada hace falta una costura por donde inyectarla, y el resumen no la
// tiene —sus constructores reciben datos, y su único puerto lee líneas de una
// tabla—. Lo que hay que impedir es que ALGUIEN LA ABRA, y eso se ve en los
// imports.
//
// Lee los imports con parser.ImportsOnly (no compila ni tipa nada): así el test
// sigue siendo cierto aunque el resto del paquete esté a medias.
func TestPaqueteEventsNoDependeDelClasificador(t *testing.T) {
	entradas, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("leer el directorio del paquete: %v", err)
	}

	fset := token.NewFileSet()
	revisados := 0
	for _, e := range entradas {
		nombre := e.Name()
		if e.IsDir() || !strings.HasSuffix(nombre, ".go") || strings.HasSuffix(nombre, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, nombre, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: leer los imports: %v", nombre, err)
		}
		revisados++
		for _, imp := range f.Imports {
			ruta := strings.ToLower(strings.Trim(imp.Path.Value, `"`))
			for _, prohibido := range clasificadoresProhibidos {
				if strings.Contains(ruta, prohibido) {
					t.Errorf("%s importa %s: el resumen determinista no puede depender del clasificador (REQ-21)",
						nombre, imp.Path.Value)
				}
			}
		}
	}
	if revisados == 0 {
		t.Fatal("no se revisó ni un archivo: el test no está mirando nada")
	}
}
