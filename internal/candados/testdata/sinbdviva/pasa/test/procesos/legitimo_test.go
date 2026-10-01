package procesos

import (
	"os"
	"testing"
)

// Lo legítimo: leer UNA variable concreta con os.Getenv, fallar con t.Fatal y registrar con
// t.Log. Un comentario que cite os.Environ(), t.Skip, t.SkipNow, t.Skipf o testing.Short()
// no cuenta: el candado mira el AST, no el texto.
func TestLegitimo(t *testing.T) {
	t.Helper()
	binario := os.Getenv("WAPP_PROCESOS_BINARIO")
	if binario == "" {
		t.Fatal("falta el binario del servidor: un proceso que no puede correr falla")
	}
	t.Log("binario", binario)
}
