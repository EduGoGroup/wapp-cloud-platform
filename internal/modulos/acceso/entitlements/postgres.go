// Porta internal/entitlements/postgres.go @ 9a77307

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

// defaultCacheTTL acota cuánto vive una respuesta cacheada. Corto a propósito: un cambio de plan u
// override se propaga en <= TTL sin re-emitir nada.
const defaultCacheTTL = 60 * time.Second

// Postgres es el Resolver contra la BD (public.tenants, public.plan_features y
// public.tenant_features, migración 0032) con una caché en memoria por delante, porque Has se
// pregunta en puntos calientes (push de config, API de intents, ingesta) y no puede pagar una
// consulta por mensaje. Es seguro para uso concurrente.
//
// Lo que promete, además de las reglas del puerto Resolver (las fija la suite
// entitlementshelpertest.ContratoResolver, que corre contra él en test/procesos):
//
//   - DOS cachés SEPARADAS: la de Has, con una entrada por PAR (tenant, feature), y la de
//     ListEffective, con una entrada por TENANT. Una respuesta de Has no sirve a ListEffective ni
//     al revés: la de Has solo sabe de los pares que alguien preguntó, y mezclarlas obligaría a
//     invalidar una desde la otra.
//   - Una entrada vale mientras el reloj (WithClock) marque un instante ANTERIOR a su
//     vencimiento, que es el instante de la consulta que la llenó más el TTL: en el instante
//     exacto del vencimiento ya no vale y se vuelve a consultar. Dentro del TTL no se re-consulta.
//   - El false también se cachea: «no habilitada» es un dato tan cacheable como «habilitada».
//   - Un error NO se cachea: la llamada siguiente vuelve a consultar.
//   - La lista de ListEffective se entrega como COPIA, en el fallo de caché y en el acierto:
//     quien la recibe puede ordenarla o recortarla sin corromper lo cacheado.
//   - El candado de la caché NO se sostiene durante la consulta a la BD: una consulta lenta de un
//     tenant no bloquea a los demás llamantes, ni siquiera a los que aciertan en la caché.
//   - Un tenant que no existe no tiene derechos: Has da (false, nil) y ListEffective ("", nil,
//     nil). No es un fallo de infraestructura.
//   - ListEffective entrega las features en orden alfabético por bytes, ordenadas en Go y no con
//     un ORDER BY: el collation del servidor puede ordenar el guion bajo de otra forma.
//   - Todo error de la BD sale envuelto (errors.Is llega al original) y con un texto que empieza
//     por "entitlements: ", copiado literal del paquete viejo: "entitlements: leer override de
//     feature: ", "entitlements: resolver feature del plan: ", "entitlements: resolver el plan del
//     tenant: ", "entitlements: listar features efectivas: ", "entitlements: scan de feature: ",
//     "entitlements: iterar features: " y "entitlements: cerrar filas de features: ". Con error,
//     Has devuelve false y ListEffective ("", nil).
type Postgres struct {
	db  *sql.DB
	ttl time.Duration
	// now es el reloj de las dos cachés (WithClock); time.Now por defecto.
	now func() time.Time

	// mu protege las dos cachés. Se suelta ANTES de consultar la BD y se vuelve a tomar para
	// escribir el resultado: dos llamantes que fallan a la vez en la misma clave consultan los
	// dos (el último escribe), lo que es más barato que serializar todas las consultas.
	mu sync.Mutex
	// cache sirve a Has: una entrada por PAR (tenant, feature).
	cache map[cacheKey]cacheEntry
	// effective sirve a ListEffective: una entrada por TENANT. Es una caché SEPARADA a propósito
	// —la de Has no puede responder «¿cuáles tiene?» (solo sabe de los pares que alguien
	// preguntó), y mezclarlas obligaría a invalidar una desde la otra.
	effective map[string]effectiveEntry
}

// cacheKey es la clave de la caché de Has.
type cacheKey struct {
	tenantID string
	feature  string
}

// cacheEntry es una respuesta de Has y su vencimiento.
type cacheEntry struct {
	has       bool
	expiresAt time.Time
}

// effectiveEntry es una respuesta de ListEffective y su vencimiento.
type effectiveEntry struct {
	plan      string
	features  []string
	expiresAt time.Time
}

var _ Resolver = (*Postgres)(nil)

// Option configura el Postgres al construirlo (NewPostgres).
type Option func(*Postgres)

// WithTTL fija el TTL de las dos cachés. Un valor <= 0 se ignora y queda el TTL por defecto, 60 s.
func WithTTL(d time.Duration) Option {
	return func(p *Postgres) {
		if d > 0 {
			p.ttl = d
		}
	}
}

// WithClock inyecta el reloj con el que las cachés deciden si una entrada sigue vigente (D-F2-6:
// la spec lo llama WithReloj; en inglés por E-11, como el WithClock de iam/infra/identity). Sin
// esta opción, el reloj es time.Now. Un now nil se ignora (queda time.Now). Nuevo: el viejo
// llamaba a time.Now en Has y en ListEffective.
func WithClock(now func() time.Time) Option {
	return func(p *Postgres) {
		if now != nil {
			p.now = now
		}
	}
}

// NewPostgres construye el Resolver Postgres sobre db, con las dos cachés vacías, TTL de 60 s y el
// reloj time.Now, y le aplica las opciones en orden. No consulta la BD al construirse.
func NewPostgres(db *sql.DB, opts ...Option) *Postgres {
	p := &Postgres{
		db:        db,
		ttl:       defaultCacheTTL,
		now:       time.Now,
		cache:     make(map[cacheKey]cacheEntry),
		effective: make(map[string]effectiveEntry),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// CacheTTL devuelve el TTL efectivo de las cachés: 60 s por defecto, o el de WithTTL. Lo publica
// GET /api/v1/entitlements como cache_ttl_seconds: es la cota superior de lo que tarda en verse un
// cambio de plan u override (Plan 040 · T2.2).
func (p *Postgres) CacheTTL() time.Duration { return p.ttl }

// Has resuelve el entitlement (ADR-0022): si el tenant tiene override para la feature, manda el
// override; si no, manda el plan del tenant (plan NULL ⇒ 'basic'). Sirve de la caché por par
// (tenant, feature) mientras la entrada siga vigente; un fallo de caché consulta la BD y cachea el
// resultado, también el false. Un error de la BD devuelve (false, err) y no se cachea.
func (p *Postgres) Has(ctx context.Context, tenantID, feature string) (bool, error) {
	k := cacheKey{tenantID: tenantID, feature: feature}
	now := p.now()

	p.mu.Lock()
	if e, ok := p.cache[k]; ok && now.Before(e.expiresAt) {
		p.mu.Unlock()
		return e.has, nil
	}
	p.mu.Unlock()

	has, err := p.lookup(ctx, tenantID, feature)
	if err != nil {
		return false, err
	}

	p.mu.Lock()
	p.cache[k] = cacheEntry{has: has, expiresAt: now.Add(p.ttl)}
	p.mu.Unlock()
	return has, nil
}

// ListEffective devuelve el plan efectivo del tenant (plan NULL ⇒ 'basic') y sus features
// encendidas —las del plan ∪ los overrides que activan, ∖ los que apagan—, en orden alfabético.
// Sirve de la caché por tenant mientras la entrada siga vigente, y entrega siempre una copia de la
// lista. Tenant inexistente ⇒ ("", nil, nil). Un error de la BD devuelve ("", nil, err) y no se
// cachea.
func (p *Postgres) ListEffective(ctx context.Context, tenantID string) (string, []string, error) {
	now := p.now()

	p.mu.Lock()
	if e, ok := p.effective[tenantID]; ok && now.Before(e.expiresAt) {
		plan, features := e.plan, slices.Clone(e.features)
		p.mu.Unlock()
		return plan, features, nil
	}
	p.mu.Unlock()

	plan, features, err := p.listEffective(ctx, tenantID)
	if err != nil {
		return "", nil, err
	}

	p.mu.Lock()
	p.effective[tenantID] = effectiveEntry{plan: plan, features: slices.Clone(features), expiresAt: now.Add(p.ttl)}
	p.mu.Unlock()
	return plan, features, nil
}

// listEffective resuelve en la BD el plan y las features ENCENDIDAS del tenant aplicando la MISMA
// regla que lookup (ADR-0022), pero de una vez para todas las claves: features del plan (plan
// NULL ⇒ 'basic') UNION los overrides que activan, MENOS las que un override desactiva (el
// override gana en ambos sentidos).
//
// Un override con enabled=false EXCLUYE la feature aunque el plan la traiga: por eso el anti-join
// final, y no un simple filtro sobre el UNION. El SQL es literal del viejo.
func (p *Postgres) listEffective(ctx context.Context, tenantID string) (plan string, features []string, err error) {
	err = p.db.QueryRowContext(ctx, `
		SELECT COALESCE(plan_id, 'basic')
		FROM public.tenants
		WHERE id = $1
	`, tenantID).Scan(&plan)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Tenant inexistente: sin plan resoluble ⇒ sin derechos. Es la MISMA respuesta que da Has
		// (su consulta arranca en tenants, así que un tenant que no existe no tiene ninguna
		// feature), no un fallo de infraestructura.
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

	// Orden alfabético GARANTIZADO aquí y no con un ORDER BY: el collation del servidor puede
	// ordenar el guion bajo de otra forma, y el contrato del endpoint promete un orden estable que
	// los tests puedan afirmar.
	slices.Sort(features)
	return plan, features, nil
}

// lookup resuelve el entitlement en la BD (ADR-0022): el override de tenant_features gana; si no
// hay override, mandan las features del plan del tenant (plan NULL ⇒ 'basic'). El SQL es literal
// del viejo.
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
