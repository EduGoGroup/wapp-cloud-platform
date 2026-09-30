// cobertura: adaptador postgres (05 E-6)

// Package falso se marca como adaptador Postgres sin importar database/sql ni pgx.
package falso

// Suma está casi toda cubierta.
func Suma(a, b int) int { return a + b }
