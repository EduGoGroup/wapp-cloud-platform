package candados

import (
	"fmt"
	"maps"
	"sort"
)

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
	vs := make([]Violacion, 0)
	for _, f := range fuentes {
		lines := f.Fset.File(f.Archivo.Pos()).LineCount()
		if ceiling, ok := oversizedFiles[f.Ruta]; ok {
			if lines > ceiling {
				vs = append(vs, Violacion{Fichero: f.Ruta, Motivo: fmt.Sprintf(
					"E-13: %d líneas pasan del techo de %d que le da OversizedFiles (ya pasaba de %d): no puede crecer; "+
						"pártelo por tema, solo moviendo declaraciones, con el sufijo de su origen", lines, ceiling, FileLinesLimit)})
			}
			continue
		}
		if lines > FileLinesLimit {
			vs = append(vs, Violacion{Fichero: f.Ruta, Motivo: fmt.Sprintf(
				"E-13: %d líneas pasan del tope de %d (%d + 20 %%): pártelo por tema, solo moviendo declaraciones, "+
					"con el sufijo de su origen", lines, FileLinesLimit, FileLinesTarget)})
		}
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].Fichero < vs[j].Fichero })
	return vs
}

// OversizedFiles devuelve la lista CERRADA de ficheros del árbol nuevo que ya pasaban de
// FileLinesLimit cuando nació E-13 (2026-10-04), con su techo: el número de líneas que tenían.
// No se parten en la sesión que puso la regla (no eran suyos) y se parten cuando su fase o una
// sesión dedicada los toque; mientras, no pueden crecer. Devuelve una copia nueva en cada
// llamada. Quitar un fichero de la lista es lo esperado; añadir uno exige una decisión en
// documentations/reorganizacion-modular/plan/DECISIONES.md.
func OversizedFiles() map[string]int {
	// Clone y no el mapa: devolver la variable dejaría a cualquier llamante ampliar la
	// excepción (o subir un techo) para todos los demás.
	return maps.Clone(oversizedFiles)
}

// oversizedFiles es el mapa del que OversizedFiles devuelve copias, medido con wc -l el
// 2026-10-04 sobre la rama de F2-03 (D-R-7). No se exporta: nadie fuera de este fichero lo
// lee ni lo cambia.
var oversizedFiles = map[string]int{
	"internal/arranque/huellatest/huellatest.go":      732, // F0 · la huella de los arranques
	"internal/arranque/huellatest/huellatest_test.go": 669, // F0
	"internal/candados/sinbdviva_openers_test.go":     629, // F9 · D-F9-6
	"internal/nucleo/contact/repository_postgres.go":  618, // F1 · el piloto
}
