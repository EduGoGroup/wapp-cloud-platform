// Package receiptshelpertest es la suite de contrato del puerto receipts.Store y su doble en
// memoria, Memoria (D-F3-1: el doble vivía en el paquete de producción viejo, memory.go). Ningún
// código de producción lo importa: arrastra "testing".
//
//   - contrato.go: la entrada. Montaje, ContratoStore y la tabla de casos.
//   - memoria.go: Memoria, el Store en memoria que usan los tests de los consumidores del puerto.
//
// La suite la corren las dos implementaciones del puerto: Memoria en unitario (memoria_test.go) y
// receipts.PostgresStore en los procesos de F9, con el arnés de testcontainers (F3-05).
//
// Nuevo: no tiene fichero viejo. Los casos salen de plan/F3-edge/diseno.md §2 y de los tests
// viejos de internal/receipts @ 8896f13 (TestMemoryStore_Idempotent,
// TestPostgresStore_SaveIdempotentAndList), leídos, no portados.
package receiptshelpertest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/receipts"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso: ContratoStore llama a
// nuevo una vez por caso.
type Montaje struct {
	// Store es la implementación bajo prueba.
	Store receipts.Store
	// SessionA y SessionB son dos session_id distintos, no vacíos y SIN acuses guardados. El
	// acuse no porta tenant y la tabla no tiene claves foráneas: con Postgres basta con que sean
	// únicos por caso (la base se comparte entre casos).
	SessionA, SessionB string
}

// ContratoStore ejecuta las promesas de receipts.Store contra la implementación que devuelve
// nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta nada.
//
// El puerto deja escribir (Save) y observar (List) todo lo que la suite necesita: el Montaje no
// trae ni siembra ni observador.
//
// Lo que la suite NO afirma, a propósito, porque las dos implementaciones divergen:
//
//   - el ReceiptAt de un acuse guardado con ReceiptAt cero: Memoria devuelve el cero y
//     receipts.PostgresStore la época Unix (COALESCE(receipt_at, 'epoch'));
//   - que refrescar un acuse lo suba al primer puesto de List: la marca de persistencia de la
//     Memoria es el reloj del proceso y la de Postgres el now() de la sentencia, y dos marcas
//     iguales se desempatan por id, que el refresco no cambia. Solo se afirma que la marca no
//     retrocede.
//
// Los instantes de la suite van al segundo y en UTC, y se comparan con Equal: Postgres guarda
// microsegundos y devuelve la zona de la sesión.
func ContratoStore(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("receiptshelpertest.ContratoStore: nuevo es nil; hace falta una función que devuelva un Montaje")
	}
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateMontaje(t, m)
			c.run(t, m)
		})
	}
}

// contractCase es una promesa del puerto: su nombre (el del t.Run) y la función que la afirma.
type contractCase struct {
	name string
	run  func(t *testing.T, m Montaje)
}

// cases es la tabla de la suite. El comentario de cada fila es la promesa que fija.
func cases() []contractCase {
	return []contractCase{
		{"Save_ThenList_ReturnsTheStoredRow", caseSaveThenList},               // lo guardado se lee entero
		{"Save_SameKey_DoesNotDuplicate", caseSameKeyDoesNotDuplicate},        // idempotente por (sesión, mensaje, estado)
		{"Save_SameKey_RefreshesTheRow", caseSameKeyRefreshes},                // refresca command_id y receipt_at
		{"Save_DeliveredAndRead_AreTwoRows", caseDeliveredAndRead},            // el estado está en la clave
		{"Save_SameMessageInAnotherSession_IsAnotherRow", caseOtherSession},   // la sesión está en la clave
		{"Save_ConcurrentSameKey_OneRow", caseConcurrentSameKey},              // idempotente también en paralelo
		{"List_UnknownSession_EmptyWithoutError", caseUnknownSession},         // vacío no es error
		{"List_NewestFirst", caseNewestFirst},                                 // más recientes primero
		{"List_LimitAndOffset_Paginate", casePagination},                      // paginado
		{"List_NonPositiveLimit_Is100", caseDefaultLimit},                     // limit <= 0 ⇒ 100
		{"List_NegativeOffset_IsZero", caseNegativeOffset},                    // offset < 0 ⇒ 0
		{"List_OffsetBeyondTheEnd_EmptyWithoutError", caseOffsetBeyondTheEnd}, // pasarse no es error
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Store == nil:
		t.Fatal("Montaje.Store es nil")
	case m.SessionA == "" || m.SessionB == "":
		t.Fatalf("Montaje: SessionA (%q) y SessionB (%q) no pueden ser vacíos", m.SessionA, m.SessionB)
	case m.SessionA == m.SessionB:
		t.Fatalf("Montaje: SessionA y SessionB son el mismo (%q); deben ser distintos", m.SessionA)
	}
	for _, session := range []string{m.SessionA, m.SessionB} {
		if got := list(t, m, session, 0, 0); len(got) != 0 {
			t.Fatalf("Montaje: la sesión %q trae %d acuses; tiene que venir sin ninguno", session, len(got))
		}
	}
}

// receiptAt es el instante de acuse de la suite: al segundo y en UTC.
var receiptAt = time.Unix(1_700_000_000, 0).UTC()

func caseSaveThenList(t *testing.T, m Montaje) {
	want := receipts.Receipt{
		SessionID: m.SessionA, CommandID: "cmd-1", MessageID: "msg-1",
		Status: receipts.StatusDelivered, ReceiptAt: receiptAt,
	}
	save(t, m, want)
	got := list(t, m, m.SessionA, 10, 0)
	if len(got) != 1 {
		t.Fatalf("List tras un Save: %d filas, quería 1", len(got))
	}
	row := got[0]
	if row.SessionID != want.SessionID || row.CommandID != want.CommandID ||
		row.MessageID != want.MessageID || row.Status != want.Status {
		t.Errorf("fila leída = %+v, quería los campos de %+v", row.Receipt, want)
	}
	if !row.ReceiptAt.Equal(want.ReceiptAt) {
		t.Errorf("ReceiptAt = %v, quería %v", row.ReceiptAt, want.ReceiptAt)
	}
	if row.ID <= 0 {
		t.Errorf("ID = %d, quería un id positivo", row.ID)
	}
	if row.RecordedAt.IsZero() {
		t.Error("RecordedAt es cero: la fila persistida lleva la marca de la nube")
	}
}

func caseSameKeyDoesNotDuplicate(t *testing.T, m Montaje) {
	r := receipts.Receipt{SessionID: m.SessionA, CommandID: "cmd-1", MessageID: "msg-1", Status: receipts.StatusDelivered}
	for range 3 {
		save(t, m, r)
	}
	if got := list(t, m, m.SessionA, 10, 0); len(got) != 1 {
		t.Fatalf("el mismo acuse tres veces: %d filas, quería 1 (Save es idempotente)", len(got))
	}
}

func caseSameKeyRefreshes(t *testing.T, m Montaje) {
	r := receipts.Receipt{
		SessionID: m.SessionA, CommandID: "cmd-old", MessageID: "msg-1",
		Status: receipts.StatusRead, ReceiptAt: receiptAt,
	}
	save(t, m, r)
	before := list(t, m, m.SessionA, 10, 0)
	if len(before) != 1 {
		t.Fatalf("List tras el primer Save: %d filas, quería 1", len(before))
	}
	r.CommandID = "cmd-new"
	r.ReceiptAt = receiptAt.Add(time.Minute)
	save(t, m, r)
	after := list(t, m, m.SessionA, 10, 0)
	if len(after) != 1 {
		t.Fatalf("List tras repetir el acuse: %d filas, quería 1", len(after))
	}
	if after[0].ID != before[0].ID {
		t.Errorf("el refresco cambió el id de %d a %d: la fila es la misma", before[0].ID, after[0].ID)
	}
	if after[0].CommandID != "cmd-new" {
		t.Errorf("CommandID = %q, quería %q: el refresco lo reemplaza", after[0].CommandID, "cmd-new")
	}
	if !after[0].ReceiptAt.Equal(r.ReceiptAt) {
		t.Errorf("ReceiptAt = %v, quería %v: el refresco lo reemplaza", after[0].ReceiptAt, r.ReceiptAt)
	}
	if after[0].RecordedAt.Before(before[0].RecordedAt) {
		t.Errorf("RecordedAt retrocedió de %v a %v", before[0].RecordedAt, after[0].RecordedAt)
	}
}

func caseDeliveredAndRead(t *testing.T, m Montaje) {
	save(t, m, receipts.Receipt{SessionID: m.SessionA, MessageID: "msg-1", Status: receipts.StatusDelivered})
	save(t, m, receipts.Receipt{SessionID: m.SessionA, MessageID: "msg-1", Status: receipts.StatusRead})
	got := list(t, m, m.SessionA, 10, 0)
	if len(got) != 2 {
		t.Fatalf("delivered y read del mismo mensaje: %d filas, quería 2", len(got))
	}
	if got[0].Status == got[1].Status {
		t.Errorf("las dos filas tienen el estado %q: quería una delivered y una read", got[0].Status)
	}
	if got[0].ID == got[1].ID {
		t.Errorf("las dos filas comparten el id %d", got[0].ID)
	}
}

func caseOtherSession(t *testing.T, m Montaje) {
	save(t, m, receipts.Receipt{SessionID: m.SessionA, CommandID: "cmd-a", MessageID: "msg-1", Status: receipts.StatusDelivered})
	save(t, m, receipts.Receipt{SessionID: m.SessionB, CommandID: "cmd-b", MessageID: "msg-1", Status: receipts.StatusDelivered})
	for session, command := range map[string]string{m.SessionA: "cmd-a", m.SessionB: "cmd-b"} {
		got := list(t, m, session, 10, 0)
		if len(got) != 1 {
			t.Fatalf("List(%q): %d filas, quería 1 (cada sesión ve solo lo suyo)", session, len(got))
		}
		if got[0].SessionID != session || got[0].CommandID != command {
			t.Errorf("List(%q) devolvió la fila de (%q, %q)", session, got[0].SessionID, got[0].CommandID)
		}
	}
}

// caseConcurrentSameKey guarda el mismo acuse desde varias goroutines: bajo -race, un Store sin
// proteger se ve aquí, y la clave única tiene que seguir dando una sola fila.
func caseConcurrentSameKey(t *testing.T, m Montaje) {
	const writers = 8
	r := receipts.Receipt{SessionID: m.SessionA, MessageID: "msg-1", Status: receipts.StatusDelivered}
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Go(func() { errs <- m.Store.Save(context.Background(), r) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Save en paralelo: error inesperado %v", err)
		}
	}
	if got := list(t, m, m.SessionA, 10, 0); len(got) != 1 {
		t.Errorf("el mismo acuse desde %d goroutines: %d filas, quería 1", writers, len(got))
	}
}

func caseUnknownSession(t *testing.T, m Montaje) {
	save(t, m, receipts.Receipt{SessionID: m.SessionA, MessageID: "msg-1", Status: receipts.StatusDelivered})
	if got := list(t, m, m.SessionB, 10, 0); len(got) != 0 {
		t.Errorf("List de una sesión sin acuses: %d filas, quería 0", len(got))
	}
}

func caseNewestFirst(t *testing.T, m Montaje) {
	saveMessages(t, m, m.SessionA, 3)
	requireMessages(t, list(t, m, m.SessionA, 10, 0), "msg-003", "msg-002", "msg-001")
}

func casePagination(t *testing.T, m Montaje) {
	saveMessages(t, m, m.SessionA, 5)
	requireMessages(t, list(t, m, m.SessionA, 2, 0), "msg-005", "msg-004")
	requireMessages(t, list(t, m, m.SessionA, 2, 2), "msg-003", "msg-002")
	requireMessages(t, list(t, m, m.SessionA, 2, 4), "msg-001")
}

// caseDefaultLimit guarda 101 acuses: con limit <= 0 vuelven 100, no todos ni ninguno.
func caseDefaultLimit(t *testing.T, m Montaje) {
	saveMessages(t, m, m.SessionA, 101)
	for _, limit := range []int{0, -1} {
		got := list(t, m, m.SessionA, limit, 0)
		if len(got) != 100 {
			t.Fatalf("List con limit=%d sobre 101 acuses: %d filas, quería 100", limit, len(got))
		}
		if got[0].MessageID != "msg-101" || got[99].MessageID != "msg-002" {
			t.Errorf("List con limit=%d: va de %q a %q, quería de msg-101 a msg-002",
				limit, got[0].MessageID, got[99].MessageID)
		}
	}
}

func caseNegativeOffset(t *testing.T, m Montaje) {
	saveMessages(t, m, m.SessionA, 3)
	requireMessages(t, list(t, m, m.SessionA, 2, -5), "msg-003", "msg-002")
}

func caseOffsetBeyondTheEnd(t *testing.T, m Montaje) {
	saveMessages(t, m, m.SessionA, 2)
	for _, offset := range []int{2, 50} {
		if got := list(t, m, m.SessionA, 10, offset); len(got) != 0 {
			t.Errorf("List con offset=%d sobre 2 acuses: %d filas, quería 0", offset, len(got))
		}
	}
}

// save guarda r o falla el test.
func save(t *testing.T, m Montaje, r receipts.Receipt) {
	t.Helper()
	if err := m.Store.Save(context.Background(), r); err != nil {
		t.Fatalf("Save(%+v): error inesperado %v", r, err)
	}
}

// saveMessages guarda n acuses delivered de la sesión, msg-001 … msg-n, en ese orden.
func saveMessages(t *testing.T, m Montaje, session string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		save(t, m, receipts.Receipt{
			SessionID: session, MessageID: fmt.Sprintf("msg-%03d", i), Status: receipts.StatusDelivered,
		})
	}
}

// list lee una página de la sesión o falla el test.
func list(t *testing.T, m Montaje, session string, limit, offset int) []receipts.Stored {
	t.Helper()
	got, err := m.Store.List(context.Background(), session, limit, offset)
	if err != nil {
		t.Fatalf("List(%q, %d, %d): error inesperado %v", session, limit, offset, err)
	}
	return got
}

// requireMessages afirma que got trae exactamente esos message_id, en ese orden.
func requireMessages(t *testing.T, got []receipts.Stored, want ...string) {
	t.Helper()
	ids := make([]string, 0, len(got))
	for _, row := range got {
		ids = append(ids, row.MessageID)
	}
	if len(ids) != len(want) {
		t.Fatalf("message_id leídos = %v, quería %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("message_id leídos = %v, quería %v", ids, want)
		}
	}
}
