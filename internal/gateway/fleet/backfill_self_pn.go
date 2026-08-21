package fleet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
)

// DefaultSelfPnBackfillBatch es el tamaño de lote por defecto del backfill cuando
// el llamador no especifica uno (<= 0). Calcado de crypto.DefaultRekeyBatch (500),
// que es el precedente más cercano del repo: acota memoria y duración de la
// transacción. Para esta tabla es holgado —una flota son decenas de filas, no
// millones— y esa holgura es deliberada: el lote existe para que el día que la
// tabla crezca el arranque no cargue la tabla entera en RAM, no para ir rápido hoy.
const DefaultSelfPnBackfillBatch = 500

// SelfPnBackfillReport resume UNA pasada del backfill, para que el arranque pueda
// decir en UNA línea qué hizo.
//
// 🔴 NO LLEVA NI UN NÚMERO, y no puede llevarlo nunca: son contadores y nada más
// (misma regla que crypto.Report, §10.H). El backfill es el único sitio del sistema
// que tiene TODOS los self_pn en claro pasando por memoria a la vez; si algo de aquí
// acabara en un log, sería la fuga con mejor relación coste/daño de todo el plan.
type SelfPnBackfillReport struct {
	// Encrypted son las filas que ESTA pasada convirtió: sobre escrito y columna en
	// claro vaciada, confirmadas por RowsAffected (no por intención).
	Encrypted int
	// Skipped son las filas cuyo self_pn NO normaliza (contact.ErrInvalidRef) y que
	// por tanto quedan CON SU NÚMERO EN CLARO. Es el número que el operador tiene
	// que mirar: mientras sea > 0, el criterio (a) de T4.1 NO se cumple. Cada una
	// deja además su propio Warn con los IDs opacos de la fila.
	Skipped int
}

// selfPnCursor es la posición del barrido por clave (keyset pagination) sobre la PK
// (tenant_id, edge_id, session_id).
//
// 🔴 POR QUÉ UN CURSOR Y NO UN `LIMIT` A SECAS, que es lo que uno escribe primero.
// El criterio de selección de este backfill NO se consume solo: una fila OMITIDA
// (número que no normaliza) sigue casando el centinela después de procesarla, así
// que un bucle «SELECT … LIMIT n; procesa; repite» que topara con un lote entero de
// filas omitidas volvería a leer LAS MISMAS filas para siempre — un arranque colgado
// en un bucle infinito, gastando CPU y sin una sola línea que lo delate. Con el
// cursor, cada vuelta arranca DESPUÉS de la última fila VISTA (procesada u omitida),
// así que el barrido siempre avanza y termina.
//
// Esta es la diferencia con crypto.Rekey, que sí puede permitirse el LIMIT pelado:
// allí toda fila seleccionada o se re-envuelve (y deja de casar `kek_id <> current`)
// o aborta el batch entero. Aquí hay un tercer desenlace —omitida— que Rekey no tiene.
//
// El cursor arranca en tres cadenas VACÍAS y eso ya es la primera vuelta: cualquier
// tenant_id::text real (un UUID) es mayor que ella al comparar filas en SQL, así que
// no hace falta una segunda sentencia ni un `if primera_vuelta`.
type selfPnCursor struct {
	tenantID  string
	edgeID    string
	sessionID string
}

// backfillSelfPnSelect toma el siguiente lote de filas PENDIENTES.
//
// 🔴 EL CENTINELA ES `self_pn_bidx IS NULL`, Y MIRA EL BIDX Y NO EL `_enc` A
// PROPÓSITO (aviso explícito de la migración 0068, regla (2) de su cabecera). Sin
// centinela, CADA REINICIO re-cifraría filas ya cifradas; y como el nonce del
// envelope es fresco por escritura, cada arranque dejaría un `self_pn_enc` distinto
// para el mismo número mientras el bidx —que es determinista— seguiría casando: el
// anti-self-loop seguiría funcionando, la consola seguiría mostrando el número, y
// NADIE SE ENTERARÍA de que la tabla se reescribe entera en cada boot. Escritura
// muda, la peor clase. El bidx es el testigo correcto justamente por ser el único de
// los cuatro campos que NO cambia entre dos cifrados del mismo número.
//
// ⚠️ Descartar la cadena vacía NO estaba en el enunciado de la tarea y se añade aquí:
// una cadena vacía no es PII, no se puede normalizar (contact.Normalize la rechaza) y
// no hay nada que cifrar en ella. Sin esta condición, toda fila con self_pn vacío
// entraría al backfill, fallaría al normalizar y sumaría al contador de OMITIDAS en
// cada arranque — un Warn perpetuo por un dato que no es un problema. La consulta de
// verificación (V3) de la 0068 ya excluye la cadena vacía por lo mismo. Y si a
// alguien le molesta ese vacío residual, lo limpia solo el siguiente Heartbeat de esa
// sesión: la guarda de SetSelfPn incluye `self_pn IS NOT NULL`, que casa con el vacío.
//
// El número EN CLARO se selecciona —es el único sitio del que se puede leer— y a
// partir de aquí vive en memoria hasta que la fila se escribe. No se loguea, no se
// mete en un error y no sale de esta función más que hacia selfPnEnvelope.
//
// ⚠️ EL PRECIO DEL `::text` EN EL CURSOR, DICHO EN VOZ ALTA (anotado el 2026-08-21,
// revisión de T4.1; antes esto no estaba escrito en ninguna parte). La PK de
// fleet_sessions es (tenant_id uuid, edge_id text, session_id text). Al comparar y
// ordenar por `tenant_id::text` la expresión deja de casar con la primera columna
// del índice de la PK, así que el planner NO puede usarlo: cada lote es un SEQ SCAN
// + SORT de la tabla entera. Con el loteo, un barrido de N filas cuesta N/batch
// barridos completos — o sea que el lote, que existe «para el día que la tabla
// crezca» (ver DefaultSelfPnBackfillBatch), es justo lo que multiplica el coste ESE
// día. Es la contradicción que hay que conocer antes de tocar esto.
//
// SE MANTIENE EL CAST, y no por inercia. Quitarlo exige que $1 sea un uuid VÁLIDO,
// y el cursor arranca en la cadena VACÍA —que es lo que hace que la primera vuelta
// no necesite una segunda sentencia ni un `if primera_vuelta`—: el vacío casteado a uuid es un
// error de sintaxis en Postgres, no un valor mínimo. Las dos salidas conocidas
// tienen su propio pero, y ninguna se puede probar en el entorno donde se escribe
// esto (sin Go, sin Postgres):
//
//	(a) arrancar el cursor en el uuid nil '00000000-…-000000000000'. Indexable y
//	    barato, pero `>` ESTRICTO deja fuera a un tenant cuyo id fuera exactamente
//	    el nil: esa fila no se cifraría NUNCA y conservaría su número en claro en
//	    silencio, que es literalmente el modo de fallo que el Plan 046 existe para
//	    matar. Hoy no hay ningún tenant nil en el repo (se comprobó: cero apariciones
//	    de uuid.Nil y de la constante en texto), pero el fallo sería mudo.
//	(b) `($1::uuid IS NULL OR (tenant_id, edge_id, session_id) > ($1::uuid,$2,$3))`
//	    con el cursor como *string. Correcto al 100 %, pero el OR le quita al planner
//	    el arranque por rango igual: gana el ORDER BY indexado y poco más.
//
// Coste real HOY, para dimensionar: este backfill corre UNA vez por arranque, la
// tabla son decenas de filas (una por sesión CloudLink viva) y tras la primera
// pasada exitosa el centinela `self_pn_bidx IS NULL` no casa NADA, así que las
// vueltas siguientes son un seq scan que devuelve cero filas. Quien vaya a mover
// esto necesita una base real para medirlo: es trabajo del barrido del CLI, no de
// una lectura.
const backfillSelfPnSelect = `
		SELECT tenant_id::text, edge_id, session_id, self_pn
		FROM public.fleet_sessions
		WHERE self_pn IS NOT NULL
		  AND self_pn <> ''
		  AND self_pn_bidx IS NULL
		  AND (tenant_id::text, edge_id, session_id) > ($1, $2, $3)
		ORDER BY tenant_id::text, edge_id, session_id
		LIMIT $4
	`

// backfillSelfPnUpdate escribe el sobre y VACÍA la columna en claro, en el MISMO
// UPDATE. Es el gemelo exacto del de SetSelfPn y por la misma razón: la fila nunca
// puede quedar, ni un instante, con el número en claro Y el sobre a la vez.
//
// 🔴 `AND self_pn_bidx IS NULL` REPITE EL CENTINELA EN LA ESCRITURA, y no es
// redundante: convierte el UPDATE en un compare-and-swap. Dos instancias de la
// plataforma arrancando a la vez (un despliegue con solape, dos réplicas) leerían el
// mismo lote y cifrarían el mismo número con DOS sobres distintos; con esta guarda,
// la segunda escribe CERO filas y su RowsAffected lo dice. Es la misma técnica que el
// centinela `greeted_at IS NULL` de MarkGreeted, y se prefiere a un `FOR UPDATE SKIP
// LOCKED` (lo que hace Rekey) porque aquí no hay nada que serializar: la operación es
// idempotente por contenido, así que basta con que la pierda quien llegue tarde.
//
// Mueve `updated_at` (es el reloj de la FILA y esta escritura la cambia) y NO toca
// `profile_updated_at`: la regla de la 0065 dice que SOLO SetProfile mueve el reloj
// del eje. Importa más de lo que parece — si esto tocara `profile_updated_at`, el
// primer arranque tras el despliegue publicaría una `version` nueva del kind
// "filters" a toda la flota con el mapa IDÉNTICO.
const backfillSelfPnUpdate = `
		UPDATE public.fleet_sessions
		SET self_pn_enc    = $1,
		    self_pn_dek    = $2,
		    self_pn_kek_id = $3,
		    self_pn_bidx   = $4,
		    self_pn        = NULL,
		    updated_at     = now()
		WHERE tenant_id = $5 AND edge_id = $6 AND session_id = $7
		  AND self_pn_bidx IS NULL
	`

// BackfillSelfPn cifra las filas de public.fleet_sessions que todavía tienen el
// número propio EN CLARO (las que existían antes de la migración 0068) y vacía esa
// columna. Es la mitad en Go de T4.1: la 0068 solo pudo montar el sobre vacío, porque
// cifrar exige la KEK y la indexKey y POSTGRES NO LAS TIENE —que es justo lo que hace
// que este sobre proteja algo—.
//
// 🔴 ES EL PRIMER BACKFILL CIFRADO EN GO DE ESTE REPO, y por tanto el molde de los que
// vengan (T4.2). Los backfills de hoy viven dentro del SQL de su migración; este no
// puede, y esa imposibilidad es de diseño, no una carencia del runner.
//
// 📌 QUÉ HACE EL SEGUNDO CON ESTE MOLDE — decidido por Jhoan el 2026-08-21: **T4.2 lo
// COPIA, no lo generaliza.** El motivo no es pereza: es que lo que difiere entre los
// dos backfills no son NOMBRES, son REGLAS. Compárese con crypto.Rekey, que sí está
// generalizado por tabla y funciona: allí la operación es idéntica en las cuatro
// tablas —re-envolver una DEK sin tocar el dato— y lo único que cambia son cuatro
// strings (tabla, PK, columna DEK, columna KEK), así que se parametriza con cuatro
// strings. Aquí no: este backfill NORMALIZA el valor (y por eso puede fallar y tiene
// un TERCER desenlace, «omitida») y calcula un ÍNDICE CIEGO; el de push_name no hace
// ninguna de las dos cosas —es texto libre, sin bidx, con solo dos desenlaces— y vive
// además en otro paquete (flujos/contact), cuyo repositorio ya tiene su propio cipher
// y KeyProvider. Un genérico que cubriera ambos acabaría recibiendo un callback
// «dame el sobre de esta fila, o dime que la omita», y ese callback ES casi todo lo
// específico: habría movido el código, no eliminado la duplicación.
//
// La regla que se aplica es la de la casa: copiar dos veces está bien, tres es deuda.
// Este es el primero y T4.2 el segundo. **Al TERCER caso se extrae**, y para entonces
// habrá tres formas reales sobre las que diseñar en vez de una escrita y otra
// imaginada. T4.2 deja anotado en su docstring qué copió de aquí y qué cambió, para
// que esa extracción sea mecánica.
//
// ── DÓNDE VIVE Y POR QUÉ (decisión de Jhoan) ──────────────────────────────────────
// Como PASO DE ARRANQUE del servidor, justo después de aplicar migraciones y mucho
// antes de aceptar tráfico. NO como un `cmd/` nuevo ni como un flag de `cmd/migrate`.
// El motivo es el modo de fallo, no la comodidad: un backfill que se dispara A MANO se
// puede olvidar, y el olvido deja PII en claro EN SILENCIO —sin error, sin alerta, sin
// nada que lo delate— que es exactamente el modo de fallo que el Plan 046 entero existe
// para matar. Colgado del arranque, la única forma de saltárselo es no arrancar.
//
// ── IDEMPOTENCIA, Y QUÉ PASA CON EL FULL-REPLAY ───────────────────────────────────
// Reejecutar es un no-op: en el segundo arranque el centinela no casa NINGUNA fila
// (todas tienen bidx) y la primera consulta devuelve cero. Y el replay del runner
// —que re-aplica TODO el directorio de migraciones cuando cambia el hash— no lo
// resucita: el `ADD COLUMN IF NOT EXISTS self_pn` de la 0028 no se ejecuta cuando la
// columna ya existe, y un ADD COLUMN que no corre no escribe nada, así que los números
// que este backfill vació NO vuelven (0068:78-84). Un FULL-REPLAY sobre una base
// creada desde cero es el otro extremo del mismo argumento: la tabla nace vacía, no
// hay filas en claro y esto no tiene nada que hacer.
//
// ⚠️ El hueco conocido: una fila que YA tiene bidx y a la que un binario ANTIGUO
// (rollback de despliegue) le vuelve a escribir `self_pn` en claro NO la recoge este
// centinela —tiene bidx, luego no es "pendiente"—. No se amplía el centinela para
// cubrirlo, porque la persistencia ya lo cubre mejor: la guarda de SetSelfPn incluye
// `OR self_pn IS NOT NULL`, así que esa fila se sanea sola en su siguiente latido, sin
// esperar a un reinicio. Ampliar el centinela aquí a `OR self_pn IS NOT NULL` traería
// de vuelta el re-cifrado mudo que el bidx-como-testigo acaba de cerrar.
//
// ── MODO DE FALLO, DECIDIDO Y ARGUMENTADO ─────────────────────────────────────────
// Los dos desenlaces que la tarea pide sopesar —abortar el arranque, u omitir con un
// contador— NO son alternativas: son la respuesta correcta a DOS fallos distintos, y
// el criterio para separarlos es si el fallo es DE UNA FILA o DE TODAS.
//
//   - Un self_pn que NO NORMALIZA (contact.ErrInvalidRef) es un defecto DE ESA FILA,
//     PERMANENTE y no reparable por el proceso: basura escrita por un Edge viejo, un
//     texto que no es un teléfono. Abortar el arranque aquí sería un BUCLE DE ARRANQUE
//     —el proceso muere, el supervisor lo levanta, vuelve a leer la misma fila y vuelve
//     a morir— y la plataforma ENTERA se quedaría caída por un dato basura de una
//     sesión, sin más salida que entrar a la base a mano. Se OMITE, se CUENTA y se
//     AVISA por fila con los IDs opacos. Precio, dicho sin rebajarlo: esa fila conserva
//     su número en claro. Por eso el contador va en el Report y el arranque lo imprime:
//     el criterio (a) de T4.1 se lee de ahí, y mientras Skipped > 0 NO se cumple.
//
//   - Un fallo del CIFRADO (o del SQL) NO es de una fila: si la KEK no está, no está
//     para ninguna. Se ABORTA el arranque. No hay bucle de arranque que temer —el fallo
//     no depende de qué fila toque— y sobre todo: ese mismo stack de claves lo usan los
//     contactos, los datos del comprador y el puente CRM, así que un proceso que
//     arrancara «tolerando» esto quedaría medio roto en cinco sitios más, sirviendo
//     tráfico. Fail-fast es lo que ya hace todo el arranque de este binario.
//
// El sesgo entre los dos casos es intencionado: se elige el bucle-de-arranque CERO y
// la PII residual VISIBLE, frente a la caída total invisible. Una fila con PII que
// nadie mira nunca sería peor que un arranque bloqueado —en eso el enunciado tiene
// razón—, pero aquí NADIE deja de mirarla: sale contada en el arranque y avisada por
// Warn cada vez.
//
// ── HIGIENE ───────────────────────────────────────────────────────────────────────
// No se loguea un solo número, ni en el camino feliz ni en el de error: se cuentan
// FILAS. Los Warn llevan tenant/edge/session (identificadores opacos) y la causa
// —contact.Normalize describe el fallo por LONGITUD, nunca con el valor (contact.go:121-131)—.
//
// batch <= 0 usa DefaultSelfPnBackfillBatch (misma convención que crypto.Rekey).
func (r *PostgresRepository) BackfillSelfPn(ctx context.Context, batch int) (SelfPnBackfillReport, error) {
	if batch <= 0 {
		batch = DefaultSelfPnBackfillBatch
	}
	var report SelfPnBackfillReport
	// El cursor arranca vacío: ver selfPnCursor (cualquier UUID en texto es > '').
	cursor := selfPnCursor{}
	for {
		seen, next, batchReport, err := r.backfillSelfPnBatch(ctx, batch, cursor)
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
		report.Skipped += batchReport.Skipped
		cursor = next
	}
}

// pendingSelfPnRow es una fila pendiente de cifrar. Lleva el número EN CLARO: es la
// única estructura de todo el paquete que lo hace y vive lo que dura un lote.
type pendingSelfPnRow struct {
	tenantID  string
	edgeID    string
	sessionID string
	selfPn    string
}

// backfillSelfPnBatch procesa UN lote dentro de UNA transacción y devuelve cuántas
// filas VIO (procesadas + omitidas: es lo que decide si el barrido sigue), dónde se
// quedó el cursor y los contadores del lote. Todo o nada: cualquier error hace
// rollback (defer) sin commit, igual que rekeyBatch.
func (r *PostgresRepository) backfillSelfPnBatch(ctx context.Context, batch int, cursor selfPnCursor) (seen int, next selfPnCursor, rep SelfPnBackfillReport, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, cursor, rep, fmt.Errorf("fleet: backfill self_pn: iniciar transacción: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rerr := tx.Rollback(); rerr != nil {
				r.log.Warn("fleet: backfill self_pn: rollback del lote", "error", rerr)
			}
		}
	}()

	// El cursor se materializa ANTES de emitir ningún UPDATE: en una misma tx no puede
	// haber un *sql.Rows abierto mientras se ejecutan sentencias (misma razón que
	// rekey.selectPending). Con ello el lote entero de números en claro está en RAM,
	// que es la razón de que el lote exista y tenga tope.
	pending, err := r.selectPendingSelfPn(ctx, tx, batch, cursor)
	if err != nil {
		return 0, cursor, rep, err
	}
	if len(pending) == 0 {
		return 0, cursor, rep, nil
	}

	for _, row := range pending {
		// Se reutiliza EL MISMO helper que la persistencia (selfPnEnvelope, que a su vez
		// normaliza con normalizeSelfPn → contact.Normalize y calcula el bidx con el
		// MISMO KeyProvider). No hay una segunda copia de esta lógica y no puede
		// haberla: dos normalizaciones distintas partirían el índice ciego en dos
		// poblaciones que ya nadie podría reconciliar, porque el valor en claro con el
		// que compararlas es justo lo que este backfill borra.
		bidx, enc, dek, kekID, encErr := r.selfPnEnvelope(row.tenantID, row.selfPn)
		if encErr != nil {
			if errors.Is(encErr, contact.ErrInvalidRef) {
				// Defecto PERMANENTE de esta fila: se omite, se cuenta y se avisa. Ver el
				// modo de fallo en el docstring de BackfillSelfPn. CERO PII en el aviso.
				rep.Skipped++
				r.log.Warn("fleet: backfill self_pn: la fila tiene un número que no normaliza; se deja SIN cifrar y CON el valor en claro",
					"tenant_id", row.tenantID, "edge_id", row.edgeID, "session_id", row.sessionID,
					"error", encErr)
				continue
			}
			// Fallo del stack de claves: no es de esta fila, es de todas. Aborta.
			return 0, cursor, SelfPnBackfillReport{}, fmt.Errorf(
				"fleet: backfill self_pn: cifrar la sesión %s/%s del tenant %s: %w",
				row.edgeID, row.sessionID, row.tenantID, encErr)
		}
		res, uerr := tx.ExecContext(ctx, backfillSelfPnUpdate,
			enc, dek, kekID, bidx, row.tenantID, row.edgeID, row.sessionID)
		if uerr != nil {
			return 0, cursor, SelfPnBackfillReport{}, fmt.Errorf(
				"fleet: backfill self_pn: actualizar la sesión %s/%s del tenant %s: %w",
				row.edgeID, row.sessionID, row.tenantID, uerr)
		}
		n, raerr := res.RowsAffected()
		if raerr != nil {
			return 0, cursor, SelfPnBackfillReport{}, fmt.Errorf(
				"fleet: backfill self_pn: filas afectadas: %w", raerr)
		}
		// n == 0 significa que otra instancia ganó el compare-and-swap: la fila YA está
		// cifrada, solo que no por nosotros. No se cuenta como cifrada (este contador es
		// «lo que hizo ESTA pasada») y NO es un error: el resultado es el correcto.
		rep.Encrypted += int(n)
	}

	if cerr := tx.Commit(); cerr != nil {
		return 0, cursor, SelfPnBackfillReport{}, fmt.Errorf("fleet: backfill self_pn: confirmar lote: %w", cerr)
	}
	committed = true

	last := pending[len(pending)-1]
	return len(pending), selfPnCursor{tenantID: last.tenantID, edgeID: last.edgeID, sessionID: last.sessionID}, rep, nil
}

// selectPendingSelfPn lee el siguiente lote de filas con número en claro sin cifrar,
// desde la posición del cursor. Devuelve menos de batch filas cuando se acaba la
// tabla (y cero cuando ya no queda nada, que es la condición de parada del barrido).
func (r *PostgresRepository) selectPendingSelfPn(ctx context.Context, tx *sql.Tx, batch int, cursor selfPnCursor) (out []pendingSelfPnRow, err error) {
	rows, err := tx.QueryContext(ctx, backfillSelfPnSelect,
		cursor.tenantID, cursor.edgeID, cursor.sessionID, batch)
	if err != nil {
		return nil, fmt.Errorf("fleet: backfill self_pn: seleccionar lote pendiente: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("fleet: backfill self_pn: cerrar filas del lote: %w", cerr)
			out = nil
		}
	}()

	// Capacidad exacta: el LIMIT del SELECT es `batch`, así que el lote NUNCA trae
	// más filas que eso y el slice no necesita crecer ni una vez. Sin esto, un lote
	// de 500 encadena ~10 realocaciones con su copia, y cada copia deja una copia
	// MÁS de números en claro en un bloque de heap liberado que nadie borra —el
	// argumento aquí no es el rendimiento (el backfill corre una vez por arranque)
	// sino no repartir PII por el heap sin necesidad. `prealloc` no marca este caso
	// porque solo audita bucles `range`; el motivo para hacerlo bien no es el lint.
	out = make([]pendingSelfPnRow, 0, batch)

	for rows.Next() {
		var row pendingSelfPnRow
		if serr := rows.Scan(&row.tenantID, &row.edgeID, &row.sessionID, &row.selfPn); serr != nil {
			// El error de Scan no lleva el valor: solo el hecho de que no se pudo leer.
			return nil, fmt.Errorf("fleet: backfill self_pn: escanear fila pendiente: %w", serr)
		}
		out = append(out, row)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("fleet: backfill self_pn: iterar el lote pendiente: %w", rerr)
	}
	return out, nil
}
