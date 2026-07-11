package entitlements

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// defaultCacheTTL acota cuánto vive una respuesta cacheada de Has. Corto a
// propósito: un cambio de plan/override se propaga en <=TTL sin re-emitir nada.
const defaultCacheTTL = 60 * time.Second

// Postgres resuelve entitlements contra la BD y cachea cada (tenant, feature) en
// memoria por defaultCacheTTL. Es seguro para uso concurrente.
type Postgres struct {
	db  *sql.DB
	ttl time.Duration

	// lookupFn resuelve el entitlement en la BD ante un miss de caché. Es un campo
	// (no una llamada directa a p.lookup) para poder sustituirlo por un stub en los
	// tests de la caché sin necesitar una BD real. NewPostgres lo fija a p.lookup.
	lookupFn func(ctx context.Context, tenantID, feature string) (bool, error)

	mu    sync.Mutex
	cache map[cacheKey]cacheEntry
}

type cacheKey struct {
	tenantID string
	feature  string
}

type cacheEntry struct {
	has       bool
	expiresAt time.Time
}

// Option configura el Postgres al construirlo.
type Option func(*Postgres)

// WithTTL fija el TTL de la caché (un valor <=0 se ignora y cae al default).
func WithTTL(d time.Duration) Option {
	return func(p *Postgres) {
		if d > 0 {
			p.ttl = d
		}
	}
}

// NewPostgres construye el Resolver Postgres con caché sobre el *sql.DB dado.
func NewPostgres(db *sql.DB, opts ...Option) *Postgres {
	p := &Postgres{db: db, ttl: defaultCacheTTL, cache: make(map[cacheKey]cacheEntry)}
	for _, opt := range opts {
		opt(p)
	}
	p.lookupFn = p.lookup
	return p
}

// Has resuelve el entitlement, sirviendo de la caché si la entrada sigue vigente.
// Un miss consulta la BD y cachea el resultado (incluido el false: no habilitar es
// un dato cacheable igual que habilitar).
func (p *Postgres) Has(ctx context.Context, tenantID, feature string) (bool, error) {
	k := cacheKey{tenantID: tenantID, feature: feature}
	now := time.Now()

	p.mu.Lock()
	if e, ok := p.cache[k]; ok && now.Before(e.expiresAt) {
		p.mu.Unlock()
		return e.has, nil
	}
	p.mu.Unlock()

	has, err := p.lookupFn(ctx, tenantID, feature)
	if err != nil {
		return false, err
	}

	p.mu.Lock()
	p.cache[k] = cacheEntry{has: has, expiresAt: now.Add(p.ttl)}
	p.mu.Unlock()
	return has, nil
}

// lookup resuelve el entitlement en la BD (ADR-0022): el override de
// tenant_features gana; si no hay override, mandan las features del plan del
// tenant (plan NULL ⇒ 'basic').
func (p *Postgres) lookup(ctx context.Context, tenantID, feature string) (bool, error) {
	// 1) Override explícito del tenant (activa o desactiva con independencia del plan).
	var enabled bool
	err := p.db.QueryRowContext(ctx, `
		SELECT enabled
		FROM public.tenant_features
		WHERE tenant_id = $1 AND feature = $2
	`, tenantID, feature).Scan(&enabled)
	switch {
	case err == nil:
		return enabled, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("entitlements: leer override de feature: %w", err)
	}

	// 2) Sin override: features del plan del tenant (plan NULL ⇒ 'basic').
	var has bool
	err = p.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM public.tenants t
			JOIN public.plan_features pf ON pf.plan_id = COALESCE(t.plan_id, 'basic')
			WHERE t.id = $1 AND pf.feature = $2
		)
	`, tenantID, feature).Scan(&has)
	if err != nil {
		return false, fmt.Errorf("entitlements: resolver feature del plan: %w", err)
	}
	return has, nil
}
