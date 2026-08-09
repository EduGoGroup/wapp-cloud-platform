package events

import (
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

// AliveLister lee los eventos VIVOS de una conversación. Lo satisface *Store.
//
// Es un puerto de LECTURA a propósito y por eso no expone nada más: el
// despachador decide, no ejecuta. Quien crea, conmuta o cierra eventos es el
// runtime (T2.2/T2.4), para que ese cableado viva en un solo sitio.
type AliveLister interface {
	ListAlive(ctx context.Context, tenantID, sessionID, contactID string) ([]Event, error)
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
	events AliveLister
	kinds  KindOffer
	feats  entitlements.Resolver
}

// NewDispatcher construye el despachador sobre sus tres fuentes: los eventos
// vivos del contacto, los tipos que el tenant ofrece y los derechos del tenant.
func NewDispatcher(ev AliveLister, kinds KindOffer, feats entitlements.Resolver) *Dispatcher {
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
func (d *Dispatcher) Build(ctx context.Context, ref ConversationRef) (Menu, error) {
	alive, err := d.events.ListAlive(ctx, ref.TenantID, ref.SessionID, ref.ContactID)
	if err != nil {
		return Menu{}, fmt.Errorf("events: listar los eventos vivos del contacto: %w", err)
	}
	offered, err := d.kinds.OfferedKinds(ctx, ref.TenantID, ref.SessionID)
	if err != nil {
		return Menu{}, fmt.Errorf("events: listar los tipos que ofrece el tenant: %w", err)
	}
	permite, sinFiltro, err := d.gate(ctx, ref.TenantID)
	if err != nil {
		return Menu{}, err
	}

	return Menu{Options: componer(alive, offered, permite), Unfiltered: sinFiltro}, nil
}

// componer numera las opciones: primero lo que se puede pedir (todos los tipos
// ofrecidos, tengan o no evento vivo) y después lo rescatable (un evento vivo,
// una opción). La numeración es densa y arranca en 1.
func componer(alive []Event, offered []string, permite func(string) bool) []MenuOption {
	opts := make([]MenuOption, 0, len(alive)+len(offered))

	visto := make(map[string]bool, len(offered))
	for _, k := range offered {
		if k == kindMenu || visto[k] || !permite(k) {
			continue
		}
		visto[k] = true // dedupe: un tipo ofrecido dos veces es una opción, no dos
		opts = append(opts, MenuOption{Action: ActionStart, Kind: k})
	}
	// Los vivos NO se deduplican contra los ofrecidos: aparecer dos veces es el
	// diseño. Tampoco entre sí hace falta —el único parcial (E-2) garantiza como
	// mucho un vivo por tipo—, y un vivo de un tipo que el tenant ya no ofrece
	// sigue siendo rescatable: lo que se dejó a medias no depende de la
	// configuración de hoy.
	for _, ev := range alive {
		if ev.Kind == kindMenu || !permite(ev.Kind) {
			continue
		}
		opts = append(opts, MenuOption{Action: ActionResume, Kind: ev.Kind, EventID: ev.ID})
	}

	for i := range opts {
		opts[i].Number = i + 1
	}
	if len(opts) == 0 {
		return nil
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
