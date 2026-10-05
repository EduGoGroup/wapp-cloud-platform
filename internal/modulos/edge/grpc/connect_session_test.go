package grpc

import (
	"sort"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// seedEdgeSessions deja sesiones vivas para un Edge en el seguimiento del Server, como hará
// trackSession cuando llegue connect.go (hasta entonces el mapa solo se puede sembrar por
// dentro).
func seedEdgeSessions(srv *Server, tenantID, edgeID string, sessionIDs ...string) {
	srv.trackMu.Lock()
	defer srv.trackMu.Unlock()
	k := edgeKey{tenantID: tenantID, edgeID: edgeID}
	set := srv.edgeSessions[k]
	if set == nil {
		set = make(map[string]struct{})
		srv.edgeSessions[k] = set
	}
	for _, sid := range sessionIDs {
		set[sid] = struct{}{}
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
	seedEdgeSessions(srv, "tenant-1", "edge-1", "s-1", "s-2", "s-3")
	seedEdgeSessions(srv, "tenant-1", "edge-2", "s-4")
	seedEdgeSessions(srv, "tenant-2", "edge-1", "s-5", "s-6")

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
	seedEdgeSessions(srv, "tenant-1", "edge-1", "s-1", "s-2")

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
