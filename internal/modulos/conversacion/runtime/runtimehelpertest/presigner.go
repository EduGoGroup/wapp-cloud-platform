package runtimehelpertest

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

// Presigner es el doble de runtime.Presigner: no firma nada ni toca el almacén; apunta las claves
// que se le piden y contesta una URL y una caducidad fijas, o el error que se le haya inyectado.
// No mira el reloj: la caducidad es la que se le dio al construirlo.
//
// Seguro para uso concurrente.
type Presigner struct {
	mu        sync.Mutex
	url       string
	expiresAt time.Time
	err       error
	keys      []string
}

var _ runtime.Presigner = (*Presigner)(nil)

// NewPresigner devuelve un Presigner que contesta SIEMPRE esa URL con esa caducidad.
func NewPresigner(url string, expiresAt time.Time) *Presigner {
	return &Presigner{url: url, expiresAt: expiresAt}
}

// GenerateDownloadURL apunta la clave y devuelve la URL y la caducidad fijas; con un error
// inyectado, ("", instante cero, error).
func (p *Presigner) GenerateDownloadURL(_ context.Context, key string) (url string, expiresAt time.Time, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys = append(p.keys, key)
	if p.err != nil {
		return "", time.Time{}, p.err
	}
	return p.url, p.expiresAt, nil
}

// Fail hace que las llamadas siguientes fallen con err; nil las vuelve a dejar pasar.
func (p *Presigner) Fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// Keys devuelve las claves pedidas, fallaran o no, en orden.
func (p *Presigner) Keys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.keys...)
}
