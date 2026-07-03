package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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
	// orders indexa order_id → orden; imita public.orders (Plan 016 · T0).
	// Consultable en tests vía Orders().
	orders map[string]Order
	// orderItems indexa order_id → líneas (append-only); imita public.order_items
	// (Plan 016 · T0). Consultable en tests vía OrderItems(orderID).
	orderItems map[string][]OrderItem
	// settings indexa tenant_id → config; imita public.tenant_settings (Plan 016 ·
	// T0). Sembrable en tests vía SetTenantSettings; leído por GetTenantSettings
	// (defaults si no hay fila).
	settings map[string]TenantSettings
}

// NewMemoryRepository crea un repositorio en memoria vacío.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		state:      make(map[string]model.Conversation),
		defs:       make(map[string]map[int]model.Flow),
		maxVer:     make(map[string]int),
		content:    make(map[string][]byte),
		orders:     make(map[string]Order),
		orderItems: make(map[string][]OrderItem),
		settings:   make(map[string]TenantSettings),
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

// ListDefinitions devuelve el resumen de cada flujo del tenant (flow_id + última
// versión), ordenado por flow_id (Plan 018 · T5). Acota por tenant_id (INV-8). El
// repositorio en memoria no rastrea created_at: FlowSummary.CreatedAt queda en cero.
func (r *MemoryRepository) ListDefinitions(_ context.Context, tenantID string) ([]FlowSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prefix := tenantID + "\x00"
	out := make([]FlowSummary, 0)
	for dk, max := range r.maxVer {
		if !strings.HasPrefix(dk, prefix) {
			continue
		}
		out = append(out, FlowSummary{FlowID: strings.TrimPrefix(dk, prefix), Version: max})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FlowID < out[j].FlowID })
	return out, nil
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

// UpsertOrder implementa Repository: inserta o actualiza (por ID) la orden,
// imitando el upsert en public.orders (Plan 016 · T0). Idempotente por o.ID: en el
// alta fija created_at/updated_at a now(); en la actualización preserva el
// created_at almacenado y refresca updated_at (misma semántica que el DEFAULT +
// ON CONFLICT del Postgres).
func (r *MemoryRepository) UpsertOrder(_ context.Context, o Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if prev, ok := r.orders[o.ID]; ok {
		o.CreatedAt = prev.CreatedAt
	} else {
		o.CreatedAt = now
	}
	o.UpdatedAt = now
	r.orders[o.ID] = o
	return nil
}

// InsertOrderItems implementa Repository: acumula las líneas de la orden en un
// slice interno (append-only), imitando el INSERT en public.order_items (Plan 016
// · T0). len(items)==0 es un no-op. Fija OrderID y AddedAt (DEFAULT now()) en cada
// línea; copia por valor (structs sin punteros) para no compartir estado.
func (r *MemoryRepository) InsertOrderItems(_ context.Context, orderID string, items []OrderItem) error {
	if len(items) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for _, it := range items {
		it.OrderID = orderID
		if it.AddedAt.IsZero() {
			it.AddedAt = now
		}
		r.orderItems[orderID] = append(r.orderItems[orderID], it)
	}
	return nil
}

// GetOpenOrder implementa Repository: devuelve la orden "open" del contacto para
// (tenantID, contactID); found=false sin error si no hay (Plan 016 · T2/T3).
// Identidad de negocio: UNA orden "open" por (tenant_id, contact_id) (design.md
// §3.4), así que devuelve la primera coincidente.
func (r *MemoryRepository) GetOpenOrder(_ context.Context, tenantID, contactID string) (Order, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, o := range r.orders {
		if o.TenantID == tenantID && o.ContactID == contactID && o.Status == "open" {
			return o, true, nil
		}
	}
	return Order{}, false, nil
}

// MarkOrderStatus implementa Repository: transiciona el estado de la orden (por
// ID) y fija su total, refrescando updated_at (Plan 016 · T2/T3). Si la orden no
// existe es un no-op sin error (misma semántica que el UPDATE sin filas).
func (r *MemoryRepository) MarkOrderStatus(_ context.Context, orderID, status string, total float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o, ok := r.orders[orderID]; ok {
		o.Status = status
		o.Total = total
		o.UpdatedAt = time.Now()
		r.orders[orderID] = o
	}
	return nil
}

// GetTenantSettings implementa Repository: devuelve la config sembrada para
// tenantID o los DEFAULTS (DefaultPageSize, DefaultOrderTTL) si no hay fila, SIN
// error (design.md §9.E/§9.G).
func (r *MemoryRepository) GetTenantSettings(_ context.Context, tenantID string) (TenantSettings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.settings[tenantID]; ok {
		return s, nil
	}
	return TenantSettings{
		TenantID: tenantID,
		PageSize: DefaultPageSize,
		OrderTTL: DefaultOrderTTL,
	}, nil
}

// Orders devuelve una copia de las órdenes acumuladas por UpsertOrder. Es un
// helper de test; devuelve una copia para no exponer el mapa interno.
func (r *MemoryRepository) Orders() []Order {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Order, 0, len(r.orders))
	for _, o := range r.orders {
		out = append(out, o)
	}
	return out
}

// OrderItems devuelve una copia de las líneas persistidas para orderID por
// InsertOrderItems. Es un helper de test; devuelve una copia para no exponer el
// slice interno.
func (r *MemoryRepository) OrderItems(orderID string) []OrderItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	src := r.orderItems[orderID]
	out := make([]OrderItem, len(src))
	copy(out, src)
	return out
}

// SetTenantSettings siembra la config del carrito para un tenant. Es un helper de
// test (imita el alta en tenant_settings).
func (r *MemoryRepository) SetTenantSettings(s TenantSettings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings[s.TenantID] = s
}
