// Porta internal/intakes/postgres.go @ 64c181a

// postgres_revisions_read.go es la LECTURA de revisiones del adaptador, con la
// retención del literal de nivel 2 (Plan 044 · T3.5, migración 0079): la consulta, el
// descifrado de lo vigente, la poda perezosa de lo vencido y su sello. No tiene
// métodos exportados —la usan Get, ReplaceItems y ApplyRevalidation— y sus promesas,
// con los textos de sus errores, están escritas en la cabecera de
// postgres_revisions.go, de donde este fichero es la parte de lectura (E-13). Lo
// prueba postgres_revisions_read_test.go, a través de Get.

package intakes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// selectRevisionsQuery lee las revisiones de una solicitud MÁS lo que hace falta
// para decidir su retención (Plan 044 · T3.5).
//
// # LAS DOS COSAS DE MÁS QUE TRAE, Y POR QUÉ VIENEN DE LA BD Y NO DE GO
//
//   - LA EDAD (`now() - created_at`) se calcula EN SQL. `created_at` lo pone
//     Postgres con su DEFAULT; restarle un `time.Now()` de Go compararía DOS
//     RELOJES, que en esta casa ya tiene ficha de incidente propia. Con la resta
//     hecha por la BD hay un solo reloj y el desfase entre máquinas deja de existir
//     como categoría de error. Se pide en segundos enteros: un TTL se dimensiona en
//     meses y la fracción no decide nada.
//   - EL TTL sale de `tenant_settings` por LEFT JOIN. Un tenant SIN fila de config
//     lee el default de plataforma —el `$2` que pasa el llamante, DefaultLiteralTTL—
//     y un tenant CON fila manda siempre, incluido su 0 explícito («no podar»). Es
//     el mismo contrato que `event_inactivity_ttl_seconds` y `aggregation_window_seconds`,
//     y confundir «sin fila» con «0» es el error caro de estas claves.
//
// El JOIN a `intakes` es lo único que ata la revisión a su tenant: `intake_revisions`
// no tiene `tenant_id` (la FK ya la hace suya). No filtra por tenant y no debe: la
// cabecera se validó antes y la FK garantiza la pertenencia (mismo criterio que
// itemsOf).
const selectRevisionsQuery = `
	SELECT r.revision_no, r.kind, r.payload, r.rendered_text, r.created_by, r.created_at,
	       r.literal_enc, r.literal_dek, r.literal_kek_id, r.literal_pruned_at,
	       EXTRACT(EPOCH FROM (now() - r.created_at))::bigint AS edad_segundos,
	       COALESCE(ts.intake_literal_ttl_seconds, $2) AS ttl_segundos
	FROM public.intake_revisions r
	JOIN public.intakes i ON i.id = r.intake_id
	LEFT JOIN public.tenant_settings ts ON ts.tenant_id = i.tenant_id
	WHERE r.intake_id = $1
	ORDER BY r.revision_no`

// pruneLiteralQuery (era podarLiteralQuery en el viejo) es LA PODA (T3.5). Mírese lo que NO menciona: la columna
// `payload`. «La interpretación estructurada queda intacta» no es aquí una promesa
// que haya que creerse ni un cuidado que alguien pueda olvidar en el próximo
// cambio — es que esta sentencia no tiene forma de tocarla.
//
// El guard `literal_enc IS NOT NULL` la hace idempotente: podar dos veces la misma
// revisión afecta 0 filas la segunda, y `literal_pruned_at` conserva el instante en
// que el texto se destruyó de verdad en vez de moverse con cada lectura posterior.
//
// 🔑 EL `RETURNING` NO ES UN ADORNO: es lo que permite PUBLICAR el sello en la misma
// lectura que lo pone. El instante lo fija el `now()` de la BD, y devolverlo aquí
// significa que lo que sale por la API y lo que queda en la columna son EL MISMO
// valor del MISMO reloj — no dos lecturas de dos relojes que se parecen. Un
// `time.Now()` de Go para la respuesta y un `now()` de SQL para la fila darían dos
// instantes distintos del mismo hecho, y la segunda lectura contradiría a la primera.
const pruneLiteralQuery = `
	UPDATE public.intake_revisions
	   SET literal_enc       = NULL,
	       literal_dek       = NULL,
	       literal_kek_id    = NULL,
	       literal_pruned_at = now()
	 WHERE intake_id = $1 AND revision_no = $2 AND literal_enc IS NOT NULL
	RETURNING literal_pruned_at`

// prunedRevision (era revisionPodada en el viejo) es una poda pendiente de ejecutar: la decisión se toma leyendo y
// la escritura se hace DESPUÉS de cerrar el cursor. Meter un UPDATE dentro del
// rows.Next() lo ejecutaría sobre la misma conexión que está sirviendo el SELECT.
type prunedRevision struct {
	revisionNo int
	age        time.Duration
	ttl        time.Duration
}

// revisionsOf lee las revisiones de una solicitud en orden cronológico, PODA las
// que hayan vencido su retención y devuelve el literal DESCIFRADO dentro del payload
// de las que no.
//
// Es la puerta única de lectura de revisiones del store, y por eso la poda perezosa
// vive aquí y no en Get: se accede a una revisión desde el detalle, desde la
// corrección manual y desde la revalidación, y una poda que solo mirara uno de los
// tres caminos dejaría el literal vivo por los otros dos sin decirlo.
func (p *Postgres) revisionsOf(ctx context.Context, q querier, intakeID string) (out []Revision, err error) {
	rows, err := q.QueryContext(ctx, selectRevisionsQuery, intakeID, int64(DefaultLiteralTTL.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("intakes: listar revisiones: %w", err)
	}

	var prunes []prunedRevision
	out, err = p.scanRevisions(rows, intakeID, &prunes)
	if cerr := rows.Close(); cerr != nil && err == nil {
		return nil, fmt.Errorf("intakes: cerrar filas de revisiones: %w", cerr)
	}
	if err != nil {
		return nil, err
	}

	// 🔴 LA PODA VA DESPUÉS DE HABER DEVUELTO LA DECISIÓN, y su fallo NO tumba la
	// lectura. Es el mismo criterio que el evento del PersistSink: el dueño está
	// mirando su pedido, y no poder destruir un texto —que no le hace daño a nadie
	// mientras siga cifrado y bajo su KEK— no puede costarle la pantalla. Lo que sí
	// pasa es que se entera el log, y la siguiente lectura lo reintenta sola.
	for _, prune := range prunes {
		sealPruned(out, prune.revisionNo, p.runPrune(ctx, q, intakeID, prune))
	}
	return out, nil
}

// sealPruned (era sellarPodada en el viejo) publica en la revisión `revisionNo` de `out` el instante que la poda
// acaba de sellar en su fila. Es PURA —no toca la BD ni el reloj— y por eso es la
// pieza que se puede verificar sin Postgres delante.
//
// 🔴 POR QUÉ HACE FALTA. La poda se EJECUTA después de cerrar el cursor, así que en
// la lectura que la dispara la columna `literal_pruned_at` todavía es NULL y lo que
// scanRevisions leyó de ella es el cero. Sin esto, esa primera respuesta diría «esta
// revisión nunca tuvo texto» de una que se acaba de podar —exactamente la
// ambigüedad que la columna vino a cerrar (0079, COMMENT de literal_pruned_at)— y la
// siguiente diría otra cosa sobre el mismo hecho. El MemoryStore ya afirmaba lo
// correcto en la misma lectura (memory_read.go); esto es lo que
// hace que los dos stores digan LO MISMO.
//
// Dos reglas, y las dos importan:
//
//   - LA COLUMNA MANDA. Si la revisión ya traía sello, no se toca: `literal_pruned_at`
//     conserva el instante en que el texto se destruyó DE VERDAD y no puede moverse
//     con el reloj de una lectura posterior.
//   - UN CERO NO ESCRIBE NADA. Si la poda no selló —falló, o se le adelantó otra
//     lectura—, no se inventa una fecha: la revisión sale como estaba.
func sealPruned(out []Revision, revisionNo int, sealed time.Time) {
	if sealed.IsZero() {
		return
	}
	for i := range out {
		if out[i].RevisionNo != revisionNo {
			continue
		}
		if out[i].LiteralPrunedAt.IsZero() {
			out[i].LiteralPrunedAt = sealed
		}
		return
	}
}

// scanRevisions recorre el cursor: descifra lo que sigue vigente, marca para poda lo
// vencido y deja el payload de cada revisión con la forma del contrato §7.4.
func (p *Postgres) scanRevisions(rows *sql.Rows, intakeID string, prunes *[]prunedRevision) ([]Revision, error) {
	out := []Revision{}
	for rows.Next() {
		rev := Revision{IntakeID: intakeID}
		var rendered, createdBy, kekID sql.NullString
		var prunedAt sql.NullTime
		var envelope LiteralEnvelope
		var ageSec, ttlSec int64
		if serr := rows.Scan(&rev.RevisionNo, &rev.Kind, &rev.Payload,
			&rendered, &createdBy, &rev.CreatedAt,
			&envelope.Enc, &envelope.DEK, &kekID, &prunedAt, &ageSec, &ttlSec); serr != nil {
			return nil, fmt.Errorf("intakes: leer revisión: %w", serr)
		}
		rev.RenderedText, rev.CreatedBy = rendered.String, createdBy.String
		rev.LiteralPrunedAt, envelope.KEKID = prunedAt.Time, kekID.String

		age, ttl := time.Duration(ageSec)*time.Second, time.Duration(ttlSec)*time.Second
		switch {
		case envelope.Empty():
			// Sin sobre no hay nada que abrir ni que podar. Es el caso de TODAS las
			// revisiones del carrito numérico y el de las ya podadas.
		case LiteralExpired(age, ttl):
			// 🔴 NO SE DESCIFRA. Un literal vencido no se abre «para mirarlo antes de
			// tirarlo»: el plazo de retención es el permiso para leerlo, y se acabó.
			// El payload que sale es el que había en la columna, o sea la
			// interpretación estructurada sola.
			*prunes = append(*prunes, prunedRevision{revisionNo: rev.RevisionNo, age: age, ttl: ttl})
		default:
			payload, err := p.openLiteral(rev.Payload, envelope)
			if err != nil {
				return nil, fmt.Errorf("intakes: revisión %d de la solicitud %s: %w", rev.RevisionNo, intakeID, err)
			}
			rev.Payload = payload
		}
		out = append(out, rev)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("intakes: recorrer revisiones: %w", rerr)
	}
	return out, nil
}

// runPrune (era ejecutarPoda en el viejo) destruye el sobre de una revisión vencida y DEJA CONSTANCIA. El
// evento de poda es obligatorio (criterio de T3.5) y por eso el logger nunca es nil:
// borrar el texto original de un cliente sin dejar rastro convertiría una política de
// retención en una pérdida de datos indistinguible de un bug.
//
// Devuelve el instante que SELLÓ, o el cero si no selló nada —error o carrera—. Ese
// valor es lo que se publica: ver sealPruned. Un cero no es una fecha inventada
// hacia atrás, es «esta lectura no podó»; la siguiente lo vuelve a evaluar.
func (p *Postgres) runPrune(ctx context.Context, q querier, intakeID string, prune prunedRevision) time.Time {
	var sealed time.Time
	err := q.QueryRowContext(ctx, pruneLiteralQuery, intakeID, prune.revisionNo).Scan(&sealed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Otra lectura concurrente se adelantó. No es un fallo y no se anuncia como
		// poda: anunciarla dos veces haría creer que hubo dos textos.
		//
		// Es el mismo caso que antes se leía de RowsAffected() == 0, y por el
		// RETURNING ya no hay que contar filas ni tragarse un driver que no sepa
		// hacerlo: o vuelve el sello, o vuelve ErrNoRows.
		return time.Time{}
	case err != nil:
		p.log.Error("retención: no se pudo podar el literal de una revisión vencida",
			"intake_id", intakeID, "revision_no", prune.revisionNo, "error", err)
		return time.Time{}
	}
	// 🔴 CERO CONTENIDO EN ESTE EVENTO. Lo que se poda es literal del cliente, así
	// que el log de la poda no puede llevar ni una palabra suya: dejaría en un
	// fichero de texto plano —que no se cifra, no se rota por KEK y se retiene por
	// otras reglas— justo lo que se acaba de destruir de la base.
	p.log.Info("retención: literal de la revisión podado por TTL vencido",
		"intake_id", intakeID,
		"revision_no", prune.revisionNo,
		"edad_segundos", int64(prune.age.Seconds()),
		"ttl_segundos", int64(prune.ttl.Seconds()))
	return sealed
}

// openLiteral (era abrirLiteral en el viejo) descifra el sobre y devuelve el literal a su sitio dentro del
// payload, que es lo que hace que la API de detalle enseñe el texto original al lado
// de la interpretación (§7.6) sin que ninguna capa de arriba sepa que viajó aparte.
//
// 🔴 LOS DOS FALLOS DE AQUÍ SE PROPAGAN, NO SE TRAGAN. Ni un store sin cipher ni un
// sobre que no abre pueden devolver «la revisión, pero sin su original»: el dueño
// estaría comparando su interpretación contra un hueco y creyendo que el cliente no
// escribió nada. Es el mismo criterio que el hilo del evento (flujos/events).
func (p *Postgres) openLiteral(payload json.RawMessage, envelope LiteralEnvelope) (json.RawMessage, error) {
	if !envelope.Complete() {
		return nil, fmt.Errorf("sobre del literal incompleto en BD (enc=%d dek=%d kek_id=%t): son las tres o ninguna",
			len(envelope.Enc), len(envelope.DEK), envelope.KEKID != "")
	}
	if p.cipher == nil {
		return nil, errors.New("la revisión trae literal cifrado y el store no tiene FieldCipher (fallo de cableado, no de dato)")
	}
	plain, err := p.cipher.Decrypt(envelope.Enc, envelope.DEK, envelope.KEKID)
	if err != nil {
		return nil, fmt.Errorf("descifrar el literal: %w", err)
	}
	var lit LiteralRevision
	if uerr := json.Unmarshal([]byte(plain), &lit); uerr != nil {
		return nil, fmt.Errorf("interpretar el literal descifrado: %w", uerr)
	}
	out, err := MergeLiteral(payload, lit)
	if err != nil {
		return nil, err
	}
	return out, nil
}
