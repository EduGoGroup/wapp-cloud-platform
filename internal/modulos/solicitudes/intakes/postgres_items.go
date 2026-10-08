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
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
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
	if _, err := uuid.Parse(intakeID); err != nil {
		return Detail{}, ErrNotFound
	}

	// Fuera de la clausura: WithTx puede REEJECUTARLA ante un deadlock y vale el
	// resultado del intento que confirmó (mismo criterio que UpdateStatus).
	var out Detail
	err := postgres.WithTx(ctx, p.db, func(tx *sql.Tx) error {
		if err := lockEditableTx(ctx, tx, tenantID, intakeID, expected); err != nil {
			return err
		}
		if err := replaceClientItemsTx(ctx, tx, intakeID, items); err != nil {
			return err
		}
		head, err := recomputeTotalTx(ctx, tx, tenantID, intakeID)
		if err != nil {
			return err
		}
		lines, err := itemsOf(ctx, tx, intakeID)
		if err != nil {
			return err
		}
		signal, err := correctionSignal(mode, lastRevisionTx(ctx, tx, intakeID))
		if err != nil {
			return err
		}
		rev, err := correctedRevision(intakeID, head.Total, lines, signal)
		if err != nil {
			return err
		}
		if _, err := p.insertRevisionOnce(ctx, tx, rev); err != nil {
			return err
		}
		revs, err := p.revisionsOf(ctx, tx, intakeID)
		if err != nil {
			return err
		}
		out = Detail{Intake: head, Items: lines, Revisions: revs}
		return nil
	})
	if err != nil {
		return Detail{}, err
	}
	return out, nil
}

// lockEditableTx toma el candado de la cabecera y comprueba que su estado siga
// siendo uno de los esperados. Distingue "no es del tenant" (ErrNotFound) de
// "alguien la movió" (ErrConflict) porque son dos respuestas distintas para quien
// llama: la primera no se reintenta nunca y la segunda se resuelve releyendo.
func lockEditableTx(ctx context.Context, tx *sql.Tx, tenantID, intakeID string, expected []string) error {
	var status string
	err := tx.QueryRowContext(ctx,
		`SELECT status FROM public.intakes WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		tenantID, intakeID).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("intakes: bloquear la solicitud para editarla: %w", err)
	}
	if !slices.Contains(expected, status) {
		return ErrConflict
	}
	return nil
}

// replaceClientItemsTx borra las líneas de CLIENTE y escribe las nuevas. Las del
// sistema (prefijo reservado: hoy la de envío, D-041.11) sobreviven intactas — con
// su precio puesto a mano, su etiqueta y su sitio—, y por eso el DELETE las excluye
// por el prefijo y no por el sku exacto: cuando la plataforma añada otra línea
// suya, esta puerta seguirá sin tocarla.
//
// El INSERT no puede colar una segunda línea de envío ni por accidente: la
// validación del dominio rechaza el prefijo reservado en la entrada y el índice
// único parcial de la 0045 lo convertiría en un error de escritura.
func replaceClientItemsTx(ctx context.Context, tx *sql.Tx, intakeID string, items []Item) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM public.intake_items
		WHERE intake_id = $1 AND left(sku, 1) <> $2
	`, intakeID, ReservedSKUPrefix); err != nil {
		return fmt.Errorf("intakes: retirar las líneas de la solicitud: %w", err)
	}

	// Una sentencia por línea: N está acotado por MaxEditableItems y van todas
	// dentro de la misma transacción, así que el coste es un puñado de viajes y no
	// una escritura parcial posible.
	for _, it := range items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, intakeID, it.SKU, it.Label, it.Customization, it.Qty, it.UnitPrice); err != nil {
			return fmt.Errorf("intakes: escribir la línea %q de la solicitud: %w", it.SKU, err)
		}
	}
	return nil
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
	if _, err := uuid.Parse(intakeID); err != nil {
		return Detail{}, ErrNotFound
	}

	// Fuera de la clausura: WithTx puede REEJECUTARLA ante un deadlock y vale el
	// resultado del intento que confirmó (mismo criterio que UpdateStatus).
	var out Detail
	err := postgres.WithTx(ctx, p.db, func(tx *sql.Tx) error {
		if err := lockEditableTx(ctx, tx, tenantID, intakeID, expected); err != nil {
			return err
		}
		if err := applyRevalidationItemsTx(ctx, tx, intakeID, rv); err != nil {
			return err
		}
		head, err := recomputeTotalTx(ctx, tx, tenantID, intakeID)
		if err != nil {
			return err
		}
		rev, err := revalidatedRevision(intakeID, rv, renderedText)
		if err != nil {
			return err
		}
		if _, err := p.insertRevisionOnce(ctx, tx, rev); err != nil {
			return err
		}
		lines, err := itemsOf(ctx, tx, intakeID)
		if err != nil {
			return err
		}
		revs, err := p.revisionsOf(ctx, tx, intakeID)
		if err != nil {
			return err
		}
		out = Detail{Intake: head, Items: lines, Revisions: revs}
		return nil
	})
	if err != nil {
		return Detail{}, err
	}
	return out, nil
}

// applyRevalidationItemsTx aplica el diff sobre las líneas: UPDATE de lo repreciado
// y DELETE de lo retirado. NUNCA un DELETE+INSERT del conjunto, y no es una
// optimización: el orden en que el cliente ve su pedido es el `added_at` de sus
// líneas (itemsOf ordena por él), así que reescribirlas todas le reordenaría el
// pedido por dentro sin que nadie lo hubiera tocado.
//
// Los dos WHERE excluyen el prefijo reservado aunque el diff ya excluya las líneas
// de la plataforma. Es redundante a propósito: en el propio SQL —no en una función
// pura que hay que ir a buscar— queda dicho que por este camino la línea de envío no
// se puede re-preciar ni borrar JAMÁS (criterio (d) del plan). Es la misma cerradura
// doble, y con el mismo literal, que el DELETE de replaceClientItemsTx.
//
// El UPDATE va por SKU y no por id, y eso alcanza a las DOS líneas cuando una se
// partió en dos por sus indicaciones (D-041.20). Es lo correcto: es el mismo
// artículo y el precio nuevo es el mismo para las dos.
func applyRevalidationItemsTx(ctx context.Context, tx *sql.Tx, intakeID string, rv Revalidation) error {
	for _, c := range rv.Repriced() {
		if _, err := tx.ExecContext(ctx, `
			UPDATE public.intake_items
			SET label = $3, unit_price = $4
			WHERE intake_id = $1 AND sku = $2 AND left(sku, 1) <> $5
		`, intakeID, c.SKU, c.Label, c.To, ReservedSKUPrefix); err != nil {
			return fmt.Errorf("intakes: re-preciar la línea %q de la solicitud: %w", c.SKU, err)
		}
	}
	for _, c := range rv.Removed() {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM public.intake_items
			WHERE intake_id = $1 AND sku = $2 AND left(sku, 1) <> $3
		`, intakeID, c.SKU, ReservedSKUPrefix); err != nil {
			return fmt.Errorf("intakes: retirar la línea %q de la solicitud: %w", c.SKU, err)
		}
	}
	return nil
}
