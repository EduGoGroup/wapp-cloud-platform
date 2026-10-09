//go:build pendiente

package stages_test

// draft_push_test.go — el contrato de draft_push.go: el empuje al puente CRM, que cuelga
// de UNA marca —`intake_jobs.requested_by`— (R-07, D-044.19). Todos son el mismo par,
// con la marca y sin ella, y la mitad SIN marca es la que protege: el pipeline normal no
// puede empezar a empujar al CRM como efecto colateral.

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// pushed es lo que se le pidió al puente.
type pushed struct {
	tenant, intake string
	revisionNo     int
}

// pushSpy es un stages.CRMPusher que retiene lo que se le pidió. Con mutex porque el
// puerto no promete en qué goroutine se llama.
type pushSpy struct {
	mu    sync.Mutex
	err   error
	calls []pushed
}

func (p *pushSpy) PushRevisionByID(_ context.Context, tenant, intakeID string, revisionNo int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, pushed{tenant, intakeID, revisionNo})
	return p.err
}

func (p *pushSpy) received() []pushed {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pushed(nil), p.calls...)
}

var _ stages.CRMPusher = (*pushSpy)(nil)

// TestCRMPusherFunc_AdaptsAFunctionToThePort: la clausura recibe los cuatro argumentos
// tal cual y su error vuelve tal cual. Es lo que corta el nudo de construcción del
// arranque (la etapa nace antes que el Service de solicitudes).
func TestCRMPusherFunc_AdaptsAFunctionToThePort(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marca")
	boom := errors.New("el puente no está")
	var got pushed
	var sawCtx bool
	var port stages.CRMPusher = stages.CRMPusherFunc(func(c context.Context, tenant, intakeID string, revisionNo int) error {
		sawCtx = c.Value(ctxKey{}) == "marca"
		got = pushed{tenant, intakeID, revisionNo}
		return boom
	})

	err := port.PushRevisionByID(ctx, "t", "i", 7)
	if !errors.Is(err, boom) || !sawCtx || got != (pushed{"t", "i", 7}) {
		t.Fatalf("PushRevisionByID = %v con %+v (ctx %v); se esperaba el error y los argumentos tal cual", err, got, sawCtx)
	}
	if err := stages.CRMPusherFunc(func(context.Context, string, string, int) error { return nil }).PushRevisionByID(ctx, "", "", 0); err != nil {
		t.Fatalf("sin error de la función, el puerto devolvió %v", err)
	}
}

// TestDraftRun_OwnersReanalysisPushesItsRevisionToTheCRM: la tercera puerta de D-044.19.
// UNA llamada, con el tenant del job, la solicitud y el `revision_no` REAL —el que el
// puente usa para su UPSERT—: con historia delante, es el 3 y no un 1 clavado.
func TestDraftRun_OwnersReanalysisPushesItsRevisionToTheCRM(t *testing.T) {
	pusher := &pushSpy{}
	b := newDraftBench(t, ambarNow(), stages.WithCRMPush(pusher))
	b.runAmbar(t, ambarP4Job())
	b.runAmbar(t, ambarP4Job())
	if got := pusher.received(); len(got) != 0 {
		t.Fatalf("las dos pasadas normales empujaron: %+v", got)
	}

	art := b.runAmbar(t, reanalysisJob())

	want := []pushed{{tenant: tenantID, intake: ambarIntakeID, revisionNo: 3}}
	if got := pusher.received(); !reflect.DeepEqual(got, want) || art.RevisionNo != 3 {
		t.Fatalf("empujes = %+v (revisión %d), se esperaba %+v", got, art.RevisionNo, want)
	}
}

// TestDraftRun_PushGoesToTheIntakeThatAlreadyExisted: si el evento ya tenía solicitud
// (la del carrito), lo que se empuja es ESA, no el id derivado.
func TestDraftRun_PushGoesToTheIntakeThatAlreadyExisted(t *testing.T) {
	pusher := &pushSpy{}
	b := newDraftBench(t, ambarNow(), stages.WithCRMPush(pusher))
	b.intakes.rows = append(b.intakes.rows, cartIntakeRow("confirmed"))

	b.runAmbar(t, reanalysisJob())

	want := []pushed{{tenant: tenantID, intake: cartIntakeID, revisionNo: 1}}
	if got := pusher.received(); !reflect.DeepEqual(got, want) {
		t.Fatalf("empujes = %+v, se esperaba %+v", got, want)
	}
}

// TestDraftRun_WithoutTheOwnersMarkNothingIsPushed es R-07, la mitad que protege: si el
// empuje colgara de «he escrito una revisión» y no de la marca del job, TODO borrador
// interpretado saldría al CRM, y el integrador los vería como pedidos nuevos que el dueño
// ni ha mirado. Con puente cableado y sin la marca de la dueña: ni empuje, ni queja.
func TestDraftRun_WithoutTheOwnersMarkNothingIsPushed(t *testing.T) {
	stranger := reanalysisJob()
	stranger.Reanalysis.RequestedBy = "crm"
	cases := map[string]intake.ClaimedJob{
		"normal pipeline":                ambarP4Job(),
		"a mark that is not the owner's": stranger,
	}
	for name, job := range cases {
		t.Run(name, func(t *testing.T) {
			pusher := &pushSpy{}
			b := newDraftBench(t, ambarNow(), stages.WithCRMPush(pusher))
			art := b.runAmbar(t, job)

			if got := pusher.received(); len(got) != 0 {
				t.Fatalf("la revisión %d no es POSTERIOR AL CIERRE y salió al CRM: %+v", art.RevisionNo, got)
			}
			b.assertLogLacks(t, "puente CRM")

			// Y sin puente tampoco se queja: no había nada que empujar.
			quiet := newDraftBench(t, ambarNow())
			quiet.runAmbar(t, job)
			quiet.assertLogLacks(t, "puente CRM")
		})
	}
}

// TestDraftRun_ReanalysisWithoutAPusher_ShoutsAndGoesOn: la ausencia del cable no puede
// ser muda —el integrador se queda con una versión del pedido que ya no es verdad— pero
// tampoco puede tumbar el borrador, que ya está escrito. Pasar nil a WithCRMPush es el
// mismo estado que no llamar a la opción.
func TestDraftRun_ReanalysisWithoutAPusher_ShoutsAndGoesOn(t *testing.T) {
	cases := map[string][]stages.DraftOption{
		"no option":  nil,
		"nil pusher": {stages.WithCRMPush(nil)},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			b := newDraftBench(t, ambarNow(), opts...)
			art := b.runAmbar(t, reanalysisJob())

			if art.IntakeID != ambarIntakeID || art.RevisionNo != 1 || len(b.jobs.saved) != 1 {
				t.Fatalf("el borrador se escribe igual: %+v", *art)
			}
			b.assertLogHas(t, "draft: el re-análisis escribió su revisión y NO hay puente CRM cableado; el CRM se queda con la versión vieja")
			b.assertLogHas(t, "level=ERROR")
			if got := b.events.names(); !reflect.DeepEqual(got, []string{stages.EventDraftCreated, stages.EventReanalyzed}) {
				t.Fatalf("eventos = %v; la falta del puente no se lleva las métricas", got)
			}
		})
	}

	// Un nil DESPUÉS de un puente real no lo desenchufa.
	pusher := &pushSpy{}
	b := newDraftBench(t, ambarNow(), stages.WithCRMPush(pusher), stages.WithCRMPush(nil))
	b.runAmbar(t, reanalysisJob())
	if len(pusher.received()) != 1 {
		t.Fatalf("empujes = %+v; WithCRMPush(nil) no hace nada", pusher.received())
	}
}

// TestDraftRun_PusherFailureDoesNotKillTheDraft: BEST-EFFORT, como la métrica y por lo
// mismo. Lo que se pierde es la ENCOLADA, y el log lo dice con lo necesario para
// reencolarla a mano.
func TestDraftRun_PusherFailureDoesNotKillTheDraft(t *testing.T) {
	pusher := &pushSpy{err: errors.New("el store no responde")}
	b := newDraftBench(t, ambarNow(), stages.WithCRMPush(pusher))

	art := b.runAmbar(t, reanalysisJob())

	if art.IntakeID != ambarIntakeID || len(b.revisions.Revisions(art.IntakeID)) != 1 || len(b.jobs.saved) != 1 {
		t.Fatalf("el empuje fallido tumbó el borrador: %+v", *art)
	}
	if len(pusher.received()) != 1 {
		t.Fatalf("empujes = %+v; se intenta UNA vez, sin reintento aquí", pusher.received())
	}
	b.assertLogHas(t, "draft: no se pudo encolar la revisión del re-análisis para el puente CRM")
	for _, field := range []string{"level=ERROR", "intake_id=" + ambarIntakeID, "revision_no=1", "el store no responde"} {
		b.assertLogHas(t, field)
	}
	if got := b.events.names(); !reflect.DeepEqual(got, []string{stages.EventDraftCreated, stages.EventReanalyzed}) {
		t.Fatalf("eventos = %v; el empuje fallido no se lleva las métricas", got)
	}
}

// TestDraftRun_PushHappensWithTheRevisionAlreadyWritten: el empuje va DESPUÉS de la
// revisión —lo que se empuja ya se puede leer— y ANTES de los eventos y de la marca.
func TestDraftRun_PushHappensWithTheRevisionAlreadyWritten(t *testing.T) {
	var b *draftBench
	var revisionsAtPush, eventsAtPush, marksAtPush int
	pusher := stages.CRMPusherFunc(func(_ context.Context, _, intakeID string, _ int) error {
		revisionsAtPush = len(b.revisions.PersistedRevisions(intakeID))
		eventsAtPush = len(b.events.calls)
		marksAtPush = len(b.jobs.saved)
		return nil
	})
	b = newDraftBench(t, ambarNow(), stages.WithCRMPush(pusher))
	b.runAmbar(t, reanalysisJob())

	if revisionsAtPush != 1 || eventsAtPush != 0 || marksAtPush != 0 {
		t.Fatalf("al empujar había %d revisiones, %d eventos y %d marcas; se esperaba 1, 0 y 0",
			revisionsAtPush, eventsAtPush, marksAtPush)
	}
}
