package procesos

import (
	"context"
	"net"

	"github.com/jackc/pgx/v5"
)

// Literales construidos: ni el esquema ni host:puerto aparecen enteros en un solo literal, así
// que la lista negra no ve ninguno. Muerden las dos aperturas (D-F9-6).
func openBuiltURL(ctx context.Context) (*pgx.Conn, error) {
	return pgx.Connect(ctx, "postgres"+"://wapp@"+net.JoinHostPort("localhost", "54"+"32")+"/wapp")
}

func openBuiltConfig(ctx context.Context) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig("host=localhost port=" + "54" + "32")
	if err != nil {
		return nil, err
	}
	return pgx.ConnectConfig(ctx, cfg)
}
