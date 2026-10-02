package procesos

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// base_test.go es el ÚNICO fichero de test/procesos que abre conexiones (lista blanca de
// D-F9-6), y las abre con la cadena del contenedor de la corrida.
func openMaintenance(ctx context.Context, ctr contenedor) (*pgx.Conn, error) {
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, err
	}
	return pgx.Connect(ctx, dsn)
}

func openClone(dsn string) (*sql.DB, error) {
	return sql.Open("pgx", dsn)
}
