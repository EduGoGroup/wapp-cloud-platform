// Package pendiente marca un contrato sin lógica (05 E-2). Es un paquete de UNA función a
// propósito: todo cuerpo de contrato es `panic(pendiente.Implementar("paquete.Símbolo"))`, y
// `make test-pendiente` cuenta esas llamadas para decir cuánto falta.
package pendiente

import "errors"

// Implementar devuelve el error con el que entra en pánico un cuerpo de contrato.
// El mensaje contiene `simbolo` literal y el prefijo "pendiente: ", para que un test en rojo
// diga QUÉ falta sin leer la traza. Un `simbolo` vacío produce "pendiente: (símbolo sin
// nombre)", nunca un mensaje vacío. Dos llamadas con el mismo símbolo dan mensajes iguales.
// No tiene estado ni efectos: no loguea, no cuenta, no mira el entorno.
func Implementar(simbolo string) error {
	// Sin nombre, el mensaje seguiría siendo "pendiente: " y el test en rojo no diría nada:
	// por eso un marcador explícito en vez de la cadena vacía.
	if simbolo == "" {
		return errors.New("pendiente: (símbolo sin nombre)")
	}
	return errors.New("pendiente: " + simbolo)
}
