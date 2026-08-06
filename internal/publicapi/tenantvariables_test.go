package publicapi_test

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantvars"
)

// tenantVariablesDTO espeja el contrato del wire (GET y respuesta del PUT).
type tenantVariablesWire struct {
	Variables map[string]string `json:"variables"`
	UpdatedAt string            `json:"updated_at"`
}

// varsAPI arma la API con un store de variables en memoria. Reusa las
// credenciales keyAContent/keyBContent de apiKeys(): las variables de empresa
// comparten scope con tenant-content (content.read/content.write) a propósito, y
// que el MISMO portador entre a las dos rutas es justamente lo que se afirma.
func varsAPI() (*testAPI, *tenantvars.MemoryStore) {
	store := tenantvars.NewMemoryStore()
	return newAPI(publicapi.Deps{TenantVariables: store}, apiKeys()), store
}

func putVars(t *testing.T, api *testAPI, credential, body string) *tenantVariablesWire {
	t.Helper()
	rec := call(api, credential, http.MethodPut, "/api/v1/tenant-variables", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	return decodeVars(t, rec.Body.Bytes())
}

func getVars(t *testing.T, api *testAPI, credential string) *tenantVariablesWire {
	t.Helper()
	rec := call(api, credential, http.MethodGet, "/api/v1/tenant-variables", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	return decodeVars(t, rec.Body.Bytes())
}

func decodeVars(t *testing.T, body []byte) *tenantVariablesWire {
	t.Helper()
	var out tenantVariablesWire
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal de variables: %v; body=%s", err, body)
	}
	return &out
}

// TestTenantVariables_Roundtrip_Verbatim es el corazón de D-041.1: wApp NO
// interpreta claves ni valores. Van de ida y vuelta un valor con acentos y signos
// castellanos, uno con espacios en los bordes y uno que es un JSON serializado
// DENTRO de una cadena. Si algo del camino "entendiera" los valores —normalizando
// tildes, recortando espacios, reparseando el JSON— este test lo delata.
func TestTenantVariables_Roundtrip_Verbatim(t *testing.T) {
	api, _ := varsAPI()

	quiero := map[string]string{
		"moneda":          "Bs",
		"saludo":          "¡Hola! ¿Qué tal? — Panadería Ñandú",
		"aviso":           "   dos espacios a cada lado   ",
		"config_externa":  `{"a":1,"b":["x","y"],"c":null}`,
		"linea_en_blanco": "",
	}
	cuerpo, err := json.Marshal(map[string]any{"variables": quiero})
	if err != nil {
		t.Fatalf("armando el cuerpo: %v", err)
	}

	// La respuesta del PUT ya es el estado resultante...
	if got := putVars(t, api, keyAContent, string(cuerpo)); !maps.Equal(got.Variables, quiero) {
		t.Fatalf("PUT devolvió %#v, quiero %#v", got.Variables, quiero)
	}
	// ...y el GET posterior devuelve lo MISMO, carácter a carácter.
	got := getVars(t, api, keyAContent)
	if !maps.Equal(got.Variables, quiero) {
		t.Fatalf("GET devolvió %#v, quiero %#v", got.Variables, quiero)
	}
	if got.UpdatedAt == "" {
		t.Fatalf("con variables guardadas el updated_at no puede venir vacío: %#v", got)
	}
}

// TestTenantVariables_Put_ReemplazaElConjunto fija la semántica decidida: el PUT
// manda la foto completa, así que lo que no viene se BORRA (no hay merge). Es la
// única forma de quitar una variable con el contrato GET/PUT del diseño.
func TestTenantVariables_Put_ReemplazaElConjunto(t *testing.T) {
	api, _ := varsAPI()

	putVars(t, api, keyAContent, `{"variables":{"moneda":"Bs","envio":"gratis","tel":"soporte"}}`)
	// Segundo PUT SIN "envio" ni "tel": deben desaparecer.
	got := putVars(t, api, keyAContent, `{"variables":{"moneda":"USD"}}`)
	if !maps.Equal(got.Variables, map[string]string{"moneda": "USD"}) {
		t.Fatalf("tras el reemplazo quedó %#v, quiero solo moneda=USD", got.Variables)
	}
	if got := getVars(t, api, keyAContent); !maps.Equal(got.Variables, map[string]string{"moneda": "USD"}) {
		t.Fatalf("GET tras el reemplazo devolvió %#v", got.Variables)
	}
}

// TestTenantVariables_Put_VacioBorraTodo: {"variables":{}} es una intención
// explícita (dejar al tenant sin variables), no un cuerpo inválido.
func TestTenantVariables_Put_VacioBorraTodo(t *testing.T) {
	api, _ := varsAPI()

	putVars(t, api, keyAContent, `{"variables":{"moneda":"Bs"}}`)
	got := putVars(t, api, keyAContent, `{"variables":{}}`)
	if len(got.Variables) != 0 {
		t.Fatalf("quedaron variables tras vaciar: %#v", got.Variables)
	}
	if got.UpdatedAt != "" {
		t.Fatalf("sin variables no hay updated_at que reportar: %q", got.UpdatedAt)
	}
	// El mapa viaja como {} y NUNCA como null (el cliente itera sin comprobar nil).
	rec := call(api, keyAContent, http.MethodGet, "/api/v1/tenant-variables", "")
	if !strings.Contains(rec.Body.String(), `"variables":{}`) {
		t.Fatalf("el conjunto vacío debe serializarse como {}: %s", rec.Body.String())
	}
}

// TestTenantVariables_Aislamiento_PorTenant (INV-8): el tenant sale del token. B
// no ve las de A, y su PUT —que reemplaza el conjunto ENTERO de B— no roza las
// de A.
func TestTenantVariables_Aislamiento_PorTenant(t *testing.T) {
	api, store := varsAPI()

	putVars(t, api, keyAContent, `{"variables":{"moneda":"Bs","secreto_de_a":"solo-a"}}`)
	if got := getVars(t, api, keyBContent); len(got.Variables) != 0 {
		t.Fatalf("el tenant B no debe ver variables ajenas: %#v", got.Variables)
	}

	putVars(t, api, keyBContent, `{"variables":{"moneda":"USD"}}`)
	gotA := getVars(t, api, keyAContent)
	if !maps.Equal(gotA.Variables, map[string]string{"moneda": "Bs", "secreto_de_a": "solo-a"}) {
		t.Fatalf("el PUT de B pisó las variables de A: %#v", gotA.Variables)
	}
	gotB := getVars(t, api, keyBContent)
	if !maps.Equal(gotB.Variables, map[string]string{"moneda": "USD"}) {
		t.Fatalf("el tenant B ve %#v, quiero solo moneda=USD", gotB.Variables)
	}
	// Y en el store, cada conjunto bajo SU tenant.
	rowsA, err := store.List(context.Background(), tenantA)
	if err != nil || len(rowsA) != 2 {
		t.Fatalf("store tenantA: rows=%d err=%v", len(rowsA), err)
	}
}

// TestTenantVariables_SinScope_403: el scope compartido con tenant-content manda.
// keyARead (flows.read) no cubre ni content.read ni content.write.
func TestTenantVariables_SinScope_403(t *testing.T) {
	api, _ := varsAPI()

	if rec := call(api, keyARead, http.MethodGet, "/api/v1/tenant-variables", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("GET sin content.read code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	if rec := call(api, keyARead, http.MethodPut, "/api/v1/tenant-variables", `{"variables":{"x":"1"}}`); rec.Code != http.StatusForbidden {
		t.Fatalf("PUT sin content.write code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	// Y sin credencial ninguna, 401 (no 403: primero se pregunta quién eres).
	if rec := call(api, "", http.MethodGet, "/api/v1/tenant-variables", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET sin token code=%d, quiero 401", rec.Code)
	}
}

// TestTenantVariables_SoloLectura_PuedeLeer: viewer ('*.read' → content.read) lee
// pero no escribe. Es el reparto que se hereda del scope de tenant-content.
func TestTenantVariables_SoloLectura_PuedeLeer(t *testing.T) {
	store := tenantvars.NewMemoryStore()
	if err := store.Replace(context.Background(), tenantA, map[string]string{"moneda": "Bs"}); err != nil {
		t.Fatalf("sembrando: %v", err)
	}
	keys := apiKeys()
	keys["key-a-viewer"] = testIdentity{TenantID: tenantA, Subject: "viewer-a", Grants: []string{"*.read"}}
	api := newAPI(publicapi.Deps{TenantVariables: store}, keys)

	if got := getVars(t, api, "key-a-viewer"); !maps.Equal(got.Variables, map[string]string{"moneda": "Bs"}) {
		t.Fatalf("el viewer debe poder LEER las variables: %#v", got.Variables)
	}
	if rec := call(api, "key-a-viewer", http.MethodPut, "/api/v1/tenant-variables", `{"variables":{}}`); rec.Code != http.StatusForbidden {
		t.Fatalf("el viewer NO debe poder escribir: code=%d", rec.Code)
	}
}

// TestTenantVariables_CuerposInvalidos: lo único que se valida es la FORMA. Ojo
// con el segundo caso: {} sin el campo `variables` es un 400 A PROPÓSITO — se
// niega a adivinar si el cliente quiso vaciar el conjunto o se equivocó de forma.
func TestTenantVariables_CuerposInvalidos(t *testing.T) {
	api, store := varsAPI()
	if err := store.Replace(context.Background(), tenantA, map[string]string{"moneda": "Bs"}); err != nil {
		t.Fatalf("sembrando: %v", err)
	}

	casos := []struct {
		nombre string
		cuerpo string
	}{
		{"no es json", `esto no es json`},
		{"sin el campo variables", `{}`},
		{"valor que no es cadena", `{"variables":{"moneda":42}}`},
		{"clave vacía", `{"variables":{"":"x"}}`},
		{"clave larguísima", `{"variables":{"` + strings.Repeat("k", 201) + `":"x"}}`},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			rec := call(api, keyAContent, http.MethodPut, "/api/v1/tenant-variables", c.cuerpo)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	// Ningún cuerpo inválido tocó lo guardado.
	if got := getVars(t, api, keyAContent); !maps.Equal(got.Variables, map[string]string{"moneda": "Bs"}) {
		t.Fatalf("un cuerpo inválido alteró el conjunto: %#v", got.Variables)
	}
}

// TestTenantVariables_SinStore_NoSeMontanLasRutas: sin store cableado la ruta no
// existe (404), que es preferible a un 500 a medio camino.
func TestTenantVariables_SinStore_NoSeMontanLasRutas(t *testing.T) {
	api := newAPI(publicapi.Deps{}, apiKeys())
	if rec := call(api, keyAContent, http.MethodGet, "/api/v1/tenant-variables", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404 (ruta no montada)", rec.Code)
	}
}
