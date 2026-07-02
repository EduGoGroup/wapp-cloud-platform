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
}

// NewMemoryRepository crea un repositorio en memoria vacío.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		state:  make(map[string]model.Conversation),
		defs:   make(map[string]map[int]model.Flow),
		maxVer: make(map[string]int),
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
