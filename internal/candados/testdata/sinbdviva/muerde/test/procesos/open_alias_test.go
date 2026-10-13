package procesos

import (
	"context"
	db "database/sql"

	p "github.com/jackc/pgx/v5"
	pool "github.com/jackc/pgx/v5/pgxpool"
)

// Con alias el receptor ya no se llama sql, pgx ni pgxpool, pero es el mismo paquete: la
// apertura se persigue por la ruta del import, y el motivo la sigue nombrando por el nombre
// del paquete (sql.Open, pgx.Connect, pgxpool.NewWithConfig).
func openBehindAlias(ctx context.Context, cfg *pool.Config) (*db.DB, *p.Conn, *pool.Pool, error) {
	handle, err := db.Open("pgx", "")
	if err != nil {
		return nil, nil, nil, err
	}
	conn, err := p.Connect(ctx, "")
	if err != nil {
		return nil, nil, nil, err
	}
	shared, err := pool.NewWithConfig(ctx, cfg)
	return handle, conn, shared, err
}
