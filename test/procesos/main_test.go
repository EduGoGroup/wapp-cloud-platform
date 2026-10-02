//go:build integracion

package procesos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Credenciales del Postgres de la corrida. Son de un contenedor que vive lo que dura el
// `go test` y escucha en un puerto que pone Docker: no protegen nada, solo se nombran en un
// sitio para que el contenedor, el entorno de los subprocesos y las DSN digan lo mismo.
const usuarioBD, claveBD = "wapp", "wapp"

const (
	// imagenPostgres es la única imagen de la corrida (R9.1.a): Postgres 17, como UAT.
	imagenPostgres = "postgres:17-alpine"
	// basePlantilla es la base que migra cmd/migrate una sola vez y de la que se clona la de
	// cada proceso. El arnés NO abre jamás una conexión a ella (T-13): CREATE DATABASE …
	// TEMPLATE falla si la plantilla tiene una sesión abierta. Y desde que migrarPlantilla
	// termina, NADIE puede: closeTemplate la deja con ALLOW_CONNECTIONS false, así que un test
	// que lo intente recibe un error de Postgres en vez de romper los clones de los demás.
	basePlantilla = "plantilla"
	// baseMantenimiento es la base `postgres` del contenedor: desde ella se crean y se borran
	// las bases clonadas.
	baseMantenimiento = "postgres"

	// Topes de cada etapa del arranque: se espera sondeando o con un contexto, nunca a ciegas.
	topeLevantarPostgres = 3 * time.Minute
	topeCompilar         = 10 * time.Minute
	topeMigrar           = 2 * time.Minute
	topeSesiones         = 30 * time.Second
)

var (
	// instancia es el Postgres 17 de la corrida: uno solo, levantado por TestMain y terminado
	// por TestMain. Es nil fuera de una corrida de este paquete.
	instancia *postgres.PostgresContainer

	// urlInstancia es la URL que devolvió instancia.ConnectionString(ctx, "sslmode=disable"),
	// apuntando a la base `plantilla`. De ella salen el host, el puerto y, cambiando solo el
	// Path, la DSN de cualquier otra base del contenedor (nunca un literal). 🚫 No se conecta
	// con ella tal cual —ni con instancia.ConnectionString—: apunta a la plantilla, que no acepta
	// conexiones (closeTemplate). La base de un proceso se pide con nuevaBase.
	urlInstancia *url.URL

	// binElegido es el binario bajo prueba, «viejo» o «nuevo», ya validado.
	binElegido string

	// binarios guarda la ruta de cada ejecutable compilado, por clave «migrate» y el binario
	// elegido. Se escribe una vez en TestMain antes de m.Run y después solo se lee.
	binarios map[string]string
)

// TestMain levanta UN Postgres para toda la corrida, compila una vez cmd/migrate y el servidor
// elegido, migra la base `plantilla` con el subproceso migrate y corre los tests. Si algo de eso
// no se puede, la corrida FALLA (código ≠ 0): no se salta nada. Está partido en ejecutar para que
// corran los defer antes de os.Exit.
func TestMain(m *testing.M) {
	os.Exit(ejecutar(m))
}

// ejecutar es el cuerpo de TestMain: recibe el *testing.M y devuelve el código de salida del
// proceso. Devuelve 2 si WAPP_PROCESOS_BINARIO no vale «viejo» o «nuevo» o si GOWORK no vale
// «off» (las dos cosas se miran al entrar, antes de arrancar nada, y se dicen las dos si fallan
// las dos), 1 si no se pudo levantar Postgres, compilar o migrar, y el código de m.Run en otro caso.
//
// Pasada la entrada, y antes de levantar nada, barre del directorio temporal del sistema los
// directorios de corrida que dejaron huérfanos las corridas que murieron sin llegar a su defer
// (sweepOrphanRunDirs, en sweep_test.go, con las condiciones que hacen seguro ese borrado). Un
// barrido que falla se dice por stderr y no cambia el código de salida.
func ejecutar(m *testing.M) int {
	cual, err := validarBinario(os.Getenv("WAPP_PROCESOS_BINARIO"))
	if err := errors.Join(err, requireGoworkOff(os.Getenv("GOWORK"))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	binElegido = cual

	sweepOrphanRunDirs(os.TempDir(), time.Now(), os.RemoveAll, stderrf)

	if err := levantarPostgres(); err != nil {
		fmt.Fprintf(os.Stderr, "procesos: no se pudo levantar Postgres (¿hay Docker?): %v\n", err)
		return 1
	}
	defer terminarPostgres()

	dir, err := createRunDir(os.TempDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "procesos: no se pudo crear el directorio temporal: %v\n", err)
		return 1
	}
	defer borrarDirectorio(dir)

	if err := prepararPlantilla(dir); err != nil {
		fmt.Fprintf(os.Stderr, "procesos: %v\n", err)
		return 1
	}
	return m.Run()
}

// validarBinario recibe el valor de WAPP_PROCESOS_BINARIO y devuelve el binario elegido. Falla,
// con el mensaje que ve el desarrollador, si el valor no es exactamente «viejo» o «nuevo»
// (también si está vacío): sin binario no hay nada que probar y no se adivina uno (R9.4.a).
func validarBinario(valor string) (string, error) {
	if valor != "viejo" && valor != "nuevo" {
		return "", fmt.Errorf("procesos: WAPP_PROCESOS_BINARIO debe ser «viejo» o «nuevo» (vale %q)", valor)
	}
	return valor, nil
}

// requireGoworkOff recibe el valor de la variable de entorno GOWORK y devuelve nil solo si es
// exactamente «off». En cualquier otro caso —vacía, «auto», la ruta de un go.work— devuelve el
// error con el mensaje que ve el desarrollador (D-F9-7). El arnés compila el servidor y cmd/migrate
// con un `go build` que hereda el entorno (compilar): sin GOWORK=off, en la ubicación real del repo
// ese build usa el go.work de la raíz del ecosistema y enlaza los árboles vecinos de wapp-cloudlink
// y wapp-shared en vez de las versiones de go.mod, así que la corrida probaría un servidor que no es
// el que se publica. `make test-procesos` ya la pone; una invocación directa o la de un IDE, no.
// Solo cuenta la variable de entorno: un `go env -w GOWORK=off` no la pone en el entorno del test y
// no se da por bueno. No toca el entorno ni lanza nada.
func requireGoworkOff(value string) error {
	if value != "off" {
		return fmt.Errorf("procesos: GOWORK debe ser «off» (vale %q): sin él, el arnés compilaría el servidor contra "+
			"los módulos vecinos del go.work y no contra las versiones de go.mod; usa `make test-procesos` o antepón GOWORK=off", value)
	}
	return nil
}

// binarioElegido devuelve el binario bajo prueba, «viejo» o «nuevo», ya validado por TestMain.
// Es lo único que los procesos saben de la elección: nombran su base y su servidor con él, pero
// jamás ramifican por él (R9.8.b).
func binarioElegido() string {
	return binElegido
}

// rutaBinario recibe «viejo», «nuevo» o «migrate» y devuelve la ruta absoluta del ejecutable ya
// compilado por TestMain. De «viejo»/«nuevo» solo existe el elegido: pedir el otro, o una clave
// desconocida, devuelve la cadena vacía (quien la ejecute falla con un error claro de exec).
func rutaBinario(cual string) string {
	return binarios[cual]
}

// entornoBD recibe el nombre de una base del contenedor y devuelve las variables WAPP_DB_*
// (HOST, PORT, USER, PASSWORD, NAME, SSLMODE) como «K=V» que apuntan a ese Postgres y a esa
// base. Son las que lee el servidor y cmd/migrate (el prefijo WAPP_ va completo: config.go
// compone WAPP_ + clave). Solo es válida dentro de una corrida, cuando TestMain ya fijó la URL
// de la instancia; fuera de ella entra en pánico por nil.
func entornoBD(nombreBase string) []string {
	return []string{
		"WAPP_DB_HOST=" + urlInstancia.Hostname(),
		"WAPP_DB_PORT=" + urlInstancia.Port(),
		"WAPP_DB_USER=" + usuarioBD,
		"WAPP_DB_PASSWORD=" + claveBD,
		"WAPP_DB_NAME=" + nombreBase,
		"WAPP_DB_SSLMODE=disable",
	}
}

// levantarPostgres arranca el contenedor postgres:17-alpine de la corrida (con la base
// `plantilla` y el usuario del arnés) y fija instancia y urlInstancia. No recibe nada y devuelve
// el error de Docker o del contenedor; si el contenedor llegó a crearse pero no quedó listo, lo
// termina antes de devolver el error, así que un fallo no deja nada vivo.
func levantarPostgres() error {
	ctx, cancelar := context.WithTimeout(context.Background(), topeLevantarPostgres)
	defer cancelar()

	inicio := time.Now()
	ctr, err := postgres.Run(ctx, imagenPostgres,
		postgres.WithDatabase(basePlantilla),
		postgres.WithUsername(usuarioBD),
		postgres.WithPassword(claveBD),
		postgres.BasicWaitStrategies(),
	)
	var destino *url.URL
	if err == nil {
		destino, err = urlDe(ctx, ctr)
	}
	if err != nil {
		if terr := testcontainers.TerminateContainer(ctr); terr != nil {
			fmt.Fprintf(os.Stderr, "procesos: no se pudo terminar el contenedor tras el fallo: %v\n", terr)
		}
		return err
	}
	instancia, urlInstancia = ctr, destino
	fmt.Fprintf(os.Stderr, "procesos: Postgres %s listo en %s\n", imagenPostgres, time.Since(inicio).Round(time.Millisecond))
	return nil
}

// urlDe recibe el contenedor ya listo y devuelve la URL de ConnectionString(ctx,
// "sslmode=disable") parseada. Falla si el contenedor no sabe su dirección o la cadena no es una
// URL, cosa que no pasa con el módulo de testcontainers, pero que se comprueba.
func urlDe(ctx context.Context, ctr *postgres.PostgresContainer) (*url.URL, error) {
	cadena, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("cadena de conexión del contenedor: %w", err)
	}
	u, err := url.Parse(cadena)
	if err != nil {
		return nil, fmt.Errorf("la cadena de conexión del contenedor no es una URL: %w", err)
	}
	return u, nil
}

// terminarPostgres termina el contenedor de la corrida. No devuelve nada: un fallo al terminar
// se vuelca a stderr (el reaper de testcontainers lo recogerá), pero no cambia el código de
// salida de los tests.
func terminarPostgres() {
	if err := testcontainers.TerminateContainer(instancia); err != nil {
		fmt.Fprintf(os.Stderr, "procesos: no se pudo terminar el contenedor de Postgres: %v\n", err)
	}
}

// borrarDirectorio borra el directorio temporal de la corrida con los binarios compilados.
// Un fallo se vuelca a stderr y no cambia el código de salida. Si el binario de test muere antes
// de llegar aquí, el directorio queda huérfano y lo barre una corrida posterior
// (sweepOrphanRunDirs).
func borrarDirectorio(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "procesos: no se pudo borrar %s: %v\n", dir, err)
	}
}

// prepararPlantilla recibe el directorio temporal de la corrida, compila en él cmd/migrate y el
// servidor elegido, y migra la base `plantilla`, que queda cerrada a conexiones nuevas. Devuelve
// el primer error: sin raíz del módulo, sin compilar o sin migrar no hay corrida.
func prepararPlantilla(dir string) error {
	raiz, err := raizDelModulo()
	if err != nil {
		return err
	}
	if err := compilarBinarios(raiz, dir); err != nil {
		return err
	}
	return migrarPlantilla(dir)
}

// raizDelModulo devuelve el directorio del go.mod que contiene el paquete en prueba, subiendo
// desde el directorio de trabajo (go test corre en test/procesos). Falla si llega a la raíz del
// sistema de ficheros sin encontrar go.mod.
func raizDelModulo() (string, error) {
	inicio, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("directorio de trabajo: %w", err)
	}
	dir := inicio
	for {
		_, err := os.Stat(filepath.Join(dir, "go.mod"))
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("buscando go.mod en %s: %w", dir, err)
		}
		padre := filepath.Dir(dir)
		if padre == dir {
			return "", fmt.Errorf("no se encontró go.mod subiendo desde %s", inicio)
		}
		dir = padre
	}
}

// compilarBinarios recibe la raíz del módulo y el directorio temporal, y compila en este último
// cmd/migrate y el servidor elegido (./cmd/server si viejo, ./cmd/server-modular si nuevo),
// dejando sus rutas en binarios. Se compila UNA vez por corrida; el tiempo de cada build se
// vuelca a stderr. Falla con la salida del compilador si `go build` falla.
func compilarBinarios(raiz, dir string) error {
	paquetes := map[string]struct{ paquete, fichero string }{
		"migrate": {"./cmd/migrate", "migrate"},
		"viejo":   {"./cmd/server", "servidor-viejo"},
		"nuevo":   {"./cmd/server-modular", "servidor-nuevo"},
	}
	binarios = make(map[string]string, 2)
	for _, cual := range []string{"migrate", binElegido} {
		destino := filepath.Join(dir, paquetes[cual].fichero)
		inicio := time.Now()
		if err := compilar(raiz, paquetes[cual].paquete, destino); err != nil {
			return err
		}
		binarios[cual] = destino
		fmt.Fprintf(os.Stderr, "procesos: compilado %s en %s\n", paquetes[cual].paquete, time.Since(inicio).Round(time.Millisecond))
	}
	return nil
}

// compilar recibe la raíz del módulo, un paquete main («./cmd/…») y la ruta del ejecutable a
// producir, y corre `go build -o` con el directorio de trabajo en la raíz. Hereda el entorno
// (GOCACHE, GOFLAGS, GOWORK, GOTOOLCHAIN… hacen falta para compilar); el GOWORK que hereda es
// «off», porque TestMain no llega hasta aquí con otro valor (requireGoworkOff). Falla con la
// salida del compilador incluida en el error.
func compilar(raiz, paquete, destino string) error {
	ctx, cancelar := context.WithTimeout(context.Background(), topeCompilar)
	defer cancelar()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", destino, paquete) //nolint:gosec // argumentos fijos del arnés, sin entrada externa
	cmd.Dir = raiz
	// cmd.Env se queda en nil a propósito: go build necesita el entorno de Go del desarrollador.
	if salida, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build %s falló: %w\n%s", paquete, err, salida)
	}
	return nil
}

// migrarPlantilla recibe el directorio temporal y migra la base `plantilla` con el subproceso
// migrate ya compilado, que sale (y cierra su conexión) al terminar. El entorno del subproceso
// se construye desde cero: PATH, TMPDIR, un HOME vacío y entornoBD("plantilla"). Vuelca la salida
// de migrate a stderr y exige la línea «migraciones aplicadas: … skipped=false»; después espera,
// sin abrir nunca una conexión a `plantilla`, a que Postgres haya cerrado sus sesiones, y la
// cierra a conexiones nuevas (closeTemplate). Falla si migrate falla, no aplica el esquema, la
// plantilla se queda con sesiones o no se puede cerrar.
func migrarPlantilla(dir string) error {
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		return fmt.Errorf("no se pudo crear el HOME vacío de migrate: %w", err)
	}
	ctx, cancelar := context.WithTimeout(context.Background(), topeMigrar)
	defer cancelar()

	inicio := time.Now()
	var salida bytes.Buffer
	cmd := exec.CommandContext(ctx, rutaBinario("migrate")) //nolint:gosec // binario compilado por este mismo arnés
	cmd.Dir = home
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + os.TempDir(),
		"HOME=" + home,
	}, entornoBD(basePlantilla)...)
	cmd.Stdout = io.MultiWriter(os.Stderr, &salida)
	cmd.Stderr = cmd.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("migrate falló sobre %s: %w", basePlantilla, err)
	}
	texto := salida.String()
	if !strings.Contains(texto, "migraciones aplicadas: ") || !strings.Contains(texto, "skipped=false") {
		return fmt.Errorf("migrate terminó sin la línea «migraciones aplicadas: … skipped=false»: la plantilla %s no quedó migrada", basePlantilla)
	}
	if err := esperarSinSesiones(basePlantilla); err != nil {
		return err
	}
	if err := closeTemplate(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "procesos: plantilla migrada y cerrada a conexiones en %s\n", time.Since(inicio).Round(time.Millisecond))
	return nil
}

// closeTemplate deja la base `plantilla` sin admitir conexiones nuevas (ALTER DATABASE … WITH
// ALLOW_CONNECTIONS false), desde la base de mantenimiento y sin conectarse nunca a ella. Se llama
// una vez, cuando la plantilla ya está migrada y sin sesiones. A partir de ahí, quien intente
// conectarse recibe de Postgres «database "plantilla" is not currently accepting connections»
// (SQLSTATE 55000) y no llega a tener una sesión: sin esto, una sola sesión abierta en la
// plantilla hace fallar el CREATE DATABASE … TEMPLATE de todos los demás tests («source database
// "plantilla" is being accessed by other users», SQLSTATE 55006). Clonar sigue funcionando: una
// plantilla no necesita aceptar conexiones (template0 tampoco las acepta), y el clon nace
// aceptándolas. Devuelve el error de la conexión de mantenimiento o de la sentencia.
func closeTemplate() error {
	ctx, cancelar := context.WithTimeout(context.Background(), topeSesiones)
	defer cancelar()
	sentencia := "ALTER DATABASE " + pgx.Identifier{basePlantilla}.Sanitize() + " WITH ALLOW_CONNECTIONS false"
	if err := ejecutarMantenimiento(ctx, sentencia); err != nil {
		return fmt.Errorf("cerrar %s a conexiones nuevas: %w", basePlantilla, err)
	}
	return nil
}

// ---------------------------------------------------------------------------------------------
// Tests propios del arnés
// ---------------------------------------------------------------------------------------------

// topeEntryCheck acota la reejecución del binario de test en TestArnes_GoworkOff: sale al entrar en
// TestMain, así que tarda lo que tarda en arrancar un proceso.
const topeEntryCheck = 30 * time.Second

// TestArnes_GoworkOff prueba que el arnés exige GOWORK=off (D-F9-7). La función pura acepta solo
// «off», letra por letra, y el mensaje nombra la variable y el valor que vio. Y de punta a punta:
// el propio binario de test, reejecutado con otro GOWORK (o sin él), sale de TestMain con código 2
// y ese mensaje ANTES de levantar nada —ni Postgres ni un build—; si además WAPP_PROCESOS_BINARIO
// no vale, dice las dos cosas. No necesita Docker más que para la corrida que lo contiene.
func TestArnes_GoworkOff(t *testing.T) {
	t.Parallel()
	t.Run("requireGoworkOff", func(t *testing.T) {
		t.Parallel()
		if err := requireGoworkOff("off"); err != nil {
			t.Errorf("requireGoworkOff(«off») = %v, quería nil", err)
		}
		for _, value := range []string{"", "auto", "OFF", "Off", " off", "off ", "0", "false", "/ruta/al/go.work"} {
			err := requireGoworkOff(value)
			if err == nil {
				t.Errorf("requireGoworkOff(%q) = nil, quería un error", value)
				continue
			}
			if msg := err.Error(); !strings.Contains(msg, "GOWORK debe ser «off»") || !strings.Contains(msg, fmt.Sprintf("(vale %q)", value)) {
				t.Errorf("requireGoworkOff(%q): el mensaje no nombra la variable y el valor: %s", value, msg)
			}
		}
	})

	const goworkMsg, binaryMsg = "procesos: GOWORK debe ser «off»", "procesos: WAPP_PROCESOS_BINARIO debe ser"
	cases := []struct {
		name    string
		env     []string // lo que se añade al entorno mínimo del subproceso
		want    []string // lo que debe decir stderr
		wantNot []string // lo que no debe decir
	}{
		{"sin GOWORK", []string{"WAPP_PROCESOS_BINARIO=viejo"}, []string{goworkMsg, `(vale "")`}, []string{binaryMsg}},
		{"GOWORK con la ruta de un go.work", []string{"WAPP_PROCESOS_BINARIO=nuevo", "GOWORK=/ruta/al/go.work"},
			[]string{goworkMsg, `(vale "/ruta/al/go.work")`}, []string{binaryMsg}},
		{"GOWORK y el binario mal a la vez", []string{"WAPP_PROCESOS_BINARIO=otro", "GOWORK=auto"},
			[]string{goworkMsg, `(vale "auto")`, binaryMsg}, nil},
		{"GOWORK=off y el binario mal", []string{"WAPP_PROCESOS_BINARIO=otro", "GOWORK=off"}, []string{binaryMsg}, []string{goworkMsg}},
	}
	for _, c := range cases {
		t.Run("TestMain: "+c.name, func(t *testing.T) {
			t.Parallel()
			code, stderr := rerunTestMain(t, c.env)
			if code != 2 {
				t.Fatalf("el binario de test salió con código %d, quería 2\nstderr: %s", code, stderr)
			}
			for _, w := range c.want {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr no dice %q:\n%s", w, stderr)
				}
			}
			// «Postgres» lo dicen tanto el contenedor listo como el fallo al levantarlo: si aparece,
			// TestMain pasó de la comprobación de entrada.
			for _, w := range append(c.wantNot, "Postgres", "compilado") {
				if strings.Contains(stderr, w) {
					t.Errorf("stderr dice %q y no debía:\n%s", w, stderr)
				}
			}
		})
	}
}

// rerunTestMain reejecuta el binario de test en curso sin ningún test que correr (-test.run=^$),
// con un entorno construido desde cero —PATH, TMPDIR, un HOME vacío, un DOCKER_HOST que no existe
// y lo que traiga extra—, y devuelve su código de salida y su stderr. El HOME vacío y el
// DOCKER_HOST están para que, si la comprobación de entrada de TestMain dejara pasar, el subproceso
// falle al buscar Docker en vez de levantar un segundo Postgres. Falla el test (t.Fatalf) si el
// binario no se puede lanzar o pasa del tope.
func rerunTestMain(t *testing.T, extra []string) (code int, stderr string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("rerunTestMain: la ruta del binario de test: %v", err)
	}
	ctx, cancelar := context.WithTimeout(t.Context(), topeEntryCheck)
	defer cancelar()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, self, "-test.run=^$") //nolint:gosec // el propio binario de test, con argumentos fijos
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + os.TempDir(),
		"HOME=" + t.TempDir(),
		"DOCKER_HOST=unix:///nada",
	}, extra...)
	cmd.Stderr = &out
	err = cmd.Run()
	var salida *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String()
	case errors.As(err, &salida) && ctx.Err() == nil:
		return salida.ExitCode(), out.String()
	default:
		t.Fatalf("rerunTestMain: lanzar %s: %v (contexto: %v)\nstderr: %s", self, err, ctx.Err(), out.String())
		return 0, ""
	}
}
