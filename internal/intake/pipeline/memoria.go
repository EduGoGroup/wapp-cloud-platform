package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/catalogo"
)

// ════════════════════════════════════════════════════════════════════════════
// EL DOBLE EN MEMORIA DE `PipelineStore` — QUÉ PRUEBA Y QUÉ **NO** PRUEBA
// ════════════════════════════════════════════════════════════════════════════
//
// 🔴 T2.1 DECIDIÓ QUE NO HUBIERA DOBLE DE LA MÁQUINA, Y ESA DECISIÓN SIGUE EN PIE.
// `intake/machine.go` lo dice con todas las letras: los guards de la máquina viven en
// SQL (el `status =` del claim, el `array_position` del avance, el vaciado del sobre en
// la misma sentencia) y un doble en Go los REESCRIBIRÍA a mano, con lo que la suite
// pasaría a probar el doble. Los tests de la máquina son de integración y lo siguen
// siendo: este fichero no toca uno solo de ellos.
//
// LO QUE ESTE DOBLE PRUEBA ES OTRA COSA: LA POLÍTICA DEL WORKER. Cuántos intentos,
// cuánto se empuja la marca, qué desenlace le toca a cada causa, qué etapas se saltan
// al reanudar, que un job envenenado no para a los demás. Nada de eso vive en SQL: vive
// en `pipeline.go`, en Go, y probarlo contra Postgres costaría un contenedor por
// escenario para observar exactamente las mismas variables.
//
// ⚠️ POR ESO ESTE DOBLE **NO SE PUEDE USAR PARA AFIRMAR NADA DE LA MÁQUINA**. Su
// `ClaimNext` no bloquea filas ni tiene `SKIP LOCKED`, su `Retry` no es atómico frente
// a otra goroutine que llame a `Finish` entre dos líneas, y sus guards son `if` que
// alguien puede cambiar sin que se caiga ningún test de Postgres. Si un día un test
// quiere afirmar «doble-claim pierde uno» o «los terminales vacían el sobre», va a
// `machine_integration_test.go`, no aquí.
//
// # POR QUÉ ES CÓDIGO EXPORTADO Y NO UN FAKE EN UN `_test.go`
//
// Porque lo necesitan DOS paquetes de test que no se pueden ver entre sí:
// `pipeline_test` (los escenarios de robustez) y `runtime_test` (el criterio INV-10,
// que además del worker necesita el runtime de flujos entero con sus helpers
// unexported). Duplicarlo sería garantizar que las dos copias divergen. El precedente
// de la casa es `intake.MemoryStore`, exportado por el mismo motivo.
// ════════════════════════════════════════════════════════════════════════════

// Fila es una fila de `intake_jobs` tal como la guarda el doble. Lleva lo que el worker
// lee y lo que un test necesita afirmar, y nada más.
type Fila struct {
	// ID es el identificador del job. Si se siembra vacío, el doble pone uno.
	ID string
	// Key es la tupla de ventana.
	Key intake.WindowKey
	// Status es `pending`, `processing`, `done` o `failed`.
	Status string
	// Stage es la última etapa persistida, "" si ninguna.
	Stage string
	// Attempts son los intentos ya consumidos (columna `attempts` de la 0078).
	Attempts int
	// NextAttemptAt es la marca del backoff. Cero se trata como «reclamable ya»,
	// igual que el DEFAULT `now()` de la 0078.
	NextAttemptAt time.Time
	// CreatedAt es el desempate del ORDER BY del claim.
	CreatedAt time.Time
	// MessageTS es la base de fechas de P4 (D-044.9).
	MessageTS time.Time
	// SourceRefs son los `wa_message_id` de la ventana.
	SourceRefs []string
	// SourceText es el sobre del literal.
	SourceText intake.SourceText
	// Artifacts es lo persistido por etapa.
	Artifacts map[string]json.RawMessage
	// Error es la causa de muerte de un job `failed`.
	Error string
	// IntakeID es el borrador (Ola 3).
	IntakeID string
}

// StoreEnMemoria es el doble. Ver el bloque de cabecera antes de usarlo.
type StoreEnMemoria struct {
	mu    sync.Mutex
	filas []*Fila
	ahora func() time.Time
	seq   int

	// claims cuenta las llamadas a ClaimNext. Es lo que permite afirmar «el worker
	// siguió preguntando» sin medir tiempos.
	claims int
	// falloAlReclamar, si no es nil, es lo que devuelve ClaimNext. Existe para
	// probar el camino de «la base no contesta», que si no sería inalcanzable.
	falloAlReclamar error
}

// NuevoStoreEnMemoria construye el doble. `ahora` es el reloj con el que se resuelve
// el `next_attempt_at <= now()` del claim; nil usa `time.Now`.
//
// 🔴 EL RELOJ SE INYECTA para que los tests del backoff puedan mirar HACIA DELANTE sin
// dormir. Un test que hiciera `time.Sleep(30 * time.Second)` para ver vencer un backoff
// no es un test, es una espera; y bajar la base del backoff hasta que quepa en un sleep
// convertiría el test en uno de otra política.
func NuevoStoreEnMemoria(ahora func() time.Time) *StoreEnMemoria {
	if ahora == nil {
		ahora = time.Now
	}
	return &StoreEnMemoria{ahora: ahora}
}

// Sembrar mete una fila y devuelve su id.
func (s *StoreEnMemoria) Sembrar(f Fila) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	if f.ID == "" {
		f.ID = fmt.Sprintf("job-%03d", s.seq)
	}
	if f.Status == "" {
		f.Status = intake.StatusPending
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = s.ahora()
	}
	if f.Artifacts == nil {
		f.Artifacts = map[string]json.RawMessage{}
	}
	copia := f
	s.filas = append(s.filas, &copia)
	return f.ID
}

// Ver devuelve una COPIA de la fila. Copia y no puntero: un test que pudiera mutar la
// fila desde fuera podría fabricar un estado que la máquina nunca produce, y entonces
// estaría afirmando algo sobre un sistema que no existe.
func (s *StoreEnMemoria) Ver(id string) (Fila, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.filas {
		if f.ID == id {
			return *f, true
		}
	}
	return Fila{}, false
}

// Claims son las veces que se llamó a ClaimNext.
func (s *StoreEnMemoria) Claims() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims
}

// RomperElClaim hace que ClaimNext devuelva `err` a partir de ahora. nil lo repara.
func (s *StoreEnMemoria) RomperElClaim(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.falloAlReclamar = err
}

// ClaimNext implementa intake.PipelineStore. Reproduce el predicado del claim real
// —`status = 'pending' AND next_attempt_at <= now()`, `ORDER BY next_attempt_at,
// created_at`— porque es lo que la POLÍTICA necesita que sea cierto para poder
// probarse. NO reproduce `FOR UPDATE SKIP LOCKED`: ver la cabecera.
func (s *StoreEnMemoria) ClaimNext(_ context.Context) (intake.ClaimedJob, bool, error) {
	ahora := s.ahora()
	return s.reclamar(func(f *Fila) bool {
		return f.Status == intake.StatusPending && !f.NextAttemptAt.After(ahora)
	})
}

// ClaimNextIgnorandoBackoff implementa intake.PipelineStore. Reproduce el predicado
// del claim POR EVENTO: mismo `status = 'pending'`, filtro por tenant, y SIN la mitad
// temporal — que es exactamente la diferencia que el criterio (f) de T2.7 tiene que
// poder ver («un job pending cuyo backoff aún NO venció»).
//
// El `tenantID` vacío no reclama nada, igual que el store real: un flanco sin
// identidad no puede barrer la cola de todo el mundo.
func (s *StoreEnMemoria) ClaimNextIgnorandoBackoff(_ context.Context, tenantID string) (intake.ClaimedJob, bool, error) {
	if tenantID == "" {
		return intake.ClaimedJob{}, false, nil
	}
	return s.reclamar(func(f *Fila) bool {
		return f.Status == intake.StatusPending && f.Key.TenantID == tenantID
	})
}

// reclamar es el cuerpo COMÚN de los dos claims: cuenta la llamada, aplica el fallo
// inyectado, filtra con el predicado que le pasen, ordena por el `ORDER BY` real
// —`next_attempt_at`, y `created_at` de desempate, en los DOS casos— y mueve el
// primero a `processing`.
//
// Que el orden sea el mismo para los dos no es descuido: en el claim por evento
// `next_attempt_at` deja de filtrar pero sigue ordenando, y ordena por el criterio
// correcto (el castigo que vencía antes va primero). Ver claimIgnorandoBackoffSQL.
func (s *StoreEnMemoria) reclamar(elegible func(*Fila) bool) (intake.ClaimedJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	if s.falloAlReclamar != nil {
		return intake.ClaimedJob{}, false, s.falloAlReclamar
	}

	elegibles := make([]*Fila, 0, len(s.filas))
	for _, f := range s.filas {
		if elegible(f) {
			elegibles = append(elegibles, f)
		}
	}
	if len(elegibles) == 0 {
		return intake.ClaimedJob{}, false, nil
	}
	sort.SliceStable(elegibles, func(i, j int) bool {
		if !elegibles[i].NextAttemptAt.Equal(elegibles[j].NextAttemptAt) {
			return elegibles[i].NextAttemptAt.Before(elegibles[j].NextAttemptAt)
		}
		return elegibles[i].CreatedAt.Before(elegibles[j].CreatedAt)
	})

	f := elegibles[0]
	f.Status = intake.StatusProcessing
	arts := make(map[string]json.RawMessage, len(f.Artifacts))
	for k, v := range f.Artifacts {
		arts[k] = v
	}
	return intake.ClaimedJob{
		ID: f.ID, Key: f.Key, Stage: f.Stage, MessageTS: f.MessageTS,
		SourceRefs: f.SourceRefs, SourceText: f.SourceText,
		Artifacts: arts, Attempts: f.Attempts,
	}, true, nil
}

// SaveStage implementa intake.PipelineStore. Valida el artefacto con la MISMA puerta
// que el store real (`intake.Artifact.Validate`) —esa sí es Go y no SQL— y aplica el
// guard de no retroceder con `intake.StageIndex`.
func (s *StoreEnMemoria) SaveStage(_ context.Context, jobID string, a intake.Artifact) (bool, error) {
	if err := a.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.buscar(jobID)
	if f == nil || f.Status != intake.StatusProcessing {
		return false, nil
	}
	if f.Stage != "" && intake.StageIndex(f.Stage) > intake.StageIndex(a.Stage) {
		return false, nil
	}
	f.Stage = a.Stage
	f.Artifacts[a.Stage] = a.Payload
	return true, nil
}

// Release implementa intake.PipelineStore: vuelta SIN castigo.
func (s *StoreEnMemoria) Release(_ context.Context, jobID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.buscar(jobID)
	if f == nil || f.Status != intake.StatusProcessing {
		return false, nil
	}
	f.Status = intake.StatusPending
	return true, nil
}

// Retry implementa intake.PipelineStore: vuelta CON castigo. Las tres escrituras van
// juntas, como en `retrySQL`.
func (s *StoreEnMemoria) Retry(_ context.Context, jobID string, next time.Time) (bool, error) {
	if next.IsZero() {
		return false, fmt.Errorf("intake: reencolar el job %s sin marca de reintento: el backoff quedaría en el pasado", jobID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.buscar(jobID)
	if f == nil || f.Status != intake.StatusProcessing {
		return false, nil
	}
	f.Status = intake.StatusPending
	f.Attempts++
	f.NextAttemptAt = next
	return true, nil
}

// Finish implementa intake.PipelineStore. Vacía el sobre en el mismo paso (INV-13).
func (s *StoreEnMemoria) Finish(_ context.Context, jobID, intakeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.buscar(jobID)
	if f == nil || f.Status != intake.StatusProcessing {
		return false, nil
	}
	f.Status = intake.StatusDone
	if intakeID != "" {
		f.IntakeID = intakeID
	}
	f.SourceText = intake.SourceText{}
	return true, nil
}

// Fail implementa intake.PipelineStore. Vacía el sobre igual que Finish: lo que
// dispara INV-13 es TERMINAR, no terminar bien.
func (s *StoreEnMemoria) Fail(_ context.Context, jobID, reason string) (bool, error) {
	if reason == "" {
		return false, fmt.Errorf("intake: fallar el job %s sin causa", jobID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.buscar(jobID)
	if f == nil || f.Status != intake.StatusProcessing {
		return false, nil
	}
	f.Status = intake.StatusFailed
	f.Error = reason
	f.SourceText = intake.SourceText{}
	return true, nil
}

// buscar devuelve la fila. Se llama SIEMPRE con el candado tomado.
func (s *StoreEnMemoria) buscar(id string) *Fila {
	for _, f := range s.filas {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// El doble satisface el puerto, comprobado en compilación: si alguien añade un método a
// `intake.PipelineStore` y no lo trae aquí, esto no compila.
var _ intake.PipelineStore = (*StoreEnMemoria)(nil)

// ════════════════════════════════════════════════════════════════════════════
// EL DOBLE EN MEMORIA DE `Catalogos` (T3.8)
// ════════════════════════════════════════════════════════════════════════════
//
// # POR QUÉ TAMBIÉN ÉSTE ES CÓDIGO EXPORTADO Y NO UN FAKE EN UN `_test.go`
//
// La primera razón es la de StoreEnMemoria: lo necesitan dos paquetes de test que no
// se pueden ver entre sí (`pipeline` y `flujos/runtime`).
//
// 🔴 LA SEGUNDA ES UNA FRONTERA QUE NO SE PUEDE CRUZAR NI EN UN TEST.
// `TestFrontera_NingunFicheroDeFlujosImportaElIndice` (catalogo/frontera_test.go)
// prohíbe que CUALQUIER fichero bajo `internal/flujos/**` —de producción y de test,
// explícitamente— importe `internal/intake/catalogo`: el índice vive en el worker del
// pipeline y llevarlo al turno conversacional devuelve el parseo del catálogo al
// camino del entrante, que es lo que INV-02/T1.5 prohíbe. Un test de `flujos` que
// quisiera fabricarse su propio `Catalogos` tendría que nombrar `*catalogo.Indice` en
// la firma y cruzaría esa frontera. Con el doble aquí, allí solo se USA un valor cuyo
// tipo no hace falta nombrar — y la frontera sigue entera.

// CatalogoEnMemoria es el doble del puerto Catalogos. Es seguro para uso concurrente.
type CatalogoEnMemoria struct {
	mu       sync.Mutex
	idx      *catalogo.Indice
	err      error
	llamadas int
}

// NuevoCatalogoEnMemoria construye el doble con un artículo por etiqueta.
//
// 🔴 INDEXA CON EL NORMALIZADOR DE PRODUCCIÓN (`textmatch.Normalize`) y no con uno de
// laboratorio: el índice y la cascada del match tienen que opinar lo mismo sobre la ñ
// y sobre los acentos, y un doble con `strings.ToLower` haría que «Café» dejara de
// casar «cafe» solo dentro de los tests.
//
// Sin etiquetas sirve un catálogo VACÍO, que es un estado legítimo —el tenant que aún
// no ha subido el suyo— y con el que todo ítem sale `unmatched`.
func NuevoCatalogoEnMemoria(etiquetas ...string) (*CatalogoEnMemoria, error) {
	articulos := make([]cart.Article, 0, len(etiquetas))
	for i, e := range etiquetas {
		articulos = append(articulos, cart.Article{
			Code:  strconv.Itoa(i + 1),
			SKU:   "ART-" + strconv.Itoa(i+1),
			Label: e,
			Price: 1000,
		})
	}
	idx, err := catalogo.Construir(cart.Catalog{Categories: []cart.Category{
		{Code: "1", Label: "Catálogo", Items: articulos},
	}}, textmatch.Normalize)
	if err != nil {
		return nil, fmt.Errorf("pipeline: catálogo en memoria: %w", err)
	}
	return &CatalogoEnMemoria{idx: idx}, nil
}

// RomperLaLectura hace que Obtener falle a partir de ahora. Es el «tenant_content no
// contesta», que sin esto sería inalcanzable desde un test.
func (c *CatalogoEnMemoria) RomperLaLectura(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// Obtener implementa Catalogos.
func (c *CatalogoEnMemoria) Obtener(_ context.Context, _ string) (*catalogo.Indice, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.llamadas++
	if c.err != nil {
		return nil, c.err
	}
	return c.idx, nil
}

// Lecturas es cuántas veces se pidió el índice. Es lo que permite afirmar el criterio
// (a) de T3.7 desde fuera: UNA lectura por job, no una por ítem.
func (c *CatalogoEnMemoria) Lecturas() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.llamadas
}
