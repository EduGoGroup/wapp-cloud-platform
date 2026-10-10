package eventshelpertest

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// Las cuatro carreras del almacén. Los tests viejos no tenían ninguna: el único parcial, el guard
// `AND status = 'open'` de la transición y el reintento de la numeración solo se probaban en
// secuencia, y así un mutante que cambia el índice por un SELECT previo, el compare-and-swap por
// un «leer, decidir, escribir» o le quita el reintento a la numeración sobrevive. Aquí N llamadas
// salen a la vez y se afirma el resultado que SOLO da una implementación que las serializa; no se
// duerme ni se mira el reloj. En memoria las serializa el mutex; contra Postgres, la base.

// racers es el número de llamadas simultáneas de las carreras de evento.
const racers = 8

// appendRacers es el número de escritores simultáneos del historial de UN evento. Son cuatro y no
// ocho a propósito: el adaptador reintenta la numeración hasta 5 veces por entrada, y con N
// escritores uno puede perder N-1 veces seguidas. Con más de cinco, que alguno agote los intentos
// sería un resultado LEGÍTIMO del contrato y la carrera dejaría de ser determinista.
const appendRacers = 4

// race lanza n goroutines que esperan la misma señal y llaman a fn con su índice, y devuelve
// cuando todas han terminado. fn no puede llamar a t.Fatal: corre fuera de la goroutine del test.
func race(n int, fn func(i int)) {
	var (
		start = make(chan struct{})
		done  sync.WaitGroup
	)
	for i := range n {
		done.Add(1)
		go func() {
			defer done.Done()
			<-start
			fn(i)
		}()
	}
	close(start)
	done.Wait()
}

// soleWinner exige que EXACTAMENTE una de las llamadas de la carrera haya ganado (error nil) y
// que todas las demás hayan perdido con el centinela dado, y devuelve el índice de la ganadora.
func soleWinner(t *testing.T, what string, errs []error, lost error) int {
	t.Helper()
	winner := -1
	for i, err := range errs {
		switch {
		case err == nil && winner < 0:
			winner = i
		case err == nil:
			t.Fatalf("%s: ganaron dos llamadas, la %d y la %d", what, winner, i)
		case !errors.Is(err, lost):
			t.Errorf("%s %d: falló con %v, quería %v", what, i, err, lost)
		}
	}
	if winner < 0 {
		t.Fatalf("%s: no ganó ninguna llamada", what)
	}
	return winner
}

// caseCreateConcurrent: N nacimientos simultáneos del MISMO tipo en la MISMA conversación: gana
// exactamente uno, los demás reciben ErrAliveExists, y queda UNA fila —la que devolvió el
// ganador—. Es el índice único parcial (E-2): entre un SELECT y un INSERT cabe otro escritor.
func caseCreateConcurrent(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	seedWitnesses(t, m, c, kindCart)
	before := take(t, m)
	ctx := t.Context()

	born := make([]events.Event, racers)
	errs := make([]error, racers)
	race(racers, func(i int) {
		in := c.input(kindCart)
		in.FlowVersion = i + 1
		born[i], errs[i] = m.Store.CreateEvent(ctx, in)
	})

	winner := soleWinner(t, "nacimiento", errs, events.ErrAliveExists)
	for i, ev := range born {
		if i != winner && ev != (events.Event{}) {
			t.Errorf("el nacimiento %d perdió y devolvió %+v, quería el evento cero", i, ev)
		}
	}
	won := born[winner]
	after := requireOnlyChanged(t, "la carrera de nacimientos", m, before, won.ID)
	if got := after[won.ID].row; !sameEvent(got, won) || got.FlowVersion != winner+1 {
		t.Errorf("la fila que quedó es %+v, quería la del ganador %+v", got, won)
	}
	alive, err := m.Store.ListAlive(ctx, c.tenant, c.session, c.contact)
	requireNoError(t, "ListAlive", err)
	if n := len(alive); n != 2 { // el cart ganador y el survey testigo
		t.Errorf("la conversación tiene %d vivos, quería 2", n)
	}
}

// caseTransitionConcurrent: N transiciones simultáneas del mismo evento, la mitad a closed y la
// mitad a cancelled: gana exactamente UNA, las demás reciben ErrNotOpen, y el estado que queda es
// el de la ganadora, con closed_at sellado y el resto de la fila intacto. Es el compare-and-swap:
// quien pierde la carrera no pisa la muerte que el otro ya selló.
func caseTransitionConcurrent(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	ev := mustCreate(t, m, c, kindCart)
	seedWitnesses(t, m, c, kindCart)
	m.Advance(t, 10*time.Minute)
	before := take(t, m)
	ctx := t.Context()

	target := func(i int) events.Status {
		if i%2 == 0 {
			return events.StatusClosed
		}
		return events.StatusCancelled
	}
	errs := make([]error, racers)
	race(racers, func(i int) { errs[i] = m.Store.TransitionEvent(ctx, ev.ID, target(i)) })
	winner := soleWinner(t, "transición", errs, events.ErrNotOpen)
	after := requireOnlyChanged(t, "la carrera de transiciones", m, before, ev.ID)
	want := ev
	want.Status, want.ClosedAt = target(winner), m.Now(t)
	if got := after[ev.ID].row; !sameEvent(got, want) {
		t.Errorf("la fila que quedó es %+v, quería %+v", got, want)
	}
}

// caseTouchTransitionConcurrent: un refresco del reloj y una transición simultáneos sobre el mismo
// evento no se pisan: Touch solo escribe last_activity_at y la transición solo status y closed_at,
// así que salga quien salga primero quedan LAS DOS escrituras. Una implementación que leyera la
// fila, la cambiara y la guardara entera perdería una de las dos. Se repite por parejas, cada una
// sobre su evento.
func caseTouchTransitionConcurrent(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	var pairs [racers / 2]events.Event
	for i := range pairs {
		pairs[i] = mustCreate(t, m, conversation{c.tenant, c.session + "-" + strconv.Itoa(i), c.contact}, kindCart)
	}
	witness := mustCreate(t, m, c, kindCart)
	m.Advance(t, 10*time.Minute)
	before := take(t, m)
	ctx := t.Context()

	var errs [racers]error
	race(racers, func(i int) {
		ev := pairs[i/2]
		if i%2 == 0 {
			errs[i] = m.Store.Touch(ctx, ev.ID)
			return
		}
		errs[i] = m.Store.TransitionEvent(ctx, ev.ID, events.StatusClosed)
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("la llamada %d falló: %v", i, err)
		}
	}

	after := requireOnlyChanged(t, "la carrera de Touch y transición", m, before, ids(pairs[:])...)
	for _, ev := range pairs {
		want := ev
		want.Status, want.ClosedAt, want.LastActivityAt = events.StatusClosed, m.Now(t), m.Now(t)
		if got := after[ev.ID].row; !sameEvent(got, want) {
			t.Errorf("la fila que quedó es %+v, quería las DOS escrituras: %+v", got, want)
		}
	}
	if got := after[witness.ID].row; !sameEvent(got, witness) {
		t.Errorf("el testigo cambió: %+v, era %+v", got, witness)
	}
}

// caseAppendConcurrent: cuatro escritores simultáneos sobre el historial del MISMO evento, uno
// por cada puerta que devuelve seq: ninguno falla, cada uno recibe un seq distinto y entre todos
// numeran a continuación de lo que había, sin huecos ni repetidos. Lo que se guarda bajo cada seq
// es lo que escribió quien lo recibió. Es el UNIQUE (event_id, seq) más el reintento.
func caseAppendConcurrent(t *testing.T, m Montaje) {
	c := newConversation(m.TenantA)
	ev := mustCreate(t, m, c, kindCart)
	w := seedWitnesses(t, m, c, kindCart)
	mustMessage(t, m, ev.ID, events.RoleClient, "antes de la carrera")
	mustMessage(t, m, w.otherKind.ID, events.RoleClient, "esto es de otro evento")
	before := take(t, m)
	ctx := t.Context()

	doors := [appendRacers]struct {
		write func() (int, error)
		want  Entry
	}{
		{func() (int, error) { return m.Store.AppendMessage(ctx, ev.ID, events.RoleBusiness, "en turno") },
			Entry{Role: events.RoleBusiness, Kind: events.KindMessage, Origin: events.OriginWhatsApp, Sealed: true}},
		{func() (int, error) { return m.Store.AppendOutOfTurnMessage(ctx, ev.ID, "fuera de turno") },
			Entry{Role: events.RoleBusiness, Kind: events.KindMessageOutOfTurn, Origin: events.OriginWhatsApp, Sealed: true}},
		{func() (int, error) { return m.Store.AppendPastedMessage(ctx, ev.ID, "pegado") },
			Entry{Role: events.RoleClient, Kind: events.KindMessage, Origin: events.OriginOwnerPasted, Sealed: true}},
		{func() (int, error) { return m.Store.AppendSummary(ctx, ev.ID, json.RawMessage(summaryBody)) },
			Entry{Role: events.RoleSystem, Kind: events.KindSummary, Origin: events.OriginWhatsApp, Payload: []byte(summaryBody)}},
	}
	var (
		seqs [appendRacers]int
		errs [appendRacers]error
	)
	race(appendRacers, func(i int) { seqs[i], errs[i] = doors[i].write() })
	for i, err := range errs {
		if err != nil {
			t.Fatalf("el escritor %d falló: %v", i, err)
		}
	}
	sorted := slices.Sorted(slices.Values(seqs[:]))
	for i, seq := range sorted {
		if seq != i+2 {
			t.Fatalf("los seq asignados son %v, quería 2..%d sin huecos ni repetidos", sorted, appendRacers+1)
		}
	}

	after := requireOnlyChanged(t, "la carrera de escritores", m, before, ev.ID)
	entries := after[ev.ID].entries
	if len(entries) != appendRacers+1 {
		t.Fatalf("el historial tiene %d entradas, quería %d", len(entries), appendRacers+1)
	}
	requireEntry(t, "la entrada anterior a la carrera", entries[0], before[ev.ID].entries[0])
	for i, door := range doors {
		want := door.want
		want.Seq = seqs[i]
		requireEntry(t, "la entrada del escritor "+strconv.Itoa(i), entries[seqs[i]-1], want)
	}
}
