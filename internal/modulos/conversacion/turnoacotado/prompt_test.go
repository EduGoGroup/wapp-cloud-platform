package turnoacotado_test

// prompt_test.go — EL TEXTO QUE VIAJA AL MODELO, visto desde fuera.
//
// prompt.go no exporta nada: lo que compone se observa en el TurnoRequest que el
// resolutor le pasa al selector de vía.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
)

const (
	wantQuantitySchema = `{"type":"object","properties":{"usable":{"type":"boolean","description":"true si el cliente dio una cantidad utilizable"},"value":{"type":["integer","null"],"description":"la cantidad pedida por el cliente; null si no aplica"},"reason":{"type":"string","enum":["ok","cambio_de_intencion","fuera_de_rango","no_entendido","otra_pregunta"]}},"required":["usable","value","reason"]}`
	wantOptionSchema   = `{"type":"object","properties":{"usable":{"type":"boolean","description":"true si el cliente eligió una opción válida de la lista"},"value":{"type":["integer","null"],"description":"el número de la opción elegida según la lista; null si no aplica"},"reason":{"type":"string","enum":["ok","cambio_de_intencion","fuera_de_rango","no_entendido","otra_pregunta"]}},"required":["usable","value","reason"]}`

	// Huella (SHA-256) del prompt completo que el paquete viejo componía para
	// quantity("mejor dos") y fourOptionMenu("la doble"). El texto —instrucciones y
	// few-shot— es el que se midió contra el modelo real: no se retoca sin volver a
	// medir, y un byte cambiado invalida además la caché de prefijo del Edge.
	wantQuantityPromptSHA = "389da1b8556b8b4468507c2bbacebb00c66d04d097c8e105ecf4fdb586634bdc"
	wantOptionPromptSHA   = "1195f9d0b1e1e1a14c3c3fd01e92097039cec3b61884946bc46d78e27dc03c9f"
)

// sentRequest devuelve la única petición que el resolutor le mandó al selector.
func sentRequest(t *testing.T, q modules.Query) llmvia.TurnoRequest {
	t.Helper()
	f := &fakeTurner{replies: []string{"{}"}}
	if _, err := newResolver(t, f).ResolveQuery(context.Background(), "t", "s", q); err != nil {
		t.Fatalf("ResolveQuery: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("llamadas = %d, quiero 1", len(f.calls))
	}
	return f.calls[0].req
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// El prompt entero de cada clase, byte a byte, es el del paquete viejo.
func TestPrompt_MeasuredTextIsUntouched(t *testing.T) {
	t.Parallel()
	if got := sha(sentRequest(t, quantity("mejor dos")).Prompt); got != wantQuantityPromptSHA {
		t.Errorf("el prompt de CANTIDAD cambió (sha256 = %s): es texto medido contra el modelo real", got)
	}
	if got := sha(sentRequest(t, fourOptionMenu("la doble")).Prompt); got != wantOptionPromptSHA {
		t.Errorf("el prompt de ELECCIÓN cambió (sha256 = %s): es texto medido contra el modelo real", got)
	}
}

// Cada clase lleva SU esquema, escrito a mano y estable entre arranques. Es un JSON
// Schema serializado (el Edge lo distingue de la cadena "json" por el primer byte).
func TestPrompt_EachClassSendsItsOwnSchema(t *testing.T) {
	t.Parallel()
	if got := sentRequest(t, quantity("mejor dos")).Formato; got != wantQuantitySchema {
		t.Errorf("esquema de cantidad = %s", got)
	}
	if got := sentRequest(t, fourOptionMenu("la doble")).Formato; got != wantOptionSchema {
		t.Errorf("esquema de elección = %s", got)
	}
}

// Un esquema único midió 9/12 y separado por clase 11/12: `value` significa cosas
// distintas en cada pregunta, y cada una lleva sus instrucciones y su few-shot.
func TestPrompt_EachClassSendsItsOwnInstructionsAndExamples(t *testing.T) {
	t.Parallel()
	qty := sentRequest(t, quantity("mejor dos")).Prompt
	opt := sentRequest(t, fourOptionMenu("la doble")).Prompt

	if !strings.HasPrefix(qty, "Eres el intérprete de una respuesta de un cliente a una pregunta de CANTIDAD ") {
		t.Errorf("el prompt de cantidad no empieza por sus instrucciones")
	}
	if !strings.HasPrefix(opt, "Eres el intérprete de una respuesta de un cliente a una pregunta de ELECCIÓN ") {
		t.Errorf("el prompt de elección no empieza por sus instrucciones")
	}
	// El few-shot, por el ejemplo más característico de cada clase.
	if !strings.Contains(qty, "Respuesta del cliente: \"dame un par\"\n{\"usable\": true, \"value\": 2, \"reason\": \"ok\"}\n") {
		t.Errorf("al prompt de cantidad le falta su few-shot medido")
	}
	if !strings.Contains(opt, "1. Torta\n2. Helado\nRespuesta del cliente: \"quiero el Helado\"\n{\"usable\": true, \"value\": 2, \"reason\": \"ok\"}\n") {
		t.Errorf("al prompt de elección le falta su few-shot medido")
	}
	if strings.Contains(qty, "quiero el Helado") || strings.Contains(opt, "dame un par") {
		t.Errorf("una clase lleva el few-shot de la otra")
	}
	for name, p := range map[string]string{"cantidad": qty, "elección": opt} {
		if strings.Count(p, "\nRespuesta del cliente: \"") != 6 {
			t.Errorf("%s: quiero cinco ejemplos y el caso real, con el mismo formato", name)
		}
		// El orden es instrucciones, ejemplos, caso.
		rules, examples, realCase := strings.Index(p, "\nReglas:\n"), strings.Index(p, "\n\nEjemplos resueltos:\n\n"), strings.Index(p, "\nAhora resolvé este caso:\n\n")
		if rules < 0 || rules >= examples || examples >= realCase {
			t.Errorf("%s: el orden no es instrucciones (%d), ejemplos (%d), caso (%d)", name, rules, examples, realCase)
		}
	}
}

// El caso real va AL FINAL y con el mismo formato de tres líneas que los ejemplos.
func TestPrompt_RealCaseClosesThePrompt(t *testing.T) {
	t.Parallel()
	if p := sentRequest(t, quantity("mejor dos")).Prompt; !strings.HasSuffix(p,
		"\nAhora resolvé este caso:\n\n"+
			"Pregunta hecha al cliente: \"¿Cuántas unidades querés?\"\n"+
			"Opciones ofrecidas: (ninguna, es una pregunta abierta de cantidad)\n"+
			"Respuesta del cliente: \"mejor dos\"") {
		t.Errorf("el prompt de cantidad no cierra con el caso real:\n%s", p[max(0, len(p)-260):])
	}
	if p := sentRequest(t, fourOptionMenu("la doble")).Prompt; !strings.HasSuffix(p,
		"\nAhora resolvé este caso:\n\n"+
			"Pregunta hecha al cliente: \"Elegí una opción:\"\n"+
			"Opciones ofrecidas:\n"+
			"1. Hamburguesa clásica\n2. Hamburguesa doble\n3. Papas fritas\n4. Volver\n"+
			"Respuesta del cliente: \"la doble\"") {
		t.Errorf("el prompt de elección no cierra con el caso real:\n%s", p[max(0, len(p)-260):])
	}
}

// Al modelo se le enseña lo que el CLIENTE tiene delante (rótulos numerados), nunca
// los códigos del catálogo: la traducción posición → código se hace en Go.
func TestPrompt_OptionsAreNumberedLabelsWithoutCodes(t *testing.T) {
	t.Parallel()
	p := sentRequest(t, fourOptionMenu("la doble")).Prompt
	for _, code := range []string{"burger-doble", "burger-clasica", "papas\n", "volver"} {
		if strings.Contains(p, code) {
			t.Errorf("el prompt lleva el código %q", code)
		}
	}
}

// Una pregunta de cantidad no enseña lista aunque la consulta traiga opciones.
func TestPrompt_QuantityNeverShowsOptions(t *testing.T) {
	t.Parallel()
	q := fourOptionMenu("mejor dos")
	q.Class = modules.QueryClassQuantity
	if got, want := sentRequest(t, q), sentRequest(t, quantity("mejor dos")); got != want {
		t.Errorf("las opciones cambiaron la pregunta de cantidad")
	}
}

// Todo lo que no depende del mensaje va delante y byte a byte igual: el Edge cachea
// el PREFIJO del prompt, y uno que cambia en cada turno paga prefill en frío.
func TestPrompt_PrefixIsStableBetweenTurns(t *testing.T) {
	t.Parallel()
	const tail = "Respuesta del cliente: \""
	for name, pair := range map[string][2]modules.Query{
		"cantidad": {quantity("mejor dos"), quantity("ponme una docena")},
		"elección": {fourOptionMenu("la doble"), fourOptionMenu("unas papas")},
	} {
		a, b := sentRequest(t, pair[0]).Prompt, sentRequest(t, pair[1]).Prompt
		cut := strings.LastIndex(a, tail) + len(tail)
		if len(b) < cut || a[:cut] != b[:cut] {
			t.Errorf("%s: dos turnos no comparten todo el prompt hasta el mensaje del cliente", name)
		}
	}
}

// El nivel de la consulta es telemetría: no entra en el prompt.
func TestPrompt_LevelDoesNotTravel(t *testing.T) {
	t.Parallel()
	q := fourOptionMenu("la doble")
	q.Level = "otro-nivel"
	if got, want := sentRequest(t, q), sentRequest(t, fourOptionMenu("la doble")); got != want {
		t.Errorf("el nivel cambió el prompt")
	}
}

// El texto del cliente y los rótulos viajan VERBATIM entre las comillas del caso: no
// se escapan ni se recortan, ni las comillas ni los saltos de línea.
func TestPrompt_ClientTextTravelsVerbatim(t *testing.T) {
	t.Parallel()
	const text = "la \"doble\"\ny  nada más "
	q := fourOptionMenu(text)
	q.Options[0].Label = " Clásica \"de la casa\" "
	p := sentRequest(t, q).Prompt
	if !strings.HasSuffix(p, "Respuesta del cliente: \""+text+"\"") {
		t.Errorf("el texto del cliente no viaja tal cual")
	}
	if !strings.Contains(p, "\n1.  Clásica \"de la casa\" \n2. Hamburguesa doble\n") {
		t.Errorf("el rótulo de la opción no viaja tal cual")
	}
}
