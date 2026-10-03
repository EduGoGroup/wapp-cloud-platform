//go:build integracion

package procesos

import "github.com/EduGoGroup/wapp-cloud-platform/internal/candados"

// Un fichero-candado con etiqueta no correría en ci-local.
var _ = candados.ProcessImports
