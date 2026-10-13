package grpc

// Los dobles que comparten los tests del paquete. Va en un fichero sin etiqueta de
// compilación para que cada fichero de test pueda pasar a verde por separado.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// logBuffer captura lo que escribe el logger desde cualquier goroutine, con su propio
// mutex para que -race no tenga nada que decir.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *logBuffer) contains(sub string) bool {
	return strings.Contains(b.String(), sub)
}

// capturedLog devuelve un logger de texto que escribe en un logBuffer nuevo.
func capturedLog() (logger.Logger, *logBuffer) {
	buf := &logBuffer{}
	return logger.New(logger.WithWriter(buf)), buf
}

// quietLog da un logger mudo para los tests que no miran lo que se escribe.
func quietLog() logger.Logger {
	return logger.New(logger.WithWriter(io.Discard))
}

// newSigningKey genera una clave Ed25519 para el lease del test. Ninguna clave del
// repositorio: todas nacen aquí.
func newSigningKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generando la clave del test: %v", err)
	}
	return priv
}

// watchdog es el plazo tras el cual un test da por COLGADA una espera que debía resolverse
// en el acto. No sincroniza nada: solo convierte un cuelgue en un fallo legible.
const watchdog = 5 * time.Second

// await espera a que ch entregue (o se cierre) y devuelve lo recibido; si no ocurre dentro
// del watchdog, falla diciendo qué se esperaba.
func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(watchdog):
		t.Fatalf("colgado esperando: %s", what)
		panic("inalcanzable")
	}
}
