package intakes

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// MemoryStore es un Store en memoria para tests. Reproduce las MISMAS semánticas
// que el store Postgres —rango [From, To), variantes legadas del estado, orden por
// created_at descendente con desempate por id, paginación y total sin paginar— para
// que un test de handler contra este store diga algo verdadero sobre producción.
// Guarda el estado EN CRUDO (como la BD, con su `closed` legado) y normaliza al
// leer: así el camino de normalización se ejercita de verdad.
type MemoryStore struct {
	mu        sync.Mutex
	rows      map[string][]row // por tenant
	items     map[string][]Item
	revisions map[string][]Revision     // por solicitud, en orden de escritura
	zones     map[string][]ShippingZone // por tenant; imita tenant_settings.shipping_zones
	// notify indexa tenant_id → config del aviso al cliente; imita las columnas
	// deposit_template / deposit_due_days de tenant_settings (T4.2).
	notify map[string]NotifySettings
	// events imita public.conversation_events reducida a lo que este dominio mira:
	// eventID → status (`open` | `closed` | `cancelled`). La consumen la guarda
	// `live_event` de Discard (DT-043.2: el criterio real es el estado del evento
	// que la solicitud declara) y el cierre del contenedor (REQ-32e).
	events map[string]string
	// eventOf imita intakes.event_id (D-043.21): intakeID → evento padre. "" o
	// ausencia = fila legada pre-0054 sin ligadura.
	eventOf map[string]string
	// buyerData imita public.intake_buyer_data por solicitud (T4.5). ⚠️ Guarda los
	// valores EN CLARO, y es correcto que lo haga: este store es un doble de tests
	// que vive en memoria y muere con el proceso. Lo que reproduce del store real es
	// la SEMÁNTICA que los tests tienen que poder comprobar —fusión campo a campo y
	// una fila por solicitud—, no el cifrado, que se prueba contra Postgres de
	// verdad (buyerdata_integration_test.go). Nadie debe cablear esto en producción:
	// el proyector recibe un escritor, y en el arranque ese escritor es
	// PostgresBuyerData.
	buyerData map[string]BuyerData
	// now es el reloj con el que se fija deposit_due_at al pedir seña, el equivalente
	// del now() de la transacción en el store real (T4.4). Inyectable con SetClock
	// para que un test pueda fijar un plazo y luego cruzar la fecha sin esperar días.
	//
	// Desde T3.5 es TAMBIÉN el reloj de la retención del literal: fija el CreatedAt
	// de una revisión nueva y mide su edad al leerla. Los dos instantes salen del
	// mismo reloj, que es la condición que LiteralVencido exige — y con SetClock ese
	// reloj es falso de arriba abajo, así que un test puede envejecer una revisión
	// doce meses sin esperar ni sembrar fechas a mano.
	now func() time.Time
	// literals guarda el material de NIVEL 2 de cada revisión APARTE del payload,
	// indexado intakeID → revision_no (T3.5). En claro, y es correcto que lo esté por
	// la misma razón que buyerData: este store vive en memoria y muere con el
	// proceso. Lo que reproduce del real es la SEMÁNTICA que los tests tienen que
	// poder comprobar —que el literal no está en el payload persistido, que vuelve al
	// leer y que la poda lo destruye sin tocar la interpretación—, no el cifrado, que
	// se prueba contra Postgres de verdad.
	literals map[string]map[int]LiteralRevision
	// literalTTL es el equivalente de tenant_settings.intake_literal_ttl_seconds.
	// Arranca en TTLLiteralPorDefecto (12 meses) igual que el DEFAULT de la 0079, y
	// se cambia con SetLiteralTTL. 0 = sin poda, la misma lectura que en la columna.
	literalTTL time.Duration
	// log es por donde sale el evento de poda, igual que en el store real.
	log logger.Logger
}

// row es una solicitud almacenada con su estado tal cual (sin normalizar).
type row struct {
	intake Intake
	status string
}

// NewMemoryStore construye un store en memoria vacío.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		rows:      map[string][]row{},
		items:     map[string][]Item{},
		revisions: map[string][]Revision{},
		zones:     map[string][]ShippingZone{},
		notify:    map[string]NotifySettings{},
		events:    map[string]string{},
		eventOf:   map[string]string{},
		buyerData: map[string]BuyerData{},
		now:       time.Now,

		literals:   map[string]map[int]LiteralRevision{},
		literalTTL: TTLLiteralPorDefecto,
		log:        logger.Default(),
	}
}

// SetLiteralTTL cambia la retención del literal de las revisiones, como haría un
// UPDATE de tenant_settings.intake_literal_ttl_seconds (T3.5). 0 = sin poda.
func (m *MemoryStore) SetLiteralTTL(ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.literalTTL = ttl
}

// SetLogDeRetencion sustituye el logger por el que sale el evento de poda. Existe
// para lo mismo que ConLogDeRetencion en el store real: cablear el de la aplicación
// y, sobre todo, poder OBSERVAR la poda en un test — el criterio de T3.5 exige que
// quede logueada, y un criterio que no se puede observar no se puede verificar.
func (m *MemoryStore) SetLogDeRetencion(l logger.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l != nil {
		m.log = l
	}
}

// guardarRevisiónLocked es el ÚNICO camino de escritura de revisiones de este store
// (lo llaman los cinco sitios que numeran una): parte el literal del payload y lo
// guarda aparte, igual que insertRevisionOnce lo saca antes de tocar la BD.
//
// Sin este punto único, cada uno de los cinco tendría que acordarse de partir el
// payload — y el que se olvidara dejaría el literal en el payload sin que nada
// fallara, que es el modo de fallo silencioso que T3.5 viene a cerrar.
//
// Devuelve la revisión ya numerada, fechada y CON EL PAYLOAD SIN LITERAL: la misma
// forma que devuelve el store real, para que un test contra este doble no vea algo
// que en producción no vería.
func (m *MemoryStore) guardarRevisiónLocked(rev Revision) (Revision, error) {
	limpio, lit, err := PartirLiteral(rev.Payload)
	if err != nil {
		return Revision{}, err
	}
	rev.Payload = limpio
	rev.RevisionNo = len(m.revisions[rev.IntakeID]) + 1
	if rev.CreatedAt.IsZero() {
		rev.CreatedAt = m.now()
	}
	rev.LiteralPrunedAt = time.Time{}
	if !lit.Vacio() {
		if m.literals[rev.IntakeID] == nil {
			m.literals[rev.IntakeID] = map[int]LiteralRevision{}
		}
		m.literals[rev.IntakeID][rev.RevisionNo] = lit
	}
	m.revisions[rev.IntakeID] = append(m.revisions[rev.IntakeID], rev)
	return rev, nil
}

// leerRevisionesLocked es el espejo de revisionsOf del store real: poda lo vencido y
// devuelve el literal a su sitio en lo que sigue vigente.
//
// Devuelve copias: el literal se funde sobre el payload de la COPIA y nunca sobre el
// almacenado, para que dos lecturas seguidas no acumulen. Un error al fundir se
// LOGUEA y deja la revisión con su interpretación sola, en vez de tumbar la lectura
// entera de la bandeja de un doble de tests.
func (m *MemoryStore) leerRevisionesLocked(intakeID string) []Revision {
	guardadas := m.revisions[intakeID]
	out := make([]Revision, 0, len(guardadas))
	for i, rev := range guardadas {
		lit, hay := m.literals[intakeID][rev.RevisionNo]
		switch {
		case !hay:
		case LiteralVencido(m.now().Sub(rev.CreatedAt), m.literalTTL):
			delete(m.literals[intakeID], rev.RevisionNo)
			// Se sella sobre la fila GUARDADA, no sobre la copia: la poda es un
			// hecho persistente y la siguiente lectura tiene que verlo.
			guardadas[i].LiteralPrunedAt = m.now()
			rev.LiteralPrunedAt = guardadas[i].LiteralPrunedAt
			// CERO CONTENIDO, igual que el del store real: lo que se acaba de
			// destruir no puede acabar en un log.
			m.log.Info("retención: literal de la revisión podado por TTL vencido",
				"intake_id", intakeID,
				"revision_no", rev.RevisionNo,
				"edad_segundos", int64(m.now().Sub(rev.CreatedAt).Seconds()),
				"ttl_segundos", int64(m.literalTTL.Seconds()))
		default:
			payload, err := FundirLiteral(rev.Payload, lit)
			if err != nil {
				m.log.Error("retención: no se pudo devolver el literal a la revisión",
					"intake_id", intakeID, "revision_no", rev.RevisionNo, "error", err)
				break
			}
			rev.Payload = payload
		}
		out = append(out, rev)
	}
	return out
}

// PutBuyerField imita PostgresBuyerData.PutBuyerField: FUSIONA el campo en el
// checklist de la solicitud (no lo sustituye) y crea la entrada si no la había.
// Sin cifrar — ver el campo buyerData.
func (m *MemoryStore) PutBuyerField(_ context.Context, intakeID, key, value string) error {
	if key == "" {
		return ErrBuyerFieldEmpty
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.buyerData[intakeID]
	if !ok {
		data = BuyerData{}
		m.buyerData[intakeID] = data
	}
	data[key] = value
	return nil
}

// BuyerDataOf devuelve una COPIA del checklist guardado para la solicitud. Es un
// mirador de tests (el store real no publica ninguna lectura en claro: el
// descifrado está custodiado, ver buyerdata.go).
func (m *MemoryStore) BuyerDataOf(intakeID string) BuyerData {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := BuyerData{}
	for k, v := range m.buyerData[intakeID] {
		out[k] = v
	}
	return out
}

// SetClock fija el reloj del store, como WithReminderClock hace con el del
// recordatorio. Es un mutador para los tests, no parte de ningún puerto.
func (m *MemoryStore) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if now != nil {
		m.now = now
	}
}

// SetDepositTemplate siembra la plantilla de seña del tenant y su plazo, como haría
// un UPDATE de tenant_settings.deposit_template / deposit_due_days. Sin llamarla el
// tenant no tiene plantilla —el DEFAULT de la columna y el estado de arranque de
// cualquier tenant real—, y por tanto no puede pedir seña (ver depositText).
func (m *MemoryStore) SetDepositTemplate(tenantID, template string, dueDays int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notify[tenantID] = NotifySettings{DepositTemplate: template, DepositDueDays: dueDays}
}

// NotifySettings implementa SettingsReader. Un tenant sin sembrar devuelve la
// config de arranque: sin plantilla y con el plazo por defecto, igual que el store
// Postgres cuando no hay fila en tenant_settings.
func (m *MemoryStore) NotifySettings(_ context.Context, tenantID string) (NotifySettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, ok := m.notify[tenantID]
	if !ok || cfg.DepositDueDays <= 0 {
		cfg.DepositDueDays = DefaultDepositDueDays
	}
	return cfg, nil
}

// SetEvent siembra el estado de un evento conversacional (`open` | `closed` |
// `cancelled`), como haría una fila de public.conversation_events. Mutador para
// tests, como Add o SetShippingZones; no es parte de ningún puerto.
func (m *MemoryStore) SetEvent(eventID, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[eventID] = status
}

// EventStatus devuelve el estado sembrado/escrito del evento ("" si no existe).
// Es el espejo de lectura de SetEvent: lo que permite a un test comprobar que el
// descarte CERRÓ su contenedor (REQ-32e) sin abrir el mapa.
func (m *MemoryStore) EventStatus(eventID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events[eventID]
}

// BindEvent declara el padre de una solicitud (intakes.event_id, D-043.21), como
// lo haría el proyector del cart al parir la fila. Mutador para tests; sin
// llamarlo, la solicitud queda como una fila LEGADA pre-0054 (sin ligadura).
func (m *MemoryStore) BindEvent(intakeID, eventID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventOf[intakeID] = eventID
}

// SetShippingZones siembra las zonas de envío del tenant, como haría un UPDATE de
// tenant_settings.shipping_zones. Sin llamarla, el tenant no tiene zonas — que es
// el DEFAULT de la columna y el estado de arranque de cualquier tenant real.
func (m *MemoryStore) SetShippingZones(tenantID string, zones ...ShippingZone) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.zones[tenantID] = slices.Clone(zones)
}

// ShippingZones implementa el mismo puerto de lectura que `*Postgres` (Plan 044 ·
// T3.8): las zonas que el worker del pipeline le pasa a la etapa `match`.
//
// Existe para que los dos stores del dominio sigan siendo intercambiables en un
// test. Un tenant sin zonas sembradas devuelve nil sin error, igual que el tenant
// sin fila en `tenant_settings`.
func (m *MemoryStore) ShippingZones(_ context.Context, tenantID string) ([]ShippingZone, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.zones[tenantID]), nil
}

// SetShippingPrice precifica a mano la línea de envío de una solicitud y cuadra el
// total, que es lo que hará el dueño desde la consola cuando le ponga precio a un
// «Envío por confirmar» (D-041.11: v1, el dueño precifica). Es un mutador para los
// tests —como Add—, no parte de ningún puerto. Sin línea de envío no hace nada.
func (m *MemoryStore) SetShippingPrice(tenantID, intakeID string, price float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		for j, it := range m.items[intakeID] {
			if it.SKU != ShippingSKU {
				continue
			}
			m.items[intakeID][j].UnitPrice = price
			m.recomputeTotalLocked(tenantID, i, intakeID)
			return
		}
		return
	}
}

// Add siembra una solicitud del tenant con sus líneas. `in.Status` se guarda tal
// cual (puede ser la clave legada `closed`).
func (m *MemoryStore) Add(tenantID string, in Intake, items ...Item) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[tenantID] = append(m.rows[tenantID], row{intake: in, status: in.Status})
	if len(items) > 0 {
		m.items[in.ID] = append(m.items[in.ID], items...)
	}
}

// List implementa Store.
func (m *MemoryStore) List(_ context.Context, tenantID string, f Filter) ([]Intake, int, error) {
	f = f.Normalized()
	m.mu.Lock()
	defer m.mu.Unlock()

	matched := m.matching(tenantID, f)
	total := len(matched)
	start := f.Offset()
	if start >= total {
		return []Intake{}, total, nil
	}
	end := min(start+f.PageSize, total)
	return matched[start:end], total, nil
}

// ListDetails implementa Store: el MISMO predicado y orden que List —comparten
// `matching`, así que no pueden divergir— sin paginar, acotado a `limit`, con las
// líneas de cada solicitud colgadas.
func (m *MemoryStore) ListDetails(_ context.Context, tenantID string, f Filter, limit int) ([]Detail, error) {
	if limit <= 0 {
		return []Detail{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Ojo con Normalized(): acota PageSize a MaxPageSize, y la cota del export es
	// otra (MaxExportIntakes). Aquí solo interesa la normalización del ESTADO, así
	// que la paginación se descarta y el corte lo hace `limit`.
	matched := m.matching(tenantID, f.Normalized())
	if len(matched) > limit {
		matched = matched[:limit]
	}

	out := make([]Detail, 0, len(matched))
	for _, in := range matched {
		items := slices.Clone(m.items[in.ID])
		if items == nil {
			items = []Item{}
		}
		out = append(out, Detail{Intake: in, Items: items})
	}
	return out, nil
}

// matching filtra y ordena las solicitudes del tenant SIN paginar. Reproduce el
// predicado del store Postgres: rango [From, To), variantes legadas de CADA estado
// pedido, sesión exacta y el orden que dice `f.Sort` con desempate por id. El
// llamante tiene el candado tomado y le pasa el filtro ya NORMALIZADO (de ahí sale
// el orden por defecto).
func (m *MemoryStore) matching(tenantID string, f Filter) []Intake {
	// La expansión es de CADA estado del filtro, no del primero (D-044.47 §2):
	// el mismo StoredVariantsOf que arma el `= ANY($4)` del store real.
	variants := StoredVariantsOf(f.Statuses)

	matched := make([]Intake, 0, len(m.rows[tenantID]))
	for _, r := range m.rows[tenantID] {
		if !f.From.IsZero() && r.intake.CreatedAt.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && !r.intake.CreatedAt.Before(f.To) {
			continue // To es EXCLUSIVO, igual que el "< $3" del SQL
		}
		if variants != nil && !slices.Contains(variants, r.status) {
			continue
		}
		if f.SessionID != "" && r.intake.SessionID != f.SessionID {
			continue
		}
		in := r.intake
		in.Status = NormalizeStatus(r.status)
		matched = append(matched, in)
	}

	// Los dos criterios giran JUNTOS con `sort`, igual que el ORDER BY del store
	// real: created_at manda y el id desempata, los dos en el mismo sentido.
	slices.SortFunc(matched, func(a, b Intake) int {
		if f.Sort == SortOldest {
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Compare(b.CreatedAt) // más antiguas primero
			}
			return strings.Compare(a.ID, b.ID)
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return b.CreatedAt.Compare(a.CreatedAt) // más recientes primero
		}
		return strings.Compare(b.ID, a.ID)
	})
	return matched
}

// Get implementa Store.
func (m *MemoryStore) Get(_ context.Context, tenantID, intakeID string) (Detail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		in := r.intake
		in.Status = NormalizeStatus(r.status)
		return Detail{
			Intake:           in,
			Items:            slices.Clone(m.items[intakeID]),
			Revisions:        m.leerRevisionesLocked(intakeID),
			BuyerDataPresent: len(m.buyerData[intakeID]) > 0,
		}, nil
	}
	return Detail{}, ErrNotFound
}

// InsertRevision implementa RevisionWriter con la MISMA numeración que el store
// Postgres: el siguiente correlativo de esa solicitud, 1 para la primera.
//
// No comprueba que la solicitud exista: la FK de la tabla real sí lo hace, pero un
// store de tests que exigiera sembrar la cabecera obligaría a montar media bandeja
// para probar una revisión suelta.
func (m *MemoryStore) InsertRevision(_ context.Context, rev Revision) (Revision, error) {
	if len(rev.Payload) == 0 {
		return Revision{}, ErrEmptyRevisionPayload
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.guardarRevisiónLocked(rev)
}

// Revisions devuelve las revisiones sembradas/escritas para una solicitud, en
// orden. Es un mirador para los tests, no parte de ningún puerto.
func (m *MemoryStore) Revisions(intakeID string) []Revision {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.leerRevisionesLocked(intakeID)
}

// RevisionesPersistidas devuelve las revisiones TAL COMO ESTÁN GUARDADAS: sin poda,
// sin descifrado y sin devolverle el literal al payload. Es el equivalente en este
// doble a mirar la tabla con SQL directo, y existe para lo mismo — comprobar que lo
// que se escribió no lleva literal del cliente.
//
// No la use nadie para leer de verdad: Revisions y Get son la lectura, y son las que
// aplican la retención. Ésta enseña el crudo a propósito.
func (m *MemoryStore) RevisionesPersistidas(intakeID string) []Revision {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.revisions[intakeID])
}

// UpdateStatus implementa Store con el mismo compare-and-swap que el Postgres —y,
// como él, deja puesta la línea de envío al entrar a `pending_approval` (D-041.11).
// Esa paridad es la razón de ser de este store: un test de handler contra él solo
// dice algo verdadero sobre producción si las dos escrituras van juntas aquí
// también.
func (m *MemoryStore) UpdateStatus(_ context.Context, tenantID, intakeID, to string, expected []string) (Intake, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		if !slices.Contains(expected, r.status) {
			return Intake{}, ErrConflict
		}
		m.rows[tenantID][i].status = to
		m.rows[tenantID][i].intake.Status = to
		switch NormalizeStatus(to) {
		case StatusPendingApproval:
			if _, err := m.ensureShippingLocked(tenantID, i, ShippingAlways); err != nil {
				return Intake{}, err
			}
		case StatusDepositRequested:
			// La MISMA regla del store real: pedir seña fija su plazo en la misma
			// escritura del estado, y limpia el recordatorio para que la marca de una
			// seña anterior no silencie la nueva (T4.4).
			cfg := m.notify[tenantID]
			m.rows[tenantID][i].intake.DepositDueAt =
				m.now().AddDate(0, 0, depositDueDays(cfg.DepositDueDays))
			m.rows[tenantID][i].intake.DepositRemindedAt = time.Time{}
		}
		updated := m.rows[tenantID][i].intake
		updated.Status = NormalizeStatus(to)
		return updated, nil
	}
	return Intake{}, ErrNotFound
}

// EnsureShippingLine implementa Store con las MISMAS reglas que el Postgres: una
// sola línea de envío, la política decide si se materializa, y el total de la
// cabecera queda cuadrado con las líneas.
func (m *MemoryStore) EnsureShippingLine(_ context.Context, tenantID, intakeID string, policy ShippingPolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		_, err := m.ensureShippingLocked(tenantID, i, policy)
		return err
	}
	return ErrNotFound
}

// ensureShippingLocked es el cuerpo compartido por UpdateStatus y
// EnsureShippingLine; el llamante tiene el candado tomado y ya resolvió la fila
// (índice `i` dentro de m.rows[tenantID]).
func (m *MemoryStore) ensureShippingLocked(tenantID string, i int, policy ShippingPolicy) (bool, error) {
	zones := m.zones[tenantID]
	if !policy.applies(zones) {
		return false, nil
	}
	desired := DesiredShippingLine(zones)

	intakeID := m.rows[tenantID][i].intake.ID
	items := m.items[intakeID]
	for j, it := range items {
		if it.SKU != ShippingSKU {
			continue
		}
		if !desired.Supersedes(it) {
			return false, nil
		}
		line := desired.item()
		items[j].Label, items[j].Qty, items[j].UnitPrice = line.Label, line.Qty, line.UnitPrice
		m.recomputeTotalLocked(tenantID, i, intakeID)
		return true, nil
	}

	line := desired.item()
	line.AddedAt = time.Now()
	m.items[intakeID] = append(items, line)
	m.recomputeTotalLocked(tenantID, i, intakeID)
	return true, nil
}

// ReplaceItems implementa Store con las MISMAS reglas que el Postgres: las líneas
// del sistema (prefijo reservado) sobreviven, el total se recalcula ENTERO desde
// las líneas que quedan y la revisión `corrected` se escribe con la foto de lo
// persistido. La paridad es lo que hace que un test de handler contra este store
// diga algo verdadero sobre producción.
func (m *MemoryStore) ReplaceItems(_ context.Context, tenantID, intakeID string, items []Item, expected []string) (Detail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		if !slices.Contains(expected, r.status) {
			return Detail{}, ErrConflict
		}

		// Las del sistema primero y en su orden: es lo que hace el store real, que
		// las conserva con su added_at original mientras las nuevas se fechan ahora.
		system := systemItems(m.items[intakeID])
		lines := make([]Item, 0, len(system)+len(items))
		lines = append(lines, system...)
		for _, it := range items {
			it.AddedAt = time.Now()
			lines = append(lines, it)
		}
		m.items[intakeID] = lines
		m.rows[tenantID][i].intake.Total = editedTotal(lines)

		head := m.rows[tenantID][i].intake
		head.Status = NormalizeStatus(r.status)
		rev, err := correctedRevision(intakeID, head.Total, lines)
		if err != nil {
			return Detail{}, err
		}
		if _, err := m.guardarRevisiónLocked(rev); err != nil {
			return Detail{}, err
		}

		return Detail{
			Intake:    head,
			Items:     slices.Clone(lines),
			Revisions: m.leerRevisionesLocked(intakeID),
		}, nil
	}
	return Detail{}, ErrNotFound
}

// ApplyRevalidation implementa Store con la MISMA escritura QUIRÚRGICA que el store
// Postgres (T4.9, D-041.25): re-precia por sku, borra por sku y conserva el ORDEN y
// el `added_at` de las líneas que sobreviven. Reconstruir la lista desde rv.Items
// habría sido más corto y habría mentido: el store real no las reescribe, y un test
// que pasara aquí y fallara en producción no vale nada.
//
// Las líneas de la plataforma (prefijo reservado) se saltan explícitamente, igual
// que el `left(sku,1) <> $n` del SQL real.
func (m *MemoryStore) ApplyRevalidation(_ context.Context, tenantID, intakeID string, rv Revalidation, renderedText string, expected []string) (Detail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		if !slices.Contains(expected, r.status) {
			return Detail{}, ErrConflict
		}

		repriced := map[string]LineChange{}
		removed := map[string]bool{}
		for _, c := range rv.Changes {
			if c.Removed {
				removed[c.SKU] = true
				continue
			}
			repriced[c.SKU] = c
		}

		lines := make([]Item, 0, len(m.items[intakeID]))
		for _, it := range m.items[intakeID] {
			if strings.HasPrefix(it.SKU, ReservedSKUPrefix) {
				lines = append(lines, it)
				continue
			}
			if removed[it.SKU] {
				continue
			}
			if c, ok := repriced[it.SKU]; ok {
				it.Label, it.UnitPrice = c.Label, c.To
			}
			lines = append(lines, it)
		}
		m.items[intakeID] = lines
		m.rows[tenantID][i].intake.Total = editedTotal(lines)

		head := m.rows[tenantID][i].intake
		head.Status = NormalizeStatus(r.status)
		rev, err := revalidatedRevision(intakeID, rv, renderedText)
		if err != nil {
			return Detail{}, err
		}
		if _, err := m.guardarRevisiónLocked(rev); err != nil {
			return Detail{}, err
		}

		return Detail{
			Intake:    head,
			Items:     slices.Clone(lines),
			Revisions: m.leerRevisionesLocked(intakeID),
		}, nil
	}
	return Detail{}, ErrNotFound
}

// Discard implementa Store con el MISMO orden de rechazo que el Postgres —estado
// primero, evento vivo después (DT-043.2: el evento que ESTA solicitud declara)— y
// la misma escritura: `abandoned` más su revisión `discarded` más el cierre del
// contenedor (REQ-32e), o nada en absoluto. Esa paridad es la razón de ser de este
// store: un test de handler contra él solo dice algo verdadero sobre producción si
// las dos implementaciones rechazan por lo mismo y escriben lo mismo.
func (m *MemoryStore) Discard(_ context.Context, tenantID, intakeID string, discardable []string) (DiscardOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		out := DiscardOutcome{Status: NormalizeStatus(r.status)}
		if !slices.Contains(discardable, r.status) {
			return out, nil
		}
		eventID := m.eventOf[intakeID]
		if eventID != "" && m.events[eventID] == "open" {
			out.LiveEvent = true
			return out, nil
		}

		m.rows[tenantID][i].status = StatusAbandoned
		m.rows[tenantID][i].intake.Status = StatusAbandoned
		rev, err := discardedRevision(intakeID, r.status, m.rows[tenantID][i].intake.Total)
		if err != nil {
			return DiscardOutcome{}, err
		}
		if _, err := m.guardarRevisiónLocked(rev); err != nil {
			return DiscardOutcome{}, err
		}

		// El cierre del contenedor, calcado de cancelContainerTx: CAS open→cancelled
		// sobre el evento declarado; sin ligadura o ya terminal, no toca nada.
		if eventID != "" && m.events[eventID] == "open" {
			m.events[eventID] = "cancelled"
		}

		out.Discarded = true
		return out, nil
	}
	return DiscardOutcome{}, ErrNotFound
}

// AbandonByEvent implementa Store con el mismo CAS que el Postgres: la solicitud
// `open` del tenant que declara `eventID` pasa a `abandoned` (updated_at
// refrescado, sin revisión); 0 coincidencias es éxito idempotente (D-043.21,
// T4.5.5(a)).
func (m *MemoryStore) AbandonByEvent(_ context.Context, tenantID, eventID string) error {
	if eventID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.rows[tenantID] {
		if m.eventOf[r.intake.ID] != eventID || r.status != StatusOpen {
			continue
		}
		m.rows[tenantID][i].status = StatusAbandoned
		m.rows[tenantID][i].intake.Status = StatusAbandoned
		m.rows[tenantID][i].intake.UpdatedAt = m.now()
	}
	return nil
}

// MarkDepositReminded implementa DepositStore con el MISMO compare-and-swap que el
// Postgres: las cuatro condiciones se evalúan y la marca se escribe SIN soltar el
// candado, así que dos toques simultáneos no pueden ganar los dos. Esa paridad es lo
// que hace que un test de "un solo recordatorio" contra este store diga algo
// verdadero sobre producción.
func (m *MemoryStore) MarkDepositReminded(_ context.Context, tenantID, intakeID string, at time.Time) (Intake, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.rows[tenantID] {
		if r.intake.ID != intakeID {
			continue
		}
		in := r.intake
		in.Status = NormalizeStatus(r.status)
		if !candidate(in, at) {
			return Intake{}, false, nil
		}
		m.rows[tenantID][i].intake.DepositRemindedAt = at
		in.DepositRemindedAt = at
		return in, true, nil
	}
	return Intake{}, false, nil
}

// PendingDepositReminders implementa DepositStore: las señas vencidas y sin
// recordar de un contacto, lo más vencido primero y acotadas a `limit`, igual que la
// consulta real.
func (m *MemoryStore) PendingDepositReminders(_ context.Context, tenantID, contactID string, at time.Time, limit int) ([]Intake, error) {
	if limit <= 0 {
		return []Intake{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	out := []Intake{}
	for _, r := range m.rows[tenantID] {
		if r.intake.ContactID != contactID {
			continue
		}
		in := r.intake
		in.Status = NormalizeStatus(r.status)
		if candidate(in, at) {
			out = append(out, in)
		}
	}
	slices.SortFunc(out, func(a, b Intake) int { return a.DepositDueAt.Compare(b.DepositDueAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// recomputeTotalLocked cuadra el total de la cabecera con la suma de sus líneas,
// igual que el UPDATE del store Postgres.
func (m *MemoryStore) recomputeTotalLocked(tenantID string, i int, intakeID string) {
	var total float64
	for _, it := range m.items[intakeID] {
		total += float64(it.Qty) * it.UnitPrice
	}
	m.rows[tenantID][i].intake.Total = total
}
