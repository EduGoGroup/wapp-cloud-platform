package platformadmin_test

// Parte de handlers_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): el código de enrolamiento (TTL, formato y errores).

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

// codeFormat es el formato de un código de enrolamiento: "WAPP-" + 10 bytes en hex (R-A4).
var codeFormat = regexp.MustCompile(`^WAPP-[0-9a-f]{20}$`)

// R-A4: el TTL (por defecto, del cuerpo, acotado) llega a expires_at, y el código que se persiste
// es el que se responde.
func TestIssueEnrollmentCodeHandler_TTL(t *testing.T) {
	const day = 86400
	for _, c := range []struct {
		name string
		body string
		ttl  int
	}{
		{"NoBody_DefaultDay", "", day},
		{"BodyTTL", `{"ttl":3600}`, 3600},
		{"BelowMinimum_60", `{"ttl":30}`, 60},
		{"AboveMaximum_30Days", `{"ttl":99999999}`, 30 * day},
		{"ZeroTTL_Default", `{"ttl":0}`, day},
		{"NegativeTTL_Default", `{"ttl":-5}`, day},
		{"NotJSON_Default", `{`, day},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newInbox(t)
			issuer := &fakeIssuer{}
			h := platformadmin.IssueEnrollmentCodeHandler(b.f, issuer, platformTenant)
			path := "/admin/tenants/" + b.tenantA + "/enrollment-codes"
			r := httptest.NewRequest(http.MethodPost, path, nil)
			if c.body != "" {
				r = httptest.NewRequest(http.MethodPost, path, strings.NewReader(c.body))
			}
			before := time.Now().UTC()
			rec, audit := serve(t, enrollmentPattern, h, asCaller(r, platformTenant))
			after := time.Now().UTC()
			var resp platformadmin.IssueEnrollmentCodeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusCreated {
				t.Fatalf("respuesta = %d %s (%v)", rec.Code, rec.Body.String(), err)
			}
			ttl := time.Duration(c.ttl) * time.Second
			if resp.ExpiresAt.Before(before.Add(ttl)) || resp.ExpiresAt.After(after.Add(ttl)) {
				t.Fatalf("expires_at = %v, quiero entre %v y %v (ahora + %v)", resp.ExpiresAt, before.Add(ttl), after.Add(ttl), ttl)
			}
			if len(issuer.calls) == 1 && issuer.calls[0].expiresAt.Location() != time.UTC {
				t.Fatalf("expires_at persistido = %v (%v), quiero UTC", issuer.calls[0].expiresAt, issuer.calls[0].expiresAt.Location())
			}
			if !codeFormat.MatchString(resp.Code) {
				t.Fatalf("código = %q, quiero WAPP- y 20 hex en minúsculas", resp.Code)
			}
			if len(issuer.calls) != 1 || issuer.calls[0].code != resp.Code || issuer.calls[0].tenantID != b.tenantA ||
				!issuer.calls[0].expiresAt.Equal(resp.ExpiresAt) {
				t.Fatalf("persistido %+v, quiero UNA vez el código y el vencimiento respondidos para %s", issuer.calls, b.tenantA)
			}
			if got := audit.target(t); got != b.tenantA {
				t.Fatalf("tenant objetivo = %q, quiero %q", got, b.tenantA)
			}
		})
	}
}

// Los códigos son aleatorios: dos emisiones no repiten.
func TestIssueEnrollmentCodeHandler_CodesDiffer(t *testing.T) {
	b := newInbox(t)
	issuer := &fakeIssuer{}
	h := platformadmin.IssueEnrollmentCodeHandler(b.f, issuer, platformTenant)
	for range 2 {
		r := httptest.NewRequest(http.MethodPost, "/admin/tenants/"+b.tenantA+"/enrollment-codes", nil)
		if rec, _ := serve(t, enrollmentPattern, h, asCaller(r, platformTenant)); rec.Code != http.StatusCreated {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if len(issuer.calls) != 2 || issuer.calls[0].code == issuer.calls[1].code {
		t.Fatalf("códigos = %+v, quiero dos distintos", issuer.calls)
	}
}

// La empresa se comprueba antes de emitir, y un fallo del emisor es un 500 con su texto.
func TestIssueEnrollmentCodeHandler_Errors(t *testing.T) {
	post := func(t *testing.T, b inbox, issuer *fakeIssuer, id string) *httptest.ResponseRecorder {
		t.Helper()
		h := platformadmin.IssueEnrollmentCodeHandler(b.f, issuer, platformTenant)
		r := httptest.NewRequest(http.MethodPost, "/admin/tenants/"+id+"/enrollment-codes", nil)
		rec, _ := serve(t, enrollmentPattern, h, asCaller(r, platformTenant))
		return rec
	}
	t.Run("MissingTenant_404_NothingIssued", func(t *testing.T) {
		b := newInbox(t)
		issuer := &fakeIssuer{}
		wantText(t, post(t, b, issuer, uuid.NewString()), http.StatusNotFound, "empresa no encontrada")
		if len(issuer.calls) != 0 {
			t.Fatal("se emitió un código para una empresa que no existe")
		}
	})
	t.Run("ExistsFails_500", func(t *testing.T) {
		b := newInbox(t)
		b.f.Fail("ExistsTenant", errors.New("bd caída"))
		wantText(t, post(t, b, &fakeIssuer{}, b.tenantA), http.StatusInternalServerError, "error al verificar empresa")
	})
	t.Run("IssuerFails_500", func(t *testing.T) {
		b := newInbox(t)
		wantText(t, post(t, b, &fakeIssuer{err: errors.New("bd caída")}, b.tenantA), http.StatusInternalServerError,
			"error al persistir código de enrolamiento")
	})
}
