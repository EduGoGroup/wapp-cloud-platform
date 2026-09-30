package arranque

// huella_vieja_test.go es el ÚNICO fichero que la reconstrucción modular añade al arranque
// viejo (F0 · T0.14, decisión D-F0-2), y es de test: no cambia producción ni lo que corre en
// UAT, y muere con este paquete en F10.
//
// Mide la huella de EJECUCIÓN de este arranque (rutas por listener, rpc, familias wapp_* de
// /metrics, en los dos perfiles) y la compara con la dorada
// internal/arranque/testdata/huella.json. Con `-args -actualizar` la REESCRIBE: es el único
// que puede hacerlo (R0.5.g). El candado del arranque nuevo (internal/arranque/huella_test.go)
// arma el mismo contenedor, compara con la misma dorada y no la escribe nunca.
//
// Si alguien cambia este arranque (un arreglo «hecho dos veces», 05 §9.1), este test falla
// hasta regenerar la dorada, y entonces falla el del nuevo hasta que recibe lo mismo.
//
//	go test -run '^TestHuellaVieja$' ./internal/bootstrap/arranque/ -args -actualizar

import (
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"errors"
	"flag"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/arranque/huellatest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	iamidentity "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/identity"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
)

// actualizar reescribe la dorada con la huella de ESTE arranque en vez de compararla.
var actualizar = flag.Bool("actualizar", false, "reescribe internal/arranque/testdata/huella.json con la huella del arranque viejo")

const (
	// raizDelRepo es la raíz del módulo vista desde este paquete.
	raizDelRepo = "../../.."
	// doradaDeHuella es la dorada, que vive con el arranque nuevo (diseno.md §6.4).
	doradaDeHuella = "../../arranque/testdata/huella.json"
)

// TestHuellaVieja mide la huella de ejecución del arranque viejo y la compara con la dorada
// (o la reescribe con -actualizar).
func TestHuellaVieja(t *testing.T) {
	hs := huellasDeEjecucion(t)
	if *actualizar {
		if err := huellatest.Escribir(doradaDeHuella, hs); err != nil {
			t.Fatalf("huella: escribiendo la dorada: %v", err)
		}
		t.Logf("huella: dorada reescrita en %s (%d perfiles)", doradaDeHuella, len(hs))
		return
	}
	compararConLaDorada(t, hs)
}

// ─── Contenedor de huella (diseno.md §6.3) ───────────────────────────────────────────────
//
// 🔴 ESTE BLOQUE ES EL MISMO TEXTO en internal/bootstrap/arranque/huella_vieja_test.go y en
// internal/arranque/huella_test.go: el tipo contenedor es privado a cada paquete y no se
// puede compartir, así que se escribe dos veces. Una diferencia entre las dos copias es un
// defecto de la huella, no una adaptación (compárense con `diff`).
//
// Arma el arranque SIN red y SIN Postgres, con las fases 2–8 REALES:
//
//   - prólogo: config.Load() con un entorno WAPP_* limpio y fijado aquí (listeners gRPC en
//     127.0.0.1:0, rate-limit del :8103 altísimo para que las sondas no den 429);
//   - fase 1: SIMULADA — setupDatabase migra contra Postgres y loadPKI lee ficheros. En su
//     lugar: *sql.DB perezoso que nunca conecta, metrics.New() + RegisterDBStats, CA de
//     desarrollo en memoria, y buildLeaseManager y buildEnrollServer reales;
//   - fases 2–8: las de `fases`, por construir() (que comprueba cada requiere()). La fase 3
//     corre ENTERA: su única salida a la red, el HeadBucket de R2 (flows.go), va contra un
//     S3 falso dentro del proceso (httptest; con endpoint IP el SDK usa path-style solo,
//     D-F9-2), y el keyring es uno de prueba por WAPP_KEK_MASTER_B64 (la KEK del envelope
//     de PII de negocio, no la DEK del ADR-0007: homónimo, reglas.md trampa 12);
//   - fase 9: NO se ejecuta (sus Run tocarían la BD); sus `go` los cubre la parte estática.

// perfilesDeHuella son las dos configuraciones que la huella compara: «minimo» (sin
// identity: signup y exchange a 503) y «con-m2m» (cliente M2M de identity presente:
// POST /api/v1/signup se registra por la otra rama, http.go:165/168, trampa 8).
var perfilesDeHuella = []string{"minimo", "con-m2m"}

// contenedorDeHuella devuelve el contenedor con las fases 1 (simulada) a 8 (reales) hechas.
func contenedorDeHuella(t *testing.T, perfil string) *contenedor {
	t.Helper()
	entornoDeHuella(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("huella: config.Load: %v", err)
	}
	c := nuevoContenedor(cfg, quietLogger())
	t.Cleanup(c.cerrar)

	infraestructuraDeHuella(t, c)
	// Fase 2 sola: el perfil con M2M inyecta el cliente entre la 2 y la 8. Por entorno no se
	// puede: WAPP_IDENTITY_URL enciende también la delegación, que exige el JWKS (red).
	if err := construir(t.Context(), c, fases[1:2]); err != nil {
		t.Fatalf("huella: %v", err)
	}
	if perfil == "con-m2m" {
		m2m, err := iamidentity.NewM2M("http://127.0.0.1:1", "clave-de-huella", time.Second)
		if err != nil {
			t.Fatalf("huella: NewM2M: %v", err)
		}
		c.authStk.m2mClient = m2m
	}
	if err := construir(t.Context(), c, fases[2:8]); err != nil {
		t.Fatalf("huella: %v", err)
	}
	t.Cleanup(func() {
		c.enrollGS.Stop()
		c.connectGS.Stop()
		for _, l := range []net.Listener{c.enrollLis, c.connectLis} {
			if err := l.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				t.Logf("huella: cerrando listener: %v", err)
			}
		}
	})
	return c
}

// entornoDeHuella deja fuera todo WAPP_* heredado del shell (el loader usa LookupEnv: una
// clave vacía EXISTE) y fija el entorno de la huella. t.Setenv restaura al terminar.
func entornoDeHuella(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		clave, valor, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(clave, config.EnvPrefix) {
			continue
		}
		t.Setenv(clave, valor)
		if err := os.Unsetenv(clave); err != nil {
			t.Fatalf("huella: desfijando %s: %v", clave, err)
		}
	}
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // HeadBucket: el bucket existe
	}))
	t.Cleanup(s3.Close)
	for clave, valor := range map[string]string{
		"APP_ENV":                      "dev",
		"HTTP_ADDR":                    "127.0.0.1:0",
		"PUBLIC_HTTP_ADDR":             "127.0.0.1:0",
		"GRPC_ENROLL_ADDR":             "127.0.0.1:0",
		"GRPC_CONNECT_ADDR":            "127.0.0.1:0",
		"RATELIMIT_PUBLIC_RPS":         "1000000",
		"RATELIMIT_PUBLIC_BURST":       "1000000",
		"KEK_PROVIDER":                 "env",
		"KEK_MASTER_B64":               base64.StdEncoding.EncodeToString([]byte("kek-de-prueba-de-la-huella-32-by")),
		"KEK_INDEX_B64":                base64.StdEncoding.EncodeToString([]byte("indice-de-prueba-de-la-huella-32")),
		"STORAGE_S3_REGION":            "auto",
		"STORAGE_S3_BUCKET":            "huella",
		"STORAGE_S3_ACCESS_KEY_ID":     "huella",
		"STORAGE_S3_SECRET_ACCESS_KEY": "huella",
		"STORAGE_S3_ENDPOINT":          s3.URL,
	} {
		t.Setenv(config.EnvPrefix+clave, valor)
	}
}

// infraestructuraDeHuella es la fase 1 sin red: lo mismo que faseInfraestructura deja en el
// contenedor, con la base perezosa y la PKI en memoria.
func infraestructuraDeHuella(t *testing.T, c *contenedor) {
	t.Helper()
	c.mtx = metrics.New()
	// Perezoso: sql.Open no conecta. Puerto 1 para que un acceso accidental falle rápido.
	db, err := sql.Open("pgx", "host=127.0.0.1 port=1 user=huella dbname=huella sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatalf("huella: sql.Open: %v", err)
	}
	c.db = db
	if err := c.mtx.RegisterDBStats(c.db); err != nil {
		t.Fatalf("huella: RegisterDBStats: %v", err)
	}
	ca, err := enroll.NewDevCA("wapp-huella-ca", time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("huella: NewDevCA: %v", err)
	}
	certPEM, keyPEM, err := ca.IssueServerCert("localhost", []string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("huella: IssueServerCert: %v", err)
	}
	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("huella: X509KeyPair: %v", err)
	}
	c.ca, c.serverCert = ca, serverCert
	leaseMgr, err := buildLeaseManager(c.cfg, c.db, c.log)
	if err != nil {
		t.Fatalf("huella: buildLeaseManager: %v", err)
	}
	c.leaseMgr = leaseMgr
	enrollSrv, cloudEncPriv, err := buildEnrollServer(c.cfg, c.db, c.ca, c.leaseMgr.PublicKey(), c.log)
	if err != nil {
		t.Fatalf("huella: buildEnrollServer: %v", err)
	}
	c.enrollSrv, c.cloudEncPriv = enrollSrv, cloudEncPriv
	c.marca("db", "metricas", "pki", "lease", "enroll")
}

// candidatosDeHuella son los patrones que se sondean, los MISMOS y en el MISMO orden en los
// dos lados: los literales de Handle/HandleFunc de todo internal/ (el árbol viejo y el
// nuevo, que cuelga de él) más los de la dorada. Así una ruta de más en cualquier lado es
// candidata y aparece como «sobra»; una que desaparece del código sigue en la dorada y
// aparece como «falta».
func candidatosDeHuella(t *testing.T) []string {
	t.Helper()
	cand, err := huellatest.Candidatos(raizDelRepo, "internal")
	if err != nil {
		t.Fatalf("huella: Candidatos: %v", err)
	}
	vistos := make(map[string]bool, len(cand))
	for _, c := range cand {
		vistos[c] = true
	}
	dorada, err := huellatest.Leer(doradaDeHuella)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("huella: leyendo la dorada: %v", err)
	}
	for _, h := range dorada {
		for _, rutas := range h.Rutas {
			for _, r := range rutas {
				vistos[r] = true
			}
		}
	}
	todos := make([]string, 0, len(vistos))
	for c := range vistos {
		todos = append(todos, c)
	}
	sort.Strings(todos)
	return todos
}

// huellaDeEjecucion arma el contenedor del perfil y mide su superficie: primero las sondas
// del :8100 y del :8103 (en ese orden), después /metrics (las familias CounterVec aparecen
// con su primer incremento: el orden es parte de la medida).
func huellaDeEjecucion(t *testing.T, perfil string, candidatos []string) huellatest.Huella {
	t.Helper()
	c := contenedorDeHuella(t, perfil)
	h := huellatest.Huella{
		Perfil: perfil,
		Rutas: map[string][]string{
			":8100": huellatest.Rutas(c.httpSrv.Handler, candidatos),
			":8103": huellatest.Rutas(c.publicSrv.Handler, candidatos),
		},
		RPC: map[string][]string{
			":8101": huellatest.RPC(c.connectGS),
			":8102": huellatest.RPC(c.enrollGS),
		},
	}
	metricas, err := huellatest.Metricas(c.httpSrv.Handler)
	if err != nil {
		t.Fatalf("huella: Metricas: %v", err)
	}
	h.Metricas = metricas
	return h
}

// huellasDeEjecucion es la huella de ejecución de este arranque, un elemento por perfil.
func huellasDeEjecucion(t *testing.T) []huellatest.Huella {
	t.Helper()
	candidatos := candidatosDeHuella(t)
	hs := make([]huellatest.Huella, 0, len(perfilesDeHuella))
	for _, p := range perfilesDeHuella {
		hs = append(hs, huellaDeEjecucion(t, p, candidatos))
	}
	return hs
}

// compararConLaDorada falla con una línea por diferencia, nombrando el perfil.
func compararConLaDorada(t *testing.T, tiene []huellatest.Huella) {
	t.Helper()
	quiere, err := huellatest.Leer(doradaDeHuella)
	if err != nil {
		t.Fatalf("huella: leyendo la dorada %s: %v (solo TestHuellaVieja -args -actualizar la escribe)", doradaDeHuella, err)
	}
	porPerfil := make(map[string]huellatest.Huella, len(quiere))
	for _, h := range quiere {
		porPerfil[h.Perfil] = h
	}
	if len(quiere) != len(tiene) {
		t.Errorf("huella: la dorada tiene %d perfiles y este arranque %d", len(quiere), len(tiene))
	}
	for _, h := range tiene {
		q, ok := porPerfil[h.Perfil]
		if !ok {
			t.Errorf("huella: el perfil %q no está en la dorada", h.Perfil)
			continue
		}
		for _, linea := range huellatest.Diferencia(q, h) {
			t.Errorf("huella [%s]: %s", h.Perfil, linea)
		}
		t.Logf("huella [%s]: :8100=%d :8103=%d rpc=%d metricas=%d", h.Perfil,
			len(h.Rutas[":8100"]), len(h.Rutas[":8103"]), len(h.RPC[":8101"])+len(h.RPC[":8102"]), len(h.Metricas))
	}
}
