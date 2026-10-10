//go:build pendiente

package engine_test

// consulta_test.go — EL MECANISMO del re-entry, probado donde vive (Plan 044 · Ola
// 3.5 · T3.5-2).
//
// 🔴 POR QUÉ ESTA BATERÍA ESTÁ AQUÍ Y NO EN EL MÓDULO. Un test que llama a
// Module.Step directamente se salta el engine y no ejerce el re-entry, porque el
// re-entry no está en el módulo. Un mecanismo probado solo desde el módulo sería un
// mecanismo sin probar — y de este cuelga un turno de WhatsApp entero.

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

const (
	typeAsker        = "asker"
	levelUnderTest   = "nivel-de-prueba"
	firstPassScreen  = "pantalla de la PRIMERA pasada"
	firstPassEffect  = "efecto_de_la_PRIMERA_pasada"
	secondPassEffect = "efecto_de_la_segunda_pasada"
	clientText       = "mejor la primera, la de PINO"
)

// pass es lo que el módulo vio en una llamada a Step.
type pass struct {
	text       string
	verdict    modules.Verdict
	hasVerdict bool
}

// asker eleva una consulta mientras no vea un veredicto en sus Vars. Su primera
// pasada declara ADEMÁS una pantalla y un efecto —cosa que un módulo real no debe
// hacer— justo para poder comprobar que el engine los DESCARTA.
type asker struct {
	passes   *[]pass
	stubborn bool     // pide SIEMPRE, también en la segunda pasada (el bug)
	chunks   []string // trozos de la consulta que eleva
	// second, si no es nil, decide el Result de la segunda pasada.
	second func(conv model.Conversation, v modules.Verdict) modules.Result
}

func (asker) Type() string                              { return typeAsker }
func (asker) WaitsForInput() bool                       { return true }
func (asker) ProducesDurableContent() bool              { return false }
func (asker) Render(model.Node, model.Content) []string { return []string{"elige"} }

func (a asker) Step(_ model.Node, conv model.Conversation, input string) modules.Result {
	v, ok := modules.VerdictFrom(conv.Vars)
	*a.passes = append(*a.passes, pass{text: input, verdict: v, hasVerdict: ok})
	if !ok || a.stubborn {
		return modules.Result{
			Vars:    conv.Vars,
			Outputs: []string{firstPassScreen},
			Effects: []modules.Effect{{Kind: "event", Name: firstPassEffect}},
			Query: &modules.Query{
				Class: modules.QueryClassOption, Level: levelUnderTest, Text: input, Chunks: a.chunks,
				Options: []modules.QueryOption{{Code: "1", Label: "Confirmar y finalizar"}},
			},
		}
	}
	if a.second != nil {
		return a.second(conv, v)
	}
	// Devuelve las Vars que recibió, CON el veredicto dentro: no lo limpia.
	if v.ResolvedAny() {
		return modules.Result{Vars: conv.Vars, Outputs: []string{"resuelto:" + v.Code},
			Effects: []modules.Effect{{Kind: "event", Name: secondPassEffect}}}
	}
	return modules.Result{Vars: conv.Vars, Outputs: []string{"degradado:" + string(v.Reason)}}
}

// resolverCall es una llamada al resolutor: con qué identidad y qué consulta.
type resolverCall struct {
	marker          any
	tenant, session string
	query           modules.Query
}

// resolverStub responde lo que le digan, o falla, y anota cada llamada.
type resolverStub struct {
	verdict modules.Verdict
	err     error
	calls   *[]resolverCall
}

func (r resolverStub) ResolveQuery(ctx context.Context, tenantID, sessionID string, q modules.Query) (modules.Verdict, error) {
	if r.calls != nil {
		*r.calls = append(*r.calls, resolverCall{ctx.Value(ctxKey{}), tenantID, sessionID, q})
	}
	return r.verdict, r.err
}

var _ engine.QueryResolver = resolverStub{}

// observations recoge lo que el engine publica por su observador.
type observations [][3]string

func (o *observations) record(class, level, outcome string) {
	*o = append(*o, [3]string{class, level, outcome})
}

func askerFlow() model.Flow {
	return model.Flow{FlowID: "f", Version: 1, Initial: "n1", Nodes: map[string]model.Node{
		"n1":   {Type: typeAsker},
		"done": {Type: model.NodeTypeMessage, Text: "hecho"},
	}}
}

func askerConversation() model.Conversation {
	return model.Conversation{TenantID: "tenant-a", SessionID: "session-9", CurrentNode: "n1", Vars: map[string]any{"session_var": "intacto"}}
}

// askOnce corre un turno del asker con las opciones dadas.
func askOnce(t *testing.T, mod asker, opts ...engine.Option) (model.Conversation, []engine.Output, []modules.Effect, []pass) {
	t.Helper()
	passes := &[]pass{}
	mod.passes = passes
	st, outs, effects, err := newEngine([]modules.Module{mod}, opts...).
		Step(context.Background(), askerFlow(), askerConversation(), engine.Input{Text: clientText})
	if err != nil {
		t.Fatalf("Step: %v (una consulta nunca devuelve error)", err)
	}
	if _, alive := st.Vars[modules.VarQueryVerdict]; alive {
		t.Fatalf("el veredicto quedó vivo en Vars y se persistiría en flow_state: %v", st.Vars)
	}
	return st, outs, effects, *passes
}

// Las etiquetas salen por el observador hacia una métrica: no cambian.
func TestQueryOutcome_Literals(t *testing.T) {
	literals := map[string]string{
		engine.QueryOutcomeResolved:     "resuelto",
		engine.QueryOutcomeInconclusive: "no_concluyente",
		engine.QueryOutcomeNoResolver:   "sin_resolutor",
		engine.QueryOutcomeFailure:      "fallo",
		engine.QueryOutcomePartial:      "parcial",
		engine.QueryOutcomeLoop:         "bucle",
	}
	if len(literals) != 6 {
		t.Errorf("hay %d desenlaces distintos, quiero 6", len(literals))
	}
	for got, want := range literals {
		if got != want {
			t.Errorf("desenlace = %q, quiero %q", got, want)
		}
	}
}

// El 99 % de los turnos: el módulo no pregunta y el mecanismo no existe.
func TestQuery_ModuleThatDoesNotAskIsLeftAlone(t *testing.T) {
	var seen observations
	var calls []resolverCall
	var observer engine.QueryObserver = seen.record
	e := newEngine([]modules.Module{choice()},
		engine.WithQueryResolver(resolverStub{verdict: modules.Verdict{Code: "1"}, calls: &calls}),
		engine.WithQueryObserver(observer))

	st, outs, _, err := e.Step(context.Background(), choiceFlow(typeChoice), model.Conversation{CurrentNode: "root"}, engine.Input{Text: "1"})
	if err != nil || !st.Finished() || !slices.Equal(texts(outs), []string{"Te paso con Ventas."}) {
		t.Fatalf("nodo = %q, salidas = %q, err = %v", st.CurrentNode, texts(outs), err)
	}
	if len(calls) != 0 || len(seen) != 0 {
		t.Errorf("resolutor llamado %d veces y observador %d; quiero los dos mudos", len(calls), len(seen))
	}
}

// El camino feliz, y cada aserción es un defecto distinto.
func TestQuery_ReentryResolvesAndDiscardsTheFirstPass(t *testing.T) {
	var seen observations
	var calls []resolverCall
	st, outs, effects, passes := askOnce(t, asker{},
		engine.WithQueryResolver(resolverStub{verdict: modules.Verdict{Code: "1"}, calls: &calls}),
		engine.WithQueryObserver(seen.record))

	// Exactamente una re-entrada, con el texto ORIGINAL: el módulo es quien traduce.
	want := []pass{{text: clientText}, {text: clientText, verdict: modules.Verdict{Code: "1"}, hasVerdict: true}}
	if !reflect.DeepEqual(passes, want) {
		t.Errorf("pasadas = %+v, quiero %+v", passes, want)
	}
	// El Result de la primera se descartó ENTERO: si no, el efecto saldría duplicado.
	if !slices.Equal(texts(outs), []string{"resuelto:1"}) {
		t.Errorf("salidas = %q, quiero solo las de la segunda pasada", texts(outs))
	}
	if len(effects) != 1 || effects[0].Name != secondPassEffect {
		t.Errorf("efectos = %v, quiero solo %q", effects, secondPassEffect)
	}
	if !maps.Equal(st.Vars, map[string]any{"session_var": "intacto"}) || st.CurrentNode != "n1" {
		t.Errorf("estado = %+v, quiero las Vars originales y el mismo nodo", st)
	}
	// El resolutor recibió la consulta del módulo tal cual, una vez.
	if len(calls) != 1 || calls[0].query.Text != clientText || calls[0].query.Class != modules.QueryClassOption ||
		calls[0].query.Level != levelUnderTest || len(calls[0].query.Options) != 1 {
		t.Errorf("el resolutor recibió %+v", calls)
	}
	if !slices.Equal(seen, observations{{"opcion", levelUnderTest, "resuelto"}}) {
		t.Errorf("observado = %v", seen)
	}
}

// Qué veredicto ve el módulo y qué desenlace se observa, según el resolutor.
func TestQuery_VerdictAndOutcome(t *testing.T) {
	two := []string{"dos de pino", "una de queso"}
	cases := map[string]struct {
		resolver    *resolverStub // nil = sin resolutor
		chunks      []string
		wantVerdict modules.Verdict
		wantOutcome string
	}{
		"no resolver": {nil, nil, modules.Verdict{Reason: modules.QueryReasonNoResolver}, engine.QueryOutcomeNoResolver},
		// Lo que acompañe al error se descarta.
		"resolver fails": {&resolverStub{verdict: modules.Verdict{Code: "1"}, err: errors.New("timeout")}, nil,
			modules.Verdict{Reason: modules.QueryReasonFailure}, engine.QueryOutcomeFailure},
		"resolver does not know": {&resolverStub{}, nil,
			modules.Verdict{Reason: modules.QueryReasonInconclusive}, engine.QueryOutcomeInconclusive},
		"resolver's own reason is respected": {&resolverStub{verdict: modules.Verdict{Reason: modules.QueryReasonFailure}}, nil,
			modules.Verdict{Reason: modules.QueryReasonFailure}, engine.QueryOutcomeInconclusive},
		"no chunk resolved": {&resolverStub{verdict: modules.Verdict{Codes: []string{"", ""}}}, two,
			modules.Verdict{Codes: []string{"", ""}, Reason: modules.QueryReasonInconclusive}, engine.QueryOutcomeInconclusive},
		"single code": {&resolverStub{verdict: modules.Verdict{Code: "1"}}, nil,
			modules.Verdict{Code: "1"}, engine.QueryOutcomeResolved},
		// Valida el módulo, no el engine.
		"code outside the offered options": {&resolverStub{verdict: modules.Verdict{Code: "99"}}, nil,
			modules.Verdict{Code: "99"}, engine.QueryOutcomeResolved},
		"resolved verdict keeps its reason": {&resolverStub{verdict: modules.Verdict{Code: "1", Reason: modules.QueryReasonFailure}}, nil,
			modules.Verdict{Code: "1", Reason: modules.QueryReasonFailure}, engine.QueryOutcomeResolved},
		"every chunk resolved": {&resolverStub{verdict: modules.Verdict{Codes: []string{"1", "2"}}}, two,
			modules.Verdict{Codes: []string{"1", "2"}}, engine.QueryOutcomeResolved},
		"one chunk unresolved": {&resolverStub{verdict: modules.Verdict{Codes: []string{"1", ""}}}, two,
			modules.Verdict{Codes: []string{"1", ""}}, engine.QueryOutcomePartial},
		// Se mide contra los trozos PEDIDOS, no contra los contestados.
		"fewer codes than chunks": {&resolverStub{verdict: modules.Verdict{Codes: []string{"1"}}}, two,
			modules.Verdict{Codes: []string{"1"}}, engine.QueryOutcomePartial},
		"single code for a chunked query": {&resolverStub{verdict: modules.Verdict{Code: "1"}}, two,
			modules.Verdict{Code: "1"}, engine.QueryOutcomePartial},
		"extra codes are not looked at": {&resolverStub{verdict: modules.Verdict{Codes: []string{"1", "2", ""}}}, two,
			modules.Verdict{Codes: []string{"1", "2", ""}}, engine.QueryOutcomeResolved},
		"codes without chunks are never partial": {&resolverStub{verdict: modules.Verdict{Codes: []string{"", "2"}}}, nil,
			modules.Verdict{Codes: []string{"", "2"}}, engine.QueryOutcomeResolved},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var seen observations
			opts := []engine.Option{engine.WithQueryObserver(seen.record)}
			if tc.resolver != nil {
				opts = append(opts, engine.WithQueryResolver(*tc.resolver))
			}
			_, outs, _, passes := askOnce(t, asker{chunks: tc.chunks}, opts...)

			// Se re-entra IGUAL, resuelva o no: que no haya LLM no deja a nadie sin respuesta.
			if len(passes) != 2 || len(outs) != 1 {
				t.Fatalf("pasadas = %+v, salidas = %v; quiero dos pasadas y la pantalla de la segunda", passes, outs)
			}
			if !passes[1].hasVerdict || !reflect.DeepEqual(passes[1].verdict, tc.wantVerdict) {
				t.Errorf("veredicto de la segunda pasada = %+v, quiero %+v", passes[1].verdict, tc.wantVerdict)
			}
			if !slices.Equal(seen, observations{{"opcion", levelUnderTest, tc.wantOutcome}}) {
				t.Errorf("observado = %v, quiero un solo desenlace %q", seen, tc.wantOutcome)
			}
		})
	}
}

// Un módulo con un bug pide otra vez en la segunda pasada. El engine NO obedece, y
// lo que no obedece tampoco lo paga: el resolutor no se llama una segunda vez (otra
// inferencia entera dentro del mismo turno de WhatsApp).
func TestQuery_ModuleAskingTwiceDoesNotLoop(t *testing.T) {
	var seen observations
	var calls []resolverCall
	st, outs, effects, passes := askOnce(t, asker{stubborn: true},
		engine.WithQueryResolver(resolverStub{verdict: modules.Verdict{Code: "1"}, calls: &calls}),
		engine.WithQueryObserver(seen.record))

	if len(passes) != 2 {
		t.Errorf("pasadas = %d, quiero 2: una tercera llamada ya es el bucle", len(passes))
	}
	if len(calls) != 1 {
		t.Errorf("el resolutor se llamó %d veces, quiero 1: la petición de la segunda pasada se ignora", len(calls))
	}
	// Lo que el módulo produjo en la segunda pasada se entrega: solo se ignora la petición.
	if !slices.Equal(texts(outs), []string{firstPassScreen}) || len(effects) != 1 || st.CurrentNode != "n1" {
		t.Errorf("nodo = %q, salidas = %q, efectos = %v", st.CurrentNode, texts(outs), effects)
	}
	want := observations{{"opcion", levelUnderTest, "resuelto"}, {"opcion", levelUnderTest, "bucle"}}
	if !slices.Equal(seen, want) {
		t.Errorf("observado = %v, quiero %v", seen, want)
	}
}

// El módulo no conoce ni el tenant ni la sesión: los pone el engine desde la
// conversación, junto al ctx del Step. Sin tenant no hay vía; sin sesión la pregunta
// sale por otro Edge.
func TestQuery_AsksWithTheConversationIdentity(t *testing.T) {
	var calls []resolverCall
	passes := &[]pass{}
	e := newEngine([]modules.Module{asker{passes: passes}},
		engine.WithQueryResolver(resolverStub{verdict: modules.Verdict{Code: "1"}, calls: &calls}))

	ctx := context.WithValue(context.Background(), ctxKey{}, "turno-7")
	if _, _, _, err := e.Step(ctx, askerFlow(), askerConversation(), engine.Input{Text: clientText}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(calls) != 1 || calls[0].tenant != "tenant-a" || calls[0].session != "session-9" || calls[0].marker != "turno-7" {
		t.Errorf("el resolutor recibió %+v; quiero el ctx, el tenant y la sesión de ESTA conversación", calls)
	}
}

// El veredicto se siembra en una COPIA: el mapa del llamante no lo ve nunca.
func TestQuery_VerdictNeverReachesTheCallersMap(t *testing.T) {
	conv := askerConversation()
	caller := conv.Vars
	e := newEngine([]modules.Module{asker{passes: &[]pass{}}},
		engine.WithQueryResolver(resolverStub{verdict: modules.Verdict{Code: "1"}}))

	if _, _, _, err := e.Step(context.Background(), askerFlow(), conv, engine.Input{Text: clientText}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if !maps.Equal(caller, map[string]any{"session_var": "intacto"}) {
		t.Errorf("mapa del llamante = %v, quiero sin el veredicto", caller)
	}
}

// La segunda pasada es un Result como cualquier otro: puede transicionar o terminar.
func TestQuery_SecondPassDrivesTheTurn(t *testing.T) {
	resolver := engine.WithQueryResolver(resolverStub{verdict: modules.Verdict{Code: "1"}})

	t.Run("transition", func(t *testing.T) {
		mod := asker{second: func(conv model.Conversation, _ modules.Verdict) modules.Result {
			return modules.Result{Next: ptr("done"), Vars: conv.Vars}
		}}
		st, outs, _, _ := askOnce(t, mod, resolver)
		if !st.Finished() || !slices.Equal(texts(outs), []string{"hecho"}) {
			t.Errorf("nodo = %q, salidas = %q; quiero el render del destino", st.CurrentNode, texts(outs))
		}
	})

	t.Run("declared end", func(t *testing.T) {
		mod := asker{second: func(conv model.Conversation, _ modules.Verdict) modules.Result {
			end := model.NodeTerminal
			return modules.Result{Next: &end, Outcome: model.OutcomeCompleted, Vars: conv.Vars, Outputs: []string{"adiós"}}
		}}
		st, outs, _, _ := askOnce(t, mod, resolver)
		if !st.Finished() || st.Outcome() != model.OutcomeCompleted || !slices.Equal(texts(outs), []string{"adiós"}) {
			t.Errorf("nodo = %q, desenlace = %q, salidas = %q", st.CurrentNode, st.Outcome(), texts(outs))
		}
	})
}

// Cardinalidad acotada: por el observador solo salen la clase y el nivel que puso el
// módulo y uno de los seis desenlaces. El texto del cliente, jamás.
func TestQueryObserver_ArgumentsAreBounded(t *testing.T) {
	chunks := []string{"TROZO-UNO del cliente", "TROZO-DOS del cliente"}
	turns := map[string]struct {
		mod  asker
		opts []engine.Option
	}{
		"no resolver":  {asker{chunks: chunks}, nil},
		"failure":      {asker{chunks: chunks}, []engine.Option{engine.WithQueryResolver(resolverStub{err: errors.New(clientText)})}},
		"inconclusive": {asker{chunks: chunks}, []engine.Option{engine.WithQueryResolver(resolverStub{})}},
		"partial":      {asker{chunks: chunks}, []engine.Option{engine.WithQueryResolver(resolverStub{verdict: modules.Verdict{Codes: []string{"1"}}})}},
		"resolved and loop": {asker{chunks: chunks, stubborn: true},
			[]engine.Option{engine.WithQueryResolver(resolverStub{verdict: modules.Verdict{Codes: []string{"1", "2"}}})}},
	}
	closed := []string{"resuelto", "no_concluyente", "sin_resolutor", "fallo", "parcial", "bucle"}

	var seen observations
	for _, turn := range turns {
		askOnce(t, turn.mod, append(turn.opts, engine.WithQueryObserver(seen.record))...)
	}

	outcomes := map[string]bool{}
	for _, got := range seen {
		if got[0] != "opcion" || got[1] != levelUnderTest || !slices.Contains(closed, got[2]) {
			t.Errorf("observado %v: quiero la clase y el nivel del módulo y un desenlace del conjunto cerrado", got)
		}
		for _, arg := range got {
			if strings.Contains(arg, "cliente") || strings.Contains(arg, "PINO") {
				t.Errorf("observado %v: el texto del cliente salió por el observador", got)
			}
		}
		outcomes[got[2]] = true
	}
	if len(outcomes) != len(closed) {
		t.Errorf("desenlaces vistos = %v, quiero los seis", outcomes)
	}
}

// Cablear a medias no deja el engine peor que no cablear: un nil se ignora y no
// quita lo que ya hubiera.
func TestQueryOptions_NilIsIgnored(t *testing.T) {
	var seen observations
	var calls []resolverCall
	_, outs, _, _ := askOnce(t, asker{},
		engine.WithQueryResolver(resolverStub{verdict: modules.Verdict{Code: "1"}, calls: &calls}),
		engine.WithQueryObserver(seen.record),
		engine.WithQueryResolver(nil),
		engine.WithQueryObserver(nil))
	if !slices.Equal(texts(outs), []string{"resuelto:1"}) || len(calls) != 1 || len(seen) != 1 {
		t.Errorf("salidas = %q, llamadas = %d, observado = %v; quiero el resolutor y el observador de antes", texts(outs), len(calls), seen)
	}

	// Solo con nil: sin resolutor, y sin observador el mecanismo funciona en silencio.
	_, outs, _, passes := askOnce(t, asker{}, engine.WithQueryResolver(nil), engine.WithQueryObserver(nil))
	if !slices.Equal(texts(outs), []string{"degradado:sin_resolutor"}) || len(passes) != 2 {
		t.Errorf("salidas = %q, pasadas = %d; quiero la degradación de siempre", texts(outs), len(passes))
	}
}

// Un observador que entra en pánico se lleva el turno: no se traga.
func TestQueryObserver_PanicIsNotSwallowed(t *testing.T) {
	e := newEngine([]modules.Module{asker{passes: &[]pass{}}},
		engine.WithQueryObserver(func(string, string, string) { panic("observador roto") }))

	defer func() {
		if got := recover(); got != "observador roto" {
			t.Errorf("recover = %v, quiero el pánico del observador", got)
		}
	}()
	_, _, _, _ = e.Step(context.Background(), askerFlow(), askerConversation(), engine.Input{Text: clientText})
	t.Error("Step volvió sin propagar el pánico del observador")
}
