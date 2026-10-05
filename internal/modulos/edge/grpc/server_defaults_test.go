package grpc

// Lo que New y las With* dejan DENTRO del Server (nace con el verde: mira campos no
// exportados). La conducta que sale de esos campos se afirma en los tests de cada fichero
// que los usa.

import (
	"bytes"
	"context"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet/fleethelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/inferstats"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease/leasehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// Los NÚMEROS pactados, no solo su existencia: 8 s de Ack, 64 trabajos por sesión y 5 s por
// trabajo. Quien los cambie tiene que cambiar el otro extremo (el WriteTimeout HTTP, el techo
// de entrantes del runtime de flujos) en el mismo cambio.
func TestDefaultsAreTheAgreedNumbers(t *testing.T) {
	t.Parallel()
	if defaultAckTimeout != 8*time.Second {
		t.Errorf("defaultAckTimeout = %v, se esperaban 8s", defaultAckTimeout)
	}
	if defaultWorkQueue != 64 {
		t.Errorf("defaultWorkQueue = %d, se esperaban 64", defaultWorkQueue)
	}
	if defaultWorkBudget != 5*time.Second {
		t.Errorf("defaultWorkBudget = %v, se esperaban 5s", defaultWorkBudget)
	}
}

// D-F2-10: el cero NUNCA es «sin reloj» ni «cola infinita». Sin opción, o con un valor no
// positivo, New cae al valor por defecto; un valor positivo se respeta.
func TestNewMaterializesClocksAndQueueCap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		opts       []Option
		ackTimeout time.Duration
		workQueue  int
		workBudget time.Duration
	}{
		{name: "no options", ackTimeout: 8 * time.Second, workQueue: 64, workBudget: 5 * time.Second},
		{
			name:       "zero values fall back",
			opts:       []Option{WithAckTimeout(0), WithWorkQueue(0), WithWorkTimeout(0)},
			ackTimeout: 8 * time.Second, workQueue: 64, workBudget: 5 * time.Second,
		},
		{
			name:       "negative values fall back",
			opts:       []Option{WithAckTimeout(-time.Second), WithWorkQueue(-3), WithWorkTimeout(-time.Minute)},
			ackTimeout: 8 * time.Second, workQueue: 64, workBudget: 5 * time.Second,
		},
		{
			name:       "positive values are kept",
			opts:       []Option{WithAckTimeout(3 * time.Second), WithWorkQueue(7), WithWorkTimeout(2 * time.Second)},
			ackTimeout: 3 * time.Second, workQueue: 7, workBudget: 2 * time.Second,
		},
		{
			name:       "smallest positive values are kept",
			opts:       []Option{WithAckTimeout(1), WithWorkQueue(1), WithWorkTimeout(1)},
			ackTimeout: 1, workQueue: 1, workBudget: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := New(session.NewRegistry(), quietLog(), tc.opts...)
			if srv.ackTimeout != tc.ackTimeout {
				t.Errorf("ackTimeout = %v, se esperaba %v", srv.ackTimeout, tc.ackTimeout)
			}
			if srv.workQueue != tc.workQueue {
				t.Errorf("workQueue = %d, se esperaba %d", srv.workQueue, tc.workQueue)
			}
			if srv.workBudget != tc.workBudget {
				t.Errorf("workBudget = %v, se esperaba %v", srv.workBudget, tc.workBudget)
			}
		})
	}
}

// recordingSink es un ReceiptSink que apunta lo que recibe, y con qué ctx.
type recordingSink struct {
	got  []*cloudlinkv1.MessageReceipt
	ctxs []context.Context
	err  error
}

func (r *recordingSink) Record(ctx context.Context, receipt *cloudlinkv1.MessageReceipt) error {
	r.got = append(r.got, receipt)
	r.ctxs = append(r.ctxs, ctx)
	return r.err
}

// El sink de acuses nunca es nil: sin opción (o con nil) es un LogReceiptSink sobre el logger
// de New; con opción, es el inyectado.
func TestNewDefaultsTheReceiptSinkToLogOnly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{name: "no option"},
		{name: "nil sink", opts: []Option{WithReceiptSink(nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log, buf := capturedLog()
			srv := New(session.NewRegistry(), log, tc.opts...)
			sink, ok := srv.receiptSink.(*LogReceiptSink)
			if !ok {
				t.Fatalf("receiptSink = %T, se esperaba *LogReceiptSink", srv.receiptSink)
			}
			if err := sink.Record(context.Background(), &cloudlinkv1.MessageReceipt{CommandId: "cmd-default"}); err != nil {
				t.Fatalf("Record = %v", err)
			}
			if !buf.contains("command_id=cmd-default") {
				t.Errorf("el sink por defecto no escribe en el logger de New: %q", buf.String())
			}
		})
	}

	custom := &recordingSink{}
	srv := New(session.NewRegistry(), quietLog(), WithReceiptSink(custom))
	if srv.receiptSink != ReceiptSink(custom) {
		t.Fatalf("receiptSink = %T, se esperaba el inyectado", srv.receiptSink)
	}
}

// nopBundleReceiver es un diagnostics.BundleReceiver que no hace nada.
type nopBundleReceiver struct{}

func (nopBundleReceiver) SaveBundle(context.Context, string, string, string, diagnostics.Bundle) (bool, error) {
	return false, nil
}

// Sin opciones, cada pieza opcional queda en su cero («sin esa pieza»), el Registry es el
// que se dio y los mapas nacen vacíos pero no nil.
func TestNewWithoutOptionsHasNoOptionalDependency(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	bare := New(reg, quietLog())
	if bare.registry != reg {
		t.Error("el Server no conserva el Registry que se le dio")
	}
	if bare.leaseMgr != nil || bare.fleet != nil || bare.cloudEncPriv != nil || bare.inferStats != nil || bare.diag != nil {
		t.Error("un Server sin opciones tiene alguna dependencia opcional puesta")
	}
	if bare.acks == nil || len(bare.acks) != 0 {
		t.Errorf("acks = %v, se esperaba un mapa vacío no nil", bare.acks)
	}
	if bare.edgeSessions == nil || len(bare.edgeSessions) != 0 {
		t.Errorf("edgeSessions = %v, se esperaba un mapa vacío no nil", bare.edgeSessions)
	}
	if bare.edgeReadiness == nil || len(bare.edgeReadiness) != 0 {
		t.Errorf("edgeReadiness = %v, se esperaba un mapa vacío no nil", bare.edgeReadiness)
	}
}

// Cada With* deja en el Server exactamente lo que se le dio.
func TestOptionsInjectTheirDependency(t *testing.T) {
	t.Parallel()
	mgr, err := lease.NewManager(newSigningKey(t), leasehelpertest.NewMemoria())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	fleetRepo := fleethelpertest.NewMemoria()
	priv := bytes.Repeat([]byte{7}, 32)
	stats := inferstats.New()
	diag := nopBundleReceiver{}

	full := New(session.NewRegistry(), quietLog(),
		WithLease(mgr), WithFleet(fleetRepo), WithCloudEncPrivKey(priv),
		WithInferenceStats(stats), WithDiagnosticsSink(diag))
	if full.leaseMgr != mgr {
		t.Error("WithLease no dejó el Manager inyectado")
	}
	if full.fleet != fleetRepo {
		t.Error("WithFleet no dejó el repositorio inyectado")
	}
	if !bytes.Equal(full.cloudEncPriv, priv) {
		t.Error("WithCloudEncPrivKey no dejó la clave de tránsito inyectada")
	}
	if full.inferStats != stats {
		t.Error("WithInferenceStats no dejó el almacén inyectado")
	}
	if full.diag != diagnostics.BundleReceiver(diag) {
		t.Error("WithDiagnosticsSink no dejó el receptor inyectado")
	}
}
