package runtimehelpertest

import (
	"context"
	"fmt"
	"sync"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

// Send es un envío que llegó al Sender de mentira: un texto o un adjunto.
type Send struct {
	// Media dice si entró por SendMedia (true) o por SendText (false).
	Media bool
	// SessionID y To son la sesión por la que sale y el destinatario.
	SessionID, To string
	// Text es el texto de un SendText; vacío en un adjunto.
	Text string
	// URL, Filename, Mime, Caption y Kind son los de un SendMedia; vacíos en un texto.
	URL, Filename, Mime, Caption, Kind string
	// CommandID es el AckedCommandId del Ack devuelto ("cmd-1", "cmd-2"…, numerados por envío
	// despachado, textos y adjuntos juntos). Vacío si el envío falló.
	CommandID string
	// Err es el error con que el Sender contestó; nil si se despachó.
	Err error
}

// Sender es el doble de runtime.Sender: no envía nada, apunta cada intento EN ORDEN —textos y
// adjuntos en una sola lista— y contesta un Ack correcto, o el error que se le haya inyectado.
//
// Seguro para uso concurrente. El valor cero sirve; NewSender existe por simetría.
type Sender struct {
	mu         sync.Mutex
	attempts   []Send
	dispatched int
	textErr    error
	mediaErr   error
	onSend     func(Send)
}

var _ runtime.Sender = (*Sender)(nil)

// NewSender devuelve un Sender sin envíos y sin errores inyectados.
func NewSender() *Sender { return &Sender{} }

// SendText apunta el intento. Sin error inyectado devuelve un Ack con Ok y el siguiente
// AckedCommandId; con él, (nil, error).
func (s *Sender) SendText(_ context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	return s.record(Send{SessionID: sessionID, To: to, Text: text})
}

// SendMedia apunta el intento. Sin error inyectado devuelve un Ack con Ok y el siguiente
// AckedCommandId; con él, (nil, error).
func (s *Sender) SendMedia(_ context.Context, sessionID, to, presignedURL, filename, mime, caption, kind string) (*cloudlinkv1.Ack, error) {
	return s.record(Send{Media: true, SessionID: sessionID, To: to, URL: presignedURL, Filename: filename, Mime: mime, Caption: caption, Kind: kind})
}

// record apunta el intento, con su resultado, y avisa al observador FUERA del candado.
func (s *Sender) record(send Send) (*cloudlinkv1.Ack, error) {
	s.mu.Lock()
	send.Err = s.textErr
	if send.Media {
		send.Err = s.mediaErr
	}
	if send.Err == nil {
		s.dispatched++
		send.CommandID = fmt.Sprintf("cmd-%d", s.dispatched)
	}
	s.attempts = append(s.attempts, send)
	onSend := s.onSend
	s.mu.Unlock()

	if onSend != nil {
		onSend(send)
	}
	if send.Err != nil {
		return nil, send.Err
	}
	return &cloudlinkv1.Ack{AckedCommandId: send.CommandID, Ok: true}, nil
}

// FailText hace que los SendText siguientes fallen con err; nil los vuelve a dejar pasar.
func (s *Sender) FailText(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.textErr = err
}

// FailMedia hace que los SendMedia siguientes fallen con err; nil los vuelve a dejar pasar.
func (s *Sender) FailMedia(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mediaErr = err
}

// OnSend registra una función que se llama en CADA intento, dentro de la llamada a SendText o
// SendMedia y antes de que devuelva: sirve para mirar el resto del mundo en el instante del envío
// (p. ej. que el estado ya estaba guardado). nil la retira. La función puede llamar al Sender.
func (s *Sender) OnSend(fn func(Send)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSend = fn
}

// Attempts devuelve todos los intentos, despachados o fallidos, en orden.
func (s *Sender) Attempts() []Send {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Send(nil), s.attempts...)
}

// Sends devuelve los envíos DESPACHADOS (sin error), textos y adjuntos, en orden.
func (s *Sender) Sends() []Send {
	return s.filter(func(send Send) bool { return send.Err == nil })
}

// Media devuelve los adjuntos despachados, en orden.
func (s *Sender) Media() []Send {
	return s.filter(func(send Send) bool { return send.Err == nil && send.Media })
}

// Texts devuelve el texto de los SendText despachados, en orden.
func (s *Sender) Texts() []string {
	sent := s.filter(func(send Send) bool { return send.Err == nil && !send.Media })
	texts := make([]string, 0, len(sent))
	for _, send := range sent {
		texts = append(texts, send.Text)
	}
	return texts
}

// filter devuelve los intentos que cumplen keep, en orden.
func (s *Sender) filter(keep func(Send) bool) []Send {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Send, 0, len(s.attempts))
	for _, send := range s.attempts {
		if keep(send) {
			out = append(out, send)
		}
	}
	return out
}
