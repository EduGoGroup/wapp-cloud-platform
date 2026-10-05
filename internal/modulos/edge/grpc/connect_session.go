// Porta internal/gateway/grpc/connect.go @ c851591 (el seguimiento de las sesiones
// vivas por Edge). Trozo de connect.go, partido por E-13.
//
// En esta tanda solo nace sessionsForEdge, que es lo que la familia de la revocación
// (send_revoke.go) necesita para su fan-out. El resto del trozo —registerSession,
// trackSession, untrackSession y los ganchos del registro y del cierre— llega con
// connect.go, que es quien escribe el mapa que aquí se lee.

package grpc

// sessionsForEdge devuelve una copia de las sesiones vivas del Edge dado: las de
// ESE (tenant, Edge) y ninguna más —el mismo edge_id bajo otro tenant es otro Edge—.
// Sin sesiones devuelve una lista vacía, no nil. El orden no está definido.
func (s *Server) sessionsForEdge(tenantID, edgeID string) []string {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	set := s.edgeSessions[edgeKey{tenantID: tenantID, edgeID: edgeID}]
	out := make([]string, 0, len(set))
	for sid := range set {
		out = append(out, sid)
	}
	return out
}
