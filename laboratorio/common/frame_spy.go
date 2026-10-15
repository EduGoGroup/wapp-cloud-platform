//go:build laboratorio

package common

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
)

// SpyFrame envuelve un local.Frame y guarda la última petición que viajó por el cable, la salida
// cruda del modelo (ANTES de llm.ExtractJSON y del Parse… de la etapa) y lo que tardó. Es lo que deja
// al narrador enseñar «lo que salió de la nube» y «lo que contestó el modelo» por separado.
type SpyFrame struct {
	inner local.Frame

	mu   sync.Mutex
	last Trip
	n    int
}

var _ local.Frame = (*SpyFrame)(nil)

// Trip es una inferencia vista desde el cable.
type Trip struct {
	Request  edgegrpc.InferRequest
	Stage    Stage
	Output   string
	Err      error
	Duration time.Duration
}

// Spy envuelve f.
func Spy(f local.Frame) *SpyFrame { return &SpyFrame{inner: f} }

// Inner es el frame envuelto.
func (e *SpyFrame) Inner() local.Frame { return e.inner }

// Last devuelve el último viaje y cuántos van.
func (e *SpyFrame) Last() (Trip, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last, e.n
}

// Infer implementa local.Frame.
func (e *SpyFrame) Infer(ctx context.Context, tenantID string, req edgegrpc.InferRequest) (string, error) {
	start := time.Now()
	out, err := e.inner.Infer(ctx, tenantID, req)
	stage, _ := StageOf(req.Prompt)
	e.mu.Lock()
	e.last = Trip{Request: req, Stage: stage, Output: out, Err: err, Duration: time.Since(start)}
	e.n++
	e.mu.Unlock()
	return out, err
}
