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
	deposits DepositTouch
	crm      CRMPusher
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

// DepositTouch evalúa el recordatorio PEREZOSO de la seña sobre las solicitudes que
// una lectura acaba de tocar (D-041.12, T4.4). Lo satisface *DepositReminder.
//
// NO devuelve error, exactamente por lo mismo que StatusNotifier: si pudiera, el
// primer llamante que lo propagara convertiría el LISTADO del dueño en un 500 porque
// el teléfono de un cliente estaba apagado. Una lectura no puede fallar por un
// mensaje que no salió.
type DepositTouch interface {
	Remind(ctx context.Context, tenantID string, touched []Intake)
}

// CRMPusher empuja al puente CRM del tenant la revisión de una solicitud que
// ACABA de escribirse (Plan 044 · Ola 4 · Tanda 2). Lo satisface
// *crmpush.RevisionPusher.
//
// 🔴 ES UN PUERTO LOCAL, Y NO UN IMPORT, POR UN CICLO REAL. La pieza que arma y
// encola el contrato vive en internal/integrations/crmpush, y ESE paquete importa
// a éste (necesita NormalizeStatus: el contrato wapp-crm-v1 jamás emite `closed`).
// Si el Service importara crmpush para llamarlo, el ciclo sería directo —
// intakes → crmpush → intakes— y no compilaría. Declarar aquí la forma que se
// necesita es exactamente lo que ya hacen StatusNotifier, DepositTouch,
// Destinations y SettingsReader: este paquete describe a sus colaboradores, no los
// importa.
//
// NO devuelve error, y el motivo es MÁS FUERTE que el de sus dos hermanos. Ahí el
// argumento es «una escritura aplicada no se deshace porque el teléfono del cliente
// esté apagado»; aquí, además, las escrituras que disparan el empuje NO SON
// IDEMPOTENTES: ReplaceItems escribe una revisión NUEVA en cada llamada. Un error
// propagado le devolvería un 500 al dueño por un encolado fallido, el dueño
// reintentaría, y el reintento escribiría la revisión N+1 — dos revisiones para una
// sola corrección. Perder un empuje es malo; fabricar una revisión de más por
// intentar recuperarlo es peor.
//
// ⚠️ LA CONSECUENCIA HAY QUE SABERLA: un encolado que falla NO se reintenta. La
// durabilidad de webhook_outbox empieza cuando la fila ENTRA; antes de eso no hay
// red. El fallo queda en el log con su intake_id.
type CRMPusher interface {
	PushRevision(ctx context.Context, tenantID string, d Detail, revisionNo int)
}

// Option configura el Service al construirlo.
type Option func(*Service)

// WithNotifier cablea el aviso al cliente de cada transición aplicada (T4.2). Sin
// esta opción el servicio funciona igual y NO manda nada: es lo que hace que un
// test de dominio no le haga sonar el teléfono a nadie por accidente.
func WithNotifier(n StatusNotifier) Option {
	return func(s *Service) { s.notifier = n }
}

// WithDepositReminder cablea el recordatorio PEREZOSO de la seña a las LECTURAS del
// dueño (T4.4). Sin esta opción, List y Get son exactamente las lecturas puras que
// eran —ni una sentencia de más, ni un mensaje—, que es lo que mantiene honestos
// tanto los tests de dominio como cualquier consumidor que solo quiera leer.
//
// POR QUÉ AQUÍ Y NO EN EL HANDLER. «Tocar la solicitud dispara el recordatorio» es
// una regla del DOMINIO de las solicitudes, no del transporte: colgarla del handler
// HTTP la dejaría fuera de cualquier otro lector (el puente del Plan 042, una
// pantalla futura, un caso de uso interno) y la haría depender de que cada uno se
// acuerde de copiar la llamada. Colgada de la lectura, viaja con ella.
//
// El precio —una lectura deja de ser pura— se paga con la MISMA moneda que ya paga
// SetStatus con su notificador: colaborador opcional, no puede devolver error, no
// puede tumbar al llamante, y sin cablear no existe.
func WithDepositReminder(d DepositTouch) Option {
	return func(s *Service) { s.deposits = d }
}

// WithCRMPusher cablea el empuje al puente CRM de las revisiones que el DUEÑO
// escribe desde su consola (Plan 044 · Ola 4 · Tanda 2). Sin esta opción el
// servicio funciona igual y no encola nada — lo mismo que promete WithNotifier
// para el teléfono del cliente.
//
// POR QUÉ AQUÍ Y NO EN EL HANDLER, que es la parte que decide el diseño. Hasta esta
// tarea el ÚNICO productor de `intake.push` era el WebhookSink del motor de flujos,
// que solo reacciona al cierre del carrito: cualquier ruta nueva que moviera una
// solicitud tenía que ACORDARSE de encolar por su cuenta, y «acordarse en cada
// sitio» es exactamente cómo nacen los defectos que este plan lleva dos olas
// pagando. Colgado del Service, el empuje viaja con la escritura y una puerta nueva
// lo hereda sin copiar una línea.
//
// El precio es el mismo que ya paga SetStatus con su notificador: colaborador
// opcional, no puede devolver error, no puede tumbar al llamante, y sin cablear no
// existe.
func WithCRMPusher(p CRMPusher) Option {
	return func(s *Service) { s.crm = p }
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
//
// Es además uno de los tres TOQUES que evalúan el recordatorio de la seña (D-041.12,
// T4.4): el dueño abriendo su bandeja es lo que hace de reloj, porque en esta
// plataforma no hay ninguno (ADR-0003). El toque va DESPUÉS de tener la página —solo
// se evalúa lo que de verdad se leyó— y no puede alterar lo que se devuelve.
func (s *Service) List(ctx context.Context, tenantID string, f Filter) (Page, error) {
	f = f.Normalized()
	items, total, err := s.store.List(ctx, tenantID, f)
	if err != nil {
		return Page{}, err
	}
	if items == nil {
		items = []Intake{} // la UI itera sin ramificar por el nulo
	}
	s.touch(ctx, tenantID, items)
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
//
// Es el segundo TOQUE del recordatorio de la seña (ver List): abrir la solicitud
// concreta es la lectura más específica que existe y, si esa es justo la que tiene
// la seña vencida, es donde antes se nota.
func (s *Service) Get(ctx context.Context, tenantID, intakeID string) (Detail, error) {
	detail, err := s.store.Get(ctx, tenantID, intakeID)
	if err != nil {
		return Detail{}, err
	}
	s.touch(ctx, tenantID, []Intake{detail.Intake})
	return detail, nil
}

// touch evalúa el recordatorio de la seña sobre lo que una lectura acaba de leer.
// Sin la opción cableada (el default, y lo que usan todos los tests de dominio) no
// hace nada: la lectura sigue siendo tan pura como antes de T4.4.
//
// NO se llama desde ListDetails ni Summary a propósito, aunque también leen
// solicitudes: son el EXPORT y el resumen, caminos de datos masivos que un dueño
// dispara para llevarse una hoja de cálculo. Que descargar un CSV le mande WhatsApps
// a sus clientes sería una sorpresa desagradable, y encima sin cota útil (ahí no hay
// página: son hasta MaxExportIntakes solicitudes).
func (s *Service) touch(ctx context.Context, tenantID string, touched []Intake) {
	if s.deposits == nil || len(touched) == 0 {
		return
	}
	s.deposits.Remind(ctx, tenantID, touched)
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
//
// `notice` dice QUIÉN da ese aviso (ver StatusNotice en notifier.go). El
// <select> de estado de la consola pasa NoticeToClient y se comporta exactamente
// como antes; una puerta que ya le escribe al cliente con su propio texto pasa
// NoticeByCaller y la plataforma se calla en esa transición. La transición se
// aplica IGUAL en los dos casos: callarse no es no registrar.
func (s *Service) SetStatus(ctx context.Context, tenantID, intakeID, to string, notice StatusNotice) (Intake, error) {
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
	s.notify(ctx, tenantID, updated, from, notice)
	return updated, nil
}

// AbandonByEvent deja en `abandoned` la solicitud que colgaba del evento `eventID`
// (D-043.21, Ola 4.5 · T4.5.5(a)). Es la puerta que el motor consume por el puerto
// IntakeAbandoner desde que la FK se invirtió: el runtime cancela un evento y pide
// abandonar SU contenido sin conocer ningún id de hijo — la columna
// conversation_events.intake_id de la que dependía el viejo Abandon(intakeID)
// murió en la 0054, y con ella se retiró ese método (Ola 4.5, cableado final).
//
// La idempotencia que aquel exigía vive ahora en el SQL: 0 filas —ya
// abandonada, ya resuelta, o un evento sin contenido (menu/survey)— es ÉXITO, y el
// guard `status='open'` del store garantiza que una `confirmed` jamás se abandona
// por aquí. NO notifica al cliente a propósito, como ninguna de las puertas del
// abandono por cancelación lo hacía de forma efectiva: el aviso de NotifyStatus
// cuelga de las transiciones del OPERADOR (SetStatus), y la muerte del evento ya se
// la contó al cliente el propio flujo.
//
// ⚠️ LEGADO REGISTRADO: una solicitud pre-0054 (event_id NULL) es INALCANZABLE por
// esta puerta — no declara padre, así que ningún eventID la encuentra. Ese hueco es
// el mismo que ya tenía: nadie podía llegar a ella desde un evento cuando la
// ligadura vivía (sin escribirse) en el padre.
func (s *Service) AbandonByEvent(ctx context.Context, tenantID, eventID string) error {
	return s.store.AbandonByEvent(ctx, tenantID, eventID)
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
//
// El CUARTO motivo para callar lo trae el llamante (D-044.49): con NoticeByCaller
// la transición se aplica y el aviso genérico no sale, porque quien la pidió ya le
// escribió al cliente con su propio texto. Va PRIMERO —antes del notificador y
// antes de la guarda del destino— porque es una decisión de producto y no una
// defensa: si el llamante habla, aquí no hay nada que evaluar.
func (s *Service) notify(ctx context.Context, tenantID string, updated Intake, from string, notice StatusNotice) {
	if notice.silencia() {
		return
	}
	if s.notifier == nil {
		return
	}
	if NormalizeStatus(updated.Status) == from {
		return
	}
	s.notifier.NotifyStatus(ctx, tenantID, updated, from)
}

// PushRevisionToCRM encola para el puente CRM del tenant la revisión `revisionNo`
// de la solicitud `d`, con su ciclo de vida REAL (Plan 044 · Ola 4 · Tanda 2).
//
// La llama la escritura que acaba de parir esa revisión —dentro de este Service, no
// desde el handler—: es el mismo criterio que notify, que cuelga del compare-and-swap
// ganador y no de la petición. Sin CRMPusher cableado no hace nada.
//
// 🔴 EL `revisionNo` ES OBLIGATORIO Y EXPLÍCITO, Y AHÍ ESTÁ TODO EL DISEÑO. El
// puente hace UPSERT por (intake_id, revision_no) y trata como DUPLICADO todo par
// repetido (manual del integrador §4), así que el número no es un adorno del
// documento: es lo que distingue «esto es un estado nuevo» de «esto ya lo sabía».
// Se pide por parámetro en vez de deducirlo de d.Revisions porque solo el llamante
// sabe cuál de ellas acaba de escribir, y porque un empuje sin revisión NUEVA es un
// empuje que el puente descarta en silencio.
//
// ⚠️ CONSECUENCIA QUE NO SE TAPA: webhook_outbox NO tiene unicidad por
// (intake_id, revision_no) —la 0046 deja la idempotencia al receptor, y su COMMENT
// lo dice—, así que llamar dos veces con el MISMO número encola dos filas y el
// puente recibe dos entregas. Lo que impide que eso pase no es la base: es colgar
// esta llamada de la escritura que numeró la revisión, una vez por revisión.
func (s *Service) PushRevisionToCRM(ctx context.Context, tenantID string, d Detail, revisionNo int) {
	if s.crm == nil {
		return
	}
	s.crm.PushRevision(ctx, tenantID, d, revisionNo)
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
