// Package tarde importa database/sql, pero su marca no está en la cabecera.
package tarde

// cobertura: adaptador postgres (05 E-6)

import "database/sql"

// Abrir no está cubierta.
func Abrir() *sql.DB { return nil }
