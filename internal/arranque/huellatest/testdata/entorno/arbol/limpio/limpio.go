// Package limpio no lee el entorno.
package limpio

import "os"

// Salir usa os, pero no lee el entorno.
func Salir() { os.Exit(0) }
