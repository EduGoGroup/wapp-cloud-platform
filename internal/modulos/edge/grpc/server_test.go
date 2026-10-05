//go:build pendiente

package grpc

// El contrato de server.go visto desde fuera: construcción, orden de las opciones, hooks y
// registro del servicio. Lo que New materializa por dentro (plazos, tope, sink por defecto) y
// lo que cada With* deja en el Server se afirma en server_defaults_test.go, que nace con el
// verde porque mira campos no exportados; la conducta de WithAckTimeout, WithLease, WithFleet
// y WithWorkTimeout se afirma además donde se nota: en send_test.go y send_revoke_test.go.

import (
	"io"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"
	googlegrpc "google.golang.org/grpc"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/inferstats"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// quietLog da un logger mudo para los tests que no miran lo que se escribe.
func quietLog() logger.Logger {
	return logger.New(logger.WithWriter(io.Discard))
}

// recordingRegistrar es un googlegrpc.ServiceRegistrar que apunta lo que se le registra.
type recordingRegistrar struct {
	descs []*googlegrpc.ServiceDesc
	impls []any
}

func (r *recordingRegistrar) RegisterService(desc *googlegrpc.ServiceDesc, impl any) {
	r.descs = append(r.descs, desc)
	r.impls = append(r.impls, impl)
}

// New devuelve un Server utilizable cuyos cuatro hooks nacen nil.
func TestNewStartsWithNilHooks(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	if srv == nil {
		t.Fatal("New devolvió nil")
	}
	if srv.OnIncoming != nil || srv.OnHeartbeat != nil || srv.OnWarmup != nil || srv.OnEdgeReady != nil {
		t.Fatal("algún hook no nace nil: el gateway llamaría a algo que nadie cableó")
	}
}

// Los hooks son campos asignables con la firma del contrato (se cablean tras New).
func TestHooksAreAssignableAfterNew(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	srv.OnIncoming = func(string, *cloudlinkv1.IncomingMessage) {}
	srv.OnHeartbeat = func(string, *cloudlinkv1.Heartbeat) {}
	srv.OnWarmup = func(_, _, _, _ string) {}
	srv.OnEdgeReady = func(_, _ string) {}
	if srv.OnIncoming == nil || srv.OnHeartbeat == nil || srv.OnWarmup == nil || srv.OnEdgeReady == nil {
		t.Fatal("un hook asignado sigue nil")
	}
}

// New aplica las opciones en el orden dado, una vez cada una y sobre el Server que devuelve.
func TestNewAppliesOptionsInOrderOnTheReturnedServer(t *testing.T) {
	t.Parallel()
	var order []string
	var seen []*Server
	mark := func(name string) Option {
		return func(s *Server) {
			order = append(order, name)
			seen = append(seen, s)
		}
	}

	srv := New(session.NewRegistry(), quietLog(), mark("first"), mark("second"), mark("third"))

	if len(order) != 3 || order[0] != "first" || order[1] != "second" || order[2] != "third" {
		t.Fatalf("orden de aplicación = %v, se esperaba [first second third]", order)
	}
	for i, s := range seen {
		if s != srv {
			t.Errorf("la opción %d se aplicó sobre otro Server que el devuelto", i)
		}
	}
}

// Las nueve opciones del paquete son Option no nulas y New las acepta todas juntas, también
// con sus valores cero («sin esa pieza»).
func TestBuiltInOptionsAreAcceptedByNew(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opt  Option
	}{
		{"WithLease nil", WithLease(nil)},
		{"WithFleet nil", WithFleet(nil)},
		{"WithCloudEncPrivKey", WithCloudEncPrivKey(make([]byte, 32))},
		{"WithReceiptSink nil", WithReceiptSink(nil)},
		{"WithInferenceStats", WithInferenceStats(inferstats.New())},
		{"WithDiagnosticsSink nil", WithDiagnosticsSink(nil)},
		{"WithAckTimeout", WithAckTimeout(time.Second)},
		{"WithWorkQueue", WithWorkQueue(8)},
		{"WithWorkTimeout", WithWorkTimeout(time.Second)},
	}
	opts := make([]Option, 0, len(cases))
	for _, tc := range cases {
		if tc.opt == nil {
			t.Fatalf("%s devolvió una Option nil", tc.name)
		}
		opts = append(opts, tc.opt)
	}
	if srv := New(session.NewRegistry(), quietLog(), opts...); srv == nil {
		t.Fatal("New con las nueve opciones devolvió nil")
	}
}

// Register registra ESTE servidor como el servicio wapp.cloudlink.v1.CloudLink, una vez.
func TestRegisterRegistersTheCloudLinkService(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	reg := &recordingRegistrar{}

	srv.Register(reg)

	if len(reg.descs) != 1 {
		t.Fatalf("Register hizo %d registros, se esperaba 1", len(reg.descs))
	}
	if got := reg.descs[0].ServiceName; got != "wapp.cloudlink.v1.CloudLink" {
		t.Errorf("servicio registrado = %q, se esperaba wapp.cloudlink.v1.CloudLink", got)
	}
	if reg.impls[0] != any(srv) {
		t.Error("la implementación registrada no es el propio Server")
	}
	if _, ok := reg.impls[0].(cloudlinkv1.CloudLinkServer); !ok {
		t.Error("el Server registrado no cumple cloudlinkv1.CloudLinkServer")
	}
}
