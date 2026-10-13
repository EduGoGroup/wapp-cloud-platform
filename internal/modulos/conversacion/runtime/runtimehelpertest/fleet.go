package runtimehelpertest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Los valores de las columnas state y profile de public.fleet_sessions, tal cual los escribe el
// esquema (CHECK de la 0029 y de la 0063). Están aquí para que las suites y los tests que siembran
// sesiones no repitan literales.
const (
	StateOnline    = "online"
	StateOffline   = "offline"
	StateLoggedOut = "loggedout"

	ProfileActive  = "active"
	ProfilePassive = "passive"
)

// Session es una fila de public.fleet_sessions vista por lo ÚNICO que leen los dos adaptadores del
// runtime: su clave (tenant, edge, sesión), su estado, su perfil y el índice ciego de su número.
// Es lo que se siembra (Seed) y lo que se observa (Sessions) en los dos Montajes.
type Session struct {
	TenantID, EdgeID, SessionID string
	// State es online, offline o loggedout.
	State string
	// Profile es el perfil de la sesión. Al SEMBRAR, vacío significa «no escribir la columna»: la
	// fila cae al DEFAULT del esquema, que es passive (0063, D-07). Al OBSERVAR nunca viene vacío.
	Profile string
	// SelfPnBidx es el índice ciego del número propio; vacío es NULL (sesión sin número conocido).
	SelfPnBidx string
}

// FleetSessions es public.fleet_sessions en memoria: la tabla sobre la que responden los gemelos
// de los dos adaptadores Postgres del runtime (MemoryTenantResolver y MemorySelfNumbers). Imita lo
// que del esquema pesa en sus consultas: la clave primaria (tenant, edge, sesión), el DEFAULT
// passive del perfil y el CHECK de su dominio, que se puede retirar (AllowAnyProfile) como hace la
// suite contra Postgres para sembrar un perfil que hoy no existe.
//
// Seguro para uso concurrente. El valor cero no sirve: se construye con NewFleetSessions.
type FleetSessions struct {
	mu         sync.Mutex
	rows       map[sessionKey]Session
	anyProfile bool
}

// sessionKey es la clave primaria de la tabla.
type sessionKey struct{ tenantID, edgeID, sessionID string }

// NewFleetSessions devuelve la tabla vacía, con el CHECK del perfil puesto.
func NewFleetSessions() *FleetSessions {
	return &FleetSessions{rows: make(map[sessionKey]Session)}
}

// Seed inserta la sesión o, si ya existe una con su misma clave (tenant, edge, sesión), le cambia
// estado, perfil e índice. Un Profile vacío deja el perfil en el DEFAULT del esquema, passive.
//
// Devuelve error, sin tocar la tabla, si falta alguna parte de la clave, si el estado no es uno de
// los tres del esquema o si el perfil está fuera de dominio y no se llamó antes a AllowAnyProfile.
func (f *FleetSessions) Seed(s Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.TenantID == "" || s.EdgeID == "" || s.SessionID == "" {
		return fmt.Errorf("fleet_sessions en memoria: la clave (%q, %q, %q) tiene una parte vacía", s.TenantID, s.EdgeID, s.SessionID)
	}
	if s.State != StateOnline && s.State != StateOffline && s.State != StateLoggedOut {
		return fmt.Errorf("fleet_sessions en memoria: el estado %q viola fleet_sessions_state_chk", s.State)
	}
	if s.Profile == "" {
		s.Profile = ProfilePassive
	}
	if !f.anyProfile && s.Profile != ProfileActive && s.Profile != ProfilePassive {
		return fmt.Errorf("fleet_sessions en memoria: el perfil %q viola fleet_sessions_profile_chk", s.Profile)
	}
	f.rows[sessionKey{s.TenantID, s.EdgeID, s.SessionID}] = s
	return nil
}

// AllowAnyProfile retira el CHECK del dominio del perfil: desde aquí Seed acepta cualquiera.
func (f *FleetSessions) AllowAnyProfile() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.anyProfile = true
}

// Rows devuelve una copia de TODAS las filas, ordenadas por tenant, edge y sesión.
func (f *FleetSessions) Rows() []Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Session, 0, len(f.rows))
	for _, s := range f.rows {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Session) int {
		return cmp.Or(cmp.Compare(a.TenantID, b.TenantID), cmp.Compare(a.EdgeID, b.EdgeID), cmp.Compare(a.SessionID, b.SessionID))
	})
	return out
}

// TenantResolver devuelve el gemelo en memoria de runtime.PostgresTenantResolver sobre esta tabla.
func (f *FleetSessions) TenantResolver() *MemoryTenantResolver {
	return &MemoryTenantResolver{fleet: f}
}

// SelfNumbers devuelve el gemelo en memoria de runtime.PostgresSelfNumbers sobre esta tabla, con
// el KeyProvider dado (nil: el gemelo construido sin él).
func (f *FleetSessions) SelfNumbers(kp crypto.KeyProvider) *MemorySelfNumbers {
	return &MemorySelfNumbers{fleet: f, kp: kp}
}

// MemoryTenantResolver es el gemelo en memoria de runtime.PostgresTenantResolver: responde sobre
// una FleetSessions lo mismo que el adaptador sobre public.fleet_sessions, con sus mismos errores
// y textos. Lo fija ContratoTenantResolver, que corren los dos.
type MemoryTenantResolver struct {
	fleet *FleetSessions
}

var _ runtime.TenantResolver = (*MemoryTenantResolver)(nil)

// ResolveTenant agrupa por tenant las filas con ese session_id, sin mirar su estado. Con un solo
// tenant devuelve su id y el perfil efectivo: "passive" si ALGUNA de sus filas no es 'active',
// "active" si lo son todas. Con ninguno o con varios, el centinela runtime.ErrTenantNotResolved
// con el detalle del adaptador. Con el contexto cancelado, el error que daría la consulta.
func (r *MemoryTenantResolver) ResolveTenant(ctx context.Context, sessionID string) (tenantID string, profile string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", fmt.Errorf("resolver tenant: consulta fleet_sessions: %w", err)
	}
	var tenants []string
	anyPassive := make(map[string]bool)
	for _, s := range r.fleet.Rows() {
		if s.SessionID != sessionID {
			continue
		}
		if _, known := anyPassive[s.TenantID]; !known {
			tenants = append(tenants, s.TenantID)
		}
		anyPassive[s.TenantID] = anyPassive[s.TenantID] || s.Profile != ProfileActive
	}
	switch len(tenants) {
	case 1:
		if anyPassive[tenants[0]] {
			return tenants[0], ProfilePassive, nil
		}
		return tenants[0], ProfileActive, nil
	case 0:
		return "", "", fmt.Errorf("%w: session_id=%s (0 filas en fleet_sessions)", runtime.ErrTenantNotResolved, sessionID)
	default:
		return "", "", fmt.Errorf("%w: session_id=%s ambiguo (%d tenants)", runtime.ErrTenantNotResolved, sessionID, len(tenants))
	}
}

// MemorySelfNumbers es el gemelo en memoria de runtime.PostgresSelfNumbers: responde sobre una
// FleetSessions lo mismo que el adaptador sobre public.fleet_sessions. Calcula el índice ciego con
// SU KeyProvider, como el adaptador: si las filas se sembraron con otro, nada casa. Lo fija
// ContratoSelfNumbers, que corren los dos.
type MemorySelfNumbers struct {
	fleet *FleetSessions
	kp    crypto.KeyProvider
}

var _ runtime.SelfNumberChecker = (*MemorySelfNumbers)(nil)

// IsSelfNumber sigue el orden del adaptador: número vacío → (false, nil); sin KeyProvider →
// runtime.ErrSelfNumbersNoKeyProvider; contexto cancelado → el error que daría la consulta; y si
// no, true solo si ALGUNA fila del tenant lleva el índice ciego de ese número (calculado sobre el
// número tal cual llega), no está loggedout y su perfil no es 'passive'.
func (c *MemorySelfNumbers) IsSelfNumber(ctx context.Context, tenantID, normalizedNumber string) (bool, error) {
	if normalizedNumber == "" {
		return false, nil
	}
	if c.kp == nil {
		return false, runtime.ErrSelfNumbersNoKeyProvider
	}
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("self_numbers: consulta fleet_sessions: %w", err)
	}
	bidx := c.kp.BlindIndex(tenantID, normalizedNumber)
	for _, s := range c.fleet.Rows() {
		if s.TenantID == tenantID && s.SelfPnBidx == bidx && s.State != StateLoggedOut && s.Profile != ProfilePassive {
			return true, nil
		}
	}
	return false, nil
}
