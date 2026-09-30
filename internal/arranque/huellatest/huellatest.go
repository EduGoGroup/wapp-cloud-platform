// Package huellatest calcula y compara la «huella» de un arranque de la Plataforma Cloud: lo
// que el proceso EXPONE (rutas HTTP montadas por listener, rpc gRPC, familias de métricas
// `wapp_*` en frío) y lo que su código fuente LANZA y CABLEA (sentencias `go`, ganchos de
// métricas, lecturas del entorno). Nace en F0 · T0.12 (plan/F0-andamiaje/diseno.md §6.2):
// no porta ningún fichero viejo, es nuevo.
//
// Lo usan dos tests, y solo ellos (regla 5 de internal/candados/fronteras.go): el del
// arranque VIEJO, internal/bootstrap/arranque/huella_vieja_test.go, que escribe la dorada
// internal/arranque/testdata/huella.json; y el del arranque NUEVO,
// internal/arranque/huella_test.go, que la compara. Si las dos huellas no son iguales, el
// arranque nuevo ha dejado de ser una copia fiel del viejo, y Diferencia dice en qué.
//
// Todo lo que devuelve es determinista: listas ordenadas (sort.Strings), sin fechas ni SHA,
// para que un diff de la dorada sea exactamente el cambio.
package huellatest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/grpc"
)

// Huella es la superficie de UN arranque en UN perfil de configuración (diseno.md §6.1).
//
//   - Perfil: nombre del perfil ("minimo", "m2m"…); la dorada lleva una Huella por perfil.
//   - Rutas: listener (":8100", ":8103") → patrones candidatos montados, como los devuelve
//     Rutas.
//   - RPC: listener (":8101", ":8102") → "servicio/método", como los devuelve RPC.
//   - Metricas: familias `wapp_*` visibles en /metrics, como las devuelve Metricas.
//   - Goroutines, Hooks, Entorno: la parte ESTÁTICA (Goroutines, Hooks, Entorno), que se
//     calcula sobre el código fuente y se compara entre los dos árboles, no contra la
//     dorada. Llevan `omitempty`: la dorada solo guarda la parte de ejecución.
//
// Las etiquetas JSON son los nombres en minúscula: perfil, rutas, rpc, metricas,
// goroutines, hooks, entorno.
type Huella struct {
	Perfil     string              `json:"perfil"`
	Rutas      map[string][]string `json:"rutas"`
	RPC        map[string][]string `json:"rpc"`
	Metricas   []string            `json:"metricas"`
	Goroutines []string            `json:"goroutines,omitempty"`
	Hooks      []string            `json:"hooks,omitempty"`
	Entorno    []string            `json:"entorno,omitempty"`
}

// Candidatos devuelve los patrones de ruta candidatos: el primer argumento de toda llamada
// `X.Handle(…)` o `X.HandleFunc(…)` (cualquier X: un mux, el paquete http…) cuando es un
// literal de cadena (interpretado o crudo), con su valor ya sin comillas —p. ej.
// "GET /admin/tenants", "/healthz", "POST /api/v1/x/{id}"—.
//
// Promesas:
//   - recorre, recursivamente, raiz/dir para cada dir de dirs (dir relativo a raiz, con
//     barras); solo mira los ficheros .go que NO terminan en "_test.go";
//   - salta todo directorio llamado "testdata" que cuelgue por debajo de raiz/dir (la propia
//     raiz puede estar dentro de un testdata: así se prueba);
//   - un dir que no existe aporta cero candidatos y NO es un error (el árbol nuevo no tiene
//     aún internal/modulos ni internal/apipublica);
//   - un primer argumento que no es un literal (una variable, una concatenación) se ignora;
//   - un fichero que no parsea es un error que nombra su ruta, sin resultado parcial;
//   - el resultado está ordenado y sin duplicados (el mismo literal en dos ficheros sale una
//     vez); sin candidatos, es una lista de longitud 0 y error nil.
func Candidatos(raiz string, dirs ...string) ([]string, error) {
	vistos := make(map[string]bool)
	err := recorrerGo(raiz, dirs, "", func(_ string, _ *token.FileSet, f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := patronDeHandle(n); ok {
				vistos[lit] = true
			}
			return true
		})
	})
	if err != nil {
		return nil, err
	}
	return ordenados(vistos), nil
}

// Rutas sondea h con cada candidato y devuelve los que están MONTADOS.
//
// La sonda de un candidato es UNA petición httptest sobre h, sin cuerpo:
//   - método: el del patrón ("POST /x" → POST); sin método ("/x"), GET;
//   - ruta: la del patrón con cada comodín `{x}` o `{x...}` sustituido por su nombre `x`
//     ("POST /api/v1/x/{id}" → POST /api/v1/x/id; "/f/{resto...}" → /f/resto);
//   - RemoteAddr fijo "192.0.2.1:1234" (TEST-NET-1): un limitador por IP ve siempre la misma.
//
// Los candidatos se sondean EN EL ORDEN DADO, cada uno una vez: el orden importa, porque una
// familia CounterVec aparece en /metrics con su primer incremento, y Metricas se llama
// después de las sondas.
//
// Un candidato está montado SALVO que la respuesta sea
//   - el 404 del mux: estado 404 Y cuerpo exactamente "404 page not found\n" (un handler que
//     llama a http.NotFound es, para la sonda, indistinguible: limitación aceptada), o
//   - un 405 con cabecera Allow (el mux tiene el camino, pero no para ese método).
//
// Cualquier otra respuesta —un 404 con otro cuerpo, un 405 sin Allow, un 401, un 503— cuenta
// como montada: la sonda ve la superficie, no el comportamiento. Un panic del handler también
// cuenta como montada: Rutas lo recupera y sigue con el siguiente candidato.
//
// El resultado son los candidatos montados, tal como vinieron (el texto del candidato, no la
// petición), ordenados y sin duplicados; ninguno montado es una lista de longitud 0.
func Rutas(h http.Handler, candidatos []string) []string {
	montadas := make(map[string]bool)
	sondeados := make(map[string]bool)
	for _, c := range candidatos {
		// Un candidato repetido se sondea una sola vez: la segunda sonda incrementaría otra
		// vez los contadores y el resultado ya lo tiene.
		if sondeados[c] {
			continue
		}
		sondeados[c] = true
		if sondear(h, peticion(c)) {
			montadas[c] = true
		}
	}
	return ordenados(montadas)
}

// RPC devuelve los rpc registrados en gs según gs.GetServiceInfo(): un "servicio/método" por
// cada método unario Y cada stream de cada servicio (p. ej. "wapp.cloudlink.v1.Link/Connect"),
// ordenados. Un servidor sin servicios da una lista de longitud 0.
func RPC(gs *grpc.Server) []string {
	var rpcs []string
	for servicio, info := range gs.GetServiceInfo() {
		// info.Methods trae los unarios Y los streams (MethodInfo.IsClientStream…).
		for _, m := range info.Methods {
			rpcs = append(rpcs, servicio+"/"+m.Name)
		}
	}
	if rpcs == nil {
		rpcs = []string{}
	}
	sort.Strings(rpcs)
	return rpcs
}

// Metricas hace UNA petición GET /metrics a través de admin (el handler del listener :8100
// entero, no el de /metrics suelto) y devuelve el nombre de cada familia declarada en una
// línea `# TYPE <nombre> <tipo>` cuyo nombre empieza por "wapp_". Las líneas `# HELP`, las
// muestras y las familias sin ese prefijo (go_*, process_*, promhttp_*) no cuentan.
//
// El resultado está ordenado y sin duplicados. Un estado distinto de 200 es un error que
// incluye el estado recibido (un admin sin /metrics no es «cero familias»).
func Metricas(admin http.Handler) ([]string, error) {
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("huellatest: GET /metrics respondió %d, no 200", rec.Code)
	}
	familias := make(map[string]bool)
	for _, linea := range strings.Split(rec.Body.String(), "\n") {
		campos := strings.Fields(linea)
		if len(campos) >= 3 && campos[0] == "#" && campos[1] == "TYPE" && strings.HasPrefix(campos[2], "wapp_") {
			familias[campos[2]] = true
		}
	}
	return ordenados(familias), nil
}

// Goroutines devuelve, normalizada, cada sentencia `go` de los ficheros de producción (los
// que no terminan en "_test.go") del paquete Go del directorio dir. Es un MULTICONJUNTO: una
// sentencia repetida sale repetida, y la lista va ordenada.
//
// Normalización, con el tipo ESTÁTICO que da go/types (el paquete se comprueba entero, con
// sus imports; el mecanismo del importador no es parte del contrato):
//   - llamada a método → "<paquete>.<Tipo>.<Método>" del tipo del receptor, sin puntero:
//     `go c.intakePipeline.Run(ctx)` con un *pipeline.Worker → "pipeline.Worker.Run";
//     <paquete> es el NOMBRE del paquete que declara el tipo, no su ruta (net/http → "http");
//   - llamada a función con nombre → "<paquete>.<función>", con <paquete> el nombre del
//     paquete que la declara: `go serveHTTP(…)` en arranque → "arranque.serveHTTP";
//     `go runtime.GC()` → "runtime.GC";
//   - literal de función, `go func(){…}()` → "<paquete>.<Contenedora>.func", con <paquete> el
//     del propio dir y <Contenedora> la declaración de primer nivel que lo contiene: una
//     función ("arranque.servir.func") o un método, escrito "<Tipo>.<Método>"
//     ("fondo.Worker.Lanzar.func"). Un literal dentro de otro literal cuenta como el de fuera.
//
// Errores: un dir sin ficheros de producción, que no existe, que no parsea o que no
// comprueba tipos es un error (sin resultado parcial): la parte estática no se da por vacía
// en silencio.
func Goroutines(dir string) ([]string, error) {
	fset := token.NewFileSet()
	ficheros, err := paqueteProduccion(fset, dir)
	if err != nil {
		return nil, err
	}
	info := &types.Info{
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	imp, err := importadorDe(fset, dir)
	if err != nil {
		return nil, err
	}
	conf := types.Config{Importer: imp}
	pkg, err := conf.Check(ficheros[0].Name.Name, fset, ficheros, info)
	if err != nil {
		return nil, fmt.Errorf("huellatest: comprobar tipos de %s: %w", dir, err)
	}
	gs := []string{}
	for _, f := range ficheros {
		for _, d := range f.Decls {
			contenedora := nombreContenedora(d)
			ast.Inspect(d, func(n ast.Node) bool {
				if g, ok := n.(*ast.GoStmt); ok {
					gs = append(gs, normalizarGo(pkg.Name(), contenedora, g.Call, info))
				}
				return true
			})
		}
	}
	sort.Strings(gs)
	return gs, nil
}

// Hooks devuelve el MULTICONJUNTO (ordenado, con repeticiones) de los nombres M de toda
// expresión selectora `X.M` de los ficheros de producción (no "_test.go") del paquete de
// dir cuyo M está en metodos: la llamada `m.InstrumentHTTP(h)` y el valor de método
// `f := m.RegisterDBStats` cuentan los dos; la declaración de un método no es un selector y
// no cuenta. Es sintáctico: no comprueba el tipo de X (metodos sale, por reflect, del
// conjunto de métodos de *metrics.Metrics, y sus nombres son lo bastante distintivos).
//
// Un nombre de metodos que no aparece no aporta nada; ninguno aparece, lista de longitud 0.
// Errores: un dir que no existe, sin ficheros de producción o que no parsea.
func Hooks(dir string, metodos []string) ([]string, error) {
	quiere := make(map[string]bool, len(metodos))
	for _, m := range metodos {
		quiere[m] = true
	}
	ficheros, err := paqueteProduccion(token.NewFileSet(), dir)
	if err != nil {
		return nil, err
	}
	hs := []string{}
	for _, f := range ficheros {
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && quiere[sel.Sel.Name] {
				hs = append(hs, sel.Sel.Name)
			}
			return true
		})
	}
	sort.Strings(hs)
	return hs, nil
}

// Entorno devuelve las lecturas directas del entorno bajo raiz/dir para cada dir de dirs:
// cada llamada a os.Getenv, os.LookupEnv u os.Environ de un fichero de producción (no
// "_test.go"), escrita "<ruta relativa a raiz, con barras>:<línea> os.<Función>"
// (p. ej. "internal/arranque/http.go:42 os.Getenv"). El paquete se reconoce por su import
// "os", con o sin alias: con `import sistema "os"`, `sistema.Getenv(…)` sale como
// "os.Getenv". Otras funciones de os (Setenv, Exit…) no cuentan.
//
// Promesas:
//   - salta los directorios "testdata" que cuelguen por debajo de raiz/dir y todo lo que
//     esté en raiz/internal/platform/config o por debajo (el único sitio legítimo, 05);
//   - un dir que no existe aporta cero lecturas y NO es un error;
//   - un fichero que no parsea es un error que nombra su ruta;
//   - el resultado está ordenado y sin duplicados; sin lecturas, es una lista de longitud 0
//     (no nil) y error nil: en el árbol nuevo la huella exige exactamente eso.
func Entorno(raiz string, dirs ...string) ([]string, error) {
	lecturas := make(map[string]bool)
	err := recorrerGo(raiz, dirs, dirConfig, func(rel string, fset *token.FileSet, f *ast.File) {
		for _, l := range lecturasEntorno(fset, f) {
			lecturas[rel+":"+l] = true
		}
	})
	if err != nil {
		return nil, err
	}
	return ordenados(lecturas), nil
}

// Leer lee el fichero JSON ruta (el formato de Escribir: un array de Huella) y devuelve sus
// huellas en el orden del fichero. Un fichero que no existe o que no es JSON válido de ese
// formato es un error que nombra ruta.
func Leer(ruta string) ([]Huella, error) {
	b, err := os.ReadFile(ruta) //nolint:gosec // G304: la ruta la da el test (la dorada), no un usuario
	if err != nil {
		return nil, fmt.Errorf("huellatest: leer %s: %w", ruta, err)
	}
	var hs []Huella
	if err := json.Unmarshal(b, &hs); err != nil {
		return nil, fmt.Errorf("huellatest: %s no es un array de Huella: %w", ruta, err)
	}
	return hs, nil
}

// Escribir escribe hs en ruta (lo crea o lo trunca) como un array JSON indentado con dos
// espacios, las huellas en el orden dado y las listas tal como vienen (no reordena: la
// dorada refleja lo calculado), terminado en un salto de línea. Las claves de los mapas van
// ordenadas (encoding/json). Determinista: escribir dos veces lo mismo da los mismos bytes,
// y Leer(ruta) devuelve hs (con las listas `omitempty` vacías leídas como nil). Un
// directorio padre que no existe es un error; Escribir no crea directorios.
func Escribir(ruta string, hs []Huella) error {
	// Un Encoder y no MarshalIndent: sin escapar <, > y & la dorada se lee como se escribe,
	// y Encode ya termina en salto de línea.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(hs); err != nil {
		return fmt.Errorf("huellatest: codificar huellas: %w", err)
	}
	if err := os.WriteFile(ruta, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("huellatest: escribir %s: %w", ruta, err)
	}
	return nil
}

// Diferencia compara la huella esperada (quiere: la dorada, o el arranque viejo) con la
// obtenida (tiene) y devuelve una línea legible por diferencia, ordenadas (sort.Strings);
// longitud 0 si son iguales.
//
// Todas las listas se comparan como MULTICONJUNTOS, por número de apariciones: una
// aparición de más en tiene es una línea "sobra", una de menos es una línea "falta" (dos
// de más, dos líneas). nil y la lista vacía son iguales, y un listener ausente de un mapa
// equivale a uno presente con la lista vacía. Formatos exactos:
//
//	perfil:      "perfil: quiere <quiere.Perfil>, tiene <tiene.Perfil>"   (si difieren)
//	rutas:       "<listener> falta <patrón>"      ":8100 falta GET /admin/tenants"
//	             "<listener> sobra <patrón>"      ":8103 sobra /x"
//	rpc:         "rpc <listener> falta <servicio/método>"   "rpc :8101 sobra svc/M"
//	metricas:    "metricas falta <familia>"       "metricas sobra wapp_x"
//	goroutines:  "goroutines sobra <entrada>"     "goroutines sobra pipeline.Worker.Run"
//	hooks:       "hooks falta <método>"           "hooks falta InstrumentHTTP"
//	entorno:     "entorno sobra <lectura>"        "entorno sobra a/b.go:3 os.Getenv"
//
// Es pura: no modifica quiere ni tiene.
func Diferencia(quiere, tiene Huella) []string {
	lineas := []string{}
	if quiere.Perfil != tiene.Perfil {
		lineas = append(lineas, fmt.Sprintf("perfil: quiere %s, tiene %s", quiere.Perfil, tiene.Perfil))
	}
	lineas = diferenciaPorListener(lineas, "", quiere.Rutas, tiene.Rutas)
	lineas = diferenciaPorListener(lineas, "rpc ", quiere.RPC, tiene.RPC)
	lineas = diferenciaMulticonjunto(lineas, "metricas ", quiere.Metricas, tiene.Metricas)
	lineas = diferenciaMulticonjunto(lineas, "goroutines ", quiere.Goroutines, tiene.Goroutines)
	lineas = diferenciaMulticonjunto(lineas, "hooks ", quiere.Hooks, tiene.Hooks)
	lineas = diferenciaMulticonjunto(lineas, "entorno ", quiere.Entorno, tiene.Entorno)
	sort.Strings(lineas)
	return lineas
}

// ── Piezas internas ──────────────────────────────────────────────────────────

const (
	// remoteAddr de toda sonda (TEST-NET-1, RFC 5737): un limitador por IP ve siempre la
	// misma y nunca una real.
	remoteAddr = "192.0.2.1:1234"
	// cuerpo404 es lo que escribe http.NotFound, y por tanto el 404 del ServeMux.
	cuerpo404 = "404 page not found\n"
	// dirConfig es el único sitio que puede leer el entorno (relativo a la raíz).
	dirConfig = "internal/platform/config"
)

// comodin casa un segmento `{x}`, `{x...}` o `{$}` de un patrón de ServeMux.
var comodin = regexp.MustCompile(`\{[^{}]*\}`)

// lecturaEntorno son las funciones de os que leen el entorno.
var lecturaEntorno = map[string]bool{"Getenv": true, "LookupEnv": true, "Environ": true}

// recorrerGo parsea cada .go de producción bajo raiz/dir, para cada dir, y llama a fn con su
// ruta relativa a raiz (con barras). Salta los testdata que cuelgan por debajo del inicio y,
// si excluir no es "", el subárbol raiz/excluir. Un dir inexistente no es un error; un
// fichero que no parsea sí, y corta el recorrido.
func recorrerGo(raiz string, dirs []string, excluir string, fn func(rel string, fset *token.FileSet, f *ast.File)) error {
	fset := token.NewFileSet()
	for _, dir := range dirs {
		inicio := filepath.Join(raiz, filepath.FromSlash(dir))
		if _, err := os.Stat(inicio); errors.Is(err, fs.ErrNotExist) {
			// El árbol nuevo aún no tiene internal/modulos ni internal/apipublica.
			continue
		} else if err != nil {
			return fmt.Errorf("huellatest: %w", err)
		}
		err := filepath.WalkDir(inicio, func(ruta string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(raiz, ruta)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if saltarDir(ruta != inicio, d.Name(), rel, excluir) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(ruta, ".go") || strings.HasSuffix(ruta, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, ruta, nil, parser.SkipObjectResolution)
			if err != nil {
				// Un fichero ilegible no se salta en silencio: sería un agujero de la huella.
				return fmt.Errorf("huellatest: %s no parsea: %w", ruta, err)
			}
			fn(rel, fset, f)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// saltarDir: testdata por debajo del inicio (como la toolchain de Go; el propio inicio puede
// estar dentro de uno) o el subárbol excluido.
func saltarDir(bajoInicio bool, nombre, rel, excluir string) bool {
	if bajoInicio && nombre == "testdata" {
		return true
	}
	return excluir != "" && (rel == excluir || strings.HasPrefix(rel, excluir+"/"))
}

// patronDeHandle devuelve el literal del primer argumento de una llamada X.Handle(…) o
// X.HandleFunc(…).
func patronDeHandle(n ast.Node) (string, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// peticion construye la sonda de un candidato "[MÉTODO ][HOST]/RUTA": método por omisión
// GET, comodines por su nombre ({$} por nada), RemoteAddr fijo. nil si el candidato no da
// una URL válida (no se puede sondear: no está montado).
func peticion(candidato string) *http.Request {
	metodo, resto := http.MethodGet, candidato
	if i := strings.IndexAny(candidato, " \t"); i >= 0 {
		metodo, resto = candidato[:i], strings.TrimLeft(candidato[i+1:], " \t")
	}
	host := ""
	if i := strings.Index(resto, "/"); i > 0 {
		host, resto = resto[:i], resto[i:]
	}
	ruta := comodin.ReplaceAllStringFunc(resto, func(m string) string {
		nombre := strings.TrimSuffix(m[1:len(m)-1], "...")
		if nombre == "$" {
			return ""
		}
		return nombre
	})
	if !strings.HasPrefix(ruta, "/") {
		ruta = "/" + ruta
	}
	// http.NewRequest y no httptest.NewRequest: este entra en pánico con una URL inválida,
	// y un candidato raro no debe tumbar la huella.
	req, err := http.NewRequest(metodo, "http://example.com"+ruta, http.NoBody)
	if err != nil {
		return nil
	}
	if host != "" {
		req.Host = host
	}
	req.RequestURI = ruta
	req.RemoteAddr = remoteAddr
	return req
}

// sondear sirve req por h y dice si la ruta está montada: todo menos el 404 del mux y un
// 405 con Allow. Un panic del handler cuenta como montada (el camino existe).
func sondear(h http.Handler, req *http.Request) (montada bool) {
	if req == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			montada = true
		}
	}()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound && rec.Body.String() == cuerpo404 {
		return false
	}
	return rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") == ""
}

// ordenados devuelve las claves de m ordenadas; nunca nil.
func ordenados(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// paqueteProduccion parsea los ficheros de producción del paquete de dir, respetando las
// etiquetas de compilación por omisión (go/build): los _test.go y los `//go:build
// pendiente` quedan fuera. Sin ninguno, es un error.
func paqueteProduccion(fset *token.FileSet, dir string) ([]*ast.File, error) {
	bp, err := build.ImportDir(dir, 0)
	var nogo *build.NoGoError
	if err != nil && !errors.As(err, &nogo) {
		return nil, fmt.Errorf("huellatest: %s: %w", dir, err)
	}
	if bp == nil || len(bp.GoFiles)+len(bp.CgoFiles) == 0 {
		return nil, fmt.Errorf("huellatest: %s no tiene ficheros de producción", dir)
	}
	nombres := append(append([]string{}, bp.GoFiles...), bp.CgoFiles...)
	ficheros := make([]*ast.File, 0, len(nombres))
	for _, n := range nombres {
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("huellatest: %s no parsea: %w", filepath.Join(dir, n), err)
		}
		ficheros = append(ficheros, f)
	}
	return ficheros, nil
}

// importadorDe es el importador con el que Goroutines comprueba tipos: el de datos de
// exportación del compilador ("gc"), alimentado por `go list -export -deps` corrido en dir.
//
// Por qué no el importador "source" que proponía diseno.md §6.1: comprueba desde fuente
// todo el grafo de dependencias (grpc, aws sdk, prometheus…) en cada llamada, y sobre
// internal/bootstrap/arranque tardó 105 s (medido el 2026-09-30); con los datos de
// exportación, que la caché de compilación ya tiene, son segundos. El resultado es el mismo:
// los tipos los da el compilador en los dos casos.
func importadorDe(fset *token.FileSet, dir string) (types.Importer, error) {
	var salida, errores bytes.Buffer
	cmd := exec.Command("go", "list", "-export", "-deps", "-f", "{{.ImportPath}}={{.Export}}", ".")
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &salida, &errores
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("huellatest: go list -export en %s: %w: %s", dir, err, strings.TrimSpace(errores.String()))
	}
	exportados := make(map[string]string)
	for _, linea := range strings.Split(salida.String(), "\n") {
		if ruta, fichero, ok := strings.Cut(linea, "="); ok && fichero != "" {
			exportados[ruta] = fichero
		}
	}
	buscar := func(ruta string) (io.ReadCloser, error) {
		fichero, ok := exportados[ruta]
		if !ok {
			return nil, fmt.Errorf("huellatest: sin datos de exportación para %q", ruta)
		}
		return os.Open(fichero) //nolint:gosec // G304: ruta de la caché de compilación, dada por go list
	}
	return importer.ForCompiler(fset, "gc", buscar), nil
}

// nombreContenedora es el nombre de la declaración de primer nivel d: "F" para una función,
// "T.M" para un método. Una sentencia go en el inicializador de una var de paquete se
// atribuye a "init" (es inicialización del paquete).
func nombreContenedora(d ast.Decl) string {
	fd, ok := d.(*ast.FuncDecl)
	if !ok {
		return "init"
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	return nombreReceptor(fd.Recv.List[0].Type) + "." + fd.Name.Name
}

// nombreReceptor quita el puntero y los parámetros de tipo del receptor: T, *T, T[K].
func nombreReceptor(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return types.ExprString(e)
		}
	}
}

// normalizarGo escribe la llamada de una sentencia go como dice el contrato de Goroutines.
func normalizarGo(paquete, contenedora string, call *ast.CallExpr, info *types.Info) string {
	fun := ast.Unparen(call.Fun)
	if _, ok := fun.(*ast.FuncLit); ok {
		return paquete + "." + contenedora + ".func"
	}
	// Una función genérica instanciada (F[T]) se nombra como F.
	switch x := fun.(type) {
	case *ast.IndexExpr:
		fun = x.X
	case *ast.IndexListExpr:
		fun = x.X
	}
	if sel, ok := fun.(*ast.SelectorExpr); ok {
		if s := info.Selections[sel]; s != nil && (s.Kind() == types.MethodVal || s.Kind() == types.MethodExpr) {
			if n := tipoNombrado(s.Recv()); n != nil {
				return nombreObjeto(n.Obj()) + "." + sel.Sel.Name
			}
		}
		fun = sel.Sel // pkg.F: la función la dice el identificador de la derecha
	}
	if id, ok := fun.(*ast.Ident); ok {
		if fn, ok := info.Uses[id].(*types.Func); ok {
			return nombreObjeto(fn)
		}
	}
	// Otra forma (un valor de función: una variable, un campo, el resultado de una llamada):
	// no tiene nombre estático; se escribe la expresión, atribuida a su contenedora.
	return paquete + "." + contenedora + "." + types.ExprString(call.Fun)
}

// tipoNombrado es el tipo con nombre de t, sin alias ni puntero; nil si no lo tiene.
func tipoNombrado(t types.Type) *types.Named {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	if n, ok := t.(*types.Named); ok {
		return n
	}
	return nil
}

// nombreObjeto es "<nombre del paquete>.<nombre>" (sin paquete para los del universo).
func nombreObjeto(o types.Object) string {
	if o.Pkg() == nil {
		return o.Name()
	}
	return o.Pkg().Name() + "." + o.Name()
}

// lecturasEntorno devuelve "<línea> os.<Función>" por cada lectura del entorno de f,
// reconociendo el paquete por su import "os" (con o sin alias).
func lecturasEntorno(fset *token.FileSet, f *ast.File) []string {
	nombres := make(map[string]bool)
	for _, imp := range f.Imports {
		if ruta, err := strconv.Unquote(imp.Path.Value); err != nil || ruta != "os" {
			continue
		}
		nombre := "os"
		if imp.Name != nil {
			nombre = imp.Name.Name
		}
		nombres[nombre] = true
	}
	var ls []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !lecturaEntorno[sel.Sel.Name] {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && nombres[x.Name] {
			ls = append(ls, fmt.Sprintf("%d os.%s", fset.Position(call.Pos()).Line, sel.Sel.Name))
		}
		return true
	})
	return ls
}

// diferenciaPorListener compara, listener a listener, dos mapas de listas; un listener
// ausente es una lista vacía.
func diferenciaPorListener(lineas []string, prefijo string, quiere, tiene map[string][]string) []string {
	listeners := make(map[string]bool)
	for l := range quiere {
		listeners[l] = true
	}
	for l := range tiene {
		listeners[l] = true
	}
	for l := range listeners {
		lineas = diferenciaMulticonjunto(lineas, prefijo+l+" ", quiere[l], tiene[l])
	}
	return lineas
}

// diferenciaMulticonjunto añade una línea "<prefijo>falta x" por cada aparición de x de
// menos en tiene, y una "<prefijo>sobra x" por cada una de más.
func diferenciaMulticonjunto(lineas []string, prefijo string, quiere, tiene []string) []string {
	cuenta := make(map[string]int)
	for _, x := range quiere {
		cuenta[x]--
	}
	for _, x := range tiene {
		cuenta[x]++
	}
	for x, n := range cuenta {
		for ; n < 0; n++ {
			lineas = append(lineas, prefijo+"falta "+x)
		}
		for ; n > 0; n-- {
			lineas = append(lineas, prefijo+"sobra "+x)
		}
	}
	return lineas
}
