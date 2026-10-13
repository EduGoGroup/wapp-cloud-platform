// cobertura: adaptador postgres (05 E-6)

// Package falso lleva la marca vieja sin importar database/sql ni pgx: hasta P2 era una
// violación; hoy la marca es inerte y el fichero solo se mide (90 %).
package falso

// Suma está casi toda cubierta.
func Suma(a, b int) int { return a + b }
