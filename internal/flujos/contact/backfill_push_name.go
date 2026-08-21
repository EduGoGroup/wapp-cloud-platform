package contact

import (
	"context"
	"database/sql"
	"fmt"
)

// DefaultPushNameBackfillBatch es el tamaño de lote por defecto del backfill cuando
// el llamador no especifica uno (<= 0). Mismo valor y mismo argumento que
// fleet.DefaultSelfPnBackfillBatch y crypto.DefaultRekeyBatch (500): acota memoria y
// duración de la transacción. Para public.contacts es holgadísimo —en UAT son 15
// filas, 13 con nombre— y esa holgura es deliberada: el lote existe para que el día
// que la tabla crezca el arranque no cargue la tabla entera en RAM, no para ir
// rápido hoy.
const DefaultPushNameBackfillBatch = 500

// PushNameBackfillReport resume UNA pasada del backfill, para que el arranque pueda
// decir en UNA línea qué hizo.
//
// 🔴 NO LLEVA NI UN DATO, y no puede llevarlo nunca: son contadores y nada más
// (misma regla que crypto.Report y fleet.SelfPnBackfillReport, §10.H). El backfill es
// el único sitio del sistema que tiene TODOS los push_name en claro pasando por
// memoria a la vez; si algo de aquí acabara en un log, sería la fuga con mejor
// relación coste/daño de todo el plan.
type PushNameBackfillReport struct {
	// Encrypted son las filas que ESTA pasada convirtió: sobre escrito y columna en
	// claro vaciada en el MISMO UPDATE, confirmadas por RowsAffected (no por
	// intención).
	Encrypted int
	// Emptied son las filas cuyo push_name era la CADENA VACÍA: se vacían a NULL sin
	// cifrar nada. Ver el porqué en el docstring de BackfillPushName.
	//
	// ⚠️ Emptied NO es el «tercer desenlace» que el molde advierte de no copiar. Aquel
	// era un desenlace de FALLO —una fila que se queda con su PII en claro y hay que
	// mirar—; este es un ÉXITO: la fila queda limpia, sin dato en claro y sin sobre
	// que estorbe. Por eso no lleva Warn ni hace falta vigilarlo: se cuenta para que
	// el arranque cuadre (Encrypted + Emptied = filas vistas) y ya.
	Emptied int
}

// pushNameCursor es la posición del barrido por clave (keyset pagination) sobre la PK
// de public.contacts: (tenant_id, kind, value_bidx).
//
// 🔴 POR QUÉ UN CURSOR SI AQUÍ EL CENTINELA SÍ SE CONSUME. Se dice sin adornos,
// porque el molde (fleet.selfPnCursor) sí tenía un peligro real y este NO lo tiene:
// allí una fila OMITIDA seguía casando el centinela después de procesarla, así que un
// bucle con LIMIT pelado podía releer las mismas filas para siempre. Aquí los DOS
// desenlaces consumen el criterio de selección de la fila —el cifrado escribe
// push_name_enc y vacía push_name; el vaciado pone push_name a NULL—, así que ninguna
// fila procesada vuelve a casar `push_name IS NOT NULL AND push_name_enc IS NULL` y un
// LIMIT a secas bastaría para terminar.
//
// El cursor se conserva por dos motivos honestos: simetría con el molde (el día que se
// extraiga el genérico, al TERCER caso, las dos formas serán la misma) y porque hace el
// barrido REANUDABLE —si un lote aborta, el siguiente arranque retoma donde se quedó
// sin releer lo ya hecho—. No se conserva por miedo a un bucle infinito que aquí no
// puede darse.
//
// El cursor arranca en tres cadenas VACÍAS y eso ya es la primera vuelta: cualquier
// tenant_id::text real (un UUID) es mayor que ella al comparar filas en SQL, así que no
// hace falta una segunda sentencia ni un `if primera_vuelta`.
type pushNameCursor struct {
	tenantID  string
	kind      string
	valueBidx string
}

// backfillPushNameSelect toma el siguiente lote de filas PENDIENTES.
//
// 🔴 EL CENTINELA ES LA PAREJA `push_name IS NOT NULL AND push_name_enc IS NULL`, y las
// dos mitades hacen falta. La primera es la que define «pendiente» (queda dato en
// claro); la segunda es la que impide re-cifrar en cada arranque una fila que ya tiene
// sobre. Sin la segunda, cada reinicio reescribiría el sobre con un nonce nuevo —una
// escritura MUDA, la peor clase, que nadie detectaría porque el dato leído sería el
// mismo—, y además pisaría el sobre bueno.
//
// ⚠️ AQUÍ NO SE DESCARTA LA CADENA VACÍA, y es la diferencia deliberada con el molde
// (fleet, que sí filtra). La razón es de REGLA, no de nombre: allí la cadena vacía se
// excluía porque el backfill NORMALIZA y una cadena vacía no normaliza, así que entraría
// para caer en el desenlace de omitida y dejar un Warn perpetuo por un dato que no es un
// problema. Aquí no hay normalización, así que la cadena vacía no rompe nada al entrar
// —y tiene que entrar, porque hay que VACIARLA—. Ver BackfillPushName para el porqué de
// vaciarla en vez de cifrarla o de dejarla estar.
//
// El nombre EN CLARO se selecciona —es el único sitio del que se puede leer— y a partir
// de aquí vive en memoria hasta que la fila se escribe. No se loguea, no se mete en un
// error y no sale de esta función más que hacia pushNameEnvelope.
//
// ⚠️ EL PRECIO DEL `::text` EN EL CURSOR, heredado del molde y con los números de ESTA
// tabla. La PK de contacts es (tenant_id uuid, kind text, value_bidx text). Al comparar
// y ordenar por `tenant_id::text` la expresión deja de casar con la primera columna del
// índice de la PK, así que el planner NO puede usarlo: cada lote es un SEQ SCAN + SORT
// de la tabla entera, y con el loteo un barrido de N filas cuesta N/batch barridos
// completos. Es decir: el lote, que existe «para el día que la tabla crezca», es justo
// lo que multiplica el coste ESE día.
//
// SE MANTIENE EL CAST, y no por inercia: quitarlo exige que $1 sea un uuid VÁLIDO, y el
// cursor arranca en la cadena vacía (que es lo que evita la segunda sentencia). Las dos
// salidas conocidas —arrancar en el uuid nil, o un OR con el cursor como puntero— están
// argumentadas una a una en backfill_self_pn.go:98-113 y aplican palabra por palabra
// aquí; no se repiten para no tener dos copias que envejezcan por separado.
//
// Coste real HOY, para dimensionar: public.contacts tiene 15 filas en UAT (13 con
// push_name). Esto corre UNA vez por arranque y tras la primera pasada el centinela no
// casa NADA, así que las vueltas siguientes son un seq scan que devuelve cero filas.
// Quien vaya a mover esto necesita una base real para medirlo.
const backfillPushNameSelect = `
		SELECT tenant_id::text, kind, value_bidx, contact_id::text, push_name
		FROM public.contacts
		WHERE push_name IS NOT NULL
		  AND push_name_enc IS NULL
		  AND (tenant_id::text, kind, value_bidx) > ($1, $2, $3)
		ORDER BY tenant_id::text, kind, value_bidx
		LIMIT $4
	`

// backfillPushNameUpdate escribe el sobre y VACÍA la columna en claro, en el MISMO
// UPDATE. La fila nunca puede quedar, ni un instante, con el nombre en claro Y el sobre
// a la vez.
//
// 🔴 `AND push_name_enc IS NULL` REPITE EL CENTINELA EN LA ESCRITURA, y no es
// redundante: convierte el UPDATE en un compare-and-swap. Dos instancias de la
// plataforma arrancando a la vez (un despliegue con solape, dos réplicas) leerían el
// mismo lote y cifrarían el mismo nombre con DOS sobres distintos; con esta guarda, la
// segunda escribe CERO filas y su RowsAffected lo dice. Es además el MISMO centinela
// que guarda el UPDATE de resolveExisting (MD-046.5), así que backfill y tráfico vivo
// compiten por la misma condición: gane quien gane, la fila acaba con UN sobre y sin
// dato en claro.
//
// La escritura va por la PK (tenant_id, kind, value_bidx) —que es lo que devuelve el
// cursor—, NO por contact_id. Es la diferencia con el UPDATE de resolveExisting, que sí
// va por contact_id porque allí el objetivo es el contacto entero; aquí el objetivo es
// LA FILA que se leyó, y tocar sus hermanas sería tocar filas que este lote no ha visto.
const backfillPushNameUpdate = `
		UPDATE public.contacts
		SET push_name_enc    = $1,
		    push_name_dek    = $2,
		    push_name_kek_id = $3,
		    push_name        = NULL,
		    updated_at       = now()
		WHERE tenant_id = $4 AND kind = $5 AND value_bidx = $6
		  AND push_name_enc IS NULL
	`

// backfillPushNameEmpty es el desenlace de VACIADO: borra el residuo sin escribir sobre
// alguno. Repite la misma guarda que su hermano por el mismo motivo (compare-and-swap:
// si otra instancia ya selló esta fila con un nombre de verdad, esta escritura no debe
// casar) y deja las TRES columnas del sobre intactas a NULL, que es justo lo que permite
// que el nombre real, si llega algún día, todavía pueda entrar por MD-046.5.
const backfillPushNameEmpty = `
		UPDATE public.contacts
		SET push_name  = NULL,
		    updated_at = now()
		WHERE tenant_id = $1 AND kind = $2 AND value_bidx = $3
		  AND push_name_enc IS NULL
	`

// BackfillPushName cifra las filas de public.contacts que todavía tienen el nombre de
// perfil EN CLARO (las que existían antes de la migración 0069) y vacía esa columna. Es
// la mitad en Go de T4.2: la 0069 solo pudo montar el sobre vacío, porque cifrar exige
// la KEK y POSTGRES NO LA TIENE —que es justo lo que hace que este sobre proteja algo—.
//
// ── LINAJE: QUÉ SE COPIÓ DEL MOLDE Y QUÉ CAMBIÓ (D-046.19) ────────────────────────
// El molde es fleet.BackfillSelfPn (internal/gateway/fleet/backfill_self_pn.go), el
// primer backfill cifrado en Go del repo. Este es el SEGUNDO, y por decisión de Jhoan se
// COPIA, no se generaliza: lo que difiere entre los dos no son NOMBRES, son REGLAS. Al
// TERCER caso se extrae, y esta lista es para que esa extracción sea mecánica.
//
// Se copió, íntegro y en espíritu: la forma del barrido (lote → transacción → commit →
// avanza el cursor), el cursor keyset sobre la PK con el cast a text, el centinela
// repetido en la escritura como compare-and-swap, la escritura del sobre y el vaciado
// del claro en UN SOLO UPDATE, RowsAffected como verdad en vez de la intención, los
// contadores sumados SOLO de lotes confirmados, el Report sin ni un dato, el lote
// preasignado con capacidad exacta y el fail-fast ante cualquier fallo de cifrado.
//
// Cambió, y cada cambio tiene su motivo escrito donde vive:
//   - El CENTINELA es `push_name_enc IS NULL` y no un bidx: aquí no hay índice ciego que
//     usar de testigo, porque no hay índice ciego (nadie busca por nombre). Ver
//     backfillPushNameSelect.
//   - NO se normaliza. El push_name es texto libre, no una referencia con formato. Ver
//     pushNameEnvelope, en repository_postgres.go.
//   - NO existe el desenlace «omitida». Era consecuencia directa de normalizar: sin
//     normalización no hay fallo POR FILA, y todo fallo que quede es del stack de claves,
//     o sea de TODAS las filas. Por eso este backfill no tiene contador de fallos que
//     vigilar ni Warn por fila.
//   - Aparece en cambio el desenlace de VACIADO (ver más abajo), que es un éxito y no un
//     fallo.
//   - La escritura va por la PK y no por contact_id: ver backfillPushNameUpdate.
//
// ── LA CADENA VACÍA: SE VACÍA, NO SE CIFRA, Y NO SE IGNORA ────────────────────────
// Es la decisión menos obvia del fichero, así que va entera.
//
// Desde Go una fila con push_name igual a la cadena vacía es INALCANZABLE hoy: nullStr
// mandaba la cadena vacía a NULL en los dos INSERT (repository_postgres.go:314-324, que
// sigue haciendo lo mismo ahora con push_name_kek_id) y el UPDATE de resolveExisting está
// guardado por un pushName no vacío. El manejo es DEFENSIVO —SQL escrito a mano, un binario antiguo—, y en UAT hoy
// hay CERO filas así.
//
// No se copia el filtro del molde que descarta la cadena vacía en el SELECT, porque allí
// respondía a otra regla: se excluía para que no cayera en el desenlace de omitida, que
// aquí no existe. Aquí la fila entra.
//
// Y NO SE CIFRA, aunque cifrarla funcionaría técnicamente: dejaría push_name_enc NO NULO
// con el sobre de una cadena vacía, y entonces el centinela de MD-046.5 no volvería a
// casar NUNCA en esa fila, así que el nombre real que llegara después se perdería para
// siempre. Un sobre vacío ganaría la carrera del «primer valor no vacío» con un valor que
// no es un valor, que es exactamente lo que MD-046.5 existe para evitar.
//
// Y NO SE IGNORA, que es la diferencia grande con T4.1 y hay que decirla en voz alta:
// allí el residuo se auto-saneaba en el siguiente latido, porque la guarda de SetSelfPn
// incluye una condición sobre la columna en claro. AQUÍ NO HAY LATIDO EQUIVALENTE: ni los
// INSERT (ON CONFLICT DO NOTHING / DO UPDATE SET updated_at) ni el UPDATE (guardado por
// un pushName no vacío) tocarían jamás esa fila. Si el backfill no la vacía, no la vacía
// NADIE, JAMÁS.
//
// El resultado es que el criterio (a) del plan se cumple LITERAL —un recuento de filas
// con push_name no nulo da cero— sin reescribir el criterio y sin dejar residuo.
//
// ── DÓNDE VIVE Y POR QUÉ ──────────────────────────────────────────────────────────
// Como PASO DE ARRANQUE del servidor, justo después de aplicar migraciones y mucho antes
// de aceptar tráfico; no como un cmd nuevo ni como un flag de la migración. El motivo es
// el modo de fallo: un backfill que se dispara A MANO se puede olvidar, y el olvido deja
// PII en claro EN SILENCIO, que es el modo de fallo que el Plan 046 entero existe para
// matar. Colgado del arranque, la única forma de saltárselo es no arrancar.
//
// ── IDEMPOTENCIA Y FULL-REPLAY ────────────────────────────────────────────────────
// Reejecutar es un no-op: en el segundo arranque ninguna fila casa el centinela y la
// primera consulta devuelve cero. El full-replay del runner tampoco lo resucita: un ADD
// COLUMN IF NOT EXISTS que no corre no escribe nada, así que los nombres que este
// backfill vació no vuelven. Y sobre una base creada desde cero no hay filas en claro que
// convertir.
//
// ✅ EL HUECO QUE HUBO AQUÍ, Y CÓMO SE CERRÓ — se cuenta porque el modo de fallo es
// instructivo, no porque siga abierto. Una fila legacy (nombre en claro, sobre vacío) a
// la que el TRÁFICO VIVO le sellara el sobre antes de que este backfill la viera se
// quedaba con su nombre en claro PARA SIEMPRE: escrito el sobre, la fila deja de casar
// el centinela de aquí y nadie la vuelve a mirar. La ventana es estrecha —el backfill
// corre antes de que ESTE proceso acepte tráfico— pero no es cero con varias réplicas o
// un despliegue con solape. Se cerró añadiendo `push_name = NULL` al SET del UPDATE de
// resolveExisting (repository_postgres.go), que es una ENMIENDA deliberada al SQL
// literal de MD-046.5: aquella decisión fijaba la GUARDA, y su SET se escribió pensando
// en filas nuevas, donde la columna en claro ya nace vacía. El argumento de fondo es la
// regla que ya gobierna al gemelo de T4.1: la fila no puede quedar, ni un instante, con
// el dato en claro Y el sobre a la vez. Lo protege un test propio
// (backfill_push_name_integration_test.go, el caso de la fila legacy con tráfico).
//
// ⚠️ LO QUE ESE ARREGLO NO CUBRE, y que no se puede cubrir desde aquí: un ROLLBACK del
// binario a una versión anterior a T4.2 vuelve a escribir `push_name` en claro, después
// de que este backfill haya pasado. Por eso el criterio (a) se verifica al TERMINAR el
// rollout, no al arrancar el primer proceso. El DROP de la columna en T5.4 (D-046.17)
// es lo que lo cierra de verdad.
//
// ── MODO DE FALLO ─────────────────────────────────────────────────────────────────
// Uno solo, y por eso este backfill es más simple que el molde: cualquier fallo de
// cifrado o de SQL ABORTA. Un fallo del cifrado no es de una fila —si la KEK no está, no
// está para ninguna— y ese mismo stack de claves lo usan las refs de contacto, los datos
// del comprador y el puente CRM, así que un proceso que arrancara «tolerando» esto
// quedaría medio roto en cinco sitios más, sirviendo tráfico. Fail-fast, como todo el
// arranque de este binario. No hay riesgo de bucle de arranque porque no hay fallo que
// dependa de QUÉ fila toque.
//
// ── HIGIENE ───────────────────────────────────────────────────────────────────────
// No se loguea un solo nombre, ni en el camino feliz ni en el de error: se cuentan FILAS.
// Y es trivial de cumplir porque AQUÍ NO SE LOGUEA NADA: PostgresResolver no tiene logger
// (repository_postgres.go:43-47) y NO se le añade uno. Que no lo tenga no es un olvido: el
// molde loguea Warn porque tiene un desenlace de fallo POR FILA que alguien debe mirar, y
// aquí no existe ese desenlace. Los errores que sí importan suben envueltos al arranque,
// que es quien decide. El contexto de esos errores son tenant_id y contact_id
// (identificadores opacos), nunca el nombre.
//
// batch <= 0 usa DefaultPushNameBackfillBatch (misma convención que crypto.Rekey).
func (r *PostgresResolver) BackfillPushName(ctx context.Context, batch int) (PushNameBackfillReport, error) {
	if batch <= 0 {
		batch = DefaultPushNameBackfillBatch
	}
	var report PushNameBackfillReport
	// El cursor arranca vacío: ver pushNameCursor (cualquier UUID en texto es mayor).
	cursor := pushNameCursor{}
	for {
		seen, next, batchReport, err := r.backfillPushNameBatch(ctx, batch, cursor)
		if err != nil {
			// El Report que sube es el de los lotes YA CONFIRMADOS: los contadores del
			// lote que abortó NO se suman, porque su transacción hizo rollback y esas
			// filas no se escribieron. Un informe que contara intenciones en vez de
			// commits mentiría justo en el arranque que falló.
			return report, err
		}
		if seen == 0 {
			return report, nil
		}
		report.Encrypted += batchReport.Encrypted
		report.Emptied += batchReport.Emptied
		cursor = next
	}
}

// pendingPushNameRow es una fila pendiente de cifrar. Lleva el nombre EN CLARO: es la
// única estructura de todo el paquete que lo hace y vive lo que dura un lote. contactID
// no participa en ninguna escritura (la PK es el trío de arriba): se trae SOLO para dar
// contexto opaco a los errores.
type pendingPushNameRow struct {
	tenantID  string
	kind      string
	valueBidx string
	contactID string
	pushName  string
}

// backfillPushNameBatch procesa UN lote dentro de UNA transacción y devuelve cuántas
// filas VIO (es lo que decide si el barrido sigue), dónde se quedó el cursor y los
// contadores del lote. Todo o nada: cualquier error hace rollback (defer) sin commit.
func (r *PostgresResolver) backfillPushNameBatch(ctx context.Context, batch int, cursor pushNameCursor) (seen int, next pushNameCursor, rep PushNameBackfillReport, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, cursor, rep, fmt.Errorf("contact: backfill push_name: iniciar transacción: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// El error del rollback se descarta EXPLÍCITAMENTE: PostgresResolver no tiene
			// logger (ver el docstring de BackfillPushName) y el error que de verdad
			// importa —el que provocó el rollback— ya va de vuelta al llamante.
			_ = tx.Rollback()
		}
	}()

	// El lote se materializa ANTES de emitir ningún UPDATE: en una misma tx no puede
	// haber un *sql.Rows abierto mientras se ejecutan sentencias. Con ello el lote
	// entero de nombres en claro está en RAM, que es la razón de que el lote exista y
	// tenga tope.
	pending, err := r.selectPendingPushName(ctx, tx, batch, cursor)
	if err != nil {
		return 0, cursor, rep, err
	}
	if len(pending) == 0 {
		return 0, cursor, rep, nil
	}

	for _, row := range pending {
		if row.pushName == "" {
			// Residuo: se VACÍA sin cifrar. El porqué, entero, en el docstring de
			// BackfillPushName. Es un ÉXITO, no una omisión: la fila queda limpia y con el
			// sobre libre para el nombre real que llegue después.
			n, eerr := r.execPushNameRow(ctx, tx, backfillPushNameEmpty, row,
				row.tenantID, row.kind, row.valueBidx)
			if eerr != nil {
				return 0, cursor, PushNameBackfillReport{}, eerr
			}
			rep.Emptied += n
			continue
		}
		// Se reutiliza EL MISMO helper que la persistencia (pushNameEnvelope, con el
		// mismo FieldCipher y el mismo KeyProvider). No hay una segunda copia de esta
		// lógica y no puede haberla: el sobre que escribe el backfill tiene que abrirse
		// con exactamente lo mismo que abre el que escribe el tráfico vivo.
		enc, dek, kekID, encErr := r.pushNameEnvelope(row.pushName)
		if encErr != nil {
			// Fallo del stack de claves: no es de esta fila, es de todas. Aborta.
			return 0, cursor, PushNameBackfillReport{}, fmt.Errorf(
				"contact: backfill push_name: cifrar el contacto %s del tenant %s: %w",
				row.contactID, row.tenantID, encErr)
		}
		n, uerr := r.execPushNameRow(ctx, tx, backfillPushNameUpdate, row,
			enc, dek, kekID, row.tenantID, row.kind, row.valueBidx)
		if uerr != nil {
			return 0, cursor, PushNameBackfillReport{}, uerr
		}
		// n == 0 significa que otra instancia (o el tráfico vivo) ganó el
		// compare-and-swap: la fila YA tiene sobre, solo que no el nuestro. No se cuenta
		// como cifrada (este contador es «lo que hizo ESTA pasada») y NO es un error: el
		// resultado es el correcto.
		rep.Encrypted += n
	}

	if cerr := tx.Commit(); cerr != nil {
		return 0, cursor, PushNameBackfillReport{}, fmt.Errorf("contact: backfill push_name: confirmar lote: %w", cerr)
	}
	committed = true

	last := pending[len(pending)-1]
	return len(pending), pushNameCursor{tenantID: last.tenantID, kind: last.kind, valueBidx: last.valueBidx}, rep, nil
}

// execPushNameRow ejecuta una de las dos sentencias de escritura de una fila y devuelve
// las filas REALMENTE afectadas (0 si otra instancia ganó el compare-and-swap). Existe
// para que los dos desenlaces compartan el mismo manejo de errores y la misma lectura de
// RowsAffected, y para que ninguno pueda contar una intención como un hecho. row solo se
// usa para el contexto OPACO del error (tenant_id, contact_id): nunca lleva el nombre.
func (r *PostgresResolver) execPushNameRow(ctx context.Context, tx *sql.Tx, stmt string, row pendingPushNameRow, args ...any) (int, error) {
	res, err := tx.ExecContext(ctx, stmt, args...)
	if err != nil {
		return 0, fmt.Errorf("contact: backfill push_name: actualizar el contacto %s del tenant %s: %w",
			row.contactID, row.tenantID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("contact: backfill push_name: filas afectadas: %w", err)
	}
	return int(n), nil
}

// selectPendingPushName lee el siguiente lote de filas con nombre en claro sin cifrar,
// desde la posición del cursor. Devuelve menos de batch filas cuando se acaba la tabla (y
// cero cuando ya no queda nada, que es la condición de parada del barrido).
func (r *PostgresResolver) selectPendingPushName(ctx context.Context, tx *sql.Tx, batch int, cursor pushNameCursor) (out []pendingPushNameRow, err error) {
	rows, err := tx.QueryContext(ctx, backfillPushNameSelect,
		cursor.tenantID, cursor.kind, cursor.valueBidx, batch)
	if err != nil {
		return nil, fmt.Errorf("contact: backfill push_name: seleccionar lote pendiente: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("contact: backfill push_name: cerrar filas del lote: %w", cerr)
			out = nil
		}
	}()

	// Capacidad exacta: el LIMIT del SELECT es batch, así que el lote NUNCA trae más
	// filas que eso y el slice no necesita crecer ni una vez. Sin esto, un lote de 500
	// encadena una decena de realocaciones con su copia, y cada copia deja una copia MÁS
	// de nombres en claro en un bloque de heap liberado que nadie borra. El argumento no
	// es el rendimiento (esto corre una vez por arranque) sino no repartir PII por el
	// heap sin necesidad.
	out = make([]pendingPushNameRow, 0, batch)

	for rows.Next() {
		var row pendingPushNameRow
		if serr := rows.Scan(&row.tenantID, &row.kind, &row.valueBidx, &row.contactID, &row.pushName); serr != nil {
			// El error de Scan no lleva el valor: solo el hecho de que no se pudo leer.
			return nil, fmt.Errorf("contact: backfill push_name: escanear fila pendiente: %w", serr)
		}
		out = append(out, row)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("contact: backfill push_name: iterar el lote pendiente: %w", rerr)
	}
	return out, nil
}
