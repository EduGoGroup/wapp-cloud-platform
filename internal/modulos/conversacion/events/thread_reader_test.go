package events

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// TestEntryKind_Vocabulary: los cuatro grados, con los literales de la columna entry_kind.
func TestEntryKind_Vocabulary(t *testing.T) {
	cases := []struct {
		got  EntryKind
		want string
	}{
		{KindMessage, "message"},
		{KindSummary, "summary"},
		{KindDecision, "decision"},
		{KindMessageOutOfTurn, "message_out_of_turn"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("grado = %q, quería %q", c.got, c.want)
		}
	}
}

// sealedRow es una fila de nivel 2 del hilo: el cuerpo cifrado con cipher.
func sealedRow(t *testing.T, cipher *crypto.FieldCipher, seq int, role Role, kind EntryKind, body string) []driver.Value {
	t.Helper()
	enc, dek, kekID, err := cipher.Encrypt(body)
	if err != nil {
		t.Fatalf("cifrar la fila de test: %v", err)
	}
	return []driver.Value{int64(seq), string(role), string(kind), nil, enc, dek, kekID}
}

// clearRow es una fila de nivel 1 del hilo: payload en claro y el sobre a NULL.
func clearRow(seq int, role Role, kind EntryKind, payload string) []driver.Value {
	var p driver.Value
	if payload != "" {
		p = []byte(payload)
	}
	return []driver.Value{int64(seq), string(role), string(kind), p, nil, nil, nil}
}

// TestListThread_ResolvesEveryGradeToText: cada fila sale con su seq, su rol y su grado, y con el
// texto según su NIVEL: el cuerpo descifrado; el render del resumen; "" para una decisión, para un
// payload que no se deja leer como resumen y para una fila sin nada. UNA consulta con el evento y
// el tope.
func TestListThread_ResolvesEveryGradeToText(t *testing.T) {
	cipher := testCipher(t)
	summary := BuildCartSummary(CartState{Lines: []SummaryLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}}})
	body, err := summary.Encode()
	if err != nil {
		t.Fatalf("Summary.Encode: %v", err)
	}
	h := newFakeStoreWith(t, cipher, pgReply{rows: [][]driver.Value{
		sealedRow(t, cipher, 3, RoleClient, KindMessage, "quiero dos cafés"),
		sealedRow(t, cipher, 4, RoleBusiness, KindMessageOutOfTurn, "te recordamos tu seña"),
		clearRow(5, RoleSystem, KindSummary, string(body)),
		clearRow(6, RoleClient, KindDecision, `{"effect":"item_added"}`),
		clearRow(7, RoleClient, KindDecision, `[1,2]`),
		clearRow(8, RoleSystem, KindSummary, ""),
	}})

	thread, err := h.store.ListThread(t.Context(), pgEvent, 20)
	if err != nil {
		t.Fatalf("ListThread: %v", err)
	}
	want := []ThreadEntry{
		{Seq: 3, Role: RoleClient, Kind: KindMessage, Text: "quiero dos cafés"},
		{Seq: 4, Role: RoleBusiness, Kind: KindMessageOutOfTurn, Text: "te recordamos tu seña"},
		{Seq: 5, Role: RoleSystem, Kind: KindSummary, Text: summary.Render()},
		{Seq: 6, Role: RoleClient, Kind: KindDecision, Text: ""},
		{Seq: 7, Role: RoleClient, Kind: KindDecision, Text: ""},
		{Seq: 8, Role: RoleSystem, Kind: KindSummary, Text: ""},
	}
	if len(thread) != len(want) {
		t.Fatalf("ListThread devolvió %d entradas, quería %d: %+v", len(thread), len(want), thread)
	}
	for i := range want {
		if thread[i] != want[i] {
			t.Errorf("entrada %d = %+v, quería %+v", i, thread[i], want[i])
		}
	}
	if want[2].Text == "" || strings.Contains(want[2].Text, "{") {
		t.Errorf("el resumen se entregó como %q, quería su render", want[2].Text)
	}

	stmt := requireStatements(t, h.fake, 1)[0]
	requireArgs(t, "ListThread", stmt, pgEvent, 20)
	for _, clause := range []string{"ORDER BY seq DESC", "LIMIT $2", "ORDER BY seq\n"} {
		if !strings.Contains(stmt.query, clause) {
			t.Errorf("la lectura del hilo no contiene %q (recorta por el principio y devuelve en orden):\n%s", clause, stmt.query)
		}
	}
	if strings.Contains(stmt.query, "WHERE entry_kind") || strings.Contains(stmt.query, "origin") {
		t.Errorf("la lectura del hilo filtra por grado o por origen:\n%s", stmt.query)
	}
}

// TestListThread_Guards_NeverReachTheDatabase: store nil, store sin base, evento vacío y tope <= 0
// devuelven (nil, nil); sin cipher, ErrNoCipher. Las guardas de «nada que leer» van antes que la
// del cipher.
func TestListThread_Guards_NeverReachTheDatabase(t *testing.T) {
	var none *Store
	if thread, err := none.ListThread(t.Context(), pgEvent, 10); thread != nil || err != nil {
		t.Errorf("store nil: (%v, %v), quería (nil, nil)", thread, err)
	}
	if thread, err := NewStore(nil, testCipher(t)).ListThread(t.Context(), pgEvent, 10); thread != nil || err != nil {
		t.Errorf("store sin base: (%v, %v), quería (nil, nil)", thread, err)
	}

	bare := newFakeStoreWith(t, nil)
	cases := []struct {
		name  string
		id    string
		limit int
		want  error
	}{
		{"empty id", "", 10, nil},
		{"zero limit", pgEvent, 0, nil},
		{"negative limit", pgEvent, -1, nil},
		{"no cipher", pgEvent, 10, ErrNoCipher},
	}
	for _, c := range cases {
		thread, err := bare.store.ListThread(t.Context(), c.id, c.limit)
		if thread != nil || (c.want == nil) != (err == nil) || (c.want != nil && !exactly(err, c.want)) {
			t.Errorf("%s: (%v, %v), quería (nil, %v)", c.name, thread, err, c.want)
		}
	}
	requireStatements(t, bare.fake, 0)
}

// TestListThread_Failures_NameTheEventAndNeverTheBody: cada fallo con su texto; una entrada que no
// se descifra aborta la lectura nombrando el seq y el evento, sin hilo a medias.
func TestListThread_Failures_NameTheEventAndNeverTheBody(t *testing.T) {
	cipher := testCipher(t)
	good := sealedRow(t, cipher, 1, RoleClient, KindMessage, "texto del cliente")
	broken := sealedRow(t, cipher, 2, RoleClient, KindMessage, "texto del cliente")
	enc, ok := broken[4].([]byte)
	if !ok {
		t.Fatalf("la fila de test no trae el cuerpo cifrado en su quinta columna: %T", broken[4])
	}
	broken[4] = append([]byte("basura"), enc...)

	cases := []struct {
		name   string
		reply  pgReply
		prefix string
		cause  error
	}{
		{"query", pgFails(), "events: leer el hilo del evento " + pgEvent + ": ", errPgBoom},
		{"scan", pgOne("solo", "dos"), "events: scan de una entrada del hilo del evento " + pgEvent + ": ", nil},
		{"undecryptable", pgReply{rows: [][]driver.Value{good, broken}}, "events: resolver la entrada 2 del hilo del evento " + pgEvent + ": ", nil},
		{"iteration", pgReply{rows: [][]driver.Value{good}, endErr: errPgBoom}, "events: iterar el hilo del evento " + pgEvent + ": ", errPgBoom},
		{"close", pgReply{rows: [][]driver.Value{good}, closeErr: errPgBoom}, "events: cerrar el hilo del evento " + pgEvent + ": ", errPgBoom},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFakeStoreWith(t, cipher, c.reply)
			thread, err := h.store.ListThread(t.Context(), pgEvent, 10)
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) || (c.cause != nil && !errors.Is(err, c.cause)) {
				t.Fatalf("err = %v, quería el prefijo %q", err, c.prefix)
			}
			if thread != nil {
				t.Errorf("hilo = %+v, quería nil", thread)
			}
			if strings.Contains(err.Error(), "texto del cliente") {
				t.Errorf("el error cita el cuerpo: %q", err.Error())
			}
		})
	}
}

// pastedRow es una fila de la consulta de transcripciones pegadas.
func pastedRow(t *testing.T, cipher *crypto.FieldCipher, seq int, body string) []driver.Value {
	t.Helper()
	enc, dek, kekID, err := cipher.Encrypt(body)
	if err != nil {
		t.Fatalf("cifrar la fila de test: %v", err)
	}
	return []driver.Value{int64(seq), enc, dek, kekID}
}

// TestListPastedByOwner_DecryptsInOrder: UNA consulta con el evento y el origen owner_pasted, que
// acota al grado message y no lleva tope; los textos salen descifrados, en el orden de la base.
func TestListPastedByOwner_DecryptsInOrder(t *testing.T) {
	cipher := testCipher(t)
	h := newFakeStoreWith(t, cipher, pgReply{rows: [][]driver.Value{
		pastedRow(t, cipher, 2, "dos empanadas"),
		pastedRow(t, cipher, 5, "y una gaseosa"),
	}}, pgReply{})

	pasted, err := h.store.ListPastedByOwner(t.Context(), pgEvent)
	if err != nil || len(pasted) != 2 || pasted[0] != "dos empanadas" || pasted[1] != "y una gaseosa" {
		t.Fatalf("ListPastedByOwner = (%v, %v)", pasted, err)
	}
	if pasted, err = h.store.ListPastedByOwner(t.Context(), pgEvent); err != nil || len(pasted) != 0 {
		t.Errorf("sin filas: (%v, %v), quería ninguna", pasted, err)
	}

	stmt := requireStatements(t, h.fake, 2)[0]
	requireArgs(t, "ListPastedByOwner", stmt, pgEvent, "owner_pasted")
	if !strings.Contains(stmt.query, "entry_kind = 'message'") || !strings.Contains(stmt.query, "origin = $2") || strings.Contains(stmt.query, "LIMIT") {
		t.Errorf("la consulta no acota por grado y origen, o lleva tope:\n%s", stmt.query)
	}
}

// TestListPastedByOwner_GuardsAndFailures: las mismas guardas que ListThread (sin tope que mirar) y
// cada fallo con su texto, nombrando el evento y el seq y nunca el cuerpo.
func TestListPastedByOwner_GuardsAndFailures(t *testing.T) {
	var none *Store
	if pasted, err := none.ListPastedByOwner(t.Context(), pgEvent); pasted != nil || err != nil {
		t.Errorf("store nil: (%v, %v), quería (nil, nil)", pasted, err)
	}
	if pasted, err := NewStore(nil, testCipher(t)).ListPastedByOwner(t.Context(), pgEvent); pasted != nil || err != nil {
		t.Errorf("store sin base: (%v, %v), quería (nil, nil)", pasted, err)
	}
	bare := newFakeStoreWith(t, nil)
	if pasted, err := bare.store.ListPastedByOwner(t.Context(), ""); pasted != nil || err != nil {
		t.Errorf("evento vacío: (%v, %v), quería (nil, nil)", pasted, err)
	}
	if pasted, err := bare.store.ListPastedByOwner(t.Context(), pgEvent); pasted != nil || !exactly(err, ErrNoCipher) {
		t.Errorf("sin cipher: (%v, %v), quería (nil, ErrNoCipher)", pasted, err)
	}
	requireStatements(t, bare.fake, 0)

	cipher := testCipher(t)
	good := pastedRow(t, cipher, 1, "texto del cliente")
	broken := pastedRow(t, cipher, 4, "texto del cliente")
	broken[2] = []byte("esto no es una DEK envuelta")
	cases := []struct {
		name   string
		reply  pgReply
		prefix string
	}{
		{"query", pgFails(), "events: leer las transcripciones pegadas del evento " + pgEvent + ": "},
		{"scan", pgOne("solo", "dos"), "events: scan de una transcripción pegada del evento " + pgEvent + ": "},
		{"undecryptable", pgReply{rows: [][]driver.Value{good, broken}}, "events: descifrar la transcripción 4 del evento " + pgEvent + ": "},
		{"iteration", pgReply{rows: [][]driver.Value{good}, endErr: errPgBoom}, "events: iterar las transcripciones pegadas del evento " + pgEvent + ": "},
		{"close", pgReply{rows: [][]driver.Value{good}, closeErr: errPgBoom}, "events: cerrar las transcripciones pegadas del evento " + pgEvent + ": "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFakeStoreWith(t, cipher, c.reply)
			pasted, err := h.store.ListPastedByOwner(t.Context(), pgEvent)
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) || pasted != nil {
				t.Fatalf("(%v, %v), quería (nil, %q…)", pasted, err, c.prefix)
			}
			if strings.Contains(err.Error(), "texto del cliente") {
				t.Errorf("el error cita el cuerpo: %q", err.Error())
			}
		})
	}
}

// TestThreadEntry_RoundTripsAsJSONFreeValue: una entrada del hilo es un valor comparable con sus
// cuatro campos; no lleva el origen (el LLM no lo ve).
func TestThreadEntry_RoundTripsAsJSONFreeValue(t *testing.T) {
	e := ThreadEntry{Seq: 2, Role: RoleBusiness, Kind: KindMessageOutOfTurn, Text: "hola"}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("serializar la entrada: %v", err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "origin") {
		t.Errorf("la entrada del hilo publica el origen: %s", raw)
	}
	if e != (ThreadEntry{Seq: 2, Role: RoleBusiness, Kind: KindMessageOutOfTurn, Text: "hola"}) {
		t.Error("dos entradas iguales no comparan iguales")
	}
}
