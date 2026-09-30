//go:build pendiente

package pendiente_test

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// TestImplementarDevuelveError: Implementar devuelve un error, nunca nil.
func TestImplementarDevuelveError(t *testing.T) {
	if err := pendiente.Implementar("contact.Service.Crear"); err == nil {
		t.Fatal("Implementar devolvió nil; se esperaba un error")
	}
}

// TestImplementarPrefijo: el mensaje empieza por "pendiente: ", con símbolo y sin él.
func TestImplementarPrefijo(t *testing.T) {
	casos := []struct {
		nombre  string
		simbolo string
	}{
		{"símbolo con paquete y método", "contact.Service.Crear"},
		{"símbolo corto", "x.Y"},
		{"símbolo vacío también lleva prefijo", ""},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			msg := pendiente.Implementar(c.simbolo).Error()
			if !strings.HasPrefix(msg, "pendiente: ") {
				t.Errorf("Implementar(%q) = %q; falta el prefijo %q", c.simbolo, msg, "pendiente: ")
			}
		})
	}
}

// TestImplementarContieneSimbolo: el mensaje contiene el símbolo literal.
func TestImplementarContieneSimbolo(t *testing.T) {
	casos := []struct {
		nombre  string
		simbolo string
	}{
		{"método de servicio", "contact.Service.Crear"},
		{"función de paquete", "x.Y"},
		{"símbolo con caracteres no ASCII", "catálogo.Añadir"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			msg := pendiente.Implementar(c.simbolo).Error()
			if !strings.Contains(msg, c.simbolo) {
				t.Errorf("Implementar(%q) = %q; no contiene el símbolo literal", c.simbolo, msg)
			}
		})
	}
}

// TestImplementarSimboloVacio: un símbolo vacío da el mensaje exacto de símbolo sin nombre,
// nunca un mensaje vacío.
func TestImplementarSimboloVacio(t *testing.T) {
	const quiero = "pendiente: (símbolo sin nombre)"
	msg := pendiente.Implementar("").Error()
	if msg == "" {
		t.Fatal("Implementar(\"\") dio un mensaje vacío")
	}
	if msg != quiero {
		t.Errorf("Implementar(\"\") = %q; quiero %q", msg, quiero)
	}
}

// TestImplementarDeterminista: dos llamadas con el mismo símbolo dan mensajes iguales.
func TestImplementarDeterminista(t *testing.T) {
	casos := []struct {
		nombre  string
		simbolo string
	}{
		{"símbolo con nombre", "contact.Service.Crear"},
		{"símbolo vacío", ""},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			a := pendiente.Implementar(c.simbolo).Error()
			b := pendiente.Implementar(c.simbolo).Error()
			if a != b {
				t.Errorf("Implementar(%q) no es determinista: %q != %q", c.simbolo, a, b)
			}
		})
	}
}

// TestImplementarPanicRecuperaError: lo que recupera recover() de
// panic(pendiente.Implementar("x.Y")) es un error cuyo texto contiene "x.Y".
func TestImplementarPanicRecuperaError(t *testing.T) {
	recuperado := func() (r any) {
		defer func() { r = recover() }()
		panic(pendiente.Implementar("x.Y"))
	}()
	err, ok := recuperado.(error)
	if !ok {
		t.Fatalf("recover() dio %T (%v); se esperaba un error", recuperado, recuperado)
	}
	if !strings.Contains(err.Error(), "x.Y") {
		t.Errorf("el error recuperado %q no contiene %q", err.Error(), "x.Y")
	}
}
