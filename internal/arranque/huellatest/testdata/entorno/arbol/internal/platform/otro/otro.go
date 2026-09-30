// Package otro está en platform, pero no es config: sí cuenta.
package otro

import "os"

// Leer lee el entorno fuera de config.
func Leer() (string, bool) { return os.LookupEnv("WAPP_OTRO") }
