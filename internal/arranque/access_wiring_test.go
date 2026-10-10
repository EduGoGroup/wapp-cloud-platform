package arranque

// access_wiring_test.go — EL TEST DE CABLEADO DE acceso, COMPLETO (F2 · T2.29, R2.4.a, R2.5.f;
// 05 §4.2, hallazgo 39 de F1). No basta mirar el tipo de un campo del contenedor: se afirma,
// sobre el arranque nuevo REAL (las fases 2–8 del contenedor de la huella, sin red ni BD), que
// construye el resolver de derechos, el autenticador delegado y el auditor NUEVOS y que esas
// MISMAS instancias llegan a sus consumidores; y, por import, que ningún fichero de producción de
// internal/arranque toca los paquetes viejos de acceso.
//
// Desde F3 (T3.28, conmutar(edge)) el gateway es el NUEVO y recibe el in.Authenticator y el
// in.Auditor nuevos SIN adaptador: murió bridge_iam.go y `acceso` entró en Conmutados
// (internal/modulos/fronteras_test.go).

import (
	"errors"
	"go/ast"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

// Rutas de import de los paquetes VIEJOS de acceso. oldIAMImportPath es un prefijo: cubre
// internal/iam y todos sus subpaquetes.
const (
	oldIAMImportPath           = "github.com/EduGoGroup/wapp-cloud-platform/internal/iam"
	oldEntitlementsImportPath  = "github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	oldPlatformadminImportPath = "github.com/EduGoGroup/wapp-cloud-platform/internal/platformadmin"
)

// isOldAccessPath dice si path es un paquete viejo de acceso (o un subpaquete suyo).
func isOldAccessPath(path string) bool {
	for _, old := range []string{oldIAMImportPath, oldEntitlementsImportPath, oldPlatformadminImportPath} {
		if path == old || strings.HasPrefix(path, old+"/") {
			return true
		}
	}
	return false
}

// TestBootWiring_AccessNoFileImportsOldPackages es la mitad «ninguna fase importa el viejo»
// (R2.5.f): sin ella, una fase podría construir el resolver o el auditor VIEJOS y pasárselos a
// su consumidor sin tocar ningún campo del contenedor. Hasta F3 había una excepción con lista
// blanca de símbolos, bridge_iam.go (el adaptador hacia el gateway viejo); murió con él en
// conmutar(edge), así que ya no hay ninguna: ni un fichero de producción de internal/arranque
// importa internal/iam, internal/entitlements o internal/platformadmin. (Los _test.go los cubre
// la regla 3 de internal/modulos/fronteras_test.go, con acceso en Conmutados.)
func TestBootWiring_AccessNoFileImportsOldPackages(t *testing.T) {
	fset, files := astDelArranque(t)
	for _, f := range files {
		name := fset.Position(f.Pos()).Filename
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: import %s ilegible: %v", name, imp.Path.Value, err)
			}
			if isOldAccessPath(path) {
				t.Errorf("%s importa %s: en el arranque nuevo ningún fichero puede importar un paquete "+
					"viejo de acceso (bridge_iam.go, la única excepción, murió en F3)", name, path)
			}
		}
	}
}

// TestBootWiring_AccessSingleNewEntitlementsResolver (R2.4.a, T-5): en todo el código de
// producción del arranque hay UNA sola construcción del resolver de derechos, y es la NUEVA. Dos
// serían dos cachés con TTL y dos verdades sobre qué tiene contratado un tenant.
func TestBootWiring_AccessSingleNewEntitlementsResolver(t *testing.T) {
	_, files := astDelArranque(t)
	calls := 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && esLlamada(call, "entitlements", "NewPostgres") {
				calls++
			}
			return true
		})
	}
	if calls != 1 {
		t.Errorf("entitlements.NewPostgres aparece %d veces en la producción de internal/arranque; se espera 1 (R2.4.a)", calls)
	}
}

// TestBootWiring_AccessNewServicesReachEveryConsumer (R2.4.a, R2.5.a, R2.5.f), sobre el arranque
// real en el perfil «minimo» (sin identity): el resolver de derechos es el *entitlements.Postgres
// NUEVO y la MISMA instancia llega a la bandeja de plataforma (platformadmin.Repository), al gate
// del puente CRM y —desde F8, conmutar(conversacion)— al Motor de Flujos nuevo (el runtime, el
// agregador de ventanas y el despachador) y a las tres áreas de conversación de la cara nueva que
// gatean por plan (I14–I17, I18 e I19). La cara vieja murió con depsDeLaAPIPublica: lo que se
// afirmaba de su Entitlements se afirma ahora de lo que buildPublicAPIServer recibe y reparte
// (TestBootWiring_AccessTheFaceGetsTheOneResolver). El gateway (el nuevo) recibe
// como auditor el MISMO *usecase.AuditService NUEVO del authStack, sin nada alrededor, y como
// autenticador un nil DE VERDAD (sin identity no hay quien valide credenciales): un
// *usecase.DelegatedAuthService nil metido en la interfaz le haría creer que tiene autenticador.
func TestBootWiring_AccessNewServicesReachEveryConsumer(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	if c.entResolver == nil {
		t.Fatal("la fase 3 no construyó el resolver de derechos")
	}
	if got := reflect.TypeOf(c.entResolver).Elem().PkgPath(); got != reflect.TypeFor[entitlements.Postgres]().PkgPath() {
		t.Errorf("c.entResolver es de %s; se espera el paquete NUEVO de acceso", got)
	}
	if !sameInstance(reflect.ValueOf(c.platformRepo).Elem().FieldByName("features"), c.entResolver) {
		t.Error("platformadmin.Repository no recibe c.entResolver: el alta de la bandeja preguntaría a otra caché")
	}
	if !sameInstance(reflect.ValueOf(c.webhookGate).Elem().FieldByName("features"), c.entResolver) {
		t.Error("el gate del puente CRM no recibe c.entResolver")
	}
	face := conversationDepsOfTheNewFace(c)
	consumers := []struct {
		name string
		got  reflect.Value
	}{
		{"el runtime del Motor de Flujos (flowruntime.WithEntitlements)", field(t, c.flowRuntime, "entitlements")},
		{"el agregador de ventanas (flowruntime.NewIntakeAggregator)", field(t, c.intakeAggregator, "ents")},
		{"el despachador de eventos (events.NewDispatcher)", field(t, c.dispatcher, "feats")},
		{"el Entitlements de I14–I17 en la cara nueva", reflect.ValueOf(face.catalogImport.Entitlements)},
		{"el Entitlements de I18 en la cara nueva", reflect.ValueOf(face.events.Entitlements)},
		{"el Entitlements de I19 en la cara nueva", reflect.ValueOf(face.eventCancel.Entitlements)},
	}
	for _, k := range consumers {
		if !sameInstance(k.got, c.entResolver) {
			t.Errorf("%s no es la MISMA instancia que c.entResolver: habría dos cachés de derechos", k.name)
		}
	}

	if got := gatewayField(t, c, "authn"); !got.IsNil() {
		t.Errorf("sin identity el gateway recibe un autenticador %s; se espera un nil de verdad", got.Elem().Type())
	}
	assertGatewayHolds(t, gatewayField(t, c, "authAuditor"), c.authStk.auditor)
}

// TestBootWiring_AccessTheFaceGetsTheOneResolver (R2.4.a; F8 · conmutar(conversacion), T-2): lo
// que hasta F8 se leía de publicapi.Deps.Entitlements —que la API pública gatea con la MISMA
// instancia que c.entResolver— se afirma ahora por AST sobre el camino que quedó: la fase de
// transporte entrega `c.entResolver` a buildPublicAPIServer (publicAPIDeps.entResolver) y éste lo
// reparte como `d.entResolver` a apipublica.EntitlementsDeps. Las dos líneas son valores de un
// literal: escribir ahí otro resolver compilaría igual y serían dos cachés.
func TestBootWiring_AccessTheFaceGetsTheOneResolver(t *testing.T) {
	_, files := astDelArranque(t)
	wants := map[string]string{
		"publicAPIDeps.entResolver":                "c.entResolver",
		"apipublica.EntitlementsDeps.Entitlements": "d.entResolver",
	}
	seen := make(map[string][]string, len(wants))
	inspecciona(files, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				key := campoCompletoDe(lit.Type) + "." + campoCompletoDe(kv.Key)
				if _, watched := wants[key]; watched {
					seen[key] = append(seen[key], campoCompletoDe(kv.Value))
				}
			}
		}
		return true
	})
	for key, want := range wants {
		if got := seen[key]; len(got) != 1 || got[0] != want {
			t.Errorf("%s se cablea con %q; se espera una sola vez, con %s (una sola caché de derechos)", key, got, want)
		}
	}
}

// TestBootWiring_AccessGatewayGetsTheNewDelegatedAuth (R2.5.a): con la delegación a identity
// encendida —la que construye wireDelegatedAuth, la función de producción—, el gateway recibe
// EXACTAMENTE el *usecase.DelegatedAuthService NUEVO del authStack (y su auditor), no un
// autenticador viejo ni un adaptador.
func TestBootWiring_AccessGatewayGetsTheNewDelegatedAuth(t *testing.T) {
	c := containerWithDelegatedAuth(t)
	if c.authStk.edgeAuthSvc == nil {
		t.Fatal("wireDelegatedAuth no construyó el autenticador delegado")
	}
	assertGatewayHolds(t, gatewayField(t, c, "authn"), c.authStk.edgeAuthSvc)
	assertGatewayHolds(t, gatewayField(t, c, "authAuditor"), c.authStk.auditor)
}

// gatewayField lee por reflexión el campo name (una interfaz) del *edgegrpc.Server de la fase 4.
func gatewayField(t *testing.T, c *contenedor, name string) reflect.Value {
	t.Helper()
	f := reflect.ValueOf(c.gw).Elem().FieldByName(name)
	if !f.IsValid() || f.Kind() != reflect.Interface {
		t.Fatalf("*edgegrpc.Server no tiene el campo de interfaz %q: el test de cableado se quedó atrás", name)
	}
	return f
}

// assertGatewayHolds afirma que la interfaz field del gateway guarda EXACTAMENTE la instancia
// want (un servicio nuevo del authStack), sin adaptador de por medio.
func assertGatewayHolds(t *testing.T, field reflect.Value, want any) {
	t.Helper()
	if field.IsNil() {
		t.Fatalf("el gateway recibe nil; se espera el %T del authStack", want)
	}
	if !sameInstance(field, want) {
		t.Errorf("el gateway recibe un %s que no es la instancia %T del authStack", field.Elem().Type(), want)
	}
}

// containerWithDelegatedAuth es el contenedor de la huella (fases 1 simulada y 2–8 reales) con
// la delegación a identity encendida SIN red: tras la fase 2 se llama a wireDelegatedAuth, la
// función de producción, con una URL de identity (el cliente no conecta al construirse) y un canje
// de relleno (el de verdad exige el JWKS, que es red). El canje se retira después, para que el
// resto del arranque vea el modo dual apagado, como en el perfil «minimo».
func containerWithDelegatedAuth(t *testing.T) *contenedor {
	t.Helper()
	entornoDeHuella(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	c := nuevoContenedor(cfg, quietLogger())
	t.Cleanup(c.cerrar)
	infraestructuraDeHuella(t, c)
	if err := construir(t.Context(), c, fases[1:2]); err != nil {
		t.Fatalf("fase 2: %v", err)
	}
	withIdentity := c.cfg
	withIdentity.Identity.URL = "http://127.0.0.1:1"
	c.authStk.exchangeSvc = &iamusecase.ExchangeService{}
	if err := c.authStk.wireDelegatedAuth(withIdentity, c.authStk.validator, quietLogger()); err != nil {
		t.Fatalf("wireDelegatedAuth: %v", err)
	}
	c.authStk.exchangeSvc = nil
	if err := construir(t.Context(), c, fases[2:8]); err != nil {
		t.Fatalf("fases 3–8: %v", err)
	}
	t.Cleanup(func() {
		c.enrollGS.Stop()
		c.connectGS.Stop()
		for _, l := range []net.Listener{c.enrollLis, c.connectLis} {
			if err := l.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				t.Logf("cerrando listener: %v", err)
			}
		}
	})
	return c
}
