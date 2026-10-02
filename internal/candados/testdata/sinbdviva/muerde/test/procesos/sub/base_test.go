package sub

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Un fichero que se llama como el de la lista blanca, pero en otro directorio: la lista
// blanca es de rutas exactas (test/procesos/base_test.go), no de nombres base.
func openInSubdir(ctx context.Context) (*pgx.Conn, error) {
	return pgx.Connect(ctx, "")
}
