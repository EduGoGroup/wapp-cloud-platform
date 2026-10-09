package arranque

// gateway_wiring_test.go — EL TEST DE CABLEADO DE edge, COMPLETO (F3 · T3.25, R3.6.f; FX TX.11;
// 05 §4.2, hallazgo 39 de F1). No basta mirar el tipo de un campo del contenedor: se afirma, por
// import, que ningún fichero de producción de internal/arranque toca el gateway viejo; que el
// arranque construye UN solo gateway y es el nuevo; y, sobre el arranque REAL (las fases 2–8 del
// contenedor de la huella, sin red ni BD), que todos sus consumidores apuntan a esa MISMA
// instancia. Dos gateways en el proceso son un Edge conectado a uno y los envíos buscándolo en el
// otro (T-4).
//
// Desde F4 (T4.24, conmutar(inferencia)) el selector de vía es el NUEVO y recibe el
// *edgegrpc.Server como local.Frame SIN adaptador: murió bridge_gateway.go, que era la única
// excepción de este fichero.

import (
	"go/ast"
	"reflect"
	"strconv"
	"strings"
	"testing"

	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/usecase"
	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
)

// Rutas de import del gateway: el árbol VIEJO (prefijo: cubre internal/gateway y todos sus
// subpaquetes) y los dos paquetes NUEVOS que solo pueden construirse una vez.
const (
	oldGatewayTreeImportPath = "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway"
	newGatewayImportPath     = "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	newSessionImportPath     = "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// importsOf devuelve, de un fichero ya parseado, ruta de import → nombre local (el alias, o el
// último elemento de la ruta si no tiene).
func importsOf(t *testing.T, f *ast.File) map[string]string {
	t.Helper()
	imports := make(map[string]string, len(f.Imports))
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("import %s ilegible: %v", imp.Path.Value, err)
		}
		local := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			local = imp.Name.Name
		}
		imports[path] = local
	}
	return imports
}

// callsTo cuenta, en toda la producción de internal/arranque, las llamadas a la función fn del
// paquete importPath, sea cual sea el alias con el que cada fichero lo importe.
func callsTo(t *testing.T, importPath, fn string) int {
	t.Helper()
	_, files := astDelArranque(t)
	calls := 0
	for _, f := range files {
		local, ok := importsOf(t, f)[importPath]
		if !ok {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && esLlamada(call, local, fn) {
				calls++
			}
			return true
		})
	}
	return calls
}

// TestCableado_NoBootFileImportsTheOldGateway (R3.6.f): ningún fichero de producción de
// internal/arranque importa nada de internal/gateway/**. Sin esto, una fase podría levantar un
// gateway, un registro o un lease viejos y pasárselos a su consumidor sin tocar ningún campo del
// contenedor. Hasta F4 había una excepción, bridge_gateway.go (el adaptador hacia el local.Frame
// viejo); murió en conmutar(inferencia), así que ya no hay ninguna.
func TestCableado_NoBootFileImportsTheOldGateway(t *testing.T) {
	fset, files := astDelArranque(t)
	for _, f := range files {
		name := fset.Position(f.Pos()).Filename
		for path := range importsOf(t, f) {
			if path == oldGatewayTreeImportPath || strings.HasPrefix(path, oldGatewayTreeImportPath+"/") {
				t.Errorf("%s importa %s: en el arranque nuevo ningún fichero de producción puede importar el "+
					"gateway viejo (bridge_gateway.go, la única excepción, murió en F4)", name, path)
			}
		}
	}
}

// TestCableado_TheBootBuildsOneNewGateway (R3.5.a, T-4): el arranque real construye el gateway y
// es el *edgegrpc.Server NUEVO; y en toda la producción del arranque hay exactamente UN
// edgegrpc.New y UN session.NewRegistry (el que se le pasa). Un segundo de cualquiera de los dos
// serían dos mapas de sesiones vivas.
func TestCableado_TheBootBuildsOneNewGateway(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")
	if c.gw == nil {
		t.Fatal("la fase 4 no construyó el gateway")
	}
	if got := reflect.TypeOf(c.gw); got != reflect.TypeFor[*edgegrpc.Server]() {
		t.Errorf("c.gw es un %s; se espera el *grpc.Server de internal/modulos/edge", got)
	}
	if got := reflect.TypeOf(c.gw).Elem().PkgPath(); got != newGatewayImportPath {
		t.Errorf("c.gw es de %s; se espera %s", got, newGatewayImportPath)
	}
	if n := callsTo(t, newGatewayImportPath, "New"); n != 1 {
		t.Errorf("edgegrpc.New aparece %d veces en la producción de internal/arranque; se espera 1", n)
	}
	if n := callsTo(t, newSessionImportPath, "NewRegistry"); n != 1 {
		t.Errorf("session.NewRegistry aparece %d veces en la producción de internal/arranque; se espera 1 "+
			"(el del único gateway)", n)
	}
	if n := callsTo(t, oldGatewayTreeImportPath+"/grpc", "New"); n != 0 {
		t.Errorf("el gateway VIEJO se construye %d veces en la producción de internal/arranque; se espera 0", n)
	}
}

// field lee por reflexión el campo name de la estructura a la que apunta ptr.
func field(t *testing.T, ptr any, name string) reflect.Value {
	t.Helper()
	v := reflect.ValueOf(ptr)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		t.Fatalf("%T no es un puntero vivo: el arranque no construyó a este consumidor", ptr)
	}
	f := v.Elem().FieldByName(name)
	if !f.IsValid() {
		t.Fatalf("%T no tiene el campo %q: el test de cableado se quedó atrás", ptr, name)
	}
	return f
}

// TestIdentidad_EveryConsumerSharesTheOneGateway (FX TX.11, T-4): sobre el arranque real, todo el
// que habla con un Edge lo hace por el MISMO puntero que c.gw: el notificador de solicitudes
// (el nuevo desde F6), el empuje de filtros nuevo, el ConfigPush de E2 —en la cara NUEVA desde F7—, el selector LLM
// nuevo —que lo recibe como Frame y, del mismo valor, como enrutador de plazas—, y las deps de D1
// y D5 de la cara nueva. Y la cara VIEJA ya no recibe ConfigPush: su único lector era E2.
func TestIdentidad_EveryConsumerSharesTheOneGateway(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")
	if c.gw == nil {
		t.Fatal("la fase 4 no construyó el gateway")
	}

	// Desde F4 no hay adaptador: el Frame del selector es el propio *edgegrpc.Server. Y el
	// enrutador de plazas sale del mismo valor por aserción de tipo: si quedara nil, el aforo
	// por Edge del pipeline de lote se apagaría con solo un Warn (T-1).
	frame := field(t, c.llmSelector, "frame")
	if frame.IsNil() {
		t.Fatal("el selector LLM no tiene frame: la vía local quedaría sin gateway")
	}
	if got := frame.Elem().Type(); got != reflect.TypeFor[*edgegrpc.Server]() {
		t.Fatalf("el Frame del selector LLM es un %s; se espera el *grpc.Server de internal/modulos/edge, "+
			"sin adaptador", got)
	}

	edge := edgeDepsOfTheNewFace(c)
	consumers := []struct {
		name string
		got  reflect.Value
	}{
		{"el MessageSender del notificador de solicitudes (intakes.Notifier)", field(t, c.intakeNotifier, "sender")},
		{"el ConfigPusher del empuje de filtros (filtercfg.Pusher)", field(t, c.filtersPusher, "push")},
		{"el IntentsDeps.ConfigPush de E2 en la cara nueva", reflect.ValueOf(captureDepsOfTheNewFace(c).intents.ConfigPush)},
		{"el Frame del selector LLM", frame},
		{"el enrutador de plazas del selector LLM (aforo por Edge)", field(t, c.llmSelector, "router")},
		{"el Sender de D1 en la cara nueva", reflect.ValueOf(edge.messages.Sender)},
		{"el DiagnosticsRequester de D5 en la cara nueva", reflect.ValueOf(edge.diagnostics.DiagnosticsRequester)},
	}
	for _, k := range consumers {
		if !sameInstance(k.got, c.gw) {
			t.Errorf("%s no es la MISMA instancia que c.gw: habría dos gateways en el proceso", k.name)
		}
	}
	if old := depsDeLaAPIPublica(c).ConfigPush; old != nil {
		t.Errorf("la cara vieja recibe un ConfigPush (%T); se espera nil: E2, su único lector, la sirve la nueva", old)
	}
}

// TestCableado_AbsentAccessServicesReachTheGatewayAsTrueNil: el gateway decide «auth no
// disponible» y «no audito» comparando sus puertos con nil. Un *usecase.DelegatedAuthService nil
// metido en la interfaz NO es nil: el gateway creería tener autenticador y reventaría en el
// primer login. Aquí se cubren la costura y el arranque real; que nadie esquiva la costura lo
// afirma TestCableado_TheGatewayAuthOptionsGoThroughTheNilSeam.
func TestCableado_AbsentAccessServicesReachTheGatewayAsTrueNil(t *testing.T) {
	// La costura: nil entra, nil DE VERDAD sale; con servicio, sale ESA instancia.
	if got := edgeAuthenticatorPort(nil); got != nil {
		t.Errorf("edgeAuthenticatorPort(nil) = %T; se espera un nil de verdad", got)
	}
	if got := edgeAuditorPort(nil); got != nil {
		t.Errorf("edgeAuditorPort(nil) = %T; se espera un nil de verdad", got)
	}
	authn, auditor := &iamusecase.DelegatedAuthService{}, &iamusecase.AuditService{}
	if got, ok := edgeAuthenticatorPort(authn).(*iamusecase.DelegatedAuthService); !ok || got != authn {
		t.Errorf("edgeAuthenticatorPort(svc) no entrega la misma instancia: %T", got)
	}
	if got, ok := edgeAuditorPort(auditor).(*iamusecase.AuditService); !ok || got != auditor {
		t.Errorf("edgeAuditorPort(svc) no entrega la misma instancia: %T", got)
	}

	// El arranque real sin identity: el campo del gateway es una interfaz nil.
	c := contenedorDeHuella(t, "minimo")
	if c.authStk.edgeAuthSvc != nil {
		t.Fatal("el perfil «minimo» construyó un autenticador delegado: este caso ya no cubre el nil")
	}
	if got := gatewayField(t, c, "authn"); !got.IsNil() {
		t.Errorf("sin identity el gateway recibe un autenticador %s; se espera un nil de verdad", got.Elem().Type())
	}
}

// TestCableado_TheGatewayAuthOptionsGoThroughTheNilSeam: por AST, las dos opciones de auth del
// gateway reciben la costura (edgeAuthenticatorPort, edgeAuditorPort) y no el campo del authStack
// a pelo, que compilaría igual y metería un puntero nil en la interfaz.
func TestCableado_TheGatewayAuthOptionsGoThroughTheNilSeam(t *testing.T) {
	seams := map[string]string{"WithAuthenticator": "edgeAuthenticatorPort", "WithAuthAuditor": "edgeAuditorPort"}
	seen := make(map[string]bool, len(seams))
	fset, files := astDelArranque(t)
	for _, f := range files {
		local, ok := importsOf(t, f)[newGatewayImportPath]
		if !ok {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for option, seam := range seams {
				if !esLlamada(call, local, option) {
					continue
				}
				seen[option] = true
				if !isSingleCallTo(call.Args, seam) {
					t.Errorf("%s: %s.%s no recibe %s(…): un puntero nil a pelo llegaría al gateway como "+
						"una interfaz NO nil", fset.Position(call.Pos()), local, option, seam)
				}
			}
			return true
		})
	}
	for option := range seams {
		if !seen[option] {
			t.Errorf("la producción de internal/arranque no cablea edgegrpc.%s", option)
		}
	}
}

// isSingleCallTo dice si args es exactamente una llamada a la función fn de este paquete.
func isSingleCallTo(args []ast.Expr, fn string) bool {
	if len(args) != 1 {
		return false
	}
	call, ok := args[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == fn
}
