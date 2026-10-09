// Porta internal/flujos/store/store.go @ c0c0c03
//
// El fichero viejo medía 875 líneas y aquí NACE PARTIDO por tema (05 E-13), solo
// repartiendo declaraciones:
//
//   - store.go (este): la clave conversacional, las interfaces del estado vivo, las
//     definiciones, las encuestas y el outbox de efectos, la composición Repository y
//     las aserciones de que los dos adaptadores la satisfacen.
//   - store_tenant_content.go: el contenido de negocio por tenant y su versionado.
//   - store_intakes.go: las solicitudes del carrito (lectura, escritura y sus tipos).
//   - store_settings.go: la configuración por tenant, sus valores por defecto y la
//     bienvenida única (WelcomeStore), que se gobierna con esa configuración.
//
// Fuera de las 13 interfaces, los DOS adaptadores ofrecen además ListDefinitions,
// UpsertTenantContent, ListTenantContent y DeleteTenantContent: los consume la API
// pública declarando su propia interfaz estrecha. Su contrato está en el método de
// cada adaptador y lo afirma la misma suite (storehelpertest.Contrato).

// Package store define el contrato de persistencia del motor de flujos: la clave
// conversacional, las interfaces segregadas por subdominio y los tipos que cruzan
// por ellas. Lo implementan MemoryRepository (unitario, sin BD) y
// PostgresRepository (SQL crudo sobre *sql.DB).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// Key es la clave lógica de una conversación (Pieza 05 §3, design.md §5).
// ContactID es la identidad OPACA del contacto (contacts.contact_id, UUID como
// texto), NO el JID crudo: el motor se clava por contact_id (Plan 010, design.md
// §1, §3). La resolución JID→contact_id la hace el runtime (T4); esta capa opera
// sobre el contact_id ya resuelto.
type Key struct {
	TenantID  string
	SessionID string
	ContactID string
}

// String devuelve una representación estable y OPACA de la clave, apta como índice
// de mapa fuera de este paquete (p. ej. el token-bucket de auto-respuestas del
// runtime, Plan 020 · T0). Son IDs opacos (tenant/session/contact): no expone PII.
func (k Key) String() string {
	return k.TenantID + "|" + k.SessionID + "|" + k.ContactID
}

// Interfaces SEGREGADAS por subdominio (ISP, Plan 027 · Ola 2 · T9, cierra H12).
// Antes Repository era una única interfaz "gorda" (7 tablas, ~5 subdominios) que
// obligaba a cada consumidor a depender de métodos que no usa. Ahora cada
// subdominio tiene su interfaz pequeña; los consumidores declaran SOLO lo que
// necesitan (el runtime compone su FlowStore; el PersistSink su almacén de
// proyección) y Repository queda como la COMPOSICIÓN de todas (retrocompat: las
// implementaciones Postgres/Memory la satisfacen entera sin cambios).

// ConversationStore persiste el estado conversacional vivo por Key (design.md §5/§6).
type ConversationStore interface {
	// Exists indica si ya hay una conversación viva para la clave.
	Exists(ctx context.Context, key Key) (bool, error)
	// Load carga el estado de la conversación; found=false sin error si no hay.
	Load(ctx context.Context, key Key) (state model.Conversation, found bool, err error)
	// Save inserta o actualiza (upsert) el estado de la conversación.
	Save(ctx context.Context, state model.Conversation) error
	// Delete elimina la conversación viva de la clave (libera la clave para que
	// un entrante posterior pueda volver a disparar un flujo). Idempotente: si no
	// había fila, NO es error. Lo usa el escape global (Plan 019 · T4) para cortar
	// una conversación viva, misma liberación de clave que se hacía por SQL manual
	// en e2e previos (design.md §6).
	Delete(ctx context.Context, key Key) error
}

// DefinitionReader lee definiciones de flujo versionadas (lo que necesita el runtime).
type DefinitionReader interface {
	// LatestDefinition devuelve la versión vigente de la definición del flujo.
	LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error)
	// GetDefinition devuelve la definición de una versión EXACTA. El runtime lo
	// usa para avanzar una conversación con la versión con la que arrancó
	// (Conversation.FlowVersion), de modo que publicar una versión nueva no
	// "salte" una conversación en curso (versionado, design.md §4). Devuelve
	// ErrDefinitionNotFound si no existe esa (tenant_id, flow_id, version).
	GetDefinition(ctx context.Context, tenantID, flowID string, version int) (model.Flow, error)
}

// DefinitionStore es DefinitionReader + el alta de versiones (lo usa el admin).
type DefinitionStore interface {
	DefinitionReader
	// InsertDefinition persiste una definición como versión nueva (no muta la
	// vigente; versionado design.md §4). La versión la asigna el repositorio
	// (version = COALESCE(max(version),0)+1 por (tenant_id, flow_id)); el campo
	// f.Version del argumento se ignora. Devuelve la versión asignada.
	InsertDefinition(ctx context.Context, tenantID string, f model.Flow) (version int, err error)
}

// SurveyResultStore proyecta las respuestas de encuesta EN CLARO (Plan 014/015).
type SurveyResultStore interface {
	// InsertResults persiste (en lote) las respuestas de una encuesta como datos
	// de negocio EN CLARO en survey_results (Plan 014 §10.D, ADR-0009). El
	// runtime (T3) lo llama al terminar la conversación (flush). len(rows)==0 es
	// un no-op. answer_code NO se cifra: es un código de opción agregable, no PII
	// (la identidad la protege el contact_id opaco, ADR-0010).
	InsertResults(ctx context.Context, rows []SurveyResult) error
	// ListResults devuelve las respuestas que este contacto ya dio en este flujo,
	// EN ORDEN CRONOLÓGICO (created_at, id), acotadas al tenant (INV-8). Sin
	// respuestas devuelve la lista vacía SIN error.
	//
	// Existe para el RESUMEN del rescate (Plan 043 · Ola 3): el estado vivo de la
	// encuesta —vars["answers"]— se borra al conmutar de evento, así que sin esta
	// lectura un rescate enseñaría siempre CERO respuestas. La fuente durable ya
	// estaba escrita respuesta a respuesta por el efecto survey_answer; lo único
	// que faltaba era leerla.
	//
	// DOS TRAMPAS DE ESTA TABLA, y ninguna la puede arreglar esta función:
	//
	//   - NO HAY SESIÓN. survey_results no tiene session_id (migración 0008), así
	//     que dos sesiones del mismo tenant y contacto comparten resultados. Es la
	//     misma asimetría que ya tiene GetOpenIntake, resuelto por (tenant,
	//     contacto) sin sesión, mientras el evento es por (tenant, sesión,
	//     contacto).
	//   - NO HAY IDENTIDAD DE PASADA. Tampoco hay columna que diga QUÉ recorrido
	//     produjo la fila: quien respondió la misma encuesta el mes pasado y vuelve
	//     hoy trae las dos tandas mezcladas. Por eso el orden es cronológico y
	//     FlowVersion viaja en cada fila: quien resuma decide la política (lo
	//     normal es quedarse con la ÚLTIMA respuesta de cada question_id). Elegirla
	//     aquí sería imponérsela a todos los lectores futuros con una columna que
	//     no existe.
	ListResults(ctx context.Context, tenantID, contactID, flowID string) ([]SurveyResult, error)
}

// FlowEventStore materializa el outbox append-only de efectos (Plan 015 · T2).
type FlowEventStore interface {
	// InsertFlowEvent persiste UN efecto del motor de flujos en el outbox
	// append-only flow_events (Plan 015 · T2, ADR-0009). CERO PII: ContactID es la
	// identidad OPACA (ADR-0010). El Payload (map) se serializa a JSONB; nil → {}.
	InsertFlowEvent(ctx context.Context, ev FlowEvent) error
}

// Repository es la COMPOSICIÓN de las interfaces segregadas: persiste el estado
// conversacional, las definiciones versionadas, los resultados/efectos y las
// solicitudes del carrito. Implementaciones: MemoryRepository (unit CI-safe) y
// PostgresRepository (integración, JSONB vía json.Marshal/Unmarshal). Se conserva
// para retrocompat y para quien necesite el conjunto completo; los consumidores
// concretos deben preferir la interfaz segregada que realmente usan (ISP).
type Repository interface {
	ConversationStore
	DefinitionStore
	SurveyResultStore
	FlowEventStore
	TenantContentReader
	IntakeStore
	TenantSettingsReader
}

// Ambas implementaciones satisfacen la composición completa (retrocompat tras la
// segregación por subdominio, Plan 027 · Ola 2 · T9): un método que falte en
// cualquiera rompe aquí en compilación, no en un consumidor lejano.
var (
	_ Repository = (*PostgresRepository)(nil)
	_ Repository = (*MemoryRepository)(nil)

	// WelcomeStore NO entra en Repository, y es la misma razón que el versionado de
	// catálogo (justo abajo): solo lo necesita el runtime cuando la bienvenida está
	// cableada, y meterlo en la composición obligaría a implementarlo a cualquier
	// doble futuro que solo quiera conversaciones. Se afirma aparte, para que un
	// método que falte rompa AQUÍ y no en el bootstrap.
	_ WelcomeStore = (*PostgresRepository)(nil)
	_ WelcomeStore = (*MemoryRepository)(nil)

	// El versionado NO entra en Repository: solo lo necesita el import de catálogo
	// (Plan 041 · T3.3), y meterlo en la composición obligaría a implementarlo a
	// cualquier doble futuro que solo quiera conversaciones. Se afirma aparte.
	_ TenantContentVersioner = (*PostgresRepository)(nil)
	_ TenantContentVersioner = (*MemoryRepository)(nil)
)

// FlowSummary es el resumen de UN flujo publicado por un tenant: su flow_id y la
// última versión vigente (más su fecha de alta). Lo devuelve ListDefinitions para
// alimentar el listado de la API pública (GET /api/v1/flows, Plan 018 · T5). No
// incluye la definición completa (solo la cabecera); el detalle se obtiene con
// LatestDefinition. CERO PII: flow_id es un identificador de negocio (ADR-0009).
type FlowSummary struct {
	FlowID    string
	Version   int
	CreatedAt time.Time
}

// SurveyResult es una respuesta de encuesta lista para persistir EN CLARO en
// survey_results (Plan 014 §10.D). ContactID es la identidad OPACA del contacto
// (contacts.contact_id, Plan 010 / ADR-0010), NUNCA el número/JID crudo.
// AnswerCode es el código de la opción elegida (dato de negocio agregable, no
// PII). created_at lo pone el DEFAULT de la tabla.
type SurveyResult struct {
	TenantID    string
	ContactID   string
	FlowID      string
	FlowVersion int
	QuestionID  string
	AnswerCode  string
	// EventID es el evento conversacional (conversation_events.id) durante el que
	// se respondió (D-043.21: el hijo declara a su padre; migración 0054). Lo
	// escribe el proyector del módulo survey con el EventID de la EffectMeta. Es
	// TRAZABILIDAD, no «contenido que puede morir» (D-043.18): la encuesta NO entra
	// en la vista event_content. Cadena vacía ⇒ NULL en la tabla — solo legítimo en
	// filas legadas pre-0054; para una fila NUEVA el CHECK
	// survey_results_event_id_required_chk la rechaza.
	EventID string
	// CreatedAt lo pone el DEFAULT now() de la tabla y solo lo rellena la LECTURA
	// (ListResults); InsertResults ni lo mira, igual que IntakeItem.AddedAt.
	//
	// Se lee porque hasta la 0054 era lo ÚNICO que permitía acotar «las respuestas
	// de ESTA pasada»: survey_results no tenía session_id ni event_id, así que
	// quien resumía una encuesta rescatada solo podía separar la tanda de hoy de la
	// del mes pasado usando la fecha del evento como cota inferior
	// (runtime/summary_sources.go). Con EventID esa correlación por timestamp queda
	// como FALLBACK del legado (filas con event_id NULL); la vía por event_id es la
	// preferida (D-043.21).
	CreatedAt time.Time
}

// FlowEvent es un efecto del motor de flujos listo para persistir en el outbox
// append-only flow_events (Plan 015 · T2, ADR-0009). ContactID es la identidad
// OPACA del contacto (contacts.contact_id, Plan 010 / ADR-0010), NUNCA el
// número/JID crudo. Kind es "persist" | "event"; Name es el nombre lógico del
// efecto (p.ej. "survey_answer"). Payload es el cuerpo de negocio del efecto y se
// serializa a JSONB en el repositorio Postgres (nil → {}). created_at lo pone el
// DEFAULT de la tabla.
type FlowEvent struct {
	TenantID    string
	ContactID   string // OPACO (Plan 010 / ADR-0010); NUNCA número/JID en claro
	FlowID      string
	FlowVersion int
	Kind        string         // "persist" | "event"
	Name        string         // "survey_answer" | ...
	Payload     map[string]any // se serializa a JSONB en el repo postgres
}

// ErrDefinitionNotFound lo devuelve LatestDefinition cuando no existe ninguna
// versión de la definición para (tenant_id, flow_id). Se inspecciona con
// errors.Is.
var ErrDefinitionNotFound = errors.New("definición de flujo no encontrada")
