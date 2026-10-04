//go:build pendiente

package platformadmin_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
)

// Este test es EXTERNO (package platformadmin_test): usa el doble de platformadminhelpertest,
// que importa platformadmin, y un test interno que lo importara daría un ciclo.

// Los seis centinelas de la bandeja conservan su texto, byte a byte (diseño F2 §5): el de
// ErrSystemsSyncFailed viaja tal cual en el cuerpo del 502 (ApprovePartialResult.Reason).
func TestAccessRequestSentinels_LiteralTexts(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"ErrPlatformSystemForbidden", platformadmin.ErrPlatformSystemForbidden,
			"platformadmin: wapp.platform no se concede desde la bandeja de solicitudes de acceso"},
		{"ErrSystemsUnionUnavailable", platformadmin.ErrSystemsUnionUnavailable,
			"platformadmin: no se puede unir con los systems actuales del usuario en identity (sin lectura)"},
		{"ErrIdentityM2MUnavailable", platformadmin.ErrIdentityM2MUnavailable,
			"platformadmin: no hay cliente M2M configurado hacia identity; no se pudieron conceder los systems solicitados"},
		{"ErrRetryRoleMismatch", platformadmin.ErrRetryRoleMismatch,
			"platformadmin: el reintento pide un rol distinto del ya aprobado la primera vez; no converge"},
		{"ErrTenantNotFound", platformadmin.ErrTenantNotFound,
			"platformadmin: el tenant_id de la aprobación no existe"},
		{"ErrSystemsSyncFailed", platformadmin.ErrSystemsSyncFailed,
			"platformadmin: fallo al sincronizar systems en identity tras aprobar localmente"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.err.Error() != c.want {
				t.Fatalf("%s = %q, quiero %q", c.name, c.err.Error(), c.want)
			}
		})
	}
}

// Los tags JSON de los DTO de la bandeja son contrato con la consola de plataforma.
func TestAccessRequestDTOs_JSONShape(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	item := platformadmin.AccessRequestItem{
		ID: "r1", UserID: "u1", Email: "ana@x.com", Origin: "bff", Status: "pending", CreatedAt: at,
		Systems: []string{}, SystemsKnown: false,
	}
	const itemJSON = `{"id":"r1","user_id":"u1","email":"ana@x.com","origin":"bff","status":"pending",` +
		`"created_at":"2026-10-04T12:00:00Z","systems":[],"systems_known":false}`
	for _, c := range []struct {
		name string
		v    any
		want string
	}{
		{"AccessRequestItem", item, itemJSON},
		{"ListAccessRequestsResponse", platformadmin.ListAccessRequestsResponse{Items: []platformadmin.AccessRequestItem{item}},
			`{"items":[` + itemJSON + `]}`},
		{"ApprovePartialResult", platformadmin.ApprovePartialResult{Local: "ok", Identity: "failed", Reason: "r"},
			`{"local":"ok","identity":"failed","reason":"r"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := json.Marshal(c.v)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(got) != c.want {
				t.Fatalf("JSON = %s\nquiero  %s", got, c.want)
			}
		})
	}
}

// Los cuerpos de aprobar y rechazar se leen con los nombres de campo de la consola.
func TestAccessRequestBodies_JSONFieldNames(t *testing.T) {
	var approve platformadmin.ApproveAccessRequestRequest
	if err := json.Unmarshal([]byte(`{"tenant_id":"t1","role":"operator","systems":["wapp.bff"]}`), &approve); err != nil {
		t.Fatalf("json.Unmarshal(approve): %v", err)
	}
	if approve.TenantID != "t1" || approve.Role != "operator" || len(approve.Systems) != 1 || approve.Systems[0] != "wapp.bff" {
		t.Fatalf("ApproveAccessRequestRequest = %+v", approve)
	}
	var reject platformadmin.RejectAccessRequestRequest
	if err := json.Unmarshal([]byte(`{"reason":"duplicada"}`), &reject); err != nil {
		t.Fatalf("json.Unmarshal(reject): %v", err)
	}
	if reject.Reason != "duplicada" {
		t.Fatalf("RejectAccessRequestRequest = %+v", reject)
	}
}
