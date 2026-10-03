//go:build integracion

package procesos

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// Un proceso (no es *_contrato_test.go) que importa el paquete que una suite ya usa: el comando
// de la spec, que miraba el paquete entero, no lo veía (hallazgo 36 de F1).
func TestP2(t *testing.T) { _ = contact.NewMemoryResolver(nil) }
