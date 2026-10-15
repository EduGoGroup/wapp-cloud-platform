//go:build laboratorio

// e12-ahead es el escalón E12 del laboratorio (Plan 058 · Bloque C): EL ADELANTO P1.
//
// La ventana de E09 cierra por su reloj: 45 s de silencio. Si el primer mensaje ya dice «quiero un
// presupuesto de…», esperar 45 s es tiempo perdido. El adelanto (intakeahead.Pool) PIDE una
// clasificación P1 por cada mensaje que entra en una ventana; si vuelve `intake_request` con
// confianza ≥ 0,7, el agregador anota una PISTA y el siguiente barrido cierra la ventana YA, sin
// esperar al silencio. Cualquier otra respuesta no cambia nada: la ventana sigue con su reloj.
//
//   - REAL: el agregador (OnClassified, Sweep), el pool del adelanto (cola, workers, reintento a
//     0,3, el filtro de evidencia), el catálogo de intenciones (intentcfg.MemoryStore con el blob
//     publicado y su validador), el prompt de P1 y su parseo.
//
//   - DOBLE: el reloj del agregador, el almacén de ventanas, los derechos y el viaje al Edge (frame).
//
//     GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e12-ahead
//     … -frame ollama                 (Gemma clasifica de verdad cada mensaje)
//     … -threshold 0.95               (otro umbral: WithIntentConfidence)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-shared/intents"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intakeahead"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

const (
	session = "lab-session-e12"
	contact = "lab-contact-ana"
	// defaultThreshold es el umbral de PLATAFORMA del agregador (runtime.defaultIntentConfidence, no
	// exportado): 0,7. Se repite aquí solo para la bandera -threshold.
	defaultThreshold = 0.7
)

// scene es un mensaje que abre una ventana nueva y lo que el guion contestaría en P1. Con
// -frame ollama, script se ignora: contesta Gemma.
type scene struct {
	title  string
	text   string
	script func(text string) string
	// ahead dice si una PERSONA esperaría que este mensaje adelantara el cierre. Con -frame script
	// el guion está escrito para cumplirlo; con -frame ollama es el patrón contra el que se mide
	// al modelo (la escena 4 es solo del guion: Gemma no inventa la evidencia).
	ahead bool
	// followUp, si no es «», es lo que el cliente escribe justo después del cierre.
	followUp string
}

func scenes() []scene {
	return []scene{
		{
			title: "Un pedido claro: intake_request con confianza alta",
			text:  "Hola! quiero encargar 2 tortas de chocolate para el sábado",
			script: func(t string) string {
				return common.P1Reply(common.IntentIntakeRequest, 0.95, t)
			},
			ahead:    true,
			followUp: "ah, y también un paquete de tequeños de 30",
		},
		{
			title: "Un pedido dudoso: intake_request con confianza baja",
			text:  "y eso cuánto sale?",
			script: func(t string) string {
				return common.P1Reply(common.IntentIntakeRequest, 0.55, t)
			},
		},
		{
			title: "Otra intención, aunque sea segurísima",
			text:  "¿a qué hora cierran hoy?",
			script: func(t string) string {
				return common.P1Reply(common.IntentOpeningHours, 0.99, t)
			},
		},
		{
			title: "Una evidencia que no está en el mensaje (con el guion; con Gemma, un pedido corto)",
			text:  "necesito presupuesto para 30 empanadas de carne",
			script: func(string) string {
				return common.P1Reply(common.IntentIntakeRequest, 0.97, "quiero tequeños")
			},
		},
	}
}

func main() {
	flags := common.RegisterFlags(nil)
	threshold := flag.Float64("threshold", defaultThreshold, "umbral de confianza del adelanto (runtime.WithIntentConfidence)")
	flag.Parse()
	if err := run(flags, *threshold); err != nil {
		os.Exit(1)
	}
}

// lab es el montaje del escalón.
type lab struct {
	n         *common.Narrator
	ctx       context.Context
	clock     *common.Clock
	start     time.Time
	spy       *notifyingFrame
	jobs      *intake.MemoryStore
	agg       *runtime.IntakeAggregator
	pool      *intakeahead.Pool
	threshold float64
	// classified recibe lo que el pool entrega al agregador (lo intercepta el Sink).
	classified chan verdict
	script     *common.ScriptFrame
}

type verdict struct {
	key        intake.WindowKey
	intent     string
	confidence float64
}

func run(flags *common.Flags, threshold float64) error {
	n := common.NewNarrator(nil)
	n.Title("E12 · El adelanto P1",
		"Cómo una clasificación intake_request ≥ umbral cierra la ventana ANTES de los 45 s de silencio, y cómo no.",
		"Frame: "+flags.Describe())
	l, cancel, err := setup(n, flags, threshold)
	if err != nil {
		return err
	}
	defer cancel()
	for i, s := range scenes() {
		l.play(i+1, s)
	}
	n.Step("Lo que te llevas")
	n.OK("El adelanto es una PISTA, no una decisión: solo intake_request ≥ %.2f la pone, y quien cierra sigue siendo el barrido.", threshold)
	n.OK("Todo lo demás —confianza baja, otra intención, evidencia inventada, Ollama caído— deja la ventana con su reloj.")
	n.Note("El umbral es de PLATAFORMA (0,7), no del tenant: el umbral_confianza del catálogo publicado no lo lee nadie aquí.")
	n.Note("El adelanto no cambia lo que entra al pipeline: cierra antes, y lo que llegue después abre la ventana siguiente.")
	return nil
}

func setup(n *common.Narrator, flags *common.Flags, threshold float64) (*lab, context.CancelFunc, error) {
	n.Step("El montaje")
	ctx, cancel := context.WithCancel(context.Background())
	clock := common.NewClock(time.Time{})
	l := &lab{n: n, ctx: ctx, clock: clock, start: clock.Now(), threshold: threshold, classified: make(chan verdict, 8)}
	log := common.NewLog(flags.Verbose)

	inner, err := common.FrameFromFlags(flags)
	if err != nil {
		n.Fail(err, "banderas")
		cancel()
		return nil, nil, err
	}
	if s, ok := inner.Inner().(*common.ScriptFrame); ok {
		l.script = s
	}
	l.spy = &notifyingFrame{SpyFrame: inner, done: make(chan struct{}, 8)}
	sel, err := common.NewSelector(l.spy, log)
	if err != nil {
		n.Fail(err, "selector")
		cancel()
		return nil, nil, err
	}

	// El catálogo de intenciones PUBLICADO del tenant: el blob que el dueño edita por API, validado por
	// el mismo intents.ParseAndValidate que usa el pool. Con un umbral_confianza de 0,6 a propósito.
	cfgs := intentcfg.NewMemoryStore()
	blob, err := amberIntentsBlob()
	if err == nil {
		_, err = intents.ParseAndValidate(blob)
	}
	if err == nil {
		err = cfgs.Upsert(ctx, common.LabTenant, "v-lab", blob)
	}
	if err != nil {
		n.Fail(err, "catálogo de intenciones")
		cancel()
		return nil, nil, err
	}

	l.jobs = intake.NewMemoryStore(clock.Now)
	settings := store.NewMemoryRepository()
	settings.SetTenantSettings(store.DefaultTenantSettings(common.LabTenant))
	ents := entitlementshelpertest.NewFake()
	ents.Enable(common.LabTenant, entitlements.FeatureLLMIntake)

	// El ciclo agregador ↔ pool se corta como en el arranque (fase7_flujos.go): el pool entrega a un
	// Sink que se resuelve AL LLAMAR. Aquí el Sink, además, avisa al programa de qué llegó.
	var agg *runtime.IntakeAggregator
	sink := intakeahead.SinkFunc(func(k intake.WindowKey, intent string, confidence float64) {
		l.classified <- verdict{key: k, intent: intent, confidence: confidence}
		agg.OnClassified(k, intent, confidence)
	})
	l.pool = intakeahead.New(log, cfgs, sel, sink, intakeahead.WithWorkers(1), intakeahead.WithWarmup(false))
	agg = runtime.NewIntakeAggregator(log, l.jobs, settings, ents,
		runtime.WithAggregatorClock(clock.Now), runtime.WithAheadRequester(l.pool), runtime.WithIntentConfidence(threshold))
	l.agg = agg

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); l.pool.Run(ctx) }()
	stop := func() { cancel(); wg.Wait() }

	n.Note("Catálogo de intenciones publicado (intentcfg.MemoryStore, versión v-lab): %s y %s, más la reservada %q.",
		common.IntentIntakeRequest, common.IntentOpeningHours, intents.ReservedUnknown)
	n.Note("Pool del adelanto con 1 worker y sin calentamiento; plazo por clasificación %s (calibrado con qwen3:1.7b).", intakeahead.DefaultTimeout)
	n.Note("Agregador con WithAheadRequester(pool) y umbral %.2f. Sin la pista, cierra a los %s de silencio.", threshold, store.DefaultAggregationWindow)
	return l, stop, nil
}

// amberIntentsBlob es el catálogo del caso Ámbar en el formato PUBLICADO (intents.Config): el que el
// pool lee y aplana. Lleva umbral_confianza 0,6, que el adelanto NO usa (lo enseña el paso final).
func amberIntentsBlob() ([]byte, error) {
	cfg := intents.Config{Version: "v-lab", UmbralConfianza: 0.6}
	for _, spec := range common.AmberIntents() {
		it := intents.Intent{Name: spec.Name, Descripcion: spec.Description}
		for _, ex := range spec.Examples {
			it.Ejemplos = append(it.Ejemplos, intents.Ejemplo{Mensaje: ex.Message})
		}
		cfg.Intents = append(cfg.Intents, it)
	}
	return json.Marshal(cfg)
}

func (l *lab) rel() string { return "t+" + l.clock.Now().Sub(l.start).String() }

// play abre una ventana con UN mensaje, espera a que el adelanto conteste (o no) y barre.
func (l *lab) play(i int, s scene) {
	l.n.Step(fmt.Sprintf("Escena %d · %s", i, s.title))
	k := intake.WindowKey{TenantID: common.LabTenant, SessionID: session, ContactID: contact, EventID: fmt.Sprintf("0a1b0000-0000-4000-8000-0000000012%02d", i)}
	if l.script != nil {
		l.script.Respond(common.StageP1, s.script(s.text))
	}
	l.n.Note("%s · Observe %q → la ventana se abre y el agregador PIDE la clasificación (Request: encolar, no esperar).", l.rel(), s.text)
	wall := time.Now()
	l.agg.Observe(l.ctx, runtime.IncomingRef{Key: k, WaMessageID: fmt.Sprintf("wamid.E12%02d", i), MessageTS: l.clock.Now(), Text: s.text})
	l.n.Note("Observe volvió en %s: el mensaje del cliente no espera al modelo.", time.Since(wall).Round(time.Microsecond))

	got, ok := l.await()
	trip, _ := l.spy.Last()
	if trip.Output != "" {
		l.n.Raw("lo que contestó P1 (salida cruda)", trip.Output)
		l.n.Note("Tardó %s de pared, a temperatura %.1f.", trip.Duration.Round(time.Millisecond), trip.Request.Temperature)
	}
	switch {
	case !ok:
		l.n.Fail(nil, "el pool NO entregó nada al agregador: la clasificación no salió o se descartó (evidencia que no está en el mensaje, calidad…).")
	case got.intent == common.IntentIntakeRequest && got.confidence >= l.threshold:
		l.n.OK("OnClassified(%q, %.2f): %s y ≥ %.2f → PISTA anotada y despertador tocado.", got.intent, got.confidence, got.intent, l.threshold)
	default:
		l.n.Note("OnClassified(%q, %.2f): no es %s ≥ %.2f → se ignora, sin pista.", got.intent, got.confidence, common.IntentIntakeRequest, l.threshold)
	}

	closed := l.agg.Sweep(l.ctx)
	l.n.Note("%s · Sweep con 0 s de silencio → cerró %d.", l.rel(), closed)
	want := s.ahead
	if l.script == nil && i == 4 {
		want = true // con Gemma la escena 4 es un pedido corto de verdad, con su evidencia real.
	}
	l.judge(closed > 0, want)
	if closed > 0 {
		l.n.OK("CERRADA YA, %s antes de lo que la habría cerrado el silencio.", store.DefaultAggregationWindow)
		if s.followUp != "" {
			l.clock.Advance(5 * time.Second)
			l.agg.Observe(l.ctx, runtime.IncomingRef{Key: k, WaMessageID: fmt.Sprintf("wamid.E12%02db", i), MessageTS: l.clock.Now(), Text: s.followUp})
			_, _ = l.await()
			l.n.Note("%s · el cliente sigue: %q.", l.rel(), s.followUp)
			l.windows(k)
			l.n.Note("El precio del adelanto: lo que llega después ya no entra en la ventana cerrada, abre la SIGUIENTE (con su")
			l.n.Note("  propia petición de P1). Según draft.go, su job colgará una revisión nueva de la MISMA solicitud del evento.")
			l.agg.Sweep(l.ctx)
		}
		return
	}
	l.clock.Advance(store.DefaultAggregationWindow)
	closed = l.agg.Sweep(l.ctx)
	l.n.Note("Sin pista, la ventana espera a su reloj. %s · %s de silencio → Sweep cerró %d.", l.rel(), store.DefaultAggregationWindow, closed)
}

// judge compara lo que pasó con lo que una persona esperaría.
func (l *lab) judge(advanced, want bool) {
	say := func(b bool) string {
		if b {
			return "adelanta"
		}
		return "no adelanta"
	}
	if advanced == want {
		l.n.OK("Esperado: %s · salió: %s.", say(want), say(advanced))
		return
	}
	l.n.Fail(nil, "Esperado: %s · salió: %s. El modelo no se comporta como lo haría una persona.", say(want), say(advanced))
}

// windows enseña las ventanas de la tupla.
func (l *lab) windows(k intake.WindowKey) {
	for _, j := range l.jobs.Jobs() {
		if j.Key == k {
			l.n.Note("   %s %-12s refs=%v", j.ID, j.Status, j.SourceRefs)
		}
	}
}

// await espera lo que el pool entregue al agregador. Si el frame ya contestó y en medio segundo no
// llega nada, el pool lo descartó (o falló): no hay nada más que esperar.
func (l *lab) await() (verdict, bool) {
	deadline := time.After(intakeahead.DefaultTimeout + 5*time.Second)
	var grace <-chan time.Time
	for {
		select {
		case v := <-l.classified:
			return v, true
		case <-l.spy.done:
			grace = time.After(500 * time.Millisecond)
		case <-grace:
			return verdict{}, false
		case <-deadline:
			return verdict{}, false
		}
	}
}
