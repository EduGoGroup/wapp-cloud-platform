//go:build pendiente

package intakes

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// SetStatus, su aviso al cliente, AbandonByEvent, el empuje al CRM y la línea de envío. Los
// dobles y la siembra están en service_test.go.

// svcRacingStore simula la carrera de dos operadores: entre la lectura que valida la transición
// y la escritura que la aplica, otro canceló la solicitud. El compare-and-swap REAL del store lo
// detecta.
type svcRacingStore struct {
	*MemoryStore
	raced bool
}

func (s *svcRacingStore) Get(ctx context.Context, tenantID, intakeID string) (Detail, error) {
	d, err := s.MemoryStore.Get(ctx, tenantID, intakeID)
	if err == nil && !s.raced {
		s.raced = true
		if _, uerr := s.MemoryStore.UpdateStatus(ctx, tenantID, intakeID, StatusCancelled, StoredVariants(d.Status)); uerr != nil {
			return Detail{}, uerr
		}
	}
	return d, err
}

// svcUnmovedStore es el store que «arregla» una transición devolviendo la solicitud en el estado
// en que estaba, sin error.
type svcUnmovedStore struct{ *MemoryStore }

func (s svcUnmovedStore) UpdateStatus(ctx context.Context, tenantID, intakeID, _ string, _ []string) (Intake, error) {
	d, err := s.MemoryStore.Get(ctx, tenantID, intakeID)
	return d.Intake, err
}

// TestService_SetStatus_AppliesAndPersists: la transición válida devuelve la solicitud ya
// transicionada y queda escrita.
func TestService_SetStatus_AppliesAndPersists(t *testing.T) {
	t.Parallel()
	st := svcSeedStore(t, StatusConfirmed)

	got, err := NewService(st).SetStatus(context.Background(), svcTenantA, svcIntakeID, StatusDepositRequested, NoticeToClient)
	if err != nil {
		t.Fatalf("SetStatus devolvió el error %v", err)
	}
	if got.ID != svcIntakeID || got.Status != StatusDepositRequested {
		t.Errorf("SetStatus devolvió (%q, %q), quería la sembrada en deposit_requested", got.ID, got.Status)
	}
	if after := svcStatusOf(t, st); after != StatusDepositRequested {
		t.Errorf("la transición no se persistió: estado %q", after)
	}
}

// TestService_SetStatus_LegacyClosed: el alias `closed` se normaliza por los DOS lados — como
// destino entra como `confirmed`, y una fila que quedó guardada como `closed` transiciona como
// `confirmed` sin migrar el dato (el compare-and-swap recibe todas sus variantes guardadas).
func TestService_SetStatus_LegacyClosed(t *testing.T) {
	t.Parallel()
	t.Run("as the target", func(t *testing.T) {
		t.Parallel()
		st := svcSeedStore(t, StatusPendingApproval)
		got, err := NewService(st).SetStatus(context.Background(), svcTenantA, svcIntakeID, StatusClosedLegacy, NoticeToClient)
		if err != nil || got.Status != StatusConfirmed {
			t.Fatalf("SetStatus(closed) devolvió (%q, %v), quería (confirmed, nil)", got.Status, err)
		}
	})
	t.Run("as the stored origin", func(t *testing.T) {
		t.Parallel()
		st := &svcTracedStore{MemoryStore: svcSeedStore(t, StatusClosedLegacy)}
		got, err := NewService(st).SetStatus(context.Background(), svcTenantA, svcIntakeID, StatusSettled, NoticeToClient)
		if err != nil || got.Status != StatusSettled {
			t.Fatalf("SetStatus sobre una fila `closed` devolvió (%q, %v), quería (settled, nil)", got.Status, err)
		}
		if len(st.expected) != 1 || !slices.Equal(st.expected[0], StoredVariants(StatusConfirmed)) ||
			!slices.Contains(st.expected[0], StatusClosedLegacy) || !slices.Contains(st.expected[0], StatusConfirmed) {
			t.Errorf("el compare-and-swap esperaba %v, quería las variantes guardadas de confirmed (con closed)", st.expected)
		}
	})
}

// TestService_SetStatus_NormalizesWhatItReads: el origen se normaliza AQUÍ, no se le supone al
// store. Con una lectura que trae `closed` crudo, la transición vale como desde `confirmed`, el
// aviso lleva el origen canónico y un rechazo lo reporta canónico.
func TestService_SetStatus_NormalizesWhatItReads(t *testing.T) {
	t.Parallel()
	spy := &svcNotifierSpy{}
	st := svcRawStatusStore{MemoryStore: svcSeedStore(t, StatusClosedLegacy), raw: StatusClosedLegacy}
	svc := NewService(st, WithNotifier(spy))

	_, err := svc.SetStatus(context.Background(), svcTenantA, svcIntakeID, StatusOpen, NoticeToClient)
	var rejected *TransitionError
	if !errors.As(err, &rejected) || rejected.From != StatusConfirmed {
		t.Fatalf("SetStatus devolvió %v, quería un *TransitionError desde confirmed", err)
	}

	got, err := svc.SetStatus(context.Background(), svcTenantA, svcIntakeID, StatusSettled, NoticeToClient)
	if err != nil || got.Status != StatusSettled {
		t.Fatalf("SetStatus devolvió (%q, %v), quería (settled, nil)", got.Status, err)
	}
	want := []svcNotice{{tenantID: svcTenantA, intakeID: svcIntakeID, from: StatusConfirmed, to: StatusSettled}}
	if !reflect.DeepEqual(spy.calls, want) {
		t.Errorf("avisos = %+v, quería %+v", spy.calls, want)
	}
}

// TestService_SetStatus_RejectedTransitions: destino inalcanzable, destino desconocido y el
// propio estado dan *TransitionError con el origen NORMALIZADO, el destino y adónde sí se puede
// ir. No se escribe nada.
func TestService_SetStatus_RejectedTransitions(t *testing.T) {
	t.Parallel()
	fromConfirmed := []string{"cancelled", "deposit_requested", "pending_approval", "settled"}
	cases := []struct {
		name        string
		stored      string
		to          string
		wantFrom    string
		wantTo      string
		wantAllowed []string
	}{
		{name: "unreachable target", stored: StatusClosedLegacy, to: StatusOpen, wantFrom: StatusConfirmed, wantTo: StatusOpen, wantAllowed: fromConfirmed},
		{name: "unknown target", stored: StatusConfirmed, to: "en_camino", wantFrom: StatusConfirmed, wantTo: "en_camino", wantAllowed: fromConfirmed},
		{name: "same state is not a transition", stored: StatusConfirmed, to: StatusConfirmed, wantFrom: StatusConfirmed, wantTo: StatusConfirmed, wantAllowed: fromConfirmed},
		{name: "legacy alias of the same state", stored: StatusConfirmed, to: StatusClosedLegacy, wantFrom: StatusConfirmed, wantTo: StatusConfirmed, wantAllowed: fromConfirmed},
		{
			name: "from the quote", stored: StatusPendingApproval, to: StatusSettled, wantFrom: StatusPendingApproval, wantTo: StatusSettled,
			wantAllowed: []string{"cancelled", "confirmed", "needs_info", "rejected"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			st := &svcTracedStore{MemoryStore: svcSeedStore(t, c.stored)}
			spy := &svcNotifierSpy{}

			_, err := NewService(st, WithNotifier(spy)).SetStatus(context.Background(), svcTenantA, svcIntakeID, c.to, NoticeToClient)
			var rejected *TransitionError
			if !errors.As(err, &rejected) {
				t.Fatalf("SetStatus devolvió %v, quería un *TransitionError", err)
			}
			if rejected.From != c.wantFrom || rejected.To != c.wantTo || !slices.Equal(rejected.Allowed, c.wantAllowed) {
				t.Errorf("TransitionError = %+v, quería from=%q to=%q allowed=%v", rejected, c.wantFrom, c.wantTo, c.wantAllowed)
			}
			if len(st.expected) != 0 {
				t.Errorf("una transición rechazada llegó a escribir (%d intentos)", len(st.expected))
			}
			if len(spy.calls) != 0 {
				t.Errorf("avisos = %d, quería 0: no hubo cambio que contar", len(spy.calls))
			}
		})
	}
}

// TestService_SetStatus_ResourceBeforeBody: una solicitud ajena responde ErrNotFound aunque el
// destino pedido sea absurdo — el código de error no revela que existe (INV-8).
func TestService_SetStatus_ResourceBeforeBody(t *testing.T) {
	t.Parallel()
	for _, to := range []string{StatusConfirmed, "en_camino", ""} {
		_, err := NewService(svcSeedStore(t, StatusOpen)).SetStatus(context.Background(), svcTenantB, svcIntakeID, to, NoticeToClient)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("SetStatus(%q) desde otro tenant devolvió %v, quería ErrNotFound", to, err)
		}
	}
}

// TestService_SetStatus_LostCompareAndSwap: de dos operadores, el que llega tarde se lleva
// ErrConflict, no pisa lo que escribió el otro y NO le manda un mensaje al cliente.
func TestService_SetStatus_LostCompareAndSwap(t *testing.T) {
	t.Parallel()
	st := svcSeedStore(t, StatusOpen)
	spy := &svcNotifierSpy{}

	_, err := NewService(&svcRacingStore{MemoryStore: st}, WithNotifier(spy)).
		SetStatus(context.Background(), svcTenantA, svcIntakeID, StatusConfirmed, NoticeToClient)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("SetStatus devolvió %v, quería ErrConflict", err)
	}
	if after := svcStatusOf(t, st); after != StatusCancelled {
		t.Errorf("estado %q, quería cancelled: el perdedor no puede pisar al ganador", after)
	}
	if len(spy.calls) != 0 {
		t.Errorf("avisos = %d, quería 0: quien no escribió no notifica", len(spy.calls))
	}
}

// TestService_SetStatus_NotifiesOncePerAppliedTransition: una transición aplicada ⇒ exactamente
// un aviso, DESPUÉS de escribir, con la solicitud transicionada y el origen normalizado.
func TestService_SetStatus_NotifiesOncePerAppliedTransition(t *testing.T) {
	t.Parallel()
	spy := &svcNotifierSpy{}
	svc := NewService(svcSeedStore(t, StatusClosedLegacy), WithNotifier(spy))

	if _, err := svc.SetStatus(context.Background(), svcTenantA, svcIntakeID, StatusDepositRequested, NoticeToClient); err != nil {
		t.Fatalf("SetStatus devolvió el error %v", err)
	}
	want := []svcNotice{{tenantID: svcTenantA, intakeID: svcIntakeID, from: StatusConfirmed, to: StatusDepositRequested}}
	if !reflect.DeepEqual(spy.calls, want) {
		t.Fatalf("avisos = %+v, quería %+v", spy.calls, want)
	}

	// Pedir lo mismo otra vez ya no es una transición: al cliente le llegó UN mensaje, no dos.
	if _, err := svc.SetStatus(context.Background(), svcTenantA, svcIntakeID, StatusDepositRequested, NoticeToClient); err == nil {
		t.Error("repetir la transición debería rechazarse")
	}
	if len(spy.calls) != 1 {
		t.Errorf("avisos = %d tras pedir dos veces lo mismo, quería 1", len(spy.calls))
	}
}

// TestService_SetStatus_NoticeByCallerAppliesInSilence: callarse no es no registrar (D-044.49).
func TestService_SetStatus_NoticeByCallerAppliesInSilence(t *testing.T) {
	t.Parallel()
	st := svcSeedStore(t, StatusPendingApproval)
	spy := &svcNotifierSpy{}

	got, err := NewService(st, WithNotifier(spy)).SetStatus(context.Background(), svcTenantA, svcIntakeID, StatusNeedsInfo, NoticeByCaller)
	if err != nil || got.Status != StatusNeedsInfo {
		t.Fatalf("SetStatus devolvió (%q, %v), quería (needs_info, nil)", got.Status, err)
	}
	if after := svcStatusOf(t, st); after != StatusNeedsInfo {
		t.Errorf("estado %q, quería needs_info: la transición se aplica igual", after)
	}
	if len(spy.calls) != 0 {
		t.Errorf("avisos = %d, quería 0: con NoticeByCaller la plataforma se calla", len(spy.calls))
	}
}

// TestService_SetStatus_UnmovedStoreDoesNotNotify: si el store devuelve la solicitud en el estado
// de origen, al cliente no le llega un WhatsApp que no corresponde a ningún cambio.
func TestService_SetStatus_UnmovedStoreDoesNotNotify(t *testing.T) {
	t.Parallel()
	spy := &svcNotifierSpy{}
	svc := NewService(svcUnmovedStore{svcSeedStore(t, StatusClosedLegacy)}, WithNotifier(spy))

	if _, err := svc.SetStatus(context.Background(), svcTenantA, svcIntakeID, StatusSettled, NoticeToClient); err != nil {
		t.Fatalf("SetStatus devolvió el error %v", err)
	}
	if len(spy.calls) != 0 {
		t.Errorf("avisos = %d, quería 0: la solicitud sigue en el estado de origen", len(spy.calls))
	}
}

// TestService_SetStatus_WithoutNotifierWorks: sin WithNotifier el servicio transiciona y no manda nada.
func TestService_SetStatus_WithoutNotifierWorks(t *testing.T) {
	t.Parallel()
	got, err := NewService(svcSeedStore(t, StatusConfirmed)).SetStatus(context.Background(), svcTenantA, svcIntakeID, StatusSettled, NoticeToClient)
	if err != nil || got.Status != StatusSettled {
		t.Errorf("SetStatus sin notificador devolvió (%q, %v), quería (settled, nil)", got.Status, err)
	}
}

// TestService_OnlyExplicitPushesReachTheCRM es R-03: cablear el puente no hace que el Service
// empuje por su cuenta. Leer, transicionar y abandonar no encolan nada.
func TestService_OnlyExplicitPushesReachTheCRM(t *testing.T) {
	t.Parallel()
	crm := &svcCRMSpy{}
	st := svcSeedStore(t, StatusOpen)
	st.BindEvent(svcIntakeID, "evento-1")
	svc := NewService(st, WithCRMPusher(crm))
	ctx := context.Background()

	if _, err := svc.List(ctx, svcTenantA, Filter{}); err != nil {
		t.Fatalf("List devolvió el error %v", err)
	}
	if _, err := svc.Get(ctx, svcTenantA, svcIntakeID); err != nil {
		t.Fatalf("Get devolvió el error %v", err)
	}
	if _, err := svc.SetStatus(ctx, svcTenantA, svcIntakeID, StatusPendingApproval, NoticeToClient); err != nil {
		t.Fatalf("SetStatus devolvió el error %v", err)
	}
	if err := svc.AbandonByEvent(ctx, svcTenantA, "evento-1"); err != nil {
		t.Fatalf("AbandonByEvent devolvió el error %v", err)
	}
	if len(crm.calls) != 0 {
		t.Errorf("empujes = %d, quería 0: solo empuja quien llama a PushRevisionToCRM", len(crm.calls))
	}
}

// TestService_PushRevisionToCRM: le pasa al puente exactamente lo recibido, con el número
// EXPLÍCITO (R-12) aunque no sea el de la última revisión del detalle; no deduplica.
func TestService_PushRevisionToCRM(t *testing.T) {
	t.Parallel()
	crm := &svcCRMSpy{}
	svc := NewService(NewMemoryStore(), WithCRMPusher(crm))
	detail := Detail{
		Intake:    svcIntake(svcIntakeID, StatusNeedsInfo),
		Items:     svcItems(),
		Revisions: []Revision{{IntakeID: svcIntakeID, RevisionNo: 7, Kind: RevisionKindCorrected}},
	}

	svc.PushRevisionToCRM(context.Background(), svcTenantA, detail, 4)
	svc.PushRevisionToCRM(context.Background(), svcTenantA, detail, 4)

	want := svcPush{tenantID: svcTenantA, detail: detail, revisionNo: 4}
	if len(crm.calls) != 2 || !reflect.DeepEqual(crm.calls[0], want) || !reflect.DeepEqual(crm.calls[1], want) {
		t.Errorf("empujes = %+v, quería dos veces %+v", crm.calls, want)
	}
}

// TestService_PushRevisionToCRM_WithoutPusherDoesNothing: sin puente cableado no revienta.
func TestService_PushRevisionToCRM_WithoutPusherDoesNothing(t *testing.T) {
	t.Parallel()
	NewService(NewMemoryStore()).PushRevisionToCRM(context.Background(), svcTenantA, Detail{Intake: svcIntake(svcIntakeID, StatusOpen)}, 1)
}

// TestService_AbandonByEvent: la solicitud `open` que cuelga del evento queda `abandoned`, sin
// avisar al cliente; repetirlo, un evento sin contenido o uno desconocido son éxito.
func TestService_AbandonByEvent(t *testing.T) {
	t.Parallel()
	st := svcSeedStore(t, StatusOpen)
	st.BindEvent(svcIntakeID, "evento-1")
	spy := &svcNotifierSpy{}
	svc := NewService(st, WithNotifier(spy))

	for _, eventID := range []string{"evento-1", "evento-1", "evento-sin-contenido", ""} {
		if err := svc.AbandonByEvent(context.Background(), svcTenantA, eventID); err != nil {
			t.Fatalf("AbandonByEvent(%q) devolvió el error %v, quería nil (idempotente)", eventID, err)
		}
	}
	if after := svcStatusOf(t, st); after != StatusAbandoned {
		t.Errorf("estado %q, quería abandoned", after)
	}
	if len(spy.calls) != 0 {
		t.Errorf("avisos = %d, quería 0: el abandono por evento no notifica", len(spy.calls))
	}
}

// TestService_AbandonByEvent_NeverAbandonsAResolvedIntake: una `confirmed` no se abandona por
// esta puerta, y tampoco la de otro tenant.
func TestService_AbandonByEvent_NeverAbandonsAResolvedIntake(t *testing.T) {
	t.Parallel()
	st := svcSeedStore(t, StatusConfirmed)
	st.BindEvent(svcIntakeID, "evento-1")
	svc := NewService(st)

	for _, tenantID := range []string{svcTenantA, svcTenantB} {
		if err := svc.AbandonByEvent(context.Background(), tenantID, "evento-1"); err != nil {
			t.Fatalf("AbandonByEvent devolvió el error %v", err)
		}
	}
	if after := svcStatusOf(t, st); after != StatusConfirmed {
		t.Errorf("estado %q, quería confirmed", after)
	}
}
