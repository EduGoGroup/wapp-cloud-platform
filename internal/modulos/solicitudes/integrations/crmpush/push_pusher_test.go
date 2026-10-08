//go:build pendiente

package crmpush

// push_pusher_test.go — la REGLA del `intake.push`: Pusher.Push, NewPusher y
// WithClock. Partido de push_test.go por tamaño (E-13); los dobles viven allí.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// pusherFixture es un Pusher sobre los tres dobles y el reloj fijo.
type pusherFixture struct {
	pusher *Pusher
	queue  *fakeQueue
	gate   *fakeGate
	log    *recordingLogger
}

// newPusherFixture arma el encolador con el gate del tenant de prueba abierto o
// cerrado, y los errores que deban devolver el gate y la cola.
func newPusherFixture(open bool, gateErr, queueErr error) pusherFixture {
	f := pusherFixture{
		queue: &fakeQueue{err: queueErr, firstID: 41},
		gate:  &fakeGate{open: map[string]bool{sampleTenant: open}, err: gateErr},
		log:   newRecordingLogger(),
	}
	f.pusher = NewPusher(f.log, f.queue, f.gate, WithClock(fixedClock))
	return f
}

// ctxKey marca el contexto del test para comprobar que es el que llega a los puertos.
type ctxKey struct{}

// TestPush_OpenGateEnqueuesOnce (R-13): el camino feliz — UNA consulta al gate, UN
// INSERT con el tenant y el kind del contrato, y el cuerpo es el documento de Build.
func TestPush_OpenGateEnqueuesOnce(t *testing.T) {
	f := newPusherFixture(true, nil, nil)
	ctx := context.WithValue(context.Background(), ctxKey{}, "marca")

	res, err := f.pusher.Push(ctx, sampleInput())
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	want := Build(sampleInput(), fixedClock())
	if !res.Enqueued || res.OutboxID != 41 || !reflect.DeepEqual(res.Payload, want) {
		t.Fatalf("Result = %+v; quiero Enqueued=true, OutboxID=41 (el que dio el almacén) y el documento de Build", res)
	}
	if !reflect.DeepEqual(f.gate.calls, []string{sampleTenant}) {
		t.Fatalf("el gate se consultó con %v; quiero UNA consulta con %q", f.gate.calls, sampleTenant)
	}
	calls := f.queue.recorded()
	if len(calls) != 1 {
		t.Fatalf("se encoló %d veces, quiero 1", len(calls))
	}
	if calls[0].kind != "intake.push" || calls[0].tenantID != sampleTenant {
		t.Fatalf("kind=%q tenant=%q; quiero intake.push y %q", calls[0].kind, calls[0].tenantID, sampleTenant)
	}
	if f.gate.ctxs[0].Value(ctxKey{}) != "marca" || calls[0].ctx.Value(ctxKey{}) != "marca" {
		t.Fatal("el contexto del llamante no llegó al gate o al almacén")
	}
	wantBody, _ := wireDoc(t, want)
	if string(calls[0].payload) != string(wantBody) {
		t.Fatalf("el cuerpo encolado no es el JSON del documento\n got: %s\nwant: %s", calls[0].payload, wantBody)
	}
	// Por NOMBRE DE CABLE: un decode tipado taparía una etiqueta json renombrada.
	var body map[string]any
	if err := json.Unmarshal(calls[0].payload, &body); err != nil {
		t.Fatalf("el cuerpo encolado no es JSON válido: %v", err)
	}
	if body["lifecycle_status"] != statusPendingApproval || body["revision_no"] != float64(4) {
		t.Fatalf("lifecycle_status=%#v revision_no=%#v; quiero %q y 4 (los del llamante, R-12)",
			body["lifecycle_status"], body["revision_no"], statusPendingApproval)
	}
	if n := len(f.log.at("error")); n != 0 {
		t.Fatalf("un empuje completo no denuncia nada y hubo %d líneas de Error", n)
	}
}

// TestPush_ClosedGateDoesNotEnqueueAndIsNotAnError (R-13): un tenant sin puente CRM
// activo no es una avería. Sale por Result.Enqueued y no por error, y queda en Debug.
func TestPush_ClosedGateDoesNotEnqueueAndIsNotAnError(t *testing.T) {
	f := newPusherFixture(false, nil, nil)

	res, err := f.pusher.Push(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("un gate cerrado NO es un error: %v", err)
	}
	if !reflect.DeepEqual(res, Result{}) {
		t.Fatalf("Result = %+v; con el gate cerrado quiero el valor cero", res)
	}
	if n := len(f.queue.recorded()); n != 0 {
		t.Fatalf("gate cerrado no debe encolar, encoló %d", n)
	}
	if len(f.gate.calls) != 1 {
		t.Fatalf("el gate se consultó %d veces, quiero 1", len(f.gate.calls))
	}
	debug := f.log.at("debug")
	if len(debug) != 1 || debug[0].msg != "crmpush: tenant sin puente CRM activo, no se encola" {
		t.Fatalf("Debug = %+v; quiero UNA línea con el mensaje literal", debug)
	}
	if debug[0].fields["tenant"] != sampleTenant || debug[0].fields["intake_id"] != sampleIntake {
		t.Fatalf("claves del Debug = %v; quiero tenant e intake_id", debug[0].fields)
	}
}

// TestPush_GateErrorFailsClosed: el error SÍ sube —el llamante decide qué hacer con
// él— pero no se encola nada: un puente que no se pudo evaluar no recibe basura.
func TestPush_GateErrorFailsClosed(t *testing.T) {
	down := errors.New("resolver caído")
	f := newPusherFixture(true, down, nil)

	res, err := f.pusher.Push(context.Background(), sampleInput())
	if !errors.Is(err, down) {
		t.Fatalf("err = %v; quiero el error del gate envuelto", err)
	}
	if got, want := err.Error(), "crmpush: evaluar el gate del puente CRM de tenant-abc: resolver caído"; got != want {
		t.Fatalf("texto del error\n got: %s\nwant: %s", got, want)
	}
	if !reflect.DeepEqual(res, Result{}) {
		t.Fatalf("Result = %+v; con el gate en error quiero el valor cero", res)
	}
	if n := len(f.queue.recorded()); n != 0 {
		t.Fatalf("gate en error no debe encolar (fail-closed), encoló %d", n)
	}
}

// TestPush_QueueErrorSurfaces: el fallo del INSERT no se traga aquí. Quien decide qué
// hacer es la puerta.
func TestPush_QueueErrorSurfaces(t *testing.T) {
	lost := errors.New("conexión perdida")
	f := newPusherFixture(true, nil, lost)

	res, err := f.pusher.Push(context.Background(), sampleInput())
	if !errors.Is(err, lost) {
		t.Fatalf("err = %v; un fallo del almacén tiene que subir envuelto", err)
	}
	want := "crmpush: encolar intake.push de 11111111-1111-1111-1111-111111111111: conexión perdida"
	if err.Error() != want {
		t.Fatalf("texto del error\n got: %s\nwant: %s", err.Error(), want)
	}
	if res.Enqueued || res.OutboxID != 0 {
		t.Fatalf("Result = %+v; con el INSERT fallido quiero Enqueued=false y OutboxID=0", res)
	}
	if !reflect.DeepEqual(res.Payload, Build(sampleInput(), fixedClock())) {
		t.Fatalf("el Result no trae el documento que se armó: %+v", res.Payload)
	}
}

// TestPush_UnserializableDocumentSurfaces: un total que JSON no sabe escribir no se
// encola a medias; sube con su propio prefijo.
func TestPush_UnserializableDocumentSurfaces(t *testing.T) {
	f := newPusherFixture(true, nil, nil)
	in := sampleInput()
	in.Total = math.NaN()

	res, err := f.pusher.Push(context.Background(), in)
	var unsupported *json.UnsupportedValueError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v; quiero el error de serialización envuelto", err)
	}
	prefix := "crmpush: serializar intake.push de 11111111-1111-1111-1111-111111111111: "
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("texto del error = %q; quiero el prefijo %q", err.Error(), prefix)
	}
	if res.Enqueued || res.OutboxID != 0 || res.Payload.IntakeID != sampleIntake {
		t.Fatalf("Result = %+v; quiero Enqueued=false, OutboxID=0 y el documento armado", res)
	}
	if n := len(f.queue.recorded()); n != 0 {
		t.Fatalf("un documento que no se pudo serializar no se encola, encoló %d", n)
	}
}

// TestPush_HalfBuiltPusherIsANoOp: un Pusher a medias no encola, no consulta el gate
// y no panica: apaga el empuje, no mata al llamante.
func TestPush_HalfBuiltPusherIsANoOp(t *testing.T) {
	openGate := func() *fakeGate { return &fakeGate{open: map[string]bool{sampleTenant: true}} }
	t.Run("nil receiver", func(t *testing.T) {
		var p *Pusher
		if res, err := p.Push(context.Background(), sampleInput()); err != nil || !reflect.DeepEqual(res, Result{}) {
			t.Fatalf("Result=%+v err=%v; quiero el valor cero y sin error", res, err)
		}
	})
	for _, c := range []struct {
		name  string
		build func(q *fakeQueue, g *fakeGate) *Pusher
	}{
		{"no queue", func(_ *fakeQueue, g *fakeGate) *Pusher { return NewPusher(newRecordingLogger(), nil, g) }},
		{"no gate", func(q *fakeQueue, _ *fakeGate) *Pusher { return NewPusher(newRecordingLogger(), q, nil) }},
		{"no log", func(q *fakeQueue, g *fakeGate) *Pusher { return NewPusher(nil, q, g) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			q, g := &fakeQueue{}, openGate()
			p := c.build(q, g)
			if p == nil {
				t.Fatal("NewPusher devolvió nil; nunca debe")
			}
			res, err := p.Push(context.Background(), sampleInput())
			if err != nil {
				t.Fatalf("un Pusher a medias no debe fallar: %v", err)
			}
			if !reflect.DeepEqual(res, Result{}) {
				t.Fatalf("Result = %+v; un Pusher a medias devuelve el valor cero", res)
			}
			if len(q.recorded()) != 0 || len(g.calls) != 0 {
				t.Fatalf("un Pusher a medias encoló %d veces y consultó el gate %d; quiero 0 y 0",
					len(q.recorded()), len(g.calls))
			}
		})
	}
}

// TestPush_DenouncesWhatTheSchemaWillRejectButStillEnqueues (R-12): un push sin
// número o sin estado se encola IGUAL —un rechazo visible del puente vale más que
// perder el pedido en silencio— y queda denunciado en Error con su intake_id.
func TestPush_DenouncesWhatTheSchemaWillRejectButStillEnqueues(t *testing.T) {
	const (
		noRevision = "crmpush: intake.push sin revision_no; se encola con un número que el contrato " +
			"rechaza en vez de inventar uno (un número FALSO lo aplica el puente sin sospechar)"
		noStatus = "crmpush: intake.push sin lifecycle_status; se encola vacío en vez de inventar " +
			"un estado (el literal `confirmed` que esto sustituye mentía en cuanto la solicitud no venía " +
			"de un cierre de carrito)"
	)
	for _, c := range []struct {
		name     string
		revision int
		status   string
		want     []string
	}{
		{"absent revision_no", 0, statusPendingApproval, []string{noRevision}},
		{"negative revision_no", -1, statusPendingApproval, []string{noRevision}},
		{"absent lifecycle_status", 4, "", []string{noStatus}},
		{"both absent", 0, "", []string{noRevision, noStatus}},
		{"first revision is legitimate", 1, statusClosedLegacy, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newPusherFixture(true, nil, nil)
			in := sampleInput()
			in.RevisionNo, in.LifecycleStatus = c.revision, c.status

			res, err := f.pusher.Push(context.Background(), in)
			if err != nil || !res.Enqueued || len(f.queue.recorded()) != 1 {
				t.Fatalf("la denuncia NO aborta el encolado: err=%v Enqueued=%v encolados=%d",
					err, res.Enqueued, len(f.queue.recorded()))
			}
			if res.Payload.RevisionNo != c.revision {
				t.Fatalf("revision_no encolado = %d, quiero %d: no se inventa un número", res.Payload.RevisionNo, c.revision)
			}
			logged := f.log.at("error")
			var msgs []string
			for _, e := range logged {
				msgs = append(msgs, e.msg)
				if e.fields["tenant"] != sampleTenant || e.fields["intake_id"] != sampleIntake {
					t.Errorf("claves de la denuncia = %v; quiero tenant e intake_id", e.fields)
				}
				if e.msg == noRevision && e.fields["revision_no"] != c.revision {
					t.Errorf("revision_no de la denuncia = %#v, quiero %d", e.fields["revision_no"], c.revision)
				}
			}
			if !reflect.DeepEqual(msgs, c.want) {
				t.Fatalf("denuncias en Error\n got: %q\nwant: %q", msgs, c.want)
			}
		})
	}
}

// TestWithClock_LastNonNilWins: el reloj inyectado es el del `timestamp`; un nil no
// pisa al que ya había.
func TestWithClock_LastNonNilWins(t *testing.T) {
	other := func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }
	for _, c := range []struct {
		name string
		opts []Option
		want string
	}{
		{"single clock", []Option{WithClock(fixedClock)}, "2026-08-27T10:00:00Z"},
		{"the last one wins", []Option{WithClock(fixedClock), WithClock(other)}, "2030-01-02T03:04:05Z"},
		{"nil does not override", []Option{WithClock(other), WithClock(nil)}, "2030-01-02T03:04:05Z"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := NewPusher(newRecordingLogger(), &fakeQueue{}, &fakeGate{open: map[string]bool{sampleTenant: true}}, c.opts...)
			res, err := p.Push(context.Background(), sampleInput())
			if err != nil {
				t.Fatalf("Push: %v", err)
			}
			if res.Payload.Timestamp != c.want {
				t.Fatalf("timestamp = %q, quiero %q", res.Payload.Timestamp, c.want)
			}
		})
	}
}

// TestNewPusher_DefaultClockIsUTC: sin reloj inyectado (o con uno nil) el timestamp
// sale del reloj del proceso EN UTC. No se compara contra la hora: solo la zona.
func TestNewPusher_DefaultClockIsUTC(t *testing.T) {
	for name, opts := range map[string][]Option{"no option": nil, "nil clock": {WithClock(nil)}} {
		t.Run(name, func(t *testing.T) {
			p := NewPusher(newRecordingLogger(), &fakeQueue{}, &fakeGate{open: map[string]bool{sampleTenant: true}}, opts...)
			res, err := p.Push(context.Background(), sampleInput())
			if err != nil {
				t.Fatalf("Push: %v", err)
			}
			ts, perr := time.Parse(time.RFC3339, res.Payload.Timestamp)
			if perr != nil || !strings.HasSuffix(res.Payload.Timestamp, "Z") || ts.IsZero() {
				t.Fatalf("timestamp = %q (%v); quiero un instante RFC3339 en UTC", res.Payload.Timestamp, perr)
			}
		})
	}
}

// TestPush_IsSafeForConcurrentUse: las dos puertas comparten UN Pusher. Con -race,
// este test es el que delata un estado propio mal guardado.
func TestPush_IsSafeForConcurrentUse(t *testing.T) {
	f := newPusherFixture(true, nil, nil)
	const pushes = 32
	var wg sync.WaitGroup
	errs := make(chan error, pushes)
	for i := range pushes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := sampleInput()
			in.RevisionNo = i + 1
			if _, err := f.pusher.Push(context.Background(), in); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Push concurrente: %v", err)
	}
	seen := map[float64]bool{}
	for _, c := range f.queue.recorded() {
		var body map[string]any
		if err := json.Unmarshal(c.payload, &body); err != nil {
			t.Fatalf("el cuerpo encolado no es JSON válido: %v", err)
		}
		n, _ := body["revision_no"].(float64)
		seen[n] = true
	}
	if len(seen) != pushes {
		t.Fatalf("se encolaron %d revisiones distintas, quiero %d: algún empuje pisó a otro", len(seen), pushes)
	}
}
