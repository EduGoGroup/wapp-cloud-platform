package postgres

import (
	"context"
	"testing"
)

// TestOpen_EmptyDSN verifica el fail-fast con DSN vacío (sin tocar la red).
func TestOpen_EmptyDSN(t *testing.T) {
	if _, err := Open(context.Background(), Config{DSN: ""}); err == nil {
		t.Fatal("Open con DSN vacío debería devolver error")
	}
}
