// Porta internal/flujos/model/model.go @ 3c74b80

// Package model define los tipos de la definición de flujo (Flow/Node) y del
// estado conversacional (Conversation), junto con su (de)serialización JSON y
// la validación de esquema.
//
// Esquema B (resuelto 2026-06-26, design.md §10.B): `Nodes` es un mapa
// id→nodo; los tipos de nodo son "menu" (Prompt + Options) y "message"
// (Text + Next). `Next == nil` termina el flujo.
package model

import (
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Tipos de nodo soportados en este corte (Menú). Ver design.md §4.
const (
	NodeTypeMenu           = "menu"
	NodeTypeMessage        = "message"
	NodeTypeSurveyQuestion = "survey_question"
)

// NodeTerminal es el valor centinela de Conversation.CurrentNode que marca el
// fin de la conversación (un nodo "message" con Next == nil). No puede colisionar
// con un id real de nodo: la validación rechaza cualquier flujo cuyo mapa de
// nodos contenga esta clave. Ver design.md §6 y el método Finished.
//
// IMPORTANTE: NO usar bytes nulos (0x00) ni de control aquí. Este valor se
// persiste en la columna TEXT flow_state.current_node y PostgreSQL rechaza el
// 0x00 ("invalid byte sequence for encoding UTF8"). El MemoryRepository (mapas Go)
// lo toleraba y enmascaraba el fallo; solo el e2e real contra PostgreSQL lo
// destapó. Mantener un sentinel imprimible.
const NodeTerminal = "__wapp_flow_end__"

// Outcome es el DESENLACE con el que un flujo llegó al centinela: cómo terminó, no
// que terminara (eso ya lo dice Finished). Nace del hallazgo #29 (Plan 043 · Ola 6,
// decisión de Jhoan 2026-08-11): el módulo DECLARA el desenlace y el runtime lo
// TRADUCE al estado del evento conversacional (runtime/event_lifecycle.go). Sin él,
// cancelar un pedido dejaba su evento en `closed` —«fin natural del flujo», D-043.5—
// cuando la enmienda E-11 del ADR-0029 ya produce `cancelled` para un cliente que
// cierra su evento con un gesto MENOS explícito.
//
// Vive en `model` y no en `modules` por la MISMA razón que MediaRef: modules importa
// model y nunca al revés, y este valor tiene que viajar en Conversation.
//
// El CERO es «sin declarar» a propósito, y es lo que hace el campo ADITIVO: menu,
// survey y media no declaran nada y siguen valiendo lo de siempre (el evento muere
// `closed`). Por eso es un tipo propio con tres valores y no un `Cancelled bool`:
// con un bool, «no lo declaré» y «terminó bien» serían el mismo valor y un módulo
// nuevo no podría distinguir entre heredar el default y afirmar un final feliz.
type Outcome string

const (
	// OutcomeUndeclared es el cero: el módulo no dijo nada. Trato de siempre.
	OutcomeUndeclared Outcome = ""
	// OutcomeCompleted es el flujo que llegó a su fin HACIENDO lo que venía a hacer
	// (el pedido confirmado). Es explícito aunque hoy se traduzca igual que el cero:
	// un módulo que lo declara está afirmando algo, no callándose.
	OutcomeCompleted Outcome = "completed"
	// OutcomeCancelled es el flujo que terminó porque el cliente lo ABANDONÓ desde
	// dentro (el pedido cancelado). El runtime lo traduce a events.StatusCancelled.
	OutcomeCancelled Outcome = "cancelled"
)

// VarOutcome es la clave de Conversation.Vars donde el ENGINE deja el desenlace que
// el módulo declaró al llegar al centinela (engine.Step). Es una clave del contrato
// engine↔runtime, como VarContentRaw lo es del contrato engine↔módulos.
//
// 🔴 POR QUÉ EN Vars Y NO EN UN CAMPO SUELTO DEL STRUCT (decidido midiendo, no por
// estilo): un campo del struct NO sobrevive al Save —el repositorio Postgres escribe
// columnas nombradas una a una (store/repository_postgres.go), no el struct— y el
// desenlace tiene que sobrevivir EXACTAMENTE UN caso: el reintento de E-8 §4. Cuando
// TransitionEvent falla de verdad, closeIfFinished conserva el puntero a propósito y
// el SIGUIENTE entrante vuelve a intentar el cierre sobre un estado RELEÍDO DE LA
// BASE; con el desenlace en memoria, ese reintento habría escrito `closed` sobre un
// pedido cancelado, que es justo el defecto que el #29 arregla, solo que un turno más
// tarde y sin que nadie lo viera. Vars es la parte de Conversation que se persiste,
// así que el desenlace vive donde vive el resto del estado del flujo.
//
// No es PII (un enum de tres valores) y muere con la fila: el flow_state terminal lo
// borra entero el primer entrante posterior (releaseFinishedState, runtime/incoming.go).
const VarOutcome = "flow_outcome"

// ErrInvalidFlow es el error base (envoltura) de toda definición de flujo que
// no cumple el esquema. Se inspecciona con errors.Is.
var ErrInvalidFlow = errors.New("definición de flujo inválida")

// Flow es la definición declarativa y versionada de un flujo (datos, no
// código; Pieza 05 §3). La unidad persistida es (tenant_id, flow_id, version).
type Flow struct {
	FlowID  string          `json:"flow_id"`
	Version int             `json:"version"`
	Initial string          `json:"initial"`
	Nodes   map[string]Node `json:"nodes"`
}

// Node es un nodo del flujo. Según Type usa unos u otros campos:
//   - "menu":    Prompt + Options (opción→id de nodo destino).
//   - "message": Text + Next (id de nodo siguiente; nil termina el flujo).
//
// Content es OPCIONAL (puntero): describe DE DÓNDE sale el contenido a renderizar
// (fuente + referencia). Ausente/nil ⇒ contenido estático inline (Prompt/Options),
// retro-compatible con las definiciones existentes. Es la primera costura del
// refactor hexagonal del Motor de Flujos (Plan 015): en T0 solo se abre la firma;
// la resolución real por fuente llega en T1.
type Node struct {
	Type       string            `json:"type"`
	Prompt     string            `json:"prompt,omitempty"`
	Text       string            `json:"text,omitempty"`
	Options    map[string]string `json:"options,omitempty"`
	Next       *string           `json:"next,omitempty"`
	QuestionID string            `json:"question_id,omitempty"`
	Content    *ContentRef       `json:"content,omitempty"`
}

// Content es el contenido RESUELTO que un módulo renderiza en un nodo (tipos
// PUROS, sin dependencias externas). Es la vista que el engine entrega a
// Module.Render tras resolver la fuente (Plan 015): Prompt/Options para nodos
// interactivos, Items para catálogos (pedido) y Raw para el resto de la carga
// específica de la fuente. En T0 el engine lo construye como un placeholder
// inline (copia de Prompt/Options del nodo); la resolución real llega en T1.
type Content struct {
	Prompt  string
	Options map[string]string
	Items   []ContentItem
	Raw     map[string]any `json:"-"`
}

// ContentItem es un ítem de catálogo (p. ej. una línea del menú de un pedido):
// código de selección, SKU, etiqueta y precio. Tipo PURO de dominio (Plan 015).
type ContentItem struct {
	Code  string  `json:"code"`
	SKU   string  `json:"sku"`
	Label string  `json:"label"`
	Price float64 `json:"price"`
}

// ContentRef es la referencia declarativa a la fuente del contenido de un nodo
// (Plan 015): Source indica el origen ("static" | "inline" | "json") y Ref la
// clave/identificador dentro de esa fuente. Vive en la definición del flujo
// (Node.Content) y la resuelve el engine a un Content antes de renderizar.
type ContentRef struct {
	Source string `json:"source"` // "static" | "inline" | "json"
	Ref    string `json:"ref"`

	// Descriptor INLINE del nodo "media" (Plan 017 §4.1/§9.B): cuando el nodo es
	// {"type":"media","content":{"source":"static", …}}, estos campos viajan en el
	// MISMO objeto `content` y el módulo media los lee para construir un MediaRef.
	// Son ADITIVOS y OPCIONALES (omitempty): los nodos menu/message/survey/cart no
	// los declaran ⇒ retro-compatibles, sin tocar el adapter static ni el Router
	// (el descriptor no pasa por model.Content; el módulo lo lee del `node`).
	Key      string `json:"key,omitempty"`
	Filename string `json:"filename,omitempty"`
	Mime     string `json:"mime,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Caption  string `json:"caption,omitempty"`
}

// MediaRef es el descriptor PURO de un archivo a enviar por un nodo "media"
// (Plan 017 §4.1): identifica el objeto en el almacén (Key) y los metadatos que
// el Edge fija en WhatsApp (Filename/Mime/Kind/Caption). Es un tipo de DOMINIO
// neutral (vive en model, hoja del grafo de imports del Motor) para que el módulo
// media lo DECLARE y el engine lo transporte OPACO en Output.Media SIN que el
// engine importe el paquete del módulo (dirección hexagonal, §9.C).
//
// PURO: no lleva URL ni credenciales. El runtime presigna la Key (T4) y el Edge
// descarga y sube el binario; el módulo solo describe QUÉ archivo mandar.
type MediaRef struct {
	Key      string // key del objeto en el almacén (p. ej. "wapp/media/lista-precios.pdf")
	Filename string // nombre que verá el usuario en WhatsApp
	Mime     string // "application/pdf", "image/png", …
	Kind     string // "document" | "image" (mapea a MediaKind del proto, T4)
	Caption  string // texto que ACOMPAÑA al archivo en el MISMO mensaje (§9.I)
}

// Conversation es el estado vivo de una conversación ligada a la clave lógica
// (TenantID, SessionID, ContactID) (Pieza 05 §3). ContactID es la identidad
// OPACA del contacto (contacts.contact_id, UUID como texto), NO el JID crudo
// (Plan 010, design.md §1, §3). `Vars` guarda el contador de reprompt
// (design.md §10.E) y variables recolectadas.
type Conversation struct {
	TenantID        string         `json:"tenant_id"`
	SessionID       string         `json:"session_id"`
	ContactID       string         `json:"contact_id"`
	FlowID          string         `json:"flow_id"`
	FlowVersion     int            `json:"flow_version"`
	CurrentNode     string         `json:"current_node"`
	Vars            map[string]any `json:"vars"`
	LastWaMessageID string         `json:"last_wa_message_id,omitempty"`
	// EventID es el evento conversacional ACTIVO de esta conversación
	// (flow_state.event_id → conversation_events.id, Plan 043 · T1.3, D-043.4).
	// «Activo» NO es un estado del evento sino un PUNTERO de la conversación: por eso
	// vive aquí y no como un cuarto valor de conversation_events.status, y por eso un
	// evento puede seguir vivo sin ser el activo.
	//
	// "" ⇒ la conversación NO tiene evento activo. Es el estado NORMAL y frecuente —el
	// saludo no crea evento (E-6)— y también el que deja cerrar o cancelar: apagar el
	// puntero es escribir "" y guardar. NO es un error ni un "no sé".
	//
	// Se representa como string vacío (no *string ni un tipo Null*) siguiendo la MISMA
	// convención que LastWaMessageID: el dominio usa el cero de Go y el NULL vive solo
	// en la frontera SQL (sql.NullString en el repositorio Postgres).
	EventID string `json:"event_id,omitempty"`
	// OwnerEventID es el evento DUEÑO del flujo que corre en ESTA fila
	// (flow_state.owner_event_id → conversation_events.id, Plan 053 · T1.3/T1.4).
	//
	// «Dueño» y «activo» son dos preguntas distintas sobre la misma conversación:
	// EventID contesta A QUIÉN LE HABLA el contacto ahora; OwnerEventID contesta DE
	// QUIÉN ES el flujo guardado en FlowID/FlowVersion/CurrentNode/Vars. Casi siempre
	// coinciden —y por eso una sola columna pareció bastar durante todo el Plan 043—,
	// pero DIVERGEN en cuanto un evento se monta encima de otro vivo: al abrir el
	// `menu` sobre un `cart` a medias, EventID pasa a ser el del menú y OwnerEventID
	// sigue siendo el del carrito, que es de quien sigue siendo el flujo. Con un solo
	// puntero, cerrar el menú se llevaba por delante el flujo del carrito.
	//
	// "" ⇒ esta conversación NO tiene flujo con dueño. Es un estado NORMAL y
	// frecuente, no un error ni un "no sé": lo produce el menú puro de D-043.3 —que ni
	// siquiera tiene flujo— y también lo trae una fila LEGADA cuya columna sigue en
	// NULL. Quien lea "" debe comportarse como se comportaba antes de que existiera
	// este campo.
	//
	// Se representa como string vacío (no *string ni un tipo Null*) siguiendo la MISMA
	// convención que EventID y LastWaMessageID: el dominio usa el cero de Go y el NULL
	// vive solo en la frontera SQL (sql.NullString en el repositorio Postgres). Un
	// puntero obligaría a cada lector a distinguir nil de "" para acabar tratando los
	// dos igual, y metería un desreferenciado nulo posible donde hoy no hay ninguno.
	OwnerEventID string `json:"owner_event_id,omitempty"`
	// UpdatedAt es la marca de la última escritura del estado (flow_state.updated_at).
	// La ESTAMPA el store en cada Save (no el llamante); Load la devuelve. La consume
	// el TTL conversacional del runtime (Plan 029 · T9) para decidir si un estado vivo
	// venció. Zero (sin marca) ⇒ el TTL no lo vence. No viaja en la columna vars (es
	// una columna propia); la etiqueta json solo sirve al clon en memoria (omitempty
	// no aplica a time.Time, que no es "vacío" en JSON).
	UpdatedAt time.Time `json:"updated_at"`
}

// Finished indica si la conversación llegó al fin del flujo (CurrentNode quedó
// en el centinela NodeTerminal). Un Step sobre una conversación terminada no
// avanza (ver engine.Step).
func (c Conversation) Finished() bool {
	panic(pendiente.Implementar("model.Conversation.Finished"))
}

// Outcome devuelve el DESENLACE declarado por el módulo al terminar el flujo
// (VarOutcome). Un estado sin la clave —o con algo que no sea uno de los valores
// conocidos— vale OutcomeUndeclared: es el trato de siempre, y es lo que hace que
// una fila LEGADA (escrita antes de esta ola) o el estado de un módulo que no
// declara nada se comporten exactamente igual que ayer. Vale también para un
// Vars nil y para un valor que no es texto (un número, un booleano).
//
// El desconocido se degrada a «sin declarar» en vez de propagarse: quien traduce
// esto decide la MUERTE de un evento, y ante un valor que no entiende debe hacer lo
// conservador (cerrar), no inventarse una tercera conducta. La comparación es
// exacta: «Cancelled» o « cancelled» son desconocidos.
func (c Conversation) Outcome() Outcome {
	panic(pendiente.Implementar("model.Conversation.Outcome"))
}

// SetOutcome sella el desenlace en Vars, como texto plano (string, no Outcome:
// es lo que vuelve del JSONB). Lo llama el ENGINE al reconocer el centinela; nadie
// más. OutcomeUndeclared BORRA la clave en vez de escribir "": un mapa que no dice
// nada es lo mismo que un mapa sin la clave, y dejar el hueco escrito ensuciaría el
// JSONB de flow_state.vars de todos los flujos que no declaran desenlace (menú,
// encuesta, media) — que son la mayoría. Con Vars nil, declarar crea el mapa y
// «sin declarar» lo deja nil; las demás claves de Vars no se tocan.
func (c *Conversation) SetOutcome(o Outcome) {
	panic(pendiente.Implementar("model.Conversation.SetOutcome"))
}

// MarshalDefinition serializa una definición de flujo a JSON (cuerpo JSONB), con
// las etiquetas de Flow y Node: es el JSON de flow_definitions, contrato con la BD.
func MarshalDefinition(f Flow) ([]byte, error) {
	panic(pendiente.Implementar("model.MarshalDefinition"))
}

// UnmarshalDefinition deserializa una definición de flujo desde JSON. NO valida:
// un JSON bien formado con un esquema inválido sale sin error; el error de un JSON
// mal formado es el de encoding/json tal cual, SIN envolver en ErrInvalidFlow. Las
// claves desconocidas se ignoran.
func UnmarshalDefinition(data []byte) (Flow, error) {
	panic(pendiente.Implementar("model.UnmarshalDefinition"))
}

// ParseAndValidate deserializa y valida en un paso: rechaza JSON mal formado y
// definiciones que no cumplen el esquema. Es el punto de entrada del handler
// admin (T3) para publicar una definición.
//
// Un JSON mal formado da «definición de flujo inválida: JSON mal formado: <error
// de encoding/json>», envuelto sobre ErrInvalidFlow; un esquema inválido, el error
// de Validate. En los dos casos devuelve el Flow cero.
//
// moduleTypes son los tipos de nodo que aportan los MÓDULOS enchufables (Registry):
// nodos de esos tipos se aceptan de forma LAXA (la validación profunda del contenido
// la hace el módulo en runtime). Así el modelo NO se acopla a los módulos concretos
// (los tipos se inyectan como strings, evitando el ciclo model→modules).
func ParseAndValidate(data []byte, moduleTypes ...string) (Flow, error) {
	panic(pendiente.Implementar("model.ParseAndValidate"))
}

// Validate comprueba el esquema de la definición (design.md §4), en este orden, y
// devuelve el PRIMER defecto, envuelto sobre ErrInvalidFlow (inspeccionable con
// errors.Is) y con este texto detrás de «definición de flujo inválida: »:
//   - flow_id no vacío: «flow_id vacío»;
//   - version >= 1 (unidad persistida (tenant,flow_id,version)): «version %d
//     inválida (debe ser >= 1)»;
//   - nodes no vacío: «nodes vacío»;
//   - sin la clave centinela NodeTerminal: «un id de nodo usa la clave reservada
//     de fin de flujo»;
//   - initial no vacío («initial vacío») y presente en nodes («initial %q no
//     existe en nodes»);
//   - cada nodo "menu" tiene Options no vacío («nodo menu %q sin options») y cada
//     destino existe en nodes («nodo menu %q: opción %q apunta a nodo inexistente
//     %q»);
//   - cada nodo "survey_question" tiene QuestionID («nodo %q survey sin
//     question_id»; se mira ANTES que sus options) y cumple lo mismo que el menú,
//     con la etiqueta «survey» en vez de «menu»;
//   - cada nodo "message" con Next != nil apunta a un nodo existente («nodo
//     message %q: next apunta a nodo inexistente %q»); con Next nil termina el flujo;
//   - el Type de cada nodo es un tipo CORE (menu, message, survey_question) o un
//     tipo de MÓDULO declarado en moduleTypes (Registry), que se acepta laxo; si
//     no, «nodo %q: tipo desconocido %q».
//
// moduleTypes son los tipos de nodo registrados por los módulos enchufables
// (p. ej. "cart"): un nodo de ese tipo pasa la validación sin exigir el esquema
// de los tipos core (options/question_id); su contenido lo valida el módulo en
// runtime. El rechazo de "tipo desconocido" se conserva para todo lo que no sea
// ni core ni de módulo (protege contra typos). Declarar como módulo un tipo core
// NO lo relaja: un "menu" sin options sigue fallando.
//
// Lo que NO hace, y el nuevo tampoco (es lo que corre en UAT): no recorta
// espacios (un flow_id de solo blancos pasa; un id con un espacio de más es otro
// id), no distingue mayúsculas en los tipos («Menu» es desconocido), no detecta
// ciclos (un message cuyo Next es él mismo vale) ni nodos inalcanzables, y acepta
// una opción con clave vacía. Como el centinela no puede ser un nodo, apuntarle
// con una opción o con Next es «nodo inexistente». Los nodos se recorren en el
// orden del mapa: con varios nodos rotos, cuál se informa no está fijado.
func Validate(f Flow, moduleTypes ...string) error {
	panic(pendiente.Implementar("model.Validate"))
}
