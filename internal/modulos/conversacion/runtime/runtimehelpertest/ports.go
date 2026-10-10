package runtimehelpertest

import (
	"context"
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

// TenantResolver es el doble de runtime.TenantResolver para los tests del runtime: contesta
// SIEMPRE el mismo tenant y el mismo perfil, sea cual sea la sesión, y apunta por qué sesiones se
// le preguntó. Para la semántica de la tabla (varios edges, ambigüedad) está MemoryTenantResolver.
//
// Seguro para uso concurrente.
type TenantResolver struct {
	mu       sync.Mutex
	tenantID string
	profile  string
	err      error
	sessions []string
}

var _ runtime.TenantResolver = (*TenantResolver)(nil)

// NewTenantResolver devuelve un resolver que contesta ese tenant con ese perfil. El perfil va tal
// cual: vacío o desconocido son valores legítimos (el runtime los trata como activo).
func NewTenantResolver(tenantID, profile string) *TenantResolver {
	return &TenantResolver{tenantID: tenantID, profile: profile}
}

// ResolveTenant apunta la sesión y devuelve el tenant y el perfil fijados; con un error inyectado,
// ("", "", error), como promete el puerto.
func (r *TenantResolver) ResolveTenant(_ context.Context, sessionID string) (tenantID string, profile string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = append(r.sessions, sessionID)
	if r.err != nil {
		return "", "", r.err
	}
	return r.tenantID, r.profile, nil
}

// SetProfile cambia el perfil que contestan las llamadas siguientes.
func (r *TenantResolver) SetProfile(profile string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profile = profile
}

// Fail hace que las llamadas siguientes fallen con err; nil las vuelve a dejar pasar.
func (r *TenantResolver) Fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// Sessions devuelve los session_id por los que se preguntó, en orden.
func (r *TenantResolver) Sessions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sessions...)
}

// SelfNumberQuery es una pregunta que llegó al SelfNumbers de mentira, con el número EXACTO que
// pasó el runtime (para afirmar que llega normalizado sin depender del veredicto).
type SelfNumberQuery struct {
	TenantID, Number string
}

// SelfNumbers es el doble de runtime.SelfNumberChecker para los tests del runtime: un conjunto de
// números propios por tenant, YA filtrado (sin perfiles ni estados: eso es de MemorySelfNumbers).
//
// Compara por igualdad EXACTA, a propósito: así se comporta como el HMAC del índice ciego, que
// tampoco perdona un «+» ni un espacio. Si el runtime dejara de normalizar antes de preguntar,
// aquí dejaría de casar, igual que en producción.
//
// Seguro para uso concurrente.
type SelfNumbers struct {
	mu      sync.Mutex
	numbers map[SelfNumberQuery]bool
	err     error
	queries []SelfNumberQuery
}

var _ runtime.SelfNumberChecker = (*SelfNumbers)(nil)

// NewSelfNumbers devuelve un conjunto vacío: ningún número es propio de nadie.
func NewSelfNumbers() *SelfNumbers {
	return &SelfNumbers{numbers: make(map[SelfNumberQuery]bool)}
}

// Add declara esos números, tal cual se escriben, como propios del tenant.
func (s *SelfNumbers) Add(tenantID string, numbers ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, number := range numbers {
		s.numbers[SelfNumberQuery{TenantID: tenantID, Number: number}] = true
	}
}

// IsSelfNumber apunta la pregunta y dice si ese número exacto se declaró propio de ese tenant; con
// un error inyectado, (false, error).
func (s *SelfNumbers) IsSelfNumber(_ context.Context, tenantID, normalizedNumber string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := SelfNumberQuery{TenantID: tenantID, Number: normalizedNumber}
	s.queries = append(s.queries, query)
	if s.err != nil {
		return false, s.err
	}
	return s.numbers[query], nil
}

// Fail hace que las llamadas siguientes fallen con err; nil las vuelve a dejar pasar.
func (s *SelfNumbers) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Queries devuelve las preguntas que llegaron, fallaran o no, en orden.
func (s *SelfNumbers) Queries() []SelfNumberQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SelfNumberQuery(nil), s.queries...)
}

// IngestKey es la clave de un entrante en el dedupe: la sesión y el id del mensaje de WhatsApp.
type IngestKey struct {
	SessionID, WaMessageID string
}

// IngestDeduper es el doble de runtime.IngestDeduper: recuerda en memoria las claves vistas, así
// que la primera vez contesta false y desde la segunda true, como el de verdad. MarkSeen deja una
// clave vista de antemano (el reenvío de algo que llegó en otra vida del proceso).
//
// Seguro para uso concurrente.
type IngestDeduper struct {
	mu    sync.Mutex
	seen  map[IngestKey]bool
	err   error
	calls []IngestKey
}

var _ runtime.IngestDeduper = (*IngestDeduper)(nil)

// NewIngestDeduper devuelve un deduplicador que no ha visto nada.
func NewIngestDeduper() *IngestDeduper {
	return &IngestDeduper{seen: make(map[IngestKey]bool)}
}

// Seen apunta la llamada, dice si la clave ya se había visto y la deja vista. Con un error
// inyectado devuelve (false, error) y NO la registra.
func (d *IngestDeduper) Seen(_ context.Context, sessionID, waMessageID string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := IngestKey{SessionID: sessionID, WaMessageID: waMessageID}
	d.calls = append(d.calls, key)
	if d.err != nil {
		return false, d.err
	}
	already := d.seen[key]
	d.seen[key] = true
	return already, nil
}

// MarkSeen deja la clave como ya vista, sin apuntarla como llamada.
func (d *IngestDeduper) MarkSeen(sessionID, waMessageID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[IngestKey{SessionID: sessionID, WaMessageID: waMessageID}] = true
}

// Fail hace que las llamadas siguientes fallen con err; nil las vuelve a dejar pasar.
func (d *IngestDeduper) Fail(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.err = err
}

// Calls devuelve las claves con que se llamó a Seen, fallara o no, en orden.
func (d *IngestDeduper) Calls() []IngestKey {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]IngestKey(nil), d.calls...)
}

// LimiterCall es una petición de token que llegó al ReplyLimiter de mentira, con su veredicto.
type LimiterCall struct {
	Key     string
	Allowed bool
}

// ReplyLimiter es el doble del puerto ReplyLimiter del runtime (Allow(key string) bool): un cupo
// de tokens POR CLAVE, sin reloj y sin recarga. Recién construido no tiene tope y lo concede todo.
//
// El puerto se declara en runtime_engine.go, que nace en la ola siguiente: por eso aquí no hay
// aserción de compilación contra él. La firma es la del viejo.
//
// Seguro para uso concurrente.
type ReplyLimiter struct {
	mu      sync.Mutex
	limited bool
	budget  int
	spent   map[string]int
	calls   []LimiterCall
}

// NewReplyLimiter devuelve un limitador sin tope.
func NewReplyLimiter() *ReplyLimiter {
	return &ReplyLimiter{spent: make(map[string]int)}
}

// Limit fija el cupo: desde aquí cada clave tiene tokens tokens EN TOTAL, contando los que ya
// gastó. Limit(0) lo niega todo. Un valor negativo quita el tope.
func (l *ReplyLimiter) Limit(tokens int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limited = tokens >= 0
	l.budget = tokens
}

// Allow apunta la petición y consume un token de la clave: true si le quedaba, false si no. Una
// petición denegada no consume.
func (l *ReplyLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	allowed := !l.limited || l.spent[key] < l.budget
	if allowed {
		l.spent[key]++
	}
	l.calls = append(l.calls, LimiterCall{Key: key, Allowed: allowed})
	return allowed
}

// Calls devuelve las peticiones que llegaron, con su veredicto, en orden.
func (l *ReplyLimiter) Calls() []LimiterCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LimiterCall(nil), l.calls...)
}

// ReminderCall es un toque que llegó al DepositReminder de mentira.
type ReminderCall struct {
	TenantID, ContactID string
}

// DepositReminder es el doble del puerto DepositReminder del runtime
// (RemindContact(ctx, tenantID, contactID string) []string): no manda nada; apunta cada toque y
// devuelve los textos que finge haber enviado. Recién construido sin textos devuelve nil, que es
// «no procedía»: lo que contesta el de verdad casi siempre.
//
// El puerto se declara en runtime_engine.go, que nace en la ola siguiente: por eso aquí no hay
// aserción de compilación contra él. La firma es la del viejo.
//
// Seguro para uso concurrente.
type DepositReminder struct {
	mu    sync.Mutex
	texts []string
	calls []ReminderCall
}

// NewDepositReminder devuelve un recordatorio que en CADA toque finge haber enviado esos textos,
// en ese orden. Sin textos, ninguno.
func NewDepositReminder(texts ...string) *DepositReminder {
	return &DepositReminder{texts: append([]string(nil), texts...)}
}

// RemindContact apunta el toque y devuelve una copia de los textos fijados (nil si no hay).
func (r *DepositReminder) RemindContact(_ context.Context, tenantID, contactID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, ReminderCall{TenantID: tenantID, ContactID: contactID})
	return append([]string(nil), r.texts...)
}

// SetTexts cambia los textos que devuelven los toques siguientes; sin argumentos, ninguno.
func (r *DepositReminder) SetTexts(texts ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append([]string(nil), texts...)
}

// Calls devuelve los toques que llegaron, en orden.
func (r *DepositReminder) Calls() []ReminderCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ReminderCall(nil), r.calls...)
}
