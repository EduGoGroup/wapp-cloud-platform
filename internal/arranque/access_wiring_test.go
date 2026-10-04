package arranque

// access_wiring_test.go — EL TEST DE CABLEADO DE acceso, COMPLETO (F2 · T2.29, R2.4.a, R2.5.f;
// 05 §4.2, hallazgo 39 de F1). No basta mirar el tipo de un campo del contenedor: se afirma,
// sobre el arranque nuevo REAL (las fases 2–8 del contenedor de la huella, sin red ni BD), que
// construye el resolver de derechos, el autenticador delegado y el auditor NUEVOS y que esas
// MISMAS instancias llegan a sus consumidores; y, por import, que ningún fichero de producción de
// internal/arranque salvo el adaptador (bridge_iam.go) toca los paquetes viejos de acceso.
//
// `acceso` NO entra en Conmutados (internal/modulos/fronteras_test.go) mientras viva
// bridge_iam.go: entra en F3, cuando el gateway nuevo reciba el in.Authenticator nuevo.

import (
	"errors"
	"go/ast"
	"net"
	"reflect"
	"slices"
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

// oldAccessImporters es la lista blanca, por fichero y por ruta de import, de los ficheros de
// producción de internal/arranque que pueden importar un paquete viejo de acceso, con los ÚNICOS
// símbolos que pueden usar de él. Solo el adaptador, y solo tipos y centinelas: ningún
// constructor, así que el arranque nuevo no puede levantar un servicio viejo de acceso.
//
// Cada símbolo de la lista tiene que usarse: un permiso que nadie usa se retira en el mismo
// commit (TestBootWiring_AccessWhitelistIsTight). Todo sale de aquí en F3, cuando muere
// bridge_iam.go.
var oldAccessImporters = map[string]map[string][]string{
	"bridge_iam.go": {
		oldIAMImportPath + "/domain": {
			"AuditEvent", "AuthResult", "ErrInvalidCredentials", "ErrInvalidInput",
			"ErrRefreshInvalid", "ErrUserInactive", "IdentityContext",
		},
		oldIAMImportPath + "/ports/in": {
			"AuditInput", "Auditor", "Authenticator", "LoginInput", "LogoutInput", "RefreshInput", "VerifyResult",
		},
	},
}

// isOldAccessPath dice si path es un paquete viejo de acceso (o un subpaquete suyo).
func isOldAccessPath(path string) bool {
	for _, old := range []string{oldIAMImportPath, oldEntitlementsImportPath, oldPlatformadminImportPath} {
		if path == old || strings.HasPrefix(path, old+"/") {
			return true
		}
	}
	return false
}

// oldAccessUses recorre los ficheros de producción de internal/arranque y devuelve, por fichero y
// por ruta de import, lo que hacen con los paquetes viejos de acceso.
func oldAccessUses(t *testing.T) map[string]map[string]oldContactUse {
	t.Helper()
	fset, files := astDelArranque(t)
	uses := make(map[string]map[string]oldContactUse)
	for _, f := range files {
		name := fset.Position(f.Pos()).Filename
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: import %s ilegible: %v", name, imp.Path.Value, err)
			}
			if !isOldAccessPath(path) {
				continue
			}
			local := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			if uses[name] == nil {
				uses[name] = make(map[string]oldContactUse)
			}
			uses[name][path] = oldContactUse{localName: local, selectors: selectorsOn(f, local)}
		}
	}
	return uses
}

// TestBootWiring_AccessOnlyBridgeImportsOldPackages es la mitad «ninguna fase importa el viejo
// fuera del adaptador» (R2.5.f): sin ella, una fase podría construir el resolver o el auditor
// VIEJOS y pasárselos a su consumidor sin tocar ningún campo del contenedor.
func TestBootWiring_AccessOnlyBridgeImportsOldPackages(t *testing.T) {
	uses := oldAccessUses(t)
	for name, paths := range uses {
		for path := range paths {
			if _, ok := oldAccessImporters[name][path]; !ok {
				t.Errorf("%s importa %s: en el arranque nuevo solo bridge_iam.go (el adaptador del gateway viejo) "+
					"puede importar un paquete viejo de acceso, y solo los de su lista blanca", name, path)
			}
		}
	}
}

// TestBootWiring_AccessWhitelistIsTight cierra la otra puerta: el adaptador usa del paquete viejo
// SOLO los símbolos declarados (ningún constructor), y la lista no se queda atrás —un fichero, una
// ruta o un símbolo que ya nadie usa es un permiso sobrante y falla—.
func TestBootWiring_AccessWhitelistIsTight(t *testing.T) {
	uses := oldAccessUses(t)
	for name, paths := range oldAccessImporters {
		for path, allowed := range paths {
			use, ok := uses[name][path]
			if !ok {
				t.Errorf("%s tiene %s en la lista blanca oldAccessImporters pero ya no lo importa (o el fichero "+
					"ya no existe): quítalo de la lista", name, path)
				continue
			}
			if use.localName == "." || use.localName == "_" {
				t.Errorf("%s importa %s como %q: así no se ve qué símbolos usa; impórtalo con nombre", name, path, use.localName)
				continue
			}
			for _, sel := range use.selectors {
				if !slices.Contains(allowed, sel) {
					t.Errorf("%s usa %s.%s del paquete viejo %s; solo puede usar %v", name, use.localName, sel, path, allowed)
				}
			}
			for _, sym := range allowed {
				if !slices.Contains(use.selectors, sym) {
					t.Errorf("%s ya no usa %s.%s (%s): quítalo de la lista blanca", name, use.localName, sym, path)
				}
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
// NUEVO y la MISMA instancia llega a la cara vieja (publicapi.Deps.Entitlements), a la bandeja de
// plataforma (platformadmin.Repository) y al gate del puente CRM; el gateway viejo recibe como
// auditor un auditorBridge sobre el *usecase.AuditService NUEVO del authStack, y como
// autenticador un nil DE VERDAD (sin identity no hay quien valide credenciales).
func TestBootWiring_AccessNewServicesReachEveryConsumer(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	if c.entResolver == nil {
		t.Fatal("la fase 3 no construyó el resolver de derechos")
	}
	if got := reflect.TypeOf(c.entResolver).Elem().PkgPath(); got != reflect.TypeFor[entitlements.Postgres]().PkgPath() {
		t.Errorf("c.entResolver es de %s; se espera el paquete NUEVO de acceso", got)
	}
	if got := depsDeLaAPIPublica(c).Entitlements; got != c.entResolver {
		t.Errorf("publicapi.Deps.Entitlements = %T (%p); se espera la MISMA instancia que c.entResolver (una sola caché)", got, got)
	}
	if !sameInstance(reflect.ValueOf(c.platformRepo).Elem().FieldByName("features"), c.entResolver) {
		t.Error("platformadmin.Repository no recibe c.entResolver: el alta de la bandeja preguntaría a otra caché")
	}
	if !sameInstance(reflect.ValueOf(c.webhookGate).Elem().FieldByName("features"), c.entResolver) {
		t.Error("el gate del puente CRM no recibe c.entResolver")
	}

	if got := gatewayField(t, c, "authn"); !got.IsNil() {
		t.Errorf("sin identity el gateway recibe un autenticador %s; se espera un nil de verdad", got.Elem().Type())
	}
	assertBridgeOver(t, gatewayField(t, c, "authAuditor"), reflect.TypeFor[*auditorBridge](), c.authStk.auditor)
}

// TestBootWiring_AccessGatewayGetsBridgeOverNewDelegatedAuth (R2.5.a): con la delegación a identity
// encendida —la que construye wireDelegatedAuth, la función de producción—, el gateway viejo
// recibe un authenticatorBridge sobre el *usecase.DelegatedAuthService NUEVO del authStack, no un
// autenticador viejo.
func TestBootWiring_AccessGatewayGetsBridgeOverNewDelegatedAuth(t *testing.T) {
	c := containerWithDelegatedAuth(t)
	if c.authStk.edgeAuthSvc == nil {
		t.Fatal("wireDelegatedAuth no construyó el autenticador delegado")
	}
	assertBridgeOver(t, gatewayField(t, c, "authn"), reflect.TypeFor[*authenticatorBridge](), c.authStk.edgeAuthSvc)
	assertBridgeOver(t, gatewayField(t, c, "authAuditor"), reflect.TypeFor[*auditorBridge](), c.authStk.auditor)
}

// gatewayField lee por reflexión el campo name (una interfaz) del *gatewaygrpc.Server de la fase 4.
func gatewayField(t *testing.T, c *contenedor, name string) reflect.Value {
	t.Helper()
	f := reflect.ValueOf(c.gw).Elem().FieldByName(name)
	if !f.IsValid() || f.Kind() != reflect.Interface {
		t.Fatalf("*gatewaygrpc.Server no tiene el campo de interfaz %q: el test de cableado se quedó atrás", name)
	}
	return f
}

// assertBridgeOver afirma que la interfaz field guarda un adaptador del tipo bridge cuyo next es
// EXACTAMENTE la instancia want (un servicio nuevo del authStack).
func assertBridgeOver(t *testing.T, field reflect.Value, bridge reflect.Type, want any) {
	t.Helper()
	if field.IsNil() {
		t.Fatalf("el gateway recibe nil; se espera un %s", bridge)
	}
	if got := field.Elem().Type(); got != bridge {
		t.Fatalf("el gateway recibe un %s; se espera un %s (bridge_iam.go) sobre el servicio NUEVO", got, bridge)
	}
	if !sameInstance(field.Elem().Elem().FieldByName("next"), want) {
		t.Errorf("el %s del gateway no envuelve la instancia %T del authStack", bridge, want)
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
