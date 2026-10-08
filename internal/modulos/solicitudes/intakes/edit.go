// Porta internal/intakes/edit.go @ 64c181a.
//
// De momento solo nace EditMode, porque el puerto Store (intakes.go) lo nombra en
// ReplaceItems; el resto del contrato de la edición llega con las acciones de la
// bandeja (T6.7).

package intakes

// EditMode dice con qué intención se reemplazan las líneas de una solicitud. Es un
// parámetro TIPADO y no un bool desnudo: son dos momentos distintos con dos reglas
// distintas, el llamante es quien sabe cuál es, y un bool en la llamada no dice cuál
// de los dos es `true` al leerla.
//
// Lo que NO cambia con el modo: el estado desde el que se edita sigue siendo
// EditableStatus (`as_correction` no amplía los estados editables ni inventa
// transiciones); la revisión sigue siendo una, de clase `corrected` y firmada por
// `owner`; y el empuje al CRM se dispara igual con modo y sin modo, porque cuelga de
// que nazca una revisión y no de este campo.
type EditMode int

const (
	// EditPlain es el `PUT …/items` tal cual: sin señal y sin conducta nueva. Es el
	// CERO del tipo a propósito: el valor por descuido es el que ya existía, nunca el
	// que estrena conducta.
	EditPlain EditMode = iota
	// EditAsCorrection es el `correct` (`"as_correction": true`): la misma escritura,
	// con la señal few-shot dentro de la revisión.
	EditAsCorrection
)
