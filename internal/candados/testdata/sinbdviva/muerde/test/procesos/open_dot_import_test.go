package procesos

import (
	"context"

	. "github.com/jackc/pgx/v5"
)

// Con import de punto la apertura es un identificador suelto, sin receptor.
func openBehindDot(ctx context.Context) (*Conn, error) {
	return Connect(ctx, "")
}
