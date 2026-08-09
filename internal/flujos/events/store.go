package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// Store es el almacén Postgres del evento conversacional y de su historial.
//
// Dos garantías que NO son del llamador y por eso viven aquí:
//
//   - EL RELOJ ES UNO Y ES INYECTABLE. HistoryID, el nacimiento y cada Touch salen
//     de now, no de now() de la BD. Un reloj que no se puede fijar no se puede
//     testear, y un id legible derivado de un reloj que nadie controla no se puede
//     afirmar.
//   - NO HAY PUERTA PARA PERSISTIR TEXTO LIBRE EN CLARO. AppendMessage es la única
//     entrada de texto literal y cifra SIEMPRE (no admite variante sin cifrar);
//     AppendSummary solo acepta estructura ya serializada (json.RawMessage), que es
//     el nivel 1 en claro del ADR-0034 y no un cajón de prosa.
type Store struct {
	db     *sql.DB
	cipher *crypto.FieldCipher
	now    func() time.Time
}

// Option configura el Store en la construcción.
type Option func(*Store)

// WithClock inyecta el reloj del store: el que compone HistoryID, el que estampa
// el nacimiento y el que refresca last_activity_at en Touch. Sin él, NewStore usa
// time.Now. Existe para tests deterministas (mismo patrón que el runtime).
func WithClock(now func() time.Time) Option {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

// NewStore construye el store sobre el pool dado. cipher es obligatorio para
// AppendMessage: sin él, la única puerta de texto literal queda cerrada (devuelve
// error) en vez de abrir una que escriba en claro.
func NewStore(db *sql.DB, cipher *crypto.FieldCipher, opts ...Option) *Store {
	s := &Store{db: db, cipher: cipher, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// eventColumns es la lista de columnas que scanEvent espera, en orden.
const eventColumns = `id, tenant_id, session_id, contact_id, kind, history_id, status,
	       flow_id, flow_version, intake_id, created_at, last_activity_at, closed_at`

// scanner abstrae *sql.Row y *sql.Rows para compartir scanEvent.
type scanner interface {
	Scan(dest ...any) error
}

// scanEvent lee una fila de conversation_events en un Event, traduciendo los dos
// nullables (intake_id, closed_at) a sus ceros de Go.
func scanEvent(sc scanner) (Event, error) {
	var (
		ev       Event
		intakeID sql.NullString
		closedAt sql.NullTime
	)
	err := sc.Scan(&ev.ID, &ev.TenantID, &ev.SessionID, &ev.ContactID, &ev.Kind,
		&ev.HistoryID, &ev.Status, &ev.FlowID, &ev.FlowVersion, &intakeID,
		&ev.CreatedAt, &ev.LastActivityAt, &closedAt)
	if err != nil {
		return Event{}, err
	}
	ev.IntakeID = intakeID.String
	ev.ClosedAt = closedAt.Time
	return ev, nil
}

// nullableID convierte un id opcional vacío en NULL.
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

const insertEventSQL = `
INSERT INTO public.conversation_events
       (tenant_id, session_id, contact_id, kind, history_id, status,
        flow_id, flow_version, intake_id, created_at, last_activity_at)
VALUES ($1, $2, $3, $4, $5, 'open', $6, $7, $8, $9, $9)
RETURNING ` + eventColumns

// CreateEvent inserta un evento VIVO y devuelve la fila tal como quedó.
//
// El nacimiento y el reloj arrancan en el MISMO instante del reloj inyectado (por
// eso $9 va dos veces), y de ese instante sale también HistoryID en UTC: la fila
// no puede quedar diciendo que nació a una hora y que su id legible es de otra.
//
// Si ya hay un evento vivo de ese tipo en la conversación devuelve ErrAliveExists.
// Esa regla la impone el índice único parcial de la BD (E-2), no una comprobación
// previa del código: entre un SELECT y un INSERT cabe otro escritor, y en el
// índice no cabe nadie.
func (s *Store) CreateEvent(ctx context.Context, in NewEvent) (Event, error) {
	born := s.now().UTC()
	row := s.db.QueryRowContext(ctx, insertEventSQL,
		in.TenantID, in.SessionID, in.ContactID, in.Kind, HistoryID(in.Kind, born),
		in.FlowID, in.FlowVersion, nullableID(in.IntakeID), born)

	ev, err := scanEvent(row)
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return Event{}, fmt.Errorf("%w (tenant=%s sesión=%s tipo=%s): %w",
				ErrAliveExists, in.TenantID, in.SessionID, in.Kind, err)
		}
		return Event{}, fmt.Errorf("events: insertar evento: %w", err)
	}
	return ev, nil
}

const selectAliveByKindSQL = `
SELECT ` + eventColumns + `
  FROM public.conversation_events
 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3 AND kind = $4
   AND status = 'open'`

// GetAliveByKind devuelve el evento VIVO de ese tipo en la conversación. El
// segundo retorno dice si lo había: no haberlo es normal (E-6, el saludo no crea
// evento), no un error.
//
// No hace falta LIMIT 1: el índice único parcial garantiza que hay como mucho uno.
func (s *Store) GetAliveByKind(ctx context.Context, tenantID, sessionID, contactID, kind string) (Event, bool, error) {
	row := s.db.QueryRowContext(ctx, selectAliveByKindSQL, tenantID, sessionID, contactID, kind)
	ev, err := scanEvent(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Event{}, false, nil
	case err != nil:
		return Event{}, false, fmt.Errorf("events: leer evento vivo de tipo %q: %w", kind, err)
	}
	return ev, true, nil
}

const selectAliveSQL = `
SELECT ` + eventColumns + `
  FROM public.conversation_events
 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
   AND status = 'open'
 ORDER BY created_at, id`

const selectRescuableSQL = `
SELECT ` + eventColumns + `
  FROM public.conversation_events
 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
   AND status = 'open'
 ORDER BY last_activity_at DESC, id`

// ListAlive devuelve los eventos VIVOS de la conversación en orden de NACIMIENTO.
// Es la lista del despachador: al cliente se le enseñan sus eventos en el orden en
// que aparecieron, que no cambia bajo sus pies al escribir.
func (s *Store) ListAlive(ctx context.Context, tenantID, sessionID, contactID string) ([]Event, error) {
	return s.listEvents(ctx, selectAliveSQL, tenantID, sessionID, contactID)
}

// ListRescuable devuelve los eventos vivos ordenados por ÚLTIMA ACTIVIDAD
// descendente, que es lo que necesita el automensaje de rescate: lo primero que se
// ofrece retomar es lo último que se tocó.
//
// Mismo conjunto que ListAlive y distinto orden a propósito: son dos preguntas
// («¿qué tiene abierto?» y «¿qué le ofrezco retomar primero?») y colapsarlas
// obligaría a uno de los dos consumidores a reordenar en memoria. Un evento
// suspendido sigue aquí: suspendido no es muerto (E-6).
func (s *Store) ListRescuable(ctx context.Context, tenantID, sessionID, contactID string) ([]Event, error) {
	return s.listEvents(ctx, selectRescuableSQL, tenantID, sessionID, contactID)
}

// listEvents ejecuta una consulta de listado ya parametrizada por conversación.
func (s *Store) listEvents(ctx context.Context, query, tenantID, sessionID, contactID string) (out []Event, err error) {
	rows, err := s.db.QueryContext(ctx, query, tenantID, sessionID, contactID)
	if err != nil {
		return nil, fmt.Errorf("events: listar eventos vivos: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("events: cerrar filas de eventos: %w", cerr)
		}
	}()

	for rows.Next() {
		ev, sErr := scanEvent(rows)
		if sErr != nil {
			return nil, fmt.Errorf("events: leer fila de evento: %w", sErr)
		}
		out = append(out, ev)
	}
	if rErr := rows.Err(); rErr != nil {
		return nil, fmt.Errorf("events: recorrer eventos vivos: %w", rErr)
	}
	return out, nil
}

// transitionSQL es el compare-and-swap: el `AND status='open'` es el guard. Un
// evento terminal no tiene transición de vuelta, y quien pierde la carrera con
// otro escritor no pisa la muerte que el otro ya selló.
const transitionSQL = `
UPDATE public.conversation_events
   SET status = $2, closed_at = $3
 WHERE id = $1 AND status = 'open'`

// TransitionEvent mueve un evento VIVO a un estado terminal y sella closed_at con
// el reloj inyectado.
//
// Es un compare-and-swap contra la BD, no un «leer, decidir, escribir»: el UPDATE
// lleva `AND status='open'`, así que si el evento ya era closed o cancelled no
// toca nada y devuelve ErrNotOpen. De ahí salen las dos imposibilidades del
// diseño: closed→open y cancelled→closed.
//
// El destino debe ser terminal (ErrNotTerminal si no): open es el estado de
// nacimiento, y «reabrir» no es una transición sino un evento nuevo.
func (s *Store) TransitionEvent(ctx context.Context, eventID string, to Status) error {
	if to != StatusClosed && to != StatusCancelled {
		return fmt.Errorf("%w (recibido %q)", ErrNotTerminal, to)
	}

	res, err := s.db.ExecContext(ctx, transitionSQL, eventID, to, s.now().UTC())
	if err != nil {
		return fmt.Errorf("events: transitar evento a %q: %w", to, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("events: filas afectadas por la transición: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w (id=%s destino=%s)", ErrNotOpen, eventID, to)
	}
	return nil
}

// touchSQL refresca SOLO el reloj. Que status no aparezca en el SET no es un
// detalle de implementación: es la regla (E-6). Nada mata ni resucita un evento
// por actividad.
const touchSQL = `
UPDATE public.conversation_events
   SET last_activity_at = $2
 WHERE id = $1`

// Touch estampa last_activity_at con el reloj inyectado: es EL refresco del reloj
// de conversación (E-6), lo que hace que una conversación activa nunca venza.
//
// NO toca status —ni el vivo ni el terminal— y no depende de él: el llamador
// refresca al interactuar, y si el evento estaba suspendido deja de estarlo por
// haber vuelto a hablar, no por un cambio de estado que no existe.
func (s *Store) Touch(ctx context.Context, eventID string) error {
	res, err := s.db.ExecContext(ctx, touchSQL, eventID, s.now().UTC())
	if err != nil {
		return fmt.Errorf("events: refrescar el reloj del evento: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("events: filas afectadas por el refresco: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w (id=%s)", ErrEventMissing, eventID)
	}
	return nil
}

// IsSuspended reporta si el evento está suspendido AHORA, según el reloj inyectado
// del store. Es la función pura IsSuspended con el reloj ya puesto: no consulta ni
// escribe nada, y con ttl <= 0 devuelve siempre false.
func (s *Store) IsSuspended(e Event, ttl time.Duration) bool {
	return IsSuspended(e, ttl, s.now().UTC())
}

// appendEntrySQL numera la entrada leyendo el máximo actual DENTRO de la misma
// sentencia (mismo idioma que intakes.insertRevisionQuery): entre el cálculo y la
// escritura no cabe una lectura ajena. Lo que sí cabe es otro INSERT concurrente
// que calcule el mismo número; ese pierde contra el UNIQUE (event_id, seq) y lo
// resuelve el reintento, no un candado que serializaría a todos.
//
// Numerar con MAX+1 —y no con una secuencia— es lo que da el «sin huecos» que pide
// el diseño: un INSERT que falla y revierte no consume número.
const appendEntrySQL = `
INSERT INTO public.conversation_event_messages
       (event_id, seq, role, entry_kind, payload, body_enc, body_dek, body_kek_id)
SELECT $1::uuid, COALESCE(MAX(seq), 0) + 1, $2, $3, $4::jsonb, $5, $6, $7
  FROM public.conversation_event_messages WHERE event_id = $1::uuid
RETURNING seq`

// maxAppendAttempts acota los reintentos por colisión de numeración. Los
// escritores del historial de UN evento son pocos (los mensajes de una
// conversación llegan de uno en uno): 5 intentos sobran, y agotarlos devuelve el
// error en vez de girar indefinidamente.
const maxAppendAttempts = 5

// entry es una entrada del historial ya resuelta a columnas. El grado del ADR-0034
// se decide AQUÍ, en el store, y no lo elige el llamador: o trae payload en claro
// (decision/summary) o trae cuerpo cifrado (message), nunca las dos cosas — que es
// justo lo que exige conversation_event_messages_grade_chk.
type entry struct {
	role      Role
	kind      entryKind
	payload   []byte
	bodyEnc   []byte
	bodyDEK   []byte
	bodyKEKID any
}

// AppendSummary añade el RESUMEN determinista que emitimos nosotros al dejar de
// ser activo un evento (ADR-0029 E-4), con role='system' fijo.
//
// El cuerpo es json.RawMessage y no string A PROPÓSITO. Un resumen es nivel 1 del
// ADR-0034: estructura EN CLARO en payload, porque es negocio cuantificable y
// cifrarlo destruiría su valor sin proteger a nadie. Pedir estructura ya
// serializada es lo que impide que por esta puerta entre prosa: el texto libre no
// tiene sitio aquí, tiene AppendMessage, y allí se cifra siempre.
//
// El rol fijo no es comodidad: toda fila emitida por nosotros va marcada como
// nuestra, para que quien analice el hilo después no cuente dos veces la decisión
// que ya está en la tabla y ADEMÁS aparece dentro del resumen (INV-11).
//
// Devuelve el seq asignado.
func (s *Store) AppendSummary(ctx context.Context, eventID string, body json.RawMessage) (int, error) {
	if !json.Valid(body) {
		return 0, ErrSummaryNotJSON
	}
	return s.appendEntry(ctx, eventID, entry{
		role:    RoleSystem,
		kind:    entryKindSummary,
		payload: body,
	})
}

// AppendMessage añade el TEXTO LITERAL de una interacción, SIEMPRE CIFRADO
// (envelope AES-256-GCM con DEK fresca por valor envuelta por la KEK; el mismo
// FieldCipher que usa contacts).
//
// Es la única puerta de texto libre del store y no tiene variante en claro: no
// existe parámetro, bandera ni camino alternativo que persista body sin cifrar. La
// fila queda con payload NULL, como exige el CHECK de grado — el literal jamás se
// cuela en el nivel 1.
//
// Devuelve el seq asignado.
func (s *Store) AppendMessage(ctx context.Context, eventID string, role Role, body string) (int, error) {
	if role != RoleClient && role != RoleBusiness && role != RoleSystem {
		return 0, fmt.Errorf("%w: %q", ErrInvalidRole, role)
	}
	if s.cipher == nil {
		return 0, ErrNoCipher
	}

	bodyEnc, bodyDEK, kekID, err := s.cipher.Encrypt(body)
	if err != nil {
		return 0, fmt.Errorf("events: cifrar el cuerpo de la entrada: %w", err)
	}

	return s.appendEntry(ctx, eventID, entry{
		role:      role,
		kind:      entryKindMessage,
		bodyEnc:   bodyEnc,
		bodyDEK:   bodyDEK,
		bodyKEKID: kekID,
	})
}

// appendEntry escribe una entrada del historial numerándola sin huecos,
// reintentando si otro escritor se llevó ese seq.
func (s *Store) appendEntry(ctx context.Context, eventID string, e entry) (int, error) {
	var lastErr error
	for attempt := 0; attempt < maxAppendAttempts; attempt++ {
		var seq int
		err := s.db.QueryRowContext(ctx, appendEntrySQL, eventID, e.role, e.kind,
			nullableJSON(e.payload), nullableBytes(e.bodyEnc), nullableBytes(e.bodyDEK),
			e.bodyKEKID).Scan(&seq)
		switch {
		case err == nil:
			return seq, nil
		case postgres.IsUniqueViolation(err):
			// Otro escritor se llevó ese seq: reintentar relee un máximo ya mayor.
			lastErr = err
		default:
			return 0, fmt.Errorf("events: insertar entrada %q del historial: %w", e.kind, err)
		}
	}
	return 0, fmt.Errorf("events: numerar la entrada del historial tras %d intentos: %w", maxAppendAttempts, lastErr)
}

// nullableJSON manda NULL en vez de una cadena vacía (que no es JSON válido para
// el cast a jsonb) y, si hay estructura, la pasa como texto para que Postgres la
// convierta con el `::jsonb` de la sentencia.
func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// nullableBytes manda NULL en vez de un blob vacío: el CHECK de grado razona sobre
// IS NULL, y un []byte de longitud cero no es NULL para Postgres.
func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
