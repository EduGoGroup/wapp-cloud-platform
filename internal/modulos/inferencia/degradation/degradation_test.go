//go:build pendiente

package degradation_test

// Los tests de fichero del dominio de degradation: el vocabulario cerrado de motivos (y su
// candado contra el CHECK de la migración, D-F4-2), el de vías, la ventana, la forma de Notice,
// ListFilter y Store, y del escritor Notifier la ventana resuelta en el uso (T-13) y RecordAhora.
// Las promesas de Record están en degradation_record_test.go (E-13). Lo que el puerto Store
// PROMETE lo afirma degradationhelpertest.Contrato; aquí el store es su doble, Memoria, o un espía.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation/degradationhelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

const testTenant = "t-degradacion"

// window es la ventana con la que se montan los Notifier de estos tests.
const window = 15 * time.Minute

// base es un instante que cae justo en un borde de ventana de 15 minutos.
var base = time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

// wantReasons son los ocho literales, escritos a mano y en el orden de la 0075. Viajan al wire
// y a la base: si uno cambia, este fichero lo dice.
var wantReasons = []string{
	"ollama_down", "breaker_open", "edge_offline", "timeout",
	"api_error", "credencial", "lease_invalid", "edge_sin_capacidad",
}

// adversarialSuffixes convierten un literal válido en uno que NO pertenece al vocabulario por
// mucho que se le parezca: se añaden al final. Es el corpus adversario de reglas.md §5 (espacios
// Unicode y dígitos no ASCII, en la vía y en el motivo).
var adversarialSuffixes = []string{
	" ", "\n", "\t", "\u00a0", "\u2003", "\u3000", "\u200b", "\u0663", "\uff11", "1",
}

// withAdversarialSuffixes devuelve cada literal con cada sufijo adversario pegado al final.
func withAdversarialSuffixes(literals ...string) []string {
	out := make([]string, 0, len(literals)*len(adversarialSuffixes))
	for _, literal := range literals {
		for _, suffix := range adversarialSuffixes {
			out = append(out, literal+suffix)
		}
	}
	return out
}

// TestReasons_AreTheEightLiterals (R4.5.a): las ocho constantes con su literal exacto, y
// Reasons() las devuelve todas, en el orden de la migración.
func TestReasons_AreTheEightLiterals(t *testing.T) {
	constants := []degradation.Reason{
		degradation.ReasonOllamaDown, degradation.ReasonBreakerOpen, degradation.ReasonEdgeOffline,
		degradation.ReasonTimeout, degradation.ReasonAPIError, degradation.ReasonCredencial,
		degradation.ReasonLeaseInvalid, degradation.ReasonEdgeSinCapacidad,
	}
	got := degradation.Reasons()
	if len(constants) != len(wantReasons) || len(got) != len(wantReasons) {
		t.Fatalf("hay %d constantes y Reasons() devuelve %d motivos, quería %d", len(constants), len(got), len(wantReasons))
	}
	for i, want := range wantReasons {
		if string(constants[i]) != want {
			t.Errorf("la constante %d vale %q, quería %q", i, constants[i], want)
		}
		if string(got[i]) != want {
			t.Errorf("Reasons()[%d] = %q, quería %q (el orden es el del IN de la 0075)", i, got[i], want)
		}
		if s := constants[i].String(); s != want {
			t.Errorf("Reason(%q).String() = %q, quería el literal", want, s)
		}
	}
	if s := degradation.Reason("fastlane").String(); s != "fastlane" {
		t.Errorf("String() de un motivo fuera del vocabulario = %q, quería el literal tal cual", s)
	}
}

// TestReason_Valid: true solo para los ocho literales exactos. Los motivos SANOS de alto volumen
// dan false —es lo que impide que avisar el funcionamiento correcto mate el canal (D-044.32)—,
// igual que los vecinos plausibles y que cualquier variante con espacios, mayúsculas o Unicode.
func TestReason_Valid(t *testing.T) {
	for _, want := range wantReasons {
		if !degradation.Reason(want).Valid() {
			t.Errorf("Valid(%q) = false, quería true", want)
		}
	}
	invalid := []string{
		// Los cuatro que el pipeline produce cuando TODO va bien.
		"atajo_determinista", "fastlane", "sin_texto", "umbral_no_alcanzado",
		// Vecinos: el error del frame que no se llama así, la variante en inglés, una vía.
		"lease_expired", "edge_busy", "local", "api",
		"", "OLLAMA_DOWN", "Timeout", " timeout", "ｔｉｍｅｏｕｔ", "time\u200bout", "t\u0456meout", // U+0456 es la «i» cirílica
		"timeout|api_error", "ollama_down,breaker_open",
	}
	for _, reason := range slices.Concat(invalid, withAdversarialSuffixes("timeout", "edge_sin_capacidad")) {
		if degradation.Reason(reason).Valid() {
			t.Errorf("Valid(%q) = true, quería false: el vocabulario es cerrado y literal", reason)
		}
	}
}

// TestReasons_ReturnsACopy: mutar lo que devuelve Reasons() no abre el vocabulario del paquete.
func TestReasons_ReturnsACopy(t *testing.T) {
	list := degradation.Reasons()
	list[0] = degradation.Reason("fastlane")
	if again := degradation.Reasons(); string(again[0]) != wantReasons[0] {
		t.Errorf("tras mutar la copia, Reasons()[0] = %q: Reasons devuelve el slice del paquete", again[0])
	}
	if !degradation.ReasonOllamaDown.Valid() {
		t.Error("mutar la copia de Reasons() sacó ollama_down del vocabulario")
	}
	if degradation.Reason("fastlane").Valid() {
		t.Error("mutar la copia de Reasons() metió un motivo sano en el vocabulario")
	}
}

// migrationsDir es el directorio de scripts que el runner embebe y aplica en orden de nombre. Se
// lee de disco porque el embed.FS de migrations no está exportado; el `go:embed` es
// `structure/*.sql` sobre este mismo directorio, así que lo que está aquí es lo embebido.
const migrationsDir = "../../../platform/storage/postgres/migrations/structure"

// reasonCheckRE captura la lista del CHECK del vocabulario de motivos. Solo casa el `ADD
// CONSTRAINT … CHECK (reason IN (…))` completo: ni el `DROP CONSTRAINT IF EXISTS` de al lado ni
// las menciones al nombre dentro de la prosa.
var reasonCheckRE = regexp.MustCompile(
	`(?s)ADD\s+CONSTRAINT\s+owner_degradation_notices_reason_check\s+CHECK\s*\(\s*reason\s+IN\s*\(([^)]*)\)`)

// checkDefinition es una definición del CHECK: el fichero que la trae y sus literales.
type checkDefinition struct {
	file    string
	reasons []string
}

// TestReasons_MatchTheMigrationCheck es el candado del vocabulario (D-F4-2, R4.5.a; uno de los
// tests autorizados a leer un fichero como texto, 05 §3.2). El vocabulario vive en DOS sitios
// que no se hablan —las constantes de degradation.go y el CHECK de un `.sql`— y esto los compara
// como CONJUNTOS, leyendo la migración de disco: una lista copiada a mano pasaría con cualquier
// CHECK. Barre el directorio entero porque el runner hace full-replay en orden de nombre y el
// vocabulario vigente es el de la ÚLTIMA redefinición; y exige que ninguna estreche el dominio,
// porque un ADD CONSTRAINT que deja fuera un motivo con filas aborta el arranque.
func TestReasons_MatchTheMigrationCheck(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.sql"))
	if err != nil {
		t.Fatalf("listando migraciones en %s: %v", migrationsDir, err)
	}
	if len(files) == 0 {
		t.Fatalf("cero migraciones en %s: la ruta del test se quedó atrás", migrationsDir)
	}
	slices.Sort(files) // el runner las aplica en orden de nombre; el test también

	var definitions []checkDefinition
	for _, file := range files {
		raw, err := os.ReadFile(file) // #nosec G304 -- ruta de Glob sobre un directorio constante
		if err != nil {
			t.Fatalf("leyendo %s: %v", file, err)
		}
		for _, match := range reasonCheckRE.FindAllStringSubmatch(stripSQLComments(string(raw)), -1) {
			definitions = append(definitions, checkDefinition{file: filepath.Base(file), reasons: quotedLiterals(match[1])})
		}
	}
	// La guarda que hace que este test MIRE: si alguien renombra la constraint o reformatea el
	// ALTER, el regex dejaría de casar y el test pasaría sin haber leído un solo vocabulario.
	if len(definitions) == 0 {
		t.Fatalf("ninguna migración de %s define owner_degradation_notices_reason_check: o el CHECK "+
			"desapareció o el regex dejó de casar, y este test iba a pasar sin comprobar nada", migrationsDir)
	}
	current := definitions[len(definitions)-1]

	inGo := make([]string, 0, len(wantReasons))
	for _, r := range degradation.Reasons() {
		inGo = append(inGo, string(r))
	}
	if missing := difference(current.reasons, inGo); len(missing) > 0 {
		t.Errorf("%s admite %v y Reasons() no los tiene: una fila que la base acepta daría Valid() == false", current.file, missing)
	}
	if extra := difference(inGo, current.reasons); len(extra) > 0 {
		t.Errorf("Reasons() tiene %v y %s no los admite: Record los dejaría pasar y el INSERT reventaría contra el CHECK", extra, current.file)
	}
	if unique := slices.Compact(slices.Sorted(slices.Values(current.reasons))); len(unique) != len(current.reasons) {
		t.Errorf("%s repite algún motivo en el CHECK: %v", current.file, current.reasons)
	}
	for _, earlier := range definitions[:len(definitions)-1] {
		if lost := difference(earlier.reasons, current.reasons); len(lost) > 0 {
			t.Errorf("%s admitía %v y %s ya no: un ADD CONSTRAINT que estrecha el dominio aborta el arranque "+
				"si hay una sola fila con ese motivo", earlier.file, lost, current.file)
		}
	}
}

// stripSQLComments quita las líneas de comentario SQL enteras: las migraciones llevan más prosa
// que sentencias y esa prosa CITA el DDL. Solo cae la línea que EMPIEZA por `--`.
func stripSQLComments(sql string) string {
	var kept []string
	for line := range strings.SplitSeq(sql, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// quotedLiterals extrae los valores entrecomillados de la lista de un `IN (…)`.
func quotedLiterals(list string) []string {
	var out []string
	for piece := range strings.SplitSeq(list, ",") {
		if v := strings.TrimSpace(piece); len(v) >= 2 && strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
			out = append(out, v[1:len(v)-1])
		}
	}
	return out
}

// difference devuelve los elementos de a que no están en b.
func difference(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// TestVias_MatchTenantLLM custodia la duplicación DELIBERADA del eje vía: este paquete declara
// sus dos constantes en vez de importar tenantllm desde producción, y el precio es que los dos
// vocabularios podrían divergir en silencio. Aquí no pueden.
func TestVias_MatchTenantLLM(t *testing.T) {
	cases := []struct {
		name, got, sibling, want string
	}{
		{"ViaLocal", degradation.ViaLocal, tenantllm.ViaLocal, "local"},
		{"ViaAPI", degradation.ViaAPI, tenantllm.ViaAPI, "api"},
	}
	for _, c := range cases {
		if c.got != c.want || c.got != c.sibling {
			t.Errorf("%s = %q, quería %q, el mismo literal que tenantllm.%s (%q)", c.name, c.got, c.want, c.name, c.sibling)
		}
		if !degradation.ValidVia(c.sibling) {
			t.Errorf("ValidVia(tenantllm.%s) = false: los dos ejes divergieron", c.name)
		}
	}
}

// TestValidVia: true solo para los dos literales exactos; nada de recortar, plegar mayúsculas o
// normalizar Unicode.
func TestValidVia(t *testing.T) {
	for _, valid := range []string{degradation.ViaLocal, degradation.ViaAPI} {
		if !degradation.ValidVia(valid) {
			t.Errorf("ValidVia(%q) = false, quería true", valid)
		}
	}
	invalid := []string{
		"", "API", "Local", " local", "ａｐｉ", "ap\u0131", "l\u043ecal", "local|api", "edge", "remota",
		tenantllm.ProviderAnthropic, tenantllm.ProviderGemini, string(degradation.ReasonAPIError),
	}
	for _, v := range slices.Concat(invalid, withAdversarialSuffixes("api", "local")) {
		if degradation.ValidVia(v) {
			t.Errorf("ValidVia(%q) = true, quería false: el vocabulario es cerrado y literal", v)
		}
	}
}

// TestVentanaDe: el bucket es el instante truncado, en UTC, y su fin; una función pura del
// instante (R4.5.c). El borde abre bucket nuevo —el precio aceptado de la ventana fija— y la
// zona del argumento no cambia la clave: dos procesos con TZ distinta no parten la ventana.
func TestVentanaDe(t *testing.T) {
	if degradation.VentanaPorDefecto != 15*time.Minute {
		t.Errorf("VentanaPorDefecto = %s, quería 15m", degradation.VentanaPorDefecto)
	}
	lima := time.FixedZone("UTC-5", -5*3600)
	tokyo := time.FixedZone("UTC+9", 9*3600)
	cases := []struct {
		name      string
		at        time.Time
		v         time.Duration
		wantStart time.Time
		wantSize  time.Duration
	}{
		{"first second of the bucket", base.Add(time.Second), window, base, window},
		{"last second of the bucket", base.Add(14*time.Minute + 59*time.Second), window, base, window},
		{"the exact start", base, window, base, window},
		{"the border opens the next bucket", base.Add(window), window, base.Add(window), window},
		{"same instant in another zone", base.Add(time.Second).In(lima), window, base, window},
		{"same instant in a zone ahead", base.Add(time.Second).In(tokyo), window, base, window},
		{"zero window falls to the default", base.Add(16 * time.Minute), 0, base.Add(15 * time.Minute), degradation.VentanaPorDefecto},
		{"negative window falls to the default", base.Add(16 * time.Minute), -time.Hour, base.Add(15 * time.Minute), degradation.VentanaPorDefecto},
		{"one hour window", base.Add(59 * time.Minute), time.Hour, base, time.Hour},
		{"five minute window", base.Add(7 * time.Minute), 5 * time.Minute, base.Add(5 * time.Minute), 5 * time.Minute},
		{"sub second instant", base.Add(123456789 * time.Nanosecond), window, base, window},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end := degradation.VentanaDe(c.at, c.v)
			if !start.Equal(c.wantStart) || !end.Equal(c.wantStart.Add(c.wantSize)) {
				t.Errorf("VentanaDe(%s, %s) = [%s, %s), quería [%s, %s)", c.at, c.v, start, end, c.wantStart, c.wantStart.Add(c.wantSize))
			}
			if start.Location() != time.UTC || end.Location() != time.UTC {
				t.Errorf("VentanaDe devolvió zonas %s y %s, quería UTC en las dos", start.Location(), end.Location())
			}
		})
	}
	// Con el reloj de verdad: el instante cae dentro de su ventana y el resultado no arrastra el
	// reloj monótono, que no es comparable entre procesos.
	now := time.Now()
	start, end := degradation.VentanaDe(now, window)
	if start.After(now) || !end.After(now) {
		t.Errorf("VentanaDe(ahora) = [%s, %s), que no contiene a %s", start, end, now)
	}
	if strings.Contains(start.String(), " m=") || strings.Contains(end.String(), " m=") {
		t.Errorf("la ventana arrastra el reloj monótono: [%s, %s)", start, end)
	}
}

// fieldsOf devuelve «Nombre tipo» de cada campo de un struct, en orden.
func fieldsOf(typ reflect.Type) []string {
	out := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		out = append(out, typ.Field(i).Name+" "+typ.Field(i).Type.String())
	}
	return out
}

// TestNotice_HasNoFieldForFreeText (INV-6): Notice tiene exactamente estos diez campos. Lo que
// no tiene campo no se puede filtrar por descuido: uno nuevo —y más uno donde quepa una frase,
// un teléfono o un id de sesión— pone esto en rojo. Y Leida traduce el cero de ReadAt.
func TestNotice_HasNoFieldForFreeText(t *testing.T) {
	want := []string{
		"ID string", "TenantID string", "Reason degradation.Reason", "Via string",
		"WindowStart time.Time", "WindowEnd time.Time", "Occurrences int",
		"ReadAt time.Time", "CreatedAt time.Time", "LastSeenAt time.Time",
	}
	if got := fieldsOf(reflect.TypeFor[degradation.Notice]()); !slices.Equal(got, want) {
		t.Errorf("campos de Notice = %v, quería %v", got, want)
	}
	if (degradation.Notice{}).Leida() {
		t.Error("un aviso con ReadAt cero se declaró leído")
	}
	if !(degradation.Notice{ReadAt: base}).Leida() {
		t.Error("un aviso con ReadAt puesto se declaró sin leer")
	}
}

// TestStore_Shape (INV-7): el filtro no tiene dónde poner un tenant —va como argumento de
// List— y el puerto tiene estos dos métodos, con estas firmas, y ninguno más. Cualquier Store
// sirve para montar el escritor.
func TestStore_Shape(t *testing.T) {
	wantFilter := []string{"SoloSinLeer bool", "Limit int", "Offset int"}
	if got := fieldsOf(reflect.TypeFor[degradation.ListFilter]()); !slices.Equal(got, wantFilter) {
		t.Errorf("campos de ListFilter = %v, quería %v", got, wantFilter)
	}
	want := map[string]string{
		"Save": "func(context.Context, degradation.Notice) (bool, error)",
		"List": "func(context.Context, string, degradation.ListFilter) ([]degradation.Notice, error)",
	}
	port := reflect.TypeFor[degradation.Store]()
	if port.NumMethod() != len(want) {
		t.Fatalf("Store tiene %d métodos, quería %d", port.NumMethod(), len(want))
	}
	for i := range port.NumMethod() {
		if m := port.Method(i); m.Type.String() != want[m.Name] {
			t.Errorf("Store.%s = %s, quería %q", m.Name, m.Type, want[m.Name])
		}
	}
	var store degradation.Store = degradationhelpertest.NewMemoria()
	if degradation.NewNotifier(store, window) == nil {
		t.Error("NewNotifier devolvió nil")
	}
}

// TestNotifier_ZeroWindowUsesTheDefaultAtUse (T-13): la ventana <= 0 se resuelve EN EL USO. Un
// Notifier con la ventana a cero —porque así se construyó o porque se la pusieron después—
// agrupa por los 15 minutos del default: con el cero vivo, time.Truncate(0) devolvería el
// instante intacto, o sea un aviso por fallo y REQ-38 roto sin que nada fallara. Y uno escrito
// con literal de struct, que no tiene store, devuelve un error nombrado en vez de reventar.
func TestNotifier_ZeroWindowUsesTheDefaultAtUse(t *testing.T) {
	ctx := context.Background()
	literal := &degradation.Notifier{}
	const noStore = "degradation: Notifier sin store: el aviso no se puede escribir"
	if created, err := literal.Record(ctx, testTenant, degradation.ReasonTimeout, degradation.ViaLocal, base); created || err == nil || err.Error() != noStore {
		t.Errorf("Record de un Notifier literal = (%v, %v), quería (false, %q)", created, err, noStore)
	}
	if _, err := literal.Record(ctx, "", degradation.ReasonTimeout, degradation.ViaLocal, base); !errors.Is(err, degradation.ErrTenantVacio) {
		t.Errorf("Notifier literal con tenant vacío: error = %v, quería ErrTenantVacio (las guardas del llamante van antes)", err)
	}

	setups := []struct {
		name  string
		build func(degradation.Store) *degradation.Notifier
		want  time.Duration
	}{
		{"built with zero", func(s degradation.Store) *degradation.Notifier { return degradation.NewNotifier(s, 0) }, degradation.VentanaPorDefecto},
		{"built with a negative window", func(s degradation.Store) *degradation.Notifier { return degradation.NewNotifier(s, -time.Minute) }, degradation.VentanaPorDefecto},
		{"field zeroed after building", func(s degradation.Store) *degradation.Notifier {
			n := degradation.NewNotifier(s, time.Hour)
			n.Ventana = 0
			return n
		}, degradation.VentanaPorDefecto},
		{"field changed after building", func(s degradation.Store) *degradation.Notifier {
			n := degradation.NewNotifier(s, time.Minute)
			n.Ventana = time.Hour
			return n
		}, time.Hour},
	}
	for _, c := range setups {
		t.Run(c.name, func(t *testing.T) {
			store := degradationhelpertest.NewMemoria()
			notifier := c.build(store)
			for i := range 3 { // tres fallos del mismo cuarto de hora
				if _, err := notifier.Record(ctx, testTenant, degradation.ReasonTimeout, degradation.ViaAPI, base.Add(time.Duration(i)*time.Minute)); err != nil {
					t.Fatalf("Record #%d: error inesperado %v", i, err)
				}
			}
			rows := store.Rows(testTenant)
			if len(rows) != 1 {
				t.Fatalf("quedaron %d filas, quería 1: la ventana no se resolvió en el uso", len(rows))
			}
			if size := rows[0].WindowEnd.Sub(rows[0].WindowStart); size != c.want {
				t.Errorf("la ventana escrita mide %s, quería %s", size, c.want)
			}
		})
	}
}

// TestRecordAhora: es Record con el instante del reloj —el de Ahora si está puesto, el de
// time.Now si no—, con las mismas guardas.
func TestRecordAhora(t *testing.T) {
	ctx := context.Background()
	clock := base.Add(4 * time.Minute).In(time.FixedZone("UTC+9", 9*3600))

	spy := &spyStore{created: true}
	notifier := degradation.NewNotifier(spy, window)
	notifier.Ahora = func() time.Time { return clock }
	if created, err := notifier.RecordAhora(ctx, testTenant, degradation.ReasonEdgeOffline, degradation.ViaLocal); !created || err != nil {
		t.Fatalf("RecordAhora = (%v, %v), quería (true, nil)", created, err)
	}
	requireSavedOnce(t, spy, degradation.Notice{
		TenantID: testTenant, Reason: degradation.ReasonEdgeOffline, Via: degradation.ViaLocal,
		WindowStart: base, WindowEnd: base.Add(window), LastSeenAt: clock,
	})

	spy = &spyStore{}
	before := time.Now()
	if _, err := degradation.NewNotifier(spy, window).RecordAhora(ctx, testTenant, degradation.ReasonTimeout, degradation.ViaLocal); err != nil {
		t.Fatalf("RecordAhora sin reloj inyectado: error inesperado %v", err)
	}
	after := time.Now()
	if len(spy.notices) != 1 || spy.notices[0].LastSeenAt.Before(before) || spy.notices[0].LastSeenAt.After(after) {
		t.Errorf("sin reloj inyectado llegó %+v, quería un aviso con LastSeenAt entre %s y %s", spy.notices, before, after)
	}

	store := degradationhelpertest.NewMemoria()
	guarded := degradation.NewNotifier(store, window)
	if _, err := guarded.RecordAhora(ctx, testTenant, "fastlane", degradation.ViaLocal); !errors.Is(err, degradation.ErrMotivoDesconocido) {
		t.Errorf("RecordAhora con un motivo sano: error = %v, quería ErrMotivoDesconocido", err)
	}
	if _, err := guarded.RecordAhora(ctx, testTenant, degradation.ReasonTimeout, "edge"); !errors.Is(err, degradation.ErrViaDesconocida) {
		t.Errorf("RecordAhora con una vía inventada: error = %v, quería ErrViaDesconocida", err)
	}
	if _, err := guarded.RecordAhora(ctx, "", degradation.ReasonTimeout, degradation.ViaLocal); !errors.Is(err, degradation.ErrTenantVacio) {
		t.Errorf("RecordAhora sin tenant: error = %v, quería ErrTenantVacio", err)
	}
	if n := store.Saves(); n != 0 {
		t.Errorf("los RecordAhora rechazados llamaron al store %d veces, quería 0", n)
	}
}
