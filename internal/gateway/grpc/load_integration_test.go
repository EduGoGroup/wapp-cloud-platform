package gatewaygrpc_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-cloudlink/mtls"
	"github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet/fleettest"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// ============================================================================
// Arnés de CARGA del Connect contra Postgres REAL (Plan 050 · T5.0 y T5.1).
//
// Para qué sirve: T5.2 mide el rendimiento del bucle Recv del Connect ANTES del
// plan, corriendo este arnés sobre el commit base. Por eso el fichero es
// TEST-ONLY y NO depende de nada de las Olas 1-4 (no hay worklane, no hay
// cambios en connect.go): si arrastrara código del plan, dejaría de poder medir
// el pre-plan.
//
// Sin build tags: el fichero se salta SOLO por variable de entorno, calcando el
// patrón de la casa (internal/gateway/fleet/integration_test.go:20-34). Sin
// WAPP_TEST_DB_DSN ⇒ t.Skip; con WAPP_TEST_REQUIRE_DB definido, la ausencia de
// BD es un FALLO (la integración DEBE correr).
//
// Por qué mTLS y no el newHarness de server_test.go: sin identidad mTLS el
// Connect DEGRADA a propósito (peerIdentity ⇒ hasIdentity=false), y tanto
// onSessionRegistered como persistHealth retornan ANTES de tocar fleet
// (connect.go). Un arnés sobre bufconn en claro NUNCA escribiría en Postgres:
// inyectarle un repositorio sería inyectar código muerto. De ahí que este arnés
// calque el patrón de newMTLSHarness (mtls_test.go) y reutilice sus helpers de
// certificados (newDevCA, issueEdgeCert) y de heartbeats (mtlsHeartbeatHealth).
//
// ⚠️ 2026-08-18 · Plan 050 · Ola 1 — el párrafo de arriba SIGUE VIGENTE, y esta
// nota está aquí para que nadie crea lo contrario al ver las enmiendas de
// correrFase y registrar. El fichero no importa nada del carril ni de ninguna ola:
// lo único que cambió es CÓMO reparte la fase sus heartbeats (n sesiones distintas
// en vez de una sola). Esa forma es válida en LOS DOS mundos —con el bucle Recv
// serial del commit base el suelo n×d se cumple igual—, así que el arnés sigue
// siendo test-only y cherry-pickeable sobre `af457c9`, que es la condición de la
// que cuelga la medición del ANTES de T5.2.
// ============================================================================

const (
	// dsnEnv habilita los tests de integración con BD real (mismo patrón que
	// fleet, lease y enroll).
	dsnEnv = "WAPP_TEST_DB_DSN"
	// requireDBEnv convierte la ausencia de BD en FALLO en vez de skip: es la
	// palanca con la que el CLI exige que la integración corra de verdad.
	requireDBEnv = "WAPP_TEST_REQUIRE_DB"
	// sondeoBD es el intervalo con el que se relee la fila de fleet_sessions
	// esperando el marcador del último heartbeat. Entra como ruido en la medición
	// (a lo sumo un sondeo de más), por eso es pequeño frente a la latencia
	// inyectada.
	sondeoBD = 2 * time.Millisecond
	// esperaMaxBD acota la espera del marcador: muy por encima del peor caso
	// (heartbeatsPorFase × latenciaPorConsulta) para que un fallo sea un fallo
	// real y no una carrera con una BD fría.
	esperaMaxBD = 30 * time.Second
	// binarioDeCarga marca en la BD las filas que escribe este arnés (columna
	// binary_version). No es PII ni configuración: solo una etiqueta para
	// reconocer lo que dejó una corrida de carga.
	binarioDeCarga = "v0.0.0-arnes-carga"
)

// erroresDeLease son los mensajes que el Server escribe en el log cuando el
// camino del lease falla (connect.go: onSessionRegistered y renewLease). El
// arnés los vigila al terminar cada test: sin esta vigilancia el lease podría
// estar roto de arriba abajo y los tests seguirían verdes, porque el cliente
// nunca vería el error y el logger iba a io.Discard.
var erroresDeLease = []string{
	"lease: emitir inicial",
	"lease: push inicial",
	"lease: renovar",
}

// openTestDB abre la conexión de test o salta si no hay BD configurada. Calca
// literalmente el helper homónimo de internal/gateway/fleet/integration_test.go:
// no es reutilizable por import (vive en un fichero _test.go de OTRO paquete y
// en el repo no existe ningún helper compartido para esto — se duplica en los 50+
// paquetes con integración).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv(requireDBEnv) != "" {
			t.Fatalf("%s no definido pero %s exige BD: la integración DEBE correr", dsnEnv, requireDBEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// El pool va MUY por encima del default de producción (25, postgres/connect.go)
	// a propósito. POR QUÉ, en dos capas y en este orden: PRIMERO la razón
	// original, que ha CADUCADO, y DESPUÉS la vigente, que es la que manda hoy. Se
	// conservan las dos porque la caducada se citó para justificar decisiones, y
	// hay que poder ver qué se creía y por qué dejó de ser cierto.
	//
	// ── ENUNCIADO ORIGINAL (2026-08-18), CONSERVADO LITERAL ───────────────────
	//	«con carril, N sesiones latiendo abren N workers concurrentes, y en el
	//	escenario N=60 eso son hasta 60 demandantes contra 25 conexiones. Los 35
	//	que no la consiguen esperan DENTRO del presupuesto de 5 s del job y algunos
	//	lo agotan, con lo que renewLease se rinde y el Edge pierde su LeaseUpdate.
	//	Medido el 2026-08-18: con 25 el test falla 3 de cada 6 corridas; con 90,
	//	0 de 6. […] Que 25 se quede corto bajo flota real es un hallazgo de
	//	producto, NO del test, y vive como DEUDA-050.2 con dueño (Ola 3).»
	//
	// ── QUÉ DE ESO SIGUE SIENDO CIERTO, Y QUÉ NO (2026-08-19, Plan 050 · T5.5) ─
	// La MECÁNICA es exacta y quedó confirmada con el contador del pool, que hasta
	// T5.5 nadie había mirado: con 60 sesiones latiendo hay 60 carriles pidiendo
	// conexión, el pico de InUse contra un pool sin restricción es EXACTAMENTE 60
	// (una por carril) y el codo —el primer tope en el que WaitCount cae a cero—
	// es EXACTAMENTE 60, en 9 de 9 corridas. Con MaxOpenConns=25 sí hay espera:
	// ~1.288 esperas por corrida (rango 1.286-1.290 en 9 corridas).
	//
	// Lo que NO se sostiene es la CONSECUENCIA. La espera media a 25 es de
	// ~3,3 ms bajo el arnés y de ~2,80 ms en este mismo test (WaitCount ≈ 8.570,
	// WaitDuration ≈ 24 s por corrida) contra un presupuesto de job de 5 s
	// (defaultWorkBudget, server.go): el 0,06 %. Con ese margen la espera por
	// conexión no puede ser lo que hace vencer a renewLease.
	//
	// Y la flakiness NO SE REPRODUCE: 17 corridas, 17 en verde, en dos manos
	// distintas y sobre la cabeza de esta rama —12 de T5.5 (6 con el lease y el
	// fleet sobre un pool de 25 y los sondeos aparte, + 6 con este openTestDB
	// entero bajado a 25, que es la condición EXACTA de la medida del 18) y 5 más
	// del orquestador, reproducidas de forma independiente bajando este pool a 25.
	//
	// HIPÓTESIS (no demostrada) de por qué el 18 salió distinto: que se midiera
	// con la Ola 1 a medio aterrizar, cuando el bucle Recv todavía gastaba el
	// presupuesto en head-of-line y no en el pool. Encaja con que el p99 del Ack
	// pasara de 619,6 ms a 447 µs, pero NO se ha comprobado y no se debe citar
	// como hecho. Tampoco se afirma que la medida del 18 fuera errónea: se tomó
	// de buena fe y aquí solo consta que hoy no se reproduce.
	//
	// ── POR QUÉ EL POOL SE QUEDA EN 90 DE TODAS FORMAS ────────────────────────
	// La decisión no cambia, pero el motivo SÍ. Ya no es tapar una flakiness que
	// no aparece: es AISLAMIENTO. Lo que T5.2/T5.3 miden es la latencia del Ack
	// con heartbeats en vuelo, no la capacidad del pool, y con el tope por debajo
	// de las sesiones del escenario (60) este gate mediría las dos cosas mezcladas
	// sin decir cuál. Un pool por encima de N mantiene el pool FUERA de la medida
	// por construcción, no por suerte. Si alguien sube sesionesT52 por encima de
	// 90, hay que subir esto con él o el gate empieza a medir otra cosa.
	//
	// ── EL PUNTERO A DEUDA-050.2, ACTUALIZADO ─────────────────────────────────
	// Ya NO es «25 se queda corto, dueño Ola 3»: T4.6 se cerró el 2026-08-19 con
	// NO MOVER NADA —el default sigue en 25— con la curva de T5.5 delante
	// (curva_pool_t55_integration_test.go, que es donde vive la medición y la
	// tabla). El riesgo que queda es OTRO y no está en el pool: la concurrencia
	// de carriles no tiene TECHO AGREGADO. Hay un carril por sesión viva y nada
	// acota su suma sobre la flota entera —el semáforo de 64 de
	// Flow.MaxConcurrentIncoming acota el camino de los ENTRANTES, no este—, así
	// que el codo se mueve con el tamaño de la flota. No busques aquí el número
	// del pool: aquí solo se aísla el gate.
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn, MaxOpenConns: 90, MaxIdleConns: 90})
	if err != nil {
		if os.Getenv(requireDBEnv) != "" {
			t.Fatalf("BD no disponible en %s (%v) pero %s exige BD", dsnEnv, err, requireDBEnv)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	// El esquema lo prepara el runner de migraciones del propio repo (full-replay
	// idempotente): NADA de DDL a mano en los tests. De paso deja listas TODAS las
	// tablas que el arnés puede necesitar, incluidas public.leases (0003) y
	// public.tenants.revoked_at (0058), que son las que usa el repositorio Postgres
	// del lease cuando se cablea con configArnes.leasePostgres.
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

// seedTenant crea un tenant con slug único y devuelve su UUID. fleet_sessions
// tiene FK a public.tenants (migración 0003), así que sin esta fila el MarkOnline
// del Connect fallaría — y como el tenant_id viaja en el Organization del cert de
// Edge, el UUID sembrado es el que hay que firmar en el certificado. La misma
// fila es la que hace viable el lease sobre Postgres: leases.tenant_id tiene FK a
// tenants(id) y TenantRevoked lee tenants.revoked_at, así que el arnés no
// necesita ningún seed extra para conmutar el lease a Postgres.
func seedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-carga-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Arnés de carga Plan 050")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

// edgeSender / edgeReceiver son las dos mitades del stream Connect que el arnés
// necesita. Se declaran como interfaces mínimas a propósito: así los helpers no
// se atan al nombre del tipo genérico que genera protoc-gen-go-grpc.
type edgeSender interface {
	Send(*cloudlinkv1.EdgeToCloud) error
}

type edgeReceiver interface {
	Recv() (*cloudlinkv1.CloudToEdge, error)
}

// configArnes son las PALANCAS del arnés de carga: lo que cada test necesita
// mover para que su medición signifique algo. Van en un struct (y no como
// argumentos posicionales) porque son booleanos de escenario y en la llamada se
// leen por su nombre.
type configArnes struct {
	// latenciaInicial es la espera que SlowRepository añade a cada consulta al
	// fleet desde el arranque. Los tests de T5.0/T5.1 arrancan en 0 y la suben en
	// caliente con SetDelay, para que el registro de la sesión no entre en la
	// medida.
	latenciaInicial time.Duration
	// drenarStream decide si el cliente lee su stream (ver drenar). true modela un
	// Edge SANO; false provoca a propósito el head-of-line que T5.3 medirá.
	drenarStream bool
	// leasePostgres conmuta el repositorio del lease de memoria a POSTGRES (el
	// mismo pool que el fleet). En producción cada Heartbeat paga tres viajes a la
	// BD por el lease (TenantRevoked + Get + Upsert, lease.go:issueAndPersist), así
	// que medir el "antes" con el lease en memoria SUBESTIMA la línea base: T5.2
	// necesita poder incluirlos. T5.1 en cambio lo deja en memoria a propósito,
	// para aislar la latencia inyectable del fleet.
	leasePostgres bool
	// dbMedido, si no es nil, es el pool sobre el que corren el repositorio del
	// LEASE y el del FLEET, en lugar del pool de openTestDB. Nace el 2026-08-19
	// para T5.5 (la curva del pool bajo carga): la pregunta de esa tarea es
	// cuántas conexiones pide el arnés REAL, y con el pool de openTestDB fijo en
	// 90 —ancho a propósito para que DEUDA-050.2 no contamine lo que miden T5.2 y
	// T5.3— no hay forma de barrer el tope.
	//
	// El nil conserva el comportamiento EXACTO anterior (todo sobre el pool de
	// openTestDB), así que ningún test existente cambia de significado. Y h.db NO
	// se toca: los sondeos del propio arnés (esperarUptime, seedTenant) siguen
	// yendo por el pool ancho, para que el tráfico del test no se cuele en las
	// estadísticas de lo que se está midiendo.
	dbMedido *sql.DB
}

// loadHarness levanta el Server con mTLS + lease (memoria o Postgres, según
// configArnes) + fleet sobre POSTGRES REAL, envuelto en el decorador de latencia
// inyectable (T5.1), sobre un bufconn.Listener; y expone el cliente CloudLink ya
// conectado con el cert del Edge.
type loadHarness struct {
	db *sql.DB
	// srv es el Server BAJO MEDICIÓN. Se expone porque el lado nube de un envío
	// NO es una RPC: SendText es un método Go que llaman los handlers HTTP
	// (send.go), y sin el puntero al servidor no hay forma de disparar el camino
	// que T5.2 mide (Push → awaitAck).
	srv    *gatewaygrpc.Server
	client cloudlinkv1.CloudLinkClient
	// slow es la perilla de T5.1: la latencia por consulta al fleet es un
	// PARÁMETRO del test, ajustable en caliente con SetDelay.
	slow *fleettest.SlowRepository
	// logBuf recoge lo que escribe el Server. Es lo que convierte un fallo del
	// lease (que el cliente NO ve: el servidor lo traga y sigue) en un rojo.
	logBuf *syncBuffer
	cfg    configArnes
	// leaseUpdates cuenta los LeaseUpdate que el cliente ha recibido de verdad.
	// Atómico porque lo incrementa la goroutine de drenaje y lo lee el test
	// (los gates corren con -race).
	leaseUpdates atomic.Int64
	// tenantID y edgeID son ÚNICOS por arnés (tenant recién sembrado + edge con
	// sufijo de nanosegundos): dos corridas seguidas no se pisan aunque compartan
	// la misma base de datos.
	tenantID string
	edgeID   string
}

// newLoadHarness construye el arnés con la configuración dada. Salta el test si
// no hay BD.
//
// Decisiones de cableado, para que la medición de T5.2 signifique algo:
//   - El LEASE se cablea SIEMPRE (no se deja nil) porque en producción cada
//     Heartbeat renueva y empuja un LeaseUpdate: quitarlo mediría un Connect que
//     no existe. Dónde vive su repositorio es cosa del test: en MEMORIA por
//     defecto —que es lo que T5.1 necesita para aislar la latencia inyectable del
//     fleet— o en POSTGRES con configArnes.leasePostgres, que es lo que T5.2 debe
//     usar para no subestimar la línea base (tres consultas por heartbeat).
//   - El cliente MIRA lo que le empujan (ver drenar y esperarLeases) y el logger
//     escribe a un buffer que se revisa al cerrar el test: con el lease cableado
//     pero nadie mirándolo, podría estar roto entero y el test seguiría verde.
func newLoadHarness(t *testing.T, cfg configArnes) *loadHarness {
	t.Helper()

	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	edgeID := fmt.Sprintf("edge-carga-%d", time.Now().UnixNano())

	ca := newDevCA(t)
	edgeCert := issueEdgeCert(t, ca, tenantID, edgeID)

	priv, err := lease.GenerateDevKey()
	if err != nil {
		t.Fatalf("GenerateDevKey: %v", err)
	}
	// medido es el pool que ven el LEASE y el FLEET. Por defecto, el mismo de
	// openTestDB; T5.5 lo sustituye por uno con el tope que quiera barrer.
	medido := db
	if cfg.dbMedido != nil {
		medido = cfg.dbMedido
	}

	var leaseRepo lease.Repository = lease.NewMemoryRepository()
	if cfg.leasePostgres {
		leaseRepo = lease.NewPostgresRepository(medido)
	}
	mgr, err := lease.NewManager(priv, leaseRepo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// El fleet REAL (Postgres) decorado con la latencia inyectable.
	slow := fleettest.NewSlow(fleet.NewPostgresRepository(medido), cfg.latenciaInicial)

	reg := session.NewRegistry()
	logBuf := &syncBuffer{}
	log := logger.New(logger.WithWriter(logBuf))
	srv := gatewaygrpc.New(reg, log, gatewaygrpc.WithLease(mgr), gatewaygrpc.WithFleet(slow))

	h := &loadHarness{
		db:       db,
		srv:      srv,
		slow:     slow,
		logBuf:   logBuf,
		cfg:      cfg,
		tenantID: tenantID,
		edgeID:   edgeID,
	}

	// Vigilancia del log. Se registra ANTES del cleanup que cierra la conexión y
	// para el servidor, y t.Cleanup es LIFO: por eso este corre DESPUÉS de que
	// gs.Stop() haya terminado los handlers, cuando el buffer ya no cambia. Leerlo
	// antes sería una carrera con el goroutine de Recv (el gate corre con -race).
	t.Cleanup(func() {
		for _, marca := range erroresDeLease {
			if n := h.logBuf.count(marca); n != 0 {
				t.Errorf("el servidor logueó %d× %q: el camino del lease FALLÓ durante el test "+
					"(el cliente no lo ve, el servidor se lo traga y sigue)\n  causa: %s",
					n, marca, strings.Join(h.logBuf.linesContaining(marca), "\n  causa: "))
			}
		}
	})

	srvCertPEM, srvKeyPEM, err := ca.IssueServerCert("localhost", []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	serverCert, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair server: %v", err)
	}

	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer(grpc.Creds(mtls.ServerCreds(serverCert, ca.Pool())))
	srv.Register(gs)

	serveErrc := make(chan error, 1)
	go func() { serveErrc <- gs.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(mtls.ClientCreds(edgeCert, ca.Pool(), "localhost")),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	t.Cleanup(func() {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("cerrando conn: %v", cerr)
		}
		gs.Stop()
		// El error de Serve NO se descarta (mismo criterio que newHarness,
		// server_test.go): un fallo del servidor moriría en silencio y el test
		// pasaría por razones equivocadas.
		if serveErr := <-serveErrc; serveErr != nil {
			t.Errorf("gs.Serve devolvió error: %v", serveErr)
		}
		if cerr := lis.Close(); cerr != nil {
			t.Errorf("cerrando listener: %v", cerr)
		}
	})

	h.client = cloudlinkv1.NewCloudLinkClient(conn)
	return h
}

// sessionIDUnico devuelve un identificador de sesión único: junto con el tenant
// recién sembrado y el edge_id con sufijo de nanosegundos, garantiza que dos
// corridas seguidas del mismo test no se pisen en la misma base de datos.
func sessionIDUnico(sufijo string) string {
	return fmt.Sprintf("sess-carga-%s-%d", sufijo, time.Now().UnixNano())
}

// drenar lanza una goroutine que consume lo que el servidor empuja (el LeaseUpdate
// inicial y la renovación de cada Heartbeat) y CUENTA los LeaseUpdate vistos, para
// que el test pueda AFIRMAR que el lease funciona en vez de tirar los frames a
// ciegas. La goroutine termina sola cuando el t.Cleanup del arnés cierra la
// conexión y el Recv devuelve error.
//
// Qué modela drenar, y qué pasa al apagarlo (configArnes.drenarStream=false): un
// Edge SANO lee su stream. No drenar NO bloquea al servidor indefinidamente
// —Registry.Push lanza el Send en otra goroutine con un canal bufferizado (cap 1) y
// lo acota con defaultSendTimeout = 10s (session/registry.go)—, pero sí FRENA el
// bucle Recv del Connect hasta esos 10 s por Push, porque route llama a renewLease
// en línea. Ese frenado ACOTADO es justo el head-of-line que el Plan 050 viene a
// medir y el que T5.3 provocará apagando el drenaje; con el drenaje activo, la
// medición de las fases no lo incluye.
func (h *loadHarness) drenar(stream edgeReceiver) {
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if msg.GetLeaseUpdate() != nil {
				h.leaseUpdates.Add(1)
			}
		}
	}()
}

// esperarLeases espera a que el cliente haya recibido al menos want LeaseUpdate.
// Es la mitad que MIRA del lease: el servidor no le devuelve al Edge ningún error
// de lease (los traga en su log), así que si esta espera vence, el lease no está
// llegando.
func (h *loadHarness) esperarLeases(t *testing.T, want int64) {
	t.Helper()
	deadline := time.Now().Add(esperaMaxBD)
	for time.Now().Before(deadline) {
		if h.leaseUpdates.Load() >= want {
			return
		}
		time.Sleep(sondeoBD)
	}
	t.Fatalf("timeout esperando %d LeaseUpdate en el cliente (recibidos %d): el lease no está llegando al Edge",
		want, h.leaseUpdates.Load())
}

// heartbeatCarga arma un Heartbeat con salud, usando n como MARCADOR DE SECUENCIA
// por dos vías a la vez:
//   - daemon_uptime_s, que persiste en fleet_sessions.uptime_s y es lo que el test
//     sondea para saber que ESE heartbeat concreto ya llegó a la BD;
//   - lease_counter, que es el ANCLA de la renovación: el servidor emite
//     heartbeatCounter+1 (lease.Manager.Renew). Ojo, la Nube NO exige que crezca:
//     ni Renew ni Repository.Upsert comparan el counter con el anterior. Quien
//     exige monotonía es el Validator del EDGE (wapp-cloudlink lease/validator.go,
//     ErrStaleCounter). Aquí n crece porque es el mismo marcador de secuencia que
//     daemon_uptime_s, no porque el servidor rechazaría el frame.
func heartbeatCarga(sessionID string, n int64) *cloudlinkv1.EdgeToCloud {
	return mtlsHeartbeatHealth(sessionID, n, &cloudlinkv1.SessionHealth{
		WhatsappSocketState:  cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_CONNECTED,
		LastInboundEventAgeS: 1,
		BinaryVersion:        binarioDeCarga,
		DaemonUptimeS:        n,
	})
}

// esperarUptime sondea la BD REAL hasta que la fila de la sesión refleje el
// marcador want en uptime_s, y devuelve cuando lo ve. Es la barrera que convierte
// un Send asíncrono (el servidor procesa en su goroutine de Recv) en algo
// medible: cuando el marcador está en Postgres, el frame ya recorrió TODO el
// camino Recv → route → persistHealth → repositorio.
//
// El ctx del llamante gobierna el sondeo (ctx primero, regla context-as-argument
// de revive): así la espera es CANCELABLE y no sobrevive al test que la lanzó.
func (h *loadHarness) esperarUptime(ctx context.Context, t *testing.T, sessionID string, want int64) {
	t.Helper()
	deadline := time.Now().Add(esperaMaxBD)
	var ultimo sql.NullInt64
	for time.Now().Before(deadline) {
		if cerr := ctx.Err(); cerr != nil {
			t.Fatalf("contexto cancelado esperando uptime_s=%d de %q: %v (último visto: %v)", want, sessionID, cerr, ultimo)
		}
		err := h.db.QueryRowContext(ctx, `
			SELECT uptime_s FROM public.fleet_sessions
			WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
		`, h.tenantID, h.edgeID, sessionID).Scan(&ultimo)
		switch {
		case err == nil:
			if ultimo.Valid && ultimo.Int64 == want {
				return
			}
		case errors.Is(err, sql.ErrNoRows):
			// La sesión aún no se registró (MarkOnline en vuelo): se reintenta.
		default:
			t.Fatalf("consultando fleet_sessions: %v", err)
		}
		time.Sleep(sondeoBD)
	}
	t.Fatalf("timeout esperando uptime_s=%d de %q en la BD (último visto: %v)", want, sessionID, ultimo)
}

// correrFase manda UN heartbeat a CADA UNA de las sesiones dadas —una sesión
// distinta por heartbeat, numerados con el marcador corrido de m— y devuelve
// cuánto tardó la fase entera. Entre un heartbeat y el siguiente espera la barrera
// de LeaseUpdate, que es lo que impide que dos latidos se solapen.
//
// 🔴 ENMENDADA EL 2026-08-18 · Plan 050 · Ola 1 (decisión de Jhoan). El enunciado
// original se conserva LITERAL aquí abajo: dejó de regir, no se borra.
//
//	«correrFase manda n heartbeats consecutivos numerados desde+1 … desde+n y
//	devuelve cuánto tardó en verse el ÚLTIMO persistido en Postgres. El bucle Recv
//	del Connect es SERIAL (una sola goroutine por stream), así que con una latencia
//	d por consulta el suelo aritmético de la fase es n×d.»
//
// POR QUÉ DEJÓ DE REGIR. Con el carril de la Ola 1 cableado (worklane.go), el
// bucle Recv ya NO hace el trabajo inline: lo suelta a una cola POR SESIÓN. Y la
// coalescencia de heartbeats (D-050.4) SUSTITUYE EN SITIO el latido pendiente de
// una sesión cuando llega otro de la MISMA sesión. Diez heartbeats seguidos al
// mismo session_id ya no son diez jobs: son ~2 —el que está en vuelo y el último
// que sustituyó a todos los demás—, así que la fase caía muy por debajo del suelo
// de 200 ms y el test se habría puesto rojo PORQUE LA OLA FUNCIONA.
//
// QUÉ LO SUSTITUYE, Y POR QUÉ EL SUELO n×d SIGUE VALIENDO (el suelo no se ha
// movido ni un milisegundo; lo que se ha arreglado es la premisa que lo sostenía):
//   - los n heartbeats de la fase se reparten en n SESIONES DISTINTAS, una cada
//     uno, así que NO hay nada que coalescer — cada sesión recibe exactamente un
//     latido por fase;
//   - entre latido y latido se espera esperarLeases, que impide el solape. Esa
//     barrera NO es un adorno: cada sesión tiene su PROPIA goroutine (worklane.go,
//     queueFor arranca un worker por session_id), de modo que sin ella los n
//     latidos correrían EN PARALELO y la fase costaría ~d, no n×d.
//
// Con las dos cosas, cada heartbeat vuelve a pagar su d entero y en serie, que es
// exactamente lo que la serialidad del bucle Recv daba gratis antes de la ola.
//
// Es el molde de T5.2 (load_ack_integration_test.go: un heartbeat a N sesiones
// distintas + esperarLeases como barrera entre rondas), con la ronda de tamaño
// UNO: lo que T5.1 mide es el suelo aritmético de la latencia inyectada, no el
// paralelismo del carril.
//
// Por qué la barrera es de LeaseUpdate y no de uptime_s: el LeaseUpdate se empuja
// al FINAL del job del heartbeat (submitHeartbeat: persistSelfPn → persistHealth →
// renewLease, connect.go), así que verlo en el cliente ya prueba que el SaveHealth
// —el único que paga la latencia inyectada— terminó, y cuesta una lectura atómica
// en vez de una consulta a la BD por latido. Aun así, al cerrar la fase se
// comprueba UNA vez contra Postgres que la salud aterrizó de verdad: un SaveHealth
// que falle se lo traga el log del servidor (connect.go) y la barrera del lease no
// se enteraría. Esa comprobación va DESPUÉS de parar el cronómetro y su fila ya
// está escrita, así que no alarga la fase.
func (h *loadHarness) correrFase(ctx context.Context, t *testing.T, stream edgeSender, sesiones []string, m *medicionT52) time.Duration {
	t.Helper()
	inicio := time.Now()
	for _, sid := range sesiones {
		m.ronda++
		if err := stream.Send(heartbeatCarga(sid, m.ronda)); err != nil {
			t.Fatalf("Send heartbeat #%d a la sesión %q: %v", m.ronda, sid, err)
		}
		m.leases++
		h.esperarLeases(t, m.leases)
	}
	fase := time.Since(inicio)
	h.esperarUptime(ctx, t, sesiones[len(sesiones)-1], m.ronda)
	return fase
}

// registrar abre el stream, deja registradas (online en Postgres) TODAS las
// sesiones dadas —sobre ese ÚNICO stream, que es lo que hace un Edge real
// (ADR-0008)— y devuelve el stream ya drenándose. El registro se hace SIEMPRE con
// la latencia que tenga el arnés en ese momento; los tests que miden lo hacen con
// latencia 0 para que el coste del MarkOnline no entre en la medición de las fases.
//
// ⚠️ 2026-08-18 · Plan 050 · Ola 1: antes registraba UNA sesión (`sessionID
// string`). Pasa a lista porque la fase de T5.1 reparte ahora sus heartbeats en n
// sesiones distintas (ver correrFase). Los llamantes de una sola sesión pasan una
// lista de uno y no cambian de comportamiento.
//
// (ctx va primero por la regla context-as-argument de revive, activa en
// .golangci.yml también para los tests.)
func (h *loadHarness) registrar(ctx context.Context, t *testing.T, sesiones []string) edgeSender {
	t.Helper()
	if len(sesiones) == 0 {
		t.Fatal("registrar sin sesiones: el arnés no tendría nada que medir")
	}
	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if h.cfg.drenarStream {
		h.drenar(stream)
	}
	for i, sid := range sesiones {
		if err := stream.Send(heartbeatCarga(sid, 1)); err != nil {
			t.Fatalf("Send heartbeat de registro de la sesión %d (%q): %v", i, sid, err)
		}
	}
	if h.cfg.drenarStream {
		// El lease se MIRA: cada sesión empuja su LeaseUpdate inicial
		// (onSessionRegistered) y su heartbeat de registro fuerza una renovación
		// (route → submitHeartbeat → renewLease). Son DOS POR SESIÓN, y si no llegan
		// el lease está roto. Con el carril de la Ola 1 esta es además la barrera
		// que de verdad cierra el registro: las sesiones se registran en paralelo
		// (una goroutine por sesión), así que ver la fila de la última NO implica
		// que las demás estén.
		h.esperarLeases(t, int64(2*len(sesiones)))
	}
	// Y se confirma contra la BD REAL, sesión por sesión: el LeaseUpdate prueba que
	// el job terminó, no que el SaveHealth escribiera (su error se lo traga el log).
	// Tras la barrera de arriba las filas ya están, así que cada sondeo acierta a la
	// primera; sin drenaje (drenarStream=false) este bucle ES la única barrera.
	for _, sid := range sesiones {
		h.esperarUptime(ctx, t, sid, 1)
	}
	return stream
}

// TestIntegration_CargaArnesPersisteEnPostgres es el criterio de T5.0: el arnés
// registra una sesión, manda un heartbeat y la fila queda PERSISTIDA en la base
// REAL — no en memoria. La comprobación se hace con SQL directo contra
// public.fleet_sessions (no releyendo por el repositorio) justamente para que no
// quepa duda de dónde acabó el dato.
func TestIntegration_CargaArnesPersisteEnPostgres(t *testing.T) {
	h := newLoadHarness(t, configArnes{drenarStream: true})
	sid := sessionIDUnico("t50")

	// Holgura sobre esperaMaxBD: si el ctx del test venciera a la vez que el
	// sondeo de esperarUptime, el fallo sería un "context deadline exceeded"
	// confuso en vez del mensaje diagnóstico que dice qué se estaba esperando.
	ctx, cancel := context.WithTimeout(context.Background(), 5*esperaMaxBD)
	defer cancel()

	h.registrar(ctx, t, []string{sid})

	var (
		estado        string
		whatsappState sql.NullString
		binario       sql.NullString
		uptime        sql.NullInt64
		lastHealthAt  sql.NullTime
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT state, whatsapp_state, binary_version, uptime_s, last_health_at
		FROM public.fleet_sessions
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, h.tenantID, h.edgeID, sid).Scan(&estado, &whatsappState, &binario, &uptime, &lastHealthAt)
	if err != nil {
		t.Fatalf("leyendo la fila persistida: %v", err)
	}

	// El link CloudLink quedó online (MarkOnline del registro de sesión)…
	if estado != string(fleet.StateOnline) {
		t.Fatalf("state persistido = %q, quiero %q", estado, fleet.StateOnline)
	}
	// …y la salud del SOCKET viajó entera en el mismo heartbeat.
	if !whatsappState.Valid || whatsappState.String != "connected" {
		t.Fatalf("whatsapp_state persistido = %v, quiero \"connected\"", whatsappState)
	}
	if !binario.Valid || binario.String != binarioDeCarga {
		t.Fatalf("binary_version persistido = %v, quiero %q", binario, binarioDeCarga)
	}
	if !uptime.Valid || uptime.Int64 != 1 {
		t.Fatalf("uptime_s persistido = %v, quiero 1", uptime)
	}
	if !lastHealthAt.Valid {
		t.Fatal("last_health_at debía quedar poblado: sin él no hubo ingesta de salud")
	}

	// Aislamiento: el tenant es recién sembrado, así que la sesión de ESTA corrida
	// es la única de su tenant. Dos corridas seguidas no se pisan.
	var filas int
	if err := h.db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.fleet_sessions WHERE tenant_id = $1`, h.tenantID).Scan(&filas); err != nil {
		t.Fatalf("contando filas del tenant: %v", err)
	}
	if filas != 1 {
		t.Fatalf("el tenant del arnés tiene %d filas, quiero 1 (aislamiento por corrida)", filas)
	}
}

// TestIntegration_CargaLeaseSobrePostgres ejercita la palanca
// configArnes.leasePostgres: el mismo arnés, con el lease cableado sobre POSTGRES
// en vez de memoria, deja el estado del lease en public.leases. Es lo que T5.2
// necesita para medir la línea base sin subestimarla — con esta palanca cada
// Heartbeat paga sus tres viajes reales a la BD (TenantRevoked + Get + Upsert) —
// y de paso demuestra que el arnés no necesita ningún seed extra para conmutar:
// el tenant sembrado ya satisface la FK de leases y la lectura de tenants.revoked_at.
func TestIntegration_CargaLeaseSobrePostgres(t *testing.T) {
	h := newLoadHarness(t, configArnes{drenarStream: true, leasePostgres: true})
	sid := sessionIDUnico("lease-pg")

	ctx, cancel := context.WithTimeout(context.Background(), 5*esperaMaxBD)
	defer cancel()

	h.registrar(ctx, t, []string{sid})

	var (
		counter int64
		revoked bool
	)
	if err := h.db.QueryRowContext(ctx, `
		SELECT counter, revoked FROM public.leases WHERE tenant_id = $1 AND edge_id = $2
	`, h.tenantID, h.edgeID).Scan(&counter, &revoked); err != nil {
		t.Fatalf("leyendo el lease persistido: %v", err)
	}
	// El registro emite el inicial (counter=1) y el heartbeat de registro renueva
	// a heartbeatCounter+1 = 2. Que sea exacto no es casualidad: registrar ya
	// esperó a ver los DOS LeaseUpdate en el cliente.
	if counter != 2 {
		t.Fatalf("leases.counter = %d, quiero 2 (inicial + la renovación del heartbeat de registro)", counter)
	}
	if revoked {
		t.Fatal("el lease nació revocado: el arnés no puede medir nada con el Edge cortado")
	}
}

const (
	// latenciaPorConsulta es EL PARÁMETRO de T5.1: lo que el repositorio decorado
	// espera antes de delegar CADA llamada al fleet. Cambiar esta constante cambia
	// el escenario entero; nada más hay que tocar.
	latenciaPorConsulta = 20 * time.Millisecond
	// heartbeatsPorFase es el tamaño de la ráfaga de cada fase. Con la latencia de
	// arriba el suelo por fase es 200 ms: bastante para dominar el ruido de un
	// bufconn y de un Postgres local, y bastante poco para no alargar el CI.
	//
	// ⚠️ 2026-08-18 · Plan 050 · Ola 1: es TAMBIÉN cuántas SESIONES DISTINTAS abre
	// la fase, porque desde la enmienda de correrFase manda exactamente un latido a
	// cada una (dos a la misma sesión se coalescerían, D-050.4). El número no
	// cambia y el suelo tampoco.
	heartbeatsPorFase = 10
	// factorControl es cuántas veces más rápida tiene que ser la fase de CONTROL
	// (sin latencia inyectada) que la fase más rápida CON latencia. Es un umbral
	// RELATIVO a lo medido en la propia corrida, no un absoluto en milisegundos:
	// un absoluto en un test de integración con -race es flaky por construcción
	// (depende de la máquina), y lo que el criterio quiere afirmar es justamente
	// la RELACIÓN entre el control y las fases.
	factorControl = 3
)

// TestIntegration_CargaLatenciaInyectableEsReproducible es el criterio de T5.1:
// la latencia por consulta es un parámetro del test y el MISMO escenario da el
// mismo resultado dos veces seguidas.
//
// Estructura: se registran las sesiones con latencia 0 (el coste del MarkOnline no
// contamina ninguna medición), se corre una fase de CONTROL sin latencia y luego
// dos fases IDÉNTICAS con la latencia inyectada.
//
// ⚠️ ENMENDADO EL 2026-08-18 · Plan 050 · Ola 1 (decisión de Jhoan). La estructura
// decía «se registra la sesión» (UNA) y la fase mandaba sus n heartbeats a ESA
// sesión. Con el carril cableado, esos n latidos caen en una sola cola y la
// coalescencia (D-050.4) los sustituye en sitio: corrían ~2 jobs en vez de n y la
// fase quedaba muy por debajo del suelo. Ahora la fase se REPARTE en n sesiones
// distintas, una por latido. El suelo, la tolerancia, el factor de control y la
// latencia inyectada NO se han tocado: si se hubiera aflojado cualquiera de ellos,
// el test dejaría de decir lo que dice. El mecanismo completo, y el enunciado
// original que sustituye, están en correrFase.
//
// El lease va en MEMORIA a propósito (default del arnés): lo que aquí se
// estrangula y se mide es el camino del FLEET, y un segundo repositorio contra BD
// metería latencia real no controlada en la misma medida.
//
// Qué margen se considera aceptable y por qué: reproducible NO es determinista al
// nanosegundo. Aquí se sostienen tres cosas distintas:
//  1. un SUELO duro (fase ≥ n×d), que es aritmética y no estadística: cada
//     heartbeat de la fase va a una SESIÓN DISTINTA y no arranca hasta que el
//     anterior terminó, así que ninguno se coalesce con otro ni corre en paralelo
//     con otro, y todos pagan su espera entera. (Enmienda del 2026-08-18: esta
//     línea decía, literal, «el bucle Recv del Connect es serial y cada heartbeat
//     paga su espera entera». Con el carril de la Ola 1 el bucle Recv ya no
//     ejecuta ese trabajo —lo sirve una goroutine por sesión—, así que la
//     serialidad la ponen ahora el reparto en n sesiones y la barrera de
//     correrFase, no el bucle. El suelo sigue siendo el mismo: n×d.);
//  2. un TECHO relativo entre las dos fases idénticas (|A−B| ≤ 50 % del suelo),
//     que absorbe el jitter del planificador de Go, el sondeo de la barrera (hasta
//     un intervalo de sondeoBD de más POR HEARTBEAT) y la varianza de un UPDATE en
//     Postgres, pero se rompería si el resultado dependiera del orden o de un
//     estado acumulado. (Enmienda del 2026-08-18: decía «el sondeo de la BD (hasta
//     un intervalo de más por fase)». Con la fase repartida en n sesiones la espera
//     es POR LATIDO —n sondeos, no uno—, así que el ruido de cuantización que la
//     tolerancia tiene que absorber es hasta n×sondeoBD, no sondeoBD. Con n=10 y
//     sondeoBD=2 ms son 20 ms contra una tolerancia de 100 ms.);
//  3. que el CONTROL (sin latencia) sea una FRACCIÓN CLARA de las fases medidas
//     (1/factorControl), que es lo que demuestra que quien manda en la medida es
//     el parámetro y no la BD. Si esto falla, la máquina no sirve para la
//     medición de T5.2 y hay que saberlo.
func TestIntegration_CargaLatenciaInyectableEsReproducible(t *testing.T) {
	h := newLoadHarness(t, configArnes{drenarStream: true})

	// Una sesión por heartbeat de la fase (ver correrFase): así ningún latido
	// coalesce con otro. El sufijo por índice —el mismo molde que
	// registrarSesionesT52— evita que dos IDs consecutivos colisionen si el reloj
	// de nanosegundos no avanza entre dos llamadas.
	sesiones := make([]string, heartbeatsPorFase)
	for i := range sesiones {
		sesiones[i] = sessionIDUnico(fmt.Sprintf("t51-%02d", i))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*esperaMaxBD)
	defer cancel()

	// Registro con latencia 0: los MarkOnline quedan FUERA de toda medición.
	stream := h.registrar(ctx, t, sesiones)

	// El marcador de secuencia y la cuenta de LeaseUpdate se arrastran de fase en
	// fase. leases arranca en 2 por sesión: los que dejó el registro (inicial + la
	// renovación del heartbeat de registro), que registrar ya esperó. ronda arranca
	// en 1 —el marcador que usó el registro— para que el PRIMER latido de la primera
	// fase sea el 2 y ningún esperarUptime pueda darse por satisfecho con la fila
	// que dejó el registro. Se reutiliza el tipo de T5.2 —mismo paquete, mismo arnés
	// y misma contabilidad— en vez de clonar un struct de dos campos.
	m := &medicionT52{ronda: 1, leases: int64(2 * len(sesiones))}

	// Fase de control, todavía sin latencia inyectada.
	sinLatencia := h.correrFase(ctx, t, stream, sesiones, m)

	// A partir de aquí, cada consulta al fleet paga la latencia del parámetro.
	suelo := heartbeatsPorFase * latenciaPorConsulta
	h.slow.SetDelay(latenciaPorConsulta)

	faseA := h.correrFase(ctx, t, stream, sesiones, m)
	faseB := h.correrFase(ctx, t, stream, sesiones, m)

	t.Logf("control=%v faseA=%v faseB=%v (suelo=%v, %d heartbeats × %v, uno por cada una de %d sesiones)",
		sinLatencia, faseA, faseB, suelo, heartbeatsPorFase, latenciaPorConsulta, len(sesiones))

	// (3) El parámetro manda: sin latencia inyectada el MISMO escenario cuesta una
	// fracción de lo que cuesta con ella. Si no, la BD ya es tan lenta como el
	// parámetro elegido.
	menorFase := min(faseA, faseB)
	techoControl := menorFase / factorControl
	if sinLatencia >= techoControl {
		t.Fatalf("la fase de control tardó %v y la fase más rápida con latencia %v: el control debía quedar "+
			"por debajo de %v (1/%d de esa fase) y no lo hace, así que la BD pesa tanto como el parámetro y no manda; "+
			"sube latenciaPorConsulta o mide en una máquina menos cargada",
			sinLatencia, menorFase, techoControl, factorControl)
	}

	// (1) Suelo duro: n heartbeats × d de espera cada uno, en n sesiones distintas y
	// serializados por la barrera de correrFase (sin ella correrían en paralelo, una
	// goroutine por sesión, y la fase costaría ~d).
	for nombre, dur := range map[string]time.Duration{"faseA": faseA, "faseB": faseB} {
		if dur < suelo {
			t.Fatalf("%s tardó %v, por debajo del suelo aritmético %v: la latencia inyectada no se está aplicando "+
				"a cada consulta", nombre, dur, suelo)
		}
	}

	// (2) Techo relativo entre las dos fases idénticas.
	tolerancia := suelo / 2
	if d := diferencia(faseA, faseB); d > tolerancia {
		t.Fatalf("faseA=%v y faseB=%v difieren en %v, más de la tolerancia %v: el escenario no es reproducible",
			faseA, faseB, d, tolerancia)
	}
}

// diferencia devuelve el valor absoluto de a-b.
func diferencia(a, b time.Duration) time.Duration {
	if a > b {
		return a - b
	}
	return b - a
}
