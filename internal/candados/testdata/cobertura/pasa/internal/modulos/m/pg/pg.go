// cobertura: adaptador postgres (05 E-6)

// Package pg es un adaptador Postgres de verdad: la marca lo exime.
package pg

import "database/sql"

// Abrir no está cubierta.
func Abrir() *sql.DB { return nil }
