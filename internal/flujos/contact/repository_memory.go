package contact

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// MemoryResolver es una implementación en memoria de Resolver, segura para
// concurrencia. Pensada para tests CI-safe (sin BD) y para el runtime en
// memoria. Imita la semántica del PostgresResolver: dedup por (tenant, kind,
// value), canónico = el contact_id más antiguo en la fusión, y migración del
// flow_state del huérfano vía el StateMigrator inyectado (design.md §5).
type MemoryResolver struct {
	mu       sync.Mutex
	migrator StateMigrator // opcional: migra flow_state en la fusión; puede ser nil
	seq      int           // orden de alta, para elegir el canónico (más antiguo)
	// refIndex mapea "tenant\x00kind\x00value" → contactID (dedup por ref).
	refIndex map[string]string
	// contacts indexa contactID → estado del contacto.
	contacts map[string]*memContact
}

// memContact es el estado en memoria de un contacto (contact_id + sus refs).
type memContact struct {
	id       string
	tenantID string
	refs     []Ref
	pushName string
	seq      int
}

// NewMemoryResolver crea un resolver en memoria. migrator migra el flow_state
// del contact_id huérfano al canónico durante la fusión (design.md §5); puede
// ser nil si no hay estado que migrar (p. ej. tests que no tocan flow_state).
func NewMemoryResolver(migrator StateMigrator) *MemoryResolver {
	return &MemoryResolver{
		migrator: migrator,
		refIndex: make(map[string]string),
		contacts: make(map[string]*memContact),
	}
}

func refIndexKey(tenantID string, ref Ref) string {
	return tenantID + "\x00" + ref.Kind + "\x00" + ref.Value
}

// Resolve implementa Resolver (design.md §4, §5).
func (r *MemoryResolver) Resolve(ctx context.Context, tenantID string, refs []Ref, pushName string) (string, error) {
	refs = dedupeRefs(refs)
	if len(refs) == 0 {
		return "", ErrNoRefs
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	found := r.distinctContactIDs(tenantID, refs)

	var canonical string
	switch len(found) {
	case 0:
		canonical = r.newContact(tenantID)
	default:
		canonical = r.pickCanonical(found)
		for _, orphan := range found {
			if orphan == canonical {
				continue
			}
			if err := r.fuse(ctx, tenantID, orphan, canonical); err != nil {
				return "", err
			}
		}
	}

	for _, ref := range refs {
		r.attach(tenantID, canonical, ref)
	}
	if pushName != "" {
		r.contacts[canonical].pushName = pushName
	}
	return canonical, nil
}

// distinctContactIDs devuelve, en orden estable, los contact_id distintos ya
// mapeados por alguna de las refs.
func (r *MemoryResolver) distinctContactIDs(tenantID string, refs []Ref) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, ref := range refs {
		if cid, ok := r.refIndex[refIndexKey(tenantID, ref)]; ok {
			if _, dup := seen[cid]; !dup {
				seen[cid] = struct{}{}
				ids = append(ids, cid)
			}
		}
	}
	return ids
}

// pickCanonical elige el contact_id más antiguo (menor seq; desempate por id
// para determinismo), tal como el PostgresResolver usa MIN(created_at).
func (r *MemoryResolver) pickCanonical(ids []string) string {
	canonical := ids[0]
	for _, id := range ids[1:] {
		c, cok := r.contacts[canonical]
		o, ook := r.contacts[id]
		if !ook {
			continue
		}
		if !cok || o.seq < c.seq || (o.seq == c.seq && id < canonical) {
			canonical = id
		}
	}
	return canonical
}

// newContact crea un contact_id opaco nuevo (UUID) vacío de refs.
func (r *MemoryResolver) newContact(tenantID string) string {
	id := uuid.NewString()
	r.seq++
	r.contacts[id] = &memContact{id: id, tenantID: tenantID, seq: r.seq}
	return id
}

// attach ata una ref al contact_id canónico si no lo estaba ya.
func (r *MemoryResolver) attach(tenantID, contactID string, ref Ref) {
	k := refIndexKey(tenantID, ref)
	if _, ok := r.refIndex[k]; ok {
		return
	}
	r.refIndex[k] = contactID
	c := r.contacts[contactID]
	c.refs = append(c.refs, ref)
}

// fuse funde el contact_id huérfano en el canónico: re-apunta sus refs y migra
// su flow_state (design.md §5). Debe correr bajo r.mu.
func (r *MemoryResolver) fuse(ctx context.Context, tenantID, orphan, canonical string) error {
	oc, ok := r.contacts[orphan]
	if !ok {
		return nil
	}
	cc := r.contacts[canonical]
	for _, ref := range oc.refs {
		r.refIndex[refIndexKey(tenantID, ref)] = canonical
		cc.refs = append(cc.refs, ref)
	}
	if oc.pushName != "" && cc.pushName == "" {
		cc.pushName = oc.pushName
	}
	delete(r.contacts, orphan)
	if r.migrator != nil {
		if err := r.migrator.MigrateContactID(ctx, tenantID, orphan, canonical); err != nil {
			return fmt.Errorf("contact: migrar flow_state en fusión: %w", err)
		}
	}
	return nil
}

// Destino implementa Resolver (design.md §10.E).
func (r *MemoryResolver) Destino(_ context.Context, tenantID, contactID string) (Ref, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.contacts[contactID]
	if !ok || c.tenantID != tenantID {
		return Ref{}, fmt.Errorf("%w: %q", ErrContactNotFound, contactID)
	}
	return pickDestino(c.refs)
}
