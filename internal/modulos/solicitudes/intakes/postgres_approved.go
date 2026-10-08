// Porta internal/intakes/aprobadas.go @ 64c181a

// postgres_approved.go es la lectura de las cotizaciones APROBADAS del tenant. En el
// paquete viejo el método vivía en aprobadas.go, junto a su gemelo en memoria; aquí
// nace con el adaptador (D-F6-6 ampliada).

package intakes

import (
	"context"
	"fmt"
)

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
	  -- El conjunto de caracteres va EXPLÍCITO: btrim(x) a secas solo quita ESPACIOS,
	  -- mientras que el strings.TrimSpace del doble en memoria quita todo el blanco.
	  -- Con un texto de espacios y un salto de línea, Go lo descartaba y Postgres lo
	  -- dejaba pasar: el doble afirmaba una paridad que no existía. Lo cazó el test de
	  -- integración; el unitario contra el doble no podía verlo.
	  AND btrim(r.rendered_text, E' \t\n\r\f\v') <> ''
	ORDER BY r.created_at DESC, r.revision_no DESC
	LIMIT $3`

// ApprovedRenderedTexts devuelve los textos (rendered_text) de las últimas `limit`
// revisiones RevisionKindApproved del tenant, de la más reciente a la más antigua
// (created_at descendente y revision_no descendente de desempate). Es una consulta
// suelta, acotada por el tenant de la SOLICITUD (las revisiones no llevan tenant).
//
//   - limit <= 0 ⇒ (nil, nil) SIN tocar la base: pedir cero ejemplos es una petición
//     válida, no un error;
//   - limit > MaxApprovedTexts ⇒ se recorta a MaxApprovedTexts en silencio: el
//     llamante no tiene por qué conocer una cota que no es suya;
//   - los textos NULL o EN BLANCO no cuentan. El blanco se mide como
//     strings.TrimSpace —espacio, tabulador, salto de línea, retorno, salto de página
//     y tabulador vertical—, con el conjunto EXPLÍCITO en el SQL: un btrim a secas
//     solo quita espacios y dejaría pasar un texto de espacios y un salto de línea
//     que el doble en memoria descarta;
//   - sin filas ⇒ slice vacío, no nil.
//
// Errores, envueltos con %w y devolviendo (nil, err):
//
//   - "intakes: listar cotizaciones aprobadas del tenant: " — falla la consulta;
//   - "intakes: leer cotización aprobada: " — una fila no se puede escanear;
//   - "intakes: recorrer cotizaciones aprobadas: " — falla el recorrido;
//   - "intakes: cerrar filas de cotizaciones aprobadas: " — falla el cierre cuando
//     lo demás fue bien.
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
	// El fallo del cierre se DEVUELVE en vez de descartarse (solo si lo demás fue
	// bien, para no tapar el error que ya viaja): es el patrón de todas las lecturas
	// de este store (errcheck aquí lleva `check-blank`, así que un `_ =` tampoco
	// eximiría).
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("intakes: cerrar filas de cotizaciones aprobadas: %w", cerr)
		}
	}()

	out = make([]string, 0, limit)
	for rows.Next() {
		var text string
		if serr := rows.Scan(&text); serr != nil {
			return nil, fmt.Errorf("intakes: leer cotización aprobada: %w", serr)
		}
		out = append(out, text)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("intakes: recorrer cotizaciones aprobadas: %w", rerr)
	}
	return out, nil
}
