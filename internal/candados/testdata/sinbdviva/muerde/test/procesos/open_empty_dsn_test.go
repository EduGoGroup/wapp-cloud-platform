package procesos

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5"
)

// Un DSN sin host ni puerto no lleva ningún literal sospechoso: pgx completa lo que falta con
// PGHOST y PGPORT del entorno, o con sus valores por defecto. La lista negra de literales no
// lo ve; la lista blanca muerde la apertura misma, diga lo que diga la cadena (D-F9-6).
func openEmptyDSN(ctx context.Context) (*pgx.Conn, *sql.DB, error) {
	conn, err := pgx.Connect(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("pgx", "dbname=wapp")
	return conn, db, err
}
