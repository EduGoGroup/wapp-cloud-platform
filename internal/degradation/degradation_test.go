// degradation_test.go custodia las invariantes del paquete que no necesitan base
// para demostrarse — las DOS de T1.5-4 y la que añade la ampliación de T1.6-6:
//
//  1. EL VOCABULARIO ESTÁ CERRADO — un motivo SANO no escribe NADA. Es la guarda
//     de la tarea: el registro automático solo vale lo que valga el estado que
//     AFIRMA, y si aquí entrara «fastlane» la tabla dejaría de significar «el LLM
//     se cayó».
//  2. LA VENTANA ES UNA FUNCIÓN PURA DEL INSTANTE — que es lo que permite que el
//     dedupe lo garantice la BASE y no el código.
//  3. EL VOCABULARIO DE GO Y EL DEL `.sql` DICEN LO MISMO
//     (TestElVocabularioDeMotivosCoincideConLaMigracion). La añade T1.6-6 al
//     ampliar el enum a ocho, y se comprueba LEYENDO la migración de disco: un
//     vocabulario cerrado que vive en dos sitios que no se hablan diverge en
//     silencio, y quien lo descubre es una fila que la base acepta y Go no
//     reconoce, o un INSERT que revienta en ejecución.
//
// El «N fallos ⇒ 1 fila» de verdad —contra el índice único de Postgres— vive en
// postgres_integration_test.go. Aquí se demuestra lo que un doble en memoria SÍ
// puede demostrar: que el escritor calcula la misma clave para los N.
package degradation_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// storeFalso es un Store en memoria que reproduce EXACTAMENTE el arbitrio del
// índice único de la 0075: la clave es (tenant, motivo, vía, inicio-de-ventana).
//
// Lleva `saves` aparte del mapa a propósito: el mapa dice cuántas FILAS quedaron,
// y el contador dice cuántas veces se LLAMÓ. La diferencia entre los dos es la
// que distingue «el escritor rechazó el motivo antes de tocar el store» de «el
// store lo colapsó», y el test del motivo sano necesita afirmar lo primero.
type storeFalso struct {
	filas map[string]int
	saves int
}

func nuevoStoreFalso() *storeFalso {
	return &storeFalso{filas: map[string]int{}}
}

func (s *storeFalso) clave(n degradation.Notice) string {
	return n.TenantID + "|" + string(n.Reason) + "|" + n.Via + "|" + n.WindowStart.UTC().Format(time.RFC3339Nano)
}

func (s *storeFalso) Save(_ context.Context, n degradation.Notice) (bool, error) {
	s.saves++
	k := s.clave(n)
	s.filas[k]++
	return s.filas[k] == 1, nil
}

func (s *storeFalso) List(_ context.Context, _ string, _ degradation.ListFilter) ([]degradation.Notice, error) {
	return nil, nil
}

const tenantDePrueba = "t-degradacion"

// TestVocabularioDeMotivosEstaCerrado comprueba los OCHO —los SEIS de tasks.md:856
// más los DOS de T1.6-6— y, sobre todo, los que NO están: los motivos SANOS de
// alto volumen. Que estos den false es lo que impide que avisar el funcionamiento
// correcto mate el canal (D-044.32).
//
// Mutación: añadir a reasonsValidos (degradation.go) la línea
//
//	Reason("fastlane"),
//
// ⇒ este test se pone ROJO en el caso «fastlane».
func TestVocabularioDeMotivosEstaCerrado(t *testing.T) {
	casos := []struct {
		motivo degradation.Reason
		valido bool
	}{
		{degradation.ReasonOllamaDown, true},
		{degradation.ReasonBreakerOpen, true},
		{degradation.ReasonEdgeOffline, true},
		{degradation.ReasonTimeout, true},
		{degradation.ReasonAPIError, true},
		{degradation.ReasonCredencial, true},
		// Los DOS que añadió T1.6-6 el 2026-08-24, cerrando el hueco que la Ola
		// 1.5 dejó declarado. `lease_invalid` es uno de los cuatro errores que
		// REQ-34 obliga a nombrar en el frame; `edge_sin_capacidad` es el semáforo
		// de concurrencia del Edge, y NO es `timeout` a propósito.
		{degradation.ReasonLeaseInvalid, true},
		{degradation.ReasonEdgeSinCapacidad, true},
		// Los CUATRO motivos SANOS que REQ-38 excluye por su nombre. No son un
		// invento del test: son los que el pipeline produce cuando TODO va bien.
		// 🔴 QUE EL VOCABULARIO CREZCA NO LOS DEJA ENTRAR, y por eso siguen aquí:
		// T1.6-6 amplió el dominio con dos FALLOS DEL ADAPTADOR, no lo abrió.
		{degradation.Reason("atajo_determinista"), false},
		{degradation.Reason("fastlane"), false},
		{degradation.Reason("sin_texto"), false},
		{degradation.Reason("umbral_no_alcanzado"), false},
		// Vecinos plausibles de los dos motivos nuevos: el error del frame que NO
		// se llama así y la variante en inglés del semáforo. Los dos valores que
		// entraron son literales exactos, no aproximaciones.
		{degradation.Reason("lease_expired"), false},
		{degradation.Reason("edge_busy"), false},
		{degradation.Reason(""), false},
		{degradation.Reason("OLLAMA_DOWN"), false},
	}
	for _, c := range casos {
		if got := c.motivo.Valid(); got != c.valido {
			t.Errorf("Valid(%q) = %v, se esperaba %v", c.motivo, got, c.valido)
		}
	}
	if n := len(degradation.Reasons()); n != 8 {
		t.Errorf("el vocabulario tiene %d motivos: SEIS de tasks.md:856 + los DOS de T1.6-6 son OCHO", n)
	}
}

// dirMigraciones es el directorio de scripts que el runner embebe y aplica en
// orden de nombre (migrations/embed.go). La ruta es relativa al paquete porque
// `go test` corre con el directorio del paquete como cwd.
//
// 🔴 SE LEE DE DISCO Y NO SE IMPORTA EL embed.FS porque `structureFS` NO está
// exportado, y exportarlo para un test abriría el runner entero a cualquier
// paquete. Lo que se pierde con el fichero de disco es la garantía de estar
// leyendo los MISMOS bytes que se embeben; lo que se gana es que el test no
// obligue a tocar el runner. El riesgo real de esa pérdida es nulo: el `go:embed`
// es `structure/*.sql` sobre este mismo directorio, así que un fichero que esté
// aquí está embebido, y uno que no esté aquí no existe.
const dirMigraciones = "../platform/storage/postgres/migrations/structure"

// reasonCheckRE captura la lista del CHECK del vocabulario de motivos. Solo casa
// el `ADD CONSTRAINT … CHECK (reason IN (…))` completo: el `DROP CONSTRAINT IF
// EXISTS` de al lado y las menciones al nombre dentro de la prosa no casan.
var reasonCheckRE = regexp.MustCompile(
	`(?s)ADD\s+CONSTRAINT\s+owner_degradation_notices_reason_check\s+CHECK\s*\(\s*reason\s+IN\s*\(([^)]*)\)`)

// TestElVocabularioDeMotivosCoincideConLaMigracion es EL test de la ampliación de
// T1.6-6, y no prueba un motivo: prueba LA REGLA.
//
// El vocabulario cerrado vive en DOS sitios que no se hablan —el `const` de
// degradation.go y el CHECK de un `.sql`— y la única forma de que no divergan en
// silencio es que algo los compare. Escribir N tests de conducta (uno por motivo)
// dejaría el hueco justo donde importa: el motivo N+1, el que alguien añada
// mañana en un solo lado, no tendría test porque el test nace con el motivo.
// Comparar las DOS LISTAS enteras no tiene esa fuga.
//
// LEE EL `.sql` DE DISCO Y NO COPIA LA LISTA. Un test que llevara los ocho
// literales escritos a mano sería tautológico: pasaría con cualquier CHECK,
// porque nunca miró el CHECK.
//
// 🔴 EL VOCABULARIO VIGENTE ES EL DEL ÚLTIMO FICHERO QUE LO REDEFINE. El runner
// hace FULL-REPLAY en orden de nombre y cada migración que toca
// este CHECK hace `DROP` + `ADD`, así que quien habla es la última. Hoy solo lo
// define la 0075 y por eso la última es ella — pero el test BARRE el directorio en
// vez de abrir un fichero fijo, porque clavar «0075_*.sql» aquí dejaría pasar
// desapercibida una redefinición posterior, que es exactamente el fallo que este
// test existe para impedir.
//
// ⚠️ Y esa redefinición posterior sería un DEFECTO, no una variante: bajo
// full-replay la 0075 corre ANTES y su ADD CONSTRAINT valida las filas, así que
// ensanchar el vocabulario desde una migración nueva aborta el arranque en cuanto
// exista UNA fila con el motivo nuevo. La comprobación de estrechamiento del final
// de este test es lo que la caza.
func TestElVocabularioDeMotivosCoincideConLaMigracion(t *testing.T) {
	ficheros, err := filepath.Glob(filepath.Join(dirMigraciones, "*.sql"))
	if err != nil {
		t.Fatalf("listando migraciones en %s: %v", dirMigraciones, err)
	}
	if len(ficheros) == 0 {
		t.Fatalf("cero migraciones en %s: la ruta del test se quedó atrás", dirMigraciones)
	}
	sort.Strings(ficheros) // el runner las aplica en orden de nombre; el test también

	// definiciones lleva TODAS las redefiniciones del CHECK, en orden de
	// aplicación. La última es la vigente; las anteriores son historia, y sirven
	// para la comprobación de estrechamiento de más abajo.
	type definicion struct {
		fichero  string
		motivos  []string
		posicion int
	}
	var definiciones []definicion
	for _, f := range ficheros {
		crudo, rerr := os.ReadFile(f) //nolint:gosec // ruta construida por Glob sobre un dir constante
		if rerr != nil {
			t.Fatalf("leyendo %s: %v", f, rerr)
		}
		sql := sinComentarios(string(crudo))
		for i, m := range reasonCheckRE.FindAllStringSubmatch(sql, -1) {
			definiciones = append(definiciones, definicion{
				fichero:  filepath.Base(f),
				motivos:  literales(m[1]),
				posicion: i,
			})
		}
	}

	// 🔴 LA GUARDA QUE HACE QUE ESTE TEST MIRE. Sin ella, el día que alguien
	// renombre la constraint o reformatee el ALTER, el regex dejaría de casar, no
	// habría nada que comparar y el test saldría VERDE sin haber leído un solo
	// vocabulario — el peor resultado posible, porque además nadie se enteraría.
	if len(definiciones) == 0 {
		t.Fatalf("ninguna migración de %s define owner_degradation_notices_reason_check: "+
			"o el CHECK desapareció o el regex dejó de casar; en los dos casos este test estaba a punto "+
			"de pasar sin comprobar nada", dirMigraciones)
	}

	vigente := definiciones[len(definiciones)-1]
	t.Logf("el vocabulario vigente lo fija %s (%d redefiniciones en total): %v",
		vigente.fichero, len(definiciones), vigente.motivos)

	enGo := make([]string, 0, len(degradation.Reasons()))
	for _, r := range degradation.Reasons() {
		enGo = append(enGo, string(r))
	}

	// La comparación es de CONJUNTOS: en un `IN (…)` el orden no significa nada, y
	// hacerlo significar algo convertiría un reordenado inocente en un rojo.
	if faltan := diferencia(vigente.motivos, enGo); len(faltan) > 0 {
		t.Errorf("%s admite %v y el enum de Go NO los tiene: una fila que la base acepta "+
			"llegaría a Reason.Valid() == false", vigente.fichero, faltan)
	}
	if sobran := diferencia(enGo, vigente.motivos); len(sobran) > 0 {
		t.Errorf("el enum de Go tiene %v y %s NO los admite: Notifier.Record los dejaría "+
			"pasar y el INSERT reventaría contra el CHECK en ejecución", sobran, vigente.fichero)
	}
	if len(vigente.motivos) != len(unicos(vigente.motivos)) {
		t.Errorf("%s repite algún motivo en el CHECK: %v", vigente.fichero, vigente.motivos)
	}

	// EL DOMINIO SOLO PUEDE CRECER, y esto no es purismo. El `ADD CONSTRAINT`
	// VALIDA las filas existentes: estrechar el vocabulario sobre una tabla con
	// avisos de un motivo retirado hace que el ALTER falle y el SERVICIO NO
	// ARRANQUE. Quien necesite quitar un motivo tiene que borrar sus filas en la
	// misma migración, y entonces vendrá aquí a decirlo a propósito.
	for _, d := range definiciones[:len(definiciones)-1] {
		if perdidos := diferencia(d.motivos, vigente.motivos); len(perdidos) > 0 {
			t.Errorf("%s admitía %v y %s ya no: un ADD CONSTRAINT que estrecha el dominio "+
				"aborta el arranque si hay una sola fila con ese motivo",
				d.fichero, perdidos, vigente.fichero)
		}
	}
}

// sinComentarios quita las líneas de comentario SQL enteras. Las migraciones de
// este repo llevan más prosa que sentencias y esa prosa CITA el DDL, así que sin
// este filtro un bloque explicativo que reprodujera el ALTER contaría como una
// definición más.
//
// Solo cae la línea que EMPIEZA por `--`: los `--` que viven dentro de un literal
// de cadena (los COMMENT ON de este repo los usan como separador de prosa) están
// siempre a media línea y se conservan.
func sinComentarios(sql string) string {
	lineas := strings.Split(sql, "\n")
	limpias := make([]string, 0, len(lineas))
	for _, l := range lineas {
		if strings.HasPrefix(strings.TrimSpace(l), "--") {
			continue
		}
		limpias = append(limpias, l)
	}
	return strings.Join(limpias, "\n")
}

// literales extrae los valores entrecomillados de la lista de un `IN (…)`.
func literales(lista string) []string {
	var out []string
	for _, trozo := range strings.Split(lista, ",") {
		v := strings.TrimSpace(trozo)
		v = strings.Trim(v, "\n\t ")
		if len(v) >= 2 && strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
			out = append(out, v[1:len(v)-1])
		}
	}
	return out
}

// diferencia devuelve los elementos de a que no están en b.
func diferencia(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func unicos(a []string) []string {
	vistos := map[string]struct{}{}
	var out []string
	for _, x := range a {
		if _, ok := vistos[x]; ok {
			continue
		}
		vistos[x] = struct{}{}
		out = append(out, x)
	}
	return out
}

// TestReasonsDevuelveCopia comprueba que el vocabulario no se puede abrir desde
// fuera. Sin la copia, degradation.Reasons()[0] = "fastlane" reescribiría el
// slice del paquete y el vocabulario cerrado dejaría de serlo para todo el
// proceso.
//
// Mutación: en degradation.go, cambiar el cuerpo de Reasons por
//
//	func Reasons() []Reason { return reasonsValidos }
//
// ⇒ este test se pone ROJO.
func TestReasonsDevuelveCopia(t *testing.T) {
	copia := degradation.Reasons()
	copia[0] = degradation.Reason("fastlane")
	if !degradation.ReasonOllamaDown.Valid() {
		t.Fatal("mutar la copia de Reasons() abrió el vocabulario del paquete")
	}
	if degradation.Reason("fastlane").Valid() {
		t.Fatal("mutar la copia de Reasons() metió un motivo sano en el vocabulario")
	}
}

// TestElVocabularioDeViaCoincideConTenantLLM custodia la duplicación DELIBERADA
// del eje vía: este paquete declara sus dos constantes en vez de importar
// internal/tenantllm (ver el comentario de ViaLocal), y el precio de esa decisión
// es que los dos vocabularios pueden divergir en silencio. Este test es lo que
// hace que no puedan.
//
// Mutación: en degradation.go, cambiar ViaLocal = "local" por
//
//	ViaLocal = "edge"
//
// ⇒ este test se pone ROJO (y con él, el dedupe contra el CHECK de la 0075).
func TestElVocabularioDeViaCoincideConTenantLLM(t *testing.T) {
	if degradation.ViaLocal != tenantllm.ViaLocal {
		t.Errorf("ViaLocal = %q y tenantllm.ViaLocal = %q: los dos ejes divergieron",
			degradation.ViaLocal, tenantllm.ViaLocal)
	}
	if degradation.ViaAPI != tenantllm.ViaAPI {
		t.Errorf("ViaAPI = %q y tenantllm.ViaAPI = %q: los dos ejes divergieron",
			degradation.ViaAPI, tenantllm.ViaAPI)
	}
	if !degradation.ValidVia(degradation.ViaLocal) || !degradation.ValidVia(degradation.ViaAPI) {
		t.Error("ValidVia no reconoce sus propias constantes")
	}
	if degradation.ValidVia("") || degradation.ValidVia("anthropic") {
		t.Error("ValidVia admite un valor fuera del vocabulario cerrado")
	}
}

// TestVentanaDeEsFuncionPuraDelInstante es la demostración de por qué el dedupe
// puede vivir en la BASE: dos réplicas que vean el mismo instante calculan la
// misma clave sin hablar entre ellas.
//
// Comprueba las tres cosas que lo hacen cierto: (a) instantes distintos DENTRO
// del bucket dan el MISMO inicio; (b) el borde abre bucket nuevo —el precio
// aceptado de la ventana fija—; (c) la zona horaria del argumento NO cambia la
// clave.
//
// Mutación: en VentanaDe (degradation.go), quitar el truncado
//
//	inicio = at.UTC()
//
// ⇒ este test se pone ROJO en el primer caso (dos fallos del mismo bucket dejarían
// de compartir clave, y REQ-38 se rompería por el camino largo).
func TestVentanaDeEsFuncionPuraDelInstante(t *testing.T) {
	v := 15 * time.Minute
	base := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	inicio1, fin1 := degradation.VentanaDe(base.Add(time.Second), v)
	inicio2, fin2 := degradation.VentanaDe(base.Add(14*time.Minute+59*time.Second), v)
	if !inicio1.Equal(inicio2) {
		t.Errorf("dos fallos del mismo bucket dieron inicios distintos: %s vs %s", inicio1, inicio2)
	}
	if !fin1.Equal(fin2) {
		t.Errorf("dos fallos del mismo bucket dieron fines distintos: %s vs %s", fin1, fin2)
	}
	if !inicio1.Equal(base) {
		t.Errorf("inicio = %s, se esperaba el truncado %s", inicio1, base)
	}
	if !fin1.Equal(base.Add(v)) {
		t.Errorf("fin = %s, se esperaba %s", fin1, base.Add(v))
	}

	// (b) EL PRECIO ACEPTADO: un segundo más tarde, otro bucket. Está escrito como
	// aserción y no como comentario para que nadie lo «arregle» pensando que es un
	// defecto: es la contrapartida de que la base pueda garantizar el dedupe.
	inicioBorde, _ := degradation.VentanaDe(base.Add(v), v)
	if inicioBorde.Equal(inicio1) {
		t.Error("el borde de la ventana no abrió bucket nuevo: la ventana no es fija")
	}

	// (c) La zona del argumento no puede cambiar la clave: dos procesos con TZ
	// distinta partirían la ventana en dos.
	otraZona := time.FixedZone("UTC-5", -5*3600)
	inicioZona, _ := degradation.VentanaDe(base.Add(time.Second).In(otraZona), v)
	if !inicioZona.Equal(inicio1) {
		t.Errorf("la zona horaria cambió la clave: %s vs %s", inicioZona, inicio1)
	}

	// (d) Ventana <= 0 cae al default en vez de truncar con duración cero (que
	// devolvería el instante intacto: un aviso por fallo).
	inicioCero, finCero := degradation.VentanaDe(base.Add(time.Second), 0)
	if finCero.Sub(inicioCero) != degradation.VentanaPorDefecto {
		t.Errorf("ventana 0 dio %s, se esperaba el default %s",
			finCero.Sub(inicioCero), degradation.VentanaPorDefecto)
	}
}

// TestRecordRechazaMotivoSanoSinTocarElStore es EL test de la tarea.
//
// No comprueba «la base lo rechazó»: comprueba que el store NO SE LLEGÓ A LLAMAR.
// Es una afirmación más fuerte, y es la que corresponde — la guarda tiene que
// estar en el escritor, porque el escritor es lo único que existe entre un
// productor equivocado y la tabla. El CHECK de la 0075 es la red de debajo, no la
// primera.
//
// Mutación: en Record (degradation.go), cambiar
//
//	if !reason.Valid() {
//
// por
//
//	if false {
//
// ⇒ este test se pone ROJO: saves pasa de 0 a 1 en los cuatro motivos sanos.
func TestRecordRechazaMotivoSanoSinTocarElStore(t *testing.T) {
	sanos := []degradation.Reason{
		"atajo_determinista", "fastlane", "sin_texto", "umbral_no_alcanzado",
		// Y uno inventado, que es el otro caso del mismo «no».
		"me_lo_acabo_de_inventar",
	}
	for _, motivo := range sanos {
		store := nuevoStoreFalso()
		n := degradation.NewNotifier(store, 15*time.Minute)
		creado, err := n.Record(context.Background(), tenantDePrueba, motivo, degradation.ViaLocal, time.Now())
		if !errors.Is(err, degradation.ErrMotivoDesconocido) {
			t.Errorf("Record(%q) devolvió %v, se esperaba ErrMotivoDesconocido", motivo, err)
		}
		if creado {
			t.Errorf("Record(%q) dijo haber creado un aviso", motivo)
		}
		if store.saves != 0 {
			t.Errorf("Record(%q) llamó al store %d veces: la guarda tiene que estar ANTES", motivo, store.saves)
		}
		if len(store.filas) != 0 {
			t.Errorf("Record(%q) dejó %d filas, se esperaban CERO", motivo, len(store.filas))
		}
	}
}

// TestRecordRechazaViaYTenant cierra los otros dos huecos por los que podría
// entrar una fila que nadie sabe leer: una vía inventada (que reventaría contra
// el CHECK de la 0075 y convertiría un defecto del llamante en un 500) y un aviso
// sin dueño.
//
// Mutación: en Record, cambiar
//
//	if !ValidVia(via) {
//
// por
//
//	if via == "imposible" {
//
// ⇒ este test se pone ROJO en el caso de la vía.
func TestRecordRechazaViaYTenant(t *testing.T) {
	store := nuevoStoreFalso()
	n := degradation.NewNotifier(store, 15*time.Minute)
	ctx := context.Background()

	_, err := n.Record(ctx, tenantDePrueba, degradation.ReasonTimeout, "edge", time.Now())
	if !errors.Is(err, degradation.ErrViaDesconocida) {
		t.Errorf("vía inventada devolvió %v, se esperaba ErrViaDesconocida", err)
	}
	_, err = n.Record(ctx, "", degradation.ReasonTimeout, degradation.ViaLocal, time.Now())
	if !errors.Is(err, degradation.ErrTenantVacio) {
		t.Errorf("tenant vacío devolvió %v, se esperaba ErrTenantVacio", err)
	}
	if store.saves != 0 {
		t.Errorf("el store se llamó %d veces con argumentos inválidos", store.saves)
	}
}

// TestRecordColapsaLaVentana es la mitad en memoria del criterio «N fallos en la
// ventana ⇒ 1 fila»: demuestra que el ESCRITOR le da la misma clave a los N. La
// otra mitad —que la BASE la respeta con dos réplicas— solo la puede demostrar
// Postgres, y está en postgres_integration_test.go.
//
// Se registran DIEZ fallos repartidos por toda la ventana y UNO en la siguiente.
// Resultado: 2 filas, la primera con 10 ocurrencias.
//
// Mutación: en Record, cambiar
//
//	inicio, fin := VentanaDe(at, n.ventana())
//
// por
//
//	inicio, fin := at, at.Add(n.ventana())
//
// ⇒ este test se pone ROJO: aparecerían 11 filas (una por fallo), que es
// exactamente el «un aviso por mensaje» que REQ-38 prohíbe.
func TestRecordColapsaLaVentana(t *testing.T) {
	store := nuevoStoreFalso()
	n := degradation.NewNotifier(store, 15*time.Minute)
	ctx := context.Background()
	base := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	nacidos := 0
	for i := range 10 {
		// 0, 89, 178 … segundos: los diez caen dentro de los 15 minutos.
		at := base.Add(time.Duration(i) * 89 * time.Second)
		creado, err := n.Record(ctx, tenantDePrueba, degradation.ReasonOllamaDown, degradation.ViaLocal, at)
		if err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
		if creado {
			nacidos++
		}
	}
	if nacidos != 1 {
		t.Errorf("nacieron %d avisos con 10 fallos sostenidos, se esperaba 1", nacidos)
	}
	if len(store.filas) != 1 {
		t.Errorf("quedaron %d filas, se esperaba 1 (REQ-38: una degradación sostenida = UN aviso)", len(store.filas))
	}
	if store.saves != 10 {
		t.Errorf("el store recibió %d escrituras, se esperaban 10 (el colapso lo hace la clave, no el escritor)", store.saves)
	}

	// La ventana SIGUIENTE sí abre aviso nuevo: el aviso es por ventana, no
	// eterno. Sin esto, un tenant con el Ollama caído toda la semana recibiría un
	// solo aviso el lunes.
	creado, err := n.Record(ctx, tenantDePrueba, degradation.ReasonOllamaDown, degradation.ViaLocal, base.Add(20*time.Minute))
	if err != nil {
		t.Fatalf("Record de la ventana siguiente: %v", err)
	}
	if !creado {
		t.Error("la ventana siguiente no abrió aviso nuevo")
	}
	if len(store.filas) != 2 {
		t.Errorf("quedaron %d filas, se esperaban 2 (dos ventanas)", len(store.filas))
	}
}

// TestRecordSeparaPorMotivoViaYTenant comprueba que la clave del dedupe son las
// CUATRO columnas y no solo la ventana: dos fallos distintos en el mismo minuto
// son dos avisos, porque el dueño necesita saber que se le cayeron dos cosas.
//
// Mutación: en storeFalso.clave (este fichero), quitar el motivo de la clave
//
//	return n.TenantID + "|" + n.Via + "|" + n.WindowStart.UTC().Format(time.RFC3339Nano)
//
// ⇒ este test se pone ROJO. (La mutación equivalente en producción es quitar
// `reason` del índice único de la 0075 y del ON CONFLICT de saveSQL, y el test de
// integración es quien la atrapa allí.)
func TestRecordSeparaPorMotivoViaYTenant(t *testing.T) {
	store := nuevoStoreFalso()
	n := degradation.NewNotifier(store, 15*time.Minute)
	ctx := context.Background()
	at := time.Date(2026, 8, 23, 10, 3, 0, 0, time.UTC)

	casos := []struct {
		tenant string
		motivo degradation.Reason
		via    string
	}{
		{tenantDePrueba, degradation.ReasonOllamaDown, degradation.ViaLocal},
		{tenantDePrueba, degradation.ReasonBreakerOpen, degradation.ViaLocal}, // otro motivo
		{tenantDePrueba, degradation.ReasonTimeout, degradation.ViaLocal},
		{tenantDePrueba, degradation.ReasonTimeout, degradation.ViaAPI}, // misma razón, otra vía
		{"otro-tenant", degradation.ReasonOllamaDown, degradation.ViaLocal},
	}
	for _, c := range casos {
		creado, err := n.Record(ctx, c.tenant, c.motivo, c.via, at)
		if err != nil {
			t.Fatalf("Record(%s/%s/%s): %v", c.tenant, c.motivo, c.via, err)
		}
		if !creado {
			t.Errorf("Record(%s/%s/%s) se colapsó sobre otro aviso: la clave está incompleta",
				c.tenant, c.motivo, c.via)
		}
	}
	if len(store.filas) != len(casos) {
		t.Errorf("quedaron %d filas, se esperaban %d", len(store.filas), len(casos))
	}
}

// TestNotifierSinVentanaUsaElDefault custodia que un Notifier con ventana 0 AGRUPE
// IGUAL: tres fallos del mismo cuarto de hora dejan UNA fila, no tres. Si el cero
// llegara vivo hasta el truncado, time.Truncate(0) devuelve el instante intacto y
// el resultado sería un aviso por fallo —REQ-38 roto, y NADA fallando.
//
// Mutación: en degradation.go, quitar de VentanaDe la guarda
//
//	if v <= 0 {
//		v = VentanaPorDefecto
//	}
//
// ⇒ este test se pone ROJO (aparecerían 3 filas en vez de 1).
//
// 🔧 LA MUTACIÓN ANTERIOR ERA INERTE Y SE CORRIGE DICIENDO POR QUÉ (code review
// 2026-08-23). Decía «cambiar el cuerpo de ventana() por `return n.Ventana`», y
// con eso el test seguía VERDE: el cero llega a VentanaDe, que tiene SU PROPIA
// guarda y lo vuelve a cambiar por el default. El default está en DOS sitios a
// propósito —cinturón y tirantes— y la consecuencia hay que aceptarla entera:
// ninguna mutación de `ventana()` sola es observable desde fuera. Tampoco se
// puede llamar: este fichero es `package degradation_test` y el método es
// privado, y un Notifier con literal de struct no puede escribir porque `store`
// no se exporta. Así que lo que este test custodia de verdad es el
// COMPORTAMIENTO —ventana 0 ⇒ se agrupa por el default— por el camino real
// Record → ventana() → VentanaDe, y la red que lo sostiene hoy es la de VentanaDe.
// La otra mitad, la que ejercita VentanaDe(at, 0) directamente, está en el caso
// (d) de TestVentanaDeEsFuncionPuraDelInstante.
func TestNotifierSinVentanaUsaElDefault(t *testing.T) {
	store := nuevoStoreFalso()
	n := &degradation.Notifier{} // sin ventana, sin reloj
	// El campo store no se exporta, así que el literal de arriba no sirve para
	// escribir: se comprueba que devuelve error nombrado en vez de reventar, y el
	// caso del default se ejercita con el constructor y ventana 0.
	if _, err := n.Record(context.Background(), tenantDePrueba,
		degradation.ReasonTimeout, degradation.ViaLocal, time.Now()); err == nil {
		t.Error("un Notifier sin store aceptó escribir: el aviso se perdería en silencio")
	}

	conDefault := degradation.NewNotifier(store, 0)
	base := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for i := range 3 {
		if _, err := conDefault.Record(context.Background(), tenantDePrueba,
			degradation.ReasonTimeout, degradation.ViaAPI, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
	}
	if len(store.filas) != 1 {
		t.Errorf("con ventana 0 quedaron %d filas: el default no se aplicó y se truncó con duración cero",
			len(store.filas))
	}
}

// TestNoticeLeidaTraduceElCero deja escrito en un test lo que la tabla dice en un
// comentario: el cero de ReadAt significa «sin leer», y esa traducción vive en un
// solo sitio para que la capa HTTP no la reinvente.
//
// Mutación: en degradation.go, cambiar el cuerpo de Leida por
//
//	func (n Notice) Leida() bool { return n.ReadAt.IsZero() }
//
// ⇒ este test se pone ROJO.
func TestNoticeLeidaTraduceElCero(t *testing.T) {
	if (degradation.Notice{}).Leida() {
		t.Error("un aviso con ReadAt cero se declaró leído")
	}
	if !(degradation.Notice{ReadAt: time.Now()}).Leida() {
		t.Error("un aviso con ReadAt puesto se declaró sin leer")
	}
}
