package apipublica

// flows_internal_test.go — lo que de flows.go no se alcanza desde una ruta montada (05 E-4, P6:
// nace con el verde): la defensa de los handlers de I2–I4 ante una petición SIN identidad con
// empresa —RequirePermission corta antes— y ante un {id} vacío, que el mux no deja pasar. Se
// prueban llamando a los handlers a pelo.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// flowsCountingPorts es FlowsStore y admin.Starter a la vez: contesta el cero y cuenta las
// llamadas que le llegan por cualquiera de los dos puertos.
type flowsCountingPorts struct{ calls int }

var (
	_ FlowsStore    = (*flowsCountingPorts)(nil)
	_ admin.Starter = (*flowsCountingPorts)(nil)
)

func (p *flowsCountingPorts) InsertDefinition(context.Context, string, model.Flow) (int, error) {
	p.calls++
	return 0, nil
}

func (p *flowsCountingPorts) LatestDefinition(context.Context, string, string) (model.Flow, error) {
	p.calls++
	return model.Flow{}, nil
}

func (p *flowsCountingPorts) ListDefinitions(context.Context, string) ([]store.FlowSummary, error) {
	p.calls++
	return nil, nil
}

func (p *flowsCountingPorts) Start(context.Context, string, string, string, contact.Ref) (*cloudlinkv1.Ack, error) {
	p.calls++
	return &cloudlinkv1.Ack{}, nil
}

// flowsInternalHandlers son los tres handlers propios del área sobre los mismos puertos.
func flowsInternalHandlers(p *flowsCountingPorts) map[string]http.Handler {
	return map[string]http.Handler{
		"I2_list":  flowsListHandler(p),
		"I3_get":   flowsGetHandler(p),
		"I4_start": flowsStartHandler(p),
	}
}

const flowsInternalStartBody = `{"session_id":"s","contact":"15550001"}`

func TestFlowsHandlers_IdentityDefense(t *testing.T) {
	for ctxName, ctx := range map[string]context.Context{
		"without_identity":        context.Background(),
		"identity_without_tenant": httpapi.WithIdentity(context.Background(), httpapi.Identity{Subject: "alguien"}),
	} {
		ports := &flowsCountingPorts{}
		for name, handler := range flowsInternalHandlers(ports) {
			t.Run(ctxName+"/"+name, func(t *testing.T) {
				req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/flows/x/start", strings.NewReader(flowsInternalStartBody))
				req.SetPathValue("id", "x")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusUnauthorized || rec.Body.String() != `{"error":"autenticación requerida"}` {
					t.Errorf("código %d y cuerpo %s; quiero 401 {\"error\":\"autenticación requerida\"}", rec.Code, rec.Body.String())
				}
			})
		}
		if ports.calls != 0 {
			t.Errorf("%s: los puertos recibieron %d llamadas sin identidad, quiero 0", ctxName, ports.calls)
		}
	}
}

// TestFlowsHandlers_EmptyPathID: I3 e I4 rechazan un {id} vacío antes de leer el cuerpo y sin
// tocar ningún puerto; con el id puesto, la misma petición pasa.
func TestFlowsHandlers_EmptyPathID(t *testing.T) {
	ctx := httpapi.WithIdentity(context.Background(), httpapi.Identity{TenantID: "tenant-a", Subject: "alguien"})
	for _, name := range []string{"I3_get", "I4_start"} {
		t.Run(name, func(t *testing.T) {
			ports := &flowsCountingPorts{}
			handler := flowsInternalHandlers(ports)[name]

			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(`{no es json`))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || rec.Body.String() != `{"error":"flow id requerido en la ruta"}` {
				t.Errorf("id vacío: código %d y cuerpo %s; quiero 400 {\"error\":\"flow id requerido en la ruta\"}", rec.Code, rec.Body.String())
			}
			if ports.calls != 0 {
				t.Errorf("id vacío: los puertos recibieron %d llamadas, quiero 0", ports.calls)
			}

			req = httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(flowsInternalStartBody))
			req.SetPathValue("id", "x")
			rec = httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || ports.calls != 1 {
				t.Errorf("con id: código %d y %d llamadas a los puertos; quiero 200 y 1", rec.Code, ports.calls)
			}
		})
	}
}
