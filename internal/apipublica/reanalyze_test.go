package apipublica_test

// reanalyze_test.go — cubre el contrato de reanalyze.go (ReanalysisService, ReanalyzeDeps,
// MountReanalyze): el montaje, la cadena, la regla T-8 (la cara no pone gate de feature), la
// petición y el acuse de H1. Aquí viven además el doble y los auxiliares del área; la tabla de
// errores está en reanalyze_errors_test.go.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
)

const (
	reanalyzePattern  = "POST /api/v1/intakes/{id}/reanalyze"
	reanalyzePerm     = "intakes.write"
	reanalyzeResource = "intake"

	reanalyzeIntakeID = "0b3f6c1e-7a52-4d0e-9f11-2a6c5d8e9f01"
	reanalyzeJobID    = "5f1d2c3b-4a59-4e6f-8a7b-9c0d1e2f3a4b"
	reanalyzeTarget   = "/api/v1/intakes/" + reanalyzeIntakeID + "/reanalyze"

	reanalyzeMsgBadJSON = "cuerpo JSON inválido"
	reanalyzeAckBody    = `{"intake_id":"` + reanalyzeIntakeID + `","revision_no":3,"job_id":"` + reanalyzeJobID + `","via":"local","status":"processing"}`
)

// El puerto del re-análisis lo cumple el servicio REAL del módulo captación nuevo.
var _ apipublica.ReanalysisService = (*reanalisis.Service)(nil)

// reanalyzeServiceSpy es ReanalysisService: contesta lo que se le siembra (o err) y apunta cada
// llamada con su petición y si su contexto traía plazo.
type reanalyzeServiceSpy struct {
	out reanalisis.Result
	err error

	calls       int
	got         reanalisis.Request
	hasDeadline bool
}

var _ apipublica.ReanalysisService = (*reanalyzeServiceSpy)(nil)

func (s *reanalyzeServiceSpy) Reanalyze(ctx context.Context, req reanalisis.Request) (reanalisis.Result, error) {
	s.calls++
	s.got = req
	_, s.hasDeadline = ctx.Deadline()
	return s.out, s.err
}

// reanalyzeAck es el Result que el doble devuelve en el camino feliz: el de reanalyzeAckBody.
func reanalyzeAck() reanalisis.Result {
	return reanalisis.Result{IntakeID: reanalyzeIntakeID, RevisionNo: 3, JobID: reanalyzeJobID,
		Via: "local", Status: reanalisis.StatusInProgress}
}

// reanalyzeCara monta H1 con k y el servicio dado.
func reanalyzeCara(k apipublica.Common, svc apipublica.ReanalysisService) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountReanalyze(c, k, apipublica.ReanalyzeDeps{Reanalysis: svc})
	return c
}

// reanalyzeDo sirve UNA petición a H1 con un token del tenant A que trae SOLO el permiso de
// escritura, y devuelve también el banco para mirar la auditoría.
func reanalyzeDo(t *testing.T, svc apipublica.ReanalysisService, target, body string) (*apipublicahelpertest.Harness, *httptest.ResponseRecorder) {
	t.Helper()
	h := apipublicahelpertest.New(t)
	cara := reanalyzeCara(h.Common(), svc)
	return h, h.Call(cara, h.With(tenantA, reanalyzePerm), http.MethodPost, target, body)
}

// reanalyzeWantCalls exige cuántas veces se llamó al servicio.
func reanalyzeWantCalls(t *testing.T, what string, svc *reanalyzeServiceSpy, want int) {
	t.Helper()
	if svc.calls != want {
		t.Errorf("%s: el servicio recibió %d llamadas, quiero %d", what, svc.calls, want)
	}
}

// reanalyzeWantOneAudit exige EXACTAMENTE un registro de auditoría de H1 con ese resultado y ese
// código.
func reanalyzeWantOneAudit(t *testing.T, what string, h *apipublicahelpertest.Harness, result string, status int) {
	t.Helper()
	records := h.Auditor().Records()
	if len(records) != 1 {
		t.Fatalf("%s: H1 dejó %d registros de auditoría, quiero exactamente 1", what, len(records))
	}
	r := records[0]
	if r.TenantID != tenantA || r.Action != reanalyzePerm || r.Resource != reanalyzeResource || r.Result != result || r.Meta["status"] != status {
		t.Errorf("%s: registro %+v, quiero tenant %s, action %s, resource %s, result %s, status %d",
			what, r, tenantA, reanalyzePerm, reanalyzeResource, result, status)
	}
}

func TestMountReanalyze_Chain(t *testing.T) {
	h := apipublicahelpertest.New(t)
	svc := &reanalyzeServiceSpy{out: reanalyzeAck()}
	cara := reanalyzeCara(h.Common(), svc)
	wantPatterns(t, "H1", cara, []string{reanalyzePattern})
	checkChain(t, h, cara, routeCase{id: "H1", method: http.MethodPost, target: reanalyzeTarget, body: `{}`,
		perm: reanalyzePerm, resource: reanalyzeResource, want: http.StatusOK})
	// De las cuatro peticiones de checkChain solo la que pasa la cadena toca el servicio.
	reanalyzeWantCalls(t, "H1 cadena", svc, 1)
}

// TestMountReanalyze_NoServiceIs404: sin el caso de uso la ruta no existe (trampa T-11), y el
// montaje ni mira la cadena: tampoco hace panic con MW nil.
func TestMountReanalyze_NoServiceIs404(t *testing.T) {
	h := apipublicahelpertest.New(t)
	cara := apipublica.Nueva()
	apipublica.MountReanalyze(cara, h.Common(), apipublica.ReanalyzeDeps{})
	wantPatterns(t, "sin servicio", cara, nil)
	rec := h.Call(cara, h.With(tenantA, reanalyzePerm), http.MethodPost, reanalyzeTarget, `{}`)
	wantCode(t, "sin servicio", rec, http.StatusNotFound)

	if v := recuperar(func() {
		apipublica.MountReanalyze(apipublica.Nueva(), apipublica.Common{}, apipublica.ReanalyzeDeps{})
	}); v != nil {
		t.Errorf("MountReanalyze sin servicio y con MW nil: panic = %v; quiero que no monte nada ni mire la cadena", v)
	}
}

func TestMountReanalyze_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		apipublica.MountReanalyze(apipublica.Nueva(), apipublica.Common{}, apipublica.ReanalyzeDeps{Reanalysis: &reanalyzeServiceSpy{}})
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountReanalyze") {
		t.Errorf("MountReanalyze con MW nil: panic = %v; quiero un panic de cableado que nombre MountReanalyze", v)
	}
}

// TestMountReanalyze_DoesNotDependOnTheInbox: H1 se monta sola, sin MountIntakes al lado, y las
// rutas de la bandeja siguen sin existir en esa cara.
func TestMountReanalyze_DoesNotDependOnTheInbox(t *testing.T) {
	svc := &reanalyzeServiceSpy{out: reanalyzeAck()}
	h := apipublicahelpertest.New(t)
	cara := reanalyzeCara(h.Common(), svc)
	token := h.With(tenantA, reanalyzePerm, "intakes.read")
	wantCode(t, "H1 sola", h.Call(cara, token, http.MethodPost, reanalyzeTarget, `{}`), http.StatusOK)
	wantCode(t, "G2 sin bandeja", h.Call(cara, token, http.MethodGet, "/api/v1/intakes/"+reanalyzeIntakeID, ""), http.StatusNotFound)
}

// TestMountReanalyze_FaceHasNoFeatureGate es la trampa T-8: la cara no tiene con qué preguntar
// por una feature (ReanalyzeDeps lleva UN campo, el servicio) y un tenant sin NINGUNA feature
// —aquí no hay resolver que se la dé— LLEGA al servicio. El 403 de feature solo existe si lo
// decide el servicio, y entonces sale con su clave.
func TestMountReanalyze_FaceHasNoFeatureGate(t *testing.T) {
	deps := reflect.TypeFor[apipublica.ReanalyzeDeps]()
	if deps.NumField() != 1 || deps.Field(0).Name != "Reanalysis" {
		t.Errorf("ReanalyzeDeps tiene %d campos; quiero solo Reanalysis: los gates viven en el servicio (T-8)", deps.NumField())
	}

	svc := &reanalyzeServiceSpy{out: reanalyzeAck()}
	_, rec := reanalyzeDo(t, svc, reanalyzeTarget, `{"via":"chatgpt"}`)
	wantCode(t, "tenant sin features", rec, http.StatusOK)
	reanalyzeWantCalls(t, "tenant sin features", svc, 1)

	svc = &reanalyzeServiceSpy{err: reanalisis.FeatureMissingError{Feature: "llm_intake"}}
	h, rec := reanalyzeDo(t, svc, reanalyzeTarget, `{}`)
	wantCode(t, "gate del servicio", rec, http.StatusForbidden)
	wantExactBody(t, "gate del servicio", rec, `{"error":"feature_not_enabled","feature":"llm_intake"}`)
	reanalyzeWantCalls(t, "gate del servicio", svc, 1)
	reanalyzeWantOneAudit(t, "gate del servicio", h, "failure", http.StatusForbidden)
}

// TestMountReanalyze_TenantFromTokenAndPathID: el tenant es el del token —uno en la query o en el
// cuerpo no cuenta—, el id es el de la ruta tal cual, el contexto no trae plazo propio y la
// respuesta no lleva el tenant.
func TestMountReanalyze_TenantFromTokenAndPathID(t *testing.T) {
	svc := &reanalyzeServiceSpy{out: reanalyzeAck()}
	other := apipublicahelpertest.TenantB
	_, rec := reanalyzeDo(t, svc, reanalyzeTarget+"?tenant_id="+other, `{"tenant_id":"`+other+`","intake_id":"otro"}`)
	wantCode(t, "H1", rec, http.StatusOK)
	reanalyzeWantCalls(t, "H1", svc, 1)
	want := reanalisis.Request{TenantID: tenantA, IntakeID: reanalyzeIntakeID}
	if svc.got != want {
		t.Errorf("H1: el servicio recibió %+v, quiero %+v", svc.got, want)
	}
	if svc.hasDeadline {
		t.Error("H1: el contexto del servicio trae plazo; la puerta no pone uno propio")
	}
	if body := rec.Body.String(); strings.Contains(body, tenantA) || strings.Contains(body, other) {
		t.Errorf("H1: la respuesta lleva un tenant (%s); es el del token y no viaja", body)
	}

	// El id no se valida ni se normaliza aquí: lo que diga la ruta.
	svc = &reanalyzeServiceSpy{out: reanalyzeAck()}
	_, rec = reanalyzeDo(t, svc, "/api/v1/intakes/No-Es-UUID/reanalyze", `{}`)
	wantCode(t, "id libre", rec, http.StatusOK)
	if svc.got.IntakeID != "No-Es-UUID" {
		t.Errorf("id libre: el servicio recibió el id %q, quiero \"No-Es-UUID\"", svc.got.IntakeID)
	}
}

// TestMountReanalyze_AckIsTheServiceResultAsIs: el 200 es el ACUSE del §8.1 —cinco claves, en su
// orden, y no el detalle de la solicitud— con los valores del Result tal cual, `status`
// incluido; y `via` y `text` llegan al servicio sin recortar ni sanear.
func TestMountReanalyze_AckIsTheServiceResultAsIs(t *testing.T) {
	svc := &reanalyzeServiceSpy{out: reanalyzeAck()}
	h, rec := reanalyzeDo(t, svc, reanalyzeTarget, `{"via":" Local ","text":"  son 30\ntequeños  "}`)
	wantCode(t, "acuse", rec, http.StatusOK)
	wantExactBody(t, "acuse", rec, reanalyzeAckBody)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("acuse: Content-Type %q, quiero application/json", got)
	}
	if svc.got.Via != " Local " || svc.got.Text != "  son 30\ntequeños  " {
		t.Errorf("acuse: el servicio recibió via=%q text=%q; quiero los dos tal cual", svc.got.Via, svc.got.Text)
	}
	reanalyzeWantOneAudit(t, "acuse", h, "success", http.StatusOK)

	svc = &reanalyzeServiceSpy{out: reanalisis.Result{IntakeID: "i", RevisionNo: 0, JobID: "", Via: "api", Status: "otro"}}
	_, rec = reanalyzeDo(t, svc, reanalyzeTarget, `{}`)
	wantCode(t, "acuse tal cual", rec, http.StatusOK)
	wantExactBody(t, "acuse tal cual", rec, `{"intake_id":"i","revision_no":0,"job_id":"","via":"api","status":"otro"}`)
}

// TestMountReanalyze_BothFieldsAreOptional: `{}` es el caso normal («regenera otra vez, según el
// origen») y `null` vale lo mismo: los dos llegan al servicio con la vía y el texto vacíos.
func TestMountReanalyze_BothFieldsAreOptional(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"via":null,"text":null}`, `{"via":"","text":""}`} {
		svc := &reanalyzeServiceSpy{out: reanalyzeAck()}
		_, rec := reanalyzeDo(t, svc, reanalyzeTarget, body)
		wantCode(t, body, rec, http.StatusOK)
		wantExactBody(t, body, rec, reanalyzeAckBody)
		reanalyzeWantCalls(t, body, svc, 1)
		if svc.got.Via != "" || svc.got.Text != "" {
			t.Errorf("%s: el servicio recibió via=%q text=%q; un cuerpo vacío no inventa campos", body, svc.got.Via, svc.got.Text)
		}
	}
}

// TestMountReanalyze_UnknownFieldsAreIgnored: no hay campo `provider` —el proveedor sale siempre
// de `tenant_llm` (D-044.28 §a)— ni como nombre viejo de `via`: la petición corre por la vía del
// tenant, que es el desenlace seguro.
func TestMountReanalyze_UnknownFieldsAreIgnored(t *testing.T) {
	for _, body := range []string{
		`{"provider":"api"}`,
		`{"provider":"anthropic","model":"x","status":"done","job_id":"j","revision_no":9}`,
	} {
		svc := &reanalyzeServiceSpy{out: reanalyzeAck()}
		_, rec := reanalyzeDo(t, svc, reanalyzeTarget, body)
		wantCode(t, body, rec, http.StatusOK)
		want := reanalisis.Request{TenantID: tenantA, IntakeID: reanalyzeIntakeID}
		if svc.got != want {
			t.Errorf("%s: el servicio recibió %+v, quiero %+v", body, svc.got, want)
		}
	}
}

// TestMountReanalyze_UnreadableBodyNeverReachesTheService: un cuerpo ilegible es 400 y no abre
// trabajo; queda auditado como fallo.
func TestMountReanalyze_UnreadableBodyNeverReachesTheService(t *testing.T) {
	for _, body := range []string{"", "{no es json", `[]`, `"texto suelto"`, `{"via":7}`, `{"text":["a"]}`, `{"via":{"x":"api"}}`} {
		svc := &reanalyzeServiceSpy{out: reanalyzeAck()}
		h, rec := reanalyzeDo(t, svc, reanalyzeTarget, body)
		wantCode(t, body, rec, http.StatusBadRequest)
		wantErrorBody(t, body, rec, reanalyzeMsgBadJSON)
		reanalyzeWantCalls(t, body, svc, 0)
		reanalyzeWantOneAudit(t, body, h, "failure", http.StatusBadRequest)
	}
}

// TestMountReanalyze_BodyIsCappedAt8KiB: el tope es del CUERPO (8 KiB), no del texto: pasarse es
// el mismo 400 de cuerpo ilegible, y por debajo el texto llega entero aunque pase de las 280
// runas (ese tope lo aplica el servicio, que rechaza en vez de truncar).
func TestMountReanalyze_BodyIsCappedAt8KiB(t *testing.T) {
	const limit = 8 << 10
	wrap := func(n int) string { return `{"text":"` + strings.Repeat("a", n) + `"}` }
	overhead := len(wrap(0))

	svc := &reanalyzeServiceSpy{out: reanalyzeAck()}
	_, rec := reanalyzeDo(t, svc, reanalyzeTarget, wrap(limit-overhead))
	wantCode(t, "8 KiB justos", rec, http.StatusOK)
	if len(svc.got.Text) != limit-overhead {
		t.Errorf("8 KiB justos: el servicio recibió %d bytes de texto, quiero %d (la cara no recorta)", len(svc.got.Text), limit-overhead)
	}

	svc = &reanalyzeServiceSpy{out: reanalyzeAck()}
	_, rec = reanalyzeDo(t, svc, reanalyzeTarget, wrap(limit-overhead+1))
	wantCode(t, "8 KiB + 1", rec, http.StatusBadRequest)
	wantErrorBody(t, "8 KiB + 1", rec, reanalyzeMsgBadJSON)
	reanalyzeWantCalls(t, "8 KiB + 1", svc, 0)
}
