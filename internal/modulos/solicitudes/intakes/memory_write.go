// Porta internal/intakes/memory.go @ 64c181a (las escrituras).
//
// Regla común a todas las de aquí: lo que se RECHAZA no escribe NADA —ni cabecera, ni
// líneas, ni revisión, ni evento—, y ninguna toca otra solicitud ni otro tenant. La
// suite compara la fila entera antes y después (hallazgo 35 de F1).

package intakes

import (
	"context"
	"slices"
	"strings"
	"time"
)

// InsertRevision implementa RevisionWriter con la MISMA numeración que el store
// Postgres: el siguiente correlativo de ESA solicitud, 1 para la primera;
// rev.RevisionNo se ignora. Es el ÚNICO camino de escritura de revisiones del doble:
// por él pasan también la corrección, la revalidación y el descarte, y por eso todas
// parten el literal igual.
//
//   - Un Payload vacío se rechaza con ErrEmptyRevisionPayload y no escribe nada.
//   - El literal del cliente sale del Payload (SplitLiteral) y se guarda APARTE: la
//     revisión que se devuelve —y la que queda guardada— lleva el payload ya limpio. Si
//     el payload no se puede partir, se devuelve ese error y no se escribe nada.
//   - CreatedAt es el instante del reloj del store si la revisión llega sin fecha; si
//     trae una, se respeta (es como los tests siembran una revisión antigua).
//   - LiteralPrunedAt se devuelve siempre en cero.
//   - No es idempotente: dos llamadas iguales son dos revisiones.
//
// No comprueba que la solicitud exista. La FK de la tabla real sí, y es una
// divergencia consciente: exigirlo obligaría a montar la cabecera para probar una
// revisión suelta. No toca la cabecera (tampoco su UpdatedAt).
func (m *MemoryStore) InsertRevision(_ context.Context, rev Revision) (Revision, error) {
	if len(rev.Payload) == 0 {
		return Revision{}, ErrEmptyRevisionPayload
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.saveRevisionLocked(rev)
}

// UpdateStatus implementa Store con el mismo compare-and-swap que el Postgres: escribe
// `to` (tal cual, sin normalizar) solo si el estado ALMACENADO es uno de `expected`.
// ErrNotFound si la solicitud no es del tenant; ErrConflict si existe y su estado ya
// no es el esperado. Devuelve la cabecera ya escrita, con el Status normalizado.
//
// Al escribir refresca UpdatedAt con el reloj del store y, según el destino
// (normalizado):
//
//   - `pending_approval`: garantiza la línea de envío con ShippingAlways en la MISMA
//     escritura (D-041.11), y la cabecera devuelta ya trae el total con ella. Sin zona
//     resuelta NO pisa el precio que el dueño le puso a una línea que ya estaba:
//     re-presupuestar es rutina (D-041.26);
//   - `deposit_requested`: fija DepositDueAt = reloj + los días de la seña del tenant
//     (DefaultDepositDueDays si no hay configuración o es ≤ 0) y LIMPIA
//     DepositRemindedAt, para que la marca de una seña anterior no silencie la nueva;
//   - cualquier otro: solo el estado. Las líneas y las revisiones no se tocan (un
//     abandono conserva la negociación auditada).
//
// No valida la transición ni que `to` sea un estado conocido: eso es del dominio
// (Service.SetStatus y su TransitionError).
func (m *MemoryStore) UpdateStatus(_ context.Context, tenantID, intakeID, to string, expected []string) (Intake, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	i := m.indexLocked(tenantID, intakeID)
	if i < 0 {
		return Intake{}, ErrNotFound
	}
	if !slices.Contains(expected, m.rows[tenantID][i].status) {
		return Intake{}, ErrConflict
	}
	head := &m.rows[tenantID][i].intake
	m.rows[tenantID][i].status = to
	head.Status = to
	head.UpdatedAt = m.now()
	switch NormalizeStatus(to) {
	case StatusPendingApproval:
		m.ensureShippingLocked(tenantID, i, ShippingAlways)
	case StatusDepositRequested:
		// La MISMA regla del store real: pedir seña fija su plazo en la misma
		// escritura del estado, y limpia el recordatorio para que la marca de una
		// seña anterior no silencie la nueva (T4.4).
		head.DepositDueAt = m.now().AddDate(0, 0, depositDueDays(m.notify[tenantID].DepositDueDays))
		head.DepositRemindedAt = time.Time{}
	}
	updated := *head
	updated.Status = NormalizeStatus(to)
	return updated, nil
}

// EnsureShippingLine implementa Store con las MISMAS reglas que el Postgres: deja
// EXACTAMENTE UNA línea de envío (ShippingSKU) y el total de la cabecera cuadrado con
// la suma de las líneas. ErrNotFound si la solicitud no es del tenant.
//
//   - La política decide si se materializa: con ShippingOnlyIfZones y un tenant sin
//     zonas no se toca nada.
//   - Si no hay línea, se añade AL FINAL la que dicta la configuración
//     (DesiredShippingLine): la de la zona con su tarifa, o «Envío por confirmar» a 0.
//   - Si ya la hay, solo se sustituye —EN SU SITIO, conservando su AddedAt— cuando la
//     configuración manda sobre ella (ShippingLine.Supersedes): con zona resuelta y
//     distinta etiqueta, precio o cantidad. Sin zona resuelta el precio es del dueño y
//     no se pisa.
//
// Es idempotente: N llamadas dejan lo mismo que una. Cuando no cambia nada no escribe
// nada, tampoco UpdatedAt; cuando cambia, lo refresca junto con el total.
func (m *MemoryStore) EnsureShippingLine(_ context.Context, tenantID, intakeID string, policy ShippingPolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	i := m.indexLocked(tenantID, intakeID)
	if i < 0 {
		return ErrNotFound
	}
	m.ensureShippingLocked(tenantID, i, policy)
	return nil
}

// ReplaceItems implementa Store con las MISMAS reglas que el Postgres. ErrNotFound si
// la solicitud no es del tenant; ErrConflict si su estado almacenado no está en
// `expected`. Si escribe:
//
//   - las líneas del SISTEMA (ReservedSKUPrefix) sobreviven intactas y van PRIMERO, en
//     su orden y con su AddedAt; detrás, las de `items` en el orden dado, fechadas al
//     escribir (AddedAt no es cero). Repetir la edición nunca duplica el envío;
//   - el Total se recalcula ENTERO desde las líneas que quedan (Qty × UnitPrice; la
//     personalización no cobra, INV-13) y UpdatedAt se refresca: una corrección
//     REINICIA el plazo del presupuesto, a propósito;
//   - se escribe UNA revisión `corrected` de `owner` con la foto de lo persistido
//     (CorrectedRevisionPayload), numerada como la siguiente. El estado no cambia.
//
// La señal few-shot la decide `mode`: con EditPlain la revisión no lleva ninguna de
// las tres claves (el payload sale como antes de existir el parámetro); con
// EditAsCorrection lleva `as_correction` y el número y la clase de la revisión de
// número más alto que había ANTES de escribir ésta (ninguna de las dos si no había
// revisiones). Esa «última» se mira en crudo, sin podar ni fundir literales.
//
// Devuelve el detalle ya coherente —cabecera normalizada, líneas y todas las
// revisiones, la nueva incluida—, con BuyerDataPresent en false.
func (m *MemoryStore) ReplaceItems(_ context.Context, tenantID, intakeID string, items []Item, expected []string, mode EditMode) (Detail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	i := m.indexLocked(tenantID, intakeID)
	if i < 0 {
		return Detail{}, ErrNotFound
	}
	stored := m.rows[tenantID][i].status
	if !slices.Contains(expected, stored) {
		return Detail{}, ErrConflict
	}

	// Las del sistema primero y en su orden: es lo que hace el store real, que
	// las conserva con su added_at original mientras las nuevas se fechan ahora.
	now := m.now()
	lines := systemItems(m.items[intakeID])
	for _, it := range items {
		it.AddedAt = now
		lines = append(lines, it)
	}
	total := itemsTotal(lines)

	// La señal se resuelve mirando cuál era la última revisión ANTES de escribir la
	// nueva, y solo cuando el modo lo pide. Y la revisión se escribe ANTES de tocar
	// líneas y cabecera: es el único paso que puede fallar, y lo que se rechaza no
	// escribe nada.
	rev, err := correctedRevision(intakeID, total, lines, m.correctionSignalLocked(mode, intakeID))
	if err != nil {
		return Detail{}, err
	}
	if _, err := m.saveRevisionLocked(rev); err != nil {
		return Detail{}, err
	}
	m.items[intakeID] = lines
	head := &m.rows[tenantID][i].intake
	head.Total = total
	head.UpdatedAt = now

	out := *head
	out.Status = NormalizeStatus(stored)
	return Detail{
		Intake:    out,
		Items:     slices.Clone(lines),
		Revisions: m.readRevisionsLocked(intakeID),
	}, nil
}

// ApplyRevalidation implementa Store con la MISMA escritura QUIRÚRGICA que el Postgres
// (D-041.25). ErrNotFound si la solicitud no es del tenant; ErrConflict si su estado
// almacenado no está en `expected`. Si escribe, aplica rv.Changes POR SKU sobre las
// líneas guardadas —no reconstruye la lista desde rv.Items, que es lo que habría
// mentido respecto al store real—:
//
//   - un cambio con Removed borra las líneas de ese sku;
//   - uno sin Removed les pone Label y UnitPrice = To; cantidad, personalización y
//     AddedAt no se tocan;
//   - las líneas que sobreviven conservan su ORDEN y su AddedAt;
//   - las de LA PLATAFORMA (ReservedSKUPrefix) se saltan siempre, aunque un cambio
//     nombrara su sku: ni se re-precian ni se borran.
//
// El Total se recalcula desde las líneas que quedan, UpdatedAt se refresca y se escribe
// UNA revisión `revalidated` de `system` con `renderedText` como texto mandado al
// cliente (las reglas de esa revisión, ErrEmptyRevalidationText incluida, son las de
// revalidate.go; si falla no se escribe nada). El estado NO cambia, aunque se retiren
// todas las líneas.
//
// Devuelve el detalle ya coherente, revisión nueva incluida, con BuyerDataPresent en
// false.
func (m *MemoryStore) ApplyRevalidation(_ context.Context, tenantID, intakeID string, rv Revalidation, renderedText string, expected []string) (Detail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	i := m.indexLocked(tenantID, intakeID)
	if i < 0 {
		return Detail{}, ErrNotFound
	}
	stored := m.rows[tenantID][i].status
	if !slices.Contains(expected, stored) {
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
		// Las líneas de la plataforma (prefijo reservado) se saltan explícitamente,
		// igual que el `left(sku,1) <> $n` del SQL real.
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

	// La revisión primero: es lo único que puede fallar (ErrEmptyRevalidationText), y
	// si falla no se escribe nada.
	rev, err := revalidatedRevision(intakeID, rv, renderedText)
	if err != nil {
		return Detail{}, err
	}
	if _, err := m.saveRevisionLocked(rev); err != nil {
		return Detail{}, err
	}
	m.items[intakeID] = lines
	head := &m.rows[tenantID][i].intake
	head.Total = itemsTotal(lines)
	head.UpdatedAt = m.now()

	out := *head
	out.Status = NormalizeStatus(stored)
	return Detail{
		Intake:    out,
		Items:     slices.Clone(lines),
		Revisions: m.readRevisionsLocked(intakeID),
	}, nil
}

// Discard implementa Store con el MISMO orden de rechazo que el Postgres. ErrNotFound
// si la solicitud no es del tenant (con un DiscardOutcome a cero). Si existe, el
// outcome trae siempre en Status el estado normalizado en el que ESTABA al llegar, y:
//
//  1. si su estado almacenado no está en `discardable`: no escribe nada (Discarded y
//     LiveEvent en false). El estado se mira ANTES que el evento: una no descartable
//     con el evento vivo NO dice LiveEvent;
//  2. si el evento que ELLA declara está `open`: LiveEvent = true y no escribe nada,
//     ni en la solicitud ni en el evento. Que el tenant tenga OTRO evento `open` no
//     frena nada (DT-043.2), y una solicitud sin ligadura no tiene evento vivo;
//  3. si no: queda `abandoned`, con UpdatedAt refrescado y UNA revisión `discarded`
//     de `owner` (DiscardedRevisionPayload con el estado almacenado del que venía y su
//     total), y Discarded = true. Las líneas y el total no se tocan. El cierre del
//     contenedor es un CAS open→cancelled: un evento ya terminal no se pisa.
//
// Descartar dos veces deja el mismo estado y UNA sola revisión: la segunda llamada
// cae en el punto 1.
func (m *MemoryStore) Discard(_ context.Context, tenantID, intakeID string, discardable []string) (DiscardOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	i := m.indexLocked(tenantID, intakeID)
	if i < 0 {
		return DiscardOutcome{}, ErrNotFound
	}
	stored := m.rows[tenantID][i].status
	out := DiscardOutcome{Status: NormalizeStatus(stored)}
	if !slices.Contains(discardable, stored) {
		return out, nil
	}
	if m.hasLiveEventLocked(intakeID) {
		out.LiveEvent = true
		return out, nil
	}

	head := &m.rows[tenantID][i].intake
	rev, err := discardedRevision(intakeID, stored, head.Total)
	if err != nil {
		return DiscardOutcome{}, err
	}
	if _, err := m.saveRevisionLocked(rev); err != nil {
		return DiscardOutcome{}, err
	}
	m.rows[tenantID][i].status = StatusAbandoned
	head.Status = StatusAbandoned
	head.UpdatedAt = m.now()

	// El cierre del contenedor del store real (cancelContainerTx) es un CAS
	// open→cancelled sobre el evento declarado. Aquí no le queda nada que escribir:
	// bajo este mismo candado ya se vio que ese evento NO está `open` —si lo
	// estuviera, la guarda de arriba habría frenado el descarte—, y un evento ya
	// terminal no se pisa. El viejo repetía el CAS; era una rama que no podía darse.

	out.Discarded = true
	return out, nil
}

// AbandonByEvent implementa Store con el mismo CAS que el Postgres: la solicitud del
// tenant que declara `eventID` y está almacenada como `open` pasa a `abandoned`, con
// UpdatedAt refrescado y SIN revisión; sus líneas y el evento no se tocan. Cero
// coincidencias es ÉXITO IDEMPOTENTE (nil): un evento desconocido, de otro tenant, sin
// contenido, o cuya solicitud ya no está `open` (una `settled` no se abandona porque
// su evento muera; la ya abandonada no se vuelve a tocar). Con eventID "" devuelve nil
// sin mirar nada: no abandona las filas legadas sin ligadura. Nunca devuelve error.
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

// PutBuyerField imita PostgresBuyerData.PutBuyerField: FUSIONA el campo en el
// checklist del comprador de la solicitud (no sustituye el checklist; repetir una
// clave pisa su valor) y crea la entrada si no la había. Un valor vacío se guarda como
// cualquier otro. Con la clave vacía devuelve ErrBuyerFieldEmpty y no guarda nada. Sin
// cifrar — ver MemoryStore. No comprueba que la solicitud exista. Desde que hay un
// campo guardado, Get dice BuyerDataPresent.
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

// saveRevisionLocked es el ÚNICO camino de escritura de revisiones de este store
// (lo llaman los cuatro sitios que numeran una): parte el literal del payload y lo
// guarda aparte, igual que insertRevisionOnce lo saca antes de tocar la BD.
//
// Sin este punto único, cada uno tendría que acordarse de partir el payload — y el
// que se olvidara dejaría el literal en el payload sin que nada fallara, que es el
// modo de fallo silencioso que T3.5 viene a cerrar.
//
// Devuelve la revisión ya numerada, fechada y CON EL PAYLOAD SIN LITERAL: la misma
// forma que devuelve el store real, para que un test contra este doble no vea algo
// que en producción no vería. Era guardarRevisiónLocked en el viejo.
func (m *MemoryStore) saveRevisionLocked(rev Revision) (Revision, error) {
	clean, lit, err := SplitLiteral(rev.Payload)
	if err != nil {
		return Revision{}, err
	}
	rev.Payload = clean
	rev.RevisionNo = len(m.revisions[rev.IntakeID]) + 1
	if rev.CreatedAt.IsZero() {
		rev.CreatedAt = m.now()
	}
	rev.LiteralPrunedAt = time.Time{}
	if !lit.Empty() {
		if m.literals[rev.IntakeID] == nil {
			m.literals[rev.IntakeID] = map[int]LiteralRevision{}
		}
		m.literals[rev.IntakeID][rev.RevisionNo] = lit
	}
	// Lo guardado lleva su PROPIA copia del payload: el que se devuelve es del
	// llamante, y pisarle los bytes no puede pisar lo guardado.
	kept := rev
	kept.Payload = slices.Clone(clean)
	m.revisions[rev.IntakeID] = append(m.revisions[rev.IntakeID], kept)
	return rev, nil
}

// correctionSignalLocked resuelve la señal few-shot de una edición: vacía si el modo
// no la pide y, si la pide, con el número y la clase de la revisión de número más
// alto de las que hay AHORA, que es el borrador que la corrección está reemplazando
// (ninguna de las dos si no hay revisiones).
//
// Lee `m.revisions` directamente en vez de pasar por readRevisionsLocked por lo
// mismo que el store real no reusa revisionsOf: aquélla PODA los literales vencidos y
// los funde sobre el payload, que son efectos que nadie pidió por escribir una línea.
// Aquí solo hacen falta el número y la clase.
//
// La REGLA —sin EditAsCorrection no hay señal y la consulta ni se hace— no vive aquí
// sino en correctionSignal (edit.go), compartida con Postgres para que la guarda del
// modo exista una sola vez. Esto es solo la consulta (era últimaRevisiónLocked).
func (m *MemoryStore) correctionSignalLocked(mode EditMode, intakeID string) CorrectionSignal {
	// La consulta de este doble no puede fallar: el error de correctionSignal es
	// siempre el de la consulta, así que aquí es siempre nil.
	signal, _ := correctionSignal(mode, func() (no int, kind string, err error) {
		for _, rev := range m.revisions[intakeID] {
			if rev.RevisionNo > no {
				no, kind = rev.RevisionNo, rev.Kind
			}
		}
		return no, kind, nil
	})
	return signal
}

// ensureShippingLocked es el cuerpo compartido por UpdateStatus y
// EnsureShippingLine; el llamante tiene el candado tomado y ya resolvió la fila
// (índice `i` dentro de m.rows[tenantID]). Cuando cambia algo cuadra el total y
// refresca UpdatedAt, como el recálculo del store real; cuando no, no escribe nada.
func (m *MemoryStore) ensureShippingLocked(tenantID string, i int, policy ShippingPolicy) {
	zones := m.zones[tenantID]
	if !policy.applies(zones) {
		return
	}
	desired := DesiredShippingLine(zones)

	intakeID := m.rows[tenantID][i].intake.ID
	items := m.items[intakeID]
	for j, it := range items {
		if it.SKU != ShippingSKU {
			continue
		}
		if !desired.Supersedes(it) {
			return
		}
		line := desired.item()
		items[j].Label, items[j].Qty, items[j].UnitPrice = line.Label, line.Qty, line.UnitPrice
		m.recomputeTotalLocked(tenantID, i)
		m.rows[tenantID][i].intake.UpdatedAt = m.now()
		return
	}

	line := desired.item()
	line.AddedAt = m.now()
	m.items[intakeID] = append(items, line)
	m.recomputeTotalLocked(tenantID, i)
	m.rows[tenantID][i].intake.UpdatedAt = m.now()
}
