package bootstrap_test

// quotetext_cableado_test.go — QUE EL GENERADOR DE COTIZACIÓN ESTÉ ENCHUFADO
// (Plan 044 · Ola 5 · T5.1).
//
// # POR QUÉ ESTE TEST EXISTE
//
// Por lo mismo que su hermano de al lado, y esta ola ya lo pagó DOS veces: una pieza
// que nadie construye pasa igual sus propios tests. Y el modo de fallo de ÉSTA es
// especialmente mudo:
//
//   - sin `quotetext.NewServicio` o sin `Deps.QuoteSuggestions`, la ruta
//     `POST /api/v1/intakes/{id}/quote-suggestion` NO SE MONTA y responde 404 de ruta
//     inexistente. Nada falla y nada avisa; el botón «Sugerir texto» de la consola
//     simplemente no hace nada;
//   - sin `quotetext.ConSemilla`, el generador FUNCIONA —da textos, con el historial
//     aprobado— y lo único que se pierde en silencio es el arranque en frío: un tenant
//     recién dado de alta que se molestó en escribir su `quote_style_examples` no lo
//     vería usado nunca, y el síntoma sería «esto no imita mi voz», que nadie
//     relacionaría con un cable que falta.
//
// Mismo método que reanalisis_cableado_test.go: se lee el AST de bootstrap.go. No
// monta el proceso —no hay base de datos aquí— y por eso NO SE SALTA nunca.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCableado_ElGeneradorDeCotizacionEstaEnchufado exige las tres cosas que
// bootstrap.go tiene que hacer para que T5.1 exista en producción.
func TestCableado_ElGeneradorDeCotizacionEstaEnchufado(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, "bootstrap.go", nil, 0)
	if err != nil {
		t.Fatalf("parseando bootstrap.go: %v", err)
	}

	var visto cableadoDelGenerador
	ast.Inspect(archivo, func(n ast.Node) bool {
		visto.anota(t, fset, n)
		return true
	})
	visto.exige(t)
}

type cableadoDelGenerador struct {
	servicio bool // (a) quotetext.NewServicio(...)
	enDeps   bool // (b) publicapi.Deps{... QuoteSuggestions: ...}
	semilla  bool // (c) quotetext.ConSemilla(<algo que no es nil>)
}

func (c *cableadoDelGenerador) anota(t *testing.T, fset *token.FileSet, n ast.Node) {
	t.Helper()
	switch v := n.(type) {
	case *ast.CallExpr:
		switch campoDe(v.Fun) {
		case "quotetext.NewServicio":
			c.servicio = true
		case "quotetext.ConSemilla":
			// 🔴 NO BASTA CON VER LA OPCIÓN ESCRITA: la opción es nil-safe a propósito
			// (`ConSemilla(nil)` compila y se traga el nil), así que un cable roto no
			// daría error de compilación ni pondría rojo ningún otro test.
			if len(v.Args) != 1 || campoDe(v.Args[0]) == "nil" {
				t.Fatalf("quotetext.ConSemilla no recibe un lector utilizable: la opción se traga el nil "+
					"y los ejemplos semilla dejarían de leerse en silencio (%s)", fset.Position(v.Pos()))
			}
			c.semilla = true
		}
	case *ast.KeyValueExpr:
		if campoDe(v.Key) == "QuoteSuggestions" {
			if campoDe(v.Value) == "nil" {
				t.Fatalf("publicapi.Deps.QuoteSuggestions se cablea a nil: la ruta no se montaría (%s)",
					fset.Position(v.Pos()))
			}
			c.enDeps = true
		}
	}
}

func (c *cableadoDelGenerador) exige(t *testing.T) {
	t.Helper()
	if !c.servicio {
		t.Error("internal/bootstrap/bootstrap.go NO llama a quotetext.NewServicio.\n" +
			"Sin el generador construido, Deps.QuoteSuggestions queda nil y la ruta " +
			"POST /api/v1/intakes/{id}/quote-suggestion NO SE MONTA: responde 404 de ruta " +
			"inexistente. Nada falla y nada avisa.")
	}
	if !c.enDeps {
		t.Error("publicapi.Deps NO recibe el generador (campo QuoteSuggestions).\n" +
			"Construirlo y no pasarlo tiene el MISMO efecto que no construirlo: la ruta no se monta.")
	}
	if !c.semilla {
		t.Error("quotetext.NewServicio se construye SIN quotetext.ConSemilla.\n" +
			"El generador sigue dando textos con el historial aprobado, así que ningún otro test se " +
			"pone rojo. Lo que se pierde es el ARRANQUE EN FRÍO de D-044.11: un tenant sin historial " +
			"que escribió su `quote_style_examples` no lo vería usado nunca, y el síntoma —«esto no " +
			"imita mi voz»— no señala a ningún sitio.")
	}
}
