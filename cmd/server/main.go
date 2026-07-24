// Command server es el binario único de la Plataforma Cloud (monolito modular).
//
// Orquesta el arranque del Gateway CloudLink con DOS listeners gRPC y un HTTP:
//   - Enrollment (TLS de servidor SOLAMENTE): el Edge enrola aquí sin cert de
//     cliente (CSR -> código -> cert firmado por la CA).
//   - CloudLink (mTLS estricto): el Edge conecta aquí con el cert emitido; el
//     servidor exige y verifica el cert de cliente contra la MISMA CA.
//   - HTTP: health (/healthz, incluye el check de BD) y admin interno de
//     revocación de leases (/admin/leases/revoke, kill-switch).
//
// Carga la configuración, construye el logger, abre PostgreSQL, corre las
// migraciones al arrancar, loguea la clave pública del lease (para configurar el
// Edge) y hace graceful shutdown de los tres servidores.
package main

import (
	"context"
	stdlog "log"
	"os/signal"
	"syscall"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/bootstrap"
)

func main() {
	if err := run(); err != nil {
		stdlog.Fatalf("fallo fatal del arranque: %v", err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return bootstrap.Run(ctx)
}
