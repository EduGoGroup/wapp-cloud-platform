//go:build integracion

package procesos

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// El ADELANTO POR CLASIFICACIÓN de la ventana de captación, de extremo a extremo: con catálogo de
// intenciones publicado, cada entrante con texto pide una P1 al Edge y, si el modelo contesta
// `intake_request` con confianza suficiente, el barrido cierra la ventana SIN esperar sus plazos. Es
// el único camino de P4 que cruza los dos sentidos del cable agregador ↔ pool de clasificación
// (pedir: agregador → pool; responder: pool → agregador). El recorrido de p4_borrador_test.go afirma
// lo contrario para su escenario (sin catálogo no hay ninguna P1), así que este va con servidor y
// base propios.
//
// Lo que se ve desde la puerta: el adelanto no deja log ni columna (el job que cierra por adelanto es
// indistinguible del que cierra por plazo). La prueba es por descarte: los dos plazos de la ventana
// valen draftHoldSeconds (una hora) durante todo el test, así que una ventana que sale de
// `aggregating` solo ha podido salir por la pista.

const (
	// p4AheadOtherPn y p4AheadLowPn son los contactos de las dos ventanas que NO se adelantan, y
	// p4AheadPn el de la que sí.
	p4AheadOtherPn = "573004460001"
	p4AheadLowPn   = "573004460002"
	p4AheadPn      = "573004460003"

	// p4AheadIntent es la intención que adelanta el cierre (IntentIntakeRequest,
	// internal/flujos/runtime/aggregator.go) y p4AheadOtherIntent otra del mismo catálogo, que no.
	p4AheadIntent      = "intake_request"
	p4AheadOtherIntent = "consulta_horario"

	// p4AheadConfidence es la confianza del caso Ámbar (`fusClasificador`: 0.95) y p4AheadLowConfidence
	// una por debajo del umbral de fábrica del agregador (defaultIntentConfidence = 0.7).
	p4AheadConfidence    = 0.95
	p4AheadLowConfidence = 0.5

	// p4AheadOtherText es el único mensaje de la ventana de p4AheadOtherPn: abre el evento (lleva
	// draftKeyword) y pregunta otra cosa. p4AheadOtherEvidence es una subcadena literal suya: una
	// evidencia que no está en el mensaje tumbaría la clasificación antes de llegar al agregador, y
	// el caso no probaría el umbral sino el saneo.
	p4AheadOtherText     = "Hola, ¿me pasan un presupuesto? Y de paso, ¿hasta qué hora abren hoy?"
	p4AheadOtherEvidence = "hasta qué hora abren hoy"

	// p4AheadGate es el retardo con el que P1 queda EN VUELO mientras entra la ráfaga entera; lo corta
	// Script.Release. Queda por debajo del presupuesto de una P1 (intakeahead.DefaultTimeout, 45 s).
	p4AheadGate = 40 * time.Second
)

// p4AheadCatalog es el catálogo de intenciones del escenario, en el contrato de PUT /api/v1/intents:
// la que adelanta y otra, cada una con un ejemplo y sin params (D-044.20: la salida útil de P1 es
// «esto es un pedido», no la lista de productos).
func p4AheadCatalog() map[string]any {
	intent := func(name, description, example string) map[string]any {
		return map[string]any{"name": name, "descripcion": description, "ejemplos": []map[string]any{{"mensaje": example}}}
	}
	return map[string]any{
		"version": "v1",
		"intents": []map[string]any{
			intent(p4AheadIntent, "El cliente pide productos o un presupuesto", "quiero dos tortas para el sábado"),
			intent(p4AheadOtherIntent, "El cliente pregunta por el horario de atención", "¿a qué hora cierran?"),
		},
	}
}

// TestP4_AheadClassification recorre, contra un servidor propio y con el catálogo de intenciones
// publicado, la política de disparo del adelanto. Los plazos de la ventana valen una hora de principio
// a fin: nada cierra por plazo.
//
//   - no_adelanta: una P1 que contesta OTRA intención con confianza alta, y otra que contesta
//     `intake_request` por debajo del umbral, no cierran su ventana.
//   - adelanta: una P1 que contesta `intake_request` con 0.95 cierra la ventana de su ráfaga y el
//     pipeline la lleva a borrador, con UNA sola P1 para los tres mensajes.
//   - alcance: la pista es de SU ventana. Con el borrador de la ráfaga adelantada ya hecho —es decir,
//     pasado al menos un barrido posterior a las dos clasificaciones que no disparan— las otras dos
//     ventanas siguen abiertas.
func TestP4_AheadClassification(t *testing.T) {
	t.Parallel()
	sc := draftScenario(t, "p4_ahead", "p4-adelanto")
	if r := sc.Pub.Put(t, "/api/v1/intents", p4AheadCatalog()); r.Codigo != http.StatusOK {
		t.Fatalf("publicar el catálogo de intenciones: HTTP %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	t.Run("no_adelanta", func(t *testing.T) { p4AheadDoesNotTrigger(t, sc) })
	t.Run("adelanta", func(t *testing.T) { p4AheadTriggers(t, sc) })
	t.Run("alcance", func(t *testing.T) { p4AheadIsPerWindow(t, sc) })
	t.Run("cierre", func(t *testing.T) {
		if problems := sc.Script.Problems(); len(problems) != 0 {
			t.Errorf("el guion no supo atender: %v", problems)
		}
		if errs := sc.Edge.Errores(); len(errs) != 0 {
			t.Errorf("errores del núcleo del Edge: %v", errs)
		}
		edgeSinErrores(t, sc.S, nil)
	})
}

// p4AheadWaitP1 espera a que el Edge haya atendido want inferencias de P1 en total. Que no lleguen es
// que el agregador no pidió la clasificación (o que el pool la descartó antes de preguntar).
func p4AheadWaitP1(t *testing.T, sc *draftScene, want int, what string) {
	t.Helper()
	edgeEsperar(t, edgeTopeFila, what, func() bool { return len(sc.Script.Calls(stageP1)) >= want })
	if n := len(sc.Script.Calls(stageP1)); n != want {
		t.Fatalf("%s: el Edge lleva %d inferencias de P1, quería %d", what, n, want)
	}
}

// p4AheadDoesNotTrigger abre dos ventanas de UN mensaje cada una (una sola petición por ventana: sin
// carrera con el cerrojo del pool) y deja que el modelo conteste una clasificación VÁLIDA que no
// dispara: otra intención del catálogo con confianza alta, y la intención que adelanta por debajo del
// umbral. Las dos llegan al agregador —la evidencia es literal de su mensaje—; ninguna anota pista.
// Aquí solo se afirma que la P1 se pidió, una por mensaje; que la ventana NO cierra lo afirma
// p4AheadIsPerWindow, cuando ya ha pasado un barrido.
func p4AheadDoesNotTrigger(t *testing.T, sc *draftScene) {
	for i, c := range []struct {
		contact, text, reply string
	}{
		{p4AheadOtherPn, p4AheadOtherText, scriptClassification(t, p4AheadOtherIntent, p4AheadConfidence, p4AheadOtherEvidence)},
		{p4AheadLowPn, draftBurst()[0], scriptClassification(t, p4AheadIntent, p4AheadLowConfidence, scriptEvidenceDelivery)},
	} {
		sc.Script.Respond(stageP1, c.reply)
		sendDraftBurst(t, sc, c.contact, []string{c.text})
		p4AheadWaitP1(t, sc, i+1, "la P1 del mensaje de "+c.contact)
		call := sc.Script.Calls(stageP1)[i]
		if !strings.Contains(call.Prompt, c.text) || call.MaxOutputTokens != scriptCeilings[stageP1] {
			t.Errorf("la P1 de %s viajó con techo %d y sin el texto del mensaje; quería techo %d y el texto literal",
				c.contact, call.MaxOutputTokens, scriptCeilings[stageP1])
		}
	}
}

// p4AheadTriggers manda la ráfaga canónica con la P1 del primer mensaje retenida en el Edge, la
// suelta con la ráfaga ya entera en la ventana y espera el borrador SIN tocar los plazos (no hay
// flushDraftWindow): lo que cierra la ventana es la pista que deja la clasificación.
//
// 🔢 UNA P1 PARA LOS TRES MENSAJES, y es exacto. El agregador pide una clasificación POR MENSAJE con
// texto (requestAhead, al final de Observe), pero el pool lleva un cerrojo por ventana
// (intakeahead.Pool.Request): con una petición viva de esa ventana, las siguientes se descartan sin
// encolar. La P1 del primer mensaje está en vuelo —retenida por p4AheadGate— mientras entran el
// segundo y el tercero, así que sus dos peticiones se descartan. Sin la retención el número dependería
// de cuánto tarde el modelo (entre una y tres), y además la pista del primero podría cerrar la
// ventana a media ráfaga.
func p4AheadTriggers(t *testing.T, sc *draftScene) {
	texts := draftBurst()
	sc.Script.Respond(stageP1, scriptClassification(t, p4AheadIntent, p4AheadConfidence, scriptEvidenceDelivery))
	sc.Script.Delay(stageP1, p4AheadGate)
	before := len(sc.Script.Calls(""))
	ids := sendDraftBurst(t, sc, p4AheadPn, texts)
	p4AheadWaitP1(t, sc, 3, "la única P1 de la ráfaga, con sus tres mensajes ya en la ventana")
	call := sc.Script.Calls(stageP1)[2]
	if !strings.Contains(call.Prompt, texts[0]) || strings.Contains(call.Prompt, texts[1]) || strings.Contains(call.Prompt, texts[2]) {
		t.Errorf("la P1 de la ráfaga no clasifica el PRIMER mensaje y solo ese: se pide por mensaje, no por literal acumulado")
	}
	if got := p9Scalar(t, sc.DB, draftJobQuery, sc.Tenant, ids[0]); got != "aggregating|3" {
		t.Fatalf("con la P1 todavía en vuelo la ventana vale %q, quería aggregating|3: nada la ha cerrado aún", got)
	}

	sc.Script.Release()
	jobID, intakeID := waitDraft(t, sc, p4AheadPn)

	const settings = `SELECT aggregation_window_seconds::text || '|' || aggregation_max_seconds::text FROM public.tenant_settings WHERE tenant_id = $1`
	if got := p9Scalar(t, sc.DB, settings, sc.Tenant); got != "3600|3600" {
		t.Fatalf("los plazos de la ventana = %q, quería 3600|3600: el cierre tiene que ser por adelanto y no por plazo", got)
	}
	const job = `SELECT status || '|' || coalesce(stage, '-') || '|' || attempts::text || '|' || jsonb_array_length(source_refs)::text
			|| '|' || (SELECT status FROM public.intakes i WHERE i.id = j.intake_id)
		FROM public.intake_jobs j WHERE id = $1::uuid`
	if got, want := p9Scalar(t, sc.DB, job, jobID), "done|draft|0|3|pending_approval"; got != want {
		t.Errorf("el job de la ventana adelantada = %q, quería %q", got, want)
	}
	p4CheckAmbarPayload(t, p4RevisionPayload(t, sc, intakeID), "la revisión del borrador de la ventana adelantada")

	calls := sc.Script.Calls("")[before:]
	got := make([]string, 0, len(calls))
	for _, c := range calls {
		got = append(got, string(c.Stage))
	}
	if want := "p1 p2 p3 p3 p3 p4"; strings.Join(got, " ") != want {
		t.Errorf("las inferencias de la ráfaga adelantada = %v, quería %q: una P1 y el pipeline una vez", got, want)
	}
}

// p4AheadIsPerWindow afirma que las dos ventanas cuya P1 no disparaba siguen abiertas, cada una con
// su mensaje, y que no hubo más P1 que las tres pedidas. No duerme a ciegas: el borrador de la ráfaga
// adelantada (p4AheadTriggers) solo existe si el barrido corrió DESPUÉS de su pista, y esa pista es
// posterior a las dos clasificaciones que no disparan (el Edge sirve las inferencias de una en una y
// en orden). Un barrido que hubiera tenido pista de estas dos ventanas las habría cerrado con ella.
func p4AheadIsPerWindow(t *testing.T, sc *draftScene) {
	for _, contact := range []string{p4AheadOtherPn, p4AheadLowPn} {
		if got := p9Scalar(t, sc.DB, draftJobQuery, sc.Tenant, draftWaID(contact, 0)); got != "aggregating|1" {
			t.Errorf("la ventana de %s = %q, quería aggregating|1: su P1 no era un intake_request sobre el umbral", contact, got)
		}
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.intake_jobs WHERE tenant_id = $1`, sc.Tenant); n != 3 {
		t.Errorf("intake_jobs tiene %d filas de la empresa, quería 3 (dos ventanas abiertas y la adelantada)", n)
	}
	if n := len(sc.Script.Calls(stageP1)); n != 3 {
		t.Errorf("el Edge atendió %d inferencias de P1, quería 3: una por ventana", n)
	}
}
