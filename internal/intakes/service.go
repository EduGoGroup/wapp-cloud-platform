package intakes

import (
	"context"
	"time"
)

// Service es la capa de dominio de las solicitudes: aplica las reglas (paginación
// acotada, normalización de estados, máquina de estados) sobre un Store. No sabe
// de HTTP y no toma decisiones de transporte; el handler traduce sus errores a
// códigos.
type Service struct {
	store    Store
	notifier StatusNotifier
}

// StatusNotifier avisa al CLIENTE de que su solicitud cambió de estado (D-041.14).
// Lo satisface *Notifier.
//
// NO devuelve error, y eso es el contrato entero: un aviso que no sale no puede
// tumbar una transición que ya está escrita en la base. Si esta firma ganara un
// error, el primer llamante que lo propagara le devolvería un 500 al dueño por un
// pedido que SÍ cambió de estado — y el dueño reintentaría, chocaría con un 422 por
// estar ya en el destino, y acabaría creyendo que no se aplicó. Ver notifier.go.
type StatusNotifier interface {
	NotifyStatus(ctx context.Context, tenantID string, in Intake, from string)
}

// Option configura el Service al construirlo.
type Option func(*Service)

// WithNotifier cablea el aviso al cliente de cada transición aplicada (T4.2). Sin
// esta opción el servicio funciona igual y NO manda nada: es lo que hace que un
// test de dominio no le haga sonar el teléfono a nadie por accidente.
func WithNotifier(n StatusNotifier) Option {
	return func(s *Service) { s.notifier = n }
}

// NewService construye el servicio sobre el store dado.
func NewService(store Store, opts ...Option) *Service {
	s := &Service{store: store}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// List devuelve la página de solicitudes del tenant que casan con el filtro, con
// el total de coincidencias sin paginar. Sanea la paginación (Filter.Normalized)
// antes de consultar: el llamante no puede pedir 100k filas de un golpe.
func (s *Service) List(ctx context.Context, tenantID string, f Filter) (Page, error) {
	f = f.Normalized()
	items, total, err := s.store.List(ctx, tenantID, f)
	if err != nil {
		return Page{}, err
	}
	if items == nil {
		items = []Intake{} // la UI itera sin ramificar por el nulo
	}
	return Page{Intakes: items, Page: f.Page, PageSize: f.PageSize, Total: total}, nil
}

// ListDetails devuelve TODAS las solicitudes del filtro con sus líneas, sin
// paginar: es lo que consumen el export y el summary. La cota de paginación no
// aplica aquí (una hoja de cálculo partida en páginas no sirve), así que la pone
// MaxExportIntakes.
//
// Se pide UNA solicitud MÁS que la cota justamente para poder distinguir "cabe
// justo" de "se pasa": si el store devolviera exactamente la cota no habría forma
// de saber si sobraban filas, y el export saldría recortado sin avisar.
func (s *Service) ListDetails(ctx context.Context, tenantID string, f Filter) ([]Detail, error) {
	details, err := s.store.ListDetails(ctx, tenantID, f, MaxExportIntakes+1)
	if err != nil {
		return nil, err
	}
	if len(details) > MaxExportIntakes {
		return nil, ErrTooLarge
	}
	return details, nil
}

// Summary agrega las solicitudes del filtro (totales, desglose por estado, ranking
// de artículos y el detalle crudo). Lee por el MISMO camino que el export —misma
// cota incluida— y delega la aritmética en BuildSummary, que es pura.
func (s *Service) Summary(ctx context.Context, tenantID string, f Filter) (Summary, error) {
	details, err := s.ListDetails(ctx, tenantID, f)
	if err != nil {
		return Summary{}, err
	}
	return BuildSummary(details, f.Normalized(), time.Now()), nil
}

// Get devuelve la solicitud con sus líneas. ErrNotFound si no es del tenant (404
// opaco, INV-8).
func (s *Service) Get(ctx context.Context, tenantID, intakeID string) (Detail, error) {
	return s.store.Get(ctx, tenantID, intakeID)
}

// SetStatus aplica una transición del ciclo de vida y devuelve la solicitud ya
// transicionada. El orden importa:
//
//  1. lee el estado actual (ErrNotFound si la solicitud no es del tenant): el
//     recurso se resuelve ANTES que el cuerpo, para no revelar por el código de
//     error si una solicitud ajena existe;
//  2. valida la transición contra la máquina de estados (*TransitionError con el
//     estado actual y los destinos permitidos, que el handler publica en el 422).
//     Un destino DESCONOCIDO cae por aquí sin caso aparte: no está en el mapa, así
//     que no es alcanzable desde ningún origen, y el llamante recibe la misma
//     respuesta útil — dónde está y adónde puede ir;
//  3. escribe con compare-and-swap sobre el estado leído (ErrConflict si otro
//     operador se adelantó entre 1 y 3).
//
// La LÍNEA DE ENVÍO de `pending_approval` (D-041.11) no se ve en este método a
// propósito: no es un efecto colateral que se dispare después, sino parte de la
// escritura del estado, y por eso vive dentro de la misma transacción del store
// (ver Store.UpdateStatus). La Intake que se devuelve ya trae el total con ella.
//
// El AVISO AL CLIENTE (D-041.14, T4.2) es el paso 4 y va DESPUÉS de la escritura,
// nunca antes ni en paralelo: solo se le cuenta a alguien lo que ya es verdad en la
// base. No puede fallar hacia arriba —NotifyStatus no devuelve error— porque una
// transición aplicada no se deshace porque el teléfono del cliente esté apagado.
func (s *Service) SetStatus(ctx context.Context, tenantID, intakeID, to string) (Intake, error) {
	to = NormalizeStatus(to)

	current, err := s.store.Get(ctx, tenantID, intakeID)
	if err != nil {
		return Intake{}, err
	}
	from := NormalizeStatus(current.Status)
	if !CanTransition(from, to) {
		return Intake{}, &TransitionError{From: from, To: to, Allowed: AllowedTransitions(from)}
	}

	updated, err := s.store.UpdateStatus(ctx, tenantID, intakeID, to, StoredVariants(from))
	if err != nil {
		return Intake{}, err
	}
	s.notify(ctx, tenantID, updated, from)
	return updated, nil
}

// notify dispara el aviso al cliente de UNA transición efectivamente aplicada.
//
// Es el punto donde se sostiene «como mucho un mensaje por transición», y lo hace
// colgando el aviso de la ESCRITURA y no de la petición:
//
//   - la transición ya pasó por CanTransition, que rechaza from == to: pedir dos
//     veces `confirmed` sobre algo ya confirmado no llega hasta aquí;
//   - UpdateStatus es un compare-and-swap sobre el estado leído, así que de dos
//     operadores que piden lo mismo a la vez solo UNO escribe; el otro se lleva
//     ErrConflict y sale por el `return` de arriba sin avisar a nadie;
//   - y si aun así el store devolviera algo que no es el destino, la guarda de
//     abajo calla. Es defensa barata contra un store futuro que "arregle" una
//     transición imposible devolviendo el estado actual: al cliente le llegaría un
//     WhatsApp que no corresponde a ningún cambio.
func (s *Service) notify(ctx context.Context, tenantID string, updated Intake, from string) {
	if s.notifier == nil {
		return
	}
	if NormalizeStatus(updated.Status) == from {
		return
	}
	s.notifier.NotifyStatus(ctx, tenantID, updated, from)
}

// EnsureShippingLine garantiza la línea estándar de envío de una solicitud
// (D-041.11) y deja su total cuadrado con las líneas. Es idempotente.
//
// Existe suelta —y no solo dentro de SetStatus— porque hay un segundo momento en
// que la línea tiene que aparecer y no hay transición de por medio: el cierre del
// carrito, que va directo a `confirmed`. Ese llamante usa ShippingOnlyIfZones; el
// del presupuesto, ShippingAlways.
func (s *Service) EnsureShippingLine(ctx context.Context, tenantID, intakeID string, policy ShippingPolicy) error {
	return s.store.EnsureShippingLine(ctx, tenantID, intakeID, policy)
}
