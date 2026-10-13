//go:build integracion

package procesos

import (
	. "github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact/contacthelpertest"
)

// Con import de punto no se ve qué usa del puerto.
var _, _ = Normalize, contacthelpertest.Contrato
