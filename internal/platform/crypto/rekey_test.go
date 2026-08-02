package crypto

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestRekey_ClosedDB_FailsGracefully(t *testing.T) {
	db, err := sql.Open("pgx", "host=127.0.0.1 port=1 user=nobody dbname=nobody sslmode=disable")
	if err != nil {
		t.Fatalf("sql.Open err: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close err: %v", err)
	}

	kp, err := NewEnvKeyProvider(KeyringConfig{
		MasterB64: masterB64(),
		IndexB64:  indexB64(),
	})
	if err != nil {
		t.Fatalf("NewEnvKeyProvider err: %v", err)
	}

	cipher := NewFieldCipher(kp)

	// Rekey con DB cerrada debe devolver error sin panic
	_, err = Rekey(context.Background(), db, cipher, kp, 10)
	if err == nil {
		t.Fatal("esperaba error al ejecutar Rekey con db cerrada")
	}

	// PendingByKeyID con DB cerrada debe devolver error
	_, err = PendingByKeyID(context.Background(), db, "1")
	if err == nil {
		t.Fatal("esperaba error al ejecutar PendingByKeyID con db cerrada")
	}
}
