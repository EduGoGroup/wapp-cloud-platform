package events

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
)

// ErrNoResolver lo devuelve Build si el despachador se construyó sin Resolver de
// entitlements. Falla en vez de listarlo todo: sin resolver no se puede saber qué
// tiene contratado el tenant, y «no pude averiguarlo» no debe parecerse a «lo
// tiene» (fail-closed, la misma regla que RequireFeature).
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

// kindMenu es el tipo del propio despachador (D-043.3: el menú es un evento
// kind='menu'). Se nombra aquí porque el menú NO se ofrece a sí mismo.
const kindMenu = "menu"

// featurePorTipo mapea cada tipo de evento a la feature del Plan 040 que lo
// habilita. Las claves son constantes del paquete entitlements, no literales:
// un typo entre el SQL sembrado y el Go apagaría un tipo del menú en silencio, y
// el test de integración de la siembra ata las dos puntas.
//
// Un tipo que NO esté aquí no se filtra: gatear con una feature inventada sería
// peor que no gatear, porque nadie la tendría nunca y el tipo desaparecería del
// menú sin que ninguna tabla lo explique.
var featurePorTipo = map[string]string{
	kindMenu: entitlements.FeatureMenu,
	"cart":   entitlements.FeatureCartBasic,
	"survey": entitlements.FeatureSurvey,
	"media":  entitlements.FeatureMedia,
}

// Dispatcher arma el menú numérico dinámico del nivel superior (T2.3) y no hace
// nada más: SOLO LEE. No escribe en conversation_events ni en flow_state.
//
// Reparto de trabajo con el runtime, que es lo que hace testeable esto sin BD:
// aquí se decide QUÉ se le ofrece al cliente y qué significa el número que
// responda; allí se ejecuta la consecuencia (crear, conmutar, cerrar).
type Dispatcher struct {
	events RescuableLister
	kinds  KindOffer
	feats  entitlements.Resolver
}

// NewDispatcher construye el despachador sobre sus tres fuentes: los eventos
// rescatables del contacto, los tipos que el tenant ofrece y los derechos del
// tenant.
func NewDispatcher(ev RescuableLister, kinds KindOffer, feats entitlements.Resolver) *Dispatcher {
	return &Dispatcher{events: ev, kinds: kinds, feats: feats}
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
// ORDEN, que es una decisión de presentación (ver porNacimiento).
func (d *Dispatcher) Build(ctx context.Context, ref ConversationRef) (Menu, error) {
	permite, sinFiltro, err := d.gate(ctx, ref.TenantID)
	if err != nil {
		return Menu{}, err
	}
	offered, err := d.kinds.OfferedKinds(ctx, ref.TenantID, ref.SessionID)
	if err != nil {
		return Menu{}, fmt.Errorf("events: listar los tipos que ofrece el tenant: %w", err)
	}
	resc, err := d.rescatables(ctx, ref, permite)
	if err != nil {
		return Menu{}, err
	}

	return Menu{
		Options:    componer(porNacimiento(eventosDe(resc)), offered, permite),
		Unfiltered: sinFiltro,
	}, nil
}

// porNacimiento devuelve los eventos en el orden en que APARECIERON, que es el del
// menú: los números que el cliente tiene delante no pueden bailar entre dos
// mensajes porque haya escrito en uno de sus eventos (el rescate, en cambio, los
// quiere por última actividad — lo último que se tocó es lo primero que se ofrece
// retomar).
//
// El orden se pone AQUÍ y el filtro en la consulta a propósito: filtrar es una
// regla de negocio que tiene que valer para todos los que lean, y ordenar es una
// decisión de quien enseña. Poner los dos en la BD costaría una segunda consulta
// cuyo WHERE habría que mantener idéntico al primero, que es exactamente el bicho
// que INV-17 acaba de destapar.
func porNacimiento(evs []Event) []Event {
	slices.SortStableFunc(evs, func(a, b Event) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID) // desempate estable: dos eventos del mismo instante
	})
	return evs
}

// componer numera las opciones: primero lo que se puede pedir (todos los tipos
// ofrecidos, tengan o no evento vivo) y después lo rescatable (un evento vivo,
// una opción). La numeración es densa y arranca en 1.
func componer(alive []Event, offered []string, permite func(string) bool) []MenuOption {
	return numerar(append(opcionesStart(offered, permite), opcionesResume(alive, permite)...))
}

// opcionesStart son las opciones de PEDIR: un tipo ofrecido, una opción. Sin
// numerar todavía — numera quien compone la lista final, que es quien sabe qué más
// va a haber en ella.
func opcionesStart(offered []string, permite func(string) bool) []MenuOption {
	opts := make([]MenuOption, 0, len(offered))
	visto := make(map[string]bool, len(offered))
	for _, k := range offered {
		if k == kindMenu || visto[k] || !permite(k) {
			continue
		}
		visto[k] = true // dedupe: un tipo ofrecido dos veces es una opción, no dos
		opts = append(opts, MenuOption{Action: ActionStart, Kind: k})
	}
	return opts
}

// opcionesResume son las opciones de RETOMAR: un evento, una opción.
//
// Los rescatables NO se deduplican contra los ofrecidos: aparecer dos veces es el
// diseño. Tampoco entre sí hace falta —el único parcial (E-2) garantiza como mucho
// un vivo por tipo—, y uno de un tipo que el tenant ya no ofrece sigue siendo
// rescatable: lo que se dejó a medias no depende de la configuración de hoy.
func opcionesResume(rescatables []Event, permite func(string) bool) []MenuOption {
	opts := make([]MenuOption, 0, len(rescatables))
	for _, ev := range rescatables {
		if ev.Kind == kindMenu || !permite(ev.Kind) {
			continue
		}
		opts = append(opts, MenuOption{Action: ActionResume, Kind: ev.Kind, EventID: ev.ID})
	}
	return opts
}

// numerar pone los números que el cliente teclea: densos y empezando en 1. Sin
// opciones devuelve nil y no un slice vacío, porque Menu.Empty y el render razonan
// sobre «no hay nada que ofrecer», no sobre la capacidad del slice.
func numerar(opts []MenuOption) []MenuOption {
	if len(opts) == 0 {
		return nil
	}
	for i := range opts {
		opts[i].Number = i + 1
	}
	return opts
}

// gate devuelve el predicado «este tipo se puede ofrecer» y si el menú quedó SIN
// filtrar.
//
// Resuelve los derechos de UNA pasada con ListEffective en vez de un Has por
// tipo: el menú se arma para un solo tenant y las cuatro preguntas comparten
// respuesta, así que una consulta basta y el resultado no puede quedar
// incoherente entre tipos.
//
// El caso «taxonomía sin poblar» (el tenant no tiene NINGUNA feature efectiva)
// lista SIN filtro, por criterio explícito del Plan 043 · T2.3: una instalación
// donde el Plan 040 nunca sembró planes no debe quedarse con un menú vacío y sin
// explicación. Es la ÚNICA excepción al fail-closed, y se reporta hacia arriba
// (Menu.Unfiltered) para que quede anotada en vez de pasar inadvertida.
//
// Un fallo del resolver NO es ese caso: se propaga. Un error de infraestructura
// que abriera capacidades de pago sería peor que un menú que no sale.
func (d *Dispatcher) gate(ctx context.Context, tenantID string) (permite func(string) bool, sinFiltro bool, err error) {
	todo := func(string) bool { return true }
	if d.feats == nil {
		return nil, false, ErrNoResolver
	}

	_, features, err := d.feats.ListEffective(ctx, tenantID)
	if err != nil {
		return nil, false, fmt.Errorf("events: resolver los derechos del tenant para el menú: %w", err)
	}
	if len(features) == 0 {
		return todo, true, nil
	}

	return func(kind string) bool {
		feature, gateado := featurePorTipo[kind]
		if !gateado {
			return true
		}
		return slices.Contains(features, feature)
	}, false, nil
}

// ── Lo que se le ofrece al contacto: rescate (T3.6) y entrada (T3.8) ─────────

const (
	// topeRescatables es cuántos rescatables se le ENSEÑAN al cliente como mucho.
	// Es la constante única del tope: si aparece un 5 suelto en otro sitio, uno de
	// los dos está mintiendo. Cinco es propuesta nuestra (T3.8 · punto 3): una
	// lista más larga en WhatsApp deja de leerse.
	topeRescatables = 5
	// loteRescatables es cuántos se le PIDEN a la BD: uno más que el tope. Ese uno
	// de más es todo lo que hace falta para saber que había más sin contarlos —de
	// ahí sale el «…y N más» sin una segunda consulta.
	loteRescatables = topeRescatables + 1
)

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
func (o Offering) Empty() bool { return o.Menu.Empty() }

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
func (d *Dispatcher) BuildRescue(ctx context.Context, ref ConversationRef) (Offering, error) {
	permite, sinFiltro, err := d.gate(ctx, ref.TenantID)
	if err != nil {
		return Offering{}, err
	}
	resc, err := d.rescatables(ctx, ref, permite)
	if err != nil {
		return Offering{}, err
	}

	sobran := 0
	if len(resc) > topeRescatables {
		sobran = len(resc) - topeRescatables
		resc = resc[:topeRescatables]
	}

	m := Menu{Options: numerar(opcionesResume(eventosDe(resc), permite)), Unfiltered: sinFiltro}
	return Offering{Text: m.renderList(rescueHeader, rescueMore(sobran), menuFooter), Menu: m}, nil
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
func (d *Dispatcher) BuildOpening(ctx context.Context, ref ConversationRef) (Offering, error) {
	permite, sinFiltro, err := d.gate(ctx, ref.TenantID)
	if err != nil {
		return Offering{}, err
	}
	offered, err := d.kinds.OfferedKinds(ctx, ref.TenantID, ref.SessionID)
	if err != nil {
		return Offering{}, fmt.Errorf("events: listar los tipos que ofrece el tenant: %w", err)
	}
	resc, err := d.rescatables(ctx, ref, permite)
	if err != nil {
		return Offering{}, err
	}

	opts := opcionesStart(offered, permite)
	if len(resc) > 0 {
		// El N es lo que reveló el lote (como mucho loteRescatables): con más, dice
		// «6» y la lista dirá «…y 1 más». Los dos números salen de la misma lectura,
		// así que el cliente nunca lee dos cuentas que se contradigan.
		opts = append(opts, MenuOption{Action: ActionRescue, Count: len(resc)})
	}

	m := Menu{Options: numerar(opts), Unfiltered: sinFiltro}
	return Offering{Text: m.Render(), Menu: m}, nil
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
func (d *Dispatcher) BuildTagline(ctx context.Context, ref ConversationRef) (string, error) {
	permite, _, err := d.gate(ctx, ref.TenantID)
	if err != nil {
		return "", err
	}
	resc, err := d.rescatables(ctx, ref, permite)
	if err != nil {
		return "", err
	}
	kinds := make([]string, 0, len(resc))
	for _, r := range resc {
		kinds = append(kinds, r.Kind)
	}
	return tagline(kinds), nil
}

// rescatables lee el lote de rescatables de la conversación y aparta los tipos que
// el tenant no tiene contratados.
//
// El gate se aplica DESPUÉS del lote y no dentro de la consulta a propósito: las
// features son de la nube y cambian sin tocar la BD del evento, y meterlas en el
// SQL obligaría a la consulta —que también sirve al listado del dueño (T3.9b)— a
// saber de planes comerciales.
func (d *Dispatcher) rescatables(ctx context.Context, ref ConversationRef, permite func(string) bool) ([]Rescuable, error) {
	resc, err := d.events.ListRescuable(ctx, ref.TenantID, ref.SessionID, ref.ContactID, loteRescatables)
	if err != nil {
		return nil, fmt.Errorf("events: listar los eventos rescatables del contacto: %w", err)
	}
	out := make([]Rescuable, 0, len(resc))
	for _, r := range resc {
		if r.Kind == kindMenu || !permite(r.Kind) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// eventosDe desnuda los rescatables hasta el evento: la marca «vencido» informa al
// dueño (T3.9b), no al cliente, y las opciones se componen igual con o sin ella.
func eventosDe(resc []Rescuable) []Event {
	evs := make([]Event, 0, len(resc))
	for _, r := range resc {
		evs = append(evs, r.Event)
	}
	return evs
}
