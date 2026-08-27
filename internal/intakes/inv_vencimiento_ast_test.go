package intakes_test

// inv_vencimiento_ast_test.go — LOS CANDADOS DEL PLAZO
// (Plan 044 · Ola 4 · T4.5, criterio (b) y la mitad vigilable de D-044.50 §1).
//
// Dos invariantes que un test de conducta NO puede sostener, porque las dos son
// afirmaciones sobre el CÓDIGO ENTERO y no sobre un camino:
//
//	(b) el evento de telemetría del vencimiento no se emite EN NINGUNA RUTA;
//	§1  el plazo del presupuesto NO obedece a tenant_settings.order_ttl_seconds.
//
// Las dos se contestan mirando el código, no ejecutándolo. Y las dos nacen VERDES
// —hoy no hay ni una aparición—, así que su trabajo no es descubrir nada sino
// impedir que aparezca: son candados, no hallazgos.
//
// 🚨 LA GUARDA ANTI-HUECO ES LA MITAD DEL TEST, igual que en su molde
// (inv1_aprobar_ast_test.go). Un barrido que no encuentra nada pasa siempre, y ese
// es su modo de fallo natural: se cambia la ruta, se rompe el filtro de
// extensiones, y el candado sigue verde vigilando una pared. Por eso cada barrido
// exige un CONTROL POSITIVO —un literal que SÍ está y que tiene que aparecer—
// antes de que su silencio signifique algo.
//
// LOS DOS BARRIDOS MIRAN COSAS DISTINTAS, Y NO ES INCONSISTENCIA:
//
//   - el del evento es de TEXTO CRUDO sobre el repo entero, comentarios incluidos.
//     Lo que se prohíbe ahí es el NOMBRE, porque el literal es la puerta: primero
//     aparece en un comentario, luego en una constante «por si acaso» y al final en
//     un `emit`;
//   - el del TTL derogado es sobre el AST del paquete, sin comentarios. Ahí lo que
//     se prohíbe es OBEDECER la columna, y el comentario de vencimiento.go que
//     explica por qué no se reusa es justamente lo que hay que conservar.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// eventoProhibido es el nombre del evento de telemetría que REQ-25 y el criterio (b)
// prohíben. Se compone por CONCATENACIÓN y no se escribe entero en ninguna parte del
// repo: el barrido de abajo mira texto crudo, así que un literal suelto en cualquier
// fichero lo pondría rojo — y el primero que se delataría sería éste.
//
// Que el nombre no se pueda escribir es el punto. Detrás no hay una cadena sino una
// doctrina: los objetos de negocio no mueren por tiempo, mueren por acción humana
// (ADR-0029 Enmienda 2, D-041.16). La solicitud pasada de plazo se MARCA, y nadie
// emite que expiró.
var eventoProhibido = "intake_" + "expired"

// controlPositivoTexto es un literal que SÍ existe en el repo y que el mismo barrido
// tiene que encontrar. Se elige `deposit_reminded_at` porque cumple las dos
// condiciones que hacen útil a un control: está en varios ficheros y de VARIAS
// extensiones —.go (dominio y stores) y .sql (la migración 0045)—, así que caza
// tanto una raíz mal puesta como un filtro de extensiones roto.
const controlPositivoTexto = "deposit_reminded_at"

// extensionesBarridas son los ficheros donde un evento podría emitirse o declararse:
// código, esquema, plantillas y contratos. Se enumeran en vez de barrer todo para no
// leer binarios ni el contenido de .git.
var extensionesBarridas = map[string]bool{
	".go": true, ".sql": true, ".html": true, ".json": true, ".yml": true, ".yaml": true,
}

// raízDelRepo es la raíz de wapp-cloud-platform vista desde internal/intakes.
const raízDelRepo = "../.."

// esteCandado es el ÚNICO fichero del repo donde los literales perseguidos pueden
// vivir legítimamente, porque es el que los persigue.
const esteCandado = "inv_vencimiento_ast_test.go"

// TestVencimiento_ElEventoDelVencimientoNoExisteEnNingunaRuta es el criterio (b).
//
// El criterio dice «no se emite», y aquí se comprueba algo MÁS FUERTE: que el nombre
// no aparece. Es a propósito — «no se emite» solo se puede comprobar sobre los
// caminos que uno se acuerda de mirar, y el que se olvide es justo por donde pasará.
// «No existe» se comprueba entero.
func TestVencimiento_ElEventoDelVencimientoNoExisteEnNingunaRuta(t *testing.T) {
	prohibido, positivo, leídos := barrerTexto(t, raízDelRepo, eventoProhibido, controlPositivoTexto)

	// (1) Control POSITIVO y anti-hueco. Van primero: si el barrido no leyó
	// ficheros, o no encontró el literal que SÍ está, ninguna de sus ausencias
	// significa nada.
	if leídos == 0 {
		t.Fatalf("el barrido no leyó ni un fichero bajo %s: no está mirando nada, así que su "+
			"silencio no prueba nada. ¿Se movió el paquete o se rompió el filtro de extensiones?", raízDelRepo)
	}
	if len(positivo) == 0 {
		t.Fatalf("el barrido leyó %d ficheros y no encontró %q, que está en el repo desde la "+
			"migración 0045. El barrido está roto: arréglalo ANTES de fiarte de su verde",
			leídos, controlPositivoTexto)
	}

	// (2) La invariante.
	if len(prohibido) > 0 {
		t.Errorf("🔴 el evento del vencimiento aparece en %d sitio(s): %s\n\n"+
			"REQ-25 y el criterio (b) de T4.5 lo prohíben, y no es una preferencia de nombres: un "+
			"presupuesto pasado de plazo se MARCA y sigue en pending_approval. Emitir que algo "+
			"expiró es afirmar que murió, y aquí nada muere por tiempo (ADR-0029 Enmienda 2, "+
			"D-041.16). Si de verdad hace falta telemetría del vencimiento, la decisión es de "+
			"producto y va escrita antes que el código",
			len(prohibido), strings.Join(prohibido, ", "))
	}
}

// TestVencimiento_ElPlazoNoObedeceAlTTLDerogado es la mitad de D-044.50 §1 que se
// puede vigilar sola.
//
// `tenant_settings.order_ttl_seconds` existe, tiene default y HASTA SE LEE
// (TenantSettings.OrderTTL, internal/flujos/store) — pero el COMMENT de su migración
// 0013 afirma que «NO se obedece: ningun codigo actua sobre este valor» desde que
// D-041.16 lo derogó como causa de muerte, y esa afirmación hoy es CIERTA.
//
// T4.5 era la primera tarea en dos olas con un motivo plausible para reusarlo —hacía
// falta un plazo y ahí había uno—, y se decidió que no. Este candado es lo que impide
// que el dominio de las solicitudes empiece a obedecerlo por la puerta de atrás y
// convierta en falso un COMMENT del esquema que nadie volvería a leer.
//
// Mira el AST y no el texto: un `order_ttl` dentro de una cadena o de un identificador
// es código que lo usa; el mismo texto dentro del comentario que explica por qué NO
// se usa es exactamente lo que hay que conservar.
func TestVencimiento_ElPlazoNoObedeceAlTTLDerogado(t *testing.T) {
	const dominio = "." // internal/intakes, el paquete donde vive el plazo
	ttlDerogado, positivo, leídos := barrerAST(t, dominio, []string{"order_ttl", "OrderTTL"}, "QuoteDeadline")

	if leídos == 0 {
		t.Fatalf("el barrido no leyó ni un fichero de producción de %s: no está mirando nada", dominio)
	}
	if len(positivo) == 0 {
		t.Fatal("el barrido no encontró QuoteDeadline en el propio paquete del plazo. O la " +
			"constante se renombró —y entonces este candado y su decisión (D-044.50 §1) hay que " +
			"revisarlos— o el barrido está roto")
	}
	if len(ttlDerogado) > 0 {
		t.Errorf("🔴 el dominio de las solicitudes usa order_ttl en %s.\n\n"+
			"D-044.50 §1: el plazo del presupuesto es una CONSTANTE DE PLATAFORMA "+
			"(intakes.QuoteDeadline) y NO se lee de tenant_settings.order_ttl_seconds. Obedecer esa "+
			"columna convertiría en FALSA una afirmación del esquema que hoy es cierta —el COMMENT "+
			"de la 0013— y reabriría como plazo lo que D-041.16 derogó como causa de muerte. Si el "+
			"plazo tiene que ser configurable, la salida escrita es una columna NUEVA con esta "+
			"constante de DEFAULT, no reciclar la derogada",
			strings.Join(ttlDerogado, ", "))
	}
}

// barrerTexto recorre `raíz` y devuelve dónde aparece cada uno de los dos literales
// (`fichero:línea`) y cuántos ficheros llegó a leer de verdad.
//
// Se excluye ESTE fichero y nada más. El literal prohibido viaja compuesto por
// concatenación, así que ni siquiera se auto-encontraría; la exclusión está por el
// control positivo y para que quien lea esto no tenga que deducirlo.
//
// El conteo de ficheros leídos es el anti-hueco: sin él, un filtro de extensiones
// roto o una raíz mal puesta darían cero apariciones y verde.
func barrerTexto(t *testing.T, raíz, prohibido, positivo string) (dóndeProhibido, dóndePositivo []string, leídos int) {
	t.Helper()

	err := filepath.WalkDir(raíz, func(ruta string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "certs":
				return filepath.SkipDir
			}
			return nil
		}
		if !extensionesBarridas[filepath.Ext(d.Name())] || d.Name() == esteCandado {
			return nil
		}
		//nolint:gosec // G304: la ruta sale del propio WalkDir sobre el repo, no de entrada externa
		contenido, rerr := os.ReadFile(ruta)
		if rerr != nil {
			return rerr
		}
		leídos++
		for i, línea := range strings.Split(string(contenido), "\n") {
			if strings.Contains(línea, prohibido) {
				dóndeProhibido = append(dóndeProhibido, ruta+":"+strconv.Itoa(i+1))
			}
			if strings.Contains(línea, positivo) {
				dóndePositivo = append(dóndePositivo, ruta+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("barriendo %s: %v. Si el repo se reorganizó, este candado se quedó vigilando un "+
			"sitio que no existe: arregla la raíz", raíz, err)
	}
	return dóndeProhibido, dóndePositivo, leídos
}

// barrerAST parsea los ficheros de PRODUCCIÓN del directorio y busca los literales en
// lo que el compilador ve —cadenas e identificadores—, NO en los comentarios: el
// parser se invoca con modo 0, que los descarta.
//
// Se excluyen los _test.go: un test que nombre la columna derogada para comprobar
// que nadie la usa no es código que la use. Se leen los ficheros a mano porque
// parser.ParseDir está deprecado desde Go 1.22.
func barrerAST(t *testing.T, dir string, prohibidos []string, positivo string) (dóndeProhibido, dóndePositivo []string, leídos int) {
	t.Helper()
	entradas, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no se pudo listar %s: %v. Si el paquete se movió, este candado se queda vigilando "+
			"un sitio que no existe", dir, err)
	}

	for _, e := range entradas {
		nombre := e.Name()
		if e.IsDir() || !strings.HasSuffix(nombre, ".go") || strings.HasSuffix(nombre, "_test.go") {
			continue
		}
		leídos++
		ruta := filepath.Join(dir, nombre)
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, ruta, nil, 0)
		if perr != nil {
			t.Fatalf("no se pudo parsear %s: %v", ruta, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			texto := ""
			switch nodo := n.(type) {
			case *ast.BasicLit:
				texto = nodo.Value
			case *ast.Ident:
				texto = nodo.Name
			default:
				return true
			}
			sitio := ruta + ":" + strconv.Itoa(fset.Position(n.Pos()).Line)
			for _, p := range prohibidos {
				if strings.Contains(texto, p) {
					dóndeProhibido = append(dóndeProhibido, sitio)
				}
			}
			if strings.Contains(texto, positivo) {
				dóndePositivo = append(dóndePositivo, sitio)
			}
			return true
		})
	}
	if leídos == 0 {
		t.Fatalf("el barrido no leyó ni un fichero de producción de %s: no está mirando nada, así "+
			"que su silencio no prueba nada", dir)
	}
	return dóndeProhibido, dóndePositivo, leídos
}
