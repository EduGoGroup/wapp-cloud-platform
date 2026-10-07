package catalogimport_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
)

// TestImportPrompt_MatchesTheOldPromptByteForByte: el prompt se porta literal. Nunca
// se probó contra un LLM real (deuda D-20) y no se va a probar (cero gasto, D-11):
// «mejorarlo» de paso sería cambiar una hipótesis por otra (reglas.md T-9). El
// fixture es la salida del paquete viejo (ver template_test.go).
func TestImportPrompt_MatchesTheOldPromptByteForByte(t *testing.T) {
	want := readFixture(t, "import_prompt.txt")
	if got := catalogimport.ImportPrompt(); got != want {
		t.Errorf("el prompt NO coincide con testdata/import_prompt.txt.\n--- esperado ---\n%s\n--- obtenido ---\n%s", want, got)
	}
}

// TestImportPrompt_IsReviewedWithTheContract es el mecanismo que hace REAL el
// «versionado junto al contrato» del design §6: subir ImportVersion pone la suite en
// rojo hasta que alguien abra prompt.go.
func TestImportPrompt_IsReviewedWithTheContract(t *testing.T) {
	if catalogimport.PromptContractVersion != catalogimport.ImportVersion {
		t.Fatalf("PromptContractVersion=%d e ImportVersion=%d: el contrato del import cambió y el prompt-plantilla no se revisó. "+
			"Lee prompt.go, decide si el texto sigue diciendo la verdad y sube el número EN ESE MISMO commit.",
			catalogimport.PromptContractVersion, catalogimport.ImportVersion)
	}
}

// TestImportPrompt_DictatesTheContractTheValidatorDemands: el prompt le dice al LLM
// exactamente el format y la versión que este validador acepta. Escritos a mano se
// quedarían viejos al primer cambio de contrato y el dueño culparía a su LLM de un
// documento que rechazamos nosotros.
func TestImportPrompt_DictatesTheContractTheValidatorDemands(t *testing.T) {
	prompt := catalogimport.ImportPrompt()

	header := "(format: " + strconv.Quote(catalogimport.ImportFormat) + ", version: " + strconv.Itoa(catalogimport.ImportVersion) + ")"
	if !strings.Contains(prompt, header) {
		t.Errorf("el prompt no dicta la cabecera del contrato %s:\n%s", header, prompt)
	}
	// Las piezas del contrato que un LLM se salta si no se las nombran.
	for _, field := range []string{"sku", "price", "variants", "components"} {
		if !strings.Contains(prompt, field) {
			t.Errorf("el prompt no menciona %q:\n%s", field, prompt)
		}
	}
	if strings.Contains(prompt, "%!") {
		t.Errorf("el prompt tiene un verbo de formato sin sustituir:\n%s", prompt)
	}
	if strings.ContainsAny(prompt, "\n`") {
		t.Errorf("el prompt va en una sola línea y sin acentos graves de Markdown:\n%s", prompt)
	}
	if again := catalogimport.ImportPrompt(); again != prompt {
		t.Error("ImportPrompt devuelve textos distintos en dos llamadas")
	}
}
