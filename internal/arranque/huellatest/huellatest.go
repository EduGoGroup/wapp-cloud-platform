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
	"net/http"

	"google.golang.org/grpc"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
	panic(pendiente.Implementar("huellatest.Candidatos"))
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
	panic(pendiente.Implementar("huellatest.Rutas"))
}

// RPC devuelve los rpc registrados en gs según gs.GetServiceInfo(): un "servicio/método" por
// cada método unario Y cada stream de cada servicio (p. ej. "wapp.cloudlink.v1.Link/Connect"),
// ordenados. Un servidor sin servicios da una lista de longitud 0.
func RPC(gs *grpc.Server) []string {
	panic(pendiente.Implementar("huellatest.RPC"))
}

// Metricas hace UNA petición GET /metrics a través de admin (el handler del listener :8100
// entero, no el de /metrics suelto) y devuelve el nombre de cada familia declarada en una
// línea `# TYPE <nombre> <tipo>` cuyo nombre empieza por "wapp_". Las líneas `# HELP`, las
// muestras y las familias sin ese prefijo (go_*, process_*, promhttp_*) no cuentan.
//
// El resultado está ordenado y sin duplicados. Un estado distinto de 200 es un error que
// incluye el estado recibido (un admin sin /metrics no es «cero familias»).
func Metricas(admin http.Handler) ([]string, error) {
	panic(pendiente.Implementar("huellatest.Metricas"))
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
	panic(pendiente.Implementar("huellatest.Goroutines"))
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
	panic(pendiente.Implementar("huellatest.Hooks"))
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
	panic(pendiente.Implementar("huellatest.Entorno"))
}

// Leer lee el fichero JSON ruta (el formato de Escribir: un array de Huella) y devuelve sus
// huellas en el orden del fichero. Un fichero que no existe o que no es JSON válido de ese
// formato es un error que nombra ruta.
func Leer(ruta string) ([]Huella, error) {
	panic(pendiente.Implementar("huellatest.Leer"))
}

// Escribir escribe hs en ruta (lo crea o lo trunca) como un array JSON indentado con dos
// espacios, las huellas en el orden dado y las listas tal como vienen (no reordena: la
// dorada refleja lo calculado), terminado en un salto de línea. Las claves de los mapas van
// ordenadas (encoding/json). Determinista: escribir dos veces lo mismo da los mismos bytes,
// y Leer(ruta) devuelve hs (con las listas `omitempty` vacías leídas como nil). Un
// directorio padre que no existe es un error; Escribir no crea directorios.
func Escribir(ruta string, hs []Huella) error {
	panic(pendiente.Implementar("huellatest.Escribir"))
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
	panic(pendiente.Implementar("huellatest.Diferencia"))
}
