//go:build pendiente

package apipublica_test

// intents_put_push_test.go — ROJO de D-F7-12 (hallazgo 43 (c) de F7): el push de E2 deja de ir
// con el contexto de la petición. Mientras dure el rojo convive con
// TestMountIntents_PutPushUsesTheRequestContext (intents_put_test.go), que fija la conducta
// heredada y sigue siendo verdad del código; el verde lo retira y trae este test a su sitio.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMountIntents_PutPushSurvivesTheRequestCancellation: ✎ divergencia con la cara vieja, a
// propósito (D-F7-12). El cliente cuelga con la config YA persistida y el push por hacer —el
// caso límite—: el push se hace igual, con un contexto que NO hereda esa cancelación, que
// conserva los valores de la petición (la Identity) y que lleva un plazo propio de 5 s. El
// Upsert sigue yendo con el contexto de la petición, sin plazo.
func TestMountIntents_PutPushSurvivesTheRequestCancellation(t *testing.T) {
	rig := newIntentsRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rig.store.afterUpsert = cancel // el cliente se fue: persistido, y el push aún por hacer
	req := httptest.NewRequest(http.MethodPut, intentsTarget, strings.NewReader(intentsValidBody)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+rig.h.With(tenantA, intentsWritePerm))
	rec := httptest.NewRecorder()
	rig.cara.ServeHTTP(rec, req)

	wantCode(t, "E2 con el cliente ido", rec, http.StatusOK)
	if ctx.Err() == nil {
		t.Fatal("la petición no llegó a cancelarse: el test no ejerce el caso que dice")
	}
	if rig.store.upserts != 1 || rig.store.upsertRemaining != -1 {
		t.Errorf("Upsert: %d llamadas, plazo restante %v; quiero 1 y sin plazo (va con el contexto de la petición)",
			rig.store.upserts, rig.store.upsertRemaining)
	}
	if rig.pusher.calls != 1 {
		t.Fatalf("PushConfig se llamó %d veces, quiero 1: que el cliente cuelgue no puede perder el push", rig.pusher.calls)
	}
	if rig.pusher.ctxErr != nil {
		t.Errorf("el push recibió un contexto MUERTO (%v): falta soltar la cancelación de la petición", rig.pusher.ctxErr)
	}
	if rig.pusher.remaining > 5*time.Second || rig.pusher.remaining < 4*time.Second {
		t.Errorf("al push le quedaba un plazo de %v (-1 = sin plazo); quiero uno propio de ~5 s", rig.pusher.remaining)
	}
	if rig.pusher.tenantInCtx != tenantA {
		t.Errorf("el contexto del push perdió los valores de la petición (tenant %q, quiero %q)", rig.pusher.tenantInCtx, tenantA)
	}
}
