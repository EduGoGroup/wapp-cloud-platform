// Porta internal/intake/stages/fechas.go @ 4cd9cfb

package stages

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/evidence"
)

// dates.go — LA ARITMÉTICA DE FECHAS ES DE GO, Y EL MODELO SOLO ETIQUETA LA EXPRESIÓN.
//
// El LLM dice DÓNDE está la expresión de entrega —P2 devuelve `delivery_hint.text` en
// las palabras del cliente— y este fichero la convierte en una fecha absoluta. Los dos
// motivos son medibles: determinismo (un `AddDate` no da dos fechas en dos llamadas) y
// reanudación (la base es `intake_jobs.message_ts` y nada más, así que reanudar el job
// días después no mueve la fecha).

// ResolveDate (antes `ResolverFecha`) convierte la expresión de entrega que el cliente
// escribió en una fecha absoluta, calculada contra `base` —`intake_jobs.message_ts` YA
// en la zona horaria que gobierna—. Es una función PURA de sus dos argumentos: no lee
// el reloj.
//
// Devuelve `(fecha, true)` solo cuando la expresión se reconoce SIN AMBIGÜEDAD, y la
// fecha vuelve a MEDIANOCHE en la zona de `base`. Lo que no se reconoce devuelve
// `(time.Time{}, false)`: el presupuesto sale sin fecha y el dueño la pregunta, que es
// estrictamente mejor que fabricar un día.
//
// # LA NORMALIZACIÓN DE LA EXPRESIÓN
//
// `evidence.Normalize` (minúsculas, blancos de Unicode colapsados y recortados) y,
// encima, se pliegan las tildes de las vocales minúsculas (`á é í ó ú ü`): «miercoles»
// sin tilde es lo normal en WhatsApp. La `ñ` NO se pliega («manana» no es «mañana»), y
// nada más se toca: los dígitos que no son ASCII, los caracteres invisibles y los
// acentos en forma combinante se quedan como vienen, así que rompen el patrón en vez de
// interpretarse.
//
// # LAS REGLAS, EN ESTE ORDEN — gana la primera que resuelve
//
//  1. **Fecha con el mes en letra**: «22 de julio», «22 de julio de 2026» (`setiembre`
//     vale). Con el año escrito, manda ese año aunque ya haya pasado. Sin año, se
//     prueba el del mensaje y, si esa fecha es ANTERIOR al día del mensaje, se pasa al
//     siguiente (el cruce de año: «el 5 de enero» dicho el 28 de diciembre); el mismo
//     día del mensaje no cruza.
//  2. **Fecha numérica**, en la convención DÍA/MES: «22/7», «22-07», «22/07/2026». Un
//     mes fuera de 1–12 no resuelve («07/22»). Sin año, sigue la regla del año
//     omitido de (1); un año menor que 100 se toma como 2000 más ese número.
//  3. **«<día> de la semana que viene»**, con cualquiera de las marcas «semana que
//     viene», «próxima semana», «semana próxima», «semana entrante», «semana
//     siguiente»: el día pedido de la semana ISO (de lunes a domingo) SIGUIENTE a la
//     del mensaje. Dicho un domingo, «el lunes de la semana que viene» es mañana.
//  4. **Día suelto**: «pasado mañana» (+2) antes que «mañana» (+1), y «hoy» (el día del
//     mensaje).
//  5. **Cantidad**: «en N días» / «en N semanas», con N de una a tres cifras o en letra
//     (`un`, `una`, `dos` … `diez`, `quince`); N menor que 1 no resuelve.
//  6. **Día de la semana a secas** («el miércoles», «el miércoles que viene», «el
//     próximo miércoles»): la PRÓXIMA APARICIÓN ESTRICTA del día, nunca el mismo día
//     del mensaje. De las dos lecturas de «el miércoles que viene» se elige la temprana
//     a propósito: falla ANTES de la fecha real y deja margen para corregir.
//
// El orden es contrato: la fecha explícita gana al día de la semana que la acompaña
// («el miércoles 22 de julio»), y (3) va antes que (6) porque el segundo patrón está
// contenido en el primero. Una regla que reconoce su patrón pero no puede dar fecha NO
// corta: se sigue probando con las siguientes.
//
// Cuando la expresión nombra varios días de la semana, cuenta el que aparece ANTES en
// el texto. Las marcas y los días se buscan como subcadenas, no como palabras.
//
// # LO QUE NO SE RESUELVE
//
// La expresión vacía; lo que no está en la tabla («cuando puedas»); «la semana que
// viene» SIN día, que es un rango de siete; y una fecha que NO EXISTE («31 de febrero»,
// el 29 de febrero de un año no bisiesto): no se normaliza al día siguiente.
//
// # LA ARITMÉTICA NO SE CAE EN UN CAMBIO DE HORA
//
// Los días se suman sobre el MEDIODÍA del día del mensaje y el resultado se deja a
// medianoche: un cambio de horario de verano no mueve el día.
func ResolveDate(expr string, base time.Time) (time.Time, bool) {
	e := foldAccents(evidence.Normalize(expr))
	if e == "" {
		return time.Time{}, false
	}
	anchor := anchorOf(base)
	for _, rule := range dateRules {
		if d, ok := rule(e, anchor); ok {
			return dateOnly(d), true
		}
	}
	return time.Time{}, false
}

// # POR QUÉ NO SE USA `delivery_date` DE LA SALIDA DEL MODELO
//
// Porque el prompt SÍ se la pide (`BuildNormalizeQuantitiesPrompt` le manda resolver
// las expresiones relativas contra la fecha de referencia) y el parser compartido la
// decodifica —su propio docstring lo dice: «este campo se decodifica, no se da por
// bueno»—. P4 la lee para COMPARARLA con la suya y dejar un aviso cuando no coinciden,
// y persiste siempre la de Go.
//
// # LO QUE ESTE FICHERO NO NORMALIZA IGUAL QUE `evidence`, Y POR QUÉ
//
// La regla de la evidencia NO pliega acentos, y está razonado allí: una evidencia es
// una COPIA LITERAL, y quien escribe «cafe» donde el cliente escribió «café» no está
// copiando. Aquí es al revés: esto no compara con el original, INTERPRETA una expresión
// escrita a mano por un cliente en WhatsApp, donde «miercoles» sin tilde es lo normal.
// Así que se reusa `evidence.Normalize` (minúsculas + blancos) —la regla no se duplica—
// y encima se pliegan las tildes, que es de este fichero y de nadie más.

// dateRules (antes `reglasDeFecha`) son las reglas EN ORDEN, de la más específica a la
// más general. El orden es parte del contrato y no es decorativo:
//
//   - las fechas explícitas van primero porque «el miércoles 22 de julio» trae las dos
//     cosas y la explícita es la que el cliente escribió con todas las letras;
//   - «<día> de la semana que viene» va antes que «<día>» a secas, porque el segundo
//     patrón está CONTENIDO en el primero y resolvería a la semana equivocada;
//   - «pasado mañana» va antes que «mañana», por lo mismo (dentro de byLooseDay).
var dateRules = []func(expr string, anchor time.Time) (time.Time, bool){
	byDateWithMonthName,
	byNumericDate,
	byNextWeek,
	byLooseDay,
	byAmount,
	byWeekday,
}

// ---------------------------------------------------------------------------
// Las reglas
// ---------------------------------------------------------------------------

// reDateWithMonth casa «22 de julio» y «22 de julio de 2026». `setiembre` está a
// propósito: es la grafía corriente en el Cono Sur.
var reDateWithMonth = regexp.MustCompile(`\b(\d{1,2}) de (enero|febrero|marzo|abril|mayo|junio|julio|agosto|septiembre|setiembre|octubre|noviembre|diciembre)(?: de (\d{4}))?\b`)

// monthsES traduce el nombre del mes a su número.
var monthsES = map[string]time.Month{
	"enero": time.January, "febrero": time.February, "marzo": time.March,
	"abril": time.April, "mayo": time.May, "junio": time.June,
	"julio": time.July, "agosto": time.August, "septiembre": time.September,
	"setiembre": time.September, "octubre": time.October,
	"noviembre": time.November, "diciembre": time.December,
}

// byDateWithMonthName resuelve «el 22 de julio» y «el 5 de enero de 2027».
//
// 🔴 EL AÑO OMITIDO ES EL CRUCE DE AÑO. Sin año, se prueba el del mensaje y, si esa
// fecha ya PASÓ respecto del mensaje, se pasa al siguiente: un «el 5 de enero» escrito
// el 28 de diciembre de 2026 es el 2027-01-05 y jamás un 2026-01-05, que estaría once
// meses en el pasado. Se compara contra el día del mensaje y no contra hoy, por lo
// mismo que todo lo demás de este fichero.
func byDateWithMonthName(expr string, anchor time.Time) (time.Time, bool) {
	m := reDateWithMonth.FindStringSubmatch(expr)
	if m == nil {
		return time.Time{}, false
	}
	day, ok := numberOf(m[1])
	if !ok {
		return time.Time{}, false
	}
	month := monthsES[m[2]]
	if m[3] != "" {
		year, ok := numberOf(m[3])
		if !ok {
			return time.Time{}, false
		}
		return validDate(year, month, day, anchor.Location())
	}
	d, exists := validDate(anchor.Year(), month, day, anchor.Location())
	if !exists {
		return time.Time{}, false
	}
	if d.Before(dateOnly(anchor)) {
		return validDate(anchor.Year()+1, month, day, anchor.Location())
	}
	return d, true
}

// reNumericDate casa «22/7», «22-07» y «22/07/2026».
var reNumericDate = regexp.MustCompile(`\b(\d{1,2})[/-](\d{1,2})(?:[/-](\d{2,4}))?\b`)

// byNumericDate resuelve la fecha escrita con barras.
//
// 🔴 EL ORDEN ES DÍA/MES, no mes/día: el producto habla español y escribe 22/07. Un
// «07/22» no es una fecha en esa convención y se rechaza (mes 22 no existe), que es
// justo lo que tiene que pasar: mejor sin fecha que con el día y el mes cambiados.
func byNumericDate(expr string, anchor time.Time) (time.Time, bool) {
	m := reNumericDate.FindStringSubmatch(expr)
	if m == nil {
		return time.Time{}, false
	}
	day, okDay := numberOf(m[1])
	month, okMonth := numberOf(m[2])
	if !okDay || !okMonth || month < 1 || month > 12 {
		return time.Time{}, false
	}
	if m[3] == "" {
		return byDateWithMonthName(strconv.Itoa(day)+" de "+monthName(time.Month(month)), anchor)
	}
	year, ok := numberOf(m[3])
	if !ok {
		return time.Time{}, false
	}
	if year < 100 {
		year += 2000
	}
	return validDate(year, time.Month(month), day, anchor.Location())
}

// nextWeekMarks son las formas de decir «la semana siguiente a la del mensaje». Todas
// apuntan al mismo lunes, y ninguna de ellas es ambigua.
var nextWeekMarks = []string{
	"semana que viene", "proxima semana", "semana proxima",
	"semana entrante", "semana siguiente",
}

// byNextWeek resuelve «el miércoles de la semana que viene», que es el ejemplo que el
// plan pone y el del caso Ambar: mensaje del lunes 2026-07-13 ⇒ 2026-07-22.
//
// La semana empieza en LUNES (ISO), no en domingo como el `time.Weekday` de Go. Importa
// de verdad en el caso que más se da en un negocio de WhatsApp: un mensaje escrito un
// domingo. Con la semana ISO, el domingo pertenece a la semana que se acaba y «la
// semana que viene» es la de mañana; con la semana en domingo, el mismo mensaje se iría
// siete días más lejos.
//
// «La semana que viene» SIN día no devuelve nada: no es una fecha, es un rango de
// siete. Elegir uno sería inventarlo.
func byNextWeek(expr string, anchor time.Time) (time.Time, bool) {
	if !containsAny(expr, nextWeekMarks) {
		return time.Time{}, false
	}
	weekday, ok := weekdayIn(expr)
	if !ok {
		return time.Time{}, false
	}
	monday := anchor.AddDate(0, 0, -offsetFromMonday(anchor.Weekday())+7)
	return monday.AddDate(0, 0, offsetFromMonday(weekday)), true
}

// byLooseDay resuelve «hoy», «mañana» y «pasado mañana».
func byLooseDay(expr string, anchor time.Time) (time.Time, bool) {
	switch {
	case strings.Contains(expr, "pasado mañana"):
		return anchor.AddDate(0, 0, 2), true
	case strings.Contains(expr, "mañana"):
		return anchor.AddDate(0, 0, 1), true
	case strings.Contains(expr, "hoy"):
		return anchor, true
	}
	return time.Time{}, false
}

// reAmount casa «en 5 dias», «en una semana», «en dos semanas».
var reAmount = regexp.MustCompile(`\ben (\d{1,3}|un|una|dos|tres|cuatro|cinco|seis|siete|ocho|nueve|diez|quince) (dias?|semanas?)\b`)

// numbersES traduce los cardinales que un cliente escribe con letra.
var numbersES = map[string]int{
	"un": 1, "una": 1, "dos": 2, "tres": 3, "cuatro": 4, "cinco": 5,
	"seis": 6, "siete": 7, "ocho": 8, "nueve": 9, "diez": 10, "quince": 15,
}

// byAmount resuelve «en 5 días» y «en dos semanas» sumando sobre el día del mensaje.
func byAmount(expr string, anchor time.Time) (time.Time, bool) {
	m := reAmount.FindStringSubmatch(expr)
	if m == nil {
		return time.Time{}, false
	}
	n, spelled := numbersES[m[1]]
	if !spelled {
		var ok bool
		if n, ok = numberOf(m[1]); !ok {
			return time.Time{}, false
		}
	}
	if n < 1 {
		return time.Time{}, false
	}
	if strings.HasPrefix(m[2], "semana") {
		n *= 7
	}
	return anchor.AddDate(0, 0, n), true
}

// byWeekday resuelve el día a secas: «el miércoles», «el miércoles que viene», «el
// próximo miércoles».
//
// 🔴 DECISIÓN CON AMBIGÜEDAD REAL, ESCRITA AQUÍ PARA QUE NADIE LA HEREDE SIN VERLA.
// «El miércoles que viene» significa cosas distintas según quién lo diga: en buena
// parte de América es el miércoles que llega, y en España suele ser el de la semana
// siguiente. Aquí se resuelve SIEMPRE como la PRÓXIMA APARICIÓN ESTRICTA del día
// —nunca el mismo día del mensaje—, y el motivo es de seguridad, no de gramática: de
// las dos lecturas, la temprana falla ANTES de la fecha real y deja margen para
// corregir; la tardía puede pasarse del día del cliente sin que nadie se entere. La
// forma inequívoca —«de la semana que viene»— la resuelve byNextWeek, que va antes en
// la tabla.
//
// Que sea ESTRICTA (nunca el mismo día) también es deliberado: «el miércoles», dicho un
// miércoles, no es «hoy» —para eso está «hoy»— y prometer para hoy un pedido que se
// pidió para dentro de una semana es el peor de los dos errores.
func byWeekday(expr string, anchor time.Time) (time.Time, bool) {
	weekday, ok := weekdayIn(expr)
	if !ok {
		return time.Time{}, false
	}
	delta := (int(weekday) - int(anchor.Weekday()) + 7) % 7
	if delta == 0 {
		delta = 7
	}
	return anchor.AddDate(0, 0, delta), true
}

// ---------------------------------------------------------------------------
// Piezas
// ---------------------------------------------------------------------------

// weekdaysES traduce el nombre del día —ya sin tildes— al time.Weekday de Go.
var weekdaysES = map[string]time.Weekday{
	"lunes": time.Monday, "martes": time.Tuesday, "miercoles": time.Wednesday,
	"jueves": time.Thursday, "viernes": time.Friday, "sabado": time.Saturday,
	"domingo": time.Sunday,
}

// weekdaySearchOrder fija un recorrido ESTABLE del mapa: un `range` sobre un mapa de Go
// va en orden aleatorio, y una expresión con dos días («lunes o martes») daría una
// fecha distinta en cada ejecución. Con la lista, gana siempre el que aparece antes en
// el texto.
var weekdaySearchOrder = []string{"lunes", "martes", "miercoles", "jueves", "viernes", "sabado", "domingo"}

// weekdayIn busca un día de la semana en la expresión. Si hay más de uno, gana el que
// aparece ANTES en el texto.
func weekdayIn(expr string) (time.Weekday, bool) {
	best := -1
	var weekday time.Weekday
	for _, name := range weekdaySearchOrder {
		i := strings.Index(expr, name)
		if i < 0 {
			continue
		}
		if best < 0 || i < best {
			best, weekday = i, weekdaysES[name]
		}
	}
	return weekday, best >= 0
}

// numberOf convierte un grupo capturado por las expresiones regulares de este fichero.
//
// El error se comprueba de verdad —`errcheck` aquí lleva `check-blank`, así que un `_`
// no exime— aunque las capturas sean `\d{1,4}` y no puedan fallar hoy: si algún día una
// de esas expresiones se ensancha, un número que no cabe en un int tiene que salir por
// «sin fecha», que es la respuesta segura, y jamás por un cero silencioso.
func numberOf(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// offsetFromMonday son los días que han pasado desde el lunes de ESA semana. El
// `+6 % 7` convierte la semana de Go (domingo = 0) en la semana ISO (lunes = 0).
func offsetFromMonday(d time.Weekday) int {
	return (int(d) + 6) % 7
}

// validDate construye la fecha y comprueba que EXISTE. `time.Date` normaliza hacia
// adelante —un 31 de febrero se convierte en el 3 de marzo sin decir nada—, así que se
// verifica que los tres componentes salieron como entraron. Un «31/02» no es una fecha
// y el presupuesto se queda sin ella, que es lo correcto.
func validDate(year int, month time.Month, day int, loc *time.Location) (time.Time, bool) {
	d := time.Date(year, month, day, 12, 0, 0, 0, loc)
	if d.Year() != year || d.Month() != month || d.Day() != day {
		return time.Time{}, false
	}
	return d, true
}

// monthName es la inversa de monthsES para las formas numéricas, que se resuelven
// reusando la regla de la fecha con mes en letra en vez de repetir su lógica de año
// omitido (que es la del cruce de año, y no puede haber dos).
func monthName(m time.Month) string {
	for name, month := range monthsES {
		if month == m && name != "setiembre" {
			return name
		}
	}
	return ""
}

// anchorOf lleva un instante al MEDIODÍA de su día, en su zona. Es el ancla de toda la
// aritmética: sumar días sobre el mediodía no puede caerse en el agujero que deja un
// cambio de horario de verano (sumar 24 h a una medianoche en una zona que ese día
// adelanta el reloj cae en el día anterior).
func anchorOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 12, 0, 0, 0, t.Location())
}

// dateOnly deja el instante a medianoche: es la fecha civil, que es lo que se persiste
// en formato AAAA-MM-DD.
func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// containsAny dice si el texto trae alguna de las marcas.
func containsAny(text string, marks []string) bool {
	for _, m := range marks {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// accentFolding quita las tildes de las vocales. La `ñ` NO se toca: «mañana» se escribe
// con ñ siempre y plegarla no ganaría nada.
var accentFolding = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u")

// foldAccents aplica el plegado. La entrada llega YA en minúsculas desde
// evidence.Normalize, así que no hace falta cubrir las mayúsculas acentuadas.
func foldAccents(s string) string {
	return accentFolding.Replace(s)
}
