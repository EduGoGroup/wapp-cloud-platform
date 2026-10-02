package procesos

import (
	e "os"
	tt "testing"
)

// Con alias el receptor ya no se llama os ni testing, pero es el mismo paquete: el servidor
// heredaría el entorno del shell igual, y el proceso se saltaría igual.
var aliasedEnv = e.Environ()

func TestShortBehindAlias(t *tt.T) {
	if tt.Short() {
		return
	}
	t.Log(aliasedEnv)
}
