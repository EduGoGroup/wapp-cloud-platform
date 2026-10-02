//go:build integracion

package procesos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	// topeListo es lo que se espera, como máximo, a que un servidor recién arrancado quede listo:
	// /healthz del admin en 200 y los otros tres listeners aceptando conexiones.
	topeListo = 30 * time.Second
	// sondeoListo es cada cuánto se sondea la disponibilidad del servidor mientras arranca.
	sondeoListo = 100 * time.Millisecond
	// topeSondeoUno acota un solo sondeo (una petición HTTP o una conexión TCP).
	topeSondeoUno = 3 * time.Second
	// topeParadaServidor es lo que se espera a que el servidor salga tras SIGTERM antes de matarlo.
	topeParadaServidor = 15 * time.Second
	// esperaTrasMuerte acota cuánto se espera a que se vacíen las tuberías de salida tras la muerte
	// del proceso (exec.Cmd.WaitDelay): un nieto que heredara stdout no puede colgar Wait.
	esperaTrasMuerte = 5 * time.Second
	// lineasDeLog es cuántas líneas finales del log se vuelcan cuando algo falla.
	lineasDeLog = 200
	// bucketServidor es el bucket que el servidor de prueba cree tener; el S3 falso solo contesta
	// 200 al HEAD de este nombre.
	bucketServidor = "wapp-procesos"

	// textoPuertoOcupado es lo que escribe el runtime de Go cuando un listener no puede enlazar.
	textoPuertoOcupado = "address already in use"
	// textoParadaLimpia es la línea con la que el servidor confirma que cerró todo tras SIGTERM.
	textoParadaLimpia = "servidor detenido limpiamente"
)

// errSalioAntes marca que el proceso terminó antes de quedar listo (se envuelve con su código).
var errSalioAntes = errors.New("el servidor terminó antes de quedar listo")

var (
	// puertosMu protege puertosEntregados.
	puertosMu sync.Mutex
	// puertosEntregados son los puertos que reservarPuertos ya ha entregado en esta corrida: dos
	// servidores en paralelo del mismo proceso de test nunca reciben el mismo, aunque el kernel
	// reciclara uno que el primero ya liberó (la carrera con OTROS procesos la cubre el reintento).
	puertosEntregados = map[int]struct{}{}

	// elegirPuertos es el punto por el que arrancar pide sus cuatro direcciones. Es una variable
	// solo para que TestArnes_ReintentoPuertoOcupado fuerce una colisión; en el resto vale
	// reservarPuertos.
	elegirPuertos = reservarPuertos
)

// opcionesServidor son las perillas de arrancar. Proceso nombra la base y debe cumplir
// [a-z0-9_]+ (lo valida nuevaBase). SondeoWebhook, si es > 0, fija WAPP_WEBHOOK_POLL_INTERVAL
// (cada cuánto el worker mira webhook_outbox); MaxIntentosWebhook, si es > 0, fija
// WAPP_WEBHOOK_MAX_ATTEMPTS. En cero no se pone la variable y manda el valor por defecto.
type opcionesServidor struct {
	Proceso            string
	SondeoWebhook      time.Duration
	MaxIntentosWebhook int
}

// servidor es un servidor de la nube corriendo como subproceso real, con todo lo que necesita
// (base clonada, PKI, claves, S3 falso e identidad falsa) y las cuatro direcciones donde escucha.
// Lo crea arrancar; lo para Parar o, si el test no lo hace, el Cleanup que registra arrancar.
type servidor struct {
	Base      baseClonada
	PKI       pki
	Claves    claves
	S3        *s3Falso
	Identidad *identidad

	// AdminAddr es el HTTP admin (/healthz, /metrics, /admin/…), el :8100 de producción;
	// PublicaAddr es la API pública (:8103); EnrolarAddr y ConectarAddr son los dos gRPC
	// (:8102 enrolamiento, :8101 CloudLink). Todas "127.0.0.1:<puerto>".
	AdminAddr, PublicaAddr, EnrolarAddr, ConectarAddr string

	proceso *procesoHijo // el subproceso del intento vigente; nil mientras no se haya lanzado
	volcado bool         // true si el log ya se volcó en un mensaje de fallo (no repetirlo en el Cleanup)
}

// arrancar crea todo lo que un servidor necesita —la base clonada, la PKI, las claves, el S3
// falso y el JWKS de identidad, en ese orden— y arranca el binario elegido por TestMain como
// subproceso con un entorno construido DESDE CERO (nada de lo que tenga el test o el
// desarrollador llega al servidor). Espera a que esté listo: /healthz del admin en 200 y
// conexión aceptada en los otros tres listeners, con tope de 30 s. Recibe el test y las opciones
// y devuelve el servidor ya listo. Registra en t.Cleanup la parada (SIGTERM, 15 s, SIGKILL); como
// la base se crea primero, su DROP DATABASE corre después de parar el servidor.
//
// Falla (t.Fatalf) si el nombre de proceso no es válido, si el proceso termina o no responde
// antes de estar listo (con el código de salida y las últimas 200 líneas del log), o si tras un
// reintento por puerto ocupado vuelve a fallar. Si el servidor muere con «address already in use»
// —la carrera entre soltar un puerto libre y que el servidor lo enlace— reintenta UNA vez con
// puertos nuevos, y del intento fallido no queda rastro en lo que el test puede mirar: Log y
// LineasLog son solo del proceso vigente (cada intento tiene su búfer) y el doble de S3 olvida lo
// que le pidió el intento muerto (S3.forget), así que tras un reintento consta UN HeadBucket, no
// dos. La base clonada sí es la misma para los dos intentos. Si el test falla, el Cleanup vuelca
// las últimas 200 líneas del log con t.Logf.
func arrancar(t *testing.T, o opcionesServidor) *servidor {
	t.Helper()
	base := nuevaBase(t, o.Proceso) // la primera: su DROP DATABASE es el último Cleanup en correr
	s := &servidor{
		Base:      base,
		PKI:       nuevaPKI(t),
		Claves:    nuevasClaves(t),
		S3:        nuevoS3Falso(t, bucketServidor),
		Identidad: nuevaIdentidad(t),
	}
	home := t.TempDir() // HOME y directorio de trabajo del subproceso: vacío, sin ~/.aws ni nada
	t.Cleanup(func() { s.limpiar(t) })

	for intento := 1; ; intento++ {
		listoEn, err := s.lanzar(t.Context(), o, home)
		if err == nil {
			t.Logf("servidor %q (%s) listo en %s: admin=%s publica=%s enrolar=%s conectar=%s", o.Proceso,
				binarioElegido(), listoEn.Round(time.Millisecond), s.AdminAddr, s.PublicaAddr, s.EnrolarAddr, s.ConectarAddr)
			return s
		}
		if intento == 1 && s.murioPorPuertoOcupado(err) {
			// Sin volcar el log del intento fallido: la frase del error es lo que busca el gate para
			// detectar colisiones reales, y aquí la colisión ya está absorbida por el reintento.
			t.Logf("servidor %q: puerto ocupado entre reservarlo y enlazarlo (admin=%s publica=%s enrolar=%s conectar=%s); reintento con puertos nuevos",
				o.Proceso, s.AdminAddr, s.PublicaAddr, s.EnrolarAddr, s.ConectarAddr)
			// El intento fallido ya murió (errSalioAntes), pero antes de enlazar hizo su HeadBucket: el
			// doble lo olvida para que solo conste lo que pida el servidor que queda.
			s.S3.forget()
			continue
		}
		s.fallarArranque(t, o, err)
	}
}

// Log devuelve TODA la salida del servidor hasta ahora —stdout y stderr mezclados, en el orden
// en que llegaron—. Es segura mientras el proceso sigue escribiendo. Devuelve "" si el servidor
// no llegó a lanzarse. Sirve para afirmar que algo (o nada) se logueó.
func (s *servidor) Log() string {
	if s.proceso == nil {
		return ""
	}
	return s.proceso.registro.String()
}

// LineasLog devuelve las líneas del log que son un objeto JSON (los logs de slog con
// WAPP_LOG_JSON=true), cada una parseada a map[string]any, en orden. Las que no son JSON
// —avisos en texto de stderr, líneas vacías, JSON truncado o que no es un objeto— se ignoran. Los
// números salen como float64 (es lo que hace encoding/json con any). No falla; devuelve un slice
// vacío, no nil, si no hay ninguna.
func (s *servidor) LineasLog() []map[string]any {
	return lineasJSON(s.Log())
}

// Parar envía SIGTERM al servidor y espera a que salga (hasta 15 s; pasado ese plazo lo mata con
// SIGKILL). Devuelve el código de salida: 0 si cerró limpio, el que haya devuelto el proceso si
// salió con error, y -1 si lo mató una señal (la nuestra tras el plazo, o una propia). Es
// idempotente y segura entre goroutines: llamarla de nuevo, o dejar que corra el Cleanup de
// arrancar, no vuelve a señalar ni a esperar y devuelve el mismo código. No falla el test: el
// llamador decide qué hace con el código. Si el servidor no llegó a lanzarse devuelve 0.
func (s *servidor) Parar(t *testing.T) (codigo int) {
	t.Helper()
	if s.proceso == nil {
		return 0
	}
	return s.proceso.parar(t)
}

// limpiar es el cuerpo del Cleanup de arrancar: para el servidor y, si el test ya falló, vuelca
// las últimas 200 líneas del log. Si el servidor no paró limpio (código ≠ 0) y el test aún iba
// bien, lo falla: un servidor que no cierra con SIGTERM es un defecto, no un detalle.
func (s *servidor) limpiar(t *testing.T) {
	t.Helper()
	codigo := s.Parar(t)
	if codigo != 0 && !t.Failed() {
		t.Errorf("el servidor no paró limpio: código de salida %d (0 era lo esperado)", codigo)
	}
	if t.Failed() && !s.volcado && s.proceso != nil {
		t.Logf("últimas %d líneas del log del servidor:\n%s", lineasDeLog, ultimasLineas(s.Log(), lineasDeLog))
	}
}

// fallarArranque recibe el test, las opciones y el error de un intento de arranque, para el
// proceso si sigue vivo, y llama a t.Fatalf con el motivo, el código de salida y las últimas 200
// líneas del log. No devuelve.
func (s *servidor) fallarArranque(t *testing.T, o opcionesServidor, err error) {
	t.Helper()
	if s.proceso == nil {
		t.Fatalf("arrancar(%q): %v", o.Proceso, err)
	}
	codigo := s.Parar(t)
	s.volcado = true
	t.Fatalf("arrancar(%q): %v (código de salida %d)\núltimas %d líneas del log:\n%s",
		o.Proceso, err, codigo, lineasDeLog, ultimasLineas(s.Log(), lineasDeLog))
}

// murioPorPuertoOcupado dice si el error de un intento es «el proceso terminó antes de estar
// listo» y su log menciona «address already in use»: la carrera de los puertos.
func (s *servidor) murioPorPuertoOcupado(err error) bool {
	return errors.Is(err, errSalioAntes) && s.proceso != nil &&
		strings.Contains(s.proceso.registro.String(), textoPuertoOcupado)
}

// lanzar hace UN intento de arranque: reserva las cuatro direcciones, lanza el binario elegido
// con el entorno armado desde cero y espera a que esté listo. Recibe el contexto del test, las
// opciones y el HOME vacío. Devuelve cuánto tardó desde el Start hasta estar listo, o el error:
// el de reservar puertos o de lanzar, el de salir antes de estar listo (errSalioAntes) o el de no
// estarlo a tiempo. Cada intento tiene su propio log y su propio proceso.
func (s *servidor) lanzar(ctx context.Context, o opcionesServidor, home string) (time.Duration, error) {
	direcciones, err := elegirPuertos(4)
	if err != nil {
		return 0, err
	}
	s.AdminAddr, s.PublicaAddr, s.EnrolarAddr, s.ConectarAddr = direcciones[0], direcciones[1], direcciones[2], direcciones[3]

	cmd := exec.Command(rutaBinario(binarioElegido())) //nolint:gosec // binario compilado por este mismo arnés
	cmd.Dir = home
	cmd.Env = s.entorno(o, home)

	inicio := time.Now()
	proceso, err := iniciarProceso(cmd, topeParadaServidor)
	if err != nil {
		return 0, err
	}
	s.proceso = proceso
	if err := s.esperarListo(ctx); err != nil {
		return 0, err
	}
	return time.Since(inicio), nil
}

// entorno recibe las opciones y el HOME vacío, y devuelve el entorno COMPLETO del subproceso,
// construido desde cero (jamás se hereda el del test): lo mínimo del sistema (PATH, TMPDIR, un
// HOME vacío para que el SDK de AWS no lea ~/.aws), el modo desarrollo con log JSON de nivel
// debug, las cuatro direcciones y las salidas Entorno() de base, PKI, claves, S3 e identidad.
// Los nombres van completos, con WAPP_. No pone WAPP_CONFIG_FILE, WAPP_IDENTITY_URL,
// WAPP_IDENTITY_API_KEY, WAPP_LLM_PROMPTS_DIR ni WAPP_KEK_KMS_*. No falla.
func (s *servidor) entorno(o opcionesServidor, home string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + os.TempDir(),
		"HOME=" + home,
		"WAPP_APP_ENV=dev",
		"WAPP_LOG_LEVEL=debug",
		"WAPP_LOG_JSON=true",
		"WAPP_HTTP_ADDR=" + s.AdminAddr,
		"WAPP_PUBLIC_HTTP_ADDR=" + s.PublicaAddr,
		"WAPP_GRPC_ENROLL_ADDR=" + s.EnrolarAddr,
		"WAPP_GRPC_CONNECT_ADDR=" + s.ConectarAddr,
	}
	for _, parte := range [][]string{
		s.Base.Entorno(), s.PKI.Entorno(), s.Claves.Entorno(), s.S3.Entorno(), s.Identidad.Entorno(),
	} {
		env = append(env, parte...)
	}
	if o.SondeoWebhook > 0 {
		env = append(env, "WAPP_WEBHOOK_POLL_INTERVAL="+o.SondeoWebhook.String())
	}
	if o.MaxIntentosWebhook > 0 {
		env = append(env, "WAPP_WEBHOOK_MAX_ATTEMPTS="+strconv.Itoa(o.MaxIntentosWebhook))
	}
	return env
}

// esperarListo sondea cada 100 ms hasta que el servidor está listo, con tope de 30 s. Recibe el
// contexto del test. Devuelve nil cuando /healthz del admin da 200 y los otros tres listeners
// aceptan conexión; un error que envuelve errSalioAntes (con el código) si el proceso termina
// antes —el sondeo en vuelo se aborta en ese instante, no espera a su propio tope—; o el último
// error de sondeo si vence el tope o se cancela el contexto.
func (s *servidor) esperarListo(ctx context.Context) error {
	ctx, cancelar := context.WithTimeout(ctx, topeListo)
	defer cancelar()
	proceso := s.proceso
	go func() {
		select {
		case <-proceso.salio:
			cancelar()
		case <-ctx.Done():
		}
	}()
	for {
		ultimo := s.sondearUnaVez(ctx)
		if proceso.haSalido() { // antes que nada: un sondeo «bueno» de un proceso muerto no vale
			return fmt.Errorf("%w (código %d)", errSalioAntes, proceso.codigoSalida())
		}
		if ultimo == nil {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("el servidor no estuvo listo en %s (último sondeo: %w)", topeListo, ultimo)
		}
		select {
		case <-ctx.Done():
		case <-time.After(sondeoListo):
		}
	}
}

// sondearUnaVez hace una pasada de comprobaciones: GET /healthz del admin → 200 y conexión TCP
// aceptada en los listeners público, de enrolamiento y de CloudLink. Devuelve nil si todo
// responde, o el primer fallo. El :8103 no tiene /healthz (da 404), por eso solo se le conecta.
func (s *servidor) sondearUnaVez(ctx context.Context) error {
	codigo, _, err := consultarHealthz(ctx, s.AdminAddr)
	if err != nil {
		return err
	}
	if codigo != http.StatusOK {
		return fmt.Errorf("GET /healthz de %s respondió %d", s.AdminAddr, codigo)
	}
	for _, addr := range []string{s.PublicaAddr, s.EnrolarAddr, s.ConectarAddr} {
		if err := sondearTCP(ctx, addr); err != nil {
			return err
		}
	}
	return nil
}

// consultarHealthz hace GET http://<addr>/healthz sin reutilizar conexiones ni proxies, con tope
// de 3 s. Recibe el contexto y la dirección "host:puerto" del admin; devuelve el código HTTP y el
// cuerpo (hasta 1 MiB). Devuelve error si no puede conectar, leer o cerrar la respuesta; un 503
// NO es error aquí: es un código que el llamador interpreta.
func consultarHealthz(ctx context.Context, addr string) (codigo int, cuerpo []byte, err error) {
	ctx, cancelar := context.WithTimeout(ctx, topeSondeoUno)
	defer cancelar()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return 0, nil, fmt.Errorf("construyendo GET /healthz de %s: %w", addr, err)
	}
	cliente := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := cliente.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("GET /healthz de %s: %w", addr, err)
	}
	cuerpo, errLectura := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errCierre := resp.Body.Close(); errCierre != nil && errLectura == nil {
		errLectura = errCierre
	}
	if errLectura != nil {
		return 0, nil, fmt.Errorf("leyendo /healthz de %s: %w", addr, errLectura)
	}
	return resp.StatusCode, cuerpo, nil
}

// sondearTCP abre una conexión TCP a addr y la cierra, con tope de 3 s. Devuelve nil si el
// listener la aceptó, o el error de conexión o de cierre.
func sondearTCP(ctx context.Context, addr string) error {
	ctx, cancelar := context.WithTimeout(ctx, topeSondeoUno)
	defer cancelar()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("conectar a %s: %w", addr, err)
	}
	if err := conn.Close(); err != nil {
		return fmt.Errorf("cerrar la conexión de sondeo a %s: %w", addr, err)
	}
	return nil
}

// reservarPuertos recibe cuántas direcciones quiere y devuelve ese número de "127.0.0.1:<puerto>"
// libres y DISTINTAS entre sí y de las ya entregadas en esta corrida. Abre n listeners en el puerto
// 0, lee los puertos que dio el kernel y los cierra todos al final: entre ese cierre y que el
// servidor enlace hay una carrera con otros procesos de la máquina, que el reintento de arrancar
// absorbe. Devuelve error si el kernel no da puertos o no se consiguen n distintos.
func reservarPuertos(n int) (direcciones []string, err error) {
	puertosMu.Lock()
	defer puertosMu.Unlock()

	var escuchas []net.Listener
	defer func() {
		if errCierre := cerrarEscuchas(escuchas); errCierre != nil && err == nil {
			direcciones, err = nil, errCierre
		}
	}()
	// Los listeners repetidos se mantienen abiertos hasta el final para que el kernel no los
	// vuelva a ofrecer en este mismo bucle.
	for intentos := 0; len(direcciones) < n; intentos++ {
		if intentos >= 20*n {
			return nil, fmt.Errorf("no se consiguieron %d puertos libres distintos tras %d intentos", n, intentos)
		}
		escucha, errEscucha := net.Listen("tcp", "127.0.0.1:0")
		if errEscucha != nil {
			return nil, fmt.Errorf("reservar un puerto libre: %w", errEscucha)
		}
		escuchas = append(escuchas, escucha)
		tcp, ok := escucha.Addr().(*net.TCPAddr)
		if !ok {
			return nil, fmt.Errorf("la dirección reservada %v no es TCP", escucha.Addr())
		}
		if _, repetido := puertosEntregados[tcp.Port]; repetido {
			continue
		}
		puertosEntregados[tcp.Port] = struct{}{}
		direcciones = append(direcciones, net.JoinHostPort("127.0.0.1", strconv.Itoa(tcp.Port)))
	}
	return direcciones, nil
}

// cerrarEscuchas cierra todos los listeners y devuelve los errores de cierre unidos (nil si
// ninguno falló).
func cerrarEscuchas(escuchas []net.Listener) error {
	var errs []error
	for _, e := range escuchas {
		if err := e.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// registroLog es el búfer donde cae la salida del subproceso (stdout y stderr al MISMO
// búfer). Es seguro entre goroutines: el proceso escribe mientras el test lee.
type registroLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write añade p al búfer bajo candado. Cumple io.Writer; nunca falla.
func (r *registroLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

// String devuelve una copia de todo lo escrito hasta ahora.
func (r *registroLog) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// lineasJSON recibe un texto de log y devuelve, en orden, cada línea que es un objeto JSON
// parseada a map[string]any. Ignora las líneas vacías, las de texto plano, el JSON truncado y el
// JSON que no es un objeto (un arreglo, null, un número). Devuelve un slice vacío, no nil, si no
// hay ninguna. No falla.
func lineasJSON(texto string) []map[string]any {
	lineas := []map[string]any{}
	for linea := range strings.SplitSeq(texto, "\n") {
		linea = strings.TrimSpace(linea)
		if !strings.HasPrefix(linea, "{") {
			continue
		}
		var objeto map[string]any
		if err := json.Unmarshal([]byte(linea), &objeto); err != nil || objeto == nil {
			continue
		}
		lineas = append(lineas, objeto)
	}
	return lineas
}

// ultimasLineas recibe un texto y devuelve sus últimas n líneas (sin el salto final del texto), o
// el texto entero si tiene menos. n ≤ 0 devuelve "". No falla.
func ultimasLineas(texto string, n int) string {
	if n <= 0 {
		return ""
	}
	lineas := strings.Split(strings.TrimSuffix(texto, "\n"), "\n")
	if len(lineas) > n {
		lineas = lineas[len(lineas)-n:]
	}
	return strings.Join(lineas, "\n")
}

// procesoHijo es un subproceso en marcha: el comando, su log, un canal que se cierra cuando
// cmd.Wait() devolvió y el estado de la parada. Separarlo del servidor permite probar la parada
// con un proceso cualquiera, sin Docker ni binario del servidor.
type procesoHijo struct {
	cmd        *exec.Cmd
	registro   *registroLog
	salio      chan struct{} // se cierra cuando cmd.Wait() devolvió
	errEspera  error         // lo que devolvió cmd.Wait(); solo se lee después de <-salio
	topeParada time.Duration // cuánto se espera tras SIGTERM antes de SIGKILL

	mu     sync.Mutex // serializa parar: la segunda llamada espera a la primera y no repite nada
	parado bool
	codigo int
}

// iniciarProceso recibe un comando aún sin lanzar y el plazo entre SIGTERM y SIGKILL, le conecta
// stdout y stderr al MISMO búfer y lo lanza, con una goroutine que hace cmd.Wait() UNA sola vez y
// cierra el canal de salida. Devuelve el proceso en marcha, o el error de cmd.Start. cmd.Env lo
// pone quien llama (para el servidor, desde cero).
func iniciarProceso(cmd *exec.Cmd, topeParada time.Duration) (*procesoHijo, error) {
	h := &procesoHijo{cmd: cmd, registro: &registroLog{}, salio: make(chan struct{}), topeParada: topeParada}
	cmd.Stdout, cmd.Stderr = h.registro, h.registro
	cmd.WaitDelay = esperaTrasMuerte
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lanzar %s: %w", cmd.Path, err)
	}
	go func() {
		h.errEspera = cmd.Wait()
		close(h.salio)
	}()
	return h, nil
}

// haSalido dice, sin bloquear, si cmd.Wait() ya devolvió (el canal salio está cerrado).
func (h *procesoHijo) haSalido() bool {
	select {
	case <-h.salio:
		return true
	default:
		return false
	}
}

// codigoSalida devuelve el código de salida del proceso: el que devolvió, o -1 si lo terminó una
// señal. Solo es válido una vez cerrado el canal salio; antes devuelve -1.
func (h *procesoHijo) codigoSalida() int {
	if h.cmd.ProcessState == nil {
		return -1
	}
	return h.cmd.ProcessState.ExitCode()
}

// parar envía SIGTERM, espera hasta topeParada y, pasado ese plazo, SIGKILL. Devuelve el código de
// salida. Es idempotente y segura entre goroutines: la primera llamada hace la parada, las demás
// devuelven el mismo código sin tocar el proceso. Un fallo al señalar se anota con t.Logf.
func (h *procesoHijo) parar(t *testing.T) int {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.parado {
		h.codigo = h.detener(t)
		h.parado = true
	}
	return h.codigo
}

// detener hace la parada de verdad (parar la protege con el candado). Si el proceso ya había
// salido por su cuenta, solo devuelve su código.
func (h *procesoHijo) detener(t *testing.T) int {
	t.Helper()
	if h.haSalido() {
		return h.codigoSalida()
	}
	if err := h.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Logf("no se pudo enviar SIGTERM: %v", err)
	}
	plazo := time.NewTimer(h.topeParada)
	defer plazo.Stop()
	select {
	case <-h.salio:
	case <-plazo.C:
		t.Logf("el proceso no salió en %s tras SIGTERM: SIGKILL", h.topeParada)
		if err := h.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Logf("no se pudo enviar SIGKILL: %v", err)
		}
		<-h.salio
	}
	return h.codigoSalida()
}

// ---------------------------------------------------------------------------------------------
// Tests propios del arnés
// ---------------------------------------------------------------------------------------------

// TestArnes_EntornoLimpio prueba que el subproceso no hereda NADA del entorno del test: con
// variables trampa en el entorno del test, que apuntarían a otra base, otro S3 y un fichero de
// configuración inexistente, el servidor arranca igual, su /healthz dice que Postgres está sano
// (así que usa la base del contenedor), está conectado a SU base clonada, llamó a SU S3 falso y
// ninguna línea del log menciona la trampa. No es paralelo: t.Setenv no lo permite.
func TestArnes_EntornoLimpio(t *testing.T) {
	t.Setenv("WAPP_DB_HOST", "trampa")
	t.Setenv("WAPP_DB_NAME", "trampa")
	t.Setenv("WAPP_STORAGE_S3_ENDPOINT", "http://trampa.invalid")
	t.Setenv("WAPP_CONFIG_FILE", "/no/existe.yaml")
	t.Setenv("AWS_ENDPOINT_URL", "http://trampa.invalid")

	s := arrancar(t, opcionesServidor{Proceso: "arnes_entorno"})

	verificarEntornoCerrado(t, s)

	codigo, cuerpo, err := consultarHealthz(t.Context(), s.AdminAddr)
	if err != nil || codigo != http.StatusOK {
		t.Fatalf("GET /healthz: código %d, error %v", codigo, err)
	}
	var salud struct {
		Status string `json:"status"`
		Checks map[string]struct {
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(cuerpo, &salud); err != nil {
		t.Fatalf("el cuerpo de /healthz no es JSON: %v\n%s", err, cuerpo)
	}
	if salud.Status != "healthy" || salud.Checks["postgres"].Status != "healthy" {
		t.Errorf("/healthz no dice que Postgres está sano: %s", cuerpo)
	}

	// El servidor está conectado a SU base clonada, no a la «trampa» ni a otra.
	db := s.Base.Abrir(t)
	conectadas := consultaTexto(t, db, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`)
	if conectadas == "0" {
		t.Errorf("el servidor no tiene ninguna sesión abierta en %s", s.Base.Nombre)
	}

	// El único S3 al que llamó fue el falso, con el HEAD del bucket.
	if !slices.ContainsFunc(s.S3.Peticiones(), func(p peticionS3) bool {
		return p.Metodo == http.MethodHead && p.Ruta == "/"+bucketServidor
	}) {
		t.Errorf("el S3 falso no recibió HEAD /%s: %v", bucketServidor, s.S3.Peticiones())
	}

	if strings.Contains(strings.ToLower(s.Log()), "trampa") {
		t.Errorf("el log menciona la trampa: el servidor heredó el entorno del test:\n%s", ultimasLineas(s.Log(), lineasDeLog))
	}
	if len(s.LineasLog()) == 0 {
		t.Errorf("el servidor no escribió ninguna línea JSON en el log")
	}
}

// verificarEntornoCerrado comprueba el entorno que se le dio al subproceso: solo PATH, TMPDIR,
// HOME y variables WAPP_, sin duplicados, ninguna de las que el arnés se prohíbe poner y con los
// valores que identifican la base y el S3 de ESTE servidor. Falla el test con t.Errorf por cada
// incumplimiento.
func verificarEntornoCerrado(t *testing.T, s *servidor) {
	t.Helper()
	valores := map[string]string{}
	for _, par := range s.proceso.cmd.Env {
		clave, valor, ok := strings.Cut(par, "=")
		if !ok {
			t.Errorf("entrada de entorno sin '=': %q", par)
			continue
		}
		if _, repetida := valores[clave]; repetida {
			t.Errorf("variable repetida en el entorno del subproceso: %s", clave)
		}
		valores[clave] = valor
		permitida := clave == "PATH" || clave == "TMPDIR" || clave == "HOME" || strings.HasPrefix(clave, "WAPP_")
		if !permitida {
			t.Errorf("el entorno del subproceso trae %s, que no es ni del sistema mínimo ni WAPP_*", clave)
		}
		if strings.HasPrefix(clave, "WAPP_KEK_KMS_") {
			t.Errorf("el entorno del subproceso trae %s, que el arnés no usa", clave)
		}
	}
	for _, prohibida := range []string{"WAPP_CONFIG_FILE", "WAPP_IDENTITY_URL", "WAPP_IDENTITY_API_KEY", "WAPP_LLM_PROMPTS_DIR"} {
		if _, hay := valores[prohibida]; hay {
			t.Errorf("el entorno del subproceso trae %s, que el arnés no pone", prohibida)
		}
	}
	for clave, quiere := range map[string]string{
		"WAPP_DB_NAME":             s.Base.Nombre,
		"WAPP_STORAGE_S3_ENDPOINT": s.S3.URL(),
		"WAPP_HTTP_ADDR":           s.AdminAddr,
		"WAPP_IDENTITY_JWKS_URL":   s.Identidad.JWKSURL(),
		"HOME":                     s.proceso.cmd.Dir,
	} {
		if valores[clave] != quiere {
			t.Errorf("%s = %q, quería %q", clave, valores[clave], quiere)
		}
	}
}

// ranuraParalela es el punto de encuentro de un subtest de TestArnes_ServidoresEnParalelo con el
// otro: dos canales que cada uno cierra al llegar a una etapa, y el servidor que publica.
type ranuraParalela struct {
	listo    chan struct{} // se cierra cuando srv ya está listo (o el subtest acabó sin conseguirlo)
	revisado chan struct{} // se cierra cuando ya hizo sus comprobaciones cruzadas
	srv      *servidor
}

// TestArnes_ServidoresEnParalelo arranca dos servidores a la vez desde dos subtests paralelos, con
// procesos distintos, y prueba que conviven: los dos listos y respondiendo a la vez, con puertos y
// bases distintos y sin «address already in use» en ningún log; que cada uno para limpio (código
// 0 y «servidor detenido limpiamente»), y que las opciones del webhook llegan al entorno solo
// cuando se piden.
func TestArnes_ServidoresEnParalelo(t *testing.T) {
	opciones := []opcionesServidor{
		{Proceso: "arnes_paralelo_a"},
		{Proceso: "arnes_paralelo_b", SondeoWebhook: 200 * time.Millisecond, MaxIntentosWebhook: 3},
	}
	ranuras := make([]*ranuraParalela, len(opciones))
	for i := range ranuras {
		ranuras[i] = &ranuraParalela{listo: make(chan struct{}), revisado: make(chan struct{})}
	}
	// El grupo hace que t.Run no vuelva hasta que acaben los dos subtests paralelos.
	t.Run("grupo", func(t *testing.T) {
		for i, o := range opciones {
			t.Run(o.Proceso, func(t *testing.T) {
				t.Parallel()
				paralelaCorrer(t, ranuras[i], ranuras[1-i], o)
			})
		}
	})
}

// paralelaCorrer es el cuerpo de un subtest de TestArnes_ServidoresEnParalelo: arranca su
// servidor, espera a que el otro también esté listo, hace las comprobaciones cruzadas, espera a
// que el otro haya terminado las suyas (para no parar un servidor que el otro aún mira) y para el
// suyo. Los canales se cierran con defer para que un fallo no deje colgado al otro subtest.
func paralelaCorrer(t *testing.T, propia, ajena *ranuraParalela, o opcionesServidor) {
	t.Helper()
	cerrarListo := sync.OnceFunc(func() { close(propia.listo) })
	cerrarRevisado := sync.OnceFunc(func() { close(propia.revisado) })
	defer cerrarListo()
	defer cerrarRevisado()

	s := arrancar(t, o)
	propia.srv = s
	cerrarListo()

	esperarCanal(t, ajena.listo, "a que el otro servidor esté listo")
	otro := ajena.srv
	if otro == nil {
		t.Fatalf("el otro servidor no llegó a estar listo")
	}
	paralelaVerificarDistintos(t, s, otro)
	for _, srv := range []*servidor{s, otro} {
		if codigo, _, err := consultarHealthz(t.Context(), srv.AdminAddr); err != nil || codigo != http.StatusOK {
			t.Errorf("con los dos arriba, /healthz de %s: código %d, error %v", srv.Base.Nombre, codigo, err)
		}
	}
	paralelaVerificarWebhook(t, s, o)
	cerrarRevisado()
	esperarCanal(t, ajena.revisado, "a que el otro servidor termine sus comprobaciones")

	if codigo := s.Parar(t); codigo != 0 {
		t.Errorf("Parar devolvió %d, quería 0 (salida limpia)", codigo)
	}
	if !strings.Contains(s.Log(), textoParadaLimpia) {
		t.Errorf("el log no contiene %q:\n%s", textoParadaLimpia, ultimasLineas(s.Log(), lineasDeLog))
	}
	if strings.Contains(s.Log(), textoPuertoOcupado) {
		t.Errorf("el log contiene %q:\n%s", textoPuertoOcupado, ultimasLineas(s.Log(), lineasDeLog))
	}
}

// esperarCanal espera hasta 60 s a que se cierre c. Recibe el test, el canal y una descripción de
// lo que se espera, para el mensaje. Falla el test (t.Fatalf) si vence el plazo o se cancela el
// test: con -parallel 1 los dos subtests no pueden coincidir y esto lo dice en vez de colgarse.
func esperarCanal(t *testing.T, c <-chan struct{}, que string) {
	t.Helper()
	plazo := time.NewTimer(2 * topeListo)
	defer plazo.Stop()
	select {
	case <-c:
	case <-plazo.C:
		t.Fatalf("pasaron %s esperando %s (¿se corre con -parallel 1? estos dos subtests necesitan coincidir)", 2*topeListo, que)
	case <-t.Context().Done():
		t.Fatalf("el test se canceló esperando %s", que)
	}
}

// paralelaVerificarDistintos comprueba que dos servidores vivos a la vez no comparten ninguna
// dirección, ni la base, ni el directorio de PKI, ni el endpoint del S3 falso, ni el JWKS. Falla
// el test con t.Errorf por cada coincidencia.
func paralelaVerificarDistintos(t *testing.T, a, b *servidor) {
	t.Helper()
	vistas := map[string]string{}
	for _, srv := range []*servidor{a, b} {
		for rol, addr := range map[string]string{
			"admin": srv.AdminAddr, "publica": srv.PublicaAddr, "enrolar": srv.EnrolarAddr, "conectar": srv.ConectarAddr,
		} {
			if previo, repetida := vistas[addr]; repetida {
				t.Errorf("la dirección %s se repite: %s de %s y %s", addr, rol, srv.Base.Nombre, previo)
			}
			vistas[addr] = rol + " de " + srv.Base.Nombre
		}
	}
	pares := []struct{ nombre, a, b string }{
		{"base", a.Base.Nombre, b.Base.Nombre},
		{"DSN", a.Base.DSN, b.Base.DSN},
		{"CA de la PKI", a.PKI.CACertFile, b.PKI.CACertFile},
		{"S3 falso", a.S3.URL(), b.S3.URL()},
		{"JWKS", a.Identidad.JWKSURL(), b.Identidad.JWKSURL()},
		{"clave de lease", a.Claves.LeaseSeedB64, b.Claves.LeaseSeedB64},
	}
	for _, p := range pares {
		if p.a == p.b {
			t.Errorf("los dos servidores comparten %s: %q", p.nombre, p.a)
		}
	}
}

// paralelaVerificarWebhook comprueba que las opciones del webhook llegaron al entorno del
// subproceso si y solo si se pidieron. Falla el test con t.Errorf por cada discrepancia.
func paralelaVerificarWebhook(t *testing.T, s *servidor, o opcionesServidor) {
	t.Helper()
	quiere := map[string]string{}
	if o.SondeoWebhook > 0 {
		quiere["WAPP_WEBHOOK_POLL_INTERVAL"] = o.SondeoWebhook.String()
	}
	if o.MaxIntentosWebhook > 0 {
		quiere["WAPP_WEBHOOK_MAX_ATTEMPTS"] = strconv.Itoa(o.MaxIntentosWebhook)
	}
	for _, clave := range []string{"WAPP_WEBHOOK_POLL_INTERVAL", "WAPP_WEBHOOK_MAX_ATTEMPTS"} {
		valor, hay := "", false
		for _, par := range s.proceso.cmd.Env {
			if k, v, ok := strings.Cut(par, "="); ok && k == clave {
				valor, hay = v, true
			}
		}
		esperado, debeEstar := quiere[clave]
		if hay != debeEstar || valor != esperado {
			t.Errorf("%s en el entorno de %s: hay=%v valor=%q; quería hay=%v valor=%q", clave, s.Base.Nombre, hay, valor, debeEstar, esperado)
		}
	}
}

// TestArnes_ReintentoPuertoOcupado prueba el reintento por puerto ocupado: el primer intento
// recibe un puerto de admin que otro listener ya tiene, el servidor muere con «address already in
// use» y arrancar vuelve a intentarlo, una sola vez, con puertos nuevos. Y que el intento fallido
// no deja rastro en lo que un proceso mira después: el log visible es solo el del servidor vigente
// (sin la frase del puerto ni líneas ERROR), el doble de S3 cuenta UN HEAD /<bucket> y la base
// sigue con una sola fila en schema_version. No es paralelo: cambia elegirPuertos, que solo
// restaura al terminar.
func TestArnes_ReintentoPuertoOcupado(t *testing.T) {
	ocupante, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no se pudo ocupar un puerto: %v", err)
	}
	t.Cleanup(func() {
		if err := ocupante.Close(); err != nil {
			t.Logf("cerrar el listener ocupante: %v", err)
		}
	})

	original := elegirPuertos
	llamadas := 0
	elegirPuertos = func(n int) ([]string, error) {
		llamadas++
		direcciones, err := original(n)
		if err == nil && llamadas == 1 {
			direcciones[0] = ocupante.Addr().String() // el admin del primer intento choca
		}
		return direcciones, err
	}
	t.Cleanup(func() { elegirPuertos = original })

	s := arrancar(t, opcionesServidor{Proceso: "arnes_reintento"})

	if llamadas != 2 {
		t.Errorf("se reservaron puertos %d veces, quería 2 (el intento fallido y el reintento)", llamadas)
	}
	if s.AdminAddr == ocupante.Addr().String() {
		t.Errorf("el servidor sigue con el puerto ocupado %s", s.AdminAddr)
	}
	if strings.Contains(s.Log(), textoPuertoOcupado) {
		t.Errorf("el log del reintento aún contiene %q: no es el del intento nuevo", textoPuertoOcupado)
	}
	checkNoTraceOfFailedAttempt(t, s)
	if codigo, _, err := consultarHealthz(t.Context(), s.AdminAddr); err != nil || codigo != http.StatusOK {
		t.Errorf("tras el reintento, /healthz: código %d, error %v", codigo, err)
	}
}

// checkNoTraceOfFailedAttempt comprueba, en un servidor que arrancó tras un reintento por puerto
// ocupado, que el intento fallido no dejó rastro: ninguna línea ERROR en el log visible, UN solo
// HEAD /<bucket> en el doble de S3 y una sola fila en schema_version. Falla el test con t.Errorf
// por cada incumplimiento (t.Fatalf si no puede abrir o consultar la base).
func checkNoTraceOfFailedAttempt(t *testing.T, s *servidor) {
	t.Helper()
	for _, l := range s.LineasLog() {
		if strings.EqualFold(p0Cadena(l, "level"), "ERROR") {
			t.Errorf("el log visible arrastra una línea ERROR (¿del intento fallido?): %v", l)
		}
	}
	// El intento fallido llegó a hacer su HeadBucket antes de morir al enlazar: el doble de S3 solo
	// debe contar el del servidor que quedó (es lo que P0 exige con «exactamente 1»).
	cabeceras := 0
	for _, p := range s.S3.Peticiones() {
		if p.Metodo == http.MethodHead && p.Ruta == "/"+bucketServidor {
			cabeceras++
		}
	}
	if cabeceras != 1 {
		t.Errorf("HEAD /%s consta %d veces en el doble de S3 tras el reintento, quería 1 (solo el del servidor vigente); todas: %+v",
			bucketServidor, cabeceras, s.S3.Peticiones())
	}
	// La base es la misma en los dos intentos: el fallido no debe haberla migrado de nuevo.
	if filas := consultaEntero(t, s.Base.Abrir(t), `SELECT count(*) FROM public.schema_version`); filas != 1 {
		t.Errorf("schema_version tiene %d filas tras el reintento, quería 1", filas)
	}
}

// TestArnes_LineasLog prueba el análisis del log sin Docker ni servidor: solo las líneas que son
// un objeto JSON se devuelven, en orden y parseadas; el resto (texto, vacías, truncadas, arreglos,
// null) se ignora; una línea escrita en dos trozos cuenta como una; y el servidor sin lanzar da
// vacío.
func TestArnes_LineasLog(t *testing.T) {
	t.Parallel()
	texto := strings.Join([]string{
		`{"time":"2026-10-01T10:00:00Z","level":"INFO","msg":"arranque: fase completada","fase":"1/9","nombre":"infraestructura","ms":42}`,
		`2026/10/01 10:00:01 aviso en texto plano, como los de stderr`,
		``,
		`{"level":"WARN","msg":"con {llaves} dentro","anidado":{"a":[1,2]}}`,
		`[1,2,3]`,
		`{"truncada":`,
		`null`,
		`   {"level":"DEBUG","msg":"con sangría"}   `,
		`{"level":"ERROR","msg":"sin salto final"}`,
	}, "\n")

	registro := &registroLog{}
	mitad := len(texto) / 2
	for _, trozo := range []string{texto[:mitad], texto[mitad:]} { // el corte puede caer a mitad de línea
		if _, err := registro.Write([]byte(trozo)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	s := &servidor{proceso: &procesoHijo{registro: registro}}

	if s.Log() != texto {
		t.Errorf("Log() no devuelve lo escrito")
	}
	lineas := s.LineasLog()
	msgs := make([]string, len(lineas))
	for i, l := range lineas {
		msgs[i] = fmt.Sprint(l["msg"])
	}
	quiere := []string{"arranque: fase completada", "con {llaves} dentro", "con sangría", "sin salto final"}
	if !slices.Equal(msgs, quiere) {
		t.Fatalf("mensajes = %q, quería %q", msgs, quiere)
	}
	if ms, ok := lineas[0]["ms"].(float64); !ok || ms != 42 {
		t.Errorf("ms = %#v, quería 42 (float64)", lineas[0]["ms"])
	}
	if anidado, ok := lineas[1]["anidado"].(map[string]any); !ok || anidado == nil {
		t.Errorf("el objeto anidado no se parseó: %v", lineas[1])
	}

	if vacias := (&servidor{}).LineasLog(); vacias == nil || len(vacias) != 0 {
		t.Errorf("un servidor sin lanzar debe dar un slice vacío no nil, dio %#v", vacias)
	}
	if (&servidor{}).Log() != "" {
		t.Errorf("un servidor sin lanzar debe dar un log vacío")
	}
}

// TestArnes_UltimasLineas prueba el recorte del volcado: las últimas n líneas, el texto entero si
// es más corto, y los bordes (n = 0, texto vacío, salto final).
func TestArnes_UltimasLineas(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nombre string
		texto  string
		n      int
		quiere string
	}{
		{"las_ultimas_dos", "a\nb\nc\nd\n", 2, "c\nd"},
		{"sin_salto_final", "a\nb\nc", 2, "b\nc"},
		{"mas_corto_que_n", "a\nb\n", 200, "a\nb"},
		{"n_cero", "a\nb\n", 0, ""},
		{"texto_vacio", "", 5, ""},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			if got := ultimasLineas(c.texto, c.n); got != c.quiere {
				t.Errorf("ultimasLineas(%q, %d) = %q, quería %q", c.texto, c.n, got, c.quiere)
			}
		})
	}
}

// TestArnes_RegistroConcurrente prueba que el búfer del log resiste escritores concurrentes sin
// mezclar líneas: 8 goroutines escriben 200 líneas JSON cada una y todas salen enteras. Bajo
// -race caza cualquier acceso sin candado.
func TestArnes_RegistroConcurrente(t *testing.T) {
	t.Parallel()
	const escritores, porEscritor = 8, 200
	registro := &registroLog{}
	var espera sync.WaitGroup
	for g := range escritores {
		espera.Go(func() {
			for n := range porEscritor {
				linea := fmt.Sprintf("{\"g\":%d,\"n\":%d}\n", g, n)
				if _, err := registro.Write([]byte(linea)); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
				_ = registro.String() // lectura concurrente con las escrituras (String no devuelve error)
			}
		})
	}
	espera.Wait()
	if got := len(lineasJSON(registro.String())); got != escritores*porEscritor {
		t.Errorf("líneas JSON = %d, quería %d", got, escritores*porEscritor)
	}
}

// esperarEnLog espera, sondeando cada 20 ms con tope de 10 s, a que el log del proceso contenga
// texto. Recibe el test, el proceso y el texto; falla (t.Fatalf) si no aparece a tiempo.
func esperarEnLog(t *testing.T, h *procesoHijo, texto string) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelar()
	for !strings.Contains(h.registro.String(), texto) {
		select {
		case <-ctx.Done():
			t.Fatalf("el log no llegó a contener %q:\n%s", texto, h.registro.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestArnes_PararMataSiNoCede prueba la rama SIGKILL de la parada con un proceso que ignora
// SIGTERM: tras el plazo se mata, el código es -1, es idempotente y varias goroutines que paran
// a la vez reciben el mismo código. No necesita Docker ni el binario del servidor, solo `sh`.
func TestArnes_PararMataSiNoCede(t *testing.T) {
	t.Parallel()
	const tope = 300 * time.Millisecond
	// exec conserva la disposición «ignorar SIGTERM», así que el que recibe las señales es sleep.
	cmd := exec.Command("sh", "-c", `trap '' TERM; echo listo; exec sleep 60`)
	h, err := iniciarProceso(cmd, tope)
	if err != nil {
		t.Fatalf("iniciarProceso: %v", err)
	}
	esperarEnLog(t, h, "listo") // sin esto SIGTERM podría llegar antes del trap y matarlo sin SIGKILL

	codigos := make([]int, 3)
	inicio := time.Now()
	var espera sync.WaitGroup
	for i := range codigos {
		espera.Go(func() { codigos[i] = h.parar(t) })
	}
	espera.Wait()
	if tardo := time.Since(inicio); tardo < tope || tardo > 10*time.Second {
		t.Errorf("parar tardó %s; quería entre %s (el plazo) y 10 s", tardo, tope)
	}
	for i, c := range codigos {
		if c != -1 {
			t.Errorf("parar #%d devolvió %d, quería -1 (muerto por SIGKILL)", i, c)
		}
	}
	segunda := time.Now()
	if c := h.parar(t); c != -1 || time.Since(segunda) > 100*time.Millisecond {
		t.Errorf("la llamada repetida devolvió %d en %s, quería -1 al instante", c, time.Since(segunda))
	}
}

// TestArnes_PararLimpio prueba la parada que sí cede: un proceso que sale con 0 al recibir
// SIGTERM da código 0, la segunda llamada devuelve lo mismo, y parar un proceso que ya había
// salido por su cuenta devuelve su código de salida. Solo necesita `sh`.
func TestArnes_PararLimpio(t *testing.T) {
	t.Parallel()
	h, err := iniciarProceso(exec.Command("sh", "-c", `trap 'exit 0' TERM; echo listo; while :; do sleep 1; done`), 10*time.Second)
	if err != nil {
		t.Fatalf("iniciarProceso: %v", err)
	}
	esperarEnLog(t, h, "listo")
	if c := h.parar(t); c != 0 {
		t.Errorf("parar devolvió %d, quería 0", c)
	}
	if c := h.parar(t); c != 0 {
		t.Errorf("la segunda llamada devolvió %d, quería 0", c)
	}

	salido, err := iniciarProceso(exec.Command("sh", "-c", `echo adios; exit 7`), 10*time.Second)
	if err != nil {
		t.Fatalf("iniciarProceso: %v", err)
	}
	<-salido.salio
	if c := salido.parar(t); c != 7 {
		t.Errorf("parar de un proceso que ya salió con 7 devolvió %d", c)
	}
	if !strings.Contains(salido.registro.String(), "adios") {
		t.Errorf("el log no recogió la salida del proceso: %q", salido.registro.String())
	}
}
