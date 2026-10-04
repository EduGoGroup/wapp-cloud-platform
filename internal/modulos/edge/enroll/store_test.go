//go:build pendiente

package enroll_test

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/enroll/enrollhelpertest"
)

// Las dos implementaciones del puerto. Sus promesas comunes las fija
// enrollhelpertest.ContratoCodeStore.
var (
	_ enroll.CodeStore = (*enroll.PostgresCodeStore)(nil)
	_ enroll.CodeStore = (*enrollhelpertest.MemoriaCodeStore)(nil)
)

// TestCodeSentinels_TextsAndIdentity: los textos son observables y se portan literales; los
// cuatro centinelas son distintos entre sí (un errors.Is de uno no casa con otro).
func TestCodeSentinels_TextsAndIdentity(t *testing.T) {
	sentinels := []struct {
		err  error
		want string
	}{
		{enroll.ErrCodeNotFound, "enroll: código de activación desconocido"},
		{enroll.ErrCodeExpired, "enroll: código de activación expirado"},
		{enroll.ErrCodeUsed, "enroll: código de activación ya utilizado"},
		{enroll.ErrCodeInvalid, "enroll: código de activación inválido"},
	}
	for i, s := range sentinels {
		if s.err.Error() != s.want {
			t.Errorf("centinela %d: texto %q, quería %q", i, s.err, s.want)
		}
		for j, other := range sentinels {
			if i != j && errors.Is(s.err, other.err) {
				t.Errorf("el centinela %q casa con %q: tienen que ser distintos", s.err, other.err)
			}
		}
	}
}
