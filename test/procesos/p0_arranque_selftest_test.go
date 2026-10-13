//go:build integracion

package procesos

import (
	"testing"
)

// Los tests propios del criterio de «sin_errores» de P0 (p0_arranque_test.go). Ninguno arranca un
// servidor: trabajan sobre líneas de log escritas a mano.

// p0LogLine es una línea JSON del log con ese nivel, ese msg y, si no es "", ese atributo error.
func p0LogLine(level, msg, errText string) map[string]any {
	l := map[string]any{"level": level, "msg": msg, "ms": float64(3)}
	if errText != "" {
		l["error"] = errText
	}
	return l
}

// p0Msgs devuelve los msg de esas líneas, en su orden.
func p0Msgs(lineas []map[string]any) []string {
	msgs := make([]string, 0, len(lineas))
	for _, l := range lineas {
		msgs = append(msgs, p0Cadena(l, "msg"))
	}
	return msgs
}

// TestP0_CountedErrors: de los ERROR del log solo se tolera el que es de cancelación Y viene
// después de la señal de parada (D-F9-10); los demás cuentan, cada grupo en su orden, y lo que no
// es ERROR no sale en ninguno.
func TestP0_CountedErrors(t *testing.T) {
	t.Parallel()
	const (
		canceled   = "pq: context canceled"
		lookup     = "dial tcp: lookup localhost: operation was canceled"
		otherError = "pq: relation \"webhook_outbox\" does not exist"
	)
	signal := p0LogLine("INFO", p0MsgSenal, "")

	cases := []struct {
		name          string
		lineas        []map[string]any
		wantCount     []string
		wantTolerated []string
	}{
		{
			name:   "no errors at all",
			lineas: []map[string]any{p0LogLine("INFO", "arranque", ""), signal, p0LogLine("WARN", "aviso", canceled)},
		},
		{
			name: "cancellations after the signal are tolerated, in order",
			lineas: []map[string]any{
				signal,
				p0LogLine("ERROR", "webhook worker: reclamar lote", canceled),
				p0LogLine("ERROR", "webhook worker: recuperar el claim vencido", lookup),
			},
			wantTolerated: []string{"webhook worker: reclamar lote", "webhook worker: recuperar el claim vencido"},
		},
		{
			name: "a cancellation before the signal counts",
			lineas: []map[string]any{
				p0LogLine("ERROR", "antes de la parada", canceled),
				signal,
			},
			wantCount: []string{"antes de la parada"},
		},
		{
			name: "a non-cancellation after the signal counts",
			lineas: []map[string]any{
				signal,
				p0LogLine("ERROR", "otro fallo", otherError),
				p0LogLine("ERROR", "sin atributo de error", ""),
			},
			wantCount: []string{"otro fallo", "sin atributo de error"},
		},
		{
			name:      "without the signal nothing is tolerated",
			lineas:    []map[string]any{p0LogLine("ERROR", "cortado sin señal", canceled)},
			wantCount: []string{"cortado sin señal"},
		},
		{
			name: "only the first signal line opens the window",
			lineas: []map[string]any{
				p0LogLine("ERROR", "antes", canceled),
				signal,
				p0LogLine("ERROR", "entre señales", canceled),
				signal,
				p0LogLine("ERROR", "después", otherError),
			},
			wantCount:     []string{"antes", "después"},
			wantTolerated: []string{"entre señales"},
		},
		{
			name: "the mark in the msg is enough and the level is compared ignoring case",
			lineas: []map[string]any{
				signal,
				p0LogLine("error", "agregador: context canceled", ""),
			},
			wantTolerated: []string{"agregador: context canceled"},
		},
		{
			name: "the mark is compared as written",
			lineas: []map[string]any{
				signal,
				p0LogLine("ERROR", "otra grafía", "Context Canceled"),
			},
			wantCount: []string{"otra grafía"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			count, tolerated := p0CountedErrors(c.lineas)
			if got := p0Msgs(count); !equalStrings(got, c.wantCount) {
				t.Errorf("cuentan = %q, quería %q", got, c.wantCount)
			}
			if got := p0Msgs(tolerated); !equalStrings(got, c.wantTolerated) {
				t.Errorf("toleradas = %q, quería %q", got, c.wantTolerated)
			}
		})
	}
}

// equalStrings compara dos listas elemento a elemento; nil y vacía son iguales.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
