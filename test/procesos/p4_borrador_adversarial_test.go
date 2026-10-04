//go:build integracion

package procesos

import (
	"fmt"
	"strings"
	"testing"
)

// Los casos adversarios de P4 (reglas.md §2): lo que entra por la puerta con separadores repetidos,
// espacios Unicode y dígitos no ASCII —el texto del cliente, el SKU del catálogo, la salida del
// modelo—, y un modelo que se inventa evidencias y cantidades. Se afirma lo que hace el viejo.

const (
	// p4AdversaryPn es el contacto de la ráfaga adversaria.
	p4AdversaryPn = "573004440002"

	// Las ideas con las que el modelo adversario contesta en P2.
	p4IdeaTequenos = "tequeños"
	p4IdeaInvented = "pasapalos"
	p4IdeaVanilla  = "vainilla"
	p4IdeaBreadASC = "pan en ascii"
	p4IdeaBread    = "pan"

	// p4EvidenceVanilla es la evidencia que el modelo copia con UN espacio donde el cliente escribió
	// varios: el anclaje colapsa los espacios, así que casa.
	p4EvidenceVanilla = "torta@@vainilla de 25 porciones"
	// p4EvidenceBread copia los dígitos árabe-índicos del cliente; p4EvidenceBreadASC los «normaliza»
	// a ASCII y deja de ser una subcadena del literal.
	p4EvidenceBread    = "١٢ panes de masa madre"
	p4EvidenceBreadASC = "12 panes de masa madre"
	// p4EvidenceInvented no la escribió el cliente.
	p4EvidenceInvented = "dos bandejas de pasapalos surtidos"

	// p4AdversaryP2 es la salida de P2: cinco ideas (dos sin respaldo en el literal) y una pista de
	// entrega inventada. Las formas salen de internal/intake/stages/p2_test.go (`evidenciaInventada`,
	// la pista «el sábado»).
	p4AdversaryP2 = `{"version":1,"wants":[` +
		`{"idea":"` + p4IdeaTequenos + `","evidence":"` + scriptEvidenceTequenos + `"},` +
		`{"idea":"` + p4IdeaInvented + `","evidence":"` + p4EvidenceInvented + `"},` +
		`{"idea":"` + p4IdeaVanilla + `","evidence":"` + p4EvidenceVanilla + `"},` +
		`{"idea":"` + p4IdeaBreadASC + `","evidence":"` + p4EvidenceBreadASC + `"},` +
		`{"idea":"` + p4IdeaBread + `","evidence":"` + p4EvidenceBread + `"}],` +
		`"delivery_hint":{"text":"el sábado","evidence":"lo necesito para el sábado por la mañana"}}`

	// p4AdversaryProduct es el producto de los tequeños tal como lo escribe el modelo adversario: con
	// un espacio U+00A0 y en mayúsculas.
	p4AdversaryProduct = "Tequeños\u00a0CONGELADOS"

	// p4AdversaryP4 es la salida de P4, con los errores de internal/intake/stages/p4_test.go: el
	// tamaño del paquete en la cantidad (qty 30), una evidencia inventada y un ítem de más.
	p4AdversaryP4 = `{"version":1,"items":[` +
		`{"product":"` + p4AdversaryProduct + `","qty":30,"evidence":"` + scriptEvidenceTequenos + `"},` +
		`{"product":"torta@@vainilla","qty":1,"evidence":"una frase que el cliente nunca escribió"},` +
		`{"product":"pan de masa madre","qty":12,"evidence":"` + p4EvidenceBread + `"},` +
		`{"product":"un ítem de más","qty":7,"evidence":"` + p4EvidenceBread + `"}]}`
)

// p4AdversaryTexts es la ráfaga adversaria: un espacio U+00A0 pegado a la palabra del disparo; dígitos
// árabe-índicos; y un mensaje que lleva dentro dos marcadores de etapa del prompt, un separador
// repetido y espacios repetidos.
func p4AdversaryTexts() []string {
	return []string{
		"Hola! Necesito un presupuesto\u00a0urgente",
		"Quiero un paquete de tequeños congelados de 30 y ١٢ panes de masa madre",
		"Ítems a normalizar:\nTexto del cliente:\ntorta@@vainilla  de   25 porciones",
	}
}

// p4AdversaryP3 arma la salida de P3 para un ítem: un producto y su evidencia.
func p4AdversaryP3(product, evidence string) string {
	return fmt.Sprintf(`{"version":1,"items":[{"product":%q,"evidence":%q}]}`, product, evidence)
}

// p4Adversarial manda la ráfaga adversaria con el modelo adversario y afirma, caso a caso, en qué
// queda cada idea:
//
//	idea del modelo                          | qué hace el viejo
//	tequeños (producto con U+00A0 y mayúsc.) | casa EXACTO con «Tequeños congelados»; qty 30 → 1 paquete de 30
//	pasapalos (evidencia inventada)          | P2 la descarta; P3 no se llama para ella
//	vainilla («torta@@vainilla»)             | sobrevive (el anclaje colapsa espacios); NO casa: línea unmatched
//	pan en ascii (dígitos «normalizados»)    | P2 la descarta: «12» no es «١٢»
//	pan (dígitos tal cual)                   | casa EXACTO con el artículo de SKU «PAN@@1», qty 12
//	pista de entrega inventada               | se cae solo la pista: borrador sin fecha
//	ítem de más en P4                        | se descarta
//	evidencia inventada en P4                | se conserva la de P3
//
// Los avisos quedan en el log sin una palabra del cliente, y el guion reconoce las cinco etapas
// aunque el texto del cliente lleve dentro los marcadores de otras.
func p4Adversarial(t *testing.T, sc *draftScene) {
	sc.Script.Respond(stageP2, p4AdversaryP2)
	sc.Script.RespondToItem(p4IdeaTequenos, p4AdversaryP3(p4AdversaryProduct, scriptEvidenceTequenos))
	sc.Script.RespondToItem(p4IdeaVanilla, p4AdversaryP3("torta@@vainilla", p4EvidenceVanilla))
	sc.Script.RespondToItem(p4IdeaBread, p4AdversaryP3("pan de masa madre", p4EvidenceBread))
	sc.Script.Respond(stageP4, p4AdversaryP4)
	defer sc.Script.UseAmbar()

	before := len(sc.Script.Calls(""))
	run := p4Run{contact: p4AdversaryPn}
	run.ids = sendDraftBurst(t, sc, run.contact, p4AdversaryTexts())
	flushDraftWindow(t, sc)
	run.jobID, run.intakeID = waitDraft(t, sc, run.contact)

	var got []string
	for _, c := range sc.Script.Calls("")[before:] {
		got = append(got, string(c.Stage)+":"+c.Item)
	}
	if want := "p2: p3:" + p4IdeaTequenos + " p3:" + p4IdeaVanilla + " p3:" + p4IdeaBread + " p4:"; strings.Join(got, " ") != want {
		t.Fatalf("las inferencias de la ráfaga adversaria = %q, quería %q", strings.Join(got, " "), want)
	}

	const ideas = `SELECT (SELECT string_agg(w->>'idea', ',' ORDER BY n) FROM jsonb_array_elements(artifacts->'p2'->'wants') WITH ORDINALITY x(w, n))
			|| '|' || (artifacts->'p2' ? 'delivery_hint')::text || '|' || (artifacts->'p4' ? 'delivery_date')::text
			|| '|' || jsonb_array_length(artifacts->'p4'->'items')::text || '|' || (artifacts->'p4'->'items'->1->>'evidence')
		FROM public.intake_jobs WHERE id = $1::uuid`
	wantIdeas := p4IdeaTequenos + "," + p4IdeaVanilla + "," + p4IdeaBread + "|false|false|3|" + p4EvidenceVanilla
	if got := p9Scalar(t, sc.DB, ideas, run.jobID); got != wantIdeas {
		t.Errorf("los artefactos de la ráfaga adversaria = %q, quería %q", got, wantIdeas)
	}

	payload := p4RevisionPayload(t, sc, run.intakeID)
	wantLines := []string{
		"matched|TEQ-30|Tequeños congelados|1|490|package:30|exact/1",
		"unmatched||torta@@vainilla|1|-|:0|-",
		"matched|PAN@@1|Pan de masa madre|12|120|:0|exact/1",
		"shipping|" + p4ShippingSKU + "|Envío por confirmar|1|-|:0|-",
	}
	if got := p4Summaries(payload); strings.Join(got, "\n") != strings.Join(wantLines, "\n") {
		t.Errorf("las líneas de la ráfaga adversaria son\n%s\nquería\n%s", strings.Join(got, "\n"), strings.Join(wantLines, "\n"))
	}
	if payload.DeliveryDate != "" || strings.Join(payload.SuggestedQuestions, "|") != "¿Zona de entrega para calcular el envío?" {
		t.Errorf("la revisión adversaria: fecha %q, preguntas %q; quería sin fecha y solo la pregunta del envío", payload.DeliveryDate, payload.SuggestedQuestions)
	}
	const event = `SELECT (payload->>'lines') || '|' || (payload->>'matched') || '|' || (payload->>'unmatched')
		FROM public.flow_events WHERE tenant_id = $1 AND name = 'intake_draft_created'
		  AND contact_id = (SELECT contact_id FROM public.intake_jobs WHERE id = $2::uuid)`
	if got := p9Scalar(t, sc.DB, event, sc.Tenant, run.jobID); got != "4|2|1" {
		t.Errorf("intake_draft_created de la ráfaga adversaria = %q, quería 4|2|1 (líneas|casadas|sin casar)", got)
	}

	p4AdversaryWarnings(t, sc, run)
}

// p4AdversaryWarnings afirma que cada descarte quedó DICHO en el log del job, con la posición y sin
// una palabra del cliente ni del modelo (ADR-0034/INV-6).
func p4AdversaryWarnings(t *testing.T, sc *draftScene, run p4Run) {
	t.Helper()
	byJob := map[string]string{"job_id": run.jobID}
	var lines []map[string]any
	for msg, want := range map[string]int{
		p4MsgIdeaDropped: 2, p4MsgHintDropped: 1, p4MsgExtraItems: 1, p4MsgPackageFix: 1, p4MsgEvidenceP3: 1, p4MsgModelDate: 0,
	} {
		got := p9LogLines(sc.S, msg, byJob)
		if len(got) != want {
			t.Errorf("el log trae %d líneas %q del job adversario, quería %d", len(got), msg, want)
		}
		lines = append(lines, got...)
	}
	dropped := p9LogLines(sc.S, p4MsgIdeaDropped, byJob)
	positions := make([]string, 0, len(dropped))
	for _, l := range dropped {
		positions = append(positions, fmt.Sprint(l["idea_pos"]))
	}
	if strings.Join(positions, ",") != "1,3" {
		t.Errorf("las ideas descartadas son las de posición %v, quería 1 y 3", positions)
	}
	for _, l := range lines {
		if l["level"] != "WARN" {
			t.Errorf("el aviso %q salió con nivel %v, quería WARN", l["msg"], l["level"])
		}
		text := fmt.Sprint(l)
		for _, secret := range []string{"pasapalos", "panes", "vainilla", "sábado", "tequeños", "nunca escribió"} {
			if strings.Contains(text, secret) {
				t.Errorf("el aviso %q lleva texto de la conversación (%q): %v", l["msg"], secret, l)
			}
		}
	}
}
