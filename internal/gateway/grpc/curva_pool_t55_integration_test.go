package gatewaygrpc_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// ============================================================================
// T5.5 · LA CURVA DEL POOL BAJO CARGA (Plan 050 · Ola 5)
//
// QUÉ RESPONDE ESTE FICHERO, Y POR QUÉ EXISTE
// La Ola 1 quitó el head-of-line del bucle Recv y el cuello se MUDÓ al pool de
// Postgres (DEUDA-050.2). T4.6 tiene que decidir si se mueve MaxOpenConns (hoy
// 25, postgres.DefaultMaxOpenConns) y está bloqueada A PROPÓSITO hasta que
// alguien levante la curva —ADR-0040 §Decisión.8: primero se mide, después se
// mueve—. Esto es esa curva, y su pregunta literal es UNA:
//
//	¿alguna vez WaitCount > 0? Y si sí, ¿a partir de qué pool y qué concurrencia?
//
// Una salida perfectamente legítima de T4.6 es «no mover nada». Este test no
// busca confirmar que hay un problema: barre la matriz y publica lo que salga.
//
// QUÉ MIDE, EXACTAMENTE
// La consulta REAL de producción —lease.PostgresRepository.TenantRevoked, la
// misma que muere en el log de renewLease— contra Postgres REAL, con N escritores
// concurrentes soltados a la vez tras una barrera, sobre un pool con el tope de la
// celda. De cada celda salen los seis números de T4.3 (metrics.RegisterDBStats)
// leídos de sql.DBStats: WaitCount, WaitDuration, InUse, Idle, OpenConnections y
// MaxOpenConnections; más el cociente WaitDuration/WaitCount, que es la ESPERA
// MEDIA y es lo que de verdad decide (10.000 esperas de 3 µs no son un problema;
// 40 de 200 ms sí).
//
// POR QUÉ EL EJE «ESCRITORES» Y NO «SEMÁFORO DE ENTRANTES»
// 🔴 El enunciado de T5.5 pide barrer «el semáforo de entrantes en otros tantos».
// VERIFICADO CONTRA EL CÓDIGO: ese semáforo (defaultMaxConcurrentIncoming = 64,
// internal/flujos/runtime/runtime_engine.go:27) acota SOLO Runtime.OnIncoming, y
// OnIncoming lo cablea bootstrap (`gw.OnIncoming = flowRuntime.OnIncoming`,
// bootstrap.go:364) para los IncomingMessage. Ni el heartbeat ni el lease pasan
// por ahí: el arnés de carga de este paquete construye el Server con
// WithLease + WithFleet y NUNCA asigna OnIncoming, así que el semáforo no está
// en el camino que se mide. Lo que sí determina cuántos escritores concurrentes
// ve el pool es el CARRIL POR SESIÓN de la Ola 1 (worklane.go): una goroutine
// propia por sesión viva. Por eso el eje de esta matriz es el número de
// escritores concurrentes, que es el semáforo REAL de este camino; el 64 aparece
// en la matriz porque es el valor de las DOS palancas de 64 que existen
// (Flow.MaxConcurrentIncoming y GatewayWorkQueue), no porque el arnés las cruce.
//
// POR QUÉ LAS ASERCIONES SON POBRES A PROPÓSITO
// Esto es un INSTRUMENTO DE MEDICIÓN y corre en cada gate. Un instrumento que se
// pone rojo porque hoy la máquina va lenta no vale nada. Solo se afirma lo que es
// cierto POR CONSTRUCCIÓN y no por velocidad de reloj:
//
//	(a) pool < escritores en el caso patológico (2 conexiones, 25 y 64
//	    demandantes × 20 consultas cada uno) ⇒ WaitCount > 0. Es aritmética.
//	(b) pool >= escritores ⇒ WaitCount == 0. También es aritmética, y por el lado
//	    contrario: database/sql solo encola cuando numOpen ya llegó al tope, y con
//	    menos demandantes que conexiones ese tope es inalcanzable. Esta mitad es
//	    la DISCRIMINANCIA: si alguien rompiera la medición, (a) podría quedarse
//	    verde por accidente, pero (b) no.
//
// NO se afirma ningún umbral de tiempo. Todo lo demás va por t.Logf.
//
// 🔴 INV-050.6: aquí no se mueve ningún timeout de producción, ni ningún default
// del pool. Los topes de cada celda son DEL TEST y se abren a mano.
// ============================================================================

var (
	// poolsT55 son los topes de MaxOpenConns que se barren. El 25 es
	// IMPRESCINDIBLE: es postgres.DefaultMaxOpenConns, el número que T4.6 tiene
	// que decidir si mueve. El 2 es el caso patológico ya conocido (el eslabón 1
	// de DEUDA-050.2) y sirve de ancla: si esa fila no esperase, la medición
	// entera estaría rota. El 64 cierra por arriba con el valor de las dos
	// palancas de 64 del sistema.
	poolsT55 = []int{2, 10, 25, 50, 64}
	// escritoresT55 son los demandantes SIMULTÁNEOS. Modelan carriles de la FLOTA
	// (una goroutine por sesión viva, worklane.go), no sesiones de un Edge: 100
	// Edges × 2 sesiones son 200 escritores contra el MISMO pool.
	escritoresT55 = []int{8, 25, 64}
)

const (
	// consultasPorEscritorT55 hace que la contención no dependa de que las
	// goroutines coincidan en el mismo instante: cada una repite la consulta 20
	// veces, así que la celda 2×64 son 1.280 adquisiciones de conexión contra un
	// tope de 2. La espera deja de ser probable y pasa a ser aritmética.
	//
	// ⚠️ Es la palanca de COSTE de este test, que corre en cada gate. Si la matriz
	// se pasara de presupuesto, se baja ESTE número, nunca el número de celdas:
	// una curva con menos puntos deja de ser una curva.
	consultasPorEscritorT55 = 20
	// holguraT55 es el presupuesto de cada consulta del martilleo. Amplio a
	// propósito: lo que se mide es la ESPERA (WaitCount/WaitDuration), no el
	// vencimiento. Si algo vence aquí es un fallo de entorno y así se lee.
	holguraT55 = 30 * time.Second
	// muestreoT55 es el periodo del muestreador de picos. InUse e Idle son
	// instantáneos: leídos al terminar la celda valdrían 0 y «todo ocioso», que es
	// verdad y no dice nada. El pico se caza muestreando durante la celda.
	muestreoT55 = time.Millisecond
)

// celdaT55 es una fila de la curva: un par (pool, escritores) con lo que el pool
// contó mientras se le martilleaba.
type celdaT55 struct {
	pool       int
	escritores int
	stats      sql.DBStats
	// inUsePico y openPico son los máximos observados por el muestreador durante
	// la celda, no el valor final.
	inUsePico int
	openPico  int
	dur       time.Duration
}

// esperaMedia devuelve WaitDuration/WaitCount: cuánto costó CADA espera. Es el
// número que decide, y el que no se puede leer de ninguno de los dos contadores
// por separado.
func (c celdaT55) esperaMedia() time.Duration {
	if c.stats.WaitCount == 0 {
		return 0
	}
	return time.Duration(int64(c.stats.WaitDuration) / c.stats.WaitCount)
}

// TestIntegration_T55_CurvaDelPoolBajoCarga barre la matriz pool × escritores y
// publica la tabla que T4.6 necesita para decidir. Ver la cabecera del fichero.
func TestIntegration_T55_CurvaDelPoolBajoCarga(t *testing.T) {
	// openTestDB aplica el gating por WAPP_TEST_DB_DSN / WAPP_TEST_REQUIRE_DB y
	// deja el esquema migrado. Su pool es ANCHO (90) a propósito; aquí solo se usa
	// para sembrar. Lo que se mide corre siempre sobre los pools de cada celda.
	admin := openTestDB(t)
	tenantID := sembrarTenantDeuda(t, admin)

	filas := make([]celdaT55, 0, len(poolsT55)*len(escritoresT55))
	for _, pool := range poolsT55 {
		for _, escritores := range escritoresT55 {
			filas = append(filas, medirCeldaT55(t, pool, escritores, tenantID))
		}
	}

	publicarCurvaT55(t, filas)
	publicarCodoT55(t, filas)
	comprobarCurvaT55(t, filas)
}

// medirCeldaT55 abre un pool con el tope de la celda, lo martillea con sus
// escritores y devuelve la fila. CIERRA el pool al terminar y no espera al
// t.Cleanup: la matriz son 15 celdas y la suma de sus topes son 453 conexiones
// contra un Postgres cuyo max_connections típico es 100. Dejarlas abiertas
// tumbaría la corrida por «too many clients» a mitad de tabla.
func medirCeldaT55(t *testing.T, pool, escritores int, tenantID string) celdaT55 {
	t.Helper()
	db := abrirPoolDeuda(t, pool)
	repo := lease.NewPostgresRepository(db)

	m := arrancarMuestreadorT55(db)
	inicio := time.Now()
	martillearCurvaT55(t, repo, pool, escritores, tenantID)
	dur := time.Since(inicio)
	inUsePico, openPico := m.parar()

	fila := celdaT55{
		pool:       pool,
		escritores: escritores,
		stats:      db.Stats(),
		inUsePico:  inUsePico,
		openPico:   openPico,
		dur:        dur,
	}
	// Cierre EXPLÍCITO, aunque abrirPoolDeuda ya registrase su t.Cleanup: aquel
	// corre al final del test entero y para entonces ya habríamos agotado el
	// servidor. sql.DB.Close es idempotente, así que el cleanup posterior es un
	// no-op.
	if cerr := db.Close(); cerr != nil {
		t.Logf("cerrando el pool de la celda (pool=%d, escritores=%d): %v", pool, escritores, cerr)
	}
	return fila
}

// martillearCurvaT55 suelta `escritores` goroutines a la vez —barrera de salida,
// para que arrancar las goroutines secuencialmente no las escalone y el pool
// nunca llegue a tensarse— contra la consulta REAL de producción.
func martillearCurvaT55(t *testing.T, repo *lease.PostgresRepository, pool, escritores int, tenantID string) {
	t.Helper()
	salida := make(chan struct{})
	fallos := make(chan error, escritores)
	var wg sync.WaitGroup
	for i := 0; i < escritores; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-salida
			for j := 0; j < consultasPorEscritorT55; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), holguraT55)
				_, err := repo.TenantRevoked(ctx, tenantID)
				cancel()
				if err != nil {
					fallos <- err
					return
				}
			}
		}()
	}
	close(salida)
	wg.Wait()
	close(fallos)
	for err := range fallos {
		t.Fatalf("celda pool=%d escritores=%d: la consulta falló y no debía (el presupuesto es de %v): %v",
			pool, escritores, holguraT55, err)
	}
}

// muestreadorT55 caza los PICOS de InUse y OpenConnections durante una celda.
// Existe porque esos dos son instantáneos: leídos al final de la celda valen 0 y
// «todas ociosas», que es cierto y no informa de nada.
type muestreadorT55 struct {
	fin  chan struct{}
	hech chan [2]int
}

// arrancarMuestreadorT55 lanza el muestreo. Lee db.Stats() cada muestreoT55;
// db.Stats() es una copia de struct tomada bajo el mutex del pool, barata y segura
// de leer en caliente.
func arrancarMuestreadorT55(db *sql.DB) *muestreadorT55 {
	m := &muestreadorT55{fin: make(chan struct{}), hech: make(chan [2]int, 1)}
	go func() {
		tick := time.NewTicker(muestreoT55)
		defer tick.Stop()
		var inUse, open int
		for {
			select {
			case <-m.fin:
				m.hech <- [2]int{inUse, open}
				return
			case <-tick.C:
				s := db.Stats()
				if s.InUse > inUse {
					inUse = s.InUse
				}
				if s.OpenConnections > open {
					open = s.OpenConnections
				}
			}
		}
	}()
	return m
}

// parar detiene el muestreo y devuelve (picoInUse, picoOpen).
func (m *muestreadorT55) parar() (int, int) {
	close(m.fin)
	v := <-m.hech
	return v[0], v[1]
}

// publicarCurvaT55 imprime la tabla. Va en MARKDOWN a propósito: la salida de
// este test es el entregable de T5.5 y se pega tal cual en el plan, así que se
// formatea para copiar, no para leer en la terminal.
func publicarCurvaT55(t *testing.T, filas []celdaT55) {
	t.Helper()
	if len(filas) == 0 {
		t.Errorf("la matriz no produjo ninguna celda: NO se publica tabla (mismo criterio que publicarArnesT55)")
		return
	}
	t.Logf("========= T5.5 · curva del pool bajo carga (Plan 050 · Ola 5) =========")
	t.Logf("consulta medida: lease.PostgresRepository.TenantRevoked (la REAL de producción, la que muere en renewLease)")
	t.Logf("cada celda: los escritores de su fila, soltados A LA VEZ tras una barrera, × %d consultas cada uno",
		consultasPorEscritorT55)
	t.Logf("default de producción hoy: postgres.DefaultMaxOpenConns=%d · palancas de 64 del sistema: "+
		"Flow.MaxConcurrentIncoming y GatewayWorkQueue (ninguna de las dos está en este camino, ver cabecera)",
		postgres.DefaultMaxOpenConns)
	t.Logf("")
	t.Logf("| pool | escritores | consultas | WaitCount | WaitDuration | espera media | InUse pico | Open pico | Idle fin | Open fin | MaxOpen | duración |")
	t.Logf("|-----:|-----------:|----------:|----------:|-------------:|-------------:|-----------:|----------:|---------:|---------:|--------:|---------:|")
	for _, f := range filas {
		t.Logf("| %d | %d | %d | %d | %v | %v | %d | %d | %d | %d | %d | %v |",
			f.pool, f.escritores, f.escritores*consultasPorEscritorT55,
			f.stats.WaitCount,
			f.stats.WaitDuration.Round(time.Microsecond),
			f.esperaMedia().Round(time.Microsecond),
			f.inUsePico, f.openPico,
			f.stats.Idle, f.stats.OpenConnections, f.stats.MaxOpenConnections,
			f.dur.Round(time.Millisecond))
	}
	t.Logf("")
}

// publicarCodoT55 responde la pregunta que T5.5 pide con todas las letras: para
// cada nivel de concurrencia, el PRIMER tamaño de pool en el que la espera
// desaparece. Ese es el codo, y es la única entrada que T4.6 necesita.
func publicarCodoT55(t *testing.T, filas []celdaT55) {
	t.Helper()
	var totalEsperas int64
	for _, f := range filas {
		totalEsperas += f.stats.WaitCount
	}
	t.Logf("¿alguna vez WaitCount > 0? %s (suma de las %d celdas: %d esperas)",
		siNo(totalEsperas > 0), len(filas), totalEsperas)

	t.Logf("| escritores | primer pool SIN espera (codo) | mayor pool CON espera | esperas en pool=25 | espera media en pool=25 |")
	t.Logf("|-----------:|------------------------------:|----------------------:|-------------------:|------------------------:|")
	for _, esc := range escritoresT55 {
		t.Logf("| %d | %s |", esc, resumenCodoT55(filas, esc))
	}
}

// resumenCodoT55 arma, para un nivel de concurrencia, las cuatro columnas del
// codo. Está aparte de publicarCodoT55 para que ninguna de las dos pase de
// gocyclo 15 (y porque t.Run/closures inline NO bajan la complejidad: hay que
// extraer funciones NOMBRADAS).
func resumenCodoT55(filas []celdaT55, esc int) string {
	codo := -1
	mayorConEspera := -1
	esperas25 := int64(-1)
	media25 := time.Duration(0)
	for _, f := range filas {
		if f.escritores != esc {
			continue
		}
		if f.stats.WaitCount == 0 && (codo == -1 || f.pool < codo) {
			codo = f.pool
		}
		if f.stats.WaitCount > 0 && f.pool > mayorConEspera {
			mayorConEspera = f.pool
		}
		if f.pool == postgres.DefaultMaxOpenConns {
			esperas25 = f.stats.WaitCount
			media25 = f.esperaMedia()
		}
	}
	return fmt.Sprintf("%s | %s | %s | %v",
		numOrNA(codo), numOrNA(mayorConEspera), num64OrNA(esperas25), media25.Round(time.Microsecond))
}

func siNo(b bool) string {
	if b {
		return "SÍ"
	}
	return "NO"
}

func numOrNA(n int) string {
	if n < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d", n)
}

func num64OrNA(n int64) string {
	if n < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d", n)
}

// comprobarCurvaT55 es la parte que puede ponerse ROJA, y es deliberadamente
// pobre: solo aritmética, cero relojes. Ver la cabecera del fichero.
func comprobarCurvaT55(t *testing.T, filas []celdaT55) {
	t.Helper()
	for _, f := range filas {
		comprobarCeldaT55(t, f)
	}
}

// comprobarCeldaT55 aplica a una celda las dos únicas afirmaciones ciertas por
// construcción.
func comprobarCeldaT55(t *testing.T, f celdaT55) {
	t.Helper()
	switch {
	case f.pool >= f.escritores:
		// DISCRIMINANCIA. Con menos demandantes que conexiones el tope es
		// inalcanzable: database/sql solo encola cuando numOpen ya llegó a
		// MaxOpenConns. Si esta se cayera, la columna WaitCount estaría contando
		// otra cosa y la curva entera no significaría nada.
		if f.stats.WaitCount != 0 {
			t.Errorf("pool=%d escritores=%d: WaitCount=%d con MÁS conexiones que demandantes. "+
				"Nadie podía esperar: o la medición no mide el pool, o el pool no respeta su tope",
				f.pool, f.escritores, f.stats.WaitCount)
		}
	case f.pool == 2 && f.escritores >= 25:
		// El caso patológico. 2 conexiones contra 25 o 64 demandantes × 20
		// consultas: la espera es aritmética, no probabilística.
		if f.stats.WaitCount == 0 {
			t.Errorf("pool=%d escritores=%d: WaitCount=0 con %d demandantes contra %d conexiones. "+
				"Es imposible sin que alguien espere: el arnés dejó de martillear",
				f.pool, f.escritores, f.escritores, f.pool)
		}
	default:
		// Todo lo demás es DATO, no aserción: que pool=25 espere con 64
		// escritores depende de lo rápida que sea la máquina y de cuánto se
		// solapen las goroutines. Afirmarlo convertiría el instrumento en una
		// fuente de rojos falsos, que es exactamente lo que T5.5 no puede
		// permitirse corriendo en cada gate.
		t.Logf("pool=%d escritores=%d: WaitCount=%d (sin aserción: depende del solape, no de la aritmética)",
			f.pool, f.escritores, f.stats.WaitCount)
	}
}

// ============================================================================
// T5.5 · ENTREGABLE B — el pool bajo el arnés REAL
//
// La curva de arriba es SINTÉTICA y, a propósito, el PEOR CASO: N goroutines con
// ciclo de trabajo del 100 % que no hacen otra cosa que pedir conexión. Ahí el
// codo cae exactamente en pool == escritores, y eso no es un hallazgo sobre la
// plataforma: es la definición de database/sql.
//
// Lo que T4.6 tiene que decidir no depende de ese peor caso, sino de cuántas
// conexiones pide DE VERDAD el camino de producción. Aquí se mide eso: el arnés
// de carga del gateway con 60 sesiones vivas sobre UN stream (sesionesT52,
// ADR-0008), lease sobre POSTGRES —tres consultas por latido: TenantRevoked, Get
// y Upsert— y fleet sobre Postgres, con el CARRIL de la Ola 1 dando una goroutine
// por sesión. Cada ronda manda un heartbeat a cada una de las 60 sesiones, así
// que hay hasta 60 carriles pidiendo conexión a la vez y nada que coalescer
// (D-050.4 coalesce POR SESIÓN, y aquí cada sesión recibe uno solo por ronda).
//
// DOS DECISIONES DE MEDICIÓN, DICHAS EN VOZ ALTA:
//
//  1. SIN latencia inyectada al fleet (latenciaInicial = 0), al revés que T5.2/T5.3
//     que inyectan 20 ms por consulta. Esa latencia la mete el decorador
//     SlowRepository ANTES de delegar (fleettest/slowrepo.go), o sea FUERA de la
//     conexión: encenderla espaciaría los latidos y BAJARÍA la presión sobre el
//     pool. Medirlo sin ella es el caso más duro de los dos, que es el que
//     interesa cuando la pregunta es «¿cuántas conexiones hacen falta?».
//
//  2. El pool que se mide es SOLO el del lease y el fleet (configArnes.dbMedido).
//     Los sondeos del propio arnés —esperarUptime martilleando fleet_sessions—
//     siguen yendo por el pool ancho de openTestDB. Si compartieran pool, la
//     mitad de las conexiones «en uso» serían del instrumento y no del sistema.
//
// La fila de pool=90 es la OBSERVACIÓN SIN RESTRICCIÓN: con más conexiones que
// carriles, el pico de InUse es la demanda REAL del arnés, no lo que el tope le
// deja pedir. Es el número que decide, y no se puede leer de ninguna otra fila.
//
// 🔴 HALLAZGO COLATERAL DE T5.5, ANOTADO AQUÍ PORQUE CONTRADICE UN NÚMERO QUE
// ESTE PAQUETE TIENE ESCRITO. openTestDB (load_integration_test.go) justifica su
// pool de 90 con esto, medido el 2026-08-18: «con MaxOpenConns=25 el test falla 3
// de cada 6 corridas; con 90, 0 de 6», siendo «el test»
// TestIntegration_CargaAckBajoHeartbeatsEnVuelo (el gate de T5.3).
//
// El 2026-08-19, sobre la cabeza de feature/050-o5-cierre y en el mismo hardware
// (Apple M1 Pro, Postgres 16 en Docker, sin -race), esa flakiness NO SE
// REPRODUCE. Se probó de las dos maneras, 6 corridas cada una, y PASARON LAS 12:
//   - con el lease y el fleet sobre un pool de 25 y los sondeos del arnés aparte:
//     6/6 PASS, WaitCount ≈ 8.570 y WaitDuration ≈ 24 s por corrida, o sea una
//     espera media de ~2,8 ms contra un presupuesto de job de 5 s
//     (defaultWorkBudget, server.go) — el 0,06 % del presupuesto;
//   - con openTestDB entero bajado a 25, que es la condición EXACTA de aquella
//     medida (el instrumento compitiendo por las mismas conexiones): 6/6 PASS.
//
// No se toca el 90 de openTestDB: ese número es de T5.2/T5.3 y moverlo no es de
// T5.5 (T5.5 mide, T4.6 decide). Pero la única evidencia de campo que existía de
// que 25 se queda corto es ESA frase, y hoy no se sostiene. Quien la use para
// justificar un cambio de default está citando una medida caducada.
// ============================================================================

var (
	// poolsArnesT55 son los topes que se barren bajo el arnés real. El 25 es el
	// default de producción; el 90 es el del arnés de T5.2/T5.3 y hace de
	// observador SIN RESTRICCIÓN (más conexiones que carriles, así que su InUse
	// pico es la demanda real y no lo que el tope deja pedir); el 60 es
	// exactamente el número de carriles y por tanto el CODO esperado; el 10
	// comprueba, por el otro lado, que la medición se mueve cuando el tope aprieta
	// de verdad (discriminancia).
	poolsArnesT55 = []int{10, 25, 50, sesionesT52, 90}
)

const (
	// rondasArnesT55 son las rondas de medición POR VARIANTE. Cada ronda son 60
	// heartbeats (uno por sesión) con barrera de drenaje al final. Para el PICO de
	// conexiones bastaría una; son seis para que la columna de RITMO (latidos/s)
	// tenga con qué compararse entre variantes, que es lo que responde si subir el
	// tope hace el trabajo más rápido o solo cambia dónde se espera.
	rondasArnesT55 = 6
	// marcaBaseArnesT55 es el marcador de uptime desde el que arrancan las rondas.
	// registrarSesionesT52 ya gastó el 1.
	marcaBaseArnesT55 = 2
)

// filaArnesT55 es una variante del barrido bajo el arnés real.
type filaArnesT55 struct {
	pool int
	// Deltas de los contadores MONÓTONOS entre el final del registro de las
	// sesiones y el final de las rondas: lo que costó la CARGA, sin el arranque.
	waitCount    int64
	waitDuration time.Duration
	// Picos observados durante las rondas.
	inUsePico int
	openPico  int
	// idleFin y openFin son el estado del pool al terminar.
	idleFin, openFin int
	dur              time.Duration
}

// esperaMedia devuelve waitDuration/waitCount de la variante.
func (f filaArnesT55) esperaMedia() time.Duration {
	if f.waitCount == 0 {
		return 0
	}
	return time.Duration(int64(f.waitDuration) / f.waitCount)
}

// ritmo son los latidos procesados por segundo. Es la columna que impide leer la
// tabla al revés: WaitCount solo dice DÓNDE se espera, no si el sistema va más
// despacio. Si el ritmo no mejora al subir el tope, la espera del pool no era el
// cuello, solo era el sitio donde se veía.
func (f filaArnesT55) ritmo() float64 {
	if f.dur <= 0 {
		return 0
	}
	return float64(rondasArnesT55*sesionesT52) / f.dur.Seconds()
}

// TestIntegration_T55_PoolBajoElArnesReal mide sql.DBStats mientras corre el
// arnés de heartbeats del gateway con 60 sesiones vivas, barriendo MaxOpenConns.
// Ver la cabecera de la sección.
func TestIntegration_T55_PoolBajoElArnesReal(t *testing.T) {
	// 🔴 EL GATE DE BD VA AQUÍ, Y TIENE QUE IR ANTES DEL BUCLE. Sin esta línea el
	// primer subtest llamaba a abrirPoolDeuda con el entorno vacío y moría con
	// «WAPP_TEST_DB_DSN vacío después de openTestDB: no debería poder ocurrir»
	// —una guarda que SÍ puede ocurrir, porque su precondición («openTestDB ya
	// corrió y saltó») no se cumplía por este camino—. Y ocurría justo en el gate
	// principal: `make ci-docker` corre `go test -race ./...` SIN Postgres y sin
	// DSN (no puede levantarlo: sería Docker-in-Docker), donde todos los tests de
	// integración de este repo se saltan solos. Este no se saltaba: fallaba.
	//
	// Se usa openTestDB y no una comprobación propia de la variable a propósito:
	// cubre los DOS modos de no-BD con la semántica de la casa —sin DSN y con DSN
	// que no responde—, saltando salvo que WAPP_TEST_REQUIRE_DB esté puesto, en
	// cuyo caso falla. Esa distinción es la que impide que el gate de integración
	// se salte en silencio, y se conserva tal cual.
	openTestDB(t)

	filas := make([]filaArnesT55, 0, len(poolsArnesT55))
	for _, pool := range poolsArnesT55 {
		// Cada variante va en su PROPIO subtest, y no es cosmético: el arnés y el
		// pool medido registran sus t.Cleanup sobre el *testing.T que reciben, así
		// que con todas las variantes colgando del test madre las conexiones no se
		// devuelven hasta el final. Con MaxIdleConns = MaxOpenConns el pool RETIENE
		// lo que abrió, de modo que 10+25+50+90 se acumulan y el servidor corta con
		// «sorry, too many clients already» (max_connections=100 por defecto) a
		// mitad de tabla. Medido: así falla; con un subtest por variante, no.
		var fila filaArnesT55
		medida := t.Run(fmt.Sprintf("MaxOpenConns_%d", pool), func(t *testing.T) {
			fila = medirVarianteArnesT55(t, pool)
		})
		if !medida {
			// Una variante que murió NO aporta fila. Antes se añadía igual, y como
			// filaArnesT55 es un struct de valores, la tabla se publicaba con la
			// fila entera a CEROS: WaitCount=0, InUse pico=0, ritmo=0. Una tabla de
			// resultados con todo a cero se lee como «no hubo espera», que es la
			// conclusión CONTRARIA a la que dan esos datos, y es exactamente el
			// tipo de salida que alguien pega en un documento sin mirar.
			t.Logf("MaxOpenConns=%d: la variante FALLÓ; su fila NO entra en la tabla", pool)
			continue
		}
		filas = append(filas, fila)
	}
	publicarArnesT55(t, filas)
	comprobarArnesT55(t, filas)
}

// medirVarianteArnesT55 levanta un arnés con el pool pedido, registra las 60
// sesiones y corre las rondas midiendo el pool.
//
// Está fuera de un t.Run con closure inline a propósito: los FuncLit anidados se
// imputan a la función madre y no bajan gocyclo (min-complexity 15 en este repo).
func medirVarianteArnesT55(t *testing.T, pool int) filaArnesT55 {
	t.Helper()

	// El pool medido se abre ANTES del arnés y se le entrega. abrirPoolDeuda da por
	// hecho que hay DSN válido y muere si no lo hay, así que esta función SOLO es
	// legal detrás del openTestDB del test madre. Ver el comentario del gate allí:
	// invertir ese orden es el fallo que puso rojo `make ci-docker`.
	medido := abrirPoolDeuda(t, pool)
	h := newLoadHarness(t, configArnes{leasePostgres: true, dbMedido: medido})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("pool=%d · Connect: %v", pool, err)
	}
	edge := nuevoEdgeCliente(h, stream)
	edge.arrancarLector()

	sesiones := registrarSesionesT52(ctx, t, h, edge, sesionesT52)

	// Línea base DESPUÉS del registro: los contadores del pool son monótonos, así
	// que la carga se mide por diferencia y el arranque en frío no se cuela.
	base := medido.Stats()
	m := arrancarMuestreadorT55(medido)
	inicio := time.Now()
	correrRondasArnesT55(t, h, edge, sesiones)
	dur := time.Since(inicio)
	inUsePico, openPico := m.parar()
	fin := medido.Stats()

	if ferr := edge.fallo(); ferr != nil {
		t.Fatalf("pool=%d · el Edge del arnés falló durante la medición: %v", pool, ferr)
	}

	return filaArnesT55{
		pool:         pool,
		waitCount:    fin.WaitCount - base.WaitCount,
		waitDuration: fin.WaitDuration - base.WaitDuration,
		inUsePico:    inUsePico,
		openPico:     openPico,
		idleFin:      fin.Idle,
		openFin:      fin.OpenConnections,
		dur:          dur,
	}
}

// correrRondasArnesT55 manda, ronda tras ronda, UN heartbeat a cada sesión y
// espera la barrera de LeaseUpdate antes de la siguiente. La barrera no es un
// adorno: sin ella dos rondas se solaparían en la cola de una misma sesión, la
// coalescencia sustituiría el latido viejo y el recuento de LeaseUpdate dejaría
// de cuadrar (mismo razonamiento que muestraT52).
func correrRondasArnesT55(t *testing.T, h *loadHarness, edge *edgeCliente, sesiones []string) {
	t.Helper()
	esperados := int64(2 * len(sesiones)) // los que ya dejó el registro.
	for r := 0; r < rondasArnesT55; r++ {
		marca := int64(marcaBaseArnesT55 + r)
		for _, sid := range sesiones {
			if err := edge.enviar(heartbeatCarga(sid, marca)); err != nil {
				t.Fatalf("ronda %d · enviando heartbeat de %q: %v", r, sid, err)
			}
		}
		esperados += int64(len(sesiones))
		h.esperarLeases(t, esperados)
	}
}

// publicarArnesT55 imprime la tabla del entregable B, también en markdown.
func publicarArnesT55(t *testing.T, filas []filaArnesT55) {
	t.Helper()
	if len(filas) == 0 {
		t.Errorf("ninguna variante produjo medición: NO se publica tabla. " +
			"Una tabla vacía o a ceros se leería como «no hubo espera», que es la conclusión contraria")
		return
	}
	t.Logf("========= T5.5 · el pool bajo el ARNÉS REAL del gateway =========")
	t.Logf("%d sesiones vivas sobre UN stream (ADR-0008) · %d rondas × %d heartbeats · lease sobre POSTGRES "+
		"(3 consultas por latido) · fleet sobre Postgres · latencia inyectada al fleet: 0 (el caso más duro)",
		sesionesT52, rondasArnesT55, sesionesT52)
	t.Logf("carriles concurrentes posibles: %d (uno por sesión, worklane.go) · default de producción: MaxOpenConns=%d",
		sesionesT52, postgres.DefaultMaxOpenConns)
	t.Logf("")
	t.Logf("| MaxOpenConns | WaitCount | WaitDuration | espera media | InUse pico | Open pico | Idle fin | Open fin | duración | ritmo (latidos/s) |")
	t.Logf("|-------------:|----------:|-------------:|-------------:|-----------:|----------:|---------:|---------:|---------:|------------------:|")
	for _, f := range filas {
		t.Logf("| %d | %d | %v | %v | %d | %d | %d | %d | %v | %.0f |",
			f.pool, f.waitCount,
			f.waitDuration.Round(time.Microsecond),
			f.esperaMedia().Round(time.Microsecond),
			f.inUsePico, f.openPico, f.idleFin, f.openFin,
			f.dur.Round(time.Millisecond), f.ritmo())
	}
	t.Logf("")
	t.Logf("lectura, en dos pasos y en este orden:")
	t.Logf("  1. el InUse pico de la fila SIN restricción (pool=%d, más conexiones que carriles) es la DEMANDA REAL "+
		"del arnés; comparado con %d dice si el default se queda corto en CONEXIONES.",
		poolsArnesT55[len(poolsArnesT55)-1], postgres.DefaultMaxOpenConns)
	t.Logf("  2. la columna de RITMO dice si eso importa. Un WaitCount alto con el mismo ritmo significa que la espera " +
		"se ve en el pool pero el trabajo no va más lento: subir el tope movería la cola de sitio, no la quitaría.")
	t.Logf("⚠️ este arnés manda las %d sesiones EN RÁFAGA con barrera entre rondas, o sea todas a la vez. Es la "+
		"concurrencia MÁXIMA para ese N, no la media de una flota real, cuyos latidos llegan repartidos en el tiempo.",
		sesionesT52)
}

// comprobarArnesT55 afirma lo MÍNIMO, y por la misma razón que arriba: esto corre
// en cada gate y no puede ponerse rojo porque hoy la máquina vaya lenta.
func comprobarArnesT55(t *testing.T, filas []filaArnesT55) {
	t.Helper()
	for _, f := range filas {
		// Cierto por construcción: el pool no puede abrir más conexiones que su
		// tope. Si esta se cayera, el pool medido no sería el que se cree y toda
		// la tabla estaría mirando otro sitio.
		if f.openPico > f.pool {
			t.Errorf("pool=%d: se observaron %d conexiones abiertas, por encima del tope. "+
				"El pool medido NO es el que el arnés está usando", f.pool, f.openPico)
		}
		if f.inUsePico == 0 {
			t.Errorf("pool=%d: InUse pico = 0 en %d rondas de %d heartbeats con el lease sobre Postgres. "+
				"El arnés no está tocando la base por el pool medido: la tabla no significa nada",
				f.pool, rondasArnesT55, sesionesT52)
		}
		// Todo lo demás es DATO. En particular NO se afirma que con pool=25 haya
		// (ni que no haya) espera: eso es justo lo que T5.5 viene a averiguar, y
		// convertirlo en aserción congelaría hoy una respuesta que T4.6 todavía no
		// ha tomado.
		t.Logf("pool=%d: WaitCount=%d, espera media=%v, InUse pico=%d/%d",
			f.pool, f.waitCount, f.esperaMedia().Round(time.Microsecond), f.inUsePico, f.pool)
	}
}
