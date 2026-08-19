package flowlifecycle

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

const dsnEnv = "WAPP_TEST_DB_DSN"

// openTestDB abre la BD de integración propia de esta tarea (T6.5: NUNCA
// wapp_test, hay interbloqueo medido con el otro agente de la Ola 6) y aplica
// el esquema. Mismo contrato que el resto de *_integration_test.go del repo:
// sin DSN se salta.
//
// AISLAMIENTO entre tests: la consulta del colector es GLOBAL (no filtra por
// tenant — es un contador de plataforma, no uno por-tenant), así que dos tests
// que compartan esta BD verían las filas del otro si ambos arrancaran su
// Collector con cursor=0. Por eso NINGÚN test de este archivo usa cursor=0
// contra datos preexistentes: todos llaman initCursor() ANTES de insertar sus
// propias filas, y esa llamada es justo la que fija la decisión de la tarea
// (arrancar en max(id)) — así el aislamiento entre tests y la prueba de la
// decisión de producción son la MISMA mecánica, no dos cosas separadas.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten", dsnEnv, err)
	}
	if _, merr := migrations.Migrate(ctx, db); merr != nil {
		t.Fatalf("aplicar migraciones: %v", merr)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	return db
}

// insertFlowEvent inserta UNA fila cruda en flow_events, mimetizando lo que
// InsertFlowEvent (internal/flujos/store) escribe en producción — este test
// NO importa ese paquete (fuera de mi propiedad en T6.5) y construye la fila
// con SQL directo, que es exactamente el mismo contrato de columnas (0009).
func insertFlowEvent(t *testing.T, db *sql.DB, tenantID, kind, name, payloadJSON string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(`
		INSERT INTO public.flow_events (tenant_id, contact_id, flow_id, flow_version, kind, name, payload)
		VALUES ($1, 'contact-opaco', 'flow-1', 1, $2, $3, $4::jsonb)
		RETURNING id
	`, tenantID, kind, name, payloadJSON).Scan(&id)
	if err != nil {
		t.Fatalf("insertar flow_event de prueba (%s): %v", name, err)
	}
	return id
}

// recorded es una captura de UNA llamada a onCount, para inspeccionar en los
// asserts sin depender de prometheus (mismo desacoplo del paquete: el test
// tampoco lo importa).
type recorded struct {
	name, eventKind, reason string
	delta                   float64
}

// newTestCollector construye un Collector, lo hace GANAR el advisory lock
// (becomeLeader) y descarta lo que esa vuelta de "priming" cuente antes de
// devolverlo — a partir de aquí el test controla el resto de la vuelta a mano
// (pollOnce), sin ticker.
//
// Por qué priming con pollOnce y no con initCursor() directo (como hacía esta
// función antes de T6.5 · elección de líder): becomeLeader (el gate nuevo de
// pollOnce) ya llama a initCursor() la primera vez que gana el lock — llamarlo
// TAMBIÉN aquí a mano sería una segunda inicialización redundante que, si el
// caller inserta filas de prueba ENTRE esta llamada y su primer pollOnce()
// real (el patrón de TODOS los tests de este fichero), pisaría el cursor con
// un max(id) que ya incluye esas filas, y el test las vería como "histórico"
// en vez de "nuevas". Cebar con pollOnce() de verdad y descartar `got` es la
// MISMA operación que hace el primer pollOnce de Run() en producción — un
// único punto donde se fija el cursor, no dos.
//
// FALLA el test (no lo salta) si no logra ganar el lock: eso solo puede pasar
// si un test anterior en este mismo binario no lo liberó (fuga), y quiero
// verlo como rojo explícito, no como un pollOnce silencioso que no cuenta
// nada y deja un test confuso más abajo.
func newTestCollector(t *testing.T, db *sql.DB) (*Collector, *[]recorded) {
	t.Helper()
	var got []recorded
	c := NewCollector(db, func(name, eventKind, reason string, delta float64) {
		got = append(got, recorded{name, eventKind, reason, delta})
	}, nil)
	t.Cleanup(func() { c.releaseLeadership(context.Background()) })

	c.pollOnce(context.Background()) // vuelta de priming: gana el lock + fija el cursor
	if c.leaderConn == nil {
		t.Fatalf("newTestCollector: no se pudo ganar el advisory lock del colector (¿un test anterior no lo liberó?)")
	}
	got = nil // descarta lo que la vuelta de priming haya contado

	return c, &got
}

// TestCollector_ArrancaEnMaxID_NoRecuentaHistorico fija la DECISIÓN de la
// tarea (T6.5, informe): el cursor arranca en max(id), así que el histórico
// insertado ANTES de construir el colector nunca aparece en la primera vuelta
// — solo lo que llega DESPUÉS.
func TestCollector_ArrancaEnMaxID_NoRecuentaHistorico(t *testing.T) {
	db := openTestDB(t)
	tenant := "t65-" + t.Name()

	// "Histórico": ya estaba en la tabla ANTES de que el colector existiera.
	insertFlowEvent(t, db, tenant, "event", "event_started", `{"kind":"cart"}`)
	insertFlowEvent(t, db, tenant, "event", "event_started", `{"kind":"cart"}`)

	c, got := newTestCollector(t, db)
	c.pollOnce(context.Background())
	if len(*got) != 0 {
		t.Fatalf("con cursor=max(id), el histórico preexistente NO debe contarse: got %+v", *got)
	}

	// Lo NUEVO, insertado después de que el colector fijara su cursor, sí debe
	// aparecer en la vuelta siguiente.
	insertFlowEvent(t, db, tenant, "event", "event_closed", `{"kind":"cart"}`)
	c.pollOnce(context.Background())
	if len(*got) != 1 {
		t.Fatalf("una fila nueva tras el cursor debe contarse: got %+v", *got)
	}
	if (*got)[0] != (recorded{"event_closed", "cart", "", 1}) {
		t.Fatalf("grupo inesperado: %+v", (*got)[0])
	}
}

// TestCollector_EscapaGuionBajoEnElPredicado fija la enmienda #1 (LIKE
// 'event\_%' escapado): una fila cuyo nombre solo matchea el comodín SIN
// escapar («eventXfoo», donde el `_` sin escapar comodinea cualquier
// carácter, incluida la 'X') NO debe contarse. Sin el escape, esta prueba
// FALLARÍA contando 2 grupos en vez de 1 — ver la salida real de la mutación
// en el informe de la tarea.
func TestCollector_EscapaGuionBajoEnElPredicado(t *testing.T) {
	db := openTestDB(t)
	tenant := "t65-" + t.Name()

	c, got := newTestCollector(t, db)

	insertFlowEvent(t, db, tenant, "event", "event_started", `{"kind":"cart"}`)     // SÍ cuenta
	insertFlowEvent(t, db, tenant, "persist", "eventXfoo", `{"kind":"cart"}`)       // NO debe contar
	insertFlowEvent(t, db, tenant, "persist", "survey_answer", `{"kind":"survey"}`) // control negativo, ni de lejos

	c.pollOnce(context.Background())

	if len(*got) != 1 {
		t.Fatalf("quiero exactamente 1 grupo (event_started/cart), got %d: %+v", len(*got), *got)
	}
	if (*got)[0] != (recorded{"event_started", "cart", "", 1}) {
		t.Fatalf("grupo inesperado: %+v", (*got)[0])
	}
}

// TestCollector_AgregaPorNombreYPorPayloadKind fija que el GROUP BY es sobre
// `name` y `payload->>'kind'`, NUNCA sobre la columna `kind` de la fila
// ("persist"|"event"). Con tres nombres y varios event_kind deben salir
// grupos DISTINTOS — el fallo plausible que la refutación midió es un
// GROUP BY kind que colapsa todo en una sola fila con el 100%.
//
// Incluye "event_escaped", el SÉPTIMO efecto que el OTRO agente añade en
// paralelo (contrato de la Ola 6): este test NO lo declara en ningún lado del
// código de producción, solo lo inserta — si el colector lo cuenta igual que
// los demás es la prueba de que filtra por PREFIJO y no por una lista
// cerrada de nombres.
func TestCollector_AgregaPorNombreYPorPayloadKind(t *testing.T) {
	db := openTestDB(t)
	tenant := "t65-" + t.Name()

	c, got := newTestCollector(t, db)

	insertFlowEvent(t, db, tenant, "event", "event_started", `{"kind":"cart"}`)
	insertFlowEvent(t, db, tenant, "event", "event_started", `{"kind":"cart"}`)
	insertFlowEvent(t, db, tenant, "event", "event_closed", `{"kind":"cart"}`)
	insertFlowEvent(t, db, tenant, "event", "event_started", `{"kind":"survey"}`)
	insertFlowEvent(t, db, tenant, "event", "event_escaped", `{"kind":"menu"}`)

	c.pollOnce(context.Background())

	sort.Slice(*got, func(i, j int) bool {
		gi, gj := (*got)[i], (*got)[j]
		if gi.name != gj.name {
			return gi.name < gj.name
		}
		if gi.eventKind != gj.eventKind {
			return gi.eventKind < gj.eventKind
		}
		// Tercer criterio desde T4.2: sin él, dos grupos que solo difieren en el
		// reason quedan en orden indefinido y el test es intermitente.
		return gi.reason < gj.reason
	})

	// El reason va VACÍO en las cuatro: estas filas las siembra el test con payloads
	// de solo {history_id, kind} — el mismo payload que emiten hoy los seis efectos
	// que no son event_escaped, y el que tienen todas las filas anteriores a T4.1.
	// La agregación CON reason poblado la cubre TestCollector_AgregaTambienPorReason.
	want := []recorded{
		{"event_closed", "cart", "", 1},
		{"event_escaped", "menu", "", 1},
		{"event_started", "cart", "", 2},
		{"event_started", "survey", "", 1},
	}
	if len(*got) != len(want) {
		t.Fatalf("grupos = %d, quiero %d: got=%+v want=%+v", len(*got), len(want), *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Fatalf("grupo[%d] = %+v, quiero %+v (got completo: %+v)", i, (*got)[i], want[i], *got)
		}
	}
}

// TestCollector_CursorAvanzaYNoRecuenta fija la garantía central de
// "colector incremental" que da nombre al paquete: una vuelta sin filas
// nuevas NO vuelve a llamar a onCount.
func TestCollector_CursorAvanzaYNoRecuenta(t *testing.T) {
	db := openTestDB(t)
	tenant := "t65-" + t.Name()

	c, got := newTestCollector(t, db)

	insertFlowEvent(t, db, tenant, "event", "event_started", `{"kind":"cart"}`)
	c.pollOnce(context.Background())
	if len(*got) != 1 {
		t.Fatalf("primera vuelta: len=%d, quiero 1", len(*got))
	}

	c.pollOnce(context.Background())
	if len(*got) != 1 {
		t.Fatalf("segunda vuelta sin filas nuevas: len=%d, quiero seguir en 1 (no debe recontar)", len(*got))
	}

	insertFlowEvent(t, db, tenant, "event", "event_closed", `{"kind":"cart"}`)
	c.pollOnce(context.Background())
	if len(*got) != 2 {
		t.Fatalf("tercera vuelta con una fila nueva: len=%d, quiero 2", len(*got))
	}
}

// recordingLogger captura los mensajes Error para el test de "silencioso y
// barato" (T6.5 · elección de líder): perder la carrera del advisory lock NO
// debe loguear nada. Con mutex por si alguna vez se comparte entre goroutines
// (no es el caso hoy, pero mismo criterio defensivo que
// internal/bootstrap/es256_key_test.go:recordingLogger).
type recordingLogger struct {
	mu     sync.Mutex
	errors []string
}

func (l *recordingLogger) Debug(string, ...any) {}
func (l *recordingLogger) Info(string, ...any)  {}
func (l *recordingLogger) Warn(string, ...any)  {}
func (l *recordingLogger) Error(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, msg)
}
func (l *recordingLogger) With(...any) sharedlogger.Logger { return l }

func (l *recordingLogger) errorCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.errors)
}

// TestCollector_DosReplicas_SoloUnaLideraYCuenta es el test que fija la
// elección de líder (T6.5, refutación multi-réplica del revisor, decisión de
// Jhoan 2026-08-11): monta DOS colectores contra la MISMA base de datos —la
// simulación más directa de "dos réplicas del servidor apuntando a la misma
// Postgres"— y comprueba que el agregado de ambos NO duplica la fila
// insertada. SIN el arreglo (pollOnce sin becomeLeader), este test debe verse
// FALLAR con total=2 (el defecto medido: "2 réplicas → 6 donde había 3", aquí
// con una sola fila el factor N se ve como 1→2). Ver el informe de la tarea
// para la salida real en rojo (con becomeLeader revertido a un no-op) y en
// verde (con el arreglo).
//
// También fija que la réplica que PIERDE la carrera (b) lo hace SILENCIOSO Y
// BARATO: sin error logueado (recordingLogger en 0) y sin bloquearse (el test
// entero tiene el timeout normal de `go test`; si becomeLeader se quedara
// esperando el lock en vez de usar la variante NO bloqueante, este test
// colgaría en vez de fallar rápido).
func TestCollector_DosReplicas_SoloUnaLideraYCuenta(t *testing.T) {
	db := openTestDB(t)
	tenant := "t65-" + t.Name()

	// a gana el lock vía newTestCollector (que ya exige ganarlo, arriba).
	a, gotA := newTestCollector(t, db)

	// b se construye A MANO (no con newTestCollector, que hace t.Fatalf si
	// pierde la carrera): aquí perder es justo lo que quiero comprobar.
	logB := &recordingLogger{}
	var gotB []recorded
	b := NewCollector(db, func(name, eventKind, reason string, delta float64) {
		gotB = append(gotB, recorded{name, eventKind, reason, delta})
	}, logB)
	t.Cleanup(func() { b.releaseLeadership(context.Background()) })

	if b.becomeLeader(context.Background()) {
		t.Fatalf("con a ya líder, b NO debía poder ganar el advisory lock (collectorLockKey compartido)")
	}
	if got := logB.errorCount(); got != 0 {
		t.Fatalf("perder la carrera del advisory lock debe ser silencioso: %d líneas de Error logueadas, quiero 0", got)
	}

	insertFlowEvent(t, db, tenant, "event", "event_started", `{"kind":"cart"}`)

	a.pollOnce(context.Background())
	b.pollOnce(context.Background())

	total := len(*gotA) + len(gotB)
	if total != 1 {
		t.Fatalf("con elección de líder, EXACTAMENTE una réplica debe contar cada fila: total=%d (a=%+v b=%+v) — si esto da 2, el advisory lock no está evitando el doble conteo", total, *gotA, gotB)
	}
	if len(*gotA) != 1 {
		t.Fatalf("la réplica líder (a) es la que debía contar la fila nueva: gotA=%+v gotB=%+v", *gotA, gotB)
	}
	if got := logB.errorCount(); got != 0 {
		t.Fatalf("b pasando de largo su vuelta debe seguir siendo silencioso: %d líneas de Error logueadas, quiero 0", got)
	}
}

// TestCollector_RunLiberaElLockAlCancelarElContexto fija la garantía de
// liberación: Run() suelta el advisory lock (y cierra la conexión que lo
// sostenía) tanto si termina por cancelación de contexto —el único camino de
// salida que Run() tiene hoy— como por cualquier otro que se añada en el
// futuro, porque la liberación vive en un defer, no en el cuerpo del bucle.
// Un lock que no se libera aquí deja al colector muerto para TODO el clúster
// hasta que Postgres recicle la conexión (ver el comentario de Run()).
//
// Verificación: tras que Run() retorne, OTRA conexión debe poder ganar el
// mismo collectorLockKey — si Run() se hubiera quedado con el lock, este
// segundo pg_try_advisory_lock devolvería false.
func TestCollector_RunLiberaElLockAlCancelarElContexto(t *testing.T) {
	db := openTestDB(t)

	c := NewCollector(db, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()

	// Run() completa su primera vuelta (síncrona, becomeLeader incluido)
	// ANTES de mirar ctx.Done() por primera vez, así que cancelar ya mismo no
	// es una carrera con "¿llegó a ganar el lock?": para cuando el select lo
	// note, la vuelta síncrona ya corrió.
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run() no retornó tras cancelar el contexto: ¿se quedó bloqueado sosteniendo el advisory lock?")
	}

	verifyConn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("reservar conexión de verificación: %v", err)
	}
	defer func() {
		if cerr := verifyConn.Close(); cerr != nil {
			t.Logf("cerrando conexión de verificación: %v", cerr)
		}
	}()

	var acquired bool
	if err := verifyConn.QueryRowContext(context.Background(), "SELECT pg_try_advisory_lock($1)", collectorLockKey).Scan(&acquired); err != nil {
		t.Fatalf("comprobar que el advisory lock quedó libre: %v", err)
	}
	if !acquired {
		t.Fatalf("el advisory lock del colector sigue OCUPADO tras cancelar el contexto y que Run() retornara: fuga de lock")
	}
	if _, err := verifyConn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", collectorLockKey); err != nil {
		t.Fatalf("liberar el lock de verificación: %v", err)
	}
}

// TestCollector_AgregaTambienPorReason cierra REQ-053.4 por el lado de la métrica
// (Plan 053 · Ola 4 · T4.2): las TRES causas de `event_escaped` tienen que salir
// como tres series distintas, y el resto de efectos tiene que seguir saliendo
// EXACTAMENTE como antes.
//
// Lo que este test protege de verdad es la mitad aburrida: que añadir la dimensión
// no colapse ni parta lo que ya había. Un `GROUP BY 1, 2, 3` sobre una columna que
// es NULL en la mayoría de las filas es justo el sitio donde se cuela un INNER JOIN
// implícito o un COALESCE de más y desaparecen los seis efectos que no la llevan —
// sin error, con el contador a cero, que es como se pierden las métricas.
func TestCollector_AgregaTambienPorReason(t *testing.T) {
	db := openTestDB(t)
	tenant := "t53o4-" + t.Name()

	c, got := newTestCollector(t, db)

	// Las tres causas del mismo nombre de efecto y del mismo event_kind: lo ÚNICO
	// que las separa es el reason. Si el GROUP BY lo ignorase, las tres colapsarían
	// en un solo grupo de 4 y el fallo sería silencioso (un contador que suma bien
	// el total y miente en el desglose).
	insertFlowEvent(t, db, tenant, "event", "event_escaped", `{"kind":"cart","reason":"client_escape"}`)
	insertFlowEvent(t, db, tenant, "event", "event_escaped", `{"kind":"cart","reason":"client_escape"}`)
	insertFlowEvent(t, db, tenant, "event", "event_escaped", `{"kind":"cart","reason":"owner_flow_finished"}`)
	insertFlowEvent(t, db, tenant, "event", "event_escaped", `{"kind":"cart","reason":"orphan_menu"}`)
	// Un efecto SIN reason (el payload de los otros seis, y el de toda fila anterior
	// a T4.1): debe seguir contándose, con el reason vacío.
	insertFlowEvent(t, db, tenant, "event", "event_started", `{"kind":"cart"}`)

	c.pollOnce(context.Background())

	sort.Slice(*got, func(i, j int) bool {
		gi, gj := (*got)[i], (*got)[j]
		if gi.name != gj.name {
			return gi.name < gj.name
		}
		if gi.eventKind != gj.eventKind {
			return gi.eventKind < gj.eventKind
		}
		return gi.reason < gj.reason
	})

	want := []recorded{
		{"event_escaped", "cart", "client_escape", 2},
		{"event_escaped", "cart", "orphan_menu", 1},
		{"event_escaped", "cart", "owner_flow_finished", 1},
		{"event_started", "cart", "", 1},
	}
	if len(*got) != len(want) {
		t.Fatalf("las tres causas deben dar tres series distintas (más la del efecto sin causa): grupos = %d, quiero %d.\ngot  = %+v\nwant = %+v",
			len(*got), len(want), *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Fatalf("grupo[%d] = %+v, quiero %+v (got completo: %+v)", i, (*got)[i], want[i], *got)
		}
	}
}

// TestCollector_ReasonVacioNoSeConfundeConAusente fija el contrato del valor vacío,
// que es la parte que un lector de la métrica se puede creer al revés.
//
// `reason=""` significa DOS cosas que a propósito NO se distinguen: «este efecto no
// tiene causas que declarar» (los otros seis) y «esta fila es anterior a T4.1». En
// la bitácora append-only son indistinguibles —nadie va a reescribir el histórico—
// así que la métrica tampoco las separa: un tercer valor centinela para el legado
// mentiría sobre una precisión que la BD no tiene.
//
// 🔬 MATIZ MEDIDO AL ESCRIBIR ESTE TEST, y va contra la intuición: en SQL, `NULL` y
// `”` NO son el mismo grupo. `GROUP BY payload->>'reason'` devuelve DOS filas
// agregadas —una por la clave ausente, otra por la clave vacía—, no una. Lo que las
// reúne es el paso siguiente: las dos se escanean a `sql.NullString` y se publican
// con `.String == ""`, así que `WithLabelValues(..., "")` da la MISMA serie de
// Prometheus y el `Add` las acumula. El desglose de la BD tiene un grupo de más; la
// métrica que se consulta, no. Este test afirma justo eso —el total en una sola
// serie— y no el número de grupos intermedios, que es un detalle del plan de
// ejecución de Postgres y no un contrato de nadie.
func TestCollector_ReasonVacioNoSeConfundeConAusente(t *testing.T) {
	db := openTestDB(t)
	tenant := "t53o4-" + t.Name()

	c, got := newTestCollector(t, db)

	// Sin la clave (el payload de siempre, y el de toda fila anterior a T4.1) y CON
	// la clave a cadena vacía (lo que escribiría un emisor mal programado).
	insertFlowEvent(t, db, tenant, "event", "event_escaped", `{"kind":"menu"}`)
	insertFlowEvent(t, db, tenant, "event", "event_escaped", `{"kind":"menu","reason":""}`)

	c.pollOnce(context.Background())

	// Ni una fila perdida, y TODO lo publicado va a la misma etiqueta vacía: sumar
	// los deltas por (name, event_kind, reason) reproduce exactamente lo que hará
	// Prometheus con estas mismas llamadas.
	var total float64
	for _, g := range *got {
		if g.name != "event_escaped" || g.eventKind != "menu" {
			t.Fatalf("grupo inesperado: %+v", g)
		}
		if g.reason != "" {
			t.Fatalf("una clave ausente y una clave vacía deben publicarse las DOS con reason vacío; una salió con %q (%+v)", g.reason, g)
		}
		total += g.delta
	}
	if total != 2 {
		t.Fatalf("las dos filas deben contarse en la serie de reason vacío: total = %v, quiero 2 (got=%+v)", total, *got)
	}
}
