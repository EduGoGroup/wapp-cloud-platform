// Porta internal/flujos/store/repository_memory.go @ c0c0c03
//
// El fichero viejo medía 817 líneas y aquí NACE PARTIDO por tema (05 E-13), solo
// repartiendo declaraciones:
//
//   - repository_memory.go (este): el tipo, el constructor, el reloj inyectable y lo
//     que no es de ningún otro tema: el estado conversacional (con MigrateContactID),
//     las definiciones, las encuestas y el outbox de efectos.
//   - repository_memory_tenant_content.go: el contenido por tenant y sus versiones.
//   - repository_memory_intakes.go: las solicitudes del carrito y sus líneas.
//   - repository_memory_settings.go: la configuración por tenant y la bienvenida única.
//
// Reglas que valen para TODOS los ficheros del gemelo:
//
//   - UN SOLO RELOJ (SetClock) fecha todo lo que el adaptador Postgres fecha con el
//     now() de su sentencia o de su transacción. El viejo llamaba a time.Now() en ocho
//     sitios (Save, InsertResults, UpsertTenantContent, ReplaceTenantContentVersioned,
//     UpsertIntake, el reemplazo de líneas, MarkIntakeStatus y CloseIntake): aquí los
//     ocho salen del reloj inyectado, y un test fecha y cruza plazos sin esperar;
//   - cada método es atómico (un mutex): se puede usar desde varias goroutines;
//   - nada de lo que entra o sale comparte memoria con el llamante: los mapas, los
//     blobs y los slices se copian en los dos sentidos;
//   - no hay integridad referencial ni tipos de columna: acepta ids que no son UUID,
//     tenants que no existen y filas sin evento padre. Donde eso cambia una respuesta
//     observable, el método lo dice;
//   - ningún método devuelve un error de infraestructura: los únicos errores son los
//     que el contrato de cada método nombra.
//
// Que el gemelo contesta lo mismo que Postgres lo comprueba storehelpertest.Contrato,
// la MISMA suite que corre contra el adaptador real (test/procesos).

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// MemoryRepository es una implementación en memoria de Repository, WelcomeStore y
// TenantContentVersioner, segura para concurrencia. Pensada para tests unitarios
// CI-safe (sin BD) y para los dobles del runtime. Imita la semántica de la
// implementación PostgreSQL: clona el estado (round-trip JSON) para que el llamante
// no comparta punteros con lo almacenado, igual que ocurriría con una
// (de)serialización real.
//
// Lo que imita: public.flow_state, public.flow_definitions, survey_results,
// public.flow_events, public.tenant_content y public.tenant_content_versions,
// public.intakes y public.intake_items, public.tenant_settings y
// public.conversation_welcomes.
//
// Además de los puertos, ofrece mutadores y miradores que el adaptador Postgres NO
// tiene, porque allí se siembra y se mira por SQL: SetClock, SetTenantContent,
// SetTenantSettings, FlowEvents, SurveyResults, TenantContentVersions, Intakes,
// IntakeItems, Welcome y MigrateContactID. Todos menos el último los usan solo los
// tests; MigrateContactID satisface contact.StateMigrator y lo llama el resolver de
// contactos EN MEMORIA durante la fusión (que también es un doble de tests: el
// resolver Postgres migra el estado en SQL, dentro de su transacción).
type MemoryRepository struct {
	mu sync.Mutex
	// now es el reloj con el que el repositorio fecha lo que escribe (SetClock). Se lee SIEMPRE con
	// el mutex tomado.
	now   func() time.Time
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

// NewMemoryRepository crea un repositorio en memoria vacío y listo para usar: ningún
// tenant tiene conversaciones, definiciones, contenido, solicitudes, configuración ni
// bienvenidas, y el reloj es el del proceso (time.Now) mientras no se inyecte otro con
// SetClock.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		now:             time.Now,
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

// SetClock fija el reloj del repositorio. Desde la llamada, todo lo que el repositorio
// fecha sale de `now`: el UpdatedAt de una conversación, el CreatedAt de una respuesta
// de encuesta, las marcas de un blob de contenido y de sus versiones, el
// CreatedAt/UpdatedAt de una solicitud y el AddedAt de una línea. No remarca nada de lo
// ya guardado. Un `now` nil se IGNORA (queda el reloj anterior). Los instantes de la
// bienvenida NO salen de aquí: TouchContact y MarkWelcomed reciben su `now` del
// llamante. Mutador de tests; no es parte de ningún puerto.
//
// Nuevo respecto al viejo, que llamaba a time.Now() en ocho sitios (precedente:
// intakes.MemoryStore.SetClock).
func (r *MemoryRepository) SetClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now != nil {
		r.now = now
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
func (r *MemoryRepository) Exists(ctx context.Context, key Key) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.state[stateKey(key)]
	return ok, nil
}

// Load implementa Repository.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: clonar estado: %w"
func (r *MemoryRepository) Load(ctx context.Context, key Key) (model.Conversation, bool, error) {
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
// UpdatedAt = el instante del reloj (SetClock) en cada escritura, igual que la columna
// updated_at = now() del repositorio Postgres, para que el TTL conversacional (Plan
// 029 · T9) tenga una marca real de última actividad. El UpdatedAt del argumento se
// ignora. La fila se REEMPLAZA entera: lo que el estado nuevo no trae (una clave de
// Vars, el LastWaMessageID) desaparece.
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
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: clonar estado: %w"
func (r *MemoryRepository) Save(ctx context.Context, state model.Conversation) error {
	clone, err := cloneConversation(state)
	if err != nil {
		return fmt.Errorf("store: clonar estado: %w", err)
	}
	key := Key{TenantID: state.TenantID, SessionID: state.SessionID, ContactID: state.ContactID}
	r.mu.Lock()
	defer r.mu.Unlock()
	clone.UpdatedAt = r.now()
	r.state[stateKey(key)] = clone
	return nil
}

// Delete implementa Repository: elimina el estado de la clave (idempotente; si no
// existe es un no-op sin error, misma semántica que el DELETE sin filas).
func (r *MemoryRepository) Delete(ctx context.Context, key Key) error {
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
//
// Recorre TODAS las sesiones del tenant en las que `from` tiene estado. No toca el
// estado de otros tenants ni el de otros contactos, ni el UpdatedAt de lo que mueve.
// Sin estado de `from` es un no-op. Nunca devuelve error. No migra nada más (ni
// solicitudes, ni respuestas, ni bienvenidas): solo flow_state.
func (r *MemoryRepository) MigrateContactID(ctx context.Context, tenantID, from, to string) error {
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

// LatestDefinition implementa Repository: devuelve la mayor versión existente, con
// su Version = la asignada. ErrDefinitionNotFound (errors.Is) si el (tenant, flujo) no
// tiene ninguna.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: tenant=%s flow=%s"
func (r *MemoryRepository) LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error) {
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
// exacta indicada. ErrDefinitionNotFound si no existe. El texto lleva `version=` solo
// si el flujo existe y es la versión la que falta (el Postgres lo lleva siempre): lo
// común a los dos adaptadores es el prefijo hasta `flow=<id>`.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: tenant=%s flow=%s"
//   - "%w: tenant=%s flow=%s version=%d"
func (r *MemoryRepository) GetDefinition(ctx context.Context, tenantID, flowID string, version int) (model.Flow, error) {
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
// (tenant_id, flow_id) y devuelve la versión asignada. El f.Version del argumento se
// ignora y la definición se guarda con la asignada. No valida el flujo. Nunca devuelve
// error.
func (r *MemoryRepository) InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (int, error) {
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

// ListDefinitions devuelve el resumen de cada flujo del tenant (flow_id + última
// versión), ordenado por flow_id (Plan 018 · T5). Acota por tenant_id (INV-8). El
// repositorio en memoria no rastrea created_at: FlowSummary.CreatedAt queda en cero
// (el Postgres devuelve el alta de esa versión). Sin flujos, lista vacía sin error.
func (r *MemoryRepository) ListDefinitions(ctx context.Context, tenantID string) ([]FlowSummary, error) {
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

// InsertResults implementa Repository: acumula las respuestas de encuesta en un
// slice interno (append-only), imitando el INSERT en survey_results. len(rows)==0
// es un no-op. Las filas se copian para no compartir el backing array con el
// llamante.
//
// Fecha cada fila que llega con CreatedAt cero al instante del reloj (SetClock), igual
// que el DEFAULT now() de la columna: sin eso el doble devolvería un created_at CERO
// donde Postgres devuelve una fecha, y quien acote «las respuestas de esta pasada» por
// fecha vería en sus tests un filtro que deja pasar todo. Una fila que llega con
// CreatedAt lo conserva (el Postgres lo ignora). Acepta filas sin EventID, que el CHECK
// de la 0054 rechazaría. Nunca devuelve error.
func (r *MemoryRepository) InsertResults(ctx context.Context, rows []SurveyResult) error {
	if len(rows) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
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
//
// Cada fila sale con el EventID con el que se guardó, igual que en el Postgres (que
// desde F8-01 lee la columna: ver SurveyResultStore.ListResults). Nunca devuelve error.
func (r *MemoryRepository) ListResults(ctx context.Context, tenantID, contactID, flowID string) ([]SurveyResult, error) {
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
// resultado); devuelve una copia para no exponer el slice interno. Trae las de TODOS
// los tenants, en orden de escritura, con su EventID y su CreatedAt.
func (r *MemoryRepository) SurveyResults() []SurveyResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SurveyResult, len(r.results))
	copy(out, r.results)
	return out
}

// InsertFlowEvent implementa Repository: acumula el efecto en un slice interno
// (append-only), imitando el INSERT en el outbox flow_events (Plan 015 · T2). El
// Payload nil se conserva tal cual (la materialización a '{}' es del repo
// Postgres); la copia por valor de la struct no comparte el mapa con el llamante
// solo si este no lo muta, así que se clona el Payload defensivamente.
func (r *MemoryRepository) InsertFlowEvent(ctx context.Context, ev FlowEvent) error {
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
// un helper de test; devuelve una copia para no exponer el slice interno. Trae los de
// TODOS los tenants, en orden de escritura.
func (r *MemoryRepository) FlowEvents() []FlowEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]FlowEvent, len(r.flowEvents))
	copy(out, r.flowEvents)
	return out
}
