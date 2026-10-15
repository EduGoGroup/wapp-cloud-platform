//go:build laboratorio

package main

import (
	"context"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

// notifyingFrame es el SpyFrame del laboratorio con un aviso: cada vez que una inferencia termina,
// deja una señal en done. Con eso el programa sabe que el modelo YA contestó y que, si el pool no
// entrega nada al agregador enseguida, es porque descartó la respuesta.
type notifyingFrame struct {
	*common.SpyFrame
	done chan struct{}
}

var _ local.Frame = (*notifyingFrame)(nil)

// Infer implementa local.Frame.
func (f *notifyingFrame) Infer(ctx context.Context, tenantID string, req edgegrpc.InferRequest) (string, error) {
	out, err := f.SpyFrame.Infer(ctx, tenantID, req)
	select {
	case f.done <- struct{}{}:
	default:
	}
	return out, err
}
