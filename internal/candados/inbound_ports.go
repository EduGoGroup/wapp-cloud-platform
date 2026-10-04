package candados

import "slices"

// inboundPortDirsWithoutSuite es la lista de la que InboundPortDirsWithoutSuite devuelve
// copias. No se exporta: nadie fuera de este fichero la lee ni la cambia.
var inboundPortDirsWithoutSuite = []string{
	// D-F2-5 (Jhoan, 2026-09-30): los ports/in de iam los cubre el test del usecase.
	"internal/modulos/acceso/iam/ports/in",
}

// InboundPortDirsWithoutSuite devuelve la lista CERRADA de directorios de puertos de ENTRADA
// que, por decisión escrita, no llevan suite de contrato: en ellos, un fichero solo de
// interfaces queda exento de un_fichero_un_test aunque no haya suite Contrato en
// <dir>/<paquete>helpertest (UnFicheroUnTest, excepción del puerto).
//
// Promesas:
//   - cada directorio va relativo a la raíz del repo y con «/», la misma forma que
//     path.Dir(Fuente.Ruta), y se compara por IGUALDAD: ni un subdirectorio de uno listado
//     ni un hermano que comparta prefijo (…/ports/inbound frente a …/ports/in) quedan
//     dentro;
//   - devuelve una copia nueva en cada llamada: mutar el slice devuelto no cambia lo que
//     devuelve la siguiente, así que nadie amplía la excepción en tiempo de ejecución;
//   - hoy contiene exactamente internal/modulos/acceso/iam/ports/in: sus puertos de entrada
//     los cubre el test del usecase que los implementa (D-F2-5, Jhoan, 2026-09-30; excepción
//     aplicada en el candado el 2026-10-04).
//
// Añadir un directorio exige antes una decisión en
// documentations/reorganizacion-modular/plan/DECISIONES.md; sin ella, el puerto lleva su
// suite como manda 05 E-3.
func InboundPortDirsWithoutSuite() []string {
	// Clone y no el slice: devolver la variable dejaría a cualquier llamante ampliar la
	// excepción (o borrarla) para todos los demás.
	return slices.Clone(inboundPortDirsWithoutSuite)
}
