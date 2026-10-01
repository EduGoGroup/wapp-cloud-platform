package procesos

import "os"

// El servidor heredaría el entorno del shell del desarrollador (un .env exportado): prohibido.
var entorno = os.Environ()
