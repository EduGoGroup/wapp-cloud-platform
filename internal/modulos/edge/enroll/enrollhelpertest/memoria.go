// Porta internal/gateway/enroll/store.go (MemoryStore) e
// internal/gateway/enroll/edgecert.go (MemoryEdgeCertRepository) @ 8896f13

package enrollhelpertest

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
)

type activationCode struct {
	tenantID string
	expiry   time.Time
	used     bool
}

// MemoriaCodeStore es una implementación en memoria de enroll.CodeStore, segura
// para concurrencia. Pensada para unit tests CI-safe (sin BD); en prod se usa
// PostgresCodeStore. Para tests se siembran códigos con Add. En el paquete
// viejo era enroll.MemoryStore y vivía en producción (D-F3-1).
//
// A diferencia de Postgres, DISTINGUE la causa de un consumo fallido:
// ErrCodeNotFound, ErrCodeExpired o ErrCodeUsed (nunca ErrCodeInvalid).
type MemoriaCodeStore struct {
	mu    sync.Mutex
	codes map[string]*activationCode
	now   func() time.Time
}

// NewMemoriaCodeStore crea un store vacío con reloj wall-clock (en el paquete
// viejo, NewMemoryStore).
func NewMemoriaCodeStore() *MemoriaCodeStore {
	return &MemoriaCodeStore{
		codes: make(map[string]*activationCode),
		now:   time.Now,
	}
}

// Add siembra un código de activación válido hasta expiry para el tenant dado.
// Pensado para dev/tests (en prod los emite la plataforma). Sobrescribe si el
// código ya existía. El código se guarda tal cual, sin normalizar.
func (s *MemoriaCodeStore) Add(code, tenantID string, expiry time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code] = &activationCode{tenantID: tenantID, expiry: expiry}
}

// Consume implementa CodeStore: un solo uso, con validación de existencia, TTL y
// reuso. La transición a "used" ocurre bajo el mismo lock que la validación, de
// modo que dos consumos concurrentes del mismo código no pueden tener éxito ambos.
func (s *MemoriaCodeStore) Consume(_ context.Context, code string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[code]
	if !ok {
		return "", enroll.ErrCodeNotFound
	}
	if s.now().After(c.expiry) {
		return "", enroll.ErrCodeExpired
	}
	if c.used {
		return "", enroll.ErrCodeUsed
	}
	c.used = true
	return c.tenantID, nil
}

// MemoriaEdgeCertRepository es una implementación en memoria de
// enroll.EdgeCertRepository para unit tests CI-safe (sin BD), segura para
// concurrencia. En el paquete viejo era enroll.MemoryEdgeCertRepository.
type MemoriaEdgeCertRepository struct {
	mu      sync.Mutex
	records []enroll.EdgeCertRecord
}

// NewMemoriaEdgeCertRepository crea un repo en memoria vacío (en el paquete
// viejo, NewMemoryEdgeCertRepository).
func NewMemoriaEdgeCertRepository() *MemoriaEdgeCertRepository {
	return &MemoriaEdgeCertRepository{}
}

// Create agrega el registro a la lista en memoria.
func (r *MemoriaEdgeCertRepository) Create(_ context.Context, rec enroll.EdgeCertRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
	return nil
}

// Records devuelve una copia de los registros guardados (para aserciones en tests).
func (r *MemoriaEdgeCertRepository) Records() []enroll.EdgeCertRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]enroll.EdgeCertRecord, len(r.records))
	copy(out, r.records)
	return out
}
