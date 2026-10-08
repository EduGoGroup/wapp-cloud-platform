package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// LA ACCIÓN APROBAR (Service.Approve). Lo que se prueba, que es lo que el contrato promete:
//   · lo que se GUARDA en la revisión `approved` es byte a byte lo que se ENVÍA;
//   · el aviso genérico del estado destino NO sale;
//   · el empuje al CRM lleva el revision_no REAL y el estado REAL;
//   · el ORDEN de las operaciones y qué deja cada fallo parcial;
//   · las precondiciones, en su orden, cortan ANTES de escribir nada.
//
// Los dobles y la siembra están en service_test.go. Los literales salen del fichero viejo
// (internal/intakes/approve.go @ 64c181a) sobre esta misma escena.

// svcOwnerQuote es el texto que escribe el dueño. Lleva acentos, emoji, un salto de línea y
// espacios en los dos extremos a propósito: «byte a byte» solo se demuestra con un texto que un
// recorte o una normalización estropearían.
const svcOwnerQuote = "  Hola \U0001F382\nTotal $21.500  "

// svcDepositTemplate es la plantilla de seña del tenant, tal como la adjunta el emisor.
const svcDepositTemplate = "Seña: alias torta.mp"

// svcDraftPayload es un borrador del pipeline con todas sus líneas precificadas.
const svcDraftPayload = `{"lines":[{"label":"Torta","unit_price":18000}]}`

// svcApprovedPayload es la foto que deja la aprobación de la escena: el total y TODAS las líneas
// persistidas (la de envío incluida), sin la personalización.
const svcApprovedPayload = `{"version":1,"total":21500,"items":[` +
	`{"sku":"torta-v1","label":"Torta 10-12 porciones","qty":1,"unit_price":18000},` +
	`{"sku":"_shipping","label":"Envío","qty":1,"unit_price":3500}]}`

// approveScene es el escenario completo de una aprobación: la solicitud por aprobar con sus
// líneas y un borrador previo (revisión 1), y todos los colaboradores espiados sobre una traza
// común.
type approveScene struct {
	svc      *Service
	store    *svcTracedStore
	trace    *svcTrace
	quotes   *svcQuoteSpy
	crm      *svcCRMSpy
	notifier *svcNotifierSpy
	metrics  *svcMetricsSpy
}

func newApproveScene(t *testing.T, status string) *approveScene {
	t.Helper()
	trace := &svcTrace{}
	sc := &approveScene{
		store:    &svcTracedStore{MemoryStore: svcSeedStore(t, status), trace: trace},
		trace:    trace,
		quotes:   &svcQuoteSpy{trace: trace, template: svcDepositTemplate},
		crm:      &svcCRMSpy{trace: trace},
		notifier: &svcNotifierSpy{},
		metrics:  &svcMetricsSpy{trace: trace},
	}
	svcSeedRevision(t, sc.store.MemoryStore, RevisionKindInterpreted, svcDraftPayload)
	sc.svc = NewService(sc.store,
		WithQuoteSender(sc.quotes), WithCRMPusher(sc.crm), WithNotifier(sc.notifier), WithMetrics(sc.metrics, nil))
	return sc
}

// svcSeedRevision escribe una revisión por el MISMO camino que producción (InsertRevision
// numera), para que el revision_no que se afirma después sea el que el store asignó.
func svcSeedRevision(t *testing.T, st *MemoryStore, kind, payload string) Revision {
	t.Helper()
	rev, err := st.InsertRevision(context.Background(), Revision{
		IntakeID: svcIntakeID, Kind: kind, Payload: json.RawMessage(payload), CreatedBy: RevisionBySystem,
	})
	if err != nil {
		t.Fatalf("no se pudo sembrar la revisión %s: %v", kind, err)
	}
	return rev
}

// wantUntouched exige que un rechazo no haya dejado rastro: ni estado, ni revisión, ni mensaje,
// ni aviso, ni empuje, ni métrica, ni siquiera el texto compuesto.
func (sc *approveScene) wantUntouched(t *testing.T, status string) {
	t.Helper()
	// El store devuelve el estado ya normalizado (una fila `closed` se lee `confirmed`).
	if after := svcStatusOf(t, sc.store.MemoryStore); after != NormalizeStatus(status) {
		t.Errorf("estado %q, quería %q: un rechazo no transiciona", after, NormalizeStatus(status))
	}
	if len(sc.trace.events) != 0 {
		t.Errorf("el rechazo dejó rastro: %v", sc.trace.events)
	}
	if len(sc.notifier.calls) != 0 {
		t.Errorf("avisos = %d, quería 0", len(sc.notifier.calls))
	}
}

// TestApprove_StoresExactlyWhatItSends es el criterio central: la revisión `approved` tiene que
// poder citarse el día que el cliente diga «a mí me dijeron otra cosa», y para eso su
// RenderedText es el mensaje que salió por el cable, no una aproximación.
func TestApprove_StoresExactlyWhatItSends(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)
	const wantText = svcOwnerQuote + "\n\n" + svcDepositTemplate

	detail, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuote)
	if err != nil {
		t.Fatalf("Approve devolvió el error %v", err)
	}
	if detail.Status != StatusConfirmed || svcStatusOf(t, sc.store.MemoryStore) != StatusConfirmed {
		t.Errorf("estado devuelto %q, quería confirmed y persistido", detail.Status)
	}

	// Se compone sobre el texto del dueño SIN tocar y sobre la solicitud leída.
	if len(sc.quotes.composed) != 1 || sc.quotes.composed[0].text != svcOwnerQuote ||
		sc.quotes.composed[0].tenantID != svcTenantA || sc.quotes.composed[0].in.Status != StatusPendingApproval {
		t.Errorf("QuoteText recibió %+v, quería el texto del dueño byte a byte y la solicitud por aprobar", sc.quotes.composed)
	}
	// Sale UN mensaje, con la solicitud ya confirmada.
	if len(sc.quotes.quotes) != 1 || len(sc.quotes.questions) != 0 {
		t.Fatalf("mensajes = (%d cotizaciones, %d preguntas), quería (1, 0)", len(sc.quotes.quotes), len(sc.quotes.questions))
	}
	sent := sc.quotes.quotes[0]
	if sent.text != wantText || sent.tenantID != svcTenantA || sent.in.ID != svcIntakeID || sent.in.Status != StatusConfirmed {
		t.Errorf("SendQuote recibió (%q, %q, %q), quería el texto compuesto y la solicitud confirmada", sent.text, sent.in.ID, sent.in.Status)
	}

	requireApprovedRevision(t, sc, sent.text)
}

// requireApprovedRevision: la revisión es la 2 (había un borrador), la escribe el dueño y lleva
// ese mismo texto.
func requireApprovedRevision(t *testing.T, sc *approveScene, sentText string) {
	t.Helper()
	stored := sc.store.Revisions(svcIntakeID)
	if len(stored) != 2 {
		t.Fatalf("revisiones guardadas = %d, quería 2", len(stored))
	}
	rev := stored[1]
	if rev.RevisionNo != 2 || rev.Kind != RevisionKindApproved || rev.CreatedBy != RevisionByOwner || rev.IntakeID != svcIntakeID {
		t.Errorf("revisión = (no %d, %q, por %q), quería (2, approved, owner)", rev.RevisionNo, rev.Kind, rev.CreatedBy)
	}
	if rev.RenderedText != sentText {
		t.Errorf("RenderedText = %q y se envió %q: lo guardado no es lo enviado", rev.RenderedText, sentText)
	}
	if string(rev.Payload) != svcApprovedPayload {
		t.Errorf("payload = %s, quería %s", rev.Payload, svcApprovedPayload)
	}
}

// TestApprove_WithoutDepositTemplateSendsTheQuoteAlone: sin plantilla, sale el texto del dueño
// tal cual — la composición es del emisor, y Approve no le añade nada.
func TestApprove_WithoutDepositTemplateSendsTheQuoteAlone(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)
	sc.quotes.template = ""

	if _, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuote); err != nil {
		t.Fatalf("Approve devolvió el error %v", err)
	}
	stored := sc.store.Revisions(svcIntakeID)
	if len(sc.quotes.quotes) != 1 || sc.quotes.quotes[0].text != svcOwnerQuote || stored[len(stored)-1].RenderedText != svcOwnerQuote {
		t.Errorf("se envió %+v, quería el texto del dueño byte a byte, igual en la revisión", sc.quotes.quotes)
	}
}

// TestApprove_GenericNoticeStaysSilent es D-044.49 §1: el aviso genérico de `confirmed`
// repetiría el número que la cotización ya dijo. El cliente recibe UN mensaje.
func TestApprove_GenericNoticeStaysSilent(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)

	if _, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuote); err != nil {
		t.Fatalf("Approve devolvió el error %v", err)
	}
	if len(sc.notifier.calls) != 0 {
		t.Errorf("avisos genéricos = %d, quería 0", len(sc.notifier.calls))
	}
	if len(sc.quotes.quotes) != 1 {
		t.Errorf("cotizaciones enviadas = %d, quería 1", len(sc.quotes.quotes))
	}
}

// TestApprove_PushesTheRealRevisionAndStatus mata de una vez los dos literales de R-12: con un
// borrador previo la revisión de la aprobación es la 2, y un `1` clavado dejaría al puente en el
// primer estado para siempre. El detalle devuelto es el mismo que se empuja.
func TestApprove_PushesTheRealRevisionAndStatus(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)

	detail, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuote)
	if err != nil {
		t.Fatalf("Approve devolvió el error %v", err)
	}
	if len(sc.crm.calls) != 1 {
		t.Fatalf("empujes = %d, quería 1", len(sc.crm.calls))
	}
	push := sc.crm.calls[0]
	stored := sc.store.Revisions(svcIntakeID)
	if push.revisionNo != stored[len(stored)-1].RevisionNo || push.revisionNo != 2 {
		t.Errorf("revision_no empujado = %d, quería el que asignó el store (2)", push.revisionNo)
	}
	if push.tenantID != svcTenantA || push.detail.Status != StatusConfirmed || push.detail.Total != 21500 {
		t.Errorf("empuje = (%q, %q, %v), quería (tenant A, confirmed, 21500)", push.tenantID, push.detail.Status, push.detail.Total)
	}
	if !reflect.DeepEqual(push.detail, detail) {
		t.Errorf("el detalle empujado y el devuelto difieren:\n%+v\n%+v", push.detail, detail)
	}
	if !reflect.DeepEqual(detail.Items, svcItems()) {
		t.Errorf("líneas devueltas = %+v, quería las leídas, con su personalización", detail.Items)
	}
	if len(detail.Revisions) != 2 || detail.Revisions[0].Kind != RevisionKindInterpreted ||
		detail.Revisions[1].Kind != RevisionKindApproved || detail.Revisions[1].RevisionNo != 2 {
		t.Errorf("histórico devuelto = %+v, quería el borrador y, al final, la aprobación", detail.Revisions)
	}
}

// svcSpareCapacityStore entrega el histórico en una lista con capacidad de sobra: si Approve
// añadiera su revisión con un append directo, escribiría en el array del store.
type svcSpareCapacityStore struct {
	*MemoryStore
	backing []Revision
}

func (s *svcSpareCapacityStore) Get(ctx context.Context, tenantID, intakeID string) (Detail, error) {
	d, err := s.MemoryStore.Get(ctx, tenantID, intakeID)
	if err != nil {
		return d, err
	}
	if s.backing == nil {
		// Solo la PRIMERA lectura, que es la de Approve: las siguientes son de SetStatus.
		s.backing = make([]Revision, len(d.Revisions), len(d.Revisions)+4)
		copy(s.backing, d.Revisions)
		d.Revisions = s.backing
	}
	return d, nil
}

// TestApprove_DoesNotWriteIntoTheStoreHistory: la revisión nueva se añade sobre una lista NUEVA.
func TestApprove_DoesNotWriteIntoTheStoreHistory(t *testing.T) {
	t.Parallel()
	st := &svcSpareCapacityStore{MemoryStore: svcSeedStore(t, StatusPendingApproval)}
	svcSeedRevision(t, st.MemoryStore, RevisionKindInterpreted, svcDraftPayload)

	detail, err := NewService(st, WithQuoteSender(&svcQuoteSpy{})).Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza")
	if err != nil {
		t.Fatalf("Approve devolvió el error %v", err)
	}
	if len(detail.Revisions) != 2 {
		t.Fatalf("histórico devuelto = %d revisiones, quería 2", len(detail.Revisions))
	}
	if spare := st.backing[:2][1]; !reflect.DeepEqual(spare, Revision{}) {
		t.Errorf("Approve escribió en el array que entregó el store: %+v", spare)
	}
}

// TestApprove_OperationOrder FIJA EL ORDEN, que es la decisión de diseño del fichero: componer,
// transicionar, dejar rastro, enviar, empujar y medir. Es lo único que impide que alguien
// «arregle» el orden sin darse cuenta: los demás tests pasan con varios órdenes.
func TestApprove_OperationOrder(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)

	if _, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuote); err != nil {
		t.Fatalf("Approve devolvió el error %v", err)
	}
	want := []string{"quote_text", "update_status", "insert_revision", "send_quote", "crm_push", "metric"}
	if !slices.Equal(sc.trace.events, want) {
		t.Errorf("orden = %v, quería %v", sc.trace.events, want)
	}
}

// TestApprove_RevisionWriteFailureSendsNothing: si la revisión no se escribe, la solicitud queda
// confirmada, sin rastro y SIN HABER HABLADO. El error lo dice con esas palabras y envuelve la
// causa; no hay envío, ni empuje, ni métrica.
func TestApprove_RevisionWriteFailureSendsNothing(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)
	diskFull := errors.New("disco lleno")
	sc.store.insertErr = diskFull

	_, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuote)
	const want = "intakes: la solicitud quedó CONFIRMADA pero su revisión approved no se escribió, " +
		"así que la cotización NO se envió y hay que mandarla a mano " +
		"(intake_id=11111111-1111-1111-1111-111111111111): disco lleno"
	if err == nil || err.Error() != want {
		t.Fatalf("Approve devolvió %v, quería %q", err, want)
	}
	if !errors.Is(err, diskFull) {
		t.Error("el error no envuelve la causa del store")
	}
	if after := svcStatusOf(t, sc.store.MemoryStore); after != StatusConfirmed {
		t.Errorf("estado %q, quería confirmed: la transición ya había ganado", after)
	}
	if want := []string{"quote_text", "update_status", "insert_revision"}; !slices.Equal(sc.trace.events, want) {
		t.Errorf("tras el fallo se hizo %v, quería %v: no se envía lo que no se registró", sc.trace.events, want)
	}
}

// TestApprove_LostTransitionWritesAndSendsNothing: de dos aprobaciones simultáneas gana UNA. La
// perdedora ya había compuesto su texto (no cuesta nada) y sale sin revisión ni mensaje.
func TestApprove_LostTransitionWritesAndSendsNothing(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)
	sc.store.updateErr = ErrConflict

	_, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, svcOwnerQuote)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Approve devolvió %v, quería ErrConflict", err)
	}
	if want := []string{"quote_text", "update_status"}; !slices.Equal(sc.trace.events, want) {
		t.Errorf("tras perder la transición se hizo %v, quería %v", sc.trace.events, want)
	}
	if got := len(sc.store.Revisions(svcIntakeID)); got != 1 {
		t.Errorf("revisiones = %d, quería 1 (solo el borrador)", got)
	}
}

// TestApprove_Preconditions: cada rechazo, con lo que NO se llegó a hacer. Las filas van en el
// orden en que el contrato las evalúa y cada una arrastra los defectos de las siguientes, para
// que el orden de precedencia quede fijado.
func TestApprove_Preconditions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		status   string
		tenantID string
		text     string
		prepare  func(t *testing.T, sc *approveScene)
		check    func(t *testing.T, err error)
	}{
		{
			name: "no quote sender wins over everything else", status: StatusOpen, tenantID: svcTenantB, text: "",
			// El store tampoco sabe escribir revisiones: con las dos faltas a la vez manda la del canal.
			prepare: func(_ *testing.T, sc *approveScene) {
				sc.svc = NewService(struct{ Store }{sc.store}, WithCRMPusher(sc.crm))
			},
			check: svcWantIs(ErrNoQuoteSender),
		},
		{
			name: "no revision writer wins over the empty text", status: StatusOpen, tenantID: svcTenantB, text: "",
			prepare: func(_ *testing.T, sc *approveScene) {
				sc.svc = NewService(struct{ Store }{sc.store}, WithQuoteSender(sc.quotes))
			},
			check: svcWantIs(ErrNoRevisionWriter),
		},
		{name: "empty text wins over a foreign intake", status: StatusOpen, tenantID: svcTenantB, text: "", check: svcWantIs(ErrEmptyQuoteText)},
		{name: "blank text", status: StatusPendingApproval, tenantID: svcTenantA, text: " \n\t ", check: svcWantIs(ErrEmptyQuoteText)},
		{name: "unicode blank text", status: StatusPendingApproval, tenantID: svcTenantA, text: "\u00a0\u3000\u2028", check: svcWantIs(ErrEmptyQuoteText)},
		{name: "foreign intake is not found", status: StatusOpen, tenantID: svcTenantB, text: "cotiza", check: svcWantIs(ErrNotFound)},
		{
			name: "open is a live cart, even with pending prices", status: StatusOpen, tenantID: svcTenantA, text: "cotiza",
			prepare: func(t *testing.T, sc *approveScene) {
				svcSeedRevision(t, sc.store.MemoryStore, RevisionKindInterpreted, `{"lines":[{"label":"A","unit_price":null}]}`)
			},
			check: svcWantNotApprovable(StatusOpen),
		},
		{name: "already confirmed", status: StatusConfirmed, tenantID: svcTenantA, text: "cotiza", check: svcWantNotApprovable(StatusConfirmed)},
		{name: "legacy closed is reported normalized", status: StatusClosedLegacy, tenantID: svcTenantA, text: "cotiza", check: svcWantNotApprovable(StatusConfirmed)},
		{name: "waiting for info", status: StatusNeedsInfo, tenantID: svcTenantA, text: "cotiza", check: svcWantNotApprovable(StatusNeedsInfo)},
		{
			name: "pending prices list every line", status: StatusPendingApproval, tenantID: svcTenantA, text: "cotiza",
			prepare: func(t *testing.T, sc *approveScene) {
				svcSeedRevision(t, sc.store.MemoryStore, RevisionKindInterpreted,
					`{"lines":[{"label":"Torta","unit_price":null},{"label":"Velas","unit_price":500},{"label":"Cartel"}]}`)
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				var pending *PendingPriceError
				if !errors.As(err, &pending) {
					t.Fatalf("Approve devolvió %v, quería un *PendingPriceError", err)
				}
				if want := []PendingPriceLine{{Index: 0, Label: "Torta"}, {Index: 2, Label: "Cartel"}}; !reflect.DeepEqual(pending.Lines, want) {
					t.Errorf("líneas pendientes = %+v, quería %+v", pending.Lines, want)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sc := newApproveScene(t, c.status)
			if c.prepare != nil {
				c.prepare(t, sc)
			}
			revisionsBefore := len(sc.store.Revisions(svcIntakeID))

			_, err := sc.svc.Approve(context.Background(), c.tenantID, svcIntakeID, c.text)
			c.check(t, err)
			sc.wantUntouched(t, c.status)
			if got := len(sc.store.Revisions(svcIntakeID)); got != revisionsBefore {
				t.Errorf("revisiones = %d, quería %d: un rechazo no escribe", got, revisionsBefore)
			}
		})
	}
}

// svcWantIs devuelve la comprobación «el error es este centinela».
func svcWantIs(target error) func(*testing.T, error) {
	return func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, target) {
			t.Fatalf("Approve devolvió %v, quería %v", err, target)
		}
	}
}

// svcWantNotApprovable devuelve la comprobación «no está por aprobar, y está en `status`».
func svcWantNotApprovable(status string) func(*testing.T, error) {
	return func(t *testing.T, err error) {
		t.Helper()
		var notApprovable *NotApprovableError
		if !errors.As(err, &notApprovable) {
			t.Fatalf("Approve devolvió %v, quería un *NotApprovableError", err)
		}
		if notApprovable.Status != status {
			t.Errorf("estado del rechazo = %q, quería %q", notApprovable.Status, status)
		}
	}
}

// TestApprove_NothingToQuote tapa el agujero que deja el pipeline: su borrador vive en la
// revisión y NO en las líneas. La línea de envío, que es de la plataforma, no cuenta como algo
// que cotizar; y las líneas sin precio se denuncian ANTES que la falta de líneas.
func TestApprove_NothingToQuote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		items   []Item
		payload string
		check   func(*testing.T, error)
	}{
		{name: "no items at all", payload: svcDraftPayload, check: svcWantIs(ErrEmptyQuote)},
		{name: "only the shipping line", items: []Item{{SKU: ShippingSKU, Label: "Envío", Qty: 1, UnitPrice: 3500}}, payload: svcDraftPayload, check: svcWantIs(ErrEmptyQuote)},
		{name: "only reserved skus", items: []Item{{SKU: ReservedSKUPrefix + "otro", Label: "Recargo", Qty: 1, UnitPrice: 1}}, payload: svcDraftPayload, check: svcWantIs(ErrEmptyQuote)},
		{
			name: "pending prices are reported before the missing lines", payload: `{"lines":[{"label":"A","unit_price":null}]}`,
			check: func(t *testing.T, err error) {
				t.Helper()
				var pending *PendingPriceError
				if !errors.As(err, &pending) {
					t.Fatalf("Approve devolvió %v, quería un *PendingPriceError", err)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			st := NewMemoryStore()
			st.Add(svcTenantA, svcIntake(svcIntakeID, StatusPendingApproval), c.items...)
			svcSeedRevision(t, st, RevisionKindInterpreted, c.payload)
			quotes, crm := &svcQuoteSpy{}, &svcCRMSpy{}

			_, err := NewService(st, WithQuoteSender(quotes), WithCRMPusher(crm)).Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza")
			c.check(t, err)
			if svcStatusOf(t, st) != StatusPendingApproval || len(st.Revisions(svcIntakeID)) != 1 {
				t.Error("el rechazo transicionó o escribió una revisión")
			}
			if len(quotes.composed)+len(quotes.quotes)+len(crm.calls) != 0 {
				t.Error("el rechazo compuso, envió o empujó algo")
			}
		})
	}
}

// TestApprove_OwnerCorrectionResolvesPendingPrices: la salida del rechazo por precios es la
// corrección del dueño, que deja una revisión cuyo payload NO puede tener líneas sin precio. Con
// esa revisión encima, la misma solicitud se aprueba — y la aprobación es la revisión 4.
func TestApprove_OwnerCorrectionResolvesPendingPrices(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)
	svcSeedRevision(t, sc.store.MemoryStore, RevisionKindInterpreted, `{"lines":[{"label":"Torta","unit_price":null}]}`)
	if _, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza"); err == nil {
		t.Fatal("con una línea sin precio la aprobación debería rechazarse")
	}
	svcSeedRevision(t, sc.store.MemoryStore, RevisionKindCorrected,
		`{"version":1,"total":21500,"items":[{"sku":"torta-v1","label":"Torta","qty":1,"unit_price":18000}]}`)

	detail, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, "cotiza")
	if err != nil {
		t.Fatalf("Approve tras la corrección devolvió el error %v", err)
	}
	if len(sc.crm.calls) != 1 || sc.crm.calls[0].revisionNo != 4 || len(detail.Revisions) != 4 {
		t.Errorf("empujes = %+v con %d revisiones, quería un empuje de la revisión 4", sc.crm.calls, len(detail.Revisions))
	}
}

// TestApprove_InvisibleTextIsStillText: el recorte que decide si hay texto es el de espacios en
// blanco; un texto que solo lleva un invisible (U+200B) NO es blanco y se aprueba tal cual. Es
// la conducta del fichero viejo, fijada para que nadie la cambie sin decidirlo.
func TestApprove_InvisibleTextIsStillText(t *testing.T) {
	t.Parallel()
	sc := newApproveScene(t, StatusPendingApproval)
	sc.quotes.template = ""

	if _, err := sc.svc.Approve(context.Background(), svcTenantA, svcIntakeID, "\u200b"); err != nil {
		t.Fatalf("Approve devolvió el error %v, quería nil", err)
	}
	if len(sc.quotes.quotes) != 1 || sc.quotes.quotes[0].text != "\u200b" {
		t.Errorf("se envió %+v, quería el texto recibido sin tocar", sc.quotes.quotes)
	}
}
