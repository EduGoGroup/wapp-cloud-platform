// Package a lee el entorno por las tres vías que Entorno debe ver.
package a

import "os"

// Leer lee el entorno directamente (lo que el árbol nuevo no puede hacer).
func Leer() (string, bool, int) {
	v := os.Getenv("WAPP_X")
	_, ok := os.LookupEnv("WAPP_Y")
	os.Setenv("WAPP_Z", v) //nolint:errcheck // Setenv no es una lectura: no cuenta
	return v, ok, len(os.Environ())
}
