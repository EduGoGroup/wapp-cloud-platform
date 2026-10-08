// Porta internal/integrations/crmpush/push.go @ 36d5a04

// Package crmpush arma y ENCOLA el `intake.push` del contrato wapp-crm-v1.
//
// UNA REGLA, DOS PUERTAS (Plan 044 · Ola 4 · Tanda 2). El único productor de
// `intake.push` era el sink de webhooks del motor de flujos, que solo reacciona al
// cierre del carrito: la regla entera —gate del tenant, plantilla del contrato,
// INSERT en webhook_outbox— vivía dentro de un sumidero de eventos y era
// inalcanzable para cualquier llamante que no tuviera un efecto del motor en la
// mano. La segunda puerta son las revisiones que el dueño escribe por HTTP.
//
// Las dos alternativas se descartaron a propósito: fabricar un efecto falso desde
// HTTP (mentirle al motor sobre lo que pasó) y meter el motor de flujos en la cara
// HTTP (arrastrar el runtime entero a una ruta REST). Lo que queda es lo que hace
// este paquete: la regla con firma de PRIMITIVOS, que cada puerta rellena desde lo
// que tiene. Una regla, dos ranuras.
//
// 🔴 INV-02 SIGUE MANDANDO: aquí no hay red. Push evalúa el gate y hace UN INSERT
// (webhook_outbox); quien entrega de verdad es el worker de integrations, que corre
// en otra goroutina y completa buyer_data/variables{}/customer_note justo antes del
// POST (D-042.9/D-042.11). Este paquete NO importa net/http, y esa ausencia es la
// garantía estructural de que ninguna de las dos puertas se cuelga esperando al CRM
// del cliente.
//
// Tampoco importa el paquete `integrations`: Queuer y Gate son puertos estructurales
// con firmas de la biblioteca estándar, y quien los satisface se cablea en el
// arranque.
package crmpush

import (
	"context"
	"encoding/json"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Kind es el `kind` con el que la entrega viaja en webhook_outbox, y Verb el del
// documento. Son la MISMA cadena (`intake.push`) y aun así dos constantes: una la
// lee el worker para enrutar, la otra la lee el puente del cliente dentro del cuerpo.
const (
	Kind = "intake.push"
	Verb = "intake.push"

	// ContractVersion es la versión del contrato wapp-crm-v1 que se emite: la cadena
	// `"1"`. NO se mueve por emitir el estado REAL en vez del literal `confirmed`: el
	// schema congelado ya admite los once estados del ciclo de vida en
	// `lifecycle_status`, así que eso no cambia el contrato — cambia lo que se le
	// cuenta.
	ContractVersion = "1"
)

// Queuer es lo mínimo que este paquete necesita del almacén (ISP, mismo patrón que
// el resto del repo): un solo INSERT en webhook_outbox, que devuelve el id de la
// fila. Lo satisface el almacén Postgres de integrations.
type Queuer interface {
	EnqueueWebhook(ctx context.Context, tenantID, kind string, payload json.RawMessage) (int64, error)
}

// Gate decide si un tenant tiene el puente CRM activo AHORA MISMO (D-042.8:
// entitlement `crm_bridge` + tenant_integrations con events_adapter='webhook' y
// enabled=true). Lo satisface el gate de entitlements de integrations.
//
// Un error se trata como "no" (fail-closed, mismo criterio que el resolvedor de
// entitlements): un puente mal configurado no debe encolar basura.
//
// R-13: es UNA sola evaluación para las dos puertas. Un tenant no puede tener el
// puente abierto para el cierre del carrito y cerrado para las revisiones del dueño:
// las dos pasan por el mismo Pusher y, por tanto, por el mismo Gate.
type Gate interface {
	Enabled(ctx context.Context, tenantID string) (bool, error)
}

// Item es UNA línea de la solicitud TAL COMO viaja por el cable. No es
// intakes.Item ni la línea del carrito: es la forma del CONTRATO, y por eso vive
// aquí y no se importa de ninguno de los dos lados.
//
// Es la MISMA struct en la entrada (Input.Items) y en la salida (Payload.Items) a
// propósito: el contrato define esos cinco campos y no hay traducción que hacer
// entre ellos. Duplicarla en dos tipos idénticos solo añadiría un bucle de copia
// que puede equivocarse de campo.
//
// Los cinco campos viajan SIEMPRE, también vacíos: `customization` sale como `""`,
// no se omite (el schema la declara requerida).
type Item struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Customization string  `json:"customization"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
}

// Input es lo que una puerta tiene que saber para empujar una solicitud al CRM.
// Todo primitivos: quien lo rellena puede venir de un efecto del motor o de una
// fila leída por HTTP, y este paquete no puede notar la diferencia.
type Input struct {
	TenantID  string
	ContactID string // OPACO (ADR-0017/INV-01): jamás un teléfono ni un JID
	IntakeID  string

	// LifecycleStatus es el estado REAL de la solicitud, SIN normalizar: Build lo
	// normaliza (intakes.NormalizeStatus) porque el contrato JAMÁS emite `closed`.
	// Pasarlo crudo es deliberado — la puerta del carrito tiene en la mano la clave
	// legada con la que el carrito acaba de escribir la fila, y obligarla a
	// normalizar antes repartiría la regla en dos sitios.
	LifecycleStatus string

	// RevisionNo es el número que la BASE le asignó a la revisión de este push.
	// AUSENTE ⇒ 0, y eso es deliberado: el cero es el único valor que el schema
	// congelado rechaza (`minimum: 1`), así que un push sin número no puede
	// confundirse con un estado legítimo. Ver Pusher.Push, que lo denuncia.
	RevisionNo int

	Items []Item
	Total float64

	// EventHistoryID es el único campo OPCIONAL del contrato (MD-042.1): vacío ⇒ la
	// clave no aparece en el JSON.
	EventHistoryID string
}

// Payload es el documento que se ENCOLA (webhook_outbox.payload): solo los campos
// baratos y estables del contrato wapp-crm-v1 · intake.push, en este orden de
// claves.
//
// `buyer_data`, `variables` y `customer_note` NO viajan aquí —el worker los
// completa justo antes del POST (D-042.9/D-042.11)— y por eso esta struct NO tiene
// esos campos: añadirlos volvería a violar INV-02 (cripto/consulta en línea con el
// mensaje) y, con customer_note, dejaría PII en claro en una tabla que sobrevive a
// la entrega y que nadie poda (D-046.16/ADR-0043). Con esos tres añadidos, el
// documento valida contra docs/contracts/wapp-crm-v1/intake.push.schema.json; sin
// ellos, no.
type Payload struct {
	ContractVersion string  `json:"contract_version"`
	Verb            string  `json:"verb"`
	Tenant          string  `json:"tenant"`
	Contact         string  `json:"contact"`
	IntakeID        string  `json:"intake_id"`
	LifecycleStatus string  `json:"lifecycle_status"`
	RevisionNo      int     `json:"revision_no"`
	Items           []Item  `json:"items"`
	Total           float64 `json:"total"`
	Timestamp       string  `json:"timestamp"`
	EventHistoryID  string  `json:"event_history_id,omitempty"`
}

// Build arma el documento del contrato (§3, design.md). Es PURA: mismo Input y
// mismo `now`, mismo documento — que es lo que permite probar la forma del contrato
// sin gate, sin store y sin Postgres. Promete:
//
//   - ContractVersion y Verb son las constantes del paquete; Tenant, Contact,
//     IntakeID, Total y EventHistoryID son los del Input, tal cual.
//   - LifecycleStatus es el del LLAMANTE pasado por intakes.NormalizeStatus: `closed`
//     (la clave legada con la que el carrito cierra la fila) sale `confirmed`, porque
//     el contrato JAMÁS emite `closed`; cualquier otro valor sale igual, y el vacío
//     sale VACÍO — no se inventa un estado: el schema lo rechaza de forma visible.
//   - RevisionNo es el del LLAMANTE, sin tocar. Ausente ⇒ 0, que es el único valor
//     que el schema rechaza (`minimum: 1`); nunca se sustituye por un 1.
//   - Items es una COPIA de las líneas del Input, en su orden: tocar la slice del
//     llamante después no cambia el documento. Sin líneas (nil o vacía) es una lista
//     VACÍA, no nil: serializa como `[]` y no como `null`, que el puente rechaza.
//   - Timestamp es `now` en RFC3339, a segundos y con el desplazamiento que traiga
//     `now`: Build no lo convierte a UTC (el reloj del Pusher ya lo entrega en UTC).
//   - El JSON no lleva `event_history_id` mientras esté vacío, y NUNCA lleva
//     `buyer_data`, `variables` ni `customer_note`: los rellena el worker.
//
// 🔴 R-12 · DOS CAMPOS QUE NO PUEDEN SER CONSTANTES, y los dos lo fueron:
//
//   - `revision_no` fue un literal `1` hasta T4.10 (mitad 1). El puente hace UPSERT
//     por (intake_id, revision_no) y descarta como duplicado todo par repetido, así
//     que un número fijo deja al CRM con el PRIMER estado de la solicitud para
//     siempre y sin un solo error en ningún log.
//   - `lifecycle_status` fue un literal `"confirmed"` hasta T4.10 (mitad 2). Para el
//     cierre del carrito acertaba POR CASUALIDAD —ese cierre siempre confirma—; en
//     cuanto una segunda puerta empuja el mismo contrato (el re-empuje de una
//     corrección, que devuelve la solicitud a `pending_approval`) el literal miente
//     igual que mentía el `1`.
//
// Los dos los vigila contrato_test.go sobre el AST, en ESTE paquete y en el motor
// de flujos.
func Build(in Input, now time.Time) Payload {
	panic(pendiente.Implementar("crmpush.Build"))
}

// Pusher evalúa el gate del tenant y encola el documento. Es seguro para uso
// concurrente (no guarda estado propio que cambie tras construirlo).
type Pusher struct{}

// Option configura el Pusher al construirlo (functional option de CONSTRUCCIÓN,
// que es el único patrón de opciones que usa este repo).
type Option func(*Pusher)

// WithClock inyecta el reloj del `timestamp` del contrato, para que sea
// comprobable sin depender del reloj de la máquina. Sin la opción, o con un reloj
// nil, el Pusher usa time.Now().UTC(). Si se pasa más de una vez, manda la última
// que no sea nil.
func WithClock(now func() time.Time) Option {
	panic(pendiente.Implementar("crmpush.WithClock"))
}

// NewPusher construye el encolador y nunca devuelve nil. log/queuer/gate son
// OBLIGATORIOS y su ausencia se comprueba en Push, no aquí: las dos puertas
// construyen su Pusher en el arranque y un nil en cualquiera de ellos tiene que
// apagar el empuje —no matar el proceso ni, peor, colgar el mensaje del cliente.
// Aplica las opciones en el orden dado.
func NewPusher(log logger.Logger, queuer Queuer, gate Gate, opts ...Option) *Pusher {
	panic(pendiente.Implementar("crmpush.NewPusher"))
}

// Result cuenta qué pasó con un empuje.
type Result struct {
	// Enqueued dice si la fila llegó a webhook_outbox. FALSE SIN ERROR es el caso
	// normal y no una avería: el tenant no tiene el puente CRM activo. Las dos
	// puertas tienen que poder distinguirlo de un fallo —el sink lo deja en debug,
	// una ruta HTTP puede querer contárselo al dueño— y por eso no es un error.
	Enqueued bool
	// OutboxID es el id de la fila encolada (0 si no se encoló), para correlacionar
	// en logs con lo que después entrega el worker.
	OutboxID int64
	// Payload es el documento que se armó, encolado o no, para que la puerta pueda
	// loguear/inspeccionar sin volver a construirlo. Es el valor cero cuando no se
	// llegó a armar: Pusher a medias, gate cerrado o gate en error.
	Payload Payload
}

// Push es LA REGLA: gate → plantilla → JSON → INSERT. Devuelve error solo cuando
// algo falló de verdad. Promete, en este orden:
//
//   - Pusher a medias (receptor nil, o construido sin log, sin queuer o sin gate):
//     no-op seguro — Result cero, sin error, sin consultar el gate y sin encolar.
//   - Consulta el gate UNA vez, con el tenant del Input (R-13).
//   - Gate en error (fail-closed): Result cero y el error envuelto como
//     `crmpush: evaluar el gate del puente CRM de <tenant>: %w`. No encola.
//   - Gate cerrado (R-13): Result cero y error NIL — un tenant sin puente CRM activo
//     no es una avería. No encola, y lo deja en Debug con el mensaje
//     `crmpush: tenant sin puente CRM activo, no se encola` y las claves `tenant` e
//     `intake_id`.
//   - Gate abierto: arma el documento con Build y el reloj del Pusher, y lo encola
//     UNA vez con el tenant del Input, el kind Kind y el JSON del documento. Devuelve
//     Enqueued=true, el id que dio el almacén y el documento.
//   - Si el documento no se puede serializar (un total o un precio NaN/infinito):
//     no encola; devuelve el documento y el error
//     `crmpush: serializar intake.push de <intake_id>: %w`.
//   - Si el almacén falla: Enqueued=false, OutboxID=0, el documento, y el error
//     `crmpush: encolar intake.push de <intake_id>: %w`.
//
// Antes de encolar DENUNCIA en nivel Error, sin abortar, los dos campos que una
// puerta puede dejar sin rellenar y que el puente rechazará al validar (R-12):
//
//   - `revision_no` menor que 1: `crmpush: intake.push sin revision_no; se encola
//     con un número que el contrato rechaza en vez de inventar uno (un número FALSO
//     lo aplica el puente sin sospechar)`, con las claves `tenant`, `intake_id` y
//     `revision_no`;
//   - `lifecycle_status` vacío: `crmpush: intake.push sin lifecycle_status; se
//     encola vacío en vez de inventar un estado (el literal `confirmed` que esto
//     sustituye mentía en cuanto la solicitud no venía de un cierre de carrito)`,
//     con las claves `tenant` e `intake_id`.
//
// NO aborta el encolado, y esa es la decisión: dejar la entrega fuera cambiaría un
// defecto VISIBLE —un push que el puente rechaza, con su fila en webhook_outbox y
// su motivo— por la pérdida SILENCIOSA del pedido. Va aquí y no en cada puerta
// porque es parte de la regla: una puerta nueva no puede nacer sin ella.
//
// ⚠️ NO decide qué hacer con el fallo, y eso es a propósito: el sumidero del motor
// lo loguea y devuelve nil —jamás aborta el avance del flujo—, mientras que una
// ruta HTTP puede querer contestar con un código. Tragarse el error aquí le quitaría
// esa decisión a la segunda puerta.
func (p *Pusher) Push(ctx context.Context, in Input) (Result, error) {
	panic(pendiente.Implementar("crmpush.Pusher.Push"))
}
