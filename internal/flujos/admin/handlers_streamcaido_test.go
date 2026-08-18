package admin_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// startStreamCaidoError es el doble del *gatewaygrpc.SendError que sale del envío
// del primer mensaje cuando el stream de la sesión muere esperando el ack. Cumple el
// contrato por duck-typing que el handler consume, `interface{ StreamCaido() bool }`;
// que el Gateway real lo cumpla se prueba en internal/gateway/grpc. El campo caido es
// configurable a propósito: un doble que solo supiera decir "sí" no probaría que el
// handler mira el BOOL y no la mera presencia del método.
//
// La causa va envuelta con %w igual que en producción (runtime/send.go: "runtime:
// enviar texto: %w"), porque es así como el error cruza desde el Gateway hasta aquí.
type startStreamCaidoError struct {
	causa error
	caido bool
}

func (e *startStreamCaidoError) Error() string {
	return fmt.Sprintf("runtime: enviar texto: %v", e.causa)
}
func (e *startStreamCaidoError) Unwrap() error     { return e.causa }
func (e *startStreamCaidoError) StreamCaido() bool { return e.caido }

// startConError dispara POST /admin/flows/start con el error indicado y devuelve el
// código y el cuerpo (texto plano de http.Error, sin el salto de línea final).
func startConError(t *testing.T, err error) (int, string) {
	t.Helper()
	rec := do(admin.StartHandler(&fakeStarter{err: err}), http.MethodPost, "/admin/flows/start", validStartBody)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

// TestStartHandler_504_StreamCaido (Plan 050 · Ola 2 · T2.4) es la sonda mínima del
// lado admin, en el mismo estilo que TestStartHandler_DurableFlowNeedsEvent; el
// criterio EN PAREJA —las DOS rutas HTTP que comparten Starter— vive en
// internal/publicapi/flows_streamcaido_test.go.
//
// Antes de T2.4 este error caía al default y salía un 500 «no se pudo iniciar la
// conversación»: código equivocado (no falló el servidor), causa oculta, e
// incoherente con el 504 que el MISMO fallo ya devolvía por /admin/messages/send.
func TestStartHandler_504_StreamCaido(t *testing.T) {
	codigoCaido, cuerpoCaido := startConError(t, &startStreamCaidoError{caido: true})
	if codigoCaido != http.StatusGatewayTimeout {
		t.Fatalf("stream caído: code=%d, quiero 504 (antes de T2.4 esto era un 500)", codigoCaido)
	}

	// El 504 de siempre no se mueve: sin este contraste, un handler que devolviera
	// SIEMPRE el mismo cuerpo pasaría el caso de arriba.
	codigoTimeout, cuerpoTimeout := startConError(t, errWrap(context.DeadlineExceeded))
	if codigoTimeout != http.StatusGatewayTimeout {
		t.Fatalf("timeout: code=%d, quiero 504", codigoTimeout)
	}
	if cuerpoTimeout != "timeout esperando el ack del Edge" {
		t.Fatalf("timeout: el cuerpo de siempre cambió: %q", cuerpoTimeout)
	}
	if cuerpoCaido == cuerpoTimeout {
		t.Fatalf("los dos 504 devuelven el MISMO cuerpo (%q): el mapeo nuevo no distingue nada", cuerpoCaido)
	}
	assertCuerpoStreamCaidoStart(t, "/admin/flows/start", cuerpoCaido)
}

// TestStartHandler_504_StreamNoCaido_UsaElTextoViejo: el handler mira el VALOR de
// StreamCaido(), no si el error implementa el método. Sin este caso, un
// `errors.As(err, &caido)` que no comprobara el bool pasaría igual.
func TestStartHandler_504_StreamNoCaido_UsaElTextoViejo(t *testing.T) {
	code, cuerpo := startConError(t, &startStreamCaidoError{causa: context.DeadlineExceeded, caido: false})

	if code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d, quiero 504", code)
	}
	if cuerpo != "timeout esperando el ack del Edge" {
		t.Fatalf("StreamCaido()=false debe dar el cuerpo de siempre, no el nuevo: %q", cuerpo)
	}
}

// TestStartHandler_StreamCaido_NoPisaLosOtrosCasos es la red de regresión que exige
// este switch por ser LARGO: el caso nuevo se insertó entre el 502 y el 504, y los
// dos 409 del Plan 054 están justo encima. Un caso mal colocado —o un error que
// cumpla dos condiciones a la vez— los taparía sin que nada más lo note.
func TestStartHandler_StreamCaido_NoPisaLosOtrosCasos(t *testing.T) {
	casos := []struct {
		nombre string
		err    error
		code   int
		cuerpo string
	}{
		{"offline sigue 502", &startStreamCaidoError{causa: session.ErrSessionOffline, caido: true},
			http.StatusBadGateway, "sesión offline: no hay stream vivo para el Edge"},
		{"conversación existente sigue 409", runtime.ErrConversationExists,
			http.StatusConflict, "ya existe una conversación viva para la clave"},
		{"otro error sigue 500", fmt.Errorf("boom"),
			http.StatusInternalServerError, "no se pudo iniciar la conversación"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			code, cuerpo := startConError(t, c.err)
			if code != c.code {
				t.Fatalf("code=%d, quiero %d; cuerpo=%q", code, c.code, cuerpo)
			}
			if cuerpo != c.cuerpo {
				t.Fatalf("cuerpo=%q, quiero %q", cuerpo, c.cuerpo)
			}
		})
	}

	// El 409 durable del Plan 054 lleva un texto largo: se comprueba por sus señas.
	code, cuerpo := startConError(t, runtime.ErrDurableFlowNeedsEvent)
	if code != http.StatusConflict {
		t.Fatalf("durable: code=%d, quiero 409", code)
	}
	for _, want := range []string{"evento", "event_start", "/api/v1/triggers"} {
		if !strings.Contains(cuerpo, want) {
			t.Fatalf("el 409 durable del Plan 054 perdió %q: %q", want, cuerpo)
		}
	}
}

// assertCuerpoStreamCaidoStart concentra lo que el cuerpo del 504 nuevo debe decir en
// las DOS rutas de arranque (MD-054.5: accionable de verdad, y sin afirmar nada falso).
// Vive aquí y se repite en publicapi/flows_streamcaido_test.go por la misma razón que
// el propio texto: son dos paquetes que no se importan.
func assertCuerpoStreamCaidoStart(t *testing.T, ruta, cuerpo string) {
	t.Helper()
	if !strings.Contains(cuerpo, "el stream del Edge se cerró") {
		t.Fatalf("%s: el 504 del stream caído no se reconoce a simple vista: %q", ruta, cuerpo)
	}
	// La conversación quedó abierta DE VERDAD (el Save precede al envío en
	// runtime/start.go), así que el texto lo afirma en vez de insinuarlo.
	if !strings.Contains(cuerpo, "YA quedó abierta") {
		t.Fatalf("%s: el texto debe decir que la conversación quedó abierta: %q", ruta, cuerpo)
	}
	// Y reintentar no duplica el arranque: devuelve 409. Decirlo evita que el
	// llamante gaste el reintento inútil que el texto genérico invitaría a hacer.
	if !strings.Contains(cuerpo, "409") {
		t.Fatalf("%s: el texto debe avisar de que el reintento da 409: %q", ruta, cuerpo)
	}
	if strings.Contains(cuerpo, "no se pudo") {
		t.Fatalf("%s: el texto afirma algo falso; el mensaje pudo salir: %q", ruta, cuerpo)
	}
}
