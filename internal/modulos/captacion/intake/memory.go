// Porta internal/intake/memory.go @ 8d875ab

package intake

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
	// guard. Es lo que usaba TestCloseWindow_DosLlamadasSeguidas_LaSegundaNoTocaLaFila
	// (test viejo del agregador) para distinguir «el guard de estado descartó la segunda» de «alguien filtró
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
//  2. `MessageTS` se fija SOLO al abrir, nunca al ampliar, y `UpdatedAt` se mueve en
//     LAS DOS ramas (es el `updated_at = now()` explícito del `DO UPDATE`, que desde
//     T1.8-1 es el ancla del SILENCIO — si el doble dejara de moverlo, la ventana
//     híbrida se probaría contra un reloj congelado);
//  3. `CloseWindow` es idempotente por el guard de estado;
//  4. `PutSourceText` escribe en la ÚLTIMA ventana `pending` de la tupla y solo si
//     su sobre estaba vacío (T1.4 — la subconsulta y el guard de putSourceTextSQL).
//
// Si alguna de las cuatro divergiera, los tests dejarían de probar lo que creen que
// prueban — que es exactamente el riesgo de todo doble en memoria.
type MemoryStore struct{}

// NewMemoryStore construye el doble. `now` puede ser nil (usa time.Now).
func NewMemoryStore(now func() time.Time) *MemoryStore {
	panic(pendiente.Implementar("intake.NewMemoryStore"))
}

// FailOpenWith hace que OpenOrAppend devuelva `err` en las siguientes llamadas
// (nil para volver a la normalidad).
func (m *MemoryStore) FailOpenWith(err error) {
	panic(pendiente.Implementar("intake.MemoryStore.FailOpenWith"))
}

// FailPutWith hace que PutSourceText devuelva `err` en las siguientes llamadas
// (nil para volver a la normalidad).
func (m *MemoryStore) FailPutWith(err error) {
	panic(pendiente.Implementar("intake.MemoryStore.FailPutWith"))
}

// Counters devuelve una copia del presupuesto consumido hasta ahora.
func (m *MemoryStore) Counters() Counters {
	panic(pendiente.Implementar("intake.MemoryStore.Counters"))
}

// ResetCounters pone el presupuesto a cero. Sirve para medir UN entrante concreto
// después de haber montado el escenario.
func (m *MemoryStore) ResetCounters() {
	panic(pendiente.Implementar("intake.MemoryStore.ResetCounters"))
}

// Jobs devuelve una copia de las filas, en orden de creación.
//
// ✎ DIVERGENCIA CON EL VIEJO, a propósito: el viejo ordenaba la copia por `ID` como
// CADENA, y con diez filas o más "job-10" salía antes que "job-2" — lo contrario de
// lo que este comentario promete. Las filas ya se guardan en orden de creación, así
// que aquí no se reordena nada.
func (m *MemoryStore) Jobs() []Job {
	panic(pendiente.Implementar("intake.MemoryStore.Jobs"))
}

// OpenOrAppend implementa JobStore.
func (m *MemoryStore) OpenOrAppend(ctx context.Context, a Append) error {
	panic(pendiente.Implementar("intake.MemoryStore.OpenOrAppend"))
}

// CloseWindow implementa JobStore.
//
// 🔴 `liveLocked` AQUÍ ES EL `WHERE status='aggregating'` DEL UPDATE, y es lo único
// que hace idempotente al cierre: sin él, una segunda llamada volvería a marcar
// `pending` una fila ya cerrada, devolvería true y el agregador compondría el
// literal DOS veces sobre la misma ventana. Lo fija el caso
// CloseWindow_LiveWindow_TrueOnceThenFalseAndUntouched de intakehelpertest.ContratoQueue,
// que llama a esta función DIRECTAMENTE dos veces — por el camino de `Sweep` esta
// rama es inalcanzable, porque `ListAggregating` ya filtró.
func (m *MemoryStore) CloseWindow(ctx context.Context, k WindowKey) (bool, error) {
	panic(pendiente.Implementar("intake.MemoryStore.CloseWindow"))
}

// PutSourceText implementa JobStore con la MISMA semántica que Postgres: la última
// ventana cerrada de la tupla, y solo si su sobre estaba vacío.
func (m *MemoryStore) PutSourceText(ctx context.Context, k WindowKey, env SourceText) (bool, error) {
	panic(pendiente.Implementar("intake.MemoryStore.PutSourceText"))
}

// ListAggregating implementa JobStore.
func (m *MemoryStore) ListAggregating(ctx context.Context, limit int) ([]OpenJob, error) {
	panic(pendiente.Implementar("intake.MemoryStore.ListAggregating"))
}
