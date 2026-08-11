// Package flowlifecycle es el colector INCREMENTAL del outbox append-only
// public.flow_events hacia /metrics (Plan 043 · Ola 6 · T6.5, cierra MD-043.17).
//
// El criterio de la tarea (docs/plans/043-eventos-conversacionales/tasks.md
// §T6.5) exige un consumidor de PRODUCCIÓN, no un assert de test: "T6.2 ya lee
// la tabla desde un test y eso deja flow_events igual de write-only". El
// registro Prometheus del listener admin (internal/bootstrap/bootstrap.go,
// mux.Handle("/metrics", ...)) SÍ tiene un consumidor externo raspándolo ya en
// producción — es la diferencia entre "se puede leer" y "algo la lee".
//
// DESACOPLO (mismo patrón que receipts.Sink e integrations.Worker): este
// paquete NUNCA importa internal/platform/metrics ni ningún paquete de
// internal/flujos. onCount es un callback que bootstrap.go inyecta
// (mtx.FlowEventLifecycle) — el colector no sabe que existe Prometheus, y el
// motor de flujos no sabe que existe este colector.
//
// PROPIEDAD DE LA TAREA: vive bajo internal/platform/metrics/** (no bajo
// internal/flujos/, dueño físico de la tabla flow_events) porque T6.5 tiene la
// propiedad exclusiva de internal/publicapi/**, internal/platform/metrics/** e
// internal/bootstrap/** mientras OTRO agente edita internal/flujos/** a la vez
// (Ola 6 del Plan 043, dos agentes en paralelo). Es SQL de solo LECTURA contra
// una tabla PÚBLICA; no interpreta el ciclo de vida del evento ni compite con
// el dominio de flujos por ninguna escritura.
//
// ELECCIÓN DE LÍDER POR ADVISORY LOCK (T6.5, refutación multi-réplica del
// revisor, decisión de Jhoan 2026-08-11): el cursor de arriba es UN entero EN
// MEMORIA por proceso. Con más de una réplica del servidor —que es como este
// repo despliega, ver internal/integrations/worker.go:~176 y
// internal/integrations/postgres.go:~187, que documentan el MISMO problema
// para el worker de webhooks y por eso llevan claim+lease+SKIP LOCKED— cada
// instancia tenía su PROPIO cursor y publicaba el conteo de las MISMAS filas:
// medido, 2 réplicas → 6 donde había 3. Como wapp_flow_event_lifecycle_total
// no lleva label de tenant, la agregación natural (sum(rate(...))) no puede
// distinguir "dos réplicas contando lo mismo" de "el doble de tráfico real":
// miente por un factor N silencioso.
//
// La solución aquí NO es reconciliar cursores entre réplicas (eso exigiría
// persistir el cursor compartido en la propia BD, un rediseño que esta tarea
// no pide): es asegurar que SOLO UNA réplica colecte en cada momento, con
// pg_try_advisory_lock (Collector.becomeLeader/releaseLeadership, más abajo).
// Las demás pasan de largo esa vuelta sin error y sin log — ver el comentario
// de becomeLeader para el porqué de sostener el lock de SESIÓN mientras dure
// la conexión reservada, en vez de soltarlo y volver a pedirlo en cada vuelta.
//
// LO QUE ESTE CONTADOR SIGUE SIN SER (el mismo revisor midió estas dos
// propiedades; esta tarea NO las arregla — documentadas aquí para quien mire
// la métrica en seis meses sin este análisis a mano):
//
//  1. NO es reconciliable con flow_events. Lo escrito entre el apagado de
//     ESTE colector (el que hoy tenga el advisory lock) y el arranque del
//     siguiente que lo gane se pierde para siempre: initCursor salta al
//     max(id) ACTUAL, nunca reprocesa lo que quedó en medio. Sirve para
//     TENDENCIA (rate()/increase()), nunca para auditar cuántos eventos hubo
//     de verdad — para eso hay que leer flow_events directamente.
//  2. El cursor por `id` puede perder filas por ORDEN DE COMMIT, no por orden
//     de asignación de id. BIGSERIAL asigna el id ANTES del commit; una
//     transacción que tomó un id BAJO puede commitear DESPUÉS de que esta
//     vuelta ya vio (y avanzó el cursor sobre) un id más alto de otra
//     transacción más rápida. Esa fila de id bajo queda fuera de
//     `WHERE id > cursor` para siempre en cuanto el cursor la supera:
//     pérdida silenciosa y de tasa baja, en la ventana de microsegundos entre
//     "Postgres asignó el id" y "el commit es visible" (el INSERT de
//     flow_events es autocommit de un solo statement en este repo, así que la
//     ventana es mínima, pero no cero).
package flowlifecycle

import (
	"context"
	"database/sql"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// defaultPollInterval es la cadencia del ticker. Sin env (mismo criterio que
// integrations.WorkerConfig.BatchSize/ClaimLease: la tarea no pide uno, y quien
// lo necesite lo fija en código, ver NewCollector). 15s deja el contador
// razonablemente al día sin convertir el outbox en el cuello de botella de
// nadie: la consulta mide 54 buffers / 0,6 ms, así que ni a 15s hay presión real.
const defaultPollInterval = 15 * time.Second

// eventTelemetryQuery es la consulta MEDIDA (refutación T6.5, 2026-08-10/11):
// 54 buffers / 0,6 ms sobre flow_events_pkey, CERO índices nuevos — el cursor
// por `id` hace que el filtro `id > $1` entre directo por la PK, sin falta de
// ningún índice adicional (a diferencia del endpoint de PIEZA 2, que sí necesita
// uno nuevo porque filtra por tenant_id + created_at, no por id).
//
// `name LIKE 'event\_%'` lleva el guion bajo ESCAPADO a propósito: sin escapar,
// `_` es comodín de UN carácter y el conteo se contamina con filas que no
// empiezan literalmente por "event_" (medido: 5% de contaminación sobre 10.500
// filas devueltas donde el prefijo real da 10.000, entrando además `kind=
// 'persist'`, la fila que menos pinta ahí).
//
// GROUP BY 1, 2 agrupa por `name` y por `payload->>'kind'` — NUNCA por la
// columna `kind` de la fila ("persist"|"event"): un GROUP BY sobre esa columna
// colapsaría los seis (hoy) nombres de efecto en una sola fila con el 100% de
// los eventos y CERO error, el fallo plausible que la refutación midió.
//
// max(id) viaja en la MISMA consulta agregada — no en una segunda — para no
// duplicar el escaneo: el cursor avanza al mayor id de las filas que esta
// vuelta CONTÓ de verdad, nunca al de una fila que el filtro descartó.
const eventTelemetryQuery = `
SELECT name, payload->>'kind' AS event_kind, count(*), max(id)
  FROM public.flow_events
 WHERE id > $1
   AND name LIKE 'event\_%'
 GROUP BY 1, 2
`

// collectorLockKey es la clave del advisory lock de SESIÓN que elige un único
// líder entre réplicas para este colector (ver la elección de líder en el
// comentario del paquete arriba).
//
// Derivada de SHA-256("wapp_flowlifecycle_collector"), primeros 8 bytes en
// big-endian, con el bit de signo forzado a 0 (AND 0x7FFFFFFFFFFFFFFF) para
// que el literal quepa como int64 positivo — mismo estilo de derivación que
// migrationLockKey (internal/platform/storage/postgres/migrations/migrate.go:31),
// pero un DOMINIO DE LOCK DISTINTO a propósito.
//
// VERIFICADO que no colisiona: `grep -rni advisory --include=*.go .` desde la
// raíz del módulo (2026-08-11) da como ÚNICO otro resultado ese mismo
// migrationLockKey = 0x736f0b2a4c1d9e57 — ni una sola cifra hexadecimal en
// común con el valor de abajo. La colisión sería grave y no solo teórica: el
// runner de migraciones toma su lock de forma BLOQUEANTE en cada arranque de
// cmd/server (bootstrap.go llama migrations.Migrate antes de levantar
// listeners), mientras que este colector lo intenta de forma NO bloqueante
// cada 15s y lo SOSTIENE mientras sea líder (ver becomeLeader). Si ambos
// compitieran por el mismo entero, una réplica que arranca y necesita migrar
// se quedaría esperando indefinidamente a que ESTE colector —en otra
// réplica, ajeno por completo a las migraciones— suelte "su" lock.
const collectorLockKey int64 = 0x37232781758ec913

// Collector es el colector incremental: un ticker que avanza un cursor por
// `id` (BIGSERIAL, append-only por diseño de flow_events, 0009) y publica el
// delta de cada vuelta por el callback onCount. Solo lo llama UNA goroutine
// (Run) en todo su ciclo de vida, así que ni `cursor` ni `leaderConn`
// necesitan mutex — mismo razonamiento que integrations.Worker, que tampoco
// lo lleva. (Los tests de este paquete son la única excepción deliberada:
// manejan varios Collector desde la goroutine de test, nunca el MISMO
// Collector desde dos goroutines a la vez.)
type Collector struct {
	db           *sql.DB
	onCount      func(name, eventKind string, delta float64)
	log          logger.Logger
	pollInterval time.Duration
	cursor       int64

	// leaderConn es la conexión reservada que sostiene el advisory lock de
	// SESIÓN mientras esta instancia sea líder; nil cuando no lo es. Ver
	// becomeLeader/releaseLeadership.
	leaderConn *sql.Conn
}

// NewCollector construye el colector. onCount es el callback de métricas
// (mtx.FlowEventLifecycle desde bootstrap.go); nil-safe, igual que
// receipts.NewSink/integrations.NewWorker — un nil simplemente no publica nada
// (útil en tests que no montan observabilidad).
//
// SIN funcional options (refutación del revisor, 2026-08-11): la entrega traía
// un `type Option func(*Collector)` y un `WithPollInterval` EXPORTADOS con CERO
// llamantes —ni en producción ni en tests—, generalidad especulativa que el
// gate de huérfanos de esta tarea prohíbe. El único parámetro configurable es
// pollInterval y hoy nadie lo cambia: cuando alguien lo necesite, añadir el
// option de vuelta cuesta las mismas ocho líneas que costó borrarlo.
func NewCollector(db *sql.DB, onCount func(name, eventKind string, delta float64), log logger.Logger) *Collector {
	return &Collector{db: db, onCount: onCount, log: log, pollInterval: defaultPollInterval}
}

// Run bloquea hasta que ctx se cancele. Se arranca con `go collector.Run(ctx)`
// sobre el MISMO ctx derivado de signal.NotifyContext que cierra el resto del
// proceso (bootstrap.go) — mismo molde que integrations.Worker.Run: un solo
// Ctrl+C también para este colector, sin un segundo mecanismo de shutdown.
//
// El advisory lock se libera SIEMPRE al salir (defer con context.Background(),
// no con ctx: para cuando este defer corre, ctx ya está cancelado casi
// siempre, y el unlock necesita su propio contexto vivo para tener
// oportunidad real de ejecutarse). Un lock de sesión que no se suelta deja a
// este colector muerto para TODO el clúster —nadie más puede ganarlo— hasta
// que Postgres recicle esa conexión.
func (c *Collector) Run(ctx context.Context) {
	//nolint:contextcheck // context.Background() a propósito, igual que el defer
	// shutdownAll de bootstrap.go: este defer corre cuando ctx YA está cancelado
	// casi siempre (es la razón de existir de Run() saliendo), y derivar el
	// unlock de ese mismo ctx muerto le quitaría al `SELECT pg_advisory_unlock`
	// cualquier oportunidad real de llegar a Postgres.
	defer c.releaseLeadership(context.Background())

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	c.pollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			if c.log != nil {
				c.log.Info("colector de telemetría de flow_events: apagando (contexto cancelado)")
			}
			return
		case <-ticker.C:
			c.pollOnce(ctx)
		}
	}
}

// becomeLeader intenta que ESTA instancia sea la que colecta en la vuelta
// actual. Devuelve true si ya lo era, o si lo acaba de ganar ahora; false si
// otra réplica lo tiene — en cuyo caso el llamante (pollOnce) debe pasar de
// largo esta vuelta SIN error y SIN log: en un despliegue de N réplicas este
// es el camino normal de N-1 de ellas en CADA vuelta, no una anomalía.
//
// El lock es de SESIÓN (pg_try_advisory_lock, no la variante _xact_), y se
// SOSTIENE en c.leaderConn mientras esa conexión viva — NO se suelta y se
// vuelve a pedir en cada vuelta. "Por vuelta" (la decisión de Jhoan) describe
// CUÁNDO se comprueba el liderazgo —una vez por tick de Run(), vía
// pollOnce—, no que el lock cambie de dueño en cada tick: soltarlo y
// reclamarlo cada 15s dejaría la elección al azar de quién golpea primero en
// CADA vuelta, y el cursor (en memoria, por-instancia) de quien gane una
// vuelta suelta puede estar arbitrariamente desfasado del de quien colectó
// la vuelta anterior — el mismo doble conteo que esta tarea arregla, solo
// que entre vueltas en vez de entre procesos. Sostener el lock mientras la
// conexión viva es lo que hace que "líder" signifique algo de una vuelta a
// la siguiente.
func (c *Collector) becomeLeader(ctx context.Context) bool {
	if c.leaderConn != nil {
		return true
	}

	conn, err := c.db.Conn(ctx)
	if err != nil {
		if c.log != nil {
			c.log.Error("colector de telemetría de flow_events: reservar conexión para el advisory lock", "error", err)
		}
		return false
	}

	var acquired bool
	if qerr := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", collectorLockKey).Scan(&acquired); qerr != nil {
		if c.log != nil {
			c.log.Error("colector de telemetría de flow_events: intentar el advisory lock", "error", qerr)
		}
		if cerr := conn.Close(); cerr != nil && c.log != nil {
			c.log.Error("colector de telemetría de flow_events: cerrar conexión tras fallo del advisory lock", "error", cerr)
		}
		return false
	}
	if !acquired {
		// Otra réplica ya lidera: silencioso y barato a propósito (decisión
		// de Jhoan) — nada de log aquí.
		if cerr := conn.Close(); cerr != nil && c.log != nil {
			c.log.Error("colector de telemetría de flow_events: cerrar conexión tras perder el advisory lock", "error", cerr)
		}
		return false
	}

	c.leaderConn = conn
	// Acabamos de GANAR el liderazgo: primera vez que este proceso lo tiene,
	// o SUCESIÓN tras la muerte del líder anterior — desde aquí dentro no se
	// puede distinguir un caso del otro, y no hace falta: los dos exigen el
	// MISMO tratamiento.
	//
	// CONTRAPARTIDA aceptada por Jhoan (2026-08-11): si veníamos de suceder a
	// un líder muerto, este arranque en max(id) actual pierde para SIEMPRE lo
	// que se escribió en la ventana entre la muerte del líder viejo y esta
	// adquisición — nadie vuelve a leer esas filas retroactivamente. Es
	// aceptable en un contador que se lee con rate()/increase() (una
	// vuelta perdida entre despliegues es ruido, no señal), pero JAMÁS para
	// auditar cuántos eventos hubo de verdad — ver el bloque "LO QUE ESTE
	// CONTADOR SIGUE SIN SER" al principio del fichero.
	c.initCursor(ctx)
	return true
}

// releaseLeadership suelta el advisory lock y cierra la conexión reservada,
// si había una. Es idempotente (nil-safe si ya se liberó o nunca se ganó) y
// se llama desde Run() con un defer, así que corre tanto en el camino normal
// de apagado (ctx cancelado) como si Run() retornara por cualquier otro
// motivo futuro — el defer no depende de CUÁL sea ese motivo.
func (c *Collector) releaseLeadership(ctx context.Context) {
	if c.leaderConn == nil {
		return
	}
	conn := c.leaderConn
	c.leaderConn = nil
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", collectorLockKey); err != nil && c.log != nil {
		c.log.Error("colector de telemetría de flow_events: liberar el advisory lock", "error", err)
	}
	if err := conn.Close(); err != nil && c.log != nil {
		c.log.Error("colector de telemetría de flow_events: cerrar la conexión del advisory lock", "error", err)
	}
}

// initCursor arranca el cursor en el max(id) ACTUAL de flow_events.
//
// DECISIÓN (T6.5, informe de la tarea): arrancar en max(id) y NO en 0.
//
// La tarea pide explícitamente decidir entre las dos y justificar. Este
// colector reinicia en CADA despliegue del proceso (no hay nada que persista
// el cursor entre reinicios: vive en memoria, como el resto del estado de
// Worker/Collector de este repo). Arrancar en 0 volvería a escanear el
// HISTÓRICO ENTERO en cada uno de esos reinicios: con 200.500 filas sembradas
// (la base del refutador) eso es un salto artificial idéntico —y del mismo
// tamaño— en cada `wapp_flow_event_lifecycle_total` justo después de cada
// deploy, indistinguible de un pico real de tráfico para quien mire
// rate()/increase() sobre el contador. Un contador que miente en el mismo
// instante en que más se le necesita (justo tras un release) es peor que uno
// que empieza corto.
//
// Arrancar en max(id) sacrifica UNA sola cosa, UNA sola vez: la ventana de
// eventos anterior a que este colector existiera queda invisible para
// siempre (no hay forma de recuperarla retroactivamente sin volver a leer
// esas filas, que es exactamente lo que la alternativa hace mal en cada
// reinicio). Es el mismo trato que recibe cualquier contador Prometheus que
// nace en un momento dado: no reescribe el pasado, cuenta desde que existe.
//
// Un fallo al leer max(id) NO aborta el arranque del proceso (el cursor se
// queda en 0 y la primera vuelta escanea de más una sola vez) — mismo criterio
// que el resto del bootstrap: la observabilidad nunca debe poder tumbar el
// servidor.
func (c *Collector) initCursor(ctx context.Context) {
	var maxID sql.NullInt64
	err := c.db.QueryRowContext(ctx, `SELECT max(id) FROM public.flow_events`).Scan(&maxID)
	if err != nil {
		if c.log != nil {
			c.log.Error("colector de telemetría de flow_events: leer max(id) inicial", "error", err)
		}
		return
	}
	if maxID.Valid {
		c.cursor = maxID.Int64
	}
}

// pollOnce ejecuta UNA vuelta: primero se asegura de ser el líder de esta
// vuelta (becomeLeader — si otra réplica lo es, vuelve de inmediato sin
// consultar nada ni loguear nada), consulta las filas nuevas desde el
// cursor, publica el delta de cada (name, event_kind) por onCount y avanza
// el cursor al mayor id VISTO en esta vuelta. Un error de consulta o de
// escaneo deja el cursor SIN avanzar (la próxima vuelta reintenta desde el
// mismo punto — nunca se pierde una fila por un fallo transitorio de la BD).
func (c *Collector) pollOnce(ctx context.Context) {
	if !c.becomeLeader(ctx) {
		return
	}

	rows, err := c.db.QueryContext(ctx, eventTelemetryQuery, c.cursor)
	if err != nil {
		if c.log != nil {
			c.log.Error("colector de telemetría de flow_events: consultar flow_events", "error", err)
		}
		return
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && c.log != nil {
			c.log.Error("colector de telemetría de flow_events: cerrar filas", "error", cerr)
		}
	}()

	newCursor := c.cursor
	type delta struct {
		name, eventKind string
		count           float64
	}
	var deltas []delta
	for rows.Next() {
		var name string
		var eventKind sql.NullString
		var count int64
		var maxIDRow int64
		if serr := rows.Scan(&name, &eventKind, &count, &maxIDRow); serr != nil {
			if c.log != nil {
				c.log.Error("colector de telemetría de flow_events: escanear fila agregada", "error", serr)
			}
			return
		}
		if maxIDRow > newCursor {
			newCursor = maxIDRow
		}
		deltas = append(deltas, delta{name: name, eventKind: eventKind.String, count: float64(count)})
	}
	if rerr := rows.Err(); rerr != nil {
		if c.log != nil {
			c.log.Error("colector de telemetría de flow_events: iterar flow_events", "error", rerr)
		}
		return
	}

	// El cursor avanza DESPUÉS de haber leído todas las filas sin error (arriba):
	// publicar primero y avanzar después dejaría una vuelta a medio publicar
	// creyendo que ya avanzó si un onCount tardío fallara — onCount de este
	// paquete (mtx.FlowEventLifecycle) es un Inc/Add de Prometheus, que no
	// puede fallar, pero el orden no depende de esa garantía ajena.
	c.cursor = newCursor
	if c.onCount == nil {
		return
	}
	for _, d := range deltas {
		c.onCount(d.name, d.eventKind, d.count)
	}
}
