package errhelpertest_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/errhelpertest"
)

// typedError es un error de tipo propio, sin Unwrap: la forma de los dobles de transporte.
type typedError struct{ reason string }

func (e *typedError) Error() string { return "falló: " + e.reason }

// wrapper es un tipo que envuelve con Unwrap() error, como edgegrpc.InferError.
type wrapper struct{ cause error }

func (w *wrapper) Error() string { return "envuelto: " + w.cause.Error() }
func (w *wrapper) Unwrap() error { return w.cause }

func TestIsBare(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("centinela")
	twin := errors.New("centinela")
	typed := &typedError{"x"}

	for name, c := range map[string]struct {
		got, want error
		bare      bool
	}{
		"el propio centinela":                {sentinel, sentinel, true},
		"un error de tipo propio, el mismo":  {typed, typed, true},
		"los dos nil":                        {nil, nil, true},
		"nil donde se esperaba el centinela": {nil, sentinel, false},
		"el centinela donde se esperaba nil": {sentinel, nil, false},
		"un gemelo con el mismo texto":       {twin, sentinel, false},
		"otro valor del mismo tipo":          {&typedError{"x"}, typed, false},
		"envuelto con %w":                    {fmt.Errorf("contexto: %w", sentinel), sentinel, false},
		"envuelto en un tipo con Unwrap":     {&wrapper{sentinel}, sentinel, false},
		"dentro de un errors.Join":           {errors.Join(sentinel), sentinel, false},
		"dentro de un %w múltiple":           {fmt.Errorf("%w y %w", sentinel, twin), sentinel, false},
		"envuelto dos veces":                 {fmt.Errorf("a: %w", fmt.Errorf("b: %w", sentinel)), sentinel, false},
		"el tipo propio envuelto con %w":     {fmt.Errorf("contexto: %w", typed), typed, false},
		"otro error que solo copia el texto": {errors.New("contexto: " + sentinel.Error()), sentinel, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := errhelpertest.IsBare(c.got, c.want); got != c.bare {
				t.Errorf("IsBare(%v, %v) = %v; se esperaba %v", c.got, c.want, got, c.bare)
			}
		})
	}
}
