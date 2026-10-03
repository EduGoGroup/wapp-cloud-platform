//go:build integracion

package procesos

import (
	port "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease/leasehelpertest"
)

// Un puerto de internal/modulos, importado con alias.
var _, _ = port.NewPostgresStore, leasehelpertest.Contrato
