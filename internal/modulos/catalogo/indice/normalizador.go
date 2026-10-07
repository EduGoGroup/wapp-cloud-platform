// Porta internal/intake/catalogo/normalizador.go @ 3c74b80

package indice

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// normalizador.go — LA FRONTERA CON `wapp-shared/textmatch`, Y EL CONTRATO QUE
// EXIGE (Plan 044 · Ola 3 · T3.7 ↔ T3.1).
//
// # 🔴 POR QUÉ HAY UN PUERTO Y NO UN IMPORT
//
// El normalizador canónico es `textmatch.Normalize`, y es el que usa la etapa
// `match`. El índice NO lo importa ni lo copia: DECLARA la forma (Normalizador) y
// la RECIBE de quien lo cablea, que le pasa exactamente la misma función que al
// match. Copiar aquí el plegado de diacríticos dejaría dos normalizadores que
// divergen el día que alguien añada una letra a uno de los dos, con un síntoma
// —el match falla solo con ciertas palabras acentuadas— que nadie ata a la causa.
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

// VerificarNormalizador comprueba que una función cumple el contrato que el índice
// necesita. Devuelve nil —y `textmatch.Normalize` lo cumple— o un error que dice
// QUÉ caso falló, qué esperaba y qué propiedad protegía.
//
// Con n == nil devuelve ErrSinNormalizador, tal cual. En el resto de fallos, un
// error que envuelve ErrNormalizadorInvalido.
//
// EL CONTRATO, caso a caso y en este orden (lo que hace `textmatch.Normalize`:
// minúsculas, plegado de diacríticos latinos, recomposición de la «ñ» descompuesta
// ANTES del barrido de marcas combinantes, y colapso de espacios con trim):
//
//	"Café"                        → "cafe"                pliega los diacríticos latinos
//	"PIÑA COLADA"                 → "piña colada"         minúsculas PRESERVANDO la ñ
//	"Jalapeño"                    → "jalapeño"            la ñ es una LETRA, no una n con tilde
//	"An\u0303o Nuevo"             → "año nuevo"      recompone la ñ descompuesta (n + U+0303)
//	"  Torta   de   Chocolate  "  → "torta de chocolate"  colapsa espacios internos y hace trim
//	""                            → ""                    la vacía se queda vacía, no un espacio
//
// Un caso que no da lo esperado falla con el texto
// `<ErrNormalizadorInvalido>: con "<entrada>" devolvió "<obtenido>" y el contrato exige "<esperado>" — <porqué>`.
//
// ⚠️ Lo que el contrato NO exige, a propósito, porque `Normalize` tampoco lo hace:
// quitar la puntuación. «torta, de chocolate» se normaliza con su coma. Quien parte
// por puntuación es `textmatch.SplitTokens`, que es otra función y otro trabajo.
//
// Además de los casos de la tabla exige IDEMPOTENCIA sobre cada uno de ellos
// (`n(n(x)) == n(x)`), y el error lo dice con las palabras «no es idempotente»: un
// normalizador que no lo sea haría que el texto del catálogo —normalizado UNA vez
// al indexar— y el texto de la consulta —normalizado en cada búsqueda— dejaran de
// coincidir en el segundo pase, y el índice fallaría solo para algunas entradas.
//
// Y una comprobación que la tabla no puede dar: que no colapse dos textos que el
// español distingue. Si `n("año") == n("ano")` falla con «colapsa «año» con «ano»»:
// es el invariante de la ñ dicho al revés, y caza a un normalizador que pase los
// casos de arriba por casualidad (p. ej. uno con una tabla de excepciones en vez
// de la regla).
//
// Se exporta para que quien cablee `textmatch.Normalize` pueda saber con un test
// de una línea si las dos piezas siguen hablando el mismo idioma.
func VerificarNormalizador(n Normalizador) error {
	panic(pendiente.Implementar("indice.VerificarNormalizador"))
}
