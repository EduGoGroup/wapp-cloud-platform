//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// P3 · paso 9: el reintento de postgres.WithTx, ejecutado de verdad (R9.6.e, hallazgo 38 de F1).

const (
	// p3DeadlocksQuery lee cuántos deadlocks ha detectado Postgres en la base del proceso.
	p3DeadlocksQuery = `SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()`
	// p3LockRow toma el row-lock de la fila de la clase dada del único contacto del tenant.
	p3LockRow = `SELECT 1 FROM public.contacts WHERE tenant_id = $1::uuid AND kind = $2 FOR UPDATE`

	// p3GateStep es el tope de cada sentencia de las transacciones compuerta que NO debe esperar.
	p3GateStep = 10 * time.Second
	// p3GateWait es el tope de la sentencia de T1 que espera a que Postgres rompa el ciclo: el
	// segundo de deadlock_timeout del servidor y margen. Muy por debajo de los 60 s de T1 y de los
	// 30 s de vida de un entrante en el servidor.
	p3GateWait = 20 * time.Second
	// p3StatsTimeout es el tope para ver subir pg_stat_database.deadlocks: el backend abortado
	// vuelca sus contadores con retraso (hasta unos 10 s si se queda ocioso sin haberlos volcado).
	p3StatsTimeout = 30 * time.Second
)

// p3Gate es una transacción compuerta: una conexión dedicada del pool del arnés con una
// transacción abierta a mano, y el pid de su backend para reconocerla en pg_stat_activity.
type p3Gate struct {
	name string
	conn *sql.Conn
	pid  int32 // el pid de un backend de Postgres es un int4
}

// p3OpenGate saca una conexión del *sql.DB del arnés (no abre ninguna nueva), anota su pid y abre
// una transacción. Registra en t.Cleanup el ROLLBACK y la devolución de la conexión: una
// transacción colgada dejaría al servidor bloqueado sobre sus row-locks. Falla (t.Fatalf) si algo
// no sale.
func p3OpenGate(t *testing.T, db *sql.DB, name string) *p3Gate {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), p3GateStep)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("%s: sacar una conexión del pool: %v", name, err)
	}
	g := &p3Gate{name: name, conn: conn}
	t.Cleanup(func() {
		// El contexto del test ya está cancelado cuando corre el Cleanup: se usa uno propio.
		end, cancelEnd := context.WithTimeout(context.Background(), p3GateStep)
		defer cancelEnd()
		if _, err := conn.ExecContext(end, `ROLLBACK`); err != nil {
			t.Logf("%s: ROLLBACK de limpieza: %v", name, err)
		}
		if err := conn.Close(); err != nil {
			t.Logf("%s: devolver la conexión: %v", name, err)
		}
	})
	if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&g.pid); err != nil {
		t.Fatalf("%s: leer el pid del backend: %v", name, err)
	}
	g.exec(t, `BEGIN`)
	return g
}

// exec ejecuta una sentencia que no debe esperar, con tope p3GateStep. Falla (t.Fatalf) si falla.
func (g *p3Gate) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), p3GateStep)
	defer cancel()
	if _, err := g.conn.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("%s: %s: %v", g.name, query, err)
	}
}

// p3ServerWaitingOn devuelve el pid de un backend de esta base, distinto de las compuertas, que
// está esperando un lock que tiene la compuerta holder; 0 si no hay ninguno. Ese backend es el
// del servidor: nadie más toma row-locks sobre contacts en esta base.
func p3ServerWaitingOn(t *testing.T, db *sql.DB, holder *p3Gate, others ...*p3Gate) int {
	t.Helper()
	skip := make([]int32, 0, 1+len(others))
	skip = append(skip, holder.pid)
	for _, g := range others {
		skip = append(skip, g.pid)
	}
	var pid int
	err := db.QueryRowContext(t.Context(), `
		SELECT coalesce(min(pid), 0) FROM pg_stat_activity
		WHERE datname = current_database() AND wait_event_type = 'Lock'
		  AND pid <> ALL($1::int[]) AND $2::int = ANY(pg_blocking_pids(pid))`, skip, holder.pid).Scan(&pid)
	if err != nil {
		t.Fatalf("leer pg_stat_activity: %v", err)
	}
	return pid
}

// p3WaitingForLock dice si el backend pid está esperando un lock.
func p3WaitingForLock(t *testing.T, db *sql.DB, pid int32) bool {
	t.Helper()
	var waiting bool
	err := db.QueryRowContext(t.Context(), `
		SELECT coalesce(bool_or(wait_event_type = 'Lock'), false) FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&waiting)
	if err != nil {
		t.Fatalf("leer pg_stat_activity: %v", err)
	}
	return waiting
}

// TestP3_TxRetryOnSerializationFailure provoca un deadlock REAL (SQLSTATE 40P01) en la transacción
// que el servidor abre con postgres.WithTx para resolver el contacto de un entrante, de modo que el
// servidor sea la víctima, y afirma que el entrante termina bien de todos modos: WithTx reintentó.
// Con el mutante `maxTxAttempts = 1` (internal/platform/storage/postgres/tx.go) este test CAE: el
// 40P01 aflora como ERROR «runtime: procesar entrante», el nombre no se sella y no hay respuesta.
//
// Qué hace el servidor (leído, no importado): contacts.Resolve bloquea, por ref y en orden fijo
// —phone_e164 primero, wa_lid después—, la fila de cada ref con SELECT … FOR UPDATE, y después
// sella el nombre con un UPDATE sobre las filas del contacto.
//
// La compuerta, con dos transacciones del test (T1, T2) sobre el contacto ya sembrado con dos filas:
//
//  1. T2 bloquea la fila phone_e164. T1 sube SU deadlock_timeout a 60 s y bloquea la fila wa_lid.
//  2. El Edge manda un entrante con las dos refs y un nombre. El servidor pide phone_e164 y se
//     queda esperando a T2 (se comprueba en pg_stat_activity).
//  3. T1 pide phone_e164: queda en cola detrás del servidor (se comprueba que espera un lock).
//  4. T2 hace COMMIT. El servidor obtiene phone_e164 y pide wa_lid, que tiene T1; T1 espera
//     phone_e164, que ahora tiene el servidor. Ciclo.
//  5. Víctima: Postgres no busca deadlocks hasta que un backend lleva deadlock_timeout esperando, y
//     quien encuentra el ciclo se aborta a sí mismo. El servidor tiene el valor por defecto (1 s) y
//     T1 tiene 60 s: el temporizador que vence es el del SERVIDOR, que recibe el 40P01. T1 nunca
//     llega a comprobar: su sentencia vuelve SIN error en cuanto el servidor aborta, y eso es lo
//     que prueba, desde fuera, quién fue la víctima (T2 ya había terminado).
//  6. WithTx reintenta: vuelve a pedir phone_e164, que ahora tiene T1, y espera sin ciclo. El test
//     hace ROLLBACK de T1 y el servidor termina.
//
// Aserta: (a) el fallo ocurrió —pg_stat_database.deadlocks subió y la sentencia de T1 volvió sin
// error—: si no, el paso estaría verde y hueco, y eso es rojo; (b) la operación terminó bien: las
// dos filas con el sobre del nombre poblado y la respuesta del flujo entregada; (c) ninguna línea
// ERROR de ese entrante.
//
// Servidor y base PROPIOS (p3_txretry): el contador de deadlocks es por base y la ráfaga del paso
// 8 puede sumar los suyos; compartiendo base, (a) podría cumplirse por un deadlock ajeno. SET LOCAL
// deadlock_timeout exige superusuario: el usuario del contenedor lo es, y se afirma.
func TestP3_TxRetryOnSerializationFailure(t *testing.T) {
	t.Parallel()
	sc := p3ActiveMenuScene(t, "p3_txretry", "p3-txretry")
	const pn, lid, waID = "573003330001", "880033300001@lid", "P3-RETRY-1"
	from := pn + "@s.whatsapp.net"

	id := p3SeedRetryContact(t, sc, pn, lid)
	deadlocks := consultaEntero(t, sc.DB, p3DeadlocksQuery)
	started := p3ProvokeServerDeadlock(t, sc, sealedIncoming{From: from, WaID: waID, Text: "1", FromPn: pn, FromLid: lid, PushName: "Ana"})

	// (c) antes que (b): con el reintento roto, la línea ERROR llega enseguida y dice por qué.
	var reply *textoRecibido
	edgeEsperar(t, p3GateWait, "la respuesta al entrante o su línea ERROR", func() bool {
		select {
		case got := <-sc.Edge.Textos():
			reply = &got
			return true
		default:
			return len(p3IncomingErrors(sc.S, waID)) != 0
		}
	})
	if errs := p3IncomingErrors(sc.S, waID); len(errs) != 0 {
		t.Fatalf("(c) el entrante se perdió: WithTx no reintentó tras el deadlock: %v", errs)
	}
	// (b) la operación terminó bien.
	if reply == nil || reply.A != pn || reply.Texto != p3SalesText {
		t.Fatalf("(b) la respuesta al entrante fue %+v; quería a %q con %q", reply, pn, p3SalesText)
	}
	rows := p3ContactRows(t, sc, id)
	if len(rows) != 2 || !rows[0].nameSealed(t) || !rows[1].nameSealed(t) {
		t.Errorf("(b) tras el reintento las filas son %+v; quería las dos con el sobre del nombre poblado", rows)
	}
	elapsed := time.Since(started)

	// (a) el fallo ocurrió de verdad.
	var seen int
	edgeEsperar(t, p3StatsTimeout, "que pg_stat_database.deadlocks suba", func() bool {
		seen = consultaEntero(t, sc.DB, p3DeadlocksQuery)
		return seen > deadlocks
	})
	t.Logf("deadlocks: %d → %d; del entrante a su respuesta, con un 40P01 por medio: %s", deadlocks, seen, elapsed.Round(time.Millisecond))
	if elapsed < time.Second {
		t.Errorf("(a) el entrante tardó %s: menos que el deadlock_timeout del servidor (1 s), así que no esperó ningún ciclo", elapsed)
	}

	if errs := sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	edgeSinErrores(t, sc.S, nil)
}

// p3SeedRetryContact es la siembra del paso 9 y sus precondiciones: dos filas del mismo contacto,
// sin nombre (el UPDATE del reintento tiene que escribir), y un usuario de Postgres que puede subir
// deadlock_timeout. Devuelve el contact_id; falla (t.Fatalf) si algo no quedó así.
func p3SeedRetryContact(t *testing.T, sc p3Scene, pn, lid string) string {
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: pn + "@s.whatsapp.net", WaID: "P3-RETRY-SEED", Text: p3Keyword, FromPn: pn, FromLid: lid})
	sc.expectText(t, pn, p3Welcome)
	sc.expectText(t, pn, p3MenuPrompt)
	id := p3NewContactID(t, sc, nil)
	rows := p3ContactRows(t, sc, id)
	if len(rows) != 2 || rows[0].Kind != "phone_e164" || rows[1].Kind != "wa_lid" || rows[0].nameSealed(t) || rows[1].nameSealed(t) {
		t.Fatalf("precondición: la siembra dejó %+v; quería dos filas (phone_e164 y wa_lid) con el sobre del nombre a NULL", rows)
	}
	if su := consultaTexto(t, sc.DB, `SHOW is_superuser`); su != "on" {
		t.Fatalf("is_superuser = %q: sin superusuario no se puede subir deadlock_timeout y la víctima no sería el servidor", su)
	}
	return id
}

// p3ProvokeServerDeadlock son los pasos 1 a 6 de la compuerta: con T1 y T2 sobre las dos filas del
// contacto, manda el entrante in y cierra el ciclo de modo que la víctima sea el servidor. Devuelve
// el instante en que se mandó el entrante; falla (t.Fatalf) si la sentencia de T1 falla o no vuelve.
func p3ProvokeServerDeadlock(t *testing.T, sc p3Scene, in sealedIncoming) time.Time {
	// 1 · las compuertas.
	t2 := p3OpenGate(t, sc.DB, "T2")
	t1 := p3OpenGate(t, sc.DB, "T1")
	t2.exec(t, p3LockRow, sc.Tenant, "phone_e164")
	t1.exec(t, `SET LOCAL deadlock_timeout = '60s'`)
	t1.exec(t, p3LockRow, sc.Tenant, "wa_lid")

	// 2 · el entrante: el servidor se queda esperando la fila phone_e164, que tiene T2.
	started := time.Now()
	sc.Edge.sendSealedIncoming(t, in)
	edgeEsperar(t, edgeTopeFila, "que el servidor espere el row-lock de phone_e164 que tiene T2", func() bool {
		return p3ServerWaitingOn(t, sc.DB, t2, t1) != 0
	})

	// 3 · T1 pide phone_e164 y queda detrás del servidor.
	t1Done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), p3GateWait)
		defer cancel()
		_, err := t1.conn.ExecContext(ctx, p3LockRow, sc.Tenant, "phone_e164")
		t1Done <- err
	}()
	edgeEsperar(t, edgeTopeFila, "que T1 espere el row-lock de phone_e164", func() bool {
		return p3WaitingForLock(t, sc.DB, t1.pid)
	})

	// 4 · T2 suelta: se cierra el ciclo entre el servidor y T1.
	t2.exec(t, `COMMIT`)

	// 5 · Postgres aborta al servidor; la sentencia de T1 vuelve sin error.
	select {
	case err := <-t1Done:
		if err != nil {
			t.Fatalf("la sentencia de T1 falló (%v): la víctima del deadlock no fue el servidor, o el ciclo no se formó", err)
		}
	case <-time.After(p3GateWait + p3GateStep):
		t.Fatalf("la sentencia de T1 no volvió en %s", p3GateWait+p3GateStep)
	}
	// 6 · T1 suelta y el reintento del servidor termina.
	t1.exec(t, `ROLLBACK`)
	return started
}
