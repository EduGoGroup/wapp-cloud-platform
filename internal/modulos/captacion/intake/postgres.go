// Porta internal/intake/postgres.go @ 8d875ab

package intake

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Postgres es la implementación real de JobStore sobre `public.intake_jobs`
// (migración 0072). No tiene cipher y NO DEBE TENERLO, ni siquiera desde que T1.4
// escribe el sobre: lo que llega a PutSourceText son bytes YA cifrados por el
// compositor del flush, que es quien tiene el KeyProvider. Un store sin cipher es
// un store que no puede escribir literal aunque alguien se lo pida — que es la
// forma barata de sostener D-044.26.
type Postgres struct {
	db *sql.DB
}

// NewPostgres construye el store sobre el pool compartido del proceso.
func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

// openOrAppendSQL es LA SENTENCIA de D-044.26: una sola, sin ninguna lectura
// previa, ejecutada EN LÍNEA con el mensaje del cliente.
//
// # POR QUÉ CADA PIEZA ES COMO ES
//
//   - `ON CONFLICT (tenant_id, session_id, contact_id, event_id) WHERE status =
//     'aggregating'`: el árbitro es el índice ÚNICO PARCIAL
//     `intake_jobs_ventana_viva_uidx` (0072 · D.2). 🔴 CON UN ÍNDICE PARCIAL, EL
//     PREDICADO DEL `ON CONFLICT` NO ES OPCIONAL: sin el `WHERE`, Postgres no puede
//     inferir ese índice —solo miraría los únicos TOTALES, y no hay ninguno sobre
//     esas cuatro columnas— y la sentencia falla en tiempo de ejecución con «there
//     is no unique or exclusion constraint matching the ON CONFLICT
//     specification». El predicado escrito aquí es IDÉNTICO al del índice, que es
//     la condición que Postgres exige para inferirlo.
//
//   - El INSERT fija `status` a 'aggregating' EXPLÍCITAMENTE aunque la columna
//     tenga ese DEFAULT. No es redundancia decorativa: la fila que se inserta tiene
//     que caer DENTRO del índice parcial para que el árbitro pueda verla; una fila
//     insertada en otro estado sencillamente no está en el índice y el
//     `ON CONFLICT` no mordería — se colaría una segunda ventana viva sin error.
//
//   - `message_ts` está en el INSERT y NO en el DO UPDATE (D-044.26, y el COMMENT
//     de la columna en la 0072). Es así como la fila conserva el ts del PRIMER
//     mensaje SIN QUE NADIE LA LEA: la rama que se ejecuta cuando la ventana ya
//     existía sencillamente no lo toca.
//
//   - `source_refs = intake_jobs.source_refs || EXCLUDED.source_refs`: el `||` de
//     jsonb concatena arrays. `EXCLUDED` es la fila que se intentó insertar, así
//     que esto es exactamente el `|| $n::jsonb` del diseño sin repetir el
//     parámetro. Se escribe `intake_jobs.` (sin `public.`) porque ése es el nombre
//     con el que la fila EXISTENTE es visible dentro del DO UPDATE.
//
//   - No hay `RETURNING`: el sink no necesita el id, y pedirlo obligaría a un Scan
//     por entrante para tirarlo a la basura.
const openOrAppendSQL = `
INSERT INTO public.intake_jobs
       (tenant_id, session_id, contact_id, event_id, status, message_ts, source_refs)
VALUES ($1, $2, $3, $4::uuid, 'aggregating', $5, $6::jsonb)
ON CONFLICT (tenant_id, session_id, contact_id, event_id) WHERE status = 'aggregating'
DO UPDATE SET
       source_refs = intake_jobs.source_refs || EXCLUDED.source_refs,
       updated_at  = now()
`

// OpenOrAppend implementa JobStore. UNA sentencia, CERO lecturas, CERO cripto,
// CERO red más allá de esta ejecución (D-044.26).
func (p *Postgres) OpenOrAppend(ctx context.Context, a Append) error {
	if p == nil || p.db == nil {
		return nil
	}
	if !a.Key.Valid() {
		return fmt.Errorf("intake: clave de ventana incompleta (tenant/session/contact/event)")
	}
	// Las refs se serializan SIEMPRE como array, aunque venga vacío: `[] || []` es
	// `[]`, así que un entrante sin refs añade nada y la sentencia sigue siendo
	// válida. Marshalar aquí (y no construir el literal a mano) es lo que impide
	// que un identificador con comillas rompa el JSON.
	refs := a.Refs
	if refs == nil {
		refs = []string{}
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		return fmt.Errorf("intake: serializar source_refs: %w", err)
	}
	var ts any
	if !a.MessageTS.IsZero() {
		ts = a.MessageTS.UTC()
	}
	if _, err := p.db.ExecContext(ctx, openOrAppendSQL,
		a.Key.TenantID, a.Key.SessionID, a.Key.ContactID, a.Key.EventID, ts, string(raw),
	); err != nil {
		return fmt.Errorf("intake: abrir/ampliar ventana de captación: %w", err)
	}
	return nil
}

// closeWindowSQL cierra la ventana VIVA de una tupla. El guard
// `AND status = 'aggregating'` es lo que hace la operación IDEMPOTENTE y lo que
// impide que el flush por intent y el flush por ventana produzcan dos jobs: el
// segundo en llegar afecta 0 filas.
//
// No lleva `id`: el sink que adelanta el flush por intent NO SABE el id (no lee),
// y buscarlo sería el SELECT que D-044.26 prohíbe. La tupla es suficiente porque el
// índice único parcial garantiza que como mucho hay una fila viva para ella.
const closeWindowSQL = `
UPDATE public.intake_jobs
   SET status = 'pending', updated_at = now()
 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3 AND event_id = $4::uuid
   AND status = 'aggregating'
`

// CloseWindow implementa JobStore.
func (p *Postgres) CloseWindow(ctx context.Context, k WindowKey) (bool, error) {
	if p == nil || p.db == nil {
		return false, nil
	}
	if !k.Valid() {
		return false, fmt.Errorf("intake: clave de ventana incompleta al cerrar")
	}
	res, err := p.db.ExecContext(ctx, closeWindowSQL, k.TenantID, k.SessionID, k.ContactID, k.EventID)
	if err != nil {
		return false, fmt.Errorf("intake: cerrar ventana de captación: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		// Un driver que no sepa contar filas no puede convertirse en un job perdido:
		// se reporta el error y el llamante lo LOGUEA. El siguiente barrido vuelve a
		// intentarlo si la ventana sigue abierta.
		return false, fmt.Errorf("intake: contar filas cerradas: %w", err)
	}
	return n > 0, nil
}

// putSourceTextSQL escribe el sobre del literal sobre la ventana recién cerrada.
//
// # LAS DOS GUARDAS, Y EL ACCIDENTE QUE EVITA CADA UNA
//
//   - La SUBCONSULTA (`status='pending'` + `ORDER BY updated_at DESC, created_at
//     DESC LIMIT 1`) elige UNA fila y solo una. Sin ella, un `UPDATE … WHERE tupla
//     AND status='pending'` tocaría TODAS las ventanas que esa tupla haya cerrado
//     desde siempre —el índice único de la 0072 es PARCIAL y solo cubre
//     'aggregating'—, escribiendo el texto de hoy encima del de la semana pasada.
//   - El `AND j.source_text_enc IS NULL` de fuera impide lo contrario: que el sobre
//     de una ventana se escriba sobre una fila que YA tiene el suyo. Con las dos, un
//     segundo intento sobre la misma ventana afecta 0 filas y devuelve false sin
//     error, igual que CloseWindow.
//
// Las tres columnas se escriben en la MISMA sentencia: no hay forma de dejar la
// fila con dos de tres (ver SourceText.Complete, que además lo comprueba antes).
const putSourceTextSQL = `
UPDATE public.intake_jobs AS j
   SET source_text_enc    = $5,
       source_text_dek    = $6,
       source_text_kek_id = $7,
       updated_at         = now()
 WHERE j.id = (
        SELECT id
          FROM public.intake_jobs
         WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3 AND event_id = $4::uuid
           AND status = 'pending'
         ORDER BY updated_at DESC, created_at DESC
         LIMIT 1)
   AND j.source_text_enc IS NULL
`

// PutSourceText implementa JobStore.
func (p *Postgres) PutSourceText(ctx context.Context, k WindowKey, env SourceText) (bool, error) {
	if p == nil || p.db == nil {
		return false, nil
	}
	if !k.Valid() {
		return false, fmt.Errorf("intake: clave de ventana incompleta al guardar el literal")
	}
	if !env.Complete() {
		// Un sobre a medias deja una fila INDESCIFRABLE, y eso no se puede deshacer
		// leyendo: no hay copia de la DEK en ningún otro sitio. Se rechaza antes de
		// tocar la base. El error NO cita el contenido, solo dice qué falta.
		return false, fmt.Errorf("intake: sobre del literal incompleto (enc=%d dek=%d kek_id=%t): son las tres o ninguna",
			len(env.Enc), len(env.DEK), env.KEKID != "")
	}
	res, err := p.db.ExecContext(ctx, putSourceTextSQL,
		k.TenantID, k.SessionID, k.ContactID, k.EventID, env.Enc, env.DEK, env.KEKID)
	if err != nil {
		return false, fmt.Errorf("intake: guardar el literal de la ventana: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("intake: contar filas del literal guardado: %w", err)
	}
	return n > 0, nil
}

// closeWithSourceTextSQL cierra la ventana Y le guarda su sobre, en una sola sentencia.
//
// ✎ Divergencia deliberada del viejo (D-F7-9, D-F8-13). El viejo son closeWindowSQL y
// después putSourceTextSQL: entre las dos la fila está en `pending` SIN sobre, que es
// exactamente lo que reclama el worker. Aquí `status` y las tres columnas del sobre
// cambian en el MISMO UPDATE, así que ningún reclamo puede ver la fila cerrada y vacía.
//
// # LAS TRES GUARDAS, Y EL ACCIDENTE QUE EVITA CADA UNA
//
//   - `id = $1::uuid` y no la tupla: una tupla puede tener varias filas (el índice único
//     de la 0072 es PARCIAL), y quien llama es el barrido, que acaba de leer la fila con
//     listAggregatingSQL y sí conoce su id. Es lo que ahorra la subconsulta y los dos
//     ORDER BY de putSourceTextSQL, y lo que hace imposible que el sobre caiga en otra
//     `pending` de la misma tupla.
//   - `status = 'aggregating'`: la idempotencia de closeWindowSQL. Un segundo cierre —o
//     uno que llega después del adelanto por intent— afecta 0 filas.
//   - `updated_at = $2`: SOLO SE CIERRA LO QUE SE LEYÓ. `$2` es el `updated_at` que dio
//     listAggregatingSQL; cada mensaje que entra lo mueve (el `DO UPDATE … updated_at =
//     now()` de openOrAppendSQL). Si entró uno después de la lectura, el sobre que se
//     trae se compuso sin él: la sentencia afecta 0 filas, la ventana sigue viva y el
//     siguiente barrido la cierra con el literal entero. Es un bloqueo optimista con la
//     marca que la tabla ya tiene; no hace falta ni una columna de versión ni una
//     transacción. La comparación es por igualdad exacta y es segura: la columna guarda
//     microsegundos y el driver los lee y los devuelve sin redondear.
//
// Las tres columnas del sobre van en la misma sentencia y con el mismo valor de verdad:
// las tres con contenido o las tres NULL (el hilo sin mensajes). Lo decide el método,
// que no deja pasar un sobre a medias.
const closeWithSourceTextSQL = `
UPDATE public.intake_jobs
   SET status             = 'pending',
       source_text_enc    = $3,
       source_text_dek    = $4,
       source_text_kek_id = $5,
       updated_at         = now()
 WHERE id = $1::uuid
   AND status = 'aggregating'
   AND updated_at = $2
`

// CloseWithSourceText implementa JobStore: el cierre y el sobre en UNA sentencia, solo
// si la fila sigue `aggregating` con el `updated_at` que se leyó (D-F7-9, D-F8-13).
// Un *Postgres nil, o sin base, es un no-op (false, nil) como sus hermanas.
func (p *Postgres) CloseWithSourceText(ctx context.Context, seen OpenJob, env SourceText) (bool, error) {
	if p == nil || p.db == nil {
		return false, nil
	}
	// La ventana se mira ANTES que el sobre, como en PutSourceText: con las dos cosas
	// mal, el error es el de la ventana.
	if seen.ID == "" || !seen.Key.Valid() {
		return false, fmt.Errorf("intake: ventana incompleta al cerrar con el literal")
	}
	// COMPLETO O VACÍO ENTERO. Vacío es el hilo sin mensajes y viaja como tres NULL —no
	// como un bytea de longitud cero ni un kek_id "", que dejarían una fila que parece
	// tener sobre—. A medias es una fila indescifrable: se rechaza antes de tocar la
	// base, con el mismo texto que PutSourceText, que dice qué falta sin citar el contenido.
	var enc, dek, kekID any
	switch {
	case env.Complete():
		enc, dek, kekID = env.Enc, env.DEK, env.KEKID
	case !env.Empty():
		return false, fmt.Errorf("intake: sobre del literal incompleto (enc=%d dek=%d kek_id=%t): son las tres o ninguna",
			len(env.Enc), len(env.DEK), env.KEKID != "")
	}
	res, err := p.db.ExecContext(ctx, closeWithSourceTextSQL, seen.ID, seen.LastActivity, enc, dek, kekID)
	if err != nil {
		return false, fmt.Errorf("intake: cerrar la ventana con su literal: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("intake: contar filas cerradas con su literal: %w", err)
	}
	return n > 0, nil
}

// listAggregatingSQL alimenta el BARRIDO, que corre fuera del camino del entrante.
// `ORDER BY created_at` deja las más viejas primero: si el `limit` recorta, recorta
// por la cola y las que llevan más rato esperando salen igual.
//
// 🔧 AQUÍ ESTABA `COALESCE(message_ts, created_at)` —el ancla ÚNICA de la ventana
// vieja— y lo sustituyen las DOS columnas de la ventana HÍBRIDA (Plan 044 · T1.8-1):
//
//   - `updated_at` es el ancla del SILENCIO. La mueve el `DO UPDATE … updated_at =
//     now()` de openOrAppendSQL, o sea CADA mensaje de la ráfaga, así que el plazo
//     cuenta desde el ÚLTIMO mensaje del cliente y una ráfaga larga ya no se parte en
//     dos jobs. (Ese `now()` es EXPLÍCITO en la sentencia: NO hay ningún trigger sobre
//     esta tabla — el esquema entero no tiene un solo `CREATE TRIGGER`.)
//   - `created_at` es el ancla del TECHO, que es lo que impide que una conversación
//     que gotea cada 40 s deje su job abierto para siempre.
//
// 🔴 `message_ts` YA NO SE SELECCIONA, y no es un descuido: lo pone el Edge con el
// reloj del CLIENTE, y medir contra `now()` un instante de otro reloj es el fallo
// permanente y silencioso que esta casa ya tiene fichado. La columna sigue viva y
// sigue siendo el ts del PRIMER mensaje (D-044.9, base de fechas del presupuesto);
// simplemente no decide plazos. Volver a ponerla aquí es la mutación que el criterio
// (b) de T1.8-1 exige que ponga un test en rojo.
//
// Las dos columnas son NOT NULL en la 0072, así que no hace falta COALESCE ni un
// sql.NullTime: si alguna llegara NULL, el Scan fallaría en vez de inventar un cero
// que cerraría la ventana al instante.
const listAggregatingSQL = `
SELECT id::text, tenant_id, session_id, contact_id, event_id::text,
       updated_at, created_at
  FROM public.intake_jobs
 WHERE status = 'aggregating'
 ORDER BY created_at
 LIMIT $1
`

// ListAggregating implementa JobStore.
func (p *Postgres) ListAggregating(ctx context.Context, limit int) (out []OpenJob, err error) {
	if p == nil || p.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		return nil, nil
	}
	rows, err := p.db.QueryContext(ctx, listAggregatingSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("intake: listar ventanas vivas: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("intake: cerrar filas de ventanas vivas: %w", cerr)
		}
	}()

	for rows.Next() {
		var j OpenJob
		if serr := rows.Scan(&j.ID, &j.Key.TenantID, &j.Key.SessionID, &j.Key.ContactID,
			&j.Key.EventID, &j.LastActivity, &j.CreatedAt); serr != nil {
			return nil, fmt.Errorf("intake: scan de ventana viva: %w", serr)
		}
		out = append(out, j)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("intake: iterar ventanas vivas: %w", rerr)
	}
	return out, nil
}
