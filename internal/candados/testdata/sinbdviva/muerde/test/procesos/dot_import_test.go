package procesos

import (
	. "os"
	. "testing"
)

// Con import de punto no hay receptor: Environ y Short quedan como identificadores sueltos,
// y siguen siendo los de os y testing.
var dotEnv = Environ()

func TestShortBehindDotImport(t *T) {
	if Short() {
		return
	}
	t.Log(dotEnv)
}
