package procesos

import (
	"os"
	"testing"
)

// fakeServer tiene métodos PROPIOS que se llaman como los perseguidos. En un fichero sin
// import de punto de os ni de testing no disparan: el receptor no es ninguno de los dos
// paquetes, y su declaración tampoco.
type fakeServer struct{ env []string }

func (s fakeServer) Environ() []string { return s.env }

func (s fakeServer) Short() bool { return len(s.env) == 0 }

func TestOwnMethods(t *testing.T) {
	s := fakeServer{env: []string{"WAPP_ENV=" + os.Getenv("WAPP_ENV")}}
	if s.Short() {
		t.Fatal("el entorno armado desde cero no puede quedar vacío")
	}
	read := s.Environ
	t.Log(s.Environ(), read())
}
