package iamhttp

// http.go no tiene exportados: estos tests nacen en su verde (E-4, P6) y fijan las reglas de su
// fontanería. El mapeo de errores se afirma BYTE A BYTE (diseño §5): un cuerpo que cambiara una
// tilde lo verían el BFF y `wapp-ctl`, que lo enseñan en texto plano.
//
// errorBody es auxiliar común del paquete: este es el primer _test.go que nace en verde (hallazgo
// 20 de F2: no hay helpers_test.go).

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

// errorBody es el cuerpo EXACTO que writeError produce para msg: lo que se compara con
// bytes.Equal / == en todo el paquete.
func errorBody(msg string) string {
	return `{"error":"` + msg + `"}`
}

// writeDomainError: cada centinela sale con su código y su texto literal (diseño §5), también
// envuelto (errors.Is), y todo lo que no tiene rama propia cae en 500 «error interno».
func TestWriteDomainError_MapsEverySentinelToItsStatusAndText(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"invalid_input_400", domain.ErrInvalidInput, http.StatusBadRequest, "entrada inválida"},
		{"no_tenant_403", domain.ErrNoTenant, http.StatusForbidden, "el token no trae empresa: no puede administrar roles ni miembros"},
		{"not_found_404", domain.ErrNotFound, http.StatusNotFound, "recurso no encontrado"},
		{"conflict_409", domain.ErrConflict, http.StatusConflict, "conflicto: el recurso ya existe o la persona ya pertenece a otra empresa"},
		{"global_role_immutable_422", domain.ErrGlobalRoleImmutable, http.StatusUnprocessableEntity, "las plantillas de rol globales no se modifican desde una empresa"},
		{"invalid_credentials_401", domain.ErrInvalidCredentials, http.StatusUnauthorized, "no autorizado"},
		{"user_inactive_401", domain.ErrUserInactive, http.StatusUnauthorized, "no autorizado"},
		{"refresh_invalid_401", domain.ErrRefreshInvalid, http.StatusUnauthorized, "no autorizado"},
		{"identity_token_invalid_401", domain.ErrIdentityTokenInvalid, http.StatusUnauthorized, "identity token inválido"},
		{"identity_token_expiring_401", domain.ErrIdentityTokenExpiring, http.StatusUnauthorized, "al identity token le queda muy poca vida: refresca antes de canjearlo"},
		{"user_not_migrated_401", domain.ErrUserNotMigrated, http.StatusUnauthorized, "usuario no migrado"},
		{"identity_unavailable_503", domain.ErrIdentityUnavailable, http.StatusServiceUnavailable, "identity no está disponible"},
		{"identity_not_configured_503_own_body", domain.ErrIdentityNotConfigured, http.StatusServiceUnavailable, "identity_no_configurado"},
		{"system_not_allowed_502", domain.ErrSystemNotAllowed, http.StatusBadGateway, "system_no_acreditable"},
		{"machine_credential_is_server_fault_500", domain.ErrMachineCredentialInvalid, http.StatusInternalServerError, "error interno"},
		{"invitation_expired_has_no_branch_500", domain.ErrInvitationExpired, http.StatusInternalServerError, "error interno"},
		{"unknown_error_500", errors.New("la base se cayó"), http.StatusInternalServerError, "error interno"},
		{"wrapped_sentinel_still_maps", fmt.Errorf("contexto: %w", domain.ErrNotFound), http.StatusNotFound, "recurso no encontrado"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeDomainError(rec, c.err)
			if rec.Code != c.status {
				t.Errorf("código = %d; se esperaba %d", rec.Code, c.status)
			}
			if got := rec.Body.String(); got != errorBody(c.msg) {
				t.Errorf("cuerpo = %s; se esperaba %s byte a byte", got, errorBody(c.msg))
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q; se esperaba application/json", ct)
			}
		})
	}
}

// Los dos 503 NO comparten cuerpo: uno es «espera», el otro «configura» (http.go).
func TestWriteDomainError_TheTwo503HaveDifferentBodies(t *testing.T) {
	unavailable, notConfigured := httptest.NewRecorder(), httptest.NewRecorder()
	writeDomainError(unavailable, domain.ErrIdentityUnavailable)
	writeDomainError(notConfigured, domain.ErrIdentityNotConfigured)
	if unavailable.Body.String() == notConfigured.Body.String() {
		t.Fatalf("los dos 503 responden lo mismo (%s): mandaría a mirar el sitio equivocado", unavailable.Body.String())
	}
}

// decodeJSON: con JSON válido rellena dst y no escribe nada; con JSON roto o sin cuerpo
// responde 400 «cuerpo JSON inválido» y devuelve false.
func TestDecodeJSON(t *testing.T) {
	t.Run("valid_fills_dst_and_writes_nothing", func(t *testing.T) {
		var dst struct {
			A string `json:"a"`
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":"x"}`))
		if !decodeJSON(rec, req, &dst) {
			t.Fatal("decodeJSON devolvió false con JSON válido")
		}
		if dst.A != "x" {
			t.Errorf("dst.A = %q; se esperaba x", dst.A)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("escribió %q con JSON válido: el caller es quien responde", rec.Body.String())
		}
	})
	for _, c := range []struct{ name, body string }{
		{"broken_json_is_400", `{`},
		{"empty_body_is_400", ``},
	} {
		t.Run(c.name, func(t *testing.T) {
			var dst map[string]any
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(c.body))
			if decodeJSON(rec, req, &dst) {
				t.Fatal("decodeJSON devolvió true con un cuerpo inválido")
			}
			if rec.Code != http.StatusBadRequest || rec.Body.String() != errorBody("cuerpo JSON inválido") {
				t.Errorf("respuesta = %d %s; se esperaba 400 %s", rec.Code, rec.Body.String(), errorBody("cuerpo JSON inválido"))
			}
		})
	}
}

// bearer: solo el esquema Bearer (sin distinguir mayúsculas), con el token recortado y no vacío.
func TestBearer(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
		wantOK bool
	}{
		{"missing_header", "", "", false},
		{"bearer_token", "Bearer abc", "abc", true},
		{"scheme_is_case_insensitive", "bEaReR abc", "abc", true},
		{"token_is_trimmed", "Bearer   abc  ", "abc", true},
		{"other_scheme_rejected", "Basic abc", "", false},
		{"prefix_only_rejected", "Bearer ", "", false},
		{"blank_token_rejected", "Bearer     ", "", false},
		{"no_space_after_scheme_rejected", "Bearerabc", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			got, ok := bearer(req)
			if got != c.want || ok != c.wantOK {
				t.Errorf("bearer(%q) = (%q, %v); se esperaba (%q, %v)", c.header, got, ok, c.want, c.wantOK)
			}
		})
	}
}

// methodNotAllowed: 405 con cuerpo JSON tipado.
func TestMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	methodNotAllowed(rec)
	if rec.Code != http.StatusMethodNotAllowed || rec.Body.String() != errorBody("método no permitido") {
		t.Errorf("respuesta = %d %s; se esperaba 405 %s", rec.Code, rec.Body.String(), errorBody("método no permitido"))
	}
}

// writeJSON: código, Content-Type JSON y el cuerpo serializado; lo que no se puede serializar
// es un 500 en texto plano, no un 200 a medias.
func TestWriteJSON(t *testing.T) {
	t.Run("serializes_with_code_and_content_type", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeJSON(rec, http.StatusCreated, map[string]int{"n": 1})
		if rec.Code != http.StatusCreated || rec.Body.String() != `{"n":1}` {
			t.Errorf("respuesta = %d %s; se esperaba 201 {\"n\":1}", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q; se esperaba application/json", ct)
		}
	})
	t.Run("unencodable_value_is_500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeJSON(rec, http.StatusOK, make(chan int))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("código = %d; se esperaba 500", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "error codificando respuesta") {
			t.Errorf("cuerpo = %q; se esperaba «error codificando respuesta»", rec.Body.String())
		}
	})
}
