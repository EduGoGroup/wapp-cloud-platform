//go:build integracion

package procesos

import (
	"strings"
	"testing"
)

// El texto pegado de P8: el campo `text` del re-análisis es material EXTRA de la dueña (la
// transcripción de un audio, lo que el cliente le dijo por otro canal). Entra SANEADO y cifrado como
// una fila más del hilo del evento (`role=client`, `origin=owner_pasted`), una sola vez por texto, y
// el pipeline lo lee igual que el resto. Y el barrido de que ni ese texto ni el del cliente quedan en
// claro.

const (
	// p8Needle es una aguja irrepetible dentro de la transcripción: lo que se busca después en TODAS
	// las tablas y en el log.
	p8Needle = "ZZP8AGUJA4W"

	// p8Pasted es la transcripción tal como la pega la dueña: con espacios repetidos, un salto de
	// línea, dígitos no ASCII y un separador repetido. p8PastedClean es como queda saneada (saltos a
	// espacio, espacios colapsados, extremos recortados): lo que entra en el hilo y en el prompt.
	p8Pasted      = "  son ١٢٣  tequeños\ncrudos a@@b " + p8Needle + " "
	p8PastedClean = "son ١٢٣ tequeños crudos a@@b " + p8Needle
	// p8PastedAgain es el MISMO texto con otros espacios: sanea a lo mismo y no duplica la fila.
	p8PastedAgain = "son ١٢٣ tequeños   crudos a@@b " + p8Needle + "\n"
	// p8PastedNBSP cambia un espacio por un U+00A0.
	p8PastedNBSP = "son ١٢٣\u00a0tequeños crudos a@@b " + p8Needle

	// p8MsgPasted y p8MsgPastedDup son las líneas DEBUG del caso de uso al guardar la transcripción y
	// al reconocerla como ya guardada.
	p8MsgPasted    = "reanalisis: transcripción del dueño añadida al hilo"
	p8MsgPastedDup = "reanalisis: la transcripción pegada ya estaba en el hilo; no se duplica"
)

// pastedText recorre el `text` del re-análisis sobre la solicitud principal:
//
//   - con transcripción: el origen pasa a `both`, el hilo gana UNA fila `owner_pasted` cifrada y el
//     prompt la lleva como un mensaje más del cliente, saneada, sin distinguirla;
//   - la misma transcripción con otros espacios: otro re-análisis (otra revisión), la MISMA fila;
//   - con un U+00A0 por un espacio: lo que haga el viejo con el dedupe.
func (w *p8World) pastedText(t *testing.T) {
	sc, tgt := w.sc, w.main

	p := w.open(t, tgt, map[string]any{"text": p8Pasted}, "both")
	tgt.pasted = 1
	w.settle(t, p, 0, p8FullRun)
	w.checkPastedPrompts(t, p, 1)
	if n := len(p9LogLines(sc.S, p8MsgPasted, map[string]string{"event_id": tgt.eventID})); n != 1 {
		t.Errorf("el log trae %d líneas %q del evento, quería 1", n, p8MsgPasted)
	}

	p = w.open(t, tgt, map[string]any{"text": p8PastedAgain}, "both")
	w.settle(t, p, 0, p8FullRun)
	w.checkPastedPrompts(t, p, 1)
	if n := len(p9LogLines(sc.S, p8MsgPastedDup, map[string]string{"event_id": tgt.eventID})); n != 1 {
		t.Errorf("el log trae %d líneas %q del evento, quería 1", n, p8MsgPastedDup)
	}

	// El saneo trata el U+00A0 como un espacio: sanea a lo mismo y tampoco duplica.
	p = w.open(t, tgt, map[string]any{"text": p8PastedNBSP}, "both")
	w.settle(t, p, 0, p8FullRun)
	w.checkPastedPrompts(t, p, 1)
	if n := len(p9LogLines(sc.S, p8MsgPastedDup, map[string]string{"event_id": tgt.eventID})); n != 2 {
		t.Errorf("el log trae %d líneas %q del evento, quería 2", n, p8MsgPastedDup)
	}

	_, revs := w.detail(t, tgt.id)
	if last := revs[len(revs)-1]; !strings.Contains(last.Payload.SourceText, "cliente: "+p8PastedClean+"\n") {
		t.Errorf("el source_text de la última revisión no trae la transcripción pegada como un mensaje del cliente")
	}
}

// checkPastedPrompts afirma que los prompts del re-análisis llevan la transcripción saneada como el
// ÚLTIMO mensaje del cliente, las veces justas (times), y que no la distinguen: ni `owner_pasted` ni
// la palabra `origin`.
func (w *p8World) checkPastedPrompts(t *testing.T, p p8Pending, times int) {
	t.Helper()
	for _, c := range w.sc.Script.Calls("")[p.calls:] {
		if n := strings.Count(c.Prompt, "cliente: "+p8PastedClean+"\n### FIN DE LOS MENSAJES ###"); n != 1 {
			t.Errorf("el prompt de %s no cierra el hilo con la transcripción saneada como mensaje del cliente", c.Stage)
		}
		if n := strings.Count(c.Prompt, p8Needle); n != times {
			t.Errorf("el prompt de %s lleva la transcripción %d veces, quería %d", c.Stage, n, times)
		}
		if strings.Contains(c.Prompt, "owner_pasted") || strings.Contains(c.Prompt, "origin") {
			t.Errorf("el prompt de %s distingue la fila pegada (lleva `owner_pasted` u `origin`)", c.Stage)
		}
	}
}

// noLiteralInClear busca en TODA columna de TODA tabla del esquema public (p4ScanAllTables, el
// barrido de P4) la aguja de la transcripción pegada y una frase del cliente que ninguna `evidence`
// del guion cita: no pueden estar en ningún sitio. El hilo, el sobre del job y el literal de cada
// revisión van cifrados. Como control de que el barrido mira, el id del job principal sí aparece, y
// solo donde debe. Tampoco aparecen en el log ni esos textos ni los teléfonos.
func (w *p8World) noLiteralInClear(t *testing.T) {
	t.Helper()
	sc := w.sc
	const greeting = "Hola, buenas! Te quería pedir" // del primer mensaje, fuera de toda evidencia
	if !strings.HasPrefix(draftBurst()[0], greeting) {
		t.Fatalf("la ráfaga canónica ya no empieza por %q: el barrido buscaría un texto que nadie escribió", greeting)
	}
	for name, secret := range map[string]string{"la transcripción pegada": p8Needle, "el literal del cliente": greeting} {
		if hits := p4ScanAllTables(t, sc, secret); len(hits) != 0 {
			t.Errorf("%s aparece en claro en: %v", name, hits)
		}
	}
	if hits := p4ScanAllTables(t, sc, w.main.firstJob); strings.Join(hits, ",") != "intake_jobs.id" {
		t.Errorf("el id del primer job aparece en %v; quería solo intake_jobs.id (si esto falla, el barrido no está mirando nada)", hits)
	}
	log := sc.S.Log()
	for _, secret := range []string{p8Needle, "tequeños crudos", greeting, scriptEvidenceChoc, p8MainPn, p8ApprovedPn, p8RejectedPn} {
		if strings.Contains(log, secret) {
			t.Errorf("el log del servidor contiene texto o teléfono del cliente o de la dueña: %q", secret)
		}
	}
}
