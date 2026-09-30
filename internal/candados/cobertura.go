package candados

import (
	"io"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// MarcaPostgres es la línea que exime a un adaptador Postgres del umbral de cobertura
// (05 E-6). Va en la CABECERA del propio fichero —un comentario antes de la cláusula
// `package`—, como línea exacta.
const MarcaPostgres = "// cobertura: adaptador postgres (05 E-6)"

// Fichero es la cobertura agregada de un fichero del perfil: Ruta es la ruta tal como la
// escribe `go test -coverprofile` (forma de import, p. ej.
// "github.com/EduGoGroup/wapp-cloud-platform/internal/x/y.go"); Sentencias, el total de
// sentencias de sus bloques; Cubiertas, las de los bloques con cuenta > 0.
type Fichero struct {
	Ruta       string
	Sentencias int
	Cubiertas  int
}

// Agregar lee un perfil de `go test -coverprofile` y lo agrega por fichero; la clave del
// mapa es la ruta del perfil, igual a Fichero.Ruta.
//
// Formato: la primera línea no vacía es la cabecera "mode: <set|count|atomic>"; cada línea
// siguiente es "fichero:l1.c1,l2.c2 sentencias cuenta" (enteros). Las líneas vacías se
// ignoran. Un bloque repetido (misma ruta y mismo rango l1.c1,l2.c2: dos paquetes de test
// sobre el mismo fichero) se cuenta UNA vez, con la cuenta máxima: sus sentencias suman una
// sola vez a Sentencias, y a Cubiertas si esa cuenta máxima es > 0.
//
// Errores: un perfil sin cabecera (vacío, o cuya primera línea no empieza por "mode: "), una
// segunda línea "mode:", o una línea mal formada (sin los tres campos, sin ":" antes del
// rango, un rango que no es l1.c1,l2.c2, o sentencias/cuenta no enteros o negativos). El
// mensaje contiene "línea N", con N el número de línea (desde 1) culpable; para un perfil
// vacío, "línea 1". Con error no hay resultado parcial.
func Agregar(perfil io.Reader) (map[string]Fichero, error) {
	panic(pendiente.Implementar("candados.Agregar"))
}

// Cobertura exige a cada fichero en verde del alcance un porcentaje de sentencias cubiertas
// ≥ umbral (80 en D-12): 100·Cubiertas/Sentencias ≥ umbral.
//
// Cruce perfil ↔ fuentes: la entrada k de fs corresponde a la Fuente f si k == f.Ruta o si
// k termina en "/"+f.Ruta (la forma de import lleva delante la ruta del módulo); si varias
// Fuente casan, gana la de Ruta más larga. Las entradas de fs sin Fuente (fuera del alcance)
// se ignoran.
//
// Se evalúan exactamente los ficheros que devuelve Evaluables. Además, y con independencia
// del perfil y de si el fichero está en rojo, todo fichero de producción con MarcaPostgres
// en la cabecera que NO importa "database/sql" ni "github.com/jackc/pgx" (o un subpaquete,
// p. ej. ".../pgx/v5/pgxpool") es una violación: nadie se exime por decreto. Ese fichero
// marcado de forma ilegítima se evalúa además como cualquier otro.
//
// Violaciones: Fichero es la Ruta de la Fuente. Por umbral, Motivo contiene el porcentaje
// con un decimal y "%" (p. ej. "50.0 %") y el umbral; por marca ilegítima, Motivo contiene
// "marca" y "postgres".
func Cobertura(fs map[string]Fichero, fuentes []Fuente, umbral float64) []Violacion {
	panic(pendiente.Implementar("candados.Cobertura"))
}

// Exentos devuelve, ordenadas, las Ruta de los ficheros de producción de fuentes exentos
// LEGÍTIMAMENTE del umbral: llevan MarcaPostgres en la cabecera e importan "database/sql" o
// "github.com/jackc/pgx" (o un subpaquete). No mira el perfil. Los tests nunca están.
// Es lo que cmd/cobertura-ficheros cuenta como EXENTOS_POSTGRES.
func Exentos(fuentes []Fuente) []string {
	panic(pendiente.Implementar("candados.Exentos"))
}

// Evaluables devuelve, ordenadas, las Ruta de los ficheros que Cobertura mide contra el
// umbral: ficheros de producción (no EsTest) de fuentes que
//   - están en verde: no contienen ninguna llamada pendiente.Implementar(…) —detectada en el
//     AST como selector cuyo X es el identificador "pendiente" y cuyo Sel es "Implementar";
//     una mención en un comentario no cuenta—;
//   - no están en Exentos;
//   - tienen entrada en fs (con el cruce de Cobertura) con Sentencias > 0 (un fichero sin
//     sentencias, p. ej. solo tipos, no aparece o no tiene nada que medir).
//
// Es lo que cmd/cobertura-ficheros cuenta como FICHEROS_EVALUADOS.
func Evaluables(fs map[string]Fichero, fuentes []Fuente) []string {
	panic(pendiente.Implementar("candados.Evaluables"))
}
