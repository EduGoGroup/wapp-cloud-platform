package eventshelpertest

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// Las cinco puertas de escritura del historial. Cada una clava su grado, su rol y su origen; el
// llamador solo trae el contenido.

// Los cuerpos que escriben los casos.
const (
	summaryBody  = `{"kind":"cart","lines":[{"sku":"CAFE","label":"Café","qty":2,"unit_price":2.5}]}`
	decisionBody = `{"effect":"item_added","sku":"CAFE","qty":2}`
	clientText   = "quiero dos cafés sin azúcar, porfa"
)

// appendTarget es el evento sobre el que escribe un caso, con sus testigos ya sembrados (uno de
// ellos, el de otro tipo en la misma conversación, con una entrada propia que nadie debe mover).
func appendTarget(t *testing.T, m Montaje) events.Event {
	t.Helper()
	c := newConversation(m.TenantA)
	ev := mustCreate(t, m, c, kindCart)
	w := seedWitnesses(t, m, c, kindCart)
	mustMessage(t, m, w.otherKind.ID, events.RoleClient, "esto es de otro evento")
	return ev
}

// requireAppended exige que la escritura haya tocado SOLO el historial de ese evento —su fila
// intacta— añadiéndole al final exactamente esa entrada.
func requireAppended(t *testing.T, what string, m Montaje, before world, eventID string, want Entry) {
	t.Helper()
	after := requireOnlyChanged(t, what, m, before, eventID)
	if !sameEvent(after[eventID].row, before[eventID].row) {
		t.Errorf("%s: la fila del evento cambió: %+v, era %+v", what, after[eventID].row, before[eventID].row)
	}
	old, now := before[eventID].entries, after[eventID].entries
	if len(now) != len(old)+1 {
		t.Fatalf("%s: el historial pasó de %d a %d entradas, quería una más", what, len(old), len(now))
	}
	for i := range old {
		requireEntry(t, what+": entrada anterior", now[i], old[i])
	}
	requireEntry(t, what+": entrada nueva", now[len(old)], want)
}

// caseAppendNumbering: el seq arranca en 1 en CADA evento y sube de uno en uno sin huecos, sea
// cual sea la puerta; un rechazo no consume número.
func caseAppendNumbering(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	one, two := mustCreate(t, m, c, kindCart), mustCreate(t, m, c, kindSurvey)
	ctx := t.Context()

	requireSeq := func(what string, want int) func(int, error) {
		return func(seq int, err error) {
			t.Helper()
			if err != nil || seq != want {
				t.Fatalf("%s = (%d, %v), quería (%d, nil)", what, seq, err, want)
			}
		}
	}
	requireSeq("primer AppendMessage", 1)(m.Store.AppendMessage(ctx, one.ID, events.RoleClient, clientText))
	requireNoError(t, "AppendDecision", m.Store.AppendDecision(ctx, one.ID, []byte(decisionBody)))
	if _, err := m.Store.AppendSummary(ctx, one.ID, json.RawMessage("esto no es json")); err == nil {
		t.Fatal("AppendSummary con prosa no falló")
	}
	requireSeq("AppendOutOfTurnMessage", 3)(m.Store.AppendOutOfTurnMessage(ctx, one.ID, "te recordamos tu seña"))
	requireSeq("primer AppendSummary del otro evento", 1)(m.Store.AppendSummary(ctx, two.ID, json.RawMessage(summaryBody)))
	requireSeq("AppendPastedMessage", 4)(m.Store.AppendPastedMessage(ctx, one.ID, "audio transcrito"))
	requireSeq("AppendSummary", 5)(m.Store.AppendSummary(ctx, one.ID, json.RawMessage(summaryBody)))

	for id, want := range map[string]int{one.ID: 5, two.ID: 1} {
		entries := m.Entries(t, id)
		if len(entries) != want {
			t.Fatalf("el evento %s tiene %d entradas, quería %d", id, len(entries), want)
		}
		for i, e := range entries {
			if e.Seq != i+1 {
				t.Errorf("entrada %d del evento %s lleva seq %d, quería %d", i, id, e.Seq, i+1)
			}
		}
	}
}

// caseAppendSummary: el resumen es nivel 1: estructura EN CLARO en payload, sin sobre cifrado, con
// role system y origen whatsapp fijos. Devuelve su seq.
func caseAppendSummary(t *testing.T, m Montaje) {
	ev := appendTarget(t, m)
	mustMessage(t, m, ev.ID, events.RoleClient, clientText)
	before := take(t, m)

	seq, err := m.Store.AppendSummary(t.Context(), ev.ID, json.RawMessage(summaryBody))
	if err != nil || seq != 2 {
		t.Fatalf("AppendSummary = (%d, %v), quería (2, nil)", seq, err)
	}
	requireAppended(t, "AppendSummary", m, before, ev.ID, Entry{
		Seq: 2, Role: events.RoleSystem, Kind: events.KindSummary, Origin: events.OriginWhatsApp,
		Payload: []byte(summaryBody),
	})
}

// caseAppendDecision: la decisión es nivel 1 con role client fijo: la voz es del cliente aunque la
// serialice la plataforma.
func caseAppendDecision(t *testing.T, m Montaje) {
	ev := appendTarget(t, m)
	before := take(t, m)

	requireNoError(t, "AppendDecision", m.Store.AppendDecision(t.Context(), ev.ID, []byte(decisionBody)))
	requireAppended(t, "AppendDecision", m, before, ev.ID, Entry{
		Seq: 1, Role: events.RoleClient, Kind: events.KindDecision, Origin: events.OriginWhatsApp,
		Payload: []byte(decisionBody),
	})
}

// caseAppendNotJSON: por las dos puertas del nivel 1 no entra prosa: lo que no es JSON válido
// —texto libre, JSON cortado, nada— da ErrSummaryNotJSON y no escribe.
func caseAppendNotJSON(t *testing.T, m Montaje) {
	ev := appendTarget(t, m)
	before := take(t, m)

	for _, body := range []string{clientText, `{"kind":"cart"`, ""} {
		seq, err := m.Store.AppendSummary(t.Context(), ev.ID, json.RawMessage(body))
		requireIs(t, "AppendSummary", err, events.ErrSummaryNotJSON)
		if seq != 0 {
			t.Errorf("AppendSummary(%q) devolvió el seq %d con el rechazo, quería 0", body, seq)
		}
		requireIs(t, "AppendDecision", m.Store.AppendDecision(t.Context(), ev.ID, []byte(body)), events.ErrSummaryNotJSON)
	}
	_, err := m.Store.AppendSummary(t.Context(), ev.ID, nil)
	requireIs(t, "AppendSummary(nil)", err, events.ErrSummaryNotJSON)
	requireUnchanged(t, "lo que no es JSON", m, before)
}

// caseAppendMessage: el texto literal es nivel 2: va SELLADO, con payload NULL, grado message,
// origen whatsapp y el rol que diga el llamador (los tres valen). El texto vuelve entero por
// ListThread.
func caseAppendMessage(t *testing.T, m Montaje) {
	ev := appendTarget(t, m)
	roles := []events.Role{events.RoleClient, events.RoleBusiness, events.RoleSystem}
	for i, role := range roles {
		before := take(t, m)
		seq, err := m.Store.AppendMessage(t.Context(), ev.ID, role, clientText+" "+string(role))
		if err != nil || seq != i+1 {
			t.Fatalf("AppendMessage(%s) = (%d, %v), quería (%d, nil)", role, seq, err, i+1)
		}
		requireAppended(t, "AppendMessage "+string(role), m, before, ev.ID, Entry{
			Seq: i + 1, Role: role, Kind: events.KindMessage, Origin: events.OriginWhatsApp, Sealed: true,
		})
	}
	thread, err := m.Store.ListThread(t.Context(), ev.ID, 10)
	requireNoError(t, "ListThread", err)
	if len(thread) != len(roles) {
		t.Fatalf("el hilo trae %d entradas, quería %d", len(thread), len(roles))
	}
	for i, role := range roles {
		if thread[i].Text != clientText+" "+string(role) {
			t.Errorf("texto de la entrada %d = %q, quería %q", i+1, thread[i].Text, clientText+" "+string(role))
		}
	}
}

// caseAppendInvalidRole: un rol que no es uno de los tres da ErrInvalidRole y no escribe; la
// guarda va ANTES que la del cipher.
func caseAppendInvalidRole(t *testing.T, m Montaje) {
	ev := appendTarget(t, m)
	before := take(t, m)

	for _, role := range []events.Role{"", "owner", "Client", "assistant"} {
		for name, port := range map[string]Port{"Store": m.Store, "NoCipher": m.NoCipher} {
			seq, err := port.AppendMessage(t.Context(), ev.ID, role, clientText)
			requireIs(t, name+".AppendMessage con el rol "+string(role), err, events.ErrInvalidRole)
			if seq != 0 {
				t.Errorf("%s.AppendMessage(%q) devolvió el seq %d con el rechazo, quería 0", name, role, seq)
			}
		}
	}
	requireUnchanged(t, "AppendMessage con un rol desconocido", m, before)
}

// caseAppendOutOfTurn: el saliente fuera de turno va sellado como un mensaje, pero con su PROPIO
// grado (message_out_of_turn) y con role business fijo.
func caseAppendOutOfTurn(t *testing.T, m Montaje) {
	ev := appendTarget(t, m)
	before := take(t, m)

	seq, err := m.Store.AppendOutOfTurnMessage(t.Context(), ev.ID, "te recordamos tu seña")
	if err != nil || seq != 1 {
		t.Fatalf("AppendOutOfTurnMessage = (%d, %v), quería (1, nil)", seq, err)
	}
	requireAppended(t, "AppendOutOfTurnMessage", m, before, ev.ID, Entry{
		Seq: 1, Role: events.RoleBusiness, Kind: events.KindMessageOutOfTurn, Origin: events.OriginWhatsApp, Sealed: true,
	})
}

// caseAppendPasted: la transcripción que pega el dueño va sellada, con grado message y role
// client —es lo que DIJO el cliente— y lo único que la distingue es el origen: owner_pasted.
func caseAppendPasted(t *testing.T, m Montaje) {
	ev := appendTarget(t, m)
	before := take(t, m)

	seq, err := m.Store.AppendPastedMessage(t.Context(), ev.ID, "audio transcrito por el dueño")
	if err != nil || seq != 1 {
		t.Fatalf("AppendPastedMessage = (%d, %v), quería (1, nil)", seq, err)
	}
	requireAppended(t, "AppendPastedMessage", m, before, ev.ID, Entry{
		Seq: 1, Role: events.RoleClient, Kind: events.KindMessage, Origin: events.OriginOwnerPasted, Sealed: true,
	})
}

// caseAppendWithoutCipher: sin cipher las tres puertas de texto devuelven ErrNoCipher y NO
// escriben —no existe camino en claro—; las dos de estructura no lo necesitan y funcionan.
func caseAppendWithoutCipher(t *testing.T, m Montaje) {
	ev := appendTarget(t, m)
	before := take(t, m)
	ctx := t.Context()

	seq, err := m.NoCipher.AppendMessage(ctx, ev.ID, events.RoleClient, clientText)
	requireIs(t, "AppendMessage sin cipher", err, events.ErrNoCipher)
	seq2, err := m.NoCipher.AppendOutOfTurnMessage(ctx, ev.ID, clientText)
	requireIs(t, "AppendOutOfTurnMessage sin cipher", err, events.ErrNoCipher)
	seq3, err := m.NoCipher.AppendPastedMessage(ctx, ev.ID, clientText)
	requireIs(t, "AppendPastedMessage sin cipher", err, events.ErrNoCipher)
	if seq != 0 || seq2 != 0 || seq3 != 0 {
		t.Errorf("los rechazos devolvieron los seq (%d, %d, %d), quería ceros", seq, seq2, seq3)
	}
	requireUnchanged(t, "las puertas de texto sin cipher", m, before)

	if seq, err = m.NoCipher.AppendSummary(ctx, ev.ID, json.RawMessage(summaryBody)); err != nil || seq != 1 {
		t.Fatalf("AppendSummary sin cipher = (%d, %v), quería (1, nil)", seq, err)
	}
	requireNoError(t, "AppendDecision sin cipher", m.NoCipher.AppendDecision(ctx, ev.ID, []byte(decisionBody)))
	after := requireOnlyChanged(t, "las puertas de estructura sin cipher", m, before, ev.ID)
	if n := len(after[ev.ID].entries); n != 2 {
		t.Errorf("el historial tiene %d entradas, quería 2", n)
	}
}

// caseAppendUnknownEvent: escribir en un evento que no existe falla por las cinco puertas —lo
// impide la clave foránea— y no deja nada, ni en ese id ni en ningún otro evento.
func caseAppendUnknownEvent(t *testing.T, m Montaje) {
	appendTarget(t, m)
	before := take(t, m)
	ghost := uuid.NewString()
	ctx := t.Context()

	_, errSummary := m.Store.AppendSummary(ctx, ghost, json.RawMessage(summaryBody))
	errDecision := m.Store.AppendDecision(ctx, ghost, []byte(decisionBody))
	_, errMessage := m.Store.AppendMessage(ctx, ghost, events.RoleClient, clientText)
	_, errOutOfTurn := m.Store.AppendOutOfTurnMessage(ctx, ghost, clientText)
	_, errPasted := m.Store.AppendPastedMessage(ctx, ghost, clientText)
	for door, err := range map[string]error{
		"AppendSummary": errSummary, "AppendDecision": errDecision, "AppendMessage": errMessage,
		"AppendOutOfTurnMessage": errOutOfTurn, "AppendPastedMessage": errPasted,
	} {
		if err == nil {
			t.Errorf("%s sobre un evento que no existe no falló", door)
		}
	}
	if entries := m.Entries(t, ghost); len(entries) != 0 {
		t.Errorf("quedaron %d entradas colgando de un evento que no existe", len(entries))
	}
	requireUnchanged(t, "escribir en un evento que no existe", m, before)
}
