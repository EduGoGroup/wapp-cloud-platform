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
	"fmt"
	"sort"
	"strings"
	"time"
)

// contentKey compone la clave (tenant_id, ref) del índice de contenido, imitando
// la PK compuesta de tenant_content.
func contentKey(tenantID, ref string) string {
	return tenantID + "\x00" + ref
}

// tcMeta son las marcas de tiempo de un blob de tenant_content en el repo memoria
// (Plan 018 · T6): created se fija en el alta, updated en cada escritura.
type tcMeta struct {
	created time.Time
	updated time.Time
}

// GetTenantContent implementa Repository / content.Store: devuelve el blob JSON
// crudo sembrado para (tenantID, ref). ErrTenantContentNotFound si no existe.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: tenant=%s ref=%s"
func (r *MemoryRepository) GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	blob, ok := r.content[contentKey(tenantID, ref)]
	if !ok {
		return nil, fmt.Errorf("%w: tenant=%s ref=%s", ErrTenantContentNotFound, tenantID, ref)
	}
	out := make([]byte, len(blob))
	copy(out, blob)
	return out, nil
}

// SetTenantContent siembra un blob de contenido para (tenantID, ref). Es un
// helper de test (imita el alta en tenant_content); copia el blob para no
// compartir el backing array con el llamante. Sustituye el que hubiera SIN archivar
// versión y SIN fechar: una ref sembrada así sale en ListTenantContent con sus marcas
// de tiempo a cero (o con las que ya tuviera de un UpsertTenantContent anterior).
func (r *MemoryRepository) SetTenantContent(tenantID, ref string, blob []byte) {
	stored := make([]byte, len(blob))
	copy(stored, blob)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.content[contentKey(tenantID, ref)] = stored
}

// UpsertTenantContent inserta o actualiza (upsert por (tenant_id, ref)) el blob de
// contenido de negocio, imitando el upsert en public.tenant_content (Plan 018 ·
// T6). Copia el blob para no compartir el backing array con el llamante; created_at
// solo se fija en el alta, updated_at se refresca en cada escritura, los dos con el
// instante del reloj (SetClock). Acotado al tenant (INV-8). NO archiva versión: eso es
// de ReplaceTenantContentVersioned. No valida que el blob sea JSON. Nunca devuelve
// error.
func (r *MemoryRepository) UpsertTenantContent(ctx context.Context, tenantID, ref string, blob []byte) error {
	stored := make([]byte, len(blob))
	copy(stored, blob)
	k := contentKey(tenantID, ref)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	r.content[k] = stored
	meta := r.contentMeta[k]
	if meta.created.IsZero() {
		meta.created = now
	}
	meta.updated = now
	r.contentMeta[k] = meta
	return nil
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
	if !validVersionSource(source) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidVersionSource, source)
	}
	stored := make([]byte, len(blob))
	copy(stored, blob)
	k := contentKey(tenantID, ref)

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()

	archived := 0
	if current, ok := r.content[k]; ok {
		archived = len(r.contentVersions[k]) + 1
		kept := make([]byte, len(current))
		copy(kept, current)
		r.contentVersions[k] = append(r.contentVersions[k], TenantContentVersion{
			Version: archived, Content: kept, Source: source, CreatedAt: now,
		})
	}

	r.content[k] = stored
	meta := r.contentMeta[k]
	if meta.created.IsZero() {
		meta.created = now
	}
	meta.updated = now
	r.contentMeta[k] = meta
	return archived, nil
}

// TenantContentVersions devuelve las versiones archivadas de (tenantID, ref) en
// orden de archivado. Helper de test (imita un SELECT ... ORDER BY version);
// copia los blobs para no compartir el backing array con el llamante. Sin versiones,
// lista vacía.
func (r *MemoryRepository) TenantContentVersions(tenantID, ref string) []TenantContentVersion {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored := r.contentVersions[contentKey(tenantID, ref)]
	out := make([]TenantContentVersion, 0, len(stored))
	for _, v := range stored {
		content := make([]byte, len(v.Content))
		copy(content, v.Content)
		out = append(out, TenantContentVersion{
			Version: v.Version, Content: content, Source: v.Source, CreatedAt: v.CreatedAt,
		})
	}
	return out
}

// ListTenantContent devuelve las cabeceras (ref + timestamps) de los blobs del
// tenant, ordenadas por ref, imitando el listado por tenant_id de
// public.tenant_content (Plan 018 · T6). Solo del tenant dado (aislamiento INV-8):
// un blob de OTRO tenant nunca aparece.
func (r *MemoryRepository) ListTenantContent(ctx context.Context, tenantID string) ([]TenantContentSummary, error) {
	prefix := tenantID + "\x00"
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TenantContentSummary, 0)
	for k := range r.content {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		meta := r.contentMeta[k]
		out = append(out, TenantContentSummary{
			Ref:       strings.TrimPrefix(k, prefix),
			CreatedAt: meta.created,
			UpdatedAt: meta.updated,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

// DeleteTenantContent borra el blob (tenant_id, ref). Devuelve
// ErrTenantContentNotFound si no existía (simetría con GetTenantContent → 404 en el
// transporte). Acotado al tenant (INV-8).
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: tenant=%s ref=%s"
func (r *MemoryRepository) DeleteTenantContent(ctx context.Context, tenantID, ref string) error {
	k := contentKey(tenantID, ref)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.content[k]; !ok {
		return fmt.Errorf("%w: tenant=%s ref=%s", ErrTenantContentNotFound, tenantID, ref)
	}
	delete(r.content, k)
	delete(r.contentMeta, k)
	return nil
}
