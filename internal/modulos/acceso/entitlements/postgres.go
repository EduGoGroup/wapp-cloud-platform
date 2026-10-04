// Porta internal/entitlements/postgres.go @ 9a77307

package entitlements

import (
	"context"
	"database/sql"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

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
type Postgres struct{}

var _ Resolver = (*Postgres)(nil)

// Option configura el Postgres al construirlo (NewPostgres).
type Option func(*Postgres)

// WithTTL fija el TTL de las dos cachés. Un valor <= 0 se ignora y queda el TTL por defecto, 60 s.
func WithTTL(d time.Duration) Option {
	panic(pendiente.Implementar("entitlements.WithTTL"))
}

// WithClock inyecta el reloj con el que las cachés deciden si una entrada sigue vigente (D-F2-6:
// la spec lo llama WithReloj; en inglés por E-11, como el WithClock de iam/infra/identity). Sin
// esta opción, el reloj es time.Now. Un now nil se ignora (queda time.Now). Nuevo: el viejo
// llamaba a time.Now en Has y en ListEffective.
func WithClock(now func() time.Time) Option {
	panic(pendiente.Implementar("entitlements.WithClock"))
}

// NewPostgres construye el Resolver Postgres sobre db, con las dos cachés vacías, TTL de 60 s y el
// reloj time.Now, y le aplica las opciones en orden. No consulta la BD al construirse.
func NewPostgres(db *sql.DB, opts ...Option) *Postgres {
	panic(pendiente.Implementar("entitlements.NewPostgres"))
}

// CacheTTL devuelve el TTL efectivo de las cachés: 60 s por defecto, o el de WithTTL. Lo publica
// GET /api/v1/entitlements como cache_ttl_seconds: es la cota superior de lo que tarda en verse un
// cambio de plan u override (Plan 040 · T2.2).
func (p *Postgres) CacheTTL() time.Duration {
	panic(pendiente.Implementar("entitlements.Postgres.CacheTTL"))
}

// Has resuelve el entitlement (ADR-0022): si el tenant tiene override para la feature, manda el
// override; si no, manda el plan del tenant (plan NULL ⇒ 'basic'). Sirve de la caché por par
// (tenant, feature) mientras la entrada siga vigente; un fallo de caché consulta la BD y cachea el
// resultado, también el false. Un error de la BD devuelve (false, err) y no se cachea.
func (p *Postgres) Has(ctx context.Context, tenantID, feature string) (bool, error) {
	panic(pendiente.Implementar("entitlements.Postgres.Has"))
}

// ListEffective devuelve el plan efectivo del tenant (plan NULL ⇒ 'basic') y sus features
// encendidas —las del plan ∪ los overrides que activan, ∖ los que apagan—, en orden alfabético.
// Sirve de la caché por tenant mientras la entrada siga vigente, y entrega siempre una copia de la
// lista. Tenant inexistente ⇒ ("", nil, nil). Un error de la BD devuelve ("", nil, err) y no se
// cachea.
func (p *Postgres) ListEffective(ctx context.Context, tenantID string) (string, []string, error) {
	panic(pendiente.Implementar("entitlements.Postgres.ListEffective"))
}
