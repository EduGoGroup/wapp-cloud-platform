package grpc

// El cierre de una sesión (onStreamClosed; R-G4, R-G5, DEUDA-050.1): se deja de rastrear en el
// acto y su MarkOffline se ENCOLA como último job de esa sesión, donde la pregunta «¿sigue
// caída?» se hace AL EJECUTAR, no al encolar. Los dos tests de la reconexión se leen en
// pareja: el segundo impide que la mitigación degenere en «dejar de marcar offline».

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

const reconnectedLine = `msg="fleet: no se marca offline; la sesión ya reconectó por otro stream"`

// closingRig es el banco de route con UNA sesión registrada y online en flota, rastreada, y el
// worker de su carril tapado: lo que se encole se queda en la cola hasta soltar.
func closingRig(t *testing.T) (rig *routeRig, cc connCtx, release func()) {
	t.Helper()
	rig = newRouteRig(t)
	cc = phone("tenant-1", "edge-1", "s-1")
	rig.online(t, cc)
	rig.srv.trackSession(cc)
	return rig, cc, plugLane(t, rig.lane, "s-1", jobReceipt)
}

// El MarkOffline es DIFERIDO: al volver onStreamClosed la sesión ya no se rastrea, pero la
// fila sigue online y hay UN job offline en su cola. Al correr, marca offline con plazo.
func TestStreamClosedDefersMarkOfflineToTheLane(t *testing.T) {
	t.Parallel()
	rig, cc, release := closingRig(t)

	rig.srv.onStreamClosed(rig.lane, cc)

	if got := sortedSessions(rig.srv, "tenant-1", "edge-1"); got != "" {
		t.Errorf("la sesión sigue rastreada tras el cierre: %q", got)
	}
	if total, byKind := rig.lane.pending(); total != 1 || byKind["offline"] != 1 {
		t.Fatalf("el cierre dejó %d jobs %v, se esperaba UNO de tipo offline", total, byKind)
	}
	if row := rig.row(t, cc); row.State != fleet.StateOnline || rig.tl.String() != "" {
		t.Fatalf("con el carril tapado la fila ya está en %q (%q): el MarkOffline no se difirió", row.State, rig.tl.String())
	}
	release()
	closeLane(t, rig.lane)

	if row := rig.row(t, cc); row.State != fleet.StateOffline || rig.tl.String() != "MarkOffline edges=0" {
		t.Errorf("fila en %q tras %q, se esperaba offline tras un MarkOffline", row.State, rig.tl.String())
	}
	rig.tl.requireBounded(t)
}

// D-050.2: el MarkOffline es lo ÚLTIMO que se escribe de la sesión. El latido que seguía
// pendiente cuando el stream cayó se persiste ANTES, y la flota no acaba mostrando online un
// Edge que ya se fue.
func TestStreamClosedMarksOfflineAfterThePendingWork(t *testing.T) {
	t.Parallel()
	rig, cc, release := closingRig(t)
	rig.srv.route(rig.lane, cc, heartbeatFrame("s-1", fullHeartbeat()))

	rig.srv.onStreamClosed(rig.lane, cc)
	release()
	closeLane(t, rig.lane)

	if got, want := rig.tl.String(), heartbeatTimeline+" > MarkOffline edges=1"; got != want {
		t.Fatalf("se escribió %q, se esperaba %q", got, want)
	}
	if row := rig.row(t, cc); row.State != fleet.StateOffline || row.WhatsappState != "connected" {
		t.Errorf("fila = (%q, whatsapp %q), se esperaba offline con la salud del último latido", row.State, row.WhatsappState)
	}
}

// R-G4, la reconexión rápida: si entre el cierre y la ejecución del job la sesión volvió por
// OTRO stream, NO se marca offline —pisaría a la sesión viva— y queda dicho en el log.
func TestStreamClosedKeepsOnlineASessionThatReconnected(t *testing.T) {
	t.Parallel()
	rig, cc, release := closingRig(t)

	rig.srv.onStreamClosed(rig.lane, cc)
	rig.goLive(t, "s-1") // el stream nuevo registra la misma sesión
	release()
	closeLane(t, rig.lane)

	if row := rig.row(t, cc); row.State != fleet.StateOnline || rig.tl.String() != "" {
		t.Fatalf("fila en %q tras %q: el cierre del stream viejo pisó a la sesión que ya había vuelto", row.State, rig.tl.String())
	}
	requireLogHas(t, rig.log, "level=INFO", reconnectedLine, "edge_id=edge-1", "session_id=s-1")
}

// El gemelo: sin reconexión se marca offline. Y la pregunta se hace al EJECUTAR: una sesión
// que aún tenía stream al encolar pero ya no al correr el job queda offline igual.
func TestStreamClosedMarksOfflineWithoutReconnection(t *testing.T) {
	t.Parallel()
	rig, cc, release := closingRig(t)
	stillThere := rig.reg.Register("s-1", &liveSession{id: "s-1"})

	rig.srv.onStreamClosed(rig.lane, cc)
	stillThere()
	release()
	closeLane(t, rig.lane)

	if row := rig.row(t, cc); row.State != fleet.StateOffline {
		t.Fatalf("fila en %q, se esperaba offline: la sesión no tiene stream cuando corre el job", row.State)
	}
	if rig.log.contains(reconnectedLine) {
		t.Errorf("se dio por reconectada una sesión sin stream: %q", rig.log.String())
	}
}

// Sin identidad mTLS o sin session_id no hay sesión que cerrar: ni se toca el seguimiento ni
// se encola nada.
func TestStreamClosedDoesNothingWithoutIdentityOrSession(t *testing.T) {
	t.Parallel()
	for name, closing := range degradedChannels() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig, _, _ := closingRig(t)
			rig.srv.trackSession(phone("tenant-1", "edge-1", ""))

			rig.srv.onStreamClosed(rig.lane, closing)

			if got := len(rig.srv.sessionsForEdge("tenant-1", "edge-1")); got != 2 {
				t.Errorf("quedan %d sesiones rastreadas, se esperaban las 2", got)
			}
			if total, _ := rig.lane.pending(); total != 0 {
				t.Errorf("se encolaron %d jobs", total)
			}
		})
	}
}

// Sin repositorio de flota la sesión se deja de rastrear igual, y no hay nada que encolar.
func TestStreamClosedWithoutFleetOnlyUntracks(t *testing.T) {
	t.Parallel()
	rig, cc, _ := closingRig(t)
	rig.srv.fleet = nil

	rig.srv.onStreamClosed(rig.lane, cc)

	if got := sortedSessions(rig.srv, "tenant-1", "edge-1"); got != "" {
		t.Errorf("la sesión sigue rastreada: %q", got)
	}
	if total, _ := rig.lane.pending(); total != 0 {
		t.Errorf("sin flota se encolaron %d jobs", total)
	}
}

// Un fallo al marcar offline se anota con los ids y no rompe nada.
func TestStreamClosedLogsAFailedMarkOffline(t *testing.T) {
	t.Parallel()
	rig, cc, release := closingRig(t)
	rig.fleet.fail["MarkOffline"] = errors.New("flota caída")

	rig.srv.onStreamClosed(rig.lane, cc)
	release()
	closeLane(t, rig.lane)

	requireLogHas(t, rig.log, "level=ERROR", `msg="fleet: marcar offline"`, "flota caída", "edge_id=edge-1", "session_id=s-1")
}
