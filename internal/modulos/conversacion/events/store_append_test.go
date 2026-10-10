//go:build pendiente

package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Aserción de compilación (se conserva del paquete viejo): el Store satisface el puerto estrecho
// de quien abandona un evento.
var _ SummaryAppender = (*Store)(nil)

const (
	testSummaryBody  = `{"kind":"cart","lines":[{"sku":"CAFE","label":"Café","qty":2,"unit_price":2.5}]}`
	testDecisionBody = `{"effect":"item_added","sku":"CAFE"}`
	testClientText   = "quiero dos cafés sin azúcar"
)

// exactly dice si err es el centinela A SECAS: el mismo error, sin envolver ni añadirle texto.
func exactly(err, sentinel error) bool {
	return errors.Is(err, sentinel) && err.Error() == sentinel.Error()
}

// requireSealedArgs exige los ocho argumentos de una entrada de NIVEL 2: payload NULL y el sobre
// de tres piezas, que descifrado con el cipher del store da el cuerpo. El texto no viaja en claro
// en ningún argumento.
func requireSealedArgs(t *testing.T, what string, stmt pgStatement, cipher *crypto.FieldCipher, role, kind, origin, body string) {
	t.Helper()
	if len(stmt.args) != 8 {
		t.Fatalf("%s: %d argumentos, quería 8: %v", what, len(stmt.args), stmt.args)
	}
	marks := strings.Join([]string{argText(stmt.args[0]), argText(stmt.args[1]), argText(stmt.args[2]), argText(stmt.args[7])}, " ")
	if want := strings.Join([]string{pgEvent, role, kind, origin}, " "); marks != want {
		t.Errorf("%s: (evento, rol, grado, origen) = %s, quería %s", what, marks, want)
	}
	if stmt.args[3] != nil {
		t.Errorf("%s: el payload viajó como %v, quería NULL", what, stmt.args[3])
	}
	enc, dek, kekID := sealedEnvelope(t, what, stmt)
	if bytes.Contains(enc, []byte(body)) || bytes.Contains(dek, []byte(body)) {
		t.Errorf("%s: el cuerpo viaja EN CLARO dentro del sobre", what)
	}
	plain, err := cipher.Decrypt(enc, dek, kekID)
	if err != nil || plain != body {
		t.Errorf("%s: el sobre descifra a (%q, %v), quería %q", what, plain, err, body)
	}
}

// sealedEnvelope saca de la sentencia las tres piezas del sobre, que tienen que venir las tres y no
// vacías.
func sealedEnvelope(t *testing.T, what string, stmt pgStatement) (enc, dek []byte, kekID string) {
	t.Helper()
	enc, okEnc := stmt.args[4].([]byte)
	dek, okDEK := stmt.args[5].([]byte)
	kekID, okKEK := stmt.args[6].(string)
	if !okEnc || !okDEK || !okKEK || len(enc) == 0 || len(dek) == 0 || kekID == "" {
		t.Fatalf("%s: el sobre viajó como (%T, %T, %T), quería (bytes, bytes, key id) no vacíos", what, stmt.args[4], stmt.args[5], stmt.args[6])
	}
	return enc, dek, kekID
}

// TestAppendSummary_LevelOneInClear: UNA sentencia con role system, grado summary, origen whatsapp,
// el cuerpo como payload y el sobre a NULL. Devuelve el seq que da la base y no necesita cipher.
func TestAppendSummary_LevelOneInClear(t *testing.T) {
	h := newFakeStoreWith(t, nil, pgOne(int64(4)))
	seq, err := h.store.AppendSummary(t.Context(), pgEvent, json.RawMessage(testSummaryBody))
	if err != nil || seq != 4 {
		t.Fatalf("AppendSummary = (%d, %v), quería (4, nil)", seq, err)
	}
	stmt := requireStatements(t, h.fake, 1)[0]
	requireArgs(t, "AppendSummary", stmt, pgEvent, "system", "summary", testSummaryBody, nil, nil, nil, "whatsapp")
	for _, want := range []string{"INSERT INTO public.conversation_event_messages", "COALESCE(MAX(seq), 0) + 1", "RETURNING seq"} {
		if !strings.Contains(stmt.query, want) {
			t.Errorf("la sentencia no contiene %q:\n%s", want, stmt.query)
		}
	}
}

// TestAppendDecision_LevelOneInClear: role client, grado decision, origen whatsapp, payload en
// claro; sin cipher.
func TestAppendDecision_LevelOneInClear(t *testing.T) {
	h := newFakeStoreWith(t, nil, pgOne(int64(1)))
	if err := h.store.AppendDecision(t.Context(), pgEvent, []byte(testDecisionBody)); err != nil {
		t.Fatalf("AppendDecision: %v", err)
	}
	requireArgs(t, "AppendDecision", requireStatements(t, h.fake, 1)[0],
		pgEvent, "client", "decision", testDecisionBody, nil, nil, nil, "whatsapp")
}

// TestAppendLevelOne_NotJSON_NeverReachesTheDatabase: prosa, JSON cortado o nada: ErrSummaryNotJSON
// a secas, sin sentencia.
func TestAppendLevelOne_NotJSON_NeverReachesTheDatabase(t *testing.T) {
	h := newFakeStore(t)
	for _, body := range []string{testClientText, `{"kind":`, ""} {
		seq, err := h.store.AppendSummary(t.Context(), pgEvent, json.RawMessage(body))
		if !exactly(err, ErrSummaryNotJSON) || seq != 0 {
			t.Errorf("AppendSummary(%q) = (%d, %v), quería (0, ErrSummaryNotJSON)", body, seq, err)
		}
		if err := h.store.AppendDecision(t.Context(), pgEvent, []byte(body)); !exactly(err, ErrSummaryNotJSON) {
			t.Errorf("AppendDecision(%q) = %v, quería ErrSummaryNotJSON", body, err)
		}
	}
	requireStatements(t, h.fake, 0)
}

// TestAppendMessage_LevelTwoSealed: el rol dado, grado message, origen whatsapp, payload NULL y el
// cuerpo SELLADO con el cipher. Valen los tres roles.
func TestAppendMessage_LevelTwoSealed(t *testing.T) {
	cipher := testCipher(t)
	h := newFakeStoreWith(t, cipher, pgOne(int64(1)), pgOne(int64(2)), pgOne(int64(3)))
	for i, role := range []Role{RoleClient, RoleBusiness, RoleSystem} {
		seq, err := h.store.AppendMessage(t.Context(), pgEvent, role, testClientText)
		if err != nil || seq != i+1 {
			t.Fatalf("AppendMessage(%s) = (%d, %v), quería (%d, nil)", role, seq, err, i+1)
		}
		requireSealedArgs(t, "AppendMessage "+string(role), requireStatements(t, h.fake, i+1)[i], cipher,
			string(role), "message", "whatsapp", testClientText)
	}
}

// TestAppendMessage_Guards_NeverReachTheDatabase: primero el rol (ErrInvalidRole, también sin
// cipher) y después el cipher (ErrNoCipher a secas).
func TestAppendMessage_Guards_NeverReachTheDatabase(t *testing.T) {
	for name, h := range map[string]pgHarness{"with cipher": newFakeStore(t), "without cipher": newFakeStoreWith(t, nil)} {
		seq, err := h.store.AppendMessage(t.Context(), pgEvent, "owner", testClientText)
		if want := `events: rol desconocido: "owner"`; !errors.Is(err, ErrInvalidRole) || err.Error() != want || seq != 0 {
			t.Errorf("%s: (%d, %v), quería (0, %q)", name, seq, err, want)
		}
		requireStatements(t, h.fake, 0)
	}
	h := newFakeStoreWith(t, nil)
	if seq, err := h.store.AppendMessage(t.Context(), pgEvent, RoleClient, testClientText); !exactly(err, ErrNoCipher) || seq != 0 {
		t.Errorf("sin cipher: (%d, %v), quería (0, ErrNoCipher)", seq, err)
	}
	requireStatements(t, h.fake, 0)
}

// TestAppendOutOfTurnMessage_SealedBusinessOutOfTurn: role business FIJO, grado
// message_out_of_turn, origen whatsapp, sellado. Sin cipher, ErrNoCipher sin sentencia.
func TestAppendOutOfTurnMessage_SealedBusinessOutOfTurn(t *testing.T) {
	cipher := testCipher(t)
	h := newFakeStoreWith(t, cipher, pgOne(int64(9)))
	seq, err := h.store.AppendOutOfTurnMessage(t.Context(), pgEvent, testClientText)
	if err != nil || seq != 9 {
		t.Fatalf("AppendOutOfTurnMessage = (%d, %v), quería (9, nil)", seq, err)
	}
	requireSealedArgs(t, "AppendOutOfTurnMessage", requireStatements(t, h.fake, 1)[0], cipher,
		"business", "message_out_of_turn", "whatsapp", testClientText)

	bare := newFakeStoreWith(t, nil)
	if seq, err = bare.store.AppendOutOfTurnMessage(t.Context(), pgEvent, testClientText); !exactly(err, ErrNoCipher) || seq != 0 {
		t.Errorf("sin cipher: (%d, %v), quería (0, ErrNoCipher)", seq, err)
	}
	requireStatements(t, bare.fake, 0)
}

// TestAppendPastedMessage_SealedClientOwnerPasted: role client FIJO, grado message y origen
// owner_pasted, sellado. Sin cipher, ErrNoCipher sin sentencia.
func TestAppendPastedMessage_SealedClientOwnerPasted(t *testing.T) {
	cipher := testCipher(t)
	h := newFakeStoreWith(t, cipher, pgOne(int64(2)))
	seq, err := h.store.AppendPastedMessage(t.Context(), pgEvent, testClientText)
	if err != nil || seq != 2 {
		t.Fatalf("AppendPastedMessage = (%d, %v), quería (2, nil)", seq, err)
	}
	requireSealedArgs(t, "AppendPastedMessage", requireStatements(t, h.fake, 1)[0], cipher,
		"client", "message", "owner_pasted", testClientText)

	bare := newFakeStoreWith(t, nil)
	if seq, err = bare.store.AppendPastedMessage(t.Context(), pgEvent, testClientText); !exactly(err, ErrNoCipher) || seq != 0 {
		t.Errorf("sin cipher: (%d, %v), quería (0, ErrNoCipher)", seq, err)
	}
	requireStatements(t, bare.fake, 0)
}

// TestAppend_RetriesOnUniqueViolation: si otro escritor se llevó el seq, la puerta reintenta la
// MISMA sentencia y devuelve el seq del intento que entra.
func TestAppend_RetriesOnUniqueViolation(t *testing.T) {
	unique := pgReply{err: errPgUnique}
	h := newFakeStore(t, unique, unique, pgOne(int64(7)))
	seq, err := h.store.AppendSummary(t.Context(), pgEvent, json.RawMessage(testSummaryBody))
	if err != nil || seq != 7 {
		t.Fatalf("AppendSummary = (%d, %v), quería (7, nil) al tercer intento", seq, err)
	}
	stmts := requireStatements(t, h.fake, 3)
	if stmts[0].query != stmts[2].query {
		t.Error("el reintento no repite la misma sentencia")
	}
}

// TestAppend_GivesUpAfterFiveAttempts: cinco violaciones seguidas agotan los intentos: error con
// su texto, envolviendo la última violación, y exactamente cinco sentencias.
func TestAppend_GivesUpAfterFiveAttempts(t *testing.T) {
	unique := pgReply{err: errPgUnique}
	h := newFakeStore(t, unique, unique, unique, unique, unique, pgOne(int64(1)))
	seq, err := h.store.AppendMessage(t.Context(), pgEvent, RoleClient, testClientText)
	if seq != 0 {
		t.Errorf("seq = %d, quería 0", seq)
	}
	requireWrapped(t, "AppendMessage", err, "events: numerar la entrada del historial tras 5 intentos: ", errPgUnique)
	requireStatements(t, h.fake, 5)
}

// TestAppend_OtherFailure_WrappedWithoutRetry: un fallo que no es de unicidad no se reintenta y
// va envuelto con el grado de la entrada.
func TestAppend_OtherFailure_WrappedWithoutRetry(t *testing.T) {
	h := newFakeStore(t, pgFails(), pgFails(), pgFails(), pgFails(), pgFails())
	ctx := t.Context()

	_, err := h.store.AppendSummary(ctx, pgEvent, json.RawMessage(testSummaryBody))
	requireWrapped(t, "AppendSummary", err, `events: insertar entrada "summary" del historial: `, errPgBoom)
	err = h.store.AppendDecision(ctx, pgEvent, []byte(testDecisionBody))
	requireWrapped(t, "AppendDecision", err, `events: insertar entrada "decision" del historial: `, errPgBoom)
	_, err = h.store.AppendMessage(ctx, pgEvent, RoleClient, testClientText)
	requireWrapped(t, "AppendMessage", err, `events: insertar entrada "message" del historial: `, errPgBoom)
	_, err = h.store.AppendOutOfTurnMessage(ctx, pgEvent, testClientText)
	requireWrapped(t, "AppendOutOfTurnMessage", err, `events: insertar entrada "message_out_of_turn" del historial: `, errPgBoom)
	_, err = h.store.AppendPastedMessage(ctx, pgEvent, testClientText)
	requireWrapped(t, "AppendPastedMessage", err, `events: insertar entrada "message" del historial: `, errPgBoom)
	if strings.Contains(err.Error(), testClientText) {
		t.Errorf("el error cita el cuerpo: %q", err.Error())
	}
	requireStatements(t, h.fake, 5)
}
