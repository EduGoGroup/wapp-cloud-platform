package procesos

import (
	e "os"
	tt "testing"
)

// Un alias de os no es sospechoso por sí mismo: leer UNA variable concreta con Getenv sigue
// siendo legítimo, se llame como se llame el paquete en el fichero. Lo mismo con testing:
// Verbose no salta nada.
func TestGetenvBehindAlias(t *tt.T) {
	binary := e.Getenv("WAPP_PROCESOS_BINARIO")
	if binary == "" {
		t.Fatal("falta el binario del servidor: un proceso que no puede correr falla")
	}
	if tt.Verbose() {
		t.Log("binario", binary)
	}
}
