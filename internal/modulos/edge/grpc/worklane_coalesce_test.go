package grpc

// El carril de trabajo (R-G3), parte 2: la coalescencia de heartbeats (D-050.4) y el freno
// con la cola llena (REQ-050.4). Los dobles están en worklane_test.go.

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// R-G3: el heartbeat se coalesce y SOLO corre el último. Se afirma CUÁL, no solo cuántos: con
// la política invertida («descartar el nuevo si ya hay uno pendiente») saldría [hb1] —el
// recuento sigue siendo 1 y todo parece correcto, pero la salud persistida es la del PASADO—;
// sin coalescencia, [hb1 hb2 hb3].
func TestWorkLaneHeartbeatsCoalesceAndOnlyTheLastRuns(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 8, time.Minute, quietLog())
	rec := &laneRecorder{}
	release := plugLane(t, lane, "s-1", jobReceipt)

	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb1"))
	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb2"))
	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb3"))
	if n := queueLen(lane, "s-1"); n != 1 {
		t.Fatalf("tres heartbeats pendientes ocupan %d huecos de la cola, se esperaba 1", n)
	}

	release()
	closeLane(t, lane)

	if got := rec.order(); got != "[hb3]" {
		t.Fatalf("ejecutados = %s, se esperaba exactamente [hb3]", got)
	}
}

// R-G3: la sustitución es EN SITIO. El heartbeat nuevo ocupa la posición del viejo, no se
// reencola al final — reencolarlo dejaría la salud detrás de receipts que llegaron DESPUÉS.
func TestWorkLaneCoalescedHeartbeatKeepsItsPosition(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 8, time.Minute, quietLog())
	rec := &laneRecorder{}
	release := plugLane(t, lane, "s-1", jobReceipt)

	mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r1"))
	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb-old"))
	mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r2"))
	mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r3"))
	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb-new"))

	release()
	closeLane(t, lane)

	if got := rec.order(); got != "[r1 hb-new r2 r3]" {
		t.Fatalf("ejecutados = %s, se esperaba [r1 hb-new r2 r3]: el latido nuevo en el sitio del viejo", got)
	}
}

// R-G3: la coalescencia es SOLO del heartbeat. Tres jobs seguidos de cualquier otro tipo son
// tres hechos distintos y se ejecutan los tres.
func TestWorkLaneOnlyHeartbeatsAreCoalesced(t *testing.T) {
	t.Parallel()
	for _, kind := range []jobKind{jobReceipt, jobAuth, jobDiagnostics, jobOffline, jobLogout} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			lane := newWorkLane(context.Background(), 8, time.Minute, quietLog())
			rec := &laneRecorder{}
			release := plugLane(t, lane, "s-1", jobReceipt)

			mustSubmit(t, lane, "s-1", kind, rec.task("a"))
			mustSubmit(t, lane, "s-1", kind, rec.task("b"))
			mustSubmit(t, lane, "s-1", kind, rec.task("c"))

			release()
			closeLane(t, lane)

			if got := rec.order(); got != "[a b c]" {
				t.Fatalf("ejecutados = %s, se esperaba [a b c]: %s no se coalesce", got, kind)
			}
		})
	}
}

// R-G3: un logout ni se coalesce ni lo borra un latido posterior. [hb] significaría que el
// latido BORRÓ el logout y la sesión zombi seguiría renovando su lease; [hb logout], que el
// logout perdió su posición en la cola de su sesión.
func TestWorkLaneLogoutIsNeitherCoalescedNorErasedByALaterHeartbeat(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 8, time.Minute, quietLog())
	rec := &laneRecorder{}
	release := plugLane(t, lane, "s-1", jobReceipt)

	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb-old"))
	mustSubmit(t, lane, "s-1", jobLogout, rec.task("logout"))
	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb-new"))

	release()
	closeLane(t, lane)

	if got := rec.order(); got != "[hb-new logout]" {
		t.Fatalf("ejecutados = %s, se esperaba [hb-new logout]: el latido se sustituye en SU sitio y el logout queda intacto", got)
	}
}

// El hueco del heartbeat pendiente SIGUE a la cola según el worker la consume: tras sacar el
// job de delante, la sustitución tiene que caer sobre el latido y no sobre su vecino. (Regla
// 2 del carril: «pop + limpiar el índice» en el mismo tramo crítico.)
func TestWorkLanePendingHeartbeatSlotFollowsTheQueue(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 8, time.Minute, quietLog())
	rec := &laneRecorder{}
	releaseFirst := plugLane(t, lane, "s-1", jobReceipt)

	// Cola: [second-plug hb-old r1]. El segundo tapón deja al worker parado con el latido ya
	// en la cabeza de la cola.
	entered := make(chan struct{})
	gate := make(chan struct{})
	mustSubmit(t, lane, "s-1", jobReceipt, func(context.Context) {
		close(entered)
		<-gate
	})
	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb-old"))
	mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r1"))
	releaseFirst()
	await(t, entered, "el worker entra en el segundo tapón")

	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb-new"))
	mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r2"))
	close(gate)
	closeLane(t, lane)

	if got := rec.order(); got != "[hb-new r1 r2]" {
		t.Fatalf("ejecutados = %s, se esperaba [hb-new r1 r2]: la sustitución cayó fuera del latido pendiente", got)
	}
}

// Un heartbeat que YA está corriendo no es «pendiente»: el siguiente no lo sustituye (ya no
// se puede), se encola como uno nuevo. Y ese sí vuelve a ser coalescible.
func TestWorkLaneRunningHeartbeatIsNotReplaced(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 8, time.Minute, quietLog())
	rec := &laneRecorder{}
	release := plugLane(t, lane, "s-1", jobHeartbeat) // el tapón ES un latido en vuelo

	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb2"))
	mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r1"))
	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb3"))

	release()
	closeLane(t, lane)

	if got := rec.order(); got != "[hb3 r1]" {
		t.Fatalf("ejecutados = %s, se esperaba [hb3 r1]", got)
	}
}

// La coalescencia es POR SESIÓN: el latido de una sesión no sustituye al de otra.
func TestWorkLaneCoalescingIsPerSession(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 8, time.Minute, quietLog())
	rec := &laneRecorder{}
	sessions := []string{"s-1", "s-2", "s-3"}
	releases := make([]func(), 0, len(sessions))
	for _, sid := range sessions {
		releases = append(releases, plugLane(t, lane, sid, jobReceipt))
		mustSubmit(t, lane, sid, jobHeartbeat, rec.task("hb-"+sid))
	}
	for _, sid := range sessions {
		if n := queueLen(lane, sid); n != 1 {
			t.Fatalf("la cola de %s tiene %d jobs, se esperaba su latido", sid, n)
		}
	}
	for _, release := range releases {
		release()
	}
	closeLane(t, lane)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.seen) != len(sessions) {
		t.Fatalf("corrieron %v, se esperaba un latido por sesión", rec.seen)
	}
}

// brakedSubmit lanza un submit que DEBERÍA frenar y devuelve por dónde llega su resultado: el
// error y si, cuando volvió, el test ya había dado paso (gate).
//
// Exige correr DENTRO de una burbuja de synctest (hallazgo 38): synctest.Wait solo vuelve
// cuando todas las goroutines de la burbuja están bloqueadas de verdad, así que al volver de
// aquí el submit o está frenado en su sync.Cond o ya terminó. Un submit que NO frena queda
// delatado siempre, no «casi siempre».
type brakedResult struct {
	err          error
	afterTheGate bool
}

func brakedSubmit(t *testing.T, lane *workLane, sessionID string, kind jobKind, run func(context.Context), gate *atomic.Bool) <-chan brakedResult {
	t.Helper()
	started := make(chan struct{})
	res := make(chan brakedResult, 1)
	go func() {
		close(started)
		err := lane.submit(sessionID, kind, run)
		res <- brakedResult{err: err, afterTheGate: gate.Load()}
	}()
	await(t, started, "arranca el submit que debe frenar")
	synctest.Wait()
	return res
}

// R-G3 · REQ-050.4: con la cola llena el submit FRENA al llamante. Ni descarta (se ejecutan
// los tres, en orden) ni crece (la cola no pasa de su tope mientras espera), y no vuelve
// hasta que el worker hace sitio.
func TestWorkLaneFullQueueBrakesWithoutLosingOrGrowing(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		lane := newWorkLane(context.Background(), 2, time.Minute, quietLog())
		rec := &laneRecorder{}
		release := plugLane(t, lane, "s-1", jobReceipt)

		mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r1"))
		mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r2"))

		var released atomic.Bool
		third := brakedSubmit(t, lane, "s-1", jobReceipt, rec.task("r3"), &released)

		if n := queueLen(lane, "s-1"); n != 2 {
			t.Fatalf("con el worker tapado la cola tiene %d jobs, se esperaba su tope (2): creció", n)
		}

		released.Store(true)
		release()
		got := await(t, third, "el submit frenado vuelve cuando hay sitio")
		if got.err != nil {
			t.Fatalf("el submit frenado acabó en error: %v", got.err)
		}
		if !got.afterTheGate {
			t.Fatal("el tercer submit volvió ANTES de que el worker hiciera sitio: no frenó")
		}
		closeLane(t, lane)

		if order := rec.order(); order != "[r1 r2 r3]" {
			t.Fatalf("procesados = %s, se esperaba [r1 r2 r3]: frenar significa no perder nada", order)
		}
	})
}

// REQ-050.4 manda sobre REQ-050.5: un heartbeat SIN hueco que sustituir (no hay ninguno
// pendiente) frena con la cola llena igual que cualquier otro job.
func TestWorkLaneHeartbeatWithoutPendingSlotBrakesToo(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		lane := newWorkLane(context.Background(), 2, time.Minute, quietLog())
		rec := &laneRecorder{}
		release := plugLane(t, lane, "s-1", jobReceipt)

		mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r1"))
		mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r2"))

		var released atomic.Bool
		hb := brakedSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb"), &released)
		if n := queueLen(lane, "s-1"); n != 2 {
			t.Fatalf("la cola tiene %d jobs, se esperaba su tope (2): el latido se coló", n)
		}

		released.Store(true)
		release()
		if got := await(t, hb, "el latido frenado vuelve cuando hay sitio"); got.err != nil || !got.afterTheGate {
			t.Fatalf("latido sin hueco con la cola llena = %+v, se esperaba que frenara y entrara después", got)
		}
		closeLane(t, lane)

		if order := rec.order(); order != "[r1 r2 hb]" {
			t.Fatalf("procesados = %s, se esperaba [r1 r2 hb]", order)
		}
	})
}

// Con hueco, en cambio, el heartbeat NO frena aunque la cola esté llena: sustituir no ocupa
// sitio. Y el jobOffline tampoco frena nunca: es el último job del cierre y perderlo dejaría
// la flota mostrando «online» un Edge que ya se fue (crecimiento acotado: uno por sesión).
func TestWorkLaneCoalescingAndOfflineDoNotBrakeOnAFullQueue(t *testing.T) {
	t.Parallel()
	lane := newWorkLane(context.Background(), 2, time.Minute, quietLog())
	rec := &laneRecorder{}
	release := plugLane(t, lane, "s-1", jobReceipt)

	mustSubmit(t, lane, "s-1", jobHeartbeat, rec.task("hb-old"))
	mustSubmit(t, lane, "s-1", jobReceipt, rec.task("r1"))

	done := make(chan error, 2)
	go func() {
		done <- lane.submit("s-1", jobHeartbeat, rec.task("hb-new"))
		done <- lane.submit("s-1", jobOffline, rec.task("offline"))
	}()
	for _, what := range []string{"el latido con hueco entra sin frenar", "el offline entra sin frenar"} {
		if err := await(t, done, what); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	if n := queueLen(lane, "s-1"); n != 3 {
		t.Fatalf("la cola tiene %d jobs, se esperaban 3 (el tope más el offline)", n)
	}

	release()
	closeLane(t, lane)

	if order := rec.order(); order != "[hb-new r1 offline]" {
		t.Fatalf("procesados = %s, se esperaba [hb-new r1 offline]", order)
	}
}
