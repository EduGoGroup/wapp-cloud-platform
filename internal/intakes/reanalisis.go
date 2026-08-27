// reanalisis.go — lo que el RE-ANÁLISIS necesita del dominio de solicitudes
// (Plan 044 · Ola 4 · T4.6, D-044.15 / design §8.1).
//
// Son DOS piezas pequeñas y ninguna de las dos es una máquina de estados nueva:
//
//  1. `ReanalysisTargetOf` — la lectura que responde «¿de qué evento cuelga esta
//     solicitud, y por qué revisión iba?». `/reanalyze` la necesita para construir
//     la clave de ventana del job y para anotar `analysis.reanalyzed_from`.
//  2. `Service.PushRevisionByID` — el empuje al puente CRM pedido por id, que es lo
//     único que la etapa `draft` puede pedir: allí no hay `Detail` ni forma de
//     construirlo (T4.10 mitad 2).
//
// 🔴 EL RE-ANÁLISIS NO TRANSICIONA NADA, y por eso aquí no hay un `Reanalyze` que
// haga de gemelo de `Approve`. El pedido se queda donde estaba —`pending_approval`,
// `needs_info`, lo que sea— y lo único que cambia es que le cuelga una revisión más
// cuando el pipeline termine, minutos después y por otro proceso. Escribir aquí una
// transición sería inventar un estado «re-analizando» que el ciclo de vida
// (D-041.10) no tiene y que ninguna pantalla sabría pintar.
package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ReanalysisTarget es la foto de una solicitud vista por `/reanalyze`: lo justo
// para abrir el job y anotar el rastro, y ni un campo más.
//
// No es `Detail` recortado y no debe convertirse en uno: `Detail` arrastra líneas,
// revisiones enteras y el descifrado del literal con su poda perezosa (literal.go).
// Pedir todo eso para leer cuatro columnas convertiría una comprobación previa —que
// puede acabar en un 422 sin escribir nada— en la lectura más cara del endpoint.
type ReanalysisTarget struct {
	// SessionID y ContactID son dos de las cuatro columnas de `intake.WindowKey`.
	SessionID string
	ContactID string
	// EventID es el evento conversacional del que cuelga la solicitud
	// (`intakes.event_id`, D-043.21). VACÍO en una solicitud LEGADA pre-0054, y ese
	// vacío no es un error de lectura: es un pedido que nació antes de que
	// existieran los eventos, así que no tiene hilo del que re-analizar. Quien
	// llama lo traduce a `source_unavailable`/`never_stored`, que es exactamente lo
	// que le pasa.
	EventID string
	// Status es la clave CANÓNICA del ciclo de vida, ya normalizada (el `closed`
	// legado sale como `confirmed`).
	//
	// ⚠️ SE PUBLICA Y HOY NO SE FILTRA POR ÉL. `/reanalyze` NO exige un estado de
	// partida —re-interpretar un pedido ya confirmado es legítimo: es cómo el dueño
	// descubre que la máquina se equivocó— y el contrato §8.1 no lista ningún 422
	// por estado. Está aquí porque el criterio INV-10 de T4.6 exige poder afirmar
	// que el intake NO cambia de estado, y para afirmarlo hay que haberlo leído
	// antes.
	Status string
	// LastRevisionNo es el correlativo VIGENTE de la solicitud: la revisión a la
	// que el re-análisis va a suceder. 0 = no tiene ninguna, que el contrato §7.4
	// publica como `reanalyzed_from: null`.
	LastRevisionNo int
}

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

// ReanalysisTargetOf devuelve la foto de la solicitud, o ErrNotFound si no existe
// EN ESE TENANT.
//
// La indistinción es deliberada y es la misma de `Get`: «no existe» y «es de otro
// tenant» tienen que ser la MISMA respuesta (el 404 opaco de INV-8), porque
// distinguirlas convertiría este endpoint en un oráculo de qué ids existen.
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

// PushRevisionByID encola para el puente CRM la revisión `revisionNo` de una
// solicitud, leyéndola por id (Plan 044 · Ola 4 · T4.6, cierre de T4.10 mitad 2).
//
// # POR QUÉ EXISTE ESTE GEMELO DE PushRevisionToCRM
//
// Porque la TERCERA puerta que empuja revisiones no es un handler: es la etapa
// `draft` del pipeline, que corre en un worker, minutos después de la petición HTTP
// y con un `ClaimedJob` por todo contexto. Allí no hay `Detail` —y construirlo
// obligaría a que una etapa del pipeline conociera la bandeja entera del dueño—,
// así que lo único que puede aportar es el par (intake, revisión). La lectura la
// hace este Service, que ya es quien sabe leer solicitudes.
//
// 🔴 EL `revisionNo` SIGUE SIENDO OBLIGATORIO Y EXPLÍCITO, por lo mismo que en
// PushRevisionToCRM: el puente hace UPSERT por (intake_id, revision_no) y trata como
// DUPLICADO todo par repetido. Deducirlo aquí de la última revisión de `d` sería
// adivinar, y adivinar mal significa que el CRM descarta el empuje en silencio.
//
// Sin CRMPusher cableado no hace nada (mismo criterio nil-safe que su hermano). Un
// fallo de LECTURA sí se devuelve: quien llama tiene que poder decir en su log que
// la revisión existía y no se pudo empujar, en vez de dejar el hueco mudo.
func (s *Service) PushRevisionByID(ctx context.Context, tenantID, intakeID string, revisionNo int) error {
	if s == nil || s.crm == nil {
		return nil
	}
	detail, err := s.Get(ctx, tenantID, intakeID)
	if err != nil {
		return fmt.Errorf("intakes: leer la solicitud %s para empujarla al CRM: %w", intakeID, err)
	}
	s.PushRevisionToCRM(ctx, tenantID, detail, revisionNo)
	return nil
}
