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

// Role es el rol operativo de una sesión (Plan 020 · T1). Gobierna si el motor
// reactivo de flujos actúa sobre sus entrantes.
type Role string

const (
	// RoleBot ejecuta el motor de flujos: dispara triggers y auto-responde. Es el
	// DEFAULT (columna role DEFAULT 'bot') ⇒ no-regresión de todo lo previo al 020.
	RoleBot Role = "bot"
	// RolePassive solo escucha/transporta: NO dispara triggers ni auto-responde.
	RolePassive Role = "passive"
)

// ErrInvalidRole lo devuelve SetRole cuando el rol pedido no es bot|passive. Se
// inspecciona con errors.Is.
var ErrInvalidRole = errors.New("rol de sesión inválido (usar bot|passive)")

// ValidRole indica si r es un rol conocido (bot|passive).
func ValidRole(r Role) bool { return r == RoleBot || r == RolePassive }

// Session refleja una fila de public.fleet_sessions. Capabilities se omite a
// propósito: el contrato CloudLink v0.1.0 no transporta capacidades aún.
type Session struct {
	TenantID  string
	EdgeID    string
	SessionID string
	State     State
	Role      Role
	// SelfPn es el número propio (E.164 sin '+', normalizado) que la sesión
	// reporta en su Heartbeat (Plan 020 · T2). Vacío mientras la sesión no reporte
	// uno (sin emparejar). Lo consume el anti-self-loop del runtime.
	SelfPn          string
	LastConnectedAt time.Time
	LastSeenAt      time.Time
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
	// SetRole fija el rol (bot|passive) de la sesión sessionID del tenant tenantID
	// (Plan 020 · T1). Acota por tenant_id + session_id (aislamiento multi-tenant,
	// INV-8): actualiza TODAS las filas de esa sesión bajo el tenant (un mismo
	// session_id puede colgar de varios edge_id del MISMO tenant). found=false si
	// ninguna fila del tenant casa el session_id (una sesión de otro tenant queda
	// invisible ⇒ 404 opaco). Devuelve ErrInvalidRole si role ∉ {bot,passive}.
	SetRole(ctx context.Context, tenantID, sessionID string, role Role) (found bool, err error)
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

// MarkOnline implementa Repository. Preserva el rol existente (el rol lo gobierna
// SetRole, no la señal de conexión): una sesión que reconecta conserva su bot|passive.
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
	s.Role = defaultRole(s.Role)
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
	s.Role = defaultRole(s.Role)
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
	s.Role = defaultRole(s.Role)
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

// defaultRole normaliza un rol vacío a RoleBot (espeja la columna DEFAULT 'bot').
func defaultRole(r Role) Role {
	if r == "" {
		return RoleBot
	}
	return r
}

// SetRole implementa Repository: fija el rol de todas las filas de la sesión bajo
// el tenant. found=false si ninguna casa (aislamiento por tenant).
func (r *MemoryRepository) SetRole(_ context.Context, tenantID, sessionID string, role Role) (bool, error) {
	if !ValidRole(role) {
		return false, ErrInvalidRole
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	found := false
	for k, s := range r.sessions {
		if s.TenantID == tenantID && s.SessionID == sessionID {
			s.Role = role
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

// Get implementa Repository.
func (r *MemoryRepository) Get(_ context.Context, tenantID, edgeID, sessionID string) (Session, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[memKey(tenantID, edgeID, sessionID)]
	return s, ok, nil
}

// List implementa Repository.
func (r *MemoryRepository) List(_ context.Context, tenantID string) ([]Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		if s.TenantID == tenantID {
			out = append(out, s)
		}
	}
	return out, nil
}
