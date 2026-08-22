package intake

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Job es una fila de `intake_jobs` tal como la guarda el store en memoria. Lleva
// lo que la Ola 1 escribe y nada más: `artifacts` no aparece porque en esta ola
// NADIE lo escribe (es de la Ola 2), y añadirlo «por completitud» sería inventar
// forma que el código todavía no produce.
//
// 🔧 `SourceText` SÍ aparece desde T1.4: el sobre dejó de ser un hueco teórico el
// día que el compositor del flush empezó a llenarlo.
type Job struct {
	ID         string
	Key        WindowKey
	Status     string
	MessageTS  time.Time
	SourceRefs []string
	// SourceText es el sobre del literal. Vacío mientras la ventana está abierta —
	// que es el estado normal y mayoritario, igual que las tres columnas NULLables
	// de la 0072.
	SourceText SourceText
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Counters es el PRESUPUESTO DE I/O de D-044.26 hecho un número que un test puede
// afirmar. No es telemetría de producción: existe para que el criterio «una
// escritura y cero lecturas en el camino del entrante» se pueda comprobar por
// ejecución en vez de por lectura del código.
//
// La separación importa: `Writes` cuenta las sentencias que MUTAN `intake_jobs`
// (OpenOrAppend + CloseWindow) y `Reads` las que la LEEN (ListAggregating). El
// camino del entrante solo puede tocar el primero, y solo una vez.
type Counters struct {
	// OpenOrAppend cuenta las llamadas a OpenOrAppend: en Postgres es exactamente
	// UNA sentencia por llamada.
	OpenOrAppend int
	// Close cuenta las llamadas a CloseWindow (el barrido y, a través de él, el
	// adelanto por intent). NUNCA debe crecer desde el camino del entrante.
	//
	// 🔑 Y sirve para lo contrario también: afirmar que DOS llamadas LLEGARON al
	// guard. Es lo que usa TestCloseWindow_DosLlamadasSeguidas_LaSegundaNoTocaLaFila
	// para distinguir «el guard de estado descartó la segunda» de «alguien filtró
	// antes y la segunda ni se intentó» — que es exactamente la confusión por la que
	// tres tests del agregador creían estar probando la idempotencia y no la
	// probaban: por el camino de `Sweep`, `ListAggregating` filtra por
	// `status='aggregating'` y `CloseWindow` nunca ve una fila ya cerrada.
	Close int
	// Reads cuenta las llamadas a ListAggregating. 🔴 Si esto crece durante un
	// Observe, D-044.26 está rota.
	Reads int
	// PutSourceText cuenta las escrituras del sobre del literal (T1.4). Es la otra
	// mitad del mismo presupuesto: el literal se compone AL FLUSH, así que este
	// contador NUNCA debe crecer durante un Observe.
	PutSourceText int
}

// MemoryStore implementa JobStore en memoria, con la MISMA semántica que la
// implementación Postgres en las CUATRO cosas que muerden:
//
//  1. como mucho UNA ventana viva por tupla (el índice único PARCIAL de la 0072);
//  2. `MessageTS` se fija SOLO al abrir, nunca al ampliar;
//  3. `CloseWindow` es idempotente por el guard de estado;
//  4. `PutSourceText` escribe en la ÚLTIMA ventana `pending` de la tupla y solo si
//     su sobre estaba vacío (T1.4 — la subconsulta y el guard de putSourceTextSQL).
//
// Si alguna de las cuatro divergiera, los tests dejarían de probar lo que creen que
// prueban — que es exactamente el riesgo de todo doble en memoria.
type MemoryStore struct {
	mu   sync.Mutex
	jobs []*Job
	seq  int
	now  func() time.Time
	cnt  Counters
	// failOpen, cuando no es nil, hace fallar OpenOrAppend. Es el seam para probar
	// que un fallo del sink NO tumba el turno del cliente (INV-10).
	failOpen error
	// failPut, cuando no es nil, hace fallar PutSourceText. Es el seam para probar
	// que un fallo del compositor NO revierte el cierre de la ventana (T1.4): el job
	// se queda en `pending` con el sobre vacío, que es una forma legítima en la 0072.
	failPut error
}

// errIncompleteEnvelope es el equivalente en memoria del rechazo que hace Postgres
// cuando le llega un sobre a medias. Es un error propio y no el mismo objeto que el
// de postgres.go a propósito: el doble replica el COMPORTAMIENTO, no el texto.
var errIncompleteEnvelope = errors.New("intake: sobre del literal incompleto (son las tres o ninguna)")

// NewMemoryStore construye el doble. `now` puede ser nil (usa time.Now).
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{now: now}
}

// FailOpenWith hace que OpenOrAppend devuelva `err` en las siguientes llamadas
// (nil para volver a la normalidad).
func (m *MemoryStore) FailOpenWith(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failOpen = err
}

// FailPutWith hace que PutSourceText devuelva `err` en las siguientes llamadas
// (nil para volver a la normalidad).
func (m *MemoryStore) FailPutWith(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failPut = err
}

// Counters devuelve una copia del presupuesto consumido hasta ahora.
func (m *MemoryStore) Counters() Counters {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cnt
}

// ResetCounters pone el presupuesto a cero. Sirve para medir UN entrante concreto
// después de haber montado el escenario.
func (m *MemoryStore) ResetCounters() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cnt = Counters{}
}

// Jobs devuelve una copia de las filas, en orden de creación.
func (m *MemoryStore) Jobs() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		c := *j
		c.SourceRefs = append([]string(nil), j.SourceRefs...)
		// El sobre también se copia por valor de sus bytes: devolver los slices
		// originales dejaría que un test mutara la fila del store sin pasar por
		// ningún método suyo.
		c.SourceText.Enc = append([]byte(nil), j.SourceText.Enc...)
		c.SourceText.DEK = append([]byte(nil), j.SourceText.DEK...)
		out = append(out, c)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// liveLocked devuelve la ventana VIVA de la tupla, o nil. Es el equivalente en
// memoria del índice único parcial.
func (m *MemoryStore) liveLocked(k WindowKey) *Job {
	for _, j := range m.jobs {
		if j.Key == k && j.Status == StatusAggregating {
			return j
		}
	}
	return nil
}

// OpenOrAppend implementa JobStore.
func (m *MemoryStore) OpenOrAppend(_ context.Context, a Append) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cnt.OpenOrAppend++
	if m.failOpen != nil {
		return m.failOpen
	}
	now := m.now()
	if live := m.liveLocked(a.Key); live != nil {
		// RAMA «YA EXISTÍA»: crecen las refs y la marca de cambio. MessageTS NO se
		// toca — es lo que conserva el ts del PRIMER mensaje (D-044.26).
		live.SourceRefs = append(live.SourceRefs, a.Refs...)
		live.UpdatedAt = now
		return nil
	}
	m.seq++
	m.jobs = append(m.jobs, &Job{
		ID:         formatSeq(m.seq),
		Key:        a.Key,
		Status:     StatusAggregating,
		MessageTS:  a.MessageTS,
		SourceRefs: append([]string(nil), a.Refs...),
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	return nil
}

// CloseWindow implementa JobStore.
//
// 🔴 `liveLocked` AQUÍ ES EL `WHERE status='aggregating'` DEL UPDATE, y es lo único
// que hace idempotente al cierre: sin él, una segunda llamada volvería a marcar
// `pending` una fila ya cerrada, devolvería true y el agregador compondría el
// literal DOS veces sobre la misma ventana. Su test es
// TestCloseWindow_DosLlamadasSeguidas_LaSegundaNoTocaLaFila (aggregator_test.go),
// que llama a esta función DIRECTAMENTE dos veces — por el camino de `Sweep` esta
// rama es inalcanzable, porque `ListAggregating` ya filtró.
func (m *MemoryStore) CloseWindow(_ context.Context, k WindowKey) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cnt.Close++
	live := m.liveLocked(k)
	if live == nil {
		return false, nil // idempotente: ya estaba cerrada (o nunca existió).
	}
	live.Status = StatusPending
	live.UpdatedAt = m.now()
	return true, nil
}

// lastPendingLocked devuelve la ÚLTIMA ventana cerrada de la tupla: la de
// `UpdatedAt` más reciente entre las `pending`. Es el equivalente en memoria de la
// subconsulta de putSourceTextSQL, y replicarla importa — con varias ventanas
// cerradas de la misma tupla, un doble que eligiera la primera probaría lo
// contrario de lo que hace Postgres.
func (m *MemoryStore) lastPendingLocked(k WindowKey) *Job {
	var out *Job
	for _, j := range m.jobs {
		if j.Key != k || j.Status != StatusPending {
			continue
		}
		if out == nil || j.UpdatedAt.After(out.UpdatedAt) {
			out = j
		}
	}
	return out
}

// PutSourceText implementa JobStore con la MISMA semántica que Postgres: la última
// ventana cerrada de la tupla, y solo si su sobre estaba vacío.
func (m *MemoryStore) PutSourceText(_ context.Context, k WindowKey, env SourceText) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cnt.PutSourceText++
	if m.failPut != nil {
		return false, m.failPut
	}
	if !env.Complete() {
		return false, errIncompleteEnvelope
	}
	j := m.lastPendingLocked(k)
	if j == nil || len(j.SourceText.Enc) > 0 {
		return false, nil
	}
	j.SourceText = env
	j.UpdatedAt = m.now()
	return true, nil
}

// ListAggregating implementa JobStore.
func (m *MemoryStore) ListAggregating(_ context.Context, limit int) ([]OpenJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cnt.Reads++
	if limit <= 0 {
		return nil, nil
	}
	out := make([]OpenJob, 0, limit)
	for _, j := range m.jobs {
		if j.Status != StatusAggregating {
			continue
		}
		anchor := j.MessageTS
		if anchor.IsZero() {
			anchor = j.CreatedAt
		}
		out = append(out, OpenJob{ID: j.ID, Key: j.Key, Anchor: anchor})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// formatSeq da ids estables y legibles ("job-1", "job-2"). No imita un UUID a
// propósito: un id de test que PARECE un UUID invita a comparar contra uno real.
func formatSeq(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "job-0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{digits[n%10]}, buf...)
		n /= 10
	}
	return "job-" + string(buf)
}
