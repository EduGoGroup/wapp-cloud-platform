//go:build pendiente

package intake

import "testing"

// TestReanalysisRequest_Valid: una petición abre un job solo con las TRES cosas: la clave de
// ventana entera, la solicitud a la que se cuelga y un contexto pedido por el DUEÑO. La vía, el
// material y la revisión de origen no deciden nada aquí: este paquete no los interpreta.
func TestReanalysisRequest_Valid(t *testing.T) {
	full := ReanalysisRequest{
		Key:      WindowKey{TenantID: "t", SessionID: "s", ContactID: "c", EventID: "e"},
		IntakeID: "intake-1",
		Context:  Reanalysis{RequestedBy: RequestedByOwner, Via: "api", Source: "pasted_text", From: 3},
	}
	cases := []struct {
		name   string
		mutate func(*ReanalysisRequest)
		want   bool
	}{
		{"complete", func(*ReanalysisRequest) {}, true},
		{"only the three required pieces", func(r *ReanalysisRequest) { r.Context = Reanalysis{RequestedBy: RequestedByOwner} }, true},
		{"incomplete window key", func(r *ReanalysisRequest) { r.Key.EventID = "" }, false},
		{"no tenant", func(r *ReanalysisRequest) { r.Key.TenantID = "" }, false},
		{"no intake", func(r *ReanalysisRequest) { r.IntakeID = "" }, false},
		{"nobody asked", func(r *ReanalysisRequest) { r.Context.RequestedBy = "" }, false},
		{"another role", func(r *ReanalysisRequest) { r.Context.RequestedBy = "system" }, false},
		{"zero value", func(r *ReanalysisRequest) { *r = ReanalysisRequest{} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := full
			tc.mutate(&r)
			if got := r.Valid(); got != tc.want {
				t.Errorf("Valid() = %v, quería %v (%+v)", got, tc.want, r)
			}
		})
	}
}
