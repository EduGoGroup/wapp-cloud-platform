// flows_durable_guard_test.go es el criterio EN PAREJA de T2.5 (Plan 054): las dos
// puertas HTTP de arranque comparten el mismo Starter, así que un test que solo
// cubriera una de las dos rutas no bastaría — es la regla del repo para dos
// endpoints con el mismo criterio (ver design.md del Plan 054, tasks.md T2 · criterio
// 2, y el estilo ya establecido en internal/publicapi/triggers_test.go y
// internal/flujos/admin/handlers_test.go).
package publicapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// testDurableGuardFlow es el flujo de único nodo "cart" (durable) que reproduce el
// hallazgo #001/#003: cualquier arranque sin evento debe rechazarse (T2.3).
const testDurableGuardFlow = "carrito-guarda-054"

func durableGuardCartFlow() model.Flow {
	return model.Flow{
		FlowID:  testDurableGuardFlow,
		Initial: "cart",
		Nodes: map[string]model.Node{
			"cart": {Type: "cart", Content: &model.ContentRef{Source: "json", Ref: "catalogo"}},
		},
	}
}

// durableGuardSender es un Sender que NUNCA debería invocarse en este test: la
// guarda de D-054.5 rechaza ANTES de EnterPrimed/Save, así que el envío ni se
// intenta. Sus métodos existen solo para satisfacer runtime.Sender.
type durableGuardSender struct{}

func (durableGuardSender) SendText(context.Context, string, string, string) (*cloudlinkv1.Ack, error) {
	return &cloudlinkv1.Ack{AckedCommandId: "no-debio-llamarse", Ok: true}, nil
}

func (durableGuardSender) SendMedia(context.Context, string, string, string, string, string, string, string) (*cloudlinkv1.Ack, error) {
	return &cloudlinkv1.Ack{AckedCommandId: "no-debio-llamarse", Ok: true}, nil
}

// durableGuardTenantResolver satisface runtime.TenantResolver (exigido por el
// constructor aunque Start() no lo consulte: solo lo usa HandleIncoming).
type durableGuardTenantResolver struct{ tenantID string }

func (r durableGuardTenantResolver) ResolveTenant(context.Context, string) (string, string, error) {
	return r.tenantID, "", nil
}

// newDurableGuardRuntime arma un runtime REAL (*runtime.Runtime, no un Starter de
// mentira) con el carrito registrado y el flujo de único nodo sembrado bajo tenantA:
// así el 409 de este test sale del MISMO sitio que en producción —la guarda de
// startLocked, Plan 054 · T2.3— y no de un mock que finja el error. Devuelve también
// el contact.Resolver para poder verificar, tras el rechazo, que NINGUNA fila de
// flow_state quedó sembrada bajo el contact_id opaco que el motor le habría asignado.
func newDurableGuardRuntime(t *testing.T) (*runtime.Runtime, *flowstore.MemoryRepository, contact.Resolver) {
	t.Helper()
	ctx := context.Background()
	repo := flowstore.NewMemoryRepository()
	repo.SetTenantContent(tenantA, "catalogo",
		[]byte(`{"categories":[{"code":"1","label":"Bebidas","items":[{"code":"1","sku":"CAFE","label":"Café","price":2.5}]}]}`))
	if _, err := repo.InsertDefinition(ctx, tenantA, durableGuardCartFlow()); err != nil {
		t.Fatalf("sembrar flujo durable: %v", err)
	}
	reg := modules.NewRegistry()
	reg.Register(cart.New())
	eng := engine.New(reg, engine.WithContentSource(content.NewRouter(content.NewStatic(), content.NewJSON(repo))))
	contacts := contact.NewMemoryResolver(repo)
	rt := runtime.New(repo, eng, durableGuardSender{}, durableGuardTenantResolver{tenantID: tenantA}, contacts, e2eLogger())
	return rt, repo, contacts
}

// durableGuardContactID resuelve el mismo contact_id opaco que el motor asigna
// internamente a un phone_e164, para poder comprobar el store después.
func durableGuardContactID(t *testing.T, resolver contact.Resolver, phone string) string {
	t.Helper()
	ref, err := contact.NewRef(contact.KindPhoneE164, phone)
	if err != nil {
		t.Fatalf("NewRef(%s): %v", phone, err)
	}
	id, err := resolver.Resolve(context.Background(), tenantA, []contact.Ref{ref}, "")
	if err != nil {
		t.Fatalf("Resolve(%s): %v", phone, err)
	}
	return id
}

// durableGuardRoute describe una de las dos rutas HTTP que comparten el mismo
// Starter (runtime.Runtime): el nombre para diagnóstico, la sesión/contacto que
// usa (distintos por ruta para no pisar el flow_state de la otra), si hay que
// vigilar que el cuerpo NO gane un campo `code` (solo publicapi, MD-054.3), y la
// función que dispara la petición real contra esa ruta.
type durableGuardRoute struct {
	name            string
	sessionID       string
	contactPhone    string
	forbidCodeField bool
	do              func(t *testing.T) (code int, body string)
}

// durableGuardRoutes arma las dos rutas sobre el MISMO *runtime.Runtime como
// Starter: admin/handlers.go:201 y publicapi/flows.go:129 comparten el motor,
// así que el 409 de las dos debe salir del mismo sitio (la guarda de
// startLocked, Plan 054 · T2.3).
func durableGuardRoutes(rt *runtime.Runtime) []durableGuardRoute {
	return []durableGuardRoute{
		{
			name:         "/admin/flows/start",
			sessionID:    "sess-admin",
			contactPhone: "+15550000001",
			do: func(t *testing.T) (int, string) {
				t.Helper()
				adminMux := http.NewServeMux()
				admin.Register(adminMux, nil, rt, nil)
				req := httptest.NewRequest(http.MethodPost, "/admin/flows/start",
					strings.NewReader(`{"flow_id":"`+testDurableGuardFlow+`","session_id":"sess-admin","contact":"+15550000001"}`))
				req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenantA, Subject: "op-durable-guard"}))
				rec := httptest.NewRecorder()
				adminMux.ServeHTTP(rec, req)
				return rec.Code, rec.Body.String()
			},
		},
		{
			name:            "/api/v1/flows/{id}/start",
			sessionID:       "sess-pub",
			contactPhone:    "+15550000002",
			forbidCodeField: true,
			do: func(t *testing.T) (int, string) {
				t.Helper()
				mux := newAPI(publicapi.Deps{FlowDeps: publicapi.FlowDeps{Starter: rt}}, apiKeys())
				rec := call(mux, keyAFull, http.MethodPost, "/api/v1/flows/"+testDurableGuardFlow+"/start",
					`{"session_id":"sess-pub","contact":"+15550000002"}`)
				return rec.Code, rec.Body.String()
			},
		},
	}
}

// assertDurable409Body centraliza las comprobaciones comunes a los dos cuerpos
// 409 (admin y publicapi, T2.5): debe ser DISTINGUIBLE del 409 que ya existe
// para ErrConversationExists (mismo texto en las dos rutas: "ya existe una
// conversación viva para la clave", handlers.go/flows.go); debe decirle al
// operador POR QUÉ (mencionar el evento que falta); y —D-B, Plan 054 · F2b,
// decisión de Jhoan 2026-08-12— NO debe aconsejar la vía inexistente que el
// aviso viejo sugería («arráncalo desde una conversación que ya tenga un evento
// activo», algo que NINGÚN endpoint de admin ni de /api/v1 permite hacer), sino
// apuntar a la única accionable: configurar una regla event_start
// (POST /api/v1/triggers).
func assertDurable409Body(t *testing.T, label, body string) {
	t.Helper()
	const convExistsText = "ya existe una conversación viva para la clave"
	if strings.Contains(body, convExistsText) {
		t.Fatalf("el 409 de %s NO debe confundirse con el de ErrConversationExists: %s", label, body)
	}
	if !strings.Contains(body, "evento") {
		t.Fatalf("el 409 de %s debe explicar el motivo (evento): %s", label, body)
	}
	const consejoInaccionable = "arráncalo desde una conversación que ya tenga"
	if strings.Contains(body, consejoInaccionable) {
		t.Fatalf("el 409 de %s NO debe aconsejar una vía que la API no ofrece: %s", label, body)
	}
	for _, want := range []string{"event_start", "/api/v1/triggers"} {
		if !strings.Contains(body, want) {
			t.Fatalf("el 409 de %s debe apuntar a la vía accionable (%q): %s", label, want, body)
		}
	}
}

// assertNoDurableGuardTrace comprueba que la ruta NO dejó rastro: cero
// flow_state bajo el contact_id opaco que el motor le habría asignado a la
// sesión/contacto de esa ruta.
func assertNoDurableGuardTrace(t *testing.T, repo *flowstore.MemoryRepository, contacts contact.Resolver, rte durableGuardRoute) {
	t.Helper()
	cid := durableGuardContactID(t, contacts, rte.contactPhone)
	if ok, err := repo.Exists(context.Background(), flowstore.Key{TenantID: tenantA, SessionID: rte.sessionID, ContactID: cid}); err != nil || ok {
		t.Fatalf("la ruta %s no debe dejar flow_state: ok=%v err=%v", rte.name, ok, err)
	}
}

// TestFlowsStart_FlujoDurable_409EnLasDosRutas es el criterio EN PAREJA de T2.5: el
// MISMO flujo durable (único nodo "cart"), arrancado primero por
// POST /admin/flows/start y después por POST /api/v1/flows/{id}/start (comparten
// Starter, admin/handlers.go:201 / publicapi/flows.go:129) ⇒ los DOS responden 409,
// los DOS con un cuerpo que distingue este 409 del de ErrConversationExists, y
// NINGUNO de los dos deja una fila de flow_state ni de intake.
func TestFlowsStart_FlujoDurable_409EnLasDosRutas(t *testing.T) {
	rt, repo, contacts := newDurableGuardRuntime(t)

	for _, rte := range durableGuardRoutes(rt) {
		t.Run(rte.name, func(t *testing.T) {
			code, body := rte.do(t)
			if code != http.StatusConflict {
				t.Fatalf("%s code=%d, quiero 409; body=%s", rte.name, code, body)
			}
			assertDurable409Body(t, rte.name, body)
			// publicapi (MD-054.3): SOLO {"error": "<texto>"}, sin campo `code` inventado.
			if rte.forbidCodeField && strings.Contains(body, `"code"`) {
				t.Fatalf("publicapi NO debe ganar un campo `code` no autorizado (MD-054.3): %s", body)
			}
			assertNoDurableGuardTrace(t, repo, contacts, rte)
		})
	}

	// Cero intakes: el flow_state por ruta ya se verificó en assertNoDurableGuardTrace.
	if got := repo.Intakes(); len(got) != 0 {
		t.Fatalf("ninguna de las dos rutas debe abrir solicitudes: %+v", got)
	}
}
