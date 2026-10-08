package apipublica_test

// intakes_test.go — cubre el contrato de intakes.go (IntakeService, IntakesDeps, MountIntakes):
// el montaje, la cadena, el gate `cart_basic` y las reglas que valen para las siete rutas de la
// bandeja (G1–G6 y G8). Aquí viven además los dobles y los auxiliares que comparten los demás
// tests del área: intakes_dto_test.go (los cuerpos), intakes_filter_test.go (la query de G1),
// intakes_llm_gate_test.go (el gate por campo), intakes_status_test.go (G3 y G8),
// intakes_items_test.go (G4) e intakes_approve_test.go (G5 y G6).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

const (
	intakeReadPerm  = "intakes.read"
	intakeWritePerm = "intakes.write"
	intakeResource  = "intake"

	intakeID      = "11111111-2222-4333-8444-555555555555"
	intakesTarget = "/api/v1/intakes"
	intakeTarget  = intakesTarget + "/" + intakeID

	intakeDenied      = `{"error":"feature_not_enabled","feature":"cart_basic"}`
	intakeMsgNotFound = "solicitud no encontrada"
	intakeMsgBadJSON  = "cuerpo JSON inválido"
	intakeMsgConflict = "la solicitud cambió de estado; recárgala y reintenta"

	// intakeDSN es lo que un 500 no puede repetir: el error de un driver puede llevarlo dentro.
	//nolint:gosec // G101: no es una credencial, es el DSN inventado de un test
	intakeDSN = "postgres://usuario:secreto@host/bd"
)

// intakeRoute es una de las siete rutas, con una petición que el doble contesta con 200.
type intakeRoute struct {
	id, pattern, method, target, body, perm, resource, call string
}

// intakeRoutes son G1–G6 y G8 tal como las promete MountIntakes. resource "" = cadena R; call es
// el método del puerto que la ruta invoca.
var intakeRoutes = []intakeRoute{
	{"G1", "GET /api/v1/intakes", http.MethodGet, intakesTarget, "", intakeReadPerm, "", "List"},
	{"G2", "GET /api/v1/intakes/{id}", http.MethodGet, intakeTarget, "", intakeReadPerm, "", "Get"},
	{"G3", "POST /api/v1/intakes/{id}/status", http.MethodPost, intakeTarget + "/status", `{"status":"confirmed"}`, intakeWritePerm, intakeResource, "SetStatus"},
	{"G4", "PUT /api/v1/intakes/{id}/items", http.MethodPut, intakeTarget + "/items", `{"items":[]}`, intakeWritePerm, intakeResource, "ReplaceItems"},
	{"G5", "POST /api/v1/intakes/{id}/approve", http.MethodPost, intakeTarget + "/approve", `{"rendered_text":"Son $9.00"}`, intakeWritePerm, intakeResource, "Approve"},
	{"G6", "POST /api/v1/intakes/{id}/request-info", http.MethodPost, intakeTarget + "/request-info", `{"question":"¿Para cuándo?"}`, intakeWritePerm, intakeResource, "RequestInfo"},
	{"G8", "POST /api/v1/intakes/discard", http.MethodPost, intakesTarget + "/discard", `{"intake_ids":["a"]}`, intakeWritePerm, intakeResource, "Discard"},
}

// El puerto de la bandeja lo cumple el servicio REAL del módulo solicitudes nuevo.
var _ apipublica.IntakeService = (*intakes.Service)(nil)

// intakeServiceSpy es IntakeService: contesta lo que se le siembra (o err) y apunta cada llamada
// con sus argumentos y si su contexto traía plazo.
type intakeServiceSpy struct {
	page    intakes.Page
	detail  intakes.Detail
	header  intakes.Intake
	discard intakes.DiscardResult
	err     error

	calls       []string
	tenant      string
	id          string
	filter      intakes.Filter
	status      string
	notice      intakes.StatusNotice
	ids         []string
	items       []intakes.Item
	mode        intakes.EditMode
	text        string
	hasDeadline bool
}

var _ apipublica.IntakeService = (*intakeServiceSpy)(nil)

func (s *intakeServiceSpy) note(ctx context.Context, call, tenantID, id string) {
	s.calls = append(s.calls, call)
	s.tenant, s.id = tenantID, id
	_, s.hasDeadline = ctx.Deadline()
}

func (s *intakeServiceSpy) List(ctx context.Context, tenantID string, f intakes.Filter) (intakes.Page, error) {
	s.note(ctx, "List", tenantID, "")
	s.filter = f
	return s.page, s.err
}

func (s *intakeServiceSpy) Get(ctx context.Context, tenantID, id string) (intakes.Detail, error) {
	s.note(ctx, "Get", tenantID, id)
	return s.detail, s.err
}

func (s *intakeServiceSpy) SetStatus(ctx context.Context, tenantID, id, status string, notice intakes.StatusNotice) (intakes.Intake, error) {
	s.note(ctx, "SetStatus", tenantID, id)
	s.status, s.notice = status, notice
	return s.header, s.err
}

func (s *intakeServiceSpy) Discard(ctx context.Context, tenantID string, ids []string) (intakes.DiscardResult, error) {
	s.note(ctx, "Discard", tenantID, "")
	s.ids = ids
	return s.discard, s.err
}

func (s *intakeServiceSpy) ReplaceItems(ctx context.Context, tenantID, id string, items []intakes.Item, mode intakes.EditMode) (intakes.Detail, error) {
	s.note(ctx, "ReplaceItems", tenantID, id)
	s.items, s.mode = items, mode
	return s.detail, s.err
}

func (s *intakeServiceSpy) Approve(ctx context.Context, tenantID, id, renderedText string) (intakes.Detail, error) {
	s.note(ctx, "Approve", tenantID, id)
	s.text = renderedText
	return s.detail, s.err
}

func (s *intakeServiceSpy) RequestInfo(ctx context.Context, tenantID, id, question string) (intakes.Detail, error) {
	s.note(ctx, "RequestInfo", tenantID, id)
	s.text = question
	return s.detail, s.err
}

// intakeNow es el «ahora» de los tests de la bandeja: el reloj va inyectado (IntakesDeps.Now).
var intakeNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func intakeClock() time.Time { return intakeNow }

// intakeDeps son las dependencias de la bandeja con el reloj fijo y las features dadas
// encendidas.
func intakeDeps(svc apipublica.IntakeService, features ...string) apipublica.IntakesDeps {
	return apipublica.IntakesDeps{Intakes: svc, Entitlements: withFeatures(features...), Now: intakeClock}
}

// intakeCara monta la bandeja con k y d.
func intakeCara(k apipublica.Common, d apipublica.IntakesDeps) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountIntakes(c, k, d)
	return c
}

// intakeDo sirve UNA petición contra la bandeja del plan Basic (`cart_basic`, sin `llm_intake`)
// con un token del tenant A que trae los dos permisos.
func intakeDo(t *testing.T, svc apipublica.IntakeService, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := apipublicahelpertest.New(t)
	cara := intakeCara(h.Common(), intakeDeps(svc, entitlements.FeatureCartBasic))
	return h.Call(cara, h.With(tenantA, intakeReadPerm, intakeWritePerm), method, target, body)
}

// intakeWantCalls exige la lista EXACTA de llamadas que recibió el puerto.
func intakeWantCalls(t *testing.T, what string, svc *intakeServiceSpy, want ...string) {
	t.Helper()
	if strings.Join(svc.calls, ",") != strings.Join(want, ",") {
		t.Errorf("%s: el servicio recibió %v, quiero %v", what, svc.calls, want)
	}
}

func TestMountIntakes_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := intakeCara(h.Common(), intakeDeps(&intakeServiceSpy{}, entitlements.FeatureCartBasic))
	patterns := make([]string, 0, len(intakeRoutes))
	for _, r := range intakeRoutes {
		patterns = append(patterns, r.pattern)
	}
	wantPatterns(t, "G1–G6 y G8", cara, patterns)
	for _, r := range intakeRoutes {
		checkChain(t, h, cara, routeCase{id: r.id, method: r.method, target: r.target, body: r.body,
			perm: r.perm, resource: r.resource, want: http.StatusOK})
	}
}

func TestMountIntakes_BothDependenciesOrNothing(t *testing.T) {
	for name, d := range map[string]apipublica.IntakesDeps{
		"without_service":  {Entitlements: withFeatures(entitlements.FeatureCartBasic), Now: intakeClock},
		"without_resolver": {Intakes: &intakeServiceSpy{}, Now: intakeClock},
		"without_both":     {Now: intakeClock},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			cara := intakeCara(h.Common(), d)
			wantPatterns(t, name, cara, nil)
			for _, r := range intakeRoutes {
				rec := h.Call(cara, h.With(tenantA, intakeReadPerm, intakeWritePerm), r.method, r.target, r.body)
				wantCode(t, name+" "+r.id, rec, http.StatusNotFound)
			}
		})
	}
}

func TestMountIntakes_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		apipublica.MountIntakes(apipublica.Nueva(), apipublica.Common{}, intakeDeps(&intakeServiceSpy{}, entitlements.FeatureCartBasic))
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountIntakes") {
		t.Errorf("MountIntakes con MW nil: panic = %v; quiero un panic de cableado que nombre MountIntakes", v)
	}
}

// TestMountIntakes_GateIsCartBasicOnAllSeven: la puerta es `cart_basic` y solo ella. El plan
// Basic (sin `llm_intake`) entra a las siete; `llm_intake` sin `cart_basic` no abre ninguna; el
// resolver caído cierra; y sin el permiso, el 403 es el del permiso. El corte del gate no toca
// el servicio, y en las W deja su registro de auditoría (va por dentro de ella).
func TestMountIntakes_GateIsCartBasicOnAllSeven(t *testing.T) {
	down := entitlementshelpertest.NewFake()
	down.Err = errors.New("bd caída")
	cases := []struct {
		name     string
		resolver entitlements.Resolver
		granted  bool
		code     int
		body     string
	}{
		{"basic_plan_without_llm_intake_enters", withFeatures(entitlements.FeatureCartBasic), true, http.StatusOK, ""},
		{"llm_intake_does_not_open_the_door", withFeatures(entitlements.FeatureLLMIntake, entitlements.FeatureIntakesExport), true, http.StatusForbidden, intakeDenied},
		{"no_features_is_403", withFeatures(), true, http.StatusForbidden, intakeDenied},
		{"resolver_down_fails_closed", down, true, http.StatusForbidden, intakeDenied},
		{"permission_goes_before_the_feature", withFeatures(), false, http.StatusForbidden, `{"error":"permiso denegado"}`},
	}
	for _, tc := range cases {
		for _, r := range intakeRoutes {
			t.Run(tc.name+"/"+r.id, func(t *testing.T) {
				h := apipublicahelpertest.New(t)
				svc := &intakeServiceSpy{}
				cara := intakeCara(h.Common(), apipublica.IntakesDeps{Intakes: svc, Entitlements: tc.resolver, Now: intakeClock})
				grant := "otra.cosa"
				if tc.granted {
					grant = r.perm
				}
				rec := h.Call(cara, h.With(tenantA, grant), r.method, r.target, r.body)
				wantCode(t, r.id, rec, tc.code)
				if tc.body != "" {
					wantExactBody(t, r.id, rec, tc.body)
				}
				if tc.code == http.StatusOK {
					intakeWantCalls(t, r.id, svc, r.call)
					return
				}
				intakeWantCalls(t, r.id, svc)
				records := h.Auditor().Records()
				// Solo el corte del GATE de una W queda auditado: el del permiso corta antes.
				if r.resource == "" || !tc.granted {
					if len(records) != 0 {
						t.Errorf("%s: el 403 dejó %d registros de auditoría, quiero 0", r.id, len(records))
					}
					return
				}
				if len(records) != 1 || records[0].Result != "failure" || records[0].Meta["status"] != http.StatusForbidden ||
					records[0].Action != intakeWritePerm || records[0].Resource != intakeResource {
					t.Errorf("%s: el corte del gate dejó %+v, quiero UN registro failure/403 de %s sobre %s",
						r.id, records, intakeWritePerm, intakeResource)
				}
			})
		}
	}
}

// TestMountIntakes_TenantComesFromTheToken: las siete llaman al servicio con el tenant del token
// —uno en la query no cuenta—, con el contexto de la petición sin plazo propio, y ninguna
// respuesta lleva el tenant.
func TestMountIntakes_TenantComesFromTheToken(t *testing.T) {
	for _, r := range intakeRoutes {
		t.Run(r.id, func(t *testing.T) {
			svc := &intakeServiceSpy{}
			rec := intakeDo(t, svc, r.method, r.target+"?tenant_id="+apipublicahelpertest.TenantB, r.body)
			wantCode(t, r.id, rec, http.StatusOK)
			intakeWantCalls(t, r.id, svc, r.call)
			if svc.tenant != tenantA {
				t.Errorf("%s: el servicio recibió el tenant %q, quiero el del token %q", r.id, svc.tenant, tenantA)
			}
			if svc.hasDeadline {
				t.Errorf("%s: el contexto del servicio trae plazo; la bandeja no pone uno propio", r.id)
			}
			if body := rec.Body.String(); strings.Contains(body, tenantA) || strings.Contains(body, apipublicahelpertest.TenantB) {
				t.Errorf("%s: la respuesta lleva un tenant (%s); es el del token y no viaja", r.id, body)
			}
		})
	}
}

// TestMountIntakes_PathIDReachesTheService: el id de la ruta llega al servicio tal cual en las
// cinco rutas que cuelgan de una solicitud.
func TestMountIntakes_PathIDReachesTheService(t *testing.T) {
	for _, r := range intakeRoutes {
		if !strings.Contains(r.pattern, "{id}") {
			continue
		}
		svc := &intakeServiceSpy{}
		wantCode(t, r.id, intakeDo(t, svc, r.method, r.target, r.body), http.StatusOK)
		if svc.id != intakeID {
			t.Errorf("%s: el servicio recibió el id %q, quiero %q", r.id, svc.id, intakeID)
		}
	}
}

// TestMountIntakes_CorrectHasNoRouteOfItsOwn: la acción «Corregir» del 044 es G4 con
// `as_correction` (D-044.48 §1). El día que alguien añada POST …/correct habrá dos puertas
// dejando la misma revisión `corrected`.
func TestMountIntakes_CorrectHasNoRouteOfItsOwn(t *testing.T) {
	svc := &intakeServiceSpy{}
	rec := intakeDo(t, svc, http.MethodPost, intakeTarget+"/correct", `{"items":[]}`)
	wantCode(t, "POST …/correct", rec, http.StatusNotFound)
	intakeWantCalls(t, "POST …/correct", svc)
}

// TestMountIntakes_LiteralDiscardLivesWithTheWildcard: el POST al segmento literal entra al
// descarte; el GET a ese mismo camino no da 405 sino que lo sirve G2, con "discard" como id.
func TestMountIntakes_LiteralDiscardLivesWithTheWildcard(t *testing.T) {
	svc := &intakeServiceSpy{}
	wantCode(t, "POST discard", intakeDo(t, svc, http.MethodPost, intakesTarget+"/discard", `{"intake_ids":["a"]}`), http.StatusOK)
	intakeWantCalls(t, "POST discard", svc, "Discard")

	svc = &intakeServiceSpy{err: intakes.ErrNotFound}
	rec := intakeDo(t, svc, http.MethodGet, intakesTarget+"/discard", "")
	wantCode(t, "GET discard", rec, http.StatusNotFound)
	wantErrorBody(t, "GET discard", rec, intakeMsgNotFound)
	intakeWantCalls(t, "GET discard", svc, "Get")
	if svc.id != "discard" {
		t.Errorf("GET discard: Get recibió el id %q, quiero \"discard\"", svc.id)
	}
}

// TestMountIntakes_NoFailureRepeatsTheServiceError: el 500 de cada ruta es el suyo y no repite
// el error del servicio.
func TestMountIntakes_NoFailureRepeatsTheServiceError(t *testing.T) {
	msgs := map[string]string{
		"G1": "no se pudieron listar las solicitudes",
		"G2": "no se pudo leer la solicitud",
		"G3": "no se pudo cambiar el estado de la solicitud",
		"G4": "no se pudieron guardar las líneas de la solicitud",
		"G5": "no se pudo aprobar la solicitud",
		"G6": "no se pudo pedir más información sobre la solicitud",
		"G8": "no se pudieron descartar las solicitudes",
	}
	for _, r := range intakeRoutes {
		t.Run(r.id, func(t *testing.T) {
			svc := &intakeServiceSpy{err: errors.New(intakeDSN + ": conexión rechazada")}
			rec := intakeDo(t, svc, r.method, r.target, r.body)
			wantCode(t, r.id, rec, http.StatusInternalServerError)
			wantErrorBody(t, r.id, rec, msgs[r.id])
			intakeWantCalls(t, r.id, svc, r.call)
		})
	}
}
