package eventshelpertest

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// La lectura del hilo: descifrada en el borde, en orden, y con el grado de cada entrada al lado
// del texto para que quien lee pueda clasificar.

// mustThread lee el hilo del evento.
func mustThread(t *testing.T, m Montaje, eventID string, limit int) []events.ThreadEntry {
	t.Helper()
	thread, err := m.Store.ListThread(t.Context(), eventID, limit)
	requireNoError(t, "ListThread", err)
	return thread
}

// requireThread exige esas entradas, en ese orden, con sus cuatro campos.
func requireThread(t *testing.T, what string, got, want []events.ThreadEntry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d entradas (%+v), quería %d (%+v)", what, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: entrada %d = %+v, quería %+v", what, i, got[i], want[i])
		}
	}
}

// caseThreadReads: el hilo sale en orden cronológico (seq ascendente) con el texto de cada entrada
// sellada DESCIFRADO y su rol y su grado al lado: el mensaje en turno, el saliente fuera de turno
// y la transcripción pegada (que para quien lee es un mensaje del cliente más: el origen no
// viaja). Solo las entradas de ESE evento. Leer no escribe.
func caseThreadReads(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	ev := mustCreate(t, m, c, kindCart)
	w := seedWitnesses(t, m, c, kindCart)
	mustMessage(t, m, w.otherKind.ID, events.RoleClient, "esto es de otro evento")
	ctx := t.Context()

	mustMessage(t, m, ev.ID, events.RoleClient, clientText)
	mustMessage(t, m, ev.ID, events.RoleBusiness, "¡Claro! ¿Algo más?")
	_, err := m.Store.AppendOutOfTurnMessage(ctx, ev.ID, "Pasó un rato sin novedades")
	requireNoError(t, "AppendOutOfTurnMessage", err)
	_, err = m.Store.AppendPastedMessage(ctx, ev.ID, "y una medialuna — acentos: áéíóú ñ ✏️")
	requireNoError(t, "AppendPastedMessage", err)
	mustMessage(t, m, ev.ID, events.RoleClient, "")
	world := take(t, m)

	requireThread(t, "ListThread", mustThread(t, m, ev.ID, 50), []events.ThreadEntry{
		{Seq: 1, Role: events.RoleClient, Kind: events.KindMessage, Text: clientText},
		{Seq: 2, Role: events.RoleBusiness, Kind: events.KindMessage, Text: "¡Claro! ¿Algo más?"},
		{Seq: 3, Role: events.RoleBusiness, Kind: events.KindMessageOutOfTurn, Text: "Pasó un rato sin novedades"},
		{Seq: 4, Role: events.RoleClient, Kind: events.KindMessage, Text: "y una medialuna — acentos: áéíóú ñ ✏️"},
		{Seq: 5, Role: events.RoleClient, Kind: events.KindMessage, Text: ""},
	})
	requireUnchanged(t, "ListThread", m, world)
}

// caseThreadLimit: el recorte muerde por el PRINCIPIO: con más entradas que limit salen las más
// RECIENTES, y siguen en orden cronológico.
func caseThreadLimit(t *testing.T, m Montaje) {
	ev := mustCreate(t, m, newConversation(m.TenantA), kindCart)
	all := make([]events.ThreadEntry, 0, 6)
	for i := 1; i <= 6; i++ {
		text := "mensaje " + strconv.Itoa(i)
		mustMessage(t, m, ev.ID, events.RoleClient, text)
		all = append(all, events.ThreadEntry{Seq: i, Role: events.RoleClient, Kind: events.KindMessage, Text: text})
	}

	cases := []struct {
		name  string
		limit int
		want  []events.ThreadEntry
	}{
		{"one keeps the last", 1, all[5:]},
		{"four keeps the last four", 4, all[2:]},
		{"exactly the size", 6, all},
		{"above the size", 100, all},
	}
	for _, tc := range cases {
		requireThread(t, tc.name, mustThread(t, m, ev.ID, tc.limit), tc.want)
	}
}

// caseThreadNothing: sin id, con limit <= 0, con un evento sin historial o con uno que no existe,
// las dos lecturas devuelven la lista vacía SIN error.
func caseThreadNothing(t *testing.T, m Montaje) {
	ev := mustCreate(t, m, newConversation(m.TenantA), kindCart)
	withText := mustCreate(t, m, newConversation(m.TenantA), kindCart)
	mustMessage(t, m, withText.ID, events.RoleClient, clientText)

	cases := []struct {
		name  string
		id    string
		limit int
	}{
		{"empty id", "", 10},
		{"zero limit", withText.ID, 0},
		{"negative limit", withText.ID, -1},
		{"event without entries", ev.ID, 10},
		{"unknown event", uuid.NewString(), 10},
	}
	for _, tc := range cases {
		if thread := mustThread(t, m, tc.id, tc.limit); len(thread) != 0 {
			t.Errorf("%s: ListThread devolvió %+v, quería nada", tc.name, thread)
		}
	}
	for _, id := range []string{"", ev.ID, withText.ID, uuid.NewString()} {
		pasted, err := m.Store.ListPastedByOwner(t.Context(), id)
		if err != nil || len(pasted) != 0 {
			t.Errorf("ListPastedByOwner(%q) = (%v, %v), quería nada", id, pasted, err)
		}
	}
}

// caseThreadUnreadablePayload: una entrada de nivel 1 cuyo payload no se deja leer como un resumen
// (aquí, una lista) NO es un error de lectura: sale con su rol y su grado y con el texto vacío, y
// el resto del hilo se lee igual.
func caseThreadUnreadablePayload(t *testing.T, m Montaje) {
	ev := mustCreate(t, m, newConversation(m.TenantA), kindCart)
	requireNoError(t, "AppendDecision", m.Store.AppendDecision(t.Context(), ev.ID, []byte(`[1,2,3]`)))
	_, err := m.Store.AppendSummary(t.Context(), ev.ID, json.RawMessage(`"una cadena JSON"`))
	requireNoError(t, "AppendSummary", err)
	mustMessage(t, m, ev.ID, events.RoleClient, clientText)

	requireThread(t, "ListThread", mustThread(t, m, ev.ID, 10), []events.ThreadEntry{
		{Seq: 1, Role: events.RoleClient, Kind: events.KindDecision, Text: ""},
		{Seq: 2, Role: events.RoleSystem, Kind: events.KindSummary, Text: ""},
		{Seq: 3, Role: events.RoleClient, Kind: events.KindMessage, Text: clientText},
	})
}

// caseThreadUndecryptable: una entrada que no se puede descifrar ABORTA la lectura —no se salta—:
// devuelve error, sin hilo a medias, nombrando el evento y el seq y nunca el cuerpo. Si el recorte
// la deja fuera, el hilo se lee.
func caseThreadUndecryptable(t *testing.T, m Montaje) {
	ev := mustCreate(t, m, newConversation(m.TenantA), kindCart)
	mustMessage(t, m, ev.ID, events.RoleClient, "primero")
	mustMessage(t, m, ev.ID, events.RoleClient, "segundo "+clientText)
	mustMessage(t, m, ev.ID, events.RoleBusiness, "tercero")
	m.CorruptEntry(t, ev.ID, 2)

	thread, err := m.Store.ListThread(t.Context(), ev.ID, 10)
	if err == nil {
		t.Fatalf("ListThread con una entrada indescifrable devolvió %+v sin error", thread)
	}
	if thread != nil {
		t.Errorf("ListThread devolvió un hilo a medias con el error: %+v", thread)
	}
	msg := err.Error()
	if !strings.Contains(msg, "events: resolver la entrada 2 del hilo del evento "+ev.ID) {
		t.Errorf("el error no nombra la entrada y el evento: %q", msg)
	}
	if strings.Contains(msg, clientText) || strings.Contains(msg, "segundo") {
		t.Errorf("el error cita el cuerpo: %q", msg)
	}
	requireThread(t, "ListThread con la entrada rota fuera del recorte", mustThread(t, m, ev.ID, 1), []events.ThreadEntry{
		{Seq: 3, Role: events.RoleBusiness, Kind: events.KindMessage, Text: "tercero"},
	})
}

// casePasted: solo las transcripciones que el dueño PEGÓ en ese evento, en orden y sin tope. Un
// mensaje del cliente por WhatsApp con el MISMO texto no sale (son dos hechos distintos), ni un
// saliente, ni lo pegado en otro evento.
func casePasted(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	ev := mustCreate(t, m, c, kindCart)
	other := mustCreate(t, m, c, kindSurvey)
	ctx := t.Context()
	paste := func(id, body string) {
		t.Helper()
		_, err := m.Store.AppendPastedMessage(ctx, id, body)
		requireNoError(t, "AppendPastedMessage", err)
	}

	mustMessage(t, m, ev.ID, events.RoleClient, "dos empanadas")
	paste(ev.ID, "dos empanadas")
	_, err := m.Store.AppendOutOfTurnMessage(ctx, ev.ID, "dos empanadas")
	requireNoError(t, "AppendOutOfTurnMessage", err)
	requireNoError(t, "AppendDecision", m.Store.AppendDecision(ctx, ev.ID, []byte(decisionBody)))
	paste(ev.ID, "y una gaseosa")
	paste(other.ID, "esto se pegó en otro evento")
	paste(ev.ID, "dos empanadas")
	world := take(t, m)

	pasted, err := m.Store.ListPastedByOwner(ctx, ev.ID)
	requireNoError(t, "ListPastedByOwner", err)
	requireIDs(t, "ListPastedByOwner", pasted, []string{"dos empanadas", "y una gaseosa", "dos empanadas"})
	requireUnchanged(t, "ListPastedByOwner", m, world)
}

// casePastedUndecryptable: una transcripción pegada que no se puede descifrar aborta la lectura
// (tragársela dejaría pasar un duplicado), nombrando el evento y el seq y nunca el cuerpo. Una
// entrada rota que NO es pegada no le afecta.
func casePastedUndecryptable(t *testing.T, m Montaje) {
	ev := mustCreate(t, m, newConversation(m.TenantA), kindCart)
	ctx := t.Context()
	mustMessage(t, m, ev.ID, events.RoleClient, "por whatsapp")
	_, err := m.Store.AppendPastedMessage(ctx, ev.ID, "pegado "+clientText)
	requireNoError(t, "AppendPastedMessage", err)

	m.CorruptEntry(t, ev.ID, 1)
	pasted, err := m.Store.ListPastedByOwner(ctx, ev.ID)
	requireNoError(t, "ListPastedByOwner con un mensaje de whatsapp roto", err)
	requireIDs(t, "ListPastedByOwner", pasted, []string{"pegado " + clientText})

	m.CorruptEntry(t, ev.ID, 2)
	pasted, err = m.Store.ListPastedByOwner(ctx, ev.ID)
	if err == nil || pasted != nil {
		t.Fatalf("ListPastedByOwner con la pegada rota = (%v, %v), quería (nil, error)", pasted, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "events: descifrar la transcripción 2 del evento "+ev.ID) {
		t.Errorf("el error no nombra la transcripción y el evento: %q", msg)
	}
	if strings.Contains(msg, clientText) || strings.Contains(msg, "pegado") {
		t.Errorf("el error cita el cuerpo: %q", msg)
	}
}

// caseThreadWithoutCipher: sin cipher las dos lecturas fallan con ErrNoCipher en vez de devolver
// el hilo a medias —también si el evento solo tiene entradas de nivel 1—; las guardas de «nada
// que leer» van antes.
func caseThreadWithoutCipher(t *testing.T, m Montaje) {
	ev := mustCreate(t, m, newConversation(m.TenantA), kindCart)
	requireNoError(t, "AppendDecision", m.Store.AppendDecision(t.Context(), ev.ID, []byte(decisionBody)))
	mustMessage(t, m, ev.ID, events.RoleClient, clientText)
	ctx := t.Context()

	thread, err := m.NoCipher.ListThread(ctx, ev.ID, 10)
	requireIs(t, "ListThread sin cipher", err, events.ErrNoCipher)
	pasted, err := m.NoCipher.ListPastedByOwner(ctx, ev.ID)
	requireIs(t, "ListPastedByOwner sin cipher", err, events.ErrNoCipher)
	if thread != nil || pasted != nil {
		t.Errorf("las lecturas sin cipher devolvieron (%v, %v), quería nil", thread, pasted)
	}

	if thread, err = m.NoCipher.ListThread(ctx, "", 10); err != nil || thread != nil {
		t.Errorf("ListThread sin cipher y sin id = (%v, %v), quería (nil, nil)", thread, err)
	}
	if thread, err = m.NoCipher.ListThread(ctx, ev.ID, 0); err != nil || thread != nil {
		t.Errorf("ListThread sin cipher y con limit 0 = (%v, %v), quería (nil, nil)", thread, err)
	}
	if pasted, err = m.NoCipher.ListPastedByOwner(ctx, ""); err != nil || pasted != nil {
		t.Errorf("ListPastedByOwner sin cipher y sin id = (%v, %v), quería (nil, nil)", pasted, err)
	}
}
