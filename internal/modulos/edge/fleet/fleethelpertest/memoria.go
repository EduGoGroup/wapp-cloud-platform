// Porta internal/gateway/fleet/fleet.go @ 809345b (MemoryRepository)

package fleethelpertest

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// Memoria es una implementación en memoria de fleet.Repository, segura
// para concurrencia. Pensada para tests unitarios CI-safe (sin BD). En el paquete
// viejo era fleet.MemoryRepository y vivía en producción (D-F3-1).
//
// ⚠️ Diferencias con el adaptador Postgres, a propósito (se portan tal cual y las
// fija memoria_test.go, no la suite):
//   - MarkOffline y MarkLoggedOut de una sesión desconocida CREAN la fila (pasiva);
//     en Postgres son un UPDATE de 0 filas y la sesión sigue sin existir.
//   - las marcas de tiempo que nunca se fijaron salen como time.Time{}.
//   - el tenant puede ser cualquier texto (en Postgres es un UUID con clave foránea).
//   - el orden de List es el del mapa: ninguno.
type Memoria struct {
	mu       sync.Mutex
	sessions map[string]fleet.Session
	now      func() time.Time
	// profileUpdatedUs espeja la columna `profile_updated_at` (migración 0065) en
	// microsegundos unix, por clave de fila (Plan 046 · T2.1). Sin ella este doble no
	// podría responder ProfilesByTenant con una versión, y los tests de monotonicidad
	// tendrían que vivir SOLO contra Postgres.
	//
	// 🔴 ESPEJA `profile_updated_at` Y NO `updated_at`, y esa es toda la gracia: en
	// producción SOLO SetProfile mueve esa columna (y el DEFAULT now() del alta).
	// Aquí se cumple la MISMA regla —touchProfileLocked se llama solo desde SetProfile
	// y desde el alta de una fila nueva— porque un doble que se tocara en cada
	// MarkOnline/SaveHealth mentiría sobre producción justo en el punto que este
	// campo existe para vigilar: que la versión del kind:"filters" no avance cuando
	// no avanza el mapa.
	profileUpdatedUs map[string]int64
	// lastProfileUs es el último microsegundo entregado. Fuerza el ESTRICTO
	// crecimiento de la versión aunque dos escrituras caigan en el mismo microsegundo
	// de reloj: en Postgres eso no pasa (dos statements = dos now() distintos) pero en
	// un test en memoria dos llamadas seguidas sí pueden colisionar, y una versión
	// repetida haría que el Edge descartara el segundo cambio. El doble no puede ser
	// MENOS monotónico que lo que emula.
	lastProfileUs int64
}

// Memoria cumple el puerto.
var _ fleet.Repository = (*Memoria)(nil)

// NewMemoria crea un repositorio en memoria vacío con reloj wall-clock (en el
// paquete viejo, NewMemoryRepository).
func NewMemoria() *Memoria {
	return &Memoria{
		sessions:         make(map[string]fleet.Session),
		now:              time.Now,
		profileUpdatedUs: make(map[string]int64),
	}
}

// normalizeSelfPn canoniza un número propio a E.164 sin '+' ni separadores (solo
// dígitos), con el MISMO normalizador que usa el adaptador Postgres antes de
// calcular el índice ciego: contact.Normalize(contact.KindPhoneE164, …).
//
// 🔴 El doble no puede llamar al auxiliar no exportado de fleet, así que lleva el
// suyo; lo que NO puede es llevar otra regla. Si el doble guardara el valor CRUDO y
// Postgres indexara el normalizado, "+34600111222" y "34600111222" serían dos
// números aquí y uno allí: un test del tope de dispositivos (REQ-D4) pasaría en
// memoria y mentiría sobre el conteo real. Un doble que no comparte el normalizador
// con lo que emula no es un doble, es una segunda semántica.
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
func defaultProfile(p fleet.Profile) fleet.Profile {
	if p == "" {
		return fleet.ProfilePassive
	}
	return p
}

// touchProfileLocked espeja `profile_updated_at = now()` sobre la clave dada. Debe
// llamarse con r.mu tomado. Ver el comentario de lastProfileUs para el porqué del
// clamp.
//
// 🔴 SOLO puede llamarse desde SetProfile y desde el ALTA de una fila (que espeja el
// `DEFAULT now()` de la columna). Llamarla desde MarkOnline, MarkOffline,
// MarkLoggedOut, SetState, SetSelfPn o SaveHealth rompe el espejo con producción y
// deja pasar en verde exactamente el bug que la 0065 arregló.
func (r *Memoria) touchProfileLocked(key string) {
	us := r.now().UTC().UnixMicro()
	if us <= r.lastProfileUs {
		us = r.lastProfileUs + 1
	}
	r.lastProfileUs = us
	r.profileUpdatedUs[key] = us
}

// birthProfileLocked espeja el `DEFAULT now()` de profile_updated_at: fija el reloj
// del eje SOLO si la fila es NUEVA. Una fila que ya existía conserva su valor, igual
// que en Postgres, donde un ADD COLUMN con default no vuelve a tocarla y ningún
// UPDATE que no sea SetProfile la mueve. Debe llamarse con r.mu tomado.
func (r *Memoria) birthProfileLocked(key string) {
	if _, ok := r.profileUpdatedUs[key]; ok {
		return
	}
	r.touchProfileLocked(key)
}

func memKey(tenantID, edgeID, sessionID string) string {
	return tenantID + "\x00" + edgeID + "\x00" + sessionID
}

// MarkOnline implementa Repository. Preserva el perfil existente (lo gobierna
// SetProfile, no la señal de conexión): una sesión que reconecta conserva su
// active|passive.
func (r *Memoria) MarkOnline(_ context.Context, tenantID, edgeID, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	key := memKey(tenantID, edgeID, sessionID)
	s := r.sessions[key] // perfil/valores previos si existía; zero-Session si no.
	s.TenantID = tenantID
	s.EdgeID = edgeID
	s.SessionID = sessionID
	s.State = fleet.StateOnline
	s.Profile = defaultProfile(s.Profile)
	s.LastConnectedAt = now
	s.LastSeenAt = now
	r.sessions[key] = s
	// Alta de fila ⇒ espeja el DEFAULT now() de profile_updated_at. Una sesión que
	// RECONECTA no lo mueve: si lo moviera, este doble reproduciría el bug que la
	// 0065 arregló (N versiones idénticas por reconexión) en vez de vigilarlo.
	r.birthProfileLocked(key)
	return nil
}

// MarkOffline implementa Repository. Si la sesión no existía, la CREA offline y
// pasiva (diferencia documentada con Postgres, ver Memoria).
func (r *Memoria) MarkOffline(_ context.Context, tenantID, edgeID, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	key := memKey(tenantID, edgeID, sessionID)
	s, ok := r.sessions[key]
	if !ok {
		s = fleet.Session{TenantID: tenantID, EdgeID: edgeID, SessionID: sessionID}
	}
	s.State = fleet.StateOffline
	s.Profile = defaultProfile(s.Profile)
	s.LastSeenAt = now
	r.sessions[key] = s
	r.birthProfileLocked(key) // solo si la fila es nueva; desconectar no es cambiar de perfil.
	return nil
}

// MarkLoggedOut implementa Repository: marca la sesión zombie (StateLoggedOut).
// Como MarkOffline, no falla si la sesión no existía (la crea marcada zombie).
func (r *Memoria) MarkLoggedOut(_ context.Context, tenantID, edgeID, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	key := memKey(tenantID, edgeID, sessionID)
	s, ok := r.sessions[key]
	if !ok {
		s = fleet.Session{TenantID: tenantID, EdgeID: edgeID, SessionID: sessionID}
	}
	s.State = fleet.StateLoggedOut
	s.Profile = defaultProfile(s.Profile)
	s.LastSeenAt = now
	r.sessions[key] = s
	r.birthProfileLocked(key) // solo si la fila es nueva; el logout no es un cambio de perfil.
	return nil
}

// SetState implementa Repository: fija el estado de todas las filas de la sesión
// bajo el tenant a un estado admin-admitido. found=false si ninguna casa
// (aislamiento por tenant). Devuelve ErrInvalidState si state no es admitido.
func (r *Memoria) SetState(_ context.Context, tenantID, sessionID string, state fleet.State) (bool, error) {
	if !fleet.ValidAdminState(state) {
		return false, fleet.ErrInvalidState
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
			// NO se toca profile_updated_at: el estado del link no es el perfil.
			found = true
		}
	}
	return found, nil
}

// CountLiveBySelfPn implementa Repository: cuenta las sesiones vivas (no zombie)
// del tenant con el self_pn dado. selfPn vacío ⇒ 0.
//
// 🔴 NORMALIZA ANTES DE COMPARAR, igual que el adaptador Postgres normaliza antes
// de calcular el índice ciego (ver normalizeSelfPn). Sin esto el doble sería MÁS
// estricto que producción: allí "+34600111222" y "34600111222" dan el MISMO bidx
// y cuentan como el mismo teléfono; aquí, comparando strings crudos, serían dos.
// Un test del aviso del tope de dispositivos (REQ-D4) pasaría en memoria y
// mentiría sobre el conteo real.
func (r *Memoria) CountLiveBySelfPn(_ context.Context, tenantID, selfPn string) (int, error) {
	if selfPn == "" {
		return 0, nil
	}
	norm, err := normalizeSelfPn(selfPn)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, s := range r.sessions {
		if s.TenantID == tenantID && s.SelfPn == norm && s.State != fleet.StateLoggedOut {
			n++
		}
	}
	return n, nil
}

// SetProfile implementa Repository: fija el perfil de todas las filas de la sesión
// bajo el tenant. found=false si ninguna casa (aislamiento por tenant).
func (r *Memoria) SetProfile(_ context.Context, tenantID, sessionID string, profile fleet.Profile) (bool, error) {
	if !fleet.ValidProfile(profile) {
		return false, fleet.ErrInvalidProfile
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	found := false
	for k, s := range r.sessions {
		if s.TenantID == tenantID && s.SessionID == sessionID {
			s.Profile = profile
			r.sessions[k] = s
			// 🔴 EL ÚNICO sitio del doble que mueve el reloj del eje, igual que
			// SetProfile es el único UPDATE de Postgres que escribe
			// profile_updated_at (0065).
			r.touchProfileLocked(k)
			found = true
		}
	}
	return found, nil
}

// ProfilesByTenant implementa el puerto de lectura del kind:"filters" (Plan 046 ·
// T2.1) sobre el doble en memoria: devuelve el perfil de TODAS las sesiones del
// tenant y la versión (max profile_updated_at en microsegundos). Espeja exactamente
// lo que hace el adaptador Postgres, incluidas las dos decisiones que allí son SQL:
//
//   - varias filas con el MISMO session_id (una por edge_id) colapsan a UNA entrada,
//     y si discreparan gana `passive` — la lectura segura es «no auto-responde».
//   - el perfil vacío se lee como `passive` (mismo criterio que el COALESCE).
//
// 🔴 NO forma parte de la interfaz Repository a propósito: es una lectura de un
// consumidor concreto (el provider/pusher de filters), no del contrato de flota.
// Meterla en Repository obligaría a implementarla a todo decorador —SlowRepository,
// los espías de los tests del gateway— sin que ninguno la use.
func (r *Memoria) ProfilesByTenant(_ context.Context, tenantID string) (fleet.TenantProfiles, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := fleet.TenantProfiles{Sessions: make(map[string]fleet.Profile)}
	for k, s := range r.sessions {
		if s.TenantID != tenantID {
			continue
		}
		p := defaultProfile(s.Profile)
		if prev, ok := out.Sessions[s.SessionID]; ok && prev == fleet.ProfilePassive {
			p = fleet.ProfilePassive
		}
		out.Sessions[s.SessionID] = p
		if us := r.profileUpdatedUs[k]; us > out.Version {
			out.Version = us
		}
	}
	return out, nil
}

// SetSelfPn implementa Repository: fija el self_pn de la sesión. selfPn vacío es
// un no-op (protege un valor previo bueno). No falla si la sesión no existe aún.
//
// 🔴 GUARDA EL NÚMERO NORMALIZADO, no el que le pasaron, porque eso es lo que
// hace el repositorio real: allí lo que se persiste es el sobre de un valor ya
// canonizado y el índice ciego de ese mismo valor, así que lo que un lector
// recupera SIEMPRE está normalizado. Si el doble guardara el crudo, el campo
// SelfPn de una Session tendría una forma en los tests y otra en producción.
//
// Un número que NO normaliza devuelve ERROR (no un no-op silencioso), igual que
// en Postgres: ahí no se puede calcular índice ciego, así que la escritura no
// existe, y decir que salió bien haría creer que el número quedó guardado.
func (r *Memoria) SetSelfPn(_ context.Context, tenantID, edgeID, sessionID, selfPn string) error {
	if selfPn == "" {
		return nil
	}
	norm, err := normalizeSelfPn(selfPn)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := memKey(tenantID, edgeID, sessionID)
	s, ok := r.sessions[key]
	if !ok {
		return nil
	}
	s.SelfPn = norm
	r.sessions[key] = s
	// NO se toca profile_updated_at: el número propio no es el perfil.
	return nil
}

// SaveHealth implementa Repository: persiste el snapshot de salud en la sesión.
// No-op si la sesión no existe aún (espeja el UPDATE de 0 filas de Postgres).
// Fija degraded_since al entrar en degradado y lo limpia al salir.
func (r *Memoria) SaveHealth(_ context.Context, tenantID, edgeID, sessionID string, h fleet.HealthSnapshot) error {
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
	// 🔴 NO se toca profile_updated_at, y este es el caso que más importa: el
	// heartbeat llega CADA POCOS SEGUNDOS. Si SaveHealth moviera el reloj del eje,
	// el tenant publicaría una versión nueva del kind:"filters" por latido, con el
	// mapa idéntico. Es la mitad del bug que la 0065 arregló.
	return nil
}

// Get implementa Repository. La Session sale con copias del desglose de motivos y
// de los punteros del bloque del worker: el llamante no comparte respaldo con el
// repositorio (Plan 051 · T4.3).
func (r *Memoria) Get(_ context.Context, tenantID, edgeID, sessionID string) (fleet.Session, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[memKey(tenantID, edgeID, sessionID)]
	if !ok {
		return fleet.Session{}, false, nil
	}
	return detachWorkerHealth(s), true, nil
}

// List implementa Repository. Devuelve siempre un slice no nil, sin orden.
func (r *Memoria) List(_ context.Context, tenantID string) ([]fleet.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]fleet.Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		if s.TenantID == tenantID {
			out = append(out, detachWorkerHealth(s))
		}
	}
	return out, nil
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

// detachWorkerHealth devuelve una Session cuyo bloque del worker (mapa y punteros)
// ya no comparte respaldo con el repositorio en memoria.
func detachWorkerHealth(s fleet.Session) fleet.Session {
	s.IntentOmittedByReason = cloneReasons(s.IntentOmittedByReason)
	s.IntentP50Ms = cloneInt64(s.IntentP50Ms)
	s.StuckHeads = cloneInt64(s.StuckHeads)
	s.StuckHeadPolls = cloneInt64(s.StuckHeadPolls)
	s.FailedSealDispatch = cloneInt64(s.FailedSealDispatch)
	s.FailedSealBudget = cloneInt64(s.FailedSealBudget)
	return s
}
