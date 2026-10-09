// Porta internal/intake/stages/p2.go @ 4cd9cfb

// Package stages son LAS ETAPAS del pipeline de presupuestos (Plan 044 · Ola 2), una
// por fichero: P2 saca las ideas, P3 especifica cada ítem, P4 normaliza cantidades y
// fechas; `match` cruza con el catálogo y `draft` deja el borrador.
//
// # QUÉ ES UNA ETAPA LLM AQUÍ, Y QUÉ NO ES
//
// Una etapa es UNA pasada: pedirle algo al modelo por la vía del tenant, comprobar en
// Go que lo que contestó se sostiene sobre el texto del cliente, y dejar su artefacto
// escrito. El Cloud orquesta la inferencia y valida la salida; quien la sirve —el Edge
// o una API— es *prompt entra → JSON sale* y no interpreta nada (R-11). NO es de este
// paquete:
//
//   - **el bucle del worker** —reclamar, encadenar etapas, terminar el job—;
//   - **la política de reintentos DEL JOB**: en P2 y en P4 se hace UNA llamada y el
//     error sale hacia arriba con su familia intacta (`llm.ErrLLMQuality` envuelto con
//     `%w`), que es lo que el worker necesita para decidir si reintenta o suelta el
//     job. La del ÍTEM sí es de aquí, y solo de P3 (un reintento y luego aislamiento);
//   - **el aforo `K = 1` por Edge**;
//   - **el descifrado del sobre del literal**: la etapa recibe el texto EN CLARO y no
//     conoce el cipher. Quien descifra —y quien decide qué hacer con un job cuyo sobre
//     viene vacío— es el worker.
//
// # 🔴 EL PLAZO POR LLAMADA ES OBLIGATORIO EN PRODUCCIÓN (R-03)
//
// P2, P3 y P4 aceptan Option (WithCallTimeout). Sin ella el adaptador cae a sus 30 s y
// el breaker cuenta como lenta una llamada sana: el default es COMPATIBLE, no seguro.
// `match` y `draft` no llaman al modelo y NO aceptan Option: llevan su propio tipo.
//
// # 🔴 LO QUE NUNCA SALE POR EL LOG
//
// Ni una palabra del cliente. Ni la idea, ni la evidencia, ni el literal, ni la pista
// de entrega. Cuando algo se descarta o se aísla se registra SU POSICIÓN y nada más
// (ADR-0034, INV-6).
//
// # 🔴 EL AUDIO JAMÁS ENTRA EN UN PROMPT
//
// Lo ÚNICO del job que viaja al modelo es el literal en claro (y, en P4, el
// `message_ts`). `ClaimedJob.SourceRefs` —donde vive la referencia de un audio o de
// cualquier adjunto— no se concatena ni se pasa en ninguna entrada del puerto LLM.
//
// # LAS ETAPAS NO LEEN EL RELOJ
//
// La base de fechas es `intake_jobs.message_ts` y solo ella (D-044.9): un job reanudado
// dos días después da el mismo artefacto.
package stages

import (
	"context"
	"errors"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ProviderSelector traduce un tenant en el llm.LLMProvider de SU vía. Lo satisface
// `*llmvia.Selector`.
//
// 🔴 ESTE PAQUETE NO SABE QUÉ VÍA LE TOCÓ AL TENANT (C2 del ADR-0044: «si hay un `if
// via` fuera del adaptador, es defecto»). Aquí se pide un provider y se le llama igual
// venga de donde venga; el techo de tokens de salida lo pone el adaptador por etapa.
//
// `originSessionID` es la sesión de la conversación: es lo que enruta la inferencia al
// Edge que recibió el mensaje, y las etapas la pasan SIEMPRE (`job.Key.SessionID`).
type ProviderSelector interface {
	For(ctx context.Context, tenantID, originSessionID string) (llm.LLMProvider, error)
}

// StageStore es lo ÚNICO que una etapa necesita de la máquina de `intake_jobs`: dejar
// su artefacto. Lo satisface `intake.PipelineStore`.
//
// 🔴 TIENE UN SOLO MÉTODO, Y ES LA MITAD ESTRUCTURAL DE «DESCARTAR UNA IDEA NO TUMBA EL
// JOB»: una etapa no puede fallar un job porque no tiene `Fail` delante. Quien quiera
// matar un job desde una etapa tendrá que ampliar este puerto, y eso se ve en la
// revisión.
//
// `SaveStage` devuelve `(false, nil)` cuando la transición no aplicó (el job ya no
// estaba en `processing`) y error cuando falló la escritura o el artefacto no valida.
type StageStore interface {
	SaveStage(ctx context.Context, jobID string, a intake.Artifact) (bool, error)
}

// El puerto ancho de la máquina satisface el estrecho, comprobado en compilación.
var _ StageStore = intake.PipelineStore(nil)

// ErrNotWired (antes `ErrSinCablear`) se devuelve al construir una etapa LLM a la que
// le falta el log, el selector de vía o el store. Una etapa a medio cablear no se
// construye «por si acaso»: se niega a nacer.
var ErrNotWired = errors.New("stages: la etapa necesita log, selector de vía y store")

// ErrNoLiteral (antes `ErrSinLiteral`) es el job que llega sin texto que analizar: el
// compositor no escribió sobre porque la ventana cerró sin una sola línea del hilo. Las
// tres etapas cortan con él ANTES de pedir el provider y de llamar al modelo: una
// llamada de lote ocupa la plaza única 22–32 s, y un prompt sin texto del cliente solo
// llevaría dentro lo que listamos nosotros (D-044.24).
var ErrNoLiteral = errors.New("stages: el job no trae literal que analizar")

// ErrJobNotProcessing (antes `ErrJobFueraDeProcessing`) es «el artefacto no se
// persistió porque el job ya no estaba en `processing`»: `SaveStage` devolvió `(false,
// nil)`. NO es un fallo de la base ni del modelo, y por eso viaja como centinela: el
// worker lo distingue con errors.Is.
var ErrJobNotProcessing = errors.New("stages: el job ya no estaba en processing; el artefacto no se guardó")

// P2 es la etapa de las IDEAS PRINCIPALES: del literal acumulado de la ventana saca una
// entrada por cada cosa distinta que el cliente pide, más la pista de entrega si la
// dijo. Lo que P2 no vea, P3 no lo especificará nunca.
type P2 struct{}

// NewP2 construye la etapa. Devuelve ErrNotWired (y etapa nil) si `log`, `sel` o `store`
// es nil. Las opciones son las de las etapas LLM (WithCallTimeout): sin ninguna, la
// llamada hereda el ctx del llamante.
func NewP2(log logger.Logger, sel ProviderSelector, store StageStore, opts ...Option) (*P2, error) {
	panic(pendiente.Implementar("stages.NewP2"))
}

// Run ejecuta P2 sobre un job YA RECLAMADO y devuelve el artefacto tal como quedó
// persistido —ya sin lo que el literal no respalda—, que es lo que P3 consume.
// `literal` es el `source_text` EN CLARO.
//
// En este orden:
//
//  1. `literal == ""` ⇒ ErrNoLiteral, sin pedir el provider, sin llamar al modelo y sin
//     persistir nada.
//  2. Pide el provider UNA vez con `(job.Key.TenantID, job.Key.SessionID)`. Si falla:
//     `p2: elegir el proveedor del tenant: %w`.
//  3. Hace UNA llamada `ExtractMainIdeas` con el literal ENTERO como `SourceText` y
//     `Temperature: llm.TemperatureGreedy`. No hay reintento aquí —es del worker—. Con
//     WithCallTimeout, el ctx de ESA llamada lleva el plazo; el plazo acaba donde acaba
//     la llamada y NO alcanza a la persistencia. Si falla:
//     `p2: pedir las ideas principales: %w`, con su familia intacta.
//  4. Lee la salida con `llm.ParseMainIdeas`. Ilegible o sin `version` ⇒
//     `p2: la salida del modelo no es un artefacto P2 legible: %w` (familia
//     `llm.ErrLLMQuality`), y no se persiste nada. El error NO cita la salida cruda.
//  5. **Anclaje** (regla de `evidence`): la idea cuya `evidence` no aparece en el
//     literal se DESCARTA —sin tumbar el job— y deja un `Warn` con su posición
//     (`idea_pos`) y ni una palabra del cliente. La pista de entrega se ancla con la
//     misma regla: si su evidencia no aparece, se cae SOLO la pista. El orden de las
//     ideas vivas es el del modelo.
//  6. Persiste por `SaveStage(job.ID, …)` un artefacto de etapa `intake.StageP2` con las
//     ideas YA ancladas vueltas a serializar —nunca la salida cruda—. Un artefacto con
//     `wants` vacío es válido y se persiste igual. Error ⇒
//     `p2: persistir el artefacto: %w`; `(false, nil)` ⇒ ErrJobNotProcessing.
//  7. Deja el `Info` `p2: ideas principales extraídas y persistidas` con `ideas`,
//     `ideas_descartadas` y `con_pista_de_entrega`.
//
// Con cualquier error el artefacto devuelto es nil.
func (s *P2) Run(ctx context.Context, job intake.ClaimedJob, literal string) (*llm.MainIdeas, error) {
	panic(pendiente.Implementar("stages.P2.Run"))
}
