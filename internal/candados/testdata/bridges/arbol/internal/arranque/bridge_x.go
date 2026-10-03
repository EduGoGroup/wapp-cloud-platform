// Package arranque es un árbol de prueba de candados.WalkBridges.
package arranque

// bridgeX es un adaptador de arranque: el único fichero de producción que debe salir.
func bridgeX(a, b int) int {
	if a > b {
		return a
	}
	return b
}
