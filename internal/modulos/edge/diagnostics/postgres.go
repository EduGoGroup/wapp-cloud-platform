// Porta internal/diagnostics/postgres.go @ 8896f13

package diagnostics

import (
	"context"
	"database/sql"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Postgres implementa Store (y BundleReceiver) con SQL raw sobre
// public.tenant_diagnostics_consent y public.diagnostics_bundles (migración 0036).
// La limpieza de bundles vencidos es PEREZOSA (al crear una solicitud y al descargar
// una vencida): no hay jobs ni goroutines de fondo (estilo del TTL del repo).
//
// Son sentencias sueltas sobre el pool, SIN transacción: CreateRequest y GetBundle
// emiten dos cada una, y si la segunda falla la primera ya se aplicó.
//
// ⚠️ El vencimiento se mira con DOS relojes: la purga de CreateRequest y el filtro de
// SaveBundle usan el now() de la base; GetBundle compara expires_at con el reloj del
// PROCESO. Con los dos relojes desfasados, una solicitud puede estar vencida para uno
// y viva para el otro durante ese desfase. Se porta tal cual.
type Postgres struct{}

// NewPostgres construye el store sobre el pool dado. No lo consulta.
func NewPostgres(db *sql.DB) *Postgres {
	panic(pendiente.Implementar("diagnostics.NewPostgres"))
}

var _ Store = (*Postgres)(nil)

// ConsentEnabled implementa Store: default ON (opt-out). La AUSENCIA de fila cuenta
// como consentido (true); una fila enabled=FALSE excluye al tenant (false). Un fallo
// de infraestructura se propaga con false y el prefijo "diagnostics: leer
// consentimiento: " (el gate lo trata como "no verificable" ⇒ el handler no abre la
// capacidad por un error transitorio).
func (p *Postgres) ConsentEnabled(ctx context.Context, tenantID string) (bool, error) {
	panic(pendiente.Implementar("diagnostics.Postgres.ConsentEnabled"))
}

// CreateRequest implementa Store con DOS sentencias, en este orden y sin
// transacción: primero purga las vencidas de cualquier tenant (limpieza perezosa, un
// solo DELETE acotado por el índice de expires_at, contra el now() de la base) y
// después inserta la solicitud pendiente, con requested_at = now() de la base y
// expiresAt tal cual llega.
//
// Si la purga falla no inserta y devuelve "diagnostics: purgar vencidas: …"; si
// falla el INSERT devuelve "diagnostics: crear solicitud: …" y la purga ya se hizo
// (no hay nada que deshacer).
func (p *Postgres) CreateRequest(ctx context.Context, tenantID, sessionID, commandID, requestedBy string, expiresAt time.Time) error {
	panic(pendiente.Implementar("diagnostics.Postgres.CreateRequest"))
}

// DeleteRequest implementa Store: borra la solicitud del tenant (rollback si el push
// del DiagnosticsRequest al Edge falla). Acota por tenant_id (INV-8). Un fallo sale
// como "diagnostics: borrar solicitud: …".
func (p *Postgres) DeleteRequest(ctx context.Context, tenantID, commandID string) error {
	panic(pendiente.Implementar("diagnostics.Postgres.DeleteRequest"))
}

// SaveBundle implementa BundleReceiver: marca ready la solicitud PENDING que case por
// command_id + (tenant_id, session_id) de la identidad mTLS, y aún no vencida según
// el now() de la base. found=false si el UPDATE no toca ninguna fila (bundle
// huérfano/expirado/mismatch ⇒ ignorar).
//
// Errores, con found=false: "diagnostics: guardar bundle: " (el UPDATE) y
// "diagnostics: filas afectadas: " (leer cuántas filas tocó).
func (p *Postgres) SaveBundle(ctx context.Context, tenantID, sessionID, commandID string, b Bundle) (bool, error) {
	panic(pendiente.Implementar("diagnostics.Postgres.SaveBundle"))
}

// GetBundle implementa Store: lee la solicitud del tenant y decide, por este orden:
//
//   - sin fila ⇒ ErrNotFound;
//   - vencida según el reloj del PROCESO (expires_at no es posterior a ahora: el
//     instante exacto del vencimiento ya cuenta como vencida) ⇒ una SEGUNDA sentencia
//     la borra (borrado perezoso) y devuelve ErrExpired (410), esté pendiente o lista;
//   - viva pero sin bundle ⇒ ErrPending (202), sin borrar nada;
//   - viva y lista ⇒ el Record, con el command_id pedido; las columnas del bundle que
//     estén a NULL salen como texto vacío, y un received_at NULL, como el cero.
//
// Son dos sentencias sin transacción: si el borrado perezoso falla se devuelve
// "diagnostics: borrar vencida: …" en lugar de ErrExpired (raro; la fila sigue ahí y
// la siguiente descarga lo reintenta). Un fallo de la lectura sale como "diagnostics:
// leer bundle: …". Con cualquier error el Record vuelve vacío.
func (p *Postgres) GetBundle(ctx context.Context, tenantID, commandID string) (Record, error) {
	panic(pendiente.Implementar("diagnostics.Postgres.GetBundle"))
}
