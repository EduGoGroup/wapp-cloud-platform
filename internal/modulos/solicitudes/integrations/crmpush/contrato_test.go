package crmpush

// contrato_test.go — R-12 SOBRE EL AST: LOS CAMPOS CLAVE DEL CONTRATO NO PUEDEN SER
// CONSTANTES.
//
// Porta internal/integrations/crmpush/contrato_ast_test.go @ 36d5a04 (candado de
// invariante, `05` §3.2: el AST está permitido aquí, E-7). Aquel nació a su vez como
// internal/flujos/runtime/revision_no_ast_test.go y solo miraba `RevisionNo` dentro
// del motor, porque ahí vivía el constructor del payload; cuando la regla pasó a
// crmpush —que usan las DOS puertas— un candado que se quedara en el motor seguiría
// vigilando un sitio VACÍO: verde sin mirar nada. Por eso barre los DOS directorios
// y por eso exige encontrar sitios en cada uno.
//
// Nació con `//go:build pendiente` (mientras push.go era contrato sin lógica, este
// paquete no fijaba ninguno de los dos campos y la guarda anti-hueco cortaba) y la
// perdió con el verde de push.go.
//
// La lista de directorios es la de la fase F6 y se re-toca en F8, cuando el motor
// pase a `../../../conversacion/runtime` (diseno.md §6), con la guarda intacta.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// watchedFields (era camposVigilados) son los del contrato wapp-crm-v1 que un
// literal ARRUINA en silencio. Los dos estuvieron clavados y los dos mintieron:
//
//   - RevisionNo fue `1`. El puente hace UPSERT por (intake_id, revision_no) y
//     descarta como duplicado todo par repetido —manual del integrador §4—, así que
//     dos pushes de la misma solicitud con el mismo número dejan al CRM con el
//     PRIMER estado para siempre: el pedido corregido no llega, y no hay un error en
//     ningún log ni en ninguna métrica.
//   - LifecycleStatus fue `"confirmed"`. Acertaba POR CASUALIDAD mientras el único
//     productor era el cierre del carrito, que siempre confirma. El re-empuje de una
//     corrección devuelve la solicitud a `pending_approval` y el literal le contaría
//     al CRM que está confirmada — el mismo defecto que el `1`, con otro nombre.
//
// Un test de conducta sobre UN solo push pasa igual con la constante en los dos
// casos: por eso hace falta mirar el código y no la salida.
var watchedFields = []string{"RevisionNo", "LifecycleStatus"}

// watchedDirs (era directoriosVigilados) son los dos sitios donde hoy se fija alguno
// de esos campos: aquí (Build arma el documento y el adaptador de revisiones rellena
// el Input) y el traductor del motor de flujos VIEJO, que es el que corre y saca los
// datos del efecto (internal/flujos/runtime). Si uno de los dos deja de tener
// sitios, el barrido CORTA en vez de dar verde: es la señal de que el código se mudó
// otra vez y este candado se quedó mirando a la pared.
var watchedDirs = []string{".", filepath.Join("..", "..", "..", "..", "flujos", "runtime")}

// TestContract_NoKeyFieldIsConstant (era TestContrato_NingunCampoClaveEsConstante)
// recorre TODOS los ficheros de producción de los directorios vigilados y exige que
// cada sitio donde se fija uno de los campos tome su valor de algo CALCULADO —el
// número que la base asignó a la revisión, el estado real de la solicitud— y nunca
// de un literal ni de una constante del paquete.
//
// Lo que este test NO prueba: que el valor sea el CORRECTO. Eso lo prueban los tests
// de conducta (push_test.go y desde_intakes_test.go de este paquete). Éste solo
// vigila que nadie vuelva a clavarlo.
func TestContract_NoKeyFieldIsConstant(t *testing.T) {
	for _, dir := range watchedDirs {
		consts, sites := scanPackage(t, dir)

		// Un test estructural que no ve nada pasa siempre: si un campo se renombra o
		// el constructor se muda de paquete, esto tiene que cortar, no dar verde.
		if len(sites) == 0 {
			t.Fatalf("no se encontró ni un solo sitio que fije %v en %s. Si el campo se renombró "+
				"o el constructor del contrato se mudó, arregla este barrido (y watchedDirs) "+
				"ANTES de fiarte del verde", watchedFields, dir)
		}
		for _, field := range watchedFields {
			if !covers(sites, field) {
				t.Fatalf("%s no fija %s en ningún sitio: el candado dejó de cubrir ese campo ahí. "+
					"O el campo se movió a otro paquete —añádelo a watchedDirs— o dejó de "+
					"existir, y entonces sobra de watchedFields", dir, field)
			}
		}
		for _, s := range sites {
			if reason := constantReason(s.value, consts); reason != "" {
				t.Errorf("🔴 %s: %s se fija con %s. El contrato wapp-crm-v1 clavea por "+
					"(intake_id, revision_no) y el puente descarta los pares repetidos, así que un "+
					"valor fijo hace que el CRM se quede con el primer estado de la solicitud PARA "+
					"SIEMPRE, sin un solo error. Léelo del dato de verdad: el número que la base "+
					"asignó a la revisión y el estado REAL del intake.", s.where, s.field, reason)
			}
		}
	}
}

// site (era sitio) es un punto del código donde se fija uno de los campos vigilados,
// con el fichero:línea para que el rojo diga DÓNDE sin que nadie tenga que grepear.
type site struct {
	where string
	field string
	value ast.Expr
}

// covers (era cubre) responde si alguno de los sitios encontrados fija ese campo.
func covers(sites []site, field string) bool {
	return slices.ContainsFunc(sites, func(s site) bool { return s.field == field })
}

// scanPackage (era barreElPaquete) parsea los ficheros de producción del directorio
// dado y devuelve las constantes declaradas en él (para reconocer
// `RevisionNo: revOne`, que es la misma trampa con otro disfraz) y todos los sitios
// que fijan un campo vigilado.
//
// Se leen los ficheros a mano en vez de con parser.ParseDir —deprecado desde Go
// 1.22— y se excluyen los _test.go a propósito: un fixture de test SÍ puede fijar un
// número, y de hecho lo hace. No baja a subdirectorios: un barrido recursivo
// silencioso convertiría «este paquete ya no existe» en «no encontré nada».
func scanPackage(t *testing.T, dir string) (map[string]bool, []site) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no se pudo listar %s: %v. Si el paquete se movió, este candado se queda "+
			"vigilando un sitio que no existe", dir, err)
	}

	consts := map[string]bool{}
	var sites []site
	read := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		read++
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("no se pudo parsear %s: %v", path, perr)
		}
		collectConstants(f, consts)
		sites = append(sites, fieldSites(fset, path, f)...)
	}
	if read == 0 {
		t.Fatalf("el barrido no leyó ni un fichero de producción de %s: no está mirando nada", dir)
	}
	return consts, sites
}

// collectConstants (era recogeConstantes) apunta los nombres declarados con `const`
// en el fichero.
func collectConstants(f *ast.File, consts map[string]bool) {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				consts[n.Name] = true
			}
		}
	}
}

// fieldSites (era sitiosDeCampo) localiza las DOS formas de fijar un campo vigilado:
// dentro de un literal compuesto (`Payload{… RevisionNo: x …}`) y por asignación
// posterior (`p.RevisionNo = x`), que es como se colaría el arreglo «rápido».
func fieldSites(fset *token.FileSet, file string, f *ast.File) []site {
	var out []site
	pos := func(n ast.Node) string {
		return file + ":" + strconv.Itoa(fset.Position(n.Pos()).Line)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.KeyValueExpr:
			if id, ok := v.Key.(*ast.Ident); ok && slices.Contains(watchedFields, id.Name) {
				out = append(out, site{where: pos(v), field: id.Name, value: v.Value})
			}
		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || !slices.Contains(watchedFields, sel.Sel.Name) || i >= len(v.Rhs) {
					continue
				}
				out = append(out, site{where: pos(v), field: sel.Sel.Name, value: v.Rhs[i]})
			}
		}
		return true
	})
	return out
}

// constantReason (era esConstante) devuelve el motivo del rojo, o "" si el valor es
// algo calculado. Reconoce el literal pelado (`1`, `"confirmed"`), el literal con
// signo (`-1`, que además el schema rechaza) y el identificador que apunta a una
// constante del paquete.
func constantReason(v ast.Expr, consts map[string]bool) string {
	switch e := v.(type) {
	case *ast.BasicLit:
		return "el literal " + e.Value
	case *ast.UnaryExpr:
		if lit, ok := e.X.(*ast.BasicLit); ok {
			return "el literal " + e.Op.String() + lit.Value
		}
	case *ast.Ident:
		if consts[e.Name] {
			return "la constante del paquete " + e.Name
		}
	}
	return ""
}

// TestContract_DetectorRecognizesTheThreeDisguises es el control POSITIVO del propio
// detector: sobre un fuente de mentira comprueba que denuncia el literal, el literal
// con signo y la constante del paquete —en literal compuesto y en asignación— y que
// deja pasar lo calculado. Sin esto, un detector que no reconociera nada daría el
// mismo verde que un código limpio.
func TestContract_DetectorRecognizesTheThreeDisguises(t *testing.T) {
	const src = `package x

const revOne = 1

func f(in Input, n int) Payload {
	p := Payload{RevisionNo: 1, LifecycleStatus: "confirmed"}
	p.RevisionNo = -1
	p.RevisionNo = revOne
	p.RevisionNo = n
	p.LifecycleStatus = normalize(in.LifecycleStatus)
	return Payload{RevisionNo: in.RevisionNo, LifecycleStatus: in.LifecycleStatus, Total: 3}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fake.go", src, 0)
	if err != nil {
		t.Fatalf("no se pudo parsear el fuente de mentira: %v", err)
	}
	consts := map[string]bool{}
	collectConstants(f, consts)
	if !consts["revOne"] {
		t.Fatalf("collectConstants no vio la constante revOne: %v", consts)
	}

	got := map[string]string{}
	for _, s := range fieldSites(fset, "fake.go", f) {
		got[s.where+" "+s.field] = constantReason(s.value, consts)
	}
	want := map[string]string{
		"fake.go:6 RevisionNo":       "el literal 1",
		"fake.go:6 LifecycleStatus":  `el literal "confirmed"`,
		"fake.go:7 RevisionNo":       "el literal -1",
		"fake.go:8 RevisionNo":       "la constante del paquete revOne",
		"fake.go:9 RevisionNo":       "",
		"fake.go:10 LifecycleStatus": "",
		"fake.go:11 RevisionNo":      "",
		"fake.go:11 LifecycleStatus": "",
	}
	if len(got) != len(want) {
		t.Fatalf("el detector vio %d sitios y hay %d: %v", len(got), len(want), got)
	}
	for where, reason := range want {
		if g, ok := got[where]; !ok || g != reason {
			t.Errorf("%s: motivo = %q (visto=%v), quiero %q", where, g, ok, reason)
		}
	}
}
