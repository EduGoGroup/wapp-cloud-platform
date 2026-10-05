//go:build pendiente

package grpc

// El canal de control visto desde Connect (ADR-0048; R-G8, R3.4.a–c; T-5, HS-14/HS-15):
// `__wapp_control__` es el session_id que TODO Edge estampa en sus frames de auth. No es una
// sesión: no entra en el Registry —que indexa sin tenant y con última-gana— ni en el
// seguimiento, ni en flota, ni recibe lease ni calentamiento. Lo único que provoca es la
// config de su tenant por su propio cable, una vez por stream, y la respuesta de auth por
// ese mismo cable.
//
// 🔬 Mutación que pone esto en rojo: tratar ese id como cualquier otro en el bucle de Connect
// (registrarlo en el Registry).

import (
	"context"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"
)

const control = cltransport.ControlSessionID

// splitFrames separa lo que recibió un Edge en configs, respuestas de auth y leases, y falla
// ante cualquier otra cosa.
func splitFrames(t *testing.T, frames []*cloudlinkv1.CloudToEdge) (configs, answers, leases []*cloudlinkv1.CloudToEdge) {
	t.Helper()
	for _, frame := range frames {
		switch frame.GetPayload().(type) {
		case *cloudlinkv1.CloudToEdge_ConfigUpdate:
			configs = append(configs, frame)
		case *cloudlinkv1.CloudToEdge_UserAuthResponse:
			answers = append(answers, frame)
		case *cloudlinkv1.CloudToEdge_LeaseUpdate:
			leases = append(leases, frame)
		default:
			t.Fatalf("frame inesperado: %T", frame.GetPayload())
		}
	}
	return configs, answers, leases
}

// Un Edge sin teléfonos que solo hace login: recibe su config (una vez, aunque mande más
// frames de control) y sus respuestas, todo por su stream; y el gateway no guarda NADA de él
// —ni en el Registry, ni rastreado, ni en flota, ni lease, ni calentamiento—, ni al usar el
// canal ni al colgar.
func TestConnectNeverRegistersTheControlChannel(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t, WithConfigProvider(&stubProvider{cfgs: connectCfgs}))
	warmed := 0
	rig.srv.OnWarmup = func(string, string, string, string) { warmed++ }
	edge := openStream(t, rig.srv, forgedIdentity("tenant-1", "edge-1"))
	cc := controlChannel("tenant-1", "edge-1", nil)

	edge.send(t, loginFrame(control, "cmd-1"))
	if rig.reg.Online(control) || rig.reg.Count() != 0 {
		t.Fatalf("el canal de control quedó en el Registry (%d entradas)", rig.reg.Count())
	}
	if len(rig.srv.edgeSessions) != 0 || len(rig.srv.sessionsForTenant("tenant-1")) != 0 {
		t.Fatalf("el canal de control quedó en el seguimiento: %v", rig.srv.edgeSessions)
	}
	edge.send(t, refreshFrame(control, "cmd-2", "refresh-tenant-1"))
	edge.hangUp(t)

	configs, answers, leases := splitFrames(t, edge.received())
	requireConfigs(t, configs, control, connectCfgs)
	if len(answers) != 2 || len(leases) != 0 {
		t.Fatalf("el Edge recibió %d respuestas y %d leases, se esperaban 2 y ninguno", len(answers), len(leases))
	}
	requireTokens(t, requireAuthResponse(t, answers[0], cc, "cmd-1"), tokensFor("tenant-1", "user-1"))
	requireTokens(t, requireAuthResponse(t, answers[1], cc, "cmd-2"), tokensFor("tenant-1", "user-1"))

	if rig.tl.String() != "" || warmed != 0 || rig.log.contains(registeredLine) {
		t.Errorf("el canal de control tocó flota o lease (%q), calentó (%d) o se anotó como sesión", rig.tl.String(), warmed)
	}
	rows, err := rig.fleet.List(t.Context(), "tenant-1")
	if err != nil || len(rows) != 0 {
		t.Errorf("la flota tiene %d filas (err=%v), se esperaba ninguna", len(rows), err)
	}
	for _, ev := range rig.audit.recorded() {
		if ev.Action == "edge.session.open" {
			t.Errorf("el canal de control se auditó como apertura de sesión: %+v", ev)
		}
	}
	requireLogHas(t, rig.log, controlInUseLine)
}

// HS-14/HS-15 con dos Edge de empresas distintas a la vez, los dos por el canal de control:
// cada uno recibe la config de SU tenant y la respuesta a SU login, y nada del otro. Con la
// clave compartida en el Registry, lo del primero saldría por el cable del último.
func TestConnectKeepsTwoControlChannelsApart(t *testing.T) {
	t.Parallel()
	perTenant := providerFunc(func(_ context.Context, tenantID string) ([]ConfigPayload, error) {
		return []ConfigPayload{{Kind: "intents", Version: "v1", Payload: []byte("catalog-of-" + tenantID)}}, nil
	})
	rig := newRouteRig(t, WithConfigProvider(perTenant))
	edgeA := openStream(t, rig.srv, forgedIdentity("tenant-a", "edge-a"))
	edgeB := openStream(t, rig.srv, forgedIdentity("tenant-b", "edge-b"))

	edgeA.send(t, loginFrame(control, "cmd-a-1"))
	edgeB.send(t, loginFrame(control, "cmd-b-1"))
	edgeA.send(t, loginFrame(control, "cmd-a-2")) // B ya habló: con última-gana, esto saldría por B
	edgeA.hangUp(t)
	edgeB.send(t, loginFrame(control, "cmd-b-2")) // colgar A no deja a B sin canal de control
	edgeB.hangUp(t)

	for _, tc := range []struct {
		edge     *memEdge
		tenantID string
		cmdIDs   []string
	}{{edgeA, "tenant-a", []string{"cmd-a-1", "cmd-a-2"}}, {edgeB, "tenant-b", []string{"cmd-b-1", "cmd-b-2"}}} {
		configs, answers, _ := splitFrames(t, tc.edge.received())
		if len(configs) != 1 || string(configs[0].GetConfigUpdate().GetPayload()) != "catalog-of-"+tc.tenantID {
			t.Errorf("el Edge de %s recibió %d configs %q, se esperaba solo la suya", tc.tenantID, len(configs), configs)
		}
		if len(answers) != len(tc.cmdIDs) {
			t.Fatalf("el Edge de %s recibió %d respuestas, se esperaban %d", tc.tenantID, len(answers), len(tc.cmdIDs))
		}
		for i, cmdID := range tc.cmdIDs {
			cc := controlChannel(tc.tenantID, "", nil)
			requireTokens(t, requireAuthResponse(t, answers[i], cc, cmdID), tokensFor(tc.tenantID, "user-1"))
		}
	}
}

// R3.4.b en un mismo stream: el Edge tiene un teléfono Y el operador logueado. El fan-out de
// PushConfig alcanza a la sesión del teléfono —una vez— y NO al canal de control; y el Edge de
// otra empresa que solo tiene canal de control no recibe nada del catálogo ajeno.
func TestConnectPushConfigReachesPhonesAndNeverAControlChannel(t *testing.T) {
	t.Parallel()
	rig := newRouteRig(t)
	mine := openStream(t, rig.srv, forgedIdentity("tenant-a", "edge-a"))
	other := openStream(t, rig.srv, forgedIdentity("tenant-b", "edge-b"))
	mine.send(t, loginFrame(control, "cmd-a"), pongFrame("s-phone", 1))
	other.send(t, loginFrame(control, "cmd-b"))

	if err := pushIntents(t.Context(), rig.srv, "tenant-a"); err != nil {
		t.Fatalf("PushConfig: %v", err)
	}
	if err := pushIntents(t.Context(), rig.srv, "tenant-b"); err != nil {
		t.Fatalf("PushConfig a un tenant sin sesiones: %v", err)
	}
	mine.hangUp(t)
	other.hangUp(t)

	configs, _, _ := splitFrames(t, mine.received())
	if len(configs) != 1 {
		t.Fatalf("el Edge con teléfono recibió %d configs, se esperaba UNA (la de su sesión)", len(configs))
	}
	requireConfigUpdate(t, configs[0], "s-phone", intentsV2)
	if configs, _, _ = splitFrames(t, other.received()); len(configs) != 0 {
		t.Errorf("un Edge que solo tiene canal de control recibió %d configs por el fan-out", len(configs))
	}
}
