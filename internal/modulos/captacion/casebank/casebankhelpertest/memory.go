package casebankhelpertest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank"
)

// Row es una fila del banco tal como queda guardada: lo que un observador lee de
// public.intake_case_bank (sin created_at, que nadie consulta).
type Row struct {
	// ID es el id que devolvió Insert.
	ID int64
	// TenantID, Consented y SourceText son las columnas homónimas.
	TenantID   string
	Consented  bool
	SourceText string
	// Expected es la columna expected. nil es el NULL de SQL («aún no se curó»).
	Expected json.RawMessage
}

// errConsentedCheck es el rechazo de un caso sin consentimiento. Lleva el nombre
// de la constraint de la 0082, que es lo que viaja en el error de Postgres y lo
// que la suite busca en los dos.
var errConsentedCheck = errors.New(`casebankhelpertest: la fila viola el CHECK "intake_case_bank_consented_check"`)

// errInvalidExpected es el rechazo de un expected que no es JSON: la columna es
// JSONB y Postgres no lo guarda.
var errInvalidExpected = errors.New("casebankhelpertest: expected no es JSON válido (la columna es JSONB)")

// Memory (en la spec, `casebankhelpertest.Memoria`) es el doble en memoria de
// casebank.Store. Nuevo: el paquete viejo no tenía gemelo (05 E-6). Se comporta
// como public.intake_case_bank detrás de casebank.Postgres, y lo demuestra
// corriendo la misma suite (Contrato):
//
//   - los ids son crecientes desde 1, como el BIGSERIAL;
//   - rechaza el caso con Consented=false, como el CHECK de la tabla, y el
//     expected que no es JSON, como la columna JSONB; un rechazo no escribe;
//   - NO valida tenant ni texto y NO deduplica: eso es del servicio.
//
// Además deja ver lo que el puerto no enseña —las filas (Rows) y cuántas veces
// se le llamó (Calls)— y deja provocar fallos (FailInsert, FailExists), que es lo
// que los tests de casebank.Service necesitan. Es seguro para uso concurrente.
type Memory struct {
	mu        sync.Mutex
	lastID    int64
	rows      []Row
	inserts   int
	exists    int
	insertErr error
	existsErr error
}

// NewMemory devuelve un banco vacío y listo.
func NewMemory() *Memory { return &Memory{} }

var _ casebank.Store = (*Memory)(nil)

// Insert guarda el caso tal cual llega y devuelve su id. Ver Memory para lo que
// rechaza.
func (m *Memory) Insert(_ context.Context, c casebank.Case) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inserts++
	if m.insertErr != nil {
		return 0, m.insertErr
	}
	if !c.Consented {
		return 0, errConsentedCheck
	}
	// Vacío ⇒ NULL, igual que el adaptador Postgres; y lo guardado es una copia:
	// el llamador puede reutilizar su slice.
	var expected json.RawMessage
	if len(c.Expected) > 0 {
		if !json.Valid(c.Expected) {
			return 0, errInvalidExpected
		}
		expected = append(json.RawMessage(nil), c.Expected...)
	}
	m.lastID++
	m.rows = append(m.rows, Row{
		ID:         m.lastID,
		TenantID:   c.TenantID,
		Consented:  c.Consented,
		SourceText: c.SourceText,
		Expected:   expected,
	})
	return m.lastID, nil
}

// Exists dice si ese tenant tiene una fila con ese literal exacto.
func (m *Memory) Exists(_ context.Context, tenantID, sourceText string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exists++
	if m.existsErr != nil {
		return false, m.existsErr
	}
	for _, r := range m.rows {
		if r.TenantID == tenantID && r.SourceText == sourceText {
			return true, nil
		}
	}
	return false, nil
}

// Rows devuelve una copia de las filas de ese tenant, en orden de inserción (que
// es el de sus ids). Sin filas devuelve un slice vacío.
func (m *Memory) Rows(tenantID string) []Row {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Row{}
	for _, r := range m.rows {
		if r.TenantID == tenantID {
			r.Expected = append(json.RawMessage(nil), r.Expected...)
			out = append(out, r)
		}
	}
	return out
}

// Calls dice cuántas veces se llamó a Insert y a Exists, fallaran o no. Es lo que
// deja afirmar «esto no llegó a la base».
func (m *Memory) Calls() (inserts, exists int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inserts, m.exists
}

// FailInsert hace que todo Insert posterior falle con err sin escribir. Con nil
// vuelve a funcionar.
func (m *Memory) FailInsert(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.insertErr = err
}

// FailExists hace que todo Exists posterior falle con err. Con nil vuelve a
// funcionar.
func (m *Memory) FailExists(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.existsErr = err
}
