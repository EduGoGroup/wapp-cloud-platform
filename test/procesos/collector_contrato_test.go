//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics/flowlifecycle"
	"github.com/EduGoGroup/wapp-shared/logger"
)

// Las tres reglas del colector de flow_events (flowlifecycle.Collector) que P10 no puede mirar por
// la puerta del servidor (p10_plataforma_collector_test.go lleva las otras, por /metrics):
//
//   - el cursor arranca en max(id): lo que ya estaba en flow_events cuando el colector gana el
//     liderazgo no se cuenta nunca. Pide filas ANTES de que el colector exista, y el arnés crea la
//     base y lanza el servidor en un solo paso;
//   - con dos réplicas sobre la misma base solo la líder cuenta, y la que pierde ni cuenta ni
//     registra un error. Pide dos colectores sobre una base, y el arnés prohíbe dos servidores
//     sobre la misma (reglas.md §2);
//   - Run suelta el advisory lock al cancelarse su contexto. Por la puerta no se distingue: al morir
//     el proceso, Postgres suelta los locks de sesión con defer o sin él.
//
// Aquí no hay servidor: cada «réplica» es un flowlifecycle.NewCollector con su Run y su propio
// *sql.DB (su propio pool, como en dos procesos), sobre una base clonada del arnés.
//
// 🔴 NO ES UNA SUITE DE CONTRATO AL USO, Y SE DICE: el colector no se reconstruye, así que no hay
// puerto ni …helpertest. El fichero se llama *_contrato_test.go porque es la única forma de que el
// candado ProcessImports (R9.4.d, regla 3c) le deje importar internal/platform/metrics/flowlifecycle,
// que es lo único que importa de internal/. De ese paquete usa NewCollector y Run: lo que se prueba.
//
// 🔴 SOLO LA API PÚBLICA, Y POR ESO TARDA. Run da una vuelta al arrancar y otra cada 15 s, y ese
// intervalo no se puede cambiar desde fuera: cada «la fila nueva se cuenta» espera a la vuelta
// siguiente, sondeando con el tope de P10 (p10PollTimeout). Lo que el colector hace por dentro se
// mira en Postgres, que es donde lo vería otra réplica: pg_locks dice quién sostiene el lock y
// pg_stat_activity, cuál fue la última consulta de cada sesión de la base.

const (
	// collectorStopTimeout acota lo que Run puede tardar en retornar tras cancelarse su contexto.
	collectorStopTimeout = 15 * time.Second

	// collectorLockHolders cuenta las sesiones de ESTA base con el advisory lock del colector
	// concedido. La clave es p10CollectorLockKey: el contrato entre réplicas.
	collectorLockHolders = `SELECT count(*) FROM pg_locks l
		WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
		  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND ((l.classid::bigint << 32) | l.objid::bigint) = $1`

	// collectorIdleSessions cuenta las sesiones de ESTA base, ociosas, cuya última consulta casa con
	// el patrón $1 y empezó después del instante $2. El patrón viaja como parámetro para que el
	// texto de esta consulta no case consigo mismo en otra sesión del pool del test.
	collectorIdleSessions = `SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND pid <> pg_backend_pid() AND state = 'idle'
		  AND query LIKE $1 AND query_start > $2::timestamptz`

	// collectorCountQuery casa con la consulta agregada de una vuelta del colector: una sesión
	// ociosa con ella dice que esa vuelta ya leyó flow_events (y, antes, fijó su cursor).
	collectorCountQuery = `%FROM public.flow_events%GROUP BY 1, 2, 3%`
	// collectorLockQuery casa con el intento de ganar el liderazgo, que una réplica que no lidera
	// repite en cada vuelta.
	collectorLockQuery = `%pg_try_advisory_lock%`
	// collectorEpoch es «desde siempre» para collectorIdleSessions.
	collectorEpoch = "epoch"

	// collectorTenant es la empresa de las filas sembradas. flow_events la guarda como texto opaco
	// y el colector no la mira: el contador es de plataforma.
	collectorTenant = "empresa-opaca-colector"
)

// collectorCases cuenta las bases pedidas en esta corrida: cada test necesita la suya, porque el
// lock del colector es por base y su consulta lee flow_events ENTERA.
var collectorCases atomic.Int64

// newCollectorBase clona una base para un caso. La plantilla llega migrada y con flow_events vacía.
func newCollectorBase(t *testing.T) baseClonada {
	t.Helper()
	return nuevaBase(t, fmt.Sprintf("collector_contrato_%02d", collectorCases.Add(1)))
}

// collectorTally es lo que una réplica publicó por su callback, sumado por serie (p10SeriesKey):
// lo mismo que haría Prometheus con esas llamadas. El callback corre en la goroutine de Run.
type collectorTally struct {
	mu     sync.Mutex
	series map[string]float64
}

func (c *collectorTally) add(name, eventKind, reason string, delta float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.series == nil {
		c.series = map[string]float64{}
	}
	c.series[p10SeriesKey(name, eventKind, reason)] += delta
}

// snapshot devuelve una copia de lo publicado hasta ahora; vacía (no nil) si no se publicó nada.
func (c *collectorTally) snapshot() map[string]float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]float64{}
	maps.Copy(out, c.series)
	return out
}

// collectorLog es el logger de una réplica: guarda los mensajes de Error y tira lo demás.
type collectorLog struct {
	mu     sync.Mutex
	errors []string
}

func (l *collectorLog) Debug(string, ...any) {}
func (l *collectorLog) Info(string, ...any)  {}
func (l *collectorLog) Warn(string, ...any)  {}
func (l *collectorLog) Error(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, msg)
}
func (l *collectorLog) With(...any) logger.Logger { return l }

// logged devuelve una copia de los mensajes de Error registrados hasta ahora.
func (l *collectorLog) logged() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.errors...)
}

// collectorReplica es una «réplica del servidor» reducida a su colector: su pool, lo que publicó,
// lo que registró como error y cómo pararla.
type collectorReplica struct {
	tally *collectorTally
	log   *collectorLog
	stop  func()
}

// startCollectorReplica abre un pool propio sobre la base y lanza Run en una goroutine. stop cancela
// el contexto y espera a que Run retorne (falla con t.Errorf si no lo hace en collectorStopTimeout);
// es idempotente y además queda en t.Cleanup, ANTES del cierre del pool.
func startCollectorReplica(t *testing.T, base baseClonada) *collectorReplica {
	t.Helper()
	db := base.Abrir(t)
	r := &collectorReplica{tally: &collectorTally{}, log: &collectorLog{}}
	c := flowlifecycle.NewCollector(db, r.tally.add, r.log)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	var once sync.Once
	r.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(collectorStopTimeout):
				t.Errorf("Run no retornó en %s tras cancelar su contexto", collectorStopTimeout)
			}
		})
	}
	t.Cleanup(r.stop)
	return r
}

// collectorEventually sondea cond cada p10PollEvery hasta que devuelve true o vence
// p10PollTimeout, y dice cuál de las dos. NO falla: quien llama decide si el fallo es fatal o si
// las afirmaciones de después lo explican mejor.
func collectorEventually(cond func() bool) bool {
	deadline := time.Now().Add(p10PollTimeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(p10PollEvery)
	}
}

// collectorNow es el reloj de Postgres, como texto: el instante con el que se compara query_start.
func collectorNow(t *testing.T, db *sql.DB) string {
	t.Helper()
	return consultaTexto(t, db, `SELECT clock_timestamp()::text`)
}

// waitCollectorLeads espera a que UNA sesión de la base sostenga el lock del colector y a que una
// vuelta suya haya leído flow_events: desde ahí su cursor está fijado y toda fila nueva es
// posterior a él. Falla (t.Fatalf) si no llega a darse.
func waitCollectorLeads(t *testing.T, db *sql.DB) {
	t.Helper()
	var holders, counted int
	if !collectorEventually(func() bool {
		holders = consultaEntero(t, db, collectorLockHolders, p10CollectorLockKey)
		counted = consultaEntero(t, db, collectorIdleSessions, collectorCountQuery, collectorEpoch)
		return holders == 1 && counted >= 1
	}) {
		t.Fatalf("pasaron %s sin que un colector liderara y diera su primera vuelta: %d sesiones con el lock "+
			"(quería 1) y %d con la consulta de la vuelta ya hecha (quería al menos 1)", p10PollTimeout, holders, counted)
	}
}

// waitCollectorSeries espera a que lo publicado por la réplica sea EXACTAMENTE want. Falla
// (t.Fatalf) con la diferencia si no llega a serlo.
func waitCollectorSeries(t *testing.T, who string, r *collectorReplica, want map[string]float64) {
	t.Helper()
	var got map[string]float64
	if !collectorEventually(func() bool {
		got = r.tally.snapshot()
		return maps.Equal(got, want)
	}) {
		t.Fatalf("pasaron %s sin que %s publicara exactamente lo esperado: %s", p10PollTimeout, who, p10Diff(got, want))
	}
}

// TestCollectorContract_StartsAtMaxID_HistoryIsNotCounted: el cursor arranca en max(id). Las filas
// que ya estaban en flow_events cuando el colector ganó el liderazgo no se publican —ni en su
// primera vuelta ni después—, y la que llega más tarde sí, sola.
//
// Sin esa regla cada despliegue volvería a contar el histórico entero: un salto en el contador
// justo después de cada release, indistinguible de un pico de tráfico.
func TestCollectorContract_StartsAtMaxID_HistoryIsNotCounted(t *testing.T) {
	t.Parallel()
	base := newCollectorBase(t)
	db := base.Abrir(t)

	// El histórico: ya estaba en la tabla antes de que el colector existiera.
	p10InsertFlowEvents(t, db, collectorTenant, []p10FlowEvent{
		{"event", "event_started", `{"kind":"cart"}`},
		{"event", "event_started", `{"kind":"cart"}`},
	})

	r := startCollectorReplica(t, base)
	waitCollectorLeads(t, db)
	if got := r.tally.snapshot(); len(got) != 0 {
		t.Errorf("la primera vuelta publicó el histórico anterior al arranque: %v (el cursor tenía que arrancar en max(id))", got)
	}

	// Lo nuevo, posterior al cursor, sale en la vuelta siguiente; y sale SOLO eso: si el histórico
	// se hubiera contado en cualquier vuelta, lo publicado nunca sería exactamente esta serie.
	p10InsertFlowEvents(t, db, collectorTenant, []p10FlowEvent{{"event", "event_closed", `{"kind":"cart"}`}})
	waitCollectorSeries(t, "el colector", r, map[string]float64{p10SeriesKey("event_closed", "cart", ""): 1})

	if errs := r.log.logged(); len(errs) != 0 {
		t.Errorf("el colector registró errores: %q", errs)
	}
}

// TestCollectorContract_TwoReplicas_OnlyTheLeaderCounts: dos colectores sobre la misma base, cada
// uno con su pool, son dos réplicas del servidor. Solo una sostiene el lock; la fila nueva la
// publica esa y solo esa, una vez; y la que pierde vuelve a disputar el lock en su vuelta, no
// publica nada y NO registra ningún error: en un despliegue de N réplicas, perder es lo normal de
// N-1 de ellas en cada vuelta.
//
// Sin esa regla el contador, que no lleva etiqueta de réplica, miente por un factor N.
func TestCollectorContract_TwoReplicas_OnlyTheLeaderCounts(t *testing.T) {
	t.Parallel()
	base := newCollectorBase(t)
	db := base.Abrir(t)

	leader := startCollectorReplica(t, base)
	waitCollectorLeads(t, db)

	// La segunda réplica arranca con la primera ya líder: su primera vuelta pierde la carrera.
	loser := startCollectorReplica(t, base)
	var disputes int
	if !collectorEventually(func() bool {
		// Dos sesiones cuya última consulta es el intento: la de la líder, que sostiene el lock, y
		// la que la segunda réplica devolvió a su pool tras perder.
		disputes = consultaEntero(t, db, collectorIdleSessions, collectorLockQuery, collectorEpoch)
		return disputes >= 2
	}) {
		t.Errorf("la segunda réplica no llegó a disputar el lock en su primera vuelta: %d sesiones lo han pedido, quería 2", disputes)
	}
	if n := consultaEntero(t, db, collectorLockHolders, p10CollectorLockKey); n != 1 {
		t.Errorf("con dos réplicas hay %d sesiones con el lock del colector concedido, quería 1", n)
	}

	p10InsertFlowEvents(t, db, collectorTenant, []p10FlowEvent{{"event", "event_started", `{"kind":"cart"}`}})
	inserted := collectorNow(t, db)
	want := map[string]float64{p10SeriesKey("event_started", "cart", ""): 1}
	waitCollectorSeries(t, "la réplica líder", leader, want)

	// La que perdió también tiene que haber dado una vuelta con la fila ya en la tabla: sin eso,
	// «no contó» solo diría que aún no le había tocado.
	if !collectorEventually(func() bool {
		return consultaEntero(t, db, collectorIdleSessions, collectorLockQuery, inserted) >= 1
	}) {
		t.Errorf("pasaron %s sin que la réplica que no lidera volviera a disputar el lock tras la fila nueva: "+
			"quien pierde lo reintenta en cada vuelta", p10PollTimeout)
	}

	if got := loser.tally.snapshot(); len(got) != 0 {
		t.Errorf("la réplica que no lidera publicó %v: cada fila la cuenta EXACTAMENTE una réplica, y aquí van dos", got)
	}
	if got := leader.tally.snapshot(); !maps.Equal(got, want) {
		t.Errorf("la réplica líder publicó otra cosa que la fila nueva una vez: %s", p10Diff(got, want))
	}
	// Antes de parar a nadie: cancelar el contexto en mitad de una vuelta sí puede registrar un error.
	if errs := loser.log.logged(); len(errs) != 0 {
		t.Errorf("perder la carrera del lock tiene que ser silencioso, y la réplica que no lidera registró %d errores: %q", len(errs), errs)
	}
	if errs := leader.log.logged(); len(errs) != 0 {
		t.Errorf("la réplica líder registró errores: %q", errs)
	}
	if n := consultaEntero(t, db, collectorLockHolders, p10CollectorLockKey); n != 1 {
		t.Errorf("tras dos vueltas hay %d sesiones con el lock del colector concedido, quería 1", n)
	}
}

// TestCollectorContract_RunReleasesTheLockOnCancel: Run suelta el advisory lock al cancelarse su
// contexto, con el pool de esa réplica TODAVÍA abierto (cerrarlo mataría la sesión y Postgres
// soltaría el lock por su cuenta: no probaría nada). Después, otro colector lo gana.
//
// Sin esa regla un colector apagado deja el lock tomado hasta que Postgres recicle la sesión, y
// mientras tanto nadie cuenta en todo el clúster.
func TestCollectorContract_RunReleasesTheLockOnCancel(t *testing.T) {
	t.Parallel()
	base := newCollectorBase(t)
	db := base.Abrir(t)

	// Se espera a que lidere ANTES de cancelar: con el contexto cancelado de entrada no llegaría a
	// pedir el lock y no habría nada que soltar.
	first := startCollectorReplica(t, base)
	waitCollectorLeads(t, db)

	first.stop()
	if n := consultaEntero(t, db, collectorLockHolders, p10CollectorLockKey); n != 0 {
		t.Fatalf("Run retornó y el lock del colector sigue concedido a %d sesiones, quería 0: fuga de lock", n)
	}

	// El sucesor lo gana en su primera vuelta, sin esperar a nadie.
	startCollectorReplica(t, base)
	var holders int
	if !collectorEventually(func() bool {
		holders = consultaEntero(t, db, collectorLockHolders, p10CollectorLockKey)
		return holders == 1
	}) {
		t.Errorf("pasaron %s sin que otro colector ganara el lock que soltó el primero: %d sesiones lo sostienen, quería 1",
			p10PollTimeout, holders)
	}
}
