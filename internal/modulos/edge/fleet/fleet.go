// Porta internal/gateway/fleet/fleet.go @ 809345b

// Package fleet lleva el registro durable del estado online/offline de las
// sesiones CloudLink de cada Edge (tabla public.fleet_sessions). El estado es
// DERIVADO del stream vivo: el Gateway marca online al conectar una sesión y
// offline al caer. La fuente viva (para empujar comandos) está en memoria en
// session.Registry; esta capa solo durabiliza el estado para auditoría/admin.
//
// Este fichero es el dominio y el puerto: los tipos, las dos validaciones puras y
// Repository. Las implementaciones del puerto son dos y las dos corren la misma
// suite, fleethelpertest.ContratoRepository: el doble fleethelpertest.Memoria
// (unitario, sin BD; en el paquete viejo era fleet.MemoryRepository y vivía en
// producción, D-F3-1) y el adaptador Postgres de repository_postgres.go.
//
// 🔴 Homónimo: nada de este paquete toca la DEK del ADR-0007 (la que descifra el
// almacén de whatsmeow, que custodia el cliente y jamás cruza el contrato). El
// campo DekLoadDurationMs es solo una DURACIÓN que el Edge reporta.
package fleet

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// normalizeSelfPn canoniza un número propio a E.164 sin '+' ni separadores (solo
// dígitos). Es el ÚNICO normalizador del self_pn de este paquete: lo usa el
// adaptador Postgres en la escritura Y en la lectura, y el doble
// fleethelpertest.Memoria lleva la misma regla (contact.Normalize con
// contact.KindPhoneE164), porque no puede llamar a esta función.
//
// 🔴 POR QUÉ ESTÁ AQUÍ Y NO INLINE EN CADA USO (Plan 046 · T4.1). Desde que el
// número va cifrado, Postgres ya no compara textos: compara ÍNDICES CIEGOS —HMAC
// del valor NORMALIZADO— y el doble en memoria compara strings. Si el doble
// guardara el valor CRUDO y Postgres indexara el normalizado, las dos mitades
// dejarían de responder lo mismo, y la divergencia se manifiesta EN LAS DOS
// DIRECCIONES:
//
//	→ VERDE EN MEMORIA, ROJO EN POSTGRES. Un test escribe "+34600111222" y
//	  consulta por esa MISMA cadena. El doble crudo compara string contra
//	  string: casa, y el test pasa. Postgres normaliza en la escritura —el bidx
//	  se calcula sobre "34600111222"— y si la lectura no normaliza, compara
//	  contra el HMAC de "+34600111222": dos índices distintos para el mismo
//	  número, no casa, y el conteo del tope de dispositivos devuelve 0. El fallo
//	  aparece en el suite de integración, no en el unitario que lo cubría.
//
//	→ VERDE EN MEMORIA, MAL EN PRODUCCIÓN, que es el peor. El doble crudo trata
//	  "+34600111222" y "34600111222" como DOS números distintos: un test que
//	  siembre las dos formas ve dos sesiones y pasa verde. Postgres, con el bidx
//	  sobre el valor normalizado, las COLAPSA en una sola: el tope de
//	  dispositivos (REQ-D4) cuenta uno donde el test contó dos. Nadie se entera
//	  hasta que un teléfono real supera el límite sin que salte el aviso.
//
// Los dos modos se cierran igual: normalizar en LOS DOS lados, con ESTA regla,
// en la escritura Y en la lectura. Un doble que no comparte el normalizador con
// lo que emula no es un doble, es una segunda semántica.
//
// La regla es la de contact.Normalize para phone_e164: conserva solo los dígitos
// ASCII 0-9, y es un error que no quede ninguno o que queden más de 15. Su salida
// es la base del índice ciego fleet_sessions.self_pn_bidx: cambiar UN byte de lo
// que devuelve deja sin casar los números ya guardados, sin dar un solo error.
func normalizeSelfPn(selfPn string) (string, error) {
	// El error de contact.Normalize NUNCA embebe el número: describe la causa con
	// una cuenta. Se puede envolver y loguear sin filtrar PII.
	return contact.Normalize(contact.KindPhoneE164, selfPn)
}

// defaultProfile normaliza un perfil vacío a ProfilePassive, espejando el DEFAULT
// de la columna profile (0063, D-07). Solo convierte el vacío: un perfil
// desconocido pasa intacto.
//
// 🔴 El default es PASIVO y eso es una decisión de producto, no un detalle: la
// 0025 ponía DEFAULT 'bot' (una sesión nueva auto-respondía) y la 0063 lo invirtió
// (una sesión nueva NO auto-responde hasta que su dueño la active, D-07).
func defaultProfile(p Profile) Profile {
	if p == "" {
		return ProfilePassive
	}
	return p
}

// State es el conjunto de estados posibles de una sesión: el del LINK CloudLink, no
// el del socket de WhatsApp (ese es Session.WhatsappState). Sus tres valores
// literales viajan a la columna fleet_sessions.state y a la API: no cambian. El
// valor cero ("") no es un estado: ninguna escritura del puerto lo produce.
type State string

const (
	// StateOnline indica que el stream de la sesión está vivo. Su literal es "online".
	StateOnline State = "online"
	// StateOffline indica que el stream de la sesión cayó (offline por red). Es
	// DERIVADO del cierre del stream (onStreamClosed): recuperable al reconectar.
	// Su literal es "offline".
	StateOffline State = "offline"
	// StateLoggedOut indica una sesión ZOMBIE: WhatsApp cerró el device (el Edge lo
	// reporta explícitamente con un Heartbeat State=LOGGED_OUT, Plan 020 · T3). Se
	// distingue del offline-por-red (que produce el cierre del stream): un zombie no
	// vuelve solo, hay que reemparejar el device. No renueva su lease (sesión muerta).
	// Su literal es "loggedout".
	StateLoggedOut State = "loggedout"
)

// DeviceLimit es el tope de dispositivos vinculados por número de WhatsApp
// (REQ-D4). Al superarlo, WhatsApp rechaza nuevos emparejamientos; el Cloud emite
// un aviso (sin PII) cuando cuenta más sesiones VIVAS con el mismo self_pn. Vale 4.
const DeviceLimit = 4

// ErrInvalidState lo devuelve SetState cuando el estado pedido no pertenece al
// conjunto que un admin puede fijar (offline|loggedout). Se inspecciona con
// errors.Is. Su texto es observable (llega a la API) y no cambia.
var ErrInvalidState = errors.New("estado de sesión inválido (usar offline|loggedout)")

// ValidAdminState indica si s pertenece al conjunto de estados que un admin puede
// fijar a mano (offline|loggedout): retirar/limpiar una sesión zombie o dejarla
// offline. StateOnline NO se admite: es DERIVADO del stream vivo (no se falsea).
// La comparación es exacta: el vacío, otra caja ("OFFLINE") o un valor con
// espacios dan false.
func ValidAdminState(s State) bool { return s == StateOffline || s == StateLoggedOut }

// Profile es el PERFIL DE NEGOCIO de una sesión (Plan 046 · T1.1, D-046.1): el eje
// que SUSTITUYE a Role. Mismo par de estados, otra palabra y otro vocabulario de
// cara al dueño («perfil: activa / pasiva», D-046.6).
//
// ⚠️ NADA que ver con devices.role del Edge (primary|standby, ADR-0018): ese dice
// qué dispositivo manda, este dice si la sesión contesta sola o solo emite. Otro
// dominio y otro repo (deslinde explícito de este plan).
//
// Es el ÚNICO eje: la columna legada `role` y su tipo Go se RETIRARON en la
// migración 0064 (D-046.1 revisada — no había ni un consumidor de `/role` fuera de
// la propia plataforma, así que el ciclo de deprecación no protegía a nadie).
//
// Valor cero: "" NO es un perfil conocido (ValidProfile lo rechaza), y el
// repositorio lo LEE como pasivo, que es el lado seguro: una sesión sin perfil no
// auto-responde.
type Profile string

const (
	// ProfileActive ejecuta el motor de flujos: dispara triggers y auto-responde.
	// Su literal es "active".
	ProfileActive Profile = "active"
	// ProfilePassive solo emite: NO dispara triggers ni auto-responde, y sus
	// entrantes se filtran en el Edge (ADR-0027, Ola 2). Su literal es "passive".
	//
	// 🔴 Es el DEFAULT de la columna (0063, D-07): una sesión recién emparejada nace
	// PASIVA. Distinto del DEFAULT 'bot' que traía la 0025 — cambio deliberado, no
	// un descuido: privacidad por defecto hasta que el dueño active la sesión.
	ProfilePassive Profile = "passive"
)

// ErrInvalidProfile lo devuelve el store cuando el perfil pedido no es
// active|passive. Se inspecciona con errors.Is. Su texto es observable (llega a la
// API) y no cambia.
var ErrInvalidProfile = errors.New("perfil de sesión inválido (usar active|passive)")

// TenantProfiles es la FOTO COMPLETA del eje `profile` de un tenant: el perfil de
// TODAS sus sesiones (activas incluidas) y la versión de esa foto (Plan 046 · T2.1,
// D-046.2). Es lo que alimenta el kind:"filters" que se empuja al Edge.
//
// 🔴 «TODAS» no es un detalle de eficiencia: el contrato dice que una sesión AUSENTE
// del mapa el Edge la asume `active` (fail-open). Si la foto omitiera las activas,
// coincidiría por casualidad hoy y mentiría el día que una sesión pase de pasiva a
// activa: el Edge se quedaría con el `passive` anterior y la sesión seguiría muda.
//
// Version es `max(profile_updated_at)` de las filas del tenant en MICROSEGUNDOS unix
// (int64). Es monotónica por tenant y comparable como número —que es lo que el Edge
// necesita para descartar una versión vieja que llegue tarde— y NO es un hash del
// payload: dos cambios que se cancelan (active→passive→active) darían el mismo hash y
// el Edge ignoraría el segundo.
//
// 🔴 `profile_updated_at` (migración 0065) y NO `updated_at`. La primera versión de
// T2.1 usaba `updated_at`, y el code review del 2026-08-21 lo tumbó: `updated_at` es
// el reloj de la FILA y lo mueve CUALQUIER escritura —MarkOnline, el SaveHealth de
// CADA heartbeat, SetSelfPn—, así que la versión avanzaba sin que avanzara el
// contenido. Y no era un detalle estético: MarkOnline corre inmediatamente antes de
// pushConfigsOnConnect, o sea que un Edge con N sesiones del tenant publicaba N
// versiones distintas y crecientes CON EL MAPA IDÉNTICO en cada reconexión. El Edge
// las re-aplica, las re-persiste en su SQLite, y las que llegaran desordenadas le
// hacían emitir su WARN «versión anterior o igual» EN OPERACIÓN NORMAL — enterrando
// la única línea de log que delataría una anomalía real de versionado.
//
// Version = 0 con Sessions vacío significa «este tenant no tiene ni una fila de
// sesión». No es un error: se empuja igual (regla 2 de T2.1).
//
// La foto la sirve la implementación (ProfilesByTenant), no Repository: es la
// lectura de UN consumidor, el proveedor del kind:"filters", y meterla en el puerto
// obligaría a implementarla a todo decorador sin que ninguno la use.
type TenantProfiles struct {
	// Version es max(profile_updated_at) del tenant en microsegundos unix.
	Version int64
	// Sessions mapea session_id → perfil vigente. Nunca nil cuando lo devuelve una
	// implementación (mapa vacío si no hay filas); el valor cero del tipo sí lo trae nil.
	Sessions map[string]Profile
}

// ValidProfile indica si p es un perfil conocido (active|passive). La comparación
// es exacta: el vacío, otra caja ("ACTIVE") o los valores del eje retirado
// ("bot", "human") dan false.
func ValidProfile(p Profile) bool { return p == ProfileActive || p == ProfilePassive }

// Session refleja una fila de public.fleet_sessions. Capabilities se omite a
// propósito: el contrato CloudLink v0.1.0 no transporta capacidades aún.
type Session struct {
	TenantID  string
	EdgeID    string
	SessionID string
	State     State
	// Profile es el perfil de negocio de la sesión (active|passive, Plan 046 ·
	// T1.1). Es el campo con el que se decide si el motor reactivo actúa.
	Profile Profile
	// SelfPn es el número propio (E.164 sin '+', normalizado) que la sesión
	// reporta en su Heartbeat (Plan 020 · T2). Vacío mientras la sesión no reporte
	// uno (sin emparejar). Lo consume el anti-self-loop del runtime.
	//
	// 🔒 EN REPOSO VA CIFRADO (Plan 046 · T4.1) pero ESTE CAMPO SIGUE SIENDO EL
	// NÚMERO EN CLARO: el contrato público no cambió (la API de sesiones expone
	// `self_pn` y el BFF lo consume). El repositorio abre el sobre al servir y lo
	// cierra al escribir; el número en claro solo vive en memoria.
	//
	// ⚠️ VACÍO YA TIENE DOS CAUSAS, no una: «esta sesión no ha reportado número»
	// (lo de siempre) y «el sobre no se pudo descifrar» — el segundo caso deja
	// UNA línea Warn AGREGADA por llamada al repositorio (con el conteo de filas
	// afectadas, no una línea por fila) y NO tumba el listado. La API no los
	// distingue a propósito.
	SelfPn          string
	LastConnectedAt time.Time
	LastSeenAt      time.Time

	// --- Salud real del socket (Plan 031 · T3, ADR-0023). SEPARADA de State (que
	// es el registro del stream CloudLink): un Edge viejo no reporta salud y estos
	// campos quedan en su cero (WhatsappState "", timestamps IsZero). ---

	// WhatsappState es la verdad del socket whatsmeow que el Edge reporta en el
	// SessionHealth de su Heartbeat: connected|connecting|degraded|dead. Vacío si
	// la sesión aún no reportó salud. NO se confunde con State (link CloudLink).
	WhatsappState string
	// DegradedReason es el motivo del degradado (p. ej. dek_load_timeout); vacío si
	// el socket está sano.
	DegradedReason string
	// DegradedSince marca cuándo la sesión ENTRÓ en degradado (IsZero si sana). La
	// gobierna SaveHealth: se fija al entrar y se limpia al salir.
	DegradedSince time.Time
	// LastHealthAt es la marca del último snapshot de salud recibido (IsZero si
	// nunca reportó). Alimenta la derivación de stale en la API (T4).
	LastHealthAt time.Time
	// LastEventAgeS es la prueba de vida: segundos desde el último evento entrante.
	LastEventAgeS int64
	// OutboxDepth es la profundidad del outbox del Edge (ADR-0003).
	OutboxDepth int64
	// BinaryVersion es la versión del binario del Edge (la consumirá el auto-update).
	BinaryVersion string
	// UptimeS es el uptime del daemon del Edge en segundos.
	UptimeS int64
	// DekLoadDurationMs es la duración de la última carga de la DEK en ms.
	DekLoadDurationMs int64
	// IntentCircuit es el estado del circuito del clasificador (closed|open|half_open);
	// vacío si el 029 no aplica.
	//
	// ⚠️ Hasta cloudlink v0.12.0 este campo SIEMPRE viajaba vacío (el Edge nunca lo
	// llenaba). Desde el 051 · T4.3 llega LLENO. Ningún consumidor puede seguir
	// asumiendo que está vacío — y vacío sigue significando «no lo sé», NUNCA
	// «closed» (decisión 4 de la Ola 4, 2026-08-17).
	IntentCircuit string

	// --- Salud del WORKER del cajero de intents (Plan 051 · T4.3, campos 9-15 del
	// SessionHealth). 🔴 REGLA: nil/vacío = «este Edge NO LO SABE», jamás «está
	// bien». Por eso lo medible va en PUNTERO y no en su cero: un 0 de valor y un
	// «no lo sé» son cosas distintas y la consola no puede confundirlos. ---

	// WorkerTaskset es el veredicto del reparto de CPU entre el cajero y Ollama
	// (T2.8): disjunta|solapada|cajero_sin_confinar. VACÍO = el Edge no lo sabe (no
	// es Linux, o el parte del worker está rancio): NUNCA se lee como "disjunta".
	WorkerTaskset string
	// IntentP50Ms es el p50 en ms de la INFERENCIA del clasificador. nil = NO
	// MEDIBLE. El contrato lo transporta como 0 y el ingestor traduce ese 0 a nil:
	// persistir el 0 lo dejaría leyéndose como "instantáneo", que es falso. NO es
	// el p50 del handler de whatsmeow (otra población y otro proceso).
	IntentP50Ms *int64
	// IntentOmittedByReason desglosa los despachos que salieron SIN intent por
	// motivo (INV-051.3). nil = no reportado. 🔴 NUNCA se agrega en un total:
	// "fastlane" es el camino SANO y "presupuesto"/"breaker" son FALLOS. Solo
	// llegan las claves con valor distinto de cero: una clave AUSENTE no es un
	// "cero medido". Leer un mapa nil devuelve el cero sin panic, pero iterarlo da
	// cero vueltas: no supongas las ocho claves presentes.
	IntentOmittedByReason map[string]int64
	// StuckHeads son las cabezas de cola detectadas ATASCADAS (T3.12). nil = la
	// fila nunca recibió el bloque del worker; 0 = no ocurrió (o el Edge no lo mide).
	StuckHeads *int64
	// StuckHeadPolls son los sondeos de una cabeza atascada (T3.12).
	StuckHeadPolls *int64
	// FailedSealDispatch son los fallos al SELLAR EL DESPACHO (T3.12). 🔴 SEPARADO
	// de FailedSealBudget a propósito: solo ESTE implica mensajes DUPLICADOS.
	FailedSealDispatch *int64
	// FailedSealBudget son los fallos al SELLAR EL PRESUPUESTO (T3.12). NO implica
	// duplicados: solo descuadra la contabilidad del gasto. Agregarlo con
	// FailedSealDispatch deshace T3.12.
	FailedSealBudget *int64
}

// HealthSnapshot es el último estado de salud que una sesión reporta en el
// SessionHealth adjunto a su Heartbeat (Plan 031 · T3, ADR-0023). El Gateway lo
// arma desde el proto (mapeando el enum del socket a texto) y se lo pasa a
// SaveHealth; el dominio fleet no importa el contrato CloudLink. Lista CERRADA de
// campos: solo metadatos de salud, CERO PII/llaves/credenciales.
//
// Plan 051 · T4.3 suma el bloque del WORKER del cajero de intents (campos 9-15 del
// contrato). 🔴 Lo medible viaja en PUNTERO / mapa nil-able porque nil significa
// «este Edge NO LO SABE» y NO puede colapsarse con el cero: el Gateway traduce a
// nil los ceros que el contrato define como «no medible».
type HealthSnapshot struct {
	WhatsappState     string
	DegradedReason    string
	LastEventAgeS     int64
	DekLoadDurationMs int64
	IntentCircuit     string
	OutboxDepth       int64
	BinaryVersion     string
	UptimeS           int64

	// Bloque del worker (Plan 051 · T4.3). Ver los comentarios homónimos de Session.
	WorkerTaskset         string
	IntentP50Ms           *int64
	IntentOmittedByReason map[string]int64
	StuckHeads            *int64
	StuckHeadPolls        *int64
	FailedSealDispatch    *int64
	FailedSealBudget      *int64
}

// Degraded indica si el snapshot representa un socket NO sano (degraded o dead, o
// con un motivo de degradado presente). Gobierna la marca degraded_since: se fija
// al entrar en este estado y se limpia al salir. Un socket connected/connecting sin
// motivo es sano.
//
// Son tres condiciones y basta una: DegradedReason no vacío, WhatsappState
// "degraded" o WhatsappState "dead". La comparación del estado es exacta (otra caja
// no cuenta) y cualquier otro texto, sin motivo, es sano.
//
// Valor cero: el snapshot cero (WhatsappState "", sin motivo) da false. Es el parte
// de un Edge que aún no sabe su salud, y se porta tal cual: «no lo sé» no abre un
// degradado, y si lo había lo cierra.
func (h HealthSnapshot) Degraded() bool {
	return h.DegradedReason != "" || h.WhatsappState == "degraded" || h.WhatsappState == "dead"
}

// Repository persiste el estado de las sesiones. La clave lógica es
// (TenantID, EdgeID, SessionID). Son DIEZ métodos y la lista es cerrada: todo
// decorador del puerto (fleethelpertest.SlowRepository, los espías de los tests del
// gateway) los implementa uno a uno, así que una lectura de un solo consumidor no
// entra aquí (ver TenantProfiles).
//
// Sus promesas las fija fleethelpertest.ContratoRepository, que corren las dos
// implementaciones.
type Repository interface {
	// MarkOnline registra/actualiza la sesión como online (last_connected_at y
	// last_seen_at = ahora). Una sesión NUEVA nace con perfil PASIVO; una que
	// reconecta conserva su perfil, que solo mueve SetProfile.
	MarkOnline(ctx context.Context, tenantID, edgeID, sessionID string) error
	// MarkOffline marca la sesión como offline (last_seen_at = ahora). No falla si
	// la sesión no existía.
	MarkOffline(ctx context.Context, tenantID, edgeID, sessionID string) error
	// MarkLoggedOut marca la sesión como zombie (StateLoggedOut): WhatsApp cerró el
	// device (Plan 020 · T3). Es distinto de MarkOffline (offline por red): el zombie
	// lo dispara la señal explícita del Edge (Heartbeat State=LOGGED_OUT), no la
	// caída del stream. No falla si la sesión no existía (UPDATE de 0 filas es válido).
	MarkLoggedOut(ctx context.Context, tenantID, edgeID, sessionID string) error
	// SetState fija el estado de la sesión sessionID del tenant a uno del conjunto
	// admin-admitido (offline|loggedout), para retirar/limpiar una sesión zombie
	// (Plan 020 · T3). Acota por tenant_id + session_id (aislamiento multi-tenant,
	// INV-8): toca TODAS las filas de esa sesión bajo el tenant. found=false si
	// ninguna casa (sesión de otro tenant ⇒ 404 opaco). Devuelve ErrInvalidState si
	// state ∉ {offline,loggedout}, con found=false y sin escribir nada.
	SetState(ctx context.Context, tenantID, sessionID string, state State) (found bool, err error)
	// CountLiveBySelfPn cuenta las sesiones VIVAS (state != loggedout) del tenant que
	// reportan el mismo self_pn. Alimenta el aviso del tope de dispositivos (REQ-D4).
	// Un selfPn vacío devuelve 0 (sin número no hay número que contar).
	//
	// El número se NORMALIZA antes de comparar (Plan 046 · T4.1): en Postgres la
	// comparación es por índice ciego del valor canónico, no por texto. Un número
	// que no normaliza devuelve error, no 0: «no puedo contar» ≠ «hay cero».
	CountLiveBySelfPn(ctx context.Context, tenantID, selfPn string) (int, error)
	// SaveHealth persiste el último snapshot de salud (SessionHealth) que la sesión
	// reporta en su Heartbeat (Plan 031 · T3). UPDATE acotado por (tenant_id, edge_id,
	// session_id): SEPARA whatsapp_state (verdad del socket) del State (link CloudLink),
	// que NO toca. Fija degraded_since al ENTRAR en degradado y lo limpia al salir
	// (h.Degraded()); refresca last_health_at. No falla si la fila aún no existe
	// (UPDATE de 0 filas es válido: el próximo Heartbeat, tras el registro, la fijará).
	//
	// El bloque del worker se escribe SIEMPRE, también cuando llega desconocido: un
	// parte rancio BORRA el valor anterior. nil se queda nil y un 0 medido vuelve
	// como puntero a 0; un desglose vacío o nil se lee como nil.
	SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h HealthSnapshot) error
	// Get devuelve la sesión y si existe. Una sesión que no existe es
	// (Session{}, false, nil), no un error.
	Get(ctx context.Context, tenantID, edgeID, sessionID string) (s Session, found bool, err error)
	// List devuelve las sesiones de un tenant (para tests/diagnóstico), y solo las
	// de ese tenant. El orden no es parte del contrato.
	List(ctx context.Context, tenantID string) ([]Session, error)
	// SetSelfPn persiste el número propio (self_pn) que la sesión reporta en su
	// Heartbeat (Plan 020 · T2). Acota por (tenant_id, edge_id, session_id). Un
	// selfPn VACÍO es un no-op: NO sobrescribe un valor previo bueno (protege el
	// dato ante Heartbeats de una sesión que aún no se emparejó). No falla si la
	// fila no existe todavía (UPDATE de 0 filas es válido).
	//
	// 🔒 EL NÚMERO SE GUARDA CIFRADO (Plan 046 · T4.1): en Postgres van el sobre
	// (enc/dek/kek_id) y el índice ciego, y la columna en claro se VACÍA en el
	// mismo UPDATE. Lo que se persiste es siempre el valor NORMALIZADO; un número
	// que no normaliza devuelve error y no escribe nada.
	SetSelfPn(ctx context.Context, tenantID, edgeID, sessionID, selfPn string) error
	// SetProfile fija el PERFIL (active|passive) de la sesión sessionID del tenant
	// tenantID (Plan 046 · T1.2). Es la ÚNICA escritura del eje: el alias legado
	// `role` y su SetRole se retiraron con la 0064. Y es la única que mueve la
	// versión de TenantProfiles.
	//
	// Aislamiento por tenant_id + session_id (INV-8 del Plan 018): actualiza
	// TODAS las filas de esa sesión bajo el tenant y devuelve found=false si
	// ninguna casa (sesión de otro tenant ⇒ 404 opaco). Devuelve ErrInvalidProfile
	// si profile ∉ {active,passive}, con found=false y sin escribir nada.
	SetProfile(ctx context.Context, tenantID, sessionID string, profile Profile) (found bool, err error)
}
