//go:build integracion && edgereal

package procesos

// El modo «Edge real» del arnés (fase FW, D-FW-5): junto al Edge falso —que no se retira— este
// fichero lanza el binario de verdad, `wapp-edge-agent`, con el transporte de WhatsApp falso
// (ADR-0049) contra el servidor del arnés.
//
// Tres cosas lo separan del resto del paquete, y por eso lleva una SEGUNDA etiqueta (`edgereal`):
//
//   - Necesita el repo del Edge en disco (WAPP_PROCESOS_EDGE_DIR). Este repo se compila solo, y
//     `make test-procesos` tiene que seguir dando lo mismo sin ese repo al lado. Sin la variable el
//     caso FALLA: no se salta, porque un SKIP no es verde.
//   - El Edge corre en un CONTENEDOR LINUX (D-FW-7): ahí no hay llavero, y la custodia de la DEK cae
//     sola en su último recurso por el camino que ya existe. No se toca la custodia para probar.
//   - Se conduce por su plano de control, que es un socket unix DENTRO del contenedor: cada llamada
//     es un `curl --unix-socket` lanzado con Exec.
//
// El servidor del arnés escucha en el loopback del anfitrión. El contenedor llega a él por el túnel
// de testcontainers (WithHostPortAccess), que publica esos puertos como
// `host.testcontainers.internal`; el certificado del servidor se emite para `localhost`, así que el
// Edge verifica ese nombre (WAPP_AGENT_CLOUDLINK_SERVER_NAME) y no el del túnel.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

const (
	// edgeRealDirEnv es la variable con la ruta del repo del Edge en disco.
	edgeRealDirEnv = "WAPP_PROCESOS_EDGE_DIR"
	// edgeRealModule es el módulo que tiene que haber en esa ruta.
	edgeRealModule = "github.com/EduGoGroup/wapp-edge-agent"
	// edgeRealImage es la imagen del contenedor: Alpine con curl, sin nada del Edge dentro. El
	// binario se copia al crearlo. Corre como su usuario sin privilegios (curl_user).
	edgeRealImage = "curlimages/curl:8.11.1"
	// edgeRealBin, edgeRealCA, edgeRealData y edgeRealSocket son las rutas DENTRO del contenedor.
	edgeRealBin    = "/opt/edge/agent"
	edgeRealCA     = "/opt/edge/ca.crt"
	edgeRealData   = "/home/curl_user/edge"
	edgeRealSocket = edgeRealData + "/wapp-edge.sock"
	// edgeRealShout es el trozo de la línea con la que el daemon grita la palanca al arrancar
	// (internal/infra/daemon/daemon.go del Edge). Si no sale, lo que corre no es el WhatsApp falso.
	edgeRealShout = "PALANCA ECHADA (WAPP_AGENT_WHATSAPP_FALSO=true)"
	// edgeRealHealthyLink es la línea con la que el Edge dice que su CloudLink arrancó entero, y
	// edgeRealGate y edgeRealSealed los dos campos que el caso exige en ella: sin validador de lease
	// el Edge envía con el lease revocado, y sin la pública de la nube sube los entrantes en claro.
	edgeRealHealthyLink = "CloudLink habilitado (multi-sesión)"
	edgeRealGate        = "lease_gate=true"
	edgeRealSealed      = "sealed_transit=true"
	edgeRealShadowOff   = "lease_shadow_mode=false"

	edgeRealBuildTimeout = 10 * time.Minute
	edgeRealStartTimeout = 3 * time.Minute
	edgeRealCallTimeout  = 30 * time.Second
	edgeRealReadyTimeout = 60 * time.Second
)

// edgeRealBuilt es el binario del Edge de la corrida: se compila una vez, la primera vez que un
// caso lo pide.
var edgeRealBuilt struct {
	once     sync.Once
	path     string // el binario linux, en el directorio de la corrida
	dir      string // el repo del Edge
	revision string // lo que el binario dirá en /v1/health: la revisión de git, con «-dirty» si el árbol tiene cambios
	err      error
}

// edgeRealBinary devuelve el binario linux del Edge, su repo y su revisión; lo compila la primera
// vez. Falla (t.Fatalf) si WAPP_PROCESOS_EDGE_DIR falta, no es el repo del Edge o no compila.
func edgeRealBinary(t *testing.T) (path, dir, revision string) {
	t.Helper()
	edgeRealBuilt.once.Do(func() {
		edgeRealBuilt.dir, edgeRealBuilt.revision, edgeRealBuilt.err = edgeRealSource(os.Getenv(edgeRealDirEnv))
		if edgeRealBuilt.err != nil {
			return
		}
		edgeRealBuilt.path = filepath.Join(filepath.Dir(rutaBinario("migrate")), "edge-agent-linux")
		edgeRealBuilt.err = edgeRealCompile(edgeRealBuilt.dir, edgeRealBuilt.revision, edgeRealBuilt.path)
	})
	if edgeRealBuilt.err != nil {
		t.Fatalf("el binario del Edge real: %v", edgeRealBuilt.err)
	}
	return edgeRealBuilt.path, edgeRealBuilt.dir, edgeRealBuilt.revision
}

// edgeRealSource comprueba que dir es el repo del Edge y devuelve su ruta absoluta y su revisión.
func edgeRealSource(dir string) (abs, revision string, err error) {
	if dir == "" {
		return "", "", fmt.Errorf("falta %s: la ruta del repo edge/wapp-edge-agent. El modo «Edge real» no se salta sin él", edgeRealDirEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if abs, err = filepath.Abs(dir); err != nil {
		return "", "", fmt.Errorf("%s=%q: %w", edgeRealDirEnv, dir, err)
	}
	module, err := edgeRealRun(abs, exec.CommandContext(ctx, "go", "list", "-m"))
	if err != nil {
		return "", "", fmt.Errorf("%s=%q no es un módulo de Go: %w", edgeRealDirEnv, abs, err)
	}
	if module != edgeRealModule {
		return "", "", fmt.Errorf("%s=%q es el módulo %q, quería %q", edgeRealDirEnv, abs, module, edgeRealModule)
	}
	if revision, err = edgeRealRun(abs, exec.CommandContext(ctx, "git", "rev-parse", "HEAD")); err != nil {
		return "", "", fmt.Errorf("la revisión de git de %q: %w", abs, err)
	}
	changes, err := edgeRealRun(abs, exec.CommandContext(ctx, "git", "status", "--porcelain", "--untracked-files=no"))
	if err != nil {
		return "", "", fmt.Errorf("el estado de git de %q: %w", abs, err)
	}
	if changes != "" {
		revision += "-dirty"
	}
	return abs, revision, nil
}

// edgeRealCompile compila ./cmd/agent del Edge para linux, sin CGO (el Edge es Go puro en Linux),
// con la revisión grabada en main.Version: es lo que /v1/health devuelve y lo que el caso afirma.
func edgeRealCompile(dir, revision, dest string) error {
	ctx, cancel := context.WithTimeout(context.Background(), edgeRealBuildTimeout)
	defer cancel()
	// cmd.Env se queda en nil a propósito, como en compilar (main_test.go): go build necesita el
	// entorno de Go del desarrollador (GOWORK=off y GOTOOLCHAIN los pone `make`). El destino linux se
	// añade con env(1), sin leer ni copiar ese entorno. El Edge que CORRE no hereda nada de aquí: su
	// entorno se arma desde cero en edgeRealEnv.
	cmd := exec.CommandContext(ctx, "env", "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0", //nolint:gosec // argumentos del arnés (la revisión y el destino los calcula él), sin entrada externa
		"go", "build", "-trimpath", "-ldflags", "-X main.Version="+revision, "-o", dest, "./cmd/agent")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build ./cmd/agent en %s: %w\n%s", dir, err, out)
	}
	return nil
}

// edgeRealRun corre en dir un comando corto ya armado y devuelve su salida sin el salto final.
func edgeRealRun(dir string, cmd *exec.Cmd) (string, error) {
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("%s: %w\n%s", strings.Join(cmd.Args, " "), err, exit.Stderr)
		}
		return "", fmt.Errorf("%s: %w", strings.Join(cmd.Args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// edgeReal es un Edge de verdad corriendo en su contenedor, ya enrolado contra el servidor s.
type edgeReal struct {
	EdgeID   string
	Dir      string // el repo del que salió el binario
	Revision string // la revisión que el binario dice tener

	ctr *testcontainers.DockerContainer
}

// edgeRealStart enrola y arranca el Edge real contra s con el código de enrolamiento dado, y vuelve
// cuando su plano de control responde. Lo PRIMERO que afirma es qué binario corre: la revisión de
// /v1/health es la del repo de WAPP_PROCESOS_EDGE_DIR, y el daemon gritó la palanca del WhatsApp
// falso y arrancó su CloudLink con validador de lease, sellado y sin modo sombra. Falla (t.Fatalf)
// si algo de eso no se da. El contenedor se para al terminar el test.
func edgeRealStart(t *testing.T, s *servidor, code string) *edgeReal {
	t.Helper()
	bin, dir, revision := edgeRealBinary(t)
	e := &edgeReal{EdgeID: "edge-real-" + edgeAleatorioHex(t, 6), Dir: dir, Revision: revision}
	enroll, connect := edgeRealPort(t, s.EnrolarAddr), edgeRealPort(t, s.ConectarAddr)

	ctx, cancel := context.WithTimeout(context.Background(), edgeRealStartTimeout)
	defer cancel()
	ctr, err := testcontainers.Run(ctx, edgeRealImage,
		testcontainers.WithHostPortAccess(enroll, connect),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{HostFilePath: bin, ContainerFilePath: edgeRealBin, FileMode: 0o755},
			testcontainers.ContainerFile{HostFilePath: s.PKI.CACertFile, ContainerFilePath: edgeRealCA, FileMode: 0o644},
		),
		testcontainers.WithEnv(edgeRealEnv(e.EdgeID, code, enroll, connect)),
		testcontainers.WithEntrypoint("sh", "-c"),
		testcontainers.WithCmd(edgeRealScript(enroll)),
	)
	e.ctr = ctr
	t.Cleanup(func() { e.stop(t) })
	if err != nil {
		t.Fatalf("lanzar el contenedor del Edge real: %v\n%s", err, e.Log())
	}

	var health struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	edgeEsperar(t, edgeRealReadyTimeout, "que el plano de control del Edge real responda", func() bool {
		if e.exited() {
			t.Fatalf("el Edge real terminó antes de responder\n%s", e.Log())
		}
		code, body, callErr := e.call(context.Background(), "GET", "/v1/health", "", nil)
		return callErr == nil && code == 200 && json.Unmarshal(body, &health) == nil && health.Status == "ok"
	})
	if health.Version != revision {
		t.Fatalf("el Edge que corre dice ser %q; el repo %s está en %q: no es el binario que se compiló", health.Version, dir, revision)
	}
	t.Logf("Edge real: %s @ %s (linux/%s), edge_id %s", dir, revision, runtime.GOARCH, e.EdgeID)
	log := e.Log()
	for _, want := range []string{edgeRealShout, edgeRealHealthyLink} {
		if !strings.Contains(log, want) {
			t.Fatalf("el log del Edge real no trae %q\n%s", want, log)
		}
	}
	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, edgeRealHealthyLink) {
			continue
		}
		for _, want := range []string{edgeRealGate, edgeRealSealed, edgeRealShadowOff} {
			if !strings.Contains(line, want) {
				t.Fatalf("el CloudLink del Edge real arrancó sin %s: %s", want, line)
			}
		}
	}
	return e
}

// edgeRealScript es lo que corre el contenedor: espera a que el puerto de enrolamiento del servidor
// responda, enrola y sirve.
//
//   - La espera no es de cortesía: el túnel hacia el anfitrión se monta DESPUÉS de arrancar el
//     contenedor, y `agent enroll` no reintenta (medido: con dos casos en paralelo el enrolamiento
//     llegaba antes que el túnel y moría con «connection refused»). Tiene tope: 30 s.
//   - Enrolar ANTES de servir: el daemon decide su dial mTLS al arrancar.
func edgeRealScript(enroll int) string {
	wait := fmt.Sprintf(`i=0; until nc -z -w 1 %s %d; do i=$((i+1)); if [ "$i" -gt 150 ]; then echo "el tunel hacia el servidor del arnes no responde"; exit 1; fi; sleep 0.2; done`,
		testcontainers.HostInternal, enroll)
	return wait + "; " + edgeRealBin + " enroll && exec " + edgeRealBin + " serve"
}

// edgeRealEnv es el entorno del Edge: dónde guarda sus cosas, por dónde llega al servidor del arnés
// y las dos palancas de prueba. Las rutas de las dos claves públicas van puestas para que `enroll`
// las escriba; el modo sombra del lease se deja en su valor por defecto (apagado) sin nombrarlo.
func edgeRealEnv(edgeID, code string, enroll, connect int) map[string]string {
	host := func(port int) string { return net.JoinHostPort(testcontainers.HostInternal, strconv.Itoa(port)) }
	return map[string]string{
		"WAPP_AGENT_DATA_DIR":                        edgeRealData,
		"WAPP_AGENT_CONTROL_SOCKET_PATH":             edgeRealSocket,
		"WAPP_AGENT_CLOUDLINK_ENROLLMENT_ENDPOINT":   host(enroll),
		"WAPP_AGENT_CLOUDLINK_ENDPOINT":              host(connect),
		"WAPP_AGENT_CLOUDLINK_SERVER_NAME":           edgeNombreServidor,
		"WAPP_AGENT_CLOUDLINK_TLS_CA":                edgeRealCA,
		"WAPP_AGENT_CLOUDLINK_TLS_CERT":              edgeRealData + "/edge.crt",
		"WAPP_AGENT_CLOUDLINK_TLS_KEY":               edgeRealData + "/edge.key",
		"WAPP_AGENT_CLOUDLINK_LEASE_PUBKEY_PATH":     edgeRealData + "/lease.pub",
		"WAPP_AGENT_CLOUDLINK_CLOUD_ENC_PUBKEY_PATH": edgeRealData + "/cloud-enc.pub",
		"WAPP_AGENT_CLOUDLINK_EDGE_ID":               edgeID,
		"WAPP_AGENT_CLOUDLINK_ACTIVATION_CODE":       code,
		"WAPP_AGENT_WHATSAPP_FALSO":                  "true",
		"WAPP_AGENT_INYECTOR_ENTRANTES":              "true",
	}
}

// edgeRealPort saca el puerto de una dirección «host:puerto» del servidor del arnés.
func edgeRealPort(t *testing.T, addr string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("la dirección %q del servidor: %v", addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("el puerto de %q: %v", addr, err)
	}
	return n
}

// call hace una petición al plano de control del Edge (un `curl --unix-socket` dentro del
// contenedor) y devuelve el código HTTP y el cuerpo. token vacío va sin Authorization; body nil,
// sin cuerpo. No falla el test: devuelve el error.
func (e *edgeReal) call(ctx context.Context, method, path, token string, body any) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, edgeRealCallTimeout)
	defer cancel()
	args := []string{"curl", "-sS", "--unix-socket", edgeRealSocket, "-X", method, "-w", "\n%{http_code}"}
	if token != "" {
		args = append(args, "-H", "Authorization: Bearer "+token)
	}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("serializar el cuerpo de %s %s: %w", method, path, err)
		}
		args = append(args, "-H", "Content-Type: application/json", "-d", string(raw))
	}
	exit, out, err := e.ctr.Exec(ctx, append(args, "http://unix"+path), tcexec.Multiplexed())
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	raw, err := io.ReadAll(out)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: leer la salida: %w", method, path, err)
	}
	if exit != 0 {
		return 0, nil, fmt.Errorf("%s %s: curl salió con %d: %s", method, path, exit, bytes.TrimSpace(raw))
	}
	cut := bytes.LastIndexByte(raw, '\n')
	if cut < 0 {
		return 0, nil, fmt.Errorf("%s %s: salida sin código HTTP: %q", method, path, raw)
	}
	status, err := strconv.Atoi(strings.TrimSpace(string(raw[cut+1:])))
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: código HTTP ilegible en %q", method, path, raw)
	}
	return status, raw[:cut], nil
}

// run corre un comando dentro del contenedor, con el usuario del daemon, y devuelve su salida sin
// espacios alrededor. No falla el test: devuelve el error si el comando no pudo correr o salió ≠ 0.
func (e *edgeReal) run(ctx context.Context, cmd ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, edgeRealCallTimeout)
	defer cancel()
	exit, out, err := e.ctr.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.Join(cmd, " "), err)
	}
	raw, err := io.ReadAll(out)
	if err != nil {
		return "", fmt.Errorf("%s: leer la salida: %w", strings.Join(cmd, " "), err)
	}
	if exit != 0 {
		return "", fmt.Errorf("%s: salió con %d: %s", strings.Join(cmd, " "), exit, bytes.TrimSpace(raw))
	}
	return strings.TrimSpace(string(raw)), nil
}

// Log devuelve lo que el Edge lleva escrito (stdout y stderr del contenedor). Vacío si no hay
// contenedor o no se pudo leer.
func (e *edgeReal) Log() string {
	if e.ctr == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), edgeRealCallTimeout)
	defer cancel()
	r, err := e.ctr.Logs(ctx)
	if err != nil {
		return "(no se pudo leer el log del Edge real: " + err.Error() + ")"
	}
	defer func() { _ = r.Close() }()
	raw, err := io.ReadAll(r)
	if err != nil {
		return "(no se pudo leer el log del Edge real: " + err.Error() + ")"
	}
	return string(raw)
}

// exited dice si el contenedor ya no corre.
func (e *edgeReal) exited() bool {
	if e.ctr == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), edgeRealCallTimeout)
	defer cancel()
	st, err := e.ctr.State(ctx)
	return err == nil && !st.Running
}

// stop para el contenedor. Si el test falló, vuelca antes el log del Edge.
func (e *edgeReal) stop(t *testing.T) {
	if e.ctr == nil {
		return
	}
	if t.Failed() {
		t.Logf("log del Edge real (%s):\n%s", e.EdgeID, e.Log())
	}
	if err := testcontainers.TerminateContainer(e.ctr); err != nil {
		t.Errorf("parar el contenedor del Edge real: %v", err)
	}
}
