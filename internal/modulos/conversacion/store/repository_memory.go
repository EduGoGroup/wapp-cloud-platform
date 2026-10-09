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
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
//
// En el rojo no lleva campos. El verde le pone el mutex, el reloj y un índice por
// tabla imitada.
type MemoryRepository struct{}

// NewMemoryRepository crea un repositorio en memoria vacío y listo para usar: ningún
// tenant tiene conversaciones, definiciones, contenido, solicitudes, configuración ni
// bienvenidas, y el reloj es el del proceso (time.Now) mientras no se inyecte otro con
// SetClock.
func NewMemoryRepository() *MemoryRepository {
	panic(pendiente.Implementar("store.NewMemoryRepository"))
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
	panic(pendiente.Implementar("store.MemoryRepository.SetClock"))
}

// Exists implementa Repository.
func (r *MemoryRepository) Exists(ctx context.Context, key Key) (bool, error) {
	panic(pendiente.Implementar("store.MemoryRepository.Exists"))
}

// Load implementa Repository.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: clonar estado: %w"
func (r *MemoryRepository) Load(ctx context.Context, key Key) (model.Conversation, bool, error) {
	panic(pendiente.Implementar("store.MemoryRepository.Load"))
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
	panic(pendiente.Implementar("store.MemoryRepository.Save"))
}

// Delete implementa Repository: elimina el estado de la clave (idempotente; si no
// existe es un no-op sin error, misma semántica que el DELETE sin filas).
func (r *MemoryRepository) Delete(ctx context.Context, key Key) error {
	panic(pendiente.Implementar("store.MemoryRepository.Delete"))
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
	panic(pendiente.Implementar("store.MemoryRepository.MigrateContactID"))
}

// LatestDefinition implementa Repository: devuelve la mayor versión existente, con
// su Version = la asignada. ErrDefinitionNotFound (errors.Is) si el (tenant, flujo) no
// tiene ninguna.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w: tenant=%s flow=%s"
func (r *MemoryRepository) LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error) {
	panic(pendiente.Implementar("store.MemoryRepository.LatestDefinition"))
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
	panic(pendiente.Implementar("store.MemoryRepository.GetDefinition"))
}

// InsertDefinition implementa Repository: asigna version = max+1 por
// (tenant_id, flow_id) y devuelve la versión asignada. El f.Version del argumento se
// ignora y la definición se guarda con la asignada. No valida el flujo. Nunca devuelve
// error.
func (r *MemoryRepository) InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (int, error) {
	panic(pendiente.Implementar("store.MemoryRepository.InsertDefinition"))
}

// ListDefinitions devuelve el resumen de cada flujo del tenant (flow_id + última
// versión), ordenado por flow_id (Plan 018 · T5). Acota por tenant_id (INV-8). El
// repositorio en memoria no rastrea created_at: FlowSummary.CreatedAt queda en cero
// (el Postgres devuelve el alta de esa versión). Sin flujos, lista vacía sin error.
func (r *MemoryRepository) ListDefinitions(ctx context.Context, tenantID string) ([]FlowSummary, error) {
	panic(pendiente.Implementar("store.MemoryRepository.ListDefinitions"))
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
	panic(pendiente.Implementar("store.MemoryRepository.InsertResults"))
}

// ListResults implementa Repository: filtra las respuestas por (tenant, contacto,
// flujo) conservando el orden de escritura, que es el cronológico que devuelve el
// PostgresRepository (allí, ORDER BY created_at, id).
//
// Devuelve la lista VACÍA y no nil cuando no hay nada, igual que el camino Postgres:
// un test que distinga `nil` de `[]` sobre el doble estaría comprobando algo que la
// implementación real no promete.
//
// Cada fila sale con el EventID con el que se guardó. ⚠️ El Postgres NO lo lee en esta
// consulta (su SELECT no trae event_id) y allí sale siempre "": quien compare los dos
// adaptadores no mira ese campo en ListResults. Nunca devuelve error.
func (r *MemoryRepository) ListResults(ctx context.Context, tenantID, contactID, flowID string) ([]SurveyResult, error) {
	panic(pendiente.Implementar("store.MemoryRepository.ListResults"))
}

// SurveyResults devuelve una copia de las respuestas de encuesta acumuladas por
// InsertResults. Es un helper de test (los tests inspeccionan/agregan el
// resultado); devuelve una copia para no exponer el slice interno. Trae las de TODOS
// los tenants, en orden de escritura, con su EventID y su CreatedAt.
func (r *MemoryRepository) SurveyResults() []SurveyResult {
	panic(pendiente.Implementar("store.MemoryRepository.SurveyResults"))
}

// InsertFlowEvent implementa Repository: acumula el efecto en un slice interno
// (append-only), imitando el INSERT en el outbox flow_events (Plan 015 · T2). El
// Payload nil se conserva tal cual (la materialización a '{}' es del repo
// Postgres); la copia por valor de la struct no comparte el mapa con el llamante
// solo si este no lo muta, así que se clona el Payload defensivamente.
func (r *MemoryRepository) InsertFlowEvent(ctx context.Context, ev FlowEvent) error {
	panic(pendiente.Implementar("store.MemoryRepository.InsertFlowEvent"))
}

// FlowEvents devuelve una copia de los efectos acumulados por InsertFlowEvent. Es
// un helper de test; devuelve una copia para no exponer el slice interno. Trae los de
// TODOS los tenants, en orden de escritura.
func (r *MemoryRepository) FlowEvents() []FlowEvent {
	panic(pendiente.Implementar("store.MemoryRepository.FlowEvents"))
}
