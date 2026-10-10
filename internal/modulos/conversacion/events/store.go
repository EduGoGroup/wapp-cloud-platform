// Porta internal/flujos/events/store.go @ 9d5a4b6
//
// El fichero viejo medía 1.060 líneas y aquí NACE PARTIDO por tema (05 E-13), solo repartiendo
// declaraciones:
//
//   - store.go (este): el tipo, sus opciones, el constructor y el CICLO DE VIDA del evento sobre
//     public.conversation_events: nacer, leer el vivo, leer por id acotado al tenant, listar los
//     vivos, transitar, refrescar el reloj y la pregunta derivada «¿suspendido?».
//   - store_list.go: la consulta de RESCATABLES y el listado del dueño (ListRescuable, ListEvents)
//     y los tipos que el tenant puede ver (KindFeatures, AllowedKinds).
//   - store_filter.go: el filtro y la página del listado (ListFilter, ContentFilter, EventPage y
//     sus validadores). Es puro y nace VERDE: lo necesita el doble en memoria.
//   - store_append.go: las cinco puertas de escritura del historial
//     (public.conversation_event_messages).
//
// La lectura del hilo ya era fichero aparte en el viejo (thread_reader.go).
//
// Reglas que valen para TODOS los trozos del adaptador:
//
//   - el SQL se porta BYTE A BYTE del paquete viejo y no se «mejora» al portar. Que ese SQL haga
//     en un Postgres de verdad lo que el contrato promete lo prueba eventshelpertest.Contrato en
//     los procesos de F9 (test/procesos/events_contrato_test.go); el test de cada trozo afirma,
//     con un driver de mentira, la forma: qué se valida antes de ir a la base, qué sentencias
//     salen, con qué argumentos, y el mapeo de filas y errores;
//   - todo fallo de la base vuelve envuelto con %w y con el prefijo literal que dice el contrato
//     de cada método, y con los valores de retorno a cero;
//   - EL RELOJ ES EL INYECTADO (WithClock), siempre en UTC: ningún instante que este adaptador
//     escribe o compara sale de now() de la base;
//   - D-17 (deuda que se porta tal cual): los listados cierran sus filas con el ritual
//     `defer rows.Close()` que solo informa del fallo del cierre si no había ya otro error.
//
// 🔴 D-1 (deuda que se porta tal cual y NO se arregla aquí): public.conversation_event_messages
// guarda el cuerpo cifrado en un sobre de tres columnas (body_enc, body_dek, body_kek_id) y la
// tabla está FUERA del censo `rekeyTargets` de internal/platform/crypto/rekey.go: una rotación de
// KEK no re-envuelve sus filas. Por eso la lectura descifra con la KEK que envolvió CADA fila
// (body_kek_id) y no con la vigente.
//
// 🔴 HOMÓNIMO: la `body_dek` de este adaptador es la DEK del ENVELOPE DE PII DE NEGOCIO (una clave
// fresca por valor, envuelta por la KEK de la plataforma: crypto.FieldCipher). NO es la DEK del
// ADR-0007, la que descifra el almacén de whatsmeow, que custodia el cliente y jamás cruza a la
// nube. El zero-knowledge protege llaves, no este contenido, que sube a la nube a propósito.

package events

import (
	"context"
	"database/sql"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
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
//
// En el rojo no lleva campos. El verde le pone tres: el *sql.DB, el *crypto.FieldCipher y el reloj.
type Store struct{}

// Option configura el Store en la construcción.
type Option func(*Store)

// WithClock inyecta el reloj del store: el que compone HistoryID, el que estampa
// el nacimiento y el que refresca last_activity_at en Touch. Sin él, NewStore usa
// time.Now. Existe para tests deterministas (mismo patrón que el runtime).
//
// Un now nil se ignora: el store conserva el reloj que tenía.
func WithClock(now func() time.Time) Option {
	panic(pendiente.Implementar("events.WithClock"))
}

// NewStore construye el store sobre el pool dado. cipher es obligatorio para
// AppendMessage: sin él, la única puerta de texto literal queda cerrada (devuelve
// error) en vez de abrir una que escriba en claro.
//
// No toca la base ni valida el pool: un db nil no falla aquí sino en la primera sentencia (las dos
// lecturas del hilo, ListThread y ListPastedByOwner, devuelven nil sin error). Las opciones se
// aplican en orden.
func NewStore(db *sql.DB, cipher *crypto.FieldCipher, opts ...Option) *Store {
	panic(pendiente.Implementar("events.NewStore"))
}

// CreateEvent inserta un evento VIVO y devuelve la fila tal como quedó.
//
// El padre nace SIN columnas de ningún hijo (D-043.21): si el evento produce
// contenido durable, es el contenido quien declara a su padre con su event_id —
// nunca al revés.
//
// El nacimiento y el reloj arrancan en el MISMO instante del reloj inyectado (por
// eso $8 va dos veces), y de ese instante sale también HistoryID en UTC: la fila
// no puede quedar diciendo que nació a una hora y que su id legible es de otra.
//
// Si ya hay un evento vivo de ese tipo en la conversación devuelve ErrAliveExists.
// Esa regla la impone el índice único parcial de la BD (E-2), no una comprobación
// previa del código: entre un SELECT y un INSERT cabe otro escritor, y en el
// índice no cabe nadie.
//
// La fila devuelta trae Status = StatusOpen, CreatedAt = LastActivityAt = el instante del reloj
// inyectado (UTC), HistoryID = HistoryID(in.Kind, ese instante) y ClosedAt a cero. El id lo pone
// la base.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w (tenant=%s sesión=%s tipo=%s): %w": ErrAliveExists Y la violación de unicidad, las dos
//     envueltas (errors.Is casa con ErrAliveExists);
//   - "events: insertar evento: %w" ante cualquier otro fallo.
func (s *Store) CreateEvent(ctx context.Context, in NewEvent) (Event, error) {
	panic(pendiente.Implementar("events.Store.CreateEvent"))
}

// GetAliveByKind devuelve el evento VIVO de ese tipo en la conversación. El
// segundo retorno dice si lo había: no haberlo es normal (E-6, el saludo no crea
// evento), no un error.
//
// No hace falta LIMIT 1: el índice único parcial garantiza que hay como mucho uno.
//
// Acota por los CUATRO (tenant, sesión, contacto, tipo) y por status = 'open': el mismo contacto en
// otra sesión, otro contacto, otro tenant u otro tipo no casan, y un evento terminal tampoco.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "events: leer evento vivo de tipo %q: %w".
func (s *Store) GetAliveByKind(ctx context.Context, tenantID, sessionID, contactID, kind string) (Event, bool, error) {
	panic(pendiente.Implementar("events.Store.GetAliveByKind"))
}

// GetEventForTenant devuelve el evento id SI Y SOLO SI pertenece al tenant
// (T4.2). Cualquier ausencia —id inexistente, id de otro tenant, id que ni
// siquiera es un UUID— es el MISMO ErrEventNotFound: el llamante lo traduce a un
// 404 que no revela si el evento existe para otro (criterio literal de T4.2).
//
// El UUID se valida ANTES de consultar, por la misma razón que
// ListIntakeItems del store del motor: el id llega de un segmento de URL que puede traer
// cualquier cosa, y sin la guarda Postgres contestaría 22P02 —un error real que
// acabaría en 500— a una pregunta cuya respuesta honesta es «eso no existe».
//
// Lee el evento en CUALQUIER estado (open, closed, cancelled): la pregunta es por pertenencia, no por
// vida. Un id que no es UUID no llega a la base.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w (id=%q no es un UUID)": ErrEventNotFound, sin consultar;
//   - "%w (id=%s)": ErrEventNotFound, sin fila para (id, tenant);
//   - "events: leer el evento del tenant: %w".
func (s *Store) GetEventForTenant(ctx context.Context, tenantID, eventID string) (Event, error) {
	panic(pendiente.Implementar("events.Store.GetEventForTenant"))
}

// ListAlive devuelve los eventos VIVOS de la conversación en orden de NACIMIENTO:
// status='open' y nada más, sin mirar la solicitud.
//
// ⚠️ NO es la lista de lo que se le puede ofrecer a un cliente. Un carrito cuyo
// pedido descartó el dueño sigue VIVO un rato —el descarte cancela el evento en
// otra sentencia— y aquí sale. Quien vaya a ENSEÑAR o RETOMAR algo usa
// ListRescuable (INV-17: no se lista, no se rescata y no se menciona); esta
// responde la otra pregunta, «¿qué tiene abierto?», que es de quien audita o
// ejecuta.
//
// Orden: created_at ascendente y, a igual instante, id. Acota por (tenant, sesión, contacto). Sin
// eventos devuelve la lista vacía (nil) sin error.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "events: listar eventos vivos: %w" si la consulta falla;
//   - "events: leer fila de evento: %w" si una fila no se puede leer;
//   - "events: recorrer eventos vivos: %w" si el recorrido termina con error;
//   - "events: cerrar filas de eventos: %w" si lo único que falla es el cierre (D-17).
func (s *Store) ListAlive(ctx context.Context, tenantID, sessionID, contactID string) ([]Event, error) {
	panic(pendiente.Implementar("events.Store.ListAlive"))
}

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
//
// Solo toca status y closed_at: last_activity_at y el resto de la fila quedan como estaban. No acota
// por tenant (el id basta); un id que no existe es, para el guard, lo mismo que uno que ya no está
// vivo: ErrNotOpen. Dos transiciones simultáneas del mismo evento: gana EXACTAMENTE una y la otra
// recibe ErrNotOpen sin pisar el estado ni el closed_at de la ganadora.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "%w (recibido %q)": ErrNotTerminal, sin ir a la base;
//   - "events: transitar evento a %q: %w";
//   - "events: filas afectadas por la transición: %w";
//   - "%w (id=%s destino=%s)": ErrNotOpen.
func (s *Store) TransitionEvent(ctx context.Context, eventID string, to Status) error {
	panic(pendiente.Implementar("events.Store.TransitionEvent"))
}

// Touch estampa last_activity_at con el reloj inyectado: es EL refresco del reloj
// de conversación (E-6), lo que hace que una conversación activa nunca venza.
//
// NO toca status —ni el vivo ni el terminal— y no depende de él: el llamador
// refresca al interactuar, y si el evento estaba suspendido deja de estarlo por
// haber vuelto a hablar, no por un cambio de estado que no existe.
//
// Vale también sobre un evento terminal (lo refresca igual: el UPDATE no mira status).
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "events: refrescar el reloj del evento: %w";
//   - "events: filas afectadas por el refresco: %w";
//   - "%w (id=%s)": ErrEventMissing, si el id no existe.
func (s *Store) Touch(ctx context.Context, eventID string) error {
	panic(pendiente.Implementar("events.Store.Touch"))
}

// IsSuspended reporta si el evento está suspendido AHORA, según el reloj inyectado
// del store. Es la función pura IsSuspended con el reloj ya puesto: no consulta ni
// escribe nada, y con ttl <= 0 devuelve siempre false.
func (s *Store) IsSuspended(e Event, ttl time.Duration) bool {
	panic(pendiente.Implementar("events.Store.IsSuspended"))
}
