package catalogo

import "fmt"

// normalizador.go — LA FRONTERA CON `wapp-shared/textmatch`, Y EL CONTRATO QUE
// EXIGE (Plan 044 · Ola 3 · T3.7 ↔ T3.1).
//
// # 🔴 POR QUÉ HAY UN PUERTO Y NO UN IMPORT
//
// El normalizador canónico es `textmatch.Normalize`
// (`shared/wapp-shared/textmatch/normalize.go:76`). Existe EN DISCO y está
// terminado, pero el 2026-08-26 todavía NO está publicado: el directorio
// `textmatch/` aparece como `?? textmatch/` en el `git status` de `wapp-shared`, no
// hay ningún tag `textmatch/vX.Y.Z`, y el módulo tampoco está en las líneas `use`
// del `go.work` del ecosistema. El gate de este repo es `GOWORK=off`, que resuelve
// por `go.mod` y por tanto por release, así que un `import` de ese módulo HOY no
// compila. Y un `replace` tampoco vale: la ruta base de wApp lleva un espacio
// («/Volumes/Macintosh HD/…») y Go rechaza el path («malformed module path
// "/Volumes/Macintosh"»).
//
// Copiar aquí el plegado de diacríticos habría compilado, y habría sido peor: dos
// normalizadores que divergen el día que alguien añada una letra a uno de los dos,
// con un síntoma —el match falla solo con ciertas palabras acentuadas— que nadie
// ata a la causa. Así que el índice DECLARA la forma (Normalizador) y la RECIBE.
//
// # QUÉ QUEDA PENDIENTE DE CABLEAR, EXACTAMENTE
//
// Tres líneas, el día que `textmatch` tenga release:
//
//  1. `go get github.com/EduGoGroup/wapp-shared/textmatch@vX.Y.Z` en este repo;
//  2. añadir `./shared/wapp-shared/textmatch` al `go.work` del ecosistema;
//  3. en el arranque del worker, `catalogo.NewCache(fuente, textmatch.Normalize, 0)`.
//
// No hay nada más que tocar: ningún tipo de este paquete menciona textmatch.
//
// # POR QUÉ EL CONTRATO SE VERIFICA EN RUNTIME Y NO SE CONFÍA
//
// Porque el modo de fallo del normalizador equivocado es SILENCIOSO. Con un
// `strings.ToLower` a secas —que parece un normalizador perfectamente razonable—
// «Café» dejaría de casar con «cafe» y el ítem saldría `unmatched` sin un solo
// error en el log. Y con un plegado que trate la «ñ» como una «n» con tilde,
// «año» colapsa con «ano»: dos artículos distintos casarían el mismo texto.
//
// Por eso `Construir` llama a VerificarNormalizador ANTES de indexar nada. Se paga
// una vez por contenido (no por ítem, no por mensaje) y convierte «el normalizador
// tiene que preservar la ñ» de comentario en guarda.

// casoNormalizador es una exigencia del contrato: qué entra, qué tiene que salir y
// qué propiedad se está protegiendo (el porqué va al mensaje de error, para que
// quien lo vea no tenga que abrir este fichero).
type casoNormalizador struct {
	entrada  string
	esperado string
	porque   string
}

// contratoNormalizador es EL CONTRATO, caso a caso. Todos están derivados de lo que
// hace `textmatch.Normalize` leyendo su implementación: minúsculas, plegado de
// diacríticos latinos por tabla, recomposición de la «ñ» descompuesta ANTES del
// barrido de marcas combinantes, y colapso de espacios con trim vía
// `strings.Fields`.
//
// ⚠️ Lo que el contrato NO exige, a propósito, porque `Normalize` tampoco lo hace:
// quitar la puntuación. «torta, de chocolate» se normaliza con su coma. Quien parte
// por puntuación es `textmatch.SplitTokens`, que es otra función y otro trabajo.
var contratoNormalizador = []casoNormalizador{
	{"Café", "cafe", "tiene que plegar los diacríticos latinos: sin esto, «Café» no casa «cafe»"},
	{"PIÑA COLADA", "piña colada", "tiene que pasar a minúsculas PRESERVANDO la ñ"},
	{"Jalapeño", "jalapeño", "🔴 la ñ es una LETRA, no una n con tilde: plegarla colapsa «año» con «ano»"},
	{"An\u0303o Nuevo", "a\u00f1o nuevo", "tiene que recomponer la ñ DESCOMPUESTA (n + U+0303) ANTES de barrer las marcas combinantes: si la barre primero, queda «ano»"},
	{"  Torta   de   Chocolate  ", "torta de chocolate", "tiene que colapsar los espacios internos y hacer trim"},
	{"", "", "la cadena vacía se normaliza a la cadena vacía, no a un espacio"},
}

// VerificarNormalizador comprueba que una función cumple el contrato que el índice
// necesita. Devuelve nil o un error que dice QUÉ caso falló, qué esperaba y qué
// propiedad protegía.
//
// Además de los casos de la tabla exige IDEMPOTENCIA (`n(n(x)) == n(x)`): un
// normalizador que no lo sea haría que el texto del catálogo —normalizado UNA vez
// al indexar— y el texto de la consulta —normalizado en cada búsqueda— dejaran de
// coincidir en el segundo pase, y el índice fallaría solo para algunas entradas.
//
// Se exporta para que el día que se cablee `textmatch.Normalize` baste un test de
// una línea (`require.NoError(t, catalogo.VerificarNormalizador(textmatch.Normalize))`)
// para saber si las dos piezas siguen hablando el mismo idioma.
func VerificarNormalizador(n Normalizador) error {
	if n == nil {
		return ErrSinNormalizador
	}
	for _, c := range contratoNormalizador {
		got := n(c.entrada)
		if got != c.esperado {
			return fmt.Errorf("%w: con %q devolvió %q y el contrato exige %q — %s",
				ErrNormalizadorInvalido, c.entrada, got, c.esperado, c.porque)
		}
		if dos := n(got); dos != got {
			return fmt.Errorf("%w: no es idempotente — %q normaliza a %q y eso a %q; el catálogo se normaliza una vez y la consulta otra, así que la segunda pasada tiene que ser un no-op",
				ErrNormalizadorInvalido, c.entrada, got, dos)
		}
	}
	// Una comprobación que la tabla no puede dar: que no colapse dos textos que el
	// español distingue. Es el invariante de la ñ dicho al revés, y caza a un
	// normalizador que pase los casos de arriba por casualidad (p. ej. uno con una
	// tabla de excepciones en vez de la regla).
	if n("a\u00f1o") == n("ano") {
		return fmt.Errorf("%w: colapsa «año» con «ano» — la ñ tiene que sobrevivir a la normalización",
			ErrNormalizadorInvalido)
	}
	return nil
}
