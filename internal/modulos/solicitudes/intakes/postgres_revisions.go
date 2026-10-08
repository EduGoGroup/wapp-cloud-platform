// Porta internal/intakes/postgres.go @ 64c181a

// postgres_revisions.go es la ESCRITURA de revisiones (el puerto RevisionWriter) y,
// en el verde, la puerta única de LECTURA de revisiones del adaptador, con la
// retención del literal de nivel 2 (Plan 044 · T3.5, migración 0079). Esa lectura no
// es un método exportado —la usan Get, ReplaceItems y ApplyRevalidation— pero sus
// promesas son observables por los tres, y por eso se escriben aquí.
//
// # La lectura de revisiones (lo que el verde porta a este fichero)
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
// El candado AST del orden «leer → podar → sellar» no es de este fichero (T6.18).

package intakes

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

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
	panic(pendiente.Implementar("intakes.Postgres.InsertRevision"))
}
