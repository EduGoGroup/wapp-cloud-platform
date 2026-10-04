package candados

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

const (
	// FileLinesTarget es el tamaño al que apunta todo fichero .go del árbol nuevo (05 E-13).
	FileLinesTarget = 500
	// FileLinesLimit es el tope duro: FileLinesTarget más un 20 % de tolerancia. Por encima,
	// el fichero se parte por tema (05 E-13, decisión de Jhoan, 2026-10-04).
	FileLinesLimit = 600
)

// FileSize es el candado del tamaño de fichero (05 E-13, D-R-7): devuelve una violación por
// cada fuente cuyo número de líneas (las que cuenta wc -l en un fichero terminado en salto de
// línea) pasa de FileLinesLimit, salvo las de OversizedFiles, que pueden quedarse como están
// pero no crecer: una de ellas viola si pasa del techo que la lista le da. Entre
// FileLinesTarget y FileLinesLimit no viola (la tolerancia del 20 %).
//
// Fichero es la Ruta de la fuente; Motivo empieza por "E-13: " y dice las líneas, el tope (o
// el techo) y que se parte por tema, solo moviendo declaraciones, con el sufijo de su origen.
// La salida va ordenada por Fichero. Mira tests y producción por igual; las rutas de testdata
// no le llegan (Recorrer no las recorre).
func FileSize(fuentes []Fuente) []Violacion {
	panic(pendiente.Implementar("candados.FileSize"))
}

// OversizedFiles devuelve la lista CERRADA de ficheros del árbol nuevo que ya pasaban de
// FileLinesLimit cuando nació E-13 (2026-10-04), con su techo: el número de líneas que tenían.
// No se parten en la sesión que puso la regla (no eran suyos) y se parten cuando su fase o una
// sesión dedicada los toque; mientras, no pueden crecer. Devuelve una copia nueva en cada
// llamada. Quitar un fichero de la lista es lo esperado; añadir uno exige una decisión en
// documentations/reorganizacion-modular/plan/DECISIONES.md.
func OversizedFiles() map[string]int {
	panic(pendiente.Implementar("candados.OversizedFiles"))
}
