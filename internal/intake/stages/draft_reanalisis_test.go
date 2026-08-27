package stages_test

// draft_reanalisis_test.go — LO QUE T4.6 LE AÑADE A LA ETAPA `draft`: la revisión de
// un RE-ANÁLISIS se firma distinto, lleva su rastro y empuja al puente CRM.
//
// Las tres conductas cuelgan de UNA marca —`intake_jobs.requested_by` (migración
// 0080), que viaja en el `ClaimedJob`— y por eso todos estos tests son el mismo par:
// con la marca y sin ella. La mitad SIN marca es la que de verdad protege: el
// pipeline normal no puede empezar a empujar al CRM como efecto colateral.
//
// 🔴 NINGUNO SE SALTA: la etapa corre contra los dobles en memoria del paquete.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// empujeAlPuente es un doble de stages.EmpujadorCRM que retiene lo que se le pidió.
// Con mutex porque el puerto no promete en qué goroutine se llama.
type empujeAlPuente struct {
	mu       sync.Mutex
	err      error
	llamadas []empujado
}

type empujado struct {
	tenantID   string
	intakeID   string
	revisionNo int
}

func (e *empujeAlPuente) PushRevisionByID(_ context.Context, tenantID, intakeID string, revisionNo int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.llamadas = append(e.llamadas, empujado{tenantID, intakeID, revisionNo})
	return e.err
}

func (e *empujeAlPuente) recibidos() []empujado {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]empujado(nil), e.llamadas...)
}

// jobDeReanalisis es el job de Ambar CON el contexto de la 0080 puesto: lo pidió el
// dueño, por vía local, con material del hilo, sucediendo a la revisión 1.
func jobDeReanalisis() intake.ClaimedJob {
	j := jobAmbarP4()
	j.Reanalisis = intake.Reanalisis{
		RequestedBy: intake.RequestedByOwner,
		Via:         "local",
		Source:      stages.OrigenHiloDelEvento,
		From:        1,
	}
	return j
}

// correrDraft ejecuta la etapa con el job dado sobre el caso de Ambar.
func correrDraft(t *testing.T, b *banco, job intake.ClaimedJob) *stages.ArtefactoDraft {
	t.Helper()
	art, err := b.draft.Run(context.Background(), job,
		stages.EntradaDraft{Match: matchDeAmbarSinDeco(t), SourceText: textoAmbar})
	require.NoError(t, err)
	return art
}

// ---------------------------------------------------------------------------
// QUIÉN FIRMA LA REVISIÓN
// ---------------------------------------------------------------------------

// TestDraft_ElAutorSaleDelJob es el criterio de T4.6 sobre `created_by`, y su pareja
// de no-regresión.
//
// 🔴 SON DOS PAQUETES CON EL MISMO LITERAL `"owner"` —`intake.RequestedByOwner` es la
// marca del job y `intakes.RevisionByOwner` el autor de la revisión— y el test compara
// las CONSTANTES, no la cadena. Un literal acierta por casualidad mientras haya un
// solo productor; una constante dice de dónde viene el valor.
func TestDraft_ElAutorSaleDelJob(t *testing.T) {
	t.Parallel()
	casos := map[string]struct {
		job   intake.ClaimedJob
		autor string
	}{
		"re-análisis del dueño": {job: jobDeReanalisis(), autor: intakes.RevisionByOwner},
		"pipeline normal":       {job: jobAmbarP4(), autor: intakes.RevisionBySystem},
	}
	for nombre, c := range casos {
		t.Run(nombre, func(t *testing.T) {
			t.Parallel()
			b := draftDe(t, ahoraDeAmbar())

			art := correrDraft(t, b, c.job)

			rev, _ := payloadDeLaRevision(t, b, art.IntakeID)
			require.Equal(t, c.autor, rev.CreatedBy)
			require.Equal(t, intakes.RevisionKindInterpreted, rev.Kind,
				"el `kind` NO cambia: un re-análisis sigue siendo una interpretación de la máquina")
		})
	}
}

// ---------------------------------------------------------------------------
// EL RASTRO (`payload.analysis`, D-044.15 / design §7.4)
// ---------------------------------------------------------------------------

// TestDraft_ElAnalisisDelReanalisisSaleDelJob: los tres campos que el endpoint anotó
// en la 0080 acaban en el payload de la revisión.
//
// `model` se queda VACÍO y no es un olvido: el modelo concreto lo elige el adaptador
// POR LLAMADA y no lo publica ningún puerto — la misma razón por la que `provider`
// sale vacío en el pipeline normal (ver la cabecera de internal/intake/pipeline).
func TestDraft_ElAnalisisDelReanalisisSaleDelJob(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar())

	art := correrDraft(t, b, jobDeReanalisis())

	_, p := payloadDeLaRevision(t, b, art.IntakeID)
	require.Equal(t, "local", p.Analysis.Provider, "`provider` lleva la VÍA (§7.4), no el proveedor")
	require.Equal(t, stages.OrigenHiloDelEvento, p.Analysis.Source)
	require.NotNil(t, p.Analysis.ReanalyzedFrom)
	require.Equal(t, 1, *p.Analysis.ReanalyzedFrom, "sucede a la revisión que estaba vigente")
	require.Empty(t, p.Analysis.Model)
}

// TestDraft_SinReanalisisElAnalisisNoCambia es la no-regresión: el pipeline normal
// deja el payload EXACTAMENTE como antes de esta tarea — `reanalyzed_from` a `null`,
// que §7.4 escribe así a propósito («esta es la primera lectura»), y `source` al hilo
// del evento.
func TestDraft_SinReanalisisElAnalisisNoCambia(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar())

	art := correrDraft(t, b, jobAmbarP4())

	_, p := payloadDeLaRevision(t, b, art.IntakeID)
	require.Nil(t, p.Analysis.ReanalyzedFrom)
	require.Equal(t, stages.OrigenHiloDelEvento, p.Analysis.Source)
	require.Empty(t, p.Analysis.Provider)
	require.Contains(t, b.log.String(), "SIN vía de análisis",
		"el pipeline normal sigue avisando de que no puede rellenar la vía")
}

// TestDraft_ElAnalisisDelLlamanteGanaAlDelJob: lo que el worker haya rellenado NO se
// pisa, se COMPLETA. Es defensa contra el día que el selector publique la vía por un
// puerto: entonces el dato bueno será el del llamante y este código no debe taparlo.
func TestDraft_ElAnalisisDelLlamanteGanaAlDelJob(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar())

	art, err := b.draft.Run(context.Background(), jobDeReanalisis(), stages.EntradaDraft{
		Match:      matchDeAmbarSinDeco(t),
		SourceText: textoAmbar,
		Analisis:   stages.Analisis{Provider: "api", Model: "claude-x"},
	})
	require.NoError(t, err)

	_, p := payloadDeLaRevision(t, b, art.IntakeID)
	require.Equal(t, "api", p.Analysis.Provider, "lo que trae el llamante manda")
	require.Equal(t, "claude-x", p.Analysis.Model)
	require.Equal(t, stages.OrigenHiloDelEvento, p.Analysis.Source, "lo que no traía, lo completa el job")
}

// TestDraft_ReanalisisSinRevisionPrevia_ReanalyzedFromNull: `From = 0` significa «no
// había ninguna», y el contrato §7.4 lo publica como `null`. Escribir un 0 diría que
// sucede a una revisión número cero, que no existe: los correlativos empiezan en 1.
func TestDraft_ReanalisisSinRevisionPrevia_ReanalyzedFromNull(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar())
	job := jobDeReanalisis()
	job.Reanalisis.From = 0

	art := correrDraft(t, b, job)

	_, p := payloadDeLaRevision(t, b, art.IntakeID)
	require.Nil(t, p.Analysis.ReanalyzedFrom)
}

// ---------------------------------------------------------------------------
// EL EMPUJE AL PUENTE CRM (cierre de T4.10 mitad 2)
// ---------------------------------------------------------------------------

// TestDraft_ElReanalisisEmpujaAlCRM: la tercera puerta de D-044.19 queda cableada, y
// empuja con SU `revision_no`, que es lo que el puente usa para su UPSERT.
func TestDraft_ElReanalisisEmpujaAlCRM(t *testing.T) {
	t.Parallel()
	puente := &empujeAlPuente{}
	b := draftDe(t, ahoraDeAmbar(), stages.ConEmpujeCRM(puente))

	art := correrDraft(t, b, jobDeReanalisis())

	rev, _ := payloadDeLaRevision(t, b, art.IntakeID)
	require.Equal(t, []empujado{{tenantID: tenant, intakeID: art.IntakeID, revisionNo: rev.RevisionNo}},
		puente.recibidos())
}

// TestDraft_ElPipelineNormalNOEmpuja es la mitad que de verdad protege, y el motivo
// está escrito en empujarAlCRM: si el empuje colgara de «he escrito una revisión» en
// vez de la marca del job, TODO borrador interpretado empezaría a salir al CRM. Eso
// es un cambio de conducta que nadie pidió y que el integrador vería como pedidos
// nuevos que el dueño ni ha mirado.
//
// La mutación que este test caza: quitar el `if !job.Reanalisis.EsDelDueño()`.
func TestDraft_ElPipelineNormalNOEmpuja(t *testing.T) {
	t.Parallel()
	puente := &empujeAlPuente{}
	b := draftDe(t, ahoraDeAmbar(), stages.ConEmpujeCRM(puente))

	correrDraft(t, b, jobAmbarP4())

	require.Empty(t, puente.recibidos(),
		"la revisión 1 del pipeline no es POSTERIOR AL CIERRE: no sale al CRM (D-044.19)")
}

// TestDraft_SinPuenteCableado_LoGritaYSigue: la ausencia del cable no puede ser muda
// —un re-análisis que no llega al CRM deja al integrador con una versión del pedido
// que ya no es verdad— pero tampoco puede tumbar el borrador, que ya está escrito.
func TestDraft_SinPuenteCableado_LoGritaYSigue(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar()) // sin ConEmpujeCRM

	art := correrDraft(t, b, jobDeReanalisis())

	require.NotEmpty(t, art.IntakeID, "el borrador se escribe igual")
	require.Contains(t, b.log.String(), "NO hay puente CRM cableado")
}

// TestDraft_ElPuenteQueFalla_NoTumbaElBorrador: BEST-EFFORT, como la métrica y por lo
// mismo. El puente es at-least-once y su outbox reintenta; lo que aquí se pierde es la
// ENCOLADA, y eso se dice en el log con lo necesario para reencolarla a mano.
func TestDraft_ElPuenteQueFalla_NoTumbaElBorrador(t *testing.T) {
	t.Parallel()
	puente := &empujeAlPuente{err: errors.New("el store no responde")}
	b := draftDe(t, ahoraDeAmbar(), stages.ConEmpujeCRM(puente))

	art := correrDraft(t, b, jobDeReanalisis())

	require.NotEmpty(t, art.IntakeID)
	rev, _ := payloadDeLaRevision(t, b, art.IntakeID)
	require.Equal(t, intakes.RevisionByOwner, rev.CreatedBy, "la revisión quedó escrita")
	require.Contains(t, b.log.String(), "no se pudo encolar")
}

// ---------------------------------------------------------------------------
// LA MÉTRICA
// ---------------------------------------------------------------------------

// TestDraft_LaMetricaMarcaElReanalisis: sin la clave, el KPI del plan («tiempo a
// primer borrador < 5 min») quedaría envenenado por re-análisis pedidos al día
// siguiente — el job hereda el `message_ts` ORIGINAL a propósito, así que su
// `elapsed_ms` mide la espera del cliente desde que escribió y sale en horas.
//
// El envenenamiento sería INVISIBLE: dos filas idénticas midiendo cosas distintas.
func TestDraft_LaMetricaMarcaElReanalisis(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar())

	correrDraft(t, b, jobDeReanalisis())

	evs := b.flujos.FlowEvents()
	require.Len(t, evs, 1)
	require.Equal(t, intake.RequestedByOwner, evs[0].Payload["requested_by"])
}

// TestDraft_LaMetricaDelPipelineNormalNoCambia: la forma que fija design §10 sale
// BYTE A BYTE como antes de esta tarea. La quinta clave es CONDICIONAL, así que
// ningún panel existente cambia.
func TestDraft_LaMetricaDelPipelineNormalNoCambia(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar())

	correrDraft(t, b, jobAmbarP4())

	evs := b.flujos.FlowEvents()
	require.Len(t, evs, 1)
	require.Len(t, evs[0].Payload, 4, "design §10 fija CUATRO contadores y ninguno más")
	for _, clave := range []string{"elapsed_ms", "lines", "matched", "unmatched"} {
		require.Contains(t, evs[0].Payload, clave)
	}
	require.NotContains(t, evs[0].Payload, "requested_by")
}

// ---------------------------------------------------------------------------
// EL CASO DE JHOAN, EN LA MITAD QUE NO NECESITA UN LLM
// ---------------------------------------------------------------------------

// TestDraft_ElReanalisisDejaLaRevisionAnteriorINTACTA es el corazón del caso de
// Jhoan: el hilo dice «1 hamburguesa con queso y cebolla», la revisión 1 que escribió
// la máquina dice «2 hamburguesas con queso + 1 sin cebolla», el dueño pide
// re-análisis y aparece una revisión 2 — SIN que la 1 se toque.
//
// ⚠️ LO QUE ESTE TEST **NO** CUBRE, Y HAY QUE DECIRLO: que la revisión 2 traiga UNA
// unidad en vez de tres. Eso depende de lo que conteste el modelo, y ni este paquete
// ni ningún test de esta suite llama a uno. La mitad verificable aquí —y la que de
// verdad puede romperse por código— es la de la ESTRUCTURA: que se escriba una
// revisión MÁS y no se pise la anterior, que la 1 siga consultable con su contenido
// exacto, y que la 2 se firme `owner` y sepa a quién sucede.
//
// La otra mitad es prueba de campo con `via="local"` (D-044.47 §4).
func TestDraft_ElReanalisisDejaLaRevisionAnteriorINTACTA(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar())

	// La revisión 1: lo que la máquina entendió la primera vez, con su propio autor.
	primera, err := b.solicitudes.InsertRevision(context.Background(), intakes.Revision{
		IntakeID:  intakeDelEventoDeAmbar(),
		Kind:      intakes.RevisionKindInterpreted,
		Payload:   []byte(`{"version":1,"lines":[{"label":"hamburguesa con queso","qty":2}]}`),
		CreatedBy: intakes.RevisionBySystem,
	})
	require.NoError(t, err)
	require.Equal(t, 1, primera.RevisionNo)

	art := correrDraft(t, b, jobDeReanalisis())

	revs := b.solicitudes.Revisions(art.IntakeID)
	require.Len(t, revs, 2, "el re-análisis AÑADE una revisión; no sustituye a la que había")

	// La 1, intacta y consultable: mismo número, mismo autor, mismo payload byte a byte.
	require.Equal(t, 1, revs[0].RevisionNo)
	require.Equal(t, intakes.RevisionBySystem, revs[0].CreatedBy)
	require.JSONEq(t, string(primera.Payload), string(revs[0].Payload),
		"la revisión anterior se tocó: el dueño perdería la interpretación que estaba comparando")

	// La 2, la nueva: firmada por el dueño y sabiendo a quién sucede.
	require.Equal(t, 2, revs[1].RevisionNo)
	require.Equal(t, intakes.RevisionByOwner, revs[1].CreatedBy)
	require.Equal(t, 2, art.RevisionNo, "el artefacto del job publica el número real de la revisión escrita")
}

// intakeDelEventoDeAmbar es el id derivado del evento, el mismo que la etapa calcula
// (ver idDeLaSolicitud): un UUIDv5 del evento en un espacio fijo. Se reproduce aquí y
// no se copia como literal para que un cambio del espacio de nombres rompa el test en
// vez de dejarlo sembrando una solicitud que la etapa no va a mirar.
func intakeDelEventoDeAmbar() string {
	return uuid.NewSHA1(uuid.MustParse("6f8f5b2e-3d61-5a4c-9a1e-0b7c4d2f8a13"), []byte(evento)).String()
}

// ahoraDeAmbar es el reloj fijado que usan estos tests: el instante del mensaje más
// los 174 s de design §10, para que `elapsed_ms` sea un número afirmable y no un
// rango.
func ahoraDeAmbar() time.Time { return messageTSDeAmbar.Add(elapsedDeAmbar) }
