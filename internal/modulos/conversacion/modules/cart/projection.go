// Porta internal/flujos/modules/cart/projection.go @ 9d5a4b6

// projection.go es el PROYECTOR del carrito: el adaptador IMPURO que materializa en
// public.intakes / intake_items / intake_revisions / intake_buyer_data los efectos
// que el Module PURO solo declara (Plan 027 · Ola 3 · T8, cierra H10). El PersistSink
// lo ejecuta genéricamente tras escribir flow_events, sin conocer los efectos de
// ningún módulo.
//
// Es dueño del ciclo de vida que el carrito le da a una solicitud, con los estados
// tal como se guardan: "open" (al primer item_added), "closed" (al confirmar; es la
// clave LEGADA que el contrato del CRM normaliza a `confirmed`), "cancelled" y
// "expired" (histórico). Nada vence por tiempo (D-041.16, T4.7): el proyector no
// tiene reloj y no escribe expires_at.
//
// SUS ESCRITURAS VAN ENCADENADAS Y SIN TRANSACCIÓN COMÚN —el cierre, la revisión y la
// línea de envío son tres escrituras de dos dominios—, y es una decisión consciente
// con su costo dicho en Project. Lo que sí es atómico es cada una por separado.

package cart

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// Estados del ciclo de vida de una solicitud (public.intakes). Viven aquí porque la
// proyección del carrito es su dueña (design.md §3.4).
const (
	intakeStatusOpen      = "open"
	intakeStatusClosed    = "closed"
	intakeStatusCancelled = "cancelled"
	intakeStatusExpired   = "expired"
)

// ProjectionStore es lo que el proyector del carrito necesita del almacén para
// materializar sus efectos en intakes/intake_items. Interfaz mínima (ISP) que
// satisfacen *store.PostgresRepository y *store.MemoryRepository.
//
// Ya NO incluye GetTenantSettings: lo único que el proyector leía de la config del
// tenant era order_ttl_seconds, para fechar el vencimiento de la solicitud, y ese
// reloj quedó derogado (D-041.16, T4.7).
type ProjectionStore interface {
	GetOpenIntake(ctx context.Context, tenantID, contactID string) (store.Intake, bool, error)
	// GetIntakeByEvent es la SEGUNDA pregunta del proyector al asegurar la solicitud
	// (D-044.46): la que ya cuelga de ESTE evento, esté en el estado que esté. No
	// sustituye a GetOpenIntake —la identidad de negocio sigue mandando en el camino
	// normal—: cubre el hueco por el que el carrito no veía el borrador que la etapa
	// `draft` del pipeline había dejado en `pending_approval` sobre el mismo evento.
	GetIntakeByEvent(ctx context.Context, tenantID, eventID string) (store.Intake, bool, error)
	UpsertIntake(ctx context.Context, o store.Intake) error
	ReplaceIntakeItems(ctx context.Context, intakeID string, items []store.IntakeItem) error
	MarkIntakeStatus(ctx context.Context, intakeID, status string, total float64) error
	CloseIntake(ctx context.Context, in store.IntakeClose) (string, error)
}

// RevisionWriter es lo ÚNICO que el proyector necesita del dominio de solicitudes:
// dejar constancia de la versión que acaba de cerrar. Lo satisfacen
// *intakes.Postgres y *intakes.MemoryStore.
//
// Se declara aquí, del lado del consumidor (idioma Go), en vez de importar el
// puerto entero: el carrito no lista solicitudes, no las transiciona y no debe
// poder hacerlo.
type RevisionWriter interface {
	InsertRevision(ctx context.Context, rev intakes.Revision) (intakes.Revision, error)
}

// ShippingEnsurer es lo ÚNICO que el proyector necesita para la línea estándar de
// envío (D-041.11): pedir que esté. Lo satisfacen *intakes.Postgres,
// *intakes.MemoryStore y *intakes.Service.
//
// El carrito NO decide el precio del envío ni conoce las zonas del tenant: eso lo
// resuelve el dominio de solicitudes, que es de quien es la línea. Aquí solo se
// dice CUÁNDO —al cerrar— y con qué política.
type ShippingEnsurer interface {
	EnsureShippingLine(ctx context.Context, tenantID, intakeID string, policy intakes.ShippingPolicy) error
}

// BuyerDataWriter es lo ÚNICO que el proyector necesita para los datos del
// comprador (T4.5, D-041.13): guardar UN campo. Lo satisfacen
// *intakes.PostgresBuyerData y *intakes.MemoryStore.
//
// El puerto es de una sola función y eso es deliberado: el carrito ESCRIBE datos
// personales y no puede leerlos. Un puerto con `Get` le daría al módulo la
// capacidad de sacar del cifrado lo que acaba de meter, y el descifrado de esta
// tabla está custodiado (ver intakes/buyerdata.go).
//
// El CIFRADO no está aquí ni en el módulo: está detrás de este puerto. El carrito
// no conoce KEKs, DEKs ni envelopes — igual que no conoce el SQL del cierre.
type BuyerDataWriter interface {
	PutBuyerField(ctx context.Context, intakeID, key, value string) error
}

// Projector implementa modules.Projector para los efectos del carrito (Plan 027 ·
// Ola 3 · T8, cierra H10). Es un adaptador IMPURO; el Module (Render/Step) sigue
// puro. Sin reloj a propósito (T4.7): el único uso que tenía era fechar expires_at,
// y nada vence por tiempo (D-041.16).
type Projector struct {
	store     ProjectionStore
	revisions RevisionWriter
	shipping  ShippingEnsurer
	buyer     BuyerDataWriter
}

// NewProjector construye el proyector del carrito sobre el almacén de solicitudes,
// el escritor de revisiones, el garante de la línea de envío y el escritor de datos
// del comprador.
//
// Los cuatro son parámetros OBLIGATORIOS y no opciones con cero-valor a propósito:
// un proyector sin escritor de revisiones cerraría carritos sin dejar rastro, uno
// sin garante de envío cerraría pedidos sin cobrar el envío del tenant que sí lo
// cobra, y uno sin escritor de datos del comprador le pediría al cliente su RUT
// para tirarlo a la basura. Ninguna de las tres cosas la notaría un test. Si
// faltan, que rompa en compilación. No los valida: con uno nil, Project revienta al
// usarlo.
func NewProjector(s ProjectionStore, revisions RevisionWriter, shipping ShippingEnsurer, buyer BuyerDataWriter) *Projector {
	return &Projector{store: s, revisions: revisions, shipping: shipping, buyer: buyer}
}

// Handles reconoce los efectos que el carrito PROYECTA a tablas tipadas, por nombre
// exacto: item_added, note_added, cart_closed, cart_cancelled, cart_expired y
// buyer_data_captured. Los de navegación/telemetría (cart_started,
// category_selected, item_viewed) NO se proyectan —ya quedan en flow_events por el
// sink— y devuelven false, como cualquier otro nombre.
//
// buyer_data_captured es el caso INVERSO y el único: se proyecta precisamente porque
// NO queda en flow_events (Kind modules.KindPrivate). Si este proyector no lo
// reconociera, el dato del cliente no se guardaría en ningún sitio. note_added se
// proyecta por la misma razón que item_added: es el OTRO efecto que mueve las
// líneas del carrito —les pone la indicación, y parte una línea ×N en ×(N-1)+×1
// (D-041.20)—; si no se proyectara, intake_items se quedaría con el conjunto
// anterior hasta el cierre.
func (Projector) Handles(name string) bool {
	switch name {
	case EffectItemAdded, EffectNoteAdded, EffectCartClosed, EffectCartCancelled,
		EffectCartExpired, EffectBuyerDataCaptured:
		return true
	default:
		return false
	}
}

// Project materializa el efecto del carrito según eff.Name (no mira eff.Kind). El
// sink solo lo llama para los efectos cuyo Handles devolvió true; con cualquier otro
// nombre no hace nada y devuelve nil. Todo error del almacén sube.
//
// ════════════════════════════════════════════════════════════════════════════
// item_added / note_added — la solicitud "open" y sus líneas AL DÍA
// ════════════════════════════════════════════════════════════════════════════
//
// Primero ASEGURA un contenido durable para el turno (design.md §3.4), preguntando
// DOS veces antes de crear, y el orden importa:
//
//  1. Por IDENTIDAD DE NEGOCIO: la solicitud "open" de (tenant, contacto). Si la
//     hay, NO crea otra y la "toca" (UpsertIntake) tal cual está. La solicitud
//     declara a su padre (D-043.21): una "open" con event_id vacío —legado
//     pre-0054— se ESTAMPA con meta.EventID; una que YA declara OTRO evento NO se
//     pisa: se deja un Warn («cart: la solicitud abierta ya declara OTRO evento; no
//     se pisa (D-043.21)») y se sigue. Con meta.EventID vacío no se toca nada.
//  2. POR EVENTO, sin filtro de estado (D-044.46, T4.0), solo si la meta trae
//     evento: desde el Plan 044 la etapa `draft` del pipeline deja contenido durable
//     sobre el MISMO evento en `pending_approval`, invisible para la primera
//     pregunta. Sin esta, el carrito creaba una fila nueva y chocaba contra
//     `intakes_event_id_uidx`: el pedido de ese turno se perdía (medido en UAT).
//     🔴 Se REUSA la fila y NO se le toca la cabecera —no hay UpsertIntake—: su
//     `status` es de quien lo puso, y un borrador esperando al dueño no vuelve a
//     "open" porque el cliente siga agregando. Lo que sí sigue su curso son las
//     líneas.
//  3. Si no hay ninguna, CREA una: id uuid nuevo, "open", con el tenant, el
//     contacto, la sesión y el event_id de la meta, sin vencimiento. Con la meta SIN
//     evento no se inventa nada: nace sin padre, se deja un Warn y el CHECK de la
//     0054 decide en la base. Si la escritura choca contra un único
//     (postgres.IsUniqueViolation) se deja un Error con tenant, contacto, sesión,
//     evento e id intentado —«cart: el evento ya tenía un intake
//     (intakes_event_id_uidx); el pedido de este turno NO se pudo guardar (hallazgo
//     #24)»— y el error SUBE sin tocar: es una violación de integridad (SQLSTATE
//     clase 23), no cede reintentando, y como el carrito produce contenido durable
//     el runtime corta el turno con aviso al cliente en vez de despedirlo creyendo
//     que compró (D-054.4). Con el cierre natural arreglado (H24: cada pedido tiene
//     su PROPIO evento y su PROPIA solicitud) este choque ya no debería ocurrir; el
//     log es la defensa en profundidad.
//
// Después deja intake_items IGUAL a la foto del carrito que trae el efecto
// (Payload["items"], Plan 043 · Ola 3), REEMPLAZANDO el conjunto: así el mismo
// efecto reentregado no duplica ni pierde nada —dos item_added del MISMO artículo
// son dos líneas legítimas, no hay clave natural con la que reconocer un reenvío—, y
// la indicación y el split de D-041.20 llegan cuando ocurren. La foto se lee en sus
// dos formas, la del camino en proceso ([]map[string]any) y la del round-trip JSON
// ([]any de map[string]any), con números nativos o float64; un ítem que no es un
// mapa se omite, y una clave ausente o de otro tipo da el cero (`customization`
// ausente es ""). SIN la clave "items" no se toca ninguna línea, y eso NO es lo
// mismo que una foto vacía: es un flow_event HISTÓRICO reejecutado, que no sabe qué
// había en el carrito; borrarle las líneas al pedido sería inventarse que estaba
// vacío. Una foto vacía sí vacía las líneas.
//
// ════════════════════════════════════════════════════════════════════════════
// cart_closed — el cierre, su revisión y la línea de envío
// ════════════════════════════════════════════════════════════════════════════
//
//  1. CIERRA la solicitud (store.CloseIntake, atómico): la abierta, o una "closed"
//     coherente si no había, con el total y las líneas del payload —fuente de
//     verdad—, la sesión, el event_id de la meta y `customer_note` (D-041.19; su
//     ausencia es ""). Si falla, el error sube y no se escribe nada más.
//  2. Le cuelga la REVISIÓN del cierre (ADR-0031 §3): Kind intakes.RevisionKindCart,
//     CreatedBy intakes.RevisionBySystem y el payload de
//     intakes.CartRevisionPayload(total, líneas) —sku, etiqueta, cantidad y precio;
//     sin la indicación de la línea—. Se escribe FUERA de la transacción del cierre:
//     si falla, queda una solicitud cerrada y coherente sin su revisión y el error
//     sube envuelto —`cart: revisión del cierre de la solicitud <id>: %w`—. Se acepta
//     porque la verdad de lo vendido es intake_items, no la revisión.
//  3. Pide la LÍNEA DE ENVÍO (D-041.11) sobre la solicitud que cerró, con
//     intakes.ShippingOnlyIfZones: va DESPUÉS de la revisión para que esta siga
//     siendo la foto FIEL de lo que armó el cliente, y un tenant que no cobra envío
//     cierra exactamente igual que antes. Si falla, sube envuelto —`cart: línea de
//     envío de la solicitud <id>: %w`—.
//  4. ANOTA en el Payload del efecto —el MISMO mapa, no una copia: es lo que lee el
//     WebhookSink, que corre después sobre el mismo efecto— tres claves:
//     `intake_id`; `revision_no`, el número REAL que devolvió la escritura de la
//     revisión (T4.10 · D-044.19: NO es siempre 1 —si el pipeline ya colgó su
//     revisión `interpreted` de la misma fila, el cierre es la 2—, y el puente del
//     CRM hace UPSERT por (intake_id, revision_no)); y `lifecycle_status` = "closed",
//     la clave legada tal cual, sin normalizar. Si la revisión o el envío fallan, NO
//     se anota ninguna de las tres.
//
// ════════════════════════════════════════════════════════════════════════════
// cart_cancelled / cart_expired — la transición de la solicitud abierta
// ════════════════════════════════════════════════════════════════════════════
//
// Lleva la "open" de (tenant, contacto) a "cancelled" o a "expired" conservando su
// total (H29: el efecto sigue al estado). Sin solicitud abierta es un no-op sin
// error. cart_expired no tiene productor vivo (T4.7): solo lo alcanza el REPLAY de
// un flow_event histórico, y se conserva para que ese replay no reviente.
//
// ════════════════════════════════════════════════════════════════════════════
// buyer_data_captured — UN campo, a la fila CIFRADA
// ════════════════════════════════════════════════════════════════════════════
//
// Cuelga el dato de la solicitud ABIERTA de (tenant, contacto), la misma que abrió
// el primer item_added (por eso el módulo emite estos efectos ANTES de
// cart_closed). Con `key` o `value` vacíos o de otro tipo no hay nada que guardar y
// devuelve nil sin tocar nada. SIN solicitud abierta devuelve ERROR —`cart: campo
// "<key>" del comprador sin solicitud abierta que lo reciba`— en vez de tragárselo:
// un checklist que el cliente rellenó y no se guardó tiene que ser visible. Si el
// escritor falla, sube envuelto —`cart: guardar el campo "<key>" del comprador de la
// solicitud <id>: %w`—. 🔴 El VALOR no se loguea, no se devuelve y no entra en
// ningún mensaje de error: lo único que puede aparecer es la clave del campo, que es
// configuración del tenant.
func (p *Projector) Project(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	switch eff.Name {
	case EffectItemAdded, EffectNoteAdded:
		return p.projectOpenLines(ctx, meta, eff)
	case EffectCartClosed:
		return p.closeIntake(ctx, meta, eff)
	case EffectCartCancelled:
		return p.transitionOpenIntake(ctx, meta, intakeStatusCancelled)
	case EffectBuyerDataCaptured:
		return p.putBuyerField(ctx, meta, eff)
	case EffectCartExpired:
		// Rama sin productor vivo (T4.7): solo la alcanza el REPLAY de un
		// flow_event histórico. Se queda para que ese replay no reviente ni
		// deje la fila a medias; ningún camino de hoy la sintetiza.
		return p.transitionOpenIntake(ctx, meta, intakeStatusExpired)
	default:
		return nil
	}
}
