package legacysuitetest

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/m/legacysuite"
)

// Contrato es una suite bien formada en un paquete con el nombre VIEJO (termina en «test» a
// secas): no exime al puerto y, como su paquete ya no está exento, ella misma necesita test.
func Contrato(t *testing.T, build func() legacysuite.Reader) { _ = build() }
