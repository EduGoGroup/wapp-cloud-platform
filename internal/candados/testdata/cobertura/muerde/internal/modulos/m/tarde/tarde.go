// Package tarde importa database/sql y lleva la marca vieja fuera de la cabecera: inerte.
package tarde

// cobertura: adaptador postgres (05 E-6)

import "database/sql"

// Abrir no está cubierta.
func Abrir() *sql.DB { return nil }
