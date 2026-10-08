//go:build pendiente

package intakes

import (
	"context"
	"testing"
	"time"
)

// LA SIEMBRA Y LOS DOBLES que comparten todos los tests del Service (service_*_test.go,
// approve_*_test.go, requestinfo_test.go, reanalisis_test.go). Llevan el prefijo `svc` para no
// chocar con los de otros ficheros del paquete. Aquí no hay ningún test.

const (
	svcTenantA  = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	svcTenantB  = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	svcIntakeID = "11111111-1111-1111-1111-111111111111"
	svcOtherID  = "22222222-2222-2222-2222-222222222222"
)

// svcSeedTime es el instante de creación de la solicitud sembrada.
var svcSeedTime = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

// svcIntake devuelve la cabecera sembrada: contacto OPACO, total 21500.
func svcIntake(id, status string) Intake {
	return Intake{
		ID: id, ContactID: "contacto-opaco-1", SessionID: "sess-a", Status: status,
		Total: 21500, CreatedAt: svcSeedTime, UpdatedAt: svcSeedTime,
	}
}

// svcItems son las líneas sembradas: una de cliente (con personalización) y la de envío, que es
// de la plataforma. Suman 21500.
func svcItems() []Item {
	return []Item{
		{SKU: "torta-v1", Label: "Torta 10-12 porciones", Customization: "sin maní", Qty: 1, UnitPrice: 18000},
		{SKU: ShippingSKU, Label: "Envío", Qty: 1, UnitPrice: 3500},
	}
}

// svcSeedStore siembra un store en memoria con UNA solicitud del tenant A en el estado dado.
func svcSeedStore(t *testing.T, status string) *MemoryStore {
	t.Helper()
	st := NewMemoryStore()
	st.Add(svcTenantA, svcIntake(svcIntakeID, status), svcItems()...)
	return st
}

// svcStatusOf lee el estado persistido de la solicitud sembrada.
func svcStatusOf(t *testing.T, st *MemoryStore) string {
	t.Helper()
	d, err := st.Get(context.Background(), svcTenantA, svcIntakeID)
	if err != nil {
		t.Fatalf("no se pudo releer la solicitud sembrada: %v", err)
	}
	return d.Status
}

// svcTrace es el registro ORDENADO de lo que hicieron los dobles: con él se afirma el orden de
// las operaciones, no solo que ocurrieron. Un *svcTrace nil no registra nada.
type svcTrace struct{ events []string }

func (tr *svcTrace) add(event string) {
	if tr != nil {
		tr.events = append(tr.events, event)
	}
}

// svcNotice es UN aviso al cliente, tal como lo vio el puerto StatusNotifier.
type svcNotice struct {
	tenantID string
	intakeID string
	from, to string
}

type svcNotifierSpy struct{ calls []svcNotice }

func (n *svcNotifierSpy) NotifyStatus(_ context.Context, tenantID string, in Intake, from string) {
	n.calls = append(n.calls, svcNotice{tenantID: tenantID, intakeID: in.ID, from: from, to: in.Status})
}

// svcTouch es UN toque de un recordatorio perezoso.
type svcTouch struct {
	tenantID string
	touched  []Intake
}

type svcDepositSpy struct{ calls []svcTouch }

func (d *svcDepositSpy) Remind(_ context.Context, tenantID string, touched []Intake) {
	d.calls = append(d.calls, svcTouch{tenantID: tenantID, touched: touched})
}

type svcExpirySpy struct{ calls []svcTouch }

func (e *svcExpirySpy) RemindOverdue(_ context.Context, tenantID string, touched []Intake) {
	e.calls = append(e.calls, svcTouch{tenantID: tenantID, touched: touched})
}

// svcPush es UN encolado al puente CRM, tal como lo vio el puerto.
type svcPush struct {
	tenantID   string
	detail     Detail
	revisionNo int
}

type svcCRMSpy struct {
	trace *svcTrace
	calls []svcPush
}

func (c *svcCRMSpy) PushRevision(_ context.Context, tenantID string, d Detail, revisionNo int) {
	c.trace.add("crm_push")
	c.calls = append(c.calls, svcPush{tenantID: tenantID, detail: d, revisionNo: revisionNo})
}

// svcSent es UN mensaje del dueño, tal como lo vio el puerto QuoteSender.
type svcSent struct {
	tenantID string
	in       Intake
	text     string
}

// svcQuoteSpy compone como promete el puerto: el texto del dueño y, si el tenant tiene plantilla
// de seña, un renglón en blanco y la plantilla.
type svcQuoteSpy struct {
	trace     *svcTrace
	template  string
	composed  []svcSent
	quotes    []svcSent
	questions []svcSent
}

func (q *svcQuoteSpy) QuoteText(_ context.Context, tenantID string, in Intake, ownerText string) string {
	q.trace.add("quote_text")
	q.composed = append(q.composed, svcSent{tenantID: tenantID, in: in, text: ownerText})
	if q.template == "" {
		return ownerText
	}
	return ownerText + "\n\n" + q.template
}

func (q *svcQuoteSpy) SendQuote(_ context.Context, tenantID string, in Intake, text string) {
	q.trace.add("send_quote")
	q.quotes = append(q.quotes, svcSent{tenantID: tenantID, in: in, text: text})
}

func (q *svcQuoteSpy) SendQuestion(_ context.Context, tenantID string, in Intake, question string) {
	q.trace.add("send_question")
	q.questions = append(q.questions, svcSent{tenantID: tenantID, in: in, text: question})
}

// svcMetric es UNA medición, tal como la vio el puerto MetricsPublisher.
type svcMetric struct {
	tenantID  string
	contactID string
	name      string
	payload   map[string]any
}

type svcMetricsSpy struct {
	trace *svcTrace
	err   error
	calls []svcMetric
}

func (m *svcMetricsSpy) PublishMetric(_ context.Context, tenantID, contactID, name string, payload map[string]any) error {
	m.trace.add("metric")
	m.calls = append(m.calls, svcMetric{tenantID: tenantID, contactID: contactID, name: name, payload: payload})
	return m.err
}

// svcTracedStore es el store en memoria con sus dos ESCRITURAS trazadas y, si se le pide,
// rotas: `updateErr` imita el compare-and-swap perdido y `insertErr` el INSERT de la revisión
// que se cae después de que la transición ya confirmó.
type svcTracedStore struct {
	*MemoryStore
	trace     *svcTrace
	updateErr error
	insertErr error
	expected  [][]string
}

func (s *svcTracedStore) UpdateStatus(ctx context.Context, tenantID, intakeID, to string, expected []string) (Intake, error) {
	s.trace.add("update_status")
	s.expected = append(s.expected, expected)
	if s.updateErr != nil {
		return Intake{}, s.updateErr
	}
	return s.MemoryStore.UpdateStatus(ctx, tenantID, intakeID, to, expected)
}

func (s *svcTracedStore) InsertRevision(ctx context.Context, rev Revision) (Revision, error) {
	s.trace.add("insert_revision")
	if s.insertErr != nil {
		return Revision{}, s.insertErr
	}
	return s.MemoryStore.InsertRevision(ctx, rev)
}

// svcBulkStore es un Store del que solo se usa la lectura masiva: devuelve `n` solicitudes (o
// `err`) y recuerda la cota que se le pidió.
type svcBulkStore struct {
	Store
	n        int
	err      error
	gotLimit int
}

func (s *svcBulkStore) ListDetails(_ context.Context, _ string, _ Filter, limit int) ([]Detail, error) {
	s.gotLimit = limit
	if s.err != nil {
		return nil, s.err
	}
	return make([]Detail, s.n), nil
}

// svcFailingStore falla TODAS las lecturas con `err`.
type svcFailingStore struct {
	Store
	err error
}

func (s svcFailingStore) List(context.Context, string, Filter) ([]Intake, int, error) {
	return nil, 0, s.err
}

func (s svcFailingStore) Get(context.Context, string, string) (Detail, error) {
	return Detail{}, s.err
}

// svcRawStatusStore lee la solicitud con el estado TAL COMO ESTÁ GUARDADO, sin normalizar: es el
// store que no hace el favor de traducir el `closed` legado. El Service no puede depender de
// ese favor.
type svcRawStatusStore struct {
	*MemoryStore
	raw string
}

func (s svcRawStatusStore) Get(ctx context.Context, tenantID, intakeID string) (Detail, error) {
	d, err := s.MemoryStore.Get(ctx, tenantID, intakeID)
	d.Status = s.raw
	return d, err
}
