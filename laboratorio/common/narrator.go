//go:build laboratorio

package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Narrator imprime lo que hace un escalón como una lección: un título, pasos numerados, notas que
// explican el porqué y muestras de los datos que circulan. No es un log: va por stdout, en español y
// para leerse de arriba abajo. El log del cloud, si se pide (-v), va por stderr.
type Narrator struct {
	w    io.Writer
	step int
}

// NewNarrator escribe en w (nil ⇒ os.Stdout).
func NewNarrator(w io.Writer) *Narrator {
	if w == nil {
		w = os.Stdout
	}
	return &Narrator{w: w}
}

// Title abre el escalón: su nombre y, debajo, una línea por cada texto de contexto.
func (n *Narrator) Title(title string, lines ...string) {
	bar := strings.Repeat("═", 72)
	n.printf("%s\n  %s\n", bar, title)
	for _, c := range lines {
		n.printf("  %s\n", c)
	}
	n.printf("%s\n", bar)
}

// Step abre el paso siguiente, numerado.
func (n *Narrator) Step(title string) {
	n.step++
	n.printf("\n── Paso %d · %s\n", n.step, title)
}

// Note explica qué pasó y por qué.
func (n *Narrator) Note(format string, args ...any) {
	n.printf("   · %s\n", fmt.Sprintf(format, args...))
}

// OK marca algo que salió como se esperaba.
func (n *Narrator) OK(format string, args ...any) {
	n.printf("   ✓ %s\n", fmt.Sprintf(format, args...))
}

// Fail marca algo que falló, con el error si lo hay.
func (n *Narrator) Fail(err error, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if err != nil {
		msg += ": " + err.Error()
	}
	n.printf("   ✗ %s\n", msg)
}

// Show imprime un valor con su etiqueta. Un JSON (json.RawMessage, []byte o una cadena que lo sea)
// sale indentado; una cadena de varias líneas, sangrada; cualquier otra cosa, con %+v.
func (n *Narrator) Show(label string, value any) {
	text := render(value)
	if !strings.Contains(text, "\n") && len(text) <= 80 {
		n.printf("   %s: %s\n", label, text)
		return
	}
	n.printf("   %s:\n", label)
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		n.printf("      │ %s\n", line)
	}
}

// Raw imprime un texto TAL CUAL, sin reformatear: para la salida del modelo antes de ExtractJSON,
// donde un fence o un espacio de más es justo lo que hay que ver.
func (n *Narrator) Raw(label, body string) {
	n.printf("   %s (%d bytes, tal cual):\n", label, len(body))
	for _, line := range strings.Split(body, "\n") {
		n.printf("      │ %s\n", line)
	}
}

// Excerpt imprime solo las primeras max líneas de un texto largo (un prompt), y cuántas se omiten.
func (n *Narrator) Excerpt(label, body string, max int) {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) <= max {
		n.Show(label, body)
		return
	}
	shown := strings.Join(lines[:max], "\n")
	n.Show(fmt.Sprintf("%s (primeras %d de %d líneas)", label, max, len(lines)), shown)
}

// render convierte un valor en texto legible.
func render(v any) string {
	var raw []byte
	switch x := v.(type) {
	case json.RawMessage:
		raw = x
	case []byte:
		raw = x
	case string:
		if t := strings.TrimSpace(x); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			raw = []byte(t)
		} else {
			return x
		}
	case fmt.Stringer:
		return x.String()
	case error:
		return x.Error()
	default:
		// Un struct o un mapa se lee mejor como JSON; lo que no serializa, con %+v.
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%+v", v)
		}
		raw = b
	}
	var buf bytes.Buffer
	if json.Valid(raw) && json.Indent(&buf, raw, "", "  ") == nil {
		return buf.String()
	}
	return string(raw)
}

func (n *Narrator) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(n.w, format, args...)
}
