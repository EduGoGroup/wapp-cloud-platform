// Package crmpush arma y ENCOLA el `intake.push` del contrato wapp-crm-v1.
//
// UNA REGLA, DOS PUERTAS (Plan 044 · Ola 4 · Tanda 2). Hasta esta tarea el único
// productor de `intake.push` era el WebhookSink del motor de flujos, que solo
// reacciona al cierre del carrito: la regla entera —gate del tenant, plantilla del
// contrato, INSERT en webhook_outbox— vivía dentro de un EventSink y era
// inalcanzable para cualquier llamante que no tuviera un modules.Effect en la mano.
// Las puertas nuevas son handlers HTTP de internal/publicapi, fuera del motor.
//
// Las dos alternativas se descartaron a propósito: fabricar un efecto falso desde
// HTTP (mentirle al motor sobre lo que pasó) y meter el motor de flujos en
// publicapi (arrastrar el runtime entero a una ruta REST). Lo que queda es lo que
// hace este paquete: la regla con firma de PRIMITIVOS —sin EffectContext, sin
// modules.Effect—, que cada puerta rellena desde lo que tiene. Es el mismo patrón
// con el que este repo ya resuelve el saneo de la indicación del cliente
// (cart.SanitizeNote, design.md §7.4): una regla, dos ranuras.
//
// 🔴 INV-02 SIGUE MANDANDO: aquí no hay red. Push evalúa el gate y hace UN INSERT
// (webhook_outbox); quien entrega de verdad es el worker de internal/integrations,
// que corre en otra goroutina y completa buyer_data/variables{}/customer_note justo
// antes del POST (D-042.9/D-042.11). Este archivo NO importa net/http, y esa
// ausencia es la garantía estructural de que ninguna de las dos puertas se cuelga
// esperando al CRM del cliente.
package crmpush

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// Kind es el `kind` con el que la entrega viaja en webhook_outbox, y Verb el del
// documento. Son la MISMA cadena y aun así dos constantes: una la lee el worker
// para enrutar, la otra la lee el puente del cliente dentro del cuerpo.
const (
	Kind = "intake.push"
	Verb = "intake.push"

	// ContractVersion es la versión del contrato wapp-crm-v1 que se emite. NO se
	// mueve por esta tarea: el schema congelado ya admite los siete estados del
	// ciclo de vida en `lifecycle_status`, así que emitir el estado REAL en vez del
	// literal `confirmed` no cambia el contrato — cambia lo que se le cuenta.
	ContractVersion = "1"
)

// Queuer es lo mínimo que este paquete necesita del almacén (ISP, mismo patrón que
// el resto del repo): un solo INSERT. Lo satisface *integrations.Postgres.
type Queuer interface {
	EnqueueWebhook(ctx context.Context, tenantID, kind string, payload json.RawMessage) (int64, error)
}

// Gate decide si un tenant tiene el puente CRM activo AHORA MISMO (D-042.8:
// entitlements.Has(tenant,"crm_bridge") + tenant_integrations con
// events_adapter='webhook' y enabled=true). Lo satisface *integrations.EntitlementsGate.
//
// Un error se trata como "no" (fail-closed, mismo criterio que
// entitlements.Resolver): un puente mal configurado no debe encolar basura.
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
	// legada con la que cart.CloseIntake acaba de escribir la fila, y obligarla a
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
// baratos y estables del contrato wapp-crm-v1 · intake.push.
//
// `buyer_data`, `variables` y `customer_note` NO viajan aquí —el worker los
// completa justo antes del POST (D-042.9/D-042.11)— y por eso esta struct NO tiene
// esos campos: añadirlos volvería a violar INV-02 (cripto/consulta en línea con el
// mensaje) y, con customer_note, dejaría PII en claro en una tabla que sobrevive a
// la entrega y que nadie poda (D-046.16/ADR-0043).
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
// mismo `now`, mismo JSON — que es lo que permite probar la forma del contrato sin
// gate, sin store y sin Postgres.
//
// 🔴 DOS CAMPOS QUE NO PUEDEN SER CONSTANTES, y los dos lo fueron:
//
//   - `revision_no` fue un literal `1` hasta T4.10 (mitad 1). El puente hace UPSERT
//     por (intake_id, revision_no) y descarta como duplicado todo par repetido, así
//     que un número fijo deja al CRM con el PRIMER estado de la solicitud para
//     siempre y sin un solo error en ningún log.
//   - `lifecycle_status` fue un literal `"confirmed"` hasta T4.10 (mitad 2). Para el
//     cierre del carrito acertaba POR CASUALIDAD —ese cierre siempre confirma—; en
//     cuanto una segunda puerta empuja el mismo contrato (el re-empuje de una
//     corrección, que devuelve la solicitud a `pending_approval`) el literal miente
//     igual que mentía el `1`. El schema ya admitía los once estados del ciclo de
//     vida: no hay contrato que cambiar, solo un dato que dejar de inventar.
//
// Los dos los vigila contrato_ast_test.go sobre el AST, en ESTE paquete y en
// internal/flujos/runtime.
func Build(in Input, now time.Time) Payload {
	// La slice se COPIA en vez de compartirse: el documento que sale de aquí se
	// serializa y se persiste, y no puede cambiar porque el llamante siga tocando la
	// suya. `make` con largo 0 —y no nil— porque el schema declara `items` requerido
	// y un nil serializa como `null`, que el puente rechaza.
	items := make([]Item, 0, len(in.Items))
	items = append(items, in.Items...)
	return Payload{
		ContractVersion: ContractVersion,
		Verb:            Verb,
		Tenant:          in.TenantID,
		Contact:         in.ContactID,
		IntakeID:        in.IntakeID,
		// El contrato PROHÍBE emitir `closed` (intake.push.md: «El contrato JAMÁS
		// emite closed»), que es la clave legada con la que cart cierra la fila desde
		// el Plan 016. Normalizar aquí —y no en cada puerta— es lo que garantiza que
		// ninguna se lo salte: intakes.NormalizeStatus es el ÚNICO punto del sistema
		// donde se resuelve ese alias.
		LifecycleStatus: intakes.NormalizeStatus(in.LifecycleStatus),
		RevisionNo:      in.RevisionNo,
		Items:           items,
		Total:           in.Total,
		Timestamp:       now.Format(time.RFC3339),
		EventHistoryID:  in.EventHistoryID,
	}
}

// Pusher evalúa el gate del tenant y encola el documento. Es seguro para uso
// concurrente (no guarda estado propio).
type Pusher struct {
	log    logger.Logger
	queuer Queuer
	gate   Gate
	// now existe para que Result.Payload.Timestamp sea comprobable sin depender del
	// reloj de la máquina. En producción es time.Now().UTC(), como lo era en el sink.
	now func() time.Time
}

// Option configura el Pusher al construirlo (functional option de CONSTRUCCIÓN,
// que es el único patrón de opciones que usa este repo).
type Option func(*Pusher)

// WithClock inyecta el reloj del `timestamp` del contrato. Sin él, time.Now().UTC().
func WithClock(now func() time.Time) Option {
	return func(p *Pusher) {
		if now != nil {
			p.now = now
		}
	}
}

// NewPusher construye el encolador. log/queuer/gate son OBLIGATORIOS y su ausencia
// se comprueba en Push, no aquí: las dos puertas construyen su Pusher en el
// arranque y un nil en cualquiera de ellos tiene que apagar el empuje —no matar el
// proceso ni, peor, colgar el mensaje del cliente.
func NewPusher(log logger.Logger, queuer Queuer, gate Gate, opts ...Option) *Pusher {
	p := &Pusher{log: log, queuer: queuer, gate: gate, now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		opt(p)
	}
	return p
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
	// Payload es el documento que se armó, encolado o no. Se devuelve para que la
	// puerta pueda loguear/inspeccionar sin volver a construirlo.
	Payload Payload
}

// Push es LA REGLA: gate → plantilla → JSON → INSERT. Devuelve error solo cuando
// algo falló de verdad; el gate cerrado sale por Result.Enqueued=false, sin error.
//
// ⚠️ NO decide qué hacer con el fallo, y eso es a propósito: el EventSink del motor
// lo loguea y devuelve nil —jamás aborta el avance del flujo—, mientras que una
// ruta HTTP puede querer contestar con un código. Tragarse el error aquí le quitaría
// esa decisión a la segunda puerta.
func (p *Pusher) Push(ctx context.Context, in Input) (Result, error) {
	if p == nil || p.log == nil || p.queuer == nil || p.gate == nil {
		// Pusher a medias (tests que solo ejercitan el camino "no entrega"): no-op
		// seguro, exactamente como lo era el sink construido sin sender/gate.
		return Result{}, nil
	}

	enabled, err := p.gate.Enabled(ctx, in.TenantID)
	if err != nil {
		return Result{}, fmt.Errorf("crmpush: evaluar el gate del puente CRM de %s: %w", in.TenantID, err)
	}
	if !enabled {
		p.log.Debug("crmpush: tenant sin puente CRM activo, no se encola",
			"tenant", in.TenantID, "intake_id", in.IntakeID)
		return Result{}, nil
	}

	payload := Build(in, p.now())
	p.denunciaLoQueElSchemaVaARechazar(payload)

	body, err := json.Marshal(payload)
	if err != nil {
		return Result{Payload: payload}, fmt.Errorf("crmpush: serializar intake.push de %s: %w", in.IntakeID, err)
	}

	id, err := p.queuer.EnqueueWebhook(ctx, in.TenantID, Kind, body)
	if err != nil {
		return Result{Payload: payload}, fmt.Errorf("crmpush: encolar intake.push de %s: %w", in.IntakeID, err)
	}
	return Result{Enqueued: true, OutboxID: id, Payload: payload}, nil
}

// denunciaLoQueElSchemaVaARechazar registra los dos campos que una puerta puede
// dejar sin rellenar y que el puente rechazará al validar.
//
// NO aborta el encolado, y esa es la decisión: dejar la entrega fuera cambiaría un
// defecto VISIBLE —un push que el puente rechaza, con su fila en webhook_outbox y
// su motivo— por la pérdida SILENCIOSA del pedido. El operador tiene que poder
// encontrarlo por el intake_id antes de que el CRM se queje.
//
// Va aquí y no en cada puerta por lo mismo que el resto del fichero: es parte de la
// regla, y una puerta nueva no puede nacer sin ella.
func (p *Pusher) denunciaLoQueElSchemaVaARechazar(payload Payload) {
	if payload.RevisionNo < 1 {
		p.log.Error("crmpush: intake.push sin revision_no; se encola con un número que el contrato "+
			"rechaza en vez de inventar uno (un número FALSO lo aplica el puente sin sospechar)",
			"tenant", payload.Tenant, "intake_id", payload.IntakeID, "revision_no", payload.RevisionNo)
	}
	if payload.LifecycleStatus == "" {
		p.log.Error("crmpush: intake.push sin lifecycle_status; se encola vacío en vez de inventar "+
			"un estado (el literal `confirmed` que esto sustituye mentía en cuanto la solicitud no venía "+
			"de un cierre de carrito)",
			"tenant", payload.Tenant, "intake_id", payload.IntakeID)
	}
}
