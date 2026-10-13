package procesos

import "context"

type contenedor interface {
	ConnectionString(ctx context.Context, args ...string) (string, error)
}

// cadena NO lee WAPP_TEST_DB_DSN, no apunta a localhost:5432, no usa WithReuseByName
// ni escribe postgres:// a mano: los comentarios no cuentan.
func cadena(ctx context.Context, ctr contenedor) (string, error) {
	return ctr.ConnectionString(ctx, "sslmode=disable")
}
