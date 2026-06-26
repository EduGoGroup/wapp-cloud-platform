package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// LeaseRevoker dispara el kill-switch anti-clon (ADR-0007) de un Edge concreto.
// Lo satisface *gatewaygrpc.Server con su método RevokeLease.
type LeaseRevoker interface {
	RevokeLease(ctx context.Context, tenantID, edgeID string) error
}

// revokeLeaseRequest es el cuerpo JSON del endpoint de revocación.
type revokeLeaseRequest struct {
	TenantID string `json:"tenant_id"`
	EdgeID   string `json:"edge_id"`
}

// RevokeLeaseHandler devuelve el handler del endpoint admin de revocación de
// leases (kill-switch). Acepta POST con cuerpo JSON {tenant_id, edge_id} y, al
// éxito, responde 204 No Content.
//
// SEGURIDAD — auth DIFERIDA: este corte (Plan 005) aún NO tiene IAM, por lo que
// el endpoint NO está autenticado. Es un endpoint INTERNO: debe exponerse solo
// en la red de administración (mismo http.Server de /healthz, no público) hasta
// que la fase IAM añada autenticación/RBAC. No montar de cara a Internet.
func RevokeLeaseHandler(revoker LeaseRevoker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método no permitido (usar POST)", http.StatusMethodNotAllowed)
			return
		}

		var req revokeLeaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "cuerpo JSON inválido", http.StatusBadRequest)
			return
		}
		if req.TenantID == "" || req.EdgeID == "" {
			http.Error(w, "tenant_id y edge_id son requeridos", http.StatusBadRequest)
			return
		}

		if err := revoker.RevokeLease(r.Context(), req.TenantID, req.EdgeID); err != nil {
			http.Error(w, "no se pudo revocar el lease", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}
