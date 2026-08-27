package cart

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
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
// satisface *store.PostgresRepository / *MemoryRepository.
//
// Ya NO incluye GetTenantSettings: lo único que el proyector leía de la config del
// tenant era order_ttl_seconds, para fechar el vencimiento de la solicitud, y ese
// reloj quedó derogado (D-041.16, T4.7).
type ProjectionStore interface {
	GetOpenIntake(ctx context.Context, tenantID, contactID string) (store.Intake, bool, error)
	// GetIntakeByEvent es la SEGUNDA pregunta de ensureOpenIntake (D-044.46): la
	// solicitud que ya cuelga de ESTE evento, esté en el estado que esté. No
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
// Ola 3 · T8, cierra H10): item_added asegura la solicitud "open", cart_closed
// cierra atómicamente solicitud+líneas, y cart_cancelled/cart_expired transicionan
// la solicitud. Es un adaptador IMPURO; produce EXACTAMENTE las mismas
// filas que producía el switch central del PersistSink (retrofit por efectos, §9.D).
//
// Sin reloj a propósito (T4.7): el único uso que tenía era fechar expires_at, y
// nada vence por tiempo (D-041.16).
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
// faltan, que rompa en compilación.
func NewProjector(s ProjectionStore, revisions RevisionWriter, shipping ShippingEnsurer, buyer BuyerDataWriter) *Projector {
	return &Projector{store: s, revisions: revisions, shipping: shipping, buyer: buyer}
}

// Handles reconoce los efectos que el carrito PROYECTA a tablas tipadas. Los efectos
// de navegación/telemetría (cart_started, category_selected, …) NO se proyectan (ya
// quedan en flow_events por el sink) y devuelven false.
//
// buyer_data_captured es el caso INVERSO y el único: se proyecta precisamente
// porque NO queda en flow_events (Kind modules.KindPrivate). Si este proyector no
// lo reconociera, el dato del cliente no se guardaría en ningún sitio.
// note_added es el segundo caso que se proyecta sin ser un cambio de estado de la
// solicitud, y por la misma razón que item_added: es el OTRO efecto que mueve las
// líneas del carrito —les pone la indicación del cliente, y parte una línea ×N en
// ×(N-1)+×1 (D-041.20)—. Si no se proyectara, intake_items se quedaría con el
// conjunto anterior hasta el cierre y la promesa de esta tarea («al día en todo
// momento») sería falsa justo en el pedido que alguien personalizó.
func (Projector) Handles(name string) bool {
	switch name {
	case EffectItemAdded, EffectNoteAdded, EffectCartClosed, EffectCartCancelled,
		EffectCartExpired, EffectBuyerDataCaptured:
		return true
	default:
		return false
	}
}

// Project materializa el efecto del carrito. El sink solo lo llama para los efectos
// cuyo Handles devolvió true.
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

// ensureOpenIntake garantiza UN contenido durable para el turno al primer
// item_added (design.md §3.4). Idempotente por identidad de negocio: si ya hay
// abierta NO crea otra, y la "toca" tal cual está.
//
// 🔄 Desde D-044.46 (T4.0) pregunta DOS veces antes de crear, y el orden importa:
// primero por identidad de negocio (tenant, contacto) y después POR EVENTO, sin
// filtro de estado. La segunda existe porque este proyector ya no es el único
// productor de filas en `intakes`: la etapa `draft` del pipeline del Plan 044
// escribe la suya sobre el mismo evento y en `pending_approval`, un estado que la
// primera pregunta no ve. Ver el bloque de la segunda pregunta, más abajo.
//
// Hasta T4.7 esto fijaba y REFRESCABA un vencimiento (expires_at = now +
// tenant_settings.order_ttl) en CADA item_added. D-041.16 lo derogó: ninguna
// solicitud vence por tiempo, así que la solicitud nace con expires_at ZERO (NULL
// en la tabla) y la fila que ya existía se reescribe con el valor que tuviera —las
// históricas conservan el suyo tal cual, que es justo lo que promete el COMMENT de
// la columna.
// La solicitud DECLARA A SU PADRE al nacer (D-043.21, T4.5.3): event_id =
// meta.EventID, el dato que este método ya tenía en la mano y descartaba. Tres
// reglas, en orden:
//
//   - Al CREAR, la fila nace con el event_id de la meta. Si la meta llega VACÍA
//     (efecto fuera de evento — no debería pasar en cart), NO se inventa nada: va
//     NULL, se loguea, y el CHECK de la 0054 hace su trabajo en la base.
//   - Al REUSAR una "open" con event_id NULL (legado pre-0054), se ESTAMPA: es la
//     única reparación legítima de un huérfano — el evento vivo del turno es, por
//     E-2 (uno vivo por tipo), el mismo del que esa solicitud colgaba.
//   - Al REUSAR una que YA declara OTRO evento, NO se pisa: se loguea y se sigue.
//     No debería pasar (un vivo por tipo + índice único parcial), así que si se ve
//     en un log es una carrera o un dato roto que hay que mirar, no maquillar. El
//     COALESCE del UpsertIntake garantiza además EN EL SQL que ninguna escritura
//     des-declara un padre.
func (p *Projector) ensureOpenIntake(ctx context.Context, meta modules.EffectMeta) (string, error) {
	existing, found, err := p.store.GetOpenIntake(ctx, meta.TenantID, meta.ContactID)
	if err != nil {
		return "", err
	}
	if found {
		switch {
		case existing.EventID == "" && meta.EventID != "":
			existing.EventID = meta.EventID
		case meta.EventID != "" && existing.EventID != meta.EventID:
			slog.Warn("cart: la solicitud abierta ya declara OTRO evento; no se pisa (D-043.21)",
				"intake_id", existing.ID, "event_id_solicitud", existing.EventID, "event_id_meta", meta.EventID)
		}
		return existing.ID, p.store.UpsertIntake(ctx, existing)
	}
	// SEGUNDA PREGUNTA, y la que cierra el hallazgo #24 por el lado del carrito
	// (D-044.46, T4.0). La de arriba resuelve por identidad de NEGOCIO y solo ve las
	// `open`; desde el Plan 044 la etapa `draft` del pipeline para contenido durable
	// sobre el MISMO evento y lo deja en `pending_approval`, que a la de arriba le
	// resulta invisible. Sin esta pregunta el carrito creaba una fila nueva con un id
	// sorteado y chocaba contra `intakes_event_id_uidx` — medido en UAT (00:11:08 del
	// 27-08): el pedido de ese turno se perdía.
	//
	// 🔴 SE REUSA LA FILA Y NO SE LA TOCA LA CABECERA. No hay UpsertIntake aquí a
	// propósito: la fila ya declara este evento (por eso la encontramos) y su `status`
	// es de quien lo puso — un borrador esperando al dueño no vuelve a `open` porque
	// el cliente siga agregando. Lo que sí sigue su curso son las LÍNEAS: el llamante
	// (projectOpenLines) escribe sobre esta solicitud la foto del carrito, que es
	// exactamente lo que pide D-044.46 («el carrito reusando la fila»).
	if meta.EventID != "" {
		ajena, hallada, err := p.store.GetIntakeByEvent(ctx, meta.TenantID, meta.EventID)
		if err != nil {
			return "", err
		}
		if hallada {
			slog.Info("cart: el evento YA tenía contenido durable; se reusa en vez de crear otro (D-044.46)",
				"tenant_id", meta.TenantID, "contact_id", meta.ContactID, "session_id", meta.SessionID,
				"event_id", meta.EventID, "intake_id", ajena.ID, "status", ajena.Status)
			return ajena.ID, nil
		}
	}
	if meta.EventID == "" {
		slog.Warn("cart: item_added sin evento en la meta; la solicitud nace sin padre y el CHECK de la 0054 decidirá",
			"tenant_id", meta.TenantID, "session_id", meta.SessionID)
	}
	intake := store.Intake{
		ID:        uuid.NewString(),
		TenantID:  meta.TenantID,
		ContactID: meta.ContactID,
		SessionID: meta.SessionID,
		Status:    intakeStatusOpen,
		EventID:   meta.EventID,
	}
	if err := p.store.UpsertIntake(ctx, intake); err != nil {
		if postgres.IsUniqueViolation(err) {
			// Defensa en profundidad del hallazgo #24 (Plan 043 · Ola 6): con el cierre
			// natural del carrito arreglado (cart.go, Step) este choque YA NO DEBERÍA
			// ocurrir —un evento cerrado no vuelve a reusarse—, pero un INSERT que
			// choca contra el único parcial `intakes_event_id_uidx` (migración 0054,
			// "un evento tiene A LO SUMO un contenido durable, para siempre") es
			// exactamente el síntoma medido del pedido que se pierde en silencio.
			//
			// ACTUALIZADO 2026-08-13 (Plan 054 · D-054.4 · T3): la parte de este
			// comentario que decía que el dispatcher es best-effort "y esto NO lo
			// cambia" YA NO ES CIERTA, y la "decisión abierta" del hallazgo #24 quedó
			// CERRADA. Como cart.ProducesDurableContent() == true, Runtime.dispatch
			// deja de tragarse el fallo: corta el turno con aviso explícito al cliente
			// en vez de despedirlo creyendo que compró. Este error concreto NO se
			// reintenta —y eso sigue siendo lo correcto—: un choque contra el único
			// parcial es SQLSTATE clase 23 (violación de integridad), que no cede
			// reintentando.
			//
			// El log específico se conserva: sin él el único rastro sería "sink de
			// efecto falló" sin tenant/contacto/evento, insuficiente para diagnosticar
			// CUÁL pedido se perdió.
			slog.Error("cart: el evento ya tenía un intake (intakes_event_id_uidx); el pedido de este turno NO se pudo guardar (hallazgo #24)",
				"tenant_id", meta.TenantID, "contact_id", meta.ContactID, "session_id", meta.SessionID,
				"event_id", meta.EventID, "intake_id_intentado", intake.ID, "error", err)
		}
		return "", err
	}
	return intake.ID, nil
}

// projectOpenLines asegura la solicitud "open" y deja sus intake_items IGUALES a la
// foto del carrito que trae el efecto (Plan 043 · Ola 3). Es lo que cierra el hueco
// que destapó REQ-26b: hasta ahora una solicitud "open" no tenía NINGUNA línea —se
// materializaban solo al cerrar—, de modo que rescatarla, o mostrarla en el CRM,
// prometía unas líneas que no existían.
//
// Por qué REEMPLAZAR y no ir añadiendo la línea que el efecto publica:
//
//   - IDEMPOTENCIA. El mismo efecto reentregado escribe el mismo conjunto, así que
//     no duplica ni pierde nada. Añadiendo no habría forma de conseguirlo: dos
//     item_added del MISMO artículo son dos líneas legítimas del pedido (se agrega
//     dos veces, con indicaciones distintas), así que no existe clave natural con la
//     que reconocer un reenvío.
//   - FIDELIDAD. El carrito cambia sus líneas por más sitios que el "agregar": la
//     indicación de una línea y el split de D-041.20 las reescriben. Con la foto, la
//     tabla dice lo que dice el carrito; acumulando, diría solo lo que se agregó.
//
// SIN foto en el payload no se toca ninguna línea, y eso NO es lo mismo que una foto
// vacía: es un flow_event HISTÓRICO reejecutado (los hay, y el proyector los soporta
// a propósito, ver EffectCartExpired). Un efecto viejo no sabe qué había en el
// carrito, así que borrarle las líneas al pedido sería inventarse que estaba vacío.
func (p *Projector) projectOpenLines(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	intakeID, err := p.ensureOpenIntake(ctx, meta)
	if err != nil {
		return err
	}
	items, ok := cartLineSnapshot(eff.Payload)
	if !ok {
		return nil
	}
	return p.store.ReplaceIntakeItems(ctx, intakeID, items)
}

// closeIntake proyecta cart_closed: cierra ATÓMICAMENTE la solicitud abierta (o crea una
// "closed" coherente) con el total del payload e inserta TODAS las líneas (fuente de
// verdad). Delega en store.CloseIntake (una transacción, Plan 027 · Ola 1 · T4) y
// después le cuelga la REVISIÓN 1 (ADR-0031 §3): la foto de lo que el carrito armó.
//
// La revisión se escribe FUERA de la transacción del cierre, y eso es una decisión
// consciente con un costo asumido: si su INSERT falla, queda una solicitud cerrada
// y coherente —líneas incluidas— sin su revisión 1, y el error sube al dispatcher,
// que lo LOGUEA sin abortar el avance de la conversación (ver PersistSink.Handle).
// Se acepta porque la verdad de lo vendido es intake_items, no la revisión: perder
// el rastro de la negociación degrada la auditoría, mientras que meter el escritor
// de revisiones dentro de la transacción del motor obligaría al carrito a compartir
// la conexión con otro módulo. Lo que NO ocurre es que se pierdan las dos: el
// cierre ya está confirmado cuando esto se intenta.
func (p *Projector) closeIntake(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	items := cartItems(eff.Payload)
	total := modules.AsFloat(eff.Payload["total"])

	intakeID, err := p.store.CloseIntake(ctx, store.IntakeClose{
		TenantID:  meta.TenantID,
		ContactID: meta.ContactID,
		SessionID: meta.SessionID,
		Total:     total,
		// El cierre también declara al padre (D-043.21): en el camino normal solo
		// rellena un NULL legado (el open ya lo estampó ensureOpenIntake), y en la
		// rama que crea la "closed" coherente es el event_id de esa fila nueva.
		EventID: meta.EventID,
		// La indicación del pedido (D-041.19) viaja en la cabecera del efecto y su
		// AUSENCIA da la cadena vacía, igual que la personalización de cada línea: un
		// cierre emitido antes de que el campo existiera —o por un carrito sin
		// indicaciones, que es la mayoría— cierra exactamente igual.
		CustomerNote: modules.AsString(eff.Payload["customer_note"]),
		Items:        items,
	})
	if err != nil {
		return err
	}

	payload, err := intakes.CartRevisionPayload(total, revisionLines(items))
	if err != nil {
		return err
	}
	if _, err := p.revisions.InsertRevision(ctx, intakes.Revision{
		IntakeID:  intakeID,
		Kind:      intakes.RevisionKindCart,
		Payload:   payload,
		CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		return fmt.Errorf("cart: revisión del cierre de la solicitud %s: %w", intakeID, err)
	}

	// La línea de envío va DESPUÉS de la revisión, y no antes, para que la revisión
	// 1 siga siendo la foto FIEL de lo que armó el cliente: el envío lo pone la
	// plataforma encima, no el carrito. Con ShippingOnlyIfZones, un tenant que no
	// cobra envío cierra exactamente igual que antes de esta tarea — este cierre no
	// pasa por ningún ciclo de aprobación (va directo a `confirmed`), así que una
	// línea «por confirmar» que nadie va a precificar solo sería ruido (D-041.11).
	if err := p.shipping.EnsureShippingLine(ctx, meta.TenantID, intakeID, intakes.ShippingOnlyIfZones); err != nil {
		return fmt.Errorf("cart: línea de envío de la solicitud %s: %w", intakeID, err)
	}

	// Anota el ID recién generado de vuelta en el Payload (mapa compartido, no una
	// copia): el fan-out de PersistSink.Handle ya escribió flow_events con el
	// payload ORIGINAL antes de llegar aquí, así que esto no lo toca. Lo que sí ve
	// esta escritura es el WebhookSink (Plan 042), que corre DESPUÉS en el mismo
	// dispatch() sobre el mismo eff — es la única forma de correlacionar sin que el
	// sink del CRM tenga que volver a consultar la BD.
	eff.Payload["intake_id"] = intakeID
	return nil
}

// revisionLines congela las líneas del cierre en la forma del payload de la
// revisión. No lleva added_at: la revisión ya está fechada entera y el instante de
// cada línea es del carrito, no de la foto.
func revisionLines(items []store.IntakeItem) []intakes.RevisionLine {
	out := make([]intakes.RevisionLine, 0, len(items))
	for _, it := range items {
		out = append(out, intakes.RevisionLine{
			SKU: it.SKU, Label: it.Label, Qty: it.Qty, UnitPrice: it.UnitPrice,
		})
	}
	return out
}

// putBuyerField materializa UN campo del checklist del comprador en la fila
// CIFRADA de la solicitud (T4.5, D-041.13). Es el único punto del carrito por el
// que un dato personal llega a la base, y por eso conviene tener claro qué hace y
// qué no:
//
//   - Cuelga el dato de la solicitud ABIERTA de (tenant, contacto), la misma que
//     abrió el primer item_added. Es también el motivo por el que el módulo emite
//     estos efectos ANTES de cart_closed: después no habría solicitud abierta.
//   - SIN solicitud abierta devuelve ERROR en vez de tragárselo en silencio. El
//     dispatcher lo loguea sin abortar la conversación (mismo trato que la revisión
//     del cierre), pero queda constancia: un checklist que el cliente rellenó y no
//     se guardó tiene que ser visible, no un hueco.
//   - El valor NO se loguea, NO se devuelve y NO entra en ningún mensaje de error.
//     Lo que puede aparecer en un log de fallo es la clave del campo ("rut"), que
//     es configuración del tenant, jamás lo que el cliente escribió.
func (p *Projector) putBuyerField(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	key := modules.AsString(eff.Payload["key"])
	value := modules.AsString(eff.Payload["value"])
	if key == "" || value == "" {
		// Efecto vacío o mal formado: nada que guardar. No es un error — el módulo no
		// los produce así, y un replay de un payload viejo no debe reventar.
		return nil
	}
	intake, found, err := p.store.GetOpenIntake(ctx, meta.TenantID, meta.ContactID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("cart: campo %q del comprador sin solicitud abierta que lo reciba", key)
	}
	if err := p.buyer.PutBuyerField(ctx, intake.ID, key, value); err != nil {
		return fmt.Errorf("cart: guardar el campo %q del comprador de la solicitud %s: %w", key, intake.ID, err)
	}
	return nil
}

// transitionOpenIntake lleva la solicitud "open" a cancelled/expired (design.md §3.4).
// Sin solicitud abierta es un no-op sin error (idempotente / nada que transicionar).
func (p *Projector) transitionOpenIntake(ctx context.Context, meta modules.EffectMeta, status string) error {
	intake, found, err := p.store.GetOpenIntake(ctx, meta.TenantID, meta.ContactID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return p.store.MarkIntakeStatus(ctx, intake.ID, status, intake.Total)
}

// cartItems extrae las líneas del payload de cart_closed a []store.IntakeItem. Tolera
// ambas formas de la lista: el camino en-proceso ([]map[string]any que construye el
// módulo) y el round-trip JSON ([]any de map[string]any). Ítems mal formados se
// omiten sin panica. El IntakeID lo fija store.CloseIntake.
func cartItems(payload map[string]any) []store.IntakeItem {
	var out []store.IntakeItem
	switch items := payload[snapshotKey].(type) {
	case []map[string]any:
		for _, m := range items {
			out = append(out, intakeItemFromMap(m))
		}
	case []any:
		for _, e := range items {
			if m, ok := e.(map[string]any); ok {
				out = append(out, intakeItemFromMap(m))
			}
		}
	}
	return out
}

// cartLineSnapshot devuelve las líneas de la foto del carrito y si el efecto TRAÍA
// foto. Los dos casos son distintos y confundirlos borra pedidos: una foto vacía
// dice «el carrito no tiene líneas», y la AUSENCIA de foto dice «este efecto no sabe
// qué líneas hay» —un flow_event escrito antes de que la foto existiera, reejecutado
// hoy—. Por eso la presencia se mira sobre la CLAVE y no sobre el resultado de
// cartItems, que devuelve nil para las dos.
func cartLineSnapshot(payload map[string]any) ([]store.IntakeItem, bool) {
	if _, ok := payload[snapshotKey]; !ok {
		return nil, false
	}
	return cartItems(payload), true
}

// intakeItemFromMap traduce UNA línea del payload del cierre a la fila que se
// persiste. `customization` (D-041.17) se lee como cualquier otra clave y su
// AUSENCIA da la cadena vacía: un blob de carrito escrito antes de que el campo
// existiera —o por un productor que no lo escribe— cierra exactamente igual, sin
// rama especial ni versión de payload.
func intakeItemFromMap(m map[string]any) store.IntakeItem {
	return store.IntakeItem{
		SKU:           modules.AsString(m["sku"]),
		Label:         modules.AsString(m["label"]),
		Customization: modules.AsString(m["customization"]),
		Qty:           modules.AsInt(m["qty"]),
		UnitPrice:     modules.AsFloat(m["unit_price"]),
	}
}
