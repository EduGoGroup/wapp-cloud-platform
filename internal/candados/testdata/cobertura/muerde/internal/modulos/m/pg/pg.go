// cobertura: adaptador postgres (05 E-6)

// Package pg es un adaptador Postgres con la marca vieja en la cabecera: desde P2 es un
// comentario inerte y el fichero se mide como cualquier otro.
package pg

import "github.com/jackc/pgx/v5"

// Conectar abre la conexión.
func Conectar() *pgx.Conn { return nil }
