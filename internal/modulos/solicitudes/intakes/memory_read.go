// Porta internal/intakes/memory.go @ 64c181a (las lecturas) y el método
// (*MemoryStore).ApprovedRenderedTexts de internal/intakes/aprobadas.go @ 64c181a.

package intakes

import (
	"context"
	"maps"
	"slices"
	"strings"
)

// List implementa Store. El filtro se NORMALIZA dentro (Filter.Normalized), así que
// llega crudo. Devuelve la página pedida y el TOTAL de coincidencias sin paginar; una
// página más allá del final es un slice vacío NO nil con el total intacto. Nunca
// devuelve error.
//
// El predicado es el del store real, cláusula por cláusula:
//
//   - solo el tenant pedido (INV-8): un tenant ajeno o desconocido no ve nada;
//   - rango por CreatedAt con From INCLUSIVO y To EXCLUSIVO;
//   - estados: las variantes almacenadas de CADA estado del filtro (StoredVariantsOf),
//     no solo del primero — filtrar por `confirmed` alcanza las filas `closed`;
//   - sesión exacta;
//   - Orphan: fuera las que declaran un evento que sigue `open`. La fila sin ligadura
//     (legada pre-0054) y la que declara un evento que no existe son huérfanas. Es el
//     MISMO criterio que la guarda `live_event` de Discard, negado.
//
// El orden lo dice f.Sort: SortNewest (por defecto) es CreatedAt descendente y
// SortOldest ascendente; el id desempata EN EL MISMO SENTIDO que la fecha. Cada
// cabecera sale con el Status normalizado y el resto de sus campos tal como están
// guardados.
func (m *MemoryStore) List(_ context.Context, tenantID string, f Filter) ([]Intake, int, error) {
	f = f.Normalized()
	m.mu.Lock()
	defer m.mu.Unlock()

	matched := m.matchingLocked(tenantID, f)
	total := len(matched)
	start := f.Offset()
	if start >= total {
		return []Intake{}, total, nil
	}
	end := min(start+f.PageSize, total)
	return matched[start:end], total, nil
}

// ListDetails implementa Store: el MISMO predicado y el MISMO orden que List —si
// divergieran, el export no contendría lo que la bandeja muestra—, sin paginar (Page y
// PageSize se IGNORAN: PageSize se acota a MaxPageSize y la cota del export es otra) y
// cortado a `limit` CABECERAS, cada una con todas sus líneas en orden de alta.
//
// Una solicitud sin líneas sale con Items vacío NO nil. Revisions va vacío y
// BuyerDataPresent en false SIEMPRE: ni el export ni el summary publican nada de eso.
// Con `limit` ≤ 0 devuelve un slice vacío no nil. Nunca devuelve error. Las líneas son
// una copia del llamante.
func (m *MemoryStore) ListDetails(_ context.Context, tenantID string, f Filter, limit int) ([]Detail, error) {
	if limit <= 0 {
		return []Detail{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Ojo con Normalized(): acota PageSize a MaxPageSize, y la cota del export es
	// otra (MaxExportIntakes). Aquí solo interesa la normalización del ESTADO, así
	// que la paginación se descarta y el corte lo hace `limit`.
	matched := m.matchingLocked(tenantID, f.Normalized())
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

// Get implementa Store: la cabecera con el Status normalizado, sus líneas en orden de
// alta, sus revisiones por número ascendente y BuyerDataPresent (true si la solicitud
// tiene algún dato del comprador guardado). ErrNotFound si no hay solicitud con ese id
// EN ESE TENANT: inexistente y «de otro tenant» son indistinguibles (404 opaco).
//
// Las revisiones salen como en Revisions: con el literal devuelto a su sitio, y
// podando lo vencido (ver allí). Líneas y revisiones son copias del llamante.
func (m *MemoryStore) Get(_ context.Context, tenantID, intakeID string) (Detail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	i := m.indexLocked(tenantID, intakeID)
	if i < 0 {
		return Detail{}, ErrNotFound
	}
	r := m.rows[tenantID][i]
	in := r.intake
	in.Status = NormalizeStatus(r.status)
	return Detail{
		Intake:           in,
		Items:            slices.Clone(m.items[intakeID]),
		Revisions:        m.readRevisionsLocked(intakeID),
		BuyerDataPresent: len(m.buyerData[intakeID]) > 0,
	}, nil
}

// Revisions devuelve las revisiones de una solicitud en orden de escritura, TAL COMO
// LAS LEE EL DUEÑO: es la misma lectura que hace Get, sin pasar por el tenant. Mirador
// de tests; no es parte de ningún puerto. Una solicitud sin revisiones (o desconocida)
// da un slice vacío.
//
// Es una lectura CON EFECTOS, los de la retención del literal (T3.5; reglas de los
// tests viejos de retención y del sello de la poda, R-07):
//
//   - una revisión con literal vigente lo trae FUNDIDO en su Payload (MergeLiteral),
//     sobre una copia: dos lecturas seguidas no acumulan;
//   - una revisión cuyo literal venció (LiteralExpired con la edad medida por el reloj
//     del store y el TTL de SetLiteralTTL) se PODA en esta lectura: el literal se
//     destruye, la interpretación estructurada queda intacta, LiteralPrunedAt se sella
//     con el instante del reloj —y esta misma lectura ya lo publica— y se loguea por el
//     logger de retención, en nivel Info, el mensaje
//     "retención: literal de la revisión podado por TTL vencido" con las claves
//     intake_id, revision_no, edad_segundos y ttl_segundos y CERO contenido;
//   - una ya podada conserva su instante REAL: releerla no lo mueve ni vuelve a
//     anunciar la poda;
//   - se sella UNA revisión, no la solicitud: las hermanas no se enteran;
//   - una revisión que nunca tuvo literal (las del carrito) no se sella ni se anuncia,
//     por vieja que sea;
//   - con TTL 0 no se poda nunca.
//
// Si el literal no se puede fundir, se loguea en nivel Error
// ("retención: no se pudo devolver el literal a la revisión") y la revisión sale con su
// interpretación sola: una lectura de la bandeja no se cae por eso.
func (m *MemoryStore) Revisions(intakeID string) []Revision {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readRevisionsLocked(intakeID)
}

// PersistedRevisions devuelve las revisiones TAL COMO ESTÁN GUARDADAS: sin poda, sin
// devolverle el literal al payload y sin efectos. Es el equivalente en este doble a
// mirar la tabla con SQL directo, y existe para lo mismo: comprobar que lo que se
// escribió NO lleva literal del cliente. Devuelve una copia. No es la lectura de
// nadie: para leer de verdad están Get y Revisions. Era `RevisionesPersistidas` en el
// paquete viejo.
func (m *MemoryStore) PersistedRevisions(intakeID string) []Revision {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Clone(m.revisions[intakeID])
	for i := range out {
		out[i].Payload = slices.Clone(out[i].Payload)
	}
	return out
}

// ShippingZones devuelve las zonas de envío sembradas del tenant, el mismo puerto de
// lectura que tiene *Postgres (lo consume la etapa `match` del pipeline). Un tenant
// sin zonas devuelve nil sin error, igual que un tenant sin fila en tenant_settings.
// El slice es una copia del llamante. Nunca devuelve error.
func (m *MemoryStore) ShippingZones(_ context.Context, tenantID string) ([]ShippingZone, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.zones[tenantID]), nil
}

// NotifySettings implementa SettingsReader. Un tenant sin sembrar devuelve la
// configuración de arranque —sin plantilla y con DefaultDepositDueDays—, igual que el
// store Postgres cuando no hay fila en tenant_settings; uno sembrado con un plazo ≤ 0
// devuelve su plantilla con el plazo por defecto. Nunca devuelve error.
func (m *MemoryStore) NotifySettings(_ context.Context, tenantID string) (NotifySettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg := m.notify[tenantID]
	cfg.DepositDueDays = depositDueDays(cfg.DepositDueDays)
	return cfg, nil
}

// ApprovedRenderedTexts es el espejo en memoria de la consulta del historial aprobado
// (aprobadas.go): los RenderedText de las revisiones de clase `approved` de las
// solicitudes DEL TENANT, de la más reciente a la más antigua (CreatedAt descendente y,
// de desempate, RevisionNo descendente), hasta `limit`.
//
// No cuentan —ni gastan cupo— las de otra clase ni las de texto vacío o solo de
// blancos (espacio, tabulador, salto de línea, retorno, salto de página, tabulador
// vertical: el conjunto que el SQL real recorta explícitamente; un texto con solo esos
// caracteres dejaba pasar en Postgres lo que Go descartaba, y lo cazó el test de
// integración viejo). El texto que cuenta se devuelve SIN recortar.
//
// `limit` ≤ 0 devuelve (nil, nil): pedir cero ejemplos es una petición válida. Por
// encima de MaxApprovedTexts se recorta a esa cota en silencio. Sin candidatas
// devuelve un slice vacío. Nunca devuelve error.
func (m *MemoryStore) ApprovedRenderedTexts(_ context.Context, tenantID string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	if limit > MaxApprovedTexts {
		limit = MaxApprovedTexts
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	candidates := []Revision{}
	for _, r := range m.rows[tenantID] {
		for _, rev := range m.revisions[r.intake.ID] {
			if rev.Kind == RevisionKindApproved && strings.Trim(rev.RenderedText, approvedBlankCutset) != "" {
				candidates = append(candidates, rev)
			}
		}
	}
	slices.SortStableFunc(candidates, func(a, b Revision) int {
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return b.CreatedAt.Compare(a.CreatedAt) // más reciente primero
		}
		return b.RevisionNo - a.RevisionNo
	})
	out := []string{}
	for _, rev := range candidates {
		if len(out) >= limit {
			break
		}
		out = append(out, rev.RenderedText)
	}
	return out, nil
}

// StoredStatus devuelve la clave de estado de la solicitud TAL COMO ESTÁ GUARDADA, sin
// normalizar (`closed` sale `closed`), y "" si la solicitud no es de ese tenant. Sin
// efectos. Mirador de tests; no es parte de ningún puerto.
//
// NUEVO: no existía en el paquete viejo. Lo pide la suite de contrato, cuya marca de
// estado vigila la fila entera (hallazgo 35 de F1): todas las lecturas del puerto
// normalizan, y sin esto no se distingue una escritura que guarda la clave pedida de
// una que la normaliza antes de guardar. Contra Postgres es un SELECT de la columna.
func (m *MemoryStore) StoredStatus(tenantID, intakeID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.indexLocked(tenantID, intakeID)
	if i < 0 {
		return ""
	}
	return m.rows[tenantID][i].status
}

// BuyerDataOf devuelve una COPIA del checklist del comprador guardado para la
// solicitud (vacío, no nil, si no tiene nada). Mirador de tests: el store real no
// publica ninguna lectura en claro, su descifrado está custodiado (buyerdata.go).
func (m *MemoryStore) BuyerDataOf(intakeID string) BuyerData {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := BuyerData{}
	maps.Copy(out, m.buyerData[intakeID])
	return out
}

// approvedBlankCutset es el conjunto de blancos que NO hacen texto en el historial
// aprobado: el mismo que el SQL real recorta explícitamente con
// btrim(rendered_text, E' \t\n\r\f\v'). El viejo usaba strings.TrimSpace, que quita
// además el blanco Unicode que Postgres deja pasar: una paridad que no existía.
const approvedBlankCutset = " \t\n\r\f\v"

// matchesFilterLocked es el PREDICADO del filtro, fila a fila: el espejo del
// intakeFilterWhere del store real, cláusula por cláusula y en el mismo orden.
//
// Vive aparte de matchingLocked —que es quien ordena y pagina— porque son dos preguntas
// distintas: «¿esta fila entra?» y «¿en qué orden salen las que entraron». Tenerlas
// juntas hacía además que cada cláusula nueva del predicado subiera la complejidad
// de una función que ya llevaba dentro el comparador del orden.
//
// `variants` llega calculado por el llamante: es el mismo para todas las filas y
// recalcularlo por fila sería recorrer la lista de estados N veces para nada.
// Era casaConElFiltroLocked en el viejo.
func (m *MemoryStore) matchesFilterLocked(r memoryRow, f Filter, variants []string) bool {
	switch {
	case !f.From.IsZero() && r.intake.CreatedAt.Before(f.From):
		return false
	case !f.To.IsZero() && !r.intake.CreatedAt.Before(f.To):
		return false // To es EXCLUSIVO, igual que el "< $3" del SQL
	case variants != nil && !slices.Contains(variants, r.status):
		return false
	case f.SessionID != "" && r.intake.SessionID != f.SessionID:
		return false
	case f.Orphan && m.hasLiveEventLocked(r.intake.ID):
		return false
	}
	return true
}

// hasLiveEventLocked responde lo mismo que hasLiveEventTx en el store real: «¿está
// `open` el evento que ESTA solicitud declara?». Es el criterio que comparten la
// guarda `live_event` del descarte y el filtro de huérfanas (Plan 044 · T4.8), y por
// eso está escrito UNA vez: si divergieran, la bandeja de huérfanos enseñaría
// solicitudes que el descarte va a rechazar.
//
// Sin ligadura —fila legada pre-0054, eventOf vacío— no hay evento vivo que mirar.
// Era tieneEventoVivoLocked en el viejo.
func (m *MemoryStore) hasLiveEventLocked(intakeID string) bool {
	eventID := m.eventOf[intakeID]
	return eventID != "" && m.events[eventID] == "open"
}

// matchingLocked filtra y ordena las solicitudes del tenant SIN paginar. Reproduce el
// predicado del store Postgres: rango [From, To), variantes legadas de CADA estado
// pedido, sesión exacta, la orfandad de `f.Orphan` y el orden que dice `f.Sort` con
// desempate por id. El llamante tiene el candado tomado y le pasa el filtro ya
// NORMALIZADO (de ahí sale el orden por defecto). Era matching en el viejo.
func (m *MemoryStore) matchingLocked(tenantID string, f Filter) []Intake {
	// La expansión es de CADA estado del filtro, no del primero (D-044.47 §2):
	// el mismo StoredVariantsOf que arma el `= ANY($4)` del store real.
	variants := StoredVariantsOf(f.Statuses)

	matched := make([]Intake, 0, len(m.rows[tenantID]))
	for _, r := range m.rows[tenantID] {
		if !m.matchesFilterLocked(r, f, variants) {
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

// readRevisionsLocked es el espejo de revisionsOf del store real: poda lo vencido y
// devuelve el literal a su sitio en lo que sigue vigente.
//
// Devuelve copias: el literal se funde sobre el payload de la COPIA y nunca sobre el
// almacenado, para que dos lecturas seguidas no acumulen. Un error al fundir se
// LOGUEA y deja la revisión con su interpretación sola, en vez de tumbar la lectura
// entera de la bandeja de un doble de tests. Era leerRevisionesLocked en el viejo.
func (m *MemoryStore) readRevisionsLocked(intakeID string) []Revision {
	stored := m.revisions[intakeID]
	out := make([]Revision, 0, len(stored))
	for i, rev := range stored {
		// El payload es un slice: sin esta copia, quien pisara los bytes de lo
		// leído pisaría lo guardado.
		rev.Payload = slices.Clone(rev.Payload)
		lit, has := m.literals[intakeID][rev.RevisionNo]
		switch {
		case !has:
		case LiteralExpired(m.now().Sub(rev.CreatedAt), m.literalTTL):
			delete(m.literals[intakeID], rev.RevisionNo)
			// Se sella sobre la fila GUARDADA, no sobre la copia: la poda es un
			// hecho persistente y la siguiente lectura tiene que verlo.
			stored[i].LiteralPrunedAt = m.now()
			rev.LiteralPrunedAt = stored[i].LiteralPrunedAt
			// CERO CONTENIDO, igual que el del store real: lo que se acaba de
			// destruir no puede acabar en un log.
			m.log.Info("retención: literal de la revisión podado por TTL vencido",
				"intake_id", intakeID,
				"revision_no", rev.RevisionNo,
				"edad_segundos", int64(m.now().Sub(rev.CreatedAt).Seconds()),
				"ttl_segundos", int64(m.literalTTL.Seconds()))
		default:
			payload, err := MergeLiteral(rev.Payload, lit)
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
