//go:build pendiente

package cosa_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/m/cosa"
)

func TestTodo(t *testing.T) {
	c := cosa.Cosa{}
	if cosa.Hacer() != cosa.Limite || c.Medir() != 0 {
		t.Fatal("contrato")
	}
}
