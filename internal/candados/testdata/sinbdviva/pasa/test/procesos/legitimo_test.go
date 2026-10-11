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
	edge := os.Getenv("WAPP_PROCESOS_EDGE_DIR")
	if edge == "" {
		t.Fatal("falta el repo del Edge: un proceso que no puede correr falla")
	}
	t.Log("edge", edge)
}
