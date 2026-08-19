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
// correspondiente de Config viene sin fijar.
//
// Están EXPORTADOS a propósito (Plan 050 · Ola 4 · T4.2): desde que el pool se
// puede configurar por WAPP_DB_*, internal/platform/config los referencia para
// armar sus propios defaults en vez de copiar los números. Si se copiaran,
// nada impediría que divergieran, y en esa divergencia ganaría el de config
// mientras estas constantes seguirían pareciendo la verdad.
const (
	// DefaultMaxOpenConns es el máximo de conexiones abiertas simultáneas.
	DefaultMaxOpenConns = 25
	// DefaultMaxIdleConns es el máximo de conexiones ociosas retenidas.
	DefaultMaxIdleConns = 5
	// DefaultConnMaxLifetime es la vida máxima de una conexión.
	DefaultConnMaxLifetime = time.Hour
	// DefaultConnMaxIdleTime es el tiempo máximo que una conexión puede estar ociosa.
	DefaultConnMaxIdleTime = 10 * time.Minute
)

// defaultPingTimeout acota el ping inicial de Open. No es parámetro del pool y
// no se expone por entorno.
const defaultPingTimeout = 5 * time.Second

// Config agrupa el DSN y los parámetros del pool de conexiones. Los campos que
// no traigan un valor POSITIVO toman los defaults definidos arriba.
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

// applyPool fija los parámetros del pool, usando defaults para todo campo que no
// venga con un valor POSITIVO.
//
// La guarda es `<= 0`, no `== 0`, y la diferencia importa desde que el pool se
// lee del entorno: en database/sql un SetMaxOpenConns(-1) —o cualquier negativo—
// significa pool ILIMITADO, y un SetConnMaxLifetime negativo, conexiones que no
// caducan nunca. Un WAPP_DB_MAX_OPEN_CONNS=-1 mal tecleado abriría el pool de par
// en par contra Neon sin que nadie viera un error. Aquí NO hay forma deliberada
// de desactivar un tope: si se quiere más pool, se sube el número.
//
// Es la red de ABAJO. La de arriba vive en config.Load (que descarta el negativo
// antes de llegar hasta aquí) y cada una tiene su propio test: son dos redes con
// el mismo síntoma y quitar una sin que caiga su test es exactamente el fallo que
// se está previniendo.
func applyPool(db *sql.DB, cfg Config) {
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = DefaultMaxOpenConns
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = DefaultMaxIdleConns
	}
	maxLife := cfg.ConnMaxLifetime
	if maxLife <= 0 {
		maxLife = DefaultConnMaxLifetime
	}
	maxIdleTime := cfg.ConnMaxIdleTime
	if maxIdleTime <= 0 {
		maxIdleTime = DefaultConnMaxIdleTime
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(maxLife)
	db.SetConnMaxIdleTime(maxIdleTime)
}
