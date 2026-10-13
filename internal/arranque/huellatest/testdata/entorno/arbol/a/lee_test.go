package a

import (
	"os"
	"testing"
)

// TestNoCuenta: un test puede leer el entorno.
func TestNoCuenta(t *testing.T) { _ = os.Getenv("WAPP_TEST") }
