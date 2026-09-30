package candados

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

// ExportadosCubiertos exige que cada exportado de x.go aparezca como identificador en su
// x_test.go (05 E-9, «se cumple ya en rojo»).
//
// Solo mira los x.go de producción cuyo x_test.go (mismo directorio) está entre fuentes; un
// x.go sin test no es asunto de este candado (lo es de UnFicheroUnTest). Quedan fuera los
// paquetes cuyo nombre termina en "test" (D-F1-3 = sí, la misma condición que
// UnFicheroUnTest).
//
// Exportados de x.go: las funciones, tipos, variables y constantes de primer nivel con nombre
// exportado, y los métodos exportados de sus tipos exportados. Los campos de struct no se
// exigen; los identificadores no exportados, tampoco.
//
// Mención en x_test.go: cualquier *ast.Ident del fichero con ese nombre —un identificador
// suelto en el mismo paquete, o el Sel de un selector (cosa.Hacer) en el paquete externo
// x_test—. Un comentario NO es una mención, ni un literal de cadena. Las etiquetas de
// compilación se ignoran: un test en rojo (`//go:build pendiente`) cuenta.
//
// Una violación por exportado sin mención: Fichero es la Ruta del x.go y Motivo contiene el
// nombre del símbolo (para un método, "Tipo.Metodo") y el nombre base del test ("x_test.go").
func ExportadosCubiertos(fuentes []Fuente) []Violacion {
	panic(pendiente.Implementar("candados.ExportadosCubiertos"))
}
