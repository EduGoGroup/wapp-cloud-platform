// Package errhelpertest reúne las afirmaciones sobre errores que comparten los tests del árbol
// nuevo. Es un paquete de ayudas de test: no lo importa el código de producción.
package errhelpertest

import "errors"

// IsBare dice si got ES want SIN ENVOLVER: lo que promete un contrato que dice «el error sale
// tal cual». errors.Is a secas no basta para afirmarlo, porque también acepta
// fmt.Errorf("…: %w", want), que es justo lo que ese contrato prohíbe.
//
// Son tres condiciones, y hacen falta las tres:
//   - errors.Is(got, want): es want, o lo lleva dentro;
//   - errors.Unwrap(got) == nil: no lo lleva dentro de un %w ni de un tipo con Unwrap() error;
//   - got no es un errors.Join (ni un %w múltiple), cuyo Unwrap() []error no ve errors.Unwrap.
//
// Límite conocido: un tipo de error AJENO a want, sin Unwrap, con un método Is propio que
// responda true pasaría. En el repo no hay ninguno; quien añada uno no puede afirmar con esto.
//
// Con got o want a nil responde lo que errors.Is: true solo si los dos son nil.
func IsBare(got, want error) bool {
	if !errors.Is(got, want) || errors.Unwrap(got) != nil {
		return false
	}
	var joined interface{ Unwrap() []error }
	return !errors.As(got, &joined)
}
