//go:build pendiente

package stages_test

// draft_test.go — el contrato de draft.go: el cableado, los errores de entrada, la
// CABECERA (la solicitud), la marca en el job y el log. La revisión está en
// draft_revision_test.go; los eventos, en draft_events_test.go; el empuje al CRM, en
// draft_push_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// ---------------------------------------------------------------------------
// EL CABLEADO
// ---------------------------------------------------------------------------

// TestNewDraft_RefusesToBeBornHalfWired: cinco piezas, cinco formas de negarse a nacer.
func TestNewDraft_RefusesToBeBornHalfWired(t *testing.T) {
	log := logger.New()
	var (
		jobs      stages.StageStore     = &fakeStore{}
		intakeSt  stages.IntakeStore    = &fakeIntakeStore{}
		revisions stages.RevisionWriter = intakes.NewMemoryStore()
		events    stages.EventWriter    = &fakeEventWriter{}
	)
	cases := []struct {
		name  string
		build func() (*stages.Draft, error)
	}{
		{"no log", func() (*stages.Draft, error) { return stages.NewDraft(nil, jobs, intakeSt, revisions, events) }},
		{"no job store", func() (*stages.Draft, error) { return stages.NewDraft(log, nil, intakeSt, revisions, events) }},
		{"no intake store", func() (*stages.Draft, error) { return stages.NewDraft(log, jobs, nil, revisions, events) }},
		{"no revision writer", func() (*stages.Draft, error) { return stages.NewDraft(log, jobs, intakeSt, nil, events) }},
		{"no event writer", func() (*stages.Draft, error) { return stages.NewDraft(log, jobs, intakeSt, revisions, nil) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stage, err := c.build()
			if !errors.Is(err, stages.ErrDraftNotWired) || stage != nil {
				t.Fatalf("NewDraft = (%v, %v); se esperaba (nil, ErrDraftNotWired)", stage, err)
			}
		})
	}
	stage, err := stages.NewDraft(log, jobs, intakeSt, revisions, events)
	if err != nil || stage == nil {
		t.Fatalf("NewDraft con las cinco piezas = (%v, %v)", stage, err)
	}
	const text = "stages: la etapa draft necesita log, store, escritor de solicitudes, de revisiones y de eventos"
	if stages.ErrDraftNotWired.Error() != text {
		t.Fatalf("texto de ErrDraftNotWired = %q", stages.ErrDraftNotWired)
	}
	if errors.Is(stages.ErrDraftNotWired, stages.ErrNotWired) || errors.Is(stages.ErrDraftNotWired, stages.ErrMatchNotWired) {
		t.Fatal("ErrDraftNotWired no es el centinela de otra etapa: no comparte piezas")
	}
}

// TestDraftOption_IsNotTheOptionOfTheOtherStages fija R-03 donde se puede fijar sin
// dejar de compilar: DraftOption no es asignable ni convertible con Option ni con
// MatchOption, así que `NewDraft(…, WithCallTimeout(…))` no compila.
func TestDraftOption_IsNotTheOptionOfTheOtherStages(t *testing.T) {
	draftOption := reflect.TypeOf(stages.DraftOption(nil))
	others := map[string]reflect.Type{
		"Option":      reflect.TypeOf(stages.Option(nil)),
		"MatchOption": reflect.TypeOf(stages.MatchOption(nil)),
	}
	for name, other := range others {
		if other.AssignableTo(draftOption) || other.ConvertibleTo(draftOption) {
			t.Fatalf("una %s se puede pasar donde va una DraftOption: «draft con plazo» compilaría", name)
		}
		if draftOption.AssignableTo(other) || draftOption.ConvertibleTo(other) {
			t.Fatalf("una DraftOption se puede pasar donde va una %s", name)
		}
	}
	var stage *stages.Draft
	if draftOption.NumIn() != 1 || draftOption.In(0) != reflect.TypeOf(stage) {
		t.Fatalf("DraftOption = %v; configura un *Draft", draftOption)
	}
}

// TestWithClock_NilKeepsAClock: pasar nil no deja a la etapa sin reloj. Se ve porque un
// job con un `message_ts` de 1970 mide una espera positiva en vez de reventar.
func TestWithClock_NilKeepsAClock(t *testing.T) {
	b := &draftBench{intakes: &fakeIntakeStore{}, revisions: intakes.NewMemoryStore(), events: &fakeEventWriter{}, jobs: &fakeStore{}}
	stage, err := stages.NewDraft(captureLog(&b.log), b.jobs, b.intakes, b.revisions, b.events, stages.WithClock(nil))
	if err != nil {
		t.Fatalf("NewDraft: %v", err)
	}
	job := ambarJob()
	job.MessageTS = time.Unix(0, 0)
	art, err := stage.Run(context.Background(), job, ambarDraftInput())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if art.ElapsedMS <= 0 {
		t.Fatalf("elapsed_ms = %d; sin WithClock el reloj es el del proceso", art.ElapsedMS)
	}
}

// ---------------------------------------------------------------------------
// LOS ERRORES DE ENTRADA
// ---------------------------------------------------------------------------

// TestDraftRun_WithoutMatchOrEvent_FailsAndTouchesNothing: los dos mensajes describen el
// CASO REAL, y con cualquiera de los dos no se lee ni se escribe nada.
func TestDraftRun_WithoutMatchOrEvent_FailsAndTouchesNothing(t *testing.T) {
	noEvent := ambarP4Job()
	noEvent.Key.EventID = ""
	cases := []struct {
		name string
		job  intake.ClaimedJob
		in   stages.DraftInput
		want error
		text string
	}{
		{"no match artifact", ambarP4Job(), stages.DraftInput{}, stages.ErrNoMatch,
			"stages: el draft necesita el artefacto del match"},
		{"job without event", noEvent, ambarDraftInput(), stages.ErrJobWithoutEvent,
			"stages: el job no trae el evento conversacional del que cuelga el borrador: job_id=" + jobID},
		{"neither: the match is looked at first", noEvent, stages.DraftInput{}, stages.ErrNoMatch,
			"stages: el draft necesita el artefacto del match"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newDraftBench(t, ambarNow())
			art, err := b.stage.Run(context.Background(), c.job, c.in)
			if !errors.Is(err, c.want) || art != nil {
				t.Fatalf("Run = (%v, %v); se esperaba (nil, %v)", art, err, c.want)
			}
			if err.Error() != c.text {
				t.Fatalf("texto del error = %q, se esperaba %q", err, c.text)
			}
			if len(b.intakes.asked) != 0 || b.intakes.upserts != 0 || len(b.spy.got) != 0 ||
				len(b.events.calls) != 0 || len(b.jobs.saved) != 0 {
				t.Fatalf("un error de entrada tocó algo: lecturas=%v upserts=%d revisiones=%d eventos=%v artefactos=%d",
					b.intakes.asked, b.intakes.upserts, len(b.spy.got), b.events.calls, len(b.jobs.saved))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LA CABECERA
// ---------------------------------------------------------------------------

// TestDraftRun_Ambar_TheIntakeIsBornPendingApproval: del caso nace UNA solicitud,
// esperando al dueño, colgada de su evento y con el id derivado de él.
func TestDraftRun_Ambar_TheIntakeIsBornPendingApproval(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	art := b.runAmbar(t, ambarP4Job())

	want := store.Intake{
		ID:        ambarIntakeID,
		TenantID:  tenantID,
		ContactID: contactID,
		SessionID: sessionID,
		Status:    "pending_approval",
		EventID:   eventID,
	}
	if got := b.theIntake(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("cabecera = %+v\nse esperaba %+v (sin total y sin nota)", got, want)
	}
	if art.IntakeID != ambarIntakeID {
		t.Fatalf("intake_id del artefacto = %q", art.IntakeID)
	}
	if want.Status != intakes.StatusPendingApproval {
		t.Fatalf("el literal del test y intakes.StatusPendingApproval divergen: %q", intakes.StatusPendingApproval)
	}
	if len(b.intakes.asked) != 1 || b.intakes.asked[0] != tenantID+"/"+eventID {
		t.Fatalf("lecturas por evento = %v; se pregunta UNA vez, por el tenant y el evento del job", b.intakes.asked)
	}
	if b.intakes.upserts != 1 {
		t.Fatalf("upserts = %d", b.intakes.upserts)
	}
}

// TestDraftRun_BirthStatusIsALiveKey: `pending_approval` tiene que ser una clave que la
// máquina de estados RECONOZCA y de la que se pueda SALIR; las cuatro salidas son las
// acciones que la bandeja le ofrece al dueño. Una solicitud que naciera en un estado
// terminal sería un pedido inmortal, y no lo notaría ningún otro test del borrador.
func TestDraftRun_BirthStatusIsALiveKey(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	b.runAmbar(t, ambarP4Job())

	birth := b.theIntake(t).Status
	if !intakes.IsStatus(birth) {
		t.Fatalf("el borrador nació en un estado que la máquina no conoce: %q", birth)
	}
	exits := intakes.AllowedTransitions(birth)
	if want := []string{"cancelled", "confirmed", "needs_info", "rejected"}; !reflect.DeepEqual(exits, want) {
		t.Fatalf("salidas de %q = %v, se esperaban %v", birth, exits, want)
	}
}

// TestDraftRun_EventWithDurableContent_HangsFromItAndKeepsItsStatus es la dirección
// pipeline→carrito del hallazgo #24 (D-044.46): el carrito llegó primero, con un id
// SORTEADO. El borrador cuelga de LA QUE YA ESTABA, no se pare una segunda y su estado
// no se toca, sea el que sea: la lectura no filtra por estado.
func TestDraftRun_EventWithDurableContent_HangsFromItAndKeepsItsStatus(t *testing.T) {
	for _, status := range []string{intakes.StatusOpen, "confirmed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			b := newDraftBench(t, ambarNow())
			seeded := cartIntakeRow(status)
			b.intakes.rows = []store.Intake{seeded}

			art := b.runAmbar(t, ambarP4Job())

			if art.IntakeID != cartIntakeID {
				t.Fatalf("intake_id = %q; el borrador cuelga de la solicitud que YA existía", art.IntakeID)
			}
			if b.intakes.upserts != 0 {
				t.Fatalf("upserts = %d; con contenido durable la cabecera NO se escribe", b.intakes.upserts)
			}
			if got := b.theIntake(t); !reflect.DeepEqual(got, seeded) {
				t.Fatalf("la solicitud ajena cambió: %+v", got)
			}
			rev, _ := b.lastRevision(t, cartIntakeID)
			if rev.RevisionNo != 1 || rev.Kind != intakes.RevisionKindInterpreted || rev.CreatedBy != intakes.RevisionBySystem {
				t.Fatalf("revisión colgada del intake reusado = %+v", rev)
			}
			b.assertLogHas(t, "draft: el evento YA tenía contenido durable; la revisión se cuelga de él y su estado no se toca")
			b.assertLogHas(t, "status="+status)
		})
	}
}

// TestDraftRun_AnotherTenantsEventIsNotThisOne: la pregunta va con el tenant del job.
func TestDraftRun_AnotherTenantsEventIsNotThisOne(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	b.intakes.rows = []store.Intake{{ID: "ajena", TenantID: "otro-tenant", EventID: eventID, Status: intakes.StatusOpen}}

	art := b.runAmbar(t, ambarP4Job())
	if art.IntakeID != ambarIntakeID || len(b.intakes.rows) != 2 {
		t.Fatalf("intake_id = %q con %d filas; la solicitud de otro tenant no es la de este evento", art.IntakeID, len(b.intakes.rows))
	}
}

// TestDraftRun_HeaderFailures: si la cabecera no se resuelve no hay revisión ni nada
// detrás, y el error dice de qué job se trata.
func TestDraftRun_HeaderFailures(t *testing.T) {
	boom := errors.New("la base no está")
	cases := []struct {
		name     string
		sabotage func(*fakeIntakeStore)
		text     string
		upserts  int
	}{
		{"the lookup fails", func(s *fakeIntakeStore) { s.getErr = boom },
			"draft: leer la solicitud del evento del job " + jobID + ": la base no está", 0},
		{"the upsert fails", func(s *fakeIntakeStore) { s.putErr = boom },
			"draft: crear la solicitud del job " + jobID + ": la base no está", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newDraftBench(t, ambarNow())
			c.sabotage(b.intakes)
			art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarDraftInput())
			if !errors.Is(err, boom) || art != nil {
				t.Fatalf("Run = (%v, %v); se esperaba el error del almacén envuelto", art, err)
			}
			if err.Error() != c.text {
				t.Fatalf("texto = %q, se esperaba %q", err, c.text)
			}
			if b.intakes.upserts != c.upserts || len(b.spy.got) != 0 || len(b.events.calls) != 0 || len(b.jobs.saved) != 0 {
				t.Fatalf("sin cabecera no hay nada detrás: upserts=%d revisiones=%d eventos=%v artefactos=%d",
					b.intakes.upserts, len(b.spy.got), b.events.calls, len(b.jobs.saved))
			}
		})
	}
}

// TestDraftRun_TwoPassesOverTheSameEvent_OneIntakeTwoRevisions: un reintento no pare una
// segunda solicitud. La revisión SÍ se duplica, a propósito: `InsertRevision` no es
// idempotente, y lo que evita la segunda pasada en producción es el artefacto.
func TestDraftRun_TwoPassesOverTheSameEvent_OneIntakeTwoRevisions(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	first := b.runAmbar(t, ambarP4Job())
	second := b.runAmbar(t, ambarP4Job())

	if first.IntakeID != second.IntakeID || len(b.intakes.rows) != 1 {
		t.Fatalf("ids %q y %q con %d filas; un evento tiene A LO SUMO un contenido durable",
			first.IntakeID, second.IntakeID, len(b.intakes.rows))
	}
	if first.RevisionNo != 1 || second.RevisionNo != 2 {
		t.Fatalf("revisiones %d y %d; el store numera, la segunda pasada deja rastro y no pisa", first.RevisionNo, second.RevisionNo)
	}
	if b.theIntake(t).Status != intakes.StatusPendingApproval {
		t.Fatalf("estado tras la segunda pasada = %q", b.theIntake(t).Status)
	}
}

// ---------------------------------------------------------------------------
// LA NOTA DEL PEDIDO
// ---------------------------------------------------------------------------

// TestDraftRun_OrderNoteIsNotPersistedAndIsWarnedWithoutQuotingIt: hoy nadie produce la
// nota; el día que alguien la produzca se perdería SIN ERROR. Y el aviso NO la cita.
func TestDraftRun_OrderNoteIsNotPersistedAndIsWarnedWithoutQuotingIt(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	in := ambarDraftInput()
	in.Match.CustomerNote = "dejarlo en portería, calle Mayor 14"
	b.run(t, ambarP4Job(), in)

	if note := b.theIntake(t).CustomerNote; note != "" {
		t.Fatalf("customer_note = %q; esta etapa no la escribe", note)
	}
	b.assertLogHas(t, "draft: el borrador trae nota de pedido y esta etapa NO la puede persistir (intakes.customer_note solo la escribe el cierre del carrito)")
	b.assertLogHas(t, "runas=35")
	b.assertLogLacks(t, "portería")
	b.assertLogLacks(t, "Mayor 14")

	quiet := newDraftBench(t, ambarNow())
	quiet.runAmbar(t, ambarP4Job())
	quiet.assertLogLacks(t, "nota de pedido")
}

// ---------------------------------------------------------------------------
// LA MARCA EN EL JOB
// ---------------------------------------------------------------------------

// TestDraftRun_Ambar_ArtifactMarksTheStage: UN artefacto de etapa `draft`, con sus claves
// literales, y lo persistido y lo devuelto son lo mismo.
func TestDraftRun_Ambar_ArtifactMarksTheStage(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	art := b.runAmbar(t, ambarP4Job())

	want := stages.DraftArtifact{Version: 1, IntakeID: ambarIntakeID, RevisionNo: 1, Lines: 4, ElapsedMS: 174000}
	if *art != want {
		t.Fatalf("artefacto = %+v, se esperaba %+v", *art, want)
	}
	if len(b.jobs.saved) != 1 || b.jobs.jobs[0] != jobID || b.jobs.saved[0].Stage != intake.StageDraft {
		t.Fatalf("artefactos persistidos = %+v para %v; se esperaba UNO, de etapa draft, del job", b.jobs.saved, b.jobs.jobs)
	}
	const golden = `{"version":1,"intake_id":"` + ambarIntakeID + `","revision_no":1,"lines":4,"elapsed_ms":174000}`
	if got := string(b.jobs.saved[0].Payload); got != golden {
		t.Fatalf("artifacts.draft = %s\nse esperaba %s", got, golden)
	}
	var reread stages.DraftArtifact
	if err := json.Unmarshal(b.jobs.saved[0].Payload, &reread); err != nil || reread != *art {
		t.Fatalf("lo persistido (%+v, %v) no es lo devuelto", reread, err)
	}
}

// TestDraftRun_PersistenceFailures: la marca va la ÚLTIMA. Si no se puede dejar, la etapa
// lo dice —no finge que cerró— y la solicitud y su revisión ya están escritas.
func TestDraftRun_PersistenceFailures(t *testing.T) {
	boom := errors.New("la base no está")
	cases := []struct {
		name     string
		sabotage func(*fakeStore)
		want     error
		text     string
	}{
		{"the write fails", func(s *fakeStore) { s.err = boom }, boom, "draft: persistir el artefacto: la base no está"},
		{"the job left processing", func(s *fakeStore) { s.lost = true }, stages.ErrJobNotProcessing,
			stages.ErrJobNotProcessing.Error()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newDraftBench(t, ambarNow())
			c.sabotage(b.jobs)
			art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarDraftInput())
			if !errors.Is(err, c.want) || art != nil {
				t.Fatalf("Run = (%v, %v); se esperaba (nil, %v)", art, err, c.want)
			}
			if err.Error() != c.text {
				t.Fatalf("texto = %q, se esperaba %q", err, c.text)
			}
			if len(b.intakes.rows) != 1 || len(b.revisions.Revisions(ambarIntakeID)) != 1 {
				t.Fatal("la marca va la última: la solicitud y su revisión ya estaban escritas")
			}
			if got := b.events.names(); !reflect.DeepEqual(got, []string{stages.EventDraftCreated}) {
				t.Fatalf("eventos = %v; la métrica se publica antes que la marca", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EL LOG
// ---------------------------------------------------------------------------

// TestDraftRun_LogsTheDraftWithoutAWordOfTheClient: el `Info` de cierre lleva ids y
// contadores, y en TODO el log de la pasada no hay ni una palabra del cliente.
func TestDraftRun_LogsTheDraftWithoutAWordOfTheClient(t *testing.T) {
	b := newDraftBench(t, ambarNow())
	in := ambarDraftInput()
	in.Analysis = stages.Analysis{} // que hable también el aviso de la vía
	in.Match.Warnings = []stages.Warning{{ItemPos: 1, Reason: stages.WarningRangeWithoutVariant}}
	b.run(t, ambarP4Job(), in)

	b.assertLogHas(t, "draft: borrador creado y esperando al dueño")
	for _, field := range []string{
		"job_id=" + jobID, "stage=draft", "intake_id=" + ambarIntakeID, "revision_no=1",
		"status=pending_approval", "lineas=4", "casadas=2", "sin_casar=1", "preguntas=2",
		"avisos=1", "elapsed_ms=174000",
	} {
		b.assertLogHas(t, field)
	}
	assertNoClientWords(t, "el log de la etapa", b.log.String())
}
