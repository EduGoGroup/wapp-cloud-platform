// Package session mantiene el registro en memoria de los streams CloudLink
// vivos, multiplexado por session_id. Cada sesión corresponde a un stream gRPC
// bidireccional abierto por un Edge; el Registry permite empujar comandos
// (CloudToEdge) hacia el Edge correcto y saber qué sesiones están online.
//
// El estado es puramente en memoria (rápido, derivado del stream vivo). La
// durabilidad (fleet_sessions en PostgreSQL) se añade en tareas posteriores.
package session

import (
	"errors"
	"fmt"
	"sync"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// ErrSessionOffline indica que no hay un stream vivo para la sesión solicitada,
// por lo que no es posible empujar un comando hacia el Edge.
var ErrSessionOffline = errors.New("sesión offline")

// Sender es el contrato mínimo que el Registry necesita para empujar mensajes
// hacia un Edge. Un stream gRPC del servidor CloudLink lo satisface con su
// método Send(*CloudToEdge) error.
type Sender interface {
	Send(*cloudlinkv1.CloudToEdge) error
}

// liveSession envuelve un Sender y serializa los Send de esa sesión: un stream
// gRPC no admite llamadas a Send concurrentes, así que cada envío toma el mutex
// de la sesión.
type liveSession struct {
	sendMu sync.Mutex
	sender Sender
}

func (s *liveSession) send(msg *cloudlinkv1.CloudToEdge) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.sender.Send(msg)
}

// Registry es el registro concurrente de sesiones online, indexadas por
// session_id. Es seguro para uso concurrente.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*liveSession
}

// NewRegistry construye un Registry vacío listo para usar.
func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*liveSession)}
}

// Register asocia un Sender a la sesión dada y devuelve una función release que
// la marca offline. La política es última-gana: si ya existía una sesión con el
// mismo session_id (p.ej. una reconexión del Edge), la nueva la reemplaza. La
// función release devuelta solo elimina la sesión si sigue siendo la registrada
// por esta llamada (se compara la identidad de la entrada), de modo que el
// release de una sesión ya reemplazada es un no-op seguro e idempotente.
func (r *Registry) Register(sessionID string, s Sender) (release func()) {
	ls := &liveSession{sender: s}

	r.mu.Lock()
	r.sessions[sessionID] = ls
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		if r.sessions[sessionID] == ls {
			delete(r.sessions, sessionID)
		}
		r.mu.Unlock()
	}
}

// Push envía un comando hacia el Edge de la sesión dada. Devuelve un error que
// envuelve ErrSessionOffline si la sesión no está online.
func (r *Registry) Push(sessionID string, msg *cloudlinkv1.CloudToEdge) error {
	r.mu.Lock()
	ls := r.sessions[sessionID]
	r.mu.Unlock()

	if ls == nil {
		return fmt.Errorf("%w: %q", ErrSessionOffline, sessionID)
	}
	return ls.send(msg)
}

// Online indica si hay un stream vivo para la sesión dada.
func (r *Registry) Online(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.sessions[sessionID]
	return ok
}

// Count devuelve el número de sesiones online.
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}
