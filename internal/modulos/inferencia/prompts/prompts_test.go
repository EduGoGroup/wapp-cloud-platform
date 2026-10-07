package prompts_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/prompts"
)

// writeFile deja un fichero en dir con el contenido dado.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("no se pudo escribir %s: %v", name, err)
	}
}

// compiled devuelve la plantilla compilada de una etapa, que es el oráculo de
// todo el paquete.
func compiled(t *testing.T, stage llm.Etapa) llm.Plantilla {
	t.Helper()
	p, ok := llm.PlantillaPorDefecto(stage)
	if !ok {
		t.Fatalf("la etapa %q no tiene plantilla compilada", stage)
	}
	return p
}

// fileOf arma A MANO el contenido mínimo de un fichero de plantilla, sin pasar
// por Serializar: así los tests de Cargar y Parsear no dependen del escritor.
func fileOf(p llm.Plantilla) string {
	return prompts.MarcaInstruccion + "\n" + p.Instruccion + prompts.MarcaEsquema + "\n" + p.Esquema
}

// assertTemplate compara una plantilla byte a byte, sección a sección.
func assertTemplate(t *testing.T, got, want llm.Plantilla) {
	t.Helper()
	if got.Instruccion != want.Instruccion {
		t.Errorf("instrucción distinta:\n got %q\nwant %q", got.Instruccion, want.Instruccion)
	}
	if got.Esquema != want.Esquema {
		t.Errorf("esquema distinto:\n got %q\nwant %q", got.Esquema, want.Esquema)
	}
}

// assertNoFallback comprueba que una carga fallida devuelve el Cargadas cero: un
// fallo nunca degrada al texto compilado.
func assertNoFallback(t *testing.T, loaded prompts.Cargadas) {
	t.Helper()
	if loaded.Plantillas != nil || loaded.Origen != nil {
		t.Errorf("con error, Cargar devolvió %+v; no debe degradar al compilado", loaded)
	}
}

// TestFormatLiterals: los literales del formato y el texto del centinela valen
// exactamente lo que el operador lee y escribe: un fichero escrito A MANO con
// esos textos se lee, y las constantes dicen lo mismo.
func TestFormatLiterals(t *testing.T) {
	p, err := prompts.Parsear("--- INSTRUCCION ---\nA {{version}}\n--- ESQUEMA ---\nS\n")
	if err != nil {
		t.Fatalf("Parsear de un fichero escrito con los literales = error %v", err)
	}
	assertTemplate(t, p, llm.Plantilla{Instruccion: "A 1\n", Esquema: "S\n"})

	cases := []struct{ name, got, want string }{
		{"MarcaInstruccion", prompts.MarcaInstruccion, "--- INSTRUCCION ---"},
		{"MarcaEsquema", prompts.MarcaEsquema, "--- ESQUEMA ---"},
		{"Extension", prompts.Extension, ".tmpl"},
		{"HuecoVersion", prompts.HuecoVersion, "{{version}}"},
		{"OrigenCompilado", prompts.OrigenCompilado, "compilada"},
		{"ErrPromptsDir", prompts.ErrPromptsDir.Error(), "prompts: directorio de plantillas inválido"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q; se esperaba %q", c.name, c.got, c.want)
		}
	}
}

// TestCargar_WithoutDirectoryReturnsCompiled: sin directorio (vacío o solo
// espacios) no hay error y las cuatro etapas llevan la compilada.
func TestCargar_WithoutDirectoryReturnsCompiled(t *testing.T) {
	for _, dir := range []string{"", "  \t "} {
		loaded, err := prompts.Cargar(dir)
		if err != nil {
			t.Fatalf("Cargar(%q) = error %v; sin directorio no es un error", dir, err)
		}
		if len(loaded.Plantillas) != len(llm.EtapasAjustables) || len(loaded.Origen) != len(llm.EtapasAjustables) {
			t.Fatalf("Cargar(%q) dio %d plantillas y %d orígenes; se esperaban las %d etapas",
				dir, len(loaded.Plantillas), len(loaded.Origen), len(llm.EtapasAjustables))
		}
		for _, stage := range llm.EtapasAjustables {
			assertTemplate(t, loaded.Plantillas[stage], compiled(t, stage))
			if loaded.Origen[stage] != prompts.OrigenCompilado {
				t.Errorf("Origen[%s] = %q; se esperaba %q", stage, loaded.Origen[stage], prompts.OrigenCompilado)
			}
		}
	}
}

// TestCargar_SingleStage: se ajusta UN prompt y los otros tres siguen con el
// compilado; el sufijo del nombre es libre y el origen es la ruta del fichero.
func TestCargar_SingleStage(t *testing.T) {
	dir := t.TempDir()
	edited := compiled(t, llm.EtapaP4)
	edited.Instruccion = "\nAJUSTE DEL OPERADOR.\n" + edited.Instruccion
	writeFile(t, dir, "p4-lo-que-sea.tmpl", fileOf(edited))

	loaded, err := prompts.Cargar(dir)
	if err != nil {
		t.Fatalf("Cargar = error %v; una etapa suelta es el uso normal", err)
	}
	if len(loaded.Plantillas) != len(llm.EtapasAjustables) {
		t.Fatalf("Cargar dio %d plantillas; siempre son las %d", len(loaded.Plantillas), len(llm.EtapasAjustables))
	}
	assertTemplate(t, loaded.Plantillas[llm.EtapaP4], edited)
	if want := filepath.Join(dir, "p4-lo-que-sea.tmpl"); loaded.Origen[llm.EtapaP4] != want {
		t.Errorf("Origen[p4] = %q; se esperaba la ruta %q", loaded.Origen[llm.EtapaP4], want)
	}
	for _, stage := range []llm.Etapa{llm.EtapaP2, llm.EtapaP3, llm.EtapaP5} {
		assertTemplate(t, loaded.Plantillas[stage], compiled(t, stage))
		if loaded.Origen[stage] != prompts.OrigenCompilado {
			t.Errorf("Origen[%s] = %q; sin fichero se esperaba %q", stage, loaded.Origen[stage], prompts.OrigenCompilado)
		}
	}
}

// TestCargar_StartupFailures: las cinco configuraciones que impiden el arranque.
// Cada una envuelve ErrPromptsDir, nombra el fichero y el motivo, y NO degrada al
// compilado (devuelve el Cargadas cero).
func TestCargar_StartupFailures(t *testing.T) {
	validP4 := fileOf(compiled(t, llm.EtapaP4))
	if !strings.Contains(validP4, `"package_size": 30`) {
		t.Fatalf("el esquema compilado de p4 ya no imprime %q: el caso del validador no probaría nada", `"package_size": 30`)
	}

	cases := []struct {
		name    string
		files   map[string]string
		inError []string
	}{
		{
			name:  "unknown stage prefix",
			files: map[string]string{"p6-inventada.tmpl": validP4},
			inError: []string{
				"p6-inventada.tmpl no empieza por ninguna etapa conocida (p2, p3, p4, p5) seguida de un guion",
				"se quedaría sin aplicar SIN avisar",
			},
		},
		{
			name:  "two files claim the same stage, named in sorted order",
			files: map[string]string{"p4-uno.tmpl": validP4, "p4-dos.tmpl": validP4},
			inError: []string{
				`p4-dos.tmpl y p4-uno.tmpl reclaman la etapa "p4"; deja uno solo`,
				"si le quitas la extensión .tmpl",
			},
		},
		{
			name:    "missing instruction marker",
			files:   map[string]string{"p4-x.tmpl": "--- ESQUEMA ---\n{\"version\": 1}\n"},
			inError: []string{"p4-x.tmpl: ", `falta el marcador "--- INSTRUCCION ---"`},
		},
		{
			name:    "sections in reverse order",
			files:   map[string]string{"p4-x.tmpl": "--- ESQUEMA ---\nx\n--- INSTRUCCION ---\ny\n"},
			inError: []string{"p4-x.tmpl: ", "el orden de las secciones"},
		},
		{
			name: "schema rejected by its own validator",
			files: map[string]string{
				"p4-x.tmpl": strings.Replace(validP4, `"package_size": 30`, `"package_size": 0`, 1),
			},
			inError: []string{"p4-x.tmpl no se puede servir: ", "lo rechaza su propio validador"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range c.files {
				writeFile(t, dir, name, content)
			}
			loaded, err := prompts.Cargar(dir)
			if err == nil {
				t.Fatal("Cargar no dio error; esta configuración tiene que impedir el arranque")
			}
			if !errors.Is(err, prompts.ErrPromptsDir) {
				t.Errorf("el error %q no envuelve ErrPromptsDir", err)
			}
			for _, fragment := range c.inError {
				if !strings.Contains(err.Error(), fragment) {
					t.Errorf("el error %q no contiene %q", err, fragment)
				}
			}
			assertNoFallback(t, loaded)
		})
	}
}

// TestCargar_ValidatorErrorStaysWrapped: el error del módulo llm queda envuelto,
// para que el arranque pueda distinguirlo con errors.Is.
func TestCargar_ValidatorErrorStaysWrapped(t *testing.T) {
	dir := t.TempDir()
	broken := strings.Replace(fileOf(compiled(t, llm.EtapaP4)), `"package_size": 30`, `"package_size": 0`, 1)
	writeFile(t, dir, "p4-x.tmpl", broken)

	_, err := prompts.Cargar(dir)
	if !errors.Is(err, llm.ErrPlantillaInvalida) {
		t.Errorf("el error %v no envuelve llm.ErrPlantillaInvalida", err)
	}
}

// TestCargar_UnreadableDirectory: un directorio que no se puede leer no arranca,
// y el error del sistema queda envuelto junto al centinela.
func TestCargar_UnreadableDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-existe")

	loaded, err := prompts.Cargar(missing)
	if !errors.Is(err, prompts.ErrPromptsDir) {
		t.Fatalf("Cargar(%q) = %v; se esperaba un error que envuelva ErrPromptsDir", missing, err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("el error %q no envuelve el del sistema (fs.ErrNotExist)", err)
	}
	if want := "no se puede leer " + missing; !strings.Contains(err.Error(), want) {
		t.Errorf("el error %q no contiene %q", err, want)
	}
	assertNoFallback(t, loaded)
}

// TestCargar_IgnoresNonTemplates: lo que no es un fichero con extensión .tmpl se
// ignora en silencio, aunque su nombre sea el de una etapa inexistente.
func TestCargar_IgnoresNonTemplates(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "NOTAS.md", "esto no es una plantilla y no debe romper nada")
	writeFile(t, dir, "p4-viejo.tmpl.bak", "ni esto")
	writeFile(t, dir, "p6-sin-extension", "ni esto, aunque p6 no sea una etapa")
	// La extensión se compara entera: un espacio de no separación detrás de
	// «.tmpl» hace que deje de serlo.
	writeFile(t, dir, "p6-nbsp.tmpl ", "ni esto")
	if err := os.Mkdir(filepath.Join(dir, "p6-carpeta.tmpl"), 0o750); err != nil {
		t.Fatalf("no se pudo crear el subdirectorio: %v", err)
	}

	loaded, err := prompts.Cargar(dir)
	if err != nil {
		t.Fatalf("Cargar = error %v; nada de lo que hay es una plantilla", err)
	}
	for _, stage := range llm.EtapasAjustables {
		if loaded.Origen[stage] != prompts.OrigenCompilado {
			t.Errorf("Origen[%s] = %q; se esperaba %q", stage, loaded.Origen[stage], prompts.OrigenCompilado)
		}
	}
}

// TestCargar_CaseInsensitivePrefixAndExtension: las mayúsculas no cuentan en el
// prefijo ni en la extensión; `P4-x.TMPL` carga como p4 y el origen conserva el
// nombre tal como está en disco.
func TestCargar_CaseInsensitivePrefixAndExtension(t *testing.T) {
	dir := t.TempDir()
	edited := compiled(t, llm.EtapaP4)
	edited.Instruccion = "\nAJUSTE EN MAYÚSCULAS.\n" + edited.Instruccion
	writeFile(t, dir, "P4-x.TMPL", fileOf(edited))

	loaded, err := prompts.Cargar(dir)
	if err != nil {
		t.Fatalf("Cargar = error %v; P4-x.TMPL es la plantilla de p4", err)
	}
	assertTemplate(t, loaded.Plantillas[llm.EtapaP4], edited)
	if want := filepath.Join(dir, "P4-x.TMPL"); loaded.Origen[llm.EtapaP4] != want {
		t.Errorf("Origen[p4] = %q; se esperaba %q", loaded.Origen[llm.EtapaP4], want)
	}
}

// TestCargar_AdversarialPrefixes: fuera de las mayúsculas, el prefijo no se
// normaliza. Un nombre que PARECE de p4 y no lo es byte a byte tiene que impedir
// el arranque, no quedarse sin aplicar en silencio.
func TestCargar_AdversarialPrefixes(t *testing.T) {
	validP4 := fileOf(compiled(t, llm.EtapaP4))
	cases := []struct{ name, file string }{
		{"fullwidth digit", "p４-x.tmpl"},
		{"arabic-indic digit", "p٤-x.tmpl"},
		{"leading space", " p4-x.tmpl"},
		{"space before the hyphen", "p4 -x.tmpl"},
		{"no-break space before the hyphen", "p4 -x.tmpl"},
		{"zero-width space inside the prefix", "p\u200b4-x.tmpl"},
		{"unicode hyphen", "p4‐x.tmpl"},
		{"en dash", "p4–x.tmpl"},
		{"stage without hyphen", "p4.tmpl"},
		{"longer number", "p44-x.tmpl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, c.file, validP4)

			_, err := prompts.Cargar(dir)
			if !errors.Is(err, prompts.ErrPromptsDir) {
				t.Fatalf("Cargar con %q = %v; se esperaba un error que envuelva ErrPromptsDir", c.file, err)
			}
			want := c.file + " no empieza por ninguna etapa conocida (p2, p3, p4, p5) seguida de un guion"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("el error %q no contiene %q", err, want)
			}
		})
	}
}

// TestCargar_SuffixIsFree: detrás del prefijo `p4-` cabe cualquier cosa, también
// nada y también Unicode.
func TestCargar_SuffixIsFree(t *testing.T) {
	validP4 := fileOf(compiled(t, llm.EtapaP4))
	for _, file := range []string{"p4-.tmpl", "p4--x.tmpl", "p4- cotización ４.tmpl"} {
		dir := t.TempDir()
		writeFile(t, dir, file, validP4)

		loaded, err := prompts.Cargar(dir)
		if err != nil {
			t.Errorf("Cargar con %q = error %v; solo el prefijo es contrato", file, err)
			continue
		}
		if want := filepath.Join(dir, file); loaded.Origen[llm.EtapaP4] != want {
			t.Errorf("Origen[p4] = %q; se esperaba %q", loaded.Origen[llm.EtapaP4], want)
		}
	}
}

// TestParsear_PreambleStaysOut: lo escrito antes del primer marcador es
// documentación y no viaja al modelo; {{version}} se sustituye al cargar.
func TestParsear_PreambleStaysOut(t *testing.T) {
	p, err := prompts.Parsear(
		"NOTA PARA EL QUE VENGA: esto no se manda.\n\n" +
			"--- INSTRUCCION ---\nHaz esto.\n\n--- ESQUEMA ---\nEsquema de la respuesta:\n{\"version\": {{version}}}\n")
	if err != nil {
		t.Fatalf("Parsear = error %v", err)
	}
	assertTemplate(t, p, llm.Plantilla{
		Instruccion: "Haz esto.\n\n",
		Esquema:     "Esquema de la respuesta:\n{\"version\": 1}\n",
	})
}

// TestParsear_SectionsAreVerbatim: cada sección sale tal cual, sin recortar
// bordes; solo se consume el salto que cierra la línea del marcador.
func TestParsear_SectionsAreVerbatim(t *testing.T) {
	const i, e = prompts.MarcaInstruccion, prompts.MarcaEsquema
	cases := []struct {
		name    string
		content string
		want    llm.Plantilla
	}{
		{
			name:    "leading blank line is kept",
			content: i + "\n\nA\n" + e + "\n\nS\n",
			want:    llm.Plantilla{Instruccion: "\nA\n", Esquema: "\nS\n"},
		},
		{
			name:    "text starting at the margin stays at the margin",
			content: i + "\nA" + "\n" + e + "\nS",
			want:    llm.Plantilla{Instruccion: "A\n", Esquema: "S"},
		},
		{
			name:    "trailing blank lines and spaces are kept",
			content: i + "\n  A  \n\n\n" + e + "\nS \n\n\n",
			want:    llm.Plantilla{Instruccion: "  A  \n\n\n", Esquema: "S \n\n\n"},
		},
		{
			name:    "crlf consumes one line ending and keeps the rest",
			content: i + "\r\nA\r\n" + e + "\r\nS\r\n",
			want:    llm.Plantilla{Instruccion: "A\r\n", Esquema: "S\r\n"},
		},
		{
			name:    "version placeholder replaced everywhere in both sections",
			content: i + "\nv{{version}} y {{version}}\n" + e + "\n{{version}}{{version}}\n",
			want:    llm.Plantilla{Instruccion: "v1 y 1\n", Esquema: "11\n"},
		},
		{
			name:    "text before the marker on its line is preamble",
			content: "nota " + i + "\nA\n" + e + "\nS\n",
			want:    llm.Plantilla{Instruccion: "A\n", Esquema: "S\n"},
		},
		{
			name:    "duplicated instruction marker stays as text",
			content: i + "\nA\n" + i + "\nB\n" + e + "\nS\n",
			want:    llm.Plantilla{Instruccion: "A\n" + i + "\nB\n", Esquema: "S\n"},
		},
		{
			name:    "duplicated schema marker stays as text",
			content: i + "\nA\n" + e + "\nS1\n" + e + "\nS2\n",
			want:    llm.Plantilla{Instruccion: "A\n", Esquema: "S1\n" + e + "\nS2\n"},
		},
		{
			name:    "instruction marker after the schema cuts it and the rest is dropped",
			content: i + "\nA\n" + e + "\nS\n" + i + "\nSOBRA\n",
			want:    llm.Plantilla{Instruccion: "A\n", Esquema: "S\n"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := prompts.Parsear(c.content)
			if err != nil {
				t.Fatalf("Parsear(%q) = error %v", c.content, err)
			}
			assertTemplate(t, got, c.want)
		})
	}
}

// TestParsear_Rejects: los fallos del formato, con su texto exacto.
func TestParsear_Rejects(t *testing.T) {
	const i, e = prompts.MarcaInstruccion, prompts.MarcaEsquema
	const (
		aloneInstruction = `el marcador "--- INSTRUCCION ---" tiene que ir SOLO en su línea`
		aloneSchema      = `el marcador "--- ESQUEMA ---" tiene que ir SOLO en su línea`
	)
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"empty content", "", `falta el marcador "--- INSTRUCCION ---"`},
		{"missing instruction marker", e + "\nS\n", `falta el marcador "--- INSTRUCCION ---"`},
		{"missing schema marker", i + "\nA\n", `falta el marcador "--- ESQUEMA ---"`},
		{
			"schema before instruction is reported as order, not as missing",
			e + "\nS\n" + i + "\nA\n",
			`"--- ESQUEMA ---" va antes que "--- INSTRUCCION ---": el orden de las secciones es el del prompt`,
		},
		{"markers glued together", i + e + "\nS\n", aloneInstruction},
		{"instruction marker glued to itself", i + i + "\nA\n" + e + "\nS\n", aloneInstruction},
		{"space after the instruction marker", i + " \nA\n" + e + "\nS\n", aloneInstruction},
		{"no-break space after the instruction marker", i + " \nA\n" + e + "\nS\n", aloneInstruction},
		{"schema marker glued to itself", i + "\nA\n" + e + e + "\nS\n", aloneSchema},
		{"schema marker at the end of the file", i + "\nA\n" + e, aloneSchema},
		{"adjacent markers leave the instruction empty", i + "\n" + e + "\nS\n", `la sección "--- INSTRUCCION ---" está vacía`},
		{"whitespace-only instruction", i + "\n \t\n\n" + e + "\nS\n", `la sección "--- INSTRUCCION ---" está vacía`},
		{"whitespace-only schema", i + "\nA\n" + e + "\n\n  \n", `la sección "--- ESQUEMA ---" está vacía`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := prompts.Parsear(c.content)
			if err == nil {
				t.Fatalf("Parsear(%q) = %+v sin error; se esperaba %q", c.content, got, c.want)
			}
			if err.Error() != c.want {
				t.Errorf("Parsear(%q) = error %q; se esperaba %q", c.content, err, c.want)
			}
			if got != (llm.Plantilla{}) {
				t.Errorf("con error, Parsear devolvió %+v; se esperaba la plantilla cero", got)
			}
		})
	}
}
