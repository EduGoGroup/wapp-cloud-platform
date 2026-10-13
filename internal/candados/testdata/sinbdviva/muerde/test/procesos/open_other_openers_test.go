package procesos

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// Las otras puertas de las mismas librerías: la conexión de bajo nivel de pgconn, el *sql.DB
// que arma stdlib sin pasar por sql.Open, el conector y el driver de stdlib (que abren sin
// nombrar una función de apertura), y la apertura tomada como VALOR y llamada después.
func openLowLevel(ctx context.Context) (*pgconn.PgConn, error) {
	return pgconn.Connect(ctx, "")
}

func openThroughStdlib(cfg pgx.ConnConfig) (*sql.DB, *sql.DB) {
	return stdlib.OpenDB(cfg), sql.OpenDB(stdlib.GetConnector(cfg))
}

func openThroughDriver() (driver.Conn, error) {
	return stdlib.GetDefaultDriver().Open("")
}

func openAsValue(ctx context.Context) (*pgx.Conn, error) {
	dial := pgx.Connect
	return dial(ctx, "")
}
