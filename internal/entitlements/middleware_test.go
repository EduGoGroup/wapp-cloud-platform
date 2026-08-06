package entitlements

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

const featureGateada = "intakes_export"

// nextOK es el handler protegido: si responde 200 con esta marca, el gate dejó
// pasar. Cualquier otra cosa significa que cortó.
func nextOK(llamado *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*llamado = true
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("protegido")); err != nil {
			return
		}
	})
}

// requestConTenant arma una petición que YA pasó por Authenticate (el middleware
// se compone después: lee la Identity del contexto, no cabeceras).
func requestConTenant(tenantID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/lo-que-sea", nil)
	if tenantID == "" {
		return req
	}
	return req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenantID}))
}

// ejecuta corre el middleware sobre nextOK y devuelve la respuesta y si el
// handler protegido llegó a ejecutarse.
func ejecuta(t *testing.T, resolver Resolver, req *http.Request) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	var llamado bool
	rec := httptest.NewRecorder()
	RequireFeature(resolver, featureGateada)(nextOK(&llamado)).ServeHTTP(rec, req)
	return rec, llamado
}

// exigeDenegado comprueba el corte: 403 con el cuerpo EXACTO del contrato
// (design §D-040.5) y sin haber tocado el handler protegido.
func exigeDenegado(t *testing.T, rec *httptest.ResponseRecorder, llamado bool) {
	t.Helper()
	if llamado {
		t.Fatal("el handler protegido NO debió ejecutarse")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quería 403; body=%s", rec.Code, rec.Body.String())
	}
	want := `{"error":"feature_not_enabled","feature":"` + featureGateada + `"}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body=%s, quería %s", got, want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type=%q, quería application/json", ct)
	}
}

func TestRequireFeature_FeaturePresente_Pasa(t *testing.T) {
	f := NewFake()
	f.Enable("tenant-a", featureGateada)

	rec, llamado := ejecuta(t, f, requestConTenant("tenant-a"))
	if !llamado {
		t.Fatal("con la feature habilitada el handler protegido debía ejecutarse")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quería 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequireFeature_FeatureAusente_403(t *testing.T) {
	f := NewFake()
	f.Enable("tenant-a", "otra_feature")

	rec, llamado := ejecuta(t, f, requestConTenant("tenant-a"))
	exigeDenegado(t, rec, llamado)
}

// TestRequireFeature_OverrideApagado_403: la feature está declarada pero APAGADA
// por override (enabled=false) ⇒ corta igual que si no existiera.
func TestRequireFeature_OverrideApagado_403(t *testing.T) {
	f := NewFake()
	f.Disable("tenant-a", featureGateada)

	rec, llamado := ejecuta(t, f, requestConTenant("tenant-a"))
	exigeDenegado(t, rec, llamado)
}

// TestRequireFeature_ResolverCaido_FailClosed: un fallo de infraestructura NO
// abre la capacidad ni responde 500 — corta con el mismo 403 (fail-closed).
func TestRequireFeature_ResolverCaido_FailClosed(t *testing.T) {
	f := &Fake{Err: errors.New("BD caída")}
	// El tenant SÍ tendría la feature si el resolver respondiera.
	f.Enable("tenant-a", featureGateada)

	rec, llamado := ejecuta(t, f, requestConTenant("tenant-a"))
	exigeDenegado(t, rec, llamado)
}

// TestRequireFeature_SinIdentidad_FailClosed: sin Identity en el contexto no hay
// tenant que consultar ⇒ no se concede (el middleware va DESPUÉS de autenticar;
// llegar aquí sin identidad es un montaje mal compuesto, no un permiso).
func TestRequireFeature_SinIdentidad_FailClosed(t *testing.T) {
	f := NewFake()
	f.Enable("tenant-a", featureGateada)

	rec, llamado := ejecuta(t, f, requestConTenant(""))
	exigeDenegado(t, rec, llamado)
}

// TestRequireFeature_ResolverNil_FailClosed: un cableado incompleto tampoco abre
// la capacidad (ni provoca un panic por nil).
func TestRequireFeature_ResolverNil_FailClosed(t *testing.T) {
	rec, llamado := ejecuta(t, nil, requestConTenant("tenant-a"))
	exigeDenegado(t, rec, llamado)
}
