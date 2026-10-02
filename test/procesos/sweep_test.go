//go:build integracion

package procesos

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// El directorio temporal de la corrida y el barrido de los que quedaron huérfanos (D-F9-8).
//
// TestMain compila los binarios en un directorio temporal y lo borra con un defer, que no corre si
// el binario de test muere antes (kill -9, pánico, timeout): quedan ~45 MB por corrida muerta. Por
// eso, al entrar, TestMain barre los que dejaron las corridas anteriores.
//
// 🔴 BORRAR ES DESTRUCTIVO, y el directorio temporal del sistema es de todos. Un directorio solo se
// barre si cumple TODO esto, y ante cualquier duda (un error al mirarlo, un tipo que no es el
// esperado) se deja donde está:
//
//  1. Cuelga DIRECTAMENTE de la raíz que se barre (os.TempDir() en TestMain): no se desciende.
//  2. Su nombre casa con el patrón exacto del arnés: runDirPrefix seguido solo de cifras, que es lo
//     que produce os.MkdirTemp(raíz, runDirPrefix).
//  3. Es un directorio de verdad. Un enlace simbólico con ese nombre no se sigue ni se toca.
//  4. Lleva la FIRMA del arnés: el fichero sweepMarkerName, regular (no un enlace), con el contenido
//     exacto sweepMarkerSignature. El nombre y la estructura no bastan: «procesos-» es una palabra
//     corriente y otro arnés copiado de este dejaría los mismos ficheros.
//  5. Su última modificación es de hace MÁS de sweepMinAge: una corrida viva lo modifica al compilar
//     y dura menos que eso, así que no se pisa a una corrida concurrente.
//
// Consecuencia aceptada del punto 4: los directorios que dejaron las corridas ANTERIORES a este
// marcador no lo llevan, así que este barrido no los reconoce y no los borra nunca.
//
// ⚠️ QUIEN TOQUE ESTAS CONDICIONES: TestMain barre el directorio temporal REAL al entrar, en
// CUALQUIER corrida del paquete, también la que lanzas para probar tu cambio. Una condición
// aflojada «solo para ver si el test la caza» borra de verdad lo que deje de proteger (ya pasó: una
// mutación de prueba sin la condición 4 se llevó tres directorios sin firma). Mientras edites este
// fichero, corre el paquete con TMPDIR apuntando a un directorio de usar y tirar.

const (
	// runDirPrefix es el patrón de os.MkdirTemp con el que nace el directorio de la corrida.
	runDirPrefix = "procesos-"
	// sweepMarkerName es el fichero marcador que createRunDir deja en la raíz del directorio de la
	// corrida, y que el barrido exige para reconocerlo como propio.
	sweepMarkerName = ".wapp-procesos-harness"
	// sweepMarkerSignature es el contenido exacto del marcador: la ruta de importación de este
	// paquete. No es un secreto ni protege nada: identifica de quién es el directorio.
	sweepMarkerSignature = "github.com/EduGoGroup/wapp-cloud-platform/test/procesos\n"
	// sweepMinAge es la antigüedad a partir de la cual un directorio con la firma se da por
	// huérfano. Una corrida de `make test-procesos` tiene un tope de 30 minutos por binario.
	sweepMinAge = time.Hour
)

// runDirName casa los nombres que produce os.MkdirTemp con runDirPrefix: el prefijo y un número
// aleatorio en decimal, sin nada delante ni detrás.
var runDirName = regexp.MustCompile("^" + regexp.QuoteMeta(runDirPrefix) + "[0-9]+$")

// createRunDir crea, directamente bajo root, el directorio temporal de una corrida (nombre
// runDirPrefix + número) y deja dentro el marcador de barrido con la firma del arnés. Devuelve su
// ruta. Si el directorio se crea pero el marcador no se puede escribir, lo borra y devuelve el
// error: no queda nunca un directorio de corrida sin firma, que el barrido no reconocería.
func createRunDir(root string) (string, error) {
	dir, err := os.MkdirTemp(root, runDirPrefix)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, sweepMarkerName), []byte(sweepMarkerSignature), 0o600); err != nil {
		return "", errors.Join(fmt.Errorf("escribir el marcador de barrido en %s: %w", dir, err), os.RemoveAll(dir))
	}
	return dir, nil
}

// orphanRunDirs recibe la raíz que se barre, el instante que cuenta como «ahora» y la antigüedad
// mínima, y devuelve las rutas de los directorios de corrida huérfanos que hay DIRECTAMENTE bajo
// root, en orden de nombre: los que cumplen las cinco condiciones de la cabecera de este fichero.
// NO borra ni modifica nada: solo mira. Devuelve error únicamente si no puede listar root.
func orphanRunDirs(root string, now time.Time, minAge time.Duration) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("listar %s: %w", root, err)
	}
	var orphans []string
	for _, entry := range entries {
		if !runDirName.MatchString(entry.Name()) {
			continue
		}
		if path := filepath.Join(root, entry.Name()); isOrphanRunDir(path, now, minAge) {
			orphans = append(orphans, path)
		}
	}
	return orphans, nil
}

// isOrphanRunDir dice si la ruta dada es un directorio de verdad (Lstat: un enlace simbólico no lo
// es y no se sigue), modificado por última vez hace más de minAge respecto de now, y con la firma
// del arnés. El nombre lo comprueba quien llama. Un error al mirarlo cuenta como «no».
func isOrphanRunDir(path string, now time.Time, minAge time.Duration) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	if now.Sub(info.ModTime()) <= minAge {
		return false
	}
	return hasSweepMarker(path)
}

// hasSweepMarker dice si dir contiene el marcador de barrido: un fichero REGULAR llamado
// sweepMarkerName (Lstat: un enlace simbólico a otro fichero no vale) cuyo contenido es exactamente
// sweepMarkerSignature. Un error al mirarlo o al leerlo cuenta como «no».
func hasSweepMarker(dir string) bool {
	marker := filepath.Join(dir, sweepMarkerName)
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(sweepMarkerSignature)) {
		return false
	}
	content, err := os.ReadFile(marker) //nolint:gosec // G304: ruta fija del arnés bajo un directorio que ya pasó el patrón y Lstat
	return err == nil && string(content) == sweepMarkerSignature
}

// sweepOrphanRunDirs borra con remove (os.RemoveAll en TestMain, que no sigue enlaces) los
// directorios de corrida huérfanos de root —los que devuelve orphanRunDirs con sweepMinAge— y
// devuelve cuántos borró. No falla nunca: si no puede listar root, o no puede borrar uno, lo dice
// por logf y sigue con el siguiente; un barrido que no sale no es motivo para que la corrida falle.
// Cada borrado también se dice por logf.
func sweepOrphanRunDirs(root string, now time.Time, remove func(string) error, logf func(format string, args ...any)) int {
	orphans, err := orphanRunDirs(root, now, sweepMinAge)
	if err != nil {
		logf("procesos: no se pudieron buscar directorios huérfanos (la corrida sigue): %v\n", err)
		return 0
	}
	removed := 0
	for _, dir := range orphans {
		if err := remove(dir); err != nil {
			logf("procesos: no se pudo barrer el directorio huérfano %s (la corrida sigue): %v\n", dir, err)
			continue
		}
		removed++
		logf("procesos: barrido el directorio huérfano %s, de una corrida que no llegó a borrarlo\n", dir)
	}
	return removed
}

// stderrf escribe en stderr con formato: es el logf de TestMain.
func stderrf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
}

// ---------------------------------------------------------------------------------------------
// Tests propios del arnés
// ---------------------------------------------------------------------------------------------

// sweepFixture describe un directorio (o algo que se le parece) que el test deja bajo la raíz antes
// de barrer: su nombre, su antigüedad, qué marcador lleva y si debe acabar barrido.
type sweepFixture struct {
	name   string
	age    time.Duration // antigüedad de la última modificación respecto del «ahora» del test
	marker string        // contenido del marcador; vacío = sin marcador
	swept  bool          // si el barrido debe reconocerlo y borrarlo
}

// sweepScenario es lo que el test deja montado: la raíz que se barre, el «ahora» del test, las
// rutas que el barrido debe reconocer (en orden de nombre), las que deben seguir en su sitio y el
// directorio de fuera de la raíz al que apunta el enlace simbólico.
type sweepScenario struct {
	root      string
	now       time.Time
	want      []string
	survivors []string
	outside   string
}

// Antigüedades del escenario: un directorio «viejo» pasa de sobra de sweepMinAge y uno «reciente»
// no llega.
const sweepOld, sweepRecent = 3 * time.Hour, 30 * time.Minute

// sweepMust falla el test (t.Fatalf) si err no es nil, diciendo qué se estaba haciendo.
func sweepMust(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// sweepPlant crea bajo root el directorio de f con el contenido que deja una corrida real (migrate,
// el servidor y un home vacío), su marcador si lo lleva, y le pone la fecha de modificación que le
// toca respecto de now. Devuelve su ruta. Falla el test (t.Fatalf) si algo no se puede crear.
func sweepPlant(t *testing.T, root string, now time.Time, f sweepFixture) string {
	t.Helper()
	dir := filepath.Join(root, f.name)
	sweepMust(t, "sweepPlant "+f.name, os.MkdirAll(filepath.Join(dir, "home"), 0o700))
	for _, binary := range []string{"migrate", "servidor-nuevo"} {
		sweepMust(t, "sweepPlant "+f.name, os.WriteFile(filepath.Join(dir, binary), []byte("binario"), 0o600))
	}
	if f.marker != "" {
		sweepMust(t, "sweepPlant "+f.name, os.WriteFile(filepath.Join(dir, sweepMarkerName), []byte(f.marker), 0o600))
	}
	sweepSetAge(t, dir, now, f.age)
	return dir
}

// sweepSetAge deja la última modificación de path en now-age. Va lo último: crear algo dentro de
// un directorio le cambia la fecha.
func sweepSetAge(t *testing.T, path string, now time.Time, age time.Duration) {
	t.Helper()
	when := now.Add(-age)
	sweepMust(t, "sweepSetAge "+path, os.Chtimes(path, when, when))
}

// sweepExists dice si path existe, sin seguir enlaces. Falla el test si no se puede saber.
func sweepExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sweepExists(%s): %v", path, err)
	}
	return err == nil
}

// sweepFixtures son los directorios del escenario que cuelgan directamente de la raíz: los dos que
// el barrido debe llevarse y, debajo, los que se apartan de ellos en UNA sola cosa.
func sweepFixtures() []sweepFixture {
	const sig = sweepMarkerSignature
	return []sweepFixture{
		{name: "procesos-1000000001", age: sweepOld, marker: sig, swept: true},
		{name: "procesos-7", age: sweepMinAge + time.Minute, marker: sig, swept: true},
		// reciente: puede ser una corrida concurrente
		{name: "procesos-1000000002", age: sweepRecent, marker: sig},
		{name: "procesos-1000000003", age: sweepMinAge, marker: sig}, // justo una hora: aún no
		{name: "procesos-1000000004", age: -time.Hour, marker: sig},  // fecha en el futuro
		// viejo, con el contenido de una corrida, pero sin la firma (o con otra)
		{name: "procesos-1000000005", age: sweepOld},
		{name: "procesos-1000000006", age: sweepOld, marker: "otro arnés\n"},
		{name: "procesos-1000000007", age: sweepOld, marker: sig + "y algo más\n"},
		{name: "procesos-1000000008", age: sweepOld, marker: strings.TrimSuffix(sig, "\n")},
		// viejo y con firma, pero el nombre no es el del patrón
		{name: "procesos-", age: sweepOld, marker: sig},
		{name: "procesos-abc", age: sweepOld, marker: sig},
		{name: "procesos-123abc", age: sweepOld, marker: sig},
		{name: "procesos-123.bak", age: sweepOld, marker: sig},
		{name: "procesos-12-34", age: sweepOld, marker: sig},
		{name: "Procesos-123", age: sweepOld, marker: sig},
		{name: "xprocesos-123", age: sweepOld, marker: sig},
		{name: "procesos_123", age: sweepOld, marker: sig},
		{name: "proceso-123", age: sweepOld, marker: sig},
	}
}

// sweepPlantOddities deja en el escenario lo que no es un directorio corriente bajo la raíz, todo
// «viejo» y nada barrible, y lo apunta en survivors: un directorio con firma un nivel por debajo de
// la raíz; un enlace simbólico con nombre del patrón a un directorio con firma de FUERA de la raíz
// (sc.outside); un directorio cuyo marcador es un enlace simbólico a un fichero con la firma; otro
// cuyo «marcador» es un directorio; y un fichero regular con nombre del patrón.
func sweepPlantOddities(t *testing.T, sc *sweepScenario) {
	t.Helper()
	signed := func(name string) sweepFixture {
		return sweepFixture{name: name, age: sweepOld, marker: sweepMarkerSignature}
	}
	nested := sweepPlant(t, filepath.Join(sc.root, "otra-cosa"), sc.now, signed("procesos-2000000001"))
	sweepSetAge(t, filepath.Join(sc.root, "otra-cosa"), sc.now, sweepOld)

	sc.outside = sweepPlant(t, t.TempDir(), sc.now, signed("procesos-3000000001"))
	link := filepath.Join(sc.root, "procesos-3000000002")
	sweepMust(t, "crear el enlace simbólico", os.Symlink(sc.outside, link))

	signature := filepath.Join(t.TempDir(), "firma")
	sweepMust(t, "escribir la firma suelta", os.WriteFile(signature, []byte(sweepMarkerSignature), 0o600))
	linkedMarker := sweepPlant(t, sc.root, sc.now, sweepFixture{name: "procesos-4000000001", age: sweepRecent})
	sweepMust(t, "crear el marcador enlazado", os.Symlink(signature, filepath.Join(linkedMarker, sweepMarkerName)))
	sweepSetAge(t, linkedMarker, sc.now, sweepOld)

	dirMarker := sweepPlant(t, sc.root, sc.now, sweepFixture{name: "procesos-4000000002", age: sweepRecent})
	sweepMust(t, "crear el marcador-directorio", os.Mkdir(filepath.Join(dirMarker, sweepMarkerName), 0o700))
	sweepSetAge(t, dirMarker, sc.now, sweepOld)

	plainFile := filepath.Join(sc.root, "procesos-4000000003")
	sweepMust(t, "crear el fichero con nombre de directorio de corrida", os.WriteFile(plainFile, []byte(sweepMarkerSignature), 0o600))
	sweepSetAge(t, plainFile, sc.now, sweepOld)

	sc.survivors = append(sc.survivors, nested, sc.outside, link, linkedMarker, dirMarker, plainFile, signature)
}

// sweepBuildScenario monta el escenario completo bajo un t.TempDir(). El «ahora» va dos días por
// delante del reloj: así un enlace simbólico recién creado ya es «viejo» y lo único que lo salva es
// no ser un directorio.
func sweepBuildScenario(t *testing.T) *sweepScenario {
	t.Helper()
	sc := &sweepScenario{root: t.TempDir(), now: time.Now().Add(48 * time.Hour)}
	for _, f := range sweepFixtures() {
		path := sweepPlant(t, sc.root, sc.now, f)
		if f.swept {
			sc.want = append(sc.want, path)
		} else {
			sc.survivors = append(sc.survivors, path)
		}
	}
	slices.Sort(sc.want)
	sweepPlantOddities(t, sc)
	return sc
}

// sweepLog es un logf que guarda las líneas, para mirar qué dijo el barrido.
type sweepLog struct{ lines []string }

func (l *sweepLog) logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *sweepLog) String() string { return strings.Join(l.lines, "") }

// TestArnes_SweepOrphanRunDirs prueba qué barre el arnés y qué no, sobre una raíz de t.TempDir(): se
// borra SOLO el directorio que cuelga directamente de la raíz, con el nombre exacto del arnés, de
// verdad (no un enlace), con la firma y con más de una hora. Cada caso que se queda se aparta del
// que se borra en una sola cosa. Los pasos van en orden y sobre el mismo escenario: reconocer no
// borra; un fallo al borrar se dice y no detiene el barrido; el barrido se lleva lo reconocido y
// nada más; y una raíz que no se puede listar se dice y no falla. No necesita Docker más que para
// la corrida que lo contiene.
func TestArnes_SweepOrphanRunDirs(t *testing.T) {
	t.Parallel()
	sc := sweepBuildScenario(t)
	sweepCheckRecognition(t, sc)
	t.Run("un fallo al borrar se dice y no detiene el barrido", func(t *testing.T) { sweepCheckRemoveFailure(t, sc) })
	t.Run("barre lo reconocido y nada más", func(t *testing.T) { sweepCheckRemoval(t, sc) })
	t.Run("una raíz que no se puede listar se dice y no falla", func(t *testing.T) { sweepCheckUnlistableRoot(t, sc) })
}

// sweepCheckRecognition comprueba la función que decide: orphanRunDirs devuelve exactamente los
// directorios que el escenario espera, y no borra nada (todo sigue en su sitio).
func sweepCheckRecognition(t *testing.T, sc *sweepScenario) {
	t.Helper()
	got, err := orphanRunDirs(sc.root, sc.now, sweepMinAge)
	sweepMust(t, "orphanRunDirs", err)
	if !slices.Equal(got, sc.want) {
		t.Fatalf("orphanRunDirs reconoce\n  %v\ny debía reconocer solo\n  %v", got, sc.want)
	}
	for _, path := range append(slices.Clone(sc.want), sc.survivors...) {
		if !sweepExists(t, path) {
			t.Fatalf("orphanRunDirs borró %s: solo debía mirar", path)
		}
	}
}

// sweepCheckRemoveFailure barre con un borrador que falla en el primer huérfano: ese sigue en su
// sitio, los demás se borran, y el log dice una línea por directorio, la del fallo con su causa.
func sweepCheckRemoveFailure(t *testing.T, sc *sweepScenario) {
	var log sweepLog
	errRemove := errors.New("no se deja borrar")
	removed := sweepOrphanRunDirs(sc.root, sc.now, func(dir string) error {
		if dir == sc.want[0] {
			return errRemove
		}
		return os.RemoveAll(dir)
	}, log.logf)
	if removed != len(sc.want)-1 {
		t.Errorf("borrados = %d, quería %d (todos menos el que falla)", removed, len(sc.want)-1)
	}
	if !sweepExists(t, sc.want[0]) {
		t.Errorf("%s desapareció aunque su borrado falló", sc.want[0])
	}
	for _, path := range sc.want[1:] {
		if sweepExists(t, path) {
			t.Errorf("%s sigue ahí: el fallo del anterior detuvo el barrido", path)
		}
	}
	said := strings.Contains(log.String(), "no se pudo barrer el directorio huérfano "+sc.want[0]) && strings.Contains(log.String(), errRemove.Error())
	if len(log.lines) != len(sc.want) || !said {
		t.Errorf("el log del barrido no dice el fallo, o no dice una línea por directorio:\n%s", log.String())
	}
}

// sweepCheckRemoval barre de verdad (os.RemoveAll): se va el huérfano que quedaba, todo lo que no
// cumplía las condiciones sigue en su sitio —el destino del enlace simbólico, con su contenido—, y
// un segundo barrido no encuentra nada más.
func sweepCheckRemoval(t *testing.T, sc *sweepScenario) {
	var log sweepLog
	if removed := sweepOrphanRunDirs(sc.root, sc.now, os.RemoveAll, log.logf); removed != 1 {
		t.Errorf("borrados = %d, quería 1 (el que antes falló)\n%s", removed, log.String())
	}
	for _, path := range sc.want {
		if sweepExists(t, path) {
			t.Errorf("%s sigue ahí: era un huérfano con firma", path)
		}
	}
	for _, path := range sc.survivors {
		if !sweepExists(t, path) {
			t.Errorf("el barrido borró %s, que no cumplía las condiciones", path)
		}
	}
	if !hasSweepMarker(sc.outside) || !sweepExists(t, filepath.Join(sc.outside, "migrate")) {
		t.Errorf("el barrido tocó el contenido de %s, el destino del enlace simbólico", sc.outside)
	}
	if again := sweepOrphanRunDirs(sc.root, sc.now, os.RemoveAll, log.logf); again != 0 {
		t.Errorf("un segundo barrido borró %d directorios más", again)
	}
}

// sweepCheckUnlistableRoot barre una raíz que no existe: no intenta borrar nada, devuelve 0 y lo
// dice en una línea.
func sweepCheckUnlistableRoot(t *testing.T, sc *sweepScenario) {
	var log sweepLog
	removed := sweepOrphanRunDirs(filepath.Join(sc.root, "no-existe"), sc.now, func(dir string) error {
		t.Errorf("se intentó borrar %s sin haber podido listar la raíz", dir)
		return nil
	}, log.logf)
	if removed != 0 || len(log.lines) != 1 || !strings.Contains(log.String(), "no se pudieron buscar directorios huérfanos") {
		t.Errorf("borrados = %d, log = %q; quería 0 y una línea que lo diga", removed, log.lines)
	}
}

// TestArnes_CreateRunDir prueba que el directorio que crea el arnés es exactamente el que su barrido
// reconoce: cuelga de la raíz dada, su nombre casa con el patrón, lleva el marcador regular con la
// firma, recién creado NO es un huérfano, y pasado el plazo sí. Así el patrón de creación y el de
// reconocimiento no pueden separarse sin que esto falle. No necesita Docker más que para la corrida
// que lo contiene.
func TestArnes_CreateRunDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir, err := createRunDir(root)
	if err != nil {
		t.Fatalf("createRunDir: %v", err)
	}
	if filepath.Dir(dir) != root || !runDirName.MatchString(filepath.Base(dir)) {
		t.Errorf("createRunDir devolvió %s; quería un %s<número> directamente bajo %s", dir, runDirPrefix, root)
	}
	if !hasSweepMarker(dir) {
		t.Errorf("el directorio recién creado no lleva el marcador %s con la firma", sweepMarkerName)
	}
	if got, err := orphanRunDirs(root, time.Now(), sweepMinAge); err != nil || len(got) != 0 {
		t.Errorf("recién creado, orphanRunDirs = %v, %v; quería ninguno: es una corrida viva", got, err)
	}
	if got, err := orphanRunDirs(root, time.Now().Add(sweepMinAge+time.Minute), sweepMinAge); err != nil || !slices.Equal(got, []string{dir}) {
		t.Errorf("pasada una hora, orphanRunDirs = %v, %v; quería [%s]", got, err, dir)
	}
	if sweepMinAge != time.Hour {
		t.Errorf("sweepMinAge = %s, quería una hora (D-F9-8)", sweepMinAge)
	}

	if _, err := createRunDir(filepath.Join(root, "no-existe")); err == nil {
		t.Errorf("createRunDir bajo una raíz que no existe no devolvió error")
	}
}
