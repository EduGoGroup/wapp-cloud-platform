//go:build integracion

package procesos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
