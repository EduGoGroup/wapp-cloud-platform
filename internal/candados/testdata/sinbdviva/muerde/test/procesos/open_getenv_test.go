package procesos

import (
	"context"
	"database/sql"
	"os"

	"github.com/jackc/pgx/v5"
)

// os.Getenv de UNA variable concreta es legítimo para la lista negra, también cuando la
// variable es la cadena de conexión de un Postgres de fuera. Muerde la apertura (D-F9-6).
func openFromEnv(ctx context.Context) (*sql.DB, *pgx.Conn, error) {
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, nil, err
	}
	conn, err := pgx.Connect(ctx, "host="+os.Getenv("PGHOST"))
	return db, conn, err
}
