package stages

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/evidence"
)

// ════════════════════════════════════════════════════════════════════════════
// 🔴 LA ARITMÉTICA DE FECHAS ES DE GO, Y EL MODELO SOLO ETIQUETA LA EXPRESIÓN
// ════════════════════════════════════════════════════════════════════════════
//
// El reparto es el de T2.4, y no es un gusto: el LLM dice DÓNDE está la expresión de
// entrega —eso lo hace P2, que devuelve `delivery_hint.text` en las palabras del
// cliente— y este fichero la convierte en una fecha absoluta. Ni una suma de días la
// hace el modelo.
//
// Los dos motivos, los dos medibles:
//
//   - **Determinismo.** Un modelo chico a temperatura 0 sigue siendo un modelo: la
//     misma expresión puede darte dos fechas en dos llamadas. Un `AddDate` no.
//   - **Reanudación.** El job se reanuda horas o días después (design §3.2). Si la
//     fecha saliera del reloj del worker, el mismo pedido daría fechas distintas según
//     cuándo lo recogiera la cola. Aquí la base es `intake_jobs.message_ts` y nada más,
//     así que reanudar no mueve la fecha. Lo custodia
//     TestP4_JobReanudadoDosDiasDespues_NoCambiaLaFecha y, de forma estructural,
//     TestStages_NoLeenElReloj.
//
// # POR QUÉ NO SE USA `delivery_date` DE LA SALIDA DEL MODELO
//
// Porque el prompt SÍ se la pide (`BuildNormalizeQuantitiesPrompt` le manda resolver
// las expresiones relativas contra la fecha de referencia) y el parser compartido la
// decodifica —su propio docstring lo dice: «este campo se decodifica, no se da por
// bueno»—. P4 la lee para COMPARARLA con la suya y dejar un aviso cuando no coinciden,
// y persiste siempre la de Go.
//
// # LO QUE ESTE FICHERO NO NORMALIZA IGUAL QUE `internal/evidence`, Y POR QUÉ
//
// La regla de la evidencia NO pliega acentos, y está razonado allí: una evidencia es
// una COPIA LITERAL, y quien escribe «cafe» donde el cliente escribió «café» no está
// copiando. Aquí es al revés: esto no compara con el original, INTERPRETA una expresión
// escrita a mano por un cliente en WhatsApp, donde «miercoles» sin tilde es lo normal.
// Así que se reusa `evidence.Normalize` (minúsculas + blancos) —la regla no se duplica—
// y encima se pliegan las tildes, que es de este fichero y de nadie más.
// ════════════════════════════════════════════════════════════════════════════

// ResolverFecha convierte la expresión de entrega que el cliente escribió en una fecha
// absoluta, calculada contra `base` — que es `intake_jobs.message_ts` YA en la zona
// horaria que gobierna (ver el bloque de la zona en p4.go).
//
// Devuelve `(fecha, true)` solo cuando la expresión se reconoce SIN AMBIGÜEDAD. Un
// «cuando puedas», un «la semana que viene» sin día o cualquier cosa que no esté en la
// tabla devuelven `false`: el presupuesto sale sin fecha y el dueño la pregunta, que es
// estrictamente mejor que fabricar un día.
//
// La fecha vuelve a medianoche en la zona de `base`. Toda la aritmética intermedia se
// hace sobre un ancla de MEDIODÍA para que un cambio de horario de verano no mueva el
// día: sumar 24 h a una medianoche en una zona que ese día adelanta el reloj cae en el
// día anterior, y sumar días sobre el mediodía no puede.
func ResolverFecha(expr string, base time.Time) (time.Time, bool) {
	e := plegarAcentos(evidence.Normalize(expr))
	if e == "" {
		return time.Time{}, false
	}
	ancla := anclaDe(base)
	for _, regla := range reglasDeFecha {
		if f, ok := regla(e, ancla); ok {
			return soloFecha(f), true
		}
	}
	return time.Time{}, false
}

// reglasDeFecha son las reglas EN ORDEN, de la más específica a la más general. El
// orden es parte del contrato y no es decorativo:
//
//   - las fechas explícitas van primero porque «el miércoles 22 de julio» trae las dos
//     cosas y la explícita es la que el cliente escribió con todas las letras;
//   - «<día> de la semana que viene» va antes que «<día>» a secas, porque el segundo
//     patrón está CONTENIDO en el primero y resolvería a la semana equivocada;
//   - «pasado mañana» va antes que «mañana», por lo mismo (dentro de porDiaSuelto).
var reglasDeFecha = []func(expr string, ancla time.Time) (time.Time, bool){
	porFechaConMesEnLetra,
	porFechaNumerica,
	porSemanaQueViene,
	porDiaSuelto,
	porCantidad,
	porDiaDeLaSemana,
}

// ---------------------------------------------------------------------------
// Las reglas
// ---------------------------------------------------------------------------

// reFechaConMes casa «22 de julio» y «22 de julio de 2026». `setiembre` está a
// propósito: es la grafía corriente en el Cono Sur.
var reFechaConMes = regexp.MustCompile(`\b(\d{1,2}) de (enero|febrero|marzo|abril|mayo|junio|julio|agosto|septiembre|setiembre|octubre|noviembre|diciembre)(?: de (\d{4}))?\b`)

// mesesES traduce el nombre del mes a su número.
var mesesES = map[string]time.Month{
	"enero": time.January, "febrero": time.February, "marzo": time.March,
	"abril": time.April, "mayo": time.May, "junio": time.June,
	"julio": time.July, "agosto": time.August, "septiembre": time.September,
	"setiembre": time.September, "octubre": time.October,
	"noviembre": time.November, "diciembre": time.December,
}

// porFechaConMesEnLetra resuelve «el 22 de julio» y «el 5 de enero de 2027».
//
// 🔴 EL AÑO OMITIDO ES EL CRUCE DE AÑO. Sin año, se prueba el del mensaje y, si esa
// fecha ya PASÓ respecto del mensaje, se pasa al siguiente: un «el 5 de enero» escrito
// el 28 de diciembre de 2026 es el 2027-01-05 y jamás un 2026-01-05, que estaría once
// meses en el pasado. Se compara contra el día del mensaje y no contra hoy, por lo
// mismo que todo lo demás de este fichero.
func porFechaConMesEnLetra(expr string, ancla time.Time) (time.Time, bool) {
	m := reFechaConMes.FindStringSubmatch(expr)
	if m == nil {
		return time.Time{}, false
	}
	dia, ok := numeroDe(m[1])
	if !ok {
		return time.Time{}, false
	}
	mes := mesesES[m[2]]
	if m[3] != "" {
		anio, ok := numeroDe(m[3])
		if !ok {
			return time.Time{}, false
		}
		return fechaValida(anio, mes, dia, ancla.Location())
	}
	f, existe := fechaValida(ancla.Year(), mes, dia, ancla.Location())
	if !existe {
		return time.Time{}, false
	}
	if f.Before(soloFecha(ancla)) {
		return fechaValida(ancla.Year()+1, mes, dia, ancla.Location())
	}
	return f, true
}

// reFechaNumerica casa «22/7», «22-07» y «22/07/2026».
var reFechaNumerica = regexp.MustCompile(`\b(\d{1,2})[/-](\d{1,2})(?:[/-](\d{2,4}))?\b`)

// porFechaNumerica resuelve la fecha escrita con barras.
//
// 🔴 EL ORDEN ES DÍA/MES, no mes/día: el producto habla español y escribe 22/07. Un
// «07/22» no es una fecha en esa convención y se rechaza (mes 22 no existe), que es
// justo lo que tiene que pasar: mejor sin fecha que con el día y el mes cambiados.
func porFechaNumerica(expr string, ancla time.Time) (time.Time, bool) {
	m := reFechaNumerica.FindStringSubmatch(expr)
	if m == nil {
		return time.Time{}, false
	}
	dia, okDia := numeroDe(m[1])
	mes, okMes := numeroDe(m[2])
	if !okDia || !okMes || mes < 1 || mes > 12 {
		return time.Time{}, false
	}
	if m[3] == "" {
		return porFechaConMesEnLetra(strconv.Itoa(dia)+" de "+nombreDeMes(time.Month(mes)), ancla)
	}
	anio, ok := numeroDe(m[3])
	if !ok {
		return time.Time{}, false
	}
	if anio < 100 {
		anio += 2000
	}
	return fechaValida(anio, time.Month(mes), dia, ancla.Location())
}

// marcasDeSemanaQueViene son las formas de decir «la semana siguiente a la del
// mensaje». Todas apuntan al mismo lunes, y ninguna de ellas es ambigua.
var marcasDeSemanaQueViene = []string{
	"semana que viene", "proxima semana", "semana proxima",
	"semana entrante", "semana siguiente",
}

// porSemanaQueViene resuelve «el miércoles de la semana que viene», que es el ejemplo
// que el plan pone y el del caso Ambar: mensaje del lunes 2026-07-13 ⇒ 2026-07-22.
//
// La semana empieza en LUNES (ISO), no en domingo como el `time.Weekday` de Go. Importa
// de verdad en el caso que más se da en un negocio de WhatsApp: un mensaje escrito un
// domingo. Con la semana ISO, el domingo pertenece a la semana que se acaba y «la
// semana que viene» es la de mañana; con la semana en domingo, el mismo mensaje se iría
// siete días más lejos.
//
// «La semana que viene» SIN día no devuelve nada: no es una fecha, es un rango de
// siete. Elegir uno sería inventarlo.
func porSemanaQueViene(expr string, ancla time.Time) (time.Time, bool) {
	if !contieneAlguna(expr, marcasDeSemanaQueViene) {
		return time.Time{}, false
	}
	dia, ok := diaDeLaSemanaEn(expr)
	if !ok {
		return time.Time{}, false
	}
	lunes := ancla.AddDate(0, 0, -offsetDesdeLunes(ancla.Weekday())+7)
	return lunes.AddDate(0, 0, offsetDesdeLunes(dia)), true
}

// porDiaSuelto resuelve «hoy», «mañana» y «pasado mañana».
func porDiaSuelto(expr string, ancla time.Time) (time.Time, bool) {
	switch {
	case strings.Contains(expr, "pasado mañana"):
		return ancla.AddDate(0, 0, 2), true
	case strings.Contains(expr, "mañana"):
		return ancla.AddDate(0, 0, 1), true
	case strings.Contains(expr, "hoy"):
		return ancla, true
	}
	return time.Time{}, false
}

// reCantidad casa «en 5 dias», «en una semana», «en dos semanas».
var reCantidad = regexp.MustCompile(`\ben (\d{1,3}|un|una|dos|tres|cuatro|cinco|seis|siete|ocho|nueve|diez|quince) (dias?|semanas?)\b`)

// numerosES traduce los cardinales que un cliente escribe con letra.
var numerosES = map[string]int{
	"un": 1, "una": 1, "dos": 2, "tres": 3, "cuatro": 4, "cinco": 5,
	"seis": 6, "siete": 7, "ocho": 8, "nueve": 9, "diez": 10, "quince": 15,
}

// porCantidad resuelve «en 5 días» y «en dos semanas» sumando sobre el día del mensaje.
func porCantidad(expr string, ancla time.Time) (time.Time, bool) {
	m := reCantidad.FindStringSubmatch(expr)
	if m == nil {
		return time.Time{}, false
	}
	n, enLetra := numerosES[m[1]]
	if !enLetra {
		var ok bool
		if n, ok = numeroDe(m[1]); !ok {
			return time.Time{}, false
		}
	}
	if n < 1 {
		return time.Time{}, false
	}
	if strings.HasPrefix(m[2], "semana") {
		n *= 7
	}
	return ancla.AddDate(0, 0, n), true
}

// porDiaDeLaSemana resuelve el día a secas: «el miércoles», «el miércoles que viene»,
// «el próximo miércoles».
//
// 🔴 DECISIÓN CON AMBIGÜEDAD REAL, ESCRITA AQUÍ PARA QUE NADIE LA HEREDE SIN VERLA.
// «El miércoles que viene» significa cosas distintas según quién lo diga: en buena
// parte de América es el miércoles que llega, y en España suele ser el de la semana
// siguiente. Aquí se resuelve SIEMPRE como la PRÓXIMA APARICIÓN ESTRICTA del día
// —nunca el mismo día del mensaje—, y el motivo es de seguridad, no de gramática: de
// las dos lecturas, la temprana falla ANTES de la fecha real y deja margen para
// corregir; la tardía puede pasarse del día del cliente sin que nadie se entere. La
// forma inequívoca —«de la semana que viene»— la resuelve porSemanaQueViene, que va
// antes en la tabla.
//
// Que sea ESTRICTA (nunca el mismo día) también es deliberado: «el miércoles», dicho un
// miércoles, no es «hoy» —para eso está «hoy»— y prometer para hoy un pedido que se
// pidió para dentro de una semana es el peor de los dos errores.
func porDiaDeLaSemana(expr string, ancla time.Time) (time.Time, bool) {
	dia, ok := diaDeLaSemanaEn(expr)
	if !ok {
		return time.Time{}, false
	}
	delta := (int(dia) - int(ancla.Weekday()) + 7) % 7
	if delta == 0 {
		delta = 7
	}
	return ancla.AddDate(0, 0, delta), true
}

// ---------------------------------------------------------------------------
// Piezas
// ---------------------------------------------------------------------------

// diasES traduce el nombre del día —ya sin tildes— al time.Weekday de Go.
var diasES = map[string]time.Weekday{
	"lunes": time.Monday, "martes": time.Tuesday, "miercoles": time.Wednesday,
	"jueves": time.Thursday, "viernes": time.Friday, "sabado": time.Saturday,
	"domingo": time.Sunday,
}

// ordenDeBusquedaDeDias fija un recorrido ESTABLE del mapa: un `range` sobre un mapa de
// Go va en orden aleatorio, y una expresión con dos días («lunes o martes») daría una
// fecha distinta en cada ejecución. Con la lista, gana siempre el que aparece antes en
// el texto.
var ordenDeBusquedaDeDias = []string{"lunes", "martes", "miercoles", "jueves", "viernes", "sabado", "domingo"}

// diaDeLaSemanaEn busca un día de la semana en la expresión. Si hay más de uno, gana el
// que aparece ANTES en el texto.
func diaDeLaSemanaEn(expr string) (time.Weekday, bool) {
	mejor := -1
	var dia time.Weekday
	for _, nombre := range ordenDeBusquedaDeDias {
		i := strings.Index(expr, nombre)
		if i < 0 {
			continue
		}
		if mejor < 0 || i < mejor {
			mejor, dia = i, diasES[nombre]
		}
	}
	return dia, mejor >= 0
}

// numeroDe convierte un grupo capturado por las expresiones regulares de este fichero.
//
// El error se comprueba de verdad —`errcheck` aquí lleva `check-blank`, así que un `_`
// no exime— aunque las capturas sean `\d{1,4}` y no puedan fallar hoy: si algún día una
// de esas expresiones se ensancha, un número que no cabe en un int tiene que salir por
// «sin fecha», que es la respuesta segura, y jamás por un cero silencioso.
func numeroDe(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// offsetDesdeLunes son los días que han pasado desde el lunes de ESA semana. El
// `+6 % 7` convierte la semana de Go (domingo = 0) en la semana ISO (lunes = 0).
func offsetDesdeLunes(d time.Weekday) int {
	return (int(d) + 6) % 7
}

// fechaValida construye la fecha y comprueba que EXISTE. `time.Date` normaliza hacia
// adelante —un 31 de febrero se convierte en el 3 de marzo sin decir nada—, así que se
// verifica que los tres componentes salieron como entraron. Un «31/02» no es una fecha
// y el presupuesto se queda sin ella, que es lo correcto.
func fechaValida(anio int, mes time.Month, dia int, loc *time.Location) (time.Time, bool) {
	f := time.Date(anio, mes, dia, 12, 0, 0, 0, loc)
	if f.Year() != anio || f.Month() != mes || f.Day() != dia {
		return time.Time{}, false
	}
	return f, true
}

// nombreDeMes es la inversa de mesesES para las formas numéricas, que se resuelven
// reusando la regla de la fecha con mes en letra en vez de repetir su lógica de año
// omitido (que es la del cruce de año, y no puede haber dos).
func nombreDeMes(m time.Month) string {
	for nombre, mes := range mesesES {
		if mes == m && nombre != "setiembre" {
			return nombre
		}
	}
	return ""
}

// anclaDe lleva un instante al MEDIODÍA de su día, en su zona. Es el ancla de toda la
// aritmética: sumar días sobre el mediodía no puede caerse en el agujero que deja un
// cambio de horario de verano.
func anclaDe(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 12, 0, 0, 0, t.Location())
}

// soloFecha deja el instante a medianoche: es la fecha civil, que es lo que se
// persiste en formato AAAA-MM-DD.
func soloFecha(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// contieneAlguna dice si el texto trae alguna de las marcas.
func contieneAlguna(texto string, marcas []string) bool {
	for _, m := range marcas {
		if strings.Contains(texto, m) {
			return true
		}
	}
	return false
}

// plegadoDeAcentos quita las tildes de las vocales. La `ñ` NO se toca: «mañana» se
// escribe con ñ siempre y plegarla no ganaría nada.
var plegadoDeAcentos = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u")

// plegarAcentos aplica el plegado. La entrada llega YA en minúsculas desde
// evidence.Normalize, así que no hace falta cubrir las mayúsculas acentuadas.
func plegarAcentos(s string) string {
	return plegadoDeAcentos.Replace(s)
}
