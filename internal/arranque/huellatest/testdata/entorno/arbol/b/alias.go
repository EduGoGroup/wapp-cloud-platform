// Package b lee el entorno con el import de os renombrado.
package b

import sistema "os"

// Leer lee el entorno con alias.
func Leer() string { return sistema.Getenv("WAPP_ALIAS") }
