package postgres

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

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

// poolSetting lee por reflexión uno de los parámetros del pool que database/sql
// guarda en campos NO exportados. Solo MaxOpenConns asoma por Stats(); los otros
// tres no tienen getter público, y sin mirarlos el test dejaría sin custodiar
// tres de las cuatro guardas de applyPool — que es justo lo que existe para
// vigilar. Int() sí funciona sobre un campo no exportado (Interface() no).
//
// Si una versión de Go renombra el campo, esto FALLA con un mensaje explícito en
// vez de pasar en verde sin comprobar nada.
func poolSetting(t *testing.T, db *sql.DB, field string) int64 {
	t.Helper()
	f := reflect.ValueOf(db).Elem().FieldByName(field)
	if !f.IsValid() {
		t.Fatalf("database/sql cambió: *sql.DB ya no tiene el campo %q; actualiza este test", field)
	}
	return f.Int()
}

// openIdle abre un *sql.DB que nunca se usa: sql.Open no conecta, así que basta
// para observar la configuración del pool sin BD real.
func openIdle(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", "host=127.0.0.1 port=5432 user=nobody dbname=nobody sslmode=disable")
	if err != nil {
		t.Fatalf("sql.Open err: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close err: %v", err)
		}
	})
	return db
}

// TestApplyPool_Table comprueba los CUATRO parámetros del pool en los tres casos
// que importan: sin fijar, fijados y NEGATIVOS.
//
// El caso negativo es el que da sentido al test: en database/sql un
// SetMaxOpenConns(-1) significa pool ILIMITADO y un SetConnMaxLifetime(-1),
// conexiones eternas. Desde que el pool se lee de WAPP_DB_*, un signo mal puesto
// puede llegar hasta aquí. Esta es la red de ABAJO; la de arriba (config.Load
// descartando el negativo) tiene su propio test en internal/platform/config, y
// las dos se prueban por separado a propósito: si una sola cubriera ambas, borrar
// cualquiera de las dos guardas seguiría dando verde.
func TestApplyPool_Table(t *testing.T) {
	tests := []struct {
		name         string
		cfg          Config
		wantMaxOpen  int
		wantMaxIdle  int64
		wantLifetime time.Duration
		wantIdleTime time.Duration
	}{
		{
			name:         "config sin fijar aplica los defaults del paquete",
			cfg:          Config{},
			wantMaxOpen:  DefaultMaxOpenConns,
			wantMaxIdle:  DefaultMaxIdleConns,
			wantLifetime: DefaultConnMaxLifetime,
			wantIdleTime: DefaultConnMaxIdleTime,
		},
		{
			name: "config con valores positivos los respeta",
			cfg: Config{
				MaxOpenConns:    50,
				MaxIdleConns:    10,
				ConnMaxLifetime: 30 * time.Minute,
				ConnMaxIdleTime: 2 * time.Minute,
			},
			wantMaxOpen:  50,
			wantMaxIdle:  10,
			wantLifetime: 30 * time.Minute,
			wantIdleTime: 2 * time.Minute,
		},
		{
			name: "valores NEGATIVOS caen al default y no abren el pool de par en par",
			cfg: Config{
				MaxOpenConns:    -1,
				MaxIdleConns:    -1,
				ConnMaxLifetime: -1,
				ConnMaxIdleTime: -1,
			},
			wantMaxOpen:  DefaultMaxOpenConns,
			wantMaxIdle:  DefaultMaxIdleConns,
			wantLifetime: DefaultConnMaxLifetime,
			wantIdleTime: DefaultConnMaxIdleTime,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openIdle(t)
			applyPool(db, tt.cfg)

			if got := db.Stats().MaxOpenConnections; got != tt.wantMaxOpen {
				t.Errorf("MaxOpenConnections = %d, quiero %d", got, tt.wantMaxOpen)
			}
			if got := poolSetting(t, db, "maxIdleCount"); got != tt.wantMaxIdle {
				t.Errorf("maxIdleCount = %d, quiero %d", got, tt.wantMaxIdle)
			}
			if got := time.Duration(poolSetting(t, db, "maxLifetime")); got != tt.wantLifetime {
				t.Errorf("maxLifetime = %v, quiero %v", got, tt.wantLifetime)
			}
			if got := time.Duration(poolSetting(t, db, "maxIdleTime")); got != tt.wantIdleTime {
				t.Errorf("maxIdleTime = %v, quiero %v", got, tt.wantIdleTime)
			}
		})
	}
}
