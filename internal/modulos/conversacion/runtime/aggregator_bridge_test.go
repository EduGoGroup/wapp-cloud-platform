package runtime

import (
	"context"
	"slices"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// El puente del motor con el agregador, probado directamente: qué IncomingRef construye. Sus
// tres llamantes (AG-7) se prueban a través del Runtime en incoming_aggregation_test.go.

// aggregatorBridgeClock es el reloj del RUNTIME: distinto del reloj del agregador, para que se
// vea de cuál sale la base de fechas.
var aggregatorBridgeClock = time.Date(2026, 3, 4, 9, 30, 0, 0, time.FixedZone("x", -5*3600))

// newAggregatorBridge monta un Runtime mínimo (solo el reloj y el agregador, que es todo lo que
// el puente toca) sobre el entorno del agregador.
func newAggregatorBridge(rig *aggregatorRig) *Runtime {
	return &Runtime{
		now:        func() time.Time { return aggregatorBridgeClock },
		aggregator: rig.aggregator(WithAheadRequester(rig.ahead)),
	}
}

// TestObserveForAggregation_BuildsTheIncomingRef: la tupla, el wa_message_id y el texto del
// entrante llegan al agregador tal cual, sin medias, y la base de fechas es el ts_unix del
// mensaje en UTC.
func TestObserveForAggregation_BuildsTheIncomingRef(t *testing.T) {
	rig := newAggregatorRig()
	rt := newAggregatorBridge(rig)
	sent := time.Date(2026, 3, 4, 8, 0, 7, 0, time.UTC)
	msg := &cloudlinkv1.IncomingMessage{WaMessageId: "wa-1", TsUnix: sent.Unix(), Text: aggregatorClientText}

	rt.observeForAggregation(context.Background(), aggregatorTenant, "session-9", "contact-opaque", "event-1", msg)

	jobs := rig.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("el puente dejó %d filas, quería 1", len(jobs))
	}
	if want := aggregatorKey("event-1"); jobs[0].Key != want {
		t.Errorf("tupla = %+v, quería %+v", jobs[0].Key, want)
	}
	if !slices.Equal(jobs[0].SourceRefs, []string{"wa-1"}) {
		t.Errorf("source_refs = %v, quería solo [wa-1]", jobs[0].SourceRefs)
	}
	if got := jobs[0].MessageTS; !got.Equal(sent) || got.Location() != time.UTC {
		t.Errorf("message_ts = %v, quería el ts_unix del mensaje en UTC (%v)", got, sent)
	}
	if want := []aggregatorRequest{{aggregatorKey("event-1"), aggregatorClientText}}; !slices.Equal(rig.ahead.requested(), want) {
		t.Errorf("peticiones de clasificación = %+v, quería %+v", rig.ahead.requested(), want)
	}
	rig.requireNoSecret(t)
}

// TestObserveForAggregation_WithoutClientTimestampUsesTheRuntimeClock: si ts_unix no es > 0, la
// base de fechas es el reloj del runtime (WithClock), tal cual lo da.
func TestObserveForAggregation_WithoutClientTimestampUsesTheRuntimeClock(t *testing.T) {
	for name, ts := range map[string]int64{"zero": 0, "negative": -1} {
		t.Run(name, func(t *testing.T) {
			rig := newAggregatorRig()
			rt := newAggregatorBridge(rig)

			rt.observeForAggregation(context.Background(), aggregatorTenant, "session-9", "contact-opaque", "event-1",
				&cloudlinkv1.IncomingMessage{WaMessageId: "wa-1", TsUnix: ts})

			jobs := rig.jobs.Jobs()
			if len(jobs) != 1 {
				t.Fatalf("el puente dejó %d filas, quería 1", len(jobs))
			}
			if !jobs[0].MessageTS.Equal(aggregatorBridgeClock) {
				t.Errorf("message_ts = %v, quería el reloj del runtime (%v)", jobs[0].MessageTS, aggregatorBridgeClock)
			}
		})
	}

	// El primer segundo de la época SÍ es un ts del cliente.
	rig := newAggregatorRig()
	newAggregatorBridge(rig).observeForAggregation(context.Background(), aggregatorTenant, "session-9", "contact-opaque", "event-1",
		&cloudlinkv1.IncomingMessage{WaMessageId: "wa-1", TsUnix: 1})
	if got := rig.jobs.Jobs()[0].MessageTS; !got.Equal(time.Unix(1, 0)) {
		t.Errorf("message_ts = %v, quería el ts_unix 1", got)
	}
}

// TestObserveForAggregation_NilSafe: sin agregador o sin mensaje no hace nada (ni lee el reloj).
func TestObserveForAggregation_NilSafe(t *testing.T) {
	rig := newAggregatorRig()
	withoutAggregator := &Runtime{}
	withoutAggregator.observeForAggregation(context.Background(), aggregatorTenant, "session-9", "contact-opaque", "event-1",
		&cloudlinkv1.IncomingMessage{WaMessageId: "wa-1"})

	newAggregatorBridge(rig).observeForAggregation(context.Background(), aggregatorTenant, "session-9", "contact-opaque", "event-1", nil)

	if got := rig.jobs.Counters(); got != (intake.Counters{}) {
		t.Errorf("un entrante nil tocó intake_jobs: %+v", got)
	}
	if len(rig.ents.asks()) != 0 {
		t.Error("un entrante nil llegó a preguntar por la feature")
	}
}
