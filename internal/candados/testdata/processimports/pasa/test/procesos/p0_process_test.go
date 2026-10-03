//go:build integracion

package procesos

import (
	"net/http"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platformx/internal/other"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

type normalizer struct{}

func (normalizer) Normalize() {}

// La ruta del módulo dentro de un literal no es un import; otro módulo con el mismo prefijo
// tampoco es de este; y un valor local llamado contact, en un fichero que no importa el puerto,
// no es el paquete.
func TestP0(t *testing.T) {
	const pkg = "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	contact := normalizer{}
	contact.Normalize()
	_, _, _, _ = pkg, http.MethodGet, other.X, sharedlogger.New
}
