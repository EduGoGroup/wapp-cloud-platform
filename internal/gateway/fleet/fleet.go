// Package fleet lleva el registro durable del estado online/offline de las
// sesiones CloudLink de cada Edge (tabla public.fleet_sessions). El estado es
// DERIVADO del stream vivo: el Gateway marca online al conectar una sesión y
// offline al caer. La fuente viva (para empujar comandos) está en memoria en
// session.Registry; esta capa solo durabiliza el estado para auditoría/admin.
//
// Repository tiene impl memory (unit CI-safe) y postgres (integración).
package fleet

import (
	"context"
	"errors"
	"sync"
	"time"
)

// State es el conjunto de estados posibles de una sesión.
type State string

const (
	// StateOnline indica que el stream de la sesión está vivo.
	StateOnline State = "online"
	// StateOffline indica que el stream de la sesión cayó (offline por red). Es
	// DERIVADO del cierre del stream (onStreamClosed): recuperable al reconectar.
	StateOffline State = "offline"
	// StateLoggedOut indica una sesión ZOMBIE: WhatsApp cerró el device (el Edge lo
	// reporta explícitamente con un Heartbeat State=LOGGED_OUT, Plan 020 · T3). Se
	// distingue del offline-por-red (que produce el cierre del stream): un zombie no
	// vuelve solo, hay que reemparejar el device. No renueva su lease (sesión muerta).
	StateLoggedOut State = "loggedout"
)

// DeviceLimit es el tope de dispositivos vinculados por número de WhatsApp
// (REQ-D4). Al superarlo, WhatsApp rechaza nuevos emparejamientos; el Cloud emite
// un aviso (sin PII) cuando cuenta más sesiones VIVAS con el mismo self_pn.
const DeviceLimit = 4

// ErrInvalidState lo devuelve SetState cuando el estado pedido no pertenece al
// conjunto que un admin puede fijar (offline|loggedout). Se inspecciona con errors.Is.
var ErrInvalidState = errors.New("estado de sesión inválido (usar offline|loggedout)")

// ValidAdminState indica si s pertenece al conjunto de estados que un admin puede
// fijar a mano (offline|loggedout): retirar/limpiar una sesión zombie o dejarla
// offline. StateOnline NO se admite: es DERIVADO del stream vivo (no se falsea).
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
type Profile string

const (
	// ProfileActive ejecuta el motor de flujos: dispara triggers y auto-responde.
	ProfileActive Profile = "active"
	// ProfilePassive solo emite: NO dispara triggers ni auto-responde, y sus
	// entrantes se filtran en el Edge (ADR-0027, Ola 2).
	//
	// 🔴 Es el DEFAULT de la columna (0063, D-07): una sesión recién emparejada nace
	// PASIVA. Distinto del DEFAULT 'bot' que traía la 0025 — cambio deliberado, no
	// un descuido: privacidad por defecto hasta que el dueño active la sesión.
	ProfilePassive Profile = "passive"
)

// ErrInvalidProfile lo devuelve el store cuando el perfil pedido no es
// active|passive. Se inspecciona con errors.Is.
var ErrInvalidProfile = errors.New("perfil de sesión inválido (usar active|passive)")

// ValidProfile indica si p es un perfil conocido (active|passive).
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
// nil los ceros que el contrato define como «no medible» (ver mapeo en connect.go).
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

// cloneReasons devuelve una copia defensiva del desglose de motivos, o nil si no
// hay nada que copiar. El mapa llega del proto (o de un test) y el repositorio en
// memoria NO puede quedarse con el mismo respaldo que el llamante: una mutación
// posterior del Edge/test cambiaría la salud ya "persistida". nil y vacío colapsan
// a nil a propósito: un Edge nuevo SIN omisiones y un Edge viejo son
// indistinguibles en el cable, y ante la duda la lectura honesta es «no lo sé».
func cloneReasons(m map[string]int64) map[string]int64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// cloneInt64 duplica un puntero a int64 (nil se propaga como nil), para que el
// snapshot persistido no comparta respaldo con el llamante.
func cloneInt64(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// Degraded indica si el snapshot representa un socket NO sano (degraded o dead, o
// con un motivo de degradado presente). Gobierna la marca degraded_since: se fija
// al entrar en este estado y se limpia al salir. Un socket connected/connecting sin
// motivo es sano.
func (h HealthSnapshot) Degraded() bool {
	return h.DegradedReason != "" || h.WhatsappState == "degraded" || h.WhatsappState == "dead"
}

// Repository persiste el estado de las sesiones. La clave lógica es
// (TenantID, EdgeID, SessionID).
type Repository interface {
	// MarkOnline registra/actualiza la sesión como online (last_connected_at y
	// last_seen_at = ahora).
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
	// state ∉ {offline,loggedout}.
	SetState(ctx context.Context, tenantID, sessionID string, state State) (found bool, err error)
	// CountLiveBySelfPn cuenta las sesiones VIVAS (state != loggedout) del tenant que
	// reportan el mismo self_pn. Alimenta el aviso del tope de dispositivos (REQ-D4).
	// Un selfPn vacío devuelve 0 (sin número no hay número que contar).
	CountLiveBySelfPn(ctx context.Context, tenantID, selfPn string) (int, error)
	// SaveHealth persiste el último snapshot de salud (SessionHealth) que la sesión
	// reporta en su Heartbeat (Plan 031 · T3). UPDATE acotado por (tenant_id, edge_id,
	// session_id): SEPARA whatsapp_state (verdad del socket) del State (link CloudLink),
	// que NO toca. Fija degraded_since al ENTRAR en degradado y lo limpia al salir
	// (h.Degraded()); refresca last_health_at. No falla si la fila aún no existe
	// (UPDATE de 0 filas es válido: el próximo Heartbeat, tras el registro, la fijará).
	SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h HealthSnapshot) error
	// Get devuelve la sesión y si existe.
	Get(ctx context.Context, tenantID, edgeID, sessionID string) (s Session, found bool, err error)
	// List devuelve las sesiones de un tenant (para tests/diagnóstico).
	List(ctx context.Context, tenantID string) ([]Session, error)
	// SetSelfPn persiste el número propio (self_pn) que la sesión reporta en su
	// Heartbeat (Plan 020 · T2). Acota por (tenant_id, edge_id, session_id). Un
	// selfPn VACÍO es un no-op: NO sobrescribe un valor previo bueno (protege el
	// dato ante Heartbeats de una sesión que aún no se emparejó). No falla si la
	// fila no existe todavía (UPDATE de 0 filas es válido).
	SetSelfPn(ctx context.Context, tenantID, edgeID, sessionID, selfPn string) error
	// SetProfile fija el PERFIL (active|passive) de la sesión sessionID del tenant
	// tenantID (Plan 046 · T1.2). Es la ÚNICA escritura del eje: el alias legado
	// `role` y su SetRole se retiraron con la 0064.
	//
	// Aislamiento por tenant_id + session_id (INV-8 del Plan 018): actualiza
	// TODAS las filas de esa sesión bajo el tenant y devuelve found=false si
	// ninguna casa (sesión de otro tenant ⇒ 404 opaco). Devuelve ErrInvalidProfile
	// si profile ∉ {active,passive}.
	SetProfile(ctx context.Context, tenantID, sessionID string, profile Profile) (found bool, err error)
}

// MemoryRepository es una implementación en memoria de Repository, segura
// para concurrencia. Pensada para tests unitarios CI-safe (sin BD).
type MemoryRepository struct {
	mu       sync.Mutex
	sessions map[string]Session
	now      func() time.Time
}

// NewMemoryRepository crea un repositorio en memoria vacío con reloj wall-clock.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{sessions: make(map[string]Session), now: time.Now}
}

func memKey(tenantID, edgeID, sessionID string) string {
	return tenantID + "\x00" + edgeID + "\x00" + sessionID
}

// MarkOnline implementa Repository. Preserva el perfil existente (lo gobierna
// SetProfile, no la señal de conexión): una sesión que reconecta conserva su
// active|passive.
func (r *MemoryRepository) MarkOnline(_ context.Context, tenantID, edgeID, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	key := memKey(tenantID, edgeID, sessionID)
	s := r.sessions[key] // rol/valores previos si existía; zero-Session si no.
	s.TenantID = tenantID
	s.EdgeID = edgeID
	s.SessionID = sessionID
	s.State = StateOnline
	s.Profile = defaultProfile(s.Profile)
	s.LastConnectedAt = now
	s.LastSeenAt = now
	r.sessions[key] = s
	return nil
}

// MarkOffline implementa Repository.
func (r *MemoryRepository) MarkOffline(_ context.Context, tenantID, edgeID, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	key := memKey(tenantID, edgeID, sessionID)
	s, ok := r.sessions[key]
	if !ok {
		s = Session{TenantID: tenantID, EdgeID: edgeID, SessionID: sessionID}
	}
	s.State = StateOffline
	s.Profile = defaultProfile(s.Profile)
	s.LastSeenAt = now
	r.sessions[key] = s
	return nil
}

// MarkLoggedOut implementa Repository: marca la sesión zombie (StateLoggedOut).
// Como MarkOffline, no falla si la sesión no existía (la crea marcada zombie).
func (r *MemoryRepository) MarkLoggedOut(_ context.Context, tenantID, edgeID, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	key := memKey(tenantID, edgeID, sessionID)
	s, ok := r.sessions[key]
	if !ok {
		s = Session{TenantID: tenantID, EdgeID: edgeID, SessionID: sessionID}
	}
	s.State = StateLoggedOut
	s.Profile = defaultProfile(s.Profile)
	s.LastSeenAt = now
	r.sessions[key] = s
	return nil
}

// SetState implementa Repository: fija el estado de todas las filas de la sesión
// bajo el tenant a un estado admin-admitido. found=false si ninguna casa
// (aislamiento por tenant). Devuelve ErrInvalidState si state no es admitido.
func (r *MemoryRepository) SetState(_ context.Context, tenantID, sessionID string, state State) (bool, error) {
	if !ValidAdminState(state) {
		return false, ErrInvalidState
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	found := false
	for k, s := range r.sessions {
		if s.TenantID == tenantID && s.SessionID == sessionID {
			s.State = state
			s.LastSeenAt = now
			r.sessions[k] = s
			found = true
		}
	}
	return found, nil
}

// CountLiveBySelfPn implementa Repository: cuenta las sesiones vivas (no zombie)
// del tenant con el self_pn dado. selfPn vacío ⇒ 0.
func (r *MemoryRepository) CountLiveBySelfPn(_ context.Context, tenantID, selfPn string) (int, error) {
	if selfPn == "" {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, s := range r.sessions {
		if s.TenantID == tenantID && s.SelfPn == selfPn && s.State != StateLoggedOut {
			n++
		}
	}
	return n, nil
}

// defaultProfile normaliza un perfil vacío a ProfilePassive, espejando el DEFAULT
// de la columna profile (0063, D-07).
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

// SetProfile implementa Repository: fija el perfil de todas las filas de la sesión
// bajo el tenant. found=false si ninguna casa (aislamiento por tenant).
func (r *MemoryRepository) SetProfile(_ context.Context, tenantID, sessionID string, profile Profile) (bool, error) {
	if !ValidProfile(profile) {
		return false, ErrInvalidProfile
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	found := false
	for k, s := range r.sessions {
		if s.TenantID == tenantID && s.SessionID == sessionID {
			s.Profile = profile
			r.sessions[k] = s
			found = true
		}
	}
	return found, nil
}

// SetSelfPn implementa Repository: fija el self_pn de la sesión. selfPn vacío es
// un no-op (protege un valor previo bueno). No falla si la sesión no existe aún.
func (r *MemoryRepository) SetSelfPn(_ context.Context, tenantID, edgeID, sessionID, selfPn string) error {
	if selfPn == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := memKey(tenantID, edgeID, sessionID)
	s, ok := r.sessions[key]
	if !ok {
		return nil
	}
	s.SelfPn = selfPn
	r.sessions[key] = s
	return nil
}

// SaveHealth implementa Repository: persiste el snapshot de salud en la sesión.
// No-op si la sesión no existe aún (espeja el UPDATE de 0 filas de Postgres).
// Fija degraded_since al entrar en degradado y lo limpia al salir.
func (r *MemoryRepository) SaveHealth(_ context.Context, tenantID, edgeID, sessionID string, h HealthSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := memKey(tenantID, edgeID, sessionID)
	s, ok := r.sessions[key]
	if !ok {
		return nil
	}
	now := r.now().UTC()
	s.WhatsappState = h.WhatsappState
	s.DegradedReason = h.DegradedReason
	s.LastEventAgeS = h.LastEventAgeS
	s.DekLoadDurationMs = h.DekLoadDurationMs
	s.IntentCircuit = h.IntentCircuit
	s.OutboxDepth = h.OutboxDepth
	s.BinaryVersion = h.BinaryVersion
	s.UptimeS = h.UptimeS
	// Bloque del worker (Plan 051 · T4.3): se copia TAL CUAL, incluidos los nil.
	// Un snapshot que no sabe el taskset debe BORRAR el valor anterior, no
	// conservarlo: mantener un "disjunta" viejo cuando el parte del worker se
	// volvió rancio es exactamente publicar una señal de salud inventada.
	s.WorkerTaskset = h.WorkerTaskset
	s.IntentP50Ms = cloneInt64(h.IntentP50Ms)
	s.IntentOmittedByReason = cloneReasons(h.IntentOmittedByReason)
	s.StuckHeads = cloneInt64(h.StuckHeads)
	s.StuckHeadPolls = cloneInt64(h.StuckHeadPolls)
	s.FailedSealDispatch = cloneInt64(h.FailedSealDispatch)
	s.FailedSealBudget = cloneInt64(h.FailedSealBudget)
	s.LastHealthAt = now
	if h.Degraded() {
		if s.DegradedSince.IsZero() {
			s.DegradedSince = now
		}
	} else {
		s.DegradedSince = time.Time{}
	}
	r.sessions[key] = s
	return nil
}

// Get implementa Repository. La Session sale con copias del desglose de motivos y
// de los punteros del bloque del worker: el llamante no comparte respaldo con el
// repositorio (Plan 051 · T4.3).
func (r *MemoryRepository) Get(_ context.Context, tenantID, edgeID, sessionID string) (Session, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[memKey(tenantID, edgeID, sessionID)]
	if !ok {
		return Session{}, false, nil
	}
	return detachWorkerHealth(s), true, nil
}

// List implementa Repository.
func (r *MemoryRepository) List(_ context.Context, tenantID string) ([]Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		if s.TenantID == tenantID {
			out = append(out, detachWorkerHealth(s))
		}
	}
	return out, nil
}

// detachWorkerHealth devuelve una Session cuyo bloque del worker (mapa y punteros)
// ya no comparte respaldo con el repositorio en memoria.
func detachWorkerHealth(s Session) Session {
	s.IntentOmittedByReason = cloneReasons(s.IntentOmittedByReason)
	s.IntentP50Ms = cloneInt64(s.IntentP50Ms)
	s.StuckHeads = cloneInt64(s.StuckHeads)
	s.StuckHeadPolls = cloneInt64(s.StuckHeadPolls)
	s.FailedSealDispatch = cloneInt64(s.FailedSealDispatch)
	s.FailedSealBudget = cloneInt64(s.FailedSealBudget)
	return s
}
