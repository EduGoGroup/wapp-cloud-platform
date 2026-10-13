package apipublica

// integrations_validate_test.go — cubre integrations_validate.go. No exporta nada, así que el
// test es interno y nació con el verde (05 E-4, P6). Las mismas reglas, vistas desde HTTP y rama
// por rama, están en integrations_put_test.go; aquí va lo que desde fuera se diagnosticaría mal:
// el veredicto de cada función suelta, el fallo de lectura del cuerpo y que validateLiveBridge
// solo consulta el almacén cuando le hace falta.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// integrationStoreStub es un IntegrationsStore mínimo: contesta GetTenantIntegration con lo que
// se le diga y cuenta las consultas. El resto del puerto no lo usa la validación.
type integrationStoreStub struct {
	IntegrationsStore
	row   integrations.TenantIntegration
	found bool
	err   error
	gets  int
}

func (s *integrationStoreStub) GetTenantIntegration(context.Context, string) (integrations.TenantIntegration, bool, error) {
	s.gets++
	return s.row, s.found, s.err
}

func TestDecodeIntegration(t *testing.T) {
	t.Run("trims_and_defaults", func(t *testing.T) {
		req, code, errBody := decodeIntegration(strings.NewReader(
			`{"catalog_adapter":" ","events_adapter":" webhook ","endpoint_url":" https://x.example.test ","secret":" s ","enabled":true}`))
		if errBody != nil || code != 0 {
			t.Fatalf("decodeIntegration rechazó un cuerpo válido: %d %v", code, errBody)
		}
		want := integrationRequest{CatalogAdapter: "local", EventsAdapter: "webhook", EndpointURL: "https://x.example.test", Secret: " s ", Enabled: true}
		if req != want {
			t.Errorf("decodeIntegration = %+v, quiero %+v", req, want)
		}
	})
	t.Run("empty_object_is_local_local_off", func(t *testing.T) {
		req, _, errBody := decodeIntegration(strings.NewReader(`{}`))
		if want := (integrationRequest{CatalogAdapter: "local", EventsAdapter: "local"}); errBody != nil || req != want {
			t.Errorf("decodeIntegration({}) = %+v (%v), quiero %+v", req, errBody, want)
		}
	})
	t.Run("read_failure_is_400", func(t *testing.T) {
		_, code, errBody := decodeIntegration(iotest.ErrReader(errors.New("conexión cortada")))
		body, ok := errBody.(map[string]string)
		if !ok || code != http.StatusBadRequest || body["error"] != "no se pudo leer el cuerpo" {
			t.Errorf("lectura fallida: %d %v, quiero 400 «no se pudo leer el cuerpo»", code, errBody)
		}
	})
	t.Run("ceiling_is_8KiB", func(t *testing.T) {
		big := `{"x":"` + strings.Repeat("a", integrationMaxBodyBytes) + `"}`
		if _, code, _ := decodeIntegration(strings.NewReader(big)); code != http.StatusRequestEntityTooLarge {
			t.Errorf("cuerpo por encima del techo: código %d, quiero 413", code)
		}
	})
}

func TestValidateIntegrationEndpoint(t *testing.T) {
	const (
		badURL = "endpoint_url debe ser una URL absoluta http(s)"
		prefix = "https://h.example.test/"
	)
	cases := []struct {
		name, raw, msg string
	}{
		{"empty_is_allowed", "", ""},
		{"https", "https://puente.example.test/wapp?x=1#f", ""},
		{"http_local_receiver", "http://localhost:9009", ""},
		{"ipv6_host", "http://[::1]:9009/hook", ""},
		{"uppercase_scheme_is_normalized", "HTTP://puente.example.test", ""},
		{"exactly_2000_bytes", prefix + strings.Repeat("a", integrationMaxEndpointLen-len(prefix)), ""},
		{"2001_bytes", prefix + strings.Repeat("a", integrationMaxEndpointLen-len(prefix)+1), "endpoint_url es demasiado larga"},
		{"too_long_wins_over_bad_scheme", "gopher://" + strings.Repeat("a", integrationMaxEndpointLen), "endpoint_url es demasiado larga"},
		{"relative", "/solo/una/ruta", badURL},
		{"host_without_scheme", "puente.example.test", badURL},
		{"host_port_without_scheme", "localhost:9009", badURL},
		{"scheme_relative", "//puente.example.test", badURL},
		{"gopher", "gopher://viejo.example.test/x", badURL},
		{"ftp", "ftp://viejo.example.test/x", badURL},
		{"file", "file:///etc/hosts", badURL},
		{"data", "data:text/plain,hola", badURL},
		{"mailto", "mailto:alguien@example.test", badURL},
		{"https_without_host", "https:///wapp", badURL},
		{"https_opaque", "https:puente.example.test", badURL},
		{"scheme_with_cyrillic_lookalike", "һttps://puente.example.test", badURL},
		{"inner_space", "https://puente example.test", badURL},
		{"newline", "https://puente.example.test/\n", badURL},
		{"bad_percent", "https://puente.example.test/%zz", badURL},
		{"just_spaces_is_not_empty_here", "   ", badURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := validateIntegrationEndpoint(tc.raw)
			if msg != tc.msg {
				t.Errorf("validateIntegrationEndpoint(%q) = %q, quiero %q", tc.raw, msg, tc.msg)
			}
			if want := map[bool]int{true: 0, false: http.StatusBadRequest}[tc.msg == ""]; code != want {
				t.Errorf("validateIntegrationEndpoint(%q): código %d, quiero %d", tc.raw, code, want)
			}
		})
	}
}

// TestValidateLiveBridge: la regla que AVISA de un puente encendido que no puede entregar.
func TestValidateLiveBridge(t *testing.T) {
	const (
		noURL       = "un puente webhook encendido necesita endpoint_url"
		needsSigner = "un puente webhook encendido necesita un secreto de firma"
		fakeValue   = "valor-ficticio-de-prueba-0003"
	)
	live := integrationRequest{CatalogAdapter: "local", EventsAdapter: "webhook", EndpointURL: "https://x.example.test", Enabled: true}
	with := func(change func(*integrationRequest)) integrationRequest {
		req := live
		change(&req)
		return req
	}
	stored := integrations.TenantIntegration{HasSecret: true}
	down := errors.New("caído")
	cases := []struct {
		name  string
		req   integrationRequest
		store integrationStoreStub
		code  int
		msg   string
		gets  int
	}{
		{"disabled_is_never_checked", with(func(r *integrationRequest) { r.Enabled = false; r.EndpointURL = "" }),
			integrationStoreStub{}, 0, "", 0},
		{"local_events_is_never_checked", with(func(r *integrationRequest) { r.EventsAdapter = "local"; r.EndpointURL = "" }),
			integrationStoreStub{}, 0, "", 0},
		{"webhook_catalog_alone_is_not_a_live_bridge", with(func(r *integrationRequest) {
			r.CatalogAdapter, r.EventsAdapter, r.EndpointURL = "webhook", "local", ""
		}), integrationStoreStub{}, 0, "", 0},
		{"no_endpoint", with(func(r *integrationRequest) { r.EndpointURL = "" }),
			integrationStoreStub{row: stored, found: true}, http.StatusBadRequest, noURL, 0},
		{"no_endpoint_even_with_secret_in_body", with(func(r *integrationRequest) { r.EndpointURL = ""; r.Secret = fakeValue }),
			integrationStoreStub{}, http.StatusBadRequest, noURL, 0},
		{"secret_in_body_needs_no_lookup", with(func(r *integrationRequest) { r.Secret = fakeValue }),
			integrationStoreStub{err: down}, 0, "", 0},
		{"stored_secret_is_enough", live, integrationStoreStub{row: stored, found: true}, 0, "", 1},
		{"no_row", live, integrationStoreStub{}, http.StatusBadRequest, needsSigner, 1},
		{"row_without_secret", live, integrationStoreStub{found: true}, http.StatusBadRequest, needsSigner, 1},
		{"store_fails", live, integrationStoreStub{err: down}, http.StatusInternalServerError, "no se pudo comprobar la integración actual", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.store
			code, msg := validateLiveBridge(context.Background(), &store, "tenant", tc.req)
			if code != tc.code || msg != tc.msg {
				t.Errorf("validateLiveBridge = (%d, %q), quiero (%d, %q)", code, msg, tc.code, tc.msg)
			}
			if store.gets != tc.gets {
				t.Errorf("consultó el almacén %d veces, quiero %d", store.gets, tc.gets)
			}
		})
	}
}

// TestValidateIntegration_Order: lo del vocabulario va antes que lo que consulta el estado, y el
// primer defecto decide.
func TestValidateIntegration_Order(t *testing.T) {
	store := &integrationStoreStub{err: errors.New("caído")}
	req := integrationRequest{CatalogAdapter: "http", EventsAdapter: "kafka", EndpointURL: "nada", Secret: "corto", Enabled: true}
	steps := []struct {
		fix  func(*integrationRequest)
		code int
		msg  string
	}{
		{func(*integrationRequest) {}, http.StatusUnprocessableEntity,
			"catalog.pull diferido: el adaptador de catálogo «http» todavía no está implementado; usa «local»"},
		{func(r *integrationRequest) { r.CatalogAdapter = "otro" }, http.StatusBadRequest, "catalog_adapter debe ser «local» o «webhook»"},
		{func(r *integrationRequest) { r.CatalogAdapter = "local" }, http.StatusBadRequest, "events_adapter debe ser «local» o «webhook»"},
		{func(r *integrationRequest) { r.EventsAdapter = "webhook" }, http.StatusBadRequest, "endpoint_url debe ser una URL absoluta http(s)"},
		{func(r *integrationRequest) { r.EndpointURL = "https://x.example.test" }, http.StatusBadRequest,
			"el secreto de firma debe tener entre 24 y 256 caracteres"},
		{func(r *integrationRequest) { r.Secret = "" }, http.StatusInternalServerError, "no se pudo comprobar la integración actual"},
	}
	for i, step := range steps {
		step.fix(&req)
		if code, msg := validateIntegration(context.Background(), store, "tenant", req); code != step.code || msg != step.msg {
			t.Errorf("paso %d: validateIntegration = (%d, %q), quiero (%d, %q)", i, code, msg, step.code, step.msg)
		}
	}
	if store.gets != 1 {
		t.Errorf("el almacén se consultó %d veces: solo la última regla lo necesita", store.gets)
	}
}
