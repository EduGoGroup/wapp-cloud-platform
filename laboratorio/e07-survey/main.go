//go:build laboratorio

// e07-survey es el escalón E07 del laboratorio (Plan 058 · Bloque B): una encuesta de dos preguntas
// (nodos survey_question) dentro de su evento, con el motor REAL. Enseña la respuesta válida, la
// inválida y su repregunta, el menú de salida al tercer fallo, y la PROYECCIÓN: cómo el efecto
// survey_answer de cada respuesta acaba en una fila de survey_results por el PersistSink y el
// survey.Projector reales.
//
//	GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e07-survey
//	… -keyword        (el disparo es keyword en vez de event_start: ver qué pasa)
package main

import (
	"flag"
	"os"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/bench"
	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

const (
	flowID     = "encuesta-e07"
	keyword    = "encuesta"
	otherPhone = "573001110060"
)

func main() {
	useKeyword := flag.Bool("keyword", false, "dispara la encuesta con keyword en vez de event_start")
	verbose := flag.Bool("v", false, "saca el log del cloud por stderr (desde Debug)")
	flag.Parse()
	if err := run(*useKeyword, *verbose); err != nil {
		os.Exit(1)
	}
}

// surveyFlow: dos preguntas y un agradecimiento. Las claves de las opciones son lo que se guarda
// como answer_code; question_id es el nombre de la pregunta en survey_results.
func surveyFlow() model.Flow {
	return model.Flow{
		FlowID: flowID, Initial: "q1",
		Nodes: map[string]model.Node{
			"q1": {Type: model.NodeTypeSurveyQuestion, QuestionID: "atencion",
				Prompt:  "¿Cómo calificas la atención?\n1) Buena\n2) Regular\n3) Mala",
				Options: map[string]string{"1": "q2", "2": "q2", "3": "q2"}},
			"q2": {Type: model.NodeTypeSurveyQuestion, QuestionID: "volverias",
				Prompt:  "¿Volverías a comprarnos?\n1) Sí\n2) No",
				Options: map[string]string{"1": "gracias", "2": "gracias"}},
			"gracias": {Type: model.NodeTypeMessage, Text: "¡Gracias por responder!"},
		},
	}
}

func run(useKeyword, verbose bool) error {
	n := common.NewNarrator(nil)
	n.Title("E07 · Encuesta",
		"Dos survey_question dentro de su evento: válida, inválida, repregunta y proyección a survey_results.",
		"Real: runtime, engine, survey, PersistSink + survey.Projector. Doble: Sender, almacenes en memoria.")

	n.Step("Se monta el motor y se publica la encuesta")
	opts := bench.Options{Features: []string{entitlements.FeatureSurvey}}
	if verbose {
		opts.Log = common.NewLog(true)
	}
	b := bench.New(opts)
	if _, err := b.PublishFlow(surveyFlow()); err != nil {
		n.Fail(err, "publicar el flujo")
		return err
	}
	rule := trigger.Rule{Kind: trigger.KindEventStart, Keyword: keyword, MatchType: trigger.MatchExact,
		EventKind: trigger.EventKindSurvey, FlowID: flowID}
	if useKeyword {
		rule = trigger.Rule{Kind: trigger.KindKeyword, Keyword: keyword, MatchType: trigger.MatchExact, FlowID: flowID}
	}
	if _, err := b.AddRule(rule); err != nil {
		n.Fail(err, "crear el disparo")
		return err
	}
	n.OK("flujo %s: q1 «atencion» (1-3) → q2 «volverias» (1-2) → gracias. Disparo %s %q.", flowID, rule.Kind, keyword)
	n.Note("El PersistSink lleva el survey.Projector, como en el arranque: cada efecto survey_answer se PROYECTA a survey_results.")
	n.Note("El tenant tiene la feature survey; no tiene llm_intake (sin bienvenida ni hilo, para que solo se vea la encuesta).")

	n.Step("La palabra abre el evento y hace la primera pregunta")
	b.Turn(n, bench.Phone, "E07-1", keyword)
	b.ShowState(n, bench.Phone)
	if useKeyword {
		n.Note("Con keyword la encuesta NO arranca: tiene contenido durable y el runtime exige evento (lo viste en E06). Fin.")
		return nil
	}

	n.Step("Una respuesta fuera de la lista: la repregunta")
	b.Turn(n, bench.Phone, "E07-2", "5")
	b.ShowState(n, bench.Phone)
	n.Note("«5» no es una clave de q1. El texto es el mismo aviso que el menú: la encuesta usa modules.NumberedStep, igual que menu.")
	n.Note("El contador vive en vars.survey_reprompt y va SELLADO con el event_id (survey_reprompt_event_id): un fallo de otro evento no cuenta.")
	showResults(n, b)

	n.Step("Una respuesta válida: el efecto y su proyección")
	b.Turn(n, bench.Phone, "E07-3", "2")
	b.ShowState(n, bench.Phone)
	showResults(n, b)
	n.Note("La respuesta se guarda dos veces, a propósito: en vars.answers (el estado del recorrido) y, por el efecto")
	n.Note("persist:survey_answer, en flow_events y en survey_results (lo durable, lo que lee el resumen del evento).")

	n.Step("Otra inválida en la segunda pregunta, y la buena")
	b.Turn(n, bench.Phone, "E07-4", "quizás")
	b.Turn(n, bench.Phone, "E07-5", "1")
	b.ShowState(n, bench.Phone)
	showResults(n, b)
	showEvents(n, b)
	n.Note("El orden de flow_events del ÚLTIMO turno: event_closed se escribe ANTES que el survey_answer de la segunda pregunta.")
	n.Note("No es un accidente de este escalón: TestP3_CartConversation fija el mismo orden para el carrito (event_closed antes que")
	n.Note("persist:cart_closed). El cierre natural del evento sale antes que el efecto del módulo en el turno que llega al fin.")

	n.Step("Otro cliente falla tres veces seguidas: el menú de salida")
	b.Turn(n, otherPhone, "E07-6", keyword)
	for i, text := range []string{"no sé", "tal vez", "ninguna"} {
		b.Turn(n, otherPhone, "E07-"+strconv.Itoa(7+i), text)
	}
	b.ShowState(n, otherPhone)
	n.Note("Dentro de un evento, el tercer fallo no repite la pregunta: arma el MENÚ DE SALIDA (modules.ArmExitMenu) con la")
	n.Note("pantalla pendiente guardada en vars.event_exit_menu. Fuera de un evento (el menú de E05) solo cambiaba el texto.")
	b.Turn(n, otherPhone, "E07-10", "2")
	b.ShowState(n, otherPhone)
	n.Note("«2» (dejarlo por ahora) NO termina el evento: lo DESACTIVA (event_deactivated). Sigue open y rescatable; la conversación")
	n.Note("suelta el puntero ACTIVO y conserva el DUEÑO, el nodo y las vars (por eso «punteros distintos»). Ninguna fila nueva en survey_results.")
	n.Note("🔴 Mira el texto: «dejamos el encuesta». stopNotice (runtime/events_switch.go) fija el artículo «el» y events.KindName da")
	n.Note("«encuesta», femenino. Es un texto observable que llega al cliente, y su comentario lo declara «byte a byte».")
	showResults(n, b)
	showEvents(n, b)
	return nil
}

// showResults narra survey_results: lo que el survey.Projector escribió.
func showResults(n *common.Narrator, b *bench.Bench) {
	rows := b.Repo.SurveyResults()
	if len(rows) == 0 {
		n.Show("survey_results", "(ninguna fila)")
	}
	for _, r := range rows {
		ev := r.EventID
		if len(ev) > 8 {
			ev = ev[:8] + "…"
		}
		n.Show("survey_results", r.FlowID+" v"+strconv.Itoa(r.FlowVersion)+" · "+r.QuestionID+" = "+r.AnswerCode+
			" · contacto "+r.ContactID[:8]+"… · evento "+ev)
	}
}

// showEvents narra flow_events y los conversation_events.
func showEvents(n *common.Narrator, b *bench.Bench) {
	n.Show("flow_events", strings.Join(b.FlowEventNames(), ", "))
	for _, ev := range b.Events.Events(common.LabTenant) {
		n.Show("conversation_events", ev.Kind+" · "+string(ev.Status)+" · contacto "+ev.ContactID[:8]+"…")
	}
}
