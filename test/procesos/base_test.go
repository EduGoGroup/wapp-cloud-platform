//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // registra el driver «pgx» de database/sql para baseClonada.Abrir
)

const (
	// maxNombreBase es el límite de Postgres para un identificador: 63 bytes.
	maxNombreBase = 63

	topeClonar = 60 * time.Second
	topeBorrar = 30 * time.Second
	topeAbrir  = 15 * time.Second
)

// patronNombreProceso es lo único que se admite en el nombre de un proceso: minúsculas, dígitos y
// guion bajo. Con eso el nombre de la base no necesita escapes y no admite inyección.
var patronNombreProceso = regexp.MustCompile(`^[a-z0-9_]+$`)

// baseClonada es la base de datos propia de un proceso: un clon de `plantilla` ya migrada, con
// su nombre y los datos para conectarse a ella. Host y Puerto salen de la cadena de conexión del
// contenedor (el puerto lo pone Docker); DSN es esa misma cadena con el nombre de esta base.
type baseClonada struct {
	Nombre  string
	Host    string
	Puerto  string
	Usuario string
	Clave   string
	DSN     string
}

// nombreDeBase recibe el nombre de un proceso y devuelve el de su base, proc_<proceso>_<binario>.
// Falla si el proceso no es [a-z0-9_]+ o si el nombre resultante pasa de 63 bytes, el límite de
// un identificador de Postgres.
func nombreDeBase(proceso string) (string, error) {
	if !patronNombreProceso.MatchString(proceso) {
		return "", fmt.Errorf("el nombre de proceso %q no cumple [a-z0-9_]+", proceso)
	}
	nombre := "proc_" + proceso + "_" + binarioElegido()
	if len(nombre) > maxNombreBase {
		return "", fmt.Errorf("el nombre de base %q mide %d bytes y el máximo es %d", nombre, len(nombre), maxNombreBase)
	}
	return nombre, nil
}

// dsnDeBase recibe el nombre de una base del contenedor y devuelve su cadena de conexión: la que
// dio instancia.ConnectionString(ctx, "sslmode=disable") con solo el Path cambiado. Nunca es un
// literal. Solo es válida dentro de una corrida, cuando TestMain ya fijó urlInstancia.
func dsnDeBase(nombre string) string {
	u := *urlInstancia
	u.Path = "/" + nombre
	return u.String()
}

// conectarMantenimiento abre una conexión a la base `postgres` del contenedor (jamás a
// `plantilla`) con el contexto dado. Devuelve la conexión o el error de pgx; quien la abre la
// cierra con cerrarMantenimiento.
func conectarMantenimiento(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, dsnDeBase(baseMantenimiento))
	if err != nil {
		return nil, fmt.Errorf("conectar a la base de mantenimiento %s: %w", baseMantenimiento, err)
	}
	return conn, nil
}

// cerrarMantenimiento cierra una conexión abierta con conectarMantenimiento. Recibe el contexto
// con el que se usó, del que toma solo los valores: el cierre tiene su propio tope, porque el
// contexto de la operación puede haber vencido ya. No devuelve nada: si el cierre falla lo
// vuelca a stderr (la conexión ya cumplió y no hay a quién culpar).
func cerrarMantenimiento(ctx context.Context, conn *pgx.Conn) {
	ctx, cancelar := context.WithTimeout(context.WithoutCancel(ctx), topeBorrar)
	defer cancelar()
	if err := conn.Close(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "procesos: no se pudo cerrar la conexión de mantenimiento: %v\n", err)
	}
}

// ejecutarMantenimiento recibe un contexto y una sentencia sin parámetros, y la ejecuta sobre la
// base `postgres` con una conexión propia que cierra al terminar. Devuelve el error de la
// conexión o de la sentencia.
func ejecutarMantenimiento(ctx context.Context, sentencia string) error {
	conn, err := conectarMantenimiento(ctx)
	if err != nil {
		return err
	}
	defer cerrarMantenimiento(ctx, conn)
	if _, err := conn.Exec(ctx, sentencia); err != nil {
		return err
	}
	return nil
}

// esperarSinSesiones recibe el nombre de una base y espera, sondeando cada 50 ms con tope de
// topeSesiones, a que no quede ninguna sesión de cliente conectada a ella. Mira pg_stat_activity
// desde la base de mantenimiento: no abre nunca una conexión a la base consultada. Devuelve nil
// cuando no hay sesiones, o el error si la consulta falla o el tope vence con sesiones vivas.
func esperarSinSesiones(base string) error {
	ctx, cancelar := context.WithTimeout(context.Background(), topeSesiones)
	defer cancelar()
	conn, err := conectarMantenimiento(ctx)
	if err != nil {
		return err
	}
	defer cerrarMantenimiento(ctx, conn)

	const consulta = `SELECT count(*) FROM pg_stat_activity WHERE datname = $1 AND backend_type = 'client backend'`
	for {
		var abiertas int
		if err := conn.QueryRow(ctx, consulta, base).Scan(&abiertas); err != nil {
			return fmt.Errorf("sesiones abiertas en %s: %w", base, err)
		}
		if abiertas == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s sigue con %d sesiones abiertas tras %s", base, abiertas, topeSesiones)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// nuevaBase recibe el test y el nombre de un proceso, y crea su base propia con
// CREATE DATABASE proc_<proceso>_<binario> TEMPLATE plantilla, desde la base de mantenimiento.
// Devuelve la base clonada, ya migrada. Registra en t.Cleanup un DROP DATABASE … WITH (FORCE),
// que echa a quien siga conectado (un servidor, un *sql.DB). Llama a t.Fatalf si el nombre no es
// válido (ver nombreDeBase) o si no se puede clonar (p. ej. el mismo proceso dos veces).
func nuevaBase(t *testing.T, proceso string) baseClonada {
	t.Helper()
	nombre, err := nombreDeBase(proceso)
	if err != nil {
		t.Fatalf("nuevaBase: %v", err)
	}
	ctx, cancelar := context.WithTimeout(t.Context(), topeClonar)
	defer cancelar()
	if err := ejecutarMantenimiento(ctx, sentenciaClonar(nombre)); err != nil {
		t.Fatalf("nuevaBase(%q): no se pudo clonar %s en %s: %v", proceso, basePlantilla, nombre, err)
	}
	t.Cleanup(func() { borrarBase(t, nombre) })

	return baseClonada{
		Nombre:  nombre,
		Host:    urlInstancia.Hostname(),
		Puerto:  urlInstancia.Port(),
		Usuario: usuarioBD,
		Clave:   claveBD,
		DSN:     dsnDeBase(nombre),
	}
}

// sentenciaClonar recibe un nombre de base ya validado por nombreDeBase y devuelve la sentencia
// que la clona de `plantilla`. Los dos identificadores van entrecomillados con pgx.Identifier.
func sentenciaClonar(nombre string) string {
	return "CREATE DATABASE " + pgx.Identifier{nombre}.Sanitize() + " TEMPLATE " + pgx.Identifier{basePlantilla}.Sanitize()
}

// borrarBase recibe el test y el nombre de una base clonada y la borra con DROP DATABASE … WITH
// (FORCE). Es el cuerpo del t.Cleanup de nuevaBase; usa un contexto propio porque el del test ya
// está cancelado cuando corren los Cleanup. Si falla, marca el test con t.Errorf.
func borrarBase(t *testing.T, nombre string) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(context.Background(), topeBorrar)
	defer cancelar()
	sentencia := "DROP DATABASE IF EXISTS " + pgx.Identifier{nombre}.Sanitize() + " WITH (FORCE)"
	if err := ejecutarMantenimiento(ctx, sentencia); err != nil {
		t.Errorf("no se pudo borrar la base %s: %v", nombre, err)
	}
}

// Abrir abre la base con pgx/v5/stdlib y devuelve el *sql.DB, ya comprobado con un Ping. Registra
// en t.Cleanup el Close (un fallo de cierre se anota con t.Logf). Llama a t.Fatalf si no puede
// abrirla o el Ping no responde en 15 s.
func (b baseClonada) Abrir(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", b.DSN)
	if err != nil {
		t.Fatalf("abrir %s: %v", b.Nombre, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Logf("cerrar %s: %v", b.Nombre, err)
		}
	})
	ctx, cancelar := context.WithTimeout(t.Context(), topeAbrir)
	defer cancelar()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("abrir %s: ping: %v", b.Nombre, err)
	}
	return db
}

// Entorno devuelve las variables WAPP_DB_* «K=V» que apuntan a esta base: es entornoBD(b.Nombre).
// No falla.
func (b baseClonada) Entorno() []string {
	return entornoBD(b.Nombre)
}

// TestArnes_NombreDeBase comprueba la validación del nombre de proceso: solo [a-z0-9_]+ y como
// mucho 63 bytes en el nombre final, para que ninguna entrada llegue al SQL de CREATE DATABASE.
func TestArnes_NombreDeBase(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nombre  string
		proceso string
		valido  bool
	}{
		{"minusculas_digitos_y_guion_bajo", "p0_arranque9", true},
		{"vacio", "", false},
		{"mayuscula", "P0", false},
		{"guion", "p0-arranque", false},
		{"espacio", "p0 arranque", false},
		{"comillas_e_inyeccion", `a"; DROP DATABASE postgres; --`, false},
		{"acento", "arranqué", false},
		{"en_el_limite", string(slices.Repeat([]byte("a"), maxNombreBase-len("proc__"+binarioElegido()))), true},
		{"un_byte_de_mas", string(slices.Repeat([]byte("a"), maxNombreBase-len("proc__"+binarioElegido())+1)), false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			nombre, err := nombreDeBase(c.proceso)
			if c.valido {
				if err != nil {
					t.Fatalf("nombreDeBase(%q): %v", c.proceso, err)
				}
				if esperado := "proc_" + c.proceso + "_" + binarioElegido(); nombre != esperado {
					t.Fatalf("nombreDeBase(%q) = %q, quería %q", c.proceso, nombre, esperado)
				}
				return
			}
			if err == nil {
				t.Fatalf("nombreDeBase(%q) = %q sin error, debía rechazarlo", c.proceso, nombre)
			}
		})
	}
}

// TestArnes_BasePorProceso es la prueba de R9.1.c: dos procesos reciben bases distintas, las dos
// ya migradas por cmd/migrate (la plantilla), y lo que uno escribe no lo ve el otro.
func TestArnes_BasePorProceso(t *testing.T) {
	t.Parallel()
	a := nuevaBase(t, "arnes_a")
	b := nuevaBase(t, "arnes_b")
	if a.Nombre == b.Nombre {
		t.Fatalf("dos procesos recibieron la misma base %q", a.Nombre)
	}
	if esperado := "proc_arnes_a_" + binarioElegido(); a.Nombre != esperado {
		t.Fatalf("la base del proceso arnes_a se llama %q, quería %q", a.Nombre, esperado)
	}
	dbA, dbB := a.Abrir(t), b.Abrir(t)

	for _, c := range []struct {
		base baseClonada
		db   *sql.DB
	}{{a, dbA}, {b, dbB}} {
		if actual := consultaTexto(t, c.db, `SELECT current_database()`); actual != c.base.Nombre {
			t.Errorf("conectado a %q, quería %q", actual, c.base.Nombre)
		}
		// tenants es una tabla del esquema que aplica cmd/migrate: si está, la plantilla se migró.
		if migrada := consultaTexto(t, c.db, `SELECT to_regclass('public.tenants') IS NOT NULL`); migrada != "true" {
			t.Errorf("la base %s no trae public.tenants: la plantilla no estaba migrada", c.base.Nombre)
		}
		if !slices.Contains(c.base.Entorno(), "WAPP_DB_NAME="+c.base.Nombre) {
			t.Errorf("Entorno() de %s no apunta a su propia base: %v", c.base.Nombre, c.base.Entorno())
		}
		if c.base.Usuario != usuarioBD || c.base.Clave != claveBD || c.base.Host == "" || c.base.Puerto == "" {
			t.Errorf("la base %s trae datos de conexión incompletos: %+v", c.base.Nombre, c.base)
		}
	}

	// Lo escrito en A no aparece en B.
	if _, err := dbA.ExecContext(t.Context(), `CREATE TABLE marca_arnes (proceso text)`); err != nil {
		t.Fatalf("crear marca_arnes en %s: %v", a.Nombre, err)
	}
	if _, err := dbA.ExecContext(t.Context(), `INSERT INTO marca_arnes VALUES ('a')`); err != nil {
		t.Fatalf("insertar en marca_arnes de %s: %v", a.Nombre, err)
	}
	if visible := consultaTexto(t, dbB, `SELECT to_regclass('public.marca_arnes') IS NOT NULL`); visible != "false" {
		t.Errorf("la marca escrita en %s se ve en %s: las bases no están aisladas", a.Nombre, b.Nombre)
	}
}

// TestArnes_BasesEnParalelo clona varias bases a la vez con t.Parallel, como harán los procesos
// (R9.1.f): cada una debe nacer con su nombre, migrada, y sin chocar con las otras.
func TestArnes_BasesEnParalelo(t *testing.T) {
	t.Parallel()
	for i := range 3 {
		proceso := "arnes_par_" + strconv.Itoa(i)
		t.Run(proceso, func(t *testing.T) {
			t.Parallel()
			base := nuevaBase(t, proceso)
			db := base.Abrir(t)
			if actual := consultaTexto(t, db, `SELECT current_database()`); actual != base.Nombre {
				t.Errorf("conectado a %q, quería %q", actual, base.Nombre)
			}
			if migrada := consultaTexto(t, db, `SELECT to_regclass('public.tenants') IS NOT NULL`); migrada != "true" {
				t.Errorf("la base %s no trae public.tenants", base.Nombre)
			}
		})
	}
}

// consultaTexto recibe el test, una base abierta y una consulta de una fila y una columna, y
// devuelve el valor como texto (un booleano sale «true» o «false»). Llama a t.Fatalf si la
// consulta falla.
func consultaTexto(t *testing.T, db *sql.DB, consulta string) string {
	t.Helper()
	var valor string
	if err := db.QueryRowContext(t.Context(), consulta).Scan(&valor); err != nil {
		t.Fatalf("%s: %v", consulta, err)
	}
	return valor
}
