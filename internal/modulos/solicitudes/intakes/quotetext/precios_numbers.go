// Porta internal/intakes/quotetext/precios.go @ 36d5a04

package quotetext

// precios_numbers.go — parte de precios.go (E-13): cómo LEE el verificador los
// números, del texto del modelo y del propio borrador, y cómo los compara. No tiene
// exportados: lo que promete se ve por Verify y ExpectedSequence.

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// amountTolerance es cuánto pueden diferir dos importes para considerarse el mismo:
// medio céntimo.
//
// Existe porque `UnitPrice` es float64 y no centavos (así está en `intakes.Item` y en
// la columna), y `qty × unit_price` en binario no da exactamente lo que da en decimal:
// 3 × 0,1 es 0,30000000000000004. Comparar con `==` haría que un texto correcto se
// rechazara por el último bit de la mantisa.
//
// Era `toleranciaImporte` en el paquete viejo.
const amountTolerance = 0.005

// errUnreadableNumber es la familia de «este número no se puede leer».
//
// Era `errNumeroIlegible` en el paquete viejo.
var errUnreadableNumber = errors.New("quotetext: número ilegible")

// numberPattern encuentra un número del texto junto con su marca de dinero, si la
// lleva.
//
// Los tres grupos son, en orden: el símbolo de moneda pegado por delante, el número
// —dígitos con puntos y comas dentro—, y la palabra de moneda pegada por detrás.
//
// La clase del número NO incluye el guion, y eso es lo que hace que «10-12 porciones»
// dé dos números y no uno raro.
//
// Era `reNumero` en el paquete viejo.
var numberPattern = regexp.MustCompile(`(?i)(\$)?\s?(\d[\d.,]*)\s?(pesos?|clp|usd|bs|soles?|d[oó]lar(?:es)?)?`)

// textNumber es UN número encontrado, ya leído y clasificado.
//
// Era `numeroDelTexto` en el paquete viejo.
type textNumber struct {
	value float64
	// marked dice si venía con marca de dinero. Es la clase de C3 frente a C4.
	marked bool
}

// draftNumbers saca los números que ya viven en el borrador FUERA de los importes:
// las cantidades y los que van dentro de las etiquetas y las personalizaciones
// («10-12 porciones», «paquete x30»).
//
// Son los que C4 perdona por encima del techo, y el motivo es directo: el prompt le
// pide al modelo que copie el borrador, así que un número que ya estaba ahí no lo
// inventó él. Los que no se pueden leer se descartan en silencio —no aportan permiso—,
// que es el lado conservador.
//
// 🔴 El SKU no viaja en el borrador (ver Line), así que un código de catálogo con
// dígitos nunca llega a esta lista: no puede colar un permiso.
//
// Era `numerosDelBorrador` en el paquete viejo.
func draftNumbers(draft Draft) []float64 {
	out := make([]float64, 0, 2*len(draft.Lines))
	for _, line := range draft.Lines {
		if !containsAmount(out, float64(line.Qty)) {
			out = append(out, float64(line.Qty))
		}
		for _, match := range numberPattern.FindAllStringSubmatch(line.Label+" "+line.Customization, -1) {
			value, err := parseNumber(match[2])
			if err != nil || containsAmount(out, value) {
				continue
			}
			out = append(out, value)
		}
	}
	return out
}

// extractNumbers recorre el texto y devuelve cada número con su clase. Un número que
// no se puede leer NO se salta: aborta la verificación entera con error de dato.
//
// 🔴 ESO ES A PROPÓSITO Y ES LA LECCIÓN CARA DEL PLAN 044: lo que viene del modelo se
// valida en Go ANTES de convertirlo o de indexar con ello. Saltarse el número raro
// dejaría pasar un texto del que no se puede afirmar nada.
//
// Era `extraerNumeros` en el paquete viejo.
func extractNumbers(text string) ([]textNumber, error) {
	matches := numberPattern.FindAllStringSubmatch(text, -1)
	out := make([]textNumber, 0, len(matches))
	for _, match := range matches {
		value, err := parseNumber(match[2])
		if err != nil {
			return nil, err
		}
		out = append(out, textNumber{value: value, marked: match[1] != "" || match[3] != ""})
	}
	return out, nil
}

// parseNumber lee un número del texto resolviendo los separadores, y lo devuelve YA
// REDONDEADO A CÉNTIMOS. Es la contraparte de Amount.
//
// # LA REGLA DE LOS SEPARADORES, QUE ES LO ÚNICO INTERESANTE DE ESTA FUNCIÓN
//
//   - Con punto Y coma, el ÚLTIMO de los dos manda como decimal y el otro es de miles
//     («2.950,00» ⇒ 2950; «2,950.00» ⇒ 2950).
//   - Con uno solo: es DECIMAL si aparece una vez y le siguen exactamente uno o dos
//     dígitos («2100,50» ⇒ 2100,5); en cualquier otro caso es de MILES y se borra
//     («2.100» ⇒ 2100, «1.234.567» ⇒ 1234567, «2100.» ⇒ 2100, y también un separador
//     repetido: «2..100» ⇒ 2100).
//
// La regla se equivoca con «$2.10» escrito por un modelo que quería decir 2100. NO SE
// ARREGLA AQUÍ y no hace falta: el valor que salga (2,10) no estará en ESPERADOS y el
// texto se rechazará. Ver «conservador ante la duda» en la cabecera de precios.go.
//
// Devuelve error —nunca un cero silencioso y nunca un pánico— cuando el literal
// desborda el float64 o no es un número.
//
// Era `aNumero` en el paquete viejo.
func parseNumber(literal string) (float64, error) {
	// 🔴 LA PUNTUACIÓN DE LA FRASE SE RECORTA PRIMERO, y no es cosmética: un «$1234,50.»
	// al final de una oración deja el punto DENTRO del literal, y entonces la regla de
	// abajo ve punto Y coma, elige el punto como decimal y devuelve 123450 — cien veces
	// el importe real, y con pinta de importe inventado. Un número no acaba nunca en
	// separador, así que recortarlos por la derecha no puede perder información.
	s := strings.TrimRight(strings.TrimSpace(literal), ".,")
	lastDot, lastComma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	switch {
	case lastDot >= 0 && lastComma >= 0:
		decimal := "."
		if lastComma > lastDot {
			decimal = ","
		}
		s = strings.ReplaceAll(s, thousandsSeparator(decimal), "")
		s = strings.Replace(s, decimal, ".", 1)
	case lastDot >= 0 || lastComma >= 0:
		separator := "."
		if lastComma >= 0 {
			separator = ","
		}
		if isDecimal(s, separator) {
			s = strings.Replace(s, separator, ".", 1)
		} else {
			s = strings.ReplaceAll(s, separator, "")
		}
	}
	value, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %d caracteres, separadores no interpretables", errUnreadableNumber, len(s))
	}
	// El redondeo va ANTES de la comprobación de finitud a propósito: multiplicar por
	// 100 un float que ya rozaba el máximo lo desborda, y ese desbordamiento tiene que
	// salir como error de dato igual que el del parseo.
	value = toCents(value)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("%w: el valor no es finito", errUnreadableNumber)
	}
	return value, nil
}

// toCents redondea a la precisión con la que se escribe el dinero, que es la misma
// que usa Amount. Ver «la precisión es la del dinero» en la cabecera de precios.go.
//
// Era `aCentimos` en el paquete viejo.
func toCents(value float64) float64 { return math.Round(value*100) / 100 }

// thousandsSeparator devuelve el separador de miles dado el decimal.
//
// Era `mil` en el paquete viejo.
func thousandsSeparator(decimal string) string {
	if decimal == "." {
		return ","
	}
	return "."
}

// isDecimal dice si `separator` aparece UNA vez en `s` y le siguen uno o dos dígitos:
// la forma de un decimal y no la de un separador de miles.
//
// Era `esDecimal` en el paquete viejo.
func isDecimal(s, separator string) bool {
	if strings.Count(s, separator) != 1 {
		return false
	}
	tail := s[strings.Index(s, separator)+1:]
	if len(tail) == 0 || len(tail) > 2 {
		return false
	}
	for _, r := range tail {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// containsAmount dice si `value` está en `values` con la tolerancia de importe.
//
// Era `contiene` en el paquete viejo.
func containsAmount(values []float64, value float64) bool {
	for _, candidate := range values {
		if sameAmount(candidate, value) {
			return true
		}
	}
	return false
}

// sameAmount compara dos montos con amountTolerance. Ver allí por qué no es `==`.
//
// Era `mismoImporte` en el paquete viejo.
func sameAmount(a, b float64) bool { return math.Abs(a-b) < amountTolerance }

// maxOf es el mayor de un conjunto NO VACÍO. El llamante ya cortó con el vacío
// (ReasonDraftWithoutAmounts), y por eso aquí no hay un cero de cortesía que después
// alguien interpretaría como un techo de verdad.
//
// Era `maximo` en el paquete viejo.
func maxOf(values []float64) float64 {
	highest := values[0]
	for _, value := range values[1:] {
		if value > highest {
			highest = value
		}
	}
	return highest
}
