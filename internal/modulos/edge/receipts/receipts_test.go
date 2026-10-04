package receipts_test

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/receipts"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/receipts/receiptshelpertest"
)

// Las dos implementaciones del puerto lo son: lo comprueba el compilador.
var (
	_ receipts.Store = (*receipts.PostgresStore)(nil)
	_ receipts.Store = (*receiptshelpertest.Memoria)(nil)
)

// TestStatus_ValuesAreTheStoredText: los dos estados valen el texto que se guarda en la columna
// status de public.message_receipts y que sale por la métrica.
func TestStatus_ValuesAreTheStoredText(t *testing.T) {
	cases := []struct {
		name string
		got  receipts.Status
		want string
	}{
		{"delivered", receipts.StatusDelivered, "delivered"},
		{"read", receipts.StatusRead, "read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if string(c.got) != c.want {
				t.Errorf("estado = %q, quería %q", c.got, c.want)
			}
		})
	}
}

// TestStored_CarriesTheReceipt: un Stored es el Receipt guardado más el id y la marca de la nube;
// los campos del acuse se leen directamente del Stored.
func TestStored_CarriesTheReceipt(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	recorded := at.Add(time.Second)
	r := receipts.Receipt{
		SessionID: "session-1", CommandID: "cmd-1", MessageID: "msg-1",
		Status: receipts.StatusRead, ReceiptAt: at,
	}
	s := receipts.Stored{Receipt: r, ID: 7, RecordedAt: recorded}
	if s.Receipt != r {
		t.Errorf("Stored.Receipt = %+v, quería %+v", s.Receipt, r)
	}
	if s.SessionID != "session-1" || s.CommandID != "cmd-1" || s.MessageID != "msg-1" ||
		s.Status != receipts.StatusRead || !s.ReceiptAt.Equal(at) {
		t.Errorf("los campos del acuse no se leen del Stored: %+v", s)
	}
	if s.ID != 7 || !s.RecordedAt.Equal(recorded) {
		t.Errorf("Stored = (id %d, %v), quería (7, %v)", s.ID, s.RecordedAt, recorded)
	}
}
