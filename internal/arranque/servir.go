// Copia de internal/bootstrap/arranque/servir.go @ 80807ba (F0 · 05 §6): cableaba paquetes VIEJOS.
//
// 🔀 SE APARTA DEL VIEJO A PROPÓSITO en la parada del CloudLink (D-F3-13, hallazgo 81 de F3):
// el viejo espera siempre el plazo entero con un Edge conectado; este para en cuanto no queda
// nada en vuelo. El porqué y la espera están en servir_cloudlink.go; aquí solo cambia por
// dónde llega el contador (grpcServer.inFlight) y que gracefulStopGRPC lo recibe.
// 🔀 F8 · conmutar(conversacion): ya no cablea ninguno; desde F8 el arranque nuevo es todo módulos nuevos.
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
	// inFlight dice cuántas peticiones esperan respuesta por los streams de ESTE servidor
	// (D-F3-13). Solo lo trae el CloudLink; nil ⇒ la parada de siempre (GracefulStop con el
	// plazo entero), que es la del servidor de enrolamiento.
	inFlight func() int
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
		cloudLinkServer(c),
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
		shutdownAll(admin.srv, public.srv, enroll.gs, connect.gs, connect.inFlight, log)
		return serveErr
	}
	shutdownAll(admin.srv, public.srv, enroll.gs, connect.gs, connect.inFlight, log)
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

// shutdownAll cierra los cuatro servidores en el orden de siempre. connectInFlight es el
// contador del gateway (D-F3-13): solo lo recibe la parada del CloudLink. El servidor de
// enrolamiento NO lo recibe nunca: sus rpc son unarias, terminan solas, y su GracefulStop no
// tiene espera en vacío que recortar.
func shutdownAll(httpSrv, publicSrv *http.Server, enrollGS, connectGS grpcStopper, connectInFlight func() int, log sharedlogger.Logger) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("error en shutdown HTTP admin", "error", err)
	}
	if err := publicSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("error en shutdown HTTP público", "error", err)
	}
	gracefulStopGRPC(enrollGS, "enroll", nil, log)
	gracefulStopGRPC(connectGS, "cloudlink", connectInFlight, log)
}

// gracefulStopGRPC lanza GracefulStop —deja de aceptar y manda GOAWAY— y espera a que vuelva,
// con shutdownTimeout como tope antes de forzar Stop(). Con inFlight nil es la copia literal
// del viejo; con inFlight, la espera es la de stopWhenNothingInFlight (D-F3-13).
//
// La goroutine se lanza AQUÍ en los dos casos, y no en la función de la espera, para que la
// huella estática (TestHuellaEstatica) siga viendo la misma sentencia `go` que en el viejo:
// es, de hecho, la misma goroutine, y solo vive mientras dura el cierre.
func gracefulStopGRPC(gs grpcStopper, name string, inFlight func() int, log sharedlogger.Logger) {
	done := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(done)
	}()
	if inFlight != nil {
		stopWhenNothingInFlight(gs, done, name, inFlight, log)
		return
	}
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
		log.Warn("shutdown gRPC: GracefulStop excedió el timeout; forzando Stop()",
			"servidor", name, "timeout", shutdownTimeout)
		gs.Stop()
		<-done
	}
}
