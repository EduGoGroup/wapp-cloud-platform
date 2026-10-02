package procesos

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// base_test.go es el fichero de la lista blanca: abrir aquí una conexión no es violación.
// Pero la lista blanca solo exime de ESA regla: un literal de la lista negra muerde igual.
func openMaintenance(ctx context.Context) (*pgx.Conn, error) {
	return pgx.Connect(ctx, "127.0.0.1:5432")
}
