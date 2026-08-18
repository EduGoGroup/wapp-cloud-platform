package gatewaygrpc_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
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
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
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
}

// loadHarness levanta el Server con mTLS + lease (memoria o Postgres, según
// configArnes) + fleet sobre POSTGRES REAL, envuelto en el decorador de latencia
// inyectable (T5.1), sobre un bufconn.Listener; y expone el cliente CloudLink ya
// conectado con el cert del Edge.
type loadHarness struct {
	db     *sql.DB
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
	var leaseRepo lease.Repository = lease.NewMemoryRepository()
	if cfg.leasePostgres {
		leaseRepo = lease.NewPostgresRepository(db)
	}
	mgr, err := lease.NewManager(priv, leaseRepo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// El fleet REAL (Postgres) decorado con la latencia inyectable.
	slow := fleettest.NewSlow(fleet.NewPostgresRepository(db), cfg.latenciaInicial)

	reg := session.NewRegistry()
	logBuf := &syncBuffer{}
	log := logger.New(logger.WithWriter(logBuf))
	srv := gatewaygrpc.New(reg, log, gatewaygrpc.WithLease(mgr), gatewaygrpc.WithFleet(slow))

	h := &loadHarness{
		db:       db,
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
					"(el cliente no lo ve, el servidor se lo traga y sigue)", n, marca)
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

// correrFase manda n heartbeats consecutivos numerados desde+1 … desde+n y
// devuelve cuánto tardó en verse el ÚLTIMO persistido en Postgres. El bucle Recv
// del Connect es SERIAL (una sola goroutine por stream), así que con una latencia
// d por consulta el suelo aritmético de la fase es n×d.
func (h *loadHarness) correrFase(ctx context.Context, t *testing.T, stream edgeSender, sessionID string, desde, n int64) time.Duration {
	t.Helper()
	inicio := time.Now()
	for i := int64(1); i <= n; i++ {
		if err := stream.Send(heartbeatCarga(sessionID, desde+i)); err != nil {
			t.Fatalf("Send heartbeat #%d: %v", desde+i, err)
		}
	}
	h.esperarUptime(ctx, t, sessionID, desde+n)
	return time.Since(inicio)
}

// registrar abre el stream, deja la sesión registrada (online en Postgres) y
// devuelve el stream ya drenándose. El registro se hace SIEMPRE con la latencia
// que tenga el arnés en ese momento; los tests que miden lo hacen con latencia 0
// para que el coste del MarkOnline no entre en la medición de las fases.
//
// (ctx va primero por la regla context-as-argument de revive, activa en
// .golangci.yml también para los tests.)
func (h *loadHarness) registrar(ctx context.Context, t *testing.T, sessionID string) edgeSender {
	t.Helper()
	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if h.cfg.drenarStream {
		h.drenar(stream)
	}
	if err := stream.Send(heartbeatCarga(sessionID, 1)); err != nil {
		t.Fatalf("Send heartbeat de registro: %v", err)
	}
	h.esperarUptime(ctx, t, sessionID, 1)
	if h.cfg.drenarStream {
		// El lease se MIRA: el registro empuja el LeaseUpdate inicial
		// (onSessionRegistered) y el heartbeat de registro fuerza una renovación
		// (route → renewLease). Son dos, y si no llegan el lease está roto.
		h.esperarLeases(t, 2)
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

	h.registrar(ctx, t, sid)

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

	h.registrar(ctx, t, sid)

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
// Estructura: se registra la sesión con latencia 0 (el coste del MarkOnline no
// contamina ninguna medición), se corre una fase de CONTROL sin latencia y luego
// dos fases IDÉNTICAS con la latencia inyectada.
//
// El lease va en MEMORIA a propósito (default del arnés): lo que aquí se
// estrangula y se mide es el camino del FLEET, y un segundo repositorio contra BD
// metería latencia real no controlada en la misma medida.
//
// Qué margen se considera aceptable y por qué: reproducible NO es determinista al
// nanosegundo. Aquí se sostienen tres cosas distintas:
//  1. un SUELO duro (fase ≥ n×d), que es aritmética y no estadística: el bucle
//     Recv del Connect es serial y cada heartbeat paga su espera entera;
//  2. un TECHO relativo entre las dos fases idénticas (|A−B| ≤ 50 % del suelo),
//     que absorbe el jitter del planificador de Go, el sondeo de la BD (hasta un
//     intervalo de más por fase) y la varianza de un UPDATE en Postgres, pero se
//     rompería si el resultado dependiera del orden o de un estado acumulado;
//  3. que el CONTROL (sin latencia) sea una FRACCIÓN CLARA de las fases medidas
//     (1/factorControl), que es lo que demuestra que quien manda en la medida es
//     el parámetro y no la BD. Si esto falla, la máquina no sirve para la
//     medición de T5.2 y hay que saberlo.
func TestIntegration_CargaLatenciaInyectableEsReproducible(t *testing.T) {
	h := newLoadHarness(t, configArnes{drenarStream: true})
	sid := sessionIDUnico("t51")

	ctx, cancel := context.WithTimeout(context.Background(), 5*esperaMaxBD)
	defer cancel()

	// Registro con latencia 0: el MarkOnline queda FUERA de toda medición.
	stream := h.registrar(ctx, t, sid)

	// Fase de control, todavía sin latencia inyectada.
	sinLatencia := h.correrFase(ctx, t, stream, sid, 1, heartbeatsPorFase)

	// A partir de aquí, cada consulta al fleet paga la latencia del parámetro.
	suelo := heartbeatsPorFase * latenciaPorConsulta
	h.slow.SetDelay(latenciaPorConsulta)

	faseA := h.correrFase(ctx, t, stream, sid, 1+heartbeatsPorFase, heartbeatsPorFase)
	faseB := h.correrFase(ctx, t, stream, sid, 1+2*heartbeatsPorFase, heartbeatsPorFase)

	t.Logf("control=%v faseA=%v faseB=%v (suelo=%v, %d heartbeats × %v)",
		sinLatencia, faseA, faseB, suelo, heartbeatsPorFase, latenciaPorConsulta)

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

	// (1) Suelo duro: n heartbeats seriales × d de espera cada uno.
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
