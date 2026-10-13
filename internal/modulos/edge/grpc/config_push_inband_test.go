package grpc

// El push de config por el PROPIO STREAM del Edge (pushConfigsInBand; ADR-0048, R3.4.c,
// REQ-057.7): un Edge sin teléfonos recibe sus configs por el cable que acaba de hablar, y
// NUNCA por el Registry (T-5) —donde `__wapp_control__` es una clave compartida por todos los
// Edge y puede estar ocupada por el de otra empresa—. Nace con el verde: lo llamará
// onControlChannel cuando exista connect.go.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// controlRig es un Server con proveedor de config y, en el Registry, el canal de control de
// OTRA empresa ocupando la clave compartida: lo que T-5 dice que no debe recibir nada.
type controlRig struct {
	*configRig
	provider *stubProvider
	intruder *liveSession
}

func newControlRig(t *testing.T, provider *stubProvider, regOpts ...session.RegistryOption) *controlRig {
	t.Helper()
	rig := newConfigRig(regOpts, WithConfigProvider(provider))
	intruder := &liveSession{id: "el canal de control de otra empresa"}
	t.Cleanup(rig.reg.Register(cltransport.ControlSessionID, intruder))
	return &controlRig{configRig: rig, provider: provider, intruder: intruder}
}

// controlChannel es el connCtx de un frame de auth: identidad mTLS, el id del canal de
// control y el cable del stream que lo trajo.
func controlChannel(tenantID, edgeID string, sender cloudToEdgeSender) connCtx {
	return connCtx{sessionID: cltransport.ControlSessionID, tenantID: tenantID, edgeID: edgeID, hasIdentity: true, sender: sender}
}

// Las configs del tenant salen por el stream del Edge, todas y en orden, con el session_id del
// frame que las provocó; y el Registry no se toca: quien ocupe la clave de control no recibe nada.
func TestPushConfigsInBandWritesOnTheEdgeOwnStream(t *testing.T) {
	t.Parallel()
	cfgs := []ConfigPayload{jwksCfg, intentsCfg, filtersCfg}
	rig := newControlRig(t, &stubProvider{cfgs: cfgs})
	stream := &liveSession{id: "el stream del Edge"}

	rig.srv.pushConfigsInBand(context.Background(), controlChannel("tenant-1", "edge-1", stream))

	requireConfigs(t, stream.received(), cltransport.ControlSessionID, cfgs)
	requireNothing(t, rig.intruder)
	if got := rig.provider.askedFor(); got != "tenant-1" {
		t.Errorf("al proveedor se le preguntó por %q, se esperaba una vez por tenant-1", got)
	}
	if rig.log.String() != "" {
		t.Errorf("un push in-band limpio no deja rastro: %q", rig.log.String())
	}
}

// 🔴 T-5: sin cable no hay push, y NO se cae al Registry. Tampoco sin identidad mTLS (no se
// conoce el tenant) aunque haya cable. En ningún caso se llega a preguntar al proveedor.
func TestPushConfigsInBandNeverFallsBackToTheRegistry(t *testing.T) {
	t.Parallel()
	stream := &liveSession{id: "el stream del Edge"}
	anonymous := controlChannel("tenant-1", "edge-1", stream)
	anonymous.hasIdentity = false
	cases := map[string]connCtx{
		"without sender":   controlChannel("tenant-1", "edge-1", nil),
		"without identity": anonymous,
	}
	for name, cc := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newControlRig(t, &stubProvider{cfgs: []ConfigPayload{intentsCfg}})
			rig.srv.pushConfigsInBand(context.Background(), cc)
			requireNothing(t, rig.intruder, stream)
			if got := rig.provider.askedFor(); got != "" {
				t.Errorf("se preguntó al proveedor (%q)", got)
			}
		})
	}
}

// Sin proveedor de config no hay nada que empujar, ni por el cable ni por el Registry.
func TestPushConfigsInBandWithoutProviderDoesNothing(t *testing.T) {
	t.Parallel()
	rig := newConfigRig(nil)
	intruder := &liveSession{id: "el canal de control de otra empresa"}
	t.Cleanup(rig.reg.Register(cltransport.ControlSessionID, intruder))
	stream := &liveSession{id: "el stream del Edge"}

	rig.srv.pushConfigsInBand(context.Background(), controlChannel("tenant-1", "edge-1", stream))
	requireNothing(t, stream, intruder)
}

// Si el proveedor falla se registra el error (in-band, con tenant y Edge) y no se escribe
// nada, aunque hubiera devuelto configs.
func TestPushConfigsInBandLogsAProviderFailure(t *testing.T) {
	t.Parallel()
	rig := newControlRig(t, &stubProvider{cfgs: []ConfigPayload{intentsCfg}, err: errors.New("base caída")})
	stream := &liveSession{id: "el stream del Edge"}

	rig.srv.pushConfigsInBand(context.Background(), controlChannel("tenant-1", "edge-1", stream))

	requireNothing(t, stream, rig.intruder)
	for _, want := range []string{"level=ERROR", `msg="config push: resolver configs al conectar (in-band)"`, "base caída", "tenant_id=tenant-1", "edge_id=edge-1"} {
		if !rig.log.contains(want) {
			t.Errorf("al log del fallo le falta %q: %q", want, rig.log.String())
		}
	}
}

// Una escritura que falla se registra en debug y NO corta las siguientes; y sigue sin caer al
// Registry.
func TestPushConfigsInBandKeepsGoingAfterAFailedWrite(t *testing.T) {
	t.Parallel()
	rig := newControlRig(t, &stubProvider{cfgs: []ConfigPayload{jwksCfg, intentsCfg, filtersCfg}})
	var attempts []string
	broken := funcSender(func(msg *cloudlinkv1.CloudToEdge) error {
		attempts = append(attempts, msg.GetConfigUpdate().GetKind())
		return errors.New("stream roto")
	})

	rig.srv.pushConfigsInBand(context.Background(), controlChannel("tenant-1", "edge-1", broken))

	if got := strings.Join(attempts, ","); got != "jwks,intents,filters" {
		t.Fatalf("escrituras intentadas = %q, se esperaban las tres en orden", got)
	}
	if got := strings.Count(rig.log.String(), "config push: inicial in-band"); got != 3 {
		t.Errorf("líneas de escritura fallida = %d, se esperaban 3: %q", got, rig.log.String())
	}
	for _, want := range []string{"level=DEBUG", "edge_id=edge-1", "kind=jwks", "kind=intents", "kind=filters", "stream roto"} {
		if !rig.log.contains(want) {
			t.Errorf("al log de las escrituras fallidas le falta %q: %q", want, rig.log.String())
		}
	}
	requireNothing(t, rig.intruder)
}

// stuckStream es un cable que no devuelve el Send hasta que el test termina, y avisa de cada
// escritura que le entra.
func stuckStream(t *testing.T) (cloudToEdgeSender, <-chan struct{}) {
	t.Helper()
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	entered := make(chan struct{}, 8)
	return funcSender(func(*cloudlinkv1.CloudToEdge) error {
		entered <- struct{}{}
		<-stuck
		return nil
	}), entered
}

// La escritura in-band usa los MISMOS dos relojes que Push. El propio: el plazo del Registry
// (WAPP_GRPC_PUSH_TIMEOUT), no uno inventado aquí. Con el Edge atascado, cada config se rinde
// por ese plazo, nombrando al Edge, y se intenta la siguiente.
func TestPushConfigsInBandIsBoundedByTheRegistrySendTimeout(t *testing.T) {
	t.Parallel()
	rig := newControlRig(t, &stubProvider{cfgs: []ConfigPayload{jwksCfg, filtersCfg}}, session.WithSendTimeout(time.Millisecond))
	stream, entered := stuckStream(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		rig.srv.pushConfigsInBand(context.Background(), controlChannel("tenant-1", "edge-1", stream))
	}()
	await(t, done, "que el push in-band se rinda por el plazo del Registry")

	// Las dos escrituras se lanzaron (su goroutine puede entrar al Send después del plazo).
	await(t, entered, "la primera escritura")
	await(t, entered, "la segunda escritura: un plazo vencido cortó las siguientes")
	if got := strings.Count(rig.log.String(), session.ErrPushTimeout.Error()); got != 2 {
		t.Errorf("escrituras rendidas por el plazo propio = %d, se esperaban 2: %q", got, rig.log.String())
	}
	if !rig.log.contains(`\"edge-1\"`) {
		t.Errorf("el error del plazo no nombra al Edge: %q", rig.log.String())
	}
}

// El otro reloj: el ctx de quien llama (el del stream, acotado por el presupuesto del canal de
// control). Si termina, se deja de esperar al Edge atascado sin agotar el plazo del Registry.
func TestPushConfigsInBandStopsWaitingWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	rig := newControlRig(t, &stubProvider{cfgs: []ConfigPayload{intentsCfg}}, session.WithSendTimeout(time.Hour))
	stream, entered := stuckStream(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rig.srv.pushConfigsInBand(ctx, controlChannel("tenant-1", "edge-1", stream))
	}()
	await(t, entered, "que la escritura llegue al Edge atascado")
	cancel()
	await(t, done, "que el push in-band vuelva al terminar su ctx")

	if !rig.log.contains(session.ErrPushAbandoned.Error()) || !rig.log.contains("kind=intents") {
		t.Errorf("la escritura abandonada no dejó su rastro: %q", rig.log.String())
	}
}
