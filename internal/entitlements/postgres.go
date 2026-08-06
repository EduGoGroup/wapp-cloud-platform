package entitlements

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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
	// listFn resuelve la lista efectiva en la BD ante un miss de caché. Mismo
	// motivo (y mismo seam de test) que lookupFn. NewPostgres lo fija a
	// p.listEffective.
	listFn func(ctx context.Context, tenantID string) (string, []string, error)

	mu sync.Mutex
	// cache sirve a Has: una entrada por PAR (tenant, feature).
	cache map[cacheKey]cacheEntry
	// effective sirve a ListEffective: una entrada por TENANT. Es una caché
	// SEPARADA a propósito — la de Has no puede responder "¿cuáles tiene?" (solo
	// sabe de los pares que alguien preguntó), y mezclarlas obligaría a invalidar
	// una desde la otra.
	effective map[string]effectiveEntry
}

type cacheKey struct {
	tenantID string
	feature  string
}

type cacheEntry struct {
	has       bool
	expiresAt time.Time
}

type effectiveEntry struct {
	plan      string
	features  []string
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
	p := &Postgres{
		db:        db,
		ttl:       defaultCacheTTL,
		cache:     make(map[cacheKey]cacheEntry),
		effective: make(map[string]effectiveEntry),
	}
	for _, opt := range opts {
		opt(p)
	}
	p.lookupFn = p.lookup
	p.listFn = p.listEffective
	return p
}

// CacheTTL devuelve el TTL efectivo de las cachés de este Resolver. Lo publica
// GET /api/v1/entitlements como cache_ttl_seconds: es la cota superior de lo que
// tarda en verse un cambio de plan/override (Plan 040 · T2.2).
func (p *Postgres) CacheTTL() time.Duration { return p.ttl }

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

// ListEffective devuelve el plan efectivo del tenant y sus features encendidas,
// sirviendo de la caché POR TENANT si la entrada sigue vigente. La lista se
// devuelve clonada: quien la recibe puede ordenarla o recortarla sin corromper lo
// cacheado.
func (p *Postgres) ListEffective(ctx context.Context, tenantID string) (string, []string, error) {
	now := time.Now()

	p.mu.Lock()
	if e, ok := p.effective[tenantID]; ok && now.Before(e.expiresAt) {
		plan, features := e.plan, slices.Clone(e.features)
		p.mu.Unlock()
		return plan, features, nil
	}
	p.mu.Unlock()

	plan, features, err := p.listFn(ctx, tenantID)
	if err != nil {
		return "", nil, err
	}

	p.mu.Lock()
	p.effective[tenantID] = effectiveEntry{plan: plan, features: slices.Clone(features), expiresAt: now.Add(p.ttl)}
	p.mu.Unlock()
	return plan, features, nil
}

// listEffective resuelve en la BD el plan y las features ENCENDIDAS del tenant
// aplicando la MISMA regla que lookup (ADR-0022), pero de una vez para todas las
// claves: features del plan (plan NULL ⇒ 'basic') UNION los overrides que activan,
// MENOS las que un override desactiva (el override gana en ambos sentidos).
//
// Un override con enabled=false EXCLUYE la feature aunque el plan la traiga: por
// eso el anti-join final, y no un simple filtro sobre el UNION.
func (p *Postgres) listEffective(ctx context.Context, tenantID string) (plan string, features []string, err error) {
	err = p.db.QueryRowContext(ctx, `
		SELECT COALESCE(plan_id, 'basic')
		FROM public.tenants
		WHERE id = $1
	`, tenantID).Scan(&plan)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Tenant inexistente: sin plan resoluble ⇒ sin derechos. Es la MISMA
		// respuesta que da Has (su consulta arranca en tenants, así que un tenant
		// que no existe no tiene ninguna feature), no un fallo de infraestructura.
		return "", nil, nil
	case err != nil:
		return "", nil, fmt.Errorf("entitlements: resolver el plan del tenant: %w", err)
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT f.feature
		FROM (
			SELECT pf.feature
			FROM public.plan_features pf
			WHERE pf.plan_id = $2
			UNION
			SELECT tf.feature
			FROM public.tenant_features tf
			WHERE tf.tenant_id = $1 AND tf.enabled
		) AS f
		WHERE NOT EXISTS (
			SELECT 1
			FROM public.tenant_features apagada
			WHERE apagada.tenant_id = $1
			  AND apagada.feature = f.feature
			  AND NOT apagada.enabled
		)
	`, tenantID, plan)
	if err != nil {
		return "", nil, fmt.Errorf("entitlements: listar features efectivas: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			plan, features, err = "", nil, fmt.Errorf("entitlements: cerrar filas de features: %w", cerr)
		}
	}()

	for rows.Next() {
		var feature string
		if serr := rows.Scan(&feature); serr != nil {
			return "", nil, fmt.Errorf("entitlements: scan de feature: %w", serr)
		}
		features = append(features, feature)
	}
	if rerr := rows.Err(); rerr != nil {
		return "", nil, fmt.Errorf("entitlements: iterar features: %w", rerr)
	}

	// Orden alfabético GARANTIZADO aquí y no con un ORDER BY: el collation del
	// servidor puede ordenar el guion bajo de otra forma, y el contrato del
	// endpoint promete un orden estable que los tests puedan afirmar.
	slices.Sort(features)
	return plan, features, nil
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
