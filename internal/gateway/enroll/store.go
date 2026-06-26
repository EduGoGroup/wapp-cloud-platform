package enroll

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Errores de consumo de código (sentinela). El transporte (Server.EnrollEdge)
// los traduce todos a codes.PermissionDenied; se mantienen agnósticos al
// transporte.
//
// MemoryStore distingue las tres causas (útil en tests unitarios). El store
// Postgres NO las distingue a propósito: el consumo es un único UPDATE atómico
// que, si no afecta filas, no puede (ni debe) revelar si el código no existe,
// expiró o ya se usó; en todos esos casos devuelve ErrCodeInvalid. La respuesta
// de seguridad es la misma (PermissionDenied) y así no se filtra información.
var (
	// ErrCodeNotFound indica que el código no existe en el store.
	ErrCodeNotFound = errors.New("enroll: código de activación desconocido")
	// ErrCodeExpired indica que el código existe pero su TTL ya venció.
	ErrCodeExpired = errors.New("enroll: código de activación expirado")
	// ErrCodeUsed indica que el código ya fue consumido (es de un solo uso).
	ErrCodeUsed = errors.New("enroll: código de activación ya utilizado")
	// ErrCodeInvalid indica que el código no es consumible (ausente, expirado o
	// usado), sin distinguir la causa. Lo usa el store Postgres.
	ErrCodeInvalid = errors.New("enroll: código de activación inválido")
)

// CodeStore valida y consume códigos de activación de un solo uso.
type CodeStore interface {
	// Consume valida que el código exista, no esté expirado ni usado; al éxito lo
	// marca como usado (atómico) y devuelve el tenant asociado. En fallo devuelve
	// uno de los ErrCode* sentinela.
	Consume(ctx context.Context, code string) (tenantID string, err error)
}

type activationCode struct {
	tenantID string
	expiry   time.Time
	used     bool
}

// MemoryStore es una implementación en memoria de CodeStore, segura para
// concurrencia. Pensada para unit tests CI-safe (sin BD); en prod se usa
// PostgresCodeStore. Para tests se siembran códigos con Add.
type MemoryStore struct {
	mu    sync.Mutex
	codes map[string]*activationCode
	now   func() time.Time
}

// NewMemoryStore crea un store vacío con reloj wall-clock.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		codes: make(map[string]*activationCode),
		now:   time.Now,
	}
}

// Add siembra un código de activación válido hasta expiry para el tenant dado.
// Pensado para dev/tests (en prod los emite la plataforma). Sobrescribe si el
// código ya existía.
func (s *MemoryStore) Add(code, tenantID string, expiry time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code] = &activationCode{tenantID: tenantID, expiry: expiry}
}

// Consume implementa CodeStore: un solo uso, con validación de existencia, TTL y
// reuso. La transición a "used" ocurre bajo el mismo lock que la validación, de
// modo que dos consumos concurrentes del mismo código no pueden tener éxito ambos.
func (s *MemoryStore) Consume(_ context.Context, code string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[code]
	if !ok {
		return "", ErrCodeNotFound
	}
	if s.now().After(c.expiry) {
		return "", ErrCodeExpired
	}
	if c.used {
		return "", ErrCodeUsed
	}
	c.used = true
	return c.tenantID, nil
}
