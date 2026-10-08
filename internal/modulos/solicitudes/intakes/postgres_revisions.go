// Porta internal/intakes/postgres.go @ 64c181a

// postgres_revisions.go es la ESCRITURA de revisiones (el puerto RevisionWriter): el
// sellado del literal, el INSERT numerador y su reintento, y la consulta de la última
// revisión. La puerta única de LECTURA de revisiones del adaptador, con la retención
// del literal de nivel 2 (Plan 044 · T3.5, migración 0079), vive en
// postgres_revisions_read.go (E-13). Esa lectura no es un método exportado —la usan
// Get, ReplaceItems y ApplyRevalidation— pero sus promesas son observables por los
// tres, y se escriben aquí, que es la cabecera del tema.
//
// # La lectura de revisiones (portada en postgres_revisions_read.go)
//
// Es UNA consulta, por la conexión que le pasen (el pool o la transacción del
// llamante), ordenada por revision_no, con dos argumentos: la solicitud y el TTL de
// plataforma en segundos (DefaultLiteralTTL). Cada fila trae doce columnas: las seis
// de la revisión, las tres del sobre (literal_enc, literal_dek, literal_kek_id),
// literal_pruned_at, la EDAD en segundos y el TTL en segundos.
//
//   - LA EDAD la calcula la base (`now() - created_at`), no Go: created_at lo pone
//     Postgres, y restarle un time.Now() compararía dos relojes;
//   - EL TTL sale de tenant_settings por LEFT JOIN: un tenant sin fila de config lee
//     el de plataforma, y uno con fila manda siempre, incluido su 0 («no podar»).
//
// Por cada fila, según su sobre:
//
//   - sobre VACÍO (las tres columnas a NULL): la revisión sale con el payload tal
//     cual. Es la mayoría;
//   - sobre presente y literal VENCIDO (LiteralExpired(edad, ttl)): la revisión sale
//     con el payload SIN literal y queda apuntada para podar. No se descifra: lo que
//     se va a destruir no se abre, y por eso un store sin cifrador lee sin error una
//     revisión vencida;
//   - sobre presente y vigente: se descifra y se FUNDE en el payload (MergeLiteral).
//
// LA PODA se ejecuta DESPUÉS de cerrar el cursor (un UPDATE dentro del recorrido
// saldría por la conexión que sirve el SELECT), una sentencia por revisión vencida,
// en orden de revision_no. La sentencia no nombra la columna payload: la
// interpretación estructurada no puede tocarse. Su `literal_enc IS NOT NULL` la hace
// idempotente y su RETURNING devuelve el sello que puso la base:
//
//   - si sella, la revisión sale con ese LiteralPrunedAt EN LA MISMA LECTURA —salvo
//     que la fila ya trajera sello: la columna manda y no se mueve— y el logger de
//     retención recibe un Info "retención: literal de la revisión podado por TTL
//     vencido" con intake_id, revision_no, edad_segundos y ttl_segundos;
//   - si no devuelve fila (otra lectura se adelantó), no se anuncia nada y la
//     revisión sale como estaba, sin fecha inventada;
//   - si falla, la lectura NO falla: el logger recibe un Error "retención: no se pudo
//     podar el literal de una revisión vencida" con intake_id, revision_no y error, y
//     la revisión sale sin sello. La siguiente lectura lo vuelve a intentar.
//
// Errores de la lectura, envueltos con %w:
//
//   - "intakes: listar revisiones: " — falla la consulta;
//   - "intakes: leer revisión: " — una fila no se puede escanear;
//   - "intakes: recorrer revisiones: " — falla el recorrido;
//   - "intakes: cerrar filas de revisiones: " — falla el cierre cuando lo demás fue
//     bien (T-13: no se calla; un error anterior no se pisa);
//   - "intakes: revisión <n> de la solicitud <id>: " — el sobre vigente de esa
//     revisión no se puede abrir, seguido de la causa:
//     "sobre del literal incompleto en BD (enc=<bytes> dek=<bytes> kek_id=<bool>): son las tres o ninguna",
//     "la revisión trae literal cifrado y el store no tiene FieldCipher (fallo de cableado, no de dato)",
//     "descifrar el literal: " (envuelve el error del cifrador),
//     "interpretar el literal descifrado: " (lo descifrado no es el JSON del literal),
//     o el error de MergeLiteral tal cual.
//
// El candado AST de «el instante sellado no se descarta» barre
// postgres_revisions_read.go y vive en postgres_test.go (R6.2.d, R-07).

package intakes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// revisionCols es la proyección de una revisión. `id` NO se lee: la identidad que
// significa algo fuera de la BD es (intake_id, revision_no).
const revisionCols = `revision_no, kind, payload, rendered_text, created_by, created_at`

// sealLiteral (era cifrarLiteral en el viejo) saca del payload lo que es de nivel 2 y lo mete en un sobre. Es la
// BARRERA ÚNICA: la llama insertRevisionOnce, que es el único camino de escritura de
// revisiones del store, así que ningún escritor —ni los de hoy ni los que vengan—
// puede persistir literal en claro por descuido. Ése fue exactamente el fallo de
// MP-06 con `vars.intent_params`, y D-044.13 lo nombra para no repetirlo.
func (p *Postgres) sealLiteral(payload json.RawMessage) (json.RawMessage, LiteralEnvelope, error) {
	clean, lit, err := SplitLiteral(payload)
	if err != nil {
		return nil, LiteralEnvelope{}, err
	}
	if lit.Empty() {
		return clean, LiteralEnvelope{}, nil
	}
	if p.cipher == nil {
		// 🔴 SE FALLA, NO SE DEGRADA. La alternativa —escribirlo en claro «porque no
		// hay llave»— es la que convierte una tarea de cifrado en una columna con PII
		// que nadie vuelve a mirar.
		return nil, LiteralEnvelope{}, errors.New("intakes: la revisión lleva literal del cliente y el store no tiene FieldCipher: no se escribe en claro")
	}
	raw, err := json.Marshal(lit)
	if err != nil {
		return nil, LiteralEnvelope{}, fmt.Errorf("intakes: serializar el literal de la revisión: %w", err)
	}
	enc, dek, kekID, err := p.cipher.Encrypt(string(raw))
	if err != nil {
		return nil, LiteralEnvelope{}, fmt.Errorf("intakes: cifrar el literal de la revisión: %w", err)
	}
	return clean, LiteralEnvelope{Enc: enc, DEK: dek, KEKID: kekID}, nil
}

// insertRevisionQuery numera y escribe la revisión en UNA sentencia: el
// `COALESCE(MAX(revision_no), 0) + 1` se evalúa DENTRO del INSERT, así que entre el
// cálculo y la escritura no cabe una lectura ajena. Lo que sí cabe es otro INSERT
// concurrente que calcule el mismo número: ese pierde contra el UNIQUE (23505) y lo
// resuelve el reintento de InsertRevision, no un candado que serializaría a todos.
const insertRevisionQuery = `
	INSERT INTO public.intake_revisions
		(intake_id, revision_no, kind, payload, rendered_text, created_by,
		 literal_enc, literal_dek, literal_kek_id)
	SELECT $1::uuid, COALESCE(MAX(revision_no), 0) + 1, $2, $3::jsonb, $4, $5, $6, $7, $8
	FROM public.intake_revisions WHERE intake_id = $1::uuid
	RETURNING ` + revisionCols

// maxRevisionAttempts acota los reintentos por colisión de numeración. Los
// escritores de revisiones de UNA solicitud son pocos y esporádicos (un cierre de
// carrito, una corrección del dueño, una revalidación): 5 intentos sobran, y
// agotarlos devuelve el error en vez de girar indefinidamente.
const maxRevisionAttempts = 5

// InsertRevision implementa RevisionWriter: numera la revisión con el siguiente
// correlativo de esa solicitud y la escribe. rev.RevisionNo y rev.CreatedAt de
// entrada se ignoran: los pone la base.
//
// Validación, ANTES de tocar la base y en este orden:
//
//  1. payload vacío ⇒ ErrEmptyRevisionPayload;
//  2. IntakeID que no es un UUID ⇒ ErrNotFound.
//
// EL LITERAL SE SELLA AQUÍ, en el único camino de escritura. Si el payload trae
// literal del cliente (source_text, o evidence en alguna línea), SplitLiteral lo saca
// y se cifra con el FieldCipher: a la columna payload va la interpretación
// estructurada y nada más, y el sobre va a sus tres columnas. Sin literal, las tres
// viajan a NULL y el cifrador ni se toca. RenderedText y CreatedBy vacíos viajan a
// NULL.
//
//   - payload con literal y store SIN cifrador ⇒ error
//     "intakes: la revisión lleva literal del cliente y el store no tiene FieldCipher: no se escribe en claro",
//     sin emitir ninguna sentencia;
//   - el cifrador falla ⇒ "intakes: cifrar el literal de la revisión: " envolviendo
//     la causa, sin emitir ninguna sentencia;
//   - el literal no se puede serializar ⇒ "intakes: serializar el literal de la
//     revisión: " (defensivo);
//   - un payload que SplitLiteral rechaza ⇒ su error tal cual.
//
// La numeración y la escritura son UNA sentencia suelta (sin transacción): el
// `COALESCE(MAX(revision_no), 0) + 1` se evalúa dentro del INSERT. Dos escritores
// concurrentes pueden calcular el mismo número; el que pierde contra el UNIQUE
// (SQLSTATE 23505) REINTENTA la sentencia entera, hasta 5 intentos en total:
//
//   - gana algún intento ⇒ la revisión escrita;
//   - se agotan los 5 ⇒ "intakes: numerar la revisión tras 5 intentos: " envolviendo
//     la última violación de unicidad;
//   - cualquier otro fallo ⇒ "intakes: insertar revisión: " envolviendo la causa, SIN
//     reintentar (una FK rota —la solicitud no existe— cae aquí).
//
// La Revision devuelta lleva lo que quedó en la base: el número asignado, kind,
// payload SIN el literal (quien acaba de escribirla ya tiene el texto en la mano; no
// se descifra lo recién cifrado), rendered_text y created_by («» si NULL) y
// created_at. IntakeID es el de entrada.
//
// Dentro de ReplaceItems, ApplyRevalidation y Discard la misma escritura va por la
// transacción del llamante y allí NO hay reintento: un 23505 aborta la transacción
// entera, que es el precio de que la edición y su rastro se confirmen juntos.
func (p *Postgres) InsertRevision(ctx context.Context, rev Revision) (Revision, error) {
	if len(rev.Payload) == 0 {
		return Revision{}, ErrEmptyRevisionPayload
	}
	if _, err := uuid.Parse(rev.IntakeID); err != nil {
		return Revision{}, ErrNotFound
	}

	var lastErr error
	for attempt := 0; attempt < maxRevisionAttempts; attempt++ {
		out, err := p.insertRevisionOnce(ctx, p.db, rev)
		switch {
		case err == nil:
			return out, nil
		case postgres.IsUniqueViolation(err):
			// Otro escritor se llevó ese revision_no: reintentar relee un máximo
			// ya mayor y converge.
			lastErr = err
		default:
			return Revision{}, err
		}
	}
	return Revision{}, fmt.Errorf("intakes: numerar la revisión tras %d intentos: %w", maxRevisionAttempts, lastErr)
}

// insertRevisionOnce ejecuta UN intento de numeración+escritura. Toma un querier
// porque la corrección manual la escribe DENTRO de su transacción (ReplaceItems):
// allí no hay reintento posible —un 23505 aborta la transacción entera— y es el
// precio correcto, porque lo que se compra es que la edición y su rastro se
// confirmen juntos o no se confirme ninguno.
// El payload que llega puede traer literal del cliente (`source_text` y las
// `evidence` de las líneas): se SACA y se sella aquí, en el único camino de
// escritura, antes de que toque la BD (Plan 044 · T3.5). Lo que va a la columna
// `payload` es la interpretación estructurada y nada más.
//
// La Revision que se DEVUELVE lleva el payload tal como quedó en la BD —sin el
// literal—, y eso es deliberado: quien acaba de escribirla ya tiene el texto en la
// mano, y devolvérselo re-fundido obligaría a descifrar lo que se acaba de cifrar
// para no decir nada nuevo. Quien lo quiera de vuelta lo lee, que es el camino que
// pasa por la retención.
func (p *Postgres) insertRevisionOnce(ctx context.Context, q querier, rev Revision) (Revision, error) {
	payload, envelope, err := p.sealLiteral(rev.Payload)
	if err != nil {
		return Revision{}, err
	}

	out := Revision{IntakeID: rev.IntakeID}
	var rendered, createdBy sql.NullString
	err = q.QueryRowContext(ctx, insertRevisionQuery,
		rev.IntakeID, rev.Kind, []byte(payload),
		nullableText(rev.RenderedText), nullableText(rev.CreatedBy),
		nullableBytes(envelope.Enc), nullableBytes(envelope.DEK), nullableText(envelope.KEKID),
	).Scan(&out.RevisionNo, &out.Kind, &out.Payload, &rendered, &createdBy, &out.CreatedAt)
	if err != nil {
		// Se envuelve SIEMPRE, incluida la colisión de numeración: quien la
		// distingue arriba usa errors.As, que atraviesa el %w.
		return Revision{}, fmt.Errorf("intakes: insertar revisión: %w", err)
	}
	out.RenderedText, out.CreatedBy = rendered.String, createdBy.String
	return out, nil
}

// nullableText manda NULL en vez de cadena vacía a una columna opcional: "no hubo
// texto renderizado" y "el texto renderizado fue la cadena vacía" son cosas
// distintas, y la columna admite la diferencia.
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableBytes es nullableText para las columnas BYTEA del sobre. Sin ella, un
// sobre ausente escribiría `”::bytea` en vez de NULL — y entonces el barrido de
// rotación (que filtra por `literal_kek_id IS NOT NULL`) y el guard de la poda
// (`literal_enc IS NOT NULL`) verían filas que no tienen nada dentro.
func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// lastRevisionTx (era últimaRevisiónTx en el viejo) es la consulta con la que ESTE
// store contesta «¿qué revisión se está corrigiendo?» (T4.4). La REGLA —cuándo se
// pregunta y qué se guarda— no está aquí sino en correctionSignal
// (postgres_items.go), que es la que decide si esta función llega a ejecutarse: en
// el camino del 041 no se llama y la sentencia no se paga.
//
// Devuelve una FUNCIÓN y no un dato porque solo se pregunta cuando hace falta. En el
// viejo su tipo tenía nombre (últimaRevisión, en edit.go); aquí va sin nombre para
// no adelantar un tipo de edit.go.
//
// Consulta a mano en vez de reusar revisionsOf, y es a propósito: aquélla trae los
// payloads enteros, DESCIFRA el literal del cliente y aplica la poda perezosa por
// TTL (efectos persistentes). Para saber qué número se corrige hacen falta dos
// columnas y ningún literal — pedir el resto sería descifrar PII para tirarla.
//
// Una solicitud SIN revisiones no es un error: es un borrador que nadie retrató
// todavía (una solicitud del carrito que llegó a `pending_approval` sin pasar por el
// pipeline). Devuelve el cero, y la señal saldrá con la marca y sin el par.
func lastRevisionTx(ctx context.Context, q querier, intakeID string) func() (no int, kind string, err error) {
	return func() (int, string, error) {
		var (
			no   int
			kind string
		)
		err := q.QueryRowContext(ctx, `
			SELECT revision_no, kind
			FROM public.intake_revisions
			WHERE intake_id = $1
			ORDER BY revision_no DESC
			LIMIT 1
		`, intakeID).Scan(&no, &kind)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return 0, "", nil
		case err != nil:
			return 0, "", fmt.Errorf("intakes: leer la revisión que se está corrigiendo: %w", err)
		}
		return no, kind, nil
	}
}
