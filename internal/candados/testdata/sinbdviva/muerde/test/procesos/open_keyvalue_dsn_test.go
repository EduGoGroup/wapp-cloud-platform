package procesos

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Un DSN clave=valor nombra el puerto sin los dos puntos delante y no empieza por el esquema
// de una URL: ningún literal de la lista negra. Muerde la apertura (D-F9-6).
func openKeyValueDSN(ctx context.Context) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, "host=localhost port=5432 dbname=wapp sslmode=disable")
}
