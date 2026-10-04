//go:build integracion

package procesos

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// Las migraciones por su puerta, cmd/migrate, como subproceso sobre una base clonada: la parte de
// P10 que re-expresa los ficheros de internal/platform/storage/postgres (integration_test.go,
// contacts_, drop_pii_claro_, profile_replay_ y replay_integration_test.go) y
// migrations/replay_integration_test.go. No hay servidor: cmd/migrate es el mismo binario sea cual
// sea el servidor bajo prueba, así que este test dice lo mismo en las dos pasadas.
//
// Los tests viejos llamaban a migrations.Migrate (y uno a applyStructure, privada) dentro del
// proceso. Aquí la réplica completa se provoca como ocurre en producción: el runner reaplica TODO el
// directorio cuando el hash registrado no coincide con el embebido, así que se altera el hash de la
// última fila de schema_version —el estado de una base cuya migración cambió sin subir la versión—
// y se corre el binario. Las comprobaciones de p10_plataforma_schema_test.go se repiten tras cada
// réplica: lo que se afirma no es una migración, es el estado al que CONVERGE el directorio entero.
//
// Lo que NO se lleva: TestIntegration_TenantRepositoryCRUD prueba postgres.NewTenantRepository,
// que ningún binario construye (el alta de empresas va por POST /admin/tenants, P1 y P2): no hay
// puerta por la que mirarlo.

const (
	// p10MigrateTimeout acota una corrida de cmd/migrate: la réplica completa tarda segundos.
	p10MigrateTimeout = 2 * time.Minute
	// p10AlteredHash es el hash que se le pone a la última fila de schema_version para forzar la réplica.
	p10AlteredHash = "hash-alterado-p10"
)

// Las tres líneas que escribe cmd/migrate: la del desenlace de aplicar y las dos de -status.
var (
	p10AppliedLine  = regexp.MustCompile(`migraciones aplicadas: version=(\S+) content_hash=(\S+) execution_id=(\S+) skipped=(true|false)`)
	p10RecordedLine = regexp.MustCompile(`estado registrado: version=(\S*) content_hash=(\S*) execution_id=(\S*) al_dia=(true|false)`)
	p10EmbeddedLine = regexp.MustCompile(`estado embebido:\s+version=(\S+) content_hash=(\S+)`)
)

// p10MigrateResult es lo que dijo una corrida de cmd/migrate: la versión y el hash, y si la base ya
// estaba al día (skipped al aplicar, al_dia en -status).
type p10MigrateResult struct {
	Version, Hash string
	UpToDate      bool
}

// p10RunMigrate corre el cmd/migrate compilado por TestMain contra la base dada, con un entorno
// construido desde cero (PATH, TMPDIR, un HOME vacío y las WAPP_DB_* de la base), y devuelve su
// salida entera. Falla (t.Fatalf) si el binario no arranca, sale con error o pasa del tope.
func p10RunMigrate(t *testing.T, base baseClonada, args ...string) string {
	t.Helper()
	home := t.TempDir()
	path := rutaBinario("migrate")
	var out bytes.Buffer
	// El binario es el que compiló el arnés y los argumentos son literales de este fichero.
	cmd := &exec.Cmd{
		Path: path,
		Args: append([]string{path}, args...),
		Dir:  home,
		Env: append([]string{
			"PATH=" + os.Getenv("PATH"),
			"TMPDIR=" + os.TempDir(),
			"HOME=" + home,
		}, base.Entorno()...),
		Stdout: &out,
		Stderr: &out,
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("lanzar cmd/migrate %v: %v", args, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ctx, cancel := context.WithTimeout(t.Context(), p10MigrateTimeout)
	defer cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cmd/migrate %v falló: %v\n%s", args, err, out.String())
		}
	case <-ctx.Done():
		if err := cmd.Process.Kill(); err != nil {
			t.Errorf("matar cmd/migrate: %v", err)
		}
		<-done
		t.Fatalf("cmd/migrate %v no terminó en %s\n%s", args, p10MigrateTimeout, out.String())
	}
	return out.String()
}

// p10Apply corre cmd/migrate sin argumentos y devuelve su desenlace. Falla (t.Fatalf) si la salida
// no trae la línea «migraciones aplicadas».
func p10Apply(t *testing.T, base baseClonada) p10MigrateResult {
	t.Helper()
	out := p10RunMigrate(t, base)
	m := p10AppliedLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("cmd/migrate terminó sin la línea «migraciones aplicadas»:\n%s", out)
	}
	return p10MigrateResult{Version: m[1], Hash: m[2], UpToDate: m[4] == "true"}
}

// p10Status corre cmd/migrate -status y devuelve lo registrado en la base y lo embebido en el
// binario. Falla (t.Fatalf) si falta alguna de las dos líneas.
func p10Status(t *testing.T, base baseClonada) (recorded, embedded p10MigrateResult) {
	t.Helper()
	out := p10RunMigrate(t, base, "-status")
	r, e := p10RecordedLine.FindStringSubmatch(out), p10EmbeddedLine.FindStringSubmatch(out)
	if r == nil || e == nil {
		t.Fatalf("cmd/migrate -status no trae sus dos líneas de estado:\n%s", out)
	}
	return p10MigrateResult{Version: r[1], Hash: r[2], UpToDate: r[4] == "true"}, p10MigrateResult{Version: e[1], Hash: e[2]}
}

// p10LastSchemaVersion devuelve «version hash» de la última fila de schema_version y cuántas hay.
func p10LastSchemaVersion(t *testing.T, db *sql.DB) (last string, rows int) {
	t.Helper()
	return consultaTexto(t, db, `SELECT version || ' ' || content_hash FROM public.schema_version ORDER BY id DESC LIMIT 1`),
		consultaEntero(t, db, `SELECT count(*) FROM public.schema_version`)
}

// p10ForceReplay altera SOLO el hash de la última fila de schema_version, dejando la versión.
func p10ForceReplay(t *testing.T, db *sql.DB) {
	t.Helper()
	if n := p10Exec(t, db, `UPDATE public.schema_version SET content_hash = $1
		WHERE id = (SELECT id FROM public.schema_version ORDER BY id DESC LIMIT 1)`, p10AlteredHash); n != 1 {
		t.Fatalf("alterar el hash registrado tocó %d filas, quería 1", n)
	}
}

// TestP10_MigrationsReplay es la réplica completa de las migraciones sobre un clon CON DATOS, y su
// idempotencia:
//
//  1. La base clonada ya está al día: -status lo dice sin escribir, y aplicar contesta skipped=true
//     con la versión y el hash embebidos, sin añadir fila a schema_version.
//  2. Se siembran datos de negocio y se comprueba el esquema (p10CheckSchema).
//  3. Con el hash alterado, -status dice que hay cambios y aplicar REAPLICA (skipped=false) y vuelve
//     a registrar el hash embebido; tras ello el esquema es el mismo y no se perdió ni se pisó una
//     fila. Dos veces seguidas: una migración que alternara (crear en una, borrar en la otra) se
//     vería en la segunda.
//  4. Aplicar otra vez vuelve a ser skipped=true.
func TestP10_MigrationsReplay(t *testing.T) {
	t.Parallel()
	base := nuevaBase(t, "p10_migrations")
	m := &p10Migration{base: base, db: base.Abrir(t)}
	m.checkFreshClone(t)

	t.Run("second_apply_is_skipped", func(t *testing.T) { m.wantSkipped(t, "aplicar sobre una base al día") })
	seed := p10SeedReplayData(t, m.db)
	t.Run("schema_before_replay", func(t *testing.T) { p10CheckSchema(t, m.db, seed, "a") })
	for i := 1; i <= 2; i++ {
		t.Run("full_replay_"+strconv.Itoa(i), func(t *testing.T) {
			m.fullReplay(t)
			p10CheckSchema(t, m.db, seed, "r"+strconv.Itoa(i))
		})
	}
	t.Run("apply_after_replay_is_skipped", func(t *testing.T) { m.wantSkipped(t, "aplicar tras la réplica") })
}

// p10Migration es la base del test de migraciones y lo que el binario embebe: la versión y el
// hash, y «version hash» tal como tiene que quedar la última fila de schema_version.
type p10Migration struct {
	base     baseClonada
	db       *sql.DB
	embedded p10MigrateResult
	clean    string
}

// checkFreshClone es el paso 1 sin escribir: -status sobre la base recién clonada dice que está al
// día, con la misma versión y el mismo hash que el binario embebe y que la última fila de
// schema_version. Falla (t.Fatalf) si no: nada de lo que sigue tendría sentido.
func (m *p10Migration) checkFreshClone(t *testing.T) {
	t.Helper()
	recorded, embedded := p10Status(t, m.base)
	if !recorded.UpToDate || recorded.Version != embedded.Version || recorded.Hash != embedded.Hash {
		t.Fatalf("-status sobre la base recién clonada: registrado %+v, embebido %+v; quería al_dia=true y los mismos valores", recorded, embedded)
	}
	m.embedded, m.clean = embedded, embedded.Version+" "+embedded.Hash
	if last, _ := p10LastSchemaVersion(t, m.db); last != m.clean {
		t.Fatalf("la última fila de schema_version es %q y el binario embebe %q", last, m.clean)
	}
}

// wantSkipped aplica sobre una base al día y exige skipped=true con la versión y el hash embebidos,
// sin escribir en schema_version, y que -status siga diciendo al_dia=true.
func (m *p10Migration) wantSkipped(t *testing.T, what string) {
	t.Helper()
	before, rows := p10LastSchemaVersion(t, m.db)
	got := p10Apply(t, m.base)
	if !got.UpToDate || got.Version != m.embedded.Version || got.Hash != m.embedded.Hash {
		t.Errorf("%s: %+v, quería skipped=true con %+v", what, got, m.embedded)
	}
	if after, n := p10LastSchemaVersion(t, m.db); after != before || n != rows || after != m.clean {
		t.Errorf("%s escribió en schema_version: %q (%d filas), era %q (%d)", what, after, n, before, rows)
	}
	if recorded, _ := p10Status(t, m.base); !recorded.UpToDate {
		t.Errorf("%s: -status dice %+v, quería al_dia=true", what, recorded)
	}
}

// fullReplay altera el hash registrado y aplica: -status lo ve pendiente sin escribir nada, el
// runner REAPLICA (skipped=false) y vuelve a registrar la versión y el hash embebidos.
func (m *p10Migration) fullReplay(t *testing.T) {
	t.Helper()
	p10ForceReplay(t, m.db)
	if recorded, _ := p10Status(t, m.base); recorded.UpToDate || recorded.Hash != p10AlteredHash {
		t.Errorf("-status con el hash alterado: %+v, quería al_dia=false y el hash alterado (no debe escribir)", recorded)
	}
	got := p10Apply(t, m.base)
	if got.UpToDate {
		t.Fatalf("con el hash alterado el runner DEBE reaplicar, y contestó skipped=true")
	}
	if got.Version != m.embedded.Version || got.Hash != m.embedded.Hash {
		t.Errorf("la réplica registró %+v, quería la versión y el hash embebidos %+v", got, m.embedded)
	}
	if last, _ := p10LastSchemaVersion(t, m.db); last != m.clean {
		t.Errorf("tras la réplica, la última fila de schema_version es %q, quería %q", last, m.clean)
	}
}
