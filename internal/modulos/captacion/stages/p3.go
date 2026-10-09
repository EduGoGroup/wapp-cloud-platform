// Porta internal/intake/stages/p3.go @ 4cd9cfb

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

// Motivos por los que un ítem queda AISLADO: no se pudo especificar, pero no se pierde.
// Son el vocabulario CERRADO del campo `reason` de IsolatedItem; se serializan al
// artefacto y los lee la bandeja del dueño, así que sus VALORES son contrato.
//
// 🔴 LOS TRES ESTÁN AQUÍ JUNTOS A PROPÓSITO, aunque el tercero lo produzca el tope
// (cap.go): son UN vocabulario. Y son tres y no uno porque piden cosas distintas al
// dueño: `quality` y `evidence` le dicen «el modelo se portó mal con este ítem» —si se
// repiten, que mire su Ollama—; `over_limit` le dice «este ítem ni se preguntó: el
// pedido no cabía», que se resuelve hablando con el cliente.
const (
	// ReasonQuality (antes `MotivoCalidad`) es «el modelo contestó dos veces algo
	// ilegible» (REQ-03: una llamada más un reintento a temperatura 0.3).
	ReasonQuality = "quality"
	// ReasonEvidence (antes `MotivoEvidencia`) es «el modelo contestó algo bien
	// formado pero cuya frase de respaldo NO aparece en el texto del cliente».
	ReasonEvidence = "evidence"
	// ReasonOverLimit (antes `MotivoTope`) es «el pedido traía más ítems que
	// MaxItemsPerOrder y a éste no se le llegó a preguntar». NO es un fallo.
	ReasonOverLimit = "over_limit"
)

// IsolatedItem (antes `ItemAislado`) es la MARCA de un ítem que P3 no especificó. El
// pedido del cliente no se pierde: queda anotado para que la bandeja lo enseñe.
//
// 🔴 NO LLEVA UNA PALABRA DEL CLIENTE: `IdeaPos` es un PUNTERO a
// `artifacts.p2.wants[IdeaPos]`, que ya está persistido como parte del job.
type IsolatedItem struct {
	// IdeaPos es la posición de la idea en la lista que P2 dejó viva.
	IdeaPos int `json:"idea_pos"`
	// Reason es ReasonQuality, ReasonEvidence o ReasonOverLimit.
	Reason string `json:"reason"`
}

// P3Artifact (antes `ArtefactoP3`) es lo que la etapa PERSISTE: un SUPERCONJUNTO del
// contrato de design §7.2, `{"version":1,"items":[...]}` más la lista de aislados.
//
// La clave extra `isolated` NO rompe al lector compartido: `llm.ParseItemSpecs` tiene
// que seguir leyendo el artefacto persistido (versión e ítems) sin enterarse de ella.
type P3Artifact struct {
	// Version es `llm.ArtifactVersion`.
	Version int `json:"version"`
	// Items son las especificaciones que sobrevivieron, en el orden de las ideas. Se
	// serializa como `[]` cuando no hay ninguna, nunca como `null`.
	Items []llm.ItemSpec `json:"items"`
	// Isolated son los ítems no especificados, con su motivo: primero los del
	// fan-out, en su orden, y detrás los del tope, en orden ascendente de `IdeaPos`.
	// La clave NO aparece en el JSON cuando no hay ninguno.
	Isolated []IsolatedItem `json:"isolated,omitempty"`
}

// P3 es la etapa de las ESPECIFICACIONES POR ÍTEM: por cada idea que P2 dejó viva hace
// UNA llamada al modelo, con contexto fresco, y le pide que especifique ESE ítem y
// ninguno más.
//
// P3 PROPONE, EL MATCH DECIDE (D-044.14): esta etapa no consulta el catálogo, no busca
// precios y no crea líneas. `addon_candidates` y `customizations` viajan en campos
// SEPARADOS, y los rangos («10 o 12 porciones») se conservan TEXTUALES en `variant`:
// partirlos es de P4 y elegir un número es de nadie.
type P3 struct {
	log    logger.Logger
	sel    ProviderSelector
	store  StageStore
	limits callLimits
}

// NewP3 construye la etapa. Devuelve ErrNotWired (y etapa nil) si `log`, `sel` o `store`
// es nil. 🔴 WithCallTimeout acota CADA llamada del fan-out —también el reintento—, no el
// fan-out entero.
func NewP3(log logger.Logger, sel ProviderSelector, store StageStore, opts ...Option) (*P3, error) {
	if log == nil || sel == nil || store == nil {
		return nil, ErrNotWired
	}
	return &P3{log: log, sel: sel, store: store, limits: newCallLimits(opts)}, nil
}

// Run ejecuta el fan-out de P3 sobre un job YA RECLAMADO y devuelve el artefacto tal
// como quedó persistido, que es lo que P4 consume. `literal` es el `source_text` EN
// CLARO; `ideas` son las que P2 dejó VIVAS, en su orden —el orden al que apunta
// `IsolatedItem.IdeaPos`—.
//
// En este orden:
//
//  1. `literal == ""` ⇒ ErrNoLiteral, sin pedir el provider, sin llamadas y sin
//     persistir.
//  2. **El tope** (cap.go): se atienden las primeras MaxItemsPerOrder ideas; las demás
//     no se preguntan al modelo.
//  3. **Cero ideas no es un fallo**: ni se pide el provider ni se llama al modelo, y el
//     artefacto vacío se persiste igual.
//  4. Con ideas, pide el provider UNA SOLA VEZ para todo el fan-out, con
//     `(job.Key.TenantID, job.Key.SessionID)`. Si falla:
//     `p3: elegir el proveedor del tenant: %w`.
//  5. **Una llamada `ExtractItemSpecs` POR ÍTEM, nunca una por el lote**: cada una
//     lleva el literal ENTERO como `SourceText`, SU idea (`Want.Idea`) y, en el primer
//     intento, `llm.TemperatureGreedy`. Con WithCallTimeout cada llamada lleva su
//     propio plazo, que acaba donde acaba esa llamada.
//  6. Los desenlaces de un ítem:
//     - **sale bien** ⇒ su spec entra en `Items`;
//     - **sale degenerada** (`llm.ErrLLMQuality`: ilegible, o bien formada con CERO
//     especificaciones) ⇒ EXACTAMENTE UN reintento a `llm.TemperatureRetry`; si
//     persiste, el ítem queda aislado con ReasonQuality y los demás siguen;
//     - **devuelve varias especificaciones** ⇒ se conserva la PRIMERA, sin
//     reintento, y un `Warn` cuenta las `descartadas`;
//     - **bien formada pero con una `evidence` que no aparece en el literal** (regla
//     de `evidence`) ⇒ NO se reintenta: el ítem queda aislado con ReasonEvidence;
//     - **falla la infraestructura** (cualquier error que no sea de calidad, en el
//     intento o en el reintento) ⇒ ni se reintenta ni se aísla ni se persiste: el
//     error sale con su familia intacta, envuelto en
//     `p3: especificar el ítem en la posición %d: %w` (con ` (reintento)` tras la
//     posición si fue en el reintento), y no se hacen más llamadas.
//  7. Marca las ideas por encima del tope con ReasonOverLimit, detrás de las del
//     fan-out, y deja el `Warn` `p3: el pedido supera el tope de ítems; …` con `tope`,
//     `ideas`, `atendidas` y `sobre_tope`. Sin sobrantes no hay ni marca ni aviso.
//  8. Persiste por `SaveStage(job.ID, …)` UN artefacto de etapa `intake.StageP3` con el
//     P3Artifact del Cloud —nunca la salida cruda del modelo—. Error ⇒
//     `p3: persistir el artefacto: %w`; `(false, nil)` ⇒ ErrJobNotProcessing.
//  9. Deja el `Info` `p3: especificaciones por ítem extraídas y persistidas` con
//     `ideas`, `items`, `items_aislados` y, APARTE, `items_sobre_tope`.
//
// Ningún ítem aislado tumba el job: con todos aislados Run devuelve nil y el artefacto
// se persiste. Ni las marcas ni el log llevan texto del cliente: posiciones y cuentas.
// Con cualquier error el artefacto devuelto es nil.
func (s *P3) Run(ctx context.Context, job intake.ClaimedJob, literal string, ideas []llm.Want) (*P3Artifact, error) {
	if literal == "" {
		return nil, ErrNoLiteral
	}

	served, leftOver := capIdeas(ideas)

	art := &P3Artifact{Version: llm.ArtifactVersion, Items: make([]llm.ItemSpec, 0, len(served))}
	if len(served) > 0 {
		if err := s.fanOut(ctx, job, literal, served, art); err != nil {
			return nil, err
		}
	}
	s.markOverLimit(art, len(served), leftOver, job.ID)

	if err := s.persist(ctx, job.ID, art); err != nil {
		return nil, err
	}
	// 🔴 `items_sobre_tope` va APARTE de `items_aislados` y no sumado dentro: los dos
	// estados que caben en «aislado» piden cosas OPUESTAS al dueño —mirar su Ollama, o
	// hablar con el cliente—, y un solo número mentiría en la mitad de los casos.
	s.log.Info("p3: especificaciones por ítem extraídas y persistidas",
		"job_id", job.ID, "stage", intake.StageP3,
		"ideas", len(ideas), "items", len(art.Items),
		"items_aislados", len(art.Isolated), "items_sobre_tope", leftOver)
	return art, nil
}

// # POR QUÉ EL REINTENTO VIVE AQUÍ Y EN P2 NO
//
// P2 hace UNA llamada por job: si sale mal, el worker reintenta el job y no se pierde
// nada. P3 hace N: reintentar el JOB por un solo ítem envenenado tiraría las 22–32 s
// que costó cada uno de los otros N−1. El reintento tiene que ser DEL ÍTEM: no es la
// misma política que la del job, que es del worker.
//
// # UNA LLAMADA POR ÍTEM, Y NUNCA UNA POR EL LOTE
//
// Mandar los N ítems en un solo prompt sería más barato en plaza y peor en todo lo
// demás: los modelos chicos funden ítems, se saltan el último y contagian la variante
// de uno al de al lado (la lección medida que el plan hereda: «ni llamada monstruo ni
// exceso de micro-llamadas»).

// fanOut recorre las ideas y llena el artefacto. Devuelve error SOLO cuando el fallo es
// de infraestructura: todo lo demás —salida ilegible, evidencia inventada— se resuelve
// aislando el ítem y siguiendo.
//
// El provider se pide UNA VEZ, fuera del bucle, y no una por ítem: la vía es del tenant
// y de la sesión de origen, no de la idea, y el selector lee la configuración del
// tenant. N llamadas a `For` por pedido serían N lecturas para obtener N veces lo mismo.
//
// # EL ANCLAJE, CON LA MISMA REGLA QUE P2 Y UNA RESPUESTA DISTINTA
//
// La regla es la de `evidence`, la misma y desde el mismo sitio: la frase que el modelo
// dice haber copiado tiene que aparecer en el literal. La RESPUESTA sí cambia: P2
// descarta la idea sin respaldo y sigue, y aquí el ítem se AÍSLA con marca. No es una
// tolerancia distinta, es que la unidad es distinta: en P2 la idea sin respaldo se la
// acababa de inventar el modelo y no hay nada que perder; aquí P2 YA demostró que el
// cliente pidió este ítem —su `want` pasó el anclaje—, así que hacerlo desaparecer sería
// perder una petición real. Aislar es lo conservador; descartar, no.
//
// Y NO se reintenta: una evidencia inventada es una salida bien formada que miente, no
// una salida ilegible, y subir la temperatura no la vuelve honesta. Volver a llamar
// costaría otras 22–32 s de la plaza única para, con suerte, inventar otra frase.
func (s *P3) fanOut(ctx context.Context, job intake.ClaimedJob, literal string, ideas []llm.Want, art *P3Artifact) error {
	prov, err := s.sel.For(ctx, job.Key.TenantID, job.Key.SessionID)
	if err != nil {
		return fmt.Errorf("p3: elegir el proveedor del tenant: %w", err)
	}

	norm := evidence.Normalize(literal)
	for i := range ideas {
		spec, reason, err := s.specify(ctx, prov, literal, ideas[i].Idea, job.ID, i)
		if err != nil {
			return err
		}
		if reason == "" && !evidence.Contains(norm, spec.Evidence) {
			// El modelo devolvió algo bien formado que no sale del texto del
			// cliente: ni se reintenta ni se descarta en silencio. Se aísla.
			// El porqué de las dos cosas, en el docstring de esta función.
			s.log.Warn("p3: la evidencia del ítem no aparece en el literal del cliente; el ítem queda aislado",
				"job_id", job.ID, "stage", intake.StageP3, "idea_pos", i)
			reason = ReasonEvidence
		}
		if reason != "" {
			art.Isolated = append(art.Isolated, IsolatedItem{IdeaPos: i, Reason: reason})
			continue
		}
		art.Items = append(art.Items, *spec)
	}
	return nil
}

// specify (antes `especificar`) resuelve UN ítem. Devuelve, y los tres retornos son
// excluyentes:
//
//   - `(spec, "", nil)` — salió bien;
//   - `(nil, motivo, nil)` — hay que aislarlo, y el job sigue;
//   - `(nil, "", err)` — infraestructura: el job entero se suelta.
//
// # EL REINTENTO ES EXACTAMENTE UNO, Y SOLO POR CALIDAD
//
// Uno porque lo dice REQ-03 («exactamente una vez con temperatura 0.3») y porque cada
// intento cuesta 22–32 s de la plaza única: un segundo reintento por ítem convertiría un
// pedido de 5 ítems en 5 minutos de cola ajena. Y solo por calidad porque un fallo de
// infraestructura (timeout, Edge sin capacidad, socket caído) no se arregla subiendo la
// temperatura: reintentar una caída de red a los dos segundos es gastar la plaza única
// en volver a fallar, y aislar el ítem sería peor todavía —dejaría al cliente sin un
// ítem que el sistema nunca llegó a preguntar—. 🔴 Esa diferencia es la razón de ser de
// `llm.ErrLLMQuality`, y aquí es donde se paga.
//
// La temperatura sube a 0.3 y no a más: lo justo para que el modelo no repita palabra
// por palabra la misma salida degenerada (el número lo fija el paquete compartido, no
// esta etapa).
func (s *P3) specify(ctx context.Context, prov llm.LLMProvider, literal, idea, jobID string, pos int) (*llm.ItemSpec, string, error) {
	spec, err := s.oneCall(ctx, prov, literal, idea, llm.TemperatureGreedy, jobID, pos)
	if err == nil {
		return spec, "", nil
	}
	if !errors.Is(err, llm.ErrLLMQuality) {
		return nil, "", fmt.Errorf("p3: especificar el ítem en la posición %d: %w", pos, err)
	}

	s.log.Warn("p3: la salida del modelo no es legible; se reintenta UNA vez a temperatura de reintento",
		"job_id", jobID, "stage", intake.StageP3, "idea_pos", pos)

	spec, err = s.oneCall(ctx, prov, literal, idea, llm.TemperatureRetry, jobID, pos)
	if err == nil {
		return spec, "", nil
	}
	if !errors.Is(err, llm.ErrLLMQuality) {
		return nil, "", fmt.Errorf("p3: especificar el ítem en la posición %d (reintento): %w", pos, err)
	}

	s.log.Warn("p3: la salida sigue sin ser legible tras el reintento; el ítem queda aislado y el resto del pedido sigue",
		"job_id", jobID, "stage", intake.StageP3, "idea_pos", pos)
	return nil, ReasonQuality, nil
}

// oneCall (antes `unaLlamada`) es UNA pasada por el cable y su lectura. El error sale
// SIN envolver a propósito: quien lo recibe tiene que poder preguntar
// `errors.Is(err, ErrLLMQuality)` y —si es de transporte— sacarle el motivo de
// degradación con `errors.As`. Envolverlo aquí con un prefijo por etapa no rompería
// ninguna de las dos cosas, pero el sitio donde se decide qué es cada error es
// `specify`, y el prefijo lo pone allí una vez.
//
// # DOS SALIDAS BIEN FORMADAS QUE AUN ASÍ SON DEGENERADAS
//
//  1. **Cero ítems.** Se pidió UNO y no vino ninguno: `llm.ParseItemSpecs` lo acepta
//     (su bucle recorre cero elementos) y sería un ítem que desaparece sin marca. Se
//     trata como fallo de calidad ⇒ reintento y, si persiste, aislamiento. Es
//     estrictamente más conservador que aceptarlo.
//  2. **Más de un ítem.** El prompt dice «especifica UN SOLO ítem: ignora los demás,
//     aunque aparezcan en el texto», y el hilo entero va en el prompt como contexto. Un
//     modelo chico que lo ignore devolvería en CADA una de las N llamadas los N ítems
//     ⇒ N² especificaciones y el mismo producto cobrado N veces. Por eso se queda el
//     PRIMERO y los demás se cuentan en el log: la 1:1 entre idea y spec es lo que
//     sostiene el `IdeaPos` de la marca, y el lado seguro de romperla es perder una
//     repetición, nunca duplicar una línea con precio.
func (s *P3) oneCall(ctx context.Context, prov llm.LLMProvider, literal, idea string, temp float64, jobID string, pos int) (*llm.ItemSpec, error) {
	raw, err := s.askSpec(ctx, prov, literal, idea, temp)
	if err != nil {
		return nil, err
	}

	specs, err := llm.ParseItemSpecs(raw)
	if err != nil {
		// 🔴 El error NO cita `raw`: la salida del modelo lleva frases del cliente.
		return nil, fmt.Errorf("la salida del modelo no es un artefacto P3 legible: %w", err)
	}
	if len(specs.Items) == 0 {
		return nil, fmt.Errorf("%w: la llamada del ítem no devolvió ninguna especificación", llm.ErrLLMQuality)
	}
	if len(specs.Items) > 1 {
		s.log.Warn("p3: la llamada de un ítem devolvió varias especificaciones; se conserva la primera",
			"job_id", jobID, "stage", intake.StageP3, "idea_pos", pos, "descartadas", len(specs.Items)-1)
	}
	return &specs.Items[0], nil
}

// askSpec (antes `pedirSpec`) es LA llamada de UN ítem, acotada por SU propio plazo —el
// de una llamada, no el de las N—. Está extraída para que el `defer cancel()` cierre el
// plazo donde acaba la llamada y no arrastre el deadline al parseo, al anclaje ni a la
// persistencia: eso convertiría el plazo por llamada en un plazo por etapa por la
// puerta de atrás.
//
// 🔴 EL PLAZO SE APLICA TAMBIÉN AL REINTENTO por calidad (temperatura 0.3), y tiene
// que ser así: el reintento es otra llamada de lote de 22–32 s, y dejarlo sin acotar
// mandaría al Edge un `timeout_ms` distinto —el default de 30 s— para exactamente el
// mismo trabajo, corrompiendo la señal del breaker justo en el caso raro.
func (s *P3) askSpec(ctx context.Context, prov llm.LLMProvider, literal, idea string, temp float64) (json.RawMessage, error) {
	callCtx, cancel := s.limits.bound(ctx)
	defer cancel()
	return prov.ExtractItemSpecs(callCtx,
		llm.ExtractItemSpecsInput{SourceText: literal, Idea: idea},
		llm.Options{Temperature: temp})
}

// persist (antes `persistir`) serializa el artefacto y lo deja en la máquina de estados.
//
// Se serializa el artefacto DEL CLOUD y no la salida cruda del modelo por el mismo
// motivo que en P2: lo que se guarda es lo que P4 se va a creer, y guardar el crudo
// dejaría dentro las specs inventadas —descartadas de boquilla, presentes en la base—.
//
// No se revalida el `version` aquí: la puerta es `intake.Artifact.Validate`, dentro de
// `SaveStage`. Una segunda red con el mismo síntoma taparía a los tests de conducta de
// la primera.
func (s *P3) persist(ctx context.Context, jobID string, art *P3Artifact) error {
	payload, err := json.Marshal(art)
	if err != nil {
		return fmt.Errorf("p3: serializar el artefacto: %w", err)
	}
	saved, err := s.store.SaveStage(ctx, jobID, intake.Artifact{
		Stage:   intake.StageP3,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("p3: persistir el artefacto: %w", err)
	}
	if !saved {
		return ErrJobNotProcessing
	}
	return nil
}
