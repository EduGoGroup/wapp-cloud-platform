package grpc

// El contrato de diagnostics.go (R-G19): RequestDiagnostics empuja el frame y propaga el
// error del empuje; la recepción correlaciona el bundle por la identidad mTLS del stream, un
// huérfano no rompe y sin sink, identidad, sesión o bundle no hace nada.

import (
	"context"
	"errors"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics/diagnosticshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// savedBundle es una llamada a SaveBundle tal como la vio el receptor.
type savedBundle struct {
	tenantID, sessionID, commandID string
	bundle                         diagnostics.Bundle
}

// spyReceiver es un diagnostics.BundleReceiver que apunta lo que recibe y contesta lo que
// se le diga.
type spyReceiver struct {
	found bool
	err   error
	calls []savedBundle
}

func (r *spyReceiver) SaveBundle(_ context.Context, tenantID, sessionID, commandID string, b diagnostics.Bundle) (bool, error) {
	r.calls = append(r.calls, savedBundle{tenantID, sessionID, commandID, b})
	return r.found, r.err
}

func sampleBundle(commandID string) *cloudlinkv1.DiagnosticsBundle {
	return &cloudlinkv1.DiagnosticsBundle{
		CommandId:      commandID,
		LogTail:        "log tail",
		GoroutineDump:  "goroutine 1 [running]",
		SubsystemsJson: `{"wa":"ok"}`,
	}
}

// RequestDiagnostics empuja UN frame a la sesión: el DiagnosticsRequest con el command_id y
// el session_id repetidos en el sobre y dentro, y el scope tal cual.
func TestRequestDiagnosticsPushesTheRequestFrame(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	edge, other := &captureSender{}, &captureSender{}
	t.Cleanup(reg.Register("s-1", edge))
	t.Cleanup(reg.Register("s-2", other))
	srv := New(reg, quietLog())

	if err := srv.RequestDiagnostics(context.Background(), "s-1", "cmd-7", "full"); err != nil {
		t.Fatalf("RequestDiagnostics: %v", err)
	}

	frame := edge.only(t)
	if frame.GetCommandId() != "cmd-7" || frame.GetSessionId() != "s-1" {
		t.Errorf("sobre del frame = (%q, %q), se esperaba (cmd-7, s-1)", frame.GetCommandId(), frame.GetSessionId())
	}
	req := frame.GetDiagnosticsRequest()
	if req == nil {
		t.Fatalf("el frame no es un DiagnosticsRequest: %v", frame.GetPayload())
	}
	if req.GetCommandId() != "cmd-7" || req.GetSessionId() != "s-1" || req.GetScope() != "full" {
		t.Errorf("DiagnosticsRequest = (%q, %q, %q), se esperaba (cmd-7, s-1, full)",
			req.GetCommandId(), req.GetSessionId(), req.GetScope())
	}
	if n := len(other.frames); n != 0 {
		t.Errorf("otra sesión recibió %d frames", n)
	}
}

// Sin stream vivo para la sesión, propaga el ErrSessionOffline del Registry.
func TestRequestDiagnosticsToAnOfflineSessionFails(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	err := srv.RequestDiagnostics(context.Background(), "s-gone", "cmd-1", "full")
	if !errors.Is(err, session.ErrSessionOffline) {
		t.Fatalf("error = %v, se esperaba ErrSessionOffline", err)
	}
}

// El ctx del llamante acota el empuje: con el Edge atascado, un llamante que se va corta la
// espera (sin esperar al plazo del Registry) y el error dice las dos cosas.
func TestRequestDiagnosticsStopsWaitingWhenTheCallerLeaves(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry(session.WithSendTimeout(time.Hour))
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	entered := make(chan struct{}, 1)
	t.Cleanup(reg.Register("s-1", funcSender(func(*cloudlinkv1.CloudToEdge) error {
		entered <- struct{}{}
		<-stuck
		return nil
	})))
	srv := New(reg, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- srv.RequestDiagnostics(ctx, "s-1", "cmd-1", "full") }()
	await(t, entered, "que el empuje llegue al Edge atascado")
	cancel()

	err := await(t, res, "que RequestDiagnostics vuelva al irse el llamante")
	if !errors.Is(err, session.ErrPushAbandoned) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, se esperaba ErrPushAbandoned envolviendo context.Canceled", err)
	}
}

// El bundle se correlaciona con su solicitud pendiente por command_id, acotado por el tenant
// y la sesión de la identidad del stream, y se guarda entero.
func TestStoreDiagnosticsBundleCorrelatesWithItsPendingRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := diagnosticshelpertest.NewMemoria()
	if err := store.CreateRequest(ctx, "tenant-1", "s-1", "cmd-1", "ops@x", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	log, buf := capturedLog()
	srv := New(session.NewRegistry(), log, WithDiagnosticsSink(store))

	cc := connCtx{sessionID: "s-1", tenantID: "tenant-1", edgeID: "edge-1", hasIdentity: true}
	srv.storeDiagnosticsBundle(ctx, cc, sampleBundle("cmd-1"))

	rec, err := store.GetBundle(ctx, "tenant-1", "cmd-1")
	if err != nil {
		t.Fatalf("GetBundle tras recibir el bundle: %v", err)
	}
	want := diagnostics.Bundle{LogTail: "log tail", GoroutineDump: "goroutine 1 [running]", SubsystemsJSON: `{"wa":"ok"}`}
	if rec.Bundle != want {
		t.Errorf("bundle guardado = %+v, se esperaba %+v", rec.Bundle, want)
	}
	if buf.String() != "" {
		t.Errorf("un bundle correlacionado no deja rastro en el log: %q", buf.String())
	}
}

// Lo que se le pasa al receptor sale de la identidad del stream (tenant y sesión del connCtx)
// y del propio bundle (command_id y las tres partes), no de otra parte.
func TestStoreDiagnosticsBundleScopesByTheStreamIdentity(t *testing.T) {
	t.Parallel()
	spy := &spyReceiver{found: true}
	srv := New(session.NewRegistry(), quietLog(), WithDiagnosticsSink(spy))

	cc := connCtx{sessionID: "s-9", tenantID: "tenant-9", edgeID: "edge-9", hasIdentity: true}
	srv.storeDiagnosticsBundle(context.Background(), cc, sampleBundle("cmd-9"))

	if len(spy.calls) != 1 {
		t.Fatalf("SaveBundle se llamó %d veces, se esperaba 1", len(spy.calls))
	}
	want := savedBundle{"tenant-9", "s-9", "cmd-9",
		diagnostics.Bundle{LogTail: "log tail", GoroutineDump: "goroutine 1 [running]", SubsystemsJSON: `{"wa":"ok"}`}}
	if spy.calls[0] != want {
		t.Errorf("SaveBundle recibió %+v, se esperaba %+v", spy.calls[0], want)
	}
}

// Un bundle huérfano (sin solicitud pendiente que case: nadie lo pidió, o lo pidió otra
// sesión u otro tenant) se ignora con un aviso y no rompe nada.
func TestStoreDiagnosticsBundleIgnoresAnOrphan(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name string
		cc   connCtx
	}{
		{"nobody asked", connCtx{sessionID: "s-1", tenantID: "tenant-2", edgeID: "edge-1", hasIdentity: true}},
		{"asked by another session", connCtx{sessionID: "s-other", tenantID: "tenant-1", edgeID: "edge-1", hasIdentity: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := diagnosticshelpertest.NewMemoria()
			if err := store.CreateRequest(ctx, "tenant-1", "s-1", "cmd-1", "ops@x", time.Now().Add(time.Hour)); err != nil {
				t.Fatalf("CreateRequest: %v", err)
			}
			log, buf := capturedLog()
			srv := New(session.NewRegistry(), log, WithDiagnosticsSink(store))

			srv.storeDiagnosticsBundle(ctx, tc.cc, sampleBundle("cmd-1"))

			if !buf.contains("diagnóstico: bundle sin solicitud pendiente; ignorado") ||
				!buf.contains("session_id="+tc.cc.sessionID) || !buf.contains("command_id=cmd-1") {
				t.Errorf("falta el aviso del huérfano con su sesión y su command_id: %q", buf.String())
			}
			if _, err := store.GetBundle(ctx, "tenant-1", "cmd-1"); !errors.Is(err, diagnostics.ErrPending) {
				t.Errorf("la solicitud ajena dejó de estar pendiente: %v", err)
			}
		})
	}
}

// Si el receptor falla, se registra el error (con Edge, sesión y command_id) y no se da
// además el aviso de huérfano.
func TestStoreDiagnosticsBundleLogsAReceiverFailure(t *testing.T) {
	t.Parallel()
	log, buf := capturedLog()
	spy := &spyReceiver{err: errors.New("base caída")}
	srv := New(session.NewRegistry(), log, WithDiagnosticsSink(spy))

	cc := connCtx{sessionID: "s-1", tenantID: "tenant-1", edgeID: "edge-1", hasIdentity: true}
	srv.storeDiagnosticsBundle(context.Background(), cc, sampleBundle("cmd-1"))

	for _, want := range []string{"level=ERROR", "diagnóstico: persistir bundle", "base caída",
		"edge_id=edge-1", "session_id=s-1", "command_id=cmd-1"} {
		if !buf.contains(want) {
			t.Errorf("al log del fallo le falta %q: %q", want, buf.String())
		}
	}
	if buf.contains("sin solicitud pendiente") {
		t.Errorf("un fallo del receptor se anunció además como huérfano: %q", buf.String())
	}
}

// Sin identidad mTLS, sin session_id o sin bundle no se llama al receptor ni se registra nada;
// sin receptor inyectado tampoco pasa nada.
func TestStoreDiagnosticsBundleIsANoOpWithoutItsPreconditions(t *testing.T) {
	t.Parallel()
	full := connCtx{sessionID: "s-1", tenantID: "tenant-1", edgeID: "edge-1", hasIdentity: true}
	noIdentity, noSession := full, full
	noIdentity.hasIdentity = false
	noSession.sessionID = ""
	cases := []struct {
		name   string
		cc     connCtx
		bundle *cloudlinkv1.DiagnosticsBundle
	}{
		{"without identity", noIdentity, sampleBundle("cmd-1")},
		{"without session id", noSession, sampleBundle("cmd-1")},
		{"without bundle", full, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log, buf := capturedLog()
			spy := &spyReceiver{}
			srv := New(session.NewRegistry(), log, WithDiagnosticsSink(spy))
			srv.storeDiagnosticsBundle(context.Background(), tc.cc, tc.bundle)
			if len(spy.calls) != 0 || buf.String() != "" {
				t.Errorf("se esperaba un no-op: %d llamadas, log %q", len(spy.calls), buf.String())
			}
		})
	}

	t.Run("without receiver", func(t *testing.T) {
		t.Parallel()
		log, buf := capturedLog()
		srv := New(session.NewRegistry(), log)
		srv.storeDiagnosticsBundle(context.Background(), full, sampleBundle("cmd-1"))
		if buf.String() != "" {
			t.Errorf("sin receptor se esperaba silencio: %q", buf.String())
		}
	})
}
