package apipublicahelpertest_test

// harness_test.go — el arnés tiene lógica (firma de tokens, grabación de auditoría y de log) y
// por eso lleva su test (05 E-3, fila «Dobles de test»). Nace verde con él.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// whoami es una cara con una ruta autenticada que devuelve la identidad, y otra que además
// exige el permiso "x.read".
func whoami(h *apipublicahelpertest.Harness) *apipublica.Cara {
	c := apipublica.Nueva()
	c.Handle("/whoami", h.MW().Authenticate(httpapi.WhoAmIHandler()))
	c.Handle("GET /needs", h.MW().Authenticate(h.MW().RequirePermission("x.read")(httpapi.WhoAmIHandler())))
	return c
}

type identityWire struct {
	TenantID string   `json:"tenant_id"`
	Subject  string   `json:"subject"`
	Roles    []string `json:"roles"`
}

func decodeIdentity(t *testing.T, body []byte) identityWire {
	t.Helper()
	var out identityWire
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("cuerpo de whoami ilegible: %v (%s)", err, body)
	}
	return out
}

func TestHarness_WithSignsATokenTheMiddlewareAccepts(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := h.Call(whoami(h), h.With(apipublicahelpertest.TenantA), http.MethodGet, "/whoami", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("whoami con token de With: código %d, quiero 200 (%s)", rec.Code, rec.Body)
	}
	got := decodeIdentity(t, rec.Body.Bytes())
	if got.TenantID != apipublicahelpertest.TenantA || got.Subject != apipublicahelpertest.Subject {
		t.Errorf("identidad = %+v, quiero tenant %s y subject %s", got, apipublicahelpertest.TenantA, apipublicahelpertest.Subject)
	}
}

func TestHarness_WithSubjectSignsForThatPerson(t *testing.T) {
	h := apipublicahelpertest.New(t)
	rec := h.Call(whoami(h), h.WithSubject("otra", apipublicahelpertest.TenantB), http.MethodGet, "/whoami", "")
	got := decodeIdentity(t, rec.Body.Bytes())
	if got.Subject != "otra" || got.TenantID != apipublicahelpertest.TenantB {
		t.Errorf("identidad = %+v, quiero subject otra en %s", got, apipublicahelpertest.TenantB)
	}
}

func TestHarness_GrantsDecidePermission(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := whoami(h)
	cases := []struct {
		name   string
		grants []string
		want   int
	}{
		{"exact_grant", []string{"x.read"}, http.StatusOK},
		{"glob_grant", []string{"x.*"}, http.StatusOK},
		{"other_grant", []string{"y.read"}, http.StatusForbidden},
		{"no_grants", nil, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.Call(cara, h.With(apipublicahelpertest.TenantA, tc.grants...), http.MethodGet, "/needs", "")
			if rec.Code != tc.want {
				t.Errorf("grants %v: código %d, quiero %d", tc.grants, rec.Code, tc.want)
			}
		})
	}
}

func TestHarness_TenantlessHasNoTenantNorGrants(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := whoami(h)
	tok := h.Tenantless("sin-empresa")
	got := decodeIdentity(t, h.Call(cara, tok, http.MethodGet, "/whoami", "").Body.Bytes())
	if got.TenantID != "" || got.Subject != "sin-empresa" {
		t.Errorf("identidad = %+v, quiero tenant vacío y subject sin-empresa", got)
	}
	if code := h.Call(cara, tok, http.MethodGet, "/needs", "").Code; code != http.StatusForbidden {
		t.Errorf("token sin empresa en ruta con permiso: código %d, quiero 403", code)
	}
}

func TestHarness_CallWithoutCredentialSendsNoAuthorization(t *testing.T) {
	h := apipublicahelpertest.New(t)
	if code := h.Call(whoami(h), "", http.MethodGet, "/whoami", "").Code; code != http.StatusUnauthorized {
		t.Errorf("sin credencial: código %d, quiero 401", code)
	}
	if code := h.Call(whoami(h), "no-es-un-token", http.MethodGet, "/whoami", "").Code; code != http.StatusUnauthorized {
		t.Errorf("credencial basura: código %d, quiero 401", code)
	}
}

func TestHarness_CallSendsMethodTargetAndBody(t *testing.T) {
	h := apipublicahelpertest.New(t)
	var gotMethod, gotQuery, gotType string
	var gotBody map[string]string
	c := apipublica.Nueva()
	c.Handle("/echo", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotQuery, gotType = r.Method, r.URL.RawQuery, r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("cuerpo ilegible: %v", err)
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := h.Call(c, "", http.MethodPut, "/echo?a=1", `{"k":"v"}`)
	if rec.Code != http.StatusTeapot {
		t.Errorf("código %d, quiero 418 (el del handler)", rec.Code)
	}
	if gotMethod != http.MethodPut || gotQuery != "a=1" || gotType != "application/json" || gotBody["k"] != "v" {
		t.Errorf("petición = %s ?%s %q %v, quiero PUT ?a=1 application/json {k:v}", gotMethod, gotQuery, gotType, gotBody)
	}
}

func TestHarness_CommonUsesTheHarnessPieces(t *testing.T) {
	h := apipublicahelpertest.New(t)
	k := h.Common()
	if k.MW != h.MW() {
		t.Error("Common().MW no es el middleware del arnés")
	}
	if k.Auditor != httpapi.AuditRecorder(h.Auditor()) {
		t.Error("Common().Auditor no es el auditor del arnés")
	}
	if k.Log != h.Log() {
		t.Error("Common().Log no es el logger del arnés")
	}
}

func TestAuditRecorderFake_KeepsRecordsInOrderAsACopy(t *testing.T) {
	f := &apipublicahelpertest.AuditRecorderFake{}
	if len(f.Records()) != 0 {
		t.Fatal("un AuditRecorderFake nuevo debe estar vacío")
	}
	for _, action := range []string{"a", "b"} {
		if err := f.Record(context.Background(), httpapi.AuditInput{Action: action}); err != nil {
			t.Fatalf("Record devolvió error: %v", err)
		}
	}
	got := f.Records()
	if len(got) != 2 || got[0].Action != "a" || got[1].Action != "b" {
		t.Fatalf("Records = %+v, quiero a y b en orden", got)
	}
	got[0].Action = "mutado"
	if f.Records()[0].Action != "a" {
		t.Error("mutar lo devuelto por Records alteró el doble: no es una copia")
	}
}

func TestLogRecorder_RecordsLevelsMessagesAndFields(t *testing.T) {
	l := apipublicahelpertest.New(t).Log()
	l.Debug("d", "k", 1)
	l.Info("i")
	l.Warn("w", "x")
	child := l.With("pre", "p")
	child.Error("e", 7, errors.New("boom"))

	got := l.Entries()
	if len(got) != 4 {
		t.Fatalf("Entries = %d líneas, quiero 4 (las del hijo cuentan en el padre): %+v", len(got), got)
	}
	wantLevels := []string{"debug", "info", "warn", "error"}
	for i, e := range got {
		if e.Level != wantLevels[i] {
			t.Errorf("línea %d: nivel %q, quiero %q", i, e.Level, wantLevels[i])
		}
	}
	if got[0].Msg != "d" || got[0].Fields["k"] != 1 {
		t.Errorf("línea debug = %+v, quiero msg d y k=1", got[0])
	}
	if got[2].Fields["!BADKEY"] != "x" {
		t.Errorf("valor sin pareja = %+v, quiero !BADKEY=x", got[2].Fields)
	}
	if got[3].Fields["pre"] != "p" || got[3].Fields["7"] == nil {
		t.Errorf("línea del hijo = %+v, quiero pre=p (de With) y la clave no string como \"7\"", got[3].Fields)
	}
	got[0].Msg = "mutado"
	if l.Entries()[0].Msg != "d" {
		t.Error("mutar lo devuelto por Entries alteró el registro: no es una copia")
	}
}
