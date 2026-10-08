// Porta internal/intake/pipeline/memoria.go @ 8d875ab (su mitad StoreEnMemoria; D-F7-5)

package intakehelpertest

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// ════════════════════════════════════════════════════════════════════════════
// EL DOBLE EN MEMORIA DE `PipelineStore` — QUÉ PRUEBA Y QUÉ **NO** PRUEBA
// ════════════════════════════════════════════════════════════════════════════
//
// LO QUE ESTE DOBLE SIRVE PARA PROBAR ES LA POLÍTICA DEL WORKER. Cuántos intentos, cuánto
// se empuja la marca, qué desenlace le toca a cada causa, qué etapas se saltan al reanudar,
// que un job envenenado no para a los demás. Nada de eso vive en SQL: vive en el worker, en
// Go, y probarlo contra Postgres costaría un contenedor por escenario para observar
// exactamente las mismas variables.
//
// 🔴 LOS GUARDS DE LA MÁQUINA VIVEN EN SQL (el `status =` del claim, el `array_position` del
// avance, el vaciado del sobre en la misma sentencia) Y ESTE DOBLE LOS REESCRIBE A MANO. En
// el viejo eso era una advertencia —«no se puede usar para afirmar nada de la máquina»— y la
// única defensa era no usarlo. Aquí la defensa es ContratoMachine: los mismos casos corren
// contra este doble en unitario y contra intake.Postgres en F9, así que un `if` de aquí que
// se desvíe del SQL pone la suite en rojo en uno de los dos lados.
//
// ⚠️ LO QUE SIGUE SIN PODER AFIRMARSE CON ÉL: la concurrencia. Su reclamo no bloquea filas ni
// tiene `SKIP LOCKED`; toda operación va bajo un único cerrojo. «Dos réplicas se llevan jobs
// distintos sin esperarse» es de Postgres.
//
// # POR QUÉ ES CÓDIGO EXPORTADO Y NO UN FAKE EN UN `_test.go`
//
// Porque lo necesitan paquetes de test que no se pueden ver entre sí: el del worker del
// pipeline y el del runtime de flujos (el criterio INV-10). Duplicarlo sería garantizar que
// las copias divergen.
//
// # LO QUE CAMBIA RESPECTO AL VIEJO (`pipeline.StoreEnMemoria`), y por qué
//
// Todo son columnas o guardas que Postgres tiene y el doble viejo no, y que la suite vigila:
//
//   - guarda `updated_at`, y lo mueve en cada escritura que aplica;
//   - el reclamo devuelve también el contexto del re-análisis (el viejo lo dejaba a cero);
//   - una transición sin id de job es un error, como en el adaptador (el viejo devolvía
//     `(false, nil)`);
//   - View y el reclamo devuelven copias profundas (el viejo compartía las referencias y los
//     artefactos con la fila).
// ════════════════════════════════════════════════════════════════════════════

// MachineMemory es el doble (antes `pipeline.StoreEnMemoria`). Ver el bloque de cabecera antes
// de usarlo. Su fila es Row (antes `pipeline.Fila`).
type MachineMemory struct {
	mu   sync.Mutex
	rows []*Row
	now  func() time.Time
	seq  int

	// claims cuenta las llamadas a los reclamos. Es lo que permite afirmar «el worker siguió
	// preguntando» sin medir tiempos.
	claims int
	// claimFailure, si no es nil, es lo que devuelven los reclamos. Existe para probar el
	// camino de «la base no contesta», que si no sería inalcanzable.
	claimFailure error
}

// El doble satisface los dos puertos, comprobado en compilación: si alguien añade un método a
// intake.PipelineStore y no lo trae aquí, esto no compila.
var (
	_ intake.PipelineStore = (*MachineMemory)(nil)
	_ ReanalysisStore      = (*MachineMemory)(nil)
)

// NewMachineMemory (antes `pipeline.NuevoStoreEnMemoria`) construye el doble. `now` es el reloj
// con el que se resuelve el `next_attempt_at <= now()` del reclamo y con el que se fecha lo que
// se escribe; nil usa `time.Now`.
//
// 🔴 EL RELOJ SE INYECTA para que los tests del backoff puedan mirar HACIA DELANTE sin dormir.
// Un test que hiciera `time.Sleep(30 * time.Second)` para ver vencer un backoff no es un test,
// es una espera; y bajar la base del backoff hasta que quepa en un sleep convertiría el test
// en uno de otra política.
func NewMachineMemory(now func() time.Time) *MachineMemory {
	if now == nil {
		now = time.Now
	}
	return &MachineMemory{now: now}
}

// Seed (antes `Sembrar`) mete una fila y devuelve su id. Lo que venga a cero se rellena como
// lo haría la tabla: ID con uno propio ("job-001", "job-002"…), Status con `pending`,
// CreatedAt con el reloj, UpdatedAt con CreatedAt y Artifacts con el objeto vacío. Un
// NextAttemptAt cero se queda en cero, que es «reclamable ya» (el DEFAULT `now()` de la 0078).
// La fila se copia: lo que el llamante haga después con la suya no la toca.
func (s *MachineMemory) Seed(r Row) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	if r.ID == "" {
		r.ID = fmt.Sprintf("job-%03d", s.seq)
	}
	if r.Status == "" {
		r.Status = intake.StatusPending
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.now()
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = r.CreatedAt
	}
	row := cloneRow(r)
	if row.Artifacts == nil {
		row.Artifacts = map[string]json.RawMessage{}
	}
	s.rows = append(s.rows, &row)
	return row.ID
}

// View (antes `Ver`) devuelve una COPIA de la fila, y false si no existe. Copia y no puntero:
// un test que pudiera mutar la fila desde fuera podría fabricar un estado que la máquina nunca
// produce, y entonces estaría afirmando algo sobre un sistema que no existe.
func (s *MachineMemory) View(id string) (Row, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.find(id); r != nil {
		return cloneRow(*r), true
	}
	return Row{}, false
}

// Claims son las veces que se llamó a un reclamo (ClaimNext o ClaimNextIgnoringBackoff). El
// reclamo por evento con el tenant vacío no cuenta: se corta antes de llegar a la cola.
func (s *MachineMemory) Claims() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims
}

// BreakClaim (antes `RomperElClaim`) hace que los reclamos devuelvan `err` a partir de ahora,
// sin tocar ninguna fila. nil lo repara.
func (s *MachineMemory) BreakClaim(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimFailure = err
}

// ClaimNext implementa intake.PipelineStore. Reproduce el predicado del claim real
// —`status = 'pending' AND next_attempt_at <= now()`, `ORDER BY next_attempt_at,
// created_at`—. NO reproduce `FOR UPDATE SKIP LOCKED`: ver la cabecera.
func (s *MachineMemory) ClaimNext(_ context.Context) (intake.ClaimedJob, bool, error) {
	now := s.now()
	return s.claim(func(r *Row) bool {
		return r.Status == intake.StatusPending && !r.NextAttemptAt.After(now)
	})
}

// ClaimNextIgnoringBackoff implementa intake.PipelineStore. Reproduce el predicado del claim
// POR EVENTO: mismo `status = 'pending'`, filtro por tenant, y SIN la mitad temporal.
//
// El `tenantID` vacío no reclama nada, igual que el store real: un flanco sin identidad no
// puede barrer la cola de todo el mundo.
func (s *MachineMemory) ClaimNextIgnoringBackoff(_ context.Context, tenantID string) (intake.ClaimedJob, bool, error) {
	if tenantID == "" {
		return intake.ClaimedJob{}, false, nil
	}
	return s.claim(func(r *Row) bool {
		return r.Status == intake.StatusPending && r.Key.TenantID == tenantID
	})
}

// claim (antes `reclamar`) es el cuerpo COMÚN de los dos reclamos: cuenta la llamada, aplica el
// fallo inyectado, filtra con el predicado que le pasen, ordena por el `ORDER BY` real
// —`next_attempt_at`, y `created_at` de desempate, en los DOS casos— y mueve el primero a
// `processing`.
//
// Que el orden sea el mismo para los dos no es descuido: en el claim por evento
// `next_attempt_at` deja de filtrar pero sigue ordenando, y ordena por el criterio correcto
// (el castigo que vencía antes va primero).
func (s *MachineMemory) claim(eligible func(*Row) bool) (intake.ClaimedJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	if s.claimFailure != nil {
		return intake.ClaimedJob{}, false, s.claimFailure
	}

	candidates := make([]*Row, 0, len(s.rows))
	for _, r := range s.rows {
		if eligible(r) {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return intake.ClaimedJob{}, false, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].NextAttemptAt.Equal(candidates[j].NextAttemptAt) {
			return candidates[i].NextAttemptAt.Before(candidates[j].NextAttemptAt)
		}
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})

	r := candidates[0]
	r.Status = intake.StatusProcessing
	r.UpdatedAt = s.now()
	c := cloneRow(*r)
	return intake.ClaimedJob{
		ID: c.ID, Key: c.Key, Stage: c.Stage, MessageTS: c.MessageTS,
		SourceRefs: c.SourceRefs, SourceText: c.SourceText,
		Artifacts: c.Artifacts, Attempts: c.Attempts, Reanalysis: c.Reanalysis,
	}, true, nil
}

// SaveStage implementa intake.PipelineStore. Valida el artefacto con la MISMA puerta que el
// store real (`intake.Artifact.Validate`) —esa sí es Go y no SQL— y aplica la guarda de no
// retroceder con `intake.StageIndex`.
func (s *MachineMemory) SaveStage(_ context.Context, jobID string, a intake.Artifact) (bool, error) {
	if jobID == "" {
		return false, fmt.Errorf("intake: guardar etapa sin id de job")
	}
	if err := a.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.processing(jobID)
	if r == nil {
		return false, nil
	}
	if r.Stage != "" && intake.StageIndex(r.Stage) > intake.StageIndex(a.Stage) {
		return false, nil
	}
	r.Stage = a.Stage
	r.Artifacts[a.Stage] = slices.Clone(a.Payload)
	r.UpdatedAt = s.now()
	return true, nil
}

// Release implementa intake.PipelineStore: vuelta SIN castigo.
func (s *MachineMemory) Release(_ context.Context, jobID string) (bool, error) {
	if jobID == "" {
		return false, fmt.Errorf("intake: devolver a la cola sin id de job")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.processing(jobID)
	if r == nil {
		return false, nil
	}
	r.Status = intake.StatusPending
	r.UpdatedAt = s.now()
	return true, nil
}

// Retry implementa intake.PipelineStore: vuelta CON castigo. Las tres escrituras van juntas,
// como en `retrySQL`.
func (s *MachineMemory) Retry(_ context.Context, jobID string, next time.Time) (bool, error) {
	if jobID == "" {
		return false, fmt.Errorf("intake: reencolar con backoff sin id de job")
	}
	if next.IsZero() {
		return false, fmt.Errorf("intake: reencolar el job %s sin marca de reintento: el backoff quedaría en el pasado", jobID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.processing(jobID)
	if r == nil {
		return false, nil
	}
	r.Status = intake.StatusPending
	r.Attempts++
	r.NextAttemptAt = next
	r.UpdatedAt = s.now()
	return true, nil
}

// Finish implementa intake.PipelineStore. Vacía el sobre en el mismo paso (INV-13).
func (s *MachineMemory) Finish(_ context.Context, jobID, intakeID string) (bool, error) {
	if jobID == "" {
		return false, fmt.Errorf("intake: terminar sin id de job")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.processing(jobID)
	if r == nil {
		return false, nil
	}
	r.Status = intake.StatusDone
	if intakeID != "" {
		r.IntakeID = intakeID
	}
	r.SourceText = intake.SourceText{}
	r.UpdatedAt = s.now()
	return true, nil
}

// Fail implementa intake.PipelineStore. Vacía el sobre igual que Finish: lo que dispara INV-13
// es TERMINAR, no terminar bien.
func (s *MachineMemory) Fail(_ context.Context, jobID, reason string) (bool, error) {
	if jobID == "" {
		return false, fmt.Errorf("intake: fallar sin id de job")
	}
	if reason == "" {
		return false, fmt.Errorf("intake: fallar el job %s sin causa", jobID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.processing(jobID)
	if r == nil {
		return false, nil
	}
	r.Status = intake.StatusFailed
	r.Error = reason
	r.SourceText = intake.SourceText{}
	r.UpdatedAt = s.now()
	return true, nil
}

// find (antes `buscar`) devuelve la fila. Se llama SIEMPRE con el cerrojo tomado.
func (s *MachineMemory) find(id string) *Row {
	for _, r := range s.rows {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// processing devuelve la fila solo si está en `processing`: es el `WHERE id = $1 AND status =
// 'processing'` de las cinco transiciones, escrito UNA vez. Se llama con el cerrojo tomado.
func (s *MachineMemory) processing(id string) *Row {
	if r := s.find(id); r != nil && r.Status == intake.StatusProcessing {
		return r
	}
	return nil
}

// cloneRow copia la fila con sus referencias, su sobre y sus artefactos.
func cloneRow(r Row) Row {
	r.SourceRefs = slices.Clone(r.SourceRefs)
	r.SourceText.Enc = slices.Clone(r.SourceText.Enc)
	r.SourceText.DEK = slices.Clone(r.SourceText.DEK)
	r.Artifacts = maps.Clone(r.Artifacts)
	return r
}
