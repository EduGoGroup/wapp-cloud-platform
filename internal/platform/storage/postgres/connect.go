// Package postgres provee la conexión, el runner de migraciones, el health
// check de BD y los repositorios SQL raw de la Plataforma Cloud sobre
// PostgreSQL. Usa database/sql con el driver pgx/v5 en modo stdlib (sin CGO,
// sin ORM). Copia-adaptación del patrón de edugo-shared, despojado de GORM.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// pgx/stdlib registra el driver "pgx" en database/sql.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Driver es el nombre del driver registrado por pgx/v5/stdlib.
const Driver = "pgx"

// Valores por defecto del pool de conexiones, aplicados cuando el campo
// correspondiente de Config es cero.
const (
	defaultMaxOpenConns    = 25
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = time.Hour
	defaultConnMaxIdleTime = 10 * time.Minute
	defaultPingTimeout     = 5 * time.Second
)

// Config agrupa el DSN y los parámetros del pool de conexiones. Los campos en
// cero toman los defaults definidos arriba.
type Config struct {
	// DSN es la cadena de conexión en formato keyword/value de libpq
	// (host=… port=… user=… password=… dbname=… sslmode=…).
	DSN string
	// MaxOpenConns es el máximo de conexiones abiertas simultáneas.
	MaxOpenConns int
	// MaxIdleConns es el máximo de conexiones ociosas retenidas.
	MaxIdleConns int
	// ConnMaxLifetime es la vida máxima de una conexión.
	ConnMaxLifetime time.Duration
	// ConnMaxIdleTime es el tiempo máximo que una conexión puede estar ociosa.
	ConnMaxIdleTime time.Duration
}

// Open abre el pool de conexiones a PostgreSQL y verifica conectividad con un
// PingContext acotado. Devuelve error claro (no panic) si la BD no responde;
// en ese caso el *sql.DB ya está cerrado.
func Open(ctx context.Context, cfg Config) (*sql.DB, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("postgres: DSN vacío")
	}

	db, err := sql.Open(Driver, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: abriendo conexión: %w", err)
	}

	applyPool(db, cfg)

	pingCtx, cancel := context.WithTimeout(ctx, defaultPingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return nil, errors.Join(fmt.Errorf("postgres: ping inicial: %w", err), db.Close())
	}

	return db, nil
}

// applyPool fija los parámetros del pool, usando defaults para los campos en cero.
func applyPool(db *sql.DB, cfg Config) {
	maxOpen := cfg.MaxOpenConns
	if maxOpen == 0 {
		maxOpen = defaultMaxOpenConns
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle == 0 {
		maxIdle = defaultMaxIdleConns
	}
	maxLife := cfg.ConnMaxLifetime
	if maxLife == 0 {
		maxLife = defaultConnMaxLifetime
	}
	maxIdleTime := cfg.ConnMaxIdleTime
	if maxIdleTime == 0 {
		maxIdleTime = defaultConnMaxIdleTime
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(maxLife)
	db.SetConnMaxIdleTime(maxIdleTime)
}
