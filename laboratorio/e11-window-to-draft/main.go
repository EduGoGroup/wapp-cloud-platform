//go:build laboratorio

// e11-window-to-draft es el escalón E11 del laboratorio (Plan 058 · Bloque C): VENTANA → PIPELINE →
// BORRADOR, de punta a punta dentro del cloud.
//
// La ráfaga Ámbar de tres mensajes entra como entraría por el motor: cada mensaje se escribe en el
// hilo cifrado del evento y se observa en la ventana (E09). La ventana cierra por silencio y, AL
// CERRAR, el compositor real lee el hilo, compone el literal y lo sella con el cifrador real. El job
// queda `pending`; el worker real del pipeline lo reclama, descifra el sobre y encadena las cinco
// etapas (E10) hasta dejar un borrador `pending_approval`. Se narra el viaje del job estado a estado.
//
//   - REAL: agregador, compositor (runtime.SourceTextComposer), cifrador de la PII con llaves efímeras
//     en memoria, worker (pipeline.Worker con RunOnce), las cinco etapas, el índice del catálogo.
//   - DOBLE: el hilo del evento (eventshelpertest.Store), las dos caras de intake_jobs (ver abajo),
//     los almacenes del borrador, los derechos, el reloj y el viaje al Edge (el frame).
//
// 🔴 La costura que el laboratorio NO puede evitar: en producción la ventana y el worker escriben la
// MISMA tabla por el MISMO objeto (*intake.Postgres). En memoria son dos dobles distintos
// —intake.MemoryStore cumple el puerto de la cola, intakehelpertest.MachineMemory el de la máquina—
// y ninguno cumple los dos. El programa COPIA la fila cerrada de uno al otro y lo dice.
//
//	GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e11-window-to-draft
//	… -frame ollama                 (con Gemma: los reintentos del worker, si P2 falla, también se ven)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events/eventshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

const (
	session = "lab-session-e11"
	contact = "lab-contact-ana"
)

// brokenP2 es una P2 como la que Gemma devuelve con frases cortas: ideas SIN evidence. El validador
// compartido la rechaza (llm.ErrLLMQuality), y eso es lo que -break-p2 quiere enseñar.
const brokenP2 = `{"version":1,"wants":[{"idea":"2 tortas"},{"idea":"tequeños"}]}`

func main() {
	flags := common.RegisterFlags(nil)
	breakP2 := flag.Bool("break-p2", false, "con -frame script, P2 contesta ideas SIN evidence (como Gemma con frases cortas): se ven los reintentos del worker")
	flag.Parse()
	if err := run(flags, *breakP2); err != nil {
		os.Exit(1)
	}
}

// lab es el montaje del escalón.
type lab struct {
	n       *common.Narrator
	ctx     context.Context
	clock   *common.Clock
	start   time.Time
	spy     *common.SpyFrame
	thread  *eventshelpertest.Store
	cipher  *crypto.FieldCipher
	queue   *intake.MemoryStore
	agg     *runtime.IntakeAggregator
	machine *tracingStore
	stages  *common.IntakeStages
	worker  *pipeline.Worker
	key     intake.WindowKey
}

func run(flags *common.Flags, breakP2 bool) error {
	n := common.NewNarrator(nil)
	n.Title("E11 · Ventana → pipeline → borrador",
		"Tres mensajes entran por la ventana y salen como un borrador pending_approval, con el cloud real en proceso.",
		"Frame: "+flags.Describe())
	l, err := setup(n, flags)
	if err != nil {
		return err
	}
	if breakP2 {
		script, ok := l.spy.Inner().(*common.ScriptFrame)
		if !ok {
			err := errUnexpected
			n.Fail(err, "-break-p2 solo vale con -frame script")
			return err
		}
		script.Respond(common.StageP2, brokenP2)
		n.Note("-break-p2: el guion contestará en P2 %s (sin evidence).", brokenP2)
	}
	if err := l.burst(); err != nil {
		return err
	}
	if err := l.closeWindow(); err != nil {
		return err
	}
	if err := l.handOver(); err != nil {
		return err
	}
	l.work()
	l.showDraft()
	return nil
}

func setup(n *common.Narrator, flags *common.Flags) (*lab, error) {
	n.Step("El montaje")
	ctx := context.Background()
	clock := common.NewClock(time.Time{})
	l := &lab{n: n, ctx: ctx, clock: clock, start: clock.Now()}
	log := common.NewLog(flags.Verbose)

	var err error
	if l.cipher, _, err = common.NewCipher(); err != nil {
		n.Fail(err, "cifrador")
		return nil, err
	}
	// El hilo del evento: el doble en memoria de conversation_event_messages. Sella cada mensaje y
	// lo devuelve en claro al leerlo, como el store real con su cipher.
	l.thread = eventshelpertest.NewStore()
	l.thread.SetClock(clock.Now)
	ev, err := l.thread.CreateEvent(ctx, events.NewEvent{TenantID: common.LabTenant, SessionID: session, ContactID: contact, Kind: "lab_intake"})
	if err != nil {
		n.Fail(err, "evento")
		return nil, err
	}
	l.key = intake.WindowKey{TenantID: common.LabTenant, SessionID: session, ContactID: contact, EventID: ev.ID}

	// La ventana, como en E09, pero AHORA con el compositor real enchufado (WithSourceComposer).
	l.queue = intake.NewMemoryStore(clock.Now)
	settings := store.NewMemoryRepository()
	settings.SetTenantSettings(store.DefaultTenantSettings(common.LabTenant))
	ents := entitlementshelpertest.NewFake()
	ents.Enable(common.LabTenant, entitlements.FeatureLLMIntake)
	composer := runtime.NewSourceTextComposer(log, l.thread, l.cipher)
	l.agg = runtime.NewIntakeAggregator(log, l.queue, settings, ents,
		runtime.WithAggregatorClock(clock.Now), runtime.WithSourceComposer(composer))

	// El pipeline: frame → selector real → cinco etapas reales → worker real.
	if l.spy, err = common.FrameFromFlags(flags); err != nil {
		n.Fail(err, "banderas")
		return nil, err
	}
	sel, err := common.NewSelector(l.spy, log)
	if err != nil {
		n.Fail(err, "selector")
		return nil, err
	}
	l.machine = &tracingStore{MachineMemory: intakehelpertest.NewMachineMemory(clock.Now), n: n, clock: clock, start: l.start}
	if l.stages, err = common.NewIntakeStages(log, sel, l.machine, clock.Now); err != nil {
		n.Fail(err, "etapas")
		return nil, err
	}
	idx, err := common.AmberIndex()
	if err != nil {
		n.Fail(err, "catálogo")
		return nil, err
	}
	l.worker, err = pipeline.NewWorker(log, l.machine,
		l.stages.P2, l.stages.P3, l.stages.P4, l.stages.Match, l.stages.Draft,
		common.StaticCatalog{Index: idx}, l.cipher, pipeline.Config{},
		pipeline.WithClock(clock.Now), pipeline.WithShippingZones(l.stages.Intakes))
	if err != nil {
		n.Fail(err, "worker")
		return nil, err
	}
	n.Note("Evento %s abierto en el hilo (eventshelpertest.Store, que sella cada mensaje).", ev.ID)
	n.Note("Cifrador de la PII REAL (envelope por valor) con KEK e índice ciego aleatorios en memoria; no se imprimen.")
	n.Note("  🔴 Homónimo: su «DEK» es la del envelope de PII de negocio, NO la DEK del ADR-0007.")
	n.Note("Agregador con plazos de fábrica (%s / %s) y el compositor real; worker con Config{} (3 intentos por calidad,",
		store.DefaultAggregationWindow, store.DefaultAggregationMax)
	n.Note("  backoff %s → %s) y SIN aforo por Edge (WithCapacity): el frame del laboratorio no sabe de plazas.",
		pipeline.DefaultBackoffBase, pipeline.DefaultBackoffCap)
	return l, nil
}

func (l *lab) rel() string { return "t+" + l.clock.Now().Sub(l.start).String() }

// burst mete los tres mensajes como el motor: primero el literal en el hilo, luego la referencia en
// la ventana. Es la invariante del puente (AG-7): todo mensaje que entra en source_refs tiene su
// literal en el hilo.
func (l *lab) burst() error {
	l.n.Step("La ráfaga: hilo + ventana, mensaje a mensaje")
	for i, text := range common.AmberBurst() {
		if i > 0 {
			l.clock.Advance(15 * time.Second)
		}
		seq, err := l.thread.AppendMessage(l.ctx, l.key.EventID, events.RoleClient, text)
		if err != nil {
			l.n.Fail(err, "hilo")
			return err
		}
		waID := "wamid.AMBER0" + string(rune('1'+i))
		l.agg.Observe(l.ctx, runtime.IncomingRef{Key: l.key, WaMessageID: waID, MessageTS: l.clock.Now(), Text: text})
		l.n.Note("%s · hilo: entrada %d sellada · ventana: Observe(%s).", l.rel(), seq, waID)
	}
	for _, j := range l.queue.Jobs() {
		l.n.OK("Ventana %s en %q con %d referencias. El texto NO está en la ventana: está, cifrado, en el hilo.", j.ID, j.Status, len(j.SourceRefs))
	}
	return nil
}

// closeWindow deja pasar el silencio y barre: al cerrar, el compositor lee el hilo y sella el literal.
func (l *lab) closeWindow() error {
	l.n.Step("El cierre: el compositor sella el literal")
	l.clock.Advance(store.DefaultAggregationWindow)
	closed := l.agg.Sweep(l.ctx)
	l.n.Note("%s · %s de silencio · Sweep cerró %d ventana(s).", l.rel(), store.DefaultAggregationWindow, closed)
	jobs := l.queue.Jobs()
	if closed != 1 || len(jobs) != 1 {
		l.n.Fail(nil, "se esperaba UNA ventana cerrada")
		return errUnexpected
	}
	env := jobs[0].SourceText
	l.n.OK("aggregating → pending, y en la MISMA sentencia el sobre del literal: %d bytes cifrados, DEK envuelta de %d bytes, KEK %q.",
		len(env.Enc), len(env.DEK), env.KEKID)
	l.n.Note("Lo hizo runtime.SourceTextComposer ANTES de cerrar: ListThread → ComposeSourceText → FieldCipher.Encrypt.")
	l.n.Note("La base solo ve bytes cifrados; el literal en claro existe en memoria lo que tarda en sellarse.")
	return nil
}

// handOver copia el job cerrado de la cola a la máquina: la costura que en producción no existe.
func (l *lab) handOver() error {
	l.n.Step("La costura del laboratorio: de la cola a la máquina")
	j := l.queue.Jobs()[0]
	id := l.machine.Seed(intakehelpertest.Row{
		ID: j.ID, Key: j.Key, Status: intake.StatusPending, MessageTS: j.MessageTS,
		SourceRefs: j.SourceRefs, SourceText: j.SourceText, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	})
	l.n.Note("En producción no hay copia: la ventana cierra la fila y el worker la reclama de la MISMA tabla intake_jobs.")
	l.n.Note("Aquí intake.MemoryStore (cola) y MachineMemory (máquina) son dos dobles y la fila %s pasa de uno a otro tal cual:", id)
	l.n.Note("  clave, message_ts del PRIMER mensaje (%s), referencias y sobre cifrado. Nada más viaja.", j.MessageTS.Format(time.RFC3339))
	return nil
}

// work hace lo que Run hace cada 5 s: RunOnce. Si una etapa falla por calidad, el worker reencola
// con backoff; el programa adelanta el reloj hasta la marca y vuelve a llamar, hasta que el job
// termina (done o failed).
func (l *lab) work() {
	l.n.Step("El worker: RunOnce, y el viaje del job por sus estados")
	for round := 1; round <= pipeline.DefaultMaxQualityAttempts+1; round++ {
		wall := time.Now()
		found, err := l.worker.RunOnce(l.ctx)
		if found {
			l.n.Note("(RunOnce tardó %s de pared; el reloj manual del cloud no avanza mientras el modelo piensa)", time.Since(wall).Round(time.Millisecond))
		}
		if err != nil {
			l.n.Fail(err, "RunOnce")
			return
		}
		if !found {
			l.n.Note("%s · RunOnce: nada que reclamar.", l.rel())
			return
		}
		row, _ := l.machine.View(l.machine.lastID)
		if intake.IsTerminal(row.Status) {
			return
		}
		if row.NextAttemptAt.After(l.clock.Now()) {
			wait := row.NextAttemptAt.Sub(l.clock.Now())
			l.n.Note("El job vuelve a la cola castigado %s (backoff). Se adelanta el reloj hasta la marca y se reintenta.", wait)
			l.clock.Advance(wait)
		}
	}
}

// showDraft enseña lo que quedó: la solicitud, la revisión y la métrica.
func (l *lab) showDraft() {
	l.n.Step("Lo que quedó")
	row, _ := l.machine.View(l.machine.lastID)
	l.n.Note("Job %s · estado %q · etapa %q · intentos %d · intake_id %q.", row.ID, row.Status, row.Stage, row.Attempts, row.IntakeID)
	if row.Status == intake.StatusFailed {
		l.n.Fail(nil, "el job murió: %s", row.Error)
		l.n.Note("Sin borrador. El dueño no ve nada en la bandeja: el pedido se perdió para la captación automática.")
		return
	}
	if !row.SourceText.Empty() {
		l.n.Fail(nil, "el sobre debería haberse vaciado al terminar (INV-13)")
	} else {
		l.n.OK("El sobre del literal se VACIÓ al terminar (INV-13): el job ya no guarda el texto del cliente.")
	}
	head, found, _ := l.stages.Flow.GetIntakeByEvent(l.ctx, l.key.TenantID, l.key.EventID)
	if !found {
		l.n.Fail(nil, "no hay solicitud para el evento")
		return
	}
	l.n.OK("Solicitud %s en estado %q: es el borrador que el dueño ve en la bandeja (E13).", head.ID, head.Status)
	for _, rev := range l.stages.Intakes.Revisions(head.ID) {
		l.n.Note("Revisión %d · %s · %d bytes de payload.", rev.RevisionNo, rev.Kind, len(rev.Payload))
	}
	var q llm.Quantities
	if raw, ok := row.Artifacts[intake.StageP4]; ok && json.Unmarshal(raw, &q) == nil {
		l.n.Note("Fecha de entrega del borrador: %q (%s), calculada en Go contra el message_ts del primer mensaje.", q.DeliveryDate, q.DeliveryDateBasis)
	}
	var m stages.MatchArtifact
	if raw, ok := row.Artifacts[intake.StageMatch]; ok && json.Unmarshal(raw, &m) == nil {
		for _, ln := range m.Lines {
			price := "sin precio"
			if ln.UnitPrice != nil {
				price = fmt.Sprintf("%.0f", *ln.UnitPrice)
			}
			l.n.Note("  línea %-9s %-11s %-22q × %d · %s · %d variante(s) a elegir", ln.Kind, ln.SKU, ln.Label, ln.Qty, price, len(ln.VariantOptions))
		}
	}
	for _, ev := range l.stages.Flow.FlowEvents() {
		l.n.Note("Efecto publicado: %s %q · %v", ev.Kind, ev.Name, ev.Payload)
	}
}
