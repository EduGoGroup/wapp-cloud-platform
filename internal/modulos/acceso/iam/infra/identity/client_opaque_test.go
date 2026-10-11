package iamidentity_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	iamidentity "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/identity"
)

// Lo que el cliente de personas hace con una respuesta que NINGUNA tabla traduce: el error sube
// opaco, con su texto propio, y sin nada de la petición. Es el espejo, para Client, de lo que
// m2m_wire_test.go afirma del cliente M2M.

// opaqueSentinels son los centinelas con los que un error sin traducir no se puede confundir.
var opaqueSentinels = []error{
	domain.ErrInvalidCredentials, domain.ErrUserInactive, domain.ErrRefreshInvalid,
	domain.ErrInvalidInput, domain.ErrIdentityUnavailable,
}

// TestClient_OpaqueErrorTextCarriesOnlyStatusAndCode: el error de un login que la tabla no
// traduce dice el código HTTP y el `code` de identity, y NADA de lo que viajó en la petición (ni
// el correo ni la contraseña).
func TestClient_OpaqueErrorTextCarriesOnlyStatusAndCode(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.respond(http.StatusInternalServerError, "INTERNAL", "")
	_, err := f.client(t).Login(context.Background(), testEmail, testPassword, testSystem)
	if err == nil {
		t.Fatal("un 500 devolvió nil")
	}
	if got, want := err.Error(), "identity respondió 500 (INTERNAL)"; got != want {
		t.Fatalf("texto = %q, quería %q", got, want)
	}
	for _, leaked := range []string{testEmail, testPassword, testSystem, `"error"`} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("texto = %q: lleva %q, que es del cuerpo o de la petición", err.Error(), leaked)
		}
	}
}

// TestRefresh_UnmappedStatusIsOpaque: un código que la tabla de refresh/logout no traduce sube
// tal cual —nunca nil, nunca un centinela— en las tres operaciones que comparten el mapeo.
func TestRefresh_UnmappedStatusIsOpaque(t *testing.T) {
	t.Parallel()
	f := newFakeIdentity(t)
	f.respond(http.StatusInternalServerError, "INTERNAL", "")
	c := f.client(t)

	_, refreshErr := c.Refresh(context.Background(), testRefresh)
	calls := map[string]error{
		"Refresh":   refreshErr,
		"Logout":    c.Logout(context.Background(), testRefresh),
		"LogoutAll": c.LogoutAll(context.Background(), "id.tok.en"),
	}
	for name, err := range calls {
		if err == nil {
			t.Errorf("%s: un 500 no traducido devolvió nil", name)
			continue
		}
		if got, want := err.Error(), "identity respondió 500 (INTERNAL)"; got != want {
			t.Errorf("%s: texto = %q, quería %q", name, got, want)
		}
		for _, sentinel := range opaqueSentinels {
			if errors.Is(err, sentinel) {
				t.Errorf("%s: un 500 no traducido salió como %v", name, sentinel)
			}
		}
	}
}

// TestClient_UnreadableErrorBodyStillMapsByStatus: un error cuyo cuerpo no es JSON (el HTML de un
// proxy) se traduce igual por su código HTTP, y si no tiene traducción su `code` sale vacío: el
// cuerpo ilegible no se cuela en el texto.
func TestClient_UnreadableErrorBodyStillMapsByStatus(t *testing.T) {
	t.Parallel()
	const htmlBody = "<html>Bad Gateway</html>"
	login := func(t *testing.T, status int) error {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			writeBody(t, w, htmlBody)
		}))
		t.Cleanup(srv.Close)
		c, err := iamidentity.New(srv.URL, 2*time.Second)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		_, err = c.Login(context.Background(), testEmail, testPassword, testSystem)
		return err
	}

	t.Run("mapped_status_keeps_its_sentinel", func(t *testing.T) {
		t.Parallel()
		if err := login(t, http.StatusUnauthorized); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Fatalf("err = %v, quería %v", err, domain.ErrInvalidCredentials)
		}
	})
	t.Run("unmapped_status_has_empty_code", func(t *testing.T) {
		t.Parallel()
		err := login(t, http.StatusBadGateway)
		if err == nil {
			t.Fatal("un 502 devolvió nil")
		}
		if got, want := err.Error(), "identity respondió 502 ()"; got != want {
			t.Fatalf("texto = %q, quería %q", got, want)
		}
	})
}
