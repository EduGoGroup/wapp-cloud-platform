package storehelpertest

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// Las comparaciones de la marca de estado: cada una mira TODOS los campos de su fila. Los JSON
// (vars, definiciones, payloads, blobs) se comparan por contenido y los instantes con Equal.

// toJSON serializa v; si no puede, falla el test (los valores los construye la suite).
func toJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("serializar %T: %v", v, err)
	}
	return raw
}

// sameJSON dice si dos documentos tienen el mismo contenido, sin mirar el orden de sus claves ni
// sus espacios.
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// sameMap dice si dos mapas de JSON tienen el mismo contenido. Un mapa nil y uno vacío son el
// mismo: Postgres materializa el nil como `{}`.
func sameMap(t *testing.T, a, b map[string]any) bool {
	t.Helper()
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return sameJSON(toJSON(t, a), toJSON(t, b))
}

// requireSameConversation compara las once columnas de una conversación.
func requireSameConversation(t *testing.T, what string, got, want model.Conversation) {
	t.Helper()
	if got.TenantID != want.TenantID || got.SessionID != want.SessionID || got.ContactID != want.ContactID {
		t.Errorf("%s: clave = (%q, %q, %q), quería (%q, %q, %q)", what,
			got.TenantID, got.SessionID, got.ContactID, want.TenantID, want.SessionID, want.ContactID)
	}
	if got.FlowID != want.FlowID || got.FlowVersion != want.FlowVersion || got.CurrentNode != want.CurrentNode {
		t.Errorf("%s: (flujo, versión, nodo) = (%q, %d, %q), quería (%q, %d, %q)", what,
			got.FlowID, got.FlowVersion, got.CurrentNode, want.FlowID, want.FlowVersion, want.CurrentNode)
	}
	if got.LastWaMessageID != want.LastWaMessageID {
		t.Errorf("%s: LastWaMessageID = %q, quería %q", what, got.LastWaMessageID, want.LastWaMessageID)
	}
	if got.EventID != want.EventID || got.OwnerEventID != want.OwnerEventID {
		t.Errorf("%s: (EventID, OwnerEventID) = (%q, %q), quería (%q, %q)", what,
			got.EventID, got.OwnerEventID, want.EventID, want.OwnerEventID)
	}
	if !sameMap(t, got.Vars, want.Vars) {
		t.Errorf("%s: Vars = %v, quería %v", what, got.Vars, want.Vars)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("%s: UpdatedAt = %v, quería %v", what, got.UpdatedAt, want.UpdatedAt)
	}
}

// requireSameFlow compara dos definiciones por su contenido serializado, versión incluida.
func requireSameFlow(t *testing.T, what string, got, want model.Flow) {
	t.Helper()
	g, err := model.MarshalDefinition(got)
	if err != nil {
		t.Fatalf("%s: serializar la definición leída: %v", what, err)
	}
	w, err := model.MarshalDefinition(want)
	if err != nil {
		t.Fatalf("%s: serializar la definición esperada: %v", what, err)
	}
	if !sameJSON(g, w) {
		t.Errorf("%s: definición = %s, quería %s", what, g, w)
	}
}

// requireSameFlowEvents compara los efectos uno a uno, en orden, con sus siete campos.
func requireSameFlowEvents(t *testing.T, what string, got, want []FlowEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d efectos, quería %d: %+v", what, len(got), len(want), got)
		return
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.TenantID != w.TenantID || g.ContactID != w.ContactID || g.FlowID != w.FlowID ||
			g.FlowVersion != w.FlowVersion || g.Kind != w.Kind || g.Name != w.Name {
			t.Errorf("%s: efecto %d = %+v, quería %+v", what, i, g, w)
		}
		if !sameMap(t, g.Payload, w.Payload) {
			t.Errorf("%s: el payload del efecto %d = %v, quería %v", what, i, g.Payload, w.Payload)
		}
	}
}

// requireSameSurveyResults compara las respuestas una a una, en orden, con sus ocho campos.
func requireSameSurveyResults(t *testing.T, what string, got, want []SurveyResult) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d respuestas, quería %d: %+v", what, len(got), len(want), got)
		return
	}
	for i := range want {
		g, w := got[i], want[i]
		if !sameAnswer(g, w) || g.EventID != w.EventID {
			t.Errorf("%s: respuesta %d = %+v, quería %+v", what, i, g, w)
		}
		if !g.CreatedAt.Equal(w.CreatedAt) {
			t.Errorf("%s: el CreatedAt de la respuesta %d = %v, quería %v", what, i, g.CreatedAt, w.CreatedAt)
		}
	}
}

// sameAnswer compara lo que una respuesta dice sin su evento ni su fecha; quien quiera esos dos
// los mira aparte.
func sameAnswer(a, b SurveyResult) bool {
	return a.TenantID == b.TenantID && a.ContactID == b.ContactID && a.FlowID == b.FlowID &&
		a.FlowVersion == b.FlowVersion && a.QuestionID == b.QuestionID && a.AnswerCode == b.AnswerCode
}

// requireSameContent compara un blob vigente: su ref, sus dos marcas, su contenido y sus versiones.
func requireSameContent(t *testing.T, what string, got, want contentMark) {
	t.Helper()
	if got.summary.Ref != want.summary.Ref {
		t.Errorf("%s: ref = %q, quería %q", what, got.summary.Ref, want.summary.Ref)
	}
	if !got.summary.CreatedAt.Equal(want.summary.CreatedAt) || !got.summary.UpdatedAt.Equal(want.summary.UpdatedAt) {
		t.Errorf("%s: (CreatedAt, UpdatedAt) = (%v, %v), quería (%v, %v)", what,
			got.summary.CreatedAt, got.summary.UpdatedAt, want.summary.CreatedAt, want.summary.UpdatedAt)
	}
	if !sameJSON(got.blob, want.blob) {
		t.Errorf("%s: blob = %s, quería %s", what, got.blob, want.blob)
	}
	requireSameVersions(t, what, got.versions, want.versions)
}

// requireSameVersions compara las versiones archivadas una a una, en orden, con sus cuatro campos.
func requireSameVersions(t *testing.T, what string, got, want []ContentVersion) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d versiones archivadas, quería %d: %+v", what, len(got), len(want), got)
		return
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Version != w.Version || g.Source != w.Source || !sameJSON(g.Content, w.Content) {
			t.Errorf("%s: versión archivada %d = (nº %d, %s, %s), quería (nº %d, %s, %s)", what, i,
				g.Version, g.Source, g.Content, w.Version, w.Source, w.Content)
		}
		if !g.CreatedAt.Equal(w.CreatedAt) {
			t.Errorf("%s: el CreatedAt de la versión archivada %d = %v, quería %v", what, i, g.CreatedAt, w.CreatedAt)
		}
	}
}

// requireSameIntake compara los once campos de la cabecera de una solicitud.
func requireSameIntake(t *testing.T, what string, got, want Intake) {
	t.Helper()
	if got.ID != want.ID || got.TenantID != want.TenantID || got.ContactID != want.ContactID || got.SessionID != want.SessionID {
		t.Errorf("%s: (id, tenant, contacto, sesión) = (%q, %q, %q, %q), quería (%q, %q, %q, %q)", what,
			got.ID, got.TenantID, got.ContactID, got.SessionID, want.ID, want.TenantID, want.ContactID, want.SessionID)
	}
	if got.Status != want.Status || got.Total != want.Total {
		t.Errorf("%s: (Status, Total) = (%q, %v), quería (%q, %v)", what, got.Status, got.Total, want.Status, want.Total)
	}
	if got.EventID != want.EventID {
		t.Errorf("%s: EventID = %q, quería %q", what, got.EventID, want.EventID)
	}
	if got.CustomerNote != want.CustomerNote {
		t.Errorf("%s: CustomerNote = %q, quería %q", what, got.CustomerNote, want.CustomerNote)
	}
	for _, f := range []struct {
		name      string
		got, want time.Time
	}{
		{"CreatedAt", got.CreatedAt, want.CreatedAt},
		{"UpdatedAt", got.UpdatedAt, want.UpdatedAt},
		{"ExpiresAt", got.ExpiresAt, want.ExpiresAt},
	} {
		if !f.got.Equal(f.want) {
			t.Errorf("%s: %s = %v, quería %v", what, f.name, f.got, f.want)
		}
	}
}

// requireSameItems compara las líneas una a una, en orden, con sus siete campos.
func requireSameItems(t *testing.T, what string, got, want []IntakeItem) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d líneas, quería %d: %+v", what, len(got), len(want), got)
		return
	}
	for i := range want {
		g, w := got[i], want[i]
		if !sameLine(g, w) {
			t.Errorf("%s: línea %d = %+v, quería %+v", what, i, g, w)
		}
		if !g.AddedAt.Equal(w.AddedAt) {
			t.Errorf("%s: el AddedAt de la línea %d (%s) = %v, quería %v", what, i, w.SKU, g.AddedAt, w.AddedAt)
		}
	}
}

// sameLine compara lo que una línea dice sin su fecha: sus seis campos de negocio.
func sameLine(a, b IntakeItem) bool {
	return a.IntakeID == b.IntakeID && a.SKU == b.SKU && a.Label == b.Label &&
		a.Customization == b.Customization && a.Qty == b.Qty && a.UnitPrice == b.UnitPrice
}

// sameSettings compara las diez claves de la configuración. Un checklist nil y uno vacío son el
// mismo.
func sameSettings(a, b Settings) bool {
	if len(a.BuyerFields) != len(b.BuyerFields) {
		return false
	}
	for i := range a.BuyerFields {
		if a.BuyerFields[i] != b.BuyerFields[i] {
			return false
		}
	}
	return a.TenantID == b.TenantID && a.PageSize == b.PageSize && a.OrderTTL == b.OrderTTL &&
		a.ConversationTTL == b.ConversationTTL && a.EventInactivityTTL == b.EventInactivityTTL &&
		a.EventHistoryTTL == b.EventHistoryTTL && a.AggregationWindow == b.AggregationWindow &&
		a.AggregationMax == b.AggregationMax && a.WelcomeText == b.WelcomeText &&
		a.WelcomeSilence == b.WelcomeSilence
}

// requireSameWelcome compara los dos instantes de una bienvenida.
func requireSameWelcome(t *testing.T, what string, got, want WelcomeMark) {
	t.Helper()
	if !got.LastIncomingAt.Equal(want.LastIncomingAt) || !got.WelcomedAt.Equal(want.WelcomedAt) {
		t.Errorf("%s: (LastIncomingAt, WelcomedAt) = (%v, %v), quería (%v, %v)", what,
			got.LastIncomingAt, got.WelcomedAt, want.LastIncomingAt, want.WelcomedAt)
	}
}

// requireStamped afirma que un instante que puso la implementación cae entre dos lecturas de Now:
// la de antes de la operación y la de después.
func requireStamped(t *testing.T, what string, got, notBefore, notAfter time.Time) {
	t.Helper()
	if got.Before(notBefore) || got.After(notAfter) {
		t.Errorf("%s = %v; quería un instante del reloj de la implementación, entre %v y %v", what, got, notBefore, notAfter)
	}
}
