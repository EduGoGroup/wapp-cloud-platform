//go:build pendiente

package grpc

// El contrato de send.go por la API exportada: el frame que se empuja, el *SendError y sus
// caminos de salida que no necesitan un Ack (sesión offline, reloj propio, llamante que se
// rinde). Los caminos que SÍ lo necesitan —el Ack que llega, el stream que cae— se afirman en
// send_await_test.go, que nace con el verde porque entrega el Ack por deliverAck (no exportado).

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// El contrato por duck-typing de los handlers HTTP: las interfaces anónimas, no un tipo
// compartido. Si un método cambia de nombre o de firma, esto deja de compilar.
var (
	_ interface{ StreamCaido() bool } = (*SendError)(nil)
	_ interface{ CommandID() string } = (*SendError)(nil)
	_ interface{ SessionID() string } = (*SendError)(nil)
	_ error                           = (*SendError)(nil)
)

// uuidV4 es la forma del command_id: UUID versión 4, variante 10, en minúsculas.
var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// captureSender es un session.Sender que acepta todo y apunta cada frame: el Edge que recibe
// el comando y NUNCA acusa.
type captureSender struct {
	mu     sync.Mutex
	frames []*cloudlinkv1.CloudToEdge
}

func (c *captureSender) Send(msg *cloudlinkv1.CloudToEdge) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, msg)
	return nil
}

// only devuelve el único frame recibido, o falla.
func (c *captureSender) only(t *testing.T) *cloudlinkv1.CloudToEdge {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.frames) != 1 {
		t.Fatalf("el Edge recibió %d frames, se esperaba 1", len(c.frames))
	}
	return c.frames[0]
}

// muteServer monta un Server con UNA sesión viva cuyo Edge no acusa nunca, y un plazo de Ack
// mínimo (1 ns) para que los envíos se rindan por su propio reloj sin esperar a nadie.
func muteServer(t *testing.T, sessionID string) (*Server, *captureSender, *logBuffer) {
	t.Helper()
	reg := session.NewRegistry()
	edge := &captureSender{}
	t.Cleanup(reg.Register(sessionID, edge))
	log, buf := capturedLog()
	return New(reg, log, WithAckTimeout(time.Nanosecond)), edge, buf
}

// asSendError extrae el *SendError de err, o falla.
func asSendError(t *testing.T, err error) *SendError {
	t.Helper()
	var se *SendError
	if !errors.As(err, &se) {
		t.Fatalf("el error no es *SendError: %v", err)
	}
	return se
}

// §5: el texto del centinela, byte a byte, con el prefijo gatewaygrpc: conservado (T-15).
func TestErrStreamClosedText(t *testing.T) {
	t.Parallel()
	const want = "gatewaygrpc: el stream de la sesión se cerró antes de que llegara el ack"
	if got := ErrStreamClosed.Error(); got != want {
		t.Fatalf("ErrStreamClosed = %q, se esperaba %q", got, want)
	}
}

// R-G11 · invariante numérica: el plazo del Ack por defecto (8 s) va POR DEBAJO del
// WriteTimeout del servidor HTTP (10 s), para que el 504 llegue a escribirse. El WriteTimeout
// no es importable (otro paquete, constante no exportada): se replica como aserción explícita.
func TestAckTimeoutBelowHTTPWriteTimeout(t *testing.T) {
	t.Parallel()
	const httpWriteTimeout = 10 * time.Second
	if defaultAckTimeout >= httpWriteTimeout {
		t.Fatalf("defaultAckTimeout=%v >= WriteTimeout HTTP=%v: el 504 no llegaría a escribirse",
			defaultAckTimeout, httpWriteTimeout)
	}
}

// R-G11: sesión offline → *SendError con el command_id generado dentro; el comando no salió.
func TestSendTextOfflineReturnsSendErrorWithCommandID(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())

	ack, err := srv.SendText(context.Background(), "ghost", "573001112233", "texto secreto")

	if ack != nil {
		t.Fatalf("SendText a una sesión offline devolvió un Ack: %v", ack)
	}
	if !errors.Is(err, session.ErrSessionOffline) {
		t.Fatalf("errors.Is(err, ErrSessionOffline) = false; err = %v", err)
	}
	se := asSendError(t, err)
	if !uuidV4.MatchString(se.CommandID()) {
		t.Errorf("command_id = %q, no es un UUIDv4", se.CommandID())
	}
	if se.SessionID() != "ghost" {
		t.Errorf("SessionID() = %q, se esperaba ghost", se.SessionID())
	}
	if se.StreamCaido() {
		t.Error("StreamCaido() = true para una sesión offline al empujar: ahí el mensaje NO salió")
	}
	cause := se.Unwrap()
	if !errors.Is(cause, session.ErrSessionOffline) {
		t.Errorf("Unwrap() = %v, se esperaba la causa del empuje", cause)
	}
	want := fmt.Sprintf("gatewaygrpc: comando %s a la sesión %s: %v", se.CommandID(), "ghost", cause)
	if got := se.Error(); got != want {
		t.Errorf("Error() = %q, se esperaba %q", got, want)
	}
	if strings.Contains(se.Error(), "573001112233") || strings.Contains(se.Error(), "texto secreto") {
		t.Errorf("Error() filtra el destino o el texto: %q", se.Error())
	}
}

// El frame que viaja: command_id (el mismo que luego trae el error), session_id y el payload
// SendText con el destino y el texto tal cual.
func TestSendTextPushesTheCommandFrame(t *testing.T) {
	t.Parallel()
	srv, edge, _ := muteServer(t, "s-1")

	_, err := srv.SendText(context.Background(), "s-1", "573001112233", "hola")

	frame := edge.only(t)
	if frame.GetSessionId() != "s-1" {
		t.Errorf("session_id del frame = %q, se esperaba s-1", frame.GetSessionId())
	}
	if !uuidV4.MatchString(frame.GetCommandId()) {
		t.Errorf("command_id del frame = %q, no es un UUIDv4", frame.GetCommandId())
	}
	if got := asSendError(t, err).CommandID(); got != frame.GetCommandId() {
		t.Errorf("command_id del error = %q, el del frame = %q: no correlacionan", got, frame.GetCommandId())
	}
	st := frame.GetSendText()
	if st == nil {
		t.Fatalf("el payload no es SendText: %T", frame.GetPayload())
	}
	if st.GetTo() != "573001112233" || st.GetText() != "hola" {
		t.Errorf("SendText = {to:%q text:%q}, se esperaba {573001112233 hola}", st.GetTo(), st.GetText())
	}
}

// R-G11: la espera del Ack tiene reloj PROPIO. El llamante pasa context.Background() a
// propósito —un handler HTTP no trae deadline—: si el reloj del gateway desapareciera, esta
// llamada no volvería nunca.
func TestSendTextGivesUpOnItsOwnClock(t *testing.T) {
	t.Parallel()
	srv, edge, logs := muteServer(t, "s-1")

	ack, err := srv.SendText(context.Background(), "s-1", "57301", "hola")

	if ack != nil {
		t.Fatalf("SendText devolvió un Ack que nadie mandó: %v", ack)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false; err = %v", err)
	}
	se := asSendError(t, err)
	if se.CommandID() != edge.only(t).GetCommandId() || se.SessionID() != "s-1" {
		t.Errorf("el error del plazo vencido no lleva el command_id y la sesión del envío: %v", err)
	}
	if se.StreamCaido() {
		t.Error("StreamCaido() = true para un plazo vencido: el Edge sigue conectado y solo va lento")
	}
	if errors.Is(err, ErrStreamClosed) {
		t.Error("el plazo vencido se confunde con el stream caído")
	}
	// Deja rastro a nivel de COMANDO: el command_id es lo único que permite averiguar después
	// si el mensaje llegó a salir.
	if !logs.contains("gateway: se agotó la espera del ack del Edge") || !logs.contains("command_id="+se.CommandID()) {
		t.Errorf("el plazo vencido no dejó rastro con su command_id: %q", logs.String())
	}
}

// El llamante que se rinde (ctx cancelado) recibe un *SendError que envuelve SU ctx.Err(),
// se rinda al empujar o al esperar.
func TestSendTextCallerCancelledWrapsItsContextError(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	t.Cleanup(reg.Register("s-1", &captureSender{}))
	srv := New(reg, quietLog()) // plazo de Ack por defecto: 8 s; quien corta es el llamante

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := srv.SendText(ctx, "s-1", "57301", "hola")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
	if se := asSendError(t, err); !uuidV4.MatchString(se.CommandID()) || se.StreamCaido() {
		t.Errorf("el error del llamante que se rinde no es el esperado: %v", err)
	}
}

// Cada envío lleva SU command_id: dos llamadas, dos identificadores.
func TestEachSendGetsItsOwnCommandID(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	seen := make(map[string]bool)
	for range 50 {
		_, err := srv.SendText(context.Background(), "ghost", "57301", "hola")
		id := asSendError(t, err).CommandID()
		if seen[id] {
			t.Fatalf("command_id repetido: %s", id)
		}
		seen[id] = true
	}
}

// SendMedia: el frame lleva la URL prefirmada y los metadatos tal cual, y kind se traduce al
// enum del proto; un kind desconocido (o vacío) cae a UNSPECIFIED.
func TestSendMediaPushesTheCommandFrame(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind string
		want cloudlinkv1.MediaKind
	}{
		{"document", cloudlinkv1.MediaKind_MEDIA_KIND_DOCUMENT},
		{"image", cloudlinkv1.MediaKind_MEDIA_KIND_IMAGE},
		{"video", cloudlinkv1.MediaKind_MEDIA_KIND_UNSPECIFIED},
		{"IMAGE", cloudlinkv1.MediaKind_MEDIA_KIND_UNSPECIFIED},
		{"", cloudlinkv1.MediaKind_MEDIA_KIND_UNSPECIFIED},
	}
	for _, tc := range cases {
		t.Run("kind "+tc.kind, func(t *testing.T) {
			t.Parallel()
			srv, edge, _ := muteServer(t, "s-1")

			_, err := srv.SendMedia(context.Background(), "s-1", "57301",
				"https://bucket.example/obj?sig=1", "factura.pdf", "application/pdf", "tu factura", tc.kind)

			frame := edge.only(t)
			if got := asSendError(t, err).CommandID(); got != frame.GetCommandId() || !uuidV4.MatchString(got) {
				t.Errorf("command_id del error = %q, el del frame = %q", got, frame.GetCommandId())
			}
			if frame.GetSessionId() != "s-1" {
				t.Errorf("session_id del frame = %q, se esperaba s-1", frame.GetSessionId())
			}
			sm := frame.GetSendMedia()
			if sm == nil {
				t.Fatalf("el payload no es SendMedia: %T", frame.GetPayload())
			}
			if sm.GetTo() != "57301" || sm.GetCaption() != "tu factura" || sm.GetMime() != "application/pdf" ||
				sm.GetFilename() != "factura.pdf" || sm.GetPresignedUrl() != "https://bucket.example/obj?sig=1" {
				t.Errorf("SendMedia no lleva los campos tal cual: %v", sm)
			}
			if sm.GetKind() != tc.want {
				t.Errorf("kind %q → %v, se esperaba %v", tc.kind, sm.GetKind(), tc.want)
			}
		})
	}
}

// SendMedia comparte los caminos de error de SendText: offline y reloj propio.
func TestSendMediaSharesTheErrorPaths(t *testing.T) {
	t.Parallel()
	t.Run("offline", func(t *testing.T) {
		t.Parallel()
		srv := New(session.NewRegistry(), quietLog())
		ack, err := srv.SendMedia(context.Background(), "ghost", "57301", "https://x.example/o", "a.png", "image/png", "", "image")
		if ack != nil || !errors.Is(err, session.ErrSessionOffline) {
			t.Fatalf("SendMedia offline = (%v, %v), se esperaba ErrSessionOffline", ack, err)
		}
		if se := asSendError(t, err); !uuidV4.MatchString(se.CommandID()) || se.SessionID() != "ghost" || se.StreamCaido() {
			t.Errorf("SendError inesperado: %v", err)
		}
	})
	t.Run("own clock", func(t *testing.T) {
		t.Parallel()
		srv, _, _ := muteServer(t, "s-1")
		ack, err := srv.SendMedia(context.Background(), "s-1", "57301", "https://x.example/o", "a.png", "image/png", "", "image")
		if ack != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("SendMedia sin Ack = (%v, %v), se esperaba DeadlineExceeded", ack, err)
		}
		if asSendError(t, err).StreamCaido() {
			t.Error("StreamCaido() = true para un plazo vencido")
		}
	})
}

// Ping empuja el frame con su nonce y su command_id y vuelve SIN esperar nada: el plazo de
// Ack es el de producción (8 s) y el Edge no contesta.
func TestPingPushesAndDoesNotWait(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	edge := &captureSender{}
	t.Cleanup(reg.Register("s-1", edge))
	srv := New(reg, quietLog())

	if err := srv.Ping(context.Background(), "s-1", 42); err != nil {
		t.Fatalf("Ping = %v", err)
	}

	frame := edge.only(t)
	if frame.GetSessionId() != "s-1" || !uuidV4.MatchString(frame.GetCommandId()) {
		t.Errorf("frame del Ping = {session:%q command:%q}", frame.GetSessionId(), frame.GetCommandId())
	}
	if p := frame.GetPing(); p == nil || p.GetNonce() != 42 {
		t.Errorf("payload del Ping = %v, se esperaba Ping{nonce:42}", frame.GetPayload())
	}
}

// Ping a una sesión offline devuelve el error del empuje TAL CUAL: no es un *SendError.
func TestPingOfflineReturnsThePushError(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())

	err := srv.Ping(context.Background(), "ghost", 1)

	if !errors.Is(err, session.ErrSessionOffline) {
		t.Fatalf("errors.Is(err, ErrSessionOffline) = false; err = %v", err)
	}
	var se *SendError
	if errors.As(err, &se) {
		t.Fatalf("Ping envolvió su error en *SendError: %v", err)
	}
}
