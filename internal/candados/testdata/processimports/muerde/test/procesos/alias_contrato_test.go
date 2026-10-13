//go:build integracion

package procesos

import (
	port "github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact/contacthelpertest"
)

// El alias no esconde el símbolo.
var _, _, _ = port.KindPhoneE164, port.NewPostgresResolver, contacthelpertest.Contrato
