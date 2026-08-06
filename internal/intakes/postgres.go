package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// Postgres implementa Store y RevisionWriter con SQL raw sobre public.intakes,
// public.intake_items y public.intake_revisions (las dos primeras del Plan 016,
// renombradas por la migración 0041; la tercera nace en la 0045). Sobre solicitudes
// y líneas es SOLO LECTURA más la transición de estado —aquí no se crean solicitudes
// ni se tocan líneas, eso es del módulo cart—; lo único que ESCRIBE de cero son
// revisiones.
type Postgres struct {
	db *sql.DB
}

// NewPostgres construye el store sobre el pool dado.
func NewPostgres(db *sql.DB) *Postgres {
	return &Postgres{db: db}
}

// intakeCols es la proyección de la cabecera. tenant_id NO se lee: quien consulta
// ya es el dueño del tenant (INV-8) y repetirlo en la respuesta no informa de nada.
//
// customer_note (D-041.19) va al final y no en medio: el orden de esta lista es el
// de los Scan de scanIntake y scanDetailRow, y meter una columna entre dos ya
// existentes obligaría a mover los destinos de los dos escaneos a la vez —un
// descuadre que compila y devuelve el estado en el total—.
const intakeCols = `id::text, contact_id, session_id, status, total, created_at, updated_at, customer_note`

// intakeFilterWhere es el predicado COMPARTIDO por la página y por su total: si
// divergieran, el paginador mentiría. Cada filtro es opcional por el patrón
// "$n IS NULL OR …", que deja el plan estable sin construir SQL dinámico
// (concatenación de constantes: nada de esta cadena viene del usuario).
const intakeFilterWhere = `
	WHERE tenant_id = $1
	  AND ($2::timestamptz IS NULL OR created_at >= $2)
	  AND ($3::timestamptz IS NULL OR created_at <  $3)
	  AND ($4::text[]      IS NULL OR status = ANY($4))
	  AND ($5::text        IS NULL OR session_id = $5)`

// listIntakesQuery pagina las coincidencias, más recientes primero. El desempate
// por id hace el orden TOTAL: dos solicitudes con el mismo created_at no pueden
// aparecer dos veces (ni desaparecer) al pasar de página.
const listIntakesQuery = `SELECT ` + intakeCols + ` FROM public.intakes` + intakeFilterWhere + `
	ORDER BY created_at DESC, id DESC
	LIMIT $6 OFFSET $7`

// countIntakesQuery cuenta las MISMAS coincidencias sin paginar.
const countIntakesQuery = `SELECT count(*) FROM public.intakes` + intakeFilterWhere

// List implementa Store: página + total con el mismo predicado.
func (p *Postgres) List(ctx context.Context, tenantID string, f Filter) (out []Intake, total int, err error) {
	f = f.Normalized()
	args := filterArgs(tenantID, f)

	if err := p.db.QueryRowContext(ctx, countIntakesQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("intakes: contar solicitudes: %w", err)
	}
	if total == 0 {
		return []Intake{}, 0, nil
	}

	rows, err := p.db.QueryContext(ctx, listIntakesQuery, append(args, f.PageSize, f.Offset())...)
	if err != nil {
		return nil, 0, fmt.Errorf("intakes: listar solicitudes: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, total, err = nil, 0, fmt.Errorf("intakes: cerrar filas de solicitudes: %w", cerr)
		}
	}()

	out = make([]Intake, 0, f.PageSize)
	for rows.Next() {
		in, serr := scanIntake(rows)
		if serr != nil {
			return nil, 0, serr
		}
		out = append(out, in)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, 0, fmt.Errorf("intakes: recorrer solicitudes: %w", rerr)
	}
	return out, total, nil
}

// listIntakeDetailsQuery trae las cabeceras que casan con el filtro Y sus líneas
// en UNA sola consulta. Dos decisiones que no son de estilo:
//
//   - La cota (`LIMIT $6`) va DENTRO del CTE, sobre las cabeceras: si estuviera
//     fuera cortaría filas del join y devolvería solicitudes con las líneas a
//     medias, que es exactamente el error que un export no puede permitirse.
//   - El join es LEFT: una solicitud sin líneas (una `open` que nadie llegó a
//     llenar) sigue apareciendo, con las columnas de línea en NULL. Con INNER JOIN
//     desaparecería del export sin que nadie se enterara.
//
// El predicado es el MISMO de la lista (intakeFilterWhere), así que el export no
// puede divergir de lo que la bandeja muestra. `p.id` es text (intakeCols lo
// castea) y por eso el join lo devuelve a uuid.
const listIntakeDetailsQuery = `
	WITH page AS (
		SELECT ` + intakeCols + ` FROM public.intakes` + intakeFilterWhere + `
		ORDER BY created_at DESC, id DESC
		LIMIT $6
	)
	SELECT p.id, p.contact_id, p.session_id, p.status, p.total, p.created_at, p.updated_at,
	       p.customer_note,
	       it.sku, it.label, it.customization, it.qty, it.unit_price, it.added_at
	FROM page p
	LEFT JOIN public.intake_items it ON it.intake_id = p.id::uuid
	ORDER BY p.created_at DESC, p.id DESC, it.added_at, it.id`

// ListDetails implementa Store: cabeceras + líneas del filtro, sin paginar y sin
// N+1 (una consulta, no una por solicitud).
func (p *Postgres) ListDetails(ctx context.Context, tenantID string, f Filter, limit int) (out []Detail, err error) {
	if limit <= 0 {
		return []Detail{}, nil
	}
	rows, err := p.db.QueryContext(ctx, listIntakeDetailsQuery,
		append(filterArgs(tenantID, f.Normalized()), limit)...)
	if err != nil {
		return nil, fmt.Errorf("intakes: listar solicitudes con líneas: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("intakes: cerrar filas del export: %w", cerr)
		}
	}()

	out = []Detail{}
	for rows.Next() {
		head, item, hasItem, serr := scanDetailRow(rows)
		if serr != nil {
			return nil, serr
		}
		// Las filas llegan agrupadas por solicitud (ORDER BY de la consulta): basta
		// comparar con la última para saber si empieza una cabecera nueva.
		if len(out) == 0 || out[len(out)-1].ID != head.ID {
			out = append(out, Detail{Intake: head, Items: []Item{}})
		}
		if hasItem {
			last := &out[len(out)-1]
			last.Items = append(last.Items, item)
		}
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("intakes: recorrer el export: %w", rerr)
	}
	return out, nil
}

// scanDetailRow lee una fila del join: cabecera (siempre) + línea (NULL cuando la
// solicitud no tiene ninguna). Normaliza el estado en el mismo punto que scanIntake.
func scanDetailRow(sc rowScanner) (Intake, Item, bool, error) {
	var (
		in                 Intake
		sku, label, custom sql.NullString
		qty                sql.NullInt64
		unitPrice          sql.NullFloat64
		addedAt            sql.NullTime
	)
	if err := sc.Scan(&in.ID, &in.ContactID, &in.SessionID, &in.Status, &in.Total,
		&in.CreatedAt, &in.UpdatedAt, &in.CustomerNote,
		&sku, &label, &custom, &qty, &unitPrice, &addedAt); err != nil {
		return Intake{}, Item{}, false, fmt.Errorf("intakes: leer fila del export: %w", err)
	}
	in.Status = NormalizeStatus(in.Status)
	if !sku.Valid && !label.Valid && !qty.Valid {
		return in, Item{}, false, nil // solicitud sin líneas (LEFT JOIN)
	}
	// La columna es NOT NULL DEFAULT '': el sql.NullString es por el LEFT JOIN, no
	// porque la fila pueda tener NULL. Sin línea, custom.String ya es "".
	return in, Item{
		SKU: sku.String, Label: label.String, Customization: custom.String,
		Qty: int(qty.Int64), UnitPrice: unitPrice.Float64, AddedAt: addedAt.Time,
	}, true, nil
}

// filterArgs arma los cinco argumentos del predicado compartido. Un filtro sin
// valor viaja como NULL (any(nil)) para que la rama "$n IS NULL" lo desactive.
func filterArgs(tenantID string, f Filter) []any {
	var from, to, statuses, session any
	if !f.From.IsZero() {
		from = f.From
	}
	if !f.To.IsZero() {
		to = f.To
	}
	if f.Status != "" {
		// Las filas legadas guardan `closed` donde el dominio dice `confirmed`:
		// el filtro tiene que alcanzarlas (D-041.10, sin migración de datos).
		statuses = StoredVariants(f.Status)
	}
	if f.SessionID != "" {
		session = f.SessionID
	}
	return []any{tenantID, from, to, statuses, session}
}

// rowScanner abstrae *sql.Row y *sql.Rows para compartir el escaneo de cabecera.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanIntake lee una cabecera y NORMALIZA su estado: el `closed` que sigue
// escribiendo el módulo cart sale de aquí como `confirmed`, en un único punto.
func scanIntake(sc rowScanner) (Intake, error) {
	var in Intake
	if err := sc.Scan(&in.ID, &in.ContactID, &in.SessionID, &in.Status, &in.Total,
		&in.CreatedAt, &in.UpdatedAt, &in.CustomerNote); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Intake{}, err // lo traduce el llamante (ErrNotFound)
		}
		return Intake{}, fmt.Errorf("intakes: leer solicitud: %w", err)
	}
	in.Status = NormalizeStatus(in.Status)
	return in, nil
}

// Get implementa Store: cabecera + líneas, acotado al tenant. Un id que ni
// siquiera es un UUID no se consulta (la columna es UUID y la BD respondería con
// un error de sintaxis): es un 404 como cualquier otro id inexistente.
func (p *Postgres) Get(ctx context.Context, tenantID, intakeID string) (Detail, error) {
	if _, err := uuid.Parse(intakeID); err != nil {
		return Detail{}, ErrNotFound
	}

	head, err := scanIntake(p.db.QueryRowContext(ctx,
		`SELECT `+intakeCols+` FROM public.intakes WHERE tenant_id = $1 AND id = $2`,
		tenantID, intakeID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Del tenant B no existe: no se distingue de inexistente (INV-8).
		return Detail{}, ErrNotFound
	case err != nil:
		return Detail{}, err
	}

	items, err := p.items(ctx, intakeID)
	if err != nil {
		return Detail{}, err
	}
	revs, err := p.revisions(ctx, intakeID)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Intake: head, Items: items, Revisions: revs}, nil
}

// items lee las líneas de una solicitud en el orden en que se añadieron. No filtra
// por tenant: la cabecera ya se validó contra el tenant y la FK garantiza que estas
// líneas son suyas.
func (p *Postgres) items(ctx context.Context, intakeID string) (out []Item, err error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT sku, label, customization, qty, unit_price, added_at
		FROM public.intake_items
		WHERE intake_id = $1
		ORDER BY added_at, id
	`, intakeID)
	if err != nil {
		return nil, fmt.Errorf("intakes: listar líneas: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("intakes: cerrar filas de líneas: %w", cerr)
		}
	}()

	out = []Item{}
	for rows.Next() {
		var it Item
		if serr := rows.Scan(&it.SKU, &it.Label, &it.Customization, &it.Qty, &it.UnitPrice, &it.AddedAt); serr != nil {
			return nil, fmt.Errorf("intakes: leer línea: %w", serr)
		}
		out = append(out, it)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("intakes: recorrer líneas: %w", rerr)
	}
	return out, nil
}

// revisionCols es la proyección de una revisión. `id` NO se lee: la identidad que
// significa algo fuera de la BD es (intake_id, revision_no).
const revisionCols = `revision_no, kind, payload, rendered_text, created_by, created_at`

// revisions lee las revisiones de una solicitud en orden cronológico. No filtra por
// tenant: la cabecera ya se validó contra el tenant y la FK garantiza que estas
// revisiones son suyas (mismo criterio que items).
func (p *Postgres) revisions(ctx context.Context, intakeID string) (out []Revision, err error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT `+revisionCols+`
		FROM public.intake_revisions
		WHERE intake_id = $1
		ORDER BY revision_no
	`, intakeID)
	if err != nil {
		return nil, fmt.Errorf("intakes: listar revisiones: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("intakes: cerrar filas de revisiones: %w", cerr)
		}
	}()

	out = []Revision{}
	for rows.Next() {
		rev := Revision{IntakeID: intakeID}
		var rendered, createdBy sql.NullString
		if serr := rows.Scan(&rev.RevisionNo, &rev.Kind, &rev.Payload,
			&rendered, &createdBy, &rev.CreatedAt); serr != nil {
			return nil, fmt.Errorf("intakes: leer revisión: %w", serr)
		}
		rev.RenderedText, rev.CreatedBy = rendered.String, createdBy.String
		out = append(out, rev)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("intakes: recorrer revisiones: %w", rerr)
	}
	return out, nil
}

// insertRevisionQuery numera y escribe la revisión en UNA sentencia: el
// `COALESCE(MAX(revision_no), 0) + 1` se evalúa DENTRO del INSERT, así que entre el
// cálculo y la escritura no cabe una lectura ajena. Lo que sí cabe es otro INSERT
// concurrente que calcule el mismo número: ese pierde contra el UNIQUE (23505) y lo
// resuelve el reintento de InsertRevision, no un candado que serializaría a todos.
const insertRevisionQuery = `
	INSERT INTO public.intake_revisions
		(intake_id, revision_no, kind, payload, rendered_text, created_by)
	SELECT $1::uuid, COALESCE(MAX(revision_no), 0) + 1, $2, $3::jsonb, $4, $5
	FROM public.intake_revisions WHERE intake_id = $1::uuid
	RETURNING ` + revisionCols

// maxRevisionAttempts acota los reintentos por colisión de numeración. Los
// escritores de revisiones de UNA solicitud son pocos y esporádicos (un cierre de
// carrito, una corrección del dueño, una revalidación): 5 intentos sobran, y
// agotarlos devuelve el error en vez de girar indefinidamente.
const maxRevisionAttempts = 5

// InsertRevision implementa RevisionWriter: numera la revisión con el siguiente
// correlativo de esa solicitud y la escribe. rev.RevisionNo de entrada se ignora.
func (p *Postgres) InsertRevision(ctx context.Context, rev Revision) (Revision, error) {
	if len(rev.Payload) == 0 {
		return Revision{}, ErrEmptyRevisionPayload
	}
	if _, err := uuid.Parse(rev.IntakeID); err != nil {
		return Revision{}, ErrNotFound
	}

	var lastErr error
	for attempt := 0; attempt < maxRevisionAttempts; attempt++ {
		out, err := p.insertRevisionOnce(ctx, rev)
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

// insertRevisionOnce ejecuta UN intento de numeración+escritura.
func (p *Postgres) insertRevisionOnce(ctx context.Context, rev Revision) (Revision, error) {
	out := Revision{IntakeID: rev.IntakeID}
	var rendered, createdBy sql.NullString
	err := p.db.QueryRowContext(ctx, insertRevisionQuery,
		rev.IntakeID, rev.Kind, []byte(rev.Payload),
		nullableText(rev.RenderedText), nullableText(rev.CreatedBy),
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

// UpdateStatus implementa Store con un COMPARE-AND-SWAP: la escritura solo ocurre
// si el estado almacenado sigue siendo uno de los esperados. Sin esa condición, dos
// operadores simultáneos podrían encadenar dos transiciones que por separado eran
// válidas y juntas saltan un paso del ciclo.
//
// Va en TRANSACCIÓN porque entrar a `pending_approval` arrastra la línea de envío
// (D-041.11) y las dos cosas son un solo hecho: o la solicitud queda por aprobar
// CON su envío, o no queda por aprobar. El CAS toma el candado de la fila, así que
// el paso del envío no necesita bloquearla otra vez.
func (p *Postgres) UpdateStatus(ctx context.Context, tenantID, intakeID, to string, expected []string) (Intake, error) {
	if _, err := uuid.Parse(intakeID); err != nil {
		return Intake{}, ErrNotFound
	}

	// Se declara FUERA de la clausura porque WithTx puede REEJECUTARLA ante un
	// deadlock: cada intento la reasigna y vale la del intento que confirmó.
	var out Intake
	err := postgres.WithTx(ctx, p.db, func(tx *sql.Tx) error {
		updated, err := casStatusTx(ctx, tx, tenantID, intakeID, to, expected)
		if err != nil {
			return err
		}
		out = updated
		if NormalizeStatus(to) != StatusPendingApproval {
			return nil
		}
		changed, err := ensureShippingTx(ctx, tx, tenantID, intakeID, ShippingAlways)
		if err != nil || !changed {
			return err
		}
		out, err = recomputeTotalTx(ctx, tx, tenantID, intakeID)
		return err
	})
	if err != nil {
		return Intake{}, err
	}
	return out, nil
}

// casStatusTx ejecuta el compare-and-swap del estado dentro de la transacción y
// distingue "no es del tenant" (ErrNotFound) de "otro operador se adelantó"
// (ErrConflict) con una relectura: sin ella, las dos serían el mismo silencio.
func casStatusTx(ctx context.Context, tx *sql.Tx, tenantID, intakeID, to string, expected []string) (Intake, error) {
	updated, err := scanIntake(tx.QueryRowContext(ctx, `
		UPDATE public.intakes
		SET status = $3, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND status = ANY($4)
		RETURNING `+intakeCols,
		tenantID, intakeID, to, expected))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var exists bool
		if qerr := tx.QueryRowContext(ctx,
			`SELECT true FROM public.intakes WHERE tenant_id = $1 AND id = $2`,
			tenantID, intakeID).Scan(&exists); qerr != nil {
			if errors.Is(qerr, sql.ErrNoRows) {
				return Intake{}, ErrNotFound
			}
			return Intake{}, fmt.Errorf("intakes: verificar solicitud: %w", qerr)
		}
		return Intake{}, ErrConflict
	case err != nil:
		return Intake{}, err
	}
	return updated, nil
}

// EnsureShippingLine implementa Store: una transacción propia que BLOQUEA la
// cabecera (el mismo punto de serialización que toma el CAS de UpdateStatus) y
// deja la línea de envío puesta y el total cuadrado. Dos llamadas simultáneas
// sobre la misma solicitud se serializan; la segunda ve la línea de la primera y
// no escribe otra.
func (p *Postgres) EnsureShippingLine(ctx context.Context, tenantID, intakeID string, policy ShippingPolicy) error {
	if _, err := uuid.Parse(intakeID); err != nil {
		return ErrNotFound
	}
	return postgres.WithTx(ctx, p.db, func(tx *sql.Tx) error {
		var exists bool
		err := tx.QueryRowContext(ctx,
			`SELECT true FROM public.intakes WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
			tenantID, intakeID).Scan(&exists)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("intakes: bloquear la solicitud: %w", err)
		}

		changed, err := ensureShippingTx(ctx, tx, tenantID, intakeID, policy)
		if err != nil || !changed {
			return err
		}
		_, err = recomputeTotalTx(ctx, tx, tenantID, intakeID)
		return err
	})
}

// ensureShippingTx deja EXACTAMENTE una línea de envío en la solicitud y dice si
// escribió algo. No recalcula el total: eso lo hace el llamante, que es quien sabe
// si necesita la cabecera de vuelta.
//
// El orden importa: primero la política —si no aplica no se toca nada y no se lee
// una línea que no va a cambiar— y solo después la fila.
func ensureShippingTx(ctx context.Context, tx *sql.Tx, tenantID, intakeID string, policy ShippingPolicy) (bool, error) {
	zones, err := shippingZonesTx(ctx, tx, tenantID)
	if err != nil {
		return false, err
	}
	if !policy.applies(zones) {
		return false, nil
	}
	desired := DesiredShippingLine(zones)

	var (
		rowID  int64
		stored Item
	)
	err = tx.QueryRowContext(ctx, `
		SELECT id, label, qty, unit_price
		FROM public.intake_items
		WHERE intake_id = $1 AND sku = $2
	`, intakeID, ShippingSKU).Scan(&rowID, &stored.Label, &stored.Qty, &stored.UnitPrice)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		it := desired.item()
		if _, ierr := tx.ExecContext(ctx, `
			INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price)
			VALUES ($1, $2, $3, '', $4, $5)
		`, intakeID, it.SKU, it.Label, it.Qty, it.UnitPrice); ierr != nil {
			return false, fmt.Errorf("intakes: insertar la línea de envío: %w", ierr)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("intakes: leer la línea de envío: %w", err)
	}

	if !desired.Supersedes(stored) {
		return false, nil
	}
	// Se ACTUALIZA la fila en vez de borrarla e insertar otra: así la línea conserva
	// su added_at y su sitio en el pedido, y en ningún instante hay dos envíos.
	it := desired.item()
	if _, uerr := tx.ExecContext(ctx, `
		UPDATE public.intake_items SET label = $2, qty = $3, unit_price = $4 WHERE id = $1
	`, rowID, it.Label, it.Qty, it.UnitPrice); uerr != nil {
		return false, fmt.Errorf("intakes: actualizar la línea de envío: %w", uerr)
	}
	return true, nil
}

// shippingZonesTx lee tenant_settings.shipping_zones. Un tenant SIN fila de config
// no es un error: es un tenant que no configuró nada (mismo criterio que
// GetTenantSettings del módulo de flujos) y por tanto no tiene zonas.
func shippingZonesTx(ctx context.Context, tx *sql.Tx, tenantID string) ([]ShippingZone, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx,
		`SELECT shipping_zones FROM public.tenant_settings WHERE tenant_id = $1`,
		tenantID).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("intakes: leer las zonas de envío del tenant: %w", err)
	}
	return ParseShippingZones(raw)
}

// recomputeTotalTx recalcula el total de la cabecera como la SUMA de sus líneas y
// devuelve la solicitud ya coherente. Se recalcula entero en vez de sumarle el
// envío: sumar deja el total dependiendo de cuántas veces se llamó, y el criterio
// de esta tarea es justamente que llamar N veces dé lo mismo que llamar una.
func recomputeTotalTx(ctx context.Context, tx *sql.Tx, tenantID, intakeID string) (Intake, error) {
	return scanIntake(tx.QueryRowContext(ctx, `
		UPDATE public.intakes i
		SET total = COALESCE((
			    SELECT SUM(it.qty * it.unit_price)
			    FROM public.intake_items it
			    WHERE it.intake_id = i.id), 0),
		    updated_at = now()
		WHERE i.tenant_id = $1 AND i.id = $2
		RETURNING `+intakeCols,
		tenantID, intakeID))
}
