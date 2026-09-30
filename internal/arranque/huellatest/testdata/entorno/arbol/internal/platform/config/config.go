// Package config es el único sitio legítimo para leer el entorno.
package config

import "os"

// Load lee el entorno: no cuenta.
func Load() string { return os.Getenv("WAPP_CONFIG") }
