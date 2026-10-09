package apipublica_test

// intents_put_test.go — cubre E2 (PUT /api/v1/intents) del contrato de intents.go: el gate, los
// pasos del cuerpo en su orden, la version de entidad, la persistencia y el push best-effort.
// Los dobles y E1 viven en intents_test.go (05 E-13).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	sharedintents "github.com/EduGoGroup/wapp-shared/intents"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
)

// intentsVersionOf calcula la version de entidad que promete el contrato: los 12 primeros hex
// del sha256 del JSON re-serializado desde su forma decodificada.
func intentsVersionOf(t *testing.T, body string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("el cuerpo de prueba no es JSON: %v", err)
	}
	norm, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("re-serializando el cuerpo de prueba: %v", err)
	}
	sum := sha256.Sum256(norm)
	return hex.EncodeToString(sum[:])[:12]
}

// intentsVersionIn saca la version de un 200 de E2, exigiendo que sea lo ÚNICO del cuerpo.
func intentsVersionIn(t *testing.T, what string, rec *httptest.ResponseRecorder) string {
	t.Helper()
	wantCode(t, what, rec, http.StatusOK)
	var got map[string]string
	wantJSON(t, what, rec, &got)
	if len(got) != 1 || got["version"] == "" {
		t.Fatalf("%s: cuerpo %v, quiero solo {\"version\": …}", what, got)
	}
	return got["version"]
}

// intentsWantUntouched exige que un PUT cortado no haya escrito ni empujado nada.
func intentsWantUntouched(t *testing.T, what string, rig intentsRig) {
	t.Helper()
	if rig.store.upserts != 0 || rig.pusher.calls != 0 {
		t.Errorf("%s: llegó a Upsert %d veces y a PushConfig %d; quiero 0 y 0", what, rig.store.upserts, rig.pusher.calls)
	}
	if _, ok := intentsStored(t, rig.store, tenantA); ok {
		t.Errorf("%s: quedó una config guardada", what)
	}
}

// intentsWantOneAudit exige EXACTAMENTE un registro de auditoría de E2 con ese resultado.
func intentsWantOneAudit(t *testing.T, what string, h *apipublicahelpertest.Harness, result string, status int) {
	t.Helper()
	records := h.Auditor().Records()
	if len(records) != 1 {
		t.Fatalf("%s: E2 dejó %d registros de auditoría, quiero exactamente 1", what, len(records))
	}
	r := records[0]
	if r.TenantID != tenantA || r.Action != intentsWritePerm || r.Resource != intentsResource || r.Result != result || r.Meta["status"] != status {
		t.Errorf("%s: registro %+v, quiero tenant %s, action %s, resource %s, result %s, status %d",
			what, r, tenantA, intentsWritePerm, intentsResource, result, status)
	}
}

// intentsBodyOfSize arma un contrato VÁLIDO de exactamente n bytes, rellenando la descripción.
func intentsBodyOfSize(n int) string {
	const head = `{"version":"v1","intents":[{"name":"pedir_pizza","descripcion":"`
	const tail = `","ejemplos":[{"mensaje":"quiero una pizza"}]}]}`
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}

// TestMountIntents_PutPersistsAndPushes: guarda bajo el tenant del token el cuerpo BYTE A BYTE
// con la version que devuelve, y empuja (tenant, intentcfg.Kind, version, cuerpo) una vez.
func TestMountIntents_PutPersistsAndPushes(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store, pusher := newIntentsStore(), &intentsPusherSpy{}
	// Con DBTimeout cableado: el plazo es de E1 y no debe alcanzar a Upsert.
	cara := intentsCara(h.Common(), apipublica.IntentsDeps{
		Intents: store, Entitlements: withFeatures(entitlements.FeatureLLMIntent), ConfigPush: pusher, DBTimeout: 5 * time.Second,
	})
	// Espacios y orden de claves propios: lo guardado es lo que llegó, no lo normalizado.
	body := ` { "intents":[{"name":"pedir_pizza","descripcion":"pedir comida","ejemplos":[{"mensaje":"quiero una pizza"}]}], "version":"v1" } `

	rec := h.Call(cara, h.With(tenantA, intentsWritePerm), http.MethodPut, intentsTarget, body)
	version := intentsVersionIn(t, "E2", rec)
	wantExactBody(t, "E2", rec, `{"version":"`+version+`"}`)
	if want := intentsVersionOf(t, body); version != want {
		t.Errorf("version %q, quiero %q (sha256 del JSON normalizado, 12 hex)", version, want)
	}
	got, ok := intentsStored(t, store, tenantA)
	if !ok || got.Version != version || string(got.Blob) != body {
		t.Errorf("guardado %q con version %q (hay=%v); quiero el cuerpo tal cual con la version %q", got.Blob, got.Version, ok, version)
	}
	if store.upserts != 1 || store.tenant != tenantA || store.upsertRemaining != -1 {
		t.Errorf("Upsert: %d llamadas, tenant %q, plazo %s; quiero 1, %q y -1 (sin plazo propio)", store.upserts, store.tenant, store.upsertRemaining, tenantA)
	}
	if pusher.calls != 1 || pusher.tenant != tenantA || pusher.kind != intentcfg.Kind || pusher.version != version || string(pusher.payload) != body {
		t.Errorf("PushConfig: %+v; quiero 1 llamada con (%s, %s, %s, el cuerpo tal cual)", pusher, tenantA, intentcfg.Kind, version)
	}
	intentsWantOneAudit(t, "E2", h, "success", http.StatusOK)
}

// TestMountIntents_PutVersionIsOfTheNormalizedJSON: dos cuerpos con el mismo contenido lógico
// dan la misma version; otro contenido, otra.
func TestMountIntents_PutVersionIsOfTheNormalizedJSON(t *testing.T) {
	reordered := `{
		"intents": [ {"ejemplos":[{"mensaje":"quiero una pizza"}], "params":["cantidad"], "descripcion":"pedir comida", "name":"pedir_pizza"} ],
		"umbral_confianza": 0.7,
		"version": "v1"
	}`
	rig := newIntentsRig(t, nil)
	base := intentsVersionIn(t, "cuerpo base", rig.put(intentsValidBody))
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(base) {
		t.Errorf("version %q, quiero 12 hex en minúsculas", base)
	}
	if got := intentsVersionIn(t, "claves reordenadas y espacios", rig.put(reordered)); got != base {
		t.Errorf("el mismo contenido con otro orden dio %q, quiero %q", got, base)
	}
	if got := intentsVersionIn(t, "otro contenido", rig.put(intentsBodyWithEventKind)); got == base {
		t.Errorf("otro contenido dio la misma version %q", got)
	}
}

// TestMountIntents_PutGate: el gate `llm_intent` va dentro del handler y ANTES de leer el
// cuerpo. Sin la feature es 403 (también con un blob inválido o enorme); con el resolver caído,
// 500. Ninguno persiste ni empuja, y los dos quedan auditados.
func TestMountIntents_PutGate(t *testing.T) {
	onlyOthers := entitlementshelpertest.NewFake()
	for _, feature := range []string{entitlements.FeatureCartBasic, entitlements.FeatureLLMIntake, entitlements.FeatureAPILLM} {
		onlyOthers.Enable(tenantA, feature)
	}
	onlyTenantB := entitlementshelpertest.NewFake()
	onlyTenantB.Enable(tenantB, entitlements.FeatureLLMIntent)
	disabled := entitlementshelpertest.NewFake()
	disabled.Disable(tenantA, entitlements.FeatureLLMIntent)
	failing := func() entitlements.Resolver {
		return &entitlementshelpertest.Fake{Err: errors.New("postgres://usuario:ficticio@host/bd: caído")}
	}
	oversized := strings.Repeat("x", sharedintents.MaxConfigBytes+1)

	cases := []struct {
		name     string
		resolver entitlements.Resolver
		body     string
		code     int
		exact    string
	}{
		{"no_feature", entitlementshelpertest.NewFake(), intentsValidBody, http.StatusForbidden, `{"error":"` + intentsMsgNoFeature + `"}`},
		{"other_features_do_not_open_it", onlyOthers, intentsValidBody, http.StatusForbidden, `{"error":"` + intentsMsgNoFeature + `"}`},
		{"feature_of_another_tenant", onlyTenantB, intentsValidBody, http.StatusForbidden, `{"error":"` + intentsMsgNoFeature + `"}`},
		{"feature_switched_off", disabled, intentsValidBody, http.StatusForbidden, `{"error":"` + intentsMsgNoFeature + `"}`},
		{"no_feature_wins_over_invalid_body", entitlementshelpertest.NewFake(), `esto no es json`, http.StatusForbidden, `{"error":"` + intentsMsgNoFeature + `"}`},
		{"no_feature_wins_over_oversized_body", entitlementshelpertest.NewFake(), oversized, http.StatusForbidden, `{"error":"` + intentsMsgNoFeature + `"}`},
		{"resolver_error_is_500", failing(), intentsValidBody, http.StatusInternalServerError, `{"error":"` + intentsMsgGateFail + `"}`},
		{"resolver_error_wins_over_invalid_body", failing(), `esto no es json`, http.StatusInternalServerError, `{"error":"` + intentsMsgGateFail + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newIntentsRig(t, tc.resolver)
			rec := rig.put(tc.body)
			wantCode(t, tc.name, rec, tc.code)
			wantExactBody(t, tc.name, rec, tc.exact)
			intentsWantUntouched(t, tc.name, rig)
			intentsWantOneAudit(t, tc.name, rig.h, "failure", tc.code)
		})
	}
}

// TestMountIntents_PutBodyCeiling: 256 KiB justos entran; un byte más es el 413 con max_bytes.
func TestMountIntents_PutBodyCeiling(t *testing.T) {
	rig := newIntentsRig(t, nil)
	rec := rig.put(intentsBodyOfSize(sharedintents.MaxConfigBytes + 1))
	wantCode(t, "256 KiB + 1", rec, http.StatusRequestEntityTooLarge)
	wantExactBody(t, "256 KiB + 1", rec, `{"error":"la config excede el tamaño máximo de 262144 bytes","max_bytes":262144}`)
	intentsWantUntouched(t, "256 KiB + 1", rig)

	// El techo se mira ANTES que el contrato: un cuerpo enorme e inválido es 413, no 400.
	rec = rig.put(strings.Repeat("x", sharedintents.MaxConfigBytes+1))
	wantCode(t, "enorme e inválido", rec, http.StatusRequestEntityTooLarge)

	exact := intentsBodyOfSize(sharedintents.MaxConfigBytes)
	if len(exact) != 262144 {
		t.Fatalf("el cuerpo de prueba mide %d bytes, quiero 262144", len(exact))
	}
	intentsVersionIn(t, "256 KiB justos", rig.put(exact))
}

// intentsBrokenBody es un cuerpo cuya lectura falla.
type intentsBrokenBody struct{}

func (intentsBrokenBody) Read([]byte) (int, error) { return 0, errors.New("conexión cortada") }

// TestMountIntents_PutUnreadableBody: si la lectura del cuerpo falla, 400 y nada guardado.
func TestMountIntents_PutUnreadableBody(t *testing.T) {
	rig := newIntentsRig(t, nil)
	req := httptest.NewRequest(http.MethodPut, intentsTarget, intentsBrokenBody{})
	req.Header.Set("Authorization", "Bearer "+rig.h.With(tenantA, intentsWritePerm))
	rec := httptest.NewRecorder()
	rig.cara.ServeHTTP(rec, req)
	wantCode(t, "cuerpo ilegible", rec, http.StatusBadRequest)
	wantErrorBody(t, "cuerpo ilegible", rec, intentsMsgBodyRead)
	intentsWantUntouched(t, "cuerpo ilegible", rig)
}

// TestMountIntents_PutRejectsInvalidConfig: lo que el validador del contrato rechaza es un 400
// que repite SU mensaje, y un cuerpo rechazado no toca lo guardado ni empuja.
func TestMountIntents_PutRejectsInvalidConfig(t *testing.T) {
	cases := map[string]string{
		"not_json":               `esto no es json`,
		"empty_body":             ``,
		"json_null":              `null`,
		"trailing_garbage":       intentsValidBody + ` basura`,
		"no_intents":             `{"version":"v1","intents":[]}`,
		"empty_version":          strings.Replace(intentsValidBody, `"version":"v1"`, `"version":" "`, 1),
		"threshold_out_of_range": strings.Replace(intentsValidBody, `0.7`, `1.5`, 1),
		"bad_intent_name":        strings.Replace(intentsValidBody, `pedir_pizza`, `Pedir-Pizza`, 1),
		"reserved_intent_name":   strings.Replace(intentsValidBody, `pedir_pizza`, sharedintents.ReservedUnknown, 1),
		"intent_without_example": strings.Replace(intentsValidBody, `[{"mensaje":"quiero una pizza"}]`, `[]`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, verr := sharedintents.ParseAndValidate([]byte(body))
			if verr == nil {
				t.Fatalf("el caso %s no es inválido para el validador: el test no prueba nada", name)
			}
			rig := newIntentsRig(t, nil)
			intentsSeed(t, rig.store, tenantA, "v-previa", `{"previa":true}`)
			rec := rig.put(body)
			wantCode(t, name, rec, http.StatusBadRequest)
			wantErrorBody(t, name, rec, intentsMsgInvalid+verr.Error())
			if rig.store.upserts != 0 || rig.pusher.calls != 0 {
				t.Errorf("%s: un cuerpo rechazado llegó a Upsert %d veces y a PushConfig %d", name, rig.store.upserts, rig.pusher.calls)
			}
			if got, _ := intentsStored(t, rig.store, tenantA); got.Version != "v-previa" || string(got.Blob) != `{"previa":true}` {
				t.Errorf("%s: un cuerpo rechazado alteró lo guardado: %+v", name, got)
			}
		})
	}
}

// TestMountIntents_PutStoreErrorIs500: si Upsert falla, 500 sin repetir el error y SIN empujar.
func TestMountIntents_PutStoreErrorIs500(t *testing.T) {
	rig := newIntentsRig(t, nil)
	rig.store.upsertErr = errors.New("postgres://usuario:ficticio@host/bd: conexión rechazada")
	rec := rig.put(intentsValidBody)
	wantCode(t, "Upsert falla", rec, http.StatusInternalServerError)
	wantErrorBody(t, "Upsert falla", rec, intentsMsgSaveFail)
	if rig.store.upserts != 1 || rig.pusher.calls != 0 {
		t.Errorf("Upsert %d veces y PushConfig %d; quiero 1 y 0: lo que no se guardó no se empuja", rig.store.upserts, rig.pusher.calls)
	}
	intentsWantOneAudit(t, "Upsert falla", rig.h, "failure", http.StatusInternalServerError)
}

// TestMountIntents_PutWithoutPusher: sin pusher cableado, E2 persiste y responde igual.
func TestMountIntents_PutWithoutPusher(t *testing.T) {
	h := apipublicahelpertest.New(t)
	store := newIntentsStore()
	cara := intentsCara(h.Common(), apipublica.IntentsDeps{Intents: store, Entitlements: withFeatures(entitlements.FeatureLLMIntent)})
	version := intentsVersionIn(t, "sin pusher", h.Call(cara, h.With(tenantA, intentsWritePerm), http.MethodPut, intentsTarget, intentsValidBody))
	if got, ok := intentsStored(t, store, tenantA); !ok || got.Version != version {
		t.Errorf("sin pusher no quedó guardada la version %q: %+v (hay=%v)", version, got, ok)
	}
}

// TestMountIntents_PutPusherErrorIsStill200: el push es best-effort. Su fallo no cambia la
// respuesta ni deshace lo guardado: solo deja un Warn (y, sin logger, ni eso).
func TestMountIntents_PutPusherErrorIsStill200(t *testing.T) {
	pushErr := errors.New("ninguna sesión viva")
	t.Run("warns_and_answers_200", func(t *testing.T) {
		rig := newIntentsRig(t, nil)
		rig.pusher.err = pushErr
		version := intentsVersionIn(t, "push fallido", rig.put(intentsValidBody))
		if got, ok := intentsStored(t, rig.store, tenantA); !ok || got.Version != version {
			t.Errorf("el fallo del push deshizo lo guardado: %+v (hay=%v)", got, ok)
		}
		var warns []apipublicahelpertest.LogEntry
		for _, e := range rig.h.Log().Entries() {
			if e.Level == "warn" {
				warns = append(warns, e)
			}
		}
		if len(warns) != 1 || warns[0].Msg != intentsMsgPushWarn {
			t.Fatalf("avisos %+v; quiero exactamente uno con el mensaje %q", warns, intentsMsgPushWarn)
		}
		f := warns[0].Fields
		if err, ok := f["error"].(error); !ok || !errors.Is(err, pushErr) || f["tenant_id"] != tenantA || f["version"] != version || len(f) != 3 {
			t.Errorf("campos del aviso %v; quiero tenant_id, version y error, y nada más", f)
		}
		intentsWantOneAudit(t, "push fallido", rig.h, "success", http.StatusOK)
	})
	t.Run("nil_log_still_answers_200", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		cara := intentsCara(apipublica.Common{MW: h.MW()}, apipublica.IntentsDeps{
			Intents: newIntentsStore(), Entitlements: withFeatures(entitlements.FeatureLLMIntent), ConfigPush: &intentsPusherSpy{err: pushErr},
		})
		intentsVersionIn(t, "push fallido sin logger", h.Call(cara, h.With(tenantA, intentsWritePerm), http.MethodPut, intentsTarget, intentsValidBody))
	})
	t.Run("no_warn_when_the_push_works", func(t *testing.T) {
		rig := newIntentsRig(t, nil)
		intentsVersionIn(t, "push bueno", rig.put(intentsValidBody))
		for _, e := range rig.h.Log().Entries() {
			if e.Level == "warn" {
				t.Errorf("un push que funciona dejó un aviso: %+v", e)
			}
		}
	})
}

// TestMountIntents_PutPushUsesTheRequestContext: conducta heredada. El push va con el contexto
// de la PETICIÓN, no con uno desligado: cancelada la petición, el contexto del push muere.
func TestMountIntents_PutPushUsesTheRequestContext(t *testing.T) {
	rig := newIntentsRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPut, intentsTarget, strings.NewReader(intentsValidBody)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+rig.h.With(tenantA, intentsWritePerm))
	rec := httptest.NewRecorder()
	rig.cara.ServeHTTP(rec, req)
	wantCode(t, "E2", rec, http.StatusOK)
	if rig.pusher.ctx == nil || rig.pusher.ctx.Err() != nil {
		t.Fatalf("el push no recibió un contexto vivo durante la petición: %v", rig.pusher.ctx)
	}
	cancel()
	if rig.pusher.ctx.Err() == nil {
		t.Error("cancelada la petición, el contexto del push sigue vivo: el push no va con el contexto de la petición")
	}
}

// intentsConfigOf lee un 200 de E1 y devuelve su version y su `config` decodificado.
func intentsConfigOf(t *testing.T, what string, rec *httptest.ResponseRecorder) (string, any) {
	t.Helper()
	wantCode(t, what, rec, http.StatusOK)
	var got struct {
		Version string          `json:"version"`
		Config  json.RawMessage `json:"config"`
	}
	wantJSON(t, what, rec, &got)
	var config any
	if err := json.Unmarshal(got.Config, &config); err != nil {
		t.Fatalf("%s: config ilegible: %v (%s)", what, err, got.Config)
	}
	return got.Version, config
}

// intentsDecoded decodifica un cuerpo de prueba para compararlo por equivalencia JSON.
func intentsDecoded(t *testing.T, body string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("el cuerpo de prueba no es JSON: %v", err)
	}
	return v
}

// TestMountIntents_GetAfterPut: lo que E2 guarda es lo que E1 devuelve, con la misma version.
func TestMountIntents_GetAfterPut(t *testing.T) {
	rig := newIntentsRig(t, nil)
	wantCode(t, "E1 antes del PUT", rig.get(), http.StatusNotFound)
	version := intentsVersionIn(t, "E2", rig.put(intentsValidBody))
	gotVersion, config := intentsConfigOf(t, "E1 tras el PUT", rig.get())
	if gotVersion != version {
		t.Errorf("E1 da la version %q y E2 devolvió %q", gotVersion, version)
	}
	if !reflect.DeepEqual(config, intentsDecoded(t, intentsValidBody)) {
		t.Errorf("E1 devuelve %v, que no equivale al cuerpo del PUT", config)
	}
}

// TestMountIntents_EventKindIsAdditive (Plan 043 · T5.3, D-043.9): un blob sin `event_kind` y
// uno que lo trae por intent validan los dos, y el segundo se guarda con el campo intacto. Que
// sea inerte en el Cloud no se puede probar desde aquí: la cara no lo lee.
func TestMountIntents_EventKindIsAdditive(t *testing.T) {
	for name, body := range map[string]string{
		"blob_without_event_kind": intentsValidBody,
		"blob_with_event_kind":    intentsBodyWithEventKind,
	} {
		t.Run(name, func(t *testing.T) {
			rig := newIntentsRig(t, nil)
			version := intentsVersionIn(t, name, rig.put(body))
			gotVersion, config := intentsConfigOf(t, name, rig.get())
			if gotVersion != version {
				t.Errorf("%s: E1 da la version %q y E2 devolvió %q", name, gotVersion, version)
			}
			if !reflect.DeepEqual(config, intentsDecoded(t, body)) {
				t.Errorf("%s: E1 devuelve %v, que no equivale al cuerpo del PUT", name, config)
			}
		})
	}
	t.Run("event_kind_survives_in_the_stored_blob", func(t *testing.T) {
		rig := newIntentsRig(t, nil)
		intentsVersionIn(t, "con event_kind", rig.put(intentsBodyWithEventKind))
		var got struct {
			Config struct {
				Intents []struct {
					EventKind string `json:"event_kind"`
				} `json:"intents"`
			} `json:"config"`
		}
		rec := rig.get()
		wantJSON(t, "con event_kind", rec, &got)
		if len(got.Config.Intents) != 1 || got.Config.Intents[0].EventKind != "cart" {
			t.Errorf("E1 devuelve %s; quiero que el intent conserve event_kind = cart", rec.Body.String())
		}
	})
}
