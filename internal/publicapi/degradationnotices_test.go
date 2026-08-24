// degradationnotices_test.go cubre GET /api/v1/degradation-notices (Plan 044 ·
// Ola 1.5 · T1.5-4, D-044.32, REQ-38).
//
// LO QUE ESTOS TESTS CUSTODIAN, por orden de importancia:
//
//  1. EL GATE ES `llm_intake` Y NO `api_llm`. Es la decisión de la tarea y la que
//     más fácil sería «arreglar» mal: los avisos de degradación le importan a
//     cualquier tenant con el NIVEL, y SEIS de los ocho motivos son de la vía
//     LOCAL. Gatear por la vía dejaría sin bandeja justo a quien más la necesita.
//  2. INV-6 EN EL WIRE: la respuesta no tiene ni una clave donde quepa texto del
//     cliente.
//  3. INV-7: el tenant sale del token.
//
// ⏳ NO EJECUTADO (entorno sin Go). Cada test lleva su mutación, y la mutación
// compila.
package publicapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// El store REAL tiene que satisfacer el puerto de lectura: si no, estos tests
// estarían probando un fake que no se parece a lo que corre en producción.
var _ publicapi.DegradationNoticeLister = (*degradation.Postgres)(nil)

// errAvisosCaído es el fallo de infraestructura que simula el fake.
var errAvisosCaído = errors.New("degradation: la base no responde")

// fakeAvisos imita a *degradation.Postgres en lo que importa aquí.
//
// 🔴 SOLO TIENE List, igual que el puerto. Un fake con Save daría por bueno un
// handler que escribiera, y lo que este endpoint no puede hacer es escribir.
type fakeAvisos struct {
	porTenant map[string][]degradation.Notice
	fallo     error
	// últimoFiltro es el testigo que convierte «el handler leyó» en «el handler
	// leyó CON ESTO»: sin él, el recorte del limit y el filtro `unread` se
	// probarían por la respuesta, que es exactamente donde no se ven.
	últimoFiltro degradation.ListFilter
	últimoTenant string
}

func (f *fakeAvisos) List(_ context.Context, tenantID string, filtro degradation.ListFilter) ([]degradation.Notice, error) {
	f.últimoTenant, f.últimoFiltro = tenantID, filtro
	if f.fallo != nil {
		return nil, f.fallo
	}
	return f.porTenant[tenantID], nil
}

// avisoDePrueba es un aviso ya escrito, con TODOS los campos rellenos: es el que
// enseña qué sale al wire.
func avisoDePrueba(tenantID string, motivo degradation.Reason, via string) degradation.Notice {
	inicio := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	return degradation.Notice{
		ID:          "11111111-2222-3333-4444-555555555555",
		TenantID:    tenantID,
		Reason:      motivo,
		Via:         via,
		WindowStart: inicio,
		WindowEnd:   inicio.Add(15 * time.Minute),
		Occurrences: 7,
		CreatedAt:   inicio.Add(time.Second),
		LastSeenAt:  inicio.Add(13 * time.Minute),
	}
}

// apiDeAvisos monta la API con el store y las features dadas.
func apiDeAvisos(store publicapi.DegradationNoticeLister, feats *entitlements.Fake) *testAPI {
	return newAPI(publicapi.Deps{DegradationNotices: store, Entitlements: feats}, llmKeys())
}

// TestT154_ElGateEsLaCapacidadNoLaVia es EL test de la decisión de esta tarea.
//
// Un tenant de la vía LOCAL —capacidad SÍ, vía API NO— tiene que poder leer sus
// avisos: es el dueño de `ollama_down`, `breaker_open`, `edge_offline`,
// `timeout`, `lease_invalid` y `edge_sin_capacidad`, o sea de seis de los ocho
// motivos. Y un tenant que tenga la vía
// pero NO el nivel no tiene bandeja que leer.
//
// MUTACIÓN (compila, y es el error natural — «esto es del LLM, gátalo como el
// CRUD de al lado»): en publicapi.go, dentro de registerDegradationNotices,
// sustituir
//
//	llmIntake := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureLLMIntake)
//
// por
//
//	llmIntake := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureAPILLM)
//
// ⇒ el primer subcaso pasaría a 403 y este test se pone ROJO.
func TestT154_ElGateEsLaCapacidadNoLaVia(t *testing.T) {
	casos := []struct {
		nombre    string
		capacidad bool
		via       bool
		esperado  int
	}{
		// El tenant de la vía LOCAL: es el caso que la mutación rompe.
		{"capacidad sin vía ⇒ 200", true, false, http.StatusOK},
		{"capacidad y vía ⇒ 200", true, true, http.StatusOK},
		// Tener la vía API no da bandeja: lo que se lee aquí es del NIVEL.
		{"vía sin capacidad ⇒ 403", false, true, http.StatusForbidden},
		{"ni capacidad ni vía ⇒ 403", false, false, http.StatusForbidden},
	}
	for _, c := range casos {
		feats := entitlements.NewFake()
		if c.capacidad {
			feats.Enable(tenantA, entitlements.FeatureLLMIntake)
		} else {
			feats.Disable(tenantA, entitlements.FeatureLLMIntake)
		}
		if c.via {
			feats.Enable(tenantA, entitlements.FeatureAPILLM)
		} else {
			feats.Disable(tenantA, entitlements.FeatureAPILLM)
		}
		api := apiDeAvisos(&fakeAvisos{}, feats)
		rec := call(api, keyALLMRead, http.MethodGet, "/api/v1/degradation-notices", "")
		if rec.Code != c.esperado {
			t.Errorf("%s: código %d, se esperaba %d", c.nombre, rec.Code, c.esperado)
		}
	}
}

// TestT154_ListaVaciaEs200ConArray custodia el caso NORMAL de la Ola 1.5: nadie
// puebla la tabla, así que la respuesta de campo va a ser una lista vacía. Tiene
// que ser 200 con `[]` y NO 404, y NO `null` — una pantalla que recorra `null`
// revienta, y un 404 diría «no existe la bandeja» cuando lo que pasa es que el
// LLM no se ha caído.
//
// MUTACIÓN: en degradationnotices.go, cambiar el cuerpo de toDegradationNoticeDTOs
// por
//
//	func toDegradationNoticeDTOs(avisos []degradation.Notice) []degradationNoticeDTO {
//		var out []degradationNoticeDTO
//		for _, n := range avisos {
//			out = append(out, toDegradationNoticeDTO(n))
//		}
//		return out
//	}
//
// ⇒ la lista vacía serializa como null y este test se pone ROJO.
func TestT154_ListaVaciaEs200ConArray(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureLLMIntake)
	api := apiDeAvisos(&fakeAvisos{}, feats)

	rec := call(api, keyALLMRead, http.MethodGet, "/api/v1/degradation-notices", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d, se esperaba 200", rec.Code)
	}
	var cuerpo map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &cuerpo); err != nil {
		t.Fatalf("respuesta no es JSON: %v", err)
	}
	if string(cuerpo["notices"]) != "[]" {
		t.Errorf("notices = %s, se esperaba [] (una pantalla no puede recorrer null)", cuerpo["notices"])
	}
}

// TestT154_INV6_LaRespuestaNoTieneDondeMeterTextoDelCliente comprueba que las
// claves del JSON son EXACTAMENTE el conjunto cerrado esperado. No es un test de
// forma: es INV-6 en el wire. El día que alguien añada un `detail` al DTO —con la
// mejor intención, «para que el dueño sepa qué pasó»— este test lo para, porque ese
// campo es el único sitio por donde el contenido de una conversación podría salir.
//
// MUTACIÓN: añadir a degradationNoticeDTO (degradationnotices.go) la línea
//
//	Detail string `json:"detail,omitempty"`
//
// y rellenarla en toDegradationNoticeDTO con
//
//	dto.Detail = "lo que dijo el cliente"
//
// ⇒ este test se pone ROJO por clave inesperada.
func TestT154_INV6_LaRespuestaNoTieneDondeMeterTextoDelCliente(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureLLMIntake)
	store := &fakeAvisos{porTenant: map[string][]degradation.Notice{
		tenantA: {avisoDePrueba(tenantA, degradation.ReasonOllamaDown, degradation.ViaLocal)},
	}}
	api := apiDeAvisos(store, feats)

	rec := call(api, keyALLMRead, http.MethodGet, "/api/v1/degradation-notices", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d, se esperaba 200", rec.Code)
	}
	var cuerpo struct {
		Notices []map[string]any `json:"notices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cuerpo); err != nil {
		t.Fatalf("respuesta no es JSON: %v", err)
	}
	if len(cuerpo.Notices) != 1 {
		t.Fatalf("llegaron %d avisos, se esperaba 1", len(cuerpo.Notices))
	}
	permitidas := map[string]bool{
		"id": true, "reason": true, "via": true, "window_start": true,
		"window_end": true, "occurrences": true, "read": true, "read_at": true,
		"created_at": true, "last_seen_at": true,
	}
	for clave := range cuerpo.Notices[0] {
		if !permitidas[clave] {
			t.Errorf("clave inesperada %q en el aviso: INV-6 exige un conjunto CERRADO de campos", clave)
		}
	}
	// `tenant_id` NO viaja: siempre es el del token, y repetirlo solo le daría a
	// alguien la idea de mandarlo de vuelta.
	if _, hay := cuerpo.Notices[0]["tenant_id"]; hay {
		t.Error("el aviso trae tenant_id: siempre es el del token (INV-7)")
	}
	// Y el motivo que sale es del vocabulario cerrado, no una cadena cualquiera.
	m, ok := cuerpo.Notices[0]["reason"].(string)
	if !ok {
		t.Fatalf("reason no es una cadena, es %T", cuerpo.Notices[0]["reason"])
	}
	if !degradation.Reason(m).Valid() {
		t.Errorf("reason = %q, fuera del vocabulario cerrado", m)
	}
}

// TestT154_AisladoPorTenant es INV-7 en la puerta: el tenant sale del token, y no
// hay parámetro de query que lo cambie.
//
// MUTACIÓN: en listDegradationNoticesHandler (degradationnotices.go), cambiar
//
//	avisos, err := lister.List(ctx, id.TenantID, filtro)
//
// por
//
//	avisos, err := lister.List(ctx, r.URL.Query().Get("tenant_id"), filtro)
//
// ⇒ este test se pone ROJO.
func TestT154_AisladoPorTenant(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureLLMIntake)
	feats.Enable(tenantB, entitlements.FeatureLLMIntake)
	store := &fakeAvisos{porTenant: map[string][]degradation.Notice{
		tenantA: {avisoDePrueba(tenantA, degradation.ReasonOllamaDown, degradation.ViaLocal)},
		tenantB: {avisoDePrueba(tenantB, degradation.ReasonAPIError, degradation.ViaAPI)},
	}}
	api := apiDeAvisos(store, feats)

	// El tenant A pide los del B por la query: se le sirven los SUYOS.
	rec := call(api, keyALLMRead, http.MethodGet,
		"/api/v1/degradation-notices?tenant_id="+tenantB, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d, se esperaba 200", rec.Code)
	}
	if store.últimoTenant != tenantA {
		t.Errorf("el store recibió el tenant %q, se esperaba %q (el del token)", store.últimoTenant, tenantA)
	}
	var cuerpo struct {
		Notices []map[string]any `json:"notices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cuerpo); err != nil {
		t.Fatalf("respuesta no es JSON: %v", err)
	}
	if len(cuerpo.Notices) != 1 {
		t.Fatalf("llegaron %d avisos, se esperaba 1", len(cuerpo.Notices))
	}
	m, ok := cuerpo.Notices[0]["reason"].(string)
	if !ok {
		t.Fatalf("reason no es una cadena, es %T", cuerpo.Notices[0]["reason"])
	}
	if m != string(degradation.ReasonOllamaDown) {
		t.Errorf("llegó el aviso del tenant ajeno (reason=%q)", m)
	}
}

// TestT154_LaQuerySeAcotaYSeDevuelveEfectiva comprueba las tres decisiones del
// filtro: el limit se RECORTA (no se rechaza), el offset negativo cae a 0, y el
// `unread` solo lo enciende el literal "true". Y que la respuesta devuelve el
// limit EFECTIVO: sin eso, quien pida 500 y reciba 200 paginaría con agujeros.
//
// ⚠️ EL ALCANCE, DICHO ENTERO (code review 2026-08-23): los ocho casos comparan
// el FILTRO QUE LLEGA AL STORE contra el que la query pedía, y el eco de la
// respuesta contra ese mismo filtro. Eso es todo lo que hay que probar aquí —el
// recorte, el saneo y el eco son de `parseDegradationFilter`, y viven en esta
// capa—. Lo que NO se prueba, porque no es de esta capa: que la paginación
// RECORTE de verdad. `fakeAvisos` guarda el filtro y devuelve su lista entera
// ignorando limit/offset, así que ningún caso de aquí demostraría un `LIMIT`
// olvidado en el SQL. Ese aserto le toca al store contra Postgres real, donde hay
// filas que recortar, y 🔴 HOY NO LO HACE NADIE: los tests de
// degradation/postgres_integration_test.go cubren el dedupe, el CHECK de motivos
// y el aislamiento por tenant (INV-7), pero ninguno pide dos páginas de una lista
// sembrada. El `LIMIT $3 OFFSET $4` de listSQL (degradation/postgres.go:138) está
// escrito y sin custodiar. Queda dicho aquí en vez de dejar que este fichero
// parezca cubrirlo.
//
// MUTACIÓN: en parseDegradationFilter (degradationnotices.go), quitar el recorte
//
//	if limit > maxDegradationLimit {
//		limit = maxDegradationLimit
//	}
//
// ⇒ este test se pone ROJO en el primer caso.
func TestT154_LaQuerySeAcotaYSeDevuelveEfectiva(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureLLMIntake)

	casos := []struct {
		query   string
		limit   int
		offset  int
		sinLeer bool
	}{
		{"", 50, 0, false},
		{"?limit=500", 200, 0, false},          // recorta al tope, no rechaza
		{"?limit=0", 50, 0, false},             // ilegible/cero ⇒ default
		{"?limit=10&offset=-5", 10, 0, false},  // offset negativo ⇒ 0
		{"?unread=true", 50, 0, true},          // el filtro del teléfono
		{"?unread=false", 50, 0, false},        // solo el literal "true" enciende
		{"?unread=1", 50, 0, false},            // ídem: un typo no esconde avisos
		{"?limit=10&offset=20", 10, 20, false}, // la página normal
	}
	for _, c := range casos {
		store := &fakeAvisos{}
		api := apiDeAvisos(store, feats)
		rec := call(api, keyALLMRead, http.MethodGet, "/api/v1/degradation-notices"+c.query, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%q: código %d, se esperaba 200", c.query, rec.Code)
			continue
		}
		f := store.últimoFiltro
		if f.Limit != c.limit || f.Offset != c.offset || f.SoloSinLeer != c.sinLeer {
			t.Errorf("%q: filtro = {limit:%d offset:%d sinLeer:%v}, se esperaba {%d %d %v}",
				c.query, f.Limit, f.Offset, f.SoloSinLeer, c.limit, c.offset, c.sinLeer)
		}
		var cuerpo struct {
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &cuerpo); err != nil {
			t.Errorf("%q: respuesta no es JSON: %v", c.query, err)
			continue
		}
		if cuerpo.Limit != c.limit || cuerpo.Offset != c.offset {
			t.Errorf("%q: la respuesta dice {limit:%d offset:%d}, se aplicó {%d %d}",
				c.query, cuerpo.Limit, cuerpo.Offset, c.limit, c.offset)
		}
	}
}

// TestT154_SinIdentidadYConStoreCaido cierra los dos desenlaces que no son 200:
// 401 sin token y 500 cuando la base no responde. El 500 NO devuelve el error del
// store: el mensaje del driver puede llevar el DSN.
//
// MUTACIÓN: en listDegradationNoticesHandler, cambiar
//
//	writeError(w, http.StatusInternalServerError, "no se pudieron leer los avisos de degradación")
//
// por
//
//	writeError(w, http.StatusInternalServerError, err.Error())
//
// ⇒ este test se pone ROJO en la comprobación del cuerpo.
func TestT154_SinIdentidadYConStoreCaido(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureLLMIntake)

	api := apiDeAvisos(&fakeAvisos{}, feats)
	if rec := call(api, "", http.MethodGet, "/api/v1/degradation-notices", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("sin credencial: código %d, se esperaba 401", rec.Code)
	}

	caído := apiDeAvisos(&fakeAvisos{fallo: errAvisosCaído}, feats)
	rec := call(caído, keyALLMRead, http.MethodGet, "/api/v1/degradation-notices", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("store caído: código %d, se esperaba 500", rec.Code)
	}
	if cuerpo := rec.Body.String(); strings.Contains(cuerpo, "la base no responde") {
		t.Errorf("el 500 filtró el error del store: %s", cuerpo)
	}
}

// TestT154_SinStoreNoSeMontaLaRuta: nil ⇒ 404 de ruta inexistente, no un 500 a
// medio camino. Es el mismo criterio que registerTenantLLM.
//
// MUTACIÓN: en registerDegradationNotices (publicapi.go), cambiar
//
//	if d.DegradationNotices == nil || d.Entitlements == nil {
//
// por
//
//	if d.Entitlements == nil {
//
// ⇒ la ruta se monta con lister nil y responde 500: este test se pone ROJO.
func TestT154_SinStoreNoSeMontaLaRuta(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(tenantA, entitlements.FeatureLLMIntake)
	api := newAPI(publicapi.Deps{Entitlements: feats}, llmKeys())

	if rec := call(api, keyALLMRead, http.MethodGet, "/api/v1/degradation-notices", ""); rec.Code != http.StatusNotFound {
		t.Errorf("sin store: código %d, se esperaba 404", rec.Code)
	}
}
