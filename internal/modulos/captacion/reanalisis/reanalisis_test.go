package reanalisis_test

// reanalisis_test.go — el contrato de reanalisis.go: el constructor, lo que Reanalyze
// devuelve y escribe en el camino que sale bien, el ORDEN completo de los escalones, y
// lo que los puertos no pueden hacer. Los rechazos de los escalones 1–8 están en
// reanalisis_checks_test.go; el material y el texto pegado, en reanalisis_source_test.go.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// eventKey es la clave de ventana que tiene que salir de la SOLICITUD de prueba.
var eventKey = intake.WindowKey{TenantID: tenantID, SessionID: sessionID, ContactID: contactID, EventID: eventID}

// TestNewService_MissingPiece_ReturnsErrNotWired: las seis dependencias y el log son
// obligatorios (R-10). Un servicio a medias abriría jobs que nadie puede completar.
func TestNewService_MissingPiece_ReturnsErrNotWired(t *testing.T) {
	t.Parallel()
	b := newBench(t)
	log := captureLog(b.out)

	var (
		noLog      logger.Logger
		noIntakes  reanalisis.Intakes
		noThread   reanalisis.Thread
		noJobs     reanalisis.Jobs
		noComposer reanalisis.Composer
		noFeatures reanalisis.Features
		noConfig   reanalisis.LLMConfig
	)
	cases := map[string]func() (*reanalisis.Service, error){
		"without log": func() (*reanalisis.Service, error) {
			return reanalisis.NewService(noLog, b.intakes, b.thread, b.jobs, b.composer, b.features, b.config, threadLimit)
		},
		"without intakes": func() (*reanalisis.Service, error) {
			return reanalisis.NewService(log, noIntakes, b.thread, b.jobs, b.composer, b.features, b.config, threadLimit)
		},
		"without thread": func() (*reanalisis.Service, error) {
			return reanalisis.NewService(log, b.intakes, noThread, b.jobs, b.composer, b.features, b.config, threadLimit)
		},
		"without jobs": func() (*reanalisis.Service, error) {
			return reanalisis.NewService(log, b.intakes, b.thread, noJobs, b.composer, b.features, b.config, threadLimit)
		},
		"without composer": func() (*reanalisis.Service, error) {
			return reanalisis.NewService(log, b.intakes, b.thread, b.jobs, noComposer, b.features, b.config, threadLimit)
		},
		"without features": func() (*reanalisis.Service, error) {
			return reanalisis.NewService(log, b.intakes, b.thread, b.jobs, b.composer, noFeatures, b.config, threadLimit)
		},
		"without LLM config": func() (*reanalisis.Service, error) {
			return reanalisis.NewService(log, b.intakes, b.thread, b.jobs, b.composer, b.features, noConfig, threadLimit)
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc, err := build()
			if !errors.Is(err, reanalisis.ErrNotWired) {
				t.Fatalf("error = %v; se esperaba ErrNotWired", err)
			}
			if err.Error() != reanalisis.ErrNotWired.Error() {
				t.Errorf("texto = %q; con una pieza a nil es el de ErrNotWired sin adornos", err.Error())
			}
			if svc != nil {
				t.Error("se devolvió un servicio a medias")
			}
		})
	}
}

// TestNewService_NonPositiveThreadLimit_IsRejected: el límite del hilo entra por el
// constructor y no tiene valor por defecto. Con un límite <= 0 el almacén devuelve un
// hilo vacío y toda petición saldría `never_stored` en silencio.
func TestNewService_NonPositiveThreadLimit_IsRejected(t *testing.T) {
	t.Parallel()
	b := newBench(t)
	cases := map[string]struct {
		limit int
		want  string
	}{
		"zero": {0, "reanalisis: el límite del hilo debe ser positivo (0): " + reanalisis.ErrNotWired.Error()},
		"negative": {-200, "reanalisis: el límite del hilo debe ser positivo (-200): " +
			reanalisis.ErrNotWired.Error()},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc, err := reanalisis.NewService(captureLog(b.out),
				b.intakes, b.thread, b.jobs, b.composer, b.features, b.config, c.limit)
			if !errors.Is(err, reanalisis.ErrNotWired) {
				t.Fatalf("error = %v; se esperaba uno que envuelva ErrNotWired", err)
			}
			if err.Error() != c.want {
				t.Errorf("texto = %q; se esperaba %q", err.Error(), c.want)
			}
			if svc != nil {
				t.Error("se devolvió un servicio con un límite inservible")
			}
		})
	}
}

// TestReanalyze_NilService_ReturnsErrNotWired: un servicio nil no revienta.
func TestReanalyze_NilService_ReturnsErrNotWired(t *testing.T) {
	t.Parallel()
	var svc *reanalisis.Service
	out, err := svc.Reanalyze(context.Background(), reanalisis.Request{TenantID: tenantID, IntakeID: intakeID})
	if !errors.Is(err, reanalisis.ErrNotWired) {
		t.Fatalf("error = %v; se esperaba ErrNotWired", err)
	}
	if out != (reanalisis.Result{}) {
		t.Errorf("resultado = %+v; se esperaba el valor cero", out)
	}
}

// TestReanalyze_OpensTheJobWithItsWholeContext es el 200 del §8.1 y, de paso, la
// afirmación de que las cuatro columnas de la 0080 se escriben con lo que toca.
func TestReanalyze_OpensTheJobWithItsWholeContext(t *testing.T) {
	t.Parallel()
	b := newBench(t)

	out := b.mustAsk(t, reanalisis.Request{})

	if len(b.jobs.opened) != 1 {
		t.Fatalf("jobs abiertos = %v; se esperaba uno", b.jobs.opened)
	}
	want := reanalisis.Result{
		IntakeID:   intakeID,
		RevisionNo: 2, // la solicitud iba por la 1
		JobID:      b.jobs.opened[0],
		Via:        "local", // sin fila en tenant_llm la vía efectiva es local
		Status:     reanalisis.StatusInProgress,
	}
	if out != want {
		t.Fatalf("resultado = %+v; se esperaba %+v", out, want)
	}
	if reanalisis.StatusInProgress != "processing" {
		t.Errorf("StatusInProgress = %q; el contrato §8.1 publica %q", reanalisis.StatusInProgress, "processing")
	}

	row, ok := b.jobs.View(out.JobID)
	if !ok {
		t.Fatalf("el job %s no está en la cola", out.JobID)
	}
	if row.Key != eventKey {
		t.Errorf("clave de ventana = %+v; sale de la solicitud y se esperaba %+v", row.Key, eventKey)
	}
	if row.IntakeID != intakeID {
		t.Errorf("intake del job = %q; nace apuntando a la solicitud que ya existe", row.IntakeID)
	}
	if row.Status != intake.StatusPending {
		t.Errorf("estado del job = %q; el job del re-análisis nace pending", row.Status)
	}
	wantCtx := intake.Reanalysis{
		RequestedBy: intake.RequestedByOwner, Via: "local", Source: stages.SourceEventThread, From: 1,
	}
	if row.Reanalysis != wantCtx {
		t.Errorf("contexto = %+v; se esperaba %+v", row.Reanalysis, wantCtx)
	}
	if len(b.composer.keys) != 1 || b.composer.keys[0] != eventKey || b.composer.keys[0] != row.Key {
		t.Errorf("sobres compuestos = %+v; se compone UNA vez y para la MISMA ventana del job (%+v)", b.composer.keys, row.Key)
	}
}

// TestReanalyze_TheJobIsBornWithExactlyTheComposedEnvelope es T8.40 (D-F7-9, D-F8-13): el
// job que queda en la cola lleva, desde que existe, las TRES piezas que devolvió el
// compositor, byte a byte. Se lee la fila nada más volver Reanalyze: no hay una segunda
// escritura que lo rellene después, y el worker que lo reclame lo recibe con su literal.
func TestReanalyze_TheJobIsBornWithExactlyTheComposedEnvelope(t *testing.T) {
	t.Parallel()
	want := intake.SourceText{Enc: []byte("enc-de-este-test"), DEK: []byte("dek-de-este-test"), KEKID: "kek-7"}
	b := newBench(t, func(b *bench) { b.composer.env = want })

	out := b.mustAsk(t, reanalisis.Request{})

	row, ok := b.jobs.View(out.JobID)
	if !ok {
		t.Fatalf("el job %s no está en la cola", out.JobID)
	}
	if got := row.SourceText; !bytes.Equal(got.Enc, want.Enc) || !bytes.Equal(got.DEK, want.DEK) || got.KEKID != want.KEKID {
		t.Errorf("sobre del job recién abierto = %+v; se esperaba el del compositor, %+v", got, want)
	}
	if row.Status != intake.StatusPending {
		t.Errorf("estado del job = %q; nace pending y ya con su sobre", row.Status)
	}
	job, claimed, err := b.jobs.ClaimNext(context.Background())
	if err != nil || !claimed || job.ID != out.JobID || !job.SourceText.Complete() {
		t.Errorf("reclamo inmediato = (job %q, ok %t, err %v, sobre completo %t); el worker tiene que llevárselo con su literal",
			job.ID, claimed, err, job.SourceText.Complete())
	}
}

// TestReanalyze_RevisionNoIsTheCurrentOnePlusOne: el número publicado es una previsión,
// el vigente más uno; y el vigente es lo que el job lleva como `reanalyzed_from`.
func TestReanalyze_RevisionNoIsTheCurrentOnePlusOne(t *testing.T) {
	t.Parallel()
	for name, current := range map[string]int{"intake without revisions": 0, "intake at revision 4": 4} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, func(b *bench) { b.intakes.target.LastRevisionNo = current })

			out := b.mustAsk(t, reanalisis.Request{})

			if out.RevisionNo != current+1 {
				t.Errorf("RevisionNo = %d; se esperaba %d", out.RevisionNo, current+1)
			}
			if row, _ := b.jobs.View(out.JobID); row.Reanalysis.From != current {
				t.Errorf("reanalyzed_from = %d; se esperaba %d", row.Reanalysis.From, current)
			}
		})
	}
}

// TestReanalyze_AsksTheThreadWithTheConstructorLimit sostiene que la comprobación de
// fuente mira EXACTAMENTE las entradas que se van a componer: el límite es el que se le
// dio al construir (el del compositor), y el evento, el de la solicitud.
func TestReanalyze_AsksTheThreadWithTheConstructorLimit(t *testing.T) {
	t.Parallel()
	b := newBench(t)

	b.mustAsk(t, reanalisis.Request{})

	if len(b.thread.limits) != 1 || b.thread.limits[0] != threadLimit {
		t.Errorf("límites pedidos = %v; se esperaba una lectura con %d", b.thread.limits, threadLimit)
	}
	if len(b.thread.askedEvents) != 1 || b.thread.askedEvents[0] != eventID {
		t.Errorf("eventos leídos = %v; se esperaba %s", b.thread.askedEvents, eventID)
	}
}

// TestReanalyze_FullStepSequenceIsTheContract afirma el orden COMPLETO del §8.1 sobre el
// camino que recorre todos los escalones: vía `api` configurada y completa (para que
// aparezca el gate de la vía) más `text` pegado (para que aparezcan el dedupe y la
// escritura del hilo). La lista se compara ENTERA y en orden: mover la solicitud o la
// fuente delante de los gates —una fuga de existencia— no cambia el error de este
// camino, y solo esto lo ve.
func TestReanalyze_FullStepSequenceIsTheContract(t *testing.T) {
	t.Parallel()
	b := newBench(t, withAPIConfig, withAPILLM)

	out := b.mustAsk(t, reanalisis.Request{Via: "api", Text: "son 30 tequeños crudos"})

	b.requireSteps(t,
		// el NIVEL primero, después la vía, después el gate de la vía
		stepLevelGate, stepVia, stepViaGate,
		// (la credencial no toca ningún puerto: sale de la Config ya leída)
		stepIntake, stepLiveJob,
		// la FUENTE, la última de las lecturas
		stepSource,
		// y solo entonces se ESCRIBE: el texto pegado; con él ya en el hilo se compone
		// el sobre; y el job, el ÚLTIMO, nace con ese sobre (D-F7-9, D-F8-13)
		stepDedupe, stepWriteThread, stepComposeEnvel, stepOpenJob,
	)
	if out.Via != "api" {
		t.Errorf("vía = %q; se esperaba api", out.Via)
	}
	if row, _ := b.jobs.View(out.JobID); row.Reanalysis.Via != "api" {
		t.Errorf("vía del job = %q; se esperaba api", row.Reanalysis.Via)
	}
}

// TestReanalyze_LocalVia_SequenceSkipsTheViaGate: con la vía efectiva `local` la
// secuencia SALTA el gate de la vía, y sin `text` no hay dedupe ni escritura del hilo.
func TestReanalyze_LocalVia_SequenceSkipsTheViaGate(t *testing.T) {
	t.Parallel()
	b := newBench(t)

	b.mustAsk(t, reanalisis.Request{})

	b.requireSteps(t,
		stepLevelGate, stepVia,
		stepIntake, stepLiveJob, stepSource,
		stepComposeEnvel, stepOpenJob,
	)
}

// TestReanalyze_ComposerDown_FailsTheRequestAndOpensNoJob: el sobre se compone ANTES de
// abrir el job (D-F7-9, D-F8-13), así que si no se puede componer o cifrar NO se abre
// nada y la petición lo dice: el error sale envuelto con el evento y deja leer la causa.
// La fila del texto pegado, que ya se escribió, se queda. Ni el error ni el log llevan
// contenido del hilo, y el aviso viejo del «job abierto sin literal» ya no existe.
func TestReanalyze_ComposerDown_FailsTheRequestAndOpensNoJob(t *testing.T) {
	t.Parallel()
	const pasted = "son 30 tequeños crudos"
	for name, text := range map[string]string{"without pasted text": "", "with pasted text": pasted} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, func(b *bench) { b.composer.err = errInfra })

			out, err := b.ask(reanalisis.Request{Text: text})

			want := "reanalisis: componer el literal del evento " + eventID + ": " + errInfra.Error()
			if !errors.Is(err, errInfra) || err.Error() != want {
				t.Fatalf("error = %v; se esperaba %q envolviendo la causa", err, want)
			}
			if out != (reanalisis.Result{}) {
				t.Errorf("resultado = %+v; con el compositor caído no se publica nada", out)
			}
			b.requireNoJob(t)
			if len(b.composer.keys) != 1 {
				t.Errorf("composiciones = %d; se esperaba un único intento", len(b.composer.keys))
			}
			wantRows := 0
			if text != "" {
				wantRows = 1
			}
			if len(b.thread.written) != wantRows {
				t.Errorf("filas escritas en el hilo = %d; se esperaban %d: el texto pegado se queda", len(b.thread.written), wantRows)
			}
			logged := b.out.String()
			for _, banned := range []string{customerText, pasted, "SIN literal", "job abierto a petición del dueño"} {
				if strings.Contains(err.Error(), banned) || strings.Contains(logged, banned) {
					t.Errorf("el error o el log llevan %q: ni contenido del hilo ni un job que no se abrió; log:\n%s", banned, logged)
				}
			}
		})
	}
}

// TestReanalyze_OpenJobFails_ErrorAsIsAndNoJob: el fallo de la apertura sale tal cual y
// no queda ningún job. El sobre SÍ se había compuesto —una vez, antes de abrir: es con
// el que iba a nacer— y se descarta sin escribirse en ningún sitio; la fila del texto
// pegado —que ya se escribió— se queda: es material del hilo y el siguiente re-análisis
// la leerá.
func TestReanalyze_OpenJobFails_ErrorAsIsAndNoJob(t *testing.T) {
	t.Parallel()
	b := newBench(t, func(b *bench) { b.jobs.openErr = errInfra })

	_, err := b.ask(reanalisis.Request{Text: "son 30 tequeños crudos"})

	if !isUnwrapped(err, errInfra) {
		t.Fatalf("error = %v; se esperaba el de la cola tal cual", err)
	}
	if len(b.jobs.opened) != 0 {
		t.Errorf("jobs abiertos = %v; la apertura falló y no tiene que quedar ninguno", b.jobs.opened)
	}
	if len(b.composer.keys) != 1 || b.composer.keys[0] != eventKey {
		t.Errorf("sobres compuestos = %+v; se compone UNA vez, antes de abrir, para la ventana del evento", b.composer.keys)
	}
	if len(b.thread.written) != 1 {
		t.Errorf("filas pegadas = %d; la del texto ya estaba escrita y se queda", len(b.thread.written))
	}
}

// TestReanalyze_LogCarriesIdsNeverContent es REQ-10c / ADR-0034 sobre esta puerta: el
// literal del cliente y el texto del dueño viven SOLO en memoria. Se comprueba sobre el
// camino que MÁS texto maneja —con transcripción pegada y con hilo— y con el log a
// nivel Debug, para que entren también las líneas del texto pegado.
func TestReanalyze_LogCarriesIdsNeverContent(t *testing.T) {
	t.Parallel()
	const pasted = "el cliente dijo por instagram que quiere 30 tequeños"
	b := newBench(t)

	out := b.mustAsk(t, reanalisis.Request{Text: pasted})

	logged := b.out.String()
	if strings.Contains(logged, customerText) {
		t.Error("el literal del hilo se coló en el log")
	}
	if strings.Contains(logged, pasted) || strings.Contains(logged, "instagram") {
		t.Error("la transcripción del dueño se coló en el log")
	}
	for _, want := range []string{
		"level=INFO", `msg="reanalisis: job abierto a petición del dueño"`,
		"tenant_id=" + tenantID, "intake_id=" + intakeID, "event_id=" + eventID, "job_id=" + out.JobID,
		"via=local", "source=" + stages.SourceBoth, "reanalyzed_from=1", "status_intake=pending_approval",
		fmt.Sprintf("runas_pegadas=%d", utf8.RuneCountInString(pasted)),
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("el log no lleva %q; log:\n%s", want, logged)
		}
	}
}

// TestPorts_NoneCanReachTheCustomerNorAnOldEnvelope es INV-1 / INV-12 e INV-10 sostenidos
// en los TIPOS: los seis puertos tienen EXACTAMENTE estos métodos. No hay por dónde
// mandarle un mensaje al cliente (ni Sender, ni Notifier, ni Gateway), ni por dónde
// cambiar la solicitud (Intakes solo lee), ni por dónde reclamar un job o leer el sobre
// de uno viejo (Jobs solo pregunta y abre): el material se reconstruye SIEMPRE desde el
// hilo del evento. Un test de conducta solo prueba los caminos que recorre; esto prueba
// que ninguna rama futura tendrá por dónde. Sustituye a los dos tests de `go/ast` del
// viejo (no eran candados de `05` §3.2): se mira el tipo, no el texto del fichero.
func TestPorts_NoneCanReachTheCustomerNorAnOldEnvelope(t *testing.T) {
	t.Parallel()
	ports := map[string]struct {
		typ  reflect.Type
		want []string
	}{
		"Intakes":   {reflect.TypeOf((*reanalisis.Intakes)(nil)).Elem(), []string{"ReanalysisTargetOf"}},
		"Thread":    {reflect.TypeOf((*reanalisis.Thread)(nil)).Elem(), []string{"AppendPastedMessage", "ListPastedByOwner", "ListThread"}},
		"Jobs":      {reflect.TypeOf((*reanalisis.Jobs)(nil)).Elem(), []string{"LiveJobOfEvent", "OpenReanalysis"}},
		"Composer":  {reflect.TypeOf((*reanalisis.Composer)(nil)).Elem(), []string{"Compose"}},
		"Features":  {reflect.TypeOf((*reanalisis.Features)(nil)).Elem(), []string{"Has"}},
		"LLMConfig": {reflect.TypeOf((*reanalisis.LLMConfig)(nil)).Elem(), []string{"Get"}},
	}
	for name, port := range ports {
		got := make([]string, 0, port.typ.NumMethod())
		for i := range port.typ.NumMethod() {
			got = append(got, port.typ.Method(i).Name)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, port.want) {
			t.Errorf("métodos de %s = %v; se esperaban exactamente %v", name, got, port.want)
		}
	}

	// Y el constructor no recibe NADA más que el log, esos seis puertos y el límite.
	ctor := reflect.TypeOf(reanalisis.NewService)
	wantIn := []reflect.Type{
		reflect.TypeOf((*logger.Logger)(nil)).Elem(),
		ports["Intakes"].typ, ports["Thread"].typ, ports["Jobs"].typ,
		ports["Composer"].typ, ports["Features"].typ, ports["LLMConfig"].typ,
		reflect.TypeOf(0),
	}
	if ctor.NumIn() != len(wantIn) || ctor.IsVariadic() {
		t.Fatalf("NewService recibe %d parámetros (variádico=%t); se esperaban %d", ctor.NumIn(), ctor.IsVariadic(), len(wantIn))
	}
	for i, want := range wantIn {
		if ctor.In(i) != want {
			t.Errorf("parámetro %d de NewService = %v; se esperaba %v", i, ctor.In(i), want)
		}
	}
}
