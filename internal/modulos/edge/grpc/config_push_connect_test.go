package grpc

// El push de config AL CONECTAR por la sesión recién registrada (pushConfigsOnConnect, R-G17)
// y lo que WithConfigProvider deja en el Server. Nace con el verde: no tiene cara exportada
// hasta que exista Connect, así que se llama por dentro, como lo llamará registerSession.

import (
	"context"
	"errors"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

var (
	jwksCfg    = ConfigPayload{Kind: "jwks", Version: "1", Payload: []byte(`{"keys":[]}`)}
	intentsCfg = ConfigPayload{Kind: "intents", Version: "abc123", Payload: []byte(`{"version":"v1"}`)}
	filtersCfg = ConfigPayload{Kind: "filters", Version: "1700000000123456", Payload: []byte(`{"version":1700000000123456,"sessions":{}}`)}
)

// requireConfigs afirma que frames son, en ese orden, los ConfigUpdate de want dirigidos a
// sessionID, cada uno con su propio command_id.
func requireConfigs(t *testing.T, frames []*cloudlinkv1.CloudToEdge, sessionID string, want []ConfigPayload) {
	t.Helper()
	if len(frames) != len(want) {
		t.Fatalf("llegaron %d frames, se esperaban %d", len(frames), len(want))
	}
	commandIDs := map[string]bool{}
	for i, frame := range frames {
		commandIDs[requireConfigUpdate(t, frame, sessionID, want[i])] = true
	}
	if len(commandIDs) != len(want) {
		t.Errorf("command_ids distintos = %d, se esperaba uno por config (%d)", len(commandIDs), len(want))
	}
}

// WithConfigProvider deja en el Server el proveedor que se le dio; sin la opción no hay.
func TestWithConfigProviderInjectsTheProvider(t *testing.T) {
	t.Parallel()
	provider := &stubProvider{}
	if srv := New(session.NewRegistry(), quietLog(), WithConfigProvider(provider)); srv.configProvider != ConfigProvider(provider) {
		t.Errorf("configProvider = %v, se esperaba el inyectado", srv.configProvider)
	}
	if srv := New(session.NewRegistry(), quietLog()); srv.configProvider != nil {
		t.Errorf("sin WithConfigProvider, configProvider = %v", srv.configProvider)
	}
}

// Al conectar, la sesión recibe TODO lo que el proveedor da para SU tenant, en su orden y
// cada config en su frame: tres kinds con llm_intent y dos sin él (filters no se gatea).
func TestPushConfigsOnConnectDeliversEverythingTheProviderGives(t *testing.T) {
	t.Parallel()
	cases := map[string][]ConfigPayload{
		"tenant with llm_intent":    {jwksCfg, intentsCfg, filtersCfg},
		"tenant without llm_intent": {jwksCfg, filtersCfg},
		"nothing to push":           nil,
	}
	for name, cfgs := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			provider := &stubProvider{cfgs: cfgs}
			rig := newConfigRig(nil, WithConfigProvider(provider))
			mine := rig.goLive(t, "tenant-1", "edge-1", "s-1", nil)
			other := rig.goLive(t, "tenant-1", "edge-1", "s-2", nil)

			rig.srv.pushConfigsOnConnect(context.Background(), phone("tenant-1", "edge-1", "s-1"))

			requireConfigs(t, mine.received(), "s-1", cfgs)
			requireNothing(t, other)
			if got := provider.askedFor(); got != "tenant-1" {
				t.Errorf("al proveedor se le preguntó por %q, se esperaba una vez por tenant-1", got)
			}
		})
	}
}

// Sin proveedor, sin identidad mTLS o sin session_id no hay push: ni se pregunta al proveedor.
func TestPushConfigsOnConnectNeedsProviderIdentityAndSession(t *testing.T) {
	t.Parallel()
	provider := &stubProvider{cfgs: []ConfigPayload{intentsCfg}}
	rig := newConfigRig(nil, WithConfigProvider(provider))
	live := rig.goLive(t, "tenant-1", "edge-1", "s-1", nil)
	empty := &liveSession{id: ""}
	t.Cleanup(rig.reg.Register("", empty))

	rig.srv.pushConfigsOnConnect(context.Background(), connCtx{sessionID: "s-1", tenantID: "tenant-1", edgeID: "edge-1"})
	rig.srv.pushConfigsOnConnect(context.Background(), phone("tenant-1", "edge-1", ""))
	requireNothing(t, live, empty)
	if got := provider.askedFor(); got != "" {
		t.Errorf("se preguntó al proveedor (%q) sin identidad o sin sesión", got)
	}

	bare := newConfigRig(nil)
	alone := bare.goLive(t, "tenant-1", "edge-1", "s-1", nil)
	bare.srv.pushConfigsOnConnect(context.Background(), phone("tenant-1", "edge-1", "s-1"))
	requireNothing(t, alone)
}

// Si el proveedor falla se registra el error (con tenant y sesión) y no se empuja nada,
// aunque hubiera devuelto configs.
func TestPushConfigsOnConnectLogsAProviderFailure(t *testing.T) {
	t.Parallel()
	provider := &stubProvider{cfgs: []ConfigPayload{intentsCfg}, err: errors.New("base caída")}
	rig := newConfigRig(nil, WithConfigProvider(provider))
	live := rig.goLive(t, "tenant-1", "edge-1", "s-1", nil)

	rig.srv.pushConfigsOnConnect(context.Background(), phone("tenant-1", "edge-1", "s-1"))

	requireNothing(t, live)
	for _, want := range []string{"level=ERROR", `msg="config push: resolver configs al conectar"`, "base caída", "tenant_id=tenant-1", "session_id=s-1"} {
		if !rig.log.contains(want) {
			t.Errorf("al log del fallo le falta %q: %q", want, rig.log.String())
		}
	}
}

// Un empuje que falla se registra en debug y NO corta los siguientes: va por el Registry, así
// que una sesión sin stream los falla todos, uno por config.
func TestPushConfigsOnConnectKeepsGoingAfterAFailedPush(t *testing.T) {
	t.Parallel()
	rig := newConfigRig(nil, WithConfigProvider(&stubProvider{cfgs: []ConfigPayload{jwksCfg, intentsCfg, filtersCfg}}))

	rig.srv.pushConfigsOnConnect(context.Background(), phone("tenant-1", "edge-1", "s-gone"))

	if got := strings.Count(rig.log.String(), "config push: inicial a sesión"); got != 3 {
		t.Fatalf("líneas de empuje fallido = %d, se esperaban 3: %q", got, rig.log.String())
	}
	for _, want := range []string{"level=DEBUG", "session_id=s-gone", "kind=jwks", "kind=intents", "kind=filters", session.ErrSessionOffline.Error()} {
		if !rig.log.contains(want) {
			t.Errorf("al log de los empujes fallidos le falta %q: %q", want, rig.log.String())
		}
	}
}
