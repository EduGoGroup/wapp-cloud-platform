package grpc

import (
	"context"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// LogReceiptSink cumple el puerto.
var _ ReceiptSink = (*LogReceiptSink)(nil)

func sampleReceipt() *cloudlinkv1.MessageReceipt {
	return &cloudlinkv1.MessageReceipt{
		SessionId:  "s-1",
		CommandId:  "cmd-1",
		MessageIds: []string{"wamid-1", "wamid-2"},
		Status:     cloudlinkv1.ReceiptStatus_RECEIPT_STATUS_READ,
		Timestamp:  1700000000,
	}
}

// R-G22 (mitad del sink): el acuse se registra con sus metadatos y Record no falla.
func TestLogReceiptSinkRecordLogsMetadataOnly(t *testing.T) {
	t.Parallel()
	log, buf := capturedLog()
	sink := NewLogReceiptSink(log)

	if err := sink.Record(context.Background(), sampleReceipt()); err != nil {
		t.Fatalf("Record = %v; el sink log-only no falla nunca", err)
	}

	out := buf.String()
	for _, want := range []string{
		"acuse persistido (log-only)",
		"session_id=s-1",
		"command_id=cmd-1",
		"status=RECEIPT_STATUS_READ",
		"wamid-1",
		"wamid-2",
		"timestamp=1700000000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("la línea del acuse no contiene %q: %s", want, out)
		}
	}
	if n := strings.Count(out, "\n"); n != 1 {
		t.Errorf("Record escribió %d líneas, se esperaba 1: %s", n, out)
	}
}

// Sin receptor, sin logger o sin receipt, Record sigue devolviendo nil y no revienta.
func TestLogReceiptSinkRecordNeverFails(t *testing.T) {
	t.Parallel()
	log, buf := capturedLog()
	cases := []struct {
		name    string
		sink    *LogReceiptSink
		receipt *cloudlinkv1.MessageReceipt
	}{
		{name: "nil receiver", sink: nil, receipt: sampleReceipt()},
		{name: "nil logger", sink: NewLogReceiptSink(nil), receipt: sampleReceipt()},
		{name: "nil receipt", sink: NewLogReceiptSink(log), receipt: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.sink.Record(context.Background(), tc.receipt); err != nil {
				t.Fatalf("Record = %v, se esperaba nil", err)
			}
		})
	}
	// El receipt nil sí deja su línea (con los metadatos en cero): no es un caso mudo.
	if !buf.contains("acuse persistido (log-only)") {
		t.Errorf("el receipt nil no dejó línea: %q", buf.String())
	}
}
