package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
)

// MemoryRepository es una implementación en memoria de Repository, segura para
// concurrencia. Pensada para tests unitarios CI-safe (sin BD) y para los
// dobles de T3/T4. Imita la semántica de la implementación PostgreSQL: clona el
// estado (round-trip JSON) para que el llamante no comparta punteros con lo
// almacenado, igual que ocurriría con una (de)serialización real.
type MemoryRepository struct {
	mu    sync.Mutex
	state map[string]model.Conversation
	// defs indexa (tenant_id, flow_id) → versión → definición.
	defs map[string]map[int]model.Flow
	// maxVer guarda la versión máxima asignada por (tenant_id, flow_id).
	maxVer map[string]int
	// results acumula (append-only) las respuestas de encuesta persistidas por
	// InsertResults; imita survey_results (Plan 014 §10.D). Consultable en tests
	// vía SurveyResults().
	results []SurveyResult
	// flowEvents acumula (append-only) los efectos persistidos por
	// InsertFlowEvent; imita el outbox flow_events (Plan 015 · T2). Consultable en
	// tests vía FlowEvents().
	flowEvents []FlowEvent
	// content indexa (tenant_id, ref) → blob JSON crudo; imita tenant_content
	// (Plan 015 · T2). Sembrable en tests vía SetTenantContent; leído por
	// GetTenantContent.
	content map[string][]byte
}

// NewMemoryRepository crea un repositorio en memoria vacío.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		state:   make(map[string]model.Conversation),
		defs:    make(map[string]map[int]model.Flow),
		maxVer:  make(map[string]int),
		content: make(map[string][]byte),
	}
}

func stateKey(k Key) string {
	return k.TenantID + "\x00" + k.SessionID + "\x00" + k.ContactID
}

func defKey(tenantID, flowID string) string {
	return tenantID + "\x00" + flowID
}

// cloneConversation hace una copia profunda vía JSON (mismo round-trip que la
// persistencia JSONB), para no compartir el mapa Vars con el llamante.
func cloneConversation(c model.Conversation) (model.Conversation, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return model.Conversation{}, err
	}
	var out model.Conversation
	if err := json.Unmarshal(raw, &out); err != nil {
		return model.Conversation{}, err
	}
	return out, nil
}

// Exists implementa Repository.
func (r *MemoryRepository) Exists(_ context.Context, key Key) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.state[stateKey(key)]
	return ok, nil
}

// Load implementa Repository.
func (r *MemoryRepository) Load(_ context.Context, key Key) (model.Conversation, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.state[stateKey(key)]
	if !ok {
		return model.Conversation{}, false, nil
	}
	clone, err := cloneConversation(st)
	if err != nil {
		return model.Conversation{}, false, fmt.Errorf("store: clonar estado: %w", err)
	}
	return clone, true, nil
}

// Save implementa Repository (upsert por la clave conversacional).
func (r *MemoryRepository) Save(_ context.Context, state model.Conversation) error {
	clone, err := cloneConversation(state)
	if err != nil {
		return fmt.Errorf("store: clonar estado: %w", err)
	}
	key := Key{TenantID: state.TenantID, SessionID: state.SessionID, ContactID: state.ContactID}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state[stateKey(key)] = clone
	return nil
}

// MigrateContactID re-clava el estado conversacional del contact_id `from` al
// `to` dentro del tenant (satisface contact.StateMigrator; lo usa el
// MemoryResolver en la fusión, design.md §5). Política de conflicto idéntica al
// PostgresResolver: si `to` ya tiene estado en esa sesión se CONSERVA el de `to`
// (identidad canónica autoritativa) y se descarta el de `from`.
func (r *MemoryRepository) MigrateContactID(_ context.Context, tenantID, from, to string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, st := range r.state {
		if st.TenantID != tenantID || st.ContactID != from {
			continue
		}
		dstKey := stateKey(Key{TenantID: tenantID, SessionID: st.SessionID, ContactID: to})
		if _, clash := r.state[dstKey]; clash {
			// El canónico ya tiene estado en esa sesión: conservar el suyo.
			delete(r.state, k)
			continue
		}
		st.ContactID = to
		delete(r.state, k)
		r.state[dstKey] = st
	}
	return nil
}

// LatestDefinition implementa Repository: devuelve la mayor versión existente.
func (r *MemoryRepository) LatestDefinition(_ context.Context, tenantID, flowID string) (model.Flow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dk := defKey(tenantID, flowID)
	max, ok := r.maxVer[dk]
	if !ok {
		return model.Flow{}, fmt.Errorf("%w: tenant=%s flow=%s", ErrDefinitionNotFound, tenantID, flowID)
	}
	return r.defs[dk][max], nil
}

// GetDefinition implementa Repository: devuelve la definición de la versión
// exacta indicada. ErrDefinitionNotFound si no existe.
func (r *MemoryRepository) GetDefinition(_ context.Context, tenantID, flowID string, version int) (model.Flow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dk := defKey(tenantID, flowID)
	byVer, ok := r.defs[dk]
	if !ok {
		return model.Flow{}, fmt.Errorf("%w: tenant=%s flow=%s", ErrDefinitionNotFound, tenantID, flowID)
	}
	f, ok := byVer[version]
	if !ok {
		return model.Flow{}, fmt.Errorf("%w: tenant=%s flow=%s version=%d", ErrDefinitionNotFound, tenantID, flowID, version)
	}
	return f, nil
}

// InsertDefinition implementa Repository: asigna version = max+1 por
// (tenant_id, flow_id) y devuelve la versión asignada.
func (r *MemoryRepository) InsertDefinition(_ context.Context, tenantID string, f model.Flow) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dk := defKey(tenantID, f.FlowID)
	version := r.maxVer[dk] + 1
	stored := f
	stored.Version = version
	if r.defs[dk] == nil {
		r.defs[dk] = make(map[int]model.Flow)
	}
	r.defs[dk][version] = stored
	r.maxVer[dk] = version
	return version, nil
}

// InsertResults implementa Repository: acumula las respuestas de encuesta en un
// slice interno (append-only), imitando el INSERT en survey_results. len(rows)==0
// es un no-op. Las filas se copian para no compartir el backing array con el
// llamante.
func (r *MemoryRepository) InsertResults(_ context.Context, rows []SurveyResult) error {
	if len(rows) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, rows...)
	return nil
}

// SurveyResults devuelve una copia de las respuestas de encuesta acumuladas por
// InsertResults. Es un helper de test (los tests inspeccionan/agregan el
// resultado); devuelve una copia para no exponer el slice interno.
func (r *MemoryRepository) SurveyResults() []SurveyResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SurveyResult, len(r.results))
	copy(out, r.results)
	return out
}

// contentKey compone la clave (tenant_id, ref) del índice de contenido, imitando
// la PK compuesta de tenant_content.
func contentKey(tenantID, ref string) string {
	return tenantID + "\x00" + ref
}

// InsertFlowEvent implementa Repository: acumula el efecto en un slice interno
// (append-only), imitando el INSERT en el outbox flow_events (Plan 015 · T2). El
// Payload nil se conserva tal cual (la materialización a '{}' es del repo
// Postgres); la copia por valor de la struct no comparte el mapa con el llamante
// solo si este no lo muta, así que se clona el Payload defensivamente.
func (r *MemoryRepository) InsertFlowEvent(_ context.Context, ev FlowEvent) error {
	stored := ev
	if ev.Payload != nil {
		clone := make(map[string]any, len(ev.Payload))
		for k, v := range ev.Payload {
			clone[k] = v
		}
		stored.Payload = clone
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flowEvents = append(r.flowEvents, stored)
	return nil
}

// FlowEvents devuelve una copia de los efectos acumulados por InsertFlowEvent. Es
// un helper de test; devuelve una copia para no exponer el slice interno.
func (r *MemoryRepository) FlowEvents() []FlowEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]FlowEvent, len(r.flowEvents))
	copy(out, r.flowEvents)
	return out
}

// GetTenantContent implementa Repository / content.Store: devuelve el blob JSON
// crudo sembrado para (tenantID, ref). ErrTenantContentNotFound si no existe.
func (r *MemoryRepository) GetTenantContent(_ context.Context, tenantID, ref string) ([]byte, error) {
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
// compartir el backing array con el llamante.
func (r *MemoryRepository) SetTenantContent(tenantID, ref string, blob []byte) {
	stored := make([]byte, len(blob))
	copy(stored, blob)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.content[contentKey(tenantID, ref)] = stored
}
