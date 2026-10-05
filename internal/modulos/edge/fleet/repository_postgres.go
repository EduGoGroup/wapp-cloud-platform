// Porta internal/gateway/fleet/repository_postgres.go @ 809345b

package fleet

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Logger es el puerto ESTRECHO de log que este repositorio necesita: una sola
// línea de aviso. Se declara aquí —y no se importa wapp-shared/logger— para que
// el dominio de flota no dependa de una implementación concreta de log; la
// interfaz la cumple tal cual sharedlogger.Logger (y también *slog.Logger).
//
// 🔴 SOLO se usa para el ÚNICO caso que no puede ser ni un error ni un silencio:
// un sobre de self_pn que no descifra al SERVIR EL LISTADO (ver scanSession y
// selfPnDecryptTally, que lo emite AGREGADO: una línea por llamada, no por fila).
// En ningún caso se le pasa el número: es PII.
type Logger interface {
	Warn(msg string, args ...any)
}

// Option configura al repositorio en su construcción.
type Option func(*PostgresRepository)

// WithLogger enchufa el logger del proceso. Sin él, el aviso de «un sobre de
// self_pn no descifra» se pierde: el listado sigue sirviéndose, pero nadie se
// entera de que un número quedó ilegible. El arranque SIEMPRE debe pasarlo.
func WithLogger(l Logger) Option {
	panic(pendiente.Implementar("fleet.WithLogger"))
}

// PostgresRepository implementa Repository con SQL raw sobre
// public.fleet_sessions.
//
// 🔒 EL self_pn VA CIFRADO EN REPOSO desde el Plan 046 · T4.1 (migración 0068).
// La fila guarda CUATRO columnas —self_pn_enc (envelope), self_pn_dek (DEK
// envuelta), self_pn_kek_id (con qué KEK desenvolver) y self_pn_bidx (índice
// ciego, para buscar/contar sin descifrar)— y la columna EN CLARO `self_pn`
// queda VACÍA: este código NO la escribe nunca más y NO la lee nunca más para
// obtener el número. El número en claro solo vive en memoria, en el borde.
//
// Es el MISMO molde que contact.PostgresResolver (Plan 011, ADR-0017), y a
// propósito: cipher hace el envelope, kp calcula el índice ciego. Dos moldes
// distintos para el mismo problema serían dos rotaciones de KEK que gestionar.
type PostgresRepository struct{}

// NewPostgresRepository construye el repositorio sobre el pool dado. cipher y kp
// son OBLIGATORIOS desde T4.1: sin ellos no se puede ni escribir ni leer el
// self_pn, así que se piden por parámetro posicional y no por Option — un
// repositorio a medio construir escribiría filas sin número y las leería vacías,
// en silencio y para siempre.
//
// No abre ni comprueba la conexión. Sin WithLogger —o con WithLogger(nil)— el
// logger es el mudo (nopLogger).
func NewPostgresRepository(db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider, opts ...Option) *PostgresRepository {
	panic(pendiente.Implementar("fleet.NewPostgresRepository"))
}

// PostgresRepository cumple el puerto entero (los diez métodos de Repository).
var _ Repository = (*PostgresRepository)(nil)

// MarkOnline registra/actualiza la sesión como online.
//
// 🔴 EL INSERT NO NOMBRA `profile`, Y ESO ES EL CIERRE DE T3.1, NO UN OLVIDO. Esta
// es la vía de alta REAL de una sesión —la fila nace aquí, en el registro del stream
// CloudLink—, así que dejar la columna fuera de la lista hace que Postgres aplique su
// `DEFAULT 'passive'` (0063:112) y la sesión NAZCA PASIVA (D-046.7): no auto-responde
// hasta que alguien la active a mano. Nombrarla aquí —aunque fuera para escribir
// 'passive'— movería la decisión del esquema al código y abriría la puerta a que una
// vía de alta futura eligiera otra cosa sin que nadie lo note.
//
// El ON CONFLICT tampoco la toca: una sesión que RECONECTA conserva su perfil. Quien
// mueve el eje es SetProfile y solo SetProfile (ver su docstring y el de la 0065).
//
// Un fallo del driver vuelve envuelto como "fleet: marcar online: …".
func (r *PostgresRepository) MarkOnline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	panic(pendiente.Implementar("fleet.PostgresRepository.MarkOnline"))
}

// MarkOffline marca la sesión como offline. No falla si la sesión no existía
// (UPDATE de 0 filas es válido: nunca llegó a registrarse online).
// Un fallo del driver vuelve envuelto como "fleet: marcar offline: …".
func (r *PostgresRepository) MarkOffline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	panic(pendiente.Implementar("fleet.PostgresRepository.MarkOffline"))
}

// MarkLoggedOut marca la sesión como zombie (StateLoggedOut): WhatsApp cerró el
// device (Plan 020 · T3). Como MarkOffline es un UPDATE acotado por identidad; no
// falla si la sesión no existía (UPDATE de 0 filas es válido). Se distingue del
// offline-por-red por el estado escrito, no por el camino de código.
// Un fallo del driver vuelve envuelto como "fleet: marcar loggedout: …".
func (r *PostgresRepository) MarkLoggedOut(ctx context.Context, tenantID, edgeID, sessionID string) error {
	panic(pendiente.Implementar("fleet.PostgresRepository.MarkLoggedOut"))
}

// SetState fija el estado (offline|loggedout) de la sesión del tenant. UPDATE
// acotado por tenant_id + session_id (aislamiento multi-tenant, INV-8): toca TODAS
// las filas de esa sesión bajo el tenant. found=false si 0 filas (sesión
// inexistente o de otro tenant ⇒ 404 opaco). Valida el estado antes de tocar la BD.
// Un estado inválido devuelve (false, ErrInvalidState) sin emitir sentencia; un
// fallo del driver vuelve envuelto como "fleet: fijar estado: …" y el de leer las
// filas afectadas como "fleet: filas afectadas al fijar estado: …".
func (r *PostgresRepository) SetState(ctx context.Context, tenantID, sessionID string, state State) (bool, error) {
	panic(pendiente.Implementar("fleet.PostgresRepository.SetState"))
}

// Get devuelve la sesión, o found=false si no existe: sin fila la respuesta es
// (Session{}, false, nil). Un fallo del driver o del escaneo vuelve envuelto como
// "fleet: leer sesión: …". Un sobre de self_pn que no abre NO es error: la sesión
// sale con SelfPn vacío y queda un Warn (ver scanSession).
func (r *PostgresRepository) Get(ctx context.Context, tenantID, edgeID, sessionID string) (Session, bool, error) {
	panic(pendiente.Implementar("fleet.PostgresRepository.Get"))
}

// List devuelve las sesiones de un tenant, en el orden (edge_id, session_id) que
// fija la sentencia; sin filas devuelve nil. Los errores vuelven envueltos:
// "fleet: listar sesiones: …" (la consulta), "fleet: escanear sesión: …" (una
// fila) y "fleet: iterar sesiones: …" (la iteración). Un fallo al CERRAR las filas
// tras recorrerlas enteras también sale como "fleet: iterar sesiones: …":
// database/sql cierra solo al agotar la iteración y entrega ese error por
// rows.Err(), así que la rama "fleet: cerrar filas: …" del defer no se alcanza por
// ese camino (se porta tal cual). Los sobres de self_pn que no abren dejan su
// SelfPn vacío y UN solo Warn para el listado entero.
func (r *PostgresRepository) List(ctx context.Context, tenantID string) (out []Session, err error) {
	panic(pendiente.Implementar("fleet.PostgresRepository.List"))
}
