// Porta internal/intakes/postgres.go @ 64c181a

// postgres_read.go son las tres LECTURAS del puerto Store: la página de la bandeja,
// el export con líneas y el detalle de una solicitud. Ninguna abre transacción: son
// consultas sueltas sobre el pool.
//
// Lo que comparten List y ListDetails, y que el verde porta con ellas:
//
//   - EL MISMO PREDICADO para la página y para su total. Si divergieran, el paginador
//     mentiría. Son seis argumentos, siempre en este orden: tenant, desde, hasta,
//     estados, sesión, huérfanas; cada filtro ausente viaja como NULL (patrón
//     «$n IS NULL OR …», que deja el plan estable sin SQL dinámico);
//   - el filtro de estados viaja EXPANDIDO a sus variantes almacenadas
//     (StoredVariantsOf): la base puede guardar todavía claves legadas;
//   - el filtro de HUÉRFANAS (Plan 044 · T4.8, REQ-21c) es «su evento declarado ya no
//     está open», y es el mismo predicado con el que Discard decide `live_event`: la
//     vista preselecciona lo que el descarte va a aceptar. Va como subconsulta
//     correlada y NO como LEFT JOIN, que duplicaría cabeceras; una solicitud legada
//     sin evento (event_id NULL) es huérfana;
//   - el ORDER BY lo elige el filtro ya normalizado y sale de dos constantes: más
//     reciente primero (por defecto) o más antigua primero (SortOldest), siempre con
//     el id de desempate. Ningún texto del usuario llega al SQL.
//
// tenant_id NO se lee: quien consulta ya es el dueño del tenant (INV-8).

package intakes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// intakeFilterWhere es el predicado COMPARTIDO por la página y por su total: si
// divergieran, el paginador mentiría. Cada filtro es opcional por el patrón
// "$n IS NULL OR …", que deja el plan estable sin construir SQL dinámico
// (concatenación de constantes: nada de esta cadena viene del usuario).
//
// El $6 es el filtro de HUÉRFANAS (Plan 044 · T4.8, REQ-21c): «su evento declarado
// ya no está open». Es el NOT EXISTS del mismo predicado que hasLiveEventTx usa
// para la guarda `live_event` del descarte, a propósito y literalmente — la vista
// preselecciona lo que el descarte va a aceptar, y una divergencia entre las dos
// haría que el dueño marcara lotes que rebotan.
//
// Va como subconsulta correlada y NO como LEFT JOIN: el join duplicaría cabeceras
// si un evento tuviera dos contenidos (hoy lo impide intakes_event_id_uidx, pero el
// paginador no puede depender de un índice de otra tabla), y el NOT EXISTS resuelve
// además el legado sin una sola rama: event_id NULL ⇒ la subconsulta no casa ⇒
// huérfana, que es lo que hasLiveEventTx decide para esa misma fila.
//
// Lo que NO se usa aquí es la vista public.event_content de la 0054: esa mira en la
// dirección contraria —el PADRE preguntando por el estado de su contenido— y su
// vocabulario (alive|settled|discarded) es el del intake, no el del evento. Aquí la
// pregunta es sobre el EVENTO, y su vocabulario es `open`.
const intakeFilterWhere = `
	WHERE tenant_id = $1
	  AND ($2::timestamptz IS NULL OR created_at >= $2)
	  AND ($3::timestamptz IS NULL OR created_at <  $3)
	  AND ($4::text[]      IS NULL OR status = ANY($4))
	  AND ($5::text        IS NULL OR session_id = $5)
	  AND ($6::boolean     IS NULL OR NOT EXISTS (
	          SELECT 1 FROM public.conversation_events e
	          WHERE e.id = public.intakes.event_id AND e.status = 'open'))`

// listIntakesSelect y listIntakesPage son la consulta de la página PARTIDA en dos
// por el ORDER BY, que desde el Plan 044 · T4.1 lo elige el filtro (`sort`,
// D-044.48 §3) y ya no es una constante. Lo que va en medio sale de intakeOrderBy,
// que devuelve una de dos constantes: la query del usuario no llega aquí.
const listIntakesSelect = `SELECT ` + intakeCols + ` FROM public.intakes` + intakeFilterWhere
const listIntakesPage = `
	LIMIT $7 OFFSET $8`

// countIntakesQuery cuenta las MISMAS coincidencias sin paginar.
const countIntakesQuery = `SELECT count(*) FROM public.intakes` + intakeFilterWhere

// List implementa Store.List: la página pedida y el TOTAL de coincidencias sin
// paginar, con el mismo predicado.
//
// El filtro se normaliza primero (Filter.Normalized). Son DOS sentencias sueltas:
// el `count(*)` y, solo si hay coincidencias, la página (LIMIT page_size OFFSET
// (page-1)·page_size). Con total 0 devuelve ([]Intake{}, 0, nil) —slice vacío, no
// nil— sin lanzar la segunda.
//
// Errores, envueltos con %w y devolviendo (nil, 0, err):
//
//   - "intakes: contar solicitudes: " — falla el count;
//   - "intakes: listar solicitudes: " — falla la consulta de la página;
//   - "intakes: leer solicitud: " — una fila no se puede escanear;
//   - "intakes: recorrer solicitudes: " — falla el recorrido de las filas;
//   - "intakes: cerrar filas de solicitudes: " — falla el cierre de las filas cuando
//     lo demás fue bien (un error anterior no se pisa).
func (p *Postgres) List(ctx context.Context, tenantID string, f Filter) (out []Intake, total int, err error) {
	f = f.Normalized()
	args := filterArgs(tenantID, f)

	if err := p.db.QueryRowContext(ctx, countIntakesQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("intakes: contar solicitudes: %w", err)
	}
	if total == 0 {
		return []Intake{}, 0, nil
	}

	// 🔒 El G202 de gosec queda SILENCIADO abajo, y con razón: las TRES piezas son
	// constantes de este fichero, y la de en medio la elige intakeOrderBy con un
	// switch entre dos constantes. No hay un solo carácter de la query del usuario en
	// esta cadena; los valores del filtro siguen viajando como parámetros ($1..$7).
	query := listIntakesSelect + intakeOrderBy(f, "") + listIntakesPage //nolint:gosec // G202: concatenación de constantes, ver arriba
	rows, err := p.db.QueryContext(ctx, query, append(args, f.PageSize, f.Offset())...)
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

// listIntakeDetailsHead/Body/Items traen las cabeceras que casan con el filtro Y
// sus líneas en UNA sola consulta. Dos decisiones que no son de estilo:
//
//   - La cota (`LIMIT $7`) va DENTRO del CTE, sobre las cabeceras: si estuviera
//     fuera cortaría filas del join y devolvería solicitudes con las líneas a
//     medias, que es exactamente el error que un export no puede permitirse.
//   - El join es LEFT: una solicitud sin líneas (una `open` que nadie llegó a
//     llenar) sigue apareciendo, con las columnas de línea en NULL. Con INNER JOIN
//     desaparecería del export sin que nadie se enterara.
//
// El predicado es el MISMO de la lista (intakeFilterWhere), así que el export no
// puede divergir de lo que la bandeja muestra. `p.id` es text (intakeCols lo
// castea) y por eso el join lo devuelve a uuid.
// Va partida en tres por los DOS órdenes que lleva dentro, que desde el Plan 044 ·
// T4.1 los elige el filtro. Los dos tienen que girar JUNTOS: el de dentro del CTE
// decide QUÉ solicitudes entran en la cota, y el de fuera en qué orden salen. Si
// solo girara uno, `sort=oldest` con `limit` devolvería las más recientes puestas
// del revés — que no es lo que nadie pidió.
const listIntakeDetailsHead = `
	WITH page AS (
		SELECT ` + intakeCols + ` FROM public.intakes` + intakeFilterWhere
const listIntakeDetailsBody = `
		LIMIT $7
	)
	SELECT p.id, p.contact_id, p.session_id, p.status, p.total, p.created_at, p.updated_at,
	       p.customer_note, p.deposit_due_at, p.deposit_reminded_at, p.expiry_reminded_at,
	       it.sku, it.label, it.customization, it.qty, it.unit_price, it.added_at
	FROM page p
	LEFT JOIN public.intake_items it ON it.intake_id = p.id::uuid`

// listIntakeDetailsItems desempata DENTRO de cada solicitud, y NO gira con `sort`:
// las líneas de un presupuesto se leen en el orden en que se añadieron, mire el
// export las solicitudes por delante o por detrás.
const listIntakeDetailsItems = `, it.added_at, it.id`

// ListDetails implementa Store.ListDetails: hasta `limit` solicitudes del filtro CON
// sus líneas, para el export. La paginación del filtro se ignora: el tope es `limit`.
//
// Con limit <= 0 devuelve ([]Detail{}, nil) SIN tocar la base. Si no, es UNA sola
// sentencia (una CTE con la página de cabeceras y un LEFT JOIN a sus líneas), y no
// 1+N: el export puede pedir MaxExportIntakes+1 cabeceras. El adaptador agrupa las
// filas consecutivas de la misma solicitud:
//
//   - las solicitudes salen en el orden del filtro, y las líneas de cada una por
//     added_at y luego id (el orden en que el cliente las añadió);
//   - una solicitud SIN líneas sale con Items vacío, no nil (la fila del LEFT JOIN
//     trae sku, label y qty a NULL y no genera línea);
//   - Revisions y BuyerDataPresent NO se rellenan: el export no los lleva.
//
// Errores, envueltos con %w y devolviendo (nil, err):
//
//   - "intakes: listar solicitudes con líneas: " — falla la consulta;
//   - "intakes: leer fila del export: " — una fila no se puede escanear;
//   - "intakes: recorrer el export: " — falla el recorrido;
//   - "intakes: cerrar filas del export: " — falla el cierre cuando lo demás fue bien.
func (p *Postgres) ListDetails(ctx context.Context, tenantID string, f Filter, limit int) (out []Detail, err error) {
	if limit <= 0 {
		return []Detail{}, nil
	}
	f = f.Normalized()
	// 🔒 Mismo G202 silenciado que en List, y por lo mismo: todas las piezas son
	// constantes y las dos de intakeOrderBy salen de un switch entre constantes.
	query := listIntakeDetailsHead + intakeOrderBy(f, "") + listIntakeDetailsBody + //nolint:gosec // G202: concatenación de constantes, ver arriba
		intakeOrderBy(f, "p.") + listIntakeDetailsItems
	rows, err := p.db.QueryContext(ctx, query, append(filterArgs(tenantID, f), limit)...)
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
		dueAt, remindedAt  sql.NullTime
		expiryRemindedAt   sql.NullTime
		sku, label, custom sql.NullString
		qty                sql.NullInt64
		unitPrice          sql.NullFloat64
		addedAt            sql.NullTime
	)
	if err := sc.Scan(&in.ID, &in.ContactID, &in.SessionID, &in.Status, &in.Total,
		&in.CreatedAt, &in.UpdatedAt, &in.CustomerNote, &dueAt, &remindedAt, &expiryRemindedAt,
		&sku, &label, &custom, &qty, &unitPrice, &addedAt); err != nil {
		return Intake{}, Item{}, false, fmt.Errorf("intakes: leer fila del export: %w", err)
	}
	in.Status = NormalizeStatus(in.Status)
	in.DepositDueAt, in.DepositRemindedAt = dueAt.Time, remindedAt.Time
	in.ExpiryRemindedAt = expiryRemindedAt.Time
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

// filterArgs arma los SEIS argumentos del predicado compartido. Un filtro sin
// valor viaja como NULL (any(nil)) para que la rama "$n IS NULL" lo desactive.
//
// Ese NULL es lo que hace del `orphan` un filtro y no un modo: con Orphan=false el
// $6 llega NULL y el NOT EXISTS ni se evalúa, así que la bandeja de siempre paga
// exactamente lo que pagaba.
func filterArgs(tenantID string, f Filter) []any {
	var from, to, statuses, session, orphan any
	if !f.From.IsZero() {
		from = f.From
	}
	if !f.To.IsZero() {
		to = f.To
	}
	if len(f.Statuses) > 0 {
		// Las filas legadas guardan `closed` donde el dominio dice `confirmed`:
		// el filtro tiene que alcanzarlas (D-041.10, sin migración de datos). La
		// expansión es de CADA estado pedido, no del primero (D-044.47 §2).
		statuses = StoredVariantsOf(f.Statuses)
	}
	if f.SessionID != "" {
		session = f.SessionID
	}
	if f.Orphan {
		orphan = true
	}
	return []any{tenantID, from, to, statuses, session, orphan}
}

// intakeOrderBy es la cláusula de orden que corresponde al filtro, con el prefijo
// de tabla que pida el llamante (la lista consulta `intakes` a secas; el export lo
// hace desde el CTE `p`).
//
// El desempate por id hace el orden TOTAL en los dos sentidos: sin él, dos
// solicitudes con el mismo created_at podrían repetirse o desaparecer al pasar de
// página. Y los dos criterios giran JUNTOS —`ASC, ASC` o `DESC, DESC`—: mezclarlos
// dejaría un orden estable pero incomprensible.
//
// Devuelve una constante elegida por un switch, NUNCA texto del usuario: `f.Sort`
// ya salió de Normalized, que colapsa a SortNewest cualquier cosa que no sea
// SortOldest. Nada de esta cadena se concatena desde la query.
func intakeOrderBy(f Filter, prefix string) string {
	if f.Sort == SortOldest {
		return ` ORDER BY ` + prefix + `created_at ASC, ` + prefix + `id ASC`
	}
	return ` ORDER BY ` + prefix + `created_at DESC, ` + prefix + `id DESC`
}

// Get implementa Store.Get: la cabecera, sus líneas, sus revisiones y si tiene datos
// del comprador.
//
// Son cuatro lecturas sueltas, en este orden y SIN transacción: cabecera (acotada por
// tenant), líneas (por added_at, id), revisiones (por revision_no) y la existencia de
// la fila de public.intake_buyer_data. De los datos del comprador solo se pregunta SI
// EXISTEN: el contenido cifrado no se lee aquí.
//
// Devuelve ErrNotFound —sin envolver— si intakeID no es un UUID (sin tocar la base) o
// si la cabecera no existe en ese tenant; en ese caso no lanza las otras lecturas.
// Items y Revisions salen vacíos, no nil, cuando no hay filas.
//
// 🔴 LEER REVISIONES PUEDE ESCRIBIR. Si una revisión trae literal de nivel 2 y su TTL
// ya venció, Get la PODA al vuelo (ver postgres_revisions.go): la revisión sale sin
// literal y con LiteralPrunedAt puesto, y la poda queda registrada por el logger de
// retención. Un fallo de la poda no hace fallar a Get.
//
// Errores de la base, envueltos con %w y devolviendo Detail{}:
//
//   - "intakes: leer solicitud: " — la cabecera no se puede leer;
//   - "intakes: listar líneas: ", "intakes: leer línea: ", "intakes: recorrer líneas: ",
//     "intakes: cerrar filas de líneas: " — las líneas;
//   - los de la lectura de revisiones, que documenta postgres_revisions.go;
//   - "intakes: comprobar datos del comprador: " — la última lectura.
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

	items, err := itemsOf(ctx, p.db, intakeID)
	if err != nil {
		return Detail{}, err
	}
	revs, err := p.revisionsOf(ctx, p.db, intakeID)
	if err != nil {
		return Detail{}, err
	}
	present, err := buyerDataPresent(ctx, p.db, intakeID)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Intake: head, Items: items, Revisions: revs, BuyerDataPresent: present}, nil
}

// buyerDataPresent dice si la solicitud tiene fila en public.intake_buyer_data
// (D-041.13, T4.5). Es una consulta suelta y NO una columna más de intakeCols por
// dos motivos: intakeCols lo comparten el listado, el export y el detalle —y solo
// el detalle publica esto (ver Detail.BuyerDataPresent)—, y su comentario advierte
// de lo que cuesta tocar esa lista, porque el orden es el de dos Scan distintos.
//
// EXISTS y no un SELECT de las columnas: aquí no se lee data_enc ni se toca el
// cipher. Saber que el dato está no requiere poder leerlo, y esta consulta es la
// prueba de que el detalle no lo lee.
func buyerDataPresent(ctx context.Context, q querier, intakeID string) (bool, error) {
	var present bool
	if err := q.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM public.intake_buyer_data WHERE intake_id = $1)
	`, intakeID).Scan(&present); err != nil {
		return false, fmt.Errorf("intakes: comprobar datos del comprador: %w", err)
	}
	return present, nil
}

// itemsOf lee las líneas de una solicitud en el orden en que se añadieron. No
// filtra por tenant: la cabecera ya se validó contra el tenant y la FK garantiza
// que estas líneas son suyas.
func itemsOf(ctx context.Context, q querier, intakeID string) (out []Item, err error) {
	rows, err := q.QueryContext(ctx, `
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
