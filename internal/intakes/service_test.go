package intakes_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

const (
	tenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	tenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

// seedStore siembra un store en memoria con una solicitud del tenant A en el
// estado dado.
func seedStore(t *testing.T, status string) *intakes.MemoryStore {
	t.Helper()
	st := intakes.NewMemoryStore()
	st.Add(tenantA, intakes.Intake{
		ID:        "11111111-1111-1111-1111-111111111111",
		ContactID: "contacto-opaco-1",
		SessionID: "sess-a",
		Status:    status,
		Total:     18000,
		CreatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}, intakes.Item{SKU: "torta-v1", Label: "Torta 10-12 porciones", Qty: 1, UnitPrice: 18000})
	return st
}

// TestService_SetStatus_TransiciónVálida persiste el nuevo estado.
func TestService_SetStatus_TransiciónVálida(t *testing.T) {
	svc := intakes.NewService(seedStore(t, intakes.StatusConfirmed))

	got, err := svc.SetStatus(context.Background(), tenantA,
		"11111111-1111-1111-1111-111111111111", intakes.StatusDepositRequested, intakes.NoticeToClient)
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got.Status != intakes.StatusDepositRequested {
		t.Fatalf("status=%q, quiero deposit_requested", got.Status)
	}

	after, err := svc.Get(context.Background(), tenantA, "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("Get tras la transición: %v", err)
	}
	if after.Status != intakes.StatusDepositRequested {
		t.Fatalf("la transición no se persistió: status=%q", after.Status)
	}
}

// TestService_SetStatus_ClosedLegado: una fila que el cart cerró como `closed`
// transiciona como si fuera `confirmed` — sin migrar el dato.
func TestService_SetStatus_ClosedLegado(t *testing.T) {
	svc := intakes.NewService(seedStore(t, intakes.StatusClosedLegacy))

	got, err := svc.SetStatus(context.Background(), tenantA,
		"11111111-1111-1111-1111-111111111111", intakes.StatusSettled, intakes.NoticeToClient)
	if err != nil {
		t.Fatalf("SetStatus sobre una fila legada `closed`: %v", err)
	}
	if got.Status != intakes.StatusSettled {
		t.Fatalf("status=%q, quiero settled", got.Status)
	}
}

// TestService_SetStatus_Inválida devuelve *TransitionError con el estado ACTUAL y
// los destinos permitidos (el cuerpo del 422).
func TestService_SetStatus_Inválida(t *testing.T) {
	svc := intakes.NewService(seedStore(t, intakes.StatusClosedLegacy))

	_, err := svc.SetStatus(context.Background(), tenantA,
		"11111111-1111-1111-1111-111111111111", intakes.StatusOpen, intakes.NoticeToClient)
	var invalid *intakes.TransitionError
	if !errors.As(err, &invalid) {
		t.Fatalf("err=%v, quiero *TransitionError", err)
	}
	if invalid.From != intakes.StatusConfirmed {
		t.Fatalf("From=%q, quiero confirmed (el `closed` se reporta normalizado)", invalid.From)
	}
	want := []string{"cancelled", "deposit_requested", "pending_approval", "settled"}
	if !slices.Equal(invalid.Allowed, want) {
		t.Fatalf("Allowed=%v, quiero %v", invalid.Allowed, want)
	}
}

// TestService_SetStatus_EstadoDesconocido cae por el mismo camino que una
// transición inválida: el llamante recibe dónde está y adónde puede ir.
func TestService_SetStatus_EstadoDesconocido(t *testing.T) {
	svc := intakes.NewService(seedStore(t, intakes.StatusOpen))

	_, err := svc.SetStatus(context.Background(), tenantA,
		"11111111-1111-1111-1111-111111111111", "en_camino", intakes.NoticeToClient)
	var invalid *intakes.TransitionError
	if !errors.As(err, &invalid) {
		t.Fatalf("err=%v, quiero *TransitionError", err)
	}
	if invalid.From != intakes.StatusOpen || invalid.To != "en_camino" {
		t.Fatalf("TransitionError=%+v, quiero from=open to=en_camino", invalid)
	}
}

// TestService_SetStatus_CrossTenant: el tenant B no puede transicionar una
// solicitud del tenant A, y lo que recibe es "no existe" (INV-8) — no un error que
// confirme que existe.
func TestService_SetStatus_CrossTenant(t *testing.T) {
	svc := intakes.NewService(seedStore(t, intakes.StatusOpen))

	_, err := svc.SetStatus(context.Background(), tenantB,
		"11111111-1111-1111-1111-111111111111", intakes.StatusConfirmed, intakes.NoticeToClient)
	if !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("err=%v, quiero ErrNotFound", err)
	}
}

// storeQueMuta simula la carrera de dos operadores: entre el Get que valida la
// transición y el UpdateStatus que la aplica, otro cambió el estado. El
// compare-and-swap lo detecta y NO escribe.
type storeQueMuta struct {
	*intakes.MemoryStore
	mutado bool
}

func (s *storeQueMuta) Get(ctx context.Context, tenantID, intakeID string) (intakes.Detail, error) {
	d, err := s.MemoryStore.Get(ctx, tenantID, intakeID)
	if err == nil && !s.mutado {
		s.mutado = true
		// El otro operador cancela la solicitud justo después de esta lectura.
		if _, uerr := s.UpdateStatus(ctx, tenantID, intakeID,
			intakes.StatusCancelled, intakes.StoredVariants(d.Status)); uerr != nil {
			return intakes.Detail{}, uerr
		}
	}
	return d, err
}

func TestService_SetStatus_Conflicto(t *testing.T) {
	svc := intakes.NewService(&storeQueMuta{MemoryStore: seedStore(t, intakes.StatusOpen)})

	_, err := svc.SetStatus(context.Background(), tenantA,
		"11111111-1111-1111-1111-111111111111", intakes.StatusConfirmed, intakes.NoticeToClient)
	if !errors.Is(err, intakes.ErrConflict) {
		t.Fatalf("err=%v, quiero ErrConflict", err)
	}
}

// TestService_List_SaneaLaPaginación: la cota superior no es cosmética — sin ella
// un solo GET puede pedir la tabla entera.
func TestService_List_SaneaLaPaginación(t *testing.T) {
	svc := intakes.NewService(seedStore(t, intakes.StatusOpen))

	page, err := svc.List(context.Background(), tenantA, intakes.Filter{Page: 0, PageSize: 100000})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.Page != 1 {
		t.Fatalf("page=%d, quiero 1 (página 0 no existe)", page.Page)
	}
	if page.PageSize != intakes.MaxPageSize {
		t.Fatalf("page_size=%d, quiero %d (cota superior)", page.PageSize, intakes.MaxPageSize)
	}
}
