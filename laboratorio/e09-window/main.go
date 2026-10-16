//go:build laboratorio

// e09-window es el escalón E09 del laboratorio (Plan 058 · Bloque C): LA VENTANA DE CAPTACIÓN, SOLA.
//
// Un cliente no pide un presupuesto en un mensaje: lo pide en tres o cinco seguidos. El agregador
// (runtime.IntakeAggregator) junta esos mensajes en UNA ventana —una fila de intake_jobs en estado
// `aggregating`— y la cierra (`aggregating` → `pending`) cuando le toca, para que el pipeline caro
// corra UNA vez y no cinco. Este programa enseña CÓMO acumula y CUÁNDO cierra, sin nada más:
//
//   - REAL: el agregador entero (Observe, Sweep y sus plazos) y el almacén de ventanas en memoria
//     (intake.MemoryStore, el doble que cumple el mismo contrato que el de Postgres).
//
//   - DOBLE: el reloj (manual: el tiempo solo avanza cuando el programa lo dice), los derechos del
//     tenant (entitlementshelpertest.Fake) y su configuración (store.MemoryRepository).
//
//   - FUERA: la inferencia, el cifrado y el texto. El agregador no los necesita para acumular; el
//     literal cifrado lo pone el compositor al cerrar, y eso es E11.
//
//     GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e09-window
//     … -window 10s -max 30s     (otros plazos del tenant)
//     … -window 0                (el 0 NO es «por defecto»: es cierre inmediato)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

// Los identificadores del escalón. ContactID es OPACO (ADR-0017): nunca un teléfono.
const (
	session      = "lab-session-1"
	contact      = "lab-contact-ana"
	eventA       = "0a1b0000-0000-4000-8000-0000000000e1"
	eventB       = "0a1b0000-0000-4000-8000-0000000000e2"
	tenantNoPlan = "0a1b0000-0000-4000-8000-0000000000ff"
)

func main() {
	window := flag.Duration("window", store.DefaultAggregationWindow, "aggregation_window_seconds del tenant: el SILENCIO que cierra la ventana")
	ceiling := flag.Duration("max", store.DefaultAggregationMax, "aggregation_max_seconds del tenant: el TECHO de vida de la ventana")
	verbose := flag.Bool("v", false, "saca el log del cloud por stderr (desde Debug)")
	flag.Parse()
	if err := run(*window, *ceiling, *verbose); err != nil {
		os.Exit(1)
	}
}

// lab es el montaje del escalón: el agregador real y lo que lo rodea.
type lab struct {
	n     *common.Narrator
	clock *common.Clock
	start time.Time
	jobs  *intake.MemoryStore
	agg   *runtime.IntakeAggregator
	ctx   context.Context
	// window y ceiling son los plazos que el tenant tiene configurados.
	window, ceiling time.Duration
	seq             int
}

func run(window, ceiling time.Duration, verbose bool) error {
	n := common.NewNarrator(nil)
	n.Title("E09 · La ventana, sola",
		"Cómo acumula el agregador los mensajes de un cliente ANTES de mandarlos al pipeline, y cuándo los suelta.",
		"Sin inferencia, sin cifrado y sin texto: solo referencias, un reloj y dos plazos.")

	l := setup(n, window, ceiling, verbose)
	l.sceneAccumulate()
	l.sceneSilence()
	l.sceneSecondWindow()
	l.sceneCeiling()
	l.sceneWithoutFeature()

	n.Step("Lo que te llevas")
	n.OK("Observe solo ESCRIBE (una sentencia por mensaje, cero lecturas); quien decide cerrar es el barrido.")
	n.OK("Cierra lo que llegue antes: %s de silencio desde el último mensaje, o %s de vida desde el primero.", l.window, l.ceiling)
	n.OK("Cerrar no borra la memoria del último mensaje: la re-entrega no reabre; un mensaje nuevo abre la ventana siguiente.")
	n.OK("Sin la feature llm_intake no hay ventana, ni job, ni pipeline: el gate es uno y está en la puerta.")
	n.Note("Siguiente: E10 mete el texto de una ventana en el pipeline P2→P4; E11 los une; E12 adelanta el cierre con P1.")
	return nil
}

// setup arma el agregador REAL con sus tres dependencias dobladas y el reloj manual.
func setup(n *common.Narrator, window, ceiling time.Duration, verbose bool) *lab {
	n.Step("El montaje")
	clock := common.NewClock(time.Time{})
	l := &lab{n: n, clock: clock, start: clock.Now(), ctx: context.Background(), window: window, ceiling: ceiling}

	// El almacén de ventanas: el doble en memoria de intake_jobs, con el reloj del laboratorio. Sus
	// marcas created_at/updated_at son las que el barrido mira: el reloj del STORE, no el del cliente.
	l.jobs = intake.NewMemoryStore(clock.Now)

	// La configuración del tenant: se parte de DefaultTenantSettings y solo se tocan los dos plazos.
	// Sembrar un TenantSettings a mano dejaría AggregationWindow en 0, que NO es «por defecto»: es
	// cierre inmediato (trampa documentada en conversacion/store).
	settings := store.NewMemoryRepository()
	cfg := store.DefaultTenantSettings(common.LabTenant)
	cfg.AggregationWindow, cfg.AggregationMax = window, ceiling
	settings.SetTenantSettings(cfg)

	// Los derechos: el tenant del laboratorio tiene llm_intake; tenantNoPlan, no.
	ents := entitlementshelpertest.NewFake()
	ents.Enable(common.LabTenant, entitlements.FeatureLLMIntake)

	l.agg = runtime.NewIntakeAggregator(common.NewLog(verbose), l.jobs, settings, ents,
		runtime.WithAggregatorClock(clock.Now))

	n.Note("Reloj manual parado en %s (lo llamaremos t+0s). Solo avanza cuando el programa lo dice.", l.start.Format(time.RFC3339))
	n.Note("intake.MemoryStore hace de la tabla intake_jobs; store.MemoryRepository, de tenant_settings; el Fake, del plan.")
	n.Note("Plazos del tenant: aggregation_window_seconds = %s (silencio) · aggregation_max_seconds = %s (techo). De fábrica: %s y %s.",
		window, ceiling, store.DefaultAggregationWindow, store.DefaultAggregationMax)
	n.Note("Sin compositor (WithSourceComposer) y sin adelanto (WithAheadRequester): la ventana cierra SOLO por su reloj y")
	n.Note("  el sobre del literal queda a NULL (AG-8). Es legítimo aquí; en producción sería un fallo mudo. Lo pone E11.")
	if window <= 0 {
		n.Fail(nil, "OJO: con -window %s el silencio vence SIEMPRE: cada barrido cerrará todo lo que encuentre.", window)
	}
	return l
}

// key es la tupla de la ventana: tenant, sesión, contacto y evento VIVO. Sin evento no hay ventana.
func key(tenant, event string) intake.WindowKey {
	return intake.WindowKey{TenantID: tenant, SessionID: session, ContactID: contact, EventID: event}
}

// rel escribe un instante como desplazamiento desde t+0.
func (l *lab) rel(t time.Time) string { return "t+" + t.Sub(l.start).String() }

// say mete un mensaje en el agregador, como lo haría el motor al recibirlo, y cuenta qué escribió.
func (l *lab) say(k intake.WindowKey, waID, text string) {
	before := l.jobs.Counters()
	l.agg.Observe(l.ctx, runtime.IncomingRef{Key: k, WaMessageID: waID, MessageTS: l.clock.Now(), Text: text})
	after := l.jobs.Counters()
	writes, reads := after.OpenOrAppend-before.OpenOrAppend, after.Reads-before.Reads
	verdict := "escribió 1 referencia"
	if writes == 0 {
		verdict = "NO escribió nada"
	}
	l.n.Note("%s · Observe(%s) %q → %s (%d sentencia, %d lecturas).", l.rel(l.clock.Now()), waID, short(text), verdict, writes, reads)
}

func (l *lab) newWaID() string {
	l.seq++
	return fmt.Sprintf("wamid.LAB%02d", l.seq)
}

// table imprime las ventanas de la tupla: estado, referencias y las dos marcas que el barrido mira.
func (l *lab) table(k intake.WindowKey) {
	now := l.clock.Now()
	for _, j := range l.jobs.Jobs() {
		if j.Key != k {
			continue
		}
		line := fmt.Sprintf("%-6s %-12s refs=%v · abierta %s · último cambio %s", j.ID, j.Status, j.SourceRefs, l.rel(j.CreatedAt), l.rel(j.UpdatedAt))
		if j.Status == intake.StatusAggregating {
			line += fmt.Sprintf(" · silencio %s · edad %s", now.Sub(j.UpdatedAt), now.Sub(j.CreatedAt))
		}
		if j.Status == intake.StatusPending {
			line += fmt.Sprintf(" · sobre del literal: %s", envelopeState(j.SourceText))
		}
		l.n.Note("   %s", line)
	}
}

// sweep hace a mano lo que Run hace cada 5 s: un barrido. Antes, cuenta qué va a decidir y por qué.
func (l *lab) sweep(k intake.WindowKey) int {
	now := l.clock.Now()
	for _, j := range l.jobs.Jobs() {
		if j.Key != k || j.Status != intake.StatusAggregating {
			continue
		}
		silence, age := now.Sub(j.UpdatedAt), now.Sub(j.CreatedAt)
		l.n.Note("%s · Sweep mira %s: silencio %s %s %s · edad %s %s %s → %s", l.rel(now), j.ID,
			silence, cmp(silence, l.window), l.window, age, cmp(age, l.ceiling), l.ceiling, reason(silence >= l.window, age >= l.ceiling))
	}
	closed := l.agg.Sweep(l.ctx)
	if closed > 0 {
		l.n.OK("Sweep cerró %d ventana(s): aggregating → pending.", closed)
	} else {
		l.n.Note("Sweep no cerró nada.")
	}
	return closed
}

func cmp(d, limit time.Duration) string {
	if d >= limit {
		return "≥"
	}
	return "<"
}

func reason(bySilence, byCeiling bool) string {
	switch {
	case bySilence && byCeiling:
		return "CIERRA (silencio y techo)"
	case bySilence:
		return "CIERRA por silencio"
	case byCeiling:
		return "CIERRA por techo"
	}
	return "sigue abierta"
}

func envelopeState(s intake.SourceText) string {
	if s.Empty() {
		return "NULL (sin compositor)"
	}
	return fmt.Sprintf("%d bytes cifrados", len(s.Enc))
}

func short(s string) string {
	r := []rune(s)
	if len(r) <= 38 {
		return s
	}
	return string(r[:37]) + "…"
}

// sceneAccumulate: tres mensajes en 35 s acaban en UNA ventana con tres referencias.
func (l *lab) sceneAccumulate() {
	l.n.Step("Escena 1 · Tres mensajes, una ventana")
	l.n.Note("La ráfaga Ámbar: el cliente escribe su pedido en tres mensajes, separados 15 y 20 segundos.")
	k := key(common.LabTenant, eventA)
	burst := common.AmberBurst()
	l.say(k, l.newWaID(), burst[0])
	l.clock.Advance(15 * time.Second)
	l.say(k, l.newWaID(), burst[1])
	l.clock.Advance(20 * time.Second)
	l.say(k, l.newWaID(), burst[2])
	l.table(k)
	c := l.jobs.Counters()
	l.n.OK("UNA ventana con TRES referencias. El primer Observe la abrió (INSERT); los otros dos la ampliaron (UPSERT).")
	l.n.Note("Contadores del almacén: %d escrituras, %d lecturas, %d cierres. Observe corre en línea con el mensaje del", c.OpenOrAppend, c.Reads, c.Close+c.CloseWithSourceText)
	l.n.Note("  cliente y su presupuesto es UNA sentencia y CERO lecturas (D-044.26): no puede preguntar «¿ya toca cerrar?».")
	l.n.Note("Lo que entra son REFERENCIAS OPACAS (wa_message_id), nunca el texto: el literal no toca ninguna sentencia.")
	l.n.Note("«Último cambio» se movió a t+35s con cada mensaje: es el ancla del SILENCIO. «Abierta» se quedó en t+0s: la del TECHO.")
}

// sceneSilence: el barrido no cierra hasta que el silencio llega al plazo, ni un segundo antes.
func (l *lab) sceneSilence() {
	l.n.Step("Escena 2 · El barrido y el silencio")
	l.n.Note("En producción, Run llama a Sweep cada 5 s (es un ticker de Go, sin broker). Aquí lo llamamos a mano.")
	k := key(common.LabTenant, eventA)
	l.sweep(k)
	if l.window > time.Second {
		l.clock.Advance(l.window - time.Second)
		l.n.Note("Pasan %s sin mensajes…", l.window-time.Second)
		l.sweep(k)
		l.clock.Advance(time.Second)
		l.n.Note("…y uno más.")
	}
	l.sweep(k)
	l.table(k)
	l.n.Note("Ya es un job `pending`: el worker del pipeline lo reclamará (E11). El barrido cierra con el grano del tick:")
	l.n.Note("  una ventana de %s se cierra en producción entre los %s y los %s.", l.window, l.window, l.window+5*time.Second)
}

// sceneSecondWindow: la re-entrega del último mensaje no reabre; un mensaje nuevo abre otra ventana.
func (l *lab) sceneSecondWindow() {
	l.n.Step("Escena 3 · La re-entrega y la segunda ventana")
	k := key(common.LabTenant, eventA)
	last := fmt.Sprintf("wamid.LAB%02d", l.seq)
	l.clock.Advance(10 * time.Second)
	l.n.Note("El Edge vuelve a entregar el último mensaje (%s), ya procesado. Pasa a veces: un reenvío del outbox.", last)
	l.say(k, last, common.AmberBurst()[2])
	l.n.OK("No escribió: el agregador recuerda el ÚLTIMO id por tupla y NO lo olvida al cerrar (AG-5). Si lo olvidara,")
	l.n.Note("  esa re-entrega abriría una ventana nueva con un mensaje ya procesado (trampa T-10).")
	l.n.Note("Ahora el cliente escribe algo NUEVO en el mismo evento:")
	l.say(k, l.newWaID(), "Ah, y si puede ser con una vela de cumpleaños")
	l.table(k)
	l.n.OK("Segunda ventana sobre el MISMO evento: el índice único de intake_jobs es parcial (solo una VIVA por tupla).")
	l.clock.Advance(l.window)
	l.sweep(k)
}

// sceneCeiling: un cliente que no para nunca de escribir; el techo cierra la ventana igual.
func (l *lab) sceneCeiling() {
	l.n.Step("Escena 4 · El techo: el cliente que no deja de escribir")
	k := key(common.LabTenant, eventB)
	every := (l.window * 2 / 3).Round(time.Second)
	if every <= 0 {
		every = time.Second
	}
	l.n.Note("Otro evento. El cliente manda un mensaje cada %s: el silencio nunca llega a %s.", every, l.window)
	l.n.Note("Sin techo, esta ventana no cerraría nunca y el presupuesto no saldría jamás.")
	for i := 1; i <= 40; i++ {
		l.say(k, l.newWaID(), fmt.Sprintf("y otra cosa más (%d)", i))
		l.clock.Advance(every)
		if l.sweep(k) > 0 {
			break
		}
	}
	l.n.Note("El cliente sigue escribiendo justo después del cierre:")
	l.say(k, l.newWaID(), "¡ah! me olvidaba…")
	l.table(k)
	l.n.OK("Ese mensaje ya no entra en la ventana cerrada: abre la SIGUIENTE. El pipeline no espera al final de la charla.")
}

// sceneWithoutFeature: sin llm_intake, Observe ni siquiera escribe.
func (l *lab) sceneWithoutFeature() {
	l.n.Step("Escena 5 · Un tenant sin llm_intake")
	k := key(tenantNoPlan, eventA)
	before := l.jobs.Counters()
	for _, text := range common.AmberBurst() {
		l.say(k, l.newWaID(), text)
	}
	after := l.jobs.Counters()
	l.table(k)
	l.n.OK("Cero escrituras (%d → %d), cero ventanas, cero jobs. El gate es la feature y se pregunta UNA vez por mensaje,", before.OpenOrAppend, after.OpenOrAppend)
	l.n.Note("  después de las guardas baratas. Es el ÚNICO gate de este camino: aquí no se pregunta si la vía es local o API.")
	l.n.Note("Si el resolver de derechos FALLA, es fail-closed: el mensaje no entra en ninguna ventana (un Warn con -v).")
}
