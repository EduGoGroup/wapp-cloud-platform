// Package session mantiene el registro en memoria de los streams CloudLink
// vivos, multiplexado por session_id. Cada sesión corresponde a un stream gRPC
// bidireccional abierto por un Edge; el Registry permite empujar comandos
// (CloudToEdge) hacia el Edge correcto y saber qué sesiones están online.
//
// El estado es puramente en memoria (rápido, derivado del stream vivo). La
// durabilidad (fleet_sessions en PostgreSQL) se añade en tareas posteriores.
package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// ErrSessionOffline indica que no hay un stream vivo para la sesión solicitada,
// por lo que no es posible empujar un comando hacia el Edge.
var ErrSessionOffline = errors.New("sesión offline")

// ErrPushTimeout indica que el envío a un Edge no completó dentro del sendTimeout:
// un Edge lento/atascado (que no lee su stream) no debe retener al llamante
// indefinidamente (Plan 027 · Ola 1 · T5, cierra H6). Es clave para el kill-switch
// (RevokeLease), que no puede quedar atascado en la primera sesión bloqueada.
var ErrPushTimeout = errors.New("timeout empujando comando al Edge")

// ErrPushAbandonado indica que el LLAMANTE se rindió mientras se empujaba el comando
// —su contexto venció o lo cancelaron—, no que el Edge fallara. Es el hermano de
// ErrPushTimeout por el otro brazo del select, y tiene centinela propio desde el
// Plan 050 · Ola 5 · T5.4 por una razón concreta: al añadirse el presupuesto de la
// petición (publicapi.SendBudgetFrom) este camino dejó de ser una rareza —una
// cancelación del cliente— y pasó a ser el desenlace NORMAL de un envío saturado.
// Sin él, el error solo envolvía context.DeadlineExceeded y el traductor HTTP lo
// contaba como «timeout esperando el ack del Edge», que aquí es falso: no se llegó a
// esperar ningún ack, ni siquiera completó el empuje.
//
// Viaja SIEMPRE junto a ctx.Err() (doble %w), así que errors.Is(err,
// context.Canceled/DeadlineExceeded) sigue diciendo la verdad para quien no distinga
// los casos. Es la misma separación que la Enmienda 1 (regla 2) exige entre «el
// llamante se rindió» y «el Edge no lee su stream»: confundirlos borra la señal.
//
// 🔴 Lo que este error NO dice: que el comando no vaya a salir. La goroutine del Send
// sobrevive a la salida por ctx.Done() (ver el ⚠️ de Push), así que el comando puede
// viajar al Edge DESPUÉS de que el llamante haya recibido su error.
var ErrPushAbandonado = errors.New("el llamante se rindió empujando el comando al Edge")

// defaultSendTimeout acota cada Send hacia un Edge cuando no se configura otro con
// WithSendTimeout. 10s es holgado para un stream sano y a la vez desatasca al
// llamante si el Edge dejó de leer (control de flujo gRPC).
const defaultSendTimeout = 10 * time.Second

// Sender es el contrato mínimo que el Registry necesita para empujar mensajes
// hacia un Edge. DEBE ser seguro para Send concurrente: un stream gRPC crudo NO
// lo es (grpc-go prohíbe SendMsg concurrente sobre el mismo stream), así que el
// Gateway registra un envoltorio serializado POR-STREAM (por-Edge, ADR-0008), no
// el stream crudo (Plan 027 · Ola 0 · T3, cierra H2). El Registry NO añade su
// propio candado: serializar por session_id sería la granularidad EQUIVOCADA
// —dos sesiones del mismo Edge comparten un solo stream— y daría falsa seguridad.
type Sender interface {
	Send(*cloudlinkv1.CloudToEdge) error
}

// liveSession asocia un session_id a su Sender. No serializa: la seguridad de
// concurrencia del Send es responsabilidad del Sender (ver el contrato de Sender).
type liveSession struct {
	sender Sender
}

func (s *liveSession) send(msg *cloudlinkv1.CloudToEdge) error {
	return s.sender.Send(msg)
}

// Registry es el registro concurrente de sesiones online, indexadas por
// session_id. Es seguro para uso concurrente.
type Registry struct {
	mu          sync.Mutex
	sessions    map[string]*liveSession
	sendTimeout time.Duration
}

// RegistryOption configura el Registry al construirlo (functional-options).
type RegistryOption func(*Registry)

// WithSendTimeout fija el deadline de cada Send hacia un Edge (Plan 027 · Ola 1 ·
// T5, cierra H6). Un valor <=0 se ignora y cae a defaultSendTimeout.
func WithSendTimeout(d time.Duration) RegistryOption {
	return func(r *Registry) { r.sendTimeout = d }
}

// NewRegistry construye un Registry vacío listo para usar.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{sessions: make(map[string]*liveSession)}
	for _, opt := range opts {
		opt(r)
	}
	if r.sendTimeout <= 0 {
		r.sendTimeout = defaultSendTimeout
	}
	return r
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

// Push envía un comando hacia el Edge de la sesión dada, ACOTADO por DOS relojes
// independientes: el ctx del llamante y el sendTimeout propio del Registry
// (Plan 027 · Ola 1 · T5, cierra H6 · Plan 050 · T1.5-bis, ADR-0040 §Decisión.5 ·
// Enmienda 1). Devuelve un error que envuelve:
//
//   - ErrSessionOffline si la sesión no está online. Esta comprobación es PREVIA y
//     O(1), y GANA SIEMPRE: un ctx ya cancelado no debe convertir un «esa sesión no
//     existe» en un «se acabó el tiempo» (Enmienda 1, regla 3).
//   - ErrPushAbandonado junto a ctx.Err() —NO ErrPushTimeout— si el llamante se rindió
//     antes de que el Send contestara. «El llamante se rindió» y «el Edge no lee su
//     stream» son fallos DISTINTOS y confundirlos borraría la señal (Enmienda 1,
//     regla 2). El centinela se añadió en T5.4, cuando el presupuesto de la petición
//     convirtió este camino en el desenlace normal de un envío saturado.
//   - ErrPushTimeout si venció el sendTimeout (Edge lento que no lee su stream). El
//     timer NO se retira con la llegada del ctx: sigue siendo el techo absoluto para
//     el llamante que pase un ctx sin deadline —un handler HTTP no trae ninguno— y
//     WAPP_GRPC_PUSH_TIMEOUT no se toca (INV-050.6).
//
// El Send se ejecuta en una goroutine con un canal bufferizado (cap 1): si se sale
// por cualquiera de los dos relojes, la goroutine no se bloquea al entregar el
// resultado y termina en cuanto el stream se desatasca o el Edge cae (fuga acotada,
// no indefinida).
//
// ⚠️ Cancelar el ctx NO desbloquea el stream.Send de gRPC, y esta función no lo
// promete: la goroutine de arriba sobrevive a la salida por ctx.Done() exactamente
// igual que sobrevivía a la salida por timer. Lo que el ctx compra es que el
// LLAMANTE deje de esperar, no que el envío se cancele (Enmienda 1, regla 1).
func (r *Registry) Push(ctx context.Context, sessionID string, msg *cloudlinkv1.CloudToEdge) error {
	r.mu.Lock()
	ls := r.sessions[sessionID]
	r.mu.Unlock()

	if ls == nil {
		return fmt.Errorf("%w: %q", ErrSessionOffline, sessionID)
	}

	done := make(chan error, 1)
	go func() { done <- ls.send(msg) }()
	timer := time.NewTimer(r.sendTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("%w: sesión %q: %w", ErrPushAbandonado, sessionID, ctx.Err())
	case <-timer.C:
		return fmt.Errorf("%w: %q", ErrPushTimeout, sessionID)
	}
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
