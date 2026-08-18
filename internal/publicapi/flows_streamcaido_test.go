// flows_streamcaido_test.go es el criterio EN PAREJA de T2.4 (Plan 050 · Ola 2) para
// el par de gemelos que NO estaba en el enunciado de la tarea: los dos writeStartError
// (internal/publicapi/flows.go y internal/flujos/admin/handlers.go). Las dos puertas
// HTTP de arranque comparten el mismo Starter y traducen el MISMO error con dos
// switch copiados, así que un test que cubriera solo una ruta dejaría la otra libre
// para divergir — misma regla y mismo arnés que flows_durable_guard_test.go (T2.5).
//
// Lo que este fichero protege, y el motivo de que la tarea creciera de dos sitios a
// cuatro: antes de T2.4 el stream caído caía al default de estos dos switch y salía un
// 500 «no se pudo iniciar la conversación», mientras el MISMO fallo devolvía 504 por
// /api/v1/messages en el mismo despliegue.
package publicapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// starterConError es un Starter que solo sabe fallar con el error programado: aquí lo
// que se prueba es el MAPEO HTTP, no el motor (el runtime real ya tiene sus tests).
type starterConError struct{ err error }

func (s starterConError) Start(context.Context, string, string, string, contact.Ref) (*cloudlinkv1.Ack, error) {
	return nil, s.err
}

// startStreamCaidoError es el doble del *gatewaygrpc.SendError que sale del envío del
// primer mensaje cuando el stream muere esperando el ack: cumple el contrato por
// duck-typing `interface{ StreamCaido() bool }` que los dos handlers consumen. La
// causa va envuelta con %w igual que en producción (runtime/send.go: "runtime: enviar
// texto: %w"), que es como el error cruza desde el Gateway hasta el mapeo.
type startStreamCaidoError struct {
	causa error
	caido bool
}

func (e *startStreamCaidoError) Error() string {
	return fmt.Sprintf("runtime: enviar texto: %v", e.causa)
}
func (e *startStreamCaidoError) Unwrap() error     { return e.causa }
func (e *startStreamCaidoError) StreamCaido() bool { return e.caido }

// rutaArranque es una de las dos puertas HTTP que traducen el error de Start.
type rutaArranque struct {
	nombre string
	do     func(t *testing.T, err error) (int, string)
}

// rutasArranque devuelve las dos rutas, cada una montada sobre su propio handler real
// (no un mock del handler): admin.StartHandler y el mux de publicapi.
func rutasArranque() []rutaArranque {
	return []rutaArranque{
		{
			nombre: "/admin/flows/start",
			do: func(t *testing.T, err error) (int, string) {
				t.Helper()
				adminMux := http.NewServeMux()
				admin.Register(adminMux, nil, starterConError{err: err}, nil)
				req := httptest.NewRequest(http.MethodPost, "/admin/flows/start",
					strings.NewReader(`{"flow_id":"menu-soporte","session_id":"s-1","contact":"+15550000001"}`))
				req = req.WithContext(httpapi.WithIdentity(req.Context(),
					httpapi.Identity{TenantID: tenantA, Subject: "op-streamcaido"}))
				rec := httptest.NewRecorder()
				adminMux.ServeHTTP(rec, req)
				return rec.Code, strings.TrimSpace(rec.Body.String())
			},
		},
		{
			nombre: "/api/v1/flows/{id}/start",
			do: func(t *testing.T, err error) (int, string) {
				t.Helper()
				mux := newAPI(publicapi.Deps{FlowDeps: publicapi.FlowDeps{Starter: starterConError{err: err}}}, apiKeys())
				rec := call(mux, keyAFull, http.MethodPost, "/api/v1/flows/menu-soporte/start",
					`{"session_id":"s-1","contact":"+15550000002"}`)
				return rec.Code, strings.TrimSpace(rec.Body.String())
			},
		},
	}
}

// TestFlowsStart_StreamCaido_504EnLasDosRutas: el MISMO error de stream caído por las
// DOS puertas ⇒ las dos responden 504 (no el 500 de antes, ni el 502 que pedía el
// enunciado: el comando ya viajó al Edge) y las dos dicen lo mismo. Además ejercita el
// 504 de siempre en cada ruta, porque sin ese contraste el caso nuevo pasaría contra
// un handler que devolviera un cuerpo único para todo.
func TestFlowsStart_StreamCaido_504EnLasDosRutas(t *testing.T) {
	for _, r := range rutasArranque() {
		t.Run(r.nombre, func(t *testing.T) {
			codigoCaido, cuerpoCaido := r.do(t, &startStreamCaidoError{caido: true})
			if codigoCaido != http.StatusGatewayTimeout {
				t.Fatalf("%s: code=%d, quiero 504 (antes de T2.4 salía un 500); cuerpo=%s",
					r.nombre, codigoCaido, cuerpoCaido)
			}

			codigoTimeout, cuerpoTimeout := r.do(t, fmt.Errorf("runtime: enviar texto: %w", context.DeadlineExceeded))
			if codigoTimeout != http.StatusGatewayTimeout {
				t.Fatalf("%s: timeout code=%d, quiero 504", r.nombre, codigoTimeout)
			}
			if !strings.Contains(cuerpoTimeout, "timeout esperando el ack del Edge") {
				t.Fatalf("%s: el cuerpo de siempre cambió: %s", r.nombre, cuerpoTimeout)
			}
			if cuerpoCaido == cuerpoTimeout {
				t.Fatalf("%s: los dos 504 devuelven el MISMO cuerpo (%s): no distinguen nada",
					r.nombre, cuerpoCaido)
			}

			// MD-054.5: accionable, y sin afirmar nada falso. Las tres señas están
			// verificadas contra el runtime (ver el comentario de msgStreamCaidoStart):
			// la conversación quedó abierta porque el Save precede al envío, y el
			// reintento devuelve 409 en vez de duplicar el arranque.
			if !strings.Contains(cuerpoCaido, "el stream del Edge se cerró") {
				t.Fatalf("%s: el 504 nuevo no se reconoce a simple vista: %s", r.nombre, cuerpoCaido)
			}
			if !strings.Contains(cuerpoCaido, "YA quedó abierta") {
				t.Fatalf("%s: el texto debe decir que la conversación quedó abierta: %s", r.nombre, cuerpoCaido)
			}
			if !strings.Contains(cuerpoCaido, "409") {
				t.Fatalf("%s: el texto debe avisar de que el reintento da 409: %s", r.nombre, cuerpoCaido)
			}
			if strings.Contains(cuerpoCaido, "no se pudo") {
				t.Fatalf("%s: el texto afirma algo falso; el mensaje pudo salir: %s", r.nombre, cuerpoCaido)
			}
		})
	}
}

// TestFlowsStart_StreamCaido_ElTextoEsElMISMOEnLasDosRutas es la mitad del criterio
// en pareja que ningún test por ruta puede dar: los dos switch están COPIADOS, así
// que la única forma de cazar que uno se toque y el otro no es compararlos. El
// publicapi responde JSON y el admin texto plano, de ahí el Contains en vez del igual.
func TestFlowsStart_StreamCaido_ElTextoEsElMISMOEnLasDosRutas(t *testing.T) {
	rutas := rutasArranque()
	_, cuerpoAdmin := rutas[0].do(t, &startStreamCaidoError{caido: true})
	_, cuerpoPublico := rutas[1].do(t, &startStreamCaidoError{caido: true})

	// El cuerpo admin es el texto pelado; el público lo lleva dentro del JSON, con las
	// comillas escapadas por el encoder — se compara la primera frase, que basta para
	// detectar que uno de los dos se editó sin el otro.
	const seña = "el stream del Edge se cerró antes del ack: la conversación YA quedó abierta"
	if !strings.Contains(cuerpoAdmin, seña) {
		t.Fatalf("/admin/flows/start no lleva el texto acordado: %s", cuerpoAdmin)
	}
	if !strings.Contains(cuerpoPublico, seña) {
		t.Fatalf("/api/v1/flows/{id}/start no lleva el texto acordado: %s", cuerpoPublico)
	}
}

// TestFlowsStart_StreamCaido_NoPisaLosOtrosCasos es la red de regresión que exige un
// switch LARGO: el caso nuevo se insertó entre el 502 y el 504, con los dos 409 del
// Plan 054 justo encima. Se comprueba en las DOS rutas por lo mismo de siempre.
func TestFlowsStart_StreamCaido_NoPisaLosOtrosCasos(t *testing.T) {
	casos := []struct {
		nombre string
		err    error
		code   int
		seña   string
	}{
		{"offline sigue 502", &startStreamCaidoError{causa: session.ErrSessionOffline, caido: true},
			http.StatusBadGateway, "sesión offline: no hay stream vivo para el Edge"},
		{"conversación existente sigue 409", runtime.ErrConversationExists,
			http.StatusConflict, "ya existe una conversación viva para la clave"},
		{"flujo durable sin evento sigue 409", runtime.ErrDurableFlowNeedsEvent,
			http.StatusConflict, "event_start"},
		{"otro error sigue 500", fmt.Errorf("boom"),
			http.StatusInternalServerError, "no se pudo iniciar la conversación"},
	}
	for _, r := range rutasArranque() {
		for _, c := range casos {
			t.Run(r.nombre+"/"+c.nombre, func(t *testing.T) {
				code, cuerpo := r.do(t, c.err)
				if code != c.code {
					t.Fatalf("code=%d, quiero %d; cuerpo=%s", code, c.code, cuerpo)
				}
				if !strings.Contains(cuerpo, c.seña) {
					t.Fatalf("cuerpo=%s, esperaba encontrar %q", cuerpo, c.seña)
				}
			})
		}
	}
}
