//go:build pendiente

package prompts_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/prompts"
)

// preambleBody es el preámbulo que Serializar escribe detrás de su primera línea
// (la que nombra la etapa), byte a byte: es texto que el operador lee al abrir el
// fichero.
const preambleBody = "\n" +
	"Esto de aquí arriba, ANTES del primer marcador, es documentación y NO se le manda\n" +
	"al modelo. Escribe aquí lo que haga falta para el que venga detrás.\n" +
	"\n" +
	"Cómo se usa este fichero:\n" +
	"  - Edítalo y REINICIA el cloud. No hay recarga en caliente, a propósito.\n" +
	"  - Si te equivocas, el cloud NO ARRANCA y te dice qué fichero y por qué.\n" +
	"  - {{version}} se sustituye por la versión de artefacto que el código sabe leer.\n" +
	"  - El texto entre marcadores se preserva EXACTO, líneas en blanco incluidas.\n" +
	"\n" +
	"🔴 EN EL ESQUEMA NO PUEDE HABER UN VALOR QUE EL VALIDADOR RECHACE. El modelo COPIA\n" +
	"el ejemplo: un 0 escrito ahí es un 0 en su respuesta. Los `...` sí pueden quedarse\n" +
	"—son huecos reconocibles y se detectan si el modelo los ecoa—, pero un número tiene\n" +
	"que ser válido tal cual está impreso. Esto ya costó una etapa entera: P4 fue 0 de 14\n" +
	"en su primer día en campo porque su esquema imprimía `\"package_size\": 0`.\n" +
	"\n"

// TestVolcarYCargar_ReturnsExactlyTheCompiledTemplate sostiene todo el ciclo:
// volcar → cargar da, byte a byte, la plantilla que corre sin directorio, en las
// cuatro etapas. Si no fuera exacto, encender WAPP_LLM_PROMPTS_DIR cambiaría los
// prompts sin que nadie hubiera editado nada (un TrimSpace en Parsear cambia P5).
func TestVolcarYCargar_ReturnsExactlyTheCompiledTemplate(t *testing.T) {
	dir := t.TempDir()
	paths, err := prompts.Volcar(dir)
	if err != nil {
		t.Fatalf("Volcar = error %v", err)
	}
	if len(paths) != len(llm.EtapasAjustables) {
		t.Fatalf("Volcar devolvió %d rutas; se esperaban %d", len(paths), len(llm.EtapasAjustables))
	}

	loaded, err := prompts.Cargar(dir)
	if err != nil {
		t.Fatalf("Cargar del directorio recién volcado = error %v", err)
	}
	for n, stage := range llm.EtapasAjustables {
		t.Run(string(stage), func(t *testing.T) {
			assertTemplate(t, loaded.Plantillas[stage], compiled(t, stage))
			if loaded.Origen[stage] != paths[n] {
				t.Errorf("Origen[%s] = %q; con fichero presente se esperaba su ruta %q", stage, loaded.Origen[stage], paths[n])
			}
		})
	}
}

// TestSerializar_IsTheExactInverseOfParsear: Parsear(Serializar(p)) == p, byte a
// byte, sobre las cuatro compiladas y sobre plantillas con bordes incómodos.
func TestSerializar_IsTheExactInverseOfParsear(t *testing.T) {
	cases := map[string]llm.Plantilla{
		"no trailing newline":       {Instruccion: "pegada al margen", Esquema: `{"version": 1}`},
		"blank lines on both edges": {Instruccion: "\n\nA\n\n\n", Esquema: "\n\n{\"version\": 1}\n\n"},
		"crlf line endings":         {Instruccion: "\r\nA\r\n", Esquema: "{\"version\": 1}\r\n"},
		"version repeated":          {Instruccion: "A\n", Esquema: "{\"version\": 1, \"otra\": {\"version\": 1}}\n"},
	}
	for _, stage := range llm.EtapasAjustables {
		cases["compiled "+string(stage)] = compiled(t, stage)
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := prompts.Parsear(prompts.Serializar(llm.EtapaP4, want))
			if err != nil {
				t.Fatalf("Parsear(Serializar) = error %v", err)
			}
			assertTemplate(t, got, want)
		})
	}
}

// TestSerializar_FileLayout: el contenido del fichero, byte a byte — la línea de
// la etapa con su frase, el preámbulo con el aviso del `"package_size": 0`, los
// marcadores y las secciones tal cual, y el hueco {{version}} en lugar del número
// SOLO en su primera aparición.
func TestSerializar_FileLayout(t *testing.T) {
	p := llm.Plantilla{
		Instruccion: "\nHaz esto.\n\n",
		Esquema:     "Esquema de la respuesta:\n{\"version\": 1, \"anidada\": {\"version\": 1}}\n",
	}
	for _, stage := range llm.EtapasAjustables {
		want := "Prompt de la etapa " + strings.ToUpper(string(stage)) + " — " + prompts.QueHaceLaEtapa[stage] + "\n" +
			preambleBody +
			"--- INSTRUCCION ---\n" +
			"\nHaz esto.\n\n" +
			"--- ESQUEMA ---\n" +
			"Esquema de la respuesta:\n{\"version\": {{version}}, \"anidada\": {\"version\": 1}}\n"
		if got := prompts.Serializar(stage, p); got != want {
			t.Errorf("Serializar(%s) distinto:\n got %q\nwant %q", stage, got, want)
		}
	}
}

// TestVolcar_NamesAndDescriptions: las dos tablas de datos valen sus literales,
// una entrada por etapa ajustable, y los nombres son los que quedan en disco.
func TestVolcar_NamesAndDescriptions(t *testing.T) {
	wantNames := map[llm.Etapa]string{
		llm.EtapaP2: "p2-extraer-ideas.tmpl",
		llm.EtapaP3: "p3-especificar-item.tmpl",
		llm.EtapaP4: "p4-normalizar-cantidades.tmpl",
		llm.EtapaP5: "p5-redactar-cotizacion.tmpl",
	}
	dir := t.TempDir()
	if _, err := prompts.Volcar(dir); err != nil {
		t.Fatalf("Volcar = error %v", err)
	}
	for stage, name := range wantNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("Volcar no dejó en disco el fichero de %s con su nombre literal %q: %v", stage, name, err)
		}
	}
	if !maps.Equal(prompts.NombreDeFichero, wantNames) {
		t.Errorf("NombreDeFichero = %v; se esperaba %v", prompts.NombreDeFichero, wantNames)
	}
	wantDescriptions := map[llm.Etapa]string{
		llm.EtapaP2: "saca las ideas principales del mensaje: una entrada por cosa distinta que pide el cliente.",
		llm.EtapaP3: "especifica UN ítem —producto, variante, añadidos, personalizaciones—. Se llama una vez por ítem.",
		llm.EtapaP4: "normaliza cantidades, paquetes y rangos, y resuelve la fecha de entrega contra la del mensaje.",
		llm.EtapaP5: "redacta el mensaje de cotización con la voz del negocio, copiando importes del borrador.",
	}
	if !maps.Equal(prompts.QueHaceLaEtapa, wantDescriptions) {
		t.Errorf("QueHaceLaEtapa = %v; se esperaba %v", prompts.QueHaceLaEtapa, wantDescriptions)
	}
	if len(wantNames) != len(llm.EtapasAjustables) {
		t.Errorf("hay %d etapas ajustables y la tabla cubre %d", len(llm.EtapasAjustables), len(wantNames))
	}
}

// TestVolcar_WritesTheFourFiles: crea el directorio con sus padres, escribe un
// fichero por etapa con Serializar de la compilada y devuelve las rutas en el
// orden de llm.EtapasAjustables; ficheros 0o600, directorio no más abierto que
// 0o750.
func TestVolcar_WritesTheFourFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "aun", "no-existe")

	paths, err := prompts.Volcar(dir)
	if err != nil {
		t.Fatalf("Volcar = error %v", err)
	}
	if len(paths) != len(llm.EtapasAjustables) {
		t.Fatalf("Volcar devolvió %d rutas; se esperaban %d", len(paths), len(llm.EtapasAjustables))
	}
	for n, stage := range llm.EtapasAjustables {
		want := filepath.Join(dir, prompts.NombreDeFichero[stage])
		if paths[n] != want {
			t.Errorf("rutas[%d] = %q; se esperaba %q", n, paths[n], want)
		}
		raw, err := os.ReadFile(want)
		if err != nil {
			t.Fatalf("no se pudo leer el fichero volcado: %v", err)
		}
		if string(raw) != prompts.Serializar(stage, compiled(t, stage)) {
			t.Errorf("%s no contiene Serializar de la plantilla compilada de %s", want, stage)
		}
		info, err := os.Stat(want)
		if err != nil {
			t.Fatalf("no se pudo mirar el fichero volcado: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s tiene permisos %o; se esperaba 600", want, perm)
		}
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Volcar no creó el directorio: %v", err)
	}
	if perm := info.Mode().Perm(); perm&^0o750 != 0 {
		t.Errorf("el directorio tiene permisos %o; no debe ser más abierto que 750", perm)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no se pudo listar el directorio: %v", err)
	}
	if len(entries) != len(llm.EtapasAjustables) {
		t.Errorf("el directorio tiene %d entradas; se esperaban solo los %d ficheros", len(entries), len(llm.EtapasAjustables))
	}
}

// TestVolcar_DoesNotOverwrite: un fichero que ya existe se respeta, y el error
// lo nombra. Perderle a alguien sus ajustes por un comando de más no es aceptable.
func TestVolcar_DoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	const tuned = "AJUSTES DE UNA TARDE ENTERA"
	existing := filepath.Join(dir, prompts.NombreDeFichero[llm.EtapaP4])
	writeFile(t, dir, prompts.NombreDeFichero[llm.EtapaP4], tuned)

	paths, err := prompts.Volcar(dir)
	if !errors.Is(err, prompts.ErrPromptsDir) {
		t.Fatalf("Volcar sobre un fichero existente = %v; se esperaba un error que envuelva ErrPromptsDir", err)
	}
	want := existing + " ya existe y NO se sobrescribe; bórralo tú si de verdad quieres perder lo que tiene"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("el error %q no contiene %q", err, want)
	}
	if paths != nil {
		t.Errorf("con error, Volcar devolvió las rutas %v; se esperaba ninguna", paths)
	}
	raw, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("no se pudo releer el fichero existente: %v", err)
	}
	if string(raw) != tuned {
		t.Errorf("Volcar pisó el fichero existente: ahora contiene %q", raw)
	}
}

// TestVolcar_EmptyDirectory: sin directorio (vacío o solo espacios) no vuelca en
// ningún sitio y lo dice.
func TestVolcar_EmptyDirectory(t *testing.T) {
	for _, dir := range []string{"", " \t "} {
		paths, err := prompts.Volcar(dir)
		if !errors.Is(err, prompts.ErrPromptsDir) {
			t.Fatalf("Volcar(%q) = %v; se esperaba un error que envuelva ErrPromptsDir", dir, err)
		}
		if !strings.Contains(err.Error(), "volcar necesita un directorio") {
			t.Errorf("el error %q no contiene %q", err, "volcar necesita un directorio")
		}
		if paths != nil {
			t.Errorf("con error, Volcar(%q) devolvió las rutas %v; se esperaba ninguna", dir, paths)
		}
	}
}
