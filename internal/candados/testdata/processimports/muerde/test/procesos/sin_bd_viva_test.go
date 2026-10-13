package procesos

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/candados"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

// Un fichero-candado importa internal/candados y nada más de internal/.
var _, _ = candados.SinBDViva, config.Load
