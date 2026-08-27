package intakes

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ════════════════════════════════════════════════════════════════════════════
// EL HISTORIAL APROBADO DEL TENANT (Plan 044 · Ola 5 · T5.1, D-044.11)
// ════════════════════════════════════════════════════════════════════════════
//
// Es la ÚNICA lectura de revisiones de este dominio que va POR TENANT y no por
// solicitud, y por eso vive en su propio fichero en vez de colarse entre las de
// postgres.go: lo que devuelve no es «la negociación de este pedido», es «cómo escribe
// este negocio», que es material de otra naturaleza y con otro consumidor
// (internal/intakes/quotetext).
//
// # 🔴 POR QUÉ NO PASA POR revisionsOf, Y POR QUÉ ESO NO ABRE NINGÚN AGUJERO
//
// `revisionsOf` es la puerta única de lectura de revisiones porque es donde vive la
// PODA PEREZOSA del literal (T3.5). Ésta no la usa, y podría parecer que se salta la
// retención. No se la salta, y el motivo es qué columna lee:
//
//   - la poda destruye `literal_enc`/`literal_dek`/`literal_kek_id`, que es donde vive
//     el texto DEL CLIENTE (`source_text`, `evidence`) — nivel 2 del ADR-0034;
//   - esta consulta lee `rendered_text`, que es lo que escribió EL DUEÑO y lo que salió
//     por el cable hacia el cliente. Ni está cifrado (mírese `scanRevisions`: se lee
//     como un `sql.NullString` pelado) ni tiene retención, porque no es texto del
//     cliente: es la respuesta comercial del negocio.
//
// Dicho de otro modo: aquí no hay nada que podar, y arrastrar la poda a esta consulta
// obligaría a leer y descifrar N payloads de N solicitudes para tirarlos.
//
// # LO QUE SÍ HACE ES ACOTARSE AL TENANT (INV-8)
//
// `intake_revisions` no tiene `tenant_id` —la FK a `intakes` ya la hace suya—, así que
// el JOIN a la cabecera no es una comodidad: es lo único que impide que la voz de un
// negocio se le enseñe al modelo de otro.
// ════════════════════════════════════════════════════════════════════════════

// MaxApprovedTexts acota cuántas cotizaciones aprobadas puede pedir un llamante de una
// vez. No es una regla de negocio: es la cota que impide que un `limit` grande se
// convierta en un prompt de un megabyte y en un escaneo del historial entero del
// tenant. El consumidor de hoy pide cinco (quotetext.EjemplosPorDefecto).
const MaxApprovedTexts = 50

// selectApprovedTextsQuery lee los textos de las últimas cotizaciones APROBADAS del
// tenant, de la más reciente a la más antigua.
//
// El desempate por `revision_no` no es adorno: dos revisiones de la misma solicitud
// pueden compartir `created_at` al microsegundo si se escribieron en la misma
// transacción, y sin desempate el orden sería el que quisiera el planificador — o sea,
// un few-shot que cambia entre dos llamadas idénticas.
//
// Las vacías se filtran EN SQL: una revisión `approved` sin texto no puede existir hoy
// (`Approve` corta con ErrEmptyQuoteText antes de escribir), pero traerla y descartarla
// en Go gastaría cupo del LIMIT en filas que no valen como ejemplo.
const selectApprovedTextsQuery = `
	SELECT r.rendered_text
	FROM public.intake_revisions r
	JOIN public.intakes i ON i.id = r.intake_id
	WHERE i.tenant_id = $1
	  AND r.kind = $2
	  AND r.rendered_text IS NOT NULL
	  AND btrim(r.rendered_text) <> ''
	ORDER BY r.created_at DESC, r.revision_no DESC
	LIMIT $3`

// ApprovedRenderedTexts devuelve los textos de las últimas `limit` cotizaciones
// aprobadas del tenant, de la más reciente a la más antigua.
//
// `limit` <= 0 devuelve nada sin tocar la BD (pedir cero ejemplos es una petición
// válida, no un error), y por encima de MaxApprovedTexts se recorta en silencio: el
// llamante pidió más de lo que este camino sirve, y devolverle un error le obligaría a
// conocer una cota que no es suya.
func (p *Postgres) ApprovedRenderedTexts(ctx context.Context, tenantID string, limit int) (out []string, err error) {
	if limit <= 0 {
		return nil, nil
	}
	if limit > MaxApprovedTexts {
		limit = MaxApprovedTexts
	}
	rows, err := p.db.QueryContext(ctx, selectApprovedTextsQuery, tenantID, RevisionKindApproved, limit)
	if err != nil {
		return nil, fmt.Errorf("intakes: listar cotizaciones aprobadas del tenant: %w", err)
	}
	// El cierre se enriquece con errors.Join en vez de descartarse: es el patrón de
	// todas las lecturas de este store (errcheck aquí lleva `check-blank`, así que un
	// `_ =` tampoco eximiría).
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("intakes: cerrar filas de cotizaciones aprobadas: %w", cerr)
		}
	}()

	out = make([]string, 0, limit)
	for rows.Next() {
		var texto string
		if serr := rows.Scan(&texto); serr != nil {
			return nil, fmt.Errorf("intakes: leer cotización aprobada: %w", serr)
		}
		out = append(out, texto)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("intakes: recorrer cotizaciones aprobadas: %w", rerr)
	}
	return out, nil
}

// ApprovedRenderedTexts es el espejo en memoria de la consulta de arriba: mismo
// filtro (kind `approved`, texto no vacío), mismo orden (más reciente primero, y
// `revision_no` de desempate) y misma cota.
//
// Esa paridad es la razón de ser del doble: un test de quotetext contra este store
// solo dice algo verdadero sobre producción si las dos ordenan igual.
func (m *MemoryStore) ApprovedRenderedTexts(_ context.Context, tenantID string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	if limit > MaxApprovedTexts {
		limit = MaxApprovedTexts
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	candidatas := []Revision{}
	for _, r := range m.rows[tenantID] {
		for _, rev := range m.revisions[r.intake.ID] {
			if rev.Kind == RevisionKindApproved && strings.TrimSpace(rev.RenderedText) != "" {
				candidatas = append(candidatas, rev)
			}
		}
	}
	sort.SliceStable(candidatas, func(i, j int) bool {
		if !candidatas[i].CreatedAt.Equal(candidatas[j].CreatedAt) {
			return candidatas[i].CreatedAt.After(candidatas[j].CreatedAt)
		}
		return candidatas[i].RevisionNo > candidatas[j].RevisionNo
	})
	out := []string{}
	for _, rev := range candidatas {
		if len(out) >= limit {
			break
		}
		out = append(out, rev.RenderedText)
	}
	return out, nil
}
