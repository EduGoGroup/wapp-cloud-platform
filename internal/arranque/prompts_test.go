package arranque

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/prompts"
)

// Los tests de este fichero fijan las dos promesas de cargarPlantillasDePrompt (F4 · T4.26,
// R4.3.c y R4.3.d): una plantilla inválida ABORTA el arranque con un error reconocible, y un
// arranque sano deja dicho en el log de dónde salió el prompt de cada etapa. Sin red ni BD: el
// directorio se genera con prompts.Volcar, que es como lo genera un operador.

// promptsLogLine es el mensaje de la línea que el arranque escribe al cargar las plantillas.
const promptsLogLine = "prompts: plantillas de las etapas ajustables"

// dumpedPromptsDir vuelca en un directorio temporal las cuatro plantillas compiladas y devuelve
// el directorio y la ruta de cada fichero, en el orden de llm.EtapasAjustables.
func dumpedPromptsDir(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	paths, err := prompts.Volcar(dir)
	if err != nil {
		t.Fatalf("prompts.Volcar: %v", err)
	}
	if len(paths) != len(llm.EtapasAjustables) {
		t.Fatalf("Volcar escribió %d ficheros; se esperan %d, uno por etapa ajustable", len(paths), len(llm.EtapasAjustables))
	}
	return dir, paths
}

// capturedLogLine devuelve la línea del log capturado que lleva el mensaje de las plantillas.
func capturedLogLine(t *testing.T, logged string) string {
	t.Helper()
	for line := range strings.SplitSeq(logged, "\n") {
		if strings.Contains(line, promptsLogLine) {
			return line
		}
	}
	t.Fatalf("el arranque no escribió la línea %q; log capturado:\n%s", promptsLogLine, logged)
	return ""
}

// R4.3.c: en el esquema no puede haber un valor que su propio validador rechace (P4 fue 0 de 14 en
// campo por un `"package_size": 0`). Una plantilla así aborta el arranque: el error lleva el
// prefijo del arranque, envuelve el prompts.ErrPromptsDir NUEVO y no se devuelve ninguna plantilla
// ni se escribe la línea de log, que diría que esas plantillas corren.
func TestLoadPromptTemplates_InvalidTemplateAbortsTheBoot(t *testing.T) {
	const valid, invalid = `"package_size": 30`, `"package_size": 0`
	dir, paths := dumpedPromptsDir(t)
	broken := 0
	for _, path := range paths {
		content, err := os.ReadFile(path) //nolint:gosec // ruta de un fichero que este test acaba de volcar en t.TempDir
		if err != nil {
			t.Fatalf("leyendo %s: %v", path, err)
		}
		if !strings.Contains(string(content), valid) {
			continue
		}
		edited := strings.Replace(string(content), valid, invalid, 1)
		err = os.WriteFile(path, []byte(edited), 0o600) //nolint:gosec // ídem: la misma ruta, dentro de t.TempDir
		if err != nil {
			t.Fatalf("escribiendo %s: %v", path, err)
		}
		broken++
	}
	if broken != 1 {
		t.Fatalf("%d plantillas volcadas imprimen %s; se espera 1 (la de P4): el caso no probaría nada", broken, valid)
	}

	var logged bytes.Buffer
	got, err := cargarPlantillasDePrompt(sharedlogger.New(sharedlogger.WithWriter(&logged)), dir)

	if err == nil {
		t.Fatal("una plantilla con `\"package_size\": 0` no abortó el arranque")
	}
	const prefix = "prompts ajustables de P2-P5: "
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("el error no empieza por %q: %q", prefix, err.Error())
	}
	if !errors.Is(err, prompts.ErrPromptsDir) {
		t.Errorf("el error no envuelve el prompts.ErrPromptsDir nuevo: %v", err)
	}
	if got != nil {
		t.Errorf("junto al error devolvió %d plantillas; se espera nil", len(got))
	}
	if strings.Contains(logged.String(), promptsLogLine) {
		t.Errorf("el arranque abortado escribió la línea de plantillas cargadas:\n%s", logged.String())
	}
}

// R4.3.d: con un directorio válido el arranque devuelve una plantilla por etapa ajustable y la
// línea de log trae el directorio y, para p2…p5, el fichero del que salió cada una. Es lo que
// contesta «¿qué prompt corrió aquí?» sin leer el código compilado.
func TestLoadPromptTemplates_ValidDirLogsTheOriginOfEveryStage(t *testing.T) {
	dir, paths := dumpedPromptsDir(t)

	var logged bytes.Buffer
	got, err := cargarPlantillasDePrompt(sharedlogger.New(sharedlogger.WithWriter(&logged)), dir)
	if err != nil {
		t.Fatalf("cargarPlantillasDePrompt sobre un volcado sin editar: %v", err)
	}

	line := capturedLogLine(t, logged.String())
	if !strings.Contains(line, dir) {
		t.Errorf("la línea no dice el directorio %s: %s", dir, line)
	}
	wantKeys := []string{"p2", "p3", "p4", "p5"}
	if len(llm.EtapasAjustables) != len(wantKeys) {
		t.Fatalf("hay %d etapas ajustables; este test conoce %v", len(llm.EtapasAjustables), wantKeys)
	}
	for i, stage := range llm.EtapasAjustables {
		if string(stage) != wantKeys[i] {
			t.Errorf("la etapa ajustable %d es %q; se espera %q", i, stage, wantKeys[i])
		}
		if _, ok := got[stage]; !ok {
			t.Errorf("no se devolvió la plantilla de %s", stage)
		}
		if !strings.Contains(line, wantKeys[i]+"=") {
			t.Errorf("la línea no trae el campo %s: %s", wantKeys[i], line)
		}
		if !strings.Contains(line, paths[i]) {
			t.Errorf("la línea no dice que %s salió de %s: %s", wantKeys[i], paths[i], line)
		}
	}
	if len(got) != len(llm.EtapasAjustables) {
		t.Errorf("se devolvieron %d plantillas; se espera una por etapa ajustable (%d)", len(got), len(llm.EtapasAjustables))
	}
}

// Sin directorio (la palanca apagada) el arranque NO falla: corre el texto compilado, devuelve
// las cuatro plantillas y la línea lo dice en vez de dejar el campo `dir` vacío.
func TestLoadPromptTemplates_NoDirRunsTheCompiledText(t *testing.T) {
	var logged bytes.Buffer
	got, err := cargarPlantillasDePrompt(sharedlogger.New(sharedlogger.WithWriter(&logged)), "")
	if err != nil {
		t.Fatalf("cargarPlantillasDePrompt sin directorio: %v", err)
	}
	if len(got) != len(llm.EtapasAjustables) {
		t.Errorf("se devolvieron %d plantillas; se espera una por etapa ajustable (%d)", len(got), len(llm.EtapasAjustables))
	}
	line := capturedLogLine(t, logged.String())
	if !strings.Contains(line, "(ninguno: corre el texto compilado en shared/wapp-shared/llm)") {
		t.Errorf("sin directorio la línea no dice que corre el texto compilado: %s", line)
	}
	for _, stage := range llm.EtapasAjustables {
		if !strings.Contains(line, string(stage)+"=") {
			t.Errorf("la línea no trae el campo %s: %s", stage, line)
		}
	}
}
