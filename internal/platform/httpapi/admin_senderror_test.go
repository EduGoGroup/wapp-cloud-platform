package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// sendStreamCaidoError es el doble del *gatewaygrpc.SendError cuando el stream de la
// sesión murió esperando el ack: cumple los DOS contratos por duck-typing que el
// handler consume, `interface{ CommandID() string }` e `interface{ StreamCaido() bool }`.
// Que el Gateway real los cumpla se prueba en internal/gateway/grpc; aquí se prueba
// que el handler los consume. El campo caido es configurable a propósito: un doble
// que solo supiera decir "sí" no probaría que el handler mira el BOOL y no la mera
// presencia del método.
type sendStreamCaidoError struct {
	cmdID string
	causa error
	caido bool
}

func (e *sendStreamCaidoError) Error() string     { return fmt.Sprintf("comando %s: %v", e.cmdID, e.causa) }
func (e *sendStreamCaidoError) Unwrap() error     { return e.causa }
func (e *sendStreamCaidoError) CommandID() string { return e.cmdID }
func (e *sendStreamCaidoError) StreamCaido() bool { return e.caido }

// enviarAdmin hace el POST admin de envío con el error indicado y devuelve el código
// y el cuerpo (texto plano de http.Error, sin el salto de línea final).
func enviarAdmin(t *testing.T, err error) (int, string) {
	t.Helper()
	h := httpapi.SendMessageHandler(&fakeSender{err: err}, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/admin/messages/send",
		strings.NewReader(`{"session_id":"s-1","to":"549110","text":"hola"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

// TestSendMessageHandler_504_StreamCaido_TextoDistintoDelTimeout es el gemelo admin
// del test de publicapi (Plan 050 · Ola 2 · T2.4). Existe por separado porque el
// mapeo está DUPLICADO —dos funciones writeSendError, una por paquete— y tocar solo
// una dejaría la mitad del API mintiendo sobre el mismo suceso. Ejercita los dos 504
// juntos: cualquiera por separado pasaría contra un handler que devolviera siempre el
// mismo cuerpo.
//
// Los dos son 504 y no es un descuido: en ambos el comando ya viajó al Edge (si el
// Push hubiera fallado sería ErrSessionOffline y un 502), así que la única respuesta
// honesta es «no sé si llegó», nunca «no llegó».
func TestSendMessageHandler_504_StreamCaido_TextoDistintoDelTimeout(t *testing.T) {
	codigoCaido, cuerpoCaido := enviarAdmin(t, &sendStreamCaidoError{cmdID: "cmd-caido", caido: true})
	if codigoCaido != http.StatusGatewayTimeout {
		t.Fatalf("stream caído: status=%d, quiero 504 (el comando YA viajó; un 502 diría «no salió»)", codigoCaido)
	}
	// En el admin el command_id no va en JSON: se interpola en el propio texto, y sin
	// él un 504 no permite averiguar después si el mensaje llegó a salir.
	if !strings.Contains(cuerpoCaido, "(command_id: cmd-caido)") {
		t.Fatalf("el 504 llegó sin el command_id interpolado: %q", cuerpoCaido)
	}

	codigoTimeout, cuerpoTimeout := enviarAdmin(t, fmt.Errorf("esperando ack: %w", context.DeadlineExceeded))
	if codigoTimeout != http.StatusGatewayTimeout {
		t.Fatalf("timeout: status=%d, quiero 504", codigoTimeout)
	}
	if cuerpoTimeout != "timeout esperando el ack del Edge" {
		t.Fatalf("timeout: el cuerpo de siempre cambió: %q", cuerpoTimeout)
	}

	if cuerpoCaido == cuerpoTimeout {
		t.Fatalf("los dos 504 devuelven el MISMO cuerpo (%q): el mapeo nuevo no distingue nada", cuerpoCaido)
	}
	if !strings.Contains(cuerpoCaido, "el stream del Edge se cerró") {
		t.Fatalf("el 504 del stream caído no se reconoce a simple vista: %q", cuerpoCaido)
	}
	// MD-054.5: accionable de verdad — verificar antes de reenviar, no reintentar, y
	// sin afirmar que el mensaje no salió (sería falso).
	if !strings.Contains(cuerpoCaido, "ANTES de reenviar") {
		t.Fatalf("el texto no dice qué hacer (verificar antes de reenviar): %q", cuerpoCaido)
	}
	if strings.Contains(cuerpoCaido, "no se pudo enviar") {
		t.Fatalf("el texto afirma algo falso: el comando viajó y el mensaje pudo salir: %q", cuerpoCaido)
	}
}

// TestSendMessageHandler_504_StreamNoCaido_UsaElTextoViejo cierra el hueco del test
// de arriba: el handler mira el VALOR de StreamCaido(), no si el error implementa el
// método. Sin este caso, un `errors.As(err, &caido)` sin comprobar el bool pasaría.
func TestSendMessageHandler_504_StreamNoCaido_UsaElTextoViejo(t *testing.T) {
	code, cuerpo := enviarAdmin(t,
		&sendStreamCaidoError{cmdID: "cmd-9", causa: context.DeadlineExceeded, caido: false})

	if code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d, quiero 504", code)
	}
	if cuerpo != "timeout esperando el ack del Edge (command_id: cmd-9)" {
		t.Fatalf("StreamCaido()=false debe dar el cuerpo de siempre, no el nuevo: %q", cuerpo)
	}
}

// TestSendMessageHandler_StreamCaido_NoPisaEl502DeOffline: el caso nuevo comparte
// código con el timeout, pero NO debe tragarse el 502. Con la sesión offline el
// comando no llegó a salir, y esa distinción es la que sostiene todo el contrato.
func TestSendMessageHandler_StreamCaido_NoPisaEl502DeOffline(t *testing.T) {
	code, cuerpo := enviarAdmin(t,
		&sendStreamCaidoError{cmdID: "cmd-off", causa: session.ErrSessionOffline, caido: true})

	if code != http.StatusBadGateway {
		t.Fatalf("status=%d, quiero 502: con la sesión offline el comando NO salió", code)
	}
	if !strings.HasPrefix(cuerpo, "sesión offline: no hay stream vivo para el Edge") {
		t.Fatalf("cuerpo del 502 cambiado: %q", cuerpo)
	}
}
