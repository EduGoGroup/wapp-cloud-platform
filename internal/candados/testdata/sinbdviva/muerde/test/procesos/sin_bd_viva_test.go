package procesos

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Los patrones que persigue, como literales: el candado se salta a sí mismo.
var patrones = []string{"WAPP_TEST_DB_DSN", ":5432", "WithReuseByName", "postgres://", "postgresql://"}

// La auto-exención cubre los patrones que el candado nombra para perseguirlos, no la regla de
// la lista blanca: el candado no tiene por qué abrir una conexión, y si la abre, muerde.
func openInsideTheLock(ctx context.Context) (*pgx.Conn, error) {
	return pgx.Connect(ctx, "")
}
