package receiptshelpertest

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/receipts"
)

// TestMemoria_Contrato corre la suite del puerto contra la Memoria.
func TestMemoria_Contrato(t *testing.T) {
	ContratoStore(t, func(t *testing.T) Montaje {
		t.Helper()
		return Montaje{Store: NewMemoria(), SessionA: "session-a", SessionB: "session-b"}
	})
}

// TestMemoria_ZeroReceiptAtStaysZero fija la diferencia documentada con Postgres: un acuse sin
// instante informado vuelve con ReceiptAt cero, no con la época Unix.
func TestMemoria_ZeroReceiptAtStaysZero(t *testing.T) {
	ctx := context.Background()
	m := NewMemoria()
	if err := m.Save(ctx, receipts.Receipt{SessionID: "s", MessageID: "m", Status: receipts.StatusRead}); err != nil {
		t.Fatalf("Save: error inesperado %v", err)
	}
	got, err := m.List(ctx, "s", 10, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("List = (%d filas, %v), quería (1, nil)", len(got), err)
	}
	if !got[0].ReceiptAt.IsZero() {
		t.Errorf("ReceiptAt = %v, quería el cero", got[0].ReceiptAt)
	}
}

// TestMemoria_ListReturnsNilWhenEmpty: sin filas (o con el offset pasado) la lista es nil, como
// la del adaptador Postgres.
func TestMemoria_ListReturnsNilWhenEmpty(t *testing.T) {
	got, err := NewMemoria().List(context.Background(), "s", 10, 0)
	if err != nil || got != nil {
		t.Errorf("List sobre la Memoria vacía = (%v, %v), quería (nil, nil)", got, err)
	}
}
