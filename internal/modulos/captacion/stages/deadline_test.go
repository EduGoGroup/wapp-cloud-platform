package stages

import (
	"context"
	"errors"
	"testing"
	"time"
)

// perCallOf aplica las opciones como lo hace el constructor de una etapa.
func perCallOf(opts ...Option) time.Duration {
	return newCallLimits(opts).perCall
}

// TestWithCallTimeout_OnlyAPositiveValueSetsTheLimit: un valor <= 0 es «no configurado»
// y deja lo que hubiera, no «cero segundos».
func TestWithCallTimeout_OnlyAPositiveValueSetsTheLimit(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
		want time.Duration
	}{
		{"no options inherits the caller", nil, 0},
		{"positive value sets the limit", []Option{WithCallTimeout(48 * time.Second)}, 48 * time.Second},
		{"zero is ignored", []Option{WithCallTimeout(0)}, 0},
		{"negative is ignored", []Option{WithCallTimeout(-time.Second)}, 0},
		{"zero after a value keeps the value", []Option{WithCallTimeout(time.Minute), WithCallTimeout(0)}, time.Minute},
		{"the last positive value wins", []Option{WithCallTimeout(time.Minute), WithCallTimeout(time.Second)}, time.Second},
		{"a nil option is skipped", []Option{nil, WithCallTimeout(time.Second), nil}, time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := perCallOf(c.opts...); got != c.want {
				t.Fatalf("plazo por llamada = %v; se esperaba %v", got, c.want)
			}
		})
	}
}

// TestBound_WithoutLimit_ReturnsTheSameContextAndACallableCancel: sin plazo propio se
// hereda el ctx del llamante TAL CUAL, y la cancelación devuelta se puede llamar.
func TestBound_WithoutLimit_ReturnsTheSameContextAndACallableCancel(t *testing.T) {
	type key struct{}
	parent := context.WithValue(context.Background(), key{}, "padre")

	ctx, cancel := newCallLimits(nil).bound(parent)
	if cancel == nil {
		t.Fatal("la cancelación devuelta es nil: el llamante no podría hacer defer cancel()")
	}
	if ctx != parent {
		t.Fatal("sin plazo propio el ctx tiene que ser el del llamante, sin envolver")
	}
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("sin plazo propio el ctx no puede traer deadline")
	}
	cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("cancelar sin plazo propio canceló el ctx del llamante: %v", err)
	}
}

// TestBound_WithLimit_SetsTheDeadlineAndCancelReleasesIt: con plazo, el ctx sale acotado
// a ese plazo, sigue siendo hijo del llamante y la cancelación lo cierra sin tocar al
// padre.
func TestBound_WithLimit_SetsTheDeadlineAndCancelReleasesIt(t *testing.T) {
	const limit = 48 * time.Second
	type key struct{}
	parent := context.WithValue(context.Background(), key{}, "padre")

	ctx, cancel := newCallLimits([]Option{WithCallTimeout(limit)}).bound(parent)
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("el ctx salió SIN deadline: el adaptador caería a su default de 30 s")
	}
	if remaining := time.Until(deadline); remaining > limit || remaining < limit-5*time.Second {
		t.Fatalf("plazo restante = %v; se esperaba ≈ %v", remaining, limit)
	}
	if ctx.Value(key{}) != "padre" {
		t.Fatal("el ctx acotado no es hijo del ctx del llamante")
	}

	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("tras cancelar, ctx.Err() = %v; se esperaba context.Canceled", ctx.Err())
	}
	if err := parent.Err(); err != nil {
		t.Fatalf("cancelar la llamada canceló el ctx del llamante: %v", err)
	}
}

// TestBound_NeverExtendsTheCallerDeadline: el plazo por llamada acota, no amplía. Si el
// llamante ya trae un deadline más corto, manda el suyo.
func TestBound_NeverExtendsTheCallerDeadline(t *testing.T) {
	parent, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	want, _ := parent.Deadline()

	ctx, cancel := newCallLimits([]Option{WithCallTimeout(time.Hour)}).bound(parent)
	defer cancel()

	got, ok := ctx.Deadline()
	if !ok || !got.Equal(want) {
		t.Fatalf("deadline = %v (ok=%v); se esperaba el del llamante, %v", got, ok, want)
	}
}
