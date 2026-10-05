package grpc

// El seguimiento de las sesiones vivas por Edge (trackSession, untrackSession,
// sessionsForEdge): el índice que escribe el registro de la sesión y que leen el kill-switch,
// el fan-out de config y el readiness. connect_session.go no tiene exportados: nace en verde
// (T-17) y se prueba por dentro. El registro está en connect_session_register_test.go, el
// canal de control en connect_session_control_test.go y el cierre en
// connect_session_offline_test.go.

import (
	"sort"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// trackAll deja vivas esas sesiones del Edge, una llamada a trackSession por cada una.
func trackAll(srv *Server, tenantID, edgeID string, sessionIDs ...string) {
	for _, sid := range sessionIDs {
		srv.trackSession(phone(tenantID, edgeID, sid))
	}
}

// sortedSessions devuelve las sesiones del Edge ordenadas y unidas, para comparar.
func sortedSessions(srv *Server, tenantID, edgeID string) string {
	got := srv.sessionsForEdge(tenantID, edgeID)
	sort.Strings(got)
	return strings.Join(got, ",")
}

// sessionsForEdge devuelve las sesiones de ESE (tenant, Edge) y ninguna más: ni las de otro
// Edge del mismo tenant, ni las del mismo edge_id bajo otro tenant.
func TestSessionsForEdgeIsScopedToTenantAndEdge(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	trackAll(srv, "tenant-1", "edge-1", "s-1", "s-2", "s-3")
	trackAll(srv, "tenant-1", "edge-2", "s-4")
	trackAll(srv, "tenant-2", "edge-1", "s-5", "s-6")

	cases := []struct{ tenantID, edgeID, want string }{
		{"tenant-1", "edge-1", "s-1,s-2,s-3"},
		{"tenant-1", "edge-2", "s-4"},
		{"tenant-2", "edge-1", "s-5,s-6"},
		{"tenant-2", "edge-2", ""},
		{"edge-1", "tenant-1", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := sortedSessions(srv, tc.tenantID, tc.edgeID); got != tc.want {
			t.Errorf("sessionsForEdge(%q, %q) = %q, se esperaba %q", tc.tenantID, tc.edgeID, got, tc.want)
		}
	}
}

// Devuelve una COPIA (tocarla no altera el seguimiento) y, sin sesiones, una lista vacía no nil.
func TestSessionsForEdgeReturnsACopy(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	trackAll(srv, "tenant-1", "edge-1", "s-1", "s-2")

	got := srv.sessionsForEdge("tenant-1", "edge-1")
	for i := range got {
		got[i] = "tampered"
	}
	if again := sortedSessions(srv, "tenant-1", "edge-1"); again != "s-1,s-2" {
		t.Fatalf("tocar la lista devuelta alteró el seguimiento: %q", again)
	}

	if none := srv.sessionsForEdge("tenant-1", "edge-unknown"); none == nil || len(none) != 0 {
		t.Fatalf("sessionsForEdge de un Edge sin sesiones = %#v, se esperaba una lista vacía no nil", none)
	}
}

// Rastrear dos veces la misma sesión la deja una vez; dejar de rastrear quita ESA y ninguna
// otra, ni del mismo Edge ni del mismo edge_id bajo otro tenant; y con la última el Edge
// desaparece del índice.
func TestUntrackSessionRemovesOnlyThatSession(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	trackAll(srv, "tenant-1", "edge-1", "s-1", "s-2", "s-1")
	trackAll(srv, "tenant-2", "edge-1", "s-1")

	srv.untrackSession(phone("tenant-1", "edge-1", "s-1"))
	if got := sortedSessions(srv, "tenant-1", "edge-1"); got != "s-2" {
		t.Fatalf("tras quitar s-1 quedan %q, se esperaba s-2", got)
	}
	if got := sortedSessions(srv, "tenant-2", "edge-1"); got != "s-1" {
		t.Fatalf("quitar la sesión de un tenant tocó la del otro: %q", got)
	}

	srv.untrackSession(phone("tenant-1", "edge-1", "s-unknown"))
	srv.untrackSession(phone("tenant-1", "edge-1", "s-2"))
	srv.untrackSession(phone("tenant-1", "edge-1", "s-2")) // el Edge ya se vació: no-op
	if _, tracked := srv.edgeSessions[edgeKey{tenantID: "tenant-1", edgeID: "edge-1"}]; tracked || len(srv.edgeSessions) != 1 {
		t.Errorf("el Edge sin sesiones sigue en el índice: %v", srv.edgeSessions)
	}
}

// R-G16: lo que el Edge dijo de su capacidad de inferencia se olvida SOLO con su ÚLTIMA
// sesión. Un stream cierra sus sesiones de una en una; olvidar con la primera haría que el
// siguiente READY de las que quedan se leyera como un flanco inventado.
func TestUntrackSessionForgetsReadinessOnlyWithTheLastSession(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), quietLog())
	trackAll(srv, "tenant-1", "edge-1", "s-1", "s-2", "s-3")
	trackAll(srv, "tenant-1", "edge-2", "s-9")
	edge, other := phone("tenant-1", "edge-1", "s-1"), phone("tenant-1", "edge-2", "s-9")
	srv.noteReadiness(edge, saysReady)
	srv.noteReadiness(other, saysDown)

	for _, sid := range []string{"s-1", "s-2"} {
		srv.untrackSession(phone("tenant-1", "edge-1", sid))
		if got := srv.readinessOf(edge); got != saysReady {
			t.Fatalf("al cerrar %s, con sesiones aún vivas, el readiness pasó a %v", sid, got)
		}
	}
	if srv.noteReadiness(edge, saysReady) {
		t.Fatal("un READY repetido se leyó como flanco: el readiness se olvidó antes de tiempo")
	}

	srv.untrackSession(phone("tenant-1", "edge-1", "s-3"))
	if got := srv.readinessOf(edge); got != saysNothing {
		t.Errorf("con la última sesión cerrada el readiness sigue en %v", got)
	}
	if got := srv.readinessOf(other); got != saysDown || len(srv.edgeReadiness) != 1 {
		t.Errorf("olvidar un Edge tocó a otro: %v", srv.edgeReadiness)
	}

	// Quitar una sesión de un Edge que ya no tiene ninguna no borra nada.
	srv.untrackSession(phone("tenant-1", "edge-3", "s-1"))
	srv.noteReadiness(phone("tenant-1", "edge-3", "s-1"), saysReady)
	srv.untrackSession(phone("tenant-1", "edge-3", "s-1"))
	if got := srv.readinessOf(phone("tenant-1", "edge-3", "s-1")); got != saysReady {
		t.Errorf("un untrack sobre un Edge sin sesiones borró su readiness (%v)", got)
	}
}
