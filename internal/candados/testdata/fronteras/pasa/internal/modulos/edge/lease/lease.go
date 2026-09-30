package lease

import (
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/sesion"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/tunel"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

var _ = []any{fmt.Sprint, pgx.Connect, sesion.X, tunel.X, contact.X, pendiente.Implementar, config.X}
