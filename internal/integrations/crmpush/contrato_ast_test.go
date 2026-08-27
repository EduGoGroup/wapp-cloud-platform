package crmpush

// contrato_ast_test.go — LOS CAMPOS CLAVE DEL CONTRATO NO PUEDEN SER CONSTANTES,
// y se vigila sobre el AST (Plan 044 · T4.10, criterio (b), ampliado en la mitad 2).
//
// 🚚 ESTE FICHERO SE MUDÓ. Nació como internal/flujos/runtime/revision_no_ast_test.go
// y solo miraba `RevisionNo` dentro del paquete runtime, porque ahí vivía el
// constructor del payload. La Tanda 2 de la Ola 4 lo sacó de ahí —la regla ahora es
// crmpush, que usan las DOS puertas— y un candado que se quedara en runtime seguiría
// vigilando un sitio VACÍO: verde sin mirar nada. Por eso barre los DOS directorios
// y por eso exige encontrar sitios en cada uno.

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

// camposVigilados son los del contrato wapp-crm-v1 que un literal ARRUINA en
// silencio. Los dos estuvieron clavados y los dos mintieron:
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
var camposVigilados = []string{"RevisionNo", "LifecycleStatus"}

// directoriosVigilados son los dos sitios donde hoy se fija alguno de esos campos:
// aquí (crmpush.Build arma el documento) y el traductor del motor de flujos
// (runtime.effectInput, que saca los datos del efecto). Si uno de los dos deja de
// tener sitios, el barrido CORTA en vez de dar verde: es la señal de que el código
// se mudó otra vez y este candado se quedó mirando a la pared.
var directoriosVigilados = []string{".", filepath.Join("..", "..", "flujos", "runtime")}

// TestContrato_NingunCampoClaveEsConstante recorre TODOS los ficheros de producción
// de los directorios vigilados y exige que cada sitio donde se fija uno de los
// campos tome su valor de algo CALCULADO —el número que la base asignó a la
// revisión, el estado real de la solicitud— y nunca de un literal ni de una
// constante del paquete.
//
// Lo que este test NO prueba: que el valor sea el CORRECTO. Eso lo prueban los tests
// de conducta (push_test.go de este paquete, webhook_sink_revision_test.go y
// revision_no_e2e_test.go en runtime). Éste solo vigila que nadie vuelva a clavarlo.
func TestContrato_NingunCampoClaveEsConstante(t *testing.T) {
	for _, dir := range directoriosVigilados {
		consts, sitios := barreElPaquete(t, dir)

		// Un test estructural que no ve nada pasa siempre: si un campo se renombra o
		// el constructor se muda de paquete, esto tiene que cortar, no dar verde.
		if len(sitios) == 0 {
			t.Fatalf("no se encontró ni un solo sitio que fije %v en %s. Si el campo se renombró "+
				"o el constructor del contrato se mudó, arregla este barrido (y directoriosVigilados) "+
				"ANTES de fiarte del verde", camposVigilados, dir)
		}
		for _, campo := range camposVigilados {
			if !cubre(sitios, campo) {
				t.Fatalf("%s no fija %s en ningún sitio: el candado dejó de cubrir ese campo ahí. "+
					"O el campo se movió a otro paquete —añádelo a directoriosVigilados— o dejó de "+
					"existir, y entonces sobra de camposVigilados", dir, campo)
			}
		}
		for _, s := range sitios {
			if motivo := esConstante(s.valor, consts); motivo != "" {
				t.Errorf("🔴 %s: %s se fija con %s. El contrato wapp-crm-v1 clavea por "+
					"(intake_id, revision_no) y el puente descarta los pares repetidos, así que un "+
					"valor fijo hace que el CRM se quede con el primer estado de la solicitud PARA "+
					"SIEMPRE, sin un solo error. Léelo del dato de verdad: el número que la base "+
					"asignó a la revisión y el estado REAL del intake.", s.donde, s.campo, motivo)
			}
		}
	}
}

// sitio es un punto del código donde se fija uno de los campos vigilados, con el
// fichero:línea para que el rojo diga DÓNDE sin que nadie tenga que grepear.
type sitio struct {
	donde string
	campo string
	valor ast.Expr
}

// cubre responde si alguno de los sitios encontrados fija ese campo.
func cubre(sitios []sitio, campo string) bool {
	for _, s := range sitios {
		if s.campo == campo {
			return true
		}
	}
	return false
}

// barreElPaquete parsea los ficheros de producción del directorio dado y devuelve
// las constantes declaradas en él (para reconocer `RevisionNo: revUno`, que es la
// misma trampa con otro disfraz) y todos los sitios que fijan un campo vigilado.
//
// Se leen los ficheros a mano en vez de con parser.ParseDir —deprecado desde Go
// 1.22— y se excluyen los _test.go a propósito: un fixture de test SÍ puede fijar un
// número, y de hecho lo hace.
func barreElPaquete(t *testing.T, dir string) (map[string]bool, []sitio) {
	t.Helper()
	entradas, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no se pudo listar %s: %v. Si el paquete se movió, este candado se queda "+
			"vigilando un sitio que no existe", dir, err)
	}

	consts := map[string]bool{}
	var sitios []sitio
	vistos := 0
	for _, e := range entradas {
		nombre := e.Name()
		if e.IsDir() || !strings.HasSuffix(nombre, ".go") || strings.HasSuffix(nombre, "_test.go") {
			continue
		}
		vistos++
		ruta := filepath.Join(dir, nombre)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, ruta, nil, 0)
		if err != nil {
			t.Fatalf("no se pudo parsear %s: %v", ruta, err)
		}
		recogeConstantes(f, consts)
		sitios = append(sitios, sitiosDeCampo(fset, ruta, f)...)
	}
	if vistos == 0 {
		t.Fatalf("el barrido no leyó ni un fichero de producción de %s: no está mirando nada", dir)
	}
	return consts, sitios
}

// recogeConstantes apunta los nombres declarados con `const` en el fichero.
func recogeConstantes(f *ast.File, consts map[string]bool) {
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

// sitiosDeCampo localiza las DOS formas de fijar un campo vigilado: dentro de un
// literal compuesto (`Payload{… RevisionNo: x …}`) y por asignación posterior
// (`p.RevisionNo = x`), que es como se colaría el arreglo «rápido».
func sitiosDeCampo(fset *token.FileSet, fichero string, f *ast.File) []sitio {
	var out []sitio
	pos := func(n ast.Node) string {
		p := fset.Position(n.Pos())
		return fichero + ":" + strconv.Itoa(p.Line)
	}
	vigilado := func(nombre string) bool {
		for _, c := range camposVigilados {
			if c == nombre {
				return true
			}
		}
		return false
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.KeyValueExpr:
			if id, ok := v.Key.(*ast.Ident); ok && vigilado(id.Name) {
				out = append(out, sitio{donde: pos(v), campo: id.Name, valor: v.Value})
			}
		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || !vigilado(sel.Sel.Name) || i >= len(v.Rhs) {
					continue
				}
				out = append(out, sitio{donde: pos(v), campo: sel.Sel.Name, valor: v.Rhs[i]})
			}
		}
		return true
	})
	return out
}

// esConstante devuelve el motivo del rojo, o "" si el valor es algo calculado.
// Reconoce el literal pelado (`1`, `"confirmed"`), el literal con signo (`-1`, que
// además el schema rechaza) y el ident que apunta a una constante del paquete.
func esConstante(v ast.Expr, consts map[string]bool) string {
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
