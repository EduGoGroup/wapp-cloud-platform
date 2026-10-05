package fleethelpertest

// Los casos del estado del LINK CloudLink: MarkOnline, MarkOffline, MarkLoggedOut, SetState,
// Get y List. El estado es derivado del stream; lo único que un admin puede fijar a mano es
// offline|loggedout, y siempre dentro de su tenant (INV-8).

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

// caseMarkOnlineNew: el alta deja la sesión online, con su identidad y con las dos marcas de
// tiempo fijadas.
func caseMarkOnlineNew(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	s := requireState(t, m, tenant, edge, session, fleet.StateOnline)
	if s.LastConnectedAt.IsZero() {
		t.Error("MarkOnline dejó last_connected_at a cero")
	}
	if s.LastSeenAt.IsZero() {
		t.Error("MarkOnline dejó last_seen_at a cero")
	}
}

// caseOnlineOfflineOnline: el offline por red es recuperable. La caída conserva
// last_connected_at (es de la conexión, no de la caída) y la reconexión vuelve a online.
func caseOnlineOfflineOnline(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	online := mustGet(t, m, tenant, edge, session)

	markOffline(t, m, tenant, edge, session)
	offline := requireState(t, m, tenant, edge, session, fleet.StateOffline)
	if !offline.LastConnectedAt.Equal(online.LastConnectedAt) {
		t.Errorf("MarkOffline movió last_connected_at de %v a %v", online.LastConnectedAt, offline.LastConnectedAt)
	}
	if offline.LastSeenAt.IsZero() {
		t.Error("MarkOffline dejó last_seen_at a cero")
	}

	markOnline(t, m, tenant, edge, session)
	requireState(t, m, tenant, edge, session, fleet.StateOnline)
}

// caseMarkOfflineUnknown: marcar offline una sesión que nunca se registró no es un error. La
// suite no afirma si la fila nace o no (ver ContratoRepository).
func caseMarkOfflineUnknown(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	markOffline(t, m, tenant, unique("edge"), unique("session"))
}

// caseMarkLoggedOut: el zombie (WhatsApp cerró el device) es un estado propio, no el offline
// por red, y una reconexión lo saca de ahí.
func caseMarkLoggedOut(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	markLoggedOut(t, m, tenant, edge, session)
	s := requireState(t, m, tenant, edge, session, fleet.StateLoggedOut)
	if s.State == fleet.StateOffline {
		t.Error("MarkLoggedOut dejó la sesión offline: el zombie no es el offline por red")
	}
	markOnline(t, m, tenant, edge, session)
	requireState(t, m, tenant, edge, session, fleet.StateOnline)
}

// caseMarkLoggedOutUnknown: como MarkOffline, sobre una sesión desconocida no es un error.
func caseMarkLoggedOutUnknown(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	markLoggedOut(t, m, tenant, unique("edge"), unique("session"))
}

// caseRowsKeyedByEdge: la clave es (tenant, edge, session). Las escrituras por identidad
// completa (MarkOffline, MarkLoggedOut) tocan UNA fila: la misma sesión bajo otro Edge sigue
// como estaba.
func caseRowsKeyedByEdge(t *testing.T, m Montaje) {
	tenant, session := seedTenant(t, m), unique("session")
	edgeOne, edgeTwo, edgeThree := unique("edge"), unique("edge"), unique("edge")
	for _, edge := range []string{edgeOne, edgeTwo, edgeThree} {
		markOnline(t, m, tenant, edge, session)
	}
	markOffline(t, m, tenant, edgeOne, session)
	markLoggedOut(t, m, tenant, edgeTwo, session)

	requireState(t, m, tenant, edgeOne, session, fleet.StateOffline)
	requireState(t, m, tenant, edgeTwo, session, fleet.StateLoggedOut)
	requireState(t, m, tenant, edgeThree, session, fleet.StateOnline)
}

// caseSetStateInvalid: un admin solo puede fijar offline|loggedout. Online (derivado del
// stream, no se falsea), el vacío y cualquier otro texto dan ErrInvalidState, found=false, y
// la fila no cambia.
func caseSetStateInvalid(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	before := mustGet(t, m, tenant, edge, session)
	for _, state := range []fleet.State{fleet.StateOnline, "", "banned", "OFFLINE"} {
		found, err := m.Repository.SetState(context.Background(), tenant, session, state)
		if !errors.Is(err, fleet.ErrInvalidState) {
			t.Errorf("SetState(%q): err = %v, quería ErrInvalidState", state, err)
		}
		if found {
			t.Errorf("SetState(%q): found = true con un estado inválido", state)
		}
	}
	after := requireState(t, m, tenant, edge, session, fleet.StateOnline)
	if !after.LastSeenAt.Equal(before.LastSeenAt) {
		t.Errorf("un SetState rechazado movió last_seen_at de %v a %v", before.LastSeenAt, after.LastSeenAt)
	}
}

// caseSetStateUnknown: una sesión que no existe da found=false sin error, y no nace.
func caseSetStateUnknown(t *testing.T, m Montaje) {
	tenant, session := seedTenant(t, m), unique("session")
	found, err := m.Repository.SetState(context.Background(), tenant, session, fleet.StateOffline)
	if err != nil || found {
		t.Fatalf("SetState de una sesión desconocida = (found=%v, err=%v), quería (false, nil)", found, err)
	}
	sessions, err := m.Repository.List(context.Background(), tenant)
	if err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("SetState de una sesión desconocida creó filas: %+v", sessions)
	}
}

// caseSetStateAllRows: SetState acota por tenant + session, así que toca TODAS las filas de
// esa sesión (una por Edge) y ninguna de otra sesión. Vale para los dos estados admitidos.
func caseSetStateAllRows(t *testing.T, m Montaje) {
	tenant, session, other := seedTenant(t, m), unique("session"), unique("session")
	edgeOne, edgeTwo := unique("edge"), unique("edge")
	markOnline(t, m, tenant, edgeOne, session)
	markOnline(t, m, tenant, edgeTwo, session)
	markOnline(t, m, tenant, edgeOne, other)

	for _, state := range []fleet.State{fleet.StateLoggedOut, fleet.StateOffline} {
		setState(t, m, tenant, session, state)
		requireState(t, m, tenant, edgeOne, session, state)
		requireState(t, m, tenant, edgeTwo, session, state)
		requireState(t, m, tenant, edgeOne, other, fleet.StateOnline)
	}
}

// caseSetStateOtherTenant: la sesión de otro tenant no se encuentra (404 opaco) ni se toca.
func caseSetStateOtherTenant(t *testing.T, m Montaje) {
	owner, stranger := seedTwoTenants(t, m)
	edge, session := unique("edge"), unique("session")
	markOnline(t, m, owner, edge, session)

	found, err := m.Repository.SetState(context.Background(), stranger, session, fleet.StateLoggedOut)
	if err != nil || found {
		t.Fatalf("SetState desde otro tenant = (found=%v, err=%v), quería (false, nil)", found, err)
	}
	requireState(t, m, owner, edge, session, fleet.StateOnline)
}

// caseGetUnknown: una sesión que no existe es (Session cero, false, nil). También lo es la de
// un tenant pedida con la identidad de otro.
func caseGetUnknown(t *testing.T, m Montaje) {
	owner, stranger := seedTwoTenants(t, m)
	edge, session := unique("edge"), unique("session")
	markOnline(t, m, owner, edge, session)

	lookups := []struct{ what, tenant, edge, session string }{
		{"otra sesión", owner, edge, unique("session")},
		{"otro Edge", owner, unique("edge"), session},
		{"otro tenant", stranger, edge, session},
	}
	for _, l := range lookups {
		s, found, err := m.Repository.Get(context.Background(), l.tenant, l.edge, l.session)
		if err != nil {
			t.Fatalf("Get con %s: error inesperado %v", l.what, err)
		}
		if found {
			t.Errorf("Get con %s: found = true, quería false", l.what)
		}
		if !reflect.DeepEqual(s, fleet.Session{}) {
			t.Errorf("Get con %s devolvió %+v, quería la Session cero", l.what, s)
		}
	}
}

// caseListByTenant: List devuelve todas las sesiones del tenant pedido y ninguna de otro. No
// se afirma el orden.
func caseListByTenant(t *testing.T, m Montaje) {
	owner, stranger := seedTwoTenants(t, m)
	edge := unique("edge")
	want := map[string]bool{unique("session"): true, unique("session"): true}
	for session := range want {
		markOnline(t, m, owner, edge, session)
	}
	markOnline(t, m, stranger, edge, unique("session"))

	got, err := m.Repository.List(context.Background(), owner)
	if err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("List devolvió %d sesiones, quería %d: %+v", len(got), len(want), got)
	}
	for _, s := range got {
		if s.TenantID != owner || s.EdgeID != edge || !want[s.SessionID] {
			t.Errorf("List devolvió una fila que no es del tenant pedido: (%s, %s, %s)", s.TenantID, s.EdgeID, s.SessionID)
		}
		if s.State != fleet.StateOnline {
			t.Errorf("List: la sesión %s salió con state %q, quería online", s.SessionID, s.State)
		}
	}

	empty, err := m.Repository.List(context.Background(), seedTenant(t, m))
	if err != nil {
		t.Fatalf("List de un tenant sin sesiones: error inesperado %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("List de un tenant sin sesiones devolvió %+v", empty)
	}
}

// caseSameIDsInTwoTenants: el mismo edge_id y session_id bajo dos tenants son dos filas. Lo
// que se le hace a una —por identidad completa o por tenant + session— no toca la otra.
func caseSameIDsInTwoTenants(t *testing.T, m Montaje) {
	first, second := seedTwoTenants(t, m)
	edge, session := unique("edge"), unique("session")
	markOnline(t, m, first, edge, session)
	markOnline(t, m, second, edge, session)

	markOffline(t, m, first, edge, session)
	requireState(t, m, first, edge, session, fleet.StateOffline)
	requireState(t, m, second, edge, session, fleet.StateOnline)

	setState(t, m, second, session, fleet.StateLoggedOut)
	requireState(t, m, first, edge, session, fleet.StateOffline)
	requireState(t, m, second, edge, session, fleet.StateLoggedOut)
}
