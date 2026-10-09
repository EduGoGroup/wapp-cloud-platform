package arranque

// solicitudes_cableado_test.go — EL TEST DE CABLEADO DE solicitudes, COMPLETO (F6 · T6.24,
// R6.6.a–c; arquitectura.md §6 de F6; 05 §4.2, hallazgo 39 de F1). No basta mirar el tipo de un
// campo del contenedor: se afirma, sobre el arranque nuevo REAL (las fases 2–8 del contenedor de
// la huella, sin red ni BD), que el Service, el notificador, los recordatorios, el gate, los
// almacenes y el generador de cotización son los de internal/modulos/solicitudes; que de cada uno
// hay UNA construcción, la nueva; que, por ruta de import, ningún fichero de internal/arranque
// —de producción o de test— toca los paquetes viejos de solicitudes fuera del sitio declarado
// para la segunda instancia vieja del almacén (D-F6-1); y que esa instancia la lee el único
// consumidor viejo que la necesita desde F7 (el carrito) y nadie más.
//
// La mitad de IDENTIDAD (mismas instancias en el contenedor, en la cara nueva y en la vieja, el
// centinela de H1 y el plazo de G7) vive en solicitudes_cableado_identidad_test.go; se parten por
// tamaño (E-13).
//
// `solicitudes` NO entra en Conmutados (internal/modulos/fronteras_test.go) al conmutar: entra
// en F8, cuando muera lo transitorio del carrito (reglas.md §4, punto 10).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
)

// Rutas de import de solicitudes: los tres árboles VIEJOS (cada una es además prefijo de sus
// subpaquetes: la de intakes cubre quotetext y telemetria, la de integrations cubre crmpush y
// sigv1) y los seis paquetes NUEVOS que el arranque construye. Las viejas se componen con
// internalTreePath en vez de escribirse enteras para que el grep de la definición de hecho de F6
// (reglas.md §4, punto 7), que busca esas rutas como texto, solo encuentre el sitio declarado.
//
// ⚠️ La cola de captación (el paquete de F7 cuyo nombre es el de intakes sin la «s» final,
// reglas.md T-2) y el adelanto de ventana NO son solicitudes: isOldRequestsPath compara la ruta
// entera o el prefijo con «/», así que no los confunde.
const (
	oldIntakesImportPath      = internalTreePath + "intakes"
	oldIntegrationsImportPath = internalTreePath + "integrations"
	oldTenantVarsImportPath   = internalTreePath + "tenantvars"

	newIntakesImportPath      = internalTreePath + "modulos/solicitudes/intakes"
	newQuoteTextImportPath    = newIntakesImportPath + "/quotetext"
	newTelemetryImportPath    = newIntakesImportPath + "/telemetria"
	newIntegrationsImportPath = internalTreePath + "modulos/solicitudes/integrations"
	newCRMPushImportPath      = newIntegrationsImportPath + "/crmpush"
	newTenantVarsImportPath   = internalTreePath + "modulos/solicitudes/tenantvars"
)

// oldIntakesAlias es el nombre con el que el arranque nuevo importa el intakes viejo. Nunca el
// nombre corto (lo lleva el paquete NUEVO: los candados de cableado buscan ese texto, reglas.md
// T-6) ni una abreviatura (T-2).
const oldIntakesAlias = "intakesviejo"

// oldRequestsImporters es la lista blanca, por nombre de fichero, de los ficheros de
// internal/arranque que pueden importar un paquete viejo de solicitudes, con las ÚNICAS rutas que
// cada uno puede importar. Es el sitio declarado de la segunda instancia vieja del almacén
// (D-F6-1): el TIPO del campo contenedor.intakeStoreViejo y su única construcción, en la fase 3.
// Los dos salen de aquí cuando muera esa instancia, en F8 (el carrito); sus tres lectores de
// captación ya la dejaron en F7.
var oldRequestsImporters = map[string][]string{
	"contenedor.go":      {oldIntakesImportPath},
	"fase3_almacenes.go": {oldIntakesImportPath},
}

// isOldRequestsPath dice si path es un paquete viejo de solicitudes (o un subpaquete suyo).
func isOldRequestsPath(path string) bool {
	for _, old := range []string{oldIntakesImportPath, oldIntegrationsImportPath, oldTenantVarsImportPath} {
		if path == old || strings.HasPrefix(path, old+"/") {
			return true
		}
	}
	return false
}

// TestCableado_OnlyTheDeclaredSiteImportsTheOldRequests (R6.6.a, definición de hecho 7 de F6): se
// barren por ruta de import TODOS los .go de internal/arranque, producción Y tests, y los paquetes
// viejos de solicitudes solo aparecen en la lista blanca, con la ruta exacta que cada fichero
// tiene declarada y con el alias `intakesviejo`. Sin esto, una fase podría construir un Service,
// un notificador o un worker viejos y pasárselos a su consumidor sin tocar ningún campo del
// contenedor; y un test que importara lo viejo para comparar salidas sería un puente a escondidas.
// Falla también si una excepción ya no se usa: el día que muera la instancia vieja, sobra.
func TestCableado_OnlyTheDeclaredSiteImportsTheOldRequests(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("leyendo el directorio del arranque: %v", err)
	}
	fset := token.NewFileSet()
	scanned := 0
	used := make(map[string]bool)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parseando %s: %v", name, err)
		}
		scanned++
		for path, local := range importsOf(t, f) {
			if !isOldRequestsPath(path) {
				continue
			}
			if !slices.Contains(oldRequestsImporters[name], path) {
				t.Errorf("%s importa %s: en el arranque nuevo solo el sitio declarado de la segunda instancia "+
					"vieja del almacén (%v) puede importar un paquete viejo de solicitudes", name, path, oldRequestsImporters)
				continue
			}
			used[name+" "+path] = true
			if local != oldIntakesAlias {
				t.Errorf("%s importa %s como %q; se espera el alias %q (el nombre corto es del paquete NUEVO)",
					name, path, local, oldIntakesAlias)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("recorridos = 0: el barrido no miró ningún fichero")
	}
	for name, paths := range oldRequestsImporters {
		for _, path := range paths {
			if !used[name+" "+path] {
				t.Errorf("%s está en la lista blanca oldRequestsImporters pero ya no importa %s (o el fichero ya "+
					"no existe): si la instancia vieja murió, la excepción sobra", name, path)
			}
		}
	}
}

// TestCableado_TheBootBuildsTheNewRequests (R6.6.a, R6.6.b): el arranque real construye el
// Service, el notificador, los dos recordatorios, el gate del puente CRM, los tres almacenes, el
// de variables y el generador de cotización, y todos son los del módulo NUEVO. La única pieza
// vieja es la segunda instancia del almacén, y se afirma que LO ES: si un día pasara a ser nueva
// sin que murieran sus consumidores, este test y el comentario del campo se habrían quedado atrás.
func TestCableado_TheBootBuildsTheNewRequests(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	built := []struct {
		name    string
		got     any
		want    reflect.Type
		pkgPath string
	}{
		{"c.intakeService", c.intakeService, reflect.TypeFor[*intakes.Service](), newIntakesImportPath},
		{"c.intakeNotifier", c.intakeNotifier, reflect.TypeFor[*intakes.Notifier](), newIntakesImportPath},
		{"c.depositReminder (la seña)", c.depositReminder, reflect.TypeFor[*intakes.DepositReminder](), newIntakesImportPath},
		{"c.expiryReminder (el plazo)", c.expiryReminder, reflect.TypeFor[*intakes.ExpiryReminder](), newIntakesImportPath},
		{"c.webhookGate", c.webhookGate, reflect.TypeFor[*integrations.EntitlementsGate](), newIntegrationsImportPath},
		{"c.intakeStore", c.intakeStore, reflect.TypeFor[*intakes.Postgres](), newIntakesImportPath},
		{"c.buyerDataStore", c.buyerDataStore, reflect.TypeFor[*intakes.PostgresBuyerData](), newIntakesImportPath},
		{"c.integrationsStore", c.integrationsStore, reflect.TypeFor[*integrations.Postgres](), newIntegrationsImportPath},
		{"c.tenantVars", c.tenantVars, reflect.TypeFor[*tenantvars.Postgres](), newTenantVarsImportPath},
		{"c.quoteSvc", c.quoteSvc, reflect.TypeFor[*quotetext.Service](), newQuoteTextImportPath},
		{"c.intakeStoreViejo (D-F6-1)", c.intakeStoreViejo, nil, oldIntakesImportPath},
	}
	for _, b := range built {
		v := reflect.ValueOf(b.got)
		if v.Kind() != reflect.Pointer || v.IsNil() {
			t.Errorf("%s no está construido tras las fases 3, 5 y 6", b.name)
			continue
		}
		if b.want != nil && v.Type() != b.want {
			t.Errorf("%s es un %s; se espera %s", b.name, v.Type(), b.want)
		}
		if got := v.Type().Elem().PkgPath(); got != b.pkgPath {
			t.Errorf("%s es de %s; se espera %s", b.name, got, b.pkgPath)
		}
	}
}

// TestCableado_OneNewConstructionOfEachRequestsPiece (R6.6.b, reglas.md §3): en toda la producción
// del arranque hay exactamente UNA llamada a cada constructor nuevo de solicitudes y NINGUNA a su
// gemelo viejo, con una sola excepción declarada: el almacén viejo se construye exactamente UNA
// vez (la segunda instancia, D-F6-1). Dos Service serían dos máquinas de estados; dos
// notificadores o recordatorios, dos criterios de «ya avisé»; dos workers, lotes reclamados a la
// vez; y dos almacenes de variables es lo que había hasta F6 (uno por fase 8 y otro por fase 9).
func TestCableado_OneNewConstructionOfEachRequestsPiece(t *testing.T) {
	constructors := []struct {
		newPath, oldPath, fn string
		oldFn                string // nombre del gemelo viejo si cambió (E-11); "" ⇒ el mismo
		oldCalls             int
	}{
		{newIntakesImportPath, oldIntakesImportPath, "NewPostgres", "", 1},
		{newIntakesImportPath, oldIntakesImportPath, "NewPostgresBuyerData", "", 0},
		{newIntakesImportPath, oldIntakesImportPath, "NewNotifier", "", 0},
		{newIntakesImportPath, oldIntakesImportPath, "NewDepositReminder", "", 0},
		{newIntakesImportPath, oldIntakesImportPath, "NewExpiryReminder", "", 0},
		{newIntakesImportPath, oldIntakesImportPath, "NewLogOwnerNotice", "", 0},
		{newIntakesImportPath, oldIntakesImportPath, "NewService", "", 0},
		{newQuoteTextImportPath, oldIntakesImportPath + "/quotetext", "NewService", "NewServicio", 0},
		{newTelemetryImportPath, oldIntakesImportPath + "/telemetria", "New", "", 0},
		{newIntegrationsImportPath, oldIntegrationsImportPath, "NewPostgres", "", 0},
		{newIntegrationsImportPath, oldIntegrationsImportPath, "NewEntitlementsGate", "", 0},
		{newIntegrationsImportPath, oldIntegrationsImportPath, "NewWorker", "", 0},
		{newCRMPushImportPath, oldIntegrationsImportPath + "/crmpush", "NewPusher", "", 0},
		{newCRMPushImportPath, oldIntegrationsImportPath + "/crmpush", "NewRevisionPusher", "", 0},
		{newTenantVarsImportPath, oldTenantVarsImportPath, "NewPostgres", "", 0},
	}
	for _, k := range constructors {
		if n := callsTo(t, k.newPath, k.fn); n != 1 {
			t.Errorf("%s.%s aparece %d veces en la producción de internal/arranque; se espera 1", k.newPath, k.fn, n)
		}
		oldFn := k.fn
		if k.oldFn != "" {
			oldFn = k.oldFn
		}
		if n := callsTo(t, k.oldPath, oldFn); n != k.oldCalls {
			t.Errorf("%s.%s (VIEJO) aparece %d veces en la producción de internal/arranque; se espera %d",
				k.oldPath, oldFn, n, k.oldCalls)
		}
	}
}

// TestCableado_TheOutboxWorkerIsTheNewOneAndRunsOnce (R6.6.b): el worker del puente CRM es una
// variable local de la fase 9, que el contenedor de la huella no ejecuta (sus Run tocarían la BD),
// así que se afirma por AST: lo construye el integrations.NewWorker NUEVO con los tres almacenes
// y el almacén de variables del contenedor, y en ese fichero hay exactamente UN `go <worker>.Run`.
// Sin el Run el outbox se llena sin que nadie entregue; con dos, dos goroutines reclaman lotes.
func TestCableado_TheOutboxWorkerIsTheNewOneAndRunsOnce(t *testing.T) {
	fset, files := astDelArranque(t)
	built := 0
	for _, f := range files {
		local, ok := importsOf(t, f)[newIntegrationsImportPath]
		if !ok {
			continue
		}
		workers := outboxWorkerVars(t, fset, f, local)
		built += len(workers)
		for _, worker := range workers {
			if runs := goRunsOf(f, worker); runs != 1 {
				t.Errorf("%s: `go %s.Run(…)` aparece %d veces; se espera 1 (una sola goroutine del outbox)",
					fset.Position(f.Pos()).Filename, worker, runs)
			}
		}
	}
	if built != 1 {
		t.Errorf("el worker del outbox se asigna desde el integrations.NewWorker nuevo %d veces; se espera 1", built)
	}
}

// outboxWorkerVars devuelve las variables de f que se asignan desde <local>.NewWorker(…), y falla
// si alguno de los cuatro primeros argumentos de esa llamada no es el campo del contenedor que le
// toca: los tres almacenes nuevos y el único almacén de variables.
func outboxWorkerVars(t *testing.T, fset *token.FileSet, f *ast.File, local string) []string {
	t.Helper()
	wantArgs := []string{"c.integrationsStore", "c.buyerDataStore", "c.intakeStore", "c.tenantVars"}
	var workers []string
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || !esLlamada(call, local, "NewWorker") {
			return true
		}
		workers = append(workers, nombre(assign.Lhs[0]))
		for i, want := range wantArgs {
			if i >= len(call.Args) || campoCompletoDe(call.Args[i]) != want {
				t.Errorf("%s: el argumento %d de integrations.NewWorker no es %s: el worker leería de otro "+
					"almacén que el resto del proceso", fset.Position(call.Pos()), i+1, want)
			}
		}
		return true
	})
	return workers
}

// goRunsOf cuenta los `go <worker>.Run(…)` de f.
func goRunsOf(f *ast.File, worker string) int {
	runs := 0
	ast.Inspect(f, func(n ast.Node) bool {
		if g, ok := n.(*ast.GoStmt); ok && esLlamada(g.Call, worker, "Run") {
			runs++
		}
		return true
	})
	return runs
}

// requestsStoreReaders son los consumidores de producción cuyo almacén de solicitudes vigila este
// candado, con la posición del argumento —desde 0— en la que lo reciben y CUÁL reciben.
//
// El proyector del carrito es el ÚNICO que sigue leyendo de la segunda instancia vieja
// (arquitectura.md §4 de F6): sus puertos nombran tipos del intakes viejo, así que el almacén
// nuevo no los satisface. Muere en F8. Eran cuatro hasta F7: los tres de captación reciben desde
// conmutar(captacion) el almacén NUEVO (c.intakeStore) en el MISMO argumento, porque sus puertos
// nombran ya los tipos de internal/modulos/solicitudes. Sus nombres son los de E-11
// (ConZonasDeEnvio → WithShippingZones, NewServicio → NewService).
var requestsStoreReaders = []struct {
	pkg, fn string
	args    []int
	store   string
	why     string
}{
	{"cart", "NewProjector", []int{1, 2}, oldRequestsStoreField, "D-F6-1; muere en F8"},
	{"stages", "NewDraft", []int{3}, newRequestsStoreField, "desde F7 captación lee del almacén NUEVO"},
	{"pipeline", "WithShippingZones", []int{0}, newRequestsStoreField, "desde F7 captación lee del almacén NUEVO"},
	{"reanalisis", "NewService", []int{1}, newRequestsStoreField, "desde F7 captación lee del almacén NUEVO"},
}

// Los dos campos del contenedor que guardan un almacén de solicitudes, como se escriben.
const (
	oldRequestsStoreField = "c.intakeStoreViejo"
	newRequestsStoreField = "c.intakeStore"
)

// TestCableado_OnlyTheCartReadsTheOldStore (D-F6-1, y F7 · conmutar(captacion)): por AST, el
// proyector del carrito recibe c.intakeStoreViejo en sus dos argumentos, y en toda la producción
// del arranque ese campo se nombra exactamente TRES veces: su construcción y esas dos lecturas.
// Una cuarta sería alguien más leyendo del almacén viejo, que es justo lo que la conmutación
// quita (eran seis hasta F7, con las tres de captación). Los tres consumidores de captación
// reciben c.intakeStore, el NUEVO, en el argumento donde recibían el viejo. Y, sobre el arranque
// real, el re-análisis, el worker y la etapa draft guardan ESA instancia nueva.
func TestCableado_OnlyTheCartReadsTheOldStore(t *testing.T) {
	fset, files := astDelArranque(t)
	seen := make(map[string]int, len(requestsStoreReaders))
	mentions := 0
	inspecciona(files, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && campoCompletoDe(sel) == oldRequestsStoreField {
			mentions++
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, r := range requestsStoreReaders {
			if !esLlamada(call, r.pkg, r.fn) {
				continue
			}
			seen[r.pkg+"."+r.fn]++
			for _, i := range r.args {
				if i >= len(call.Args) || campoCompletoDe(call.Args[i]) != r.store {
					t.Errorf("%s: el argumento %d de %s.%s no es %s (%s)",
						fset.Position(call.Pos()), i+1, r.pkg, r.fn, r.store, r.why)
				}
			}
		}
		return true
	})
	for _, r := range requestsStoreReaders {
		if n := seen[r.pkg+"."+r.fn]; n != 1 {
			t.Errorf("%s.%s aparece %d veces en la producción de internal/arranque; se espera 1", r.pkg, r.fn, n)
		}
	}
	if mentions != 3 {
		t.Errorf("%s se nombra %d veces en la producción de internal/arranque; se esperan 3 (su construcción y "+
			"las dos lecturas del proyector del carrito): nadie más puede leer del almacén viejo", oldRequestsStoreField, mentions)
	}

	c := contenedorDeHuella(t, "minimo")
	kept := []struct {
		name string
		got  reflect.Value
	}{
		{"el re-análisis (reanalisis.Intakes)", field(t, c.reanalysisSvc, "intakes")},
		{"el worker, para las zonas de envío (pipeline.ShippingZones)", field(t, c.intakePipeline, "zones")},
		{"la etapa draft, para la revisión (stages.RevisionWriter, R-06)", inner(t, field(t, c.intakePipeline, "draft"), "revisions")},
	}
	for _, k := range kept {
		if !sameInstance(k.got, c.intakeStore) {
			t.Errorf("%s no lee de c.intakeStore, el almacén NUEVO de solicitudes", k.name)
		}
	}
}
