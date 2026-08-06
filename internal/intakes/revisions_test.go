package intakes_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// TestCartRevisionPayload_FormaCanonica: el payload de la revisión del carrito
// lleva la versión DENTRO del blob y las líneas con las claves del contrato.
func TestCartRevisionPayload_FormaCanonica(t *testing.T) {
	raw, err := intakes.CartRevisionPayload(5000, []intakes.RevisionLine{
		{SKU: "emp-pino", Label: "Empanada de pino", Qty: 2, UnitPrice: 2500},
	})
	if err != nil {
		t.Fatalf("CartRevisionPayload: %v", err)
	}

	var got struct {
		Version int     `json:"version"`
		Total   float64 `json:"total"`
		Items   []struct {
			SKU       string  `json:"sku"`
			Label     string  `json:"label"`
			Qty       int     `json:"qty"`
			UnitPrice float64 `json:"unit_price"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload no es JSON válido (%s): %v", raw, err)
	}
	if got.Version != intakes.RevisionPayloadVersion {
		t.Errorf("version: got %d, want %d", got.Version, intakes.RevisionPayloadVersion)
	}
	if got.Total != 5000 {
		t.Errorf("total: got %v, want 5000", got.Total)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items: got %d, want 1", len(got.Items))
	}
	it := got.Items[0]
	if it.SKU != "emp-pino" || it.Label != "Empanada de pino" || it.Qty != 2 || it.UnitPrice != 2500 {
		t.Errorf("línea congelada mal: %+v", it)
	}
}

// TestCartRevisionPayload_SinLineasEsListaVacia: un cierre sin líneas serializa
// `[]`, no `null`. Quien lea el payload debe distinguir "se cerró sin líneas" de
// "aquí no se registró la lista", y `null` no dice cuál de las dos es.
func TestCartRevisionPayload_SinLineasEsListaVacia(t *testing.T) {
	raw, err := intakes.CartRevisionPayload(0, nil)
	if err != nil {
		t.Fatalf("CartRevisionPayload: %v", err)
	}
	if !strings.Contains(string(raw), `"items":[]`) {
		t.Errorf("items debería ser [] y no null: %s", raw)
	}
}

// TestMemoryStore_InsertRevision_NumeraDesdeUno: el correlativo es POR SOLICITUD y
// empieza en 1; dos solicitudes distintas no comparten numeración.
func TestMemoryStore_InsertRevision_NumeraDesdeUno(t *testing.T) {
	st := intakes.NewMemoryStore()
	ctx := context.Background()
	payload := json.RawMessage(`{"version":1}`)

	for i, want := range []int{1, 2, 3} {
		got, err := st.InsertRevision(ctx, intakes.Revision{
			IntakeID: "solicitud-a", Kind: intakes.RevisionKindCart,
			Payload: payload, CreatedBy: intakes.RevisionBySystem,
		})
		if err != nil {
			t.Fatalf("InsertRevision #%d: %v", i+1, err)
		}
		if got.RevisionNo != want {
			t.Errorf("revision_no #%d: got %d, want %d", i+1, got.RevisionNo, want)
		}
		if got.CreatedAt.IsZero() {
			t.Errorf("revisión #%d sin fecha", i+1)
		}
	}

	otra, err := st.InsertRevision(ctx, intakes.Revision{
		IntakeID: "solicitud-b", Kind: intakes.RevisionKindCart, Payload: payload,
	})
	if err != nil {
		t.Fatalf("InsertRevision en otra solicitud: %v", err)
	}
	if otra.RevisionNo != 1 {
		t.Errorf("la numeración es por solicitud: got %d, want 1", otra.RevisionNo)
	}
}

// TestMemoryStore_InsertRevision_RechazaPayloadVacio: una revisión sin payload es
// un relato que afirma "aquí no pasó nada" sobre un acto que sí ocurrió. No se
// sustituye por un `{}` silencioso.
func TestMemoryStore_InsertRevision_RechazaPayloadVacio(t *testing.T) {
	st := intakes.NewMemoryStore()

	_, err := st.InsertRevision(context.Background(), intakes.Revision{
		IntakeID: "solicitud-a", Kind: intakes.RevisionKindCart,
	})
	if !errors.Is(err, intakes.ErrEmptyRevisionPayload) {
		t.Fatalf("error: got %v, want ErrEmptyRevisionPayload", err)
	}
	if revs := st.Revisions("solicitud-a"); len(revs) != 0 {
		t.Errorf("no debería haber escrito nada: %d revisiones", len(revs))
	}
}

// TestMemoryStore_Get_DevuelveRevisiones: el detalle publica las revisiones de la
// solicitud, que es lo que hace consultable la revisión 1 del cierre de carrito.
func TestMemoryStore_Get_DevuelveRevisiones(t *testing.T) {
	st := intakes.NewMemoryStore()
	ctx := context.Background()
	st.Add("tenant-1", intakes.Intake{ID: "sol-1", Status: intakes.StatusClosedLegacy})

	if _, err := st.InsertRevision(ctx, intakes.Revision{
		IntakeID: "sol-1", Kind: intakes.RevisionKindCart,
		Payload:   json.RawMessage(`{"version":1,"total":5000,"items":[]}`),
		CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("InsertRevision: %v", err)
	}

	detail, err := st.Get(ctx, "tenant-1", "sol-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.Revisions) != 1 {
		t.Fatalf("revisiones: got %d, want 1", len(detail.Revisions))
	}
	rev := detail.Revisions[0]
	if rev.RevisionNo != 1 || rev.Kind != intakes.RevisionKindCart || rev.CreatedBy != intakes.RevisionBySystem {
		t.Errorf("revisión publicada mal: %+v", rev)
	}
}
