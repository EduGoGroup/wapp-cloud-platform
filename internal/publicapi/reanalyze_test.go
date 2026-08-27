package publicapi_test

// reanalyze_test.go — POST /api/v1/intakes/{id}/reanalyze (Plan 044 · Ola 4 · T4.6).
//
// LOS CONTRACT TESTS DE LOS SEIS CÓDIGOS del design §8.1, y de sus SEIS CUERPOS. Lo
// que se afirma aquí es lo que sale POR EL CABLE: el código, el `error` y los campos
// que la UI necesita para decidir qué pantalla enseñar. La DECISIÓN —en qué orden se
// comprueba, qué se escribe y qué no— vive en internal/reanalisis y se prueba allí.
//
// 🔴 EL 403 DE FEATURE Y EL 422 DE CREDENCIAL SE VERIFICAN POR SEPARADO, que es
// criterio literal de T4.6: uno manda a la UI al paywall del add-on y el otro a los
// ajustes de `tenant-llm`. Un tenant que YA PAGÓ no puede acabar mirando una pantalla
// de venta.
//
// 🔴 NINGUNO DE ESTOS TESTS SE SALTA: el servicio es un doble y no hace falta
// Postgres.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/reanalisis"
)

// intakePorReanalizar es el pedido de la escena.
const intakePorReanalizar = "77777777-7777-7777-7777-777777777777"

// jobDelReanalisis es el id que devuelve el servicio en el camino feliz.
const jobDelReanalisis = "aaaa1111-2222-3333-4444-555566667777"

// reanalisisFalso es el doble del caso de uso: devuelve lo que el test le ponga y
// GUARDA la solicitud que recibió, que es como se comprueba que el handler tradujo
// bien el cuerpo (y que el tenant salió del TOKEN, nunca de la URL).
type reanalisisFalso struct {
	out      reanalisis.Resultado
	err      error
	recibido reanalisis.Solicitud
	llamadas int
}

func (r *reanalisisFalso) Reanalizar(_ context.Context, req reanalisis.Solicitud) (reanalisis.Resultado, error) {
	r.llamadas++
	r.recibido = req
	return r.out, r.err
}

// depsReanalizar arma unas Deps con el doble montado. El resolver de features va con
// `cart_basic` a propósito: esta ruta NO lleva gate en la cadena de middlewares —el
// suyo vive dentro del servicio— así que lo que el fake tenga encendido es
// indiferente, y ponerle justo la feature que NO usa deja constancia de ello.
func depsReanalizar(svc publicapi.ReanalysisService) publicapi.Deps {
	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureCartBasic)
	return publicapi.Deps{
		Intakes:      intakes.NewService(intakes.NewMemoryStore()),
		Entitlements: fake,
		Reanalysis:   svc,
	}
}

func reanalizar(t *testing.T, api *testAPI, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+id+"/reanalyze", body)
}

// cuerpoDe decodifica la respuesta a un mapa para poder afirmar clave a clave, que
// es lo que un contract test tiene que hacer: un struct tipado se tragaría un campo
// de más sin decir nada.
func cuerpoDe(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("la respuesta no es JSON: %v; body=%s", err, rec.Body.String())
	}
	return m
}

// exigeCodigoYCuerpo es la afirmación compartida de los seis desenlaces.
func exigeCodigoYCuerpo(t *testing.T, rec *httptest.ResponseRecorder, code int, quiero map[string]any) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("code=%d, quiero %d; body=%s", rec.Code, code, rec.Body.String())
	}
	got := cuerpoDe(t, rec)
	for k, v := range quiero {
		if got[k] != v {
			t.Fatalf("cuerpo[%q]=%v, quiero %v; body=%s", k, got[k], v, rec.Body.String())
		}
	}
}

// ---------------------------------------------------------------------------
// 200
// ---------------------------------------------------------------------------

// TestReanalyze_200_DevuelveElAcuseYNoElDetalle: el contrato §8.1 publica un ACUSE,
// no la solicitud.
//
// 🔴 Y NO ES UNA ASIMETRÍA GRATUITA respecto de `approve`/`request-info`, que sí
// responden el detalle: cuando este handler contesta, la revisión nueva TODAVÍA NO
// EXISTE —la escribirá el pipeline minutos después—, así que devolver el detalle
// enseñaría el estado viejo y una consola que repintara con él creería que no pasó
// nada.
func TestReanalyze_200_DevuelveElAcuseYNoElDetalle(t *testing.T) {
	svc := &reanalisisFalso{out: reanalisis.Resultado{
		IntakeID: intakePorReanalizar, RevisionNo: 3, JobID: jobDelReanalisis,
		Via: "local", Status: reanalisis.EstadoEnCurso,
	}}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{"via":"local","text":"son 30 tequeños"}`)

	exigeCodigoYCuerpo(t, rec, http.StatusOK, map[string]any{
		"intake_id":   intakePorReanalizar,
		"revision_no": float64(3),
		"job_id":      jobDelReanalisis,
		"via":         "local",
		"status":      "processing",
	})
	// El cuerpo llegó entero al dominio, y el tenant salió del TOKEN (INV-7).
	if svc.recibido.TenantID != tenantA {
		t.Fatalf("tenant=%q, quiero el del token (%q)", svc.recibido.TenantID, tenantA)
	}
	if svc.recibido.IntakeID != intakePorReanalizar {
		t.Fatalf("intake=%q, quiero %q", svc.recibido.IntakeID, intakePorReanalizar)
	}
	if svc.recibido.Via != "local" || svc.recibido.Text != "son 30 tequeños" {
		t.Fatalf("cuerpo mal traducido: %+v", svc.recibido)
	}
	// El acuse NO lleva el detalle: ni líneas, ni revisiones, ni estado del pedido.
	if got := cuerpoDe(t, rec); got["items"] != nil || got["revisions"] != nil || got["status"] == "pending_approval" {
		t.Fatalf("el 200 devolvió el detalle de la solicitud en vez del acuse: %s", rec.Body.String())
	}
}

// TestReanalyze_200_CuerpoVacio: los dos campos son OPCIONALES. `{}` es el caso de
// Jhoan —«regenera otra vez, según el origen»— y tiene que pasar entero.
func TestReanalyze_200_CuerpoVacio(t *testing.T) {
	svc := &reanalisisFalso{out: reanalisis.Resultado{
		IntakeID: intakePorReanalizar, RevisionNo: 2, JobID: jobDelReanalisis,
		Via: "local", Status: reanalisis.EstadoEnCurso,
	}}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if svc.recibido.Via != "" || svc.recibido.Text != "" {
		t.Fatalf("un cuerpo vacío no puede inventar campos: %+v", svc.recibido)
	}
}

// TestReanalyze_CampoProviderNoSeAceptaEnSilencio es el criterio que §8.1 dejó
// escrito para esta tarea: el campo viejo `provider` NO existe en el código —lo
// renombró T1.5-2 antes de que el endpoint naciera— así que no hay regresión que
// probar, pero SÍ hay que probar que un cuerpo con `provider` no se acepta como si
// fuera una vía.
//
// El desenlace correcto es el SEGURO: se ignora entero y la petición corre por la vía
// del tenant. Nunca «manda texto a un tercero por un campo que el servidor no
// conoce».
func TestReanalyze_CampoProviderNoSeAceptaEnSilencio(t *testing.T) {
	svc := &reanalisisFalso{out: reanalisis.Resultado{
		IntakeID: intakePorReanalizar, RevisionNo: 2, JobID: jobDelReanalisis,
		Via: "local", Status: reanalisis.EstadoEnCurso,
	}}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{"provider":"api"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	if svc.recibido.Via != "" {
		t.Fatalf("`provider` se coló como vía: %+v", svc.recibido)
	}
}

// ---------------------------------------------------------------------------
// LOS SEIS DESENLACES DEL §8.1
// ---------------------------------------------------------------------------

// TestReanalyze_400_InvalidVia: el rechazo de VOCABULARIO sale con la forma exacta
// del contrato — `error` y `via`, sin más.
func TestReanalyze_400_InvalidVia(t *testing.T) {
	svc := &reanalisisFalso{err: reanalisis.ViaInvalidaError{Via: "chatgpt"}}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{"via":"chatgpt"}`)

	exigeCodigoYCuerpo(t, rec, http.StatusBadRequest, map[string]any{
		"error": "invalid_via", "via": "chatgpt",
	})
	if _, hay := cuerpoDe(t, rec)["configured_via"]; hay {
		t.Fatal("el rechazo de vocabulario no debe hablar de la vía configurada")
	}
}

// TestReanalyze_400_InvalidVia_ContradiceLaConfigurada: el otro rechazo, con el campo
// ADITIVO que le dice a la UI cuál SÍ vale. Sin él, el usuario tendría que ir a otra
// pantalla a averiguarlo.
func TestReanalyze_400_InvalidVia_ContradiceLaConfigurada(t *testing.T) {
	svc := &reanalisisFalso{err: reanalisis.ViaInvalidaError{Via: "local", Configurada: "api"}}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{"via":"local"}`)

	exigeCodigoYCuerpo(t, rec, http.StatusBadRequest, map[string]any{
		"error": "invalid_via", "via": "local", "configured_via": "api",
	})
}

// TestReanalyze_403_SinLLMIntake es el gate BASE: el nivel que se paga.
func TestReanalyze_403_SinLLMIntake(t *testing.T) {
	svc := &reanalisisFalso{err: reanalisis.FeatureAusenteError{Feature: entitlements.FeatureLLMIntake}}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{}`)

	exigeCodigoYCuerpo(t, rec, http.StatusForbidden, map[string]any{
		"error": "feature_not_enabled", "feature": "llm_intake",
	})
}

// TestReanalyze_403_SinAPILLM es el gate de la VÍA, y es OTRO caso: mismo código de
// error, OTRA feature. La UI ofrece un add-on distinto con cada uno.
func TestReanalyze_403_SinAPILLM(t *testing.T) {
	svc := &reanalisisFalso{err: reanalisis.FeatureAusenteError{Feature: entitlements.FeatureAPILLM}}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{"via":"api"}`)

	exigeCodigoYCuerpo(t, rec, http.StatusForbidden, map[string]any{
		"error": "feature_not_enabled", "feature": "api_llm",
	})
}

// TestReanalyze_422_CredencialAusente es el criterio explícito de T4.6: tenant CON
// feature y SIN credencial ⇒ 422, NUNCA 403.
//
// El cuerpo es DISTINTO del 403 y por eso se afirma entero: lleva `via` en vez de
// `feature`, porque lo que falta es una credencial de esa vía y no un derecho.
func TestReanalyze_422_CredencialAusente(t *testing.T) {
	svc := &reanalisisFalso{err: reanalisis.CredencialAusenteError{Via: "api"}}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{"via":"api"}`)

	exigeCodigoYCuerpo(t, rec, http.StatusUnprocessableEntity, map[string]any{
		"error": "llm_credentials_missing", "via": "api",
	})
	if got := cuerpoDe(t, rec); got["feature"] != nil {
		t.Fatal("el 422 de credencial NO puede llevar `feature`: es el cuerpo del paywall, y son dos casos distintos")
	}
}

// TestReanalyze_422_SourceUnavailable recorre las DOS razones. Son dos mensajes
// distintos para el dueño y por eso la razón viaja en el cuerpo.
func TestReanalyze_422_SourceUnavailable(t *testing.T) {
	for _, razon := range []string{reanalisis.RazonPurgada, reanalisis.RazonNuncaGuardada} {
		t.Run(razon, func(t *testing.T) {
			svc := &reanalisisFalso{err: reanalisis.FuenteAusenteError{Reason: razon}}
			api := newAPI(depsReanalizar(svc), intakesKeys())

			rec := reanalizar(t, api, intakePorReanalizar, `{}`)

			exigeCodigoYCuerpo(t, rec, http.StatusUnprocessableEntity, map[string]any{
				"error": "source_unavailable", "reason": razon,
			})
		})
	}
}

// TestReanalyze_422_ReanalysisInProgress: la concurrencia de D-044.15. El cuerpo
// lleva el job para poder seguirlo en vez de reintentar a ciegas.
func TestReanalyze_422_ReanalysisInProgress(t *testing.T) {
	svc := &reanalisisFalso{err: reanalisis.EnCursoError{JobID: jobDelReanalisis}}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{}`)

	exigeCodigoYCuerpo(t, rec, http.StatusUnprocessableEntity, map[string]any{
		"error": "reanalysis_in_progress", "job_id": jobDelReanalisis,
	})
}

// TestReanalyze_404_SolicitudAjena: 404 y NUNCA 403 — un 403 confirmaría que el id
// existe. «No existe» y «es de otro tenant» son la misma respuesta (INV-8).
func TestReanalyze_404_SolicitudAjena(t *testing.T) {
	svc := &reanalisisFalso{err: intakes.ErrNotFound}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestReanalyze_400_TextoDemasiadoLargo: el `text` comparte puerta de saneo con la
// indicación del cliente (cart.SanitizeNote, 280 runas) y se RECHAZA en vez de
// truncar — recortar «…y sin maní» pierde el final, y el final es donde va el
// alérgeno (REQ-33e).
func TestReanalyze_400_TextoDemasiadoLargo(t *testing.T) {
	svc := &reanalisisFalso{err: cart.NoteTooLongError{Runes: 312, Max: cart.MaxNoteRunes}}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{"text":"`+strings.Repeat("a", 312)+`"}`)

	exigeCodigoYCuerpo(t, rec, http.StatusBadRequest, map[string]any{
		"error": "text_too_long", "runes": float64(312), "max": float64(cart.MaxNoteRunes),
	})
}

// TestReanalyze_500_SoloAnteLoDesconocido: el `default` existe y responde 500 con un
// cuerpo genérico, sin filtrar el error interno.
//
// 🔴 Y NO SE ALCANZA CON EL PROVEEDOR CAÍDO, que es el criterio INV-10 de T4.6: este
// endpoint no llama al modelo — abre un job y vuelve—, así que un proveedor muerto se
// descubre después, en el worker. Por aquí solo se cae al 500 si se cae Postgres.
func TestReanalyze_500_SoloAnteLoDesconocido(t *testing.T) {
	svc := &reanalisisFalso{err: desconocidoError{}}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d, quiero 500; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secreto-de-la-base") {
		t.Fatalf("el 500 filtró el error interno: %s", rec.Body.String())
	}
}

type desconocidoError struct{}

func (desconocidoError) Error() string { return "conexión rechazada: secreto-de-la-base" }

// ---------------------------------------------------------------------------
// EL MONTAJE
// ---------------------------------------------------------------------------

// TestReanalyze_SinServicio_NoSeMonta: sin el caso de uso cableado la ruta no existe.
// Es preferible un 404 de ruta inexistente a una puerta que responde 500 a medio
// camino — mismo criterio que el resto de registerIntakes.
func TestReanalyze_SinServicio_NoSeMonta(t *testing.T) {
	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureCartBasic)
	api := newAPI(publicapi.Deps{
		Intakes:      intakes.NewService(intakes.NewMemoryStore()),
		Entitlements: fake,
	}, intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404 de ruta inexistente; body=%s", rec.Code, rec.Body.String())
	}
}

// TestReanalyze_SinToken_401: la ruta va detrás de Authenticate como todas.
func TestReanalyze_SinToken_401(t *testing.T) {
	svc := &reanalisisFalso{}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := call(api, "", http.MethodPost, "/api/v1/intakes/"+intakePorReanalizar+"/reanalyze", `{}`)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401; body=%s", rec.Code, rec.Body.String())
	}
	if svc.llamadas != 0 {
		t.Fatal("una petición sin token llegó al caso de uso")
	}
}

// TestReanalyze_CuerpoNoJSON_400: un cuerpo roto se rechaza antes de llegar al
// dominio.
func TestReanalyze_CuerpoNoJSON_400(t *testing.T) {
	svc := &reanalisisFalso{}
	api := newAPI(depsReanalizar(svc), intakesKeys())

	rec := reanalizar(t, api, intakePorReanalizar, `{no soy json`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	if svc.llamadas != 0 {
		t.Fatal("un cuerpo inválido llegó al caso de uso")
	}
}
