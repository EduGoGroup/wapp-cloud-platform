package arranque

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"google.golang.org/grpc"
)

type httpServer struct {
	srv  *http.Server
	name string
}

type grpcServer struct {
	gs   *grpc.Server
	lis  net.Listener
	addr string
	name string
}

// servir pone a escuchar los cuatro listeners que armó la fase de transporte y se
// queda bloqueado hasta que llega una señal de parada o falla un servidor. Es lo
// último que hace el arranque y lo único que no construye nada.
//
// shutdownAll parte de context.Background() a propósito: corre cuando ctx ya está
// cancelado, y derivar de él abortaría el cierre gracioso al instante.
//
//nolint:contextcheck // el porqué está en el párrafo de arriba
func servir(ctx context.Context, c *contenedor) error {
	return serveAndWait(ctx.Done(), c.log,
		httpServer{srv: c.httpSrv, name: "admin/health"},
		httpServer{srv: c.publicSrv, name: "API pública"},
		grpcServer{gs: c.enrollGS, lis: c.enrollLis, addr: c.cfg.GRPCEnrollAddr, name: "Enrollment (TLS de servidor)"},
		grpcServer{gs: c.connectGS, lis: c.connectLis, addr: c.cfg.GRPCConnectAddr, name: "CloudLink (mTLS)"},
	)
}

func serveAndWait(done <-chan struct{}, log sharedlogger.Logger, admin, public httpServer, enroll, connect grpcServer) error {
	errCh := make(chan error, 4)
	go serveGRPC(errCh, log, enroll)
	go serveGRPC(errCh, log, connect)
	go serveHTTP(errCh, log, admin)
	go serveHTTP(errCh, log, public)

	select {
	case <-done:
		log.Info("señal de parada recibida, cerrando")
	case serveErr := <-errCh:
		log.Error("fallo de un servidor", "error", serveErr)
		shutdownAll(admin.srv, public.srv, enroll.gs, connect.gs, log)
		return serveErr
	}
	shutdownAll(admin.srv, public.srv, enroll.gs, connect.gs, log)
	log.Info("servidor detenido limpiamente")
	return nil
}

func serveGRPC(errCh chan<- error, log sharedlogger.Logger, s grpcServer) {
	log.Info("servidor gRPC iniciado", "name", s.name, "addr", s.addr)
	if err := s.gs.Serve(s.lis); err != nil {
		errCh <- fmt.Errorf("%s gRPC: %w", s.name, err)
	}
}

func serveHTTP(errCh chan<- error, log sharedlogger.Logger, s httpServer) {
	log.Info("servidor HTTP iniciado", "name", s.name, "addr", s.srv.Addr)
	if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("http %s: %w", s.name, err)
	}
}

func shutdownAll(httpSrv, publicSrv *http.Server, enrollGS, connectGS *grpc.Server, log sharedlogger.Logger) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("error en shutdown HTTP admin", "error", err)
	}
	if err := publicSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("error en shutdown HTTP público", "error", err)
	}
	gracefulStopGRPC(enrollGS, "enroll", log)
	gracefulStopGRPC(connectGS, "cloudlink", log)
}

func gracefulStopGRPC(gs *grpc.Server, name string, log sharedlogger.Logger) {
	done := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
		log.Warn("shutdown gRPC: GracefulStop excedió el timeout; forzando Stop()",
			"servidor", name, "timeout", shutdownTimeout)
		gs.Stop()
		<-done
	}
}
