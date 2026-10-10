package trigger_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// Las cinco operaciones del puerto, con su firma: un cambio de firma rompe aquí antes que en los
// consumidores. La conducta la fija triggerhelpertest.Contrato, que corren las dos implementaciones.
var (
	_ func(trigger.Store, context.Context, trigger.Rule) (trigger.Rule, error)                   = trigger.Store.Insert
	_ func(trigger.Store, context.Context, string) ([]trigger.Rule, error)                       = trigger.Store.List
	_ func(trigger.Store, context.Context, string, string, trigger.Kind) ([]trigger.Rule, error) = trigger.Store.ListByKind
	_ func(trigger.Store, context.Context, string, string) (trigger.Rule, error)                 = trigger.Store.Get
	_ func(trigger.Store, context.Context, string, string) error                                 = trigger.Store.Delete
)

// TestErrTriggerNotFound_LiteralText: el texto es observable y no cambia.
func TestErrTriggerNotFound_LiteralText(t *testing.T) {
	if got, want := trigger.ErrTriggerNotFound.Error(), "regla de disparo no encontrada"; got != want {
		t.Errorf("ErrTriggerNotFound dice %q, quería %q", got, want)
	}
}

// TestErrTriggerNotFound_SurvivesWrapping: se inspecciona con errors.Is, también envuelto, y no
// se confunde con otro error del mismo texto.
func TestErrTriggerNotFound_SurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("admin: borrar disparo: %w", trigger.ErrTriggerNotFound)
	if !errors.Is(wrapped, trigger.ErrTriggerNotFound) {
		t.Error("errors.Is no reconoce ErrTriggerNotFound envuelto con %w")
	}
	if errors.Is(errors.New("regla de disparo no encontrada"), trigger.ErrTriggerNotFound) {
		t.Error("un error distinto con el mismo texto pasa por ErrTriggerNotFound")
	}
}
