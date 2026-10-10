//go:build pendiente

package turnoacotado_test

// troceado_test.go — EL CONTADOR DE LLAMADAS Y LOS DOS FRENOS.
//
// «3 llamadas» a secas sería compatible con mandar el turno entero tres veces: por
// eso el contador va acompañado de qué lleva cada llamada. Los tests de reloj corren
// en una burbuja de synctest: el tiempo que «tarda» el doble es simulado.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/turnoacotado"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
)

// chunked arma la consulta CON TROZOS que el carrito eleva: las opciones son el
// catálogo aplanado y los trozos, lo que su cascada no casó.
func chunked(chunks ...string) modules.Query {
	return modules.Query{
		Class: modules.QueryClassOption, Level: "categories", Chunks: chunks,
		Text: strings.Join(chunks, " y "),
		Options: []modules.QueryOption{
			{Code: "0", Label: "Pizzas"},
			{Code: "1", Label: "Hamburguesas"},
			{Code: "2", Label: "Empanadas"},
			{Code: "3", Label: "Jugos"},
		},
	}
}

func clientAnswer(text string) string { return `Respuesta del cliente: "` + text + `"` }

// otherChunksIn devuelve lo que un prompt lleva de más: los trozos que no son el de
// la posición own, y el turno entero.
func otherChunksIn(prompt string, q modules.Query, own int) []string {
	var found []string
	for i, chunk := range q.Chunks {
		if i != own && strings.Contains(prompt, clientAnswer(chunk)) {
			found = append(found, chunk)
		}
	}
	if strings.Contains(prompt, q.Text) {
		found = append(found, q.Text)
	}
	return found
}

// Los tres números están razonados con segundos medidos delante (cabecera de
// troceado.go): cambiarlos es rehacer la cuenta, no editar una constante.
func TestChunking_Limits(t *testing.T) {
	t.Parallel()
	if turnoacotado.MaxCallsPerTurn != 3 {
		t.Errorf("MaxCallsPerTurn = %d, quiero 3", turnoacotado.MaxCallsPerTurn)
	}
	if turnoacotado.ChunkingBudget != 20*time.Second {
		t.Errorf("ChunkingBudget = %v, quiero 20s", turnoacotado.ChunkingBudget)
	}
	if turnoacotado.FloorPerCall != 5*time.Second {
		t.Errorf("FloorPerCall = %v, quiero 5s", turnoacotado.FloorPerCall)
	}
}

// ---------------------------------------------------------------------------
// Una llamada chica por trozo, jamás una con los N dentro
// ---------------------------------------------------------------------------

func TestChunking_OneCallPerChunk(t *testing.T) {
	t.Parallel()
	f := &fakeTurner{replies: []string{picks(1), picks(3), picks(4)}}
	q := chunked("napolitanas", "completos", "gaseosas")
	ctx := context.WithValue(context.Background(), markerKey{}, "ctx del llamante")

	v, err := newResolver(t, f).ResolveQuery(ctx, "t1", "s1", q)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if len(f.calls) != 3 {
		t.Fatalf("llamadas al modelo = %d, quiero 3 (una por trozo)", len(f.calls))
	}
	for i, chunk := range q.Chunks {
		c := f.calls[i]
		if c.tenantID != "t1" || c.sessionID != "s1" || c.marker != "ctx del llamante" {
			t.Errorf("la llamada %d no lleva el tenant, la sesión y el ctx recibidos: %+v", i+1, c)
		}
		if !strings.HasSuffix(c.req.Prompt, "1. Pizzas\n2. Hamburguesas\n3. Empanadas\n4. Jugos\n"+clientAnswer(chunk)) {
			t.Errorf("la llamada %d no pregunta por su trozo %q sobre las opciones de la consulta", i+1, chunk)
		}
		if others := otherChunksIn(c.req.Prompt, q, i); len(others) > 0 {
			t.Errorf("la llamada %d lleva además %q: eso es la llamada monstruo", i+1, others)
		}
	}
	if want := []string{"0", "2", "3"}; !slices.Equal(v.Codes, want) {
		t.Fatalf("Codes = %v, quiero %v", v.Codes, want)
	}
	if v.Code != "" || v.Reason != "" {
		t.Errorf("Code = %q, Reason = %q: un troceado sin fallo no trae código único ni motivo", v.Code, v.Reason)
	}
}

// Un trozo se pregunta como una ELECCIÓN normal: mismo prompt y mismo esquema que el
// turno de un solo texto con ese trozo como texto.
func TestChunking_EachChunkIsAPlainOptionQuestion(t *testing.T) {
	t.Parallel()
	whole := &fakeTurner{replies: []string{picks(1)}}
	single := chunked()
	single.Chunks, single.Text = nil, "completos"
	if _, err := newResolver(t, whole).ResolveQuery(context.Background(), "t", "s", single); err != nil {
		t.Fatalf("turno de un solo texto: %v", err)
	}

	f := &fakeTurner{replies: []string{picks(1)}}
	if _, err := newResolver(t, f).ResolveQuery(context.Background(), "t", "s", chunked("completos")); err != nil {
		t.Fatalf("troceado: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0].req != whole.calls[0].req {
		t.Errorf("el trozo no se pregunta igual que el turno de un solo texto")
	}
}

// Una consulta de CANTIDAD con trozos también se trocea, y cada trozo se pregunta
// como elección sobre sus Options: sin opciones, ningún trozo puede resolverse.
func TestChunking_QuantityClassChunksAreAskedAsOptions(t *testing.T) {
	t.Parallel()
	f := &fakeTurner{replies: []string{picks(1)}}
	q := modules.Query{Class: modules.QueryClassQuantity, Level: "quantity", Text: "dos y tres", Chunks: []string{"dos", "tres"}}

	v, err := newResolver(t, f).ResolveQuery(context.Background(), "t", "s", q)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("llamadas = %d, quiero 2", len(f.calls))
	}
	if !strings.Contains(f.calls[0].req.Prompt, "Elegí una opción:\"\nOpciones ofrecidas:\n"+clientAnswer("dos")) {
		t.Errorf("el trozo de una consulta de cantidad no se preguntó como elección")
	}
	if want := []string{"", ""}; !slices.Equal(v.Codes, want) || v.ResolvedAny() {
		t.Errorf("Codes = %v, quiero %v: sin opciones no hay posición que traducir", v.Codes, want)
	}
}

func TestChunking_UnresolvedChunkStaysEmptyAtItsPosition(t *testing.T) {
	t.Parallel()
	f := &fakeTurner{replies: []string{picks(2), inconclusiveReply, picks(9)}}
	v, err := newResolver(t, f).ResolveQuery(context.Background(), "t", "s", chunked("completos", "asdf", "la novena"))
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if want := []string{"1", "", ""}; !slices.Equal(v.Codes, want) {
		t.Errorf("Codes = %v, quiero %v", v.Codes, want)
	}
	if v.Reason != "" || !v.ResolvedAny() {
		t.Errorf("veredicto = %+v: un trozo sin resolver no es un fallo ni invalida los demás", v)
	}
}

// Una lista de trozos vacía es la consulta de un solo texto de siempre.
func TestChunking_NoChunksTakesTheSingleTextPath(t *testing.T) {
	t.Parallel()
	for _, chunks := range [][]string{nil, {}} {
		f := &fakeTurner{replies: []string{picks(2)}}
		q := chunked()
		q.Chunks, q.Text = chunks, "quiero el segundo"

		v, err := newResolver(t, f).ResolveQuery(context.Background(), "t1", "s1", q)
		if err != nil {
			t.Fatalf("ResolveQuery: %v", err)
		}
		if len(f.calls) != 1 || v.Code != "1" || v.Codes != nil {
			t.Errorf("llamadas=%d Code=%q Codes=%v, quiero 1 llamada y un código único", len(f.calls), v.Code, v.Codes)
		}
	}
}

// ---------------------------------------------------------------------------
// Freno 1 · el tope de llamadas
// ---------------------------------------------------------------------------

func TestChunking_CapCutsTheCalls(t *testing.T) {
	t.Parallel()
	f := &fakeTurner{replies: []string{picks(1)}}
	q := chunked("uno", "dos", "tres", "cuatro", "cinco")

	v, err := newResolver(t, f).ResolveQuery(context.Background(), "t1", "s1", q)
	if err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if len(f.calls) != turnoacotado.MaxCallsPerTurn {
		t.Fatalf("llamadas = %d, el tope es %d", len(f.calls), turnoacotado.MaxCallsPerTurn)
	}
	if want := []string{"0", "0", "0", "", ""}; !slices.Equal(v.Codes, want) {
		t.Errorf("Codes = %v, quiero %v: alineado con los trozos y vacío pasado el tope", v.Codes, want)
	}
}

// ---------------------------------------------------------------------------
// Freno 2 · el presupuesto del turno
// ---------------------------------------------------------------------------

// Cada llamada vence a ChunkingBudget de haber empezado el troceado, o antes si el
// ctx del llamante es más corto.
func TestChunking_BudgetIsTheDeadlineOfEveryCall(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		caller time.Duration // 0 ⇒ el llamante no trae plazo
		want   time.Duration
	}{
		{"caller without deadline", 0, turnoacotado.ChunkingBudget},
		{"caller with a longer deadline", time.Minute, turnoacotado.ChunkingBudget},
		{"caller with a shorter deadline", 12 * time.Second, 12 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				ctx := context.Background()
				if tc.caller > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tc.caller)
					defer cancel()
				}
				f := &fakeTurner{replies: []string{picks(1)}, delay: time.Second}
				if _, err := newResolver(t, f).ResolveQuery(ctx, "t", "s", chunked("uno", "dos")); err != nil {
					t.Fatalf("ResolveQuery: %v", err)
				}
				if len(f.calls) != 2 {
					t.Fatalf("llamadas = %d, quiero 2", len(f.calls))
				}
				for i, c := range f.calls {
					if !c.hasDeadline || c.deadline.Sub(start) != tc.want {
						t.Errorf("la llamada %d vence a %v del arranque, quiero %v", i+1, c.deadline.Sub(start), tc.want)
					}
				}
			})
		})
	}
}

// Lo ya resuelto no se pierde y la llamada que no cabe ni se arranca. La frontera es
// FloorPerCall: con eso justo de presupuesto, la llamada sí sale.
func TestChunking_ExhaustedBudgetKeepsWhatWasResolved(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		delay     time.Duration
		wantCalls int
		wantCodes []string
	}{
		{"exactly the floor is left", turnoacotado.ChunkingBudget - turnoacotado.FloorPerCall, 2, []string{"0", "0"}},
		{"less than the floor is left", turnoacotado.ChunkingBudget - turnoacotado.FloorPerCall + time.Nanosecond, 1, []string{"0", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				f := &fakeTurner{replies: []string{picks(1)}, delay: tc.delay}
				v, err := newResolver(t, f).ResolveQuery(context.Background(), "t1", "s1", chunked("napolitanas", "completos"))
				if err != nil {
					t.Fatalf("un presupuesto agotado no es un error: %v", err)
				}
				if len(f.calls) != tc.wantCalls {
					t.Fatalf("llamadas = %d, quiero %d", len(f.calls), tc.wantCalls)
				}
				if !slices.Equal(v.Codes, tc.wantCodes) {
					t.Errorf("Codes = %v, quiero %v", v.Codes, tc.wantCodes)
				}
				if !v.ResolvedAny() || v.Reason != "" {
					t.Errorf("veredicto = %+v: un troceado parcial cuenta como resuelto y no trae motivo", v)
				}
			})
		})
	}
}

// Un llamante que llega con menos del suelo no arranca ninguna llamada: sale un
// veredicto alineado y vacío, sin error.
func TestChunking_CallerBelowTheFloorStartsNoCall(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), turnoacotado.FloorPerCall-time.Nanosecond)
		defer cancel()
		f := &fakeTurner{replies: []string{picks(1)}}

		v, err := newResolver(t, f).ResolveQuery(ctx, "t", "s", chunked("uno", "dos"))
		if err != nil {
			t.Fatalf("ResolveQuery: %v", err)
		}
		if len(f.calls) != 0 {
			t.Errorf("llamadas = %d, quiero 0", len(f.calls))
		}
		if want := []string{"", ""}; !slices.Equal(v.Codes, want) || v.Reason != "" {
			t.Errorf("veredicto = %+v, quiero Codes %v sin motivo", v, want)
		}
	})
}

// ---------------------------------------------------------------------------
// El fallo de la vía, a mitad y en la primera
// ---------------------------------------------------------------------------

func TestChunking_FailureMidwayReturnsThePartial(t *testing.T) {
	t.Parallel()
	f := &fakeTurner{replies: []string{picks(1)}, err: errors.New("edge sin capacidad"), failFrom: 2}
	v, err := newResolver(t, f).ResolveQuery(context.Background(), "t1", "s1", chunked("napolitanas", "completos", "gaseosas"))
	if err != nil {
		t.Fatalf("el fallo a mitad no se propaga habiendo trabajo resuelto: %v", err)
	}
	if want := []string{"0", "", ""}; !slices.Equal(v.Codes, want) {
		t.Errorf("Codes = %v, quiero %v", v.Codes, want)
	}
	if v.Reason != modules.QueryReasonFailure {
		t.Errorf("Reason = %q, quiero %q para que el desenlace no mienta", v.Reason, modules.QueryReasonFailure)
	}
	if len(f.calls) != 2 {
		t.Errorf("llamadas = %d, quiero 2: tras el fallo no se pregunta más", len(f.calls))
	}
}

// Sin nada que salvar, el troceado se comporta como el turno de un solo texto: error
// hacia arriba, intacto. Vale para el fallo en la primera llamada y para el que llega
// cuando las anteriores no resolvieron nada.
func TestChunking_FailureWithNothingResolvedPropagates(t *testing.T) {
	t.Parallel()
	failure := errors.New("edge sin capacidad")
	for _, failFrom := range []int{1, 2} {
		f := &fakeTurner{replies: []string{inconclusiveReply}, err: failure, failFrom: failFrom}
		v, err := newResolver(t, f).ResolveQuery(context.Background(), "t1", "s1", chunked("napolitanas", "completos", "gaseosas"))
		if err != failure { //nolint:errorlint // se afirma que NO se envuelve: identidad, no errors.Is
			t.Fatalf("falla en la %d: err = %v, quiero el fallo de la vía intacto", failFrom, err)
		}
		if v.Codes != nil || v.Code != "" || v.Reason != "" {
			t.Errorf("falla en la %d: veredicto = %+v, quiero el cero junto al error", failFrom, v)
		}
		if len(f.calls) != failFrom {
			t.Errorf("falla en la %d: llamadas = %d", failFrom, len(f.calls))
		}
	}
}

// Un tenant en vía API no genera ni un error ni un aviso por trocear. El veredicto
// no trae Codes aunque un trozo anterior se hubiera resuelto: el motivo manda.
func TestChunking_APIRouteIsNotAFailure(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("envuelto: %w", llmvia.ErrViaSinTurnoAcotado)
	for _, failFrom := range []int{1, 2} {
		f := &fakeTurner{replies: []string{picks(1)}, err: wrapped, failFrom: failFrom}
		v, err := newResolver(t, f).ResolveQuery(context.Background(), "t1", "s1", chunked("napolitanas", "completos"))
		if err != nil {
			t.Fatalf("falla en la %d: la vía API no es una avería: %v", failFrom, err)
		}
		if v.Reason != modules.QueryReasonNoResolver || v.Codes != nil || v.ResolvedAny() {
			t.Errorf("falla en la %d: veredicto = %+v, quiero solo el motivo %q", failFrom, v, modules.QueryReasonNoResolver)
		}
	}
}
