package candados

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

// UnFicheroUnTest exige que cada x.go de producción de fuentes tenga su x_test.go en el
// mismo directorio (05 E-3). fuentes debe traer los tests (Recorrer con incluirTests =
// true): el test se busca SOLO entre fuentes, por Ruta ("dir/x.go" → "dir/x_test.go").
//
// Quedan fuera, sin mirar nada más:
//   - los ficheros de test (EsTest);
//   - todo fichero de un paquete cuyo nombre termina en "test" (D-F1-3 = sí: suites
//     Contrato y dobles; los dobles con lógica llevan igualmente su test, pero eso lo exige
//     la fase, no este candado).
//
// Excepciones de E-3, VERIFICADAS (se comprueba la condición, no se lista el fichero):
//   - doc.go: se exime si no tiene ninguna declaración (ni import): solo `package` y su
//     comentario. Un doc.go con una declaración necesita test como cualquiera;
//   - fichero de //go:embed: se exime si tiene al menos una declaración y todas las que no
//     son import son `var` cuyas especificaciones llevan una directiva //go:embed en su
//     comentario; una sola declaración de otra clase le quita la exención;
//   - puerto (solo interfaces): se exime si tiene al menos una declaración, todas las que no
//     son import son `type X interface{…}` (ni funciones, ni métodos, ni var, ni const, ni
//     tipos de otra clase) Y entre fuentes hay un fichero del paquete <paquete>test en el
//     subdirectorio <dir>/<paquete>test (patrón internal/gateway/fleet/fleettest) que declara
//     una función de primer nivel `Contrato` cuyo primer parámetro es *testing.T. Sin esa
//     suite entre fuentes, el puerto necesita test.
//
// Una violación por fichero sin test y sin excepción: Fichero es la Ruta del x.go y Motivo
// contiene "falta x_test.go" (el nombre base del test que falta).
func UnFicheroUnTest(fuentes []Fuente) []Violacion {
	panic(pendiente.Implementar("candados.UnFicheroUnTest"))
}
