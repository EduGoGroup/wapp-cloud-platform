package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/EduGoGroup/wapp-shared/health"
)

func TestHealthCheck_Table(t *testing.T) {
	dbClosed, err := sql.Open("pgx", "host=127.0.0.1 port=1 user=nobody dbname=nobody sslmode=disable")
	if err != nil {
		t.Fatalf("sql.Open err: %v", err)
	}
	if err := dbClosed.Close(); err != nil {
		t.Fatalf("dbClosed.Close err: %v", err)
	}

	tests := []struct {
		name          string
		db            *sql.DB
		wantComponent string
		wantStatus    health.Status
	}{
		{
			name:          "closed db reports unhealthy status",
			db:            dbClosed,
			wantComponent: "postgres",
			wantStatus:    health.StatusUnhealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := NewHealthCheck(tt.db)
			if hc.Name() != tt.wantComponent {
				t.Errorf("Name() = %q, quiero %q", hc.Name(), tt.wantComponent)
			}
			res := hc.Check(context.Background())
			if res.Component != tt.wantComponent {
				t.Errorf("Check().Component = %q, quiero %q", res.Component, tt.wantComponent)
			}
			if res.Status != tt.wantStatus {
				t.Errorf("Check().Status = %v, quiero %v", res.Status, tt.wantStatus)
			}
		})
	}
}

func TestApplyPool_Table(t *testing.T) {
	db, err := sql.Open("pgx", "host=127.0.0.1 port=5432 user=nobody dbname=nobody sslmode=disable")
	if err != nil {
		t.Fatalf("sql.Open err: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close err: %v", err)
		}
	}()

	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "zero config applies default pool settings",
			cfg:  Config{},
		},
		{
			name: "custom config applies non-zero pool settings",
			cfg: Config{
				MaxOpenConns: 50,
				MaxIdleConns: 10,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyPool(db, tt.cfg)
		})
	}
}
