// Porta internal/flujos/store/repository_postgres.go @ c0c0c03
//
// Trozo de repository_postgres.go (05 E-13): public.intakes y public.intake_items.
// Las reglas comunes del adaptador están en la cabecera de repository_postgres.go.
//
// Los auxiliares no exportados del viejo viven aquí con nombre en inglés (E-11): isUUID
// era esUUID, intakeHeaderCols era cabeceraIntakeCols y scanIntakeHeader era
// escanearCabeceraIntake; execer, intakeItemCols, replaceIntakeItemsTx e
// insertIntakeItems conservan el suyo. reservedSKUPrefix vive en store_intakes.go,
// porque lo usan los DOS adaptadores. Dos reglas suyas son del contrato:
//
//   - el prefijo RESERVADO de los skus de la plataforma es el literal "_" (hoy solo
//     la línea de envío, D-041.11). Es el MISMO literal que el del dominio de
//     solicitudes y se repite aquí en vez de importarlo: el almacén del motor de
//     flujos no depende del dominio de solicitudes para escribir una tabla. Lo que
//     impide que diverjan es un test, que los compara;
//   - las dos lecturas de cabecera (GetOpenIntake y GetIntakeByEvent) comparten UNA
//     proyección: tienen que devolver exactamente la misma foto.
//
// 🔴 MUTANTES (nivel complejo): CloseIntake lleva las guardas que la suite tiene que
// morder una a una — el filtro por tenant, por contacto y por status = 'open', el
// ORDER BY created_at DESC, el FOR UPDATE, el COALESCE de event_id y el reemplazo (no
// inserción) de las líneas.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// execer es la cara de escritura común de *sql.DB y *sql.Tx (ExecContext), para
// que los INSERT en lote se reusen tanto en el camino autocommit como dentro de
// una transacción (CloseIntake).
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// intakeItemCols es el número de columnas por fila que escribe insertIntakeItems
// (orden de intake_items salvo id y added_at, que usan sus DEFAULT).
const intakeItemCols = 6

// isUUID dice si `s` puede estar en una columna de tipo `uuid`. Se pregunta ANTES de
// consultar para no depender del 22P02 de Postgres: es un predicado, no un error, y
// como predicado lo puede leer el linter y el que venga detrás. Gemelo del `isUUID` de
// internal/intakes, que existe por lo mismo.
func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// intakeHeaderCols es la proyección de public.intakes que comparten las lecturas
// de cabecera de este repositorio. Va en una constante porque son DOS consultas
// —por identidad de negocio (GetOpenIntake) y por evento (GetIntakeByEvent)— que
// tienen que devolver EXACTAMENTE la misma foto: dos caminos que hacen lo mismo y
// divergen en una columna es la forma clásica de que el pedido se vea distinto
// según por dónde se mire.
const intakeHeaderCols = `id::text, tenant_id, contact_id, session_id, status, total,
		       created_at, updated_at, expires_at, event_id::text`

// scanIntakeHeader lee UNA fila de cabecera con la proyección de
// intakeHeaderCols. sql.ErrNoRows ⇒ (zero, false, nil): "no hay" no es un fallo.
func scanIntakeHeader(row *sql.Row, what string) (Intake, bool, error) {
	var (
		o       Intake
		expires sql.NullTime
		eventID sql.NullString
	)
	err := row.Scan(
		&o.ID, &o.TenantID, &o.ContactID, &o.SessionID, &o.Status, &o.Total,
		&o.CreatedAt, &o.UpdatedAt, &expires, &eventID,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Intake{}, false, nil
	case err != nil:
		return Intake{}, false, fmt.Errorf("store: %s: %w", what, err)
	}
	if expires.Valid {
		o.ExpiresAt = expires.Time
	}
	// event_id NULL ⇒ EventID "" (fila legada pre-0054): es la señal con la que el
	// proyector sabe que puede ESTAMPAR el padre al reusar (D-043.21).
	if eventID.Valid {
		o.EventID = eventID.String
	}
	return o, true, nil
}

// UpsertIntake inserta o actualiza (upsert por id) la solicitud en public.intakes
// (Plan 016 · T0/T2). Idempotente por o.ID. ExpiresAt zero se materializa como
// NULL. created_at/updated_at usan now() (updated_at se refresca en el UPDATE).
//
// event_id (D-043.21, migración 0054) se escribe al NACER la fila y en el UPDATE
// va protegido con COALESCE(intakes.event_id, EXCLUDED.event_id): un event_id ya
// declarado NO se pisa jamás —ni con otro valor ni con NULL—, y un NULL legado
// (fila pre-0054) SÍ se estampa cuando el proyector reusa la solicitud con el
// evento en la mano. La política de QUÉ estampar es del proyector
// (cart.ensureOpenIntake); esta sentencia solo garantiza que ninguna escritura
// pueda des-declarar a un padre.
//
// customer_note NO se escribe: ni el INSERT ni el UPDATE mencionan la columna, así que
// nace con su DEFAULT (”) y un upsert sobre una solicitud ya cerrada no puede borrar
// la nota que puso el cierre. El CreatedAt, el UpdatedAt y el CustomerNote del
// argumento se ignoran.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: upsert solicitud: %w"
func (r *PostgresRepository) UpsertIntake(ctx context.Context, o Intake) error {
	var expires sql.NullTime
	if !o.ExpiresAt.IsZero() {
		expires = sql.NullTime{Time: o.ExpiresAt, Valid: true}
	}
	var eventID sql.NullString
	if o.EventID != "" {
		eventID = sql.NullString{String: o.EventID, Valid: true}
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.intakes
			(id, tenant_id, contact_id, session_id, status, total, expires_at, event_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), now())
		ON CONFLICT (id) DO UPDATE
		SET tenant_id  = EXCLUDED.tenant_id,
		    contact_id = EXCLUDED.contact_id,
		    session_id = EXCLUDED.session_id,
		    status     = EXCLUDED.status,
		    total      = EXCLUDED.total,
		    expires_at = EXCLUDED.expires_at,
		    event_id   = COALESCE(public.intakes.event_id, EXCLUDED.event_id),
		    updated_at = now()
	`, o.ID, o.TenantID, o.ContactID, o.SessionID, o.Status, o.Total, expires, eventID)
	if err != nil {
		return fmt.Errorf("store: upsert solicitud: %w", err)
	}
	return nil
}

// GetOpenIntake devuelve la solicitud "open" del contacto para (tenantID, contactID);
// found=false sin error si no hay (Plan 016 · T2/T3). Usa el índice intakes_open_idx.
// Si hubiera varias "open", la más reciente (ORDER BY created_at DESC LIMIT 1). La
// proyección NO trae customer_note: la cabecera sale con CustomerNote "".
// expires_at y event_id NULL salen como el cero de Go.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: leer solicitud abierta: %w"
func (r *PostgresRepository) GetOpenIntake(ctx context.Context, tenantID, contactID string) (Intake, bool, error) {
	return scanIntakeHeader(r.db.QueryRowContext(ctx, `
		SELECT `+intakeHeaderCols+`
		FROM public.intakes
		WHERE tenant_id = $1 AND contact_id = $2 AND status = 'open'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID, contactID), "leer solicitud abierta")
}

// GetIntakeByEvent implementa IntakeReader: la solicitud que declara `eventID` como
// padre, SIN filtro de estado (D-044.46). La sirve el índice único parcial
// intakes_event_id_uidx, que además garantiza que la fila sea a lo sumo una.
//
// Un eventID que no parsea como UUID no puede estar en la columna (es de tipo
// `uuid`): mismo destino que "no hay" —found=false, sin error—, sin molestar a
// Postgres con un 22P02. Es el mismo guard, y por la misma razón, que el de
// intakes.Postgres.AbandonByEvent, la escritura gemela de esta lectura.
//
// Misma proyección que GetOpenIntake (sin customer_note): las dos lecturas devuelven
// exactamente la misma foto de la misma fila.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: leer la solicitud del evento: %w"
func (r *PostgresRepository) GetIntakeByEvent(ctx context.Context, tenantID, eventID string) (Intake, bool, error) {
	if !isUUID(eventID) {
		return Intake{}, false, nil
	}
	return scanIntakeHeader(r.db.QueryRowContext(ctx, `
		SELECT `+intakeHeaderCols+`
		FROM public.intakes
		WHERE tenant_id = $1 AND event_id = $2
		ORDER BY created_at, id
		LIMIT 1
	`, tenantID, eventID), "leer la solicitud del evento")
}

// ListIntakeItems devuelve las líneas de la solicitud en el orden en que las ve el
// cliente (added_at, id), que es el MISMO ORDEN Y LA MISMA PROYECCIÓN que usa
// intakes.itemsOf: dos lecturas de la misma tabla que se contradijeran en el orden
// enseñarían el pedido de dos formas distintas según por dónde se mire.
//
// El UUID se valida ANTES de consultar para no depender del error 22P02 de Postgres
// (el repositorio en memoria no lo daría, y las dos implementaciones tienen que
// contestar lo mismo a la misma pregunta).
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: listar líneas de solicitud: id %q inválido: %w"
//   - "store: listar líneas de solicitud: %w"
//   - "store: cerrar filas de líneas: %w"
//   - "store: escanear línea de solicitud: %w"
//   - "store: iterar líneas de solicitud: %w"
func (r *PostgresRepository) ListIntakeItems(ctx context.Context, intakeID string) (out []IntakeItem, err error) {
	if _, perr := uuid.Parse(intakeID); perr != nil {
		return nil, fmt.Errorf("store: listar líneas de solicitud: id %q inválido: %w", intakeID, perr)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT sku, label, customization, qty, unit_price, added_at
		FROM public.intake_items
		WHERE intake_id = $1
		ORDER BY added_at, id
	`, intakeID)
	if err != nil {
		return nil, fmt.Errorf("store: listar líneas de solicitud: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("store: cerrar filas de líneas: %w", cerr)
		}
	}()

	out = make([]IntakeItem, 0)
	for rows.Next() {
		it := IntakeItem{IntakeID: intakeID}
		if serr := rows.Scan(&it.SKU, &it.Label, &it.Customization, &it.Qty, &it.UnitPrice, &it.AddedAt); serr != nil {
			return nil, fmt.Errorf("store: escanear línea de solicitud: %w", serr)
		}
		out = append(out, it)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("store: iterar líneas de solicitud: %w", rerr)
	}
	return out, nil
}

// ReplaceIntakeItems deja las líneas de cliente de la solicitud EXACTAMENTE en
// `items`, en UNA transacción (Plan 043 · Ola 3): borrar y volver a escribir tienen
// que ser un solo acto o existiría un instante en el que el pedido no tiene líneas,
// y ese instante lo puede leer el CRM.
//
// El DELETE excluye el prefijo reservado (`left(sku, 1) <> "_"`): las líneas de la
// plataforma —hoy la de envío, D-041.11— llevan su precio puesto a mano y no son del
// carrito. Después, UN INSERT multi-fila con las líneas en el orden de `items` (la
// lectura ordena por (added_at, id) y el BIGSERIAL sigue el orden de los VALUES);
// len(items)==0 no inserta nada, así que BORRA las de cliente. customization viaja
// SIEMPRE, aunque esté vacía (NOT NULL: su vacío es «sin personalización», D-041.17);
// added_at es el DEFAULT now() y el AddedAt del argumento se ignora. No toca la
// cabecera de la solicitud.
//
// Textos de error (literales, con el error de origen envuelto en %w; los comparte
// CloseIntake, que reemplaza las líneas con el mismo camino):
//   - "store: retirar líneas de solicitud: %w"
//   - "store: insertar líneas de solicitud: %w"
func (r *PostgresRepository) ReplaceIntakeItems(ctx context.Context, intakeID string, items []IntakeItem) error {
	return postgres.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		return replaceIntakeItemsTx(ctx, tx, intakeID, items)
	})
}

// replaceIntakeItemsTx retira las líneas de CLIENTE de la solicitud y escribe las
// nuevas, sobre una transacción ya abierta. Es el ÚNICO camino por el que el motor
// de flujos escribe intake_items —lo usan la proyección de item_added y el cierre—,
// y por eso escribir dos veces el mismo conjunto deja el mismo conjunto.
//
// El DELETE excluye el prefijo reservado (copiado de intakes.replaceClientItemsTx,
// que es como el CRM rehace las líneas en una revisión): las líneas de wApp —hoy la
// de envío, D-041.11— llevan su precio puesto a mano y no son del carrito. Hoy no
// pueden coexistir con una escritura del carrito, porque la de envío se cuelga
// DESPUÉS del cierre y a una solicitud cerrada ya no le entran item_added; la
// exclusión está para que ese orden pueda cambiar sin que nadie pierda una línea.
//
// El orden del pedido se conserva aunque se reescriba entero: la lectura ordena por
// (added_at, id) y las filas de un INSERT multi-fila reciben el BIGSERIAL en el orden
// de los VALUES, que es el del carrito. La advertencia de applyRevalidationItemsTx
// —«reescribirlas todas le reordenaría el pedido»— aplica a un DELETE+INSERT PARCIAL,
// no a uno que reescribe el conjunto completo en su orden.
func replaceIntakeItemsTx(ctx context.Context, ex execer, intakeID string, items []IntakeItem) error {
	if _, err := ex.ExecContext(ctx, `
		DELETE FROM public.intake_items
		WHERE intake_id = $1 AND left(sku, 1) <> $2
	`, intakeID, reservedSKUPrefix); err != nil {
		return fmt.Errorf("store: retirar líneas de solicitud: %w", err)
	}
	return insertIntakeItems(ctx, ex, intakeID, items)
}

// insertIntakeItems ejecuta el INSERT multi-fila de líneas sobre cualquier execer.
// len(items)==0 es un no-op. NO es un punto de entrada: se llama SIEMPRE detrás del
// DELETE de replaceIntakeItemsTx, porque una solicitud recibe hoy varias escrituras
// de su conjunto de líneas y añadirlas sin retirar las anteriores las duplicaría.
func insertIntakeItems(ctx context.Context, ex execer, intakeID string, items []IntakeItem) error {
	if len(items) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(items))
	args := make([]any, 0, len(items)*intakeItemCols)
	for i, it := range items {
		base := i * intakeItemCols
		placeholders = append(placeholders, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6,
		))
		// Customization viaja SIEMPRE, aunque esté vacía: la columna es NOT NULL y
		// su vacío significa "sin personalización" (D-041.17), no "no sé".
		args = append(args, intakeID, it.SKU, it.Label, it.Customization, it.Qty, it.UnitPrice)
	}
	// #nosec G202 -- solo se concatenan placeholders generados ($1, $2, ...); los
	// valores viajan siempre parametrizados en args, nunca interpolados en el SQL.
	query := `
		INSERT INTO public.intake_items
			(intake_id, sku, label, customization, qty, unit_price)
		VALUES ` + strings.Join(placeholders, ", ")
	if _, err := ex.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("store: insertar líneas de solicitud: %w", err)
	}
	return nil
}

// MarkIntakeStatus transiciona el estado de una solicitud (por id) y fija su total,
// refrescando updated_at (Plan 016 · T2/T3). status es "closed" | "cancelled" |
// "expired", pero no se valida. Busca por id y nada más (no acota por tenant); si la
// solicitud no existe es un no-op sin error. No toca las líneas ni ninguna otra
// columna.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: marcar estado de solicitud: %w"
func (r *PostgresRepository) MarkIntakeStatus(ctx context.Context, intakeID, status string, total float64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.intakes
		SET status = $2, total = $3, updated_at = now()
		WHERE id = $1
	`, intakeID, status, total)
	if err != nil {
		return fmt.Errorf("store: marcar estado de solicitud: %w", err)
	}
	return nil
}

// CloseIntake cierra ATÓMICAMENTE la solicitud abierta del contacto e inserta sus
// líneas en la MISMA transacción (Plan 027 · Ola 1 · T4, cierra H4), vía el helper
// único postgres.WithTx (rollback inmune a panic + retry 40P01/40001). Bloquea la
// solicitud "open" con FOR UPDATE: dos cierres concurrentes del mismo contacto se
// serializan (el segundo la ve ya "closed" y no crea otra). Si no había solicitud
// abierta, crea una "closed" coherente. Garantiza que una solicitud closed nunca quede
// sin líneas.
//
// Devuelve el ID de la solicitud cerrada porque quien cierra necesita saber SOBRE
// QUÉ cerró: es lo que permite colgarle la revisión 1 (ADR-0031 §3). Sin él, el
// llamante tendría que releer "la última cerrada de este contacto", que es una
// carrera con el siguiente carrito.
//
// CON solicitud "open" del (tenant, contacto) —la más reciente por created_at—:
// status 'closed', total y customer_note los del argumento, updated_at = now();
// event_id con COALESCE (rellena un NULL legado, JAMÁS pisa un padre declarado);
// tenant, contacto, sesión y created_at quedan como estaban (in.SessionID NO se usa).
// SIN ella: nace una fila 'closed' con id uuid nuevo, in.SessionID, in.EventID, total
// y customer_note. Una solicitud en cualquier otro estado ni se cierra ni cuenta.
//
// En los dos casos las líneas se REEMPLAZAN por in.Items con la regla de
// ReplaceIntakeItems (las del prefijo reservado sobreviven; in.Items vacío deja la
// solicitud sin líneas de cliente), en la misma transacción. Si la transacción falla
// devuelve ("", err) y no queda nada escrito.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: insertar solicitud cerrada: %w"
//   - "store: bloquear solicitud abierta: %w"
//   - "store: cerrar solicitud: %w"
func (r *PostgresRepository) CloseIntake(ctx context.Context, in IntakeClose) (string, error) {
	// Se declara FUERA de la clausura porque WithTx puede REEJECUTARLA ante un
	// deadlock: cada intento reasigna el id y el que sobrevive es el del intento
	// que confirmó.
	var closedID string
	err := postgres.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		var eventID sql.NullString
		if in.EventID != "" {
			eventID = sql.NullString{String: in.EventID, Valid: true}
		}
		var intakeID string
		err := tx.QueryRowContext(ctx, `
			SELECT id::text FROM public.intakes
			WHERE tenant_id = $1 AND contact_id = $2 AND status = 'open'
			ORDER BY created_at DESC
			LIMIT 1
			FOR UPDATE
		`, in.TenantID, in.ContactID).Scan(&intakeID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			intakeID = uuid.NewString()
			// La fila "closed" coherente nace, como cualquier otra, declarando a su
			// padre (event_id, D-043.21): sin él, el CHECK de la 0054 la rechaza.
			if _, ierr := tx.ExecContext(ctx, `
				INSERT INTO public.intakes
					(id, tenant_id, contact_id, session_id, status, total, customer_note, event_id, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 'closed', $5, $6, $7, now(), now())
			`, intakeID, in.TenantID, in.ContactID, in.SessionID, in.Total, in.CustomerNote, eventID); ierr != nil {
				return fmt.Errorf("store: insertar solicitud cerrada: %w", ierr)
			}
		case err != nil:
			return fmt.Errorf("store: bloquear solicitud abierta: %w", err)
		default:
			// customer_note se escribe en el CIERRE y no al abrir la solicitud: el
			// cliente la teclea en el resumen, que es el último paso antes de
			// confirmar. La columna es NOT NULL, así que el vacío viaja igual que el
			// texto —"sin indicación" es un valor, no una omisión— y una solicitud
			// cerrada dos veces (reintento del 40P01) acaba con el mismo contenido.
			//
			// event_id con COALESCE, igual que en UpsertIntake: rellena un NULL
			// legado (pre-0054) y JAMÁS pisa un padre ya declarado (D-043.21).
			if _, uerr := tx.ExecContext(ctx, `
				UPDATE public.intakes
				SET status = 'closed', total = $2, customer_note = $3,
				    event_id = COALESCE(event_id, $4), updated_at = now()
				WHERE id = $1
			`, intakeID, in.Total, in.CustomerNote, eventID); uerr != nil {
				return fmt.Errorf("store: cerrar solicitud: %w", uerr)
			}
		}
		closedID = intakeID
		// REEMPLAZO, no INSERT: la solicitud puede llegar al cierre con las líneas que
		// la proyección de item_added ya materializó mientras estaba abierta (Plan 043 ·
		// Ola 3). Insertarlas otra vez las duplicaría todas.
		return replaceIntakeItemsTx(ctx, tx, intakeID, in.Items)
	})
	if err != nil {
		return "", err
	}
	return closedID, nil
}
