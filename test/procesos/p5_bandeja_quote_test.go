//go:build integracion

package procesos

import (
	"net/http"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// POST /api/v1/intakes/{id}/quote-suggestion: la máquina redacta la cotización con la voz de la
// dueña y la DEVUELVE; no escribe nada ni le manda nada al cliente. Es la única ruta de la API
// pública que espera a un modelo dentro de la petición, y por eso la única con plazo de escritura
// propio (trampa T-12, documentations/contratos.md §2.5).

const (
	// p5OpenPn es el contacto del borrador que la dueña deja esperando.
	p5OpenPn = "573005550003"

	// p5SlowModel es lo que tarda el «modelo» en redactar: más que el WriteTimeout global del
	// servidor (p5GlobalWriteTimeout), menos que el plazo propio de la ruta (60 s).
	p5SlowModel          = 12 * time.Second
	p5GlobalWriteTimeout = 10 * time.Second
	// p5QuoteClientTimeout es el plazo del cliente del test para esa petición.
	p5QuoteClientTimeout = 45 * time.Second

	// p5DeterministicQuote es el texto que el servidor compone SIN modelo para las líneas de p5Items:
	// es la sugerencia cuando no hay ejemplos, y el repuesto cuando lo que redacta el modelo no vale.
	p5DeterministicQuote = "Hola! Te paso el presupuesto:\n\n" +
		"• Torta de chocolate 10 porciones — $2100\n  Incluye: decoración infantil\n" +
		"• Torta de vainilla — $3900\n" +
		"• 2 × Tequeños congelados — $490 c/u — $980\n" +
		"• Pan de masa madre — $120\n  Incluye: =SUM(A1)\n" +
		"• Pan integral con semillas — $130\n" +
		"• Pan ١٢٣ — $140\n" +
		"• @@cortesía — precio por confirmar\n" +
		"\nTotal: $7370\n\nCualquier duda me dices y lo ajustamos."

	// p5ModelQuote es una redacción del «modelo» que cuadra con las líneas: cada importe del
	// presupuesto, en su orden, y el total.
	p5ModelQuote = "Hola Ambar! Te cuento: la torta de chocolate $2.100, la de vainilla $3.900, los tequeños $490 c/u " +
		"que son $980, el pan de masa madre $120, el integral $130 y el otro pan $140. Total $7.370"
)

// p5Suggestion es el cuerpo de la sugerencia.
type p5Suggestion struct {
	RenderedText   string `json:"rendered_text"`
	Source         string `json:"source"`
	FallbackReason string `json:"fallback_reason"`
}

// p5ExpectSuggestion exige un 200 con ese texto, ese origen y ese motivo de repuesto.
func p5ExpectSuggestion(t *testing.T, r respuesta, text, source, reason, what string) {
	t.Helper()
	if r.Codigo != http.StatusOK {
		t.Errorf("%s: HTTP %d %s; quería 200", what, r.Codigo, recortar(r.Cuerpo))
		return
	}
	var got p5Suggestion
	r.JSON(t, &got)
	if got.RenderedText != text || got.Source != source || got.FallbackReason != reason {
		t.Errorf("%s: %s/%s con el texto\n%q\nquería %s/%s con\n%q", what, got.Source, got.FallbackReason, got.RenderedText, source, reason, text)
	}
}

// p5QuoteWithoutHistory afirma la sugerencia de una empresa que todavía no ha aprobado nada: sin
// ejemplos de la voz de la dueña no se le pide nada al modelo; sale el texto determinista, con su
// motivo. Y no escribe: la solicitud queda igual.
func p5QuoteWithoutHistory(t *testing.T, w *p5World) {
	sc := w.sc
	before := p5Snapshot(t, sc.DB, sc.Tenant, w.first)
	r := w.read(t, http.MethodPost, p5Path(w.first, "quote-suggestion"), nil)
	p5ExpectSuggestion(t, r, p5DeterministicQuote, "deterministic", "sin_ejemplos", "la sugerencia sin historial aprobado")
	if n := len(sc.Script.Calls(stageP5)); n != 0 {
		t.Errorf("la sugerencia sin ejemplos pidió %d inferencias P5: sin la voz de la dueña no se gasta inferencia", n)
	}
	for name, id := range p5BadIDs(t, w.first) {
		r = w.read(t, http.MethodPost, p5Path(id, "quote-suggestion"), nil)
		p5ExpectError(t, r, http.StatusNotFound, p5NotFound, "quote-suggestion con un id "+name)
	}
	w.expectUntouched(t, before, w.first, "tras pedir la sugerencia")
	sc.expectNoPendingText(t, "tras pedir la sugerencia: no le habla al cliente")
}

// p5SlowQuote es T-12: con una cotización ya aprobada en la empresa (el ejemplo), la sugerencia se
// le pide al modelo; el guion tarda p5SlowModel y la respuesta LLEGA, después del WriteTimeout global.
// Después, lo que el modelo redacta mal: el servidor lo detecta y contesta el texto determinista.
func p5SlowQuote(t *testing.T, w *p5World) {
	sc := w.sc
	w.open = createDraft(t, sc, p5OpenPn)
	w.putItems(t, w.open, false)
	before := p5Snapshot(t, sc.DB, sc.Tenant, w.open)
	path := p5Path(w.open, "quote-suggestion")

	sc.Script.Respond(stageP5, scriptQuoteText(t, p5ModelQuote))
	sc.Script.Delay(stageP5, p5SlowModel)
	start := time.Now()
	r := sc.Pub.ConPlazo(p5QuoteClientTimeout).Post(t, path, nil)
	elapsed := time.Since(start)
	sc.Script.Clear(stageP5)
	p5ExpectSuggestion(t, r, p5ModelQuote, "llm", "", "la sugerencia con el modelo lento")
	if elapsed < p5SlowModel || elapsed <= p5GlobalWriteTimeout {
		t.Errorf("la sugerencia volvió en %s: con el modelo tardando %s tenía que llegar DESPUÉS del WriteTimeout global de %s",
			elapsed, p5SlowModel, p5GlobalWriteTimeout)
	}
	p5QuotePrompt(t, w)

	// Lo que el modelo redacta mal no sale: INV-2, los importes los pone el servidor.
	wrong := []struct{ name, reply, reason, warn string }{
		{"un importe que no sale de ninguna línea", scriptQuoteText(t, "Te lo dejo todo en $5.000"), "importe_ajeno", p5MsgQuoteMismatch},
		{"los precios sin el total", scriptQuoteText(t, "Chocolate $2.100, vainilla $3.900, tequeños $490 c/u son $980, panes $120, $130 y $140"),
			"falta_total", p5MsgQuoteMismatch},
		{"los importes en dígitos no ASCII", scriptQuoteText(t, "Total $٧٣٧٠"), "texto_sin_importes", p5MsgQuoteMismatch},
		{"una salida que no es el artefacto", `{"nope":1}`, "salida_no_es_artefacto", p5MsgQuoteIllegible},
	}
	warns := map[string]int{}
	for _, c := range wrong {
		sc.Script.Respond(stageP5, c.reply)
		r = w.read(t, http.MethodPost, path, nil)
		p5ExpectSuggestion(t, r, p5DeterministicQuote, "deterministic", c.reason, "la sugerencia con "+c.name)
		warns[c.warn]++
	}
	sc.Script.Fail(stageP5, cloudlinkv1.InferenceError_INFERENCE_ERROR_OLLAMA_DOWN)
	r = w.read(t, http.MethodPost, path, nil)
	sc.Script.Clear(stageP5)
	p5ExpectSuggestion(t, r, p5DeterministicQuote, "deterministic", "llm_fallo", "la sugerencia con el modelo caído")
	warns[p5MsgQuoteFailed]++

	for msg, n := range warns {
		if got := len(p9LogLines(sc.S, msg, map[string]string{"intake_id": w.open})); got != n {
			t.Errorf("el log tiene %d líneas %q de la solicitud, quería %d", got, msg, n)
		}
	}
	if n := len(sc.Script.Calls(stageP5)); n != 6 {
		t.Errorf("el Edge recibió %d inferencias P5, quería 6 (la lenta, cuatro mal redactadas y la caída)", n)
	}
	w.expectUntouched(t, before, w.open, "tras las sugerencias: quote-suggestion no escribe")
	sc.expectNoPendingText(t, "tras las sugerencias: quote-suggestion no le habla al cliente")
}

// p5QuotePrompt afirma lo que el Edge recibió para redactar: UNA inferencia P5, con su techo de
// salida, que lleva el presupuesto (las líneas y el total) y, como ejemplo de la voz de la dueña, la
// cotización que ella aprobó antes en ESTA empresa; y no lleva ni el teléfono ni el contact_id.
func p5QuotePrompt(t *testing.T, w *p5World) {
	t.Helper()
	calls := w.sc.Script.Calls(stageP5)
	if len(calls) != 1 {
		t.Fatalf("la sugerencia lenta provocó %d inferencias P5, quería 1", len(calls))
	}
	call := calls[0]
	if call.MaxOutputTokens != scriptCeilings[stageP5] {
		t.Errorf("la inferencia P5 viajó con techo %d, quería %d", call.MaxOutputTokens, scriptCeilings[stageP5])
	}
	for _, piece := range []string{scriptMarkers[stageP5], `"total":7370`, "=SUM(A1)", "Entrega el miércoles."} {
		if !strings.Contains(call.Prompt, piece) {
			t.Errorf("el prompt de P5 no lleva %q", piece)
		}
	}
	contact := p9Scalar(t, w.sc.DB, `SELECT contact_id FROM public.intakes WHERE id = $1::uuid`, w.open)
	for _, secret := range []string{p5OpenPn, p5FirstPn, contact, w.open} {
		if strings.Contains(call.Prompt, secret) {
			t.Errorf("el prompt de P5 lleva %q: al modelo solo le va el presupuesto y los ejemplos", secret)
		}
	}
}
