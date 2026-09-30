package rojo

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

// Hacer sigue en rojo: no se evalúa aunque su cobertura sea 0.
func Hacer() int {
	panic(pendiente.Implementar("rojo.Hacer"))
}
