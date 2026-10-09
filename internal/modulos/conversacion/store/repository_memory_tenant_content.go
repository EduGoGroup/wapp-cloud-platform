// Porta internal/flujos/store/repository_memory.go @ c0c0c03
//
// Trozo de repository_memory.go (05 E-13): el contenido de negocio por tenant
// (tenant_content) y su versionado (tenant_content_versions) en el gemelo en memoria.
// Las reglas comunes del gemelo están en la cabecera de repository_memory.go.
//
// El gemelo guarda el blob BYTE A BYTE; Postgres lo guarda en JSONB y lo devuelve
// re-serializado. Quien compare los dos adaptadores compara el CONTENIDO del JSON.

package store

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// GetTenantContent implementa Repository / content.Store: devuelve el blob JSON
// crudo sembrado para (tenantID, ref). ErrTenantContentNotFound si no existe.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: tenant=%s ref=%s"
func (r *MemoryRepository) GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error) {
	panic(pendiente.Implementar("store.MemoryRepository.GetTenantContent"))
}

// SetTenantContent siembra un blob de contenido para (tenantID, ref). Es un
// helper de test (imita el alta en tenant_content); copia el blob para no
// compartir el backing array con el llamante. Sustituye el que hubiera SIN archivar
// versión y SIN fechar: una ref sembrada así sale en ListTenantContent con sus marcas
// de tiempo a cero (o con las que ya tuviera de un UpsertTenantContent anterior).
func (r *MemoryRepository) SetTenantContent(tenantID, ref string, blob []byte) {
	panic(pendiente.Implementar("store.MemoryRepository.SetTenantContent"))
}

// UpsertTenantContent inserta o actualiza (upsert por (tenant_id, ref)) el blob de
// contenido de negocio, imitando el upsert en public.tenant_content (Plan 018 ·
// T6). Copia el blob para no compartir el backing array con el llamante; created_at
// solo se fija en el alta, updated_at se refresca en cada escritura, los dos con el
// instante del reloj (SetClock). Acotado al tenant (INV-8). NO archiva versión: eso es
// de ReplaceTenantContentVersioned. No valida que el blob sea JSON. Nunca devuelve
// error.
func (r *MemoryRepository) UpsertTenantContent(ctx context.Context, tenantID, ref string, blob []byte) error {
	panic(pendiente.Implementar("store.MemoryRepository.UpsertTenantContent"))
}

// ReplaceTenantContentVersioned implementa TenantContentVersioner en memoria:
// archiva el blob vigente como la siguiente versión y escribe el nuevo, todo bajo
// EL MISMO mutex. Es la contraparte fiel de la transacción de Postgres: nadie
// puede observar el estado intermedio (versión escrita, contenido aún viejo).
//
// Sin blob vigente NO archiva y devuelve 0, igual que el camino real: la versión 1
// nace del segundo import sobre una ref (D-041.8).
//
// Con blob vigente lo archiva como la versión SIGUIENTE de ese (tenant, ref) —el blob
// VIEJO, con `source` y el instante del reloj— y devuelve ese número. La numeración es
// por (tenant, ref) y sobrevive a un DeleteTenantContent, que no borra versiones. El
// blob nuevo queda vigente con created_at intacto (o el reloj, en el alta) y
// updated_at = el reloj.
//
// Una procedencia que no sea una de las tres VersionSource* devuelve (0,
// ErrInvalidVersionSource) SIN escribir nada.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: %q"
func (r *MemoryRepository) ReplaceTenantContentVersioned(ctx context.Context, tenantID, ref string, blob []byte, source string) (int, error) {
	panic(pendiente.Implementar("store.MemoryRepository.ReplaceTenantContentVersioned"))
}

// TenantContentVersions devuelve las versiones archivadas de (tenantID, ref) en
// orden de archivado. Helper de test (imita un SELECT ... ORDER BY version);
// copia los blobs para no compartir el backing array con el llamante. Sin versiones,
// lista vacía.
func (r *MemoryRepository) TenantContentVersions(tenantID, ref string) []TenantContentVersion {
	panic(pendiente.Implementar("store.MemoryRepository.TenantContentVersions"))
}

// ListTenantContent devuelve las cabeceras (ref + timestamps) de los blobs del
// tenant, ordenadas por ref, imitando el listado por tenant_id de
// public.tenant_content (Plan 018 · T6). Solo del tenant dado (aislamiento INV-8):
// un blob de OTRO tenant nunca aparece.
func (r *MemoryRepository) ListTenantContent(ctx context.Context, tenantID string) ([]TenantContentSummary, error) {
	panic(pendiente.Implementar("store.MemoryRepository.ListTenantContent"))
}

// DeleteTenantContent borra el blob (tenant_id, ref). Devuelve
// ErrTenantContentNotFound si no existía (simetría con GetTenantContent → 404 en el
// transporte). Acotado al tenant (INV-8).
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: tenant=%s ref=%s"
func (r *MemoryRepository) DeleteTenantContent(ctx context.Context, tenantID, ref string) error {
	panic(pendiente.Implementar("store.MemoryRepository.DeleteTenantContent"))
}
