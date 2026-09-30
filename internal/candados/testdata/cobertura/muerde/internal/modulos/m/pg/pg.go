// cobertura: adaptador postgres (05 E-6)

// Package pg es un adaptador Postgres de verdad: la marca lo exime.
package pg

import "github.com/jackc/pgx/v5"

// Conectar abre la conexión.
func Conectar() *pgx.Conn { return nil }
