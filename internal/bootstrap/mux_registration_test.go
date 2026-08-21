package bootstrap

import (
	"net/http"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// TestMuxRegistration_NoPanic verifica que el registro REAL de todas las rutas
// admin (incluyendo rutas parametrizadas como GET /admin/tenants/{id} y rutas
// POST /admin/tenants/revoke) no colisiona ni genera panic en http.ServeMux
// (Go >= 1.22, D-056.2 / T1.3).
//
// A-02: llama a registerAdminRoutes, la MISMA función que Run() usa en
// producción — no una copia a mano de la lista de patrones. Una copia a mano
// solo prueba que la copia no colisiona consigo misma; esto prueba que
// bootstrap.go de verdad no colisiona. Los handlers son dummy (ninguno se
// invoca: el test solo ejercita mux.Handle, que es donde http.ServeMux
// detecta y hace panic ante un conflicto de patrones).
func TestMuxRegistration_NoPanic(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic al registrar rutas en ServeMux: %v", r)
		}
	}()

	dummy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	mux := http.NewServeMux()

	registerAdminRoutes(mux, adminRouteDeps{
		// authMW nil-safe: Authenticate/RequirePermission solo construyen
		// closures en wrap-time; no dereferencian el receptor hasta ServeHTTP,
		// que este test nunca dispara.
		authMW:  httpapi.NewMiddleware(nil, nil),
		auditor: nil,
		log:     quietLogger(),

		health:         dummy,
		metrics:        dummy,
		revokeLease:    dummy,
		sendMessage:    dummy,
		cryptoRekey:    dummy,
		flowsCreate:    dummy,
		flowsStart:     dummy,
		triggersCreate: dummy,
		triggersList:   dummy,
		triggersDelete: dummy,
		sessionProfile: dummy,
		sessionStatus:  dummy,
		revokeTenant:   dummy,
		restoreTenant:  dummy,

		// Las rutas de plataforma construyen su handler INLINE dentro de
		// registerAdminRoutes (ver adminRouteDeps); los constructores
		// platformadmin.*Handler no tocan estas piezas en wrap-time, así que
		// nil es un valor válido aquí (nunca se ejercita el handler).
		platformRepo:     nil,
		platformTenantID: "",
		codeStore:        nil,
		m2mClient:        nil,
	})
}
