//go:build pendiente

package intakes

import (
	"context"
	"errors"
	"testing"
)

// Lo que el re-análisis necesita de la bandeja: la foto y el empuje por id. Los dobles y la
// siembra están en service_test.go.

// TestReanalysisTarget_ZeroValueIsALegacyIntakeWithoutRevisions: el cero del tipo es lo que el
// contrato llama una solicitud legada (sin evento) y sin revisiones; no hace falta un centinela.
func TestReanalysisTarget_ZeroValueIsALegacyIntakeWithoutRevisions(t *testing.T) {
	t.Parallel()
	var target ReanalysisTarget
	if target.EventID != "" || target.LastRevisionNo != 0 {
		t.Errorf("el cero de ReanalysisTarget = %+v, quería sin evento y con LastRevisionNo 0", target)
	}
	target = ReanalysisTarget{SessionID: "sess-a", ContactID: "contacto-opaco-1", EventID: "evento-1", Status: StatusNeedsInfo, LastRevisionNo: 3}
	if target.SessionID != "sess-a" || target.ContactID != "contacto-opaco-1" || target.Status != StatusNeedsInfo {
		t.Errorf("ReanalysisTarget no conserva sus campos: %+v", target)
	}
}

// TestPushRevisionByID_PushesTheReadDetailWithTheExplicitNumber: empuja el detalle LEÍDO con el
// número RECIBIDO (R-12), aunque no sea el de la última revisión, y no mueve la solicitud de
// estado (INV-10).
func TestPushRevisionByID_PushesTheReadDetailWithTheExplicitNumber(t *testing.T) {
	t.Parallel()
	st := svcSeedStore(t, StatusNeedsInfo)
	svcSeedRevision(t, st, RevisionKindInterpreted, svcDraftPayload)
	svcSeedRevision(t, st, RevisionKindInterpreted, svcDraftPayload)
	crm := &svcCRMSpy{}

	if err := NewService(st, WithCRMPusher(crm)).PushRevisionByID(context.Background(), svcTenantA, svcIntakeID, 1); err != nil {
		t.Fatalf("PushRevisionByID devolvió el error %v", err)
	}
	if len(crm.calls) != 1 {
		t.Fatalf("empujes = %d, quería 1", len(crm.calls))
	}
	push := crm.calls[0]
	if push.revisionNo != 1 {
		t.Errorf("revision_no empujado = %d, quería el recibido (1), no el de la última revisión", push.revisionNo)
	}
	if push.tenantID != svcTenantA || push.detail.ID != svcIntakeID || push.detail.Status != StatusNeedsInfo ||
		len(push.detail.Items) != 2 || len(push.detail.Revisions) != 2 {
		t.Errorf("detalle empujado = (%q, %q, %q, %d líneas, %d revisiones), quería el leído del store",
			push.tenantID, push.detail.ID, push.detail.Status, len(push.detail.Items), len(push.detail.Revisions))
	}
	if after := svcStatusOf(t, st); after != StatusNeedsInfo {
		t.Errorf("estado %q, quería needs_info: el re-análisis no transiciona", after)
	}
	if got := len(st.Revisions(svcIntakeID)); got != 2 {
		t.Errorf("revisiones = %d, quería 2: el empuje no escribe", got)
	}
}

// TestPushRevisionByID_IsNilSafe: sobre un *Service nil o sin puente cableado devuelve nil SIN
// leer — el store de la segunda fila fallaría cualquier lectura.
func TestPushRevisionByID_IsNilSafe(t *testing.T) {
	t.Parallel()
	var none *Service
	if err := none.PushRevisionByID(context.Background(), svcTenantA, svcIntakeID, 1); err != nil {
		t.Errorf("sobre un *Service nil devolvió %v, quería nil", err)
	}
	unwired := NewService(svcFailingStore{err: errors.New("base caída")})
	if err := unwired.PushRevisionByID(context.Background(), svcTenantA, svcIntakeID, 1); err != nil {
		t.Errorf("sin CRMPusher devolvió %v, quería nil sin leer la solicitud", err)
	}
}

// TestPushRevisionByID_ReadFailureIsReturnedAndNothingIsPushed: el fallo de lectura se devuelve
// envuelto con el id, para que quien llama pueda decir en su log qué revisión no se empujó.
func TestPushRevisionByID_ReadFailureIsReturnedAndNothingIsPushed(t *testing.T) {
	t.Parallel()
	down := errors.New("base caída")
	cases := []struct {
		name     string
		store    func(t *testing.T) Store
		tenantID string
		cause    error
		want     string
	}{
		{
			name:     "store failure",
			store:    func(*testing.T) Store { return svcFailingStore{err: down} },
			tenantID: svcTenantA, cause: down,
			want: "intakes: leer la solicitud 11111111-1111-1111-1111-111111111111 para empujarla al CRM: base caída",
		},
		{
			name:     "foreign intake",
			store:    func(t *testing.T) Store { return svcSeedStore(t, StatusOpen) },
			tenantID: svcTenantB, cause: ErrNotFound,
			want: "intakes: leer la solicitud 11111111-1111-1111-1111-111111111111 para empujarla al CRM: solicitud no encontrada",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			crm := &svcCRMSpy{}
			err := NewService(c.store(t), WithCRMPusher(crm)).PushRevisionByID(context.Background(), c.tenantID, svcIntakeID, 3)
			if err == nil || err.Error() != c.want {
				t.Fatalf("PushRevisionByID devolvió %v, quería %q", err, c.want)
			}
			if !errors.Is(err, c.cause) {
				t.Errorf("el error no envuelve la causa %v", c.cause)
			}
			if len(crm.calls) != 0 {
				t.Errorf("empujes = %d, quería 0: no se empuja lo que no se pudo leer", len(crm.calls))
			}
		})
	}
}

// TestPushRevisionByID_ReadsThroughGet: la lectura es Service.Get, así que con puente cableado
// cuenta como un TOQUE de los recordatorios perezosos, y sin puente no toca a nadie. Es la
// conducta del paquete viejo, fijada para que cambiarla sea una decisión y no un accidente.
func TestPushRevisionByID_ReadsThroughGet(t *testing.T) {
	t.Parallel()
	deposit, expiry := &svcDepositSpy{}, &svcExpirySpy{}
	st := svcSeedStore(t, StatusDepositRequested)
	wired := NewService(st, WithCRMPusher(&svcCRMSpy{}), WithDepositReminder(deposit), WithExpiryReminder(expiry))
	if err := wired.PushRevisionByID(context.Background(), svcTenantA, svcIntakeID, 1); err != nil {
		t.Fatalf("PushRevisionByID devolvió el error %v", err)
	}
	if len(deposit.calls) != 1 || len(expiry.calls) != 1 {
		t.Errorf("toques = (seña %d, plazo %d), quería (1, 1)", len(deposit.calls), len(expiry.calls))
	}

	unwired := NewService(st, WithDepositReminder(deposit), WithExpiryReminder(expiry))
	if err := unwired.PushRevisionByID(context.Background(), svcTenantA, svcIntakeID, 1); err != nil {
		t.Fatalf("PushRevisionByID sin puente devolvió el error %v", err)
	}
	if len(deposit.calls) != 1 || len(expiry.calls) != 1 {
		t.Errorf("toques = (seña %d, plazo %d), quería que siguieran en (1, 1): sin puente no se lee", len(deposit.calls), len(expiry.calls))
	}
}
