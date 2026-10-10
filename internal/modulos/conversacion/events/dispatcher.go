// Porta internal/flujos/events/dispatcher.go @ 9d5a4b6

package events

import (
	"context"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ErrNoResolver lo devuelve Build si el despachador se construyó sin Resolver de
// entitlements. Falla en vez de listarlo todo: sin resolver no se puede saber qué
// tiene contratado el tenant, y «no pude averiguarlo» no debe parecerse a «lo
// tiene» (fail-closed, la misma regla que entitlements.RequireFeature).
var ErrNoResolver = errors.New("events: el despachador necesita un Resolver de entitlements (fail-closed)")

// RescuableLister lee los eventos que se le pueden OFRECER a un contacto. Lo
// satisface *Store.
//
// Es EL único acceso del despachador a los eventos, y que no incluya ListAlive es
// la forma fuerte de INV-17: todo lo que este componente enseña —el menú, el
// automensaje de rescate, la entrada de conversación— sale de la consulta que YA
// filtra por la solicitud, así que no existe el camino por el que un pedido
// descartado vuelva a mencionarse. Con las dos lecturas en el puerto, ese camino
// era un descuido de una línea (y lo fue: el menú de la Ola 2 ofrecía retomar
// pedidos que el dueño había descartado).
//
// «¿Qué tiene abierto?» sigue siendo una pregunta legítima —el store la responde
// con ListAlive— pero no es una pregunta del despachador: quien la hace es quien
// audita o ejecuta, no quien ofrece.
//
// Es un puerto de LECTURA a propósito y por eso no expone nada más: el
// despachador decide, no ejecuta. Quien crea, conmuta o cierra eventos es el
// runtime (T2.2/T2.4), para que ese cableado viva en un solo sitio.
type RescuableLister interface {
	ListRescuable(ctx context.Context, tenantID, sessionID, contactID string, limit int) ([]Rescuable, error)
}

// KindOffer lista los TIPOS de evento que un tenant ofrece en una sesión: el
// catálogo de lo que se puede empezar, antes de filtrar por lo contratado.
//
// Es un puerto y no una consulta directa porque «qué ofrece este tenant» es una
// pregunta de configuración, no del modelo de eventos. Hoy la responde
// TriggerKindOffer leyendo las reglas event_start (T2.1); mañana podría
// responderla otra cosa sin tocar el despachador.
type KindOffer interface {
	OfferedKinds(ctx context.Context, tenantID, sessionID string) ([]string, error)
}

// ConversationRef identifica la conversación: la MISMA terna que identifica un
// evento. El mismo contacto en otra sesión es otra conversación.
type ConversationRef struct {
	TenantID  string
	SessionID string
	ContactID string
}

// Dispatcher arma el menú numérico dinámico del nivel superior (T2.3) y no hace
// nada más: SOLO LEE. No escribe en conversation_events ni en flow_state.
//
// Reparto de trabajo con el runtime, que es lo que hace testeable esto sin BD:
// aquí se decide QUÉ se le ofrece al cliente y qué significa el número que
// responda; allí se ejecuta la consecuencia (crear, conmutar, cerrar).
//
// En el rojo no lleva campos. El verde le pone tres: los rescatables, la oferta y el resolver.
type Dispatcher struct{}

// NewDispatcher construye el despachador sobre sus tres fuentes: los eventos
// rescatables del contacto, los tipos que el tenant ofrece y los derechos del
// tenant.
//
// No valida nada: un resolver nil se descubre al construir (ErrNoResolver en los cuatro Build*).
func NewDispatcher(ev RescuableLister, kinds KindOffer, feats entitlements.Resolver) *Dispatcher {
	panic(pendiente.Implementar("events.NewDispatcher"))
}

// Build arma el menú de la conversación.
//
// Compone DOS fuentes que no se solapan ni se anulan: cada tipo que el tenant
// ofrece da una opción de «pedir ese tipo», y cada evento vivo del contacto da,
// ADEMÁS, una opción de «volver a lo que dejaste a medias». Un tipo con evento
// vivo aparece por tanto DOS VECES, con sentidos distintos — que es exactamente
// lo que enseña el ejemplo del ADR-0029 §E-9.3 («1) Hacer un pedido» junto a
// «4) Retomar algo que dejaste a medias»).
//
// Es deliberado NO ocultar los tipos ya ocupados: esa era la salida (iii) de las
// tres que el design planteó para la colisión con el único parcial, y su precio
// —que el cliente no pueda pedir algo mientras tenga uno vivo de ese tipo— es la
// razón por la que se descartó. Lo que ocurre al elegir un tipo ocupado lo
// decide el EJECUTOR con la norma de la Enmienda 6 / E-11 (suspendido ⇒ se
// cierra y nace uno nuevo; dentro de su ventana ⇒ se conmuta hacia él), no este
// componente: aquí no se consulta el TTL ni se mira el reloj.
//
// El orden pone primero lo que se puede pedir y después lo rescatable, como en
// el ejemplo del ADR. Tiene una consecuencia útil: los números de las opciones
// de pedir no bailan según lo que el cliente tenga abierto ese día.
//
// El menú NO se ofrece a sí mismo: el despachador es un evento kind='menu'
// (D-043.3) y listarlo dentro del menú sería un bucle.
//
// La mitad de «retomar» sale de los RESCATABLES, no de los vivos (INV-17): un
// pedido que el dueño descartó sigue vivo un rato y no se menciona. Lo que se lee
// es la misma consulta del automensaje de rescate; lo único que cambia aquí es el
// ORDEN, que es una decisión de presentación: por NACIMIENTO (created_at y, a igual instante, id).
//
// El gate de features (común a los cuatro Build*): los derechos se resuelven de UNA pasada con
// ListEffective. Sin resolver, ErrNoResolver. Si el resolver falla, el error se propaga (un fallo
// de infraestructura no abre capacidades). Si el tenant no tiene NINGUNA feature efectiva
// (taxonomía sin sembrar) se lista SIN filtro y Menu.Unfiltered va a true: es la única excepción
// al fail-closed. Con features, un tipo pasa si su feature está entre ellas (menu → menu, cart →
// cart_basic, survey → survey, media → media) y un tipo que no tiene feature asociada pasa
// siempre. El gate alcanza a las DOS mitades: lo que se puede pedir y lo que se puede retomar.
//
// Las opciones salen numeradas desde 1 y sin huecos: primero las de ActionStart, en el orden que
// dio la oferta y sin repetir tipo; después las de ActionResume, una por rescatable, con su Kind
// y su EventID. Sin opciones, Options es nil (Menu.Empty). Los rescatables se piden con la terna
// exacta de ref y un lote de 6.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - ErrNoResolver, a secas;
//   - "events: resolver los derechos del tenant para el menú: %w";
//   - "events: listar los tipos que ofrece el tenant: %w";
//   - "events: listar los eventos rescatables del contacto: %w".
func (d *Dispatcher) Build(ctx context.Context, ref ConversationRef) (Menu, error) {
	panic(pendiente.Implementar("events.Dispatcher.Build"))
}

// Offering es lo que hay que decirle al contacto y lo que hay que recordar para
// entender su respuesta: el texto que se manda y el menú que se persiste.
//
// Los dos juntos y no por separado porque son inseparables: mandar el texto sin
// guardar el menú deja al cliente tecleando un número que ya no significa nada.
type Offering struct {
	// Text es el saliente ya renderizado. Vacío si y solo si Empty().
	Text string
	// Menu es lo que hay que guardar para resolver la respuesta (Menu.Resolve).
	Menu Menu
}

// Empty reporta que NO hay ni una sola opción que ofrecer, y es el caso que el
// llamante DEBE distinguir: sin nada que ofrecer no se emite automensaje de rescate
// (T3.6) y la rama Fallback cae al flujo de fallback de siempre (T3.8 · punto 4,
// INV-20). Un Offering vacío trae Text vacío: no hay «texto sin opciones».
func (o Offering) Empty() bool {
	panic(pendiente.Implementar("events.Offering.Empty"))
}

// BuildRescue arma el AUTOMENSAJE DE RESCATE (T3.6): «no hay nada en curso» + lo
// que el contacto dejó a medias, numerado y ordenado por última actividad, + cómo
// retomarlo.
//
// Determinista y SIN CLASIFICADOR: sale de una consulta y del vocabulario de tipos,
// no de un modelo. Lo que se enseña se nombra por tipo, nunca por identificador
// (E-3).
//
// Sin rescatables devuelve el Offering VACÍO y no un mensaje diciendo que no hay
// nada: ese aviso sería ruido para quien escribe por primera vez. Quien llama
// comprueba Empty().
//
// No mira si están vencidos ni filtra por ello (INV-19): a este mensaje se llega
// PORQUE la ventana venció, y lo que se lista es todo lo rescatable — un evento
// dentro de su ventana que también estuviera abierto seguiría siendo suyo.
//
// Enseña como mucho 5 (el tope) y pide a la base 6 (el lote): si llegan más de 5, se enseñan los 5
// primeros —en el orden en que los da ListRescuable, última actividad primero— y el texto cierra
// con «…y N más», donde N es lo que sobró del lote. Las opciones son todas ActionResume, numeradas
// desde 1. El gate de features es el de Build, y sus errores también.
//
// El texto, literal: la cabecera, una línea en blanco, las opciones, una línea en blanco y el
// cierre:
//
//	Pasó un rato sin novedades, así que ahora mismo no tenemos nada en curso. Si quieres, puedes retomar lo que dejaste a medias — responde con el número:
//
//	1. Retomar el pedido que dejaste a medias
//	2. Continuar la encuesta que dejaste a medias
//
//	Si prefieres otra cosa, escríbelo y te ayudamos.
//
// Si sobran, la línea «…y N más» va entre las opciones y el cierre, con una línea en blanco ANTES
// y NINGUNA después (el cierre queda pegado a ella; así lo hace el código viejo, cuyo comentario
// decía «cada una separada por una línea en blanco»: se porta la conducta). El final del texto es
// entonces, literal, "5. Retomar: tipo4\n\n…y 1 más\nSi prefieres otra cosa, escríbelo y te ayudamos.".
//
// Nunca aparece un UUID, un history_id, el nombre técnico del tipo, «carrito» ni «evento» (E-3).
func (d *Dispatcher) BuildRescue(ctx context.Context, ref ConversationRef) (Offering, error) {
	panic(pendiente.Implementar("events.Dispatcher.BuildRescue"))
}

// BuildOpening arma la ENTRADA de una conversación SIN evento (T3.8 · puntos 1 y
// 3): los tipos que el tenant ofrece, numerados, y —solo si hay algo que retomar—
// una ÚNICA entrada final que lo colapsa todo, «Retomar algo que dejaste a medias
// (N)».
//
// Una sola entrada y no los rescatables enumerados: la conversación se abre
// OFRECIENDO lo que se puede hacer, y mezclar ahí la lista de lo pendiente haría
// que el mismo tipo apareciera dos veces con dos sentidos antes de que el cliente
// haya dicho nada. Quien elija esa entrada recibe la lista (BuildRescue).
//
// No consulta el reloj ni escribe nada: abrir una conversación no crea evento
// (E-6). Y no consulta al clasificador: este es el camino SIN LLM (REQ-21); la
// coletilla del camino con LLM es BuildTagline.
//
// Vacío (Empty) cuando no hay ni tipos ofrecidos ni rescatables: ahí es donde el
// llamante conserva el fallback del tenant (INV-20).
//
// Las opciones: una ActionStart por tipo ofrecido (sin el menú, sin repetir, pasadas por el gate) y,
// si hay algún rescatable, UNA final con Action = ActionRescue y Count = cuántos reveló el lote
// (como mucho 6), sin Kind ni EventID. El texto es Menu.Render() de ese menú: la entrada final se
// lee «Retomar algo que dejaste a medias (N)». El gate y los textos de error son los de Build.
func (d *Dispatcher) BuildOpening(ctx context.Context, ref ConversationRef) (Offering, error) {
	panic(pendiente.Implementar("events.Dispatcher.BuildOpening"))
}

// BuildTagline arma la COLETILLA del camino CON clasificador (T3.8 · punto 2): la
// frase que se añade al final de la respuesta que ya atendió la intención del
// cliente, nombrando por tipo lo que dejó a medias.
//
// Devuelve "" cuando no hay nada que retomar, para que quien la añade no tenga que
// preguntar dos veces.
//
// Dos cosas que NO hace y son del llamante: no comprueba que el tenant tenga el
// clasificador (quien atendió la intención ya lo sabía) y no recuerda si ya la
// añadió — la coletilla se genera UNA VEZ POR CONVERSACIÓN, y esa memoria vive
// donde vive el estado de la conversación, no en un componente que solo lee.
//
// El texto, literal: con un rescatable, "Por cierto, tu pedido sigue a medias — dime si quieres
// retomarlo."; con varios, "Por cierto, tu pedido y tu encuesta siguen a medias — dime si quieres
// retomarlos." (los nombres son KindName de cada tipo con «tu» delante, unidos con comas y una
// «y» final, en el orden de ListRescuable). Con más de 5 se nombran 5 y se añade " y algo más".
// El gate y sus errores son los de Build; Menu.Unfiltered no tiene aquí por dónde salir.
func (d *Dispatcher) BuildTagline(ctx context.Context, ref ConversationRef) (string, error) {
	panic(pendiente.Implementar("events.Dispatcher.BuildTagline"))
}
