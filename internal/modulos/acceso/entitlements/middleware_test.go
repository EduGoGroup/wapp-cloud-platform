//go:build pendiente

package entitlements_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// Este test es EXTERNO (package entitlements_test): usa entitlementshelpertest.Fake, que importa
// entitlements, y un test interno que lo importara daría un ciclo.

const (
	tenantID     = "tenant-a"
	gatedFeature = entitlements.FeatureIntakesExport

	// singleDenied es el cuerpo del 403 de RequireFeature para gatedFeature, byte a byte (R-E4).
	singleDenied = `{"error":"feature_not_enabled","feature":"intakes_export"}`
	// anyDenied es el del plural para fourFeatures, en su orden (R-E4).
	anyDenied = `{"error":"feature_not_enabled","features":["cart_basic","media","menu","survey"]}`
	// emptyDenied es el del plural sin claves: omitempty quita "features" (R-E4).
	emptyDenied = `{"error":"feature_not_enabled"}`
)

// fourFeatures son claves reales del catálogo (los tipos de fábrica): un gate probado con claves
// que no existen no dice nada del que corre en producción.
var fourFeatures = []string{
	entitlements.FeatureCartBasic, entitlements.FeatureMedia, entitlements.FeatureMenu, entitlements.FeatureSurvey,
}

// spyResolver envuelve al Fake: registra las claves por las que se pregunta y revienta en failOn.
type spyResolver struct {
	*entitlementshelpertest.Fake
	failOn string
	asked  []string
}

func (s *spyResolver) Has(ctx context.Context, tenant, feature string) (bool, error) {
	s.asked = append(s.asked, feature)
	if feature == s.failOn {
		return false, errors.New("spyResolver: esta clave no se pudo resolver")
	}
	return s.Fake.Has(ctx, tenant, feature)
}

// withTenant arma una petición que YA pasó por Authenticate: el gate lee la Identity del
// contexto, no cabeceras.
func withTenant(tenant string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/whatever", nil)
	return req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenant}))
}

// withoutIdentity es una petición sin Identity en el contexto.
func withoutIdentity() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/api/v1/whatever", nil)
}

// serve corre el middleware sobre un handler protegido y dice si ese handler llegó a ejecutarse.
func serve(mw func(http.Handler) http.Handler, req *http.Request) (*httptest.ResponseRecorder, bool) {
	var called bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	return rec, called
}

// requireDenied afirma el corte: 403, JSON, el cuerpo exacto y el handler protegido sin tocar.
func requireDenied(t *testing.T, rec *httptest.ResponseRecorder, called bool, wantBody string) {
	t.Helper()
	if called {
		t.Error("el handler protegido se ejecutó: el gate debía cortar")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("código = %d, quería 403 (nunca 500 ni pase)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, quería application/json", ct)
	}
	if got := rec.Body.String(); got != wantBody {
		t.Errorf("cuerpo = %s, quería %s", got, wantBody)
	}
}

// requirePassed afirma que el gate dejó pasar sin tocar la respuesta.
func requirePassed(t *testing.T, rec *httptest.ResponseRecorder, called bool) {
	t.Helper()
	if !called {
		t.Fatalf("el handler protegido no se ejecutó: código %d, cuerpo %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Errorf("código = %d, quería el 200 del handler protegido", rec.Code)
	}
}

// --- RequireFeature ---

func TestRequireFeature_HasFeature_Passes(t *testing.T) {
	spy := &spyResolver{Fake: entitlementshelpertest.NewFake()}
	spy.Enable(tenantID, gatedFeature)
	rec, called := serve(entitlements.RequireFeature(spy, gatedFeature), withTenant(tenantID))
	requirePassed(t, rec, called)
	if !slices.Equal(spy.asked, []string{gatedFeature}) {
		t.Errorf("se preguntó por %v, quería solo %q", spy.asked, gatedFeature)
	}
}

// TestRequireFeature_TenantComesFromIdentity: el tenant que se pregunta es el de la Identity
// (INV-8): con la feature en OTRO tenant, corta.
func TestRequireFeature_TenantComesFromIdentity(t *testing.T) {
	fake := entitlementshelpertest.NewFake()
	fake.Enable("tenant-b", gatedFeature)
	rec, called := serve(entitlements.RequireFeature(fake, gatedFeature), withTenant(tenantID))
	requireDenied(t, rec, called, singleDenied)
}

func TestRequireFeature_MissingFeature_Forbidden(t *testing.T) {
	fake := entitlementshelpertest.NewFake()
	fake.Enable(tenantID, entitlements.FeatureCartBasic)
	rec, called := serve(entitlements.RequireFeature(fake, gatedFeature), withTenant(tenantID))
	requireDenied(t, rec, called, singleDenied)
}

// TestRequireFeature_DisabledOverride_Forbidden: una feature APAGADA por override cuenta como no
// tenerla (R-E2).
func TestRequireFeature_DisabledOverride_Forbidden(t *testing.T) {
	fake := entitlementshelpertest.NewFake()
	fake.Disable(tenantID, gatedFeature)
	rec, called := serve(entitlements.RequireFeature(fake, gatedFeature), withTenant(tenantID))
	requireDenied(t, rec, called, singleDenied)
}

// TestRequireFeature_NoIdentity: sin Identity no hay tenant que consultar (R-E1). Llegar aquí sin
// identidad es un montaje mal compuesto, no un permiso. Es TestRequireFeature_SinIdentidad de la
// spec (tareas.md T2.3), en inglés por 05 E-11.
func TestRequireFeature_NoIdentity(t *testing.T) {
	spy := &spyResolver{Fake: entitlementshelpertest.NewFake()}
	spy.Enable(tenantID, gatedFeature)
	rec, called := serve(entitlements.RequireFeature(spy, gatedFeature), withoutIdentity())
	requireDenied(t, rec, called, singleDenied)
	if len(spy.asked) != 0 {
		t.Errorf("sin identidad se preguntó al Resolver por %v", spy.asked)
	}
}

func TestRequireFeature_IdentityWithoutTenant(t *testing.T) {
	spy := &spyResolver{Fake: entitlementshelpertest.NewFake()}
	spy.Enable("", gatedFeature)
	rec, called := serve(entitlements.RequireFeature(spy, gatedFeature), withTenant(""))
	requireDenied(t, rec, called, singleDenied)
	if len(spy.asked) != 0 {
		t.Errorf("con una identidad sin tenant se preguntó al Resolver por %v", spy.asked)
	}
}

// TestRequireFeature_NilResolver: un cableado incompleto no abre la capacidad ni hace panic (R-E1).
func TestRequireFeature_NilResolver(t *testing.T) {
	rec, called := serve(entitlements.RequireFeature(nil, gatedFeature), withTenant(tenantID))
	requireDenied(t, rec, called, singleDenied)
}

// TestRequireFeature_ResolverError: un fallo de infraestructura no abre ni responde 500 (R-E1),
// aunque el tenant tendría la feature si el Resolver respondiera.
func TestRequireFeature_ResolverError(t *testing.T) {
	fake := entitlementshelpertest.NewFake()
	fake.Enable(tenantID, gatedFeature)
	fake.Err = errors.New("BD caída")
	rec, called := serve(entitlements.RequireFeature(fake, gatedFeature), withTenant(tenantID))
	requireDenied(t, rec, called, singleDenied)
}

// --- RequireAnyFeature ---

// TestRequireAnyFeature_OneIsEnough: con UNA de las cuatro (la segunda), pasa (R-E3).
func TestRequireAnyFeature_OneIsEnough(t *testing.T) {
	fake := entitlementshelpertest.NewFake()
	fake.Enable(tenantID, entitlements.FeatureMedia)
	rec, called := serve(entitlements.RequireAnyFeature(fake, fourFeatures...), withTenant(tenantID))
	requirePassed(t, rec, called)
}

func TestRequireAnyFeature_NoneGranted_Forbidden(t *testing.T) {
	fake := entitlementshelpertest.NewFake()
	fake.Enable(tenantID, entitlements.FeatureCRMBridge)
	rec, called := serve(entitlements.RequireAnyFeature(fake, fourFeatures...), withTenant(tenantID))
	requireDenied(t, rec, called, anyDenied)
}

// TestRequireAnyFeature_DisabledOverride_Forbidden: apagada por override no cuenta, tampoco para
// «alguna» (R-E2).
func TestRequireAnyFeature_DisabledOverride_Forbidden(t *testing.T) {
	fake := entitlementshelpertest.NewFake()
	fake.Disable(tenantID, entitlements.FeatureMedia)
	rec, called := serve(entitlements.RequireAnyFeature(fake, fourFeatures...), withTenant(tenantID))
	requireDenied(t, rec, called, anyDenied)
}

// TestRequireAnyFeature_ErrorStopsImmediately: la primera clave falla y el tenant SÍ tiene la
// segunda. Con el corte, 403 y no se pregunta por más; con un `continue`, 200, y el mismo tenant
// con la misma BD medio caída entraría o no según el orden (R-E3).
func TestRequireAnyFeature_ErrorStopsImmediately(t *testing.T) {
	spy := &spyResolver{Fake: entitlementshelpertest.NewFake(), failOn: entitlements.FeatureCartBasic}
	spy.Enable(tenantID, entitlements.FeatureMedia)
	rec, called := serve(entitlements.RequireAnyFeature(spy, fourFeatures...), withTenant(tenantID))
	requireDenied(t, rec, called, anyDenied)
	if !slices.Equal(spy.asked, []string{entitlements.FeatureCartBasic}) {
		t.Errorf("se preguntó por %v; tras el error de la primera no debía preguntarse por más", spy.asked)
	}
}

// TestRequireAnyFeature_EmptyListDoesNotOpen: sin claves es «ninguna basta», no «todas valen»
// (R-E3), y el cuerpo no lleva "features" (R-E4).
func TestRequireAnyFeature_EmptyListDoesNotOpen(t *testing.T) {
	fake := entitlementshelpertest.NewFake()
	fake.Enable(tenantID, entitlements.FeatureSurvey)
	rec, called := serve(entitlements.RequireAnyFeature(fake), withTenant(tenantID))
	requireDenied(t, rec, called, emptyDenied)
}

func TestRequireAnyFeature_NoIdentity(t *testing.T) {
	spy := &spyResolver{Fake: entitlementshelpertest.NewFake()}
	spy.Enable(tenantID, entitlements.FeatureMedia)
	rec, called := serve(entitlements.RequireAnyFeature(spy, fourFeatures...), withoutIdentity())
	requireDenied(t, rec, called, anyDenied)
	if len(spy.asked) != 0 {
		t.Errorf("sin identidad se preguntó al Resolver por %v", spy.asked)
	}
}

func TestRequireAnyFeature_IdentityWithoutTenant(t *testing.T) {
	spy := &spyResolver{Fake: entitlementshelpertest.NewFake()}
	spy.Enable("", entitlements.FeatureMedia)
	rec, called := serve(entitlements.RequireAnyFeature(spy, fourFeatures...), withTenant(""))
	requireDenied(t, rec, called, anyDenied)
	if len(spy.asked) != 0 {
		t.Errorf("con una identidad sin tenant se preguntó al Resolver por %v", spy.asked)
	}
}

func TestRequireAnyFeature_NilResolver(t *testing.T) {
	rec, called := serve(entitlements.RequireAnyFeature(nil, fourFeatures...), withTenant(tenantID))
	requireDenied(t, rec, called, anyDenied)
}

func TestRequireAnyFeature_ResolverError(t *testing.T) {
	fake := entitlementshelpertest.NewFake()
	fake.Enable(tenantID, entitlements.FeatureMedia)
	fake.Err = errors.New("BD caída")
	rec, called := serve(entitlements.RequireAnyFeature(fake, fourFeatures...), withTenant(tenantID))
	requireDenied(t, rec, called, anyDenied)
}
