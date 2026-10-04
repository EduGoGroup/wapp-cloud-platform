//go:build pendiente

package receipts_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/receipts"
)

// receiptSink es, copiado, el puerto ReceiptSink del gateway gRPC: *receipts.Sink lo satisface
// estructuralmente, así que la firma de Record no puede cambiar.
type receiptSink interface {
	Record(ctx context.Context, receipt *cloudlinkv1.MessageReceipt) error
}

var _ receiptSink = (*receipts.Sink)(nil)

// recordingStore es un Store que apunta cada Save en orden y falla los message_id de failOn.
type recordingStore struct {
	saved  []receipts.Receipt
	failOn map[string]error
	// attempts son todos los message_id que llegaron a Save, fallaran o no.
	attempts []string
}

func (s *recordingStore) Save(_ context.Context, r receipts.Receipt) error {
	s.attempts = append(s.attempts, r.MessageID)
	if err := s.failOn[r.MessageID]; err != nil {
		return err
	}
	s.saved = append(s.saved, r)
	return nil
}

func (s *recordingStore) List(context.Context, string, int, int) ([]receipts.Stored, error) {
	return nil, errors.New("recordingStore: el Sink no lista")
}

// newSink monta un Sink sobre un recordingStore y devuelve también lo que el callback recibió.
func newSink(failOn map[string]error) (*receipts.Sink, *recordingStore, *[]string) {
	store := &recordingStore{failOn: failOn}
	var recorded []string
	sink := receipts.NewSink(store, func(status string) { recorded = append(recorded, status) })
	return sink, store, &recorded
}

// TestSink_Record_OneRowPerMessageID: un acuse con varios message_id son tantos Save, en su orden,
// con la sesión, el command_id, el estado y el instante del acuse.
func TestSink_Record_OneRowPerMessageID(t *testing.T) {
	sink, store, _ := newSink(nil)
	err := sink.Record(context.Background(), &cloudlinkv1.MessageReceipt{
		SessionId:  "session-9",
		CommandId:  "cmd-9",
		MessageIds: []string{"m1", "m2", "m3"},
		Status:     cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED,
		Timestamp:  1_700_000_000,
	})
	if err != nil {
		t.Fatalf("Record: error inesperado %v", err)
	}
	at := time.Unix(1_700_000_000, 0).UTC()
	want := []receipts.Receipt{
		{SessionID: "session-9", CommandID: "cmd-9", MessageID: "m1", Status: receipts.StatusDelivered, ReceiptAt: at},
		{SessionID: "session-9", CommandID: "cmd-9", MessageID: "m2", Status: receipts.StatusDelivered, ReceiptAt: at},
		{SessionID: "session-9", CommandID: "cmd-9", MessageID: "m3", Status: receipts.StatusDelivered, ReceiptAt: at},
	}
	if !slices.Equal(store.saved, want) {
		t.Errorf("guardado = %+v, quería %+v", store.saved, want)
	}
	for _, r := range store.saved {
		if r.ReceiptAt.Location() != time.UTC {
			t.Errorf("ReceiptAt de %q está en %v, quería UTC", r.MessageID, r.ReceiptAt.Location())
		}
	}
}

// TestSink_Record_MapsTheStatus: DELIVERED y READ se traducen a los dos estados persistibles.
func TestSink_Record_MapsTheStatus(t *testing.T) {
	cases := []struct {
		name string
		in   cloudlinkv1.ReceiptStatus
		want receipts.Status
	}{
		{"delivered", cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED, receipts.StatusDelivered},
		{"read", cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_READ, receipts.StatusRead},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink, store, recorded := newSink(nil)
			err := sink.Record(context.Background(), &cloudlinkv1.MessageReceipt{
				SessionId: "s", MessageIds: []string{"m1"}, Status: c.in,
			})
			if err != nil {
				t.Fatalf("Record: error inesperado %v", err)
			}
			if len(store.saved) != 1 || store.saved[0].Status != c.want {
				t.Errorf("guardado = %+v, quería una fila con estado %q", store.saved, c.want)
			}
			if !slices.Equal(*recorded, []string{string(c.want)}) {
				t.Errorf("callback = %v, quería [%s]", *recorded, c.want)
			}
		})
	}
}

// TestSink_Record_DiscardsUnspecifiedAndUnknown: sin un estado persistible no se guarda nada ni se
// cuenta ninguna métrica, y no es un error.
func TestSink_Record_DiscardsUnspecifiedAndUnknown(t *testing.T) {
	cases := []struct {
		name string
		in   cloudlinkv1.ReceiptStatus
	}{
		{"unspecified", cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_UNSPECIFIED},
		{"unknown enum value", cloudlinkv1.ReceiptStatus(99)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink, store, recorded := newSink(nil)
			err := sink.Record(context.Background(), &cloudlinkv1.MessageReceipt{
				SessionId: "s", MessageIds: []string{"m1", "m2"}, Status: c.in, Timestamp: 1_700_000_000,
			})
			if err != nil {
				t.Errorf("Record: devolvió %v, quería nil", err)
			}
			if len(store.attempts) != 0 {
				t.Errorf("llegaron %d Save al Store, quería 0", len(store.attempts))
			}
			if len(*recorded) != 0 {
				t.Errorf("el callback se llamó %d veces, quería 0", len(*recorded))
			}
		})
	}
}

// TestSink_Record_CallbackOncePerStoredRow: la métrica cuenta filas guardadas, no acuses.
func TestSink_Record_CallbackOncePerStoredRow(t *testing.T) {
	sink, _, recorded := newSink(nil)
	err := sink.Record(context.Background(), &cloudlinkv1.MessageReceipt{
		SessionId: "s", MessageIds: []string{"m1", "m2", "m3"},
		Status: cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_READ,
	})
	if err != nil {
		t.Fatalf("Record: error inesperado %v", err)
	}
	if !slices.Equal(*recorded, []string{"read", "read", "read"}) {
		t.Errorf("callback = %v, quería tres veces \"read\"", *recorded)
	}
}

// TestSink_Record_NonPositiveTimestampIsZero: un timestamp <= 0 es «no informado».
func TestSink_Record_NonPositiveTimestampIsZero(t *testing.T) {
	for _, ts := range []int64{0, -1, -1_700_000_000} {
		sink, store, _ := newSink(nil)
		err := sink.Record(context.Background(), &cloudlinkv1.MessageReceipt{
			SessionId: "s", MessageIds: []string{"m1"},
			Status: cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED, Timestamp: ts,
		})
		if err != nil {
			t.Fatalf("Record con timestamp %d: error inesperado %v", ts, err)
		}
		if len(store.saved) != 1 || !store.saved[0].ReceiptAt.IsZero() {
			t.Errorf("timestamp %d: guardado = %+v, quería una fila con ReceiptAt cero", ts, store.saved)
		}
	}
}

// TestSink_Record_SmallestPositiveTimestampCounts: el primer segundo ya es un instante informado.
func TestSink_Record_SmallestPositiveTimestampCounts(t *testing.T) {
	sink, store, _ := newSink(nil)
	err := sink.Record(context.Background(), &cloudlinkv1.MessageReceipt{
		SessionId: "s", MessageIds: []string{"m1"},
		Status: cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED, Timestamp: 1,
	})
	if err != nil {
		t.Fatalf("Record: error inesperado %v", err)
	}
	if len(store.saved) != 1 || !store.saved[0].ReceiptAt.Equal(time.Unix(1, 0)) {
		t.Errorf("guardado = %+v, quería una fila con ReceiptAt = época + 1 s", store.saved)
	}
}

// TestSink_Record_SkipsEmptyMessageID: un message_id vacío ni llega al Store ni cuenta.
func TestSink_Record_SkipsEmptyMessageID(t *testing.T) {
	sink, store, recorded := newSink(nil)
	err := sink.Record(context.Background(), &cloudlinkv1.MessageReceipt{
		SessionId: "s", MessageIds: []string{"", "m1", "", "m2"},
		Status: cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED,
	})
	if err != nil {
		t.Fatalf("Record: error inesperado %v", err)
	}
	if !slices.Equal(store.attempts, []string{"m1", "m2"}) {
		t.Errorf("Save recibió %q, quería [m1 m2]", store.attempts)
	}
	if len(*recorded) != 2 {
		t.Errorf("el callback se llamó %d veces, quería 2", len(*recorded))
	}
}

// TestSink_Record_NoMessageIDs: un acuse sin message_id no guarda nada y no es un error.
func TestSink_Record_NoMessageIDs(t *testing.T) {
	sink, store, recorded := newSink(nil)
	err := sink.Record(context.Background(), &cloudlinkv1.MessageReceipt{
		SessionId: "s", Status: cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED,
	})
	if err != nil || len(store.attempts) != 0 || len(*recorded) != 0 {
		t.Errorf("Record sin message_id = (%v, %d Save, %d callbacks), quería (nil, 0, 0)",
			err, len(store.attempts), len(*recorded))
	}
}

// TestSink_Record_SaveErrorKeepsGoingAndReturnsTheFirst: un Save que falla no corta la expansión;
// se intentan todos, se devuelve el primer error y la métrica solo cuenta lo guardado.
func TestSink_Record_SaveErrorKeepsGoingAndReturnsTheFirst(t *testing.T) {
	errFirst := errors.New("primer fallo")
	errSecond := errors.New("segundo fallo")
	sink, store, recorded := newSink(map[string]error{"m2": errFirst, "m4": errSecond})
	err := sink.Record(context.Background(), &cloudlinkv1.MessageReceipt{
		SessionId: "s", MessageIds: []string{"m1", "m2", "m3", "m4", "m5"},
		Status: cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED,
	})
	if !errors.Is(err, errFirst) {
		t.Errorf("Record devolvió %v, quería el primer error (%v)", err, errFirst)
	}
	if errors.Is(err, errSecond) {
		t.Errorf("Record devolvió el segundo error (%v), quería solo el primero", err)
	}
	if !slices.Equal(store.attempts, []string{"m1", "m2", "m3", "m4", "m5"}) {
		t.Errorf("Save recibió %q, quería los cinco message_id", store.attempts)
	}
	saved := make([]string, 0, len(store.saved))
	for _, r := range store.saved {
		saved = append(saved, r.MessageID)
	}
	if !slices.Equal(saved, []string{"m1", "m3", "m5"}) {
		t.Errorf("guardado = %q, quería [m1 m3 m5]", saved)
	}
	if len(*recorded) != 3 {
		t.Errorf("el callback se llamó %d veces, quería 3 (una por fila guardada)", len(*recorded))
	}
}

// TestSink_Record_NilSafe: ni un *Sink nil, ni un acuse nil, ni un callback nil rompen nada.
func TestSink_Record_NilSafe(t *testing.T) {
	receipt := &cloudlinkv1.MessageReceipt{
		SessionId: "s", MessageIds: []string{"m1"},
		Status: cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED,
	}

	var nilSink *receipts.Sink
	if err := nilSink.Record(context.Background(), receipt); err != nil {
		t.Errorf("Record sobre un *Sink nil devolvió %v, quería nil", err)
	}

	sink, store, recorded := newSink(nil)
	if err := sink.Record(context.Background(), nil); err != nil {
		t.Errorf("Record de un acuse nil devolvió %v, quería nil", err)
	}
	if len(store.attempts) != 0 || len(*recorded) != 0 {
		t.Errorf("un acuse nil produjo %d Save y %d callbacks, quería 0 y 0", len(store.attempts), len(*recorded))
	}

	withoutCallback := &recordingStore{}
	if err := receipts.NewSink(withoutCallback, nil).Record(context.Background(), receipt); err != nil {
		t.Errorf("Record sin callback devolvió %v, quería nil", err)
	}
	if len(withoutCallback.saved) != 1 {
		t.Errorf("sin callback se guardaron %d filas, quería 1", len(withoutCallback.saved))
	}
}
