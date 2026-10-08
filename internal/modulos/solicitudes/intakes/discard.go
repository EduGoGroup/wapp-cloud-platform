// Porta internal/intakes/discard.go @ 64c181a.
//
// De momento solo nace DiscardOutcome, porque el puerto Store (intakes.go) lo nombra
// en Discard; el resto del contrato del descarte llega con las acciones de la bandeja
// (T6.7).

package intakes

// DiscardOutcome es lo que el store encontró y/o hizo con UNA solicitud. Lleva
// HECHOS, no razones: la traducción a `skipped[].reason` la hace el dominio
// (Service.Discard), que es quien conoce la política.
type DiscardOutcome struct {
	// Discarded dice si esta llamada escribió el `abandoned` (y su revisión, y el
	// cierre del contenedor si lo hubiera).
	Discarded bool
	// Status es el estado ACTUAL de la solicitud, ya normalizado. Con Discarded en
	// true es el estado del que VENÍA; con false, aquel en el que se quedó.
	Status string
	// LiveEvent dice si el evento conversacional que ESTA solicitud declara
	// (intakes.event_id) sigue `open`. Una solicitud legada sin event_id no tiene
	// evento vivo que mirar y es descartable.
	LiveEvent bool
}
