package eventshelpertest

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// Store es el DOBLE EN MEMORIA de events.Store: el almacén del evento conversacional y de su
// historial, sin Postgres. El paquete viejo no tenía gemelo (su adaptador solo se probaba contra
// una base viva); este nace con la reconstrucción para que el runtime, el carrito y la cara HTTP
// puedan probarse en unitario, y pasa la MISMA suite que el adaptador (Contrato).
//
// Imita a Postgres, no lo mejora:
//
//   - un evento vivo por (tenant, sesión, contacto, tipo): el índice único parcial (E-2). El
//     segundo CreateEvent devuelve events.ErrAliveExists;
//   - TransitionEvent es un compare-and-swap sobre status = 'open', y solo toca status y
//     closed_at; Touch solo toca last_activity_at. Las dos se cruzan sin pisarse;
//   - el historial se numera MAX(seq)+1 por evento, sin huecos, y escribir en un evento que no
//     existe falla (la clave foránea);
//   - los órdenes son los del SQL: los vivos por nacimiento, los rescatables por última actividad
//     descendente, los dos con desempate por id;
//   - el contenido del evento (la vista public.event_content) y el TTL de inactividad del tenant
//     (public.tenant_settings) no son de este almacén: se siembran con SetContent y
//     SetInactivityTTL. Sin sembrar, el evento no tiene contenido y el TTL es 7200 s;
//   - el reloj es el inyectado (SetClock), siempre en UTC.
//
// Lo que NO imita, a propósito: no cifra —guarda el cuerpo de un mensaje tal cual y lo marca como
// sellado; «sin cipher» es el hermano que da WithoutCipher— y no valida que tenant, contacto o
// evento sean UUID (la base sí: un id mal formado es allí un error de sintaxis). La única
// validación de UUID que conserva es la que el adaptador hace en Go: la de GetEventForTenant.
//
// Es seguro para uso concurrente. El valor cero no sirve: se construye con NewStore.
type Store struct {
	state    *state
	noCipher bool
}

// state es lo que comparten un Store y su hermano sin cipher.
type state struct {
	mu  sync.Mutex
	now func() time.Time
	// events: id → fila de conversation_events.
	events map[string]events.Event
	// entries: id del evento → su historial, por seq.
	entries map[string][]storedEntry
	// content: id del evento → su fila en la vista event_content.
	content map[string]contentRow
	// ttlSeconds: tenant → tenant_settings.event_inactivity_ttl_seconds. Sin clave, defaultTTL.
	ttlSeconds map[string]int
}

// storedEntry es una fila de conversation_event_messages.
type storedEntry struct {
	Entry
	// body es el cuerpo de una entrada sellada (el doble no cifra: lo guarda tal cual).
	body string
	// corrupt marca una entrada sellada que ya no se puede descifrar (CorruptEntry).
	corrupt bool
}

// contentRow es una fila de la vista event_content.
type contentRow struct{ state, ref string }

// Entry es una fila del historial vista desde fuera, sin el cuerpo: lo que un observador de la
// suite puede afirmar de public.conversation_event_messages sin descifrar nada.
type Entry struct {
	// Seq es el número de la entrada dentro del evento.
	Seq int
	// Role, Kind y Origin son las tres marcas que cada puerta clava.
	Role   events.Role
	Kind   events.EntryKind
	Origin events.Origin
	// Payload es la estructura en claro de una entrada de nivel 1; nil si la columna es NULL.
	Payload []byte
	// Sealed dice que la entrada lleva el sobre cifrado completo (body_enc, body_dek y
	// body_kek_id): es de nivel 2.
	Sealed bool
}

// Los tres estados que la vista event_content puede dar a un contenido.
const (
	// ContentAlive: el contenido sigue vivo (una solicitud `open`).
	ContentAlive = "alive"
	// ContentSettled: el contenido ya cuajó (una solicitud confirmada, por ejemplo).
	ContentSettled = "settled"
	// ContentDiscarded: el contenido murió (una solicitud `abandoned`).
	ContentDiscarded = "discarded"
)

// defaultTTL es el TTL de inactividad de un tenant sin fila de configuración, en segundos: el
// DEFAULT de tenant_settings.event_inactivity_ttl_seconds (2 h).
const defaultTTL = 7200

// Los fallos que en el adaptador vienen de la base.
var (
	errUniqueAlive   = errors.New("eventshelpertest: violación del único parcial conversation_events_one_alive_per_kind_idx")
	errNoSuchEvent   = errors.New("eventshelpertest: el evento no existe (clave foránea de conversation_event_messages)")
	errUndecryptable = errors.New("eventshelpertest: la entrada no se puede descifrar")
)

// Aserciones de compilación: el doble satisface los puertos del paquete que hoy cumple
// *events.Store.
var (
	_ events.RescuableLister = (*Store)(nil)
	_ events.SummaryAppender = (*Store)(nil)
	_ Port                   = (*Store)(nil)
)

// NewStore devuelve un doble vacío, con cipher y con time.Now de reloj.
func NewStore() *Store {
	return &Store{state: &state{
		now:        time.Now,
		events:     make(map[string]events.Event),
		entries:    make(map[string][]storedEntry),
		content:    make(map[string]contentRow),
		ttlSeconds: make(map[string]int),
	}}
}

// SetClock cambia el reloj del doble (y el de su hermano sin cipher): el que fecha el nacimiento,
// compone HistoryID, refresca Touch, sella closed_at y decide la marca «vencido». Un now nil se
// ignora.
func (s *Store) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	s.state.now = now
}

// WithoutCipher devuelve un hermano que comparte TODOS los datos y el reloj con s pero se comporta
// como un events.Store construido sin FieldCipher: las tres puertas de texto y las dos lecturas
// del hilo devuelven events.ErrNoCipher.
func (s *Store) WithoutCipher() *Store {
	return &Store{state: s.state, noCipher: true}
}

// SetContent deja al evento con ESE contenido en la vista event_content y devuelve su ref (un
// UUID). state es ContentAlive, ContentSettled o ContentDiscarded; cambiar el estado de un evento
// que ya tenía contenido conserva su ref. Con state vacío el evento se queda sin contenido y
// devuelve "".
func (s *Store) SetContent(eventID, contentState string) (ref string) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if contentState == "" {
		delete(s.state.content, eventID)
		return ""
	}
	row, ok := s.state.content[eventID]
	if !ok {
		row.ref = uuid.NewString()
	}
	row.state = contentState
	s.state.content[eventID] = row
	return row.ref
}

// SetInactivityTTL fija el TTL de inactividad del tenant, en segundos, como una fila de
// tenant_settings. 0 es «sin vencimiento».
func (s *Store) SetInactivityTTL(tenantID string, seconds int) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	s.state.ttlSeconds[tenantID] = seconds
}

// Events devuelve TODOS los eventos del tenant, en cualquier estado, por id.
func (s *Store) Events(tenantID string) []events.Event {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	out := make([]events.Event, 0)
	for _, ev := range s.state.events {
		if ev.TenantID == tenantID {
			out = append(out, ev)
		}
	}
	slices.SortFunc(out, func(a, b events.Event) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// Entries devuelve el historial del evento, por seq, sin los cuerpos. El payload es una copia.
func (s *Store) Entries(eventID string) []Entry {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	out := make([]Entry, 0, len(s.state.entries[eventID]))
	for _, e := range s.state.entries[eventID] {
		entry := e.Entry
		entry.Payload = slices.Clone(e.Payload)
		out = append(out, entry)
	}
	return out
}

// CorruptEntry estropea el sobre de la entrada seq del evento: desde ahora no se puede descifrar.
// Devuelve false si esa entrada no existe o no es de nivel 2.
func (s *Store) CorruptEntry(eventID string, seq int) bool {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	for i, e := range s.state.entries[eventID] {
		if e.Seq == seq && e.Sealed {
			s.state.entries[eventID][i].corrupt = true
			return true
		}
	}
	return false
}

// clock es el instante del reloj inyectado, en UTC. Requiere el mutex tomado.
func (st *state) clock() time.Time { return st.now().UTC() }

// CreateEvent inserta un evento VIVO con id nuevo y devuelve la fila: nacimiento y última
// actividad en el instante del reloj, HistoryID de ese instante y ClosedAt a cero. Con otro vivo
// del mismo tipo en la conversación devuelve events.ErrAliveExists y no escribe nada.
func (s *Store) CreateEvent(_ context.Context, in events.NewEvent) (events.Event, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	for _, ev := range s.state.events {
		if ev.Alive() && ev.TenantID == in.TenantID && ev.SessionID == in.SessionID &&
			ev.ContactID == in.ContactID && ev.Kind == in.Kind {
			return events.Event{}, fmt.Errorf("%w (tenant=%s sesión=%s tipo=%s): %w",
				events.ErrAliveExists, in.TenantID, in.SessionID, in.Kind, errUniqueAlive)
		}
	}
	born := s.state.clock()
	ev := events.Event{
		ID:             uuid.NewString(),
		TenantID:       in.TenantID,
		SessionID:      in.SessionID,
		ContactID:      in.ContactID,
		Kind:           in.Kind,
		HistoryID:      events.HistoryID(in.Kind, born),
		Status:         events.StatusOpen,
		FlowID:         in.FlowID,
		FlowVersion:    in.FlowVersion,
		CreatedAt:      born,
		LastActivityAt: born,
	}
	s.state.events[ev.ID] = ev
	return ev, nil
}

// GetAliveByKind devuelve el evento vivo de ese tipo en la conversación, si lo hay.
func (s *Store) GetAliveByKind(_ context.Context, tenantID, sessionID, contactID, kind string) (events.Event, bool, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	for _, ev := range s.state.events {
		if ev.Alive() && ev.TenantID == tenantID && ev.SessionID == sessionID &&
			ev.ContactID == contactID && ev.Kind == kind {
			return ev, true, nil
		}
	}
	return events.Event{}, false, nil
}

// GetEventForTenant devuelve el evento, en cualquier estado, si es del tenant. Un id que no es
// UUID, que no existe o que es de otro tenant da events.ErrEventNotFound.
func (s *Store) GetEventForTenant(_ context.Context, tenantID, eventID string) (events.Event, error) {
	if _, err := uuid.Parse(eventID); err != nil {
		return events.Event{}, fmt.Errorf("%w (id=%q no es un UUID)", events.ErrEventNotFound, eventID)
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	ev, ok := s.state.events[eventID]
	if !ok || ev.TenantID != tenantID {
		return events.Event{}, fmt.Errorf("%w (id=%s)", events.ErrEventNotFound, eventID)
	}
	return ev, nil
}

// ListAlive devuelve los eventos vivos de la conversación por nacimiento y, a igual instante, id.
func (s *Store) ListAlive(_ context.Context, tenantID, sessionID, contactID string) ([]events.Event, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	var out []events.Event
	for _, ev := range s.state.events {
		if ev.Alive() && ev.TenantID == tenantID && ev.SessionID == sessionID && ev.ContactID == contactID {
			out = append(out, ev)
		}
	}
	slices.SortFunc(out, func(a, b events.Event) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// TransitionEvent mueve un evento vivo a closed o cancelled y sella closed_at con el reloj. Un
// destino no terminal da events.ErrNotTerminal; un evento que no está vivo (o no existe),
// events.ErrNotOpen. No toca nada más de la fila.
func (s *Store) TransitionEvent(_ context.Context, eventID string, to events.Status) error {
	if to != events.StatusClosed && to != events.StatusCancelled {
		return fmt.Errorf("%w (recibido %q)", events.ErrNotTerminal, to)
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	ev, ok := s.state.events[eventID]
	if !ok || !ev.Alive() {
		return fmt.Errorf("%w (id=%s destino=%s)", events.ErrNotOpen, eventID, to)
	}
	ev.Status = to
	ev.ClosedAt = s.state.clock()
	s.state.events[eventID] = ev
	return nil
}

// Touch estampa last_activity_at con el reloj, en cualquier estado. Un id que no existe da
// events.ErrEventMissing.
func (s *Store) Touch(_ context.Context, eventID string) error {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	ev, ok := s.state.events[eventID]
	if !ok {
		return fmt.Errorf("%w (id=%s)", events.ErrEventMissing, eventID)
	}
	ev.LastActivityAt = s.state.clock()
	s.state.events[eventID] = ev
	return nil
}

// IsSuspended es events.IsSuspended con el reloj del doble. No lee ni escribe nada.
func (s *Store) IsSuspended(e events.Event, ttl time.Duration) bool {
	s.state.mu.Lock()
	now := s.state.clock()
	s.state.mu.Unlock()
	return events.IsSuspended(e, ttl, now)
}

// appendEntry numera y guarda una entrada. Un evento que no existe falla como la clave foránea.
func (s *Store) appendEntry(eventID string, e storedEntry) (int, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if _, ok := s.state.events[eventID]; !ok {
		return 0, fmt.Errorf("events: insertar entrada %q del historial: %w", e.Kind, errNoSuchEvent)
	}
	if e.Origin == "" {
		e.Origin = events.OriginWhatsApp
	}
	e.Seq = len(s.state.entries[eventID]) + 1
	s.state.entries[eventID] = append(s.state.entries[eventID], e)
	return e.Seq, nil
}

// AppendSummary guarda un resumen: role system, grado summary y el cuerpo en claro. Un cuerpo que
// no es JSON da events.ErrSummaryNotJSON.
func (s *Store) AppendSummary(_ context.Context, eventID string, body json.RawMessage) (int, error) {
	if !json.Valid(body) {
		return 0, events.ErrSummaryNotJSON
	}
	return s.appendEntry(eventID, storedEntry{Entry: Entry{
		Role: events.RoleSystem, Kind: events.KindSummary, Payload: slices.Clone(body),
	}})
}

// AppendDecision guarda una decisión: role client, grado decision y el payload en claro. Un
// payload que no es JSON da events.ErrSummaryNotJSON.
func (s *Store) AppendDecision(_ context.Context, eventID string, payload []byte) error {
	if !json.Valid(payload) {
		return events.ErrSummaryNotJSON
	}
	_, err := s.appendEntry(eventID, storedEntry{Entry: Entry{
		Role: events.RoleClient, Kind: events.KindDecision, Payload: slices.Clone(payload),
	}})
	return err
}

// AppendMessage guarda el texto literal de una interacción, sellado, con el rol dado. Un rol
// desconocido da events.ErrInvalidRole; sin cipher, events.ErrNoCipher.
func (s *Store) AppendMessage(_ context.Context, eventID string, role events.Role, body string) (int, error) {
	if role != events.RoleClient && role != events.RoleBusiness && role != events.RoleSystem {
		return 0, fmt.Errorf("%w: %q", events.ErrInvalidRole, role)
	}
	return s.appendSealed(eventID, role, events.KindMessage, events.OriginWhatsApp, body)
}

// AppendOutOfTurnMessage guarda un saliente fuera de turno: sellado, role business y grado
// message_out_of_turn. Sin cipher, events.ErrNoCipher.
func (s *Store) AppendOutOfTurnMessage(_ context.Context, eventID string, body string) (int, error) {
	return s.appendSealed(eventID, events.RoleBusiness, events.KindMessageOutOfTurn, events.OriginWhatsApp, body)
}

// AppendPastedMessage guarda la transcripción que pegó el dueño: sellada, role client, grado
// message y origen owner_pasted. Sin cipher, events.ErrNoCipher.
func (s *Store) AppendPastedMessage(_ context.Context, eventID string, body string) (int, error) {
	return s.appendSealed(eventID, events.RoleClient, events.KindMessage, events.OriginOwnerPasted, body)
}

// appendSealed guarda una entrada de nivel 2. Sin cipher no escribe: events.ErrNoCipher.
func (s *Store) appendSealed(eventID string, role events.Role, kind events.EntryKind, origin events.Origin, body string) (int, error) {
	if s.noCipher {
		return 0, events.ErrNoCipher
	}
	return s.appendEntry(eventID, storedEntry{
		Entry: Entry{Role: role, Kind: kind, Origin: origin, Sealed: true},
		body:  body,
	})
}

// ListThread devuelve las `limit` entradas más recientes del evento en orden cronológico, con su
// texto resuelto. Sin evento o con limit <= 0 devuelve nil; sin cipher, events.ErrNoCipher; una
// entrada que no se puede descifrar aborta la lectura.
func (s *Store) ListThread(_ context.Context, eventID string, limit int) ([]events.ThreadEntry, error) {
	if s == nil || eventID == "" || limit <= 0 {
		return nil, nil
	}
	if s.noCipher {
		return nil, events.ErrNoCipher
	}
	s.state.mu.Lock()
	all := slices.Clone(s.state.entries[eventID])
	s.state.mu.Unlock()
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	var out []events.ThreadEntry
	for _, e := range all {
		text, err := entryText(e)
		if err != nil {
			return nil, fmt.Errorf("events: resolver la entrada %d del hilo del evento %s: %w", e.Seq, eventID, err)
		}
		out = append(out, events.ThreadEntry{Seq: e.Seq, Role: e.Role, Kind: e.Kind, Text: text})
	}
	return out, nil
}

// ListPastedByOwner devuelve, por seq, el texto de las transcripciones que el dueño pegó en el
// evento. Sin evento devuelve nil; sin cipher, events.ErrNoCipher; una que no se puede descifrar
// aborta la lectura.
func (s *Store) ListPastedByOwner(_ context.Context, eventID string) ([]string, error) {
	if s == nil || eventID == "" {
		return nil, nil
	}
	if s.noCipher {
		return nil, events.ErrNoCipher
	}
	s.state.mu.Lock()
	all := slices.Clone(s.state.entries[eventID])
	s.state.mu.Unlock()
	var out []string
	for _, e := range all {
		if e.Kind != events.KindMessage || e.Origin != events.OriginOwnerPasted {
			continue
		}
		if e.corrupt {
			return nil, fmt.Errorf("events: descifrar la transcripción %d del evento %s: %w", e.Seq, eventID, errUndecryptable)
		}
		out = append(out, e.body)
	}
	return out, nil
}

// entryText resuelve el grado de una entrada a texto, como el adaptador: el cuerpo de una sellada;
// el render del resumen que describe un payload en claro ("" si no es un resumen legible); "" si
// no trae nada.
func entryText(e storedEntry) (string, error) {
	if e.Sealed {
		if e.corrupt {
			return "", errUndecryptable
		}
		return e.body, nil
	}
	if len(e.Payload) == 0 {
		return "", nil
	}
	var sum events.Summary
	if err := json.Unmarshal(e.Payload, &sum); err != nil {
		// Como el adaptador: un payload ilegible no aporta texto y no es un fallo de lectura.
		return "", nil //nolint:nilerr // degradación intencional, la misma que events.Store.
	}
	return sum.Render(), nil
}
