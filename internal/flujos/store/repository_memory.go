package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

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
	// contentMeta indexa (tenant_id, ref) → marcas de tiempo del blob de
	// tenant_content (Plan 018 · T6), en paralelo a content. Lo escribe
	// UpsertTenantContent y lo lee ListTenantContent (created/updated_at).
	contentMeta map[string]tcMeta
	// contentVersions indexa (tenant_id, ref) → versiones archivadas en orden de
	// archivado; imita public.tenant_content_versions (Plan 041 · T3.3). Lo escribe
	// ReplaceTenantContentVersioned y lo consultan los tests vía
	// TenantContentVersions.
	contentVersions map[string][]TenantContentVersion
	// intakes indexa intake_id → solicitud; imita public.intakes (Plan 016 · T0).
	// Consultable en tests vía Intakes().
	intakes map[string]Intake
	// intakeItems indexa intake_id → líneas (append-only); imita public.intake_items
	// (Plan 016 · T0). Consultable en tests vía IntakeItems(intakeID).
	intakeItems map[string][]IntakeItem
	// settings indexa tenant_id → config; imita public.tenant_settings (Plan 016 ·
	// T0). Sembrable en tests vía SetTenantSettings; leído por GetTenantSettings
	// (defaults si no hay fila).
	settings map[string]TenantSettings
	// welcomes indexa la clave conversacional → estado de la bienvenida única; imita
	// public.conversation_welcomes (Plan 044 · T1.8-2). Lo escriben TouchContact y
	// MarkWelcomed; los tests lo leen con Welcome(key).
	welcomes map[string]WelcomeMark
}

// NewMemoryRepository crea un repositorio en memoria vacío.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		state:           make(map[string]model.Conversation),
		defs:            make(map[string]map[int]model.Flow),
		maxVer:          make(map[string]int),
		content:         make(map[string][]byte),
		contentMeta:     make(map[string]tcMeta),
		contentVersions: make(map[string][]TenantContentVersion),
		intakes:         make(map[string]Intake),
		intakeItems:     make(map[string][]IntakeItem),
		settings:        make(map[string]TenantSettings),
		welcomes:        make(map[string]WelcomeMark),
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

// Save implementa Repository (upsert por la clave conversacional). Estampa
// UpdatedAt = ahora en cada escritura, igual que la columna updated_at = now() del
// repositorio Postgres, para que el TTL conversacional (Plan 029 · T9) tenga una
// marca real de última actividad.
//
// EventID (el puntero al evento activo, Plan 043 · T1.3) viaja en el clon JSON como
// un campo más, incluido cuando vale "": el upsert lo SOBRESCRIBE siempre, igual que
// el `event_id = EXCLUDED.event_id` del repo Postgres. Es lo que permite APAGAR el
// puntero al cerrar o cancelar un evento; conservar el valor previo dejaría a la
// conversación pegada a un evento muerto solo en los tests.
//
// OwnerEventID (el puntero al evento DUEÑO, Plan 053 · T1.4) no necesita nada aparte
// por la misma razón: el clon JSON copia la estructura ENTERA, así que se sobrescribe
// siempre —incluido a ""— igual que el `owner_event_id = EXCLUDED.owner_event_id` del
// repo Postgres. Este gemelo no tiene la trampa del ON CONFLICT porque no reconstruye
// la fila campo a campo: la reemplaza.
func (r *MemoryRepository) Save(_ context.Context, state model.Conversation) error {
	clone, err := cloneConversation(state)
	if err != nil {
		return fmt.Errorf("store: clonar estado: %w", err)
	}
	clone.UpdatedAt = time.Now()
	key := Key{TenantID: state.TenantID, SessionID: state.SessionID, ContactID: state.ContactID}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state[stateKey(key)] = clone
	return nil
}

// Delete implementa Repository: elimina el estado de la clave (idempotente; si no
// existe es un no-op sin error, misma semántica que el DELETE sin filas).
func (r *MemoryRepository) Delete(_ context.Context, key Key) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.state, stateKey(key))
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
	now := time.Now()
	for _, row := range rows {
		// Fecha la fila igual que el DEFAULT now() de la columna. Sin esto, el doble
		// devolvería un created_at CERO donde Postgres devuelve una fecha, y quien
		// acote «las respuestas de esta pasada» por fecha (el resumen del rescate)
		// vería en sus tests unitarios un filtro que deja pasar todo y en producción
		// uno que filtra. Es la misma imitación de un DEFAULT que ya hace AddedAt en
		// las líneas; la ESCRITURA real de survey_results no cambia.
		if row.CreatedAt.IsZero() {
			row.CreatedAt = now
		}
		r.results = append(r.results, row)
	}
	return nil
}

// ListResults implementa Repository: filtra las respuestas por (tenant, contacto,
// flujo) conservando el orden de escritura, que es el cronológico que devuelve el
// PostgresRepository (allí, ORDER BY created_at, id).
//
// Devuelve la lista VACÍA y no nil cuando no hay nada, igual que el camino Postgres:
// un test que distinga `nil` de `[]` sobre el doble estaría comprobando algo que la
// implementación real no promete.
func (r *MemoryRepository) ListResults(_ context.Context, tenantID, contactID, flowID string) ([]SurveyResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SurveyResult, 0)
	for _, s := range r.results {
		if s.TenantID == tenantID && s.ContactID == contactID && s.FlowID == flowID {
			out = append(out, s)
		}
	}
	return out, nil
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

// tcMeta son las marcas de tiempo de un blob de tenant_content en el repo memoria
// (Plan 018 · T6): created se fija en el alta, updated en cada escritura.
type tcMeta struct {
	created time.Time
	updated time.Time
}

// UpsertTenantContent inserta o actualiza (upsert por (tenant_id, ref)) el blob de
// contenido de negocio, imitando el upsert en public.tenant_content (Plan 018 ·
// T6). Copia el blob para no compartir el backing array con el llamante; created_at
// solo se fija en el alta, updated_at se refresca en cada escritura. Acotado al
// tenant (INV-8).
func (r *MemoryRepository) UpsertTenantContent(_ context.Context, tenantID, ref string, blob []byte) error {
	stored := make([]byte, len(blob))
	copy(stored, blob)
	k := contentKey(tenantID, ref)
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
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
func (r *MemoryRepository) ReplaceTenantContentVersioned(_ context.Context, tenantID, ref string, blob []byte, source string) (int, error) {
	if !validVersionSource(source) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidVersionSource, source)
	}
	stored := make([]byte, len(blob))
	copy(stored, blob)
	k := contentKey(tenantID, ref)
	now := time.Now().UTC()

	r.mu.Lock()
	defer r.mu.Unlock()

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
// copia los blobs para no compartir el backing array con el llamante.
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
func (r *MemoryRepository) ListTenantContent(_ context.Context, tenantID string) ([]TenantContentSummary, error) {
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
func (r *MemoryRepository) DeleteTenantContent(_ context.Context, tenantID, ref string) error {
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

// UpsertIntake implementa Repository: inserta o actualiza (por ID) la solicitud,
// imitando el upsert en public.intakes (Plan 016 · T0). Idempotente por o.ID: en el
// alta fija created_at/updated_at a now(); en la actualización preserva el
// created_at almacenado y refresca updated_at (misma semántica que el DEFAULT +
// ON CONFLICT del Postgres).
func (r *MemoryRepository) UpsertIntake(_ context.Context, o Intake) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if prev, ok := r.intakes[o.ID]; ok {
		o.CreatedAt = prev.CreatedAt
		// Misma semántica que el COALESCE(intakes.event_id, EXCLUDED.event_id) del
		// Postgres (D-043.21): un padre ya declarado JAMÁS se pisa; un vacío legado
		// sí se estampa. Si aquí se sobrescribiera, un test unitario daría por
		// buena una escritura que en producción no puede ocurrir.
		if prev.EventID != "" {
			o.EventID = prev.EventID
		}
	} else {
		o.CreatedAt = now
	}
	o.UpdatedAt = now
	r.intakes[o.ID] = o
	return nil
}

// GetIntakeByEvent implementa IntakeReader: la solicitud del tenant que declara
// `eventID` como padre, SIN filtro de estado (D-044.46, hallazgo #24).
//
// En producción el índice único parcial intakes_event_id_uidx garantiza que sea a lo
// sumo UNA; aquí no hay índice que lo imponga, así que ante varias se devuelve la
// MÁS ANTIGUA (created_at, id) — la que ya estaba, que es justo la que los dos
// productores tienen que reusar. Sin ese desempate el resultado dependería del orden
// de recorrido del mapa y un test podría salir verde o rojo según la vuelta.
//
// eventID vacío ⇒ no hay nada que buscar (misma puerta que AbandonByEvent): la
// cadena vacía es el NULL de las filas legadas pre-0054, y "todas las legadas" no es
// la respuesta a "¿qué tiene ESTE evento?".
//
// ⚠️ ASIMETRÍA CONOCIDA CON EL POSTGRES, y escrita para que nadie la descubra
// depurando: allí la columna es de tipo `uuid` y un eventID que no parsea sale
// found=false por el guard; aquí los ids son cadenas opacas y se comparan tal cual.
// Solo diverge para ids que en la base NO PUEDEN existir.
func (r *MemoryRepository) GetIntakeByEvent(_ context.Context, tenantID, eventID string) (Intake, bool, error) {
	if eventID == "" {
		return Intake{}, false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var (
		out   Intake
		found bool
	)
	for _, o := range r.intakes {
		if o.TenantID != tenantID || o.EventID != eventID {
			continue
		}
		if !found || o.CreatedAt.Before(out.CreatedAt) ||
			(o.CreatedAt.Equal(out.CreatedAt) && o.ID < out.ID) {
			out, found = o, true
		}
	}
	return out, found, nil
}

// ReplaceIntakeItems implementa Repository: deja las líneas de CLIENTE de la
// solicitud exactamente en `items`, imitando el DELETE+INSERT transaccional del
// PostgresRepository (Plan 043 · Ola 3). len(items)==0 BORRA las de cliente, igual
// que el SQL. Fija IntakeID y AddedAt (DEFAULT now()) en cada línea; copia por valor
// (structs sin punteros) para no compartir estado.
//
// Que este adaptador reemplace y el otro también NO es cosmética: si aquí siguiera
// acumulando, los tests unitarios verían un pedido sin duplicados que en Postgres
// SÍ los tendría, y estarían mintiendo justo sobre lo que esta tarea arregla.
func (r *MemoryRepository) ReplaceIntakeItems(_ context.Context, intakeID string, items []IntakeItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replaceIntakeItemsLocked(intakeID, items)
	return nil
}

// replaceIntakeItemsLocked es el reemplazo con el mutex YA tomado: lo comparten
// ReplaceIntakeItems y CloseIntake, que necesita hacerlo dentro de su propia sección
// crítica (el equivalente de su transacción).
//
// Conserva las líneas de LA PLATAFORMA (prefijo reservado) al frente, que es donde
// las deja Postgres cuando existen: se escriben antes de cualquier reemplazo posterior
// y la lectura ordena por added_at.
func (r *MemoryRepository) replaceIntakeItemsLocked(intakeID string, items []IntakeItem) {
	now := time.Now()
	kept := make([]IntakeItem, 0, len(items))
	for _, it := range r.intakeItems[intakeID] {
		if strings.HasPrefix(it.SKU, reservedSKUPrefix) {
			kept = append(kept, it)
		}
	}
	for _, it := range items {
		it.IntakeID = intakeID
		if it.AddedAt.IsZero() {
			it.AddedAt = now
		}
		kept = append(kept, it)
	}
	if len(kept) == 0 {
		delete(r.intakeItems, intakeID)
		return
	}
	r.intakeItems[intakeID] = kept
}

// GetOpenIntake implementa Repository: devuelve la solicitud "open" del contacto para
// (tenantID, contactID); found=false sin error si no hay (Plan 016 · T2/T3).
// Identidad de negocio: UNA solicitud "open" por (tenant_id, contact_id) (design.md
// §3.4), así que devuelve la primera coincidente.
func (r *MemoryRepository) GetOpenIntake(_ context.Context, tenantID, contactID string) (Intake, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, o := range r.intakes {
		if o.TenantID == tenantID && o.ContactID == contactID && o.Status == "open" {
			return o, true, nil
		}
	}
	return Intake{}, false, nil
}

// ListIntakeItems implementa Repository: devuelve las líneas de la solicitud en el
// orden en que se escribieron (el del carrito), que es el mismo que da el
// PostgresRepository al ordenar por (added_at, id).
//
// Valida el UUID aunque aquí las claves sean un mapa de cadenas y no haga ninguna
// falta técnica: el camino Postgres rechaza un id malformado y los dos adaptadores
// tienen que contestar lo mismo a la misma pregunta. Si aquí un `""` devolviera «sin
// líneas», un test unitario daría por bueno un resumen vacío que en producción sería
// un error.
func (r *MemoryRepository) ListIntakeItems(_ context.Context, intakeID string) ([]IntakeItem, error) {
	if _, err := uuid.Parse(intakeID); err != nil {
		return nil, fmt.Errorf("store: listar líneas de solicitud: id %q inválido: %w", intakeID, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	src := r.intakeItems[intakeID]
	out := make([]IntakeItem, len(src))
	copy(out, src)
	return out, nil
}

// MarkIntakeStatus implementa Repository: transiciona el estado de la solicitud (por
// ID) y fija su total, refrescando updated_at (Plan 016 · T2/T3). Si la solicitud no
// existe es un no-op sin error (misma semántica que el UPDATE sin filas).
func (r *MemoryRepository) MarkIntakeStatus(_ context.Context, intakeID, status string, total float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o, ok := r.intakes[intakeID]; ok {
		o.Status = status
		o.Total = total
		o.UpdatedAt = time.Now()
		r.intakes[intakeID] = o
	}
	return nil
}

// CloseIntake implementa Repository: cierra atómicamente (bajo el mutex del repo) la
// solicitud "open" del contacto —o crea una "closed" si no la hubiera— e inserta sus
// líneas, imitando la transacción del PostgresRepository (Plan 027 · Ola 1 · T4).
// Devuelve el id de la solicitud cerrada, igual que el store real.
func (r *MemoryRepository) CloseIntake(_ context.Context, in IntakeClose) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var intakeID string
	for id, o := range r.intakes {
		if o.TenantID == in.TenantID && o.ContactID == in.ContactID && o.Status == "open" {
			intakeID = id
			o.Status = "closed"
			o.Total = in.Total
			o.CustomerNote = in.CustomerNote
			// COALESCE(event_id, $n), como el UPDATE del Postgres (D-043.21): el
			// cierre rellena un NULL legado y jamás pisa un padre ya declarado.
			if o.EventID == "" {
				o.EventID = in.EventID
			}
			o.UpdatedAt = now
			r.intakes[id] = o
			break
		}
	}
	if intakeID == "" {
		intakeID = uuid.NewString()
		r.intakes[intakeID] = Intake{
			ID:           intakeID,
			TenantID:     in.TenantID,
			ContactID:    in.ContactID,
			SessionID:    in.SessionID,
			Status:       "closed",
			Total:        in.Total,
			CustomerNote: in.CustomerNote,
			EventID:      in.EventID,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
	}
	// REEMPLAZO, no acumulación: la solicitud puede llegar al cierre con las líneas
	// que la proyección de item_added ya materializó (mismo motivo, y mismo orden de
	// operaciones, que en el PostgresRepository).
	r.replaceIntakeItemsLocked(intakeID, in.Items)
	return intakeID, nil
}

// GetTenantSettings implementa Repository: devuelve la config sembrada para
// tenantID o DefaultTenantSettings si no hay fila, SIN error (design.md §9.E/§9.G).
//
// Los DOS caminos son los mismos que en Postgres, incluida la parte que muerde: una
// config SEMBRADA se devuelve TAL CUAL, así que un EventInactivityTTL de 0 sembrado
// sale 0 —el override «sin vencimiento» de D-043.7— y no se convierte en las 2 h del
// default. Los defaults salen de la MISMA función que usa el repo Postgres, de modo
// que los dos no pueden divergir.
func (r *MemoryRepository) GetTenantSettings(_ context.Context, tenantID string) (TenantSettings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.settings[tenantID]; ok {
		return s, nil
	}
	return DefaultTenantSettings(tenantID), nil
}

// Intakes devuelve una copia de las solicitudes acumuladas por UpsertIntake. Es un
// helper de test; devuelve una copia para no exponer el mapa interno.
func (r *MemoryRepository) Intakes() []Intake {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Intake, 0, len(r.intakes))
	for _, o := range r.intakes {
		out = append(out, o)
	}
	return out
}

// IntakeItems devuelve una copia de las líneas persistidas para intakeID por
// ReplaceIntakeItems / CloseIntake, EN EL ORDEN en que se escribieron (que es el
// orden del carrito, y el que da la lectura real por added_at, id). Es un helper de
// test; devuelve una copia para no exponer el slice interno.
func (r *MemoryRepository) IntakeItems(intakeID string) []IntakeItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	src := r.intakeItems[intakeID]
	out := make([]IntakeItem, len(src))
	copy(out, src)
	return out
}

// SetTenantSettings siembra la config del carrito para un tenant. Es un helper de
// test (imita el alta en tenant_settings).
//
// SIEMBRA LA FILA LITERALMENTE, como un INSERT que nombra TODAS las columnas: los
// campos que no rellenes quedan en el cero de Go, NO en el DEFAULT de su columna. La
// diferencia importa desde el Plan 043: un TenantSettings a medio construir deja
// EventInactivityTTL en 0, que aquí significa «sin vencimiento» mientras que la misma
// fila creada en Postgres sin nombrar la columna traería 2 h. Para sembrar un tenant
// realista parte de DefaultTenantSettings(tenantID) y cambia lo que el test necesite;
// el 0 déjalo solo cuando el 0 sea lo que se está probando.
//
// 🔴 Desde el Plan 044 · T1.2 la trampa tiene un SEGUNDO campo, y es peor que el
// primero: AggregationWindow a 0 significa FLUSH INMEDIATO (un pipeline por mensaje,
// agregación apagada), no «45 s por defecto». Un test que siembre a mano y no la
// nombre estará probando el agregador con la ventana desactivada y verá N jobs donde
// la producción vería UNO. Este repo NO parchea ceros a propósito (misma regla que el
// Postgres): parte de DefaultTenantSettings.
func (r *MemoryRepository) SetTenantSettings(s TenantSettings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings[s.TenantID] = s
}

// ---------------------------------------------------------------------------
// LA BIENVENIDA ÚNICA (Plan 044 · T1.8-2, D6) — el gemelo en memoria
// ---------------------------------------------------------------------------

// TouchContact implementa WelcomeStore en memoria con la MISMA semántica que el
// repo Postgres, incluida la que muerde: devuelve el estado ANTERIOR al toque.
//
// El gemelo Postgres necesita un CTE para conseguirlo (un `RETURNING` le daría la
// fila ya actualizada); aquí basta con copiar antes de escribir. Que se consiga de
// dos maneras distintas es irrelevante: lo que tiene que coincidir es lo que ve el
// llamante, y lo vigila reads_conformance_test.go.
func (r *MemoryRepository) TouchContact(_ context.Context, key Key, now time.Time) (WelcomeMark, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := stateKey(key)
	previo := r.welcomes[k] // cero si no existía: «nunca habló, nunca se le saludó».
	actual := previo
	actual.LastIncomingAt = now
	r.welcomes[k] = actual
	return previo, nil
}

// MarkWelcomed implementa WelcomeStore en memoria con el MISMO compare-and-set que
// el SQL (`welcomed_at IS NOT DISTINCT FROM $testigo`): si la marca cambió desde que
// el llamante la leyó, no se escribe y se devuelve false SIN error.
//
// ⚠️ Si NO hay fila devuelve false, igual que el UPDATE de Postgres (que afectaría 0
// filas). No la crea: MarkWelcomed solo puede llegar después de un TouchContact, que
// es quien la crea, y fabricarla aquí taparía un orden de llamadas equivocado.
func (r *MemoryRepository) MarkWelcomed(_ context.Context, key Key, testigo WelcomeMark, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := stateKey(key)
	actual, ok := r.welcomes[k]
	if !ok || !actual.WelcomedAt.Equal(testigo.WelcomedAt) {
		return false, nil
	}
	actual.WelcomedAt = now
	r.welcomes[k] = actual
	return true, nil
}

// Welcome devuelve el estado de la bienvenida de una conversación. Helper de test:
// es el equivalente a mirar la fila de conversation_welcomes con SQL directo.
func (r *MemoryRepository) Welcome(key Key) WelcomeMark {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.welcomes[stateKey(key)]
}
