//go:build integracion

package procesos

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics/flowlifecycle"
)

// El colector de flow_events, el otro paquete de platform admitido: sin helpertest ni puerto, y
// usando de él más que el constructor (Run es lo que se prueba).
var _, _ = flowlifecycle.NewCollector, (*flowlifecycle.Collector).Run
