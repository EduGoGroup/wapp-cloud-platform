package receipts

import (
	"context"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// TestMemoryStore_Idempotent verifica el dedupe: el MISMO acuse (sesión +
// message + status) repetido NO crea filas nuevas; delivered y read del mismo
// mensaje SÍ son filas distintas.
func TestMemoryStore_Idempotent(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	r := Receipt{SessionID: "sess-1", CommandID: "cmd-1", MessageID: "msg-1", Status: StatusDelivered}
	for i := 0; i < 3; i++ {
		if err := s.Save(ctx, r); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	got, err := s.List(ctx, "sess-1", 100, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("acuse duplicado: got %d filas, want 1 (idempotente)", len(got))
	}

	// delivered → read: fila distinta (status en la clave).
	if err := s.Save(ctx, Receipt{SessionID: "sess-1", MessageID: "msg-1", Status: StatusRead}); err != nil {
		t.Fatalf("Save read: %v", err)
	}
	got, err = s.List(ctx, "sess-1", 100, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("delivered+read: got %d filas, want 2", len(got))
	}
}

// TestSink_ExpandsMessageIDs verifica que el sink persiste UNA fila por
// message_id y dispara el callback de métrica por cada uno.
func TestSink_ExpandsMessageIDs(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	var recorded []string
	sink := NewSink(store, func(status string) { recorded = append(recorded, status) })

	err := sink.Record(ctx, &cloudlinkv1.MessageReceipt{
		SessionId:  "sess-9",
		CommandId:  "cmd-9",
		MessageIds: []string{"m1", "m2", ""}, // el "" se descarta
		Status:     cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_DELIVERED,
		Timestamp:  1_700_000_000,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := store.List(ctx, "sess-9", 100, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expansión de message_ids: got %d filas, want 2", len(got))
	}
	if len(recorded) != 2 {
		t.Fatalf("callback de métrica: got %d, want 2", len(recorded))
	}
	if got[0].ReceiptAt.IsZero() {
		t.Error("receipt_at debería derivarse del Timestamp del acuse")
	}
}

// TestSink_DiscardsUnspecified verifica que un status UNSPECIFIED no persiste nada.
func TestSink_DiscardsUnspecified(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	sink := NewSink(store, nil)

	if err := sink.Record(ctx, &cloudlinkv1.MessageReceipt{
		SessionId:  "sess-x",
		MessageIds: []string{"m1"},
		Status:     cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_UNSPECIFIED,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := store.List(ctx, "sess-x", 100, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("UNSPECIFIED no debería persistir: got %d filas", len(got))
	}
}
