// Package stages son LAS ETAPAS del pipeline de presupuestos (Plan 044 · Ola 2), una
// por fichero: P2 saca las ideas, P3 especifica cada ítem (T2.3), P4 normaliza
// cantidades y fechas (T2.4). La ubicación la fija design §9 (`internal/intake/` +
// `stages/`).
//
// # QUÉ ES UNA ETAPA AQUÍ, Y QUÉ NO ES
//
// Una etapa es UNA pasada: pedirle algo al modelo por la vía del tenant, comprobar en
// Go que lo que contestó se sostiene sobre el texto del cliente, y dejar su artefacto
// escrito. Nada más. En particular NO es de este paquete, y no se cuele aquí:
//
//   - **el bucle del worker** —reclamar, encadenar etapas, terminar el job— es T2.5;
//   - **la política de reintentos** (el retry único por calidad a temperatura 0.3) es
//     T2.5 también: aquí se hace UNA llamada y el error sale hacia arriba con su
//     familia intacta (`llm.ErrLLMQuality` envuelto con `%w`), que es justo lo que el
//     worker necesita para decidir si reintenta o si suelta el job;
//   - **el tope de ítems** (T2.6) y **el aforo `K = 1` por Edge** (T2.7);
//   - **el descifrado del sobre del literal**: la etapa recibe el texto EN CLARO y no
//     conoce el `FieldCipher`. Quien descifra —y quien decide qué hacer con un job
//     cuyo sobre viene vacío— es el worker, tal como dice `intake.ClaimedJob`.
//
// # 🔴 LO QUE NUNCA SALE POR EL LOG
//
// Ni una palabra del cliente. Ni la idea, ni la evidencia, ni el literal. Cuando una
// idea se descarta se registra SU POSICIÓN y nada más (ADR-0034, INV-6): el número dice
// todo lo que el operador necesita —«el modelo se inventó la idea 2»— y el texto es
// exactamente lo que no puede acabar en un fichero de log.
package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/evidence"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

// ProviderSelector traduce un tenant en el llm.LLMProvider de SU vía. Lo satisface
// *llmvia.Selector.
//
// 🔴 ESTE PAQUETE NO SABE QUÉ VÍA LE TOCÓ AL TENANT, y esa ignorancia es el requisito
// C2 del ADR-0044: «si hay un `if via` fuera del adaptador, es defecto». El único
// switch por vía del repo vive en `llmvia.Selector.For`. Aquí se pide un provider y se
// le llama igual venga de donde venga —también el techo de `max_output_tokens` de P2,
// que lo pone el adaptador por etapa y no el llamante—.
type ProviderSelector interface {
	For(ctx context.Context, tenantID, originSessionID string) (llm.LLMProvider, error)
}

// StageStore es lo ÚNICO que una etapa necesita de la máquina de `intake_jobs`: dejar
// su artefacto. Lo satisface `intake.PipelineStore` —o sea, `*intake.Postgres`—.
//
// 🔴 QUE AQUÍ HAYA UN SOLO MÉTODO ES LA MITAD DEL CRITERIO «DESCARTAR UNA IDEA NO
// TUMBA EL JOB». La otra mitad es la conducta que prueban los tests; ésta es
// estructural: una etapa no puede llamar a `Fail` porque no tiene `Fail` delante. El
// día que alguien quiera matar un job desde una etapa tendrá que ampliar este puerto, y
// eso se ve en la revisión. Lo custodia TestStageStore_NoPuedeTumbarUnJob.
type StageStore interface {
	SaveStage(ctx context.Context, jobID string, a intake.Artifact) (bool, error)
}

// El puerto ancho de la máquina satisface el estrecho, comprobado en compilación.
var _ StageStore = intake.PipelineStore(nil)

// ErrSinCablear se devuelve al construir una etapa a la que le falta una pieza. Una
// etapa a medio cablear no se construye «por si acaso»: se niega a nacer.
var ErrSinCablear = errors.New("stages: la etapa necesita log, selector de vía y store")

// ErrSinLiteral es el job que llega sin texto que analizar.
//
// Es un caso REAL y previsto, no una defensa de adorno: el compositor no escribe sobre
// cuando la ventana cerró sin una sola línea del hilo (media o audio sin texto, o el
// hilo apagado para el tenant), y `intake.ClaimedJob` avisa de que un claim con sobre
// vacío significa eso. La etapa corta ANTES de llamar al modelo, y ese orden importa:
// una llamada de lote ocupa la plaza única 22–32 s y un prompt sin texto del cliente es
// además el accidente que D-044.24 describe —lo único que quedaría dentro serían los
// productos que listamos NOSOTROS—.
var ErrSinLiteral = errors.New("stages: el job no trae literal que analizar")

// ErrJobFueraDeProcessing es «el artefacto no se persistió porque el job ya no estaba
// en `processing`»: otro worker lo terminó, o alguien lo falló mientras corría la
// etapa. NO es un fallo de la base ni del modelo, y por eso viaja como centinela y no
// como error genérico: quien decide qué hacer con un job que se movió bajo los pies es
// el worker (T2.5), y necesita poder distinguirlo con errors.Is.
var ErrJobFueraDeProcessing = errors.New("stages: el job ya no estaba en processing; el artefacto no se guardó")

// P2 es la etapa de las IDEAS PRINCIPALES (Plan 044 · T2.2): del literal acumulado de
// la ventana saca una entrada por cada cosa distinta que el cliente pide, más la pista
// de entrega si la dijo. Es la primera etapa del pipeline y la que decide en qué se
// descompone el pedido: lo que P2 no vea, P3 no lo especificará nunca.
type P2 struct {
	log   logger.Logger
	sel   ProviderSelector
	store StageStore
}

// NewP2 construye la etapa. Devuelve error si le falta cualquiera de las tres piezas.
func NewP2(log logger.Logger, sel ProviderSelector, store StageStore) (*P2, error) {
	if log == nil || sel == nil || store == nil {
		return nil, ErrSinCablear
	}
	return &P2{log: log, sel: sel, store: store}, nil
}

// Run ejecuta P2 sobre un job YA RECLAMADO y devuelve el artefacto tal como quedó
// persistido —ya sin lo que el literal no respalda—, que es lo que P3 consume.
//
// `literal` es el `source_text` EN CLARO: el worker lo descifra del trío del sobre
// (`source_text_enc` / `source_text_dek` / `source_text_kek_id`) y lo pasa aquí. 🔴 No
// existe ninguna columna `source_text` a secas, y esta etapa no habla con la base más
// que por `StageStore`.
//
// # EL ANCLAJE, QUE ES EL CORAZÓN DE LA TAREA
//
// El modelo devuelve, por contrato, una `evidence` por idea: la frase del texto del
// cliente de la que sale esa idea. Se comprueba en Go que esa frase EXISTE en el
// literal (regla en `internal/evidence`). La idea cuya evidencia no aparece se
// DESCARTA del artefacto y se deja constancia en el log.
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
func (s *P2) Run(ctx context.Context, job intake.ClaimedJob, literal string) (*llm.MainIdeas, error) {
	if literal == "" {
		return nil, ErrSinLiteral
	}

	prov, err := s.sel.For(ctx, job.Key.TenantID, job.Key.SessionID)
	if err != nil {
		return nil, fmt.Errorf("p2: elegir el proveedor del tenant: %w", err)
	}

	// UNA pasada. El reintento por calidad es del worker (T2.5) y el techo de tokens
	// de salida lo pone el adaptador por etapa: aquí no se decide ninguno de los dos.
	raw, err := prov.ExtractMainIdeas(ctx,
		llm.ExtractMainIdeasInput{SourceText: literal},
		llm.Options{Temperature: llm.TemperatureGreedy})
	if err != nil {
		return nil, fmt.Errorf("p2: pedir las ideas principales: %w", err)
	}

	ideas, err := llm.ParseMainIdeas(raw)
	if err != nil {
		// El error NO cita `raw`: la salida del modelo lleva frases del cliente.
		return nil, fmt.Errorf("p2: la salida del modelo no es un artefacto P2 legible: %w", err)
	}

	descartadas := s.anclar(ideas, literal, job.ID)

	payload, err := json.Marshal(ideas)
	if err != nil {
		return nil, fmt.Errorf("p2: serializar el artefacto: %w", err)
	}

	// El artefacto lleva `version` porque ParseMainIdeas ya rechazó cualquier otra
	// cosa: `llm.MainIdeas.Version` viene comprobada contra `llm.ArtifactVersion`. NO
	// se vuelve a validar aquí —`SaveStage` lo hace, y es su puerta— porque una
	// segunda red con el mismo síntoma taparía a los tests de conducta de la primera.
	guardado, err := s.store.SaveStage(ctx, job.ID, intake.Artifact{
		Stage:   intake.StageP2,
		Payload: payload,
	})
	if err != nil {
		return nil, fmt.Errorf("p2: persistir el artefacto: %w", err)
	}
	if !guardado {
		return nil, ErrJobFueraDeProcessing
	}

	s.log.Info("p2: ideas principales extraídas y persistidas",
		"job_id", job.ID, "stage", intake.StageP2,
		"ideas", len(ideas.Wants), "ideas_descartadas", descartadas,
		"con_pista_de_entrega", ideas.DeliveryHint != nil)
	return ideas, nil
}

// anclar quita del artefacto todo lo que el literal no respalda y devuelve cuántas
// ideas se cayeron. Modifica `ideas` in situ a propósito: lo que sale de aquí es lo
// único que se persiste y lo único que P3 verá, y dejar dentro las inventadas «por si
// acaso» sería dejar la puerta abierta a que alguien las lea sin saber que no valen.
//
// La pista de entrega se ancla con la MISMA regla y con la misma respuesta —si su
// evidencia no aparece, se cae la pista y el resto sigue vivo—. Es una decisión de esta
// tarea: el enunciado de T2.2 solo habla de las ideas, pero `delivery_hint` trae
// `evidence` por el mismo motivo (design §7.1) y una fecha inventada es peor que
// ninguna, porque P4 la convertiría en una fecha absoluta con toda la cara de ser
// cierta.
func (s *P2) anclar(ideas *llm.MainIdeas, literal, jobID string) int {
	norm := evidence.Normalize(literal)

	vivas := make([]llm.Want, 0, len(ideas.Wants))
	descartadas := 0
	for i := range ideas.Wants {
		if evidence.Contains(norm, ideas.Wants[i].Evidence) {
			vivas = append(vivas, ideas.Wants[i])
			continue
		}
		descartadas++
		// Solo el ÍNDICE: ni la idea ni la evidencia salen por el log.
		s.log.Warn("p2: la evidencia de una idea no aparece en el literal del cliente; la idea se descarta",
			"job_id", jobID, "stage", intake.StageP2, "idea_pos", i)
	}
	ideas.Wants = vivas

	if ideas.DeliveryHint != nil && !evidence.Contains(norm, ideas.DeliveryHint.Evidence) {
		ideas.DeliveryHint = nil
		s.log.Warn("p2: la evidencia de la pista de entrega no aparece en el literal del cliente; la pista se descarta",
			"job_id", jobID, "stage", intake.StageP2)
	}
	return descartadas
}
