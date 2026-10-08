// Porta internal/publicapi/intakes.go @ ed60c24 (1073 líneas: IntakeService, líneas 19-57;
// listIntakesHandler y getIntakeHandler, líneas 240-308) y el registro de G1–G6 y G8
// (internal/publicapi/publicapi.go @ ed60c24, registerIntakes, líneas 648-786).
//
// intakes.go — LA BANDEJA DE SOLICITUDES DEL DUEÑO (Plan 041 · T1.1/T1.4, ADR-0031; mapa §2.7,
// G1–G6 y G8): el puerto, las dependencias, el montaje y las dos lecturas (lista y detalle). El
// dominio vive en internal/modulos/solicitudes/intakes; aquí solo se abre la puerta HTTP.
//
// El fichero viejo se partió por tema (05 E-13), y cada trozo dice de qué líneas porta:
// intakes_filter.go (la query del listado), intakes_dto.go (las proyecciones al wire),
// intakes_llm_gate.go (el gate por campo y la única salida del detalle), intakes_status.go (G3 y
// G8), intakes_items.go (G4) e intakes_approve.go (G5 y G6).
//
// En el rojo solo existían el puerto, IntakesDeps y MountIntakes; los handlers y sus auxiliares
// nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// IntakeService es el puerto de SOLICITUDES que consume la bandeja de la cara (G1–G6 y G8). Lo
// satisface *intakes.Service del módulo solicitudes NUEVO. Toda operación va acotada al tenant,
// que sale de la identidad del token (INV-8) y NUNCA de la URL o del cuerpo.
//
// Lleva SOLO lo que la bandeja usa: el export y el resumen (G9, G10) piden su propio puerto. En
// la cara vieja era un solo puerto de nueve métodos; aquí cada área declara el suyo.
type IntakeService interface {
	// List devuelve la página de solicitudes del tenant que casan con el filtro.
	List(ctx context.Context, tenantID string, f intakes.Filter) (intakes.Page, error)
	// Get devuelve la solicitud con sus líneas y revisiones; intakes.ErrNotFound si no es del
	// tenant (404 opaco).
	Get(ctx context.Context, tenantID, intakeID string) (intakes.Detail, error)
	// SetStatus aplica una transición del ciclo de vida (D-041.10). `notice` dice quién le
	// cuenta el cambio al cliente: la ruta de estado pasa SIEMPRE intakes.NoticeToClient.
	SetStatus(ctx context.Context, tenantID, intakeID, status string, notice intakes.StatusNotice) (intakes.Intake, error)
	// Discard DESCARTA a mano un lote de solicitudes huérfanas dejándolas en `abandoned`
	// (T4.8, D-041.18). Contesta por ítem: no es todo-o-nada.
	Discard(ctx context.Context, tenantID string, intakeIDs []string) (intakes.DiscardResult, error)
	// ReplaceItems sustituye las líneas de cliente de una solicitud en `pending_approval` y
	// deja la revisión `corrected` del dueño (T4.10, REQ-36). `mode` es el `as_correction`
	// del 044 (T4.4): con intakes.EditPlain se comporta como el PUT del 041.
	ReplaceItems(ctx context.Context, tenantID, intakeID string, items []intakes.Item, mode intakes.EditMode) (intakes.Detail, error)
	// Approve APRUEBA el presupuesto (Plan 044 · T4.3): manda al cliente la cotización que
	// escribió el dueño, deja la solicitud en `confirmed` con su revisión `approved` y empuja
	// esa revisión al puente CRM. `renderedText` es el texto del DUEÑO y es obligatorio.
	Approve(ctx context.Context, tenantID, intakeID, renderedText string) (intakes.Detail, error)
	// RequestInfo manda al cliente la PREGUNTA que escribió el dueño y deja la solicitud en
	// `needs_info` (Plan 044 · T4.4). `question` es obligatoria: jamás sale sola.
	RequestInfo(ctx context.Context, tenantID, intakeID, question string) (intakes.Detail, error)
}

// IntakesDeps es lo que G1–G6 y G8 necesitan. En la cara vieja eran los campos Intakes y
// Entitlements de publicapi.Deps; el reloj es nuevo (la vieja leía time.Now() en cada handler).
type IntakesDeps struct {
	// Intakes es el servicio de solicitudes. nil ⇒ no se monta ninguna de las siete.
	Intakes IntakeService
	// Entitlements es el resolver de derechos del módulo acceso NUEVO, el MISMO (una sola caché)
	// que gatea el resto de la plataforma. Abre la puerta (`cart_basic`) y decide los campos del
	// detalle (`llm_intake`). nil ⇒ no se monta ninguna de las siete.
	Entitlements entitlements.Resolver
	// Now es el reloj contra el que se decide la marca `overdue`. nil ⇒ time.Now.
	Now func() time.Time
}

// MountIntakes registra en c las SIETE rutas de la bandeja o NINGUNA: solo si d.Intakes y
// d.Entitlements son los dos distintos de nil. Si falta cualquiera, ninguna existe (404 de ruta
// inexistente): mejor eso que una bandeja que responde 500 a medio camino o, peor, que se abre
// sin poder comprobar el plan.
//
//   - G1 "GET /api/v1/intakes": cadena R, permiso "intakes.read";
//   - G2 "GET /api/v1/intakes/{id}": cadena R, permiso "intakes.read";
//   - G3 "POST /api/v1/intakes/{id}/status": cadena W, permiso "intakes.write", recurso "intake";
//   - G4 "PUT /api/v1/intakes/{id}/items": W, "intakes.write", "intake";
//   - G5 "POST /api/v1/intakes/{id}/approve": W, "intakes.write", "intake";
//   - G6 "POST /api/v1/intakes/{id}/request-info": W, "intakes.write", "intake";
//   - G8 "POST /api/v1/intakes/discard": W, "intakes.write", "intake". Es un segmento LITERAL:
//     no compite con los comodines (ningún POST cuelga de …/{id} a secas), y un GET a ese camino
//     NO da 405: lo sirve G2 con "discard" como id (404 de solicitud inexistente).
//
// No hay más rutas: la acción «Corregir» del 044 NO tiene endpoint propio (D-044.48 §1; es G4
// con "as_correction": true), así que POST …/{id}/correct no existe.
//
// La cadena es la de Common (401 sin token; 403 {"error":"permiso denegado"} sin el permiso o
// con un token sin empresa; ningún registro en esos dos) y, POR DENTRO de ella, el gate de la
// feature "cart_basic" (entitlements.RequireFeature) EN LAS SIETE. El orden es Authenticate →
// RequirePermission → (solo W) auditoría → gate → handler:
//
//   - sin el permiso Y sin la feature ⇒ el 403 es el del permiso, no el del gate;
//   - con el permiso y sin la feature ⇒ 403 con EXACTAMENTE el cuerpo
//     {"error":"feature_not_enabled","feature":"cart_basic"}; el servicio no se toca;
//   - 🔴 "llm_intake" NO abre la puerta NI la cierra (D-044.47 §1, D-044.49 §3): un tenant con
//     "cart_basic" y sin "llm_intake" —el plan Basic— lista, edita, aprueba y pregunta; uno con
//     "llm_intake" y sin "cart_basic" recibe el 403 de "cart_basic";
//   - el gate es fail-closed: un resolver que falla corta con ese MISMO 403, nunca con 500;
//   - en las W el corte del gate SÍ deja su registro de auditoría (va por dentro de la
//     auditoría): uno, con Result "failure" y Meta {"status":403}. En G1 y G2, ninguno.
//
// El tenant es SIEMPRE el del token (INV-8): no viaja en la ruta, ni en la query, ni en el
// cuerpo, y ninguna respuesta lo lleva. Las siete llaman al servicio con el contexto de la
// petición, sin plazo propio, como en la cara vieja.
//
// LA CABECERA de una solicitud, que comparten G1, G2 y G3, es {"id","contact_id","session_id",
// "status","total","customer_note","overdue","created_at","updated_at"}, en ese orden:
//
//   - "contact_id" sale TAL CUAL lo da el servicio: opaco, ni descifrado ni enriquecido
//     (INV-04 / ADR-0010);
//   - "customer_note" (D-041.19) y "overdue" salen SIEMPRE, también vacía y en false;
//   - "overdue" es la marca DERIVADA del plazo (Plan 044 · T4.5): intakes.Overdue(solicitud,
//     d.Now()). NO cambia "status" ni los destinos; se evalúa contra UN solo instante por
//     respuesta (todas las filas de una página, contra el mismo); d.Now nil ⇒ time.Now;
//   - los instantes van en UTC, RFC 3339 con segundos.
//
// EL DETALLE, que responden G2, G4, G5 y G6, es la cabecera más "items", "revisions",
// "allowed_transitions" y "buyer_data_present", en ese orden:
//
//   - cada línea: {"sku","label","customization","qty","unit_price"}; "customization" sale
//     SIEMPRE, también vacía (D-041.17). "items":[] y nunca null;
//   - cada revisión: {"revision_no","kind","payload","rendered_text","created_by","created_at",
//     "literal_pruned_at"}. "payload" viaja como JSON CRUDO: sin tipar ni reinterpretar, con las
//     claves que da el servicio y en su orden (el codificador solo lo compacta);
//     "rendered_text" y "created_by" se omiten vacíos; "literal_pruned_at" (D-044.52 §3) se
//     omite mientras la revisión no se haya podado y, si se podó, es su instante en UTC.
//     "revisions":[] y nunca null;
//   - "allowed_transitions" es intakes.AllowedTransitions(status): un estado terminal da [] y
//     nunca null;
//   - "buyer_data_present" es el booleano del servicio: es TODO lo que se publica del comprador;
//   - 🔴 EL GATE POR CAMPO (Plan 044 · T4.1, D-044.47 §1): sin la feature "llm_intake" —o con el
//     resolver fallando: fail-closed— del payload de CADA revisión desaparecen la clave raíz
//     "suggested_questions" y la clave "variant_options" de cada elemento de "lines". La clave
//     DESAPARECE, no queda en []. Lo demás del payload no se toca; un payload que no lleva
//     ninguna de las dos (o que no es un objeto JSON) sale BYTE A BYTE igual que con la
//     feature —sin reordenar sus claves—, y "literal_pruned_at", que vive fuera del payload, no
//     se tapa. Con la feature el
//     payload sale entero. El gate vale igual en las CUATRO rutas que responden el detalle.
//
// G1 lista: List(tenant, filtro) con el filtro que sale de la query.
//
//   - "from" y "to": YYYY-MM-DD (en UTC) o RFC 3339; vacío ⇒ sin cota. Una fecha suelta en "to"
//     es «hasta el final de ESE día» (se le suma un día: el rango es [from, to)); un RFC 3339 se
//     respeta tal cual. Ilegible ⇒ 400 {"error":"from inválido: usa YYYY-MM-DD o RFC3339"} (o
//     "to inválido: …");
//   - "status" SE REPITE para pedir varios (D-044.47 §2): llegan todos al filtro, en el orden
//     de la query y normalizados ("closed" ⇒ "confirmed"); los vacíos se saltan ("?status=" es
//     «sin filtro»); uno desconocido —también entre dos buenos, y la forma con comas "a,b"— ⇒
//     400 {"error":"status desconocido"};
//   - "sort": "newest" u "oldest", tal cual; ausente ⇒ vacío (el servicio pone "newest"); otro
//     valor ⇒ 400 {"error":"sort desconocido: usa newest u oldest"};
//   - "orphan": lo que acepta strconv.ParseBool; ausente o vacío ⇒ false; otro valor ⇒ 400
//     {"error":"orphan inválido: usa true o false"};
//   - "session" pasa tal cual; "page" (defecto 1) y "page_size" (defecto 50) son enteros no
//     negativos y lo ilegible cae a su defecto (el techo lo pone el servicio);
//   - los 400 se comprueban en ese orden (from, to, status, sort, orphan) y no llaman a List;
//   - 200 {"intakes":[…],"page","page_size","total"}: las cabeceras en el orden del servicio y
//     page, page_size y total LOS DE LA PÁGINA que devuelve el servicio (los efectivos, no los
//     pedidos); "intakes":[] y nunca null;
//   - fallo de List ⇒ 500 {"error":"no se pudieron listar las solicitudes"}.
//
// G2 lee: Get(tenant, id) ⇒ 200 con el detalle. intakes.ErrNotFound ⇒ 404 {"error":"solicitud no
// encontrada"} (nunca 403: confirmaría que el id existe en otro tenant); otro fallo ⇒ 500
// {"error":"no se pudo leer la solicitud"}.
//
// G3 transiciona: cuerpo {"status"}. Ilegible ⇒ 400 {"error":"cuerpo JSON inválido"}; sin
// "status" ⇒ 400 {"error":"status es obligatorio"}; ninguno de los dos llama al servicio.
// SetStatus(tenant, id, status, intakes.NoticeToClient) —SIEMPRE NoticeToClient: esta puerta no
// escribe texto propio y el aviso genérico es el único que recibe el cliente (D-041.14)—:
//
//   - 200 con la CABECERA transicionada. Significa «la transición se aplicó», nunca «el cliente
//     recibió el aviso»;
//   - ErrNotFound ⇒ 404 "solicitud no encontrada";
//   - *intakes.TransitionError ⇒ 422 {"error":"invalid_transition","status":<From>,
//     "requested":<To>,"allowed":<Allowed>}: dónde está y adónde SÍ puede ir;
//   - intakes.ErrConflict ⇒ 409 {"error":"la solicitud cambió de estado; recárgala y
//     reintenta"};
//   - otro fallo ⇒ 500 {"error":"no se pudo cambiar el estado de la solicitud"}.
//
// G4 edita las líneas: cuerpo {"items":[{"sku","label","customization","qty","unit_price"}],
// "as_correction"}, el conjunto COMPLETO de líneas de cliente que debe quedar.
//
//   - ilegible ⇒ 400 "cuerpo JSON inválido"; sin la clave "items" (o en null) ⇒ 400
//     {"error":"items es obligatorio (manda [] para dejar la solicitud sin líneas)"}: un {} no
//     vacía el presupuesto. "items":[] SÍ se aplica;
//   - "sku" se recorta de espacios; "label" y "customization" pasan por intakes.SanitizeNote
//     (la MISMA puerta que el carrito, D-041.19): lo que llega al servicio es el texto saneado;
//   - un texto que pasa de intakes.MaxNoteRunes NO se trunca: 400 {"error":"invalid_items",
//     "errors":[{"index","field","message"}]} con "la etiqueta pasa del máximo de 280
//     caracteres" (field "label") o "la personalización pasa del máximo de 280 caracteres"
//     (field "customization"), MÁS los defectos de intakes.ValidateEditableItems sobre esas
//     mismas líneas, todos de una vez y ordenados por línea (estable: dentro de una línea, los
//     del saneo van antes). El texto que no pasó el saneo llega a esa validación VACÍO, así que
//     una etiqueta demasiado larga suma además el defecto de etiqueta obligatoria, como en la
//     cara vieja. El servicio no se toca;
//   - ReplaceItems(tenant, id, líneas, modo): modo intakes.EditAsCorrection solo con
//     "as_correction": true; ausente o false ⇒ intakes.EditPlain;
//   - 200 con el detalle (por el mismo gate por campo que G2);
//   - ErrNotFound ⇒ 404; *intakes.InvalidItemsError ⇒ 400 invalid_items con sus defectos;
//     *intakes.TooManyItemsError ⇒ 400 {"error":"la edición trae <Count> líneas y el máximo es
//     <Max>"}; *intakes.NotEditableError ⇒ 422 {"error":"not_editable","status":<Status>,
//     "editable_in":["pending_approval"]}; ErrConflict ⇒ 409 (el texto de G3); otro fallo ⇒ 500
//     {"error":"no se pudieron guardar las líneas de la solicitud"}.
//
// G5 aprueba: cuerpo {"rendered_text"}, el texto que el DUEÑO le manda al cliente. Ilegible ⇒
// 400 "cuerpo JSON inválido". Approve(tenant, id, rendered_text) con el texto TAL CUAL (que
// falte o venga vacío lo decide el servicio):
//
//   - 200 con el detalle (mismo gate por campo). Significa «la aprobación quedó registrada»,
//     nunca «el cliente recibió el mensaje»;
//   - ErrNotFound ⇒ 404; intakes.ErrEmptyQuoteText ⇒ 400 {"error":"rendered_text es
//     obligatorio: es el texto de la cotización que se le manda al cliente"};
//     *intakes.PendingPriceError ⇒ 400 {"error":"lines_without_price","lines":[{"index",
//     "label"}]} con TODAS las líneas; intakes.ErrEmptyQuote ⇒ 400 {"error":"la solicitud no
//     tiene líneas que cotizar: guarda primero las líneas del borrador con PUT
//     /api/v1/intakes/{id}/items"}; *intakes.NotApprovableError ⇒ 422 {"error":
//     "not_approvable","status":<Status>,"approvable_in":["pending_approval"]};
//     *TransitionError ⇒ el 422 invalid_transition de G3; ErrConflict ⇒ 409 (el texto de G3);
//     otro fallo ⇒ 500 {"error":"no se pudo aprobar la solicitud"}.
//
// G6 pregunta: cuerpo {"question"}, la pregunta que el DUEÑO le manda al cliente. Ilegible ⇒ 400
// "cuerpo JSON inválido". RequestInfo(tenant, id, question) con el texto TAL CUAL:
//
//   - 200 con el detalle (mismo gate por campo). Significa «quedó esperando la respuesta»,
//     nunca «el cliente recibió la pregunta»;
//   - ErrNotFound ⇒ 404; intakes.ErrEmptyQuestion ⇒ 400 {"error":"question es obligatoria: es
//     la pregunta que se le manda al cliente, y jamás sale sola"}; *TransitionError ⇒ el 422
//     invalid_transition de G3 (no hay un cuerpo propio: esta puerta no estrecha la máquina de
//     estados); ErrConflict ⇒ 409 (el texto de G3); otro fallo ⇒ 500 {"error":"no se pudo pedir
//     más información sobre la solicitud"}.
//
// 🔴 INV-1: G5 y G6 son la ÚNICA llamada a Approve y a RequestInfo de toda la cara, una cada
// una: ninguna otra ruta ni auxiliar las invoca.
//
// G8 descarta un lote: cuerpo {"intake_ids":[…]}, una lista EXPLÍCITA de ids y nunca un filtro
// (el descarte es irreversible, D-041.22). Ilegible ⇒ 400 "cuerpo JSON inválido".
// Discard(tenant, ids) con los ids tal cual:
//
//   - 200 {"discarded":[…],"skipped":[{"intake_id","reason"}]}: las razones son las claves del
//     dominio, tal cual; las dos listas salen SIEMPRE, [] y nunca null. Un lote mixto es el caso
//     normal: NO hay 404 ni 422 (un id inexistente o de otro tenant es un "not_found" DENTRO
//     del 200);
//   - intakes.ErrEmptyDiscardBatch ⇒ 400 {"error":"intake_ids es obligatorio: manda entre 1 y
//     200 ids"}; *intakes.TooLargeBatchError ⇒ 400 {"error":"el lote trae <Count> solicitudes y
//     el máximo es <Max>"}; otro fallo ⇒ 500 {"error":"no se pudieron descartar las
//     solicitudes"}.
//
// Ningún 500 repite el error del servicio (el del driver puede llevar el DSN).
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara a un handler recibiría 401 {"error":"autenticación requerida"}.
//
// Fallo de cableado: k.MW nil con las dos dependencias presentes hace panic AL MONTAR (ver
// Common).
func MountIntakes(c *Cara, k Common, d IntakesDeps) {
	panic(pendiente.Implementar("apipublica.MountIntakes"))
}
