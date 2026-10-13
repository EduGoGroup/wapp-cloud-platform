package lease_test

// La ayuda que comparten los tests del paquete para fabricar claves. Va en un fichero sin
// etiqueta de compilación para que cada fichero de test pueda pasar a verde por separado.

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

// newKey genera una clave Ed25519 para el test. Ninguna clave del repositorio: todas nacen aquí.
func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generando la clave del test: %v", err)
	}
	return priv
}
