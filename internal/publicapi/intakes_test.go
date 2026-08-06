package publicapi_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// Credenciales de la bandeja de solicitudes (Plan 041 · T1.1/T1.4).
const (
	keyAIntakes = "key-a-intakes" // tenantA, intakes.read + intakes.write
	keyBIntakes = "key-b-intakes" // tenantB, los mismos scopes en OTRO tenant
)

// ids de las solicitudes sembradas (UUID: el store Postgres exige que lo sean).
const (
	intakeA1 = "11111111-1111-1111-1111-111111111111" // closed (legado), sess-a, 08-01
	intakeA2 = "22222222-2222-2222-2222-222222222222" // open,             sess-a, 08-02
	intakeA3 = "33333333-3333-3333-3333-333333333333" // cancelled,        sess-b, 08-03
	intakeA4 = "44444444-4444-4444-4444-444444444444" // closed (legado), sess-a, 08-04
	intakeA5 = "55555555-5555-5555-5555-555555555555" // confirmed,        sess-b, 08-05
	intakeB1 = "bbbbbbb1-bbbb-bbbb-bbbb-bbbbbbbbbbbb" // del tenant B
)

// contactoOpaco es el identificador OPACO del contacto tal como está en BD
// (ADR-0010): sin número, sin JID, sin nombre. El test afirma que la API lo
// devuelve TAL CUAL, sin descifrar ni enriquecer (INV-04).
const contactoOpaco = "9f1c0a7e-0000-4000-8000-000000000abc"

// intakesKeys extiende apiKeys() con las credenciales de la bandeja.
func intakesKeys() map[string]testIdentity {
	keys := apiKeys()
	keys[keyAIntakes] = testIdentity{TenantID: tenantA, Subject: "duena-a", Grants: []string{"intakes.read", "intakes.write"}}
	keys[keyBIntakes] = testIdentity{TenantID: tenantB, Subject: "duena-b", Grants: []string{"intakes.read", "intakes.write"}}
	return keys
}

// intakeDTO espeja el contrato de la cabecera en el wire.
type intakeDTO struct {
	ID        string  `json:"id"`
	ContactID string  `json:"contact_id"`
	SessionID string  `json:"session_id"`
	Status    string  `json:"status"`
	Total     float64 `json:"total"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

type intakeListDTO struct {
	Intakes  []intakeDTO `json:"intakes"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Total    int         `json:"total"`
}

type intakeItemDTO struct {
	SKU       string  `json:"sku"`
	Label     string  `json:"label"`
	Qty       int     `json:"qty"`
	UnitPrice float64 `json:"unit_price"`
}

type intakeDetailDTO struct {
	intakeDTO
	Items []intakeItemDTO `json:"items"`
}

type invalidTransitionDTO struct {
	Error     string   `json:"error"`
	Status    string   `json:"status"`
	Requested string   `json:"requested"`
	Allowed   []string `json:"allowed"`
}

func día(d int) time.Time { return time.Date(2026, 8, d, 12, 0, 0, 0, time.UTC) }

// seedIntakes siembra la bandeja del tenant A (cinco solicitudes repartidas en
// fechas, estados y sesiones) más una del tenant B para el aislamiento.
func seedIntakes() *intakes.MemoryStore {
	st := intakes.NewMemoryStore()
	add := func(tenant, id, status, session string, day int, items ...intakes.Item) {
		st.Add(tenant, intakes.Intake{
			ID: id, ContactID: contactoOpaco, SessionID: session, Status: status,
			Total: 18000, CreatedAt: día(day), UpdatedAt: día(day),
		}, items...)
	}
	add(tenantA, intakeA1, intakes.StatusClosedLegacy, "sess-a", 1,
		intakes.Item{SKU: "torta-v1", Label: "Torta 10-12 porciones", Qty: 1, UnitPrice: 18000},
		intakes.Item{SKU: "_shipping", Label: "Envío — Providencia", Qty: 1, UnitPrice: 3000})
	add(tenantA, intakeA2, intakes.StatusOpen, "sess-a", 2)
	add(tenantA, intakeA3, intakes.StatusCancelled, "sess-b", 3)
	add(tenantA, intakeA4, intakes.StatusClosedLegacy, "sess-a", 4)
	add(tenantA, intakeA5, intakes.StatusConfirmed, "sess-b", 5)
	add(tenantB, intakeB1, intakes.StatusOpen, "sess-b1", 1)
	return st
}

// intakesDeps arma unas Deps con la bandeja sembrada y la feature cart_basic
// ENCENDIDA para el tenant A (el tenant B la tiene también: los tests de
// aislamiento deben fallar por tenant, no por plan).
func intakesDeps(store *intakes.MemoryStore) publicapi.Deps {
	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureCartBasic)
	fake.Enable(tenantB, entitlements.FeatureCartBasic)
	return publicapi.Deps{Intakes: intakes.NewService(store), Entitlements: fake}
}

func decodeList(t *testing.T, body []byte) intakeListDTO {
	t.Helper()
	var out intakeListDTO
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal de la lista: %v; body=%s", err, body)
	}
	return out
}

func idsDe(list intakeListDTO) []string {
	out := make([]string, 0, len(list.Intakes))
	for _, in := range list.Intakes {
		out = append(out, in.ID)
	}
	return out
}

// ============================ GET /api/v1/intakes ============================

// TestIntakes_200_FiltrosCombinados: estado + sesión + rango de fechas a la vez.
// El filtro `status=closed` es la clave LEGADA y tiene que alcanzar las filas que
// el módulo cart cerró con ella; `to=2026-08-04` incluye el día 4 entero (una
// fecha suelta significa "hasta el final de ese día").
func TestIntakes_200_FiltrosCombinados(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/intakes?status=closed&session=sess-a&from=2026-08-01&to=2026-08-04", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	list := decodeList(t, rec.Body.Bytes())
	if got := idsDe(list); !slices.Equal(got, []string{intakeA4, intakeA1}) {
		t.Fatalf("ids=%v, quiero [%s %s] (más recientes primero)", got, intakeA4, intakeA1)
	}
	if list.Total != 2 {
		t.Fatalf("total=%d, quiero 2", list.Total)
	}
	for _, in := range list.Intakes {
		if in.Status != intakes.StatusConfirmed {
			t.Fatalf("status=%q; el `closed` legado se sirve NORMALIZADO como confirmed", in.Status)
		}
	}
}

// TestIntakes_200_FiltroPorClaveNueva: filtrar por `confirmed` devuelve lo mismo
// que filtrar por `closed` MÁS lo escrito ya con la clave nueva. Es la prueba de
// que no hace falta migrar las filas históricas.
func TestIntakes_200_FiltroPorClaveNueva(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes?status=confirmed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	list := decodeList(t, rec.Body.Bytes())
	if got := idsDe(list); !slices.Equal(got, []string{intakeA5, intakeA4, intakeA1}) {
		t.Fatalf("ids=%v, quiero las dos legadas MÁS la nueva", got)
	}
}

// TestIntakes_200_Paginación: `total` cuenta TODAS las coincidencias del filtro,
// no las de la página — sin eso el paginador de la consola miente.
func TestIntakes_200_Paginación(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes?page=1&page_size=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	first := decodeList(t, rec.Body.Bytes())
	if first.Total != 5 || first.Page != 1 || first.PageSize != 2 || len(first.Intakes) != 2 {
		t.Fatalf("página 1 = %+v; quiero total=5 page=1 page_size=2 con 2 filas", first)
	}
	if got := idsDe(first); !slices.Equal(got, []string{intakeA5, intakeA4}) {
		t.Fatalf("ids página 1 = %v, quiero las dos más recientes", got)
	}

	rec = call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes?page=3&page_size=2", "")
	last := decodeList(t, rec.Body.Bytes())
	if last.Total != 5 || len(last.Intakes) != 1 {
		t.Fatalf("página 3 = %+v; quiero total=5 con la fila suelta", last)
	}
	if got := idsDe(last); !slices.Equal(got, []string{intakeA1}) {
		t.Fatalf("ids página 3 = %v, quiero [%s]", got, intakeA1)
	}

	// Sin paginación explícita: los defaults del contrato (design §4).
	rec = call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes", "")
	def := decodeList(t, rec.Body.Bytes())
	if def.Page != 1 || def.PageSize != 50 {
		t.Fatalf("defaults = page=%d page_size=%d; quiero 1 y 50", def.Page, def.PageSize)
	}
}

// TestIntakes_200_PageSizeAcotado: pedir 100000 filas no vacía la tabla de un GET.
func TestIntakes_200_PageSizeAcotado(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes?page_size=100000", "")
	list := decodeList(t, rec.Body.Bytes())
	if list.PageSize != 200 {
		t.Fatalf("page_size=%d, quiero 200 (cota superior del contrato)", list.PageSize)
	}
}

// TestIntakes_200_AisladoPorTenant: la lista del tenant B NO contiene nada del A.
func TestIntakes_200_AisladoPorTenant(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyBIntakes, http.MethodGet, "/api/v1/intakes", "")
	list := decodeList(t, rec.Body.Bytes())
	if got := idsDe(list); !slices.Equal(got, []string{intakeB1}) {
		t.Fatalf("ids del tenant B = %v; solo puede ver lo suyo (INV-8)", got)
	}
}

// TestIntakes_400_FiltrosInválidos: un typo en el filtro se dice, no se ignora —
// devolver la bandeja entera ante `status=confirmadas` sería peor que un error.
func TestIntakes_400_FiltrosInválidos(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	for _, q := range []string{"?status=confirmadas", "?from=ayer", "?to=2026-13-45"} {
		rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes"+q, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s → code=%d, quiero 400; body=%s", q, rec.Code, rec.Body.String())
		}
	}
}

func TestIntakes_403_SinScope(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	// keyARead porta flows.read: autenticada, pero sin intakes.read.
	rec := call(api, keyARead, http.MethodGet, "/api/v1/intakes", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 sin el scope; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntakes_403_SinFeature: el scope no basta. Con intakes.read pero sin
// cart_basic en el plan, la bandeja no se abre — y el cuerpo dice cuál falta para
// que la UI pueda ofrecer el upgrade sin adivinar.
func TestIntakes_403_SinFeature(t *testing.T) {
	d := publicapi.Deps{
		Intakes:      intakes.NewService(seedIntakes()),
		Entitlements: entitlements.NewFake(), // ninguna feature encendida
	}
	api := newAPI(d, intakesKeys())

	for _, ruta := range []struct{ method, target, body string }{
		{http.MethodGet, "/api/v1/intakes", ""},
		{http.MethodGet, "/api/v1/intakes/" + intakeA1, ""},
		{http.MethodPost, "/api/v1/intakes/" + intakeA1 + "/status", `{"status":"cancelled"}`},
	} {
		rec := call(api, keyAIntakes, ruta.method, ruta.target, ruta.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s → code=%d, quiero 403 sin la feature; body=%s",
				ruta.method, ruta.target, rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal del 403: %v; body=%s", err, rec.Body.String())
		}
		if body["error"] != "feature_not_enabled" || body["feature"] != entitlements.FeatureCartBasic {
			t.Fatalf("cuerpo del 403 = %v; quiero feature_not_enabled/cart_basic", body)
		}
	}
}

// ========================== GET /api/v1/intakes/{id} ==========================

// TestIntakeDetail_200_ConLíneasYContactoOpaco: el detalle trae las líneas y el
// contact_id sale TAL CUAL está en BD — ni descifrado, ni enriquecido, ni omitido
// (INV-04 / ADR-0010).
func TestIntakeDetail_200_ConLíneasYContactoOpaco(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/"+intakeA1, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var detail intakeDetailDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshal del detalle: %v; body=%s", err, rec.Body.String())
	}
	if detail.ContactID != contactoOpaco {
		t.Fatalf("contact_id=%q, quiero el opaco de BD %q", detail.ContactID, contactoOpaco)
	}
	if detail.Status != intakes.StatusConfirmed {
		t.Fatalf("status=%q; el `closed` legado se sirve normalizado", detail.Status)
	}
	if len(detail.Items) != 2 || detail.Items[0].SKU != "torta-v1" || detail.Items[1].SKU != "_shipping" {
		t.Fatalf("items=%+v; quiero las dos líneas en el orden en que se añadieron", detail.Items)
	}

	// El detalle NO inventa revisiones: intake_revisions nace en T4.1 (Ola 4).
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal crudo: %v", err)
	}
	if _, ok := raw["revisions"]; ok {
		t.Fatal("el detalle publica `revisions` sin que exista la tabla: es fingir el hueco")
	}
	// Ni el tenant: siempre es el del token (INV-8).
	if _, ok := raw["tenant_id"]; ok {
		t.Fatal("el detalle no debe devolver tenant_id: es el del token")
	}
}

// TestIntakeDetail_404_CrossTenant: el tenant B pide una solicitud del A con un id
// VÁLIDO y existente. Responde 404, NO 403: un 403 confirmaría que el id existe, y
// el aislamiento entre tenants no puede filtrar ni eso (INV-8).
func TestIntakeDetail_404_CrossTenant(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyBIntakes, http.MethodGet, "/api/v1/intakes/"+intakeA1, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404 (nunca 403); body=%s", rec.Code, rec.Body.String())
	}

	// Y la misma respuesta que un id que no existe en ninguna parte: las dos
	// situaciones tienen que ser indistinguibles desde fuera.
	inexistente := call(api, keyBIntakes, http.MethodGet,
		"/api/v1/intakes/99999999-9999-9999-9999-999999999999", "")
	if inexistente.Code != rec.Code || inexistente.Body.String() != rec.Body.String() {
		t.Fatalf("ajena=%d/%s vs inexistente=%d/%s: se distinguen",
			rec.Code, rec.Body.String(), inexistente.Code, inexistente.Body.String())
	}
}

// ====================== POST /api/v1/intakes/{id}/status ======================

// TestIntakeStatus_200_TransiciónVálida sobre una fila LEGADA en `closed`: el
// destino deposit_requested solo existe desde `confirmed`, así que este 200 es la
// prueba de que la normalización manda en la máquina de estados.
func TestIntakeStatus_200_ClosedLegadoEsConfirmed(t *testing.T) {
	store := seedIntakes()
	api := newAPI(intakesDeps(store), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPost,
		"/api/v1/intakes/"+intakeA1+"/status", `{"status":"deposit_requested"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var out intakeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if out.Status != intakes.StatusDepositRequested {
		t.Fatalf("status=%q, quiero deposit_requested", out.Status)
	}

	// Y quedó persistido (la lista lo confirma con el filtro de la clave nueva).
	rel := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes?status=deposit_requested", "")
	if got := idsDe(decodeList(t, rel.Body.Bytes())); !slices.Equal(got, []string{intakeA1}) {
		t.Fatalf("tras la transición, status=deposit_requested devuelve %v", got)
	}
}

// TestIntakeStatus_422_ConDestinosEnElCuerpo: la transición inválida no solo se
// rechaza — dice dónde está la solicitud y adónde SÍ puede ir. Sin `allowed`, el
// llamante tendría que adivinar el ciclo de vida a base de reintentos.
func TestIntakeStatus_422_ConDestinosEnElCuerpo(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPost,
		"/api/v1/intakes/"+intakeA2+"/status", `{"status":"settled"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d, quiero 422; body=%s", rec.Code, rec.Body.String())
	}
	var body invalidTransitionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 422: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != "invalid_transition" {
		t.Fatalf("error=%q, quiero invalid_transition", body.Error)
	}
	if body.Status != intakes.StatusOpen || body.Requested != intakes.StatusSettled {
		t.Fatalf("cuerpo=%+v; quiero el estado ACTUAL (open) y el pedido (settled)", body)
	}
	want := []string{"abandoned", "cancelled", "confirmed", "pending_approval"}
	if !slices.Equal(body.Allowed, want) {
		t.Fatalf("allowed=%v, quiero %v", body.Allowed, want)
	}
}

// TestIntakeStatus_422_ExpiredNoEsDestino: D-041.16 derogó el vencimiento por
// tiempo; nadie puede mandar una solicitud a `expired` por API.
func TestIntakeStatus_422_ExpiredNoEsDestino(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodPost,
		"/api/v1/intakes/"+intakeA2+"/status", `{"status":"expired"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d, quiero 422; body=%s", rec.Code, rec.Body.String())
	}
	var body invalidTransitionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 422: %v", err)
	}
	if slices.Contains(body.Allowed, intakes.StatusExpired) {
		t.Fatalf("allowed=%v ofrece expired", body.Allowed)
	}
}

// TestIntakeStatus_400_CuerpoInválido: sin `status` no hay nada que aplicar.
func TestIntakeStatus_400_CuerpoInválido(t *testing.T) {
	api := newAPI(intakesDeps(seedIntakes()), intakesKeys())

	for _, body := range []string{`{}`, `{"status":""}`, `no-es-json`} {
		rec := call(api, keyAIntakes, http.MethodPost,
			"/api/v1/intakes/"+intakeA2+"/status", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%s → code=%d, quiero 400; resp=%s", body, rec.Code, rec.Body.String())
		}
	}
}

// TestIntakeStatus_404_CrossTenant: el tenant B no transiciona lo del A, y su 404
// no confirma que el id exista.
func TestIntakeStatus_404_CrossTenant(t *testing.T) {
	store := seedIntakes()
	api := newAPI(intakesDeps(store), intakesKeys())

	rec := call(api, keyBIntakes, http.MethodPost,
		"/api/v1/intakes/"+intakeA2+"/status", `{"status":"cancelled"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}

	// Y no la tocó: sigue abierta para su dueño.
	detail := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/"+intakeA2, "")
	var d intakeDetailDTO
	if err := json.Unmarshal(detail.Body.Bytes(), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Status != intakes.StatusOpen {
		t.Fatalf("status=%q tras el intento cross-tenant; quiero open intacto", d.Status)
	}
}

// TestIntakeStatus_403_SinScopeDeEscritura: leer la bandeja no autoriza a mover el
// estado — el viewer (`*.read`) llega al listado y se queda fuera de la escritura.
func TestIntakeStatus_403_SinScopeDeEscritura(t *testing.T) {
	keys := intakesKeys()
	const soloLectura = "key-a-intakes-ro"
	keys[soloLectura] = testIdentity{TenantID: tenantA, Subject: "viewer-a", Grants: []string{"intakes.read"}}
	api := newAPI(intakesDeps(seedIntakes()), keys)

	if rec := call(api, soloLectura, http.MethodGet, "/api/v1/intakes", ""); rec.Code != http.StatusOK {
		t.Fatalf("la lectura con intakes.read debe pasar: code=%d", rec.Code)
	}
	rec := call(api, soloLectura, http.MethodPost,
		"/api/v1/intakes/"+intakeA2+"/status", `{"status":"cancelled"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 sin intakes.write; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntakes_RutasNoMontadasSinDependencias: sin el servicio (o sin el resolver
// de features) las rutas NO existen — fail-closed, no un 500 a medio camino.
func TestIntakes_RutasNoMontadasSinDependencias(t *testing.T) {
	api := newAPI(publicapi.Deps{}, intakesKeys())

	if rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404 (ruta no montada)", rec.Code)
	}
}
