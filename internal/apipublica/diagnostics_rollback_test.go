package apipublica_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// TestMountDiagnostics_Request_RollbackSurvivesTheClientLeaving fija D-F3-11: el rollback de D5
// NO va con el contexto de la petición. Si el cliente se va mientras se empuja al Edge —que es
// justo cuando el empuje suele fallar: un Edge que no lee agota la paciencia del cliente—, el
// contexto de la petición llega cancelado al rollback; con él, el DELETE moría en el acto y la
// solicitud se quedaba pendiente hasta su TTL (30 min por defecto), contestando «pending» a quien
// preguntara por un diagnóstico que nadie iba a entregar. El rollback va desenganchado de esa
// cancelación y con plazo propio, el de las consultas a BD (d.DBTimeout; <= 0 ⇒ 1,5 s).
//
// 🔴 Aquí la cara nueva SE APARTA de la vieja a propósito (como D-F3-9 y D-F3-10): la vieja
// conserva el defecto hasta F10.
func TestMountDiagnostics_Request_RollbackSurvivesTheClientLeaving(t *testing.T) {
	for name, tc := range map[string]struct{ wired, floor, ceil time.Duration }{
		"zero_falls_to_1500ms": {0, time.Second, 1500 * time.Millisecond},
		"wired_timeout":        {5 * time.Second, 4 * time.Second, 5 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store := newStore()
			var clientLeaves context.CancelFunc
			requester := &requesterSpy{
				err:    session.ErrSessionOffline,
				onCall: func() { clientLeaves() },
			}
			d := diagDeps(store, requester)
			d.DBTimeout = tc.wired
			cara := diagCara(h.Common(), d)
			// La petición viaja con un contexto que se cancela A MITAD del empuje.
			leaving := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				clientLeaves = cancel
				cara.ServeHTTP(w, r.WithContext(ctx))
			})

			rec := h.Call(leaving, h.With(tenantA, diagPerm), http.MethodPost, requestTarget, "")

			wantCode(t, name, rec, http.StatusBadGateway)
			if len(store.deleted) != 1 {
				t.Fatalf("DeleteRequest se llamó %d veces, quiero 1", len(store.deleted))
			}
			if store.deleteCtxErr != nil {
				t.Errorf("el rollback recibió un contexto ya terminado (%v): con una BD real no habría borrado nada", store.deleteCtxErr)
			}
			if got := store.remaining["delete"]; got <= tc.floor || got > tc.ceil {
				t.Errorf("al contexto del rollback le quedaban %s, quiero entre %s y %s (-1 = sin plazo)", got, tc.floor, tc.ceil)
			}
			if _, err := store.GetBundle(context.Background(), tenantA, requester.commandID); !errors.Is(err, diagnostics.ErrNotFound) {
				t.Errorf("tras el rollback la solicitud está en %v, quiero ErrNotFound", err)
			}
		})
	}
}
