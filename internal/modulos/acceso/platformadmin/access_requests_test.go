package platformadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

var _ out.IdentityM2MClient = (*fakeM2M)(nil)

// Los seis centinelas de la bandeja conservan su texto, byte a byte (diseño F2 §5): el de
// ErrSystemsSyncFailed viaja tal cual en el cuerpo del 502 (ApprovePartialResult.Reason).
func TestAccessRequestSentinels_LiteralTexts(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"ErrPlatformSystemForbidden", platformadmin.ErrPlatformSystemForbidden,
			"platformadmin: wapp.platform no se concede desde la bandeja de solicitudes de acceso"},
		{"ErrSystemsUnionUnavailable", platformadmin.ErrSystemsUnionUnavailable,
			"platformadmin: no se puede unir con los systems actuales del usuario en identity (sin lectura)"},
		{"ErrIdentityM2MUnavailable", platformadmin.ErrIdentityM2MUnavailable,
			"platformadmin: no hay cliente M2M configurado hacia identity; no se pudieron conceder los systems solicitados"},
		{"ErrRetryRoleMismatch", platformadmin.ErrRetryRoleMismatch,
			"platformadmin: el reintento pide un rol distinto del ya aprobado la primera vez; no converge"},
		{"ErrTenantNotFound", platformadmin.ErrTenantNotFound,
			"platformadmin: el tenant_id de la aprobación no existe"},
		{"ErrSystemsSyncFailed", platformadmin.ErrSystemsSyncFailed,
			"platformadmin: fallo al sincronizar systems en identity tras aprobar localmente"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.err.Error() != c.want {
				t.Fatalf("%s = %q, quiero %q", c.name, c.err.Error(), c.want)
			}
		})
	}
}

// Los tags JSON de los DTO de la bandeja son contrato con la consola de plataforma.
func TestAccessRequestDTOs_JSONShape(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	item := platformadmin.AccessRequestItem{
		ID: "r1", UserID: "u1", Email: "ana@x.com", Origin: "bff", Status: "pending", CreatedAt: at,
		Systems: []string{}, SystemsKnown: false,
	}
	const itemJSON = `{"id":"r1","user_id":"u1","email":"ana@x.com","origin":"bff","status":"pending",` +
		`"created_at":"2026-10-04T12:00:00Z","systems":[],"systems_known":false}`
	for _, c := range []struct {
		name string
		v    any
		want string
	}{
		{"AccessRequestItem", item, itemJSON},
		{"ListAccessRequestsResponse", platformadmin.ListAccessRequestsResponse{Items: []platformadmin.AccessRequestItem{item}},
			`{"items":[` + itemJSON + `]}`},
		{"ApprovePartialResult", platformadmin.ApprovePartialResult{Local: "ok", Identity: "failed", Reason: "r"},
			`{"local":"ok","identity":"failed","reason":"r"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := json.Marshal(c.v)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(got) != c.want {
				t.Fatalf("JSON = %s\nquiero  %s", got, c.want)
			}
		})
	}
}

// Los cuerpos de aprobar y rechazar se leen con los nombres de campo de la consola.
func TestAccessRequestBodies_JSONFieldNames(t *testing.T) {
	var approve platformadmin.ApproveAccessRequestRequest
	if err := json.Unmarshal([]byte(`{"tenant_id":"t1","role":"operator","systems":["wapp.bff"]}`), &approve); err != nil {
		t.Fatalf("json.Unmarshal(approve): %v", err)
	}
	if approve.TenantID != "t1" || approve.Role != "operator" || len(approve.Systems) != 1 || approve.Systems[0] != "wapp.bff" {
		t.Fatalf("ApproveAccessRequestRequest = %+v", approve)
	}
	var reject platformadmin.RejectAccessRequestRequest
	if err := json.Unmarshal([]byte(`{"reason":"duplicada"}`), &reject); err != nil {
		t.Fatalf("json.Unmarshal(reject): %v", err)
	}
	if reject.Reason != "duplicada" {
		t.Fatalf("RejectAccessRequestRequest = %+v", reject)
	}
}

// ── ApproveAccessRequest ─────────────────────────────────────────────────────────────────────

// approve llama a ApproveAccessRequest sobre la bandeja con el operador de los tests.
func (b inbox) approve(requestID, tenantID, role string, systems []string, m2m out.IdentityM2MClient) error {
	return platformadmin.ApproveAccessRequest(context.Background(), b.f, b.f, requestID, tenantID, role, operatorSubject, systems, m2m)
}

// ── handlers ─────────────────────────────────────────────────────────────────────────────────

const (
	listPattern    = "GET /admin/access-requests"
	approvePattern = "POST /admin/access-requests/{id}/approve"
	rejectPattern  = "POST /admin/access-requests/{id}/reject"
)

// R-A1: los tres handlers de la bandeja cortan con EnforcePlatformCaller ANTES de tocar el
// almacén: 401 sin identidad (o sin tenant), 403 con un tenant que no es el de plataforma.
func TestAccessRequestHandlers_PlatformFence_BeforeTheStore(t *testing.T) {
	b := newInbox(t)
	failAll(b.f)
	id := uuid.NewString()
	handlers := []struct {
		name, pattern, method, path string
		h                           http.Handler
	}{
		{"List", listPattern, http.MethodGet, "/admin/access-requests", platformadmin.ListAccessRequestsHandler(b.f, platformTenant)},
		{"Approve", approvePattern, http.MethodPost, "/admin/access-requests/" + id + "/approve",
			platformadmin.ApproveAccessRequestHandler(b.f, b.f, &fakeM2M{}, platformTenant)},
		{"Reject", rejectPattern, http.MethodPost, "/admin/access-requests/" + id + "/reject",
			platformadmin.RejectAccessRequestHandler(b.f, platformTenant)},
	}
	for _, h := range handlers {
		for _, c := range []struct {
			name, tenant string
			code         int
		}{
			{"NoIdentity", "", http.StatusUnauthorized},
			{"OtherTenant", b.tenantA, http.StatusForbidden},
		} {
			t.Run(h.name+"_"+c.name, func(t *testing.T) {
				body := `{"tenant_id":"` + b.tenantA + `","role":"operator","reason":"x"}`
				r := asCaller(httptest.NewRequest(h.method, h.path, strings.NewReader(body)), c.tenant)
				rec, _ := serve(t, h.pattern, h.h, r)
				if rec.Code != c.code {
					t.Fatalf("status = %d, quiero %d (y sin tocar el almacén)", rec.Code, c.code)
				}
			})
		}
	}
}

// R-A2: un {id} vacío es 400 y uno que no es UUID es 404, sin consultar el almacén.
func TestAccessRequestHandlers_RequestID_EmptyIs400_NotUUIDIs404(t *testing.T) {
	b := newInbox(t)
	failAll(b.f)
	approve := platformadmin.ApproveAccessRequestHandler(b.f, b.f, &fakeM2M{}, platformTenant)
	reject := platformadmin.RejectAccessRequestHandler(b.f, platformTenant)
	body := `{"tenant_id":"` + b.tenantA + `","role":"operator","reason":"x"}`
	for _, c := range []struct {
		name, pattern, path string
		h                   http.Handler
	}{
		{"Approve", approvePattern, "/admin/access-requests/no-es-un-uuid/approve", approve},
		{"Reject", rejectPattern, "/admin/access-requests/no-es-un-uuid/reject", reject},
	} {
		t.Run(c.name+"_NotUUID", func(t *testing.T) {
			r := asCaller(httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(body)), platformTenant)
			rec, _ := serve(t, c.pattern, c.h, r)
			wantText(t, rec, http.StatusNotFound, "solicitud no encontrada")
		})
		t.Run(c.name+"_Empty", func(t *testing.T) {
			// Sin ServeMux no hay {id}: PathValue devuelve "".
			r := asCaller(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), platformTenant)
			rec := httptest.NewRecorder()
			c.h.ServeHTTP(rec, r)
			wantText(t, rec, http.StatusBadRequest, "id de solicitud requerido")
		})
	}
}

// El listado: status por defecto pending, ?status= filtra, items nunca null y el fallo del
// almacén es un 500 con su texto.
func TestListAccessRequestsHandler(t *testing.T) {
	b := newInbox(t)
	h := platformadmin.ListAccessRequestsHandler(b.f, platformTenant)
	get := func(path string) *httptest.ResponseRecorder {
		rec, _ := serve(t, listPattern, h, asCaller(httptest.NewRequest(http.MethodGet, path, nil), platformTenant))
		return rec
	}
	wantJSON(t, get("/admin/access-requests"), http.StatusOK, `{"items":[]}`)

	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	var resp platformadmin.ListAccessRequestsResponse
	rec := get("/admin/access-requests")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("respuesta = %d %s (%v)", rec.Code, rec.Body.String(), err)
	}
	if len(resp.Items) != 1 || resp.Items[0].ID != id || resp.Items[0].Status != "pending" {
		t.Fatalf("items = %+v, quiero la pendiente %s", resp.Items, id)
	}
	wantJSON(t, get("/admin/access-requests?status=rejected"), http.StatusOK, `{"items":[]}`)

	b.f.Fail("ListAccessRequests", errors.New("bd caída"))
	wantText(t, get("/admin/access-requests"), http.StatusInternalServerError, "error al listar solicitudes de acceso")
}

// El cuerpo de aprobar: JSON inválido y campos que faltan son 400, sin tocar el almacén.
func TestApproveAccessRequestHandler_Body(t *testing.T) {
	b := newInbox(t)
	failAll(b.f)
	h := platformadmin.ApproveAccessRequestHandler(b.f, b.f, &fakeM2M{}, platformTenant)
	path := "/admin/access-requests/" + uuid.NewString() + "/approve"
	for _, c := range []struct{ name, body, text string }{
		{"NotJSON", `{`, "cuerpo JSON inválido"},
		{"NoTenant", `{"role":"operator"}`, "tenant_id y role son requeridos"},
		{"NoRole", `{"tenant_id":"` + b.tenantA + `"}`, "tenant_id y role son requeridos"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := asCaller(httptest.NewRequest(http.MethodPost, path, strings.NewReader(c.body)), platformTenant)
			rec, _ := serve(t, approvePattern, h, r)
			wantText(t, rec, http.StatusBadRequest, c.text)
		})
	}
}

// approveSetup es el escenario de un desenlace del handler de aprobar.
type approveSetup struct {
	b      inbox
	id     string
	tenant string
	role   string
	extra  map[string]any
	m2m    out.IdentityM2MClient
}

// Cada desenlace de ApproveAccessRequest tiene su código y su cuerpo, literales, y el handler
// publica el tenant_id del cuerpo como tenant objetivo de la auditoría.
func TestApproveAccessRequestHandler_Outcomes(t *testing.T) {
	for _, c := range []struct {
		name    string
		arrange func(t *testing.T, s *approveSetup)
		code    int
		text    string // http.Error
		json    string // writeJSON
	}{
		{"Approved_204", func(*testing.T, *approveSetup) {}, http.StatusNoContent, "", ""},
		{"MissingRequest_404", func(_ *testing.T, s *approveSetup) { s.id = uuid.NewString() },
			http.StatusNotFound, "solicitud no encontrada", ""},
		{"MissingTenant_404", func(_ *testing.T, s *approveSetup) { s.tenant = uuid.NewString() },
			http.StatusNotFound, "empresa no encontrada", ""},
		{"Rejected_409", func(t *testing.T, s *approveSetup) {
			if err := s.b.f.RejectAccessRequest(context.Background(), s.id, "no", operatorSubject); err != nil {
				t.Fatalf("RejectAccessRequest: %v", err)
			}
		}, http.StatusConflict, "la solicitud ya fue resuelta o la persona ya pertenece a otra empresa", ""},
		{"UnknownRole_400", func(_ *testing.T, s *approveSetup) { s.role = "no_such_role" },
			http.StatusBadRequest, "datos de solicitud o rol inválidos", ""},
		{"PlatformSystem_400", func(_ *testing.T, s *approveSetup) {
			s.extra = map[string]any{"systems": []string{"wapp.platform"}}
		}, http.StatusBadRequest, "wapp.platform no se concede desde la bandeja de solicitudes de acceso", ""},
		{"RetryAnotherRole_409", func(t *testing.T, s *approveSetup) {
			if err := platformadmin.ApproveAccessRequest(context.Background(), s.b.f, s.b.f, s.id, s.tenant, "viewer", operatorSubject, nil, nil); err != nil {
				t.Fatalf("primera aprobación: %v", err)
			}
		}, http.StatusConflict, "la solicitud ya fue aprobada con un rol distinto; el reintento no converge", ""},
		{"UnionUnavailable_409", func(_ *testing.T, s *approveSetup) {
			s.extra = map[string]any{"systems": []string{"wapp.bff"}}
			s.m2m = &fakeM2M{getErr: errors.New("x")}
		}, http.StatusConflict, "", `{"local":"ok","identity":"skipped","reason":"no se pudo leer el conjunto actual de systems del usuario en identity; para no reemplazarlo por accidente no se tocó nada en identity"}`},
		{"SyncFailed_502", func(_ *testing.T, s *approveSetup) {
			s.extra = map[string]any{"systems": []string{"wapp.bff"}}
			s.m2m = &fakeM2M{replaceErr: errors.New("identity 503")}
		}, http.StatusBadGateway, "", `{"local":"ok","identity":"failed","reason":"platformadmin: fallo al sincronizar systems en identity tras aprobar localmente: identity 503"}`},
		{"NoM2M_503", func(_ *testing.T, s *approveSetup) {
			s.extra = map[string]any{"systems": []string{"wapp.bff"}}
			s.m2m = nil
		}, http.StatusServiceUnavailable, "", `{"local":"ok","identity":"skipped","reason":"no hay cliente M2M configurado hacia identity en este despliegue; lo local (empresa y rol) quedó escrito pero los systems solicitados NO se concedieron"}`},
		{"StoreFails_500", func(_ *testing.T, s *approveSetup) { s.b.f.Fail("LookupAccessRequestStatus", errors.New("bd caída")) },
			http.StatusInternalServerError, "error al aprobar solicitud", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newInbox(t)
			s := approveSetup{b: b, id: b.pendingRequest(t, uuid.NewString()), tenant: b.tenantA, role: "operator", m2m: &fakeM2M{}}
			c.arrange(t, &s)
			body := map[string]any{"tenant_id": s.tenant, "role": s.role}
			for k, v := range s.extra {
				body[k] = v
			}
			h := platformadmin.ApproveAccessRequestHandler(s.b.f, s.b.f, s.m2m, platformTenant)
			r := asCaller(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+s.id+"/approve", jsonBody(t, body)), platformTenant)
			rec, audit := serve(t, approvePattern, h, r)
			switch {
			case c.json != "":
				wantJSON(t, rec, c.code, c.json)
			case c.text != "":
				wantText(t, rec, c.code, c.text)
			case rec.Code != c.code || rec.Body.Len() != 0:
				t.Fatalf("respuesta = %d %q, quiero %d sin cuerpo", rec.Code, rec.Body.String(), c.code)
			}
			if got := audit.target(t); got != s.tenant {
				t.Fatalf("tenant objetivo auditado = %q, quiero el tenant_id del cuerpo %q", got, s.tenant)
			}
		})
	}
}

// Aprobar por HTTP guarda como decided_by el Subject de la identidad del operador.
func TestApproveAccessRequestHandler_OperatorIsTheSubject(t *testing.T) {
	b := newInbox(t)
	user := uuid.NewString()
	id := b.pendingRequest(t, user)
	h := platformadmin.ApproveAccessRequestHandler(b.f, b.f, nil, platformTenant)
	body := `{"tenant_id":"` + b.tenantA + `","role":"operator"}`
	r := asCaller(httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+id+"/approve", strings.NewReader(body)), platformTenant)
	if rec, _ := serve(t, approvePattern, h, r); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	b.wantApprovedLocally(t, id, user, b.tenantA, roleOperator.ID)
}

// postReject manda POST …/reject con ese cuerpo (sin cuerpo si body es "") como operador de
// plataforma.
func postReject(t *testing.T, b inbox, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+id+"/reject", nil)
	if body != "" {
		r = httptest.NewRequest(http.MethodPost, "/admin/access-requests/"+id+"/reject", strings.NewReader(body))
	}
	rec, _ := serve(t, rejectPattern, platformadmin.RejectAccessRequestHandler(b.f, platformTenant), asCaller(r, platformTenant))
	return rec
}

// Rechazar: motivo obligatorio (R-A6), cuerpo opcional pero JSON si viene, y cada desenlace con
// su código y su texto.
func TestRejectAccessRequestHandler(t *testing.T) {
	t.Run("Rejected_204_ReasonAndOperatorSaved", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		if rec := postReject(t, b, id, `{"reason":"no es cliente"}`); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("respuesta = %d %q, quiero 204", rec.Code, rec.Body.String())
		}
		row := b.f.Request(t, id)
		if row.Status != "rejected" || row.Reason == nil || *row.Reason != "no es cliente" || row.DecidedBy == nil || *row.DecidedBy != operatorSubject {
			t.Fatalf("fila = %+v", row)
		}
	})
	t.Run("NoBody_400_ReasonRequired", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		wantText(t, postReject(t, b, id, ""), http.StatusBadRequest, "entrada inválida")
		if row := b.f.Request(t, id); row.Status != "pending" {
			t.Fatalf("sin motivo no se rechaza: %+v", row)
		}
	})
	t.Run("BlankReason_400", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		wantText(t, postReject(t, b, id, `{"reason":"   "}`), http.StatusBadRequest, "entrada inválida")
	})
	t.Run("NotJSON_400", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		wantText(t, postReject(t, b, id, `{`), http.StatusBadRequest, "cuerpo JSON inválido")
	})
	t.Run("Missing_404", func(t *testing.T) {
		b := newInbox(t)
		wantText(t, postReject(t, b, uuid.NewString(), `{"reason":"x"}`), http.StatusNotFound, "solicitud no encontrada")
	})
	t.Run("AlreadyDecided_409", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		if rec := postReject(t, b, id, `{"reason":"uno"}`); rec.Code != http.StatusNoContent {
			t.Fatalf("primer rechazo: %d", rec.Code)
		}
		wantText(t, postReject(t, b, id, `{"reason":"dos"}`), http.StatusConflict, "la solicitud ya fue resuelta")
	})
	t.Run("StoreFails_500", func(t *testing.T) {
		b := newInbox(t)
		id := b.pendingRequest(t, uuid.NewString())
		b.f.Fail("RejectAccessRequest", errors.New("bd caída"))
		wantText(t, postReject(t, b, id, `{"reason":"x"}`), http.StatusInternalServerError, "error al rechazar solicitud")
	})
}
