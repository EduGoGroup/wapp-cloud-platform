// Porta internal/intake/pipeline/memoria.go @ 8d875ab (su mitad StoreEnMemoria; D-F7-5)

package intakehelpertest

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
type MachineMemory struct{}

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
	panic(pendiente.Implementar("intakehelpertest.NewMachineMemory"))
}

// Seed (antes `Sembrar`) mete una fila y devuelve su id. Lo que venga a cero se rellena como
// lo haría la tabla: ID con uno propio ("job-001", "job-002"…), Status con `pending`,
// CreatedAt con el reloj, UpdatedAt con CreatedAt y Artifacts con el objeto vacío. Un
// NextAttemptAt cero se queda en cero, que es «reclamable ya» (el DEFAULT `now()` de la 0078).
// La fila se copia: lo que el llamante haga después con la suya no la toca.
func (s *MachineMemory) Seed(r Row) string {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.Seed"))
}

// View (antes `Ver`) devuelve una COPIA de la fila, y false si no existe. Copia y no puntero:
// un test que pudiera mutar la fila desde fuera podría fabricar un estado que la máquina nunca
// produce, y entonces estaría afirmando algo sobre un sistema que no existe.
func (s *MachineMemory) View(id string) (Row, bool) {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.View"))
}

// Claims son las veces que se llamó a un reclamo (ClaimNext o ClaimNextIgnoringBackoff). El
// reclamo por evento con el tenant vacío no cuenta: se corta antes de llegar a la cola.
func (s *MachineMemory) Claims() int {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.Claims"))
}

// BreakClaim (antes `RomperElClaim`) hace que los reclamos devuelvan `err` a partir de ahora,
// sin tocar ninguna fila. nil lo repara.
func (s *MachineMemory) BreakClaim(err error) {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.BreakClaim"))
}

// ClaimNext implementa intake.PipelineStore. Reproduce el predicado del claim real
// —`status = 'pending' AND next_attempt_at <= now()`, `ORDER BY next_attempt_at,
// created_at`—. NO reproduce `FOR UPDATE SKIP LOCKED`: ver la cabecera.
func (s *MachineMemory) ClaimNext(ctx context.Context) (intake.ClaimedJob, bool, error) {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.ClaimNext"))
}

// ClaimNextIgnoringBackoff implementa intake.PipelineStore. Reproduce el predicado del claim
// POR EVENTO: mismo `status = 'pending'`, filtro por tenant, y SIN la mitad temporal.
//
// El `tenantID` vacío no reclama nada, igual que el store real: un flanco sin identidad no
// puede barrer la cola de todo el mundo.
func (s *MachineMemory) ClaimNextIgnoringBackoff(ctx context.Context, tenantID string) (intake.ClaimedJob, bool, error) {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.ClaimNextIgnoringBackoff"))
}

// SaveStage implementa intake.PipelineStore. Valida el artefacto con la MISMA puerta que el
// store real (`intake.Artifact.Validate`) —esa sí es Go y no SQL— y aplica la guarda de no
// retroceder con `intake.StageIndex`.
func (s *MachineMemory) SaveStage(ctx context.Context, jobID string, a intake.Artifact) (bool, error) {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.SaveStage"))
}

// Release implementa intake.PipelineStore: vuelta SIN castigo.
func (s *MachineMemory) Release(ctx context.Context, jobID string) (bool, error) {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.Release"))
}

// Retry implementa intake.PipelineStore: vuelta CON castigo. Las tres escrituras van juntas,
// como en `retrySQL`.
func (s *MachineMemory) Retry(ctx context.Context, jobID string, next time.Time) (bool, error) {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.Retry"))
}

// Finish implementa intake.PipelineStore. Vacía el sobre en el mismo paso (INV-13).
func (s *MachineMemory) Finish(ctx context.Context, jobID, intakeID string) (bool, error) {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.Finish"))
}

// Fail implementa intake.PipelineStore. Vacía el sobre igual que Finish: lo que dispara INV-13
// es TERMINAR, no terminar bien.
func (s *MachineMemory) Fail(ctx context.Context, jobID, reason string) (bool, error) {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.Fail"))
}
