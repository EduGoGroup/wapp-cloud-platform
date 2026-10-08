// Porta internal/intakes/reanalisis.go @ 64c181a

// postgres_reanalysis.go es la lectura de la FOTO que el re-análisis necesita de una
// solicitud. En el paquete viejo el método vivía en reanalisis.go; aquí nace con el
// adaptador (D-F6-6 ampliada) y reanalisis.go se queda con el tipo y el servicio.

package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// reanalysisTargetQuery lee las cuatro columnas en UNA sentencia.
//
// El `MAX(revision_no)` va como subconsulta y no como JOIN + GROUP BY porque la
// pregunta es escalar y porque así una solicitud SIN revisiones sigue devolviendo
// su fila (con 0) en vez de desaparecer del resultado — que es lo que haría un JOIN
// interno y lo que convertiría «este pedido no tiene revisiones» en un 404.
//
// `event_id` sale por COALESCE a cadena vacía: la columna es NULLable para el
// legado pre-0054 y "" dice lo mismo que ese NULL sin obligar a un sql.NullString
// que todo llamante tendría que desempaquetar.
const reanalysisTargetQuery = `
	SELECT i.session_id, i.contact_id, COALESCE(i.event_id::text, ''), i.status,
	       COALESCE((SELECT MAX(r.revision_no)
	                   FROM public.intake_revisions r
	                  WHERE r.intake_id = i.id), 0)
	  FROM public.intakes i
	 WHERE i.tenant_id = $1 AND i.id = $2
`

// ReanalysisTargetOf devuelve la foto de la solicitud que se va a re-analizar:
// sesión, contacto, evento padre, estado y el número de su última revisión. Es UNA
// lectura suelta que trae las cinco cosas a la vez.
//
//   - Status sale normalizado;
//   - EventID sale "" para una solicitud legada sin evento declarado (event_id NULL);
//   - LastRevisionNo sale 0 para una solicitud SIN revisiones, que sigue devolviendo
//     su fila: «este pedido no tiene revisiones» no es un 404.
//
// Devuelve ErrNotFound, sin envolver, si la solicitud no existe EN ESE TENANT. «No
// existe» y «es de otro tenant» son la misma respuesta (INV-8): distinguirlas
// convertiría el endpoint en un oráculo de qué ids existen. También ErrNotFound, sin
// tocar la base, si intakeID no es un UUID o si el receptor es nil (un *Postgres nil
// no tiene base que consultar y no debe hacer panic).
//
// Fallo de la base ⇒ (ReanalysisTarget{}, err) con
// "intakes: leer la solicitud a re-analizar: ".
func (p *Postgres) ReanalysisTargetOf(ctx context.Context, tenantID, intakeID string) (ReanalysisTarget, error) {
	if p == nil || p.db == nil {
		return ReanalysisTarget{}, ErrNotFound
	}
	if _, err := uuid.Parse(intakeID); err != nil {
		// Un id que no es UUID no puede existir: se responde 404 sin ir a la base, en
		// vez de dejar que Postgres devuelva un error de sintaxis que el transporte
		// traduciría a 500. Mismo criterio que MarkDepositReminded e InsertRevision.
		return ReanalysisTarget{}, ErrNotFound
	}

	var t ReanalysisTarget
	err := p.db.QueryRowContext(ctx, reanalysisTargetQuery, tenantID, intakeID).
		Scan(&t.SessionID, &t.ContactID, &t.EventID, &t.Status, &t.LastRevisionNo)
	if errors.Is(err, sql.ErrNoRows) {
		return ReanalysisTarget{}, ErrNotFound
	}
	if err != nil {
		return ReanalysisTarget{}, fmt.Errorf("intakes: leer la solicitud a re-analizar: %w", err)
	}
	t.Status = NormalizeStatus(t.Status)
	return t, nil
}
