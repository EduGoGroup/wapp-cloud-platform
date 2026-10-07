//go:build pendiente

package tenantllm_test

// Los tests de fichero del dominio de tenantllm: los dos vocabularios con sus literales,
// ValidVia, el centinela con su texto, la forma de Config (R4.4.a: sin campo para la clave) y
// la del puerto Store. Lo que el puerto PROMETE lo afirma tenantllmhelpertest.Contrato.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

// El adaptador real cumple el puerto.
var _ tenantllm.Store = (*tenantllm.Postgres)(nil)

// TestVocabularies_Literals: los literales son los de los CHECK de la tabla
// (tenant_llm_provider_check de la 0071, tenant_llm_via_check de la 0073). Otro paquete del
// módulo duplica ViaLocal y ViaAPI y compara contra estos: no pueden moverse.
func TestVocabularies_Literals(t *testing.T) {
	cases := []struct {
		name, got, want string
	}{
		{"ProviderAnthropic", tenantllm.ProviderAnthropic, "anthropic"},
		{"ProviderGemini", tenantllm.ProviderGemini, "gemini"},
		{"ProviderLocal", tenantllm.ProviderLocal, "local"},
		{"ViaLocal", tenantllm.ViaLocal, "local"},
		{"ViaAPI", tenantllm.ViaAPI, "api"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, quería %q", c.name, c.got, c.want)
		}
	}
}

// TestValidVia: true solo para los dos literales exactos; nada de recortar, plegar mayúsculas
// o normalizar Unicode.
func TestValidVia(t *testing.T) {
	cases := []struct {
		name string
		via  string
		want bool
	}{
		{"local", tenantllm.ViaLocal, true},
		{"api", tenantllm.ViaAPI, true},
		{"empty", "", false},
		{"upper case", "API", false},
		{"mixed case", "Local", false},
		{"leading space", " local", false},
		{"trailing newline", "api\n", false},
		{"non breaking space", "api ", false},
		{"full width letters", "ａｐｉ", false},
		{"both joined", "local|api", false},
		{"unknown word", "remota", false},
		{"a provider is not a via", tenantllm.ProviderAnthropic, false},
		{"another provider is not a via", tenantllm.ProviderGemini, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := tenantllm.ValidVia(c.via); got != c.want {
				t.Errorf("ValidVia(%q) = %v, quería %v", c.via, got, c.want)
			}
		})
	}
}

// TestValidVia_ProviderLocalSharesTheLiteralOnly: ProviderLocal vale lo mismo que ViaLocal por
// coincidencia del vocabulario; como vía es válida por su literal, no porque sea un proveedor.
func TestValidVia_ProviderLocalSharesTheLiteralOnly(t *testing.T) {
	if !tenantllm.ValidVia(tenantllm.ProviderLocal) {
		t.Errorf("ValidVia(ProviderLocal) = false: el literal %q es una vía válida", tenantllm.ProviderLocal)
	}
}

// TestErrNotConfigured: es un centinela comparable con errors.Is, también envuelto, y su texto
// es exacto.
func TestErrNotConfigured(t *testing.T) {
	const want = "tenantllm: el tenant no tiene configurada la vía LLM API"
	if got := tenantllm.ErrNotConfigured.Error(); got != want {
		t.Errorf("ErrNotConfigured = %q, quería %q", got, want)
	}
	wrapped := fmt.Errorf("selector: %w", tenantllm.ErrNotConfigured)
	if !errors.Is(wrapped, tenantllm.ErrNotConfigured) {
		t.Error("errors.Is no reconoce ErrNotConfigured envuelto")
	}
}

// TestConfig_HasNoFieldForTheKey (R4.4.a): Config tiene exactamente estos ocho campos, con
// estos tipos y en este orden. Lo que no está en el struct no puede acabar en un log, en un %v
// ni en un JSON: un campo nuevo —y más uno que pueda llevar la clave— pone esto en rojo.
func TestConfig_HasNoFieldForTheKey(t *testing.T) {
	want := []struct {
		name string
		kind reflect.Type
	}{
		{"TenantID", reflect.TypeFor[string]()},
		{"Via", reflect.TypeFor[string]()},
		{"Provider", reflect.TypeFor[string]()},
		{"Model", reflect.TypeFor[string]()},
		{"HasAPIKey", reflect.TypeFor[bool]()},
		{"ConsentedAt", reflect.TypeFor[time.Time]()},
		{"CreatedAt", reflect.TypeFor[time.Time]()},
		{"UpdatedAt", reflect.TypeFor[time.Time]()},
	}
	typ := reflect.TypeFor[tenantllm.Config]()
	if typ.NumField() != len(want) {
		t.Fatalf("Config tiene %d campos, quería %d: un campo nuevo es una puerta nueva para la clave", typ.NumField(), len(want))
	}
	for i, w := range want {
		f := typ.Field(i)
		if f.Name != w.name || f.Type != w.kind {
			t.Errorf("campo %d de Config = %s %s, quería %s %s", i, f.Name, f.Type, w.name, w.kind)
		}
		lower := strings.ToLower(f.Name)
		if f.Name != "HasAPIKey" && (strings.Contains(lower, "key") || strings.Contains(lower, "secret")) {
			t.Errorf("Config.%s parece un campo para la credencial: Config cruza hacia la capa HTTP y no la lleva", f.Name)
		}
	}
}

// TestStore_Shape: el puerto tiene estos cuatro métodos y ninguno más, con estas firmas. La
// única salida de la clave es APIKey; Get no la devuelve porque Config no la lleva.
func TestStore_Shape(t *testing.T) {
	ctx := reflect.TypeFor[context.Context]()
	str, boolean := reflect.TypeFor[string](), reflect.TypeFor[bool]()
	errType := reflect.TypeFor[error]()
	cfg, instant := reflect.TypeFor[tenantllm.Config](), reflect.TypeFor[time.Time]()

	want := map[string]struct{ in, out []reflect.Type }{
		"Get":    {[]reflect.Type{ctx, str}, []reflect.Type{cfg, boolean, errType}},
		"Upsert": {[]reflect.Type{ctx, cfg, str, instant}, []reflect.Type{errType}},
		"Delete": {[]reflect.Type{ctx, str}, []reflect.Type{errType}},
		"APIKey": {[]reflect.Type{ctx, str}, []reflect.Type{str, errType}},
	}
	port := reflect.TypeFor[tenantllm.Store]()
	if port.NumMethod() != len(want) {
		t.Fatalf("Store tiene %d métodos, quería %d", port.NumMethod(), len(want))
	}
	for i := range port.NumMethod() {
		m := port.Method(i)
		w, ok := want[m.Name]
		if !ok {
			t.Errorf("Store.%s no está en el contrato", m.Name)
			continue
		}
		var in, out []reflect.Type
		for j := range m.Type.NumIn() {
			in = append(in, m.Type.In(j))
		}
		for j := range m.Type.NumOut() {
			out = append(out, m.Type.Out(j))
		}
		if !reflect.DeepEqual(in, w.in) || !reflect.DeepEqual(out, w.out) {
			t.Errorf("Store.%s = func(%v) %v, quería func(%v) %v", m.Name, in, out, w.in, w.out)
		}
	}
}
