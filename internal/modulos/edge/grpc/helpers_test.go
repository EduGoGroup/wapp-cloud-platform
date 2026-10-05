package grpc

// Los dobles que comparten los tests del paquete. Va en un fichero sin etiqueta de
// compilación para que cada fichero de test pueda pasar a verde por separado.

import (
	"bytes"
	"strings"
	"sync"

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
