package intake

import "testing"

// TestReanalysisRequest_Valid: una petición abre un job solo con las TRES cosas: la clave de
// ventana entera, la solicitud a la que se cuelga y un contexto pedido por el DUEÑO. La vía, el
// material y la revisión de origen no deciden nada aquí: este paquete no los interpreta. Tampoco
// el sobre con el que nace el job (SourceText): su regla —completo o vacío entero— es de
// OpenReanalysis, con otro texto, y Valid no la mira ni para bien ni para mal.
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
		{"complete with a full envelope", func(r *ReanalysisRequest) {
			r.SourceText = SourceText{Enc: []byte("e"), DEK: []byte("d"), KEKID: "k"}
		}, true},
		{"complete with a half envelope: not its rule", func(r *ReanalysisRequest) { r.SourceText = SourceText{Enc: []byte("e")} }, true},
		{"a full envelope does not make up for a missing intake", func(r *ReanalysisRequest) {
			r.IntakeID = ""
			r.SourceText = SourceText{Enc: []byte("e"), DEK: []byte("d"), KEKID: "k"}
		}, false},
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
