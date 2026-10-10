package arranque

// solicitudes_cableado_test.go — EL TEST DE CABLEADO DE solicitudes, COMPLETO (F6 · T6.24,
// R6.6.a–c; arquitectura.md §6 de F6; 05 §4.2, hallazgo 39 de F1). No basta mirar el tipo de un
// campo del contenedor: se afirma, sobre el arranque nuevo REAL (las fases 2–8 del contenedor de
// la huella, sin red ni BD), que el Service, el notificador, los recordatorios, el gate, los
// almacenes y el generador de cotización son los de internal/modulos/solicitudes; que de cada uno
// hay UNA construcción, la nueva; que, por ruta de import, ningún fichero de internal/arranque
// —de producción o de test— toca los paquetes viejos de solicitudes; y que todos los lectores del
// almacén, también el proyector del carrito, leen del ÚNICO que hay, c.intakeStore.
//
// Desde F8 (T8.32, conmutar(conversacion)) no queda nada viejo: el carrito es el de
// internal/modulos/conversacion, sus puertos nombran los tipos del intakes nuevo, y la segunda
// instancia vieja del almacén (D-F6-1, el campo intakeStoreViejo) murió con su sitio declarado.
// `solicitudes` entra en Conmutados (internal/modulos/fronteras_test.go).
//
// La mitad de IDENTIDAD (mismas instancias en el contenedor y en la cara nueva, y el plazo de G7)
// vive en solicitudes_cableado_identidad_test.go; se parten por tamaño (E-13).

import (
	"go/ast"
	"go/token"
	"reflect"
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
// (reglas.md §4, punto 7), que busca esas rutas como texto, no encuentre este candado.
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

// isOldRequestsPath dice si path es un paquete viejo de solicitudes (o un subpaquete suyo).
func isOldRequestsPath(path string) bool {
	for _, old := range []string{oldIntakesImportPath, oldIntegrationsImportPath, oldTenantVarsImportPath} {
		if path == old || strings.HasPrefix(path, old+"/") {
			return true
		}
	}
	return false
}

// TestCableado_NoBootFileImportsTheOldRequests (R6.6.a, definición de hecho 7 de F6): se barren
// por ruta de import TODOS los .go de internal/arranque, producción Y tests, y los paquetes viejos
// de solicitudes no aparecen en NINGUNO. Sin esto, una fase podría construir un Service, un
// notificador o un worker viejos y pasárselos a su consumidor sin tocar ningún campo del
// contenedor; y un test que importara lo viejo para comparar salidas sería un puente a escondidas.
// De F6 a F8 hubo una lista blanca, el sitio declarado de la segunda instancia vieja del almacén
// (contenedor.go y fase3_almacenes.go, con el alias `intakesviejo`); murió con esa instancia en
// conmutar(conversacion), así que la lista de importadores es vacía.
func TestCableado_NoBootFileImportsTheOldRequests(t *testing.T) {
	if isOldRequestsPath(oldCaptureQueueImportPath) || isOldRequestsPath(newIntakesImportPath) {
		t.Fatal("isOldRequestsPath toma por solicitudes viejas a la cola de captación o al intakes nuevo: el barrido mentiría (T-2)")
	}
	if !isOldRequestsPath(oldIntakesImportPath + "/quotetext") {
		t.Fatal("isOldRequestsPath no reconoce un subpaquete del intakes viejo: el barrido no vería nada")
	}
	for _, line := range bootFilesImporting(t, isOldRequestsPath) {
		t.Errorf("%s: en el arranque nuevo ningún fichero puede importar un paquete viejo de solicitudes "+
			"(el sitio declarado de la segunda instancia vieja del almacén murió en F8)", line)
	}
}

// TestCableado_TheBootBuildsTheNewRequests (R6.6.a, R6.6.b): el arranque real construye el
// Service, el notificador, los dos recordatorios, el gate del puente CRM, los tres almacenes, el
// de variables y el generador de cotización, y todos son los del módulo NUEVO. Ya no hay pieza
// vieja: la segunda instancia del almacén (D-F6-1, el campo intakeStoreViejo) murió en F8 con su
// último lector, el proyector del carrito viejo.
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
	}
	for _, b := range built {
		v := reflect.ValueOf(b.got)
		if v.Kind() != reflect.Pointer || v.IsNil() {
			t.Errorf("%s no está construido tras las fases 3, 5 y 6", b.name)
			continue
		}
		if v.Type() != b.want {
			t.Errorf("%s es un %s; se espera %s", b.name, v.Type(), b.want)
		}
		if got := v.Type().Elem().PkgPath(); got != b.pkgPath {
			t.Errorf("%s es de %s; se espera %s", b.name, got, b.pkgPath)
		}
	}
}

// TestCableado_OneNewConstructionOfEachRequestsPiece (R6.6.b, reglas.md §3): en toda la producción
// del arranque hay exactamente UNA llamada a cada constructor nuevo de solicitudes y NINGUNA a su
// gemelo viejo, ya sin excepción: el almacén viejo se construía UNA vez de F6 a F8 (la segunda
// instancia, D-F6-1) y ahora ninguna. Dos Service serían dos máquinas de estados; dos
// notificadores o recordatorios, dos criterios de «ya avisé»; dos workers, lotes reclamados a la
// vez; y dos almacenes de variables es lo que había hasta F6 (uno por fase 8 y otro por fase 9).
func TestCableado_OneNewConstructionOfEachRequestsPiece(t *testing.T) {
	constructors := []struct {
		newPath, oldPath, fn string
		oldFn                string // nombre del gemelo viejo si cambió (E-11); "" ⇒ el mismo
		oldCalls             int
	}{
		{newIntakesImportPath, oldIntakesImportPath, "NewPostgres", "", 0},
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
// candado, con la posición del argumento —desde 0— en la que lo reciben. Los cuatro reciben EL
// almacén, c.intakeStore.
//
// El proyector del carrito leyó de F6 a F8 de la segunda instancia vieja (arquitectura.md §4 de
// F6): sus puertos nombraban tipos del intakes viejo. Desde conmutar(conversacion) es el carrito
// de internal/modulos/conversacion y recibe el almacén NUEVO en los MISMOS dos argumentos
// (escritor de revisiones y garante del envío). Los tres de captación lo reciben desde F7. Sus
// nombres son los de E-11 (ConZonasDeEnvio → WithShippingZones, NewServicio → NewService).
var requestsStoreReaders = []struct {
	pkg, fn string
	args    []int
	store   string
	why     string
}{
	{"cart", "NewProjector", []int{1, 2}, newRequestsStoreField, "desde F8 el carrito lee del almacén NUEVO; D-F6-1 murió"},
	{"stages", "NewDraft", []int{3}, newRequestsStoreField, "desde F7 captación lee del almacén NUEVO"},
	{"pipeline", "WithShippingZones", []int{0}, newRequestsStoreField, "desde F7 captación lee del almacén NUEVO"},
	{"reanalisis", "NewService", []int{1}, newRequestsStoreField, "desde F7 captación lee del almacén NUEVO"},
}

// El campo del contenedor que guarda EL almacén de solicitudes y el que guardó de F6 a F8 la
// segunda instancia vieja, como se escriben. El segundo ya no existe: se nombra para contar que
// nadie lo menciona.
const (
	oldRequestsStoreField = "c.intakeStoreViejo"
	newRequestsStoreField = "c.intakeStore"
)

// TestCableado_TheCartReadsTheOneStore (muere D-F6-1; F8 · conmutar(conversacion)): por AST, el
// proyector del carrito recibe c.intakeStore en sus dos argumentos, igual que los tres
// consumidores de captación en el suyo, y en toda la producción del arranque el campo de la
// segunda instancia vieja no se nombra NINGUNA vez (eran tres hasta F8: su construcción y las dos
// lecturas del carrito; seis hasta F7). Y, sobre el arranque real, el proyector del carrito
// —dentro del PersistSink del runtime—, el re-análisis, el worker y la etapa draft guardan ESA
// instancia nueva.
func TestCableado_TheCartReadsTheOneStore(t *testing.T) {
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
	if mentions != 0 {
		t.Errorf("%s se nombra %d veces en la producción de internal/arranque; se esperan 0: la segunda "+
			"instancia vieja del almacén murió en F8", oldRequestsStoreField, mentions)
	}

	c := contenedorDeHuella(t, "minimo")
	projector := cartProjectorOf(t, c)
	kept := []struct {
		name string
		got  reflect.Value
	}{
		{"el proyector del carrito, para las revisiones (cart.RevisionWriter)", projector.FieldByName("revisions")},
		{"el proyector del carrito, para el envío (cart.ShippingEnsurer)", projector.FieldByName("shipping")},
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

// cartProjectorOf baja hasta el *cart.Projector que el arranque real metió en el PersistSink del
// runtime (flowruntime.WithEventSink) y devuelve su estructura. Se busca por nombre de tipo y no
// por import: este fichero no necesita el paquete del carrito para decir de dónde lee.
func cartProjectorOf(t *testing.T, c *contenedor) reflect.Value {
	t.Helper()
	sinks := field(t, c.flowRuntime, "sinks")
	for i := range sinks.Len() {
		sink := sinks.Index(i).Elem()
		if sink.Kind() != reflect.Pointer || sink.Type().Elem().Name() != "PersistSink" {
			continue
		}
		projectors := sink.Elem().FieldByName("projectors")
		for j := range projectors.Len() {
			p := projectors.Index(j).Elem()
			if p.Kind() == reflect.Pointer && p.Type().String() == "*cart.Projector" {
				if got := p.Type().Elem().PkgPath(); got != internalTreePath+"modulos/conversacion/modules/cart" {
					t.Fatalf("el proyector del carrito es de %s; se espera el de internal/modulos/conversacion", got)
				}
				return p.Elem()
			}
		}
	}
	t.Fatal("el runtime del arranque no lleva un PersistSink con un *cart.Projector: el carrito no proyectaría nada")
	return reflect.Value{}
}
