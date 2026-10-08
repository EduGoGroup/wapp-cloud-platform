// Porta internal/intakes/postgres.go @ 64c181a

// postgres_items.go son las dos escrituras que cambian las LÍNEAS de una solicitud:
// la edición manual del dueño y la revalidación contra el catálogo. Las dos son UNA
// transacción (postgres.WithTx) que empieza bloqueando la cabecera, y las dos dejan
// su revisión DENTRO de esa transacción: la edición y su rastro se confirman juntos
// o no se confirma ninguno.
//
// Lo que comparten, y que el verde porta a este fichero:
//
//   - EL BLOQUEO: SELECT status … FOR UPDATE acotado por tenant. Es el mismo punto de
//     serialización que toman el CAS de UpdateStatus, EnsureShippingLine y Discard.
//     Sin fila ⇒ ErrNotFound; con un estado ALMACENADO que no está en `expected`
//     (comparación exacta, sin normalizar) ⇒ ErrConflict. Son dos respuestas
//     distintas para quien llama, y las dos vuelven sin envolver y sin escribir nada;
//   - LAS LÍNEAS DEL SISTEMA NO SE TOCAN: toda sentencia sobre líneas excluye las de
//     SKU con el prefijo reservado (ReservedSKUPrefix), con el prefijo como
//     argumento. La línea de envío sobrevive a una edición;
//   - EL TOTAL se recalcula ENTERO desde las líneas que quedaron, en la base;
//   - LA REVISIÓN se escribe por la transacción y sin reintento de numeración: un
//     23505 aborta la transacción entera (ver InsertRevision);
//   - el Detail devuelto se lee DENTRO de la transacción (cabecera con el total
//     nuevo, líneas y revisiones, la recién escrita incluida) y NO rellena
//     BuyerDataPresent.

package intakes

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ReplaceItems implementa Store.ReplaceItems: la edición manual del dueño (T4.10).
// Un intakeID que no es un UUID ⇒ ErrNotFound sin tocar la base. El orden dentro de
// la transacción no es negociable:
//
//  1. bloquear la cabecera y comprobar el estado (ErrNotFound / ErrConflict);
//  2. BORRAR las líneas de cliente e INSERTAR las nuevas, una sentencia por línea y
//     en el orden recibido. `items` vacío es válido: deja la solicitud sin líneas de
//     cliente;
//  3. recalcular el total;
//  4. releer las líneas (así la revisión retrata lo YA persistido, con la de envío
//     incluida, y no lo que se pretendía escribir);
//  5. si mode es EditAsCorrection, leer la ÚLTIMA revisión de la solicitud —con la
//     cabecera ya bloqueada: fuera del candado podría apuntar a una que otra
//     escritura acababa de dejar— para componer la señal few-shot (T4.4). Con
//     EditPlain esa lectura NO se hace y la revisión no lleva señal;
//  6. escribir la revisión RevisionKindCorrected;
//  7. releer las revisiones.
//
// Errores de la base, envueltos con %w, devolviendo Detail{} y con la transacción
// revertida:
//
//   - "intakes: bloquear la solicitud para editarla: " — falla el bloqueo;
//   - "intakes: retirar las líneas de la solicitud: " — falla el borrado;
//   - `intakes: escribir la línea "<sku>" de la solicitud: ` — falla el INSERT de esa
//     línea (el SKU va con %q);
//   - "intakes: leer solicitud: " — la fila del total no se puede leer;
//   - "intakes: leer la revisión que se está corrigiendo: " — falla la lectura de la
//     última revisión (solo en EditAsCorrection);
//   - los de las líneas (postgres_read.go) y los de las revisiones
//     (postgres_revisions.go).
func (p *Postgres) ReplaceItems(ctx context.Context, tenantID, intakeID string, items []Item, expected []string, mode EditMode) (Detail, error) {
	panic(pendiente.Implementar("intakes.Postgres.ReplaceItems"))
}

// ApplyRevalidation implementa Store.ApplyRevalidation: aplica a las líneas lo que
// la revalidación contra el catálogo encontró y deja su revisión. Un intakeID que no
// es un UUID ⇒ ErrNotFound sin tocar la base. Dentro de la transacción, en este
// orden:
//
//  1. bloquear la cabecera y comprobar el estado (ErrNotFound / ErrConflict);
//  2. por cada cambio de PRECIO (rv.Repriced()), un UPDATE de label y unit_price de
//     esa línea; después, por cada línea RETIRADA (rv.Removed()), un DELETE. Solo
//     tocan la línea de ese SKU, y nunca una del sistema. Una revalidación sin
//     cambios no emite ninguna de estas sentencias;
//  3. recalcular el total;
//  4. escribir la revisión RevisionKindRevalidated con `renderedText` como texto;
//  5. releer líneas y revisiones.
//
// Errores de la base, envueltos con %w, devolviendo Detail{} y con la transacción
// revertida:
//
//   - "intakes: bloquear la solicitud para editarla: " — falla el bloqueo;
//   - `intakes: re-preciar la línea "<sku>" de la solicitud: ` — falla el UPDATE;
//   - `intakes: retirar la línea "<sku>" de la solicitud: ` — falla el DELETE;
//   - "intakes: leer solicitud: " — la fila del total no se puede leer;
//   - los de las líneas (postgres_read.go) y los de las revisiones
//     (postgres_revisions.go).
func (p *Postgres) ApplyRevalidation(ctx context.Context, tenantID, intakeID string, rv Revalidation, renderedText string, expected []string) (Detail, error) {
	panic(pendiente.Implementar("intakes.Postgres.ApplyRevalidation"))
}
