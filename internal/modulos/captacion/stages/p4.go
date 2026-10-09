// Porta internal/intake/stages/p4.go @ 4cd9cfb

package stages

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// DefaultZone (antes `ZonaPorDefecto`) es la zona que gobierna el cálculo de fechas
// mientras el producto no elija una. Es UTC A PROPÓSITO: no es la zona de ningún
// negocio, es la ausencia de decisión (DEUDA-044.11: no hay zona horaria por tenant en
// ninguna parte), y es la única que hoy coincide con la fecha de referencia que imprime
// el prompt compartido, que fuerza UTC.
//
// Se exporta para que el llamante tenga que ESCRIBIRLA: un default implícito dentro del
// constructor haría invisible la decisión que falta.
//
// 🔴 EL HUECO, QUE QUEDA FIJADO: con UTC, un mensaje del lunes a las 22:00 en UTC−3 se
// fecha con el martes.
var DefaultZone = time.UTC

// ErrNoTimeZone (antes `ErrSinZonaHoraria`) es «se intentó construir P4 sin zona». No
// cae en ErrNotWired porque no es una pieza que falte por descuido: es la decisión de
// producto que aún no existe, y quien construye la etapa tiene que elegir
// explícitamente —hoy, DefaultZone— en vez de heredar un cero.
//
// El texto es el del paquete viejo, literal: nombra `stages.ZonaPorDefecto`, que hoy es
// DefaultZone.
var ErrNoTimeZone = errors.New("stages: P4 necesita la zona horaria que gobierna el cálculo de fechas (hoy, stages.ZonaPorDefecto)")

// P4 es la etapa de la NORMALIZACIÓN: coge las especificaciones que dejó P3 y las
// convierte en cantidades comparables —unidades, paquetes y rangos— y en UNA FECHA
// ABSOLUTA de entrega. Es la última etapa antes de que el match toque el catálogo.
//
// El modelo aporta EXACTAMENTE cuatro campos por ítem: `qty`, `range`, `unit_kind` y
// `package_size`. Todo lo demás —producto, añadidos candidatos, personalizaciones,
// notas— se copia de la spec de P3 sin pasar por el modelo, y la FECHA la calcula Go
// (ResolveDate): ni una suma de días la hace el modelo.
type P4 struct{}

// NewP4 construye la etapa. Devuelve ErrNotWired si `log`, `sel` o `store` es nil y, con
// esas tres piezas puestas, ErrNoTimeZone si `zone` es nil; en los dos casos la etapa
// devuelta es nil. La zona es OBLIGATORIA y posicional —hoy el llamante pasa
// DefaultZone—; el plazo por llamada llega por opción (WithCallTimeout).
func NewP4(log logger.Logger, sel ProviderSelector, store StageStore, zone *time.Location, opts ...Option) (*P4, error) {
	panic(pendiente.Implementar("stages.NewP4"))
}

// Run normaliza un job YA RECLAMADO y devuelve el artefacto tal como quedó persistido,
// que es lo que consumen el match y el borrador. `literal` es el `source_text` EN
// CLARO; `items` son las specs que P3 dejó vivas, en su orden; `delivery` es la pista de
// entrega que P2 etiquetó, o nil si el cliente no dijo cuándo.
//
// En este orden:
//
//  1. `literal == ""` ⇒ ErrNoLiteral, sin pedir el provider, sin llamar al modelo y sin
//     persistir.
//  2. **La fecha se calcula EN GO, antes de llamar al modelo y aunque no haya ítems**:
//     `ResolveDate(delivery.Text, job.MessageTS en la zona de la etapa)`. Si resuelve,
//     `delivery_date` es esa fecha (`AAAA-MM-DD`) y `delivery_date_basis` es
//     `message_ts=` más el día del mensaje EN LA ZONA de la etapa. No se lee el reloj:
//     dos pasadas del mismo job dan el mismo artefacto byte a byte. Tres desenlaces
//     dejan el artefacto SIN fecha ni base, y ninguno es un fallo: el job no trae
//     `message_ts`, no hay pista, o la pista no se reconoce. Los avisos no llevan la
//     pista, que son palabras del cliente.
//  3. **Cero ítems no es un fallo**: ni se pide el provider ni se llama al modelo, y el
//     artefacto (con su fecha, si la hay) se persiste igual.
//  4. Con ítems, pide el provider UNA vez con `(job.Key.TenantID, job.Key.SessionID)`
//     —`p4: elegir el proveedor del tenant: %w`— y hace UNA llamada
//     `NormalizeQuantities` con el literal ENTERO, las specs y `job.MessageTS`, a
//     `llm.TemperatureGreedy`. No hay reintento aquí: es del worker. Con
//     WithCallTimeout, el plazo acaba donde acaba la llamada. Si falla:
//     `p4: pedir la normalización de cantidades: %w`, con su familia intacta.
//  5. Lee la salida con `llm.ParseQuantities`. Ilegible —o con un `qty` omitido, que el
//     parser rechaza— ⇒ `p4: la salida del modelo no es un artefacto P4 legible: %w`
//     (familia `llm.ErrLLMQuality`) y no se persiste nada: P4 NO maquilla un `qty` 0
//     como 1.
//  6. **Funde POR POSICIÓN, y la cuenta la manda P3**: sale un ítem por cada spec, ni
//     uno más ni uno menos. El ítem que el modelo no devolvió sale con la normalización
//     neutra —los datos de P3 y `qty` 1, sin rango ni paquete—; los que devuelva de más
//     se descartan con un `Warn` (`los sobrantes se descartan`).
//  7. De cada ítem del modelo se copian `qty`, `range`, `unit_kind` y `package_size`;
//     el producto y lo demás son SIEMPRE los de P3. Los rangos NO se colapsan. La
//     `evidence` del modelo sustituye a la de P3 solo si aparece en el literal (regla
//     de `evidence`); si no, se conserva la de P3 y el ítem sigue vivo.
//  8. **«Paquete de N» JAMÁS es `qty` N**: si la evidencia o las notas del ítem dicen
//     «paquete(s) de N» (hasta 40 caracteres sin dígitos entre «de» y el número) y el
//     `qty` resultante es EXACTAMENTE ese N, con N > 1, se corrige a `qty` 1,
//     `unit_kind` `package` y `package_size` N —lleve o no la etiqueta de paquete—.
//  9. La fecha que proponga el modelo NO se usa: manda la de Go, y si no coinciden
//     queda un `Warn` (`no coincide con la que calculó Go`).
//  10. Persiste por `SaveStage(job.ID, …)` UN artefacto de etapa `intake.StageP4`, que
//     `llm.ParseQuantities` tiene que poder volver a leer EN TODOS los caminos (I-CP-1
//     visto desde la etapa: lo que P4 escribe nunca trae un valor que su propio
//     validador rechace). Error ⇒ `p4: persistir el artefacto: %w`; `(false, nil)` ⇒
//     ErrJobNotProcessing.
//  11. Deja el `Info` `p4: cantidades y fecha normalizadas y persistidas` con `items` y
//     `con_fecha`.
//
// Ningún aviso lleva texto del cliente: ni la pista, ni una evidencia, ni un producto.
// Con cualquier error el artefacto devuelto es nil.
func (s *P4) Run(ctx context.Context, job intake.ClaimedJob, literal string, items []llm.ItemSpec, delivery *llm.Hint) (*llm.Quantities, error) {
	panic(pendiente.Implementar("stages.P4.Run"))
}
