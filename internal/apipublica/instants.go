// Porta internal/publicapi/conversationevents.go @ ed60c24 (formatInstant, líneas 215-221).
//
// instants.go — CÓMO VIAJA UN INSTANTE EN EL JSON DE LA CARA. En la spec FX era `instantes.go`
// (05 E-11). En el viejo la función vive en conversationevents.go (F8) y la usa además intakes.go
// (F6): un fichero común nace con su PRIMER consumidor (D-FX-5), y ese es la bandeja de
// solicitudes, no los eventos de conversación.
//
// No exporta nada: su test es interno y nace con él (05 E-4, P6).

package apipublica

import "time"

// formatInstant devuelve el instante en RFC3339 UTC, o vacío si es el cero.
//
// El cero de time.Time (algo que todavía no ha ocurrido: una solicitud sin aprobar, un evento sin
// cerrar) viaja como cadena vacía y no como «0001-01-01», que es una fecha que no significa nada.
// La zona se normaliza a UTC: el cliente no tiene que saber en qué huso corre el servidor.
func formatInstant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
