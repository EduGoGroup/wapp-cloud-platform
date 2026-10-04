//go:build pendiente

package iamhttp

// Una aserción por promesa de R-H7 y R-H8 (invitations.go), con un doble de in.InvitationAdmin.
// El `status` lo deriva el handler con el reloj del servidor: los casos usan caducidades MUY
// lejanas (2000 / 2999) para que el desenlace no dependa del momento en que corre el test.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
)

// fakeInvitationAdmin devuelve siempre lo mismo y guarda la entrada.
type fakeInvitationAdmin struct {
	issued      in.IssuedInvitation
	invitations []domain.Invitation
	err         error
	calls       int
	issueInput  in.IssueInvitationInput
	revokedID   string
}

var _ in.InvitationAdmin = (*fakeInvitationAdmin)(nil)

func (f *fakeInvitationAdmin) IssueInvitation(_ context.Context, input in.IssueInvitationInput) (in.IssuedInvitation, error) {
	f.calls++
	f.issueInput = input
	return f.issued, f.err
}

func (f *fakeInvitationAdmin) ListInvitations(context.Context) ([]domain.Invitation, error) {
	f.calls++
	return f.invitations, f.err
}

func (f *fakeInvitationAdmin) RevokeInvitation(_ context.Context, id string) error {
	f.calls++
	f.revokedID = id
	return f.err
}

func invitationsMux(f in.InvitationAdmin) *http.ServeMux {
	var h *InvitationHandler = NewInvitationHandler(f)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/invitations", h.Issue())
	mux.Handle("GET /api/v1/invitations", h.List())
	mux.Handle("DELETE /api/v1/invitations/{id}", h.Revoke())
	return mux
}

var (
	farFuture = time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)
	farPast   = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
)

// R-H8: sin cuerpo o con `{}` es válido (sin rol, ttl 0 = el default del usecase).
func TestIssue_BodyIsOptional(t *testing.T) {
	for _, body := range []string{"", `{}`} {
		f := &fakeInvitationAdmin{issued: in.IssuedInvitation{Invitation: domain.Invitation{ID: "i-1", ExpiresAt: farFuture}, Token: "WAPP-INV-x"}}
		rec := serve(t, invitationsMux(f), http.MethodPost, "/api/v1/invitations", body)
		if rec.Code != http.StatusCreated || f.calls != 1 || f.issueInput.RoleID != nil || f.issueInput.TTLSeconds != 0 {
			t.Errorf("cuerpo %q: %d (llamadas %d, entrada %+v); se esperaba 201, sin rol y ttl 0", body, rec.Code, f.calls, f.issueInput)
		}
	}
}

// R-H8: role_id recortado (vacío ⇒ nil) y ttl tal cual, sin default ni clamp.
func TestIssue_InputReachesPort(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantRole *string
		wantTTL  int
	}{
		{"role_trimmed_ttl_as_is", `{"role_id":"  r-1  ","ttl":7}`, strPtr("r-1"), 7},
		{"blank_role_is_nil", `{"role_id":"   ","ttl":99999999}`, nil, 99999999},
		{"negative_ttl_not_clamped_here", `{"ttl":-5}`, nil, -5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeInvitationAdmin{issued: in.IssuedInvitation{Invitation: domain.Invitation{ID: "i-1", ExpiresAt: farFuture}}}
			serve(t, invitationsMux(f), http.MethodPost, "/api/v1/invitations", c.body)
			got := f.issueInput
			if got.TTLSeconds != c.wantTTL || (got.RoleID == nil) != (c.wantRole == nil) || (got.RoleID != nil && *got.RoleID != *c.wantRole) {
				t.Errorf("el puerto recibió %+v; se esperaba rol %v y ttl %d", got, c.wantRole, c.wantTTL)
			}
		})
	}
}

// R-H8: 201 con la invitación y el token en claro; forma exacta, sin token_hash.
func TestIssue_201WithTokenOnce(t *testing.T) {
	created := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	f := &fakeInvitationAdmin{issued: in.IssuedInvitation{
		Invitation: domain.Invitation{ID: "i-1", TenantID: tenantA, TokenHash: []byte("digest"), RoleID: strPtr("r-1"), ExpiresAt: farFuture, CreatedAt: created},
		Token:      "WAPP-INV-abc",
	}}
	rec := serve(t, invitationsMux(f), http.MethodPost, "/api/v1/invitations", `{"role_id":"r-1"}`)
	want := `{"id":"i-1","status":"pending","expires_at":"2999-01-01T00:00:00Z","role_id":"r-1","created_at":"2030-01-02T02:04:05Z","token":"WAPP-INV-abc"}`
	if rec.Code != http.StatusCreated || rec.Body.String() != want {
		t.Errorf("respuesta = %d %s; se esperaba 201 %s", rec.Code, rec.Body.String(), want)
	}
}

// R-H7: errores de la emisión y JSON roto.
func TestIssue_Errors(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		err       error
		status    int
		msg       string
		wantCalls int
	}{
		{"no_tenant_403", `{}`, domain.ErrNoTenant, http.StatusForbidden, "el token no trae empresa: no puede administrar roles ni miembros", 1},
		{"role_not_visible_404", `{"role_id":"r"}`, domain.ErrNotFound, http.StatusNotFound, "recurso no encontrado", 1},
		{"infra_500", `{}`, errors.New("rand roto"), http.StatusInternalServerError, "error interno", 1},
		{"broken_json_400", `{`, nil, http.StatusBadRequest, "cuerpo JSON inválido", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeInvitationAdmin{err: c.err}
			rec := serve(t, invitationsMux(f), http.MethodPost, "/api/v1/invitations", c.body)
			if rec.Code != c.status || rec.Body.String() != errorBody(c.msg) || f.calls != c.wantCalls {
				t.Errorf("respuesta = %d %s (llamadas %d); se esperaba %d %s (%d)", rec.Code, rec.Body.String(), f.calls, c.status, errorBody(c.msg), c.wantCalls)
			}
		})
	}
}

// R-H8: el listado conserva el orden, deriva el status y NO lleva token ni token_hash.
func TestInvitationList_StatusOrderAndNoSecrets(t *testing.T) {
	redeemedAt := time.Date(2030, 5, 5, 5, 5, 5, 0, time.UTC)
	revokedAt := time.Date(2030, 6, 6, 6, 6, 6, 0, time.UTC)
	f := &fakeInvitationAdmin{invitations: []domain.Invitation{
		{ID: "pending", TokenHash: []byte("h1"), ExpiresAt: farFuture},
		{ID: "redeemed", TokenHash: []byte("h2"), ExpiresAt: farPast, RedeemedAt: &redeemedAt, RedeemedBy: strPtr("u")},
		{ID: "revoked", TokenHash: []byte("h3"), ExpiresAt: farFuture, RevokedAt: &revokedAt},
		{ID: "expired", TokenHash: []byte("h4"), ExpiresAt: farPast},
	}}
	rec := serve(t, invitationsMux(f), http.MethodGet, "/api/v1/invitations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("código = %d; se esperaba 200", rec.Code)
	}
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out) != 4 {
		t.Fatalf("respuesta = %s (err %v); se esperaban 4", rec.Body.String(), err)
	}
	for i, wantID := range []string{"pending", "redeemed", "revoked", "expired"} {
		if string(out[i]["id"]) != `"`+wantID+`"` || string(out[i]["status"]) != `"`+wantID+`"` {
			t.Errorf("posición %d: id %s status %s; se esperaba %q en los dos", i, out[i]["id"], out[i]["status"], wantID)
		}
		for k := range out[i] {
			if !slices.Contains([]string{"id", "status", "expires_at", "role_id", "created_at", "redeemed_at", "revoked_at"}, k) {
				t.Errorf("posición %d lleva la clave %q: el listado no puede llevar ni token ni digest", i, k)
			}
		}
	}
	if string(out[1]["redeemed_at"]) != `"2030-05-05T05:05:05Z"` || string(out[2]["revoked_at"]) != `"2030-06-06T06:06:06Z"` {
		t.Errorf("marcas terminales mal servidas: %s", rec.Body.String())
	}
	if _, ok := out[0]["redeemed_at"]; ok {
		t.Errorf("una invitación viva lleva redeemed_at: %s", rec.Body.String())
	}
}

// R-H8: vacío es `[]`; sin empresa 403.
func TestInvitationList_EmptyAndNoTenant(t *testing.T) {
	if rec := serve(t, invitationsMux(&fakeInvitationAdmin{}), http.MethodGet, "/api/v1/invitations", ""); rec.Code != http.StatusOK || rec.Body.String() != `[]` {
		t.Errorf("vacío: %d %s; se esperaba 200 []", rec.Code, rec.Body.String())
	}
	if rec := serve(t, invitationsMux(&fakeInvitationAdmin{err: domain.ErrNoTenant}), http.MethodGet, "/api/v1/invitations", ""); rec.Code != http.StatusForbidden {
		t.Errorf("sin empresa: código = %d; se esperaba 403", rec.Code)
	}
}

// R-H8: revocar — 204 (también ya revocada), 404 ajena o no-UUID, 409 canjeada, 403 sin empresa.
func TestRevoke_Outcomes(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"revoked_204", nil, http.StatusNoContent, ""},
		{"foreign_or_missing_404", domain.ErrNotFound, http.StatusNotFound, errorBody("recurso no encontrado")},
		{"already_redeemed_409", domain.ErrConflict, http.StatusConflict, errorBody("conflicto: el recurso ya existe o la persona ya pertenece a otra empresa")},
		{"no_tenant_403", domain.ErrNoTenant, http.StatusForbidden, errorBody("el token no trae empresa: no puede administrar roles ni miembros")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeInvitationAdmin{err: c.err}
			rec := serve(t, invitationsMux(f), http.MethodDelete, "/api/v1/invitations/i-9", "")
			if rec.Code != c.status || rec.Body.String() != c.body || f.revokedID != "i-9" {
				t.Errorf("respuesta = %d %q (id %q); se esperaba %d %q con id i-9", rec.Code, rec.Body.String(), f.revokedID, c.status, c.body)
			}
		})
	}
}

// R-H7: comodín vacío ⇒ 400 «id requerido en la ruta» sin llamar al puerto.
func TestRevoke_EmptyIDIs400(t *testing.T) {
	f := &fakeInvitationAdmin{}
	rec := serve(t, NewInvitationHandler(f).Revoke(), http.MethodDelete, "/x", "")
	if rec.Code != http.StatusBadRequest || rec.Body.String() != errorBody("id requerido en la ruta") || f.calls != 0 {
		t.Errorf("respuesta = %d %s (llamadas %d); se esperaba 400 sin llamar al puerto", rec.Code, rec.Body.String(), f.calls)
	}
}
