// Porta internal/intake/stages/fechas.go @ 4cd9cfb

package stages

import (
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
	panic(pendiente.Implementar("stages.ResolveDate"))
}
