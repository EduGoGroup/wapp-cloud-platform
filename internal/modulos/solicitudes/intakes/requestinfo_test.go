package intakes

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// PEDIR MÁS INFORMACIÓN (Service.RequestInfo). Lo que se prueba:
//   · el cliente recibe UN mensaje y es la PREGUNTA DEL DUEÑO, byte a byte;
//   · el aviso genérico de `needs_info` NO sale por este camino;
//   · sin pregunta, o sin canal, no se toca nada («jamás sale sola»);
//   · esta puerta no escribe revisión y, por tanto, no empuja al CRM.
//
// La escena y los dobles son los de la aprobación (approve_service_test.go, service_test.go).

// svcOwnerQuestion es lo que escribe el dueño. Lleva acentos, emoji, dos renglones y espacios en
// los extremos a propósito: «byte a byte» solo se demuestra con un texto que un recorte o una
// normalización estropearían.
const svcOwnerQuestion = " ¿Para cuántas personas? \U0001F382\n "

// TestErrEmptyQuestion_Text: el centinela es texto observable.
func TestErrEmptyQuestion_Text(t *testing.T) {
	t.Parallel()
	const want = "la petición de información no trae la pregunta que se le manda al cliente"
	if got := ErrEmptyQuestion.Error(); got != want {
		t.Errorf("ErrEmptyQuestion = %q, quería %q", got, want)
	}
}

// TestRequestInfo_SendsOnlyTheOwnersQuestion es D-044.49 §2 entero: sale UN mensaje, es la
// pregunta tal como la escribió el dueño —sin plantilla de seña: no pasa por QuoteText—, con la
// solicitud ya en `needs_info`, y la plataforma se calla en esta transición.
func TestRequestInfo_SendsOnlyTheOwnersQuestion(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)

	detail, err := sc.svc.RequestInfo(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuestion)
	if err != nil {
		t.Fatalf("RequestInfo devolvió el error %v", err)
	}
	if len(sc.quotes.questions) != 1 || len(sc.quotes.quotes) != 0 || len(sc.quotes.composed) != 0 {
		t.Fatalf("mensajes = (%d preguntas, %d cotizaciones, %d composiciones), quería (1, 0, 0)",
			len(sc.quotes.questions), len(sc.quotes.quotes), len(sc.quotes.composed))
	}
	sent := sc.quotes.questions[0]
	if sent.text != svcOwnerQuestion {
		t.Errorf("se envió %q, quería la pregunta del dueño byte a byte %q", sent.text, svcOwnerQuestion)
	}
	if sent.tenantID != svcTenantA || sent.in.ID != svcIntakeID || sent.in.Status != StatusNeedsInfo {
		t.Errorf("SendQuestion recibió (%q, %q, %q), quería la solicitud ya en needs_info", sent.tenantID, sent.in.ID, sent.in.Status)
	}
	if len(sc.notifier.calls) != 0 {
		t.Errorf("avisos genéricos = %d, quería 0: sería anunciar el mensaje que ya va pegado", len(sc.notifier.calls))
	}
	if detail.Status != StatusNeedsInfo || svcStatusOf(t, sc.store.MemoryStore) != StatusNeedsInfo {
		t.Errorf("estado devuelto %q, quería needs_info y persistido", detail.Status)
	}
}

// TestRequestInfo_WritesNoRevisionAndPushesNothing: una revisión retrata el PRESUPUESTO, y
// preguntar no cambia ni una línea ni el total; sin revisión nueva no hay nada que empujar. El
// detalle devuelto trae las líneas y el histórico que ya estaban.
func TestRequestInfo_WritesNoRevisionAndPushesNothing(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)

	detail, err := sc.svc.RequestInfo(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuestion)
	if err != nil {
		t.Fatalf("RequestInfo devolvió el error %v", err)
	}
	stored := sc.store.Revisions(svcIntakeID)
	if len(stored) != 1 || stored[0].Kind != RevisionKindInterpreted {
		t.Errorf("revisiones guardadas = %+v, quería solo el borrador que ya estaba", stored)
	}
	if len(sc.crm.calls) != 0 {
		t.Errorf("empujes = %d, quería 0: el puente recibe revisiones, no estados sueltos", len(sc.crm.calls))
	}
	if !reflect.DeepEqual(detail.Items, svcItems()) {
		t.Errorf("líneas devueltas = %+v, quería las que ya estaban", detail.Items)
	}
	if len(detail.Revisions) != 1 || detail.Revisions[0].RevisionNo != 1 || detail.BuyerDataPresent {
		t.Errorf("detalle devuelto = (%d revisiones, datos del comprador %v), quería el histórico que ya estaba", len(detail.Revisions), detail.BuyerDataPresent)
	}
}

// TestRequestInfo_OperationOrder: primero la transición, después la pregunta y al final la
// métrica. Enviar antes y fallar la transición haría que el reintento del dueño mandara la misma
// pregunta dos veces.
func TestRequestInfo_OperationOrder(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)

	if _, err := sc.svc.RequestInfo(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuestion); err != nil {
		t.Fatalf("RequestInfo devolvió el error %v", err)
	}
	if want := []string{"update_status", "send_question", "metric"}; !slices.Equal(sc.trace.events, want) {
		t.Errorf("orden = %v, quería %v", sc.trace.events, want)
	}
}

// TestRequestInfo_Preconditions: cada rechazo, en el orden del contrato, no toca nada: ni estado,
// ni mensaje, ni métrica. Cada fila arrastra los defectos de las siguientes.
func TestRequestInfo_Preconditions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		status   string
		tenantID string
		question string
		noSender bool
		want     error
	}{
		{name: "no quote sender wins over everything else", status: StatusOpen, tenantID: svcTenantB, question: "", noSender: true, want: ErrNoQuoteSender},
		{name: "empty question wins over a foreign intake", status: StatusOpen, tenantID: svcTenantB, question: "", want: ErrEmptyQuestion},
		{name: "blank question", status: StatusPendingApproval, tenantID: svcTenantA, question: " \n\t ", want: ErrEmptyQuestion},
		{name: "unicode blank question", status: StatusPendingApproval, tenantID: svcTenantA, question: "\u00a0\u3000\u2028", want: ErrEmptyQuestion},
		{name: "foreign intake is not found", status: StatusPendingApproval, tenantID: svcTenantB, question: "¿cuántas?", want: ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sc := newApproveScene(t, c.status)
			if c.noSender {
				sc.svc = NewService(sc.store, WithNotifier(sc.notifier), WithMetrics(sc.metrics, nil))
			}
			_, err := sc.svc.RequestInfo(context.Background(), c.tenantID, svcIntakeID, c.question)
			if !errors.Is(err, c.want) {
				t.Fatalf("RequestInfo devolvió %v, quería %v", err, c.want)
			}
			sc.wantUntouched(t, c.status)
		})
	}
}

// TestRequestInfo_OnlyFromPendingApproval: a `needs_info` solo se llega desde el presupuesto por
// aprobar, y eso lo dice la máquina de estados —esta puerta no la duplica—. El rechazo trae los
// destinos legales, que es lo que el dueño necesita para no adivinar.
func TestRequestInfo_OnlyFromPendingApproval(t *testing.T) {
	t.Parallel()
	fromConfirmed := []string{"cancelled", "deposit_requested", "pending_approval", "settled"}
	cases := []struct {
		name        string
		status      string
		wantFrom    string
		wantAllowed []string
	}{
		{name: "live cart", status: StatusOpen, wantFrom: StatusOpen},
		{name: "already confirmed", status: StatusConfirmed, wantFrom: StatusConfirmed, wantAllowed: fromConfirmed},
		{name: "legacy closed is reported normalized", status: StatusClosedLegacy, wantFrom: StatusConfirmed, wantAllowed: fromConfirmed},
		{name: "already waiting for info", status: StatusNeedsInfo, wantFrom: StatusNeedsInfo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sc := newApproveScene(t, c.status)

			_, err := sc.svc.RequestInfo(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuestion)
			var rejected *TransitionError
			if !errors.As(err, &rejected) {
				t.Fatalf("RequestInfo devolvió %v, quería un *TransitionError", err)
			}
			if rejected.From != c.wantFrom || rejected.To != StatusNeedsInfo {
				t.Errorf("TransitionError = %+v, quería from=%q to=needs_info", rejected, c.wantFrom)
			}
			if c.wantAllowed != nil && !slices.Equal(rejected.Allowed, c.wantAllowed) {
				t.Errorf("destinos legales = %v, quería %v", rejected.Allowed, c.wantAllowed)
			}
			sc.wantUntouched(t, c.status)
		})
	}
}

// TestRequestInfo_LostTransitionSendsNothing: si otro operador se adelantó, la pregunta no sale.
func TestRequestInfo_LostTransitionSendsNothing(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)
	sc.store.updateErr = ErrConflict

	_, err := sc.svc.RequestInfo(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuestion)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("RequestInfo devolvió %v, quería ErrConflict", err)
	}
	if want := []string{"update_status"}; !slices.Equal(sc.trace.events, want) {
		t.Errorf("tras perder la transición se hizo %v, quería %v", sc.trace.events, want)
	}
	if len(sc.quotes.questions) != 0 {
		t.Errorf("preguntas enviadas = %d, quería 0", len(sc.quotes.questions))
	}
}

// TestRequestInfo_DetailKeepsBuyerDataPresent: el detalle devuelto conserva BuyerDataPresent tal
// como se leyó (el doble svcRetotaledStore, de approve_test.go, lee siempre «hay datos»).
func TestRequestInfo_DetailKeepsBuyerDataPresent(t *testing.T) {
	t.Parallel()
	st := &svcRetotaledStore{MemoryStore: svcSeedStore(t, StatusPendingApproval), total: 21500}
	svc := NewService(st, WithQuoteSender(&svcQuoteSpy{trace: &svcTrace{}}))

	detail, err := svc.RequestInfo(context.Background(), svcTenantA, svcIntakeID, "¿Para cuántas personas?")
	if err != nil {
		t.Fatalf("RequestInfo devolvió el error %v", err)
	}
	if !detail.BuyerDataPresent || detail.Status != StatusNeedsInfo {
		t.Errorf("detalle = (datos del comprador %v, estado %q), quería (true, needs_info)", detail.BuyerDataPresent, detail.Status)
	}
}
