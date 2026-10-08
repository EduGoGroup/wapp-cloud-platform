//go:build pendiente

package intake

import (
	"context"
	"testing"
	"time"
)

// TestStatuses_AreTheClosedVocabularyOfTheTable: los cinco estados son, byte a byte, los del
// CHECK `intake_jobs_status_check` de la migración 0072. Son texto observable: viajan en SQL.
func TestStatuses_AreTheClosedVocabularyOfTheTable(t *testing.T) {
	got := []string{StatusAggregating, StatusPending, StatusProcessing, StatusDone, StatusFailed}
	want := []string{"aggregating", "pending", "processing", "done", "failed"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("estado %d = %q, quería %q", i, got[i], want[i])
		}
	}
}

// TestWindowKey_Valid_NeedsTheFourColumns: una clave identifica una ventana solo con sus CUATRO
// piezas; las cuatro columnas son NOT NULL. Un blanco no es vacío: aquí no se normaliza nada.
func TestWindowKey_Valid_NeedsTheFourColumns(t *testing.T) {
	full := WindowKey{TenantID: "t", SessionID: "s", ContactID: "c", EventID: "e"}
	cases := []struct {
		name   string
		mutate func(*WindowKey)
		want   bool
	}{
		{"full key", func(*WindowKey) {}, true},
		{"no tenant", func(k *WindowKey) { k.TenantID = "" }, false},
		{"no session", func(k *WindowKey) { k.SessionID = "" }, false},
		{"no contact", func(k *WindowKey) { k.ContactID = "" }, false},
		{"no event", func(k *WindowKey) { k.EventID = "" }, false},
		{"zero value", func(k *WindowKey) { *k = WindowKey{} }, false},
		{"whitespace is not empty", func(k *WindowKey) { k.EventID = " " }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := full
			tc.mutate(&k)
			if got := k.Valid(); got != tc.want {
				t.Errorf("WindowKey%+v.Valid() = %v, quería %v", k, got, tc.want)
			}
		})
	}
}

// TestWindowKey_IsTheFourStringsInIndexOrder: los cuatro campos, en el orden del índice único
// parcial. El arranque convierte entre esta clave y la del paquete viejo campo a campo
// (trampa T-3 de F7): cambiar un nombre o el orden rompe esa conversión.
func TestWindowKey_IsTheFourStringsInIndexOrder(t *testing.T) {
	// Conversión posicional desde un struct anónimo con la MISMA forma: solo compila si los
	// campos, sus tipos y su orden no cambian.
	k := WindowKey(struct{ TenantID, SessionID, ContactID, EventID string }{"t", "s", "c", "e"})
	if k.TenantID != "t" || k.SessionID != "s" || k.ContactID != "c" || k.EventID != "e" {
		t.Errorf("WindowKey = %+v, quería (t, s, c, e) en ese orden", k)
	}
	// Es comparable: los gemelos la usan con == para encontrar la ventana viva.
	if k != (WindowKey{TenantID: "t", SessionID: "s", ContactID: "c", EventID: "e"}) {
		t.Error("dos claves con las mismas cuatro piezas no son iguales")
	}
}

// TestSourceText_Complete_TheThreeOrNothing: el sobre está entero solo con sus TRES piezas. Un
// slice vacío no nil cuenta como ausente.
func TestSourceText_Complete_TheThreeOrNothing(t *testing.T) {
	cases := []struct {
		name string
		env  SourceText
		want bool
	}{
		{"the three", SourceText{Enc: []byte("e"), DEK: []byte("d"), KEKID: "k"}, true},
		{"zero value", SourceText{}, false},
		{"no enc", SourceText{DEK: []byte("d"), KEKID: "k"}, false},
		{"no dek", SourceText{Enc: []byte("e"), KEKID: "k"}, false},
		{"no kek id", SourceText{Enc: []byte("e"), DEK: []byte("d")}, false},
		{"empty non-nil enc", SourceText{Enc: []byte{}, DEK: []byte("d"), KEKID: "k"}, false},
		{"empty non-nil dek", SourceText{Enc: []byte("e"), DEK: []byte{}, KEKID: "k"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.env.Complete(); got != tc.want {
				t.Errorf("Complete() = %v, quería %v", got, tc.want)
			}
		})
	}
}

// TestAppendAndOpenJob_CarryNoText: lo que entra por el camino del entrante (Append) y lo que ve
// el barrido (OpenJob) son la clave, instantes y referencias opacas: NINGUNO lleva texto ni
// sobre (D-044.26). Se fija por forma: los literales posicionales solo compilan con
// exactamente estos campos.
func TestAppendAndOpenJob_CarryNoText(t *testing.T) {
	k := WindowKey{TenantID: "t", SessionID: "s", ContactID: "c", EventID: "e"}
	at := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	a := Append{k, at, []string{"wamid.one"}}
	if a.Key != k || !a.MessageTS.Equal(at) || len(a.Refs) != 1 {
		t.Errorf("Append = %+v, quería (clave, instante, referencias)", a)
	}
	j := OpenJob{"job-1", k, at.Add(time.Minute), at}
	if j.ID != "job-1" || j.Key != k || !j.LastActivity.Equal(at.Add(time.Minute)) || !j.CreatedAt.Equal(at) {
		t.Errorf("OpenJob = %+v, quería (id, clave, LastActivity, CreatedAt)", j)
	}
}

// TestJobStore_IsFourOperations_AndBothImplementationsSatisfyIt: el puerto son CUATRO operaciones
// y ninguna más —su tamaño es lo que impide que el sink lea en línea con el mensaje—, y lo
// satisfacen el adaptador y el gemelo.
func TestJobStore_IsFourOperations_AndBothImplementationsSatisfyIt(t *testing.T) {
	// Una interfaz con exactamente estos cuatro métodos es asignable a JobStore y viceversa:
	// añadir un quinto al puerto rompe la segunda asignación.
	type four interface {
		OpenOrAppend(ctx context.Context, a Append) error
		CloseWindow(ctx context.Context, k WindowKey) (bool, error)
		ListAggregating(ctx context.Context, limit int) ([]OpenJob, error)
		PutSourceText(ctx context.Context, k WindowKey, env SourceText) (bool, error)
	}
	var port JobStore = NewMemoryStore(nil)
	var narrow four = port
	port = narrow
	if port == nil {
		t.Fatal("el gemelo no satisface JobStore")
	}
	var _ JobStore = (*Postgres)(nil)
}
