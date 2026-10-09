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
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/evidence"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
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
type P2 struct {
	log    logger.Logger
	sel    ProviderSelector
	store  StageStore
	limits callLimits
}

// NewP2 construye la etapa. Devuelve ErrNotWired (y etapa nil) si `log`, `sel` o `store`
// es nil. Las opciones son las de las etapas LLM (WithCallTimeout): sin ninguna, la
// llamada hereda el ctx del llamante.
func NewP2(log logger.Logger, sel ProviderSelector, store StageStore, opts ...Option) (*P2, error) {
	if log == nil || sel == nil || store == nil {
		return nil, ErrNotWired
	}
	return &P2{log: log, sel: sel, store: store, limits: newCallLimits(opts)}, nil
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
	if literal == "" {
		return nil, ErrNoLiteral
	}

	prov, err := s.sel.For(ctx, job.Key.TenantID, job.Key.SessionID)
	if err != nil {
		return nil, fmt.Errorf("p2: elegir el proveedor del tenant: %w", err)
	}

	// UNA pasada. El reintento por calidad es del worker y el techo de tokens de
	// salida lo pone el adaptador por etapa: aquí no se decide ninguno de los dos.
	raw, err := s.askIdeas(ctx, prov, literal)
	if err != nil {
		return nil, err
	}

	ideas, err := llm.ParseMainIdeas(raw)
	if err != nil {
		// El error NO cita `raw`: la salida del modelo lleva frases del cliente.
		return nil, fmt.Errorf("p2: la salida del modelo no es un artefacto P2 legible: %w", err)
	}

	dropped := s.anchor(ideas, literal, job.ID)

	payload, err := json.Marshal(ideas)
	if err != nil {
		return nil, fmt.Errorf("p2: serializar el artefacto: %w", err)
	}

	// El artefacto lleva `version` porque ParseMainIdeas ya rechazó cualquier otra
	// cosa: `llm.MainIdeas.Version` viene comprobada contra `llm.ArtifactVersion`. NO
	// se vuelve a validar aquí —`SaveStage` lo hace, y es su puerta— porque una
	// segunda red con el mismo síntoma taparía a los tests de conducta de la primera.
	saved, err := s.store.SaveStage(ctx, job.ID, intake.Artifact{
		Stage:   intake.StageP2,
		Payload: payload,
	})
	if err != nil {
		return nil, fmt.Errorf("p2: persistir el artefacto: %w", err)
	}
	if !saved {
		return nil, ErrJobNotProcessing
	}

	s.log.Info("p2: ideas principales extraídas y persistidas",
		"job_id", job.ID, "stage", intake.StageP2,
		"ideas", len(ideas.Wants), "ideas_descartadas", dropped,
		"con_pista_de_entrega", ideas.DeliveryHint != nil)
	return ideas, nil
}

// # EL ANCLAJE, QUE ES EL CORAZÓN DE LA ETAPA
//
// 🔴 DESCARTAR UNA IDEA NO TUMBA EL JOB, y esto no es una tolerancia: es el diseño
// conservador de la ola. Una salida malformada del modelo no puede costarle al cliente
// su solicitud —lo que queda vivo se cotiza, y lo que falte lo verá el dueño en la
// bandeja, que es quien aprueba—. Tumbar el job devolvería el sistema al 7 h 28 min que
// este plan existe para borrar. Un artefacto con `wants` VACÍO es válido y se persiste
// igual («cero resultados válidos tampoco es fatal», design §3.2).
//
// # POR QUÉ SE VUELVE A SERIALIZAR EN VEZ DE GUARDAR LO QUE DIJO EL MODELO
//
// Porque lo que se persiste es lo que P3 va a creerse. Guardar el JSON crudo dejaría
// las ideas inventadas dentro del artefacto —descartadas de boquilla, presentes en la
// base— y el siguiente lector no tendría forma de saber cuáles pasaron el anclaje.

// askIdeas (antes `pedirIdeas`) es LA llamada de P2, acotada por su propio plazo. Está
// extraída de Run —en vez de un `defer cancel()` dentro de Run— porque el ctx acotado
// NO debe seguir vivo mientras se ancla y se persiste: la persistencia es una escritura
// a la base y heredar el deadline del modelo la mataría a mitad, dejando el artefacto
// en el aire. El `defer` de una función corta es lo que hace que el plazo acabe DONDE
// acaba la llamada.
func (s *P2) askIdeas(ctx context.Context, prov llm.LLMProvider, literal string) (json.RawMessage, error) {
	callCtx, cancel := s.limits.bound(ctx)
	defer cancel()
	raw, err := prov.ExtractMainIdeas(callCtx,
		llm.ExtractMainIdeasInput{SourceText: literal},
		llm.Options{Temperature: llm.TemperatureGreedy})
	if err != nil {
		return nil, fmt.Errorf("p2: pedir las ideas principales: %w", err)
	}
	return raw, nil
}

// anchor (antes `anclar`) quita del artefacto todo lo que el literal no respalda y
// devuelve cuántas ideas se cayeron. Modifica `ideas` in situ a propósito: lo que sale
// de aquí es lo único que se persiste y lo único que P3 verá, y dejar dentro las
// inventadas «por si acaso» sería dejar la puerta abierta a que alguien las lea sin
// saber que no valen.
//
// La pista de entrega se ancla con la MISMA regla y con la misma respuesta —si su
// evidencia no aparece, se cae la pista y el resto sigue vivo—. `delivery_hint` trae
// `evidence` por el mismo motivo que las ideas (design §7.1) y una fecha inventada es
// peor que ninguna, porque P4 la convertiría en una fecha absoluta con toda la cara de
// ser cierta.
func (s *P2) anchor(ideas *llm.MainIdeas, literal, jobID string) int {
	norm := evidence.Normalize(literal)

	alive := make([]llm.Want, 0, len(ideas.Wants))
	dropped := 0
	for i := range ideas.Wants {
		if evidence.Contains(norm, ideas.Wants[i].Evidence) {
			alive = append(alive, ideas.Wants[i])
			continue
		}
		dropped++
		// Solo el ÍNDICE: ni la idea ni la evidencia salen por el log.
		s.log.Warn("p2: la evidencia de una idea no aparece en el literal del cliente; la idea se descarta",
			"job_id", jobID, "stage", intake.StageP2, "idea_pos", i)
	}
	ideas.Wants = alive

	if ideas.DeliveryHint != nil && !evidence.Contains(norm, ideas.DeliveryHint.Evidence) {
		ideas.DeliveryHint = nil
		s.log.Warn("p2: la evidencia de la pista de entrega no aparece en el literal del cliente; la pista se descarta",
			"job_id", jobID, "stage", intake.StageP2)
	}
	return dropped
}
