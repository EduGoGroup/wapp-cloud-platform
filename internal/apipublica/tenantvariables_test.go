package apipublica_test

// tenantvariables_test.go — cubre el contrato de tenantvariables.go (TenantVariableStore,
// TenantVariablesDeps, MountTenantVariables): G11 y G12.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
)

const (
	tenantVarTarget     = "/api/v1/tenant-variables"
	tenantVarPatternGet = "GET /api/v1/tenant-variables"
	tenantVarPatternPut = "PUT /api/v1/tenant-variables"
	tenantVarReadPerm   = "content.read"
	tenantVarWritePerm  = "content.write"
	tenantVarMaxBody    = 1 << 18

	tenantVarMsgTimeout   = "la lectura de las variables no respondió a tiempo, reintenta"
	tenantVarMsgReadFail  = "no se pudieron leer las variables"
	tenantVarMsgShape     = `el cuerpo debe ser un JSON {"variables":{clave:valor}} de cadenas`
	tenantVarMsgMissing   = "falta el objeto variables (usa {} para dejar el tenant sin variables)"
	tenantVarMsgTooMany   = "demasiadas variables"
	tenantVarMsgEmptyKey  = "hay una clave vacía"
	tenantVarMsgLongKey   = "hay una clave demasiado larga"
	tenantVarMsgSaveFail  = "no se pudieron guardar las variables"
	tenantVarMsgRereadErr = "variables guardadas, pero no se pudieron releer"
)

// El puerto de G11–G12 lo cumplen las piezas REALES del módulo solicitudes nuevo.
var (
	_ apipublica.TenantVariableStore = (*tenantvars.Postgres)(nil)
	_ apipublica.TenantVariableStore = (*tenantvars.MemoryStore)(nil)
	_ apipublica.TenantVariableStore = tenantvars.Store(nil)
)

// tenantVarStoreSpy es TenantVariableStore sobre el doble del módulo: delega en él y apunta
// cuántas veces se llamó cada método, con qué tenant y con qué plazo (-1 = sin plazo). Puede
// fallar a la carta (listErr, replaceErr) o no contestar (block: espera a que el contexto muera,
// sin time.Sleep).
type tenantVarStoreSpy struct {
	*tenantvars.MemoryStore
	listErr    error
	replaceErr error
	block      bool

	lists, replaces  int
	tenant           string
	listRemaining    time.Duration
	replaceRemaining time.Duration
}

var _ apipublica.TenantVariableStore = (*tenantVarStoreSpy)(nil)

func newTenantVarStore() *tenantVarStoreSpy {
	return &tenantVarStoreSpy{MemoryStore: tenantvars.NewMemoryStore()}
}

// tenantVarRemaining devuelve lo que le queda al contexto, o -1 si no tiene plazo.
func tenantVarRemaining(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl)
	}
	return -1
}

func (s *tenantVarStoreSpy) List(ctx context.Context, tenantID string) ([]tenantvars.Variable, error) {
	s.lists++
	s.tenant, s.listRemaining = tenantID, tenantVarRemaining(ctx)
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.MemoryStore.List(ctx, tenantID)
}

func (s *tenantVarStoreSpy) Replace(ctx context.Context, tenantID string, vars map[string]string) error {
	s.replaces++
	s.tenant, s.replaceRemaining = tenantID, tenantVarRemaining(ctx)
	if s.replaceErr != nil {
		return s.replaceErr
	}
	return s.MemoryStore.Replace(ctx, tenantID, vars)
}

// tenantVarCara monta G11–G12 con k y d.
func tenantVarCara(k apipublica.Common, d apipublica.TenantVariablesDeps) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountTenantVariables(c, k, d)
	return c
}

// tenantVarSetup es el montaje habitual: arnés, almacén espía y la cara.
func tenantVarSetup(t *testing.T) (*apipublicahelpertest.Harness, *tenantVarStoreSpy, *apipublica.Cara) {
	t.Helper()
	h := apipublicahelpertest.New(t)
	store := newTenantVarStore()
	return h, store, tenantVarCara(h.Common(), apipublica.TenantVariablesDeps{TenantVariables: store})
}

// tenantVarSeed siembra el conjunto de un tenant por el doble, sin pasar por la cara.
func tenantVarSeed(t *testing.T, store *tenantVarStoreSpy, tenant string, vars map[string]string) {
	t.Helper()
	if err := store.MemoryStore.Replace(context.Background(), tenant, vars); err != nil {
		t.Fatalf("sembrando las variables de %s: %v", tenant, err)
	}
}

// tenantVarStored lee lo guardado de un tenant por el doble, como mapa.
func tenantVarStored(t *testing.T, store *tenantVarStoreSpy, tenant string) map[string]string {
	t.Helper()
	vars, err := store.MemoryStore.List(context.Background(), tenant)
	if err != nil {
		t.Fatalf("leyendo las variables de %s: %v", tenant, err)
	}
	out := map[string]string{}
	for _, v := range vars {
		out[v.Key] = v.Value
	}
	return out
}

func TestMountTenantVariables_Chain(t *testing.T) {
	h, _, cara := tenantVarSetup(t)
	wantPatterns(t, "G11–G12", cara, []string{tenantVarPatternGet, tenantVarPatternPut})
	checkChain(t, h, cara, routeCase{id: "G11", method: http.MethodGet, target: tenantVarTarget,
		perm: tenantVarReadPerm, want: http.StatusOK})
	checkChain(t, h, cara, routeCase{id: "G12", method: http.MethodPut, target: tenantVarTarget, body: `{"variables":{}}`,
		perm: tenantVarWritePerm, resource: "tenant_variables", want: http.StatusOK})
}

// TestMountTenantVariables_WithoutStoreIs404: sin almacén no existe NINGUNA de las dos rutas
// (T-2, T-11): 404 y no 405, y no 500.
func TestMountTenantVariables_WithoutStoreIs404(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := tenantVarCara(h.Common(), apipublica.TenantVariablesDeps{DBTimeout: time.Second})
	wantPatterns(t, "sin almacén", cara, nil)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rec := h.Call(cara, h.With(tenantA, "content.*"), method, tenantVarTarget, `{"variables":{}}`)
		wantCode(t, method+" sin almacén", rec, http.StatusNotFound)
	}
}

func TestMountTenantVariables_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		apipublica.MountTenantVariables(apipublica.Nueva(), apipublica.Common{},
			apipublica.TenantVariablesDeps{TenantVariables: newTenantVarStore()})
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountTenantVariables") {
		t.Errorf("MountTenantVariables con MW nil: panic = %v; quiero un panic de cableado que nombre MountTenantVariables", v)
	}
	// Sin almacén no hay ruta que encadenar: MW nil no es un fallo.
	if v := recuperar(func() {
		apipublica.MountTenantVariables(apipublica.Nueva(), apipublica.Common{}, apipublica.TenantVariablesDeps{})
	}); v != nil {
		t.Errorf("MountTenantVariables sin almacén y con MW nil: panic = %v; quiero que no monte nada y no falle", v)
	}
}

// TestMountTenantVariables_ScopesSplitReadFromWrite: son capa técnica, sin gate de feature (el
// Mount ni siquiera recibe un resolver), y cada scope alcanza solo a su verbo: el viewer
// (`*.read`) lee y no escribe.
func TestMountTenantVariables_ScopesSplitReadFromWrite(t *testing.T) {
	h, store, cara := tenantVarSetup(t)
	reader := h.With(tenantA, "*.read")
	wantCode(t, "viewer lee", h.Call(cara, reader, http.MethodGet, tenantVarTarget, ""), http.StatusOK)
	rec := h.Call(cara, reader, http.MethodPut, tenantVarTarget, `{"variables":{"moneda":"Bs"}}`)
	wantCode(t, "viewer escribe", rec, http.StatusForbidden)
	if store.replaces != 0 {
		t.Errorf("un token sin content.write llegó a Replace %d veces, quiero 0", store.replaces)
	}
}

// TestMountTenantVariables_GetEmptyIsAnObject: sin variables —también si el puerto devuelve
// nil— responde el mapa vacío, sin updated_at, y no un 404.
func TestMountTenantVariables_GetEmptyIsAnObject(t *testing.T) {
	h, _, cara := tenantVarSetup(t)
	rec := h.Call(cara, h.With(tenantA, tenantVarReadPerm), http.MethodGet, tenantVarTarget, "")
	wantCode(t, "G11 sin variables", rec, http.StatusOK)
	wantExactBody(t, "G11 sin variables", rec, `{"variables":{}}`)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("G11: Content-Type %q, quiero application/json", ct)
	}
}

// TestMountTenantVariables_GetBody: las variables VERBATIM y el updated_at MÁS reciente del
// conjunto, en UTC aunque el puerto lo dé en otro huso.
func TestMountTenantVariables_GetBody(t *testing.T) {
	h, store, cara := tenantVarSetup(t)
	zone := time.FixedZone("-03", -3*3600)
	clock := time.Date(2026, 9, 1, 9, 0, 0, 0, zone)
	store.SetClock(func() time.Time { return clock })
	tenantVarSeed(t, store, tenantA, map[string]string{"moneda": "Bs", "vacía": ""})
	// Solo cambia `moneda`: su marca avanza y la de `vacía` se queda atrás.
	clock = clock.Add(2 * time.Hour)
	tenantVarSeed(t, store, tenantA, map[string]string{"moneda": " $ ", "vacía": ""})

	rec := h.Call(cara, h.With(tenantA, tenantVarReadPerm), http.MethodGet, tenantVarTarget, "")
	wantCode(t, "G11", rec, http.StatusOK)
	wantExactBody(t, "G11", rec, `{"variables":{"moneda":" $ ","vacía":""},"updated_at":"2026-09-01T14:00:00Z"}`)
}

// TestMountTenantVariables_TenantComesFromTheToken (INV-8): cada tenant lee y escribe SOLO lo
// suyo; ni la query ni un campo del cuerpo cambian de quién es la operación.
func TestMountTenantVariables_TenantComesFromTheToken(t *testing.T) {
	h, store, cara := tenantVarSetup(t)
	tenantVarSeed(t, store, tenantA, map[string]string{"moneda": "Bs"})
	tenantVarSeed(t, store, tenantB, map[string]string{"moneda": "USD", "iva": "13"})

	rec := h.Call(cara, h.With(tenantA, tenantVarReadPerm), http.MethodGet, tenantVarTarget+"?tenant_id="+tenantB, "")
	var got struct {
		Variables map[string]string `json:"variables"`
	}
	wantJSON(t, "G11 de tenantA", rec, &got)
	if !maps.Equal(got.Variables, map[string]string{"moneda": "Bs"}) {
		t.Errorf("tenantA leyó %v, quiero solo lo suyo", got.Variables)
	}

	body := `{"tenant_id":"` + tenantB + `","variables":{"moneda":"EUR"}}`
	rec = h.Call(cara, h.With(tenantA, tenantVarWritePerm), http.MethodPut, tenantVarTarget+"?tenant_id="+tenantB, body)
	wantCode(t, "G12 de tenantA", rec, http.StatusOK)
	if store.tenant != tenantA {
		t.Errorf("el puerto recibió el tenant %q, quiero el del token %q", store.tenant, tenantA)
	}
	if got := tenantVarStored(t, store, tenantB); !maps.Equal(got, map[string]string{"moneda": "USD", "iva": "13"}) {
		t.Errorf("el PUT de tenantA tocó a tenantB: %v", got)
	}
	if got := tenantVarStored(t, store, tenantA); !maps.Equal(got, map[string]string{"moneda": "EUR"}) {
		t.Errorf("tenantA quedó con %v, quiero {moneda: EUR}", got)
	}
}

// TestMountTenantVariables_GetDBTimeout: G11 es la única ruta de solicitudes con plazo de BD.
func TestMountTenantVariables_GetDBTimeout(t *testing.T) {
	for name, tc := range map[string]struct{ wired, floor, ceil time.Duration }{
		"zero_falls_to_1500ms":     {0, time.Second, 1500 * time.Millisecond},
		"negative_falls_to_1500ms": {-time.Second, time.Second, 1500 * time.Millisecond},
		"wired_timeout":            {5 * time.Second, 4 * time.Second, 5 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store := newTenantVarStore()
			cara := tenantVarCara(h.Common(), apipublica.TenantVariablesDeps{TenantVariables: store, DBTimeout: tc.wired})
			wantCode(t, name, h.Call(cara, h.With(tenantA, tenantVarReadPerm), http.MethodGet, tenantVarTarget, ""), http.StatusOK)
			if store.listRemaining <= tc.floor || store.listRemaining > tc.ceil {
				t.Errorf("al contexto de List le quedaban %s, quiero entre %s y %s (-1 = sin plazo)", store.listRemaining, tc.floor, tc.ceil)
			}
		})
	}
	t.Run("store_that_does_not_answer_is_504", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		store := newTenantVarStore()
		store.block = true
		cara := tenantVarCara(h.Common(), apipublica.TenantVariablesDeps{TenantVariables: store, DBTimeout: 20 * time.Millisecond})
		rec := h.Call(cara, h.With(tenantA, tenantVarReadPerm), http.MethodGet, tenantVarTarget, "")
		wantCode(t, "plazo vencido", rec, http.StatusGatewayTimeout)
		wantErrorBody(t, "plazo vencido", rec, tenantVarMsgTimeout)
		var warned bool
		for _, e := range h.Log().Entries() {
			if e.Level == "warn" && e.Msg == "lectura a BD vencida: se responde 504" {
				warned = e.Fields["op"] == "tenant_variables.list" && e.Fields["tenant_id"] == tenantA
			}
		}
		if !warned {
			t.Errorf("el 504 no dejó el Warn con op=tenant_variables.list y tenant_id: %+v", h.Log().Entries())
		}
	})
	t.Run("nil_log_still_answers_504", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		store := newTenantVarStore()
		store.block = true
		cara := tenantVarCara(apipublica.Common{MW: h.MW()}, apipublica.TenantVariablesDeps{TenantVariables: store, DBTimeout: 20 * time.Millisecond})
		wantCode(t, "plazo vencido sin logger", h.Call(cara, h.With(tenantA, tenantVarReadPerm), http.MethodGet, tenantVarTarget, ""),
			http.StatusGatewayTimeout)
	})
}

// TestMountTenantVariables_GetStoreErrorIs500: el 500 no repite el error del puerto.
func TestMountTenantVariables_GetStoreErrorIs500(t *testing.T) {
	h, store, cara := tenantVarSetup(t)
	store.listErr = errors.New("postgres://usuario:ficticio@host/bd: conexión rechazada")
	rec := h.Call(cara, h.With(tenantA, tenantVarReadPerm), http.MethodGet, tenantVarTarget, "")
	wantCode(t, "G11 con el puerto caído", rec, http.StatusInternalServerError)
	wantErrorBody(t, "G11 con el puerto caído", rec, tenantVarMsgReadFail)
}

// TestMountTenantVariables_PutReplacesTheWholeSet: el PUT es la foto completa (lo que no viene
// se borra), responde el conjunto resultante en la forma de G11, y {} deja al tenant sin nada.
func TestMountTenantVariables_PutReplacesTheWholeSet(t *testing.T) {
	h, store, cara := tenantVarSetup(t)
	store.SetClock(func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) })
	tenantVarSeed(t, store, tenantA, map[string]string{"moneda": "Bs", "iva": "13"})
	token := h.With(tenantA, tenantVarWritePerm)

	rec := h.Call(cara, token, http.MethodPut, tenantVarTarget, `{"variables":{"moneda":"USD","horario":"9 a 18"}}`)
	wantCode(t, "G12", rec, http.StatusOK)
	wantExactBody(t, "G12", rec, `{"variables":{"horario":"9 a 18","moneda":"USD"},"updated_at":"2026-09-01T12:00:00Z"}`)
	if got := tenantVarStored(t, store, tenantA); !maps.Equal(got, map[string]string{"moneda": "USD", "horario": "9 a 18"}) {
		t.Errorf("quedó guardado %v: `iva` no venía y tenía que borrarse", got)
	}

	rec = h.Call(cara, token, http.MethodPut, tenantVarTarget, `{"variables":{}}`)
	wantCode(t, "G12 vacío", rec, http.StatusOK)
	wantExactBody(t, "G12 vacío", rec, `{"variables":{}}`)
	if got := tenantVarStored(t, store, tenantA); len(got) != 0 {
		t.Errorf("tras {\"variables\":{}} quedan %v, quiero ninguna", got)
	}
}

// TestMountTenantVariables_PutDoesNotInterpret (D-041.1): ni claves ni valores se normalizan.
// Con entradas adversarias: Unicode, espacios, mayúsculas, clave repetida, valor null.
func TestMountTenantVariables_PutDoesNotInterpret(t *testing.T) {
	key200 := strings.Repeat("k", 200)
	cases := []struct {
		name string
		body string
		want map[string]string
	}{
		{"unicode_and_spaces_verbatim", `{"variables":{" Año fiscal ":" 2026 ","موعد":"٣","emoji 🧾":"✓"}}`,
			map[string]string{" Año fiscal ": " 2026 ", "موعد": "٣", "emoji 🧾": "✓"}},
		{"case_is_not_folded", `{"variables":{"Moneda":"A","moneda":"b"}}`, map[string]string{"Moneda": "A", "moneda": "b"}},
		{"duplicate_key_last_wins", `{"variables":{"moneda":"Bs","moneda":"USD"}}`, map[string]string{"moneda": "USD"}},
		{"null_value_is_empty_string", `{"variables":{"nota":null}}`, map[string]string{"nota": ""}},
		{"value_that_looks_like_json_stays_a_string", `{"variables":{"plantilla":"{\"a\":1}"}}`, map[string]string{"plantilla": `{"a":1}`}},
		{"key_of_exactly_200_bytes", `{"variables":{"` + key200 + `":"x"}}`, map[string]string{key200: "x"}},
		{"extra_field_is_ignored", `{"otra":1,"variables":{"a":"b"}}`, map[string]string{"a": "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, cara := tenantVarSetup(t)
			rec := h.Call(cara, h.With(tenantA, tenantVarWritePerm), http.MethodPut, tenantVarTarget, tc.body)
			wantCode(t, tc.name, rec, http.StatusOK)
			if got := tenantVarStored(t, store, tenantA); !maps.Equal(got, tc.want) {
				t.Errorf("%s: quedó guardado %q, quiero %q", tc.name, got, tc.want)
			}
		})
	}
}

// tenantVarManyKeys arma un cuerpo con n claves distintas.
func tenantVarManyKeys(n int) string {
	var b strings.Builder
	b.WriteString(`{"variables":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%d":"v"`, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

// TestMountTenantVariables_PutRejectsBadShape: lo único que se valida es la FORMA, y un cuerpo
// rechazado no toca lo guardado.
func TestMountTenantVariables_PutRejectsBadShape(t *testing.T) {
	cases := []struct {
		name string
		body string
		msg  string
	}{
		{"not_json", `esto no es json`, tenantVarMsgShape},
		{"empty_body", ``, tenantVarMsgShape},
		{"trailing_garbage", `{"variables":{}} basura`, tenantVarMsgShape},
		{"two_objects", `{"variables":{}}{"variables":{}}`, tenantVarMsgShape},
		{"number_value", `{"variables":{"moneda":42}}`, tenantVarMsgShape},
		{"nested_value", `{"variables":{"moneda":{"a":"b"}}}`, tenantVarMsgShape},
		{"variables_is_an_array", `{"variables":["a"]}`, tenantVarMsgShape},
		{"variables_is_a_string", `{"variables":"a"}`, tenantVarMsgShape},
		{"missing_variables", `{}`, tenantVarMsgMissing},
		{"null_variables", `{"variables":null}`, tenantVarMsgMissing},
		{"json_null", `null`, tenantVarMsgMissing},
		{"empty_key", `{"variables":{"":"x"}}`, tenantVarMsgEmptyKey},
		{"key_of_201_bytes", `{"variables":{"` + strings.Repeat("k", 201) + `":"x"}}`, tenantVarMsgLongKey},
		{"key_of_101_runes_but_202_bytes", `{"variables":{"` + strings.Repeat("ñ", 101) + `":"x"}}`, tenantVarMsgLongKey},
		{"501_keys", tenantVarManyKeys(501), tenantVarMsgTooMany},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, cara := tenantVarSetup(t)
			tenantVarSeed(t, store, tenantA, map[string]string{"moneda": "Bs"})
			rec := h.Call(cara, h.With(tenantA, tenantVarWritePerm), http.MethodPut, tenantVarTarget, tc.body)
			wantCode(t, tc.name, rec, http.StatusBadRequest)
			wantErrorBody(t, tc.name, rec, tc.msg)
			if store.replaces != 0 {
				t.Errorf("%s: un cuerpo rechazado llegó a Replace %d veces", tc.name, store.replaces)
			}
			if got := tenantVarStored(t, store, tenantA); !maps.Equal(got, map[string]string{"moneda": "Bs"}) {
				t.Errorf("%s: un cuerpo rechazado alteró el conjunto: %v", tc.name, got)
			}
		})
	}
	t.Run("500_keys_are_accepted", func(t *testing.T) {
		h, store, cara := tenantVarSetup(t)
		rec := h.Call(cara, h.With(tenantA, tenantVarWritePerm), http.MethodPut, tenantVarTarget, tenantVarManyKeys(500))
		wantCode(t, "500 claves", rec, http.StatusOK)
		if n := len(tenantVarStored(t, store, tenantA)); n != 500 {
			t.Errorf("quedaron %d variables, quiero 500", n)
		}
	})
}

// tenantVarBodyOfSize arma un cuerpo VÁLIDO de exactamente n bytes, rellenando el valor.
func tenantVarBodyOfSize(n int) string {
	const head, tail = `{"variables":{"k":"`, `"}}`
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}

// TestMountTenantVariables_PutBodyCeiling: 256 KiB justos entran; un byte más es el 413 con
// max_bytes, y no se guarda nada.
func TestMountTenantVariables_PutBodyCeiling(t *testing.T) {
	h, store, cara := tenantVarSetup(t)
	token := h.With(tenantA, tenantVarWritePerm)

	rec := h.Call(cara, token, http.MethodPut, tenantVarTarget, tenantVarBodyOfSize(tenantVarMaxBody+1))
	wantCode(t, "256 KiB + 1", rec, http.StatusRequestEntityTooLarge)
	wantExactBody(t, "256 KiB + 1", rec, `{"error":"el cuerpo excede el tamaño máximo de 262144 bytes","max_bytes":262144}`)
	if store.replaces != 0 {
		t.Errorf("el cuerpo excesivo llegó a Replace %d veces", store.replaces)
	}

	rec = h.Call(cara, token, http.MethodPut, tenantVarTarget, tenantVarBodyOfSize(tenantVarMaxBody))
	wantCode(t, "256 KiB justos", rec, http.StatusOK)
}

// TestMountTenantVariables_PutStoreErrors: los dos 500 de G12 se distinguen (no guardó / guardó
// y no pudo releer) y ninguno repite el error del puerto.
func TestMountTenantVariables_PutStoreErrors(t *testing.T) {
	boom := errors.New("postgres://usuario:ficticio@host/bd: conexión rechazada")
	t.Run("replace_fails", func(t *testing.T) {
		h, store, cara := tenantVarSetup(t)
		store.replaceErr = boom
		rec := h.Call(cara, h.With(tenantA, tenantVarWritePerm), http.MethodPut, tenantVarTarget, `{"variables":{"a":"b"}}`)
		wantCode(t, "Replace falla", rec, http.StatusInternalServerError)
		wantErrorBody(t, "Replace falla", rec, tenantVarMsgSaveFail)
		if store.lists != 0 {
			t.Errorf("tras fallar Replace se releyó %d veces, quiero 0", store.lists)
		}
	})
	t.Run("reread_fails", func(t *testing.T) {
		h, store, cara := tenantVarSetup(t)
		store.listErr = boom
		rec := h.Call(cara, h.With(tenantA, tenantVarWritePerm), http.MethodPut, tenantVarTarget, `{"variables":{"a":"b"}}`)
		wantCode(t, "la relectura falla", rec, http.StatusInternalServerError)
		wantErrorBody(t, "la relectura falla", rec, tenantVarMsgRereadErr)
		if got := tenantVarStored(t, store, tenantA); !maps.Equal(got, map[string]string{"a": "b"}) {
			t.Errorf("el 500 de la relectura dice «guardadas», pero quedó %v", got)
		}
		records := h.Auditor().Records()
		if len(records) != 1 || records[0].Result != "failure" || records[0].Resource != "tenant_variables" {
			t.Errorf("el 500 de G12 debe quedar auditado como failure sobre tenant_variables: %+v", records)
		}
	})
}

// TestMountTenantVariables_PutHasNoDBTimeout: ni Replace ni la relectura llevan el plazo de
// G11, aunque DBTimeout esté cableado.
func TestMountTenantVariables_PutHasNoDBTimeout(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store := newTenantVarStore()
	cara := tenantVarCara(h.Common(), apipublica.TenantVariablesDeps{TenantVariables: store, DBTimeout: 5 * time.Second})
	rec := h.Call(cara, h.With(tenantA, tenantVarWritePerm), http.MethodPut, tenantVarTarget, `{"variables":{"a":"b"}}`)
	wantCode(t, "G12", rec, http.StatusOK)
	if store.replaceRemaining != -1 || store.listRemaining != -1 {
		t.Errorf("G12 puso plazo a la BD (Replace %s, relectura %s); quiero -1 en los dos: el plazo es solo de G11",
			store.replaceRemaining, store.listRemaining)
	}
}

// TestMountTenantVariables_AuditCarriesNoContent: la bitácora de G12 lleva la acción, jamás
// claves ni valores.
func TestMountTenantVariables_AuditCarriesNoContent(t *testing.T) {
	h, _, cara := tenantVarSetup(t)
	rec := h.Call(cara, h.With(tenantA, tenantVarWritePerm), http.MethodPut, tenantVarTarget,
		`{"variables":{"clave-centinela":"valor-centinela"}}`)
	wantCode(t, "G12", rec, http.StatusOK)
	audited := fmt.Sprintf("%+v", h.Auditor().Records())
	logged := fmt.Sprintf("%+v", h.Log().Entries())
	for _, leak := range []string{"clave-centinela", "valor-centinela"} {
		if strings.Contains(audited, leak) || strings.Contains(logged, leak) {
			t.Errorf("%q salió por la auditoría o por el log:\n%s\n%s", leak, audited, logged)
		}
	}
}
