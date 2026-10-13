//go:build integracion

package procesos

import (
	"testing"

	oldcontact "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
)

// Un proceso que importa un paquete viejo de dominio.
func TestP1(t *testing.T) { _, _ = oldcontact.Normalize("phone_e164", "573001112233") }
