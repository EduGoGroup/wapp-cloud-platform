package reanalisis_test

// reanalisis_source_test.go — EL MATERIAL de Reanalyze: si hay algo que analizar
// (escalón 9, con sus dos razones) y el texto pegado del dueño (REQ-32 / D-044.17): se
// sanea, se guarda UNA vez y decide el origen que viaja en el job.

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
)

// contextOnly es un hilo hecho solo de CONTEXTO: un resumen del sistema y un saliente
// fuera de turno. Ninguno es material del cliente (REQ-10b, D-044.24): el rescate LISTA
// PRODUCTOS, y un `source_text` hecho solo de esto los daría por pedidos.
func contextOnly() []events.ThreadEntry {
	return []events.ThreadEntry{
		{Seq: 1, Role: events.RoleSystem, Kind: events.KindSummary, Text: "resumen"},
		{Seq: 2, Role: events.RoleBusiness, Kind: events.KindMessageOutOfTurn, Text: "tenemos tequeños y tortas"},
	}
}

// emptiedMessages es un hilo cuyas filas `message` perdieron el cuerpo: lo que dejaría
// la poda del hilo el día que exista.
func emptiedMessages() []events.ThreadEntry {
	return []events.ThreadEntry{
		{Seq: 1, Role: events.RoleClient, Kind: events.KindMessage, Text: ""},
		{Seq: 2, Role: events.RoleBusiness, Kind: events.KindMessage, Text: ""},
	}
}

// sourceOf devuelve el origen con el que se abrió el job.
func sourceOf(t *testing.T, b *bench, jobID string) string {
	t.Helper()
	row, ok := b.jobs.View(jobID)
	if !ok {
		t.Fatalf("el job %s no está en la cola", jobID)
	}
	return row.Reanalysis.Source
}

// TestReanalyze_NoMaterial_SourceUnavailableWithItsReason es la frontera entre `purged`
// y `never_stored`, que es lo que decide qué se le dice al dueño.
func TestReanalyze_NoMaterial_SourceUnavailableWithItsReason(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		entries []events.ThreadEntry
		reason  string
	}{
		"not a single message row":       {contextOnly(), reanalisis.ReasonNeverStored},
		"empty thread":                   {nil, reanalisis.ReasonNeverStored},
		"message rows with emptied body": {emptiedMessages(), reanalisis.ReasonPurged},
		"emptied messages plus context":  {append(emptiedMessages(), contextOnly()...), reanalisis.ReasonPurged},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, func(b *bench) { b.thread.entries = c.entries })

			_, err := b.ask(reanalisis.Request{})

			var source reanalisis.SourceUnavailableError
			if !errors.As(err, &source) {
				t.Fatalf("error = %v; se esperaba SourceUnavailableError", err)
			}
			if source.Reason != c.reason {
				t.Errorf("razón = %q; se esperaba %q", source.Reason, c.reason)
			}
			b.requireSteps(t, stepLevelGate, stepVia, stepIntake, stepLiveJob, stepSource)
			b.requireNoWrites(t)
		})
	}
}

// TestReanalyze_LegacyIntakeWithoutEvent_NeverStored es el pedido pre-0054: existe, el
// dueño lo está mirando, y no cuelga de ningún evento. Es «no hay original guardado» y
// NO un 404; y sin evento no hay nada más que preguntar.
func TestReanalyze_LegacyIntakeWithoutEvent_NeverStored(t *testing.T) {
	t.Parallel()
	b := newBench(t, func(b *bench) { b.intakes.target.EventID = "" })

	_, err := b.ask(reanalisis.Request{})

	var source reanalisis.SourceUnavailableError
	if !errors.As(err, &source) || source.Reason != reanalisis.ReasonNeverStored {
		t.Fatalf("error = %v; se esperaba SourceUnavailableError{never_stored}", err)
	}
	b.requireSteps(t, stepLevelGate, stepVia, stepIntake)
	b.requireNoWrites(t)
}

// TestReanalyze_ThreadReadFails_IsWrapped: no poder leer el hilo no es «no hay material».
func TestReanalyze_ThreadReadFails_IsWrapped(t *testing.T) {
	t.Parallel()
	b := newBench(t, func(b *bench) { b.thread.listErr = errInfra })

	_, err := b.ask(reanalisis.Request{})

	if !errors.Is(err, errInfra) {
		t.Fatalf("error = %v; se esperaba el del hilo envuelto", err)
	}
	if want := "reanalisis: leer el hilo del evento " + eventID + ": " + errInfra.Error(); err.Error() != want {
		t.Errorf("texto = %q; se esperaba %q", err.Error(), want)
	}
	b.requireNoWrites(t)
}

// TestReanalyze_PastedText_OneSanitizedRowAndSourceBoth: una fila nueva con el texto
// SANEADO (saltos a espacio, espacios colapsados, extremos recortados), y con hilo el
// origen pasa a `both`.
func TestReanalyze_PastedText_OneSanitizedRowAndSourceBoth(t *testing.T) {
	t.Parallel()
	b := newBench(t)

	out := b.mustAsk(t, reanalisis.Request{Text: "  son 30  tequeños\ncrudos "})

	if len(b.thread.written) != 1 || b.thread.written[0] != "son 30 tequeños crudos" {
		t.Fatalf("filas pegadas = %q; se esperaba una, con el texto saneado", b.thread.written)
	}
	if got := sourceOf(t, b, out.JobID); got != stages.SourceBoth {
		t.Errorf("origen = %q; se esperaba %q", got, stages.SourceBoth)
	}
}

// TestReanalyze_SamePastedTextTwice_StillOneRow: «repetir la llamada con el mismo `text`
// ⇒ sigue habiendo una». Se compara el texto SANEADO, así que la segunda llamada con
// espacios distintos se reconoce igual. Y sí abre su job: dos re-análisis son dos actos.
func TestReanalyze_SamePastedTextTwice_StillOneRow(t *testing.T) {
	t.Parallel()
	b := newBench(t)

	first := b.mustAsk(t, reanalisis.Request{Text: "son 30 tequeños crudos"})
	b.finishJob(t, first.JobID)
	second := b.mustAsk(t, reanalisis.Request{Text: "son 30   tequeños\tcrudos  "})

	if len(b.thread.written) != 1 {
		t.Errorf("filas pegadas = %q; el mismo texto no duplica la fila", b.thread.written)
	}
	if len(b.jobs.opened) != 2 || first.JobID == second.JobID {
		t.Errorf("jobs abiertos = %v; se esperaban dos distintos", b.jobs.opened)
	}
	if got := sourceOf(t, b, second.JobID); got != stages.SourceBoth {
		t.Errorf("origen de la segunda = %q; trae `text`, así que sigue siendo %q", got, stages.SourceBoth)
	}
}

// TestReanalyze_DifferentPastedText_AddsAnotherRow: el dedupe es por texto, no por evento.
func TestReanalyze_DifferentPastedText_AddsAnotherRow(t *testing.T) {
	t.Parallel()
	b := newBench(t, func(b *bench) { b.thread.pasted = []string{"son 30 tequeños crudos"} })

	b.mustAsk(t, reanalisis.Request{Text: "son 30 tequeños fritos"})

	if len(b.thread.written) != 1 || b.thread.written[0] != "son 30 tequeños fritos" {
		t.Errorf("filas pegadas = %q; un texto distinto se guarda", b.thread.written)
	}
}

// TestReanalyze_SecondTimeWithoutText_ReadsTheRowBack: la fila pegada quedó en el hilo,
// así que en la segunda pasada es material del evento y el origen vuelve a ser
// `event_thread` — sin `text` en el cuerpo no hay nada «pegado» en ESTA llamada.
func TestReanalyze_SecondTimeWithoutText_ReadsTheRowBack(t *testing.T) {
	t.Parallel()
	b := newBench(t)

	first := b.mustAsk(t, reanalisis.Request{Text: "son 30 tequeños crudos"})
	b.finishJob(t, first.JobID)
	// La fila pegada ya vive en el hilo del evento, como cualquier otra.
	b.thread.entries = append(b.thread.entries, events.ThreadEntry{
		Seq: 2, Role: events.RoleClient, Kind: events.KindMessage, Text: "son 30 tequeños crudos",
	})
	second := b.mustAsk(t, reanalisis.Request{})

	if got := sourceOf(t, b, second.JobID); got != stages.SourceEventThread {
		t.Errorf("origen = %q; se esperaba %q", got, stages.SourceEventThread)
	}
	if len(b.thread.written) != 1 {
		t.Errorf("filas pegadas = %q; la segunda llamada no trae texto", b.thread.written)
	}
}

// TestReanalyze_OnlyPastedText_SourcePastedText: sin texto del cliente en el hilo pero
// CON transcripción hay material, y es solo el del dueño. Es el ÚNICO camino por el que
// `pasted_text` se escribe. El contexto y las filas vaciadas no cuentan como hilo.
func TestReanalyze_OnlyPastedText_SourcePastedText(t *testing.T) {
	t.Parallel()
	cases := map[string][]events.ThreadEntry{
		"empty thread":     nil,
		"context only":     contextOnly(),
		"emptied messages": emptiedMessages(),
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, func(b *bench) { b.thread.entries = entries })

			out := b.mustAsk(t, reanalisis.Request{Text: "son 30 tequeños crudos"})

			if got := sourceOf(t, b, out.JobID); got != stages.SourcePastedText {
				t.Errorf("origen = %q; se esperaba %q", got, stages.SourcePastedText)
			}
			if len(b.thread.written) != 1 {
				t.Errorf("filas pegadas = %q; se esperaba una", b.thread.written)
			}
		})
	}
}

// TestReanalyze_TextThatSanitizesToEmpty_IsLikeNotSendingIt: un `text` de puros
// invisibles no es un error y no escribe nada; el origen sigue siendo el hilo. Y si
// además no hay hilo, no hay material: no cuenta como texto pegado.
func TestReanalyze_TextThatSanitizesToEmpty_IsLikeNotSendingIt(t *testing.T) {
	t.Parallel()
	// Dos espacios de ancho cero (U+200B) y espacios de maquetación, construidos por
	// código: un invisible pegado en el fuente sería invisible también para quien revise.
	invisible := string([]rune{0x200B, 0x200B}) + "   \n\t"

	t.Run("with thread", func(t *testing.T) {
		t.Parallel()
		b := newBench(t)

		out := b.mustAsk(t, reanalisis.Request{Text: invisible})

		if got := sourceOf(t, b, out.JobID); got != stages.SourceEventThread {
			t.Errorf("origen = %q; se esperaba %q", got, stages.SourceEventThread)
		}
		b.requireSteps(t, stepLevelGate, stepVia, stepIntake, stepLiveJob, stepSource, stepComposeEnvel, stepOpenJob)
	})
	t.Run("without thread", func(t *testing.T) {
		t.Parallel()
		b := newBench(t, func(b *bench) { b.thread.entries = nil })

		_, err := b.ask(reanalisis.Request{Text: invisible})

		var source reanalisis.SourceUnavailableError
		if !errors.As(err, &source) || source.Reason != reanalisis.ReasonNeverStored {
			t.Fatalf("error = %v; se esperaba SourceUnavailableError{never_stored}", err)
		}
		b.requireNoWrites(t)
	})
}

// TestReanalyze_PastedRowsReadFails_IsWrappedAndOpensNothing: sin poder leer las ya
// pegadas no se puede deduplicar; no se escribe la fila ni se abre el job.
func TestReanalyze_PastedRowsReadFails_IsWrappedAndOpensNothing(t *testing.T) {
	t.Parallel()
	b := newBench(t, func(b *bench) { b.thread.pastedErr = errInfra })

	_, err := b.ask(reanalisis.Request{Text: "son 30 tequeños crudos"})

	if !errors.Is(err, errInfra) {
		t.Fatalf("error = %v; se esperaba el del hilo envuelto", err)
	}
	want := "reanalisis: leer las transcripciones ya pegadas del evento " + eventID + ": " + errInfra.Error()
	if err.Error() != want {
		t.Errorf("texto = %q; se esperaba %q", err.Error(), want)
	}
	b.requireNoWrites(t)
}

// TestReanalyze_PastedRowWriteFails_IsWrappedAndOpensNoJob: si el texto del dueño no se
// pudo guardar, el job no se abre — se analizaría sin el material que el dueño acaba de
// dar.
func TestReanalyze_PastedRowWriteFails_IsWrappedAndOpensNoJob(t *testing.T) {
	t.Parallel()
	b := newBench(t, func(b *bench) { b.thread.appendErr = errInfra })

	_, err := b.ask(reanalisis.Request{Text: "son 30 tequeños crudos"})

	if !errors.Is(err, errInfra) {
		t.Fatalf("error = %v; se esperaba el del hilo envuelto", err)
	}
	want := "reanalisis: guardar la transcripción pegada en el hilo del evento " + eventID + ": " + errInfra.Error()
	if err.Error() != want {
		t.Errorf("texto = %q; se esperaba %q", err.Error(), want)
	}
	if len(b.jobs.opened) != 0 || len(b.composer.keys) != 0 {
		t.Errorf("se abrió un job (%v) o se compuso un sobre (%v) sin el texto guardado", b.jobs.opened, b.composer.keys)
	}
}
