package bootstrap_test

import (
	"net/http"
	"testing"
)

// TestMuxRegistration_NoPanic verifica que el registro de todas las rutas admin
// (incluyendo rutas parametrizadas como GET /admin/tenants/{id} y rutas POST /admin/tenants/revoke)
// no colisiona ni genera panic en http.ServeMux (Go >= 1.22, D-056.2 / T1.3).
func TestMuxRegistration_NoPanic(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic al registrar rutas en ServeMux: %v", r)
		}
	}()

	mux := http.NewServeMux()
	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	// Rutas estándar de bootstrap.go
	mux.Handle("/healthz", dummy)
	mux.Handle("/metrics", dummy)
	mux.Handle("/admin/leases/revoke", dummy)
	mux.Handle("/admin/messages/send", dummy)
	mux.Handle("/admin/crypto/rekey", dummy)
	mux.Handle("/admin/flows", dummy)
	mux.Handle("/admin/flows/start", dummy)
	mux.Handle("POST /admin/triggers", dummy)
	mux.Handle("GET /admin/triggers", dummy)
	mux.Handle("DELETE /admin/triggers/{id}", dummy)
	mux.Handle("POST /admin/sessions/{id}/role", dummy)
	mux.Handle("POST /admin/sessions/{id}/status", dummy)

	// Rutas de plataforma (Plan 056 · T1.3, T2.2, T2.3, T3.4)
	mux.Handle("GET /admin/tenants", dummy)
	mux.Handle("POST /admin/tenants", dummy)
	mux.Handle("GET /admin/tenants/{id}", dummy)
	mux.Handle("GET /admin/tenants/{id}/installations", dummy)
	mux.Handle("POST /admin/tenants/{id}/enrollment-codes", dummy)
	mux.Handle("GET /admin/access-requests", dummy)
	mux.Handle("POST /admin/access-requests/{id}/approve", dummy)
	mux.Handle("POST /admin/access-requests/{id}/reject", dummy)
	mux.Handle("POST /admin/tenants/revoke", dummy)
	mux.Handle("POST /admin/tenants/restore", dummy)
}
