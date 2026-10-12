// Command server es el binario único de la Plataforma Cloud (monolito modular).
//
// Arranca CUATRO listeners, dos gRPC y dos HTTP:
//   - Enrollment (:8102, gRPC, TLS de servidor SOLAMENTE): el Edge enrola aquí sin
//     cert de cliente (CSR -> código -> cert firmado por la CA).
//   - CloudLink (:8101, gRPC, mTLS estricto): el Edge conecta aquí con el cert
//     emitido; el servidor exige y verifica el cert de cliente contra la MISMA CA.
//   - HTTP admin/health (:8100): health (/healthz, incluye el check de BD), /metrics
//     y el admin interno (p. ej. /admin/leases/revoke, el kill-switch).
//   - HTTP API pública (:8103): la cara /api/v1.
//
// Todo el arranque vive en internal/arranque (Ejecutar): carga la configuración,
// construye el logger, abre PostgreSQL, corre las migraciones, construye las
// dependencias por fases y hace graceful shutdown de los cuatro servidores.
package main

import (
	"context"
	stdlog "log"
	"os/signal"
	"syscall"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/arranque"
)

func main() {
	if err := run(); err != nil {
		stdlog.Fatalf("fallo fatal del arranque: %v", err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return arranque.Ejecutar(ctx)
}
