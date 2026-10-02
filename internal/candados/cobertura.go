package candados

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
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
	// Un bloque se identifica por ruta + rango: dos paquetes de test sobre el mismo fichero
	// emiten el mismo bloque dos veces, y sumarlo dos veces inflaría Sentencias.
	bloques := make(map[string]map[string]bloque)
	escaner := bufio.NewScanner(perfil)
	escaner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	n, cabecera := 0, false
	for escaner.Scan() {
		n++
		linea := strings.TrimSpace(escaner.Text())
		if linea == "" {
			continue
		}
		if err := leerLinea(linea, n, &cabecera, bloques); err != nil {
			return nil, err
		}
	}
	if err := escaner.Err(); err != nil {
		return nil, fmt.Errorf("candados: perfil de cobertura, línea %d: %w", n+1, err)
	}
	if !cabecera {
		// Vacío (o solo líneas en blanco): la cabecera faltaría en la primera línea.
		return nil, errors.New(`candados: perfil de cobertura, línea 1: perfil vacío, falta la cabecera "mode: "`)
	}
	return sumarBloques(bloques), nil
}

// Cobertura exige a cada fichero en verde del alcance un porcentaje de sentencias cubiertas
// ≥ umbral (80 en D-12): 100·Cubiertas/Sentencias ≥ umbral.
//
// Cruce perfil ↔ fuentes: la entrada k de fs corresponde a la Fuente f si k == f.Ruta o si
// k termina en "/"+f.Ruta (la forma de import lleva delante la ruta del módulo); si varias
// Fuente casan, gana la de Ruta más larga. Las entradas de fs sin Fuente (fuera del alcance)
// se ignoran.
//
// Se evalúan exactamente los ficheros que devuelve Evaluables: los de un paquete …helpertest
// no (D-F1-6: su suite solo la ejecutan los tests de las implementaciones, en otros paquetes,
// y `go test -cover` sin -coverpkg no lo cuenta; D-F1-10: el paquete se reconoce por el
// sufijo compuesto "helpertest", no por "test" a secas; ver Evaluables). Además, y con
// independencia del perfil, de si el fichero está en rojo y de si su paquete es
// …helpertest, todo fichero de producción con MarcaPostgres en la cabecera que NO importa
// "database/sql" ni "github.com/jackc/pgx" (o un subpaquete, p. ej. ".../pgx/v5/pgxpool") es
// una violación: nadie se exime por decreto. Ese fichero marcado de forma ilegítima se evalúa
// además como cualquier otro (salvo que sea de un paquete …helpertest: entonces solo queda la
// violación de marca).
//
// Violaciones: Fichero es la Ruta de la Fuente. Por umbral, Motivo contiene el porcentaje
// con un decimal y "%" (p. ej. "50.0 %") y el umbral; por marca ilegítima, Motivo contiene
// "marca" y "postgres".
func Cobertura(fs map[string]Fichero, fuentes []Fuente, umbral float64) []Violacion {
	vs := make([]Violacion, 0)
	for _, f := range fuentes {
		if !f.EsTest && tieneMarca(f.Archivo) && !importaPostgres(f.Archivo) {
			vs = append(vs, Violacion{
				Fichero: f.Ruta,
				Motivo: "marca de adaptador postgres en la cabecera sin importar database/sql ni " +
					"github.com/jackc/pgx: nadie se exime por decreto (05 E-6)",
			})
		}
	}
	medidas := cruzar(fs, fuentes)
	for _, ruta := range Evaluables(fs, fuentes) {
		m := medidas[ruta]
		pct := 100 * float64(m.Cubiertas) / float64(m.Sentencias)
		if pct >= umbral {
			continue
		}
		// Se trunca (no se redondea) al decimal: un 79.96 % que no llega a 80 no debe
		// imprimirse como "80.0 %" junto a un umbral de 80.
		vs = append(vs, Violacion{
			Fichero: ruta,
			Motivo: fmt.Sprintf("cobertura %.1f %% < umbral %g %% (%d de %d sentencias cubiertas, D-12)",
				math.Floor(pct*10)/10, umbral, m.Cubiertas, m.Sentencias),
		})
	}
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Fichero != vs[j].Fichero {
			return vs[i].Fichero < vs[j].Fichero
		}
		return vs[i].Motivo < vs[j].Motivo
	})
	return vs
}

// Exentos devuelve, ordenadas, las Ruta de los ficheros de producción de fuentes exentos
// LEGÍTIMAMENTE del umbral: llevan MarcaPostgres en la cabecera e importan "database/sql" o
// "github.com/jackc/pgx" (o un subpaquete). No mira el perfil. Los tests nunca están.
// Es lo que cmd/cobertura-ficheros cuenta como EXENTOS_POSTGRES.
func Exentos(fuentes []Fuente) []string {
	out := make([]string, 0)
	for _, f := range fuentes {
		if esExento(f) {
			out = append(out, f.Ruta)
		}
	}
	sort.Strings(out)
	return out
}

// Evaluables devuelve, ordenadas, las Ruta de los ficheros que Cobertura mide contra el
// umbral: ficheros de producción (no EsTest) de fuentes que
//   - no son de un paquete …helpertest: su Paquete (la cláusula `package`, no el directorio)
//     no cumple isHelperTestPackage, es decir, no termina en el sufijo compuesto "helpertest"
//     (D-F1-6, la misma condición que D-F1-3 aplica en UnFicheroUnTest y
//     ExportadosCubiertos). Es una exención por paquete, no por nota: un paquete …helpertest
//     al 100 % tampoco se mide. El porqué: la suite Contrato y los dobles de un puerto solo
//     los ejecutan los tests de las implementaciones, que viven en OTROS paquetes, y
//     `go test -cover` sin -coverpkg no cuenta lo que se ejecuta desde otro paquete; en el
//     perfil del propio paquete …helpertest el fichero saldría al 0 %.
//     D-F1-10 (Jhoan, 2026-10-02) estrecha D-F1-6: el sufijo era "test" a secas, y un paquete
//     de producción cuyo nombre acabara así por casualidad (latest, contest…) quedaba sin
//     medir. Ahora ese paquete SÍ se mide, y también huellatest, que conserva su nombre;
//   - están en verde: no contienen ninguna llamada pendiente.Implementar(…) —detectada en el
//     AST como selector cuyo X es el identificador "pendiente" y cuyo Sel es "Implementar";
//     una mención en un comentario no cuenta—;
//   - no están en Exentos;
//   - tienen entrada en fs (con el cruce de Cobertura) con Sentencias > 0 (un fichero sin
//     sentencias, p. ej. solo tipos, no aparece o no tiene nada que medir).
//
// Es lo que cmd/cobertura-ficheros cuenta como FICHEROS_EVALUADOS.
func Evaluables(fs map[string]Fichero, fuentes []Fuente) []string {
	medidas := cruzar(fs, fuentes)
	out := make([]string, 0)
	for _, f := range fuentes {
		// D-F1-6: un paquete …helpertest entero (suite Contrato y dobles) queda fuera de la
		// medida, igual que D-F1-3 lo deja fuera de UnFicheroUnTest y ExportadosCubiertos; el
		// sufijo es el de D-F1-10 (uno que acaba en "test" a secas, como latest, sí se mide).
		if f.EsTest || isHelperTestPackage(f.Paquete) {
			continue
		}
		if enRojo(f.Archivo) || esExento(f) {
			continue
		}
		if medidas[f.Ruta].Sentencias > 0 {
			out = append(out, f.Ruta)
		}
	}
	sort.Strings(out)
	return out
}

// bloque es un bloque del perfil ya deduplicado: sus sentencias y la cuenta máxima vista.
type bloque struct {
	sentencias int
	cuenta     int
}

// leerLinea procesa la línea no vacía número n del perfil: la cabecera si aún no se vio, o
// un bloque que se anota en bloques (con la cuenta máxima si ya estaba).
func leerLinea(linea string, n int, cabecera *bool, bloques map[string]map[string]bloque) error {
	if !*cabecera {
		if !strings.HasPrefix(linea, "mode: ") {
			return fmt.Errorf(`candados: perfil de cobertura, línea %d: falta la cabecera "mode: "`, n)
		}
		*cabecera = true
		return nil
	}
	if strings.HasPrefix(linea, "mode:") {
		return fmt.Errorf("candados: perfil de cobertura, línea %d: segunda cabecera mode", n)
	}
	ruta, rango, b, err := partirBloque(linea)
	if err != nil {
		return fmt.Errorf("candados: perfil de cobertura, línea %d: %w: %q", n, err, linea)
	}
	porRango, ok := bloques[ruta]
	if !ok {
		porRango = make(map[string]bloque)
		bloques[ruta] = porRango
	}
	if previo, visto := porRango[rango]; visto {
		b.cuenta = max(b.cuenta, previo.cuenta)
		b.sentencias = previo.sentencias
	}
	porRango[rango] = b
	return nil
}

// partirBloque separa "fichero:l1.c1,l2.c2 sentencias cuenta" en sus piezas y las valida.
func partirBloque(linea string) (ruta, rango string, b bloque, err error) {
	campos := strings.Fields(linea)
	if len(campos) != 3 {
		return "", "", b, errors.New("se esperaban tres campos (fichero:rango sentencias cuenta)")
	}
	dos := strings.LastIndex(campos[0], ":")
	if dos <= 0 {
		return "", "", b, errors.New(`falta "fichero:" antes del rango`)
	}
	ruta, rango = campos[0][:dos], campos[0][dos+1:]
	if !rangoValido(rango) {
		return "", "", b, errors.New("el rango no es l1.c1,l2.c2")
	}
	if b.sentencias, err = natural(campos[1]); err != nil {
		return "", "", b, fmt.Errorf("sentencias: %w", err)
	}
	if b.cuenta, err = natural(campos[2]); err != nil {
		return "", "", b, fmt.Errorf("cuenta: %w", err)
	}
	return ruta, rango, b, nil
}

// rangoValido dice si r tiene la forma l1.c1,l2.c2 con cuatro enteros no negativos.
func rangoValido(r string) bool {
	extremos := strings.Split(r, ",")
	if len(extremos) != 2 {
		return false
	}
	for _, e := range extremos {
		lc := strings.Split(e, ".")
		if len(lc) != 2 {
			return false
		}
		for _, x := range lc {
			if _, err := natural(x); err != nil {
				return false
			}
		}
	}
	return true
}

// natural convierte s en un entero ≥ 0; un negativo o algo que no es entero es error.
func natural(s string) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q no es entero", s)
	}
	if v < 0 {
		return 0, fmt.Errorf("%d es negativo", v)
	}
	return v, nil
}

// sumarBloques agrega los bloques deduplicados por fichero.
func sumarBloques(bloques map[string]map[string]bloque) map[string]Fichero {
	out := make(map[string]Fichero, len(bloques))
	for ruta, porRango := range bloques {
		f := Fichero{Ruta: ruta}
		for _, b := range porRango {
			f.Sentencias += b.sentencias
			if b.cuenta > 0 {
				f.Cubiertas += b.sentencias
			}
		}
		out[ruta] = f
	}
	return out
}

// cruzar lleva el perfil (claves en forma de import) a las Ruta de los ficheros de
// producción de fuentes. Para cada clave k se prueba k entera y luego cada sufijo que sigue
// a una «/», de izquierda a derecha: el primer acierto es la Ruta más larga que casa, que es
// la que gana. Las claves sin Fuente (fuera del alcance) se ignoran. Si dos claves distintas
// cayeran en la misma Fuente se suman, para no perder ninguna sentencia en silencio.
func cruzar(fs map[string]Fichero, fuentes []Fuente) map[string]Fichero {
	produccion := make(map[string]bool, len(fuentes))
	for _, f := range fuentes {
		if !f.EsTest {
			produccion[f.Ruta] = true
		}
	}
	out := make(map[string]Fichero)
	for k, fi := range fs {
		ruta, ok := casar(k, produccion)
		if !ok {
			continue
		}
		m := out[ruta]
		m.Ruta = ruta
		m.Sentencias += fi.Sentencias
		m.Cubiertas += fi.Cubiertas
		out[ruta] = m
	}
	return out
}

// casar devuelve la Ruta más larga de rutas que es k o un sufijo de k tras una «/».
func casar(k string, rutas map[string]bool) (string, bool) {
	if rutas[k] {
		return k, true
	}
	for i := 0; i < len(k); i++ {
		if k[i] == '/' && rutas[k[i+1:]] {
			return k[i+1:], true
		}
	}
	return "", false
}

// esExento dice si f es un fichero de producción exento legítimamente: marca en la
// cabecera y un import de Postgres que la respalda.
func esExento(f Fuente) bool {
	return !f.EsTest && tieneMarca(f.Archivo) && importaPostgres(f.Archivo)
}

// tieneMarca dice si MarcaPostgres aparece, como comentario de línea exacto, ANTES de la
// cláusula `package`. Una marca más abajo (p. ej. junto a los imports) no cuenta: la
// exención se declara en la cabecera, donde se ve al abrir el fichero.
func tieneMarca(a *ast.File) bool {
	for _, grupo := range a.Comments {
		if grupo.Pos() >= a.Package {
			break // los grupos vienen en orden de posición
		}
		for _, c := range grupo.List {
			if c.Text == MarcaPostgres {
				return true
			}
		}
	}
	return false
}

// importaPostgres dice si a importa "database/sql" o "github.com/jackc/pgx" (o un
// subpaquete suyo): lo que hace creíble que el fichero sea un adaptador Postgres.
func importaPostgres(a *ast.File) bool {
	for _, imp := range a.Imports {
		// El parser ya validó el literal: basta quitarle las comillas (o el acento grave).
		ruta := strings.Trim(imp.Path.Value, "`\"")
		if ruta == "database/sql" || ruta == "github.com/jackc/pgx" ||
			strings.HasPrefix(ruta, "github.com/jackc/pgx/") {
			return true
		}
	}
	return false
}

// enRojo dice si a contiene un selector pendiente.Implementar: el contrato sigue sin lógica
// y medir su cobertura no significa nada. Se mira el AST, así que citarlo en un comentario
// no cuenta.
func enRojo(a *ast.File) bool {
	rojo := false
	ast.Inspect(a, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Implementar" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "pendiente" {
				rojo = true
			}
		}
		return !rojo
	})
	return rojo
}
