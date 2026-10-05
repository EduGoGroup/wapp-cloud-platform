package grpc

// El canal de control NO es una sesión (onControlChannel; ADR-0048, R-G8, R3.4.b, R3.4.c,
// T-5): cuando un stream lo usa, lo ÚNICO que hace el gateway es empujarle la config de su
// tenant por su propio cable. Ni Registry, ni seguimiento, ni flota, ni lease, ni auditoría.
//
// 🔬 Mutación que pone esto en rojo: borrar la llamada a pushConfigsInBand de onControlChannel.

import (
	"context"
	"testing"
	"time"

	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

const (
	controlInUseLine  = `msg="canal de control en uso (no se registra como sesión)"`
	controlBudgetLine = `msg="canal de control: la config inicial no salió dentro de su presupuesto"`
	controlGoneLine   = `msg="canal de control: el stream se fue antes de empujar la config inicial"`
)

// Un Edge sin ningún teléfono recibe su config por el cable que acaba de hablar, y el gateway
// no deja NADA más: la clave compartida no entra en el Registry, el Edge no aparece en el
// seguimiento (así PushConfig y el kill-switch no lo alcanzan), y no hay flota, lease ni
// auditoría de sesión.
func TestControlChannelOnlyPushesConfigInBand(t *testing.T) {
	t.Parallel()
	providerBounded := false
	rig := newRouteRig(t, WithConfigProvider(providerFunc(func(ctx context.Context, _ string) ([]ConfigPayload, error) {
		_, providerBounded = ctx.Deadline()
		return connectCfgs, nil
	})))
	stream := &liveSession{id: "el stream del Edge"}

	rig.srv.onControlChannel(context.Background(), controlChannel("tenant-1", "edge-1", stream))

	requireConfigs(t, stream.received(), cltransport.ControlSessionID, connectCfgs)
	if !providerBounded {
		t.Error("al proveedor de config se le preguntó con un ctx sin plazo")
	}
	if rig.reg.Online(cltransport.ControlSessionID) || rig.reg.Count() != 0 {
		t.Errorf("el canal de control quedó en el Registry (%d entradas)", rig.reg.Count())
	}
	if len(rig.srv.edgeSessions) != 0 || len(rig.srv.sessionsForTenant("tenant-1")) != 0 {
		t.Errorf("el canal de control quedó en el seguimiento: %v", rig.srv.edgeSessions)
	}
	if rig.tl.String() != "" || len(rig.audit.recorded()) != 0 || rig.leaseCounter(t, phone("tenant-1", "edge-1", "")) != -1 {
		t.Errorf("el canal de control tocó flota, lease o auditoría: escrituras %q, auditoría %d", rig.tl.String(), len(rig.audit.recorded()))
	}
	requireLogHas(t, rig.log, "level=INFO", controlInUseLine, "edge_id=edge-1", "tenant_id=tenant-1")
	if rig.log.contains("canal de control: ") {
		t.Errorf("un push limpio no deja aviso: %q", rig.log.String())
	}
}

// T-5: la config no pasa por el Registry aunque la clave de control esté ocupada allí por el
// Edge de otra empresa: el intruso no recibe nada.
func TestControlChannelNeverReachesWhoeverHoldsTheSharedKey(t *testing.T) {
	t.Parallel()
	rig := newControlRig(t, &stubProvider{cfgs: connectCfgs})
	stream := &liveSession{id: "el stream del Edge"}

	rig.srv.onControlChannel(context.Background(), controlChannel("tenant-1", "edge-1", stream))

	requireConfigs(t, stream.received(), cltransport.ControlSessionID, connectCfgs)
	requireNothing(t, rig.intruder)
}

// Sin identidad mTLS no se sabe de qué tenant es la config: no se pregunta, no se empuja y no
// se anota nada.
func TestControlChannelDoesNothingWithoutIdentity(t *testing.T) {
	t.Parallel()
	rig := newControlRig(t, &stubProvider{cfgs: connectCfgs})
	stream := &liveSession{id: "el stream del Edge"}
	anonymous := controlChannel("", "", stream)
	anonymous.hasIdentity = false

	rig.srv.onControlChannel(context.Background(), anonymous)

	requireNothing(t, stream, rig.intruder)
	if rig.provider.askedFor() != "" || rig.log.String() != "" {
		t.Errorf("un stream anónimo dejó rastro: proveedor %q, log %q", rig.provider.askedFor(), rig.log.String())
	}
}

// Corre INLINE en el bucle Recv, así que trae su reloj: con el Edge atascado, la config se
// rinde por el presupuesto de trabajo —no por el plazo de envío del Registry, que aquí es de
// una hora— y queda un Warn que acusa al presupuesto.
func TestControlChannelGivesUpWhenTheBudgetRunsOut(t *testing.T) {
	t.Parallel()
	rig := newConfigRig([]session.RegistryOption{session.WithSendTimeout(time.Hour)},
		WithConfigProvider(&stubProvider{cfgs: []ConfigPayload{jwksCfg}}), WithWorkTimeout(time.Millisecond))
	stream, entered := stuckStream(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		rig.srv.onControlChannel(context.Background(), controlChannel("tenant-1", "edge-1", stream))
	}()
	await(t, entered, "que la config llegue al Edge atascado")
	await(t, done, "que el canal de control se rinda por su presupuesto")

	requireLogHas(t, rig.log, "level=WARN", controlBudgetLine, "edge_id=edge-1", "budget=1ms")
	if rig.log.contains(controlGoneLine) {
		t.Errorf("un plazo vencido se anotó como stream caído: %q", rig.log.String())
	}
}

// Y cuelga del ctx del STREAM: si el stream se fue, empujarle config a nadie no sirve; se
// anota como stream caído, no como presupuesto vencido.
func TestControlChannelGivesUpWhenTheStreamGoesAway(t *testing.T) {
	t.Parallel()
	rig := newConfigRig([]session.RegistryOption{session.WithSendTimeout(time.Hour)},
		WithConfigProvider(&stubProvider{cfgs: []ConfigPayload{jwksCfg}}))
	stream, entered := stuckStream(t)
	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		rig.srv.onControlChannel(streamCtx, controlChannel("tenant-1", "edge-1", stream))
	}()
	await(t, entered, "que la config llegue al Edge atascado")
	cancel()
	await(t, done, "que el canal de control se rinda con el stream")

	requireLogHas(t, rig.log, "level=INFO", controlGoneLine, "edge_id=edge-1", context.Canceled.Error())
	if rig.log.contains(controlBudgetLine) {
		t.Errorf("un stream caído se anotó como presupuesto vencido: %q", rig.log.String())
	}
}
