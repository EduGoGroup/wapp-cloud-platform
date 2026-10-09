// Porta internal/intake/stages/p3.go @ 4cd9cfb

package stages

import (
	"context"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
type P3 struct{}

// NewP3 construye la etapa. Devuelve ErrNotWired (y etapa nil) si `log`, `sel` o `store`
// es nil. 🔴 WithCallTimeout acota CADA llamada del fan-out —también el reintento—, no el
// fan-out entero.
func NewP3(log logger.Logger, sel ProviderSelector, store StageStore, opts ...Option) (*P3, error) {
	panic(pendiente.Implementar("stages.NewP3"))
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
	panic(pendiente.Implementar("stages.P3.Run"))
}
